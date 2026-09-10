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
		t.Fatalf("unerwarteter Fehler: %v", err)
	}
	if err := repo.Save(ctx, domain.Document{ID: "2", URL: "http://b"}); err != nil {
		t.Fatalf("unerwarteter Fehler: %v", err)
	}

	docs, err := repo.All(ctx)
	if err != nil {
		t.Fatalf("unerwarteter Fehler: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("erwartet 2 Docs, bekam %d", len(docs))
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
		t.Error("erwartet, dass All() eine Kopie liefert")
	}
}

func TestRepository_AllOnEmpty(t *testing.T) {
	repo := memrepo.New()
	docs, err := repo.All(context.Background())
	if err != nil || len(docs) != 0 {
		t.Errorf("erwartet leeres Ergebnis, bekam %v err=%v", docs, err)
	}
}
