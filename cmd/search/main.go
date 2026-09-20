package main

import (
	"context"
	"log"
	"net/http"
	"sync"

	"searchengine/internal/adapters/hookrunner"
	"searchengine/internal/adapters/httpchat"
	"searchengine/internal/adapters/httpsearxng"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/settingscrypto"
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

	// Each of these does its own independent blocking DB round-trip -- run
	// concurrently so startup latency is the slowest one, not their sum.
	// Embedder construction below needs opSettings already synced, so it
	// can't join this same batch.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		bootstrap.SyncSettings(ctx, repo, settings, opSettings, overrides, repo, repo)
	}()
	go func() { defer wg.Done(); bootstrap.SyncCorpusStats(ctx, repo, corpusStats) }()
	go func() { defer wg.Done(); bootstrap.SyncVocabulary(ctx, repo, vocabulary) }()
	wg.Wait()

	settingsEncryptionKey, err := settingscrypto.ParseKey(bootstrap.GetEnv("SETTINGS_ENCRYPTION_KEY", ""))
	if err != nil {
		log.Fatal(err)
	}
	endpoints := bootstrap.LoadEmbeddingEndpoints(ctx, repo, settingsEncryptionKey)
	embedders := bootstrap.NewEmbedders(opSettings.Get().EmbeddingHashEnabled, endpoints)
	// Attempt to enable Postgres pgvector ANN search -- a no-op on
	// SQLite/MySQL, never fatal even without the extension (falls back to
	// SampleEmbeddings). Must run after embedders are constructed, since
	// each provider's vector column is sized to its own Dimensions().
	repo.EnableANN(ctx, bootstrap.EmbedderDimensions(embedders))

	searchSvc := application.NewHybridAsSearchService(repo, embedders, settings, opSettings, overrides, corpusStats, vocabulary)

	// chatHooksDir is where every ChatHook.Script must live -- see
	// hookrunner.Runner.Dir's doc comment. Left at its default, an admin who
	// hasn't set CHAT_HOOKS_DIR yet can still configure hooks; scripts just
	// won't resolve to anything until the directory exists and is populated.
	chatHooksDir := bootstrap.GetEnv("CHAT_HOOKS_DIR", "/etc/searchengine/hooks")
	// internalSearchAPIKey is unset (empty) by default, meaning the
	// /search internal-key bypass doesn't exist at all -- see
	// requireAuthAPIOrInternalKey's doc comment. An admin opts in by
	// setting SEARCH_INTERNAL_API_KEY, letting a trusted local caller
	// (e.g. a SearXNG engine plugin) call /search without a session.
	internalSearchAPIKey := bootstrap.GetEnv("SEARCH_INTERNAL_API_KEY", "")

	handler := restapi.New(restapi.Config{
		Search:               searchSvc,
		OpSettings:           opSettings,
		Health:               repo,
		Sessions:             repo,
		ChatEndpoints:        repo,
		InternalSearchAPIKey: internalSearchAPIKey,
		Chat:                 application.NewChatService(repo, httpchat.New(), httpsearxng.New(), repo, hookrunner.New(chatHooksDir)),
	})

	addr := bootstrap.GetEnv("SEARCH_LISTEN_ADDR", "127.0.0.1:8080")
	log.Printf("Search server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesSearch()))
}
