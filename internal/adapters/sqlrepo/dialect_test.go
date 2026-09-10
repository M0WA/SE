package sqlrepo_test

import (
	"strings"
	"testing"

	"searchengine/internal/adapters/sqlrepo"
)

func TestNewDialect_SelectsCorrectDialect(t *testing.T) {
	cases := map[string]string{
		"sqlite": "sqlite", "mysql": "mysql", "postgres": "postgres",
		"pgx": "postgres", "unknown": "sqlite",
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
	d := sqlrepo.NewDialect("sqlite")
	if !strings.Contains(d.UpsertDocumentSQL(), "?") {
		t.Error("expected ?-placeholder in SQLite upsert")
	}
}

func TestMySQLDialect_UsesQuestionMarkPlaceholders(t *testing.T) {
	d := sqlrepo.NewDialect("mysql")
	if d.Placeholder(1) != "?" {
		t.Errorf("expected ? placeholder for MySQL, got %q", d.Placeholder(1))
	}
	if !strings.Contains(d.UpsertDocumentSQL(), "ON DUPLICATE KEY UPDATE") {
		t.Error("expected MySQL's ON DUPLICATE KEY UPDATE upsert syntax")
	}
}

func TestPostgresDialect_PlaceholderNumbersPositionally(t *testing.T) {
	d := sqlrepo.NewDialect("postgres")
	cases := map[int]string{0: "$0", 1: "$1", 9: "$9", 12: "$12", 103: "$103"}
	for pos, want := range cases {
		if got := d.Placeholder(pos); got != want {
			t.Errorf("Placeholder(%d): expected %q, got %q", pos, want, got)
		}
	}
}

func TestAllDialects_CreateSchemaSQLNonEmpty(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql", "postgres"} {
		stmts := sqlrepo.NewDialect(driver).CreateSchemaSQL()
		if len(stmts) == 0 {
			t.Errorf("expected schema statements for %s", driver)
		}
	}
}
