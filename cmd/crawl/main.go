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

// schedulerPollInterval is how often crawl-server checks for due crawls --
// short enough to start within seconds, cheap enough to be negligible.
const schedulerPollInterval = 3 * time.Second

// pageRankPollInterval is how often runPageRankScheduler checks whether the
// admin-configured recompute interval has elapsed.
const pageRankPollInterval = 60 * time.Second

// contentDedupPollInterval mirrors pageRankPollInterval, for content dedup.
const contentDedupPollInterval = 60 * time.Second

// crawlJobPrunePollInterval is how often runCrawlJobPruner deletes crawl
// jobs beyond the retention limit -- infrequent, since persistent storage
// doesn't need pruning the instant the limit is crossed.
const crawlJobPrunePollInterval = 5 * time.Minute

// runPageRankScheduler recomputes PageRank once immediately, then again
// whenever PageRankRecomputeIntervalMinutes has elapsed (a post-crawl
// trigger also resets this timer). Runs the first recompute in the
// background so a large corpus doesn't delay ListenAndServe.
func runPageRankScheduler(ctx context.Context, repo ports.PageRankRepository, settingsStore ports.SettingsStore, opSettings *domain.OperationalSettings) *pageRankRecomputer {
	pr := &pageRankRecomputer{repo: repo, settingsStore: settingsStore}
	go pr.recompute(ctx)

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
					pr.recompute(ctx)
				}
			}
		}
	}()
	return pr
}

// pageRankRecomputer tracks when RunPageRankJob last ran, so the ticker
// and a post-crawl trigger share one clock rather than racing two.
type pageRankRecomputer struct {
	repo          ports.PageRankRepository
	settingsStore ports.SettingsStore
	mu            sync.Mutex
	last          time.Time
	// running guards against two recompute()s overlapping -- a second call
	// while one is in flight sets pending instead of doing its own run.
	running bool
	// pending records a recompute() call that arrived while running was
	// already true, so a crawl's new links aren't dropped until the next
	// ticker interval (which can be up to PageRankRecomputeIntervalMinutes
	// away) just because they landed during a busy window -- the in-flight
	// call runs one extra pass for it before releasing running.
	pending bool
}

func (p *pageRankRecomputer) recompute(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.pending = true
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	if _, err := application.RunPageRankJobWithStatus(ctx, p.repo, p.settingsStore); err != nil {
		log.Printf("recomputing pagerank: %v", err)
	}

	// One extra pass, not a loop -- a call landing during THIS pass just
	// sets pending again for some future recompute() to pick up, so this
	// can't run forever.
	p.mu.Lock()
	runAgain := p.pending
	p.pending = false
	p.mu.Unlock()

	if runAgain {
		if _, err := application.RunPageRankJobWithStatus(ctx, p.repo, p.settingsStore); err != nil {
			log.Printf("recomputing pagerank: %v", err)
		}
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
// destructive, gated inside recompute so callers are safe unconditionally.
func runContentDedupScheduler(ctx context.Context, repo ports.ContentDedupRepository, settingsStore ports.SettingsStore, opSettings *domain.OperationalSettings) *contentDedupRecomputer {
	cd := &contentDedupRecomputer{repo: repo, settingsStore: settingsStore, opSettings: opSettings}
	go cd.recompute(ctx)

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
					cd.recompute(ctx)
				}
			}
		}
	}()
	return cd
}

// contentDedupRecomputer mirrors pageRankRecomputer's shared-clock/
// running-guard shape, for RunContentDedupJob.
type contentDedupRecomputer struct {
	repo          ports.ContentDedupRepository
	settingsStore ports.SettingsStore
	opSettings    *domain.OperationalSettings
	mu            sync.Mutex
	last          time.Time
	running       bool
}

func (c *contentDedupRecomputer) recompute(ctx context.Context) {
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

	_, err := application.RunContentDedupJobWithStatus(ctx, c.repo, c.settingsStore, v.ContentDedupMethod, v.ContentDedupSimHashMaxDistance)
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
// on every tick, with a completion callback so next_run_at reflects when
// a crawl actually finished, not when it started.
func runScheduler(ctx context.Context, store ports.ScheduledCrawlStore, handler *restapi.Handler) {
	bootstrap.PollRefresh(ctx, schedulerPollInterval, func() {
		if _, err := application.TriggerDueCrawls(ctx, store, handler.TriggerScheduledCrawl, time.Now()); err != nil {
			log.Printf("checking scheduled crawls: %v", err)
		}
	})
}

// crawlJobPruner is satisfied by *sqlrepo.Repository's PruneCrawlJobs --
// called directly on the concrete repo (like EnableANN), not through a
// ports interface, since pruning is internal maintenance, not part of
// the CrawlJobStore contract handlers use.
type crawlJobPruner interface {
	PruneCrawlJobs(ctx context.Context, maxRetained int) error
}

// runCrawlJobPruner deletes crawl jobs beyond opSettings' current
// MaxRetainedCrawlJobs, once immediately then on each poll tick -- an
// admin raising or lowering the limit takes effect within one tick.
func runCrawlJobPruner(ctx context.Context, pruner crawlJobPruner, opSettings *domain.OperationalSettings) {
	bootstrap.PollRefresh(ctx, crawlJobPrunePollInterval, func() {
		if err := pruner.PruneCrawlJobs(ctx, opSettings.Get().MaxRetainedCrawlJobs); err != nil {
			log.Printf("pruning crawl jobs: %v", err)
		}
	})
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
	// otherwise. Must run after embedders are constructed.
	repo.EnableANN(ctx, bootstrap.EmbedderDimensions(embedders))
	// fetcher does plain HTTP; RenderAwareFetcher adds an opt-in
	// real-browser path on top, chosen per-crawl or by the Tuning page's
	// default. Neither browser engine starts until a crawl asks for it.
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
		// background, on top of each recomputer's own ticker.
		// contentDedup.recompute is a no-op when ContentDedupEnabled is off.
		OnCrawlComplete: func() { go pageRank.recompute(ctx); go contentDedup.recompute(ctx) },
		// Opt-in shared secret admin-server's crawlclient.Client sends back
		// -- empty by default, so an unconfigured deployment is unaffected.
		CrawlInternalToken: bootstrap.GetEnv("CRAWL_INTERNAL_TOKEN", ""),
	})

	// A job still queued/running from before this process stopped has no
	// goroutine working on it -- recover it (or mark it failed if it
	// needed never-persisted credentials) before accepting new requests.
	if recovered, abandoned, err := application.RecoverInterruptedCrawls(ctx, repo, handler.ResumeCrawlJob); err != nil {
		log.Printf("recovering interrupted crawl jobs: %v", err)
	} else if recovered > 0 || abandoned > 0 {
		log.Printf("recovered %d interrupted crawl job(s), %d could not be resumed (needed credentials) and were marked failed", recovered, abandoned)
	}

	// in_progress is only cleared by its run's completion callback, an
	// in-memory closure that dies with this process -- a restart mid-run
	// otherwise leaves it stuck true, blocking that schedule forever.
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
