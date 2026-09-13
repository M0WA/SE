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
	mux.HandleFunc("POST /jobs/{id}/cancel", h.handleCancelCrawlJob)
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
		SeedURLs:             opts.SeedURLs,
		MaxPages:             opts.MaxPages,
		HasCookie:            opts.Cookie != "",
		HasBasicAuth:         opts.BasicAuthUser != "" || opts.BasicAuthPass != "",
		RespectRobots:        opts.RespectRobots,
		UserAgent:            opts.UserAgent,
		LinkScope:            opts.LinkScope,
		AllowedDomains:       opts.AllowedDomains,
		BlockedDomains:       opts.BlockedDomains,
		FollowIndexedDomains: opts.FollowIndexedDomains,
		UseSitemap:           opts.UseSitemap,
		FetchTimeoutSeconds:  opts.FetchTimeoutSeconds,
		MinTextLength:        opts.MinTextLength,
		CrawlDelayMs:         opts.CrawlDelayMs,
		MaxResponseKB:        opts.MaxResponseKB,
		PrioritizeUnindexed:  opts.PrioritizeUnindexed,
		Renderer:             opts.Renderer,
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
// state. Store writes (MarkRunning/AppendPage/MarkDone/MarkFailed/
// MarkCancelled) always use a fresh context.Background(), not the crawl's
// own cancelable one, since the job's final state still needs to be
// recorded even after that context is cancelled -- and the crawl must keep
// running after the triggering request's own context returns regardless.
// A store error along the way (the persistent store is unreachable, say)
// is logged rather than aborting the crawl itself -- losing this job's
// history is far less harmful than silently losing already-crawled pages.
//
// A cancel func is registered under jobID for the job's entire lifetime,
// including while it's still queued behind maxConcurrentCrawls -- so
// CancelCrawlJob can stop a job before it even starts fetching, not just
// while it's actively running.
func (h *Handler) runCrawlJob(jobID string, opts ports.CrawlOptions) {
	crawlCtx, cancel := context.WithCancel(context.Background())
	h.registerCancel(jobID, cancel)
	defer h.unregisterCancel(jobID)
	defer cancel()

	storeCtx := context.Background()

	select {
	case h.crawlSem <- struct{}{}:
	case <-crawlCtx.Done():
		if err := h.crawlJobs.MarkCancelled(storeCtx, jobID); err != nil {
			log.Printf("crawl job %s: marking cancelled: %v", jobID, err)
		}
		return
	}
	defer func() { <-h.crawlSem }()

	if err := h.crawlJobs.MarkRunning(storeCtx, jobID); err != nil {
		log.Printf("crawl job %s: marking running: %v", jobID, err)
	}
	_, err := h.crawler.Crawl(crawlCtx, opts, func(ev domain.CrawlPageEvent) {
		if err := h.crawlJobs.AppendPage(storeCtx, jobID, ev); err != nil {
			log.Printf("crawl job %s: appending page event: %v", jobID, err)
		}
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if markErr := h.crawlJobs.MarkCancelled(storeCtx, jobID); markErr != nil {
				log.Printf("crawl job %s: marking cancelled: %v", jobID, markErr)
			}
			return
		}
		if markErr := h.crawlJobs.MarkFailed(storeCtx, jobID, err); markErr != nil {
			log.Printf("crawl job %s: marking failed: %v", jobID, markErr)
		}
		return
	}
	if err := h.crawlJobs.MarkDone(storeCtx, jobID); err != nil {
		log.Printf("crawl job %s: marking done: %v", jobID, err)
	}
	// A crawl just changed the link graph -- give the caller (cmd/crawl, to
	// trigger a PageRank recompute) a chance to react. See Config's
	// OnCrawlComplete doc comment for why this is deliberately synchronous.
	if h.onCrawlComplete != nil {
		h.onCrawlComplete()
	}
}

func (h *Handler) registerCancel(jobID string, cancel context.CancelFunc) {
	h.cancelMu.Lock()
	defer h.cancelMu.Unlock()
	h.cancelFuncs[jobID] = cancel
}

func (h *Handler) unregisterCancel(jobID string) {
	h.cancelMu.Lock()
	defer h.cancelMu.Unlock()
	delete(h.cancelFuncs, jobID)
}

// CancelCrawlJob stops a queued or running job by cancelling its context,
// and reports whether it found one to cancel -- false means jobID isn't
// currently queued/running in this process (already finished, never
// existed, or -- after a crawl-server restart -- recovered under a fresh
// context that predates this call, which is fine: the old registration
// died with the old process, and a freshly recovered job is cancelable
// again as soon as ResumeCrawlJob re-registers it).
func (h *Handler) CancelCrawlJob(jobID string) bool {
	h.cancelMu.Lock()
	cancel, ok := h.cancelFuncs[jobID]
	h.cancelMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// handleCancelCrawlJob is crawl-server's own cancel endpoint -- called only
// by admin-server's crawlclient.Client, never directly reachable from the
// internet (see RoutesCrawlInternal's doc comment).
func (h *Handler) handleCancelCrawlJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.CancelCrawlJob(id) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if _, err := h.crawlJobs.Get(r.Context(), id); errors.Is(err, domain.ErrCrawlJobNotFound) {
		http.Error(w, "crawl job not found", http.StatusNotFound)
		return
	}
	http.Error(w, ports.ErrCrawlJobNotRunning.Error(), http.StatusConflict)
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
