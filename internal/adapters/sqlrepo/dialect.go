package sqlrepo

type Dialect interface {
	Name() string
	Placeholder(argPosition int) string
	UpsertDocumentSQL() string
	CreateSchemaSQL() []string
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string             { return "sqlite" }
func (sqliteDialect) Placeholder(_ int) string { return "?" }
func (sqliteDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding)
	        VALUES (?, ?, ?, ?, ?, ?)
	        ON CONFLICT(id) DO UPDATE SET
	          url=excluded.url, title=excluded.title, text=excluded.text,
	          doc_length=excluded.doc_length, embedding=excluded.embedding`
}
func (sqliteDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INTEGER NOT NULL, embedding TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INTEGER NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
	}
}

type mysqlDialect struct{}

func (mysqlDialect) Name() string             { return "mysql" }
func (mysqlDialect) Placeholder(_ int) string { return "?" }
func (mysqlDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding)
	        VALUES (?, ?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE
	          url=VALUES(url), title=VALUES(title), text=VALUES(text),
	          doc_length=VALUES(doc_length), embedding=VALUES(embedding)`
}
func (mysqlDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id VARCHAR(64) PRIMARY KEY, url TEXT NOT NULL, title TEXT, text LONGTEXT,
			doc_length INT NOT NULL, embedding LONGTEXT NOT NULL
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS postings (
			term VARCHAR(128) NOT NULL, doc_id VARCHAR(64) NOT NULL,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_postings_term ON postings(term)`,
	}
}

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }
func (postgresDialect) Placeholder(pos int) string {
	return "$" + itoa(pos)
}
func (postgresDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding)
	        VALUES ($1, $2, $3, $4, $5, $6)
	        ON CONFLICT (id) DO UPDATE SET
	          url=EXCLUDED.url, title=EXCLUDED.title, text=EXCLUDED.text,
	          doc_length=EXCLUDED.doc_length, embedding=EXCLUDED.embedding`
}
func (postgresDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INT NOT NULL, embedding TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func NewDialect(driverName string) Dialect {
	switch driverName {
	case "mysql":
		return mysqlDialect{}
	case "postgres", "pgx":
		return postgresDialect{}
	default:
		return sqliteDialect{}
	}
}
