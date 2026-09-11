package main

import (
	"context"
	"log"
	"net/http"

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
	searchSvc := application.NewHybridAsSearchService(repo, embedder, settings, overrides)

	handler := restapi.New(restapi.Config{
		Search:     searchSvc,
		OpSettings: opSettings,
	})

	addr := bootstrap.GetEnv("SEARCH_LISTEN_ADDR", "127.0.0.1:8080")
	log.Printf("Search server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesSearch()))
}
