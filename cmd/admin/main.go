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
	"searchengine/internal/adapters/sqlrepo"
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

	// These three are independent blocking DB round-trips (see cmd/search's
	// identical block) -- run concurrently so startup latency is the
	// slowest one, not their sum. Embedder construction needs opSettings
	// already synced, so it can't join this batch.
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
	// Enables Postgres pgvector ANN search when available, never fatal
	// otherwise. Must run after embedders are constructed.
	repo.EnableANN(ctx, bootstrap.EmbedderDimensions(embedders))

	debugSvc := application.NewHybridSearchService(repo, embedders, settings, opSettings, overrides, corpusStats, vocabulary)

	// A previous instance killed mid-recompute leaves InProgress=true and
	// a LastDocID checkpoint behind -- resume that same run in the
	// background from the checkpoint rather than silently discarding the
	// (possibly hours of) progress it already made.
	embeddingRecomputeConcurrency := func() int { return opSettings.Get().EmbeddingRecomputeConcurrency }
	if application.ResumeStaleEmbeddingRecomputeIfAny(ctx, repo, embedders, repo, opSettings.Get().EmbeddingTitleWeight, embeddingRecomputeConcurrency) {
		log.Print("resuming an embedding recompute left in-progress from a previous restart")
	}

	// Best-effort, non-fatal: seeds starter Agent rows for a fresh
	// deployment; one with any agents already configured is untouched.
	if err := repo.SeedDefaultAgents(ctx); err != nil {
		log.Printf("seeding default agents: %v", err)
	}

	warnIfNoAdminUser(ctx, repo)

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
		ChatVision:            repo,
		DocumentJobs:          repo,
		MCPServers:            repo,
		MCPTools:              mcpclient.New(),
		Agents:                repo,
		Users:                 repo,
		Health:                repo,
		Sessions:              repo,
		DBDriver:              driver,
		SettingsEncryptionKey: settingsEncryptionKey,
	})

	addr := bootstrap.GetEnv("ADMIN_LISTEN_ADDR", "127.0.0.1:8081")
	log.Printf("Admin server running on %s (DB: %s)", addr, driver)
	log.Fatal(http.ListenAndServe(addr, handler.RoutesAdmin()))
}

// warnIfNoAdminUser is a best-effort, non-fatal startup check (same spirit
// as SeedDefaultAgents' own log-and-continue on error): there is no
// hardcoded admin account to fall back on any more, so a fresh install (or
// one migrated from before this User.IsAdmin flag existed) with zero
// IsAdmin=true rows has /admin and /login refusing every sign-in until
// packaging/create-admin.sh seeds the first one. A query error here is
// logged and otherwise ignored -- it isn't this check's job to fail startup.
func warnIfNoAdminUser(ctx context.Context, repo *sqlrepo.Repository) {
	users, err := repo.ListUsers(ctx)
	if err != nil {
		log.Printf("checking for an admin user: %v", err)
		return
	}
	for _, u := range users {
		if u.IsAdmin {
			return
		}
	}
	log.Print("no admin user exists yet: /crawl and /admin will refuse all sign-ins until one is created -- run packaging/create-admin.sh")
}
