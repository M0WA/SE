package sqlrepo_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
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
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{rng.Float32(), rng.Float32(), rng.Float32(), rng.Float32()}}, 100, 2); err != nil {
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
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{rng.Float32(), rng.Float32()}}, 100, 2); err != nil {
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
	defer func() { _ = tx.Rollback() }()
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

// BenchmarkSaveDocumentNewDoc isolates the cost of SaveDocument's
// never-before-seen-URL branch (the sql.ErrNoRows case) at increasing
// pre-existing corpus sizes. Before the O(N^2) fix, that branch runs a
// live, unindexed `SELECT COUNT(*) FROM documents` inside the transaction
// purely to seed a placeholder pagerank -- so ns/op here should grow
// roughly linearly with corpus size (a bigger table takes proportionally
// longer to fully scan) before the fix, and stay flat across corpus sizes
// after it.
func BenchmarkSaveDocumentNewDoc(b *testing.B) {
	ctx := context.Background()

	for _, size := range []int{1000, 5000, 20000, 50000} {
		b.Run(fmt.Sprintf("corpus=%d", size), func(b *testing.B) {
			repo, err := sqlrepo.New(ctx, "sqlite", benchDSN(fmt.Sprintf("benchnewdoc%d", size)))
			if err != nil {
				b.Fatalf("failed to create repo: %v", err)
			}
			defer repo.Close()

			seedBenchDocuments(b, repo, size, nil)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				doc := domain.Document{
					ID:    fmt.Sprintf("new-%d-%d", size, i),
					URL:   fmt.Sprintf("http://example.com/new-%d-%d", size, i),
					Title: "New Document",
					Text:  "brand new content never seen before",
				}
				if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{0.1, 0.2, 0.3, 0.4}}, 100, 2); err != nil {
					b.Fatalf("SaveDocument: %v", err)
				}
			}
		})
	}
}

// saveDocumentInsertBatchSizeForBench mirrors sqlrepo's unexported
// saveDocumentInsertBatchSize constant (this test lives in the external
// sqlrepo_test package, so it can't reference the constant directly) --
// keep the two in sync if that constant ever changes.
const saveDocumentInsertBatchSizeForBench = 300

// analyticalSaveDocumentPostingsLinkExecs returns how many tx.ExecContext
// calls SaveDocument's postings+links rewrite makes for the given unique
// term/link counts, under the CURRENT (batched) implementation: one
// multi-row INSERT per saveDocumentInsertBatchSizeForBench-sized chunk of
// terms, plus one per chunk of links.
func analyticalSaveDocumentPostingsLinkExecs(terms, links int) int {
	chunks := func(n int) int {
		if n == 0 {
			return 0
		}
		return (n + saveDocumentInsertBatchSizeForBench - 1) / saveDocumentInsertBatchSizeForBench
	}
	return chunks(terms) + chunks(links)
}

// buildSaveDocumentBenchDoc builds a domain.Document with exactly termCount
// unique postings terms (each long enough, and not a stopword, to survive
// domain.Tokenize) and linkCount unique, non-self-referential links -- the
// two collections SaveDocument rewrites in full on every call.
func buildSaveDocumentBenchDoc(termCount, linkCount int) domain.Document {
	var text strings.Builder
	for i := 0; i < termCount; i++ {
		fmt.Fprintf(&text, "benchterm%d ", i)
	}
	links := make([]string, linkCount)
	for i := 0; i < linkCount; i++ {
		links[i] = fmt.Sprintf("https://example.com/linked-%d", i)
	}
	return domain.Document{
		ID:    "bench-savedoc",
		URL:   "https://example.com/bench-savedoc",
		Title: "Bench Document",
		Text:  text.String(),
		Links: links,
	}
}

// BenchmarkSaveDocumentWrites measures SaveDocument itself (postings +
// links rewrite is the dominant cost: every term and every link on the page
// is rewritten on every save, unconditionally, regardless of whether the
// document's content actually changed) across a handful of realistic
// term/link-count combinations, from a small page up through a
// heavily-linked, term-rich one. Each sub-benchmark saves the exact same
// document repeatedly -- after the first call, existingText == doc.Text, so
// no version archiving happens on subsequent iterations, isolating the
// postings/links rewrite path from the versioning path measured elsewhere.
//
// execs/op is the number of tx.ExecContext calls SaveDocument makes purely
// for postings+links (i.e. excluding the fixed handful of calls for the
// existing-version SELECT, the documents upsert, and the two DELETEs) --
// computed analytically from the known implementation (one INSERT per
// unique term/link row-by-row before this change; one INSERT per
// saveDocumentInsertChunkSize-sized chunk after), not measured via a
// counting driver, since the loop shape is a fixed, known property of the
// code under test at any given commit.
func BenchmarkSaveDocumentWrites(b *testing.B) {
	cases := []struct {
		terms int
		links int
	}{
		{terms: 50, links: 10},
		{terms: 200, links: 50},
		{terms: 500, links: 200},
	}

	for _, tc := range cases {
		b.Run(fmt.Sprintf("terms=%d_links=%d", tc.terms, tc.links), func(b *testing.B) {
			ctx := context.Background()
			repo, err := sqlrepo.New(ctx, "sqlite", benchDSN(fmt.Sprintf("benchsavedoc%d_%d", tc.terms, tc.links)))
			if err != nil {
				b.Fatalf("failed to create repo: %v", err)
			}
			defer repo.Close()

			doc := buildSaveDocumentBenchDoc(tc.terms, tc.links)
			embedding := []float32{0.1, 0.2, 0.3, 0.4}

			// Prime once outside the timed loop so every timed iteration hits
			// the "unchanged content" branch (no archiving), matching a
			// re-crawl of an unchanged page -- the common case in practice.
			if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: embedding}, 100, 2); err != nil {
				b.Fatalf("priming SaveDocument: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: embedding}, 100, 2); err != nil {
					b.Fatalf("SaveDocument: %v", err)
				}
			}
			b.StopTimer()

			execsPerOp := analyticalSaveDocumentPostingsLinkExecs(tc.terms, tc.links)
			b.ReportMetric(float64(execsPerOp), "execs/op")
		})
	}
}

// seedBenchEmbeddingCorpus saves n synthetic documents through the real
// SaveDocument path, each with a realistic dims-wide embedding (unlike
// seedBenchDocuments, which hardcodes a fixed 4-element embedding regardless
// of what's asked -- too small to meaningfully exercise the embedding
// column's own encode/decode cost, the entire point of
// BenchmarkSampleEmbeddings below).
func seedBenchEmbeddingCorpus(b *testing.B, repo *sqlrepo.Repository, n, dims int) {
	b.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(23))
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc-%d", i)
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = rng.Float32()*2 - 1
		}
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: "T", Text: "benchmark content"}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: vec}, 100, 2); err != nil {
			b.Fatalf("seeding document %s: %v", id, err)
		}
	}
}

// BenchmarkSampleEmbeddings measures the real, current SampleEmbeddings
// fallback end-to-end (seed via SaveDocument, read via SampleEmbeddings
// against a real SQLite-backed documents table) -- the brute-force
// candidate-pool path every process actually runs on every hybrid search
// request unless Postgres ANN is available (see ann.go), at poolSize=200
// (SemanticCandidatePoolSize's real default) and poolSize=5000 (this
// package's larger-corpus benchmark convention). 128-dim embeddings, this
// codebase's actual embedder default (see BenchmarkEmbeddingCodec's doc
// comment for why 128, not the proposal's assumed 384).
//
// Run before and after documents.embedding's on-disk encoding changes from
// JSON text to packed binary (see dialect.go/repository.go) to capture the
// real row-size + I/O + CPU cost together -- not just the pure codec cost
// BenchmarkEmbeddingCodec measures in isolation.
func BenchmarkSampleEmbeddings(b *testing.B) {
	const dims = 128
	const corpusSize = 5000
	ctx := context.Background()

	for _, poolSize := range []int{200, 5000} {
		repo, err := sqlrepo.New(ctx, "sqlite", benchDSN("benchsampleemb"))
		if err != nil {
			b.Fatalf("failed to create repo: %v", err)
		}
		seedBenchEmbeddingCorpus(b, repo, corpusSize, dims)

		b.Run(fmt.Sprintf("PoolSize=%d", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := repo.SampleEmbeddings(ctx, poolSize, domain.EmbeddingProviderHash)
				if err != nil {
					b.Fatalf("SampleEmbeddings: %v", err)
				}
				if len(out) != poolSize {
					b.Fatalf("expected %d embeddings, got %d", poolSize, len(out))
				}
			}
		})
		repo.Close()
	}
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
				doc_length INTEGER NOT NULL, embedding BLOB NOT NULL,
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

// BenchmarkWALConcurrency measures whether SQLite's default rollback-journal
// mode lets a writer's transaction stall concurrent readers -- the scenario
// that motivates turning on WAL + synchronous=NORMAL. Unlike
// BenchmarkConnectionPoolTuning (one repository, contention arbitrated by
// database/sql's own connection pool), this opens two *separate* repository
// instances -- a "writer" and a "reader" -- against the same on-disk SQLite
// file, exactly mirroring how cmd/crawl and cmd/search are two independent
// OS processes each with their own single-connection *sql.DB pointed at the
// same search.db file (ConfigurePool always clamps SQLite to 1 open
// connection). With two connections onto one *sql.DB, database/sql's own
// pool would serialize them regardless of SQLite's journal mode, hiding
// exactly the effect this benchmark exists to observe -- so two connections
// (two repos) is the only way to actually exercise SQLite's own file-level
// locking.
//
// A real temp-file DSN is required (not the benchDSN in-memory-shared-cache
// helper used elsewhere in this file): WAL mode writes a real "-wal" file
// alongside the main database file on disk, which an in-memory database
// doesn't have.
//
// The writer goroutine loops SaveDocument (a real multi-statement write
// transaction -- select existing version/text/pagerank, archive, prune,
// upsert, rewrite postings/links) against one already-seeded document,
// continuously, for the duration of the timed loop. Concurrently, each
// timed round fans out readConcurrency goroutines that each do one
// CorpusStats + PostingsForTerms read (the read path a live /search request
// exercises), timing it individually via time.Since so a writer-induced
// stall shows up in the per-read average even when b.N's own ns/op gets
// diluted by read/read parallelism.
func BenchmarkWALConcurrency(b *testing.B) {
	const readConcurrency = 20
	const seedSize = 500

	ctx := context.Background()
	dir := b.TempDir()
	dsn := fmt.Sprintf("file:%s/bench.db?cache=shared", dir)

	writerRepo, err := sqlrepo.New(ctx, "sqlite", dsn)
	if err != nil {
		b.Fatalf("failed to open writer repo: %v", err)
	}
	defer writerRepo.Close()

	readerRepo, err := sqlrepo.New(ctx, "sqlite", dsn)
	if err != nil {
		b.Fatalf("failed to open reader repo: %v", err)
	}
	defer readerRepo.Close()

	terms := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	seedBenchDocuments(b, writerRepo, seedSize, terms)

	rng := rand.New(rand.NewSource(13))
	writerDoc := domain.Document{
		ID:    "doc-0",
		URL:   "https://example.com/doc-0",
		Title: "Document doc-0",
		Text:  "common alpha bravo charlie delta echo",
	}

	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stopWriter:
				return
			default:
			}
			embedding := []float32{rng.Float32(), rng.Float32(), rng.Float32(), rng.Float32()}
			if err := writerRepo.SaveDocument(ctx, writerDoc, map[string][]float32{domain.EmbeddingProviderHash: embedding}, 100, 2); err != nil {
				b.Error(err)
				return
			}
		}
	}()

	var totalReadNs int64
	var readCount int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		wg.Add(readConcurrency)
		for g := 0; g < readConcurrency; g++ {
			go func() {
				defer wg.Done()
				start := time.Now()
				if _, _, err := readerRepo.CorpusStats(ctx); err != nil {
					b.Error(err)
					return
				}
				if _, err := readerRepo.PostingsForTerms(ctx, terms); err != nil {
					b.Error(err)
					return
				}
				elapsed := time.Since(start)
				atomic.AddInt64(&totalReadNs, elapsed.Nanoseconds())
				atomic.AddInt64(&readCount, 1)
			}()
		}
		wg.Wait()
	}
	b.StopTimer()

	close(stopWriter)
	<-writerDone

	if n := atomic.LoadInt64(&readCount); n > 0 {
		b.ReportMetric(float64(atomic.LoadInt64(&totalReadNs))/float64(n), "readns/op")
	}
}
