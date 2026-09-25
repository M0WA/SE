package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// noTitleWeight is passed to RunEmbeddingRecomputeJob(WithStatus) by every
// test that isn't specifically exercising title/body blending -- 0
// disables it entirely (see EmbedTitleWeighted's doc comment), reproducing
// this job's old body-only Embed behavior exactly.
const noTitleWeight = 0

// fakeEmbeddingRepo is a minimal in-memory ports.EmbeddingRepository --
// enough to exercise RunEmbeddingRecomputeJob without a real DB. mu guards
// every field UpdateEmbedding/DocumentIDsAfter mutate, since
// RunEmbeddingRecomputeJob now calls UpdateEmbedding from concurrent
// goroutines whenever concurrency > 1 -- unsynchronized access here would
// be a genuine data race, not just a hypothetical one (caught for the
// identical fakeSettingsStore fixture via go test -race).
type fakeEmbeddingRepo struct {
	mu                 sync.Mutex
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
	// docIDsAfterErr drives DocumentIDsAfter's error path; docIDsAfterCalls
	// records every afterID it was called with, so a resume test can
	// assert the right checkpoint was actually used as the cursor.
	docIDsAfterErr   error
	docIDsAfterCalls []string
}

func (r *fakeEmbeddingRepo) AllDocumentIDs(context.Context) ([]string, error) {
	if r.allIDsErr != nil {
		return nil, r.allIDsErr
	}
	return r.ids, nil
}

// DocumentIDsAfter mimics the real "WHERE id > afterID ORDER BY id" query
// against r.ids, which every test here already keeps in sorted order.
func (r *fakeEmbeddingRepo) DocumentIDsAfter(_ context.Context, afterID string) ([]string, error) {
	r.mu.Lock()
	r.docIDsAfterCalls = append(r.docIDsAfterCalls, afterID)
	r.mu.Unlock()
	if r.docIDsAfterErr != nil {
		return nil, r.docIDsAfterErr
	}
	var out []string
	for _, id := range r.ids {
		if id > afterID {
			out = append(out, id)
		}
	}
	return out, nil
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
	r.mu.Lock()
	defer r.mu.Unlock()
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil, nil)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != total {
		t.Errorf("expected all %d documents recomputed across batches, got %d", total, result.Documents)
	}
}

// TestRunEmbeddingRecomputeJob_CallsOnBatchDoneWithRunningTotalsAndLastID
// proves the checkpoint callback fires once per batch (not once per
// document, not just once at the end), with the batch's own last ID and
// the CUMULATIVE totals so far -- exactly what RunEmbeddingRecomputeJobWithStatus
// needs to persist a resumable, live-progress checkpoint.
func TestRunEmbeddingRecomputeJob_CallsOnBatchDoneWithRunningTotalsAndLastID(t *testing.T) {
	total := application.EmbeddingRecomputeBatchSize*2 + 3
	ids := make([]string, 0, total)
	docs := make(map[string]domain.Document, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("doc-%03d", i)
		ids = append(ids, id)
		docs[id] = domain.Document{ID: id, Text: id}
	}
	repo := &fakeEmbeddingRepo{ids: ids, docs: docs}
	embedder := &fakeRecomputeEmbedder{}

	type call struct {
		lastID            string
		documents, failed int
	}
	var calls []call
	_, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil,
		func(lastID string, documents, failed int) {
			calls = append(calls, call{lastID, documents, failed})
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 batch callbacks (2 full + 1 partial), got %d: %+v", len(calls), calls)
	}
	if calls[0].lastID != ids[application.EmbeddingRecomputeBatchSize-1] || calls[0].documents != application.EmbeddingRecomputeBatchSize {
		t.Errorf("expected the first callback to report the first batch's own last id and count, got %+v", calls[0])
	}
	if calls[2].lastID != ids[total-1] || calls[2].documents != total {
		t.Errorf("expected the final callback to report the true last id and cumulative total, got %+v", calls[2])
	}
}

// TestRunEmbeddingRecomputeJob_ResumeFromIDUsesDocumentIDsAfterNotAllDocumentIDs
// proves a non-empty resumeFromID switches the ID source to
// DocumentIDsAfter(resumeFromID) -- so a resumed run only re-embeds what a
// previous, interrupted run hadn't reached yet -- rather than AllDocumentIDs,
// which would redo the whole corpus.
func TestRunEmbeddingRecomputeJob_ResumeFromIDUsesDocumentIDsAfterNotAllDocumentIDs(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b", "c"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "a"}, "b": {ID: "b", Text: "b"}, "c": {ID: "c", Text: "c"},
		},
	}
	embedder := &fakeRecomputeEmbedder{}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "b", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.docIDsAfterCalls) != 1 || repo.docIDsAfterCalls[0] != "b" {
		t.Fatalf("expected exactly one DocumentIDsAfter call with cursor %q, got %v", "b", repo.docIDsAfterCalls)
	}
	if result.Documents != 1 {
		t.Errorf("expected only 'c' (sorting after 'b') recomputed, got %+v", result)
	}
	if _, done := repo.updated["a"]; done {
		t.Error("expected 'a' (already before the resume cursor) left untouched")
	}
	if _, done := repo.updated["b"]; done {
		t.Error("expected 'b' (the checkpoint itself, already done) left untouched")
	}
}

func TestRunEmbeddingRecomputeJob_PropagatesDocumentIDsAfterError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeEmbeddingRepo{docIDsAfterErr: wantErr}
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight, "some-id", nil, nil); !errors.Is(err, wantErr) {
		t.Errorf("expected DocumentIDsAfter error to propagate, got %v", err)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil, nil)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil, nil)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 1 || result.Failed != 0 {
		t.Errorf("expected the deleted document to be silently skipped, got %+v", result)
	}
}

func TestRunEmbeddingRecomputeJob_EmptyCorpusIsANoop(t *testing.T) {
	repo := &fakeEmbeddingRepo{}
	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight, "", nil, nil)
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
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight, "", nil, nil); !errors.Is(err, wantErr) {
		t.Errorf("expected AllDocumentIDs error to propagate, got %v", err)
	}
}

func TestRunEmbeddingRecomputeJob_PropagatesDocumentsByIDsError(t *testing.T) {
	wantErr := errors.New("db unavailable")
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, documentsByIDsErr: wantErr}
	if _, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, noTitleWeight, "", nil, nil); !errors.Is(err, wantErr) {
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
// proves titleWeight actually reaches EmbedTitleWeighted here too -- the
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, 0.25, "", nil, nil)
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

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, embedders, noTitleWeight, "", nil, nil)
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

// concurrencyTrackingEmbedder records the peak number of simultaneous
// in-flight Embed calls it ever saw -- proves RunEmbeddingRecomputeJob's
// semaphore actually bounds real parallelism rather than just accepting a
// concurrency value it ignores. delay is generous relative to goroutine
// launch overhead so the peak reliably reaches the true concurrency limit
// before any call completes.
type concurrencyTrackingEmbedder struct {
	current int32
	peak    int32
	delay   time.Duration
}

func (e *concurrencyTrackingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	n := atomic.AddInt32(&e.current, 1)
	for {
		p := atomic.LoadInt32(&e.peak)
		if n <= p || atomic.CompareAndSwapInt32(&e.peak, p, n) {
			break
		}
	}
	time.Sleep(e.delay)
	atomic.AddInt32(&e.current, -1)
	return []float32{1, 2, 3}, nil
}

func (e *concurrencyTrackingEmbedder) Dimensions() int { return 3 }

// TestRunEmbeddingRecomputeJob_ConcurrencyLimitsSimultaneousEmbedCalls is
// the direct proof for "parallelize it, make it configurable": with
// concurrency() returning 3 and more documents than that in a batch, at
// most 3 Embed calls are ever in flight at once -- and, given enough
// documents relative to the semaphore size, real overlap actually happens
// (peak > 1), not just a false sense of parallelism that never overlaps.
func TestRunEmbeddingRecomputeJob_ConcurrencyLimitsSimultaneousEmbedCalls(t *testing.T) {
	total := 6
	ids := make([]string, 0, total)
	docs := make(map[string]domain.Document, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("doc-%d", i)
		ids = append(ids, id)
		docs[id] = domain.Document{ID: id, Text: id}
	}
	repo := &fakeEmbeddingRepo{ids: ids, docs: docs}
	embedder := &concurrencyTrackingEmbedder{delay: 50 * time.Millisecond}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", func() int { return 3 }, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != total {
		t.Fatalf("expected all %d documents recomputed, got %+v", total, result)
	}
	if peak := atomic.LoadInt32(&embedder.peak); peak != 3 {
		t.Errorf("expected peak simultaneous Embed calls to reach exactly the configured concurrency (3), got %d", peak)
	}
}

// TestRunEmbeddingRecomputeJob_NilOrNonPositiveConcurrencyStaysSequential
// proves the "make it configurable" default is safe: a nil concurrency
// func, or one returning <= 0, processes one document at a time, same as
// before this feature existed.
func TestRunEmbeddingRecomputeJob_NilOrNonPositiveConcurrencyStaysSequential(t *testing.T) {
	for name, concurrency := range map[string]func() int{
		"nil":      nil,
		"zero":     func() int { return 0 },
		"negative": func() int { return -1 },
	} {
		t.Run(name, func(t *testing.T) {
			repo := &fakeEmbeddingRepo{
				ids: []string{"a", "b", "c"},
				docs: map[string]domain.Document{
					"a": {ID: "a", Text: "a"}, "b": {ID: "b", Text: "b"}, "c": {ID: "c", Text: "c"},
				},
			}
			embedder := &concurrencyTrackingEmbedder{delay: 20 * time.Millisecond}

			result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", concurrency, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Documents != 3 {
				t.Fatalf("expected all 3 documents recomputed, got %+v", result)
			}
			if peak := atomic.LoadInt32(&embedder.peak); peak != 1 {
				t.Errorf("expected fully sequential processing (peak=1), got %d", peak)
			}
		})
	}
}

// TestRunEmbeddingRecomputeJob_ConcurrencyIsReReadPerBatchNotCapturedOnce
// is the direct proof for "setting should apply live": concurrency() is
// called fresh at the start of every batch, so a value that changes
// between batches (as it would if an admin edits the setting mid-run)
// takes effect on the very next batch of the SAME run, not only on a
// future run.
func TestRunEmbeddingRecomputeJob_ConcurrencyIsReReadPerBatchNotCapturedOnce(t *testing.T) {
	total := application.EmbeddingRecomputeBatchSize + 5
	ids := make([]string, 0, total)
	docs := make(map[string]domain.Document, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("doc-%03d", i)
		ids = append(ids, id)
		docs[id] = domain.Document{ID: id, Text: id}
	}
	repo := &fakeEmbeddingRepo{ids: ids, docs: docs}
	embedder := &concurrencyTrackingEmbedder{delay: 20 * time.Millisecond}

	var calls int32
	concurrency := func() int {
		// First batch (the corpus' first EmbeddingRecomputeBatchSize
		// documents) sees concurrency 1; the second, partial batch sees 5 --
		// proving the value used for a batch is whatever concurrency()
		// returns AT THAT BATCH's START, not whatever it returned once at
		// job start.
		if atomic.AddInt32(&calls, 1) == 1 {
			return 1
		}
		return 5
	}

	result, err := application.RunEmbeddingRecomputeJob(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder}, noTitleWeight, "", concurrency, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != total {
		t.Fatalf("expected all %d documents recomputed, got %+v", total, result)
	}
	if calls != 2 {
		t.Fatalf("expected concurrency() called exactly once per batch (2 batches), got %d calls", calls)
	}
	if peak := atomic.LoadInt32(&embedder.peak); peak != 5 {
		t.Errorf("expected the second batch's higher concurrency (5) to actually take effect, got peak %d", peak)
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_RecordsCompletedRun(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a", "b"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}, "b": {ID: "b", Text: "y"}},
	}
	settings := newFakeSettingsStore()

	result, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil)
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
	if status.LastDocID != "" {
		t.Errorf("expected LastDocID cleared once the run completes cleanly, got %q", status.LastDocID)
	}
}

// TestRunEmbeddingRecomputeJobWithStatus_SetsInProgressBeforeRunning
// mirrors TestRunPageRankJobWithStatus_SetsInProgressBeforeRunning.
func TestRunEmbeddingRecomputeJobWithStatus_SetsInProgressBeforeRunning(t *testing.T) {
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}}}
	settings := newFakeSettingsStore()

	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil); err != nil {
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

// TestRunEmbeddingRecomputeJobWithStatus_PersistsLiveProgressPerBatch
// proves the whole point of this feature: an admin polling GET
// /admin/api/embeddings/recompute mid-run sees real, moving Documents/
// LastDocID counts checkpointed after every batch, not just 0 until the
// entire (possibly hours-long) run finishes.
func TestRunEmbeddingRecomputeJobWithStatus_PersistsLiveProgressPerBatch(t *testing.T) {
	total := application.EmbeddingRecomputeBatchSize + 5
	ids := make([]string, 0, total)
	docs := make(map[string]domain.Document, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("doc-%03d", i)
		ids = append(ids, id)
		docs[id] = domain.Document{ID: id, Text: id}
	}
	repo := &fakeEmbeddingRepo{ids: ids, docs: docs}
	settings := newFakeSettingsStore()

	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// saveCalls[0] is the initial InProgress=true write; saveCalls[1] is
	// the first batch's checkpoint (still in progress, partial count);
	// the final save is the completed-run summary.
	if len(settings.saveCalls) < 3 {
		t.Fatalf("expected at least 3 saves (start, one batch checkpoint, completion), got %d", len(settings.saveCalls))
	}
	var firstCheckpoint domain.EmbeddingRecomputeStatus
	if err := json.Unmarshal([]byte(settings.saveCalls[1]), &firstCheckpoint); err != nil {
		t.Fatalf("decoding first batch checkpoint: %v", err)
	}
	if !firstCheckpoint.InProgress {
		t.Error("expected the mid-run checkpoint to still show InProgress true")
	}
	if firstCheckpoint.Documents != application.EmbeddingRecomputeBatchSize {
		t.Errorf("expected the first checkpoint's live Documents count to be the first batch's size (%d), got %d", application.EmbeddingRecomputeBatchSize, firstCheckpoint.Documents)
	}
	if firstCheckpoint.LastDocID != ids[application.EmbeddingRecomputeBatchSize-1] {
		t.Errorf("expected the first checkpoint's LastDocID to be the first batch's own last id, got %q", firstCheckpoint.LastDocID)
	}
}

// TestRunEmbeddingRecomputeJobWithStatus_ResumesFromPersistedCheckpoint is
// the direct regression test for the real incident this feature fixes: a
// process killed mid-recompute previously lost all progress, forcing a
// full restart from document 1 every time. This proves a non-empty
// resumeFromID both (a) only re-embeds documents after the checkpoint and
// (b) reports the FULL cumulative Documents count (checkpoint's own prior
// progress plus this resumed pass), not just what this pass itself did.
func TestRunEmbeddingRecomputeJobWithStatus_ResumesFromPersistedCheckpoint(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b", "c"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "a"}, "b": {ID: "b", Text: "b"}, "c": {ID: "c", Text: "c"},
		},
	}
	settings := newFakeSettingsStore()
	// Simulates a previous run interrupted right after finishing "a" --
	// exactly the persisted shape RunEmbeddingRecomputeJobWithStatus's own
	// checkpoint save would have left behind.
	interrupted := domain.EmbeddingRecomputeStatus{InProgress: true, LastDocID: "a", Documents: 1}
	data, _ := json.Marshal(interrupted)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)

	result, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "a", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Documents != 2 {
		t.Errorf("expected only 'b' and 'c' recomputed by this resumed pass, got %+v", result)
	}
	if _, done := repo.updated["a"]; done {
		t.Error("expected 'a' (already done before the interruption) not re-embedded")
	}

	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.InProgress {
		t.Error("expected InProgress false once the resumed run completes")
	}
	if status.LastDocID != "" {
		t.Errorf("expected LastDocID cleared once the resumed run completes, got %q", status.LastDocID)
	}
	if status.Documents != 3 {
		t.Errorf("expected the persisted status to report the FULL cumulative count (1 from before the interruption + 2 from this resumed pass) = 3, got %d", status.Documents)
	}
}

// TestRunEmbeddingRecomputeJobWithStatus_FreshTriggerDiscardsOldCheckpoint
// is the safety property that makes resuming trustworthy: a genuinely new,
// explicit trigger (resumeFromID "") must NEVER reuse a checkpoint left
// over from a previous, possibly differently-configured run -- doing so
// would silently skip re-embedding documents that actually need it (e.g.
// after a model/dimension change). A fresh trigger always starts at the
// true beginning and resets any stale Documents/Failed/LastDocID before
// it begins, even if one was still sitting in the persisted status.
func TestRunEmbeddingRecomputeJobWithStatus_FreshTriggerDiscardsOldCheckpoint(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a", "b"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "a"}, "b": {ID: "b", Text: "b"}},
	}
	settings := newFakeSettingsStore()
	stale := domain.EmbeddingRecomputeStatus{InProgress: true, LastDocID: "a", Documents: 999, Failed: 7}
	data, _ := json.Marshal(stale)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)

	result, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.docIDsAfterCalls) != 0 {
		t.Errorf("expected a fresh trigger to never call DocumentIDsAfter, got %v", repo.docIDsAfterCalls)
	}
	if result.Documents != 2 {
		t.Errorf("expected both 'a' and 'b' recomputed by a fresh trigger despite the stale checkpoint claiming 'a' was already done, got %+v", result)
	}
	status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
	if status.Documents != 2 || status.Failed != 0 {
		t.Errorf("expected the stale 999/7 counts fully replaced by this fresh run's own real result, got %+v", status)
	}
}

func TestRunEmbeddingRecomputeJobWithStatus_ErrorClearsInProgressButKeepsLastResult(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids:  []string{"a"},
		docs: map[string]domain.Document{"a": {ID: "a", Text: "x"}},
	}
	settings := newFakeSettingsStore()
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil); err != nil {
		t.Fatalf("unexpected error on first (successful) run: %v", err)
	}
	successStatus := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)

	repo.allIDsErr = errors.New("db unavailable")
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, "", nil); err == nil {
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
	if _, err := application.RunEmbeddingRecomputeJobWithStatus(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, nil, noTitleWeight, "", nil); err != nil {
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

// waitForEmbeddingRecomputeSettled polls settings until
// LoadEmbeddingRecomputeStatus reports InProgress false -- ResumeStaleEmbeddingRecomputeIfAny
// runs its resumed pass in a detached background goroutine, mirroring
// admin_test.go's identical waitForEmbeddingRecomputeDone for the HTTP
// handler's own background trigger.
func waitForEmbeddingRecomputeSettled(t *testing.T, settings ports.SettingsStore) domain.EmbeddingRecomputeStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := application.LoadEmbeddingRecomputeStatus(context.Background(), settings)
		if !status.InProgress {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the resumed embedding recompute to settle")
	return domain.EmbeddingRecomputeStatus{}
}

// TestResumeStaleEmbeddingRecomputeIfAny_ResumesFromCheckpoint proves the
// exact scenario this exists for: a previous process instance was killed
// mid-run (crash, restart, redeploy), leaving InProgress=true and a
// LastDocID checkpoint behind. The next instance's startup must resume
// that same run from the checkpoint in the background, not silently
// discard the progress the way the old ResetStaleEmbeddingRecomputeStatus did.
func TestResumeStaleEmbeddingRecomputeIfAny_ResumesFromCheckpoint(t *testing.T) {
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b", "c"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "a"}, "b": {ID: "b", Text: "b"}, "c": {ID: "c", Text: "c"},
		},
	}
	settings := newFakeSettingsStore()
	stuck := domain.EmbeddingRecomputeStatus{InProgress: true, LastDocID: "a", Documents: 1}
	data, _ := json.Marshal(stuck)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)

	started := application.ResumeStaleEmbeddingRecomputeIfAny(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, nil)
	if !started {
		t.Fatal("expected started=true for a stuck in-progress status")
	}

	status := waitForEmbeddingRecomputeSettled(t, settings)
	if status.Documents != 3 {
		t.Errorf("expected the resumed run to finish with the full cumulative count (1 already done + 2 resumed), got %+v", status)
	}
	if _, done := repo.updated["a"]; done {
		t.Error("expected 'a' (already done before the interruption) not re-embedded by the resume")
	}
	if _, done := repo.updated["b"]; !done {
		t.Error("expected 'b' recomputed by the resume")
	}
	if _, done := repo.updated["c"]; !done {
		t.Error("expected 'c' recomputed by the resume")
	}
}

// TestResumeStaleEmbeddingRecomputeIfAny_JobErrorIsLoggedNotFatal proves a
// resumed run's error is logged, not left to crash the detached background
// goroutine -- mirrors RunEmbeddingRecomputeJobWithStatus's own equivalent
// coverage for the foreground trigger path.
func TestResumeStaleEmbeddingRecomputeIfAny_JobErrorIsLoggedNotFatal(t *testing.T) {
	repo := &fakeEmbeddingRepo{docIDsAfterErr: errors.New("db unavailable")}
	settings := newFakeSettingsStore()
	stuck := domain.EmbeddingRecomputeStatus{InProgress: true, LastDocID: "a", Documents: 1}
	data, _ := json.Marshal(stuck)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)

	started := application.ResumeStaleEmbeddingRecomputeIfAny(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, nil)
	if !started {
		t.Fatal("expected started=true for a stuck in-progress status")
	}

	status := waitForEmbeddingRecomputeSettled(t, settings)
	if status.InProgress {
		t.Error("expected InProgress false once the failed resume settles")
	}
}

func TestResumeStaleEmbeddingRecomputeIfAny_NotInProgressIsANoop(t *testing.T) {
	settings := newFakeSettingsStore()
	done := domain.EmbeddingRecomputeStatus{InProgress: false, Documents: 5}
	data, _ := json.Marshal(done)
	settings.values[ports.SettingsKeyEmbeddingRecomputeStatus] = string(data)
	settings.saveCalls = nil

	repo := &fakeEmbeddingRepo{}
	if application.ResumeStaleEmbeddingRecomputeIfAny(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, nil) {
		t.Error("expected started=false when nothing is in progress")
	}
	if len(settings.saveCalls) != 0 {
		t.Errorf("expected no save when there's nothing to resume, got %d saves", len(settings.saveCalls))
	}
}

func TestResumeStaleEmbeddingRecomputeIfAny_NoStatusYetIsANoop(t *testing.T) {
	settings := newFakeSettingsStore()
	repo := &fakeEmbeddingRepo{}
	if application.ResumeStaleEmbeddingRecomputeIfAny(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, settings, noTitleWeight, nil) {
		t.Error("expected started=false when no status has ever been saved")
	}
}

func TestResumeStaleEmbeddingRecomputeIfAny_NilSettingsStoreIsANoop(t *testing.T) {
	repo := &fakeEmbeddingRepo{}
	if application.ResumeStaleEmbeddingRecomputeIfAny(context.Background(), repo, map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: &fakeRecomputeEmbedder{}}, nil, noTitleWeight, nil) {
		t.Error("expected started=false for a nil settings store")
	}
}
