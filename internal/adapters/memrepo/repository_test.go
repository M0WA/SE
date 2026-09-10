package memrepo_test

import (
	"context"
	"testing"

	"searchengine/internal/adapters/memrepo"
	"searchengine/internal/domain"
)

func TestRepository_SaveAndAll(t *testing.T) {
	repo := memrepo.New()
	ctx := context.Background()

	if err := repo.Save(ctx, domain.Document{ID: "1", URL: "http://a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Save(ctx, domain.Document{ID: "2", URL: "http://b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	docs, err := repo.All(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
}

func TestRepository_AllReturnsDefensiveCopy(t *testing.T) {
	repo := memrepo.New()
	ctx := context.Background()
	_ = repo.Save(ctx, domain.Document{ID: "1"})

	docs, _ := repo.All(ctx)
	docs[0].ID = "mutated"

	fresh, _ := repo.All(ctx)
	if fresh[0].ID == "mutated" {
		t.Error("expected All() to return a copy")
	}
}

func TestRepository_AllOnEmpty(t *testing.T) {
	repo := memrepo.New()
	docs, err := repo.All(context.Background())
	if err != nil || len(docs) != 0 {
		t.Errorf("expected empty result, got %v err=%v", docs, err)
	}
}
