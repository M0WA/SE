package main

import (
	"context"
	"log"
	"net/http"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/restapi"
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

	settings := domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B)
	opSettings := domain.DefaultOperationalSettings()
	overrides := domain.DefaultRankingOverrides()
	embedder := hashembed.New(128)
	debugSvc := application.NewHybridSearchService(repo, embedder, settings, overrides)

	adminUser := bootstrap.GetEnv("ADMIN_USER", "")
	adminPass := bootstrap.GetEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	jobs := crawlclient.New(bootstrap.GetEnv("CRAWL_SERVER_URL", "http://127.0.0.1:8082"))

	handler := restapi.New(restapi.Config{
		Jobs:       jobs,
		Debug:      debugSvc,
		Admin:      repo,
		Settings:   settings,
		OpSettings: opSettings,
		Overrides:  overrides,
		DBDriver:   driver,
		AdminUser:  adminUser,
		AdminPass:  adminPass,
	})

	addr := bootstrap.GetEnv("ADMIN_LISTEN_ADDR", "127.0.0.1:8081")
	log.Printf("Admin server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesAdmin()))
}
