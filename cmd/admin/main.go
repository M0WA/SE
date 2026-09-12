package main

import (
	"context"
	"log"
	"net/http"
	"sync"

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
	corpusStats := domain.NewCorpusStatsCache(0, 1)
	vocabulary := domain.NewVocabularyCache(nil)
	embedder := hashembed.New(128)

	// See cmd/search's identical block: each of these is an independent
	// blocking DB round-trip against unrelated tables/state, so running
	// them concurrently makes startup latency the slowest one rather than
	// their sum.
	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); bootstrap.SyncSettings(ctx, repo, settings, opSettings, overrides, repo) }()
	go func() { defer wg.Done(); bootstrap.SyncCorpusStats(ctx, repo, corpusStats) }()
	go func() { defer wg.Done(); bootstrap.SyncVocabulary(ctx, repo, vocabulary) }()
	go func() {
		defer wg.Done()
		// Enables Postgres pgvector ANN search for this process when
		// available, never fatal otherwise.
		repo.EnableANN(ctx, embedder.Dimensions())
	}()
	wg.Wait()

	debugSvc := application.NewHybridSearchService(repo, embedder, settings, opSettings, overrides, corpusStats, vocabulary)

	adminUser := bootstrap.GetEnv("ADMIN_USER", "")
	adminPass := bootstrap.GetEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	jobs := crawlclient.New(bootstrap.GetEnv("CRAWL_SERVER_URL", "http://127.0.0.1:8082"))

	handler := restapi.New(restapi.Config{
		Jobs:            jobs,
		Debug:           debugSvc,
		Admin:           repo,
		Settings:        settings,
		OpSettings:      opSettings,
		Overrides:       overrides,
		SettingsStore:   repo,
		ScheduledCrawls: repo,
		Health:          repo,
		Sessions:        repo,
		DBDriver:        driver,
		AdminUser:       adminUser,
		AdminPass:       adminPass,
	})

	addr := bootstrap.GetEnv("ADMIN_LISTEN_ADDR", "127.0.0.1:8081")
	log.Printf("Admin server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesAdmin()))
}
