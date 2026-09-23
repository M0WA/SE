package sqlrepo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"searchengine/internal/domain"
)

// CrawlJobOutcomes reports how many crawl jobs created at or after since
// finished in each terminal status -- see domain.CrawlJobOutcomeCount.
func (r *Repository) CrawlJobOutcomes(ctx context.Context, since time.Time) ([]domain.CrawlJobOutcomeCount, error) {
	query := r.ph(`SELECT status, COUNT(*) AS cnt FROM crawl_jobs
	               WHERE created_at >= %s AND status IN ('done', 'failed', 'cancelled')
	               GROUP BY status ORDER BY status ASC`, 1)
	rows, err := r.db.QueryContext(ctx, query, since.UTC().Format(crawledAtLayout))
	if err != nil {
		return nil, fmt.Errorf("querying crawl job outcomes: %w", err)
	}
	defer rows.Close()

	var out []domain.CrawlJobOutcomeCount
	for rows.Next() {
		var status string
		var cnt int
		if err := rows.Scan(&status, &cnt); err != nil {
			return nil, fmt.Errorf("scanning crawl job outcome row: %w", err)
		}
		out = append(out, domain.CrawlJobOutcomeCount{Status: domain.CrawlJobStatus(status), Count: cnt})
	}
	return out, rows.Err()
}

// dayExpr extracts "YYYY-MM-DD" from column -- every timestamp here is
// RFC3339Nano text, so its first 10 chars are the date; SUBSTR is portable
// across all three dialects.
func dayExpr(column string) string {
	return "SUBSTR(" + column + ", 1, 10)"
}

// DailyFetchOutcomes reports, for each day at or after since, how many
// crawl_job_pages rows landed in each fetch outcome.
func (r *Repository) DailyFetchOutcomes(ctx context.Context, since time.Time) ([]domain.DailyFetchOutcome, error) {
	day := dayExpr("fetched_at")
	query := r.ph(`SELECT `+day+` AS day, status, COUNT(*) AS cnt FROM crawl_job_pages
	               WHERE fetched_at >= %s
	               GROUP BY `+day+`, status ORDER BY `+day+` ASC, status ASC`, 1)
	rows, err := r.db.QueryContext(ctx, query, since.UTC().Format(crawledAtLayout))
	if err != nil {
		return nil, fmt.Errorf("querying daily fetch outcomes: %w", err)
	}
	defer rows.Close()

	var out []domain.DailyFetchOutcome
	for rows.Next() {
		var d, status string
		var cnt int
		if err := rows.Scan(&d, &status, &cnt); err != nil {
			return nil, fmt.Errorf("scanning daily fetch outcome row: %w", err)
		}
		out = append(out, domain.DailyFetchOutcome{Date: d, Status: domain.CrawlPageStatus(status), Count: cnt})
	}
	return out, rows.Err()
}

// DocumentsIndexedByDay reports how many documents' crawled_at falls on
// each day at or after since.
func (r *Repository) DocumentsIndexedByDay(ctx context.Context, since time.Time) ([]domain.DailyCount, error) {
	day := dayExpr("crawled_at")
	query := r.ph(`SELECT `+day+` AS day, COUNT(*) AS cnt FROM documents
	               WHERE crawled_at >= %s
	               GROUP BY `+day+` ORDER BY `+day+` ASC`, 1)
	rows, err := r.db.QueryContext(ctx, query, since.UTC().Format(crawledAtLayout))
	if err != nil {
		return nil, fmt.Errorf("querying documents indexed by day: %w", err)
	}
	defer rows.Close()

	var out []domain.DailyCount
	for rows.Next() {
		var d string
		var cnt int
		if err := rows.Scan(&d, &cnt); err != nil {
			return nil, fmt.Errorf("scanning documents-by-day row: %w", err)
		}
		out = append(out, domain.DailyCount{Date: d, Count: cnt})
	}
	return out, rows.Err()
}

// DailyFetchDuration reports each day's mean duration_ms at or after since.
// GROUP BY guarantees a row per returned day, so AVG never hits an empty,
// null-producing group.
func (r *Repository) DailyFetchDuration(ctx context.Context, since time.Time) ([]domain.DailyAvgDuration, error) {
	day := dayExpr("fetched_at")
	query := r.ph(`SELECT `+day+` AS day, AVG(duration_ms) AS avg_ms FROM crawl_job_pages
	               WHERE fetched_at >= %s
	               GROUP BY `+day+` ORDER BY `+day+` ASC`, 1)
	rows, err := r.db.QueryContext(ctx, query, since.UTC().Format(crawledAtLayout))
	if err != nil {
		return nil, fmt.Errorf("querying daily fetch duration: %w", err)
	}
	defer rows.Close()

	var out []domain.DailyAvgDuration
	for rows.Next() {
		var d string
		var avgMs float64
		if err := rows.Scan(&d, &avgMs); err != nil {
			return nil, fmt.Errorf("scanning daily fetch duration row: %w", err)
		}
		out = append(out, domain.DailyAvgDuration{Date: d, AvgDurationMs: avgMs})
	}
	return out, rows.Err()
}

// formatPageRankBound renders a histogram bucket edge for display --
// scientific notation with 2 significant digits, since real pagerank
// values are tiny fractions a fixed-decimal format would just show as "0.00".
func formatPageRankBound(v float64) string {
	return strconv.FormatFloat(v, 'e', 2, 64)
}

// PageRankHistogram buckets every document's pagerank into
// domain.PageRankHistogramBuckets equal-width bins over the corpus's
// observed [min, max], plus how many sit at or below
// domain.PageRankOrphanThreshold and the total document count. All zero
// for an empty corpus.
func (r *Repository) PageRankHistogram(ctx context.Context) ([]domain.PageRankBucket, int, int, error) {
	var minV, maxV float64
	var totalDocs int
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(pagerank), 0), COALESCE(MAX(pagerank), 0), COUNT(*) FROM documents`,
	).Scan(&minV, &maxV, &totalDocs)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("querying pagerank range: %w", err)
	}
	if totalDocs == 0 {
		return nil, 0, 0, nil
	}

	const numBuckets = domain.PageRankHistogramBuckets
	edges := make([]float64, numBuckets+1)
	if maxV <= minV {
		// Every document shares the same score (single-document corpus, or
		// PageRank never run) -- one degenerate bucket for that value, not
		// a zero-width range.
		for i := range edges {
			edges[i] = minV
		}
	} else {
		width := (maxV - minV) / float64(numBuckets)
		for i := range edges {
			edges[i] = minV + float64(i)*width
		}
	}

	var caseExprs []string
	var args []interface{}
	pos := 1
	for i := 0; i < numBuckets; i++ {
		lo, hi := edges[i], edges[i+1]
		// The last bucket's upper bound is inclusive (<=) so the corpus
		// max lands somewhere, not excluded by the otherwise half-open
		// [lo, hi) range.
		cmp := "<"
		if i == numBuckets-1 {
			cmp = "<="
		}
		caseExprs = append(caseExprs, fmt.Sprintf(
			"SUM(CASE WHEN pagerank >= %s AND pagerank %s %s THEN 1 ELSE 0 END)",
			r.dialect.Placeholder(pos), cmp, r.dialect.Placeholder(pos+1)))
		args = append(args, lo, hi)
		pos += 2
	}
	caseExprs = append(caseExprs, fmt.Sprintf("SUM(CASE WHEN pagerank <= %s THEN 1 ELSE 0 END)", r.dialect.Placeholder(pos)))
	args = append(args, domain.PageRankOrphanThreshold)

	query := "SELECT " + strings.Join(caseExprs, ", ") + " FROM documents"
	counts := make([]int64, numBuckets+1)
	scanArgs := make([]interface{}, numBuckets+1)
	for i := range counts {
		scanArgs[i] = &counts[i]
	}
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(scanArgs...); err != nil {
		return nil, 0, 0, fmt.Errorf("querying pagerank histogram: %w", err)
	}

	buckets := make([]domain.PageRankBucket, numBuckets)
	for i := 0; i < numBuckets; i++ {
		buckets[i] = domain.PageRankBucket{
			Label: formatPageRankBound(edges[i]) + "–" + formatPageRankBound(edges[i+1]),
			Count: int(counts[i]),
		}
	}
	orphanCount := int(counts[numBuckets])

	return buckets, orphanCount, totalDocs, nil
}
