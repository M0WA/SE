package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type Repository struct {
	db      *sql.DB
	dialect Dialect
}

func New(ctx context.Context, driverName, dsn string) (*Repository, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening DB (%s): %w", driverName, err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("DB ping (%s): %w", driverName, err)
	}

	repo := &Repository{db: db, dialect: NewDialect(driverName)}
	if err := repo.migrate(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

func NewWithDB(db *sql.DB, driverName string) *Repository {
	return &Repository{db: db, dialect: NewDialect(driverName)}
}

func (r *Repository) migrate(ctx context.Context) error {
	for _, stmt := range r.dialect.CreateSchemaSQL() {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) SaveDocument(ctx context.Context, doc domain.Document, embedding []float32) error {
	tokens := domain.Tokenize(doc.Title + " " + doc.Text)
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("serializing embedding: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentSQL(),
		doc.ID, doc.URL, doc.Title, doc.Text, len(tokens), string(embJSON),
	); err != nil {
		return fmt.Errorf("saving document: %w", err)
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM postings WHERE doc_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("deleting old postings: %w", err)
	}

	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	insertSQL := r.ph(`INSERT INTO postings (term, doc_id, term_freq) VALUES (%s, %s, %s)`, 1, 2, 3)
	for term, freq := range counts {
		if _, err := tx.ExecContext(ctx, insertSQL, term, doc.ID, freq); err != nil {
			return fmt.Errorf("saving posting: %w", err)
		}
	}

	return tx.Commit()
}

func (r *Repository) PostingsForTerm(ctx context.Context, term string) ([]domain.PostingStats, error) {
	totalDocs, avgDocLen, err := r.CorpusStats(ctx)
	if err != nil {
		return nil, err
	}

	var docFreq int
	dfQuery := r.ph(`SELECT COUNT(*) FROM postings WHERE term = %s`, 1)
	if err := r.db.QueryRowContext(ctx, dfQuery, term).Scan(&docFreq); err != nil {
		return nil, fmt.Errorf("querying doc freq: %w", err)
	}
	if docFreq == 0 {
		return nil, nil
	}

	query := r.ph(`SELECT p.doc_id, p.term_freq, d.doc_length
	               FROM postings p JOIN documents d ON d.id = p.doc_id
	               WHERE p.term = %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, term)
	if err != nil {
		return nil, fmt.Errorf("querying postings: %w", err)
	}
	defer rows.Close()

	var out []domain.PostingStats
	for rows.Next() {
		var s domain.PostingStats
		if err := rows.Scan(&s.DocID, &s.TermFreq, &s.DocLength); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		s.DocFreq = docFreq
		s.TotalDocs = totalDocs
		s.AvgDocLen = avgDocLen
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) CorpusStats(ctx context.Context) (int, float64, error) {
	var totalDocs int
	var avgLen sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), AVG(doc_length) FROM documents`).Scan(&totalDocs, &avgLen)
	if err != nil {
		return 0, 0, fmt.Errorf("querying corpus stats: %w", err)
	}
	if totalDocs == 0 || !avgLen.Valid {
		return totalDocs, 1, nil
	}
	return totalDocs, avgLen.Float64, nil
}

func (r *Repository) AllEmbeddings(ctx context.Context) (map[string][]float32, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, embedding FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("querying embeddings: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]float32)
	for rows.Next() {
		var id, embJSON string
		if err := rows.Scan(&id, &embJSON); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		var vec []float32
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			return nil, fmt.Errorf("deserializing embedding (%s): %w", id, err)
		}
		out[id] = vec
	}
	return out, rows.Err()
}

func (r *Repository) DocumentByID(ctx context.Context, docID string) (domain.Document, error) {
	query := r.ph(`SELECT id, url, title, text FROM documents WHERE id = %s`, 1)
	var doc domain.Document
	err := r.db.QueryRowContext(ctx, query, docID).Scan(&doc.ID, &doc.URL, &doc.Title, &doc.Text)
	if err != nil {
		return domain.Document{}, fmt.Errorf("loading document (%s): %w", docID, err)
	}
	return doc, nil
}

// DeleteDocument removes a document and its postings (the postings table's
// foreign key cascades the delete across all three dialects' schemas).
func (r *Repository) DeleteDocument(ctx context.Context, docID string) error {
	query := r.ph(`DELETE FROM documents WHERE id = %s`, 1)
	res, err := r.db.ExecContext(ctx, query, docID)
	if err != nil {
		return fmt.Errorf("deleting document (%s): %w", docID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking delete result (%s): %w", docID, err)
	}
	if n == 0 {
		return ports.ErrDocumentNotFound
	}
	return nil
}

func (r *Repository) ListDocuments(ctx context.Context, limit int) ([]domain.IndexedDocument, error) {
	query := r.ph(`SELECT id, url, title, doc_length FROM documents ORDER BY id LIMIT %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying documents: %w", err)
	}
	defer rows.Close()

	var out []domain.IndexedDocument
	for rows.Next() {
		var d domain.IndexedDocument
		if err := rows.Scan(&d.ID, &d.URL, &d.Title, &d.DocLength); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) ph(template string, positions ...int) string {
	args := make([]interface{}, len(positions))
	for i, pos := range positions {
		args[i] = r.dialect.Placeholder(pos)
	}
	return fmt.Sprintf(template, args...)
}
