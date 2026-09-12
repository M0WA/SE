package sqlrepo

import "strconv"

type Dialect interface {
	Name() string
	Placeholder(argPosition int) string
	UpsertDocumentSQL() string
	UpsertSettingSQL() string
	CreateSchemaSQL() []string
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string             { return "sqlite" }
func (sqliteDialect) Placeholder(_ int) string { return "?" }
func (sqliteDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON CONFLICT(id) DO UPDATE SET
	          url=excluded.url, title=excluded.title, text=excluded.text,
	          doc_length=excluded.doc_length, embedding=excluded.embedding,
	          norm_embedding=excluded.norm_embedding, pagerank=excluded.pagerank,
	          host=excluded.host, version=excluded.version, crawled_at=excluded.crawled_at`
}
func (sqliteDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES (?, ?, ?)
	        ON CONFLICT(setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`
}
func (sqliteDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INTEGER NOT NULL, embedding TEXT NOT NULL,
			norm_embedding REAL NOT NULL DEFAULT 0,
			pagerank REAL NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
			crawled_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INTEGER NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			version INTEGER NOT NULL, title TEXT, text TEXT,
			doc_length INTEGER NOT NULL, crawled_at TEXT NOT NULL,
			PRIMARY KEY (doc_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS links (
			from_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			to_url TEXT NOT NULL, to_host TEXT NOT NULL,
			PRIMARY KEY (from_id, to_url)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_links_to_url ON links(to_url)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			setting_key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS scheduled_crawls (
			id TEXT PRIMARY KEY, seed_urls TEXT NOT NULL, max_pages INTEGER NOT NULL,
			respect_robots BOOLEAN, user_agent TEXT, allow_off_domain_links BOOLEAN,
			use_sitemap BOOLEAN, interval_minutes INTEGER NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true, last_run_at TEXT,
			next_run_at TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS crawl_jobs (
			id TEXT PRIMARY KEY, request TEXT NOT NULL, status TEXT NOT NULL,
			pages_crawled INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_jobs_created_at ON crawl_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS crawl_job_pages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT NOT NULL REFERENCES crawl_jobs(id) ON DELETE CASCADE,
			url TEXT NOT NULL, status TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '', doc_length INTEGER NOT NULL DEFAULT 0,
			links_found INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
			fetched_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_job_pages_job_id ON crawl_job_pages(job_id)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
	}
}

type mysqlDialect struct{}

func (mysqlDialect) Name() string             { return "mysql" }
func (mysqlDialect) Placeholder(_ int) string { return "?" }
func (mysqlDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE
	          url=VALUES(url), title=VALUES(title), text=VALUES(text),
	          doc_length=VALUES(doc_length), embedding=VALUES(embedding),
	          norm_embedding=VALUES(norm_embedding), pagerank=VALUES(pagerank),
	          host=VALUES(host), version=VALUES(version), crawled_at=VALUES(crawled_at)`
}
func (mysqlDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES (?, ?, ?)
	        ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at=VALUES(updated_at)`
}
func (mysqlDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id VARCHAR(64) PRIMARY KEY, url TEXT NOT NULL, title TEXT, text LONGTEXT,
			doc_length INT NOT NULL, embedding LONGTEXT NOT NULL,
			norm_embedding DOUBLE NOT NULL DEFAULT 0,
			pagerank DOUBLE NOT NULL DEFAULT 0,
			host VARCHAR(255) NOT NULL DEFAULT '', version INT NOT NULL DEFAULT 1,
			crawled_at VARCHAR(64) NOT NULL DEFAULT ''
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS postings (
			term VARCHAR(128) NOT NULL, doc_id VARCHAR(64) NOT NULL,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_postings_term ON postings(term)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id VARCHAR(64) NOT NULL, version INT NOT NULL,
			title TEXT, text LONGTEXT, doc_length INT NOT NULL, crawled_at VARCHAR(64) NOT NULL,
			PRIMARY KEY (doc_id, version),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS links (
			from_id VARCHAR(64) NOT NULL, to_url VARCHAR(767) NOT NULL, to_host VARCHAR(255) NOT NULL,
			PRIMARY KEY (from_id, to_url),
			FOREIGN KEY (from_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_links_to_url ON links(to_url)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			setting_key VARCHAR(64) PRIMARY KEY, value LONGTEXT NOT NULL, updated_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS scheduled_crawls (
			id VARCHAR(64) PRIMARY KEY, seed_urls TEXT NOT NULL, max_pages INT NOT NULL,
			respect_robots BOOLEAN, user_agent VARCHAR(255), allow_off_domain_links BOOLEAN,
			use_sitemap BOOLEAN, interval_minutes INT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true, last_run_at VARCHAR(64),
			next_run_at VARCHAR(64) NOT NULL, created_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS crawl_jobs (
			id VARCHAR(64) PRIMARY KEY, request LONGTEXT NOT NULL, status VARCHAR(32) NOT NULL,
			pages_crawled INT NOT NULL DEFAULT 0, error TEXT NOT NULL,
			created_at VARCHAR(64) NOT NULL, started_at VARCHAR(64), finished_at VARCHAR(64)
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_crawl_jobs_created_at ON crawl_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS crawl_job_pages (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			job_id VARCHAR(64) NOT NULL, url TEXT NOT NULL, status VARCHAR(32) NOT NULL,
			title TEXT NOT NULL, error TEXT NOT NULL, doc_length INT NOT NULL DEFAULT 0,
			links_found INT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0,
			fetched_at VARCHAR(64) NOT NULL,
			FOREIGN KEY (job_id) REFERENCES crawl_jobs(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_crawl_job_pages_job_id ON crawl_job_pages(job_id)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token VARCHAR(64) PRIMARY KEY, expires_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
	}
}

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }
func (postgresDialect) Placeholder(pos int) string {
	return "$" + strconv.Itoa(pos)
}
func (postgresDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	        ON CONFLICT (id) DO UPDATE SET
	          url=EXCLUDED.url, title=EXCLUDED.title, text=EXCLUDED.text,
	          doc_length=EXCLUDED.doc_length, embedding=EXCLUDED.embedding,
	          norm_embedding=EXCLUDED.norm_embedding, pagerank=EXCLUDED.pagerank,
	          host=EXCLUDED.host, version=EXCLUDED.version, crawled_at=EXCLUDED.crawled_at`
}
func (postgresDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES ($1, $2, $3)
	        ON CONFLICT (setting_key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`
}
func (postgresDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INT NOT NULL, embedding TEXT NOT NULL,
			norm_embedding DOUBLE PRECISION NOT NULL DEFAULT 0,
			pagerank DOUBLE PRECISION NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '', version INT NOT NULL DEFAULT 1,
			crawled_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			version INT NOT NULL, title TEXT, text TEXT,
			doc_length INT NOT NULL, crawled_at TEXT NOT NULL,
			PRIMARY KEY (doc_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS links (
			from_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			to_url TEXT NOT NULL, to_host TEXT NOT NULL,
			PRIMARY KEY (from_id, to_url)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_links_to_url ON links(to_url)`,
		`CREATE TABLE IF NOT EXISTS app_settings (
			setting_key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS scheduled_crawls (
			id TEXT PRIMARY KEY, seed_urls TEXT NOT NULL, max_pages INT NOT NULL,
			respect_robots BOOLEAN, user_agent TEXT, allow_off_domain_links BOOLEAN,
			use_sitemap BOOLEAN, interval_minutes INT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true, last_run_at TEXT,
			next_run_at TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS crawl_jobs (
			id TEXT PRIMARY KEY, request TEXT NOT NULL, status TEXT NOT NULL,
			pages_crawled INT NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_jobs_created_at ON crawl_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS crawl_job_pages (
			id BIGSERIAL PRIMARY KEY,
			job_id TEXT NOT NULL REFERENCES crawl_jobs(id) ON DELETE CASCADE,
			url TEXT NOT NULL, status TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '', doc_length INT NOT NULL DEFAULT 0,
			links_found INT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0,
			fetched_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_crawl_job_pages_job_id ON crawl_job_pages(job_id)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
	}
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
