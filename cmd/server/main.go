package main

import (
	"context"
	"log"
	"net/http"
	"os"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/application"
)

func main() {
	driver := getEnv("DB_DRIVER", "sqlite3")
	dsn := getEnv("DB_DSN", "file:search.db?cache=shared")
	alpha := 0.5

	ctx := context.Background()
	repo, err := sqlrepo.New(ctx, driver, dsn)
	if err != nil {
		log.Fatalf("DB-Verbindung fehlgeschlagen (%s): %v", driver, err)
	}
	defer repo.Close()

	embedder := hashembed.New(128)
	searchSvc := application.NewHybridSearchService(repo, embedder, alpha)

	handler := restapi.New(nil, nil)
	_ = searchSvc
	log.Printf("Suchmaschine läuft auf :8080 (DB: %s)", driver)
	log.Fatal(http.ListenAndServe(":8080", handler.Routes()))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
