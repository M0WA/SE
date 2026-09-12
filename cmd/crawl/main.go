package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// schedulerPollInterval is how often crawl-server checks for scheduled
// crawls that have come due -- frequent enough that a schedule fires
// close to its intended time without polling the database constantly.
const schedulerPollInterval = 60 * time.Second

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
func runPageRankScheduler(ctx context.Context, repo ports.PageRankRepository, opSettings *domain.OperationalSettings) *pageRankRecomputer {
	pr := &pageRankRecomputer{ctx: ctx, repo: repo}
	pr.recompute()

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
	ctx  context.Context
	repo ports.PageRankRepository
	mu   sync.Mutex
	last time.Time
}

func (p *pageRankRecomputer) recompute() {
	if err := application.RunPageRankJob(p.ctx, p.repo); err != nil {
		log.Printf("recomputing pagerank: %v", err)
	}
	p.mu.Lock()
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
// as ctx stays alive. It calls handler.TriggerCrawl -- the exact same
// job-creation path handleCrawl uses for a manually triggered crawl -- so
// a scheduled run gets identical job tracking and concurrency limiting.
func runScheduler(ctx context.Context, store ports.ScheduledCrawlStore, handler *restapi.Handler) {
	triggerDue := func() {
		if _, err := application.TriggerDueCrawls(ctx, store, handler.TriggerCrawl, time.Now()); err != nil {
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
	embedder := hashembed.New(128)

	// See cmd/search's identical block: these two are independent blocking
	// DB round-trips against unrelated state, so running them concurrently
	// makes startup latency the slower one rather than their sum.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); bootstrap.SyncSettings(ctx, repo, nil, opSettings, nil, repo) }()
	go func() {
		defer wg.Done()
		// Enables Postgres pgvector ANN search for this process when
		// available (so SaveDocument populates the vector column below),
		// never fatal otherwise.
		repo.EnableANN(ctx, embedder.Dimensions())
	}()
	wg.Wait()
	fetcher := httpfetcher.New(opSettings)
	robotsChecker := robots.New(fetcher)
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parseHTML, opSettings)

	pageRank := runPageRankScheduler(ctx, repo, opSettings)

	handler := restapi.New(restapi.Config{
		Crawler:   crawlerSvc,
		CrawlJobs: repo,
		Health:    repo,
		// A crawl just changed the link graph -- recompute right away
		// (in the background, so a slow recompute never delays the crawl
		// job's own reported completion or the concurrency semaphore's
		// release) in addition to pageRank's own periodic ticker.
		OnCrawlComplete: func() { go pageRank.recompute() },
	})

	go runScheduler(ctx, repo, handler)
	go runCrawlJobPruner(ctx, repo, opSettings)

	addr := bootstrap.GetEnv("CRAWL_LISTEN_ADDR", "127.0.0.1:8082")
	log.Printf("Crawl server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesCrawlInternal()))
}
