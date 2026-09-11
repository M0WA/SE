package main

import (
	"context"
	"log"
	"net/http"
	"strings"
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

func main() {
	ctx := context.Background()
	repo, driver, err := bootstrap.OpenDB(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer repo.Close()

	opSettings := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(ctx, repo, nil, opSettings, nil)
	embedder := hashembed.New(128)
	fetcher := httpfetcher.New(opSettings)
	robotsChecker := robots.New(fetcher)
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parseHTML, opSettings)

	handler := restapi.New(restapi.Config{
		Crawler:   crawlerSvc,
		CrawlJobs: domain.NewCrawlJobStore(),
		Health:    repo,
	})

	go runScheduler(ctx, repo, handler)

	addr := bootstrap.GetEnv("CRAWL_LISTEN_ADDR", "127.0.0.1:8082")
	log.Printf("Crawl server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesCrawlInternal()))
}
