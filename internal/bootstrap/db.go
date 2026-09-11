// Package bootstrap holds the small pieces of wiring boilerplate shared by
// searchengine's three main packages (cmd/search, cmd/admin, cmd/crawl):
// opening the SQL repository from DB_DRIVER/DB_DSN, and reading an env var
// with a fallback. Each process opens its own DB connection -- a *sql.DB
// can't be shared across separate OS processes.
package bootstrap

import (
	"context"
	"fmt"
	"os"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/sqlrepo"
)

// OpenDB opens the shared SQL repository using the same DB_DRIVER/DB_DSN
// environment variables every searchengine process reads, and reports which
// driver was used (needed for the admin diagnostics API).
func OpenDB(ctx context.Context) (repo *sqlrepo.Repository, driver string, err error) {
	driver = GetEnv("DB_DRIVER", "sqlite")
	dsn := GetEnv("DB_DSN", "file:search.db?cache=shared")
	repo, err = sqlrepo.New(ctx, driver, dsn)
	if err != nil {
		return nil, driver, fmt.Errorf("DB connection failed (%s): %w", driver, err)
	}
	return repo, driver, nil
}

func GetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
