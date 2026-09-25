package sqlrepo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
)

func TestRepository_CreateDocumentJobStartsQueued(t *testing.T) {
	repo := newTestRepo(t)
	job, err := repo.CreateDocumentJob(context.Background(), "notes.txt", "text/plain", 11, domain.DocumentJobSourceUpload, true, []byte("hello world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status != domain.DocumentJobQueued {
		t.Errorf("expected queued, got %s", job.Status)
	}
	if job.ID == "" {
		t.Error("expected a non-empty job ID")
	}
	if job.StartedAt != nil || job.FinishedAt != nil {
		t.Error("expected StartedAt/FinishedAt unset on a new job")
	}
	if job.Filename != "notes.txt" || job.ContentType != "text/plain" || job.Size != 11 || job.Source != domain.DocumentJobSourceUpload || !job.IndexVocabulary {
		t.Errorf("unexpected job fields: %+v", job)
	}
}

func TestRepository_DocumentJobDataRoundTripsThroughStorage(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, err := repo.CreateDocumentJob(ctx, "photo.png", "image/png", 3, domain.DocumentJobSourceS3, false, []byte{0x1, 0x2, 0x3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, contentType, err := repo.GetDocumentJobData(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "\x01\x02\x03" || contentType != "image/png" {
		t.Errorf("unexpected data/content type: %q %q", data, contentType)
	}
}

func TestRepository_DocumentJobMarkRunningSetsStartedAt(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	if err := repo.MarkDocumentJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.GetDocumentJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != domain.DocumentJobRunning {
		t.Errorf("expected running, got %s", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

func TestRepository_DocumentJobMarkDoneSetsFinishedAtAndDocID(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	_ = repo.MarkDocumentJobRunning(ctx, job.ID)
	if err := repo.MarkDocumentJobDone(ctx, job.ID, "doc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := repo.GetDocumentJob(ctx, job.ID)
	if got.Status != domain.DocumentJobDone {
		t.Errorf("expected done, got %s", got.Status)
	}
	if got.DocID != "doc-1" {
		t.Errorf("expected doc_id doc-1, got %q", got.DocID)
	}
	if got.FinishedAt == nil {
		t.Error("expected FinishedAt to be set")
	}
}

func TestRepository_DocumentJobMarkFailedRecordsError(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	_ = repo.MarkDocumentJobRunning(ctx, job.ID)
	if err := repo.MarkDocumentJobFailed(ctx, job.ID, errors.New("disk full")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := repo.GetDocumentJob(ctx, job.ID)
	if got.Status != domain.DocumentJobFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
	if got.Error != "disk full" {
		t.Errorf("expected error message recorded, got %q", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("expected FinishedAt to be set on failure too")
	}
}

func TestRepository_GetDocumentJobUnknownIDReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.GetDocumentJob(context.Background(), "does-not-exist"); !errors.Is(err, domain.ErrDocumentJobNotFound) {
		t.Errorf("expected ErrDocumentJobNotFound, got %v", err)
	}
}

func TestRepository_GetDocumentJobDataUnknownIDReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	if _, _, err := repo.GetDocumentJobData(context.Background(), "does-not-exist"); !errors.Is(err, domain.ErrDocumentJobNotFound) {
		t.Errorf("expected ErrDocumentJobNotFound, got %v", err)
	}
}

func TestRepository_ListDocumentJobsReturnsMostRecentFirst(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	a, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	time.Sleep(2 * time.Millisecond) // ensure a distinct created_at ordering
	b, _ := repo.CreateDocumentJob(ctx, "b.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("b"))

	list, err := repo.ListDocumentJobs(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(list))
	}
	if list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("expected most-recently-created first, got %s then %s", list[0].ID, list[1].ID)
	}
}

func TestRepository_ListDocumentJobsEmpty(t *testing.T) {
	repo := newTestRepo(t)
	list, err := repo.ListDocumentJobs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected an empty (not nil-panicking) slice, got %+v", list)
	}
}

func TestRepository_DeleteDocumentJobRemovesTheJobAndItsData(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	if err := repo.DeleteDocumentJob(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.GetDocumentJob(ctx, job.ID); !errors.Is(err, domain.ErrDocumentJobNotFound) {
		t.Errorf("expected the job to be gone, got %v", err)
	}
}

// TestRepository_DeleteDocumentJobCascadesToItsIndexedDocument proves
// DeleteDocumentJob also removes the resulting documents row once a job
// has DocID set -- not just its own document_jobs row.
func TestRepository_DeleteDocumentJobCascadesToItsIndexedDocument(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	doc := domain.Document{ID: "doc-" + job.ID, URL: "upload://" + job.ID, Title: job.ID, Text: "hello"}
	if err := repo.SaveDocumentOptionalVocabulary(ctx, doc, nil, 3, 2, true); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}
	if err := repo.MarkDocumentJobDone(ctx, job.ID, doc.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteDocumentJob(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.DocumentsByIDs(ctx, []string{doc.ID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected the indexed document to be deleted too, still found: %+v", docs)
	}
}

// TestRepository_DeleteDocumentJobToleratesAnAlreadyDeletedDocument proves
// deleting a job whose indexed document was already removed independently
// (e.g. a separate admin delete) still succeeds instead of erroring.
func TestRepository_DeleteDocumentJobToleratesAnAlreadyDeletedDocument(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.CreateDocumentJob(ctx, "a.txt", "text/plain", 1, domain.DocumentJobSourceUpload, true, []byte("a"))
	if err := repo.MarkDocumentJobDone(ctx, job.ID, "doc-never-actually-saved"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.DeleteDocumentJob(ctx, job.ID); err != nil {
		t.Fatalf("expected the already-missing document to be tolerated, got: %v", err)
	}
}

func TestRepository_DeleteDocumentJobUnknownIDReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.DeleteDocumentJob(context.Background(), "does-not-exist"); !errors.Is(err, domain.ErrDocumentJobNotFound) {
		t.Errorf("expected ErrDocumentJobNotFound, got %v", err)
	}
}

// TestRepository_SaveDocumentOptionalVocabularySkipsPostingsWhenFalse
// proves the indexVocabulary=false path actually skips BM25 postings,
// while still indexing everything else (embeddings, dedup fingerprints).
func TestRepository_SaveDocumentOptionalVocabularySkipsPostingsWhenFalse(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	doc := domain.Document{ID: "doc-1", URL: "upload://doc-1", Title: "t", Text: "unique searchable term"}
	if err := repo.SaveDocumentOptionalVocabulary(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {0.1, 0.2}}, 3, 2, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	postings, err := repo.PostingsForTerms(ctx, []string{"unique", "searchable", "term"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for term, stats := range postings {
		if len(stats) != 0 {
			t.Errorf("expected no postings for %q when indexVocabulary=false, got %+v", term, stats)
		}
	}
	docs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"})
	if err != nil || len(docs) != 1 {
		t.Fatalf("expected the document itself to still be saved: docs=%+v err=%v", docs, err)
	}
}

// TestRepository_SaveDocumentOptionalVocabularyTrueStillIndexesPostings
// proves the true case behaves exactly like SaveDocument's own default.
func TestRepository_SaveDocumentOptionalVocabularyTrueStillIndexesPostings(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	doc := domain.Document{ID: "doc-2", URL: "upload://doc-2", Title: "t", Text: "findable keyword"}
	if err := repo.SaveDocumentOptionalVocabulary(ctx, doc, nil, 3, 2, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	postings, err := repo.PostingsForTerms(ctx, []string{"findable"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings["findable"]) != 1 {
		t.Errorf("expected a posting for 'findable', got %+v", postings["findable"])
	}
}

// TestRepository_SaveDocumentOptionalVocabularyFalseClearsPriorPostings
// proves re-saving the same doc ID with indexVocabulary now false clears
// postings a previous save (or SaveDocument itself) left behind.
func TestRepository_SaveDocumentOptionalVocabularyFalseClearsPriorPostings(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	doc := domain.Document{ID: "doc-3", URL: "upload://doc-3", Title: "t", Text: "reindexed"}
	if err := repo.SaveDocument(ctx, doc, nil, 3, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postings, _ := repo.PostingsForTerms(ctx, []string{"reindexed"}); len(postings["reindexed"]) != 1 {
		t.Fatalf("expected the initial save to index a posting, got %+v", postings["reindexed"])
	}
	if err := repo.SaveDocumentOptionalVocabulary(ctx, doc, nil, 3, 2, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	postings, err := repo.PostingsForTerms(ctx, []string{"reindexed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings["reindexed"]) != 0 {
		t.Errorf("expected the prior posting to be cleared, got %+v", postings["reindexed"])
	}
}
