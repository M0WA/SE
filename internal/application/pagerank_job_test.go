package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeSettingsStore is a minimal in-memory ports.SettingsStore -- enough to
// exercise RunPageRankJobWithStatus/LoadPageRankStatus without a real DB.
type fakeSettingsStore struct {
	values    map[string]string
	saveErr   error
	getErr    error
	saveCalls []string // values saved, in order, for assertions on intermediate writes
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{values: make(map[string]string)}
}

func (s *fakeSettingsStore) SaveSetting(_ context.Context, key, value string) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.values[key] = value
	s.saveCalls = append(s.saveCalls, value)
	return nil
}

func (s *fakeSettingsStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	if s.getErr != nil {
		return "", false, s.getErr
	}
	v, ok := s.values[key]
	return v, ok, nil
}

type fakePageRankRepo struct {
	graph             map[string][]string
	linkGraphErr      error
	linkGraphDelay    time.Duration
	updated           map[string]float64
	updatePageRankErr error
	updateCalls       int
}

func (r *fakePageRankRepo) LinkGraph(context.Context) (map[string][]string, error) {
	if r.linkGraphDelay > 0 {
		time.Sleep(r.linkGraphDelay)
	}
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
	result, err := application.RunPageRankJob(context.Background(), repo)
	if err != nil {
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
	if result.Documents != 2 {
		t.Errorf("expected result.Documents=2, got %d", result.Documents)
	}
	if result.Iterations <= 0 {
		t.Errorf("expected a positive iteration count, got %d", result.Iterations)
	}
}

// TestRunPageRankJob_RecordsDuration proves DurationMs covers the whole
// job (LinkGraph included), not just the in-memory PageRank iteration --
// an artificial delay in LinkGraph must show up in the reported duration.
func TestRunPageRankJob_RecordsDuration(t *testing.T) {
	repo := &fakePageRankRepo{
		graph:          map[string][]string{"a": {"b"}, "b": {"a"}},
		linkGraphDelay: 5 * time.Millisecond,
	}
	result, err := application.RunPageRankJob(context.Background(), repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DurationMs < 5 {
		t.Errorf("expected DurationMs to reflect the ~5ms LinkGraph delay, got %d", result.DurationMs)
	}
}

func TestRunPageRankJob_EmptyGraphSkipsUpdate(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{}}
	if _, err := application.RunPageRankJob(context.Background(), repo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalls != 0 {
		t.Errorf("expected UpdatePageRanks not called for an empty graph, got %d calls", repo.updateCalls)
	}
}

func TestRunPageRankJob_PropagatesLinkGraphError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakePageRankRepo{linkGraphErr: wantErr}
	if _, err := application.RunPageRankJob(context.Background(), repo); !errors.Is(err, wantErr) {
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
	if _, err := application.RunPageRankJob(context.Background(), repo); !errors.Is(err, wantErr) {
		t.Errorf("expected UpdatePageRanks error to propagate, got %v", err)
	}
}

func TestRunPageRankJobWithStatus_RecordsCompletedRun(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}, "b": {"a"}}}
	settings := newFakeSettingsStore()

	result, err := application.RunPageRankJobWithStatus(context.Background(), repo, settings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status := application.LoadPageRankStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false once the run has finished")
	}
	if status.LastRunAt.IsZero() {
		t.Error("expected LastRunAt to be set")
	}
	if status.Documents != result.Documents || status.Iterations != result.Iterations || status.FinalDelta != result.FinalDelta || status.DurationMs != result.DurationMs {
		t.Errorf("expected persisted status to match the run result, got %+v want %+v", status, result)
	}
}

// TestRunPageRankJobWithStatus_SetsInProgressBeforeRunning proves the
// in-progress flag is visible to a concurrent reader before the run
// finishes, not only after -- the whole point of persisting it.
func TestRunPageRankJobWithStatus_SetsInProgressBeforeRunning(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}}}
	settings := newFakeSettingsStore()

	if _, err := application.RunPageRankJobWithStatus(context.Background(), repo, settings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(settings.saveCalls) < 2 {
		t.Fatalf("expected at least 2 saves (in-progress, then completed), got %d", len(settings.saveCalls))
	}
	var firstSave domain.PageRankStatus
	if err := json.Unmarshal([]byte(settings.saveCalls[0]), &firstSave); err != nil {
		t.Fatalf("decoding first save: %v", err)
	}
	if !firstSave.InProgress {
		t.Error("expected the first persisted status to have InProgress true")
	}
}

func TestRunPageRankJobWithStatus_ErrorClearsInProgressButKeepsLastResult(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}, "b": {"a"}}}
	settings := newFakeSettingsStore()
	if _, err := application.RunPageRankJobWithStatus(context.Background(), repo, settings); err != nil {
		t.Fatalf("unexpected error on first (successful) run: %v", err)
	}
	successStatus := application.LoadPageRankStatus(context.Background(), settings)

	repo.linkGraphErr = errors.New("db unavailable")
	if _, err := application.RunPageRankJobWithStatus(context.Background(), repo, settings); err == nil {
		t.Fatal("expected the second run's error to propagate")
	}

	status := application.LoadPageRankStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false after a failed run")
	}
	if !status.LastRunAt.Equal(successStatus.LastRunAt) || status.Documents != successStatus.Documents {
		t.Errorf("expected the last successful run's result preserved after a failure, got %+v want %+v", status, successStatus)
	}
}

func TestRunPageRankJobWithStatus_NilSettingsStoreIsANoop(t *testing.T) {
	repo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}}}
	if _, err := application.RunPageRankJobWithStatus(context.Background(), repo, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPageRankStatus_NilSettingsStoreReturnsZeroValue(t *testing.T) {
	status := application.LoadPageRankStatus(context.Background(), nil)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value, got %+v", status)
	}
}

func TestLoadPageRankStatus_StoreErrorReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.getErr = errors.New("db unavailable")
	status := application.LoadPageRankStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on a store error, got %+v", status)
	}
}

func TestLoadPageRankStatus_UndecodableValueReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.values[ports.SettingsKeyPageRankStatus] = "not json"
	status := application.LoadPageRankStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on undecodable stored JSON, got %+v", status)
	}
}
