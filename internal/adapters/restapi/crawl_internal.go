package restapi

import (
	"context"
	"encoding/json"
	"errors"
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

	jobID, err := h.TriggerCrawl(opts)
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
// tracking, concurrency limiting (h.crawlSem) and progress reporting.
func (h *Handler) TriggerCrawl(opts ports.CrawlOptions) (string, error) {
	if len(opts.SeedURLs) == 0 {
		return "", errors.New("seed_urls must not be empty")
	}

	job := h.crawlJobs.Create(domain.CrawlJobRequest{
		SeedURLs:            opts.SeedURLs,
		MaxPages:            opts.MaxPages,
		HasCookie:           opts.Cookie != "",
		HasBasicAuth:        opts.BasicAuthUser != "" || opts.BasicAuthPass != "",
		RespectRobots:       opts.RespectRobots,
		UserAgent:           opts.UserAgent,
		AllowOffDomainLinks: opts.AllowOffDomainLinks,
		UseSitemap:          opts.UseSitemap,
	})
	go h.runCrawlJob(job.ID, opts)

	return job.ID, nil
}

// runCrawlJob executes opts in the background against job.ID's tracked
// state. It uses context.Background(), not the triggering request's
// context, since the crawl must keep running after that request returns.
func (h *Handler) runCrawlJob(jobID string, opts ports.CrawlOptions) {
	h.crawlSem <- struct{}{}
	defer func() { <-h.crawlSem }()

	h.crawlJobs.MarkRunning(jobID)
	_, err := h.crawler.Crawl(context.Background(), opts, func(ev domain.CrawlPageEvent) {
		h.crawlJobs.AppendPage(jobID, ev)
	})
	if err != nil {
		h.crawlJobs.MarkFailed(jobID, err)
		return
	}
	h.crawlJobs.MarkDone(jobID)
	// A crawl just changed the link graph -- give the caller (cmd/crawl, to
	// trigger a PageRank recompute) a chance to react. See Config's
	// OnCrawlComplete doc comment for why this is deliberately synchronous.
	if h.onCrawlComplete != nil {
		h.onCrawlComplete()
	}
}

func (h *Handler) handleListCrawlJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.crawlJobs.List())
}

func (h *Handler) handleGetCrawlJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.crawlJobs.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "crawl job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
