package main

import (
	"context"
	"log"
	"net/http"
	"sync"

	"searchengine/internal/adapters/crawlclient"
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

	// See cmd/search's identical block: these three are independent
	// blocking DB round-trips against unrelated tables/state, so running
	// them concurrently makes startup latency the slowest one rather than
	// their sum. Embedder construction can't join this batch -- it needs
	// opSettings already synced.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); bootstrap.SyncSettings(ctx, repo, settings, opSettings, overrides, repo) }()
	go func() { defer wg.Done(); bootstrap.SyncCorpusStats(ctx, repo, corpusStats) }()
	go func() { defer wg.Done(); bootstrap.SyncVocabulary(ctx, repo, vocabulary) }()
	wg.Wait()

	embedder := bootstrap.NewEmbedder(opSettings.Get())
	// Enables Postgres pgvector ANN search for this process when
	// available, never fatal otherwise. Must run after embedder is
	// constructed -- see cmd/search's identical comment.
	repo.EnableANN(ctx, embedder.Dimensions())

	debugSvc := application.NewHybridSearchService(repo, embedder, settings, opSettings, overrides, corpusStats, vocabulary)

	adminUser := bootstrap.GetEnv("ADMIN_USER", "")
	adminPass := bootstrap.GetEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	crawlInternalToken := bootstrap.GetEnv("CRAWL_INTERNAL_TOKEN", "")
	jobs := crawlclient.New(bootstrap.GetEnv("CRAWL_SERVER_URL", "http://127.0.0.1:8082"), crawlInternalToken)

	handler := restapi.New(restapi.Config{
		Jobs:            jobs,
		Debug:           debugSvc,
		Admin:           repo,
		PageRank:        repo,
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
