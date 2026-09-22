package main

import (
	"context"
	"log"
	"net/http"
	"sync"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/adapters/mcpclient"
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

	// See cmd/search's identical block: these three are independent
	// blocking DB round-trips against unrelated tables/state, so running
	// them concurrently makes startup latency the slowest one rather than
	// their sum. Embedder construction can't join this batch -- it needs
	// opSettings already synced.
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
	// Enables Postgres pgvector ANN search for this process when
	// available, never fatal otherwise. Must run after embedders are
	// constructed -- see cmd/search's identical comment.
	repo.EnableANN(ctx, bootstrap.EmbedderDimensions(embedders))

	debugSvc := application.NewHybridSearchService(repo, embedders, settings, opSettings, overrides, corpusStats, vocabulary)

	// A previous instance killed mid-recompute leaves InProgress=true,
	// permanently blocking future triggers -- mirrors cmd/crawl's
	// ResetStaleInProgress call for scheduled_crawls.
	if application.ResetStaleEmbeddingRecomputeStatus(ctx, repo) {
		log.Print("reset a stale embedding recompute status left in-progress from a previous restart")
	}

	// Best-effort, non-fatal: a fresh deployment gets a small set of
	// ready-to-use starter Agent rows (see
	// sqlrepo.SeedDefaultAgents/default_agents.go); an existing one with
	// any agents already configured (including an admin who deleted every
	// seeded default down to zero) is left untouched.
	if err := repo.SeedDefaultAgents(ctx); err != nil {
		log.Printf("seeding default agents: %v", err)
	}

	adminUser := bootstrap.GetEnv("ADMIN_USER", "")
	adminPass := bootstrap.GetEnv("ADMIN_PASSWORD", "")
	if adminUser == "" || adminPass == "" {
		log.Print("ADMIN_USER/ADMIN_PASSWORD not set: /crawl and /admin will refuse all sign-ins")
	}

	crawlInternalToken := bootstrap.GetEnv("CRAWL_INTERNAL_TOKEN", "")
	jobs := crawlclient.New(bootstrap.GetEnv("CRAWL_SERVER_URL", "http://127.0.0.1:8082"), crawlInternalToken)

	handler := restapi.New(restapi.Config{
		Jobs:                  jobs,
		Debug:                 debugSvc,
		Admin:                 repo,
		PageRank:              repo,
		EmbeddingRepo:         repo,
		ContentDedupRepo:      repo,
		Embedders:             embedders,
		Settings:              settings,
		OpSettings:            opSettings,
		Overrides:             overrides,
		SettingsStore:         repo,
		ScheduledCrawls:       repo,
		EmbeddingEndpoints:    repo,
		ChatEndpoints:         repo,
		MCPServers:            repo,
		MCPTools:              mcpclient.New(),
		Agents:                repo,
		Users:                 repo,
		Health:                repo,
		Sessions:              repo,
		DBDriver:              driver,
		AdminUser:             adminUser,
		AdminPass:             adminPass,
		SettingsEncryptionKey: settingsEncryptionKey,
	})

	addr := bootstrap.GetEnv("ADMIN_LISTEN_ADDR", "127.0.0.1:8081")
	log.Printf("Admin server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesAdmin()))
}
