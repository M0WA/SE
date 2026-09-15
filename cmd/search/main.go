package main

import (
	"context"
	"log"
	"net/http"
	"sync"

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

	// Each of these does its own blocking DB round-trip against unrelated
	// tables/state, and none depends on another's result -- run them
	// concurrently so startup latency is the slowest one of the three
	// rather than their sum. Embedder construction (below) needs
	// opSettings already synced from the settings store (it picks
	// hashembed vs. httpembed off opSettings.EmbeddingProvider), so it
	// can't join this same batch.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); bootstrap.SyncSettings(ctx, repo, settings, opSettings, overrides, repo) }()
	go func() { defer wg.Done(); bootstrap.SyncCorpusStats(ctx, repo, corpusStats) }()
	go func() { defer wg.Done(); bootstrap.SyncVocabulary(ctx, repo, vocabulary) }()
	wg.Wait()

	embedder := bootstrap.NewEmbedder(opSettings.Get())
	// Attempt to enable Postgres pgvector-backed ANN semantic search -- a
	// no-op on SQLite/MySQL, and never fatal even on Postgres without the
	// extension installed (see sqlrepo.Repository.EnableANN): this process
	// just keeps using the brute-force SampleEmbeddings fallback either
	// way. Must run after embedder is constructed, since its vector column
	// is sized to embedder.Dimensions() -- see domain.
	// OperationalSettingsValues.EmbeddingProvider for why that means this
	// can no longer run concurrently with the settings sync above.
	repo.EnableANN(ctx, embedder.Dimensions())

	searchSvc := application.NewHybridAsSearchService(repo, embedder, settings, opSettings, overrides, corpusStats, vocabulary)

	handler := restapi.New(restapi.Config{
		Search:     searchSvc,
		OpSettings: opSettings,
		Health:     repo,
		Sessions:   repo,
	})

	addr := bootstrap.GetEnv("SEARCH_LISTEN_ADDR", "127.0.0.1:8080")
	log.Printf("Search server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesSearch()))
}
