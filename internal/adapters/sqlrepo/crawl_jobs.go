package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"searchengine/internal/domain"
)

// crawlJobColumns lists crawl_jobs' columns in the fixed order every
// query/scan in this file uses, the same convention scheduledCrawlColumns
// follows for scheduled_crawls.
const crawlJobColumns = "id, request, status, pages_crawled, error, created_at, started_at, finished_at"

// Create persists a new crawl job in domain.CrawlJobQueued status. Unlike
// the in-memory domain.CrawlJobStore, this survives a crawl-server
// restart -- see ports.CrawlJobStore's doc comment for why that matters.
func (r *Repository) Create(ctx context.Context, req domain.CrawlJobRequest) (domain.CrawlJob, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return domain.CrawlJob{}, fmt.Errorf("encoding crawl job request: %w", err)
	}
	job := domain.CrawlJob{
		ID: newCrawlJobID(), Request: req, Status: domain.CrawlJobQueued,
		CreatedAt: time.Now().UTC(),
	}
	insertSQL := r.ph(`INSERT INTO crawl_jobs (`+crawlJobColumns+`) VALUES (%s, %s, %s, %s, %s, %s, %s, %s)`,
		1, 2, 3, 4, 5, 6, 7, 8)
	_, err = r.db.ExecContext(ctx, insertSQL,
		job.ID, string(reqJSON), string(job.Status), job.PagesCrawled, job.Error,
		job.CreatedAt.Format(crawledAtLayout), nil, nil,
	)
	if err != nil {
		return domain.CrawlJob{}, fmt.Errorf("creating crawl job: %w", err)
	}
	return job, nil
}

// newCrawlJobID mirrors domain's own ID scheme (time-ordered, so
// ORDER BY created_at and ORDER BY id agree) without depending on domain's
// unexported sequence counter -- crawl-server's persistent store and the
// in-memory domain.CrawlJobStore never run in the same process, so two
// independent ID generators risk no collision between them.
var crawlJobIDSeq int64

func newCrawlJobID() string {
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&crawlJobIDSeq, 1))
}

func (r *Repository) MarkRunning(ctx context.Context, id string) error {
	updateSQL := r.ph(`UPDATE crawl_jobs SET status = %s, started_at = %s WHERE id = %s`, 1, 2, 3)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.CrawlJobRunning), time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking crawl job %s running: %w", id, err)
	}
	return nil
}

// AppendPage records one page's outcome and, for an indexed page, advances
// crawl_jobs.pages_crawled -- both in one transaction, so a concurrent Get
// never observes a page row without its corresponding count update (or
// vice versa). A page appended against a job ID that no longer exists
// (pruned by PruneCrawlJobs, say) is silently dropped, matching the
// in-memory store's same "append on an evicted job is a no-op" contract.
func (r *Repository) AppendPage(ctx context.Context, id string, ev domain.CrawlPageEvent) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	insertSQL := r.ph(`INSERT INTO crawl_job_pages
	                    (job_id, url, status, title, error, doc_length, links_found, duration_ms, fetched_at)
	                    SELECT %s, %s, %s, %s, %s, %s, %s, %s, %s
	                    WHERE EXISTS (SELECT 1 FROM crawl_jobs WHERE id = %s)`,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	res, err := tx.ExecContext(ctx, insertSQL,
		id, ev.URL, string(ev.Status), ev.Title, ev.Error, ev.DocLength, ev.LinksFound, ev.DurationMs,
		ev.FetchedAt.UTC().Format(crawledAtLayout), id,
	)
	if err != nil {
		return fmt.Errorf("appending crawl job page: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return tx.Commit() // job doesn't exist (evicted/never created) -- no-op, not an error
	}
	if ev.Status == domain.CrawlPageIndexed {
		incSQL := r.ph(`UPDATE crawl_jobs SET pages_crawled = pages_crawled + 1 WHERE id = %s`, 1)
		if _, err := tx.ExecContext(ctx, incSQL, id); err != nil {
			return fmt.Errorf("incrementing pages_crawled: %w", err)
		}
	}
	return tx.Commit()
}

func (r *Repository) MarkDone(ctx context.Context, id string) error {
	updateSQL := r.ph(`UPDATE crawl_jobs SET status = %s, finished_at = %s WHERE id = %s`, 1, 2, 3)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.CrawlJobDone), time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking crawl job %s done: %w", id, err)
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, id string, failErr error) error {
	updateSQL := r.ph(`UPDATE crawl_jobs SET status = %s, error = %s, finished_at = %s WHERE id = %s`, 1, 2, 3, 4)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.CrawlJobFailed), failErr.Error(), time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking crawl job %s failed: %w", id, err)
	}
	return nil
}

// Get returns the job and every one of its page events, oldest first
// (insertion order, served by crawl_job_pages' own auto-increment primary
// key). Returns domain.ErrCrawlJobNotFound if no job with this ID exists.
func (r *Repository) Get(ctx context.Context, id string) (domain.CrawlJob, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+crawlJobColumns+` FROM crawl_jobs WHERE id = `+r.dialect.Placeholder(1), id)
	job, err := scanCrawlJob(row)
	if err == sql.ErrNoRows {
		return domain.CrawlJob{}, domain.ErrCrawlJobNotFound
	}
	if err != nil {
		return domain.CrawlJob{}, fmt.Errorf("querying crawl job: %w", err)
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT url, status, title, error, doc_length, links_found, duration_ms, fetched_at
		 FROM crawl_job_pages WHERE job_id = `+r.dialect.Placeholder(1)+` ORDER BY id ASC`, id)
	if err != nil {
		return domain.CrawlJob{}, fmt.Errorf("querying crawl job pages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		ev, err := scanCrawlPageEvent(rows)
		if err != nil {
			return domain.CrawlJob{}, fmt.Errorf("scanning crawl job page: %w", err)
		}
		job.Pages = append(job.Pages, ev)
	}
	if err := rows.Err(); err != nil {
		return domain.CrawlJob{}, err
	}
	return job, nil
}

// List returns every retained job's summary (no per-page detail -- see
// Get for that), most recently created first.
func (r *Repository) List(ctx context.Context) ([]domain.CrawlJobSummary, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+crawlJobColumns+` FROM crawl_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying crawl jobs: %w", err)
	}
	defer rows.Close()

	out := []domain.CrawlJobSummary{}
	for rows.Next() {
		job, err := scanCrawlJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning crawl job: %w", err)
		}
		out = append(out, domain.CrawlJobSummary{
			ID: job.ID, Request: job.Request, Status: job.Status,
			PagesCrawled: job.PagesCrawled, Error: job.Error,
			CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		})
	}
	return out, rows.Err()
}

// PruneCrawlJobs deletes every crawl job beyond the maxRetained most
// recently created, cascading to their crawl_job_pages rows -- called
// periodically by cmd/crawl's own maintenance ticker (mirroring the
// PageRank recompute ticker), not part of ports.CrawlJobStore itself,
// since it's a maintenance operation rather than something the HTTP
// handler ever needs to trigger directly.
func (r *Repository) PruneCrawlJobs(ctx context.Context, maxRetained int) error {
	if maxRetained <= 0 {
		return nil
	}
	deleteSQL := r.ph(`DELETE FROM crawl_jobs WHERE id NOT IN (
	                      SELECT id FROM (
	                        SELECT id FROM crawl_jobs ORDER BY created_at DESC LIMIT %s
	                      ) AS keep
	                    )`, 1)
	if _, err := r.db.ExecContext(ctx, deleteSQL, maxRetained); err != nil {
		return fmt.Errorf("pruning crawl jobs: %w", err)
	}
	return nil
}

func scanCrawlJob(row scanner) (domain.CrawlJob, error) {
	var job domain.CrawlJob
	var reqJSON, status, createdAt string
	var startedAt, finishedAt sql.NullString
	if err := row.Scan(&job.ID, &reqJSON, &status, &job.PagesCrawled, &job.Error,
		&createdAt, &startedAt, &finishedAt); err != nil {
		return domain.CrawlJob{}, err
	}
	if err := json.Unmarshal([]byte(reqJSON), &job.Request); err != nil {
		return domain.CrawlJob{}, fmt.Errorf("decoding crawl job request: %w", err)
	}
	job.Status = domain.CrawlJobStatus(status)
	if t, err := time.Parse(crawledAtLayout, createdAt); err == nil {
		job.CreatedAt = t
	}
	if startedAt.Valid {
		if t, err := time.Parse(crawledAtLayout, startedAt.String); err == nil {
			job.StartedAt = &t
		}
	}
	if finishedAt.Valid {
		if t, err := time.Parse(crawledAtLayout, finishedAt.String); err == nil {
			job.FinishedAt = &t
		}
	}
	return job, nil
}

func scanCrawlPageEvent(row scanner) (domain.CrawlPageEvent, error) {
	var ev domain.CrawlPageEvent
	var status, fetchedAt string
	if err := row.Scan(&ev.URL, &status, &ev.Title, &ev.Error,
		&ev.DocLength, &ev.LinksFound, &ev.DurationMs, &fetchedAt); err != nil {
		return domain.CrawlPageEvent{}, err
	}
	ev.Status = domain.CrawlPageStatus(status)
	if t, err := time.Parse(crawledAtLayout, fetchedAt); err == nil {
		ev.FetchedAt = t
	}
	return ev, nil
}
