package restapi

import (
	"context"
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
// exposes: GET /jobs and GET /jobs/{id} report progress on jobs the
// scheduler ticker (cmd/crawl/main.go) has started. None of this is ever
// reachable from nginx or the internet -- admin-server is the only caller,
// over the network via internal/adapters/crawlclient.
func (h *Handler) RoutesCrawlInternal() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jobs", h.handleListCrawlJobs)
	mux.HandleFunc("GET /jobs/{id}", h.handleGetCrawlJob)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

// TriggerScheduledCrawl registers a new crawl job and starts it in the
// background, returning its ID immediately, and calls onDone exactly once
// when the job actually finishes (success or failure). This is the one
// job-creation path crawl-server has -- called only by the scheduler
// ticker in cmd/crawl/main.go for a crawl (recurring or one-off; see
// domain.ScheduledCrawl.Recurring) that just came due. onDone lets
// application.TriggerDueCrawls know the crawl has truly finished before it
// advances that entry's next_run_at, rather than assuming completion the
// moment the job starts -- a crawl that runs longer than its own interval
// would otherwise have its next run triggered while it's still going. ctx
// only scopes the Create call itself (persisting the new job record); the
// crawl that follows always runs against context.Background(), since it
// must keep running after the triggering scheduler tick returns.
func (h *Handler) TriggerScheduledCrawl(ctx context.Context, opts ports.CrawlOptions, onDone func()) (string, error) {
	job, err := h.createCrawlJob(ctx, opts)
	if err != nil {
		return "", err
	}
	go func() {
		h.runCrawlJob(job.ID, opts)
		if onDone != nil {
			onDone()
		}
	}()

	return job.ID, nil
}

// createCrawlJob persists a new job record for opts -- TriggerScheduledCrawl's
// shared first step with ResumeCrawlJob's "existing job, no new record"
// counterpart below.
func (h *Handler) createCrawlJob(ctx context.Context, opts ports.CrawlOptions) (domain.CrawlJob, error) {
	if len(opts.SeedURLs) == 0 {
		return domain.CrawlJob{}, errors.New("seed_urls must not be empty")
	}

	return h.crawlJobs.Create(ctx, domain.CrawlJobRequest{
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
