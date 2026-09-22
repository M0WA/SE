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

// noTitleWeight is passed to RunEmbeddingRecomputeJob(WithStatus) by every
// test that isn't specifically exercising title/body blending -- 0
// disables it entirely (see embedTitleWeighted's doc comment), reproducing
// this job's old body-only Embed behavior exactly.
const noTitleWeight = 0

// fakeEmbeddingRepo is a minimal in-memory ports.EmbeddingRepository --
// enough to exercise RunEmbeddingRecomputeJob without a real DB.
type fakeEmbeddingRepo struct {
	ids                []string
	docs               map[string]domain.Document
	allIDsErr          error
	documentsByIDsErr  error
	updateEmbeddingErr map[string]error // per-document UpdateEmbedding error
	updated            map[string][]float32
	// updatedEmbeddings records the full per-provider map UpdateEmbedding
	// was called with, for tests that configure more than one enabled
	// provider and need to assert on more than just the hash provider's
	// vector (updated above only ever records that one, for every
	// existing single-provider test's convenience).
	updatedEmbeddings map[string]map[string][]float32
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

func (r *fakeEmbeddingRepo) UpdateEmbedding(_ context.Context, id string, embeddings map[string][]float32) error {
	if err, ok := r.updateEmbeddingErr[id]; ok {
		return err
	}
	if r.updated == nil {
		r.updated = make(map[string][]float32)
	}
	if r.updatedEmbeddings == nil {
		r.updatedEmbeddings = make(map[string]map[string][]float32)
	}
	r.updatedEmbeddings[id] = embeddings
	// Every test in this file configures exactly one enabled provider
	// (hash) unless it says otherwise, so recording that one provider's
	// vector preserves every existing single-vector assertion unchanged.
	r.updated[id] = embeddings[domain.EmbeddingProviderHash]
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 0 {
		t.Errorf("expected the deleted document to be silently skipped, got %+v", result)
	}
}

func TestRunEmbeddingRecomputeJob_EmptyCorpusIsANoop(t *testing.T) {
	repo := &fakeEmbeddingRepo{}
	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight)
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
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight); !errors.Is(err, wantErr) {
		t.Errorf("expected AllDocumentIDs error to propagate, got %v", err)
	}
}

func TestRunEmbeddingRecomputeJob_PropagatesDocumentsByIDsError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, documentsByIDsErr: wantErr}
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight); !errors.Is(err, wantErr) {
		t.Errorf("expected DocumentsByIDs error to propagate, got %v", err)
	}
}

// textAwareRecomputeEmbedder returns a distinct vector per exact input
// text, via a caller-supplied map -- unlike fakeRecomputeEmbedder's single
// fixed vector, this lets a test tell a title Embed call apart from a body
// one, to verify a weighted title/body combination actually reaches
// UpdateEmbedding rather than just checking a pass-through value both
// calls would return identically.
type textAwareRecomputeEmbedder struct {
	vecByText map[string][]float32
}

func (e *textAwareRecomputeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return e.vecByText[text], nil
}
func (e *textAwareRecomputeEmbedder) Dimensions() int { return 2 }

// TestRunEmbeddingRecomputeJob_BlendsTitleAndBodyWhenWeightConfigured
// proves titleWeight actually reaches embedTitleWeighted here too -- the
// same mechanism sqlCrawlerService.Crawl uses at crawl time -- so a
// recompute reproduces exactly what a fresh crawl would now embed, rather
// than the old body-only recompute that silently dropped every
// document's title.
func TestRunEmbeddingRecomputeJob_BlendsTitleAndBodyWhenWeightConfigured(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a"},
		docs: map[string]domain.Document{"a": {ID: "a", Title: "Title text", Text: "Body text"}},
	}
	embedder := &textAwareRecomputeEmbedder{vecByText: map[string][]float32{
		"Title text": {1, 0},
		"Body text":  {0, 1},
	}}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, 0.25)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 0 {
		t.Fatalf("expected 1 document recomputed, got %+v", result)
	}
	// 0.25*{1,0} + 0.75*{0,1} = {0.25, 0.75}
	got := repo.updated["a"]
	if got[0] < 0.24 || got[0] > 0.26 || got[1] < 0.74 || got[1] > 0.76 {
		t.Errorf("expected the weighted title/body combination ~{0.25, 0.75}, got %v", got)
	}
}

// TestRunEmbeddingRecomputeJob_RecomputesEveryEnabledProviderNotJustOne
// proves a recompute refreshes EVERY currently-enabled provider's vector
// per document, not just whichever is active for search -- so switching
// which one is active never needs a second recompute, because both were
// already brought current by this one run.
func TestRunEmbeddingRecomputeJob_RecomputesEveryEnabledProviderNotJustOne(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "hello"}},
	}
	embedders := map[string]ports.EmbeddingProvider{
		domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{},
		"http":                       &textAwareRecomputeEmbedder{vecByText: map[string][]float32{"hello": {9, 9, 9, 9}}},
	}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedders, noTitleWeight)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 0 {
		t.Fatalf("expected 1 document recomputed, got %+v", result)
	}
	saved := repo.updatedEmbeddings["a"]
	if got := saved[domain.EmbeddingProviderHash]; len(got) != 3 {
		t.Errorf("expected the hash provider's own vector recomputed, got %v", got)
	}
	if got := saved["http"]; len(got) != 4 || got[0] != 9 {
		t.Errorf("expected the http provider's own (differently-shaped) vector recomputed too, got %v", got)
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_RecordsCompletedRun(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a", "b"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}, "b": {ID: "b", Text: "y"}},
	}
	settings := newFakeSettingsStore()

	result, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight)
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

	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight); err != nil {
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
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight); err != nil {
		t.Fatalf("unexpected error on first (successful) run: %v", err)
	}
	successStatus := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)

	repo.allIDsErr = errors.New("db unavailable")
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight); err == nil {
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
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, nil, noTitleWeight); err != nil {
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

	if !application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings) {
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

	if application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings) {
		t.Error("expected reset=false when nothing is in progress")
	}
	if len(settings.saveCalls) != 0 {
		t.Errorf("expected no save when there's nothing to reset, got %d saves", len(settings.saveCalls))
	}
}

func TestResetStaleEmbeddingRecomputeStatus_NoStatusYetIsANoop(t *testing.T) {
	settings := newFakeSettingsStore()
	if application.ResetStaleEmbeddingRecomputeStatus(context.Background(), settings) {
		t.Error("expected reset=false when no status has ever been saved")
	}
}

func TestResetStaleEmbeddingRecomputeStatus_NilSettingsStoreIsANoop(t *testing.T) {
	if application.ResetStaleEmbeddingRecomputeStatus(context.Background(), nil) {
		t.Error("expected reset=false for a nil settings store")
	}
}
