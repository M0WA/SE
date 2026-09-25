package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNewDocumentJobID_NonEmptyAndUniquePerCall(t *testing.T) {
	a := domain.NewDocumentJobID()
	b := domain.NewDocumentJobID()
	if a == "" || b == "" {
		t.Fatal("expected a non-empty ID")
	}
	if a == b {
		t.Errorf("expected two calls to mint distinct IDs, got %q twice", a)
	}
}
