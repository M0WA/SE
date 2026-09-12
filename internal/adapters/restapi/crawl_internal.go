package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxConcurrentCrawls bounds how many crawl jobs actually fetch pages at
// once on this process -- a simple safety valve, not a tuning knob, so a
// burst of triggered jobs queues behind it rather than opening unbounded
// concurrent connections and DB writes.
const maxConcurrentCrawls = 3

// RoutesCrawlInternal serves the endpoints the crawl-server binary
// exposes: POST /crawl starts a job and returns immediately, GET /jobs and
// GET /jobs/{id} report progress. None of this is ever reachable from
// nginx or the internet -- admin-server is the only caller, over the
// network via internal/adapters/crawlclient.
func (h *Handler) RoutesCrawlInternal() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /crawl", h.handleCrawl)
	mux.HandleFunc("GET /jobs", h.handleListCrawlJobs)
	mux.HandleFunc("GET /jobs/{id}", h.handleGetCrawlJob)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

type startJobResponse struct {
	JobID string `json:"job_id"`
}

// handleCrawl registers the job and returns its ID immediately; the crawl
// itself runs in the background, tracked in h.crawlJobs.
func (h *Handler) handleCrawl(w http.ResponseWriter, r *http.Request) {
	var opts ports.CrawlOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	jobID, err := h.TriggerCrawl(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusAccepted, startJobResponse{JobID: jobID})
}

// TriggerCrawl registers a new crawl job and starts it in the background,
// returning its ID immediately. This is the one job-creation path every
// caller on crawl-server goes through -- handleCrawl for a manually
// triggered crawl, and the scheduler ticker in cmd/crawl/main.go for a
// scheduled crawl that just came due -- so both get identical job
// tracking, concurrency limiting (h.crawlSem) and progress reporting. ctx
// only scopes the Create call itself (persisting the new job record); the
// crawl that follows always runs against context.Background(), since it
// must keep running after the triggering request (or scheduler tick)
// returns.
func (h *Handler) TriggerCrawl(ctx context.Context, opts ports.CrawlOptions) (string, error) {
	if len(opts.SeedURLs) == 0 {
		return "", errors.New("seed_urls must not be empty")
	}

	job, err := h.crawlJobs.Create(ctx, domain.CrawlJobRequest{
		SeedURLs:            opts.SeedURLs,
		MaxPages:            opts.MaxPages,
		HasCookie:           opts.Cookie != "",
		HasBasicAuth:        opts.BasicAuthUser != "" || opts.BasicAuthPass != "",
		RespectRobots:       opts.RespectRobots,
		UserAgent:           opts.UserAgent,
		AllowOffDomainLinks: opts.AllowOffDomainLinks,
		UseSitemap:          opts.UseSitemap,
		FetchTimeoutSeconds: opts.FetchTimeoutSeconds,
		MinTextLength:       opts.MinTextLength,
		CrawlDelayMs:        opts.CrawlDelayMs,
		MaxResponseKB:       opts.MaxResponseKB,
		PrioritizeUnindexed: opts.PrioritizeUnindexed,
	})
	if err != nil {
		return "", err
	}
	go h.runCrawlJob(job.ID, opts)

	return job.ID, nil
}

// ResumeCrawlJob re-runs an existing job (jobID, already in the store) in
// the background from opts' seed URLs, without creating a new job record.
// Used only by application.RecoverInterruptedCrawls at crawl-server
// startup, so a crawl interrupted by a restart is seen to continue --
// pages_crawled and page history keep accumulating under the same ID --
// rather than looking like it failed and was silently replaced by an
// unrelated new job starting over from zero. runCrawlJob's own
// MarkRunning call (its first step) takes the job out of whatever
// stale queued/running state it was left in.
func (h *Handler) ResumeCrawlJob(jobID string, opts ports.CrawlOptions) {
	go h.runCrawlJob(jobID, opts)
}

// runCrawlJob executes opts in the background against job.ID's tracked
// state. It uses context.Background(), not the triggering request's
// context, since the crawl must keep running after that request returns.
// A store error along the way (the persistent store is unreachable, say)
// is logged rather than aborting the crawl itself -- losing this job's
// history is far less harmful than silently losing already-crawled pages.
func (h *Handler) runCrawlJob(jobID string, opts ports.CrawlOptions) {
	h.crawlSem <- struct{}{}
	defer func() { <-h.crawlSem }()

	ctx := context.Background()
	if err := h.crawlJobs.MarkRunning(ctx, jobID); err != nil {
		log.Printf("crawl job %s: marking running: %v", jobID, err)
	}
	_, err := h.crawler.Crawl(ctx, opts, func(ev domain.CrawlPageEvent) {
		if err := h.crawlJobs.AppendPage(ctx, jobID, ev); err != nil {
			log.Printf("crawl job %s: appending page event: %v", jobID, err)
		}
	})
	if err != nil {
		if markErr := h.crawlJobs.MarkFailed(ctx, jobID, err); markErr != nil {
			log.Printf("crawl job %s: marking failed: %v", jobID, markErr)
		}
		return
	}
	if err := h.crawlJobs.MarkDone(ctx, jobID); err != nil {
		log.Printf("crawl job %s: marking done: %v", jobID, err)
	}
	// A crawl just changed the link graph -- give the caller (cmd/crawl, to
	// trigger a PageRank recompute) a chance to react. See Config's
	// OnCrawlComplete doc comment for why this is deliberately synchronous.
	if h.onCrawlComplete != nil {
		h.onCrawlComplete()
	}
}

func (h *Handler) handleListCrawlJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.crawlJobs.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handler) handleGetCrawlJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.crawlJobs.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, domain.ErrCrawlJobNotFound) {
		http.Error(w, "crawl job not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
