package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

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
}

func (e *fakeRecomputeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
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
