package sqlrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// documentJobColumns lists document_jobs' columns in the fixed order every
// query/scan here uses, EXCLUDING data -- List/Get never pull the (possibly
// large) blob just to show metadata; see GetData for that.
const documentJobColumns = "id, filename, content_type, size, source, index_vocabulary, status, error, doc_id, created_at, started_at, finished_at"

// Create persists a new Document job in domain.DocumentJobQueued status
// along with its raw bytes.
func (r *Repository) CreateDocumentJob(ctx context.Context, filename, contentType string, size int64, source domain.DocumentJobSource, indexVocabulary bool, data []byte) (domain.DocumentJob, error) {
	job := domain.DocumentJob{
		ID: domain.NewDocumentJobID(), Filename: filename, ContentType: contentType, Size: size,
		Source: source, IndexVocabulary: indexVocabulary, Status: domain.DocumentJobQueued,
		CreatedAt: time.Now().UTC(),
	}
	insertSQL := r.ph(`INSERT INTO document_jobs (`+documentJobColumns+`, data) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13)
	_, err := r.db.ExecContext(ctx, insertSQL,
		job.ID, job.Filename, job.ContentType, job.Size, string(job.Source), job.IndexVocabulary,
		string(job.Status), job.Error, job.DocID, job.CreatedAt.Format(crawledAtLayout), nil, nil, data,
	)
	if err != nil {
		return domain.DocumentJob{}, fmt.Errorf("creating document job: %w", err)
	}
	return job, nil
}

func (r *Repository) MarkDocumentJobRunning(ctx context.Context, id string) error {
	updateSQL := r.ph(`UPDATE document_jobs SET status = %s, started_at = %s WHERE id = %s`, 1, 2, 3)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.DocumentJobRunning), time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking document job %s running: %w", id, err)
	}
	return nil
}

func (r *Repository) MarkDocumentJobDone(ctx context.Context, id, docID string) error {
	updateSQL := r.ph(`UPDATE document_jobs SET status = %s, doc_id = %s, finished_at = %s WHERE id = %s`, 1, 2, 3, 4)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.DocumentJobDone), docID, time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking document job %s done: %w", id, err)
	}
	return nil
}

func (r *Repository) MarkDocumentJobFailed(ctx context.Context, id string, failErr error) error {
	updateSQL := r.ph(`UPDATE document_jobs SET status = %s, error = %s, finished_at = %s WHERE id = %s`, 1, 2, 3, 4)
	_, err := r.db.ExecContext(ctx, updateSQL, string(domain.DocumentJobFailed), failErr.Error(), time.Now().UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking document job %s failed: %w", id, err)
	}
	return nil
}

// GetDocumentJob returns domain.ErrDocumentJobNotFound if no job with this
// ID exists.
func (r *Repository) GetDocumentJob(ctx context.Context, id string) (domain.DocumentJob, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT `+documentJobColumns+` FROM document_jobs WHERE id = %s`, 1), id)
	job, err := scanDocumentJob(row)
	if err == sql.ErrNoRows {
		return domain.DocumentJob{}, domain.ErrDocumentJobNotFound
	}
	if err != nil {
		return domain.DocumentJob{}, fmt.Errorf("querying document job: %w", err)
	}
	return job, nil
}

// GetDocumentJobData returns id's raw uploaded/imported bytes and content
// type -- separate from GetDocumentJob so a list/detail view never has to
// pull a potentially large blob just to show metadata.
func (r *Repository) GetDocumentJobData(ctx context.Context, id string) ([]byte, string, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT data, content_type FROM document_jobs WHERE id = %s`, 1), id)
	var data []byte
	var contentType string
	if err := row.Scan(&data, &contentType); err == sql.ErrNoRows {
		return nil, "", domain.ErrDocumentJobNotFound
	} else if err != nil {
		return nil, "", fmt.Errorf("querying document job data: %w", err)
	}
	return data, contentType, nil
}

// ListDocumentJobs returns every retained job, most recently created first.
func (r *Repository) ListDocumentJobs(ctx context.Context) ([]domain.DocumentJob, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+documentJobColumns+` FROM document_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying document jobs: %w", err)
	}
	defer rows.Close()

	out := []domain.DocumentJob{}
	for rows.Next() {
		job, err := scanDocumentJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning document job: %w", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// DeleteDocumentJob removes the job and its stored bytes, plus -- if it
// finished indexing one -- the resulting Document itself (postings,
// embeddings, links, versions all cascade with it, via DeleteDocument).
// ports.ErrDocumentNotFound from that step is tolerated, not propagated --
// the indexed document may have already been removed independently (e.g.
// a content-dedup merge, or a direct delete from the Documents page), and
// that's not a reason to block deleting the job itself.
func (r *Repository) DeleteDocumentJob(ctx context.Context, id string) error {
	job, err := r.GetDocumentJob(ctx, id)
	if err != nil {
		return err
	}
	if job.DocID != "" {
		if err := r.DeleteDocument(ctx, job.DocID); err != nil && !errors.Is(err, ports.ErrDocumentNotFound) {
			return fmt.Errorf("deleting document job %s's indexed document: %w", id, err)
		}
	}
	if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM document_jobs WHERE id = %s`, 1), id); err != nil {
		return fmt.Errorf("deleting document job %s: %w", id, err)
	}
	return nil
}

func scanDocumentJob(row scanner) (domain.DocumentJob, error) {
	var job domain.DocumentJob
	var source, status, createdAt string
	var startedAt, finishedAt sql.NullString
	if err := row.Scan(&job.ID, &job.Filename, &job.ContentType, &job.Size, &source, &job.IndexVocabulary,
		&status, &job.Error, &job.DocID, &createdAt, &startedAt, &finishedAt); err != nil {
		return domain.DocumentJob{}, err
	}
	job.Source = domain.DocumentJobSource(source)
	job.Status = domain.DocumentJobStatus(status)
	job.CreatedAt = parseCrawledAt(createdAt)
	job.StartedAt = parseNullableCrawledAt(startedAt)
	job.FinishedAt = parseNullableCrawledAt(finishedAt)
	return job, nil
}
