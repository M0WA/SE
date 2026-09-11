package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

func main() {
	ctx := context.Background()
	repo, driver, err := bootstrap.OpenDB(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer repo.Close()

	opSettings := domain.DefaultOperationalSettings()
	embedder := hashembed.New(128)
	fetcher := httpfetcher.New(opSettings)
	robotsChecker := robots.New(fetcher)
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parseHTML, opSettings)

	handler := restapi.New(restapi.Config{
		Crawler: crawlerSvc,
	})

	addr := bootstrap.GetEnv("CRAWL_LISTEN_ADDR", "127.0.0.1:8082")
	log.Printf("Crawl server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesCrawlInternal()))
}
