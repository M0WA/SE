package sqlrepo_test

import (
	"strings"
	"testing"

	"searchengine/internal/adapters/sqlrepo"
)

func TestNewDialect_SelectsCorrectDialect(t *testing.T) {
	cases := map[string]string{
		"sqlite3": "sqlite3", "mysql": "mysql", "postgres": "postgres",
		"pgx": "postgres", "unknown": "sqlite3",
	}
	for driver, wantName := range cases {
		if got := sqlrepo.NewDialect(driver).Name(); got != wantName {
			t.Errorf("driver %q: expected dialect %q, got %q", driver, wantName, got)
		}
	}
}

func TestPostgresDialect_UsesPositionalPlaceholders(t *testing.T) {
	d := sqlrepo.NewDialect("postgres")
	if !strings.Contains(d.UpsertDocumentSQL(), "$1") {
		t.Error("expected $1 placeholder in Postgres upsert")
	}
}

func TestSQLiteDialect_UsesQuestionMarkPlaceholders(t *testing.T) {
	d := sqlrepo.NewDialect("sqlite3")
	if !strings.Contains(d.UpsertDocumentSQL(), "?") {
		t.Error("expected ?-placeholder in SQLite upsert")
	}
}

func TestAllDialects_CreateSchemaSQLNonEmpty(t *testing.T) {
	for _, driver := range []string{"sqlite3", "mysql", "postgres"} {
		stmts := sqlrepo.NewDialect(driver).CreateSchemaSQL()
		if len(stmts) == 0 {
			t.Errorf("expected schema statements for %s", driver)
		}
	}
}
