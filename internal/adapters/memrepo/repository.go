package memrepo

import (
	"context"
	"sync"

	"searchengine/internal/domain"
)

type Repository struct {
	mu   sync.RWMutex
	docs []domain.Document
}

func New() *Repository {
	return &Repository{}
}

func (r *Repository) Save(_ context.Context, doc domain.Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = append(r.docs, doc)
	return nil
}

func (r *Repository) All(_ context.Context) ([]domain.Document, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Document, len(r.docs))
	copy(out, r.docs)
	return out, nil
}
