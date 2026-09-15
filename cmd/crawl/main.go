package main

import (
	"context"
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
// have come due. Short enough that a one-off crawl (due immediately --
// see domain.ScheduledCrawl.Recurring) starts within a few seconds of the
// admin creating it, rather than waiting up to a full recurring-schedule
// interval; a single cheap query on this cadence is negligible load for a
// self-hosted, single-admin instance.
const schedulerPollInterval = 3 * time.Second

// pageRankPollInterval is how often runPageRankScheduler checks whether the
// admin-configured recompute interval has elapsed -- independent of (and
// much shorter than) that interval itself, the same way schedulerPollInterval
// is independent of any individual scheduled crawl's own interval.
const pageRankPollInterval = 60 * time.Second

// crawlJobPrunePollInterval is how often runCrawlJobPruner deletes crawl
// jobs beyond the admin-configured retention limit -- infrequent, since
// unlike the old in-memory store's per-Create trim, persistent storage
// doesn't need pruning to happen the instant the limit is crossed.
const crawlJobPrunePollInterval = 5 * time.Minute

// runPageRankScheduler recomputes every document's PageRank score once
// immediately (so a fresh process doesn't run with a stale link graph for a
// full interval), then again every time at least
// opSettings.PageRankRecomputeIntervalMinutes has elapsed since the last
// run -- checked on a fixed, shorter poll tick so an admin edit to that
// interval takes effect promptly rather than only at the next already-
// scheduled run. A recompute triggered by a just-completed crawl (see
// restapi.Config.OnCrawlComplete below) also resets this timer, so a crawl
// finishing moments before the interval would have fired doesn't trigger an
// almost-immediate redundant second run.
//
// That first recompute runs in the background (go pr.recompute()) rather
// than blocking here: on a large corpus it can take minutes (a full-graph
// PageRank pass over every document), and main() still has
// RecoverInterruptedCrawls and http.ListenAndServe to get through after
// this returns -- crawl-server should start accepting requests (and
// resuming interrupted jobs) immediately with the previous scores still in
// place, not sit unreachable until a potentially long recompute finishes.
// pageRankRecomputer.recompute's own running-guard keeps this from
// overlapping with the ticker below, which would otherwise see a
// zero-value lastRun() and fire a redundant concurrent second pass before
// this first one even finishes.
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
	// running guards against two recompute()s overlapping -- e.g. the
	// startup call (backgrounded in runPageRankScheduler, above) still
	// going when the ticker's first tick fires and sees a zero-value
	// lastRun(), or a crawl completing (OnCrawlComplete) while the
	// periodic ticker's own run is already underway. A second call while
	// one is in flight just returns immediately rather than running a
	// wasteful, contending second full-corpus pass.
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

// runScheduler triggers every scheduled crawl that's due, once immediately
// (so a schedule that came due while the process was down isn't stuck
// waiting a full poll interval) and then again on every tick for as long
// as ctx stays alive. It calls handler.TriggerScheduledCrawl -- the same
// job-creation path handleCrawl uses for a manually triggered crawl, plus
// a completion callback so TriggerDueCrawls can correct next_run_at to
// reflect when the crawl actually finished, not just when it started.
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
	bootstrap.SyncSettings(ctx, repo, nil, opSettings, nil, repo)

	settingsEncryptionKey, err := settingscrypto.ParseKey(bootstrap.GetEnv("SETTINGS_ENCRYPTION_KEY", ""))
	if err != nil {
		log.Fatal(err)
	}
	embedder := bootstrap.NewEmbedder(bootstrap.DecryptEmbeddingKey(opSettings.Get(), settingsEncryptionKey))
	// Enables Postgres pgvector ANN search for this process when available
	// (so SaveDocument populates the vector column below), never fatal
	// otherwise. Must run after embedder is constructed -- see
	// cmd/search's identical comment and domain.OperationalSettingsValues.
	// EmbeddingProvider for why.
	repo.EnableANN(ctx, embedder.Dimensions())
	// fetcher does plain HTTP; wrapping it in RenderAwareFetcher adds an
	// opt-in real-browser rendering path (see internal/adapters/
	// browserfetcher) on top, chosen per-crawl or by the Tuning page's
	// global default -- with rendering left off (the default), this is
	// byte-for-byte the same plain-HTTP behavior as before the feature
	// existed. Neither browser engine actually starts a process until a
	// crawl first asks for it.
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
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(renderingFetcher, robotsChecker, repo, embedder, parseHTML, opSettings)

	pageRank := runPageRankScheduler(ctx, repo, repo, opSettings)

	handler := restapi.New(restapi.Config{
		Crawler:   crawlerSvc,
		CrawlJobs: repo,
		Health:    repo,
		// A crawl just changed the link graph -- recompute right away
		// (in the background, so a slow recompute never delays the crawl
		// job's own reported completion or the concurrency semaphore's
		// release) in addition to pageRank's own periodic ticker.
		OnCrawlComplete: func() { go pageRank.recompute() },
		// See requireCrawlInternalToken's doc comment: opt-in shared
		// secret admin-server's crawlclient.Client must send back --
		// empty by default, so an existing deployment that hasn't set
		// this keeps working unchanged.
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

	// A scheduled crawl's in_progress flag only ever gets cleared by its
	// own triggered run's completion callback -- an in-memory closure that
	// dies with this process (see crawl_internal.go's TriggerScheduledCrawl
	// and ResumeCrawlJob, which knows nothing about it). A restart while
	// any schedule was mid-run leaves it stuck true forever otherwise,
	// silently taking that schedule out of both its own recurring cadence
	// and "Run now" (see RunScheduledCrawlNow's doc comment) until this
	// runs. Nothing can genuinely still be in progress the instant this
	// process starts, so every stale flag is reset before the scheduler's
	// first tick can ever run.
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
