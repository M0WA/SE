package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/application"
)

func main() {
	driver := getEnv("DB_DRIVER", "sqlite")
	dsn := getEnv("DB_DSN", "file:search.db?cache=shared")
	alpha := 0.5

	ctx := context.Background()
	repo, err := sqlrepo.New(ctx, driver, dsn)
	if err != nil {
		log.Fatalf("DB connection failed (%s): %v", driver, err)
	}
	defer repo.Close()

	embedder := hashembed.New(128)
	searchSvc := application.NewHybridAsSearchService(repo, embedder, alpha)
	debugSvc := application.NewHybridSearchService(repo, embedder, alpha)

	fetcher := httpfetcher.New()
	robotsChecker := robots.New(fetcher)
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parseHTML)

	adminUser := getEnv("ADMIN_USER", "")
	adminPass := getEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	handler := restapi.New(restapi.Config{
		Search:    searchSvc,
		Crawler:   crawlerSvc,
		Debug:     debugSvc,
		Admin:     repo,
		DBDriver:  driver,
		AdminUser: adminUser,
		AdminPass: adminPass,
	})
	log.Printf("Search engine running on :8080 (DB: %s)", driver)
	log.Fatal(http.ListenAndServe(":8080", handler.Routes()))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
