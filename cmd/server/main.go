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
	"searchengine/internal/domain"
)

func main() {
	driver := getEnv("DB_DRIVER", "sqlite")
	dsn := getEnv("DB_DSN", "file:search.db?cache=shared")
	settings := domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B)
	opSettings := domain.DefaultOperationalSettings()

	ctx := context.Background()
	repo, err := sqlrepo.New(ctx, driver, dsn)
	if err != nil {
		log.Fatalf("DB connection failed (%s): %v", driver, err)
	}
	defer repo.Close()

	embedder := hashembed.New(128)
	searchSvc := application.NewHybridAsSearchService(repo, embedder, settings)
	debugSvc := application.NewHybridSearchService(repo, embedder, settings)

	fetcher := httpfetcher.New(opSettings)
	robotsChecker := robots.New(fetcher)
	parseHTML := func(html, pageURL string) (string, string, []string) {
		return htmlparser.Parse(strings.NewReader(html), pageURL)
	}
	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parseHTML, opSettings)

	adminUser := getEnv("ADMIN_USER", "")
	adminPass := getEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	handler := restapi.New(restapi.Config{
		Search:     searchSvc,
		Crawler:    crawlerSvc,
		Debug:      debugSvc,
		Admin:      repo,
		Settings:   settings,
		OpSettings: opSettings,
		DBDriver:   driver,
		AdminUser:  adminUser,
		AdminPass:  adminPass,
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
