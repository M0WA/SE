package sqlrepo_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
)

func TestCrawlJobOutcomes_CountsOnlyTerminalStatusesSinceCutoff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	done, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkDone(ctx, done.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	failed, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkFailed(ctx, failed.ID, fmt.Errorf("boom")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cancelled, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkCancelled(ctx, cancelled.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Still queued/running -- neither status counts as a terminal outcome.
	if _, err := repo.Create(ctx, domain.CrawlJobRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	running, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkRunning(ctx, running.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outcomes, err := repo.CrawlJobOutcomes(ctx, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[domain.CrawlJobStatus]int{}
	total := 0
	for _, o := range outcomes {
		got[o.Status] = o.Count
		total += o.Count
	}
	if total != 3 {
		t.Fatalf("expected only the 3 terminal jobs counted, got total=%d (%+v)", total, outcomes)
	}
	if got[domain.CrawlJobDone] != 1 || got[domain.CrawlJobFailed] != 1 || got[domain.CrawlJobCancelled] != 1 {
		t.Errorf("expected 1 each of done/failed/cancelled, got %+v", got)
	}
}

func TestCrawlJobOutcomes_ExcludesJobsBeforeSince(t *testing.T) {
	n := atomic.AddInt64(&overviewDSNCounter, 1)
	dsn := fmt.Sprintf("file:testcrawljoboutcomessince%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	old, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkDone(ctx, old.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := raw.ExecContext(ctx, `UPDATE crawl_jobs SET created_at = ? WHERE id = ?`, oldTime.Format(time.RFC3339Nano), old.ID); err != nil {
		t.Fatalf("failed to backdate created_at: %v", err)
	}

	recent, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MarkDone(ctx, recent.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	outcomes, err := repo.CrawlJobOutcomes(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	total := 0
	for _, o := range outcomes {
		total += o.Count
	}
	if total != 1 {
		t.Errorf("expected only the recent job counted (old one predates since), got total=%d (%+v)", total, outcomes)
	}
}

func TestCrawlJobOutcomes_EmptyWhenNoTerminalJobs(t *testing.T) {
	repo := newTestRepo(t)
	outcomes, err := repo.CrawlJobOutcomes(context.Background(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outcomes) != 0 {
		t.Errorf("expected no outcomes for an empty crawl_jobs table, got %+v", outcomes)
	}
}

func TestDailyFetchOutcomes_GroupsByDayAndStatus(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	job, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	day1 := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 3, 2, 10, 0, 0, 0, time.UTC)
	events := []domain.CrawlPageEvent{
		{URL: "https://a.example/1", Status: domain.CrawlPageIndexed, FetchedAt: day1},
		{URL: "https://a.example/2", Status: domain.CrawlPageIndexed, FetchedAt: day1},
		{URL: "https://a.example/3", Status: domain.CrawlPageFetchFailed, FetchedAt: day1},
		{URL: "https://a.example/4", Status: domain.CrawlPageIndexed, FetchedAt: day2},
	}
	for _, ev := range events {
		if err := repo.AppendPage(ctx, job.ID, ev); err != nil {
			t.Fatalf("unexpected error appending page: %v", err)
		}
	}

	rows, err := repo.DailyFetchOutcomes(ctx, day1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]map[domain.CrawlPageStatus]int{}
	for _, r := range rows {
		if got[r.Date] == nil {
			got[r.Date] = map[domain.CrawlPageStatus]int{}
		}
		got[r.Date][r.Status] = r.Count
	}
	if got["2025-03-01"][domain.CrawlPageIndexed] != 2 || got["2025-03-01"][domain.CrawlPageFetchFailed] != 1 {
		t.Errorf("unexpected day-1 breakdown: %+v", got["2025-03-01"])
	}
	if got["2025-03-02"][domain.CrawlPageIndexed] != 1 {
		t.Errorf("unexpected day-2 breakdown: %+v", got["2025-03-02"])
	}
}

func TestDailyFetchOutcomes_ExcludesRowsBeforeSince(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	job, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.AppendPage(ctx, job.ID, domain.CrawlPageEvent{URL: "https://a.example", Status: domain.CrawlPageIndexed, FetchedAt: old}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows, err := repo.DailyFetchOutcomes(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected the old fetch to be excluded, got %+v", rows)
	}
}

func TestDocumentsIndexedByDay_GroupsByCrawledAtDate(t *testing.T) {
	n := atomic.AddInt64(&overviewDSNCounter, 1)
	dsn := fmt.Sprintf("file:testdocsbyday%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()

	docs := []struct {
		id  string
		day time.Time
	}{
		{"doc-1", time.Date(2025, 6, 1, 8, 0, 0, 0, time.UTC)},
		{"doc-2", time.Date(2025, 6, 1, 20, 0, 0, 0, time.UTC)},
		{"doc-3", time.Date(2025, 6, 2, 8, 0, 0, 0, time.UTC)},
	}
	for _, d := range docs {
		doc := domain.Document{ID: d.id, URL: "https://a.example/" + d.id, Title: d.id, Text: "text " + d.id}
		if err := repo.SaveDocument(ctx, doc, []float32{1}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.id, err)
		}
		if _, err := raw.ExecContext(ctx, `UPDATE documents SET crawled_at = ? WHERE id = ?`, d.day.Format(time.RFC3339Nano), d.id); err != nil {
			t.Fatalf("failed to backdate crawled_at for %s: %v", d.id, err)
		}
	}

	rows, err := repo.DocumentsIndexedByDay(ctx, time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 days, got %d: %+v", len(rows), rows)
	}
	if rows[0].Date != "2025-06-01" || rows[0].Count != 2 {
		t.Errorf("expected 2025-06-01 count=2, got %+v", rows[0])
	}
	if rows[1].Date != "2025-06-02" || rows[1].Count != 1 {
		t.Errorf("expected 2025-06-02 count=1, got %+v", rows[1])
	}
}

func TestDocumentsIndexedByDay_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	rows, err := repo.DocumentsIndexedByDay(context.Background(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows for an empty corpus, got %+v", rows)
	}
}

func TestDailyFetchDuration_AveragesPerDay(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	job, err := repo.Create(ctx, domain.CrawlJobRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	day1 := time.Date(2025, 4, 1, 9, 0, 0, 0, time.UTC)
	events := []domain.CrawlPageEvent{
		{URL: "https://a.example/1", Status: domain.CrawlPageIndexed, DurationMs: 100, FetchedAt: day1},
		{URL: "https://a.example/2", Status: domain.CrawlPageIndexed, DurationMs: 300, FetchedAt: day1},
	}
	for _, ev := range events {
		if err := repo.AppendPage(ctx, job.ID, ev); err != nil {
			t.Fatalf("unexpected error appending page: %v", err)
		}
	}
	rows, err := repo.DailyFetchDuration(ctx, day1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 || rows[0].Date != "2025-04-01" || rows[0].AvgDurationMs != 200 {
		t.Errorf("expected 2025-04-01 avg=200, got %+v", rows)
	}
}

func TestDailyFetchDuration_EmptyWhenNoFetches(t *testing.T) {
	repo := newTestRepo(t)
	rows, err := repo.DailyFetchDuration(context.Background(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows when nothing has ever been fetched, got %+v", rows)
	}
}

func TestPageRankHistogram_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	buckets, orphanCount, totalDocs, err := repo.PageRankHistogram(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(buckets) != 0 || orphanCount != 0 || totalDocs != 0 {
		t.Errorf("expected all-zero for an empty corpus, got buckets=%+v orphanCount=%d totalDocs=%d", buckets, orphanCount, totalDocs)
	}
}

func TestPageRankHistogram_BucketsAcrossObservedRange(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for i, id := range []string{"doc-1", "doc-2", "doc-3"} {
		doc := domain.Document{ID: id, URL: "https://a.example/" + id, Title: id, Text: fmt.Sprintf("text %d", i)}
		if err := repo.SaveDocument(ctx, doc, []float32{1}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}
	// Spread scores from 0.0 (an orphan, below the threshold) to 1.0 across
	// the histogram's observed range.
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0, "doc-2": 0.5, "doc-3": 1.0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buckets, orphanCount, totalDocs, err := repo.PageRankHistogram(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 3 {
		t.Errorf("expected totalDocs=3, got %d", totalDocs)
	}
	if len(buckets) != domain.PageRankHistogramBuckets {
		t.Fatalf("expected %d buckets, got %d: %+v", domain.PageRankHistogramBuckets, len(buckets), buckets)
	}
	sum := 0
	for _, b := range buckets {
		sum += b.Count
		if b.Label == "" {
			t.Errorf("expected every bucket to have a non-empty label, got %+v", b)
		}
	}
	if sum != 3 {
		t.Errorf("expected every document accounted for across buckets, got sum=%d (%+v)", sum, buckets)
	}
	if buckets[0].Count != 1 {
		t.Errorf("expected the lowest bucket (containing 0.0) to hold 1 document, got %+v", buckets[0])
	}
	if buckets[len(buckets)-1].Count != 1 {
		t.Errorf("expected the highest bucket (containing the max 1.0, inclusive) to hold 1 document, got %+v", buckets[len(buckets)-1])
	}
	if orphanCount != 1 {
		t.Errorf("expected 1 document at or below PageRankOrphanThreshold, got %d", orphanCount)
	}
}

// TestPageRankHistogram_DegenerateSingleValueRange proves a corpus where
// every document shares the exact same pagerank (min == max) still returns
// domain.PageRankHistogramBuckets buckets, with every document counted in
// the last one, rather than dividing by a zero-width range.
func TestPageRankHistogram_DegenerateSingleValueRange(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2"} {
		doc := domain.Document{ID: id, URL: "https://a.example/" + id, Title: id, Text: "shared text " + id}
		if err := repo.SaveDocument(ctx, doc, []float32{1}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}
	// Never explicitly scored -- both sit at the same neutral default, so
	// min == max across the whole corpus.

	buckets, _, totalDocs, err := repo.PageRankHistogram(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 2 {
		t.Errorf("expected totalDocs=2, got %d", totalDocs)
	}
	if len(buckets) != domain.PageRankHistogramBuckets {
		t.Fatalf("expected %d buckets, got %d", domain.PageRankHistogramBuckets, len(buckets))
	}
	if buckets[len(buckets)-1].Count != 2 {
		t.Errorf("expected both documents in the last bucket for a degenerate min==max range, got %+v", buckets)
	}
	sum := 0
	for _, b := range buckets {
		sum += b.Count
	}
	if sum != 2 {
		t.Errorf("expected both documents accounted for, got sum=%d (%+v)", sum, buckets)
	}
}

// overviewDSNCounter is this file's own atomic counter for in-memory
// sqlite DSNs, mirroring newTestRepo's dsnCounter -- kept separate so this
// file's raw-connection tests (which need a DSN before newTestRepo's own
// counter would hand one out) never collide with it or with each other.
var overviewDSNCounter int64
