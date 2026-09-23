package restapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// crawlConcurrencyPollInterval bounds how long a queued job waits before
// re-checking whether MaxConcurrentCrawls was raised, instead of staying
// stuck on the old, still-full permit channel.
const crawlConcurrencyPollInterval = 1 * time.Second

// crawlConcurrencySemaphore bounds how many crawl jobs fetch pages at
// once, reading MaxConcurrentCrawls fresh on every acquire instead of a
// fixed capacity, so a live setting change applies without a restart.
type crawlConcurrencySemaphore struct {
	opSettings *domain.OperationalSettings

	mu      sync.Mutex
	ch      chan struct{}
	current int
}

// defaultMaxConcurrentCrawls is used only if opSettings is nil (shouldn't
// happen in a real process) -- matches domain.OperationalSettingsValues' default.
const defaultMaxConcurrentCrawls = 3

func newCrawlConcurrencySemaphore(opSettings *domain.OperationalSettings) *crawlConcurrencySemaphore {
	s := &crawlConcurrencySemaphore{opSettings: opSettings}
	s.resize(s.configuredLimit())
	return s
}

func (s *crawlConcurrencySemaphore) configuredLimit() int {
	if s.opSettings == nil {
		return defaultMaxConcurrentCrawls
	}
	if n := s.opSettings.Get().MaxConcurrentCrawls; n > 0 {
		return n
	}
	return defaultMaxConcurrentCrawls
}

// channel returns the current permit channel for one crawl job, resizing
// it first if the configured limit changed since the last call.
func (s *crawlConcurrencySemaphore) channel() chan struct{} {
	s.resize(s.configuredLimit())
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ch
}

func (s *crawlConcurrencySemaphore) resize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n == s.current {
		return
	}
	s.ch = make(chan struct{}, n)
	s.current = n
}

// acquire blocks until a permit is available or ctx is cancelled,
// re-fetching the (possibly resized) channel every
// crawlConcurrencyPollInterval so a limit raised mid-wait applies. The
// caller must release into this same returned channel.
func (s *crawlConcurrencySemaphore) acquire(ctx context.Context) (chan struct{}, error) {
	for {
		ch := s.channel()
		select {
		case ch <- struct{}{}:
			return ch, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(crawlConcurrencyPollInterval):
		}
	}
}

// RoutesCrawlInternal serves crawl-server's endpoints. Meant only for
// admin-server over crawlclient -- enforced by network topology
// (loopback-only, no nginx proxy), so every route but /healthz also goes
// through requireCrawlInternalToken as a second, independent layer.
func (h *Handler) RoutesCrawlInternal() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jobs", h.requireCrawlInternalToken(h.handleListCrawlJobs))
	mux.HandleFunc("GET /jobs/{id}", h.requireCrawlInternalToken(h.handleGetCrawlJob))
	mux.HandleFunc("POST /jobs/{id}/cancel", h.requireCrawlInternalToken(h.handleCancelCrawlJob))
	mux.HandleFunc("DELETE /jobs", h.requireCrawlInternalToken(h.handleDeleteEndedCrawlJobs))
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

// requireCrawlInternalToken gates a handler behind a shared secret,
// constant-time compared against X-Internal-Token -- only enforced when
// configured, and never falls back to a loopback check since the two
// servers aren't necessarily on the same host.
func (h *Handler) requireCrawlInternalToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.crawlInternalToken == "" {
			next(w, r)
			return
		}
		if !requestHasSecretHeader(r, "X-Internal-Token", h.crawlInternalToken) {
			http.Error(w, "invalid or missing internal token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// TriggerScheduledCrawl registers a new job, starts it in the background,
// and calls onDone once it finishes, so TriggerDueCrawls only advances
// next_run_at when truly done. ctx scopes only the Create call; the crawl
// itself runs against context.Background().
//
// Refuses with domain.ErrCrawlAlreadyActiveForSeed if a Queued/Running job
// already covers one of opts.SeedURLs -- defense-in-depth independent of
// scheduled_crawls' in_progress bookkeeping, which a scheduler race
// already got out of sync with in production once (against cnn.com; see
// sqlrepo.Repository.ResetStaleInProgress). The caller logs and skips on
// this failure, retrying next tick.
func (h *Handler) TriggerScheduledCrawl(ctx context.Context, opts ports.CrawlOptions, onDone func()) (string, error) {
	active, err := h.hasActiveJobForSeeds(ctx, opts.SeedURLs)
	if err != nil {
		return "", err
	}
	if active {
		return "", domain.ErrCrawlAlreadyActiveForSeed
	}

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

// hasActiveJobForSeeds reports whether any currently Queued/Running crawl
// job shares at least one seed URL with seedURLs.
func (h *Handler) hasActiveJobForSeeds(ctx context.Context, seedURLs []string) (bool, error) {
	active, err := h.crawlJobs.ListActive(ctx)
	if err != nil {
		return false, fmt.Errorf("checking for an already-active crawl: %w", err)
	}
	seeds := make(map[string]bool, len(seedURLs))
	for _, u := range seedURLs {
		seeds[u] = true
	}
	for _, j := range active {
		for _, u := range j.Request.SeedURLs {
			if seeds[u] {
				return true, nil
			}
		}
	}
	return false, nil
}

// createCrawlJob persists a new job record for opts -- shared first step
// with ResumeCrawlJob's "existing job, no new record" counterpart below.
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

// ResumeCrawlJob re-runs an existing job without creating a new record --
// used by RecoverInterruptedCrawls at startup, so a crash-interrupted
// crawl keeps its ID instead of looking replaced.
func (h *Handler) ResumeCrawlJob(jobID string, opts ports.CrawlOptions) {
	go h.runCrawlJob(jobID, opts)
}

// runCrawlJob executes opts in the background. Store writes use a fresh
// context.Background() since final state must be recorded even after
// cancellation. The cancel func is registered for the job's whole
// lifetime, including while queued, so CancelCrawlJob works pre-fetch too.
func (h *Handler) runCrawlJob(jobID string, opts ports.CrawlOptions) {
	crawlCtx, cancel := context.WithCancel(context.Background())
	h.registerCancel(jobID, cancel)
	defer h.unregisterCancel(jobID)
	defer cancel()

	storeCtx := context.Background()

	sem, err := h.crawlSem.acquire(crawlCtx)
	if err != nil {
		if err := h.crawlJobs.MarkCancelled(storeCtx, jobID); err != nil {
			log.Printf("crawl job %s: marking cancelled: %v", jobID, err)
		}
		return
	}
	defer func() { <-sem }()

	if err := h.crawlJobs.MarkRunning(storeCtx, jobID); err != nil {
		log.Printf("crawl job %s: marking running: %v", jobID, err)
	}
	_, err = h.crawler.Crawl(crawlCtx, opts, func(ev domain.CrawlPageEvent) {
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
	// A crawl just changed the link graph -- give the caller (cmd/crawl,
	// PageRank recompute) a chance to react. See Config.OnCrawlComplete
	// for why this is deliberately synchronous.
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

// CancelCrawlJob stops a queued or running job, reporting whether it
// found one -- false means jobID isn't registered (finished, never
// existed, or lost by a restart, fine since ResumeCrawlJob re-registers it).
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

// handleCancelCrawlJob is crawl-server's cancel endpoint -- called only by
// admin-server's crawlclient.Client, never reachable from the internet.
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

// handleDeleteEndedCrawlJobs is crawl-server's "clear ended jobs" endpoint
// (DELETE /jobs itself, not "/jobs/clear-ended", which would collide with
// the "/jobs/{id}" wildcard above).
func (h *Handler) handleDeleteEndedCrawlJobs(w http.ResponseWriter, r *http.Request) {
	n, err := h.crawlJobs.DeleteEndedCrawlJobs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": n})
}

func (h *Handler) handleGetCrawlJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.crawlJobs.Get(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, domain.ErrCrawlJobNotFound, "crawl job not found", job)
}
