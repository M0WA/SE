package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// A test-only override of application.EmbeddingRecomputeMinInterval (see
// its own doc comment) -- without this, every test in this file that
// processes more than a handful of documents through
// RunEmbeddingRecomputeJob's real per-document pacing would take
// multiple real seconds (the batching test alone processes over 100
// documents).
func init() {
	application.EmbeddingRecomputeMinInterval = time.Microsecond
}

// fakeEmbeddingRepo is a minimal in-memory ports.EmbeddingRepository --
// enough to exercise RunEmbeddingRecomputeJob without a real DB.
type fakeEmbeddingRepo struct {
	ids                []string
	docs               map[string]domain.Document
	allIDsErr          error
	documentsByIDsErr  error
	updateEmbeddingErr map[string]error // per-document UpdateEmbedding error
	updated            map[string][]float32
}

func (r *fakeEmbeddingRepo) AllDocumentIDs(context.Context) ([]string, error) {
	if r.allIDsErr != nil {
		return nil, r.allIDsErr
	}
	return r.ids, nil
}

func (r *fakeEmbeddingRepo) DocumentsByIDs(_ context.Context, ids []string) (map[string]domain.Document, error) {
	if r.documentsByIDsErr != nil {
		return nil, r.documentsByIDsErr
	}
	out := make(map[string]domain.Document)
	for _, id := range ids {
		if doc, ok := r.docs[id]; ok {
			out[id] = doc
		}
	}
	return out, nil
}

func (r *fakeEmbeddingRepo) UpdateEmbedding(_ context.Context, id string, vec []float32) error {
	if err, ok := r.updateEmbeddingErr[id]; ok {
		return err
	}
	if r.updated == nil {
		r.updated = make(map[string][]float32)
	}
	r.updated[id] = vec
	return nil
}

// fakeRecomputeEmbedder is a minimal in-memory ports.EmbeddingProvider --
// distinct from hybrid_search_service_test.go's own fakeEmbedder (already
// taken in this package), letting individual documents' Embed calls be
// forced to fail by text.
type fakeRecomputeEmbedder struct {
	errByText map[string]error
	// delay, when set, is slept inside Embed before returning -- used to
	// prove paceEmbedCall adds no extra wait when the real call already
	// took at least as long as the configured interval.
	delay time.Duration
}

func (e *fakeRecomputeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	if err, ok := e.errByText[text]; ok {
		return nil, err
	}
	return []float32{1, 2, 3}, nil
}

func (e *fakeRecomputeEmbedder) Dimensions() int { return 3 }

func TestRunEmbeddingRecomputeJob_RecomputesEveryDocument(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "hello"},
			"b": {ID: "b", Text: "world"},
		},
	}
	embedder := &fakeRecomputeEmbedder{}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 2 || result.Failed != 0 {
		t.Errorf("expected 2 documents recomputed, 0 failed, got %+v", result)
	}
	if len(repo.updated) != 2 {
		t.Errorf("expected both documents' embeddings written, got %+v", repo.updated)
	}
}

// TestRunEmbeddingRecomputeJob_BatchesAcrossMultipleFetches proves the
// corpus is walked in EmbeddingRecomputeBatchSize-sized chunks, not fetched
// all at once -- more IDs than one batch holds must still all get
// recomputed.
func TestRunEmbeddingRecomputeJob_BatchesAcrossMultipleFetches(t *testing.T) {
	total := application.EmbeddingRecomputeBatchSize*2 + 7
	ids := make([]string, 0, total)
	docs := make(map[string]domain.Document, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("doc-%d", i)
		ids = append(ids, id)
		docs[id] = domain.Document{ID: id, Text: id}
	}
	repo := &fakeEmbeddingRepo{ids: ids, docs: docs}
	embedder := &fakeRecomputeEmbedder{}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != total {
		t.Errorf("expected all %d documents recomputed across batches, got %d", total, result.Documents)
	}
}

// TestRunEmbeddingRecomputeJob_PerDocumentEmbedFailureIsCountedNotFatal
// proves one document's Embed error doesn't abort the whole run.
func TestRunEmbeddingRecomputeJob_PerDocumentEmbedFailureIsCountedNotFatal(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "good"},
			"b": {ID: "b", Text: "bad"},
		},
	}
	embedder := &fakeRecomputeEmbedder{errByText: map[string]error{"bad": errors.New("embed failed")}}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 1 {
		t.Errorf("expected 1 succeeded, 1 failed, got %+v", result)
	}
	if _, updated := repo.updated["b"]; updated {
		t.Error("expected the failed document's embedding to never be written")
	}
}

// TestRunEmbeddingRecomputeJob_PerDocumentUpdateFailureIsCountedNotFatal
// mirrors the Embed-failure case for a subsequent UpdateEmbedding failure.
func TestRunEmbeddingRecomputeJob_PerDocumentUpdateFailureIsCountedNotFatal(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "good"},
			"b": {ID: "b", Text: "bad"},
		},
		updateEmbeddingErr: map[string]error{"b": errors.New("write failed")},
	}
	embedder := &fakeRecomputeEmbedder{}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 1 {
		t.Errorf("expected 1 succeeded, 1 failed, got %+v", result)
	}
}

// TestRunEmbeddingRecomputeJob_SkipsDocumentDeletedBetweenListAndFetch
// proves an ID present in AllDocumentIDs but absent from a subsequent
// DocumentsByIDs batch (deleted in the meantime) is silently skipped, not
// treated as a failure.
func TestRunEmbeddingRecomputeJob_SkipsDocumentDeletedBetweenListAndFetch(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a", "gone"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "hello"}},
	}
	embedder := &fakeRecomputeEmbedder{}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 0 {
		t.Errorf("expected the deleted document to be silently skipped, got %+v", result)
	}
}

func TestRunEmbeddingRecomputeJob_EmptyCorpusIsANoop(t *testing.T) {
	repo := &fakeEmbeddingRepo{}
	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, &fakeRecomputeEmbedder{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 0 || result.Failed != 0 {
		t.Errorf("expected a zero-value result for an empty corpus, got %+v", result)
	}
}

// TestRunEmbeddingRecomputeJob_PacesEmbedCalls proves the job actually
// paces its Embed calls (see application.EmbeddingRecomputeMinInterval)
// rather than firing them back-to-back -- the whole point being to
// respect a real embeddings provider's rate limit (see
// docs.ionos.com/cloud/ai/ai-model-hub/how-tos/rate-limits) instead of
// flooding it.
func TestRunEmbeddingRecomputeJob_PacesEmbedCalls(t *testing.T) {
	original := application.EmbeddingRecomputeMinInterval
	application.EmbeddingRecomputeMinInterval = 50 * time.Millisecond
	defer func() { application.EmbeddingRecomputeMinInterval = original }()

	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b", "c"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "x"}, "b": {ID: "b", Text: "y"}, "c": {ID: "c", Text: "z"},
		},
	}
	start := time.Now()
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, &fakeRecomputeEmbedder{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 documents, each paced to at least 50ms (the fake embedder returns
	// near-instantly, so nearly all of that is spent waiting) -- expect
	// close to 3 full intervals' worth of total wait, with slack for
	// scheduling jitter.
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("expected pacing to add at least ~100ms across 3 documents, took %v", elapsed)
	}
}

// TestRunEmbeddingRecomputeJob_NoExtraWaitWhenEmbedAlreadySlow proves
// pacing adds no meaningful extra delay when a real Embed call already
// took at least as long as the configured interval -- pacing should never
// make an already-slow provider slower.
func TestRunEmbeddingRecomputeJob_NoExtraWaitWhenEmbedAlreadySlow(t *testing.T) {
	original := application.EmbeddingRecomputeMinInterval
	application.EmbeddingRecomputeMinInterval = 10 * time.Millisecond
	defer func() { application.EmbeddingRecomputeMinInterval = original }()

	repo := &fakeEmbeddingRepo{ids: []string{"a"}, docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}}}
	slowEmbedder := &fakeRecomputeEmbedder{delay: 100 * time.Millisecond}
	start := time.Now()
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, slowEmbedder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("expected no meaningful extra pacing wait when Embed already took longer than the interval, took %v", elapsed)
	}
}

func TestRunEmbeddingRecomputeJob_PropagatesAllDocumentIDsError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeEmbeddingRepo{allIDsErr: wantErr}
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, &fakeRecomputeEmbedder{}); !errors.Is(err, wantErr) {
		t.Errorf("expected AllDocumentIDs error to propagate, got %v", err)
	}
}

func TestRunEmbeddingRecomputeJob_PropagatesDocumentsByIDsError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, documentsByIDsErr: wantErr}
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, &fakeRecomputeEmbedder{}); !errors.Is(err, wantErr) {
		t.Errorf("expected DocumentsByIDs error to propagate, got %v", err)
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_RecordsCompletedRun(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a", "b"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}, "b": {ID: "b", Text: "y"}},
	}
	settings := newFakeSettingsStore()

	result, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, &fakeRecomputeEmbedder{}, settings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false once the run has finished")
	}
	if status.LastRunAt.IsZero() {
		t.Error("expected LastRunAt to be set")
	}
	if status.Documents != result.Documents || status.Failed != result.Failed {
		t.Errorf("expected persisted status to match the run result, got %+v want %+v", status, result)
	}
}

// TestRunEmbeddingRecomputeJobWithStatus_SetsInProgressBeforeRunning
// mirrors TestRunPageRankJobWithStatus_SetsInProgressBeforeRunning.
func TestRunEmbeddingRecomputeJobWithStatus_SetsInProgressBeforeRunning(t *testing.T) {
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}}}
	settings := newFakeSettingsStore()

	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, &fakeRecomputeEmbedder{}, settings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(settings.saveCalls) < 2 {
		t.Fatalf("expected at least 2 saves (in-progress, then completed), got %d", len(settings.saveCalls))
	}
	var firstSave domain.EmbeddingRecomputeStatus
	if err := json.Unmarshal([]byte(settings.saveCalls[0]), &firstSave); err != nil {
		t.Fatalf("decoding first save: %v", err)
	}
	if !firstSave.InProgress {
		t.Error("expected the first persisted status to have InProgress true")
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_ErrorClearsInProgressButKeepsLastResult(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}},
	}
	settings := newFakeSettingsStore()
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, &fakeRecomputeEmbedder{}, settings); err != nil {
		t.Fatalf("unexpected error on first (successful) run: %v", err)
	}
	successStatus := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)

	repo.allIDsErr = errors.New("db unavailable")
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, &fakeRecomputeEmbedder{}, settings); err == nil {
		t.Fatal("expected the second run's error to propagate")
	}

	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false after a failed run")
	}
	if !status.LastRunAt.Equal(successStatus.LastRunAt) || status.Documents != successStatus.Documents {
		t.Errorf("expected the last successful run's result preserved after a failure, got %+v want %+v", status, successStatus)
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_NilSettingsStoreIsANoop(t *testing.T) {
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}}}
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, &fakeRecomputeEmbedder{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadEmbeddingRecomputeStatus_NilSettingsStoreReturnsZeroValue(t *testing.T) {
	status := application.LoadEmbeddingRecomputeStatus(context.Background(), nil)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value, got %+v", status)
	}
}

func TestLoadEmbeddingRecomputeStatus_StoreErrorReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.getErr = errors.New("db unavailable")
	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on a store error, got %+v", status)
	}
}

func TestLoadEmbeddingRecomputeStatus_UndecodableValueReturnsZeroValue(t *testing.T) {
	settings := newFakeSettingsStore()
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = "not json"
	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress || !status.LastRunAt.IsZero() {
		t.Errorf("expected the zero value on undecodable stored JSON, got %+v", status)
	}
}

// TestResetStaleEmbeddingRecomputeStatus_ClearsStuckInProgress proves the
// exact scenario this exists for: a previous process instance was killed
// mid-run (crash, restart, redeploy) and never got to write its own
// InProgress=false, leaving the flag stuck -- ResetStaleEmbeddingRecomputeStatus
// must clear it without touching the last real completed run's own
// Documents/Failed/DurationMs/LastRunAt.
func TestResetStaleEmbeddingRecomputeStatus_ClearsStuckInProgress(t *testing.T) {
	settings := newFakeSettingsStore()
	stuck := domain.EmbeddingRecomputeStatus{
		InProgress: true,
		LastRunAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Documents:  10,
		Failed:     2,
		DurationMs: 500,
	}
	data, _ := json.Marshal(stuck)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)

	if reset := application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings); !reset {
		t.Error("expected reset=true for a stuck in-progress status")
	}

	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress cleared to false")
	}
	if !status.LastRunAt.Equal(stuck.LastRunAt) || status.Documents != stuck.Documents || status.Failed != stuck.Failed || status.DurationMs != stuck.DurationMs {
		t.Errorf("expected the last completed run's own fields preserved, got %+v", status)
	}
}

func TestResetStaleEmbeddingRecomputeStatus_NotInProgressIsANoop(t *testing.T) {
	settings := newFakeSettingsStore()
	done := domain.EmbeddingRecomputeStatus{InProgress: false, Documents: 5}
	data, _ := json.Marshal(done)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)
	settings.saveCalls = nil

	if reset := application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings); reset {
		t.Error("expected reset=false when nothing is in progress")
	}
	if len(settings.saveCalls) != 0 {
		t.Errorf("expected no save when there's nothing to reset, got %d saves", len(settings.saveCalls))
	}
}

func TestResetStaleEmbeddingRecomputeStatus_NoStatusYetIsANoop(t *testing.T) {
	settings := newFakeSettingsStore()
	if reset := application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings); reset {
		t.Error("expected reset=false when no status has ever been saved")
	}
}

func TestResetStaleEmbeddingRecomputeStatus_NilSettingsStoreIsANoop(t *testing.T) {
	if reset := application.ResetStaleEmbeddingRecomputeStatus(context.Background(), nil); reset {
		t.Error("expected reset=false for a nil settings store")
	}
}
