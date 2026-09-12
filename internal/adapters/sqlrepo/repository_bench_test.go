package sqlrepo_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
)

// benchDSN returns a fresh unique in-memory-shared-cache SQLite DSN, so each
// benchmark gets its own isolated database (the shared cache keeps a named
// in-memory database alive across separate *sql.DB handles as long as one
// stays open, which repository_test.go's tests already rely on).
func benchDSN(prefix string) string {
	n := atomic.AddInt64(&dsnCounter, 1)
	return fmt.Sprintf("file:%s%d?mode=memory&cache=shared", prefix, n)
}

// seedBenchDocuments saves n synthetic documents through the real
// SaveDocument path (so the schema, postings, and norm_embedding column are
// all populated exactly as production writes them), each containing a
// realistic mix of a common shared term plus a handful of "rare" query
// terms distributed unevenly across the corpus -- mirroring how a real
// vocabulary has a few very common words and many rarer ones. Returns the
// full list of document IDs.
func seedBenchDocuments(b *testing.B, repo *sqlrepo.Repository, n int, rareTerms []string) []string {
	b.Helper()
	rng := rand.New(rand.NewSource(7))
	ids := make([]string, n)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc-%d", i)
		ids[i] = id
		text := "common"
		for _, term := range rareTerms {
			// Each rare term hits a different, fixed fraction of the corpus
			// (5%-25%), so a multi-term query's postings sets vary in size
			// the way real query terms do.
			if rng.Float64() < 0.05+0.2*rng.Float64() {
				text += " " + term
			}
		}
		doc := domain.Document{
			ID:    id,
			URL:   fmt.Sprintf("https://example.com/%s", id),
			Title: "Document " + id,
			Text:  text,
		}
		if err := repo.SaveDocument(ctx, doc, []float32{rng.Float32(), rng.Float32(), rng.Float32(), rng.Float32()}); err != nil {
			b.Fatalf("seeding document %s: %v", id, err)
		}
	}
	return ids
}

// BenchmarkDocumentFetch compares fetching 200 candidate documents (a
// typical constraint-filter/hydration candidate-set size) one at a time --
// a single-element DocumentsByIDs call per candidate, the pre-n1-document-
// fetches shape -- against one batched DocumentsByIDs call for all of
// them, against a 5,000-document corpus.
func BenchmarkDocumentFetch(b *testing.B) {
	ctx := context.Background()
	repo, err := sqlrepo.New(ctx, "sqlite", benchDSN("benchdocfetch"))
	if err != nil {
		b.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	const corpusSize = 5000
	const candidateCount = 200
	ids := seedBenchDocuments(b, repo, corpusSize, nil)
	candidateIDs := make([]string, candidateCount)
	copy(candidateIDs, ids[:candidateCount])

	b.Run("Loop_SingleDocumentsByIDs", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, id := range candidateIDs {
				if _, err := repo.DocumentsByIDs(ctx, []string{id}); err != nil {
					b.Fatalf("DocumentsByIDs(%s): %v", id, err)
				}
			}
		}
	})

	b.Run("Batched_DocumentsByIDs", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			docs, err := repo.DocumentsByIDs(ctx, candidateIDs)
			if err != nil {
				b.Fatalf("DocumentsByIDs: %v", err)
			}
			if len(docs) != candidateCount {
				b.Fatalf("expected %d docs, got %d", candidateCount, len(docs))
			}
		}
	})
}

// BenchmarkPostingsFetch compares a 5-term query's postings lookup done the
// pre-redundant-corpus-stats-per-term way -- PostingsForTerm called once per
// term (each call: one CorpusStats full-table COUNT/AVG scan, one doc-freq
// COUNT query, one join query -- 3 queries/term, 15 total for 5 terms, per
// this package's own PostingsForTerm implementation) -- against a single
// batched PostingsForTerms call (1 query total, with TotalDocs/AvgDocLen
// left for the caller to fill in once from a cache rather than requeried
// per term), against a 5,000-document corpus.
func BenchmarkPostingsFetch(b *testing.B) {
	ctx := context.Background()
	repo, err := sqlrepo.New(ctx, "sqlite", benchDSN("benchpostings"))
	if err != nil {
		b.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	terms := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	const corpusSize = 5000
	seedBenchDocuments(b, repo, corpusSize, terms)

	b.Run("Loop_PostingsForTerm", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, term := range terms {
				if _, err := repo.PostingsForTerm(ctx, term, corpusSize); err != nil {
					b.Fatalf("PostingsForTerm(%s): %v", term, err)
				}
			}
		}
		b.StopTimer()
		// Static from reading PostingsForTerm's implementation: CorpusStats
		// (1 query) + doc-freq COUNT (1 query) + join SELECT (1 query) per
		// term, called once per term here.
		b.ReportMetric(float64(len(terms)*3), "queries/op")
	})

	b.Run("Batched_PostingsForTerms", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := repo.PostingsForTerms(ctx, terms)
			if err != nil {
				b.Fatalf("PostingsForTerms: %v", err)
			}
			if len(out) == 0 {
				b.Fatal("expected at least one term to match")
			}
		}
		b.StopTimer()
		b.ReportMetric(1, "queries/op")
	})
}

// seedBenchDocumentsWithCrawledAt is like seedBenchDocuments, but backdates
// each document's crawled_at to a random point over the last 90 days via a
// direct UPDATE afterward (SaveDocument itself always stamps "now", so a
// realistic spread of ages has to be applied out of band) -- needed to
// exercise countDocumentsCrawled's age-bucket ranges and a recency-sorted
// fetch meaningfully.
func seedBenchDocumentsWithCrawledAt(b *testing.B, dsn string, repo *sqlrepo.Repository, n int) []string {
	b.Helper()
	ctx := context.Background()
	ids := make([]string, n)
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc-%d", i)
		ids[i] = id
		doc := domain.Document{ID: id, URL: fmt.Sprintf("https://example.com/%s", id), Title: "T", Text: "x"}
		if err := repo.SaveDocument(ctx, doc, []float32{rng.Float32(), rng.Float32()}); err != nil {
			b.Fatalf("seeding document %s: %v", id, err)
		}
	}

	// A second connection onto the same shared-cache in-memory database,
	// used only to backdate crawled_at directly -- SaveDocument has no
	// parameter for it, since production always saves with the real current
	// time.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatalf("failed to open raw connection for crawled_at backdating: %v", err)
	}
	defer raw.Close()

	tx, err := raw.Begin()
	if err != nil {
		b.Fatalf("failed to begin backdate transaction: %v", err)
	}
	stmt, err := tx.Prepare(`UPDATE documents SET crawled_at = ? WHERE id = ?`)
	if err != nil {
		b.Fatalf("failed to prepare backdate statement: %v", err)
	}
	now := time.Now().UTC()
	for _, id := range ids {
		age := time.Duration(rng.Int63n(int64(90 * 24 * time.Hour)))
		crawledAt := now.Add(-age).Format(time.RFC3339Nano)
		if _, err := stmt.Exec(crawledAt, id); err != nil {
			b.Fatalf("backdating %s: %v", id, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatalf("failed to commit backdate transaction: %v", err)
	}
	return ids
}

// dropCrawledAtIndex and recreateCrawledAtIndex let this benchmark compare
// the exact same queries with and without idx_documents_crawled_at, against
// the exact same seeded 50,000-row corpus -- isolating the index's effect
// from any other difference between two separately seeded databases.
func dropCrawledAtIndex(b *testing.B, dsn string) {
	b.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatalf("failed to open raw connection to drop index: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP INDEX IF EXISTS idx_documents_crawled_at`); err != nil {
		b.Fatalf("failed to drop crawled_at index: %v", err)
	}
}

// BenchmarkRecencyQueries seeds 50,000 documents with crawled_at spread
// randomly over 90 days, then compares two of the crawled_at-index's
// beneficiaries -- DocumentsOverview's four age-bucket range-COUNT queries,
// and a recency-sorted top-candidate fetch via
// DocumentsByIDsSortedByCrawledAt -- with the index present (as every
// database gets by default since missing-crawled-at-index) against the same
// corpus with the index dropped (the pre-fix state).
func BenchmarkRecencyQueries(b *testing.B) {
	ctx := context.Background()
	dsn := benchDSN("benchrecency")
	repo, err := sqlrepo.New(ctx, "sqlite", dsn)
	if err != nil {
		b.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	const corpusSize = 50000
	ids := seedBenchDocumentsWithCrawledAt(b, dsn, repo, corpusSize)

	// A candidate ID set the size of a bounded semantic candidate pool
	// (SemanticCandidatePoolSize's default is 200, generously rounded up
	// here to 2,000 to also stress a larger pool), for the
	// DocumentsByIDsSortedByCrawledAt half of this benchmark.
	candidateIDs := make([]string, 2000)
	copy(candidateIDs, ids[:2000])

	runBoth := func(b *testing.B) {
		b.Run("DocumentsOverview", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := repo.DocumentsOverview(ctx, 5); err != nil {
					b.Fatalf("DocumentsOverview: %v", err)
				}
			}
		})
		b.Run("DocumentsByIDsSortedByCrawledAt", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				docs, err := repo.DocumentsByIDsSortedByCrawledAt(ctx, candidateIDs)
				if err != nil {
					b.Fatalf("DocumentsByIDsSortedByCrawledAt: %v", err)
				}
				if len(docs) != len(candidateIDs) {
					b.Fatalf("expected %d docs, got %d", len(candidateIDs), len(docs))
				}
			}
		})
	}

	b.Run("WithIndex", runBoth)
	dropCrawledAtIndex(b, dsn)
	b.Run("WithoutIndex", runBoth)
}

// BenchmarkConnectionPoolTuning validates connection-pool-tuning's actual
// mechanism -- database/sql's own MaxOpenConns limiting, which ConfigurePool
// drives -- under concurrent load. A live Postgres/MySQL server (what the
// original analysis's plan called for, to see real network-connection churn)
// isn't available in this default, no-external-dependency test environment,
// so this instead points a repository constructed with a non-sqlite dialect
// name at a real in-memory SQLite database (via NewWithDB, exactly as
// TestConfigurePool_NonSQLiteAppliesGivenValues already does) -- ConfigurePool
// never issues dialect-specific SQL, so this still exercises the real
// SetMaxOpenConns/SetMaxIdleConns calls and the real database/sql pool-wait
// bookkeeping, just without a real network round trip per connection. A low
// pool size (2) forces 50 concurrent goroutines to queue for a connection
// (measurable via db.Stats().WaitCount/WaitDuration); the tuned default (25)
// should show markedly less queuing.
func BenchmarkConnectionPoolTuning(b *testing.B) {
	const concurrency = 50

	for _, maxOpen := range []int{2, 25} {
		b.Run(fmt.Sprintf("MaxOpenConns=%d", maxOpen), func(b *testing.B) {
			dsn := benchDSN("benchpool")
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				b.Fatalf("failed to open db: %v", err)
			}
			defer db.Close()

			repo := sqlrepo.NewWithDB(db, "postgres") // dialect name only -- underlying DB is still sqlite
			repo.ConfigurePool(maxOpen, maxOpen, 5*time.Minute)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS documents (
				id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
				doc_length INTEGER NOT NULL, embedding TEXT NOT NULL,
				norm_embedding REAL NOT NULL DEFAULT 0
			)`); err != nil {
				b.Fatalf("failed to create schema: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				wg.Add(concurrency)
				for g := 0; g < concurrency; g++ {
					go func() {
						defer wg.Done()
						if _, _, err := repo.CorpusStats(ctx); err != nil {
							b.Error(err)
						}
					}()
				}
				wg.Wait()
			}
			b.StopTimer()
			stats := repo.PoolStats()
			b.ReportMetric(float64(stats.WaitCount), "waitcount")
			if b.N > 0 {
				b.ReportMetric(float64(stats.WaitDuration.Nanoseconds())/float64(b.N), "waitns/op")
			}
		})
	}
}
