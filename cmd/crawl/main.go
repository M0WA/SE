package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"searchengine/internal/adapters/browserfetcher"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// schedulerPollInterval is how often crawl-server checks for crawls that
// have come due -- short enough that a one-off crawl starts within a few
// seconds, cheap enough to be negligible load for a single-admin instance.
const schedulerPollInterval = 3 * time.Second

// pageRankPollInterval is how often runPageRankScheduler checks whether the
// admin-configured recompute interval has elapsed -- independent of (and
// much shorter than) that interval itself.
const pageRankPollInterval = 60 * time.Second

// contentDedupPollInterval is how often runContentDedupScheduler checks
// whether the admin-configured recompute interval has elapsed -- mirrors
// pageRankPollInterval.
const contentDedupPollInterval = 60 * time.Second

// crawlJobPrunePollInterval is how often runCrawlJobPruner deletes crawl
// jobs beyond the admin-configured retention limit -- infrequent, since
// unlike the old in-memory store's per-Create trim, persistent storage
// doesn't need pruning to happen the instant the limit is crossed.
const crawlJobPrunePollInterval = 5 * time.Minute

// runPageRankScheduler recomputes PageRank once immediately, then again
// whenever PageRankRecomputeIntervalMinutes has elapsed (checked on a
// shorter poll tick so an admin edit takes effect promptly). A post-crawl
// trigger also resets this timer. Runs the first recompute in the
// background so a large corpus's pass doesn't delay ListenAndServe.
func runPageRankScheduler(ctx context.Context, repo ports.PageRankRepository, settingsStore ports.SettingsStore, opSettings *domain.OperationalSettings) *pageRankRecomputer {
	pr := &pageRankRecomputer{ctx: ctx, repo: repo, settingsStore: settingsStore}
	go pr.recompute()

	go func() {
		ticker := time.NewTicker(pageRankPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				interval := time.Duration(opSettings.Get().PageRankRecomputeIntervalMinutes) * time.Minute
				if time.Since(pr.lastRun()) >= interval {
					pr.recompute()
				}
			}
		}
	}()
	return pr
}

// pageRankRecomputer tracks when application.RunPageRankJob last ran, so
// both runPageRankScheduler's own ticker and a post-crawl trigger can share
// one "was it just recomputed" clock rather than racing two independent
// timers.
type pageRankRecomputer struct {
	ctx           context.Context
	repo          ports.PageRankRepository
	settingsStore ports.SettingsStore
	mu            sync.Mutex
	last          time.Time
	// running guards against two recompute()s overlapping (startup call vs.
	// ticker vs. a post-crawl trigger) -- a second call while one is in
	// flight just returns immediately rather than contending.
	running bool
}

func (p *pageRankRecomputer) recompute() {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	if _, err := application.RunPageRankJobWithStatus(p.ctx, p.repo, p.settingsStore); err != nil {
		log.Printf("recomputing pagerank: %v", err)
	}

	p.mu.Lock()
	p.running = false
	p.last = time.Now()
	p.mu.Unlock()
}

func (p *pageRankRecomputer) lastRun() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// runContentDedupScheduler mirrors runPageRankScheduler, gated by
// ContentDedupIntervalMinutes. Unlike PageRank this pass is opt-in and
// destructive -- gated inside contentDedupRecomputer.recompute so both this
// scheduler and an external trigger are safe to call unconditionally.
func runContentDedupScheduler(ctx context.Context, repo ports.ContentDedupRepository, settingsStore ports.SettingsStore, opSettings *domain.OperationalSettings) *contentDedupRecomputer {
	cd := &contentDedupRecomputer{ctx: ctx, repo: repo, settingsStore: settingsStore, opSettings: opSettings}
	go cd.recompute()

	go func() {
		ticker := time.NewTicker(contentDedupPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				interval := time.Duration(opSettings.Get().ContentDedupIntervalMinutes) * time.Minute
				if time.Since(cd.lastRun()) >= interval {
					cd.recompute()
				}
			}
		}
	}()
	return cd
}

// contentDedupRecomputer tracks when application.RunContentDedupJob last
// ran -- see pageRankRecomputer's identical doc comment for why this
// shared-clock/running-guard shape exists.
type contentDedupRecomputer struct {
	ctx           context.Context
	repo          ports.ContentDedupRepository
	settingsStore ports.SettingsStore
	opSettings    *domain.OperationalSettings
	mu            sync.Mutex
	last          time.Time
	running       bool
}

func (c *contentDedupRecomputer) recompute() {
	v := c.opSettings.Get()
	if !v.ContentDedupEnabled {
		return
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()

	_, err := application.RunContentDedupJobWithStatus(c.ctx, c.repo, c.settingsStore, v.ContentDedupMethod, v.ContentDedupSimHashMaxDistance)
	if err != nil && !errors.Is(err, ports.ErrContentDedupAlreadyRunning) {
		log.Printf("recomputing content dedup: %v", err)
	}

	c.mu.Lock()
	c.running = false
	c.last = time.Now()
	c.mu.Unlock()
}

func (c *contentDedupRecomputer) lastRun() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// runScheduler triggers every due scheduled crawl once immediately, then
// again on every tick, via handler.TriggerScheduledCrawl -- with a
// completion callback so next_run_at reflects when a crawl actually
// finished, not just when it started.
func runScheduler(ctx context.Context, store ports.ScheduledCrawlStore, handler *restapi.Handler) {
	triggerDue := func() {
		if _, err := application.TriggerDueCrawls(ctx, store, handler.TriggerScheduledCrawl, time.Now()); err != nil {
			log.Printf("checking scheduled crawls: %v", err)
		}
	}
	triggerDue()

	ticker := time.NewTicker(schedulerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			triggerDue()
		}
	}
}

// crawlJobPruner is satisfied by *sqlrepo.Repository's PruneCrawlJobs --
// called directly on the concrete repo (like EnableANN), not through a
// ports interface, since pruning is a maintenance concern internal to
// crawl-server rather than part of the CrawlJobStore contract handlers use.
type crawlJobPruner interface {
	PruneCrawlJobs(ctx context.Context, maxRetained int) error
}

// runCrawlJobPruner deletes crawl jobs beyond opSettings' current
// MaxRetainedCrawlJobs on every tick, once immediately and then on the
// fixed poll interval for as long as ctx stays alive -- an admin raising
// or lowering the limit takes effect within one poll tick either way.
func runCrawlJobPruner(ctx context.Context, pruner crawlJobPruner, opSettings *domain.OperationalSettings) {
	prune := func() {
		if err := pruner.PruneCrawlJobs(ctx, opSettings.Get().MaxRetainedCrawlJobs); err != nil {
			log.Printf("pruning crawl jobs: %v", err)
		}
	}
	prune()

	ticker := time.NewTicker(crawlJobPrunePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func main() {
	ctx := context.Background()
	repo, driver, err := bootstrap.OpenDB(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer repo.Close()

	opSettings := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(ctx, repo, nil, opSettings, nil, repo, repo)

	settingsEncryptionKey, err := settingscrypto.ParseKey(bootstrap.GetEnv("SETTINGS_ENCRYPTION_KEY", ""))
	if err != nil {
		log.Fatal(err)
	}
	endpoints := bootstrap.LoadEmbeddingEndpoints(ctx, repo, settingsEncryptionKey)
	embedders := bootstrap.NewEmbedders(opSettings.Get().EmbeddingHashEnabled, endpoints)
	// Enables Postgres pgvector ANN search when available, never fatal
	// otherwise -- see cmd/search's identical comment. Must run after
	// embedders are constructed.
	repo.EnableANN(ctx, bootstrap.EmbedderDimensions(embedders))
	// fetcher does plain HTTP; RenderAwareFetcher adds an opt-in real-browser
	// path on top, chosen per-crawl or by the Tuning page's default -- with
	// rendering off (the default), byte-for-byte the same as before the
	// feature existed. Neither browser engine starts until a crawl asks for it.
	fetcher := httpfetcher.New(opSettings)
	renderingFetcher := &application.RenderAwareFetcher{
		Base:       fetcher,
		OpSettings: opSettings,
		Renderers: map[string]ports.Renderer{
			domain.RendererChromium: browserfetcher.New(domain.RendererChromium),
			domain.RendererFirefox:  browserfetcher.New(domain.RendererFirefox),
		},
	}
	robotsChecker := robots.New(renderingFetcher)
	parseHTML := func(html, pageURL string) (string, string, []string, string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(renderingFetcher, robotsChecker, repo, embedders, parseHTML, opSettings)

	pageRank := runPageRankScheduler(ctx, repo, repo, opSettings)
	contentDedup := runContentDedupScheduler(ctx, repo, repo, opSettings)

	handler := restapi.New(restapi.Config{
		Crawler:    crawlerSvc,
		CrawlJobs:  repo,
		Health:     repo,
		OpSettings: opSettings,
		// A crawl just changed the corpus -- recompute right away in the
		// background (so it never delays the job's reported completion),
		// on top of each recomputer's own ticker. contentDedup.recompute is
		// a no-op when ContentDedupEnabled is off.
		OnCrawlComplete: func() { go pageRank.recompute(); go contentDedup.recompute() },
		// Opt-in shared secret admin-server's crawlclient.Client sends back
		// -- empty by default, so an unconfigured deployment is unaffected.
		CrawlInternalToken: bootstrap.GetEnv("CRAWL_INTERNAL_TOKEN", ""),
	})

	// Any job still queued/running from before this process last stopped
	// has no goroutine actually working on it anymore -- recover it (or,
	// for one that needed credentials that were never persisted, mark it
	// failed) before this process starts accepting new crawl requests.
	if recovered, abandoned, err := application.RecoverInterruptedCrawls(ctx, repo, handler.ResumeCrawlJob); err != nil {
		log.Printf("recovering interrupted crawl jobs: %v", err)
	} else if recovered > 0 || abandoned > 0 {
		log.Printf("recovered %d interrupted crawl job(s), %d could not be resumed (needed credentials) and were marked failed", recovered, abandoned)
	}

	// in_progress is only ever cleared by its triggering run's completion
	// callback, an in-memory closure that dies with this process -- a
	// restart mid-run otherwise leaves it stuck true forever, silently
	// blocking that schedule's recurring cadence and "Run now." Nothing can
	// genuinely still be in progress the instant this process starts.
	if reset, err := repo.ResetStaleInProgress(ctx); err != nil {
		log.Printf("resetting stale scheduled-crawl in-progress flags: %v", err)
	} else if reset > 0 {
		log.Printf("reset %d scheduled crawl(s) stuck in-progress from a previous restart", reset)
	}

	go runScheduler(ctx, repo, handler)
	go runCrawlJobPruner(ctx, repo, opSettings)

	addr := bootstrap.GetEnv("CRAWL_LISTEN_ADDR", "127.0.0.1:8082")
	log.Printf("Crawl server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesCrawlInternal()))
}
