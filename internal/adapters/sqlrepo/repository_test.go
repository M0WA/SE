package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

var dsnCounter int64

// newTestRepo gives each test its own isolated in-memory SQLite database
// (a shared cache keyed by a unique name, so the pooled *sql.DB connections
// within one test all see the same schema/data without leaking into
// other tests).
func newTestRepo(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testdb%d?mode=memory&cache=shared", n)
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestNew_MigratesSchemaAndConnects(t *testing.T) {
	repo := newTestRepo(t)
	totalDocs, avgDocLen, err := repo.CorpusStats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 0 || avgDocLen != 1 {
		t.Errorf("expected an empty fresh DB (0 docs, avgDocLen=1), got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestNew_UnknownDriverErrors(t *testing.T) {
	_, err := sqlrepo.New(context.Background(), "not-a-real-driver", "whatever")
	if err == nil {
		t.Fatal("expected an error for an unregistered driver")
	}
}

func TestNewWithDB_UsesGivenConnectionAndDialect(t *testing.T) {
	db, err := sql.Open("sqlite", "file:testnewwithdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := sqlrepo.NewWithDB(db, "sqlite")
	// NewWithDB doesn't migrate, so exercise the connection directly first.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if _, _, err := repo.CorpusStats(context.Background()); err != nil {
		t.Errorf("unexpected error using NewWithDB-constructed repository: %v", err)
	}
}

func TestSaveDocument_ThenRetrieveEverywhere(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets indeed"}
	embedding := []float32{0.1, 0.2, 0.3}

	if err := repo.SaveDocument(ctx, doc, embedding); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}

	got, err := repo.DocumentByID(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error loading document: %v", err)
	}
	if got.URL != doc.URL || got.Title != doc.Title || got.Text != doc.Text {
		t.Errorf("expected saved document back, got %+v", got)
	}

	embeddings, err := repo.AllEmbeddings(ctx)
	if err != nil {
		t.Fatalf("unexpected error loading embeddings: %v", err)
	}
	if len(embeddings["doc-1"]) != 3 || embeddings["doc-1"][0] != 0.1 {
		t.Errorf("expected saved embedding back, got %v", embeddings["doc-1"])
	}

	docs, err := repo.ListDocuments(ctx, 10)
	if err != nil {
		t.Fatalf("unexpected error listing documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "doc-1" || docs[0].DocLength == 0 {
		t.Errorf("expected the saved document in the listing, got %+v", docs)
	}

	postings, err := repo.PostingsForTerm(ctx, "cats")
	if err != nil {
		t.Fatalf("unexpected error querying postings: %v", err)
	}
	// "cats" appears twice: once in the title, once in the text (SaveDocument
	// tokenizes title+text together).
	if len(postings) != 1 || postings[0].DocID != "doc-1" || postings[0].TermFreq != 2 {
		t.Errorf("expected one posting for 'cats' with freq 2, got %+v", postings)
	}
	if postings[0].TotalDocs != 1 {
		t.Errorf("expected TotalDocs=1, got %d", postings[0].TotalDocs)
	}

	totalDocs, avgDocLen, err := repo.CorpusStats(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 1 || avgDocLen <= 0 {
		t.Errorf("expected corpus stats to reflect the saved doc, got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestSaveDocument_UpsertReplacesPostings(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	first := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "cats everywhere"}
	if err := repo.SaveDocument(ctx, first, []float32{1}); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}

	second := domain.Document{ID: "doc-1", URL: "http://a", Title: "Dogs", Text: "dogs everywhere"}
	if err := repo.SaveDocument(ctx, second, []float32{2}); err != nil {
		t.Fatalf("unexpected error on upsert save: %v", err)
	}

	catsPostings, err := repo.PostingsForTerm(ctx, "cats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(catsPostings) != 0 {
		t.Errorf("expected the old 'cats' posting to be gone after upsert, got %+v", catsPostings)
	}

	dogsPostings, err := repo.PostingsForTerm(ctx, "dogs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dogsPostings) != 1 {
		t.Errorf("expected a 'dogs' posting after upsert, got %+v", dogsPostings)
	}

	got, err := repo.DocumentByID(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "Dogs" {
		t.Errorf("expected upserted title, got %q", got.Title)
	}
}

func TestDocumentByID_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.DocumentByID(context.Background(), "does-not-exist")
	if err == nil {
		t.Error("expected an error for a missing document")
	}
}

func TestPostingsForTerm_NoMatches(t *testing.T) {
	repo := newTestRepo(t)
	postings, err := repo.PostingsForTerm(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postings != nil {
		t.Errorf("expected nil postings for an unindexed term, got %+v", postings)
	}
}

func TestPostingsForTerm_AcrossMultipleDocuments(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared term here"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared term appears twice, shared term"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerm(ctx, "shared")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("expected postings from both documents, got %+v", postings)
	}
	for _, p := range postings {
		if p.DocFreq != 2 {
			t.Errorf("expected DocFreq=2 (term appears in 2 docs), got %d", p.DocFreq)
		}
	}
}

func TestVocabularyStats_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	vocabSize, topTerms, err := repo.VocabularyStats(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vocabSize != 0 || topTerms != nil {
		t.Errorf("expected an empty vocabulary, got (%d, %+v)", vocabSize, topTerms)
	}
}

func TestVocabularyStats_ReportsSizeAndTopTermsByDocFreq(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common rare"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared common common"},
		{ID: "doc-3", URL: "http://c", Title: "C", Text: "shared unique"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	vocabSize, topTerms, err := repo.VocabularyStats(ctx, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// distinct terms across all three docs: shared, common, rare, unique.
	if vocabSize != 4 {
		t.Errorf("expected vocabulary size 4, got %d", vocabSize)
	}
	if len(topTerms) != 2 {
		t.Fatalf("expected limit=2 to be respected, got %+v", topTerms)
	}
	if topTerms[0].Term != "shared" || topTerms[0].DocFreq != 3 || topTerms[0].TotalFreq != 3 {
		t.Errorf("expected 'shared' first with doc_freq=3, total_freq=3, got %+v", topTerms[0])
	}
	if topTerms[1].Term != "common" || topTerms[1].DocFreq != 2 || topTerms[1].TotalFreq != 3 {
		t.Errorf("expected 'common' second with doc_freq=2, total_freq=3, got %+v", topTerms[1])
	}
}

func TestDeleteDocument_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "cats are great"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	if err := repo.DeleteDocument(ctx, "doc-1"); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}

	if _, err := repo.DocumentByID(ctx, "doc-1"); err == nil {
		t.Error("expected the document to be gone after delete")
	}
	postings, err := repo.PostingsForTerm(ctx, "cats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 0 {
		t.Errorf("expected postings to cascade-delete with the document, got %+v", postings)
	}
}

func TestDeleteDocument_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteDocument(context.Background(), "does-not-exist")
	if !errors.Is(err, ports.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

func TestListDocuments_RespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2", "doc-3"} {
		doc := domain.Document{ID: id, URL: "http://" + id, Title: id, Text: "content for " + id}
		if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}

	docs, err := repo.ListDocuments(ctx, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected limit=2 to be respected, got %d documents", len(docs))
	}
}

func TestNew_MigrationFailureErrors(t *testing.T) {
	// A read-only DB file: Open and Ping succeed, but CREATE TABLE fails.
	path := t.TempDir() + "/readonly.db"
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create temp db file: %v", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("failed to make temp db file read-only: %v", err)
	}

	_, err = sqlrepo.New(context.Background(), "sqlite", "file:"+path+"?mode=ro")
	if err == nil {
		t.Fatal("expected a migration error against a read-only database")
	}
}

func TestNew_PingFailureErrors(t *testing.T) {
	// A file DSN under a directory that doesn't exist: sql.Open succeeds
	// (it's lazy), but PingContext fails trying to actually open the file.
	_, err := sqlrepo.New(context.Background(), "sqlite", "file:/nonexistent-dir-xyz/test.db")
	if err == nil {
		t.Fatal("expected an error when the DB can't actually be reached")
	}
}

// closedRepo returns a repository whose underlying connection is already
// closed, to exercise the DB-error branches of every method with a real
// (not mocked) failure: any query against a closed *sql.DB errors.
func closedRepo(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	repo := newTestRepo(t)
	if err := repo.Close(); err != nil {
		t.Fatalf("failed to close repository: %v", err)
	}
	return repo
}

func TestRepository_MethodsErrorOnClosedConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("CorpusStats", func(t *testing.T) {
		if _, _, err := closedRepo(t).CorpusStats(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("AllEmbeddings", func(t *testing.T) {
		if _, err := closedRepo(t).AllEmbeddings(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("VocabularyStats", func(t *testing.T) {
		if _, _, err := closedRepo(t).VocabularyStats(ctx, 10); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PostingsForTerm", func(t *testing.T) {
		if _, err := closedRepo(t).PostingsForTerm(ctx, "term"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentByID", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentByID(ctx, "doc-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DeleteDocument", func(t *testing.T) {
		if err := closedRepo(t).DeleteDocument(ctx, "doc-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ListDocuments", func(t *testing.T) {
		if _, err := closedRepo(t).ListDocuments(ctx, 10); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SaveDocument", func(t *testing.T) {
		doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}
		if err := closedRepo(t).SaveDocument(ctx, doc, []float32{1}); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestAllEmbeddings_EmptyWhenNoDocuments(t *testing.T) {
	repo := newTestRepo(t)
	embeddings, err := repo.AllEmbeddings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected no embeddings, got %v", embeddings)
	}
}
