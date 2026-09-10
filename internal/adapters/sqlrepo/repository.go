package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"searchengine/internal/domain"
)

type Repository struct {
	db      *sql.DB
	dialect Dialect
}

func New(ctx context.Context, driverName, dsn string) (*Repository, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("DB öffnen (%s): %w", driverName, err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("DB Ping (%s): %w", driverName, err)
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
			return fmt.Errorf("Migration fehlgeschlagen: %w", err)
		}
	}
	return nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) SaveDocument(ctx context.Context, doc domain.Document, embedding []float32) error {
	tokens := domain.Tokenize(doc.Title + " " + doc.Text)
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("Embedding serialisieren: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("Transaktion starten: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentSQL(),
		doc.ID, doc.URL, doc.Title, doc.Text, len(tokens), string(embJSON),
	); err != nil {
		return fmt.Errorf("Dokument speichern: %w", err)
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM postings WHERE doc_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("alte Postings löschen: %w", err)
	}

	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	insertSQL := r.ph(`INSERT INTO postings (term, doc_id, term_freq) VALUES (%s, %s, %s)`, 1, 2, 3)
	for term, freq := range counts {
		if _, err := tx.ExecContext(ctx, insertSQL, term, doc.ID, freq); err != nil {
			return fmt.Errorf("Posting speichern: %w", err)
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
		return nil, fmt.Errorf("DocFreq abfragen: %w", err)
	}
	if docFreq == 0 {
		return nil, nil
	}

	query := r.ph(`SELECT p.doc_id, p.term_freq, d.doc_length
	               FROM postings p JOIN documents d ON d.id = p.doc_id
	               WHERE p.term = %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, term)
	if err != nil {
		return nil, fmt.Errorf("Postings abfragen: %w", err)
	}
	defer rows.Close()

	var out []domain.PostingStats
	for rows.Next() {
		var s domain.PostingStats
		if err := rows.Scan(&s.DocID, &s.TermFreq, &s.DocLength); err != nil {
			return nil, fmt.Errorf("Zeile scannen: %w", err)
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
		return 0, 0, fmt.Errorf("Corpus-Statistik abfragen: %w", err)
	}
	if totalDocs == 0 || !avgLen.Valid {
		return totalDocs, 1, nil
	}
	return totalDocs, avgLen.Float64, nil
}

func (r *Repository) AllEmbeddings(ctx context.Context) (map[string][]float32, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, embedding FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("Embeddings abfragen: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]float32)
	for rows.Next() {
		var id, embJSON string
		if err := rows.Scan(&id, &embJSON); err != nil {
			return nil, fmt.Errorf("Zeile scannen: %w", err)
		}
		var vec []float32
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			return nil, fmt.Errorf("Embedding deserialisieren (%s): %w", id, err)
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
		return domain.Document{}, fmt.Errorf("Dokument laden (%s): %w", docID, err)
	}
	return doc, nil
}

func (r *Repository) ph(template string, positions ...int) string {
	args := make([]interface{}, len(positions))
	for i, pos := range positions {
		args[i] = r.dialect.Placeholder(pos)
	}
	return fmt.Sprintf(template, args...)
}
