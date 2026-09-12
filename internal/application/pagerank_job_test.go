package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
)

type fakePageRankRepo struct {
	graph             map[string][]string
	linkGraphErr      error
	updated           map[string]float64
	updatePageRankErr error
	updateCalls       int
}

func (r *fakePageRankRepo) LinkGraph(context.Context) (map[string][]string, error) {
	if r.linkGraphErr != nil {
		return nil, r.linkGraphErr
	}
	return r.graph, nil
}

func (r *fakePageRankRepo) UpdatePageRanks(_ context.Context, scores map[string]float64) error {
	r.updateCalls++
	if r.updatePageRankErr != nil {
		return r.updatePageRankErr
	}
	r.updated = scores
	return nil
}

func TestRunPageRankJob_ComputesAndWritesScores(t *testing.T) {
	repo := &fakePageRankRepo{
		graph: map[string][]string{
			"a": {"b"},
			"b": {"a"},
		},
	}
	if err := application.RunPageRankJob(context.Background(), repo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("expected UpdatePageRanks called exactly once, got %d", repo.updateCalls)
	}
	if len(repo.updated) != 2 {
		t.Fatalf("expected scores for 2 nodes, got %+v", repo.updated)
	}
	if repo.updated["a"] <= 0 || repo.updated["b"] <= 0 {
		t.Errorf("expected both nodes to get a positive score, got %+v", repo.updated)
	}
}

func TestRunPageRankJob_EmptyGraphSkipsUpdate(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{}}
	if err := application.RunPageRankJob(context.Background(), repo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalls != 0 {
		t.Errorf("expected UpdatePageRanks not called for an empty graph, got %d calls", repo.updateCalls)
	}
}

func TestRunPageRankJob_PropagatesLinkGraphError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakePageRankRepo{linkGraphErr: wantErr}
	if err := application.RunPageRankJob(context.Background(), repo); !errors.Is(err, wantErr) {
		t.Errorf("expected LinkGraph error to propagate, got %v", err)
	}
	if repo.updateCalls != 0 {
		t.Errorf("expected UpdatePageRanks not called when LinkGraph fails, got %d calls", repo.updateCalls)
	}
}

func TestRunPageRankJob_PropagatesUpdateError(t *testing.T) {
	wantErr := errors.New("update failed")
	repo := &fakePageRankRepo{
		graph:             map[string][]string{"a": {"b"}},
		updatePageRankErr: wantErr,
	}
	if err := application.RunPageRankJob(context.Background(), repo); !errors.Is(err, wantErr) {
		t.Errorf("expected UpdatePageRanks error to propagate, got %v", err)
	}
}
