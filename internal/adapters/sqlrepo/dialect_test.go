package sqlrepo_test

import (
	"strings"
	"testing"

	"searchengine/internal/adapters/sqlrepo"
)

func TestNewDialect_SelectsCorrectDialect(t *testing.T) {
	cases := map[string]string{
		"sqlite3": "sqlite3", "mysql": "mysql", "postgres": "postgres",
		"pgx": "postgres", "unbekannt": "sqlite3",
	}
	for driver, wantName := range cases {
		if got := sqlrepo.NewDialect(driver).Name(); got != wantName {
			t.Errorf("Treiber %q: erwartet Dialect %q, bekam %q", driver, wantName, got)
		}
	}
}

func TestPostgresDialect_UsesPositionalPlaceholders(t *testing.T) {
	d := sqlrepo.NewDialect("postgres")
	if !strings.Contains(d.UpsertDocumentSQL(), "$1") {
		t.Error("erwartet $1-Platzhalter im Postgres-Upsert")
	}
}

func TestSQLiteDialect_UsesQuestionMarkPlaceholders(t *testing.T) {
	d := sqlrepo.NewDialect("sqlite3")
	if !strings.Contains(d.UpsertDocumentSQL(), "?") {
		t.Error("erwartet ?-Platzhalter im SQLite-Upsert")
	}
}

func TestAllDialects_CreateSchemaSQLNonEmpty(t *testing.T) {
	for _, driver := range []string{"sqlite3", "mysql", "postgres"} {
		stmts := sqlrepo.NewDialect(driver).CreateSchemaSQL()
		if len(stmts) == 0 {
			t.Errorf("erwartet Schema-Statements für %s", driver)
		}
	}
}
