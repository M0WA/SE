package bootstrap_test

import (
	"context"
	"testing"

	"searchengine/internal/bootstrap"
)

func TestGetEnv_ReturnsSetValue(t *testing.T) {
	t.Setenv("SEARCHENGINE_TEST_VAR", "actual-value")
	if got := bootstrap.GetEnv("SEARCHENGINE_TEST_VAR", "fallback"); got != "actual-value" {
		t.Errorf("expected actual-value, got %q", got)
	}
}

func TestGetEnv_ReturnsFallbackWhenUnset(t *testing.T) {
	if got := bootstrap.GetEnv("SEARCHENGINE_TEST_UNSET_VAR", "fallback"); got != "fallback" {
		t.Errorf("expected fallback, got %q", got)
	}
}

func TestGetEnv_ReturnsFallbackWhenSetButEmpty(t *testing.T) {
	t.Setenv("SEARCHENGINE_TEST_EMPTY_VAR", "")
	if got := bootstrap.GetEnv("SEARCHENGINE_TEST_EMPTY_VAR", "fallback"); got != "fallback" {
		t.Errorf("expected fallback for an empty (but set) env var, got %q", got)
	}
}

func TestOpenDB_UsesEnvDriverAndDSN(t *testing.T) {
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", "file:bootstrapopendbtest?mode=memory&cache=shared")
	repo, driver, err := bootstrap.OpenDB(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer repo.Close()
	if driver != "sqlite" {
		t.Errorf("expected driver sqlite, got %q", driver)
	}
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("expected a working repository, got ping error: %v", err)
	}
}

func TestOpenDB_PropagatesConnectionError(t *testing.T) {
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", "file:/nonexistent-dir-xyz/test.db")
	if _, _, err := bootstrap.OpenDB(context.Background()); err == nil {
		t.Error("expected an error for an invalid DSN")
	}
}
