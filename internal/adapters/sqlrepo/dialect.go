package sqlrepo

import "strconv"

type Dialect interface {
	Name() string
	Placeholder(argPosition int) string
	UpsertDocumentSQL() string
	UpsertDocumentEmbeddingSQL() string
	UpsertSettingSQL() string
	UpsertDocumentAliasSQL() string
	UpsertChatEndpointSQL() string
	// SeedContentDedupLockSQL atomically inserts content_dedup_lock's one
	// sentinel row (id=1, in_progress=false) if it isn't already there --
	// a plain SELECT-then-INSERT would have the exact same
	// multiple-processes-racing-at-startup problem CreateSchemaSQL's own
	// doc comment describes, so this is a single insert-or-noop statement
	// per dialect instead.
	SeedContentDedupLockSQL() string
	CreateSchemaSQL() []string
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string             { return "sqlite" }
func (sqliteDialect) Placeholder(_ int) string { return "?" }
func (sqliteDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at, content_hash, simhash)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON CONFLICT(id) DO UPDATE SET
	          url=excluded.url, title=excluded.title, text=excluded.text,
	          doc_length=excluded.doc_length, embedding=excluded.embedding,
	          norm_embedding=excluded.norm_embedding, pagerank=excluded.pagerank,
	          host=excluded.host, version=excluded.version, crawled_at=excluded.crawled_at,
	          content_hash=excluded.content_hash, simhash=excluded.simhash`
}
func (sqliteDialect) UpsertDocumentEmbeddingSQL() string {
	return `INSERT INTO document_embeddings (doc_id, provider, embedding, norm_embedding) VALUES (?, ?, ?, ?)
	        ON CONFLICT(doc_id, provider) DO UPDATE SET
	          embedding=excluded.embedding, norm_embedding=excluded.norm_embedding`
}
func (sqliteDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES (?, ?, ?)
	        ON CONFLICT(setting_key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`
}
func (sqliteDialect) UpsertDocumentAliasSQL() string {
	return `INSERT INTO document_aliases (alias_url, canonical_id, reason, created_at, host) VALUES (?, ?, ?, ?, ?)
	        ON CONFLICT(alias_url) DO UPDATE SET
	          canonical_id=excluded.canonical_id, reason=excluded.reason, created_at=excluded.created_at, host=excluded.host`
}
func (sqliteDialect) UpsertChatEndpointSQL() string {
	return `INSERT INTO chat_endpoint (id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, system_prompt, default_agent_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON CONFLICT(id) DO UPDATE SET
	          base_url=excluded.base_url, api_key=excluded.api_key, model=excluded.model,
	          enabled=excluded.enabled, rag_enabled=excluded.rag_enabled,
	          rag_result_count=excluded.rag_result_count, max_context_tokens=excluded.max_context_tokens,
	          web_search_enabled=excluded.web_search_enabled, web_search_base_url=excluded.web_search_base_url,
	          web_search_result_count=excluded.web_search_result_count,
	          system_prompt=excluded.system_prompt,
	          default_agent_id=excluded.default_agent_id,
	          updated_at=excluded.updated_at`
}
func (sqliteDialect) SeedContentDedupLockSQL() string {
	return `INSERT OR IGNORE INTO content_dedup_lock (id, in_progress) VALUES (1, false)`
}
func (sqliteDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INTEGER NOT NULL, embedding BLOB NOT NULL,
			norm_embedding REAL NOT NULL DEFAULT 0,
			pagerank REAL NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
			crawled_at TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '', simhash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INTEGER NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
		// doc_id is the trailing column of postings' own (term, doc_id)
		// primary key, so "DELETE FROM postings WHERE doc_id = ?" (every
		// re-crawl of an existing page, in SaveDocument) can't seek that
		// index directly -- confirmed via EXPLAIN ANALYZE on production
		// taking 600ms+ on a 9.7M-row table. This index makes it a direct
		// index lookup instead.
		`CREATE INDEX IF NOT EXISTS idx_postings_doc_id ON postings(doc_id)`,
		`CREATE TABLE IF NOT EXISTS document_aliases (
			alias_url TEXT PRIMARY KEY, canonical_id TEXT NOT NULL,
			reason TEXT NOT NULL, created_at TEXT NOT NULL, host TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_aliases_canonical_id ON document_aliases(canonical_id)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			version INTEGER NOT NULL, title TEXT, text TEXT,
			doc_length INTEGER NOT NULL, crawled_at TEXT NOT NULL,
			PRIMARY KEY (doc_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS document_embeddings (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			provider TEXT NOT NULL, embedding BLOB NOT NULL,
			norm_embedding REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (doc_id, provider)
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
			respect_robots BOOLEAN, user_agent TEXT,
			cookie TEXT NOT NULL DEFAULT '', basic_auth_user TEXT NOT NULL DEFAULT '', basic_auth_pass TEXT NOT NULL DEFAULT '',
			link_scope TEXT NOT NULL DEFAULT '',
			allowed_domains TEXT NOT NULL DEFAULT '[]', blocked_domains TEXT NOT NULL DEFAULT '[]', follow_indexed_domains BOOLEAN NOT NULL DEFAULT false,
			use_sitemap BOOLEAN, interval_minutes INTEGER NOT NULL,
			fetch_timeout_seconds INTEGER NOT NULL DEFAULT 0, min_text_length INTEGER NOT NULL DEFAULT 0,
			crawl_delay_ms INTEGER NOT NULL DEFAULT 0, max_response_kb INTEGER NOT NULL DEFAULT 0,
			prioritize_unindexed BOOLEAN NOT NULL DEFAULT false, recurring BOOLEAN NOT NULL DEFAULT true, max_runs INTEGER NOT NULL DEFAULT 0, run_count INTEGER NOT NULL DEFAULT 0, renderer TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, in_progress BOOLEAN NOT NULL DEFAULT false, job_id TEXT NOT NULL DEFAULT '', last_run_at TEXT,
			next_run_at TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS embedding_http_endpoints (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
			dimensions INTEGER NOT NULL, rate_limit_per_second REAL NOT NULL DEFAULT 0,
			enabled BOOLEAN NOT NULL DEFAULT true,
			chunk_size_tokens INTEGER NOT NULL DEFAULT 0, tokenize_url TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		// chat_endpoint holds the single admin-configured chat-completions
		// backend (see domain.ChatEndpoint) -- unlike
		// embedding_http_endpoints (a list of many blended providers), chat
		// only ever has one active configuration, kept as a single sentinel
		// row (id = the fixed value chatEndpointRowID) upserted in place
		// rather than a growing table.
		`CREATE TABLE IF NOT EXISTS chat_endpoint (
			id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
			rag_result_count INTEGER NOT NULL DEFAULT 0,
			max_context_tokens INTEGER NOT NULL DEFAULT 0,
			web_search_enabled BOOLEAN NOT NULL DEFAULT false,
			web_search_base_url TEXT NOT NULL DEFAULT '',
			web_search_result_count INTEGER NOT NULL DEFAULT 0,
			system_prompt TEXT NOT NULL DEFAULT '',
			default_agent_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// mcp_servers is a list of many admin-configured MCP (Model Context
		// Protocol) server connections (see domain.MCPServer) -- unlike
		// chat_endpoint's single sentinel row, this grows the same way
		// embedding_http_endpoints does. Replaces the old chat_hooks table
		// (one-row-per-script tool hooks, removed in favor of real MCP
		// tool-calling) -- an already-deployed instance's own chat_hooks
		// table and any rows in it are left physically in place, untouched
		// and unused, rather than dropped (see repository.go's migrate()
		// doc comments for why a real DROP TABLE isn't done here).
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, transport TEXT NOT NULL,
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false
		)`,
		// agents holds admin-defined domain.Agent rows -- a named
		// specialization (Description for a planner/picker to reason
		// about, SystemPrompt actually injected into the agent's own
		// conversation) plus an optional MCPServerIDs allow-list scoping
		// which of the global mcp_servers rows it may use (JSON-encoded
		// []string, same convention as mcp_servers.args).
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			system_prompt TEXT NOT NULL DEFAULT '', mcp_server_ids TEXT NOT NULL DEFAULT '[]',
			enabled BOOLEAN NOT NULL DEFAULT true
		)`,
		// content_dedup_lock is a single sentinel row (id = 1) whose
		// in_progress flag TryAcquireContentDedupLock/ReleaseContentDedupLock
		// claim/clear via a conditional UPDATE -- see ports.
		// ContentDedupRepository's doc comment for why this needs to be a
		// real DB row (visible to every process) rather than an in-memory
		// bool. The row is seeded once at migration time (see
		// migrateDocumentColumns), not here, since CREATE TABLE alone
		// leaves it empty and the conditional UPDATE has no row to match
		// against otherwise.
		`CREATE TABLE IF NOT EXISTS content_dedup_lock (
			id INTEGER PRIMARY KEY, in_progress BOOLEAN NOT NULL DEFAULT false
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
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin', user_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		// users is a list of many DB-backed regular-user accounts (see
		// domain.User) -- distinct from the single hardcoded admin account,
		// which is never a row here. Unlike chat_hooks, username must be
		// unique (enforced at the DB layer, not just checked-then-inserted
		// at the application layer, to close the race between the two).
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL, custom_prompt TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		// user_mcp_servers is mcp_servers' self-service, per-user sibling --
		// same domain.MCPServer field set, but every row is owned by one
		// user_id (cascade-deleted with its owner) and (user_id, id) is the
		// primary key rather than id alone, since two different users may
		// each independently mint a server they call "web-tools" (see
		// domain.NewMCPServerID -- ports.UserMCPServerStore only checks
		// uniqueness within one owner's own rows). transport defaults to,
		// and restapi's validation enforces, "http" only -- see
		// ports.UserMCPServerStore's own doc comment for why "stdio" (real
		// local command execution) can never be handed to a regular,
		// non-admin user. Created after users (not alongside mcp_servers
		// above) since its FK needs that table to already exist -- SQLite
		// doesn't check at CREATE TABLE time, but Postgres/MySQL do.
		`CREATE TABLE IF NOT EXISTS user_mcp_servers (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			id TEXT NOT NULL, name TEXT NOT NULL, transport TEXT NOT NULL DEFAULT 'http',
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false,
			PRIMARY KEY (user_id, id)
		)`,
		// chats holds domain.PersistedChat rows -- a chat a user explicitly
		// pinned to persist across reloads, owned by (cascade-deleted
		// with) one user_id, id a random opaque token (see sqlrepo's
		// randomFileID/randomChatID) for the same reason uploaded_files.id
		// is. history is the full turn-by-turn transcript, JSON-encoded
		// (same convention as mcp_servers.args) rather than a normalized
		// per-message table -- always read/written as one whole document,
		// never queried by individual message. Created before
		// uploaded_files (not alongside user_mcp_servers above) since that
		// table's own chat_id FK needs this one to already exist.
		`CREATE TABLE IF NOT EXISTS chats (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
			history TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chats_user_id ON chats(user_id)`,
		// uploaded_files is user_mcp_servers' sibling for domain.UploadedFile
		// -- every row owned by (cascade-deleted with) one user_id, id a
		// random opaque token (see sqlrepo's randomFileID) rather than a
		// name-derived slug, since a filename is never unique enough to
		// safely reuse as an ID and this ID appears directly in a download
		// URL, where unguessability matters the same way a session token's
		// does. data holds the file's raw bytes directly in this shared
		// database, the same tier of "just another row" every other piece
		// of this app's state already gets -- no separate blob store to
		// stand up or back up independently. chat_id ties a file to the
		// domain.PersistedChat it was attached/produced during --
		// NULLable (not NOT NULL DEFAULT '') specifically so a file
		// created before chats could be pinned, with no chat to point at,
		// doesn't fail this foreign key ('' would never match a real
		// chats.id and NULL is the only value a FK constraint exempts
		// from the check) -- scanned back into
		// domain.UploadedFile.ChatID as "". The "ON DELETE CASCADE" here
		// only actually fires under Postgres (which always enforces its
		// own foreign keys) -- SQLite enforces one only when a
		// connection has run "PRAGMA foreign_keys = ON", which this
		// package's connections never do, so Repository.DeleteChat
		// deletes a chat's files explicitly itself rather than relying
		// on this constraint; it's kept for Postgres' own referential
		// integrity and as documentation of the real relationship, not
		// as the actual cross-dialect cleanup mechanism.
		`CREATE TABLE IF NOT EXISTS uploaded_files (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			chat_id TEXT REFERENCES chats(id) ON DELETE CASCADE,
			filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0, data BLOB NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_user_id ON uploaded_files(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_chat_id ON uploaded_files(chat_id)`,
	}
}

type mysqlDialect struct{}

func (mysqlDialect) Name() string             { return "mysql" }
func (mysqlDialect) Placeholder(_ int) string { return "?" }
func (mysqlDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at, content_hash, simhash)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE
	          url=VALUES(url), title=VALUES(title), text=VALUES(text),
	          doc_length=VALUES(doc_length), embedding=VALUES(embedding),
	          norm_embedding=VALUES(norm_embedding), pagerank=VALUES(pagerank),
	          host=VALUES(host), version=VALUES(version), crawled_at=VALUES(crawled_at),
	          content_hash=VALUES(content_hash), simhash=VALUES(simhash)`
}
func (mysqlDialect) UpsertDocumentEmbeddingSQL() string {
	return `INSERT INTO document_embeddings (doc_id, provider, embedding, norm_embedding) VALUES (?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE embedding=VALUES(embedding), norm_embedding=VALUES(norm_embedding)`
}
func (mysqlDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES (?, ?, ?)
	        ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at=VALUES(updated_at)`
}
func (mysqlDialect) UpsertDocumentAliasSQL() string {
	return `INSERT INTO document_aliases (alias_url, canonical_id, reason, created_at, host) VALUES (?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE canonical_id=VALUES(canonical_id), reason=VALUES(reason), created_at=VALUES(created_at), host=VALUES(host)`
}
func (mysqlDialect) UpsertChatEndpointSQL() string {
	return `INSERT INTO chat_endpoint (id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, system_prompt, default_agent_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE
	          base_url=VALUES(base_url), api_key=VALUES(api_key), model=VALUES(model),
	          enabled=VALUES(enabled), rag_enabled=VALUES(rag_enabled),
	          rag_result_count=VALUES(rag_result_count), max_context_tokens=VALUES(max_context_tokens),
	          web_search_enabled=VALUES(web_search_enabled), web_search_base_url=VALUES(web_search_base_url),
	          web_search_result_count=VALUES(web_search_result_count),
	          system_prompt=VALUES(system_prompt),
	          default_agent_id=VALUES(default_agent_id),
	          updated_at=VALUES(updated_at)`
}
func (mysqlDialect) SeedContentDedupLockSQL() string {
	return `INSERT IGNORE INTO content_dedup_lock (id, in_progress) VALUES (1, false)`
}
func (mysqlDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id VARCHAR(64) PRIMARY KEY, url TEXT NOT NULL, title TEXT, text LONGTEXT,
			doc_length INT NOT NULL, embedding LONGBLOB NOT NULL,
			norm_embedding DOUBLE NOT NULL DEFAULT 0,
			pagerank DOUBLE NOT NULL DEFAULT 0,
			host VARCHAR(255) NOT NULL DEFAULT '', version INT NOT NULL DEFAULT 1,
			crawled_at VARCHAR(64) NOT NULL DEFAULT '',
			content_hash VARCHAR(64) NOT NULL DEFAULT '', simhash VARCHAR(16) NOT NULL DEFAULT ''
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS postings (
			term VARCHAR(128) NOT NULL, doc_id VARCHAR(64) NOT NULL,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_postings_term ON postings(term)`,
		// See the sqlite dialect's idx_postings_doc_id comment: doc_id is
		// only the trailing column of postings' primary key, so a
		// per-document DELETE can't seek it directly without this index.
		`CREATE INDEX idx_postings_doc_id ON postings(doc_id)`,
		`CREATE TABLE IF NOT EXISTS document_aliases (
			alias_url VARCHAR(767) PRIMARY KEY, canonical_id VARCHAR(64) NOT NULL,
			reason VARCHAR(32) NOT NULL, created_at VARCHAR(64) NOT NULL, host VARCHAR(255) NOT NULL DEFAULT ''
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_document_aliases_canonical_id ON document_aliases(canonical_id)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id VARCHAR(64) NOT NULL, version INT NOT NULL,
			title TEXT, text LONGTEXT, doc_length INT NOT NULL, crawled_at VARCHAR(64) NOT NULL,
			PRIMARY KEY (doc_id, version),
			FOREIGN KEY (doc_id) REFERENCES documents(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS document_embeddings (
			doc_id VARCHAR(64) NOT NULL, provider VARCHAR(32) NOT NULL,
			embedding LONGBLOB NOT NULL, norm_embedding DOUBLE NOT NULL DEFAULT 0,
			PRIMARY KEY (doc_id, provider),
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
			respect_robots BOOLEAN, user_agent VARCHAR(255),
			cookie TEXT, basic_auth_user VARCHAR(255) NOT NULL DEFAULT '', basic_auth_pass VARCHAR(255) NOT NULL DEFAULT '',
			link_scope VARCHAR(16) NOT NULL DEFAULT '',
			allowed_domains TEXT NOT NULL DEFAULT '[]', blocked_domains TEXT NOT NULL DEFAULT '[]', follow_indexed_domains BOOLEAN NOT NULL DEFAULT false,
			use_sitemap BOOLEAN, interval_minutes INT NOT NULL,
			fetch_timeout_seconds INT NOT NULL DEFAULT 0, min_text_length INT NOT NULL DEFAULT 0,
			crawl_delay_ms INT NOT NULL DEFAULT 0, max_response_kb INT NOT NULL DEFAULT 0,
			prioritize_unindexed BOOLEAN NOT NULL DEFAULT false, recurring BOOLEAN NOT NULL DEFAULT true, max_runs INT NOT NULL DEFAULT 0, run_count INT NOT NULL DEFAULT 0, renderer VARCHAR(32) NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, in_progress BOOLEAN NOT NULL DEFAULT false, job_id VARCHAR(64) NOT NULL DEFAULT '', last_run_at VARCHAR(64),
			next_run_at VARCHAR(64) NOT NULL, created_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		`CREATE TABLE IF NOT EXISTS embedding_http_endpoints (
			id VARCHAR(20) PRIMARY KEY, name VARCHAR(255) NOT NULL, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL, model VARCHAR(255) NOT NULL,
			dimensions INT NOT NULL, rate_limit_per_second DOUBLE NOT NULL DEFAULT 0,
			enabled BOOLEAN NOT NULL DEFAULT true,
			chunk_size_tokens INT NOT NULL DEFAULT 0, tokenize_url TEXT NOT NULL DEFAULT '',
			created_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		// See the sqlite dialect's chat_endpoint comment: a single sentinel
		// row (id = chatEndpointRowID), upserted in place, not a growing
		// table.
		`CREATE TABLE IF NOT EXISTS chat_endpoint (
			id VARCHAR(20) PRIMARY KEY, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL, model VARCHAR(255) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
			rag_result_count INT NOT NULL DEFAULT 0,
			max_context_tokens INT NOT NULL DEFAULT 0,
			web_search_enabled BOOLEAN NOT NULL DEFAULT false,
			web_search_base_url TEXT NOT NULL,
			web_search_result_count INT NOT NULL DEFAULT 0,
			system_prompt TEXT NOT NULL,
			default_agent_id TEXT NOT NULL,
			updated_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		// See the sqlite dialect's mcp_servers comment.
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id VARCHAR(20) PRIMARY KEY, name VARCHAR(255) NOT NULL, transport VARCHAR(20) NOT NULL,
			command TEXT NOT NULL, args TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL,
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false
		) ENGINE=InnoDB`,
		// See the sqlite dialect's own agents table comment.
		`CREATE TABLE IF NOT EXISTS agents (
			id VARCHAR(20) PRIMARY KEY, name VARCHAR(255) NOT NULL, description TEXT NOT NULL,
			system_prompt TEXT NOT NULL, mcp_server_ids TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true
		) ENGINE=InnoDB`,
		// See the sqlite dialect's content_dedup_lock comment.
		`CREATE TABLE IF NOT EXISTS content_dedup_lock (
			id INT PRIMARY KEY, in_progress BOOLEAN NOT NULL DEFAULT false
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
			token VARCHAR(64) PRIMARY KEY, expires_at VARCHAR(64) NOT NULL,
			role VARCHAR(16) NOT NULL DEFAULT 'admin', user_id VARCHAR(20) NOT NULL DEFAULT ''
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
		// See the sqlite dialect's users comment.
		`CREATE TABLE IF NOT EXISTS users (
			id VARCHAR(20) PRIMARY KEY, username VARCHAR(255) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL, custom_prompt TEXT NOT NULL DEFAULT '',
			created_at VARCHAR(64) NOT NULL, updated_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		// See the sqlite dialect's user_mcp_servers comment.
		`CREATE TABLE IF NOT EXISTS user_mcp_servers (
			user_id VARCHAR(20) NOT NULL, id VARCHAR(20) NOT NULL, name VARCHAR(255) NOT NULL,
			transport VARCHAR(20) NOT NULL DEFAULT 'http',
			command TEXT NOT NULL, args TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL,
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false,
			PRIMARY KEY (user_id, id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		// See the sqlite dialect's chats comment. id is VARCHAR(32) (a
		// 16-byte random token, hex-encoded), like uploaded_files.id.
		`CREATE TABLE IF NOT EXISTS chats (
			id VARCHAR(32) NOT NULL, user_id VARCHAR(20) NOT NULL,
			title VARCHAR(255) NOT NULL, agent_id VARCHAR(20) NOT NULL DEFAULT '',
			history LONGTEXT NOT NULL,
			created_at VARCHAR(64) NOT NULL, updated_at VARCHAR(64) NOT NULL,
			PRIMARY KEY (id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_chats_user_id ON chats(user_id)`,
		// See the sqlite dialect's uploaded_files comment. id is
		// VARCHAR(32) (a 16-byte random token, hex-encoded), unlike
		// user_id/other slug-derived ids' VARCHAR(20). chat_id is
		// NULLable (see the sqlite dialect's own note on why).
		`CREATE TABLE IF NOT EXISTS uploaded_files (
			id VARCHAR(32) NOT NULL, user_id VARCHAR(20) NOT NULL, chat_id VARCHAR(32),
			filename VARCHAR(255) NOT NULL, content_type VARCHAR(255) NOT NULL DEFAULT '',
			size INT NOT NULL DEFAULT 0, data LONGBLOB NOT NULL,
			created_at VARCHAR(64) NOT NULL,
			PRIMARY KEY (id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_uploaded_files_user_id ON uploaded_files(user_id)`,
		`CREATE INDEX idx_uploaded_files_chat_id ON uploaded_files(chat_id)`,
	}
}

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }
func (postgresDialect) Placeholder(pos int) string {
	return "$" + strconv.Itoa(pos)
}
func (postgresDialect) UpsertDocumentSQL() string {
	return `INSERT INTO documents (id, url, title, text, doc_length, embedding, norm_embedding, pagerank, host, version, crawled_at, content_hash, simhash)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	        ON CONFLICT (id) DO UPDATE SET
	          url=EXCLUDED.url, title=EXCLUDED.title, text=EXCLUDED.text,
	          doc_length=EXCLUDED.doc_length, embedding=EXCLUDED.embedding,
	          norm_embedding=EXCLUDED.norm_embedding, pagerank=EXCLUDED.pagerank,
	          host=EXCLUDED.host, version=EXCLUDED.version, crawled_at=EXCLUDED.crawled_at,
	          content_hash=EXCLUDED.content_hash, simhash=EXCLUDED.simhash`
}
func (postgresDialect) UpsertDocumentEmbeddingSQL() string {
	return `INSERT INTO document_embeddings (doc_id, provider, embedding, norm_embedding) VALUES ($1, $2, $3, $4)
	        ON CONFLICT (doc_id, provider) DO UPDATE SET
	          embedding=EXCLUDED.embedding, norm_embedding=EXCLUDED.norm_embedding`
}
func (postgresDialect) UpsertSettingSQL() string {
	return `INSERT INTO app_settings (setting_key, value, updated_at) VALUES ($1, $2, $3)
	        ON CONFLICT (setting_key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`
}
func (postgresDialect) UpsertDocumentAliasSQL() string {
	return `INSERT INTO document_aliases (alias_url, canonical_id, reason, created_at, host) VALUES ($1, $2, $3, $4, $5)
	        ON CONFLICT (alias_url) DO UPDATE SET
	          canonical_id=EXCLUDED.canonical_id, reason=EXCLUDED.reason, created_at=EXCLUDED.created_at, host=EXCLUDED.host`
}
func (postgresDialect) UpsertChatEndpointSQL() string {
	return `INSERT INTO chat_endpoint (id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, system_prompt, default_agent_id, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	        ON CONFLICT (id) DO UPDATE SET
	          base_url=EXCLUDED.base_url, api_key=EXCLUDED.api_key, model=EXCLUDED.model,
	          enabled=EXCLUDED.enabled, rag_enabled=EXCLUDED.rag_enabled,
	          rag_result_count=EXCLUDED.rag_result_count, max_context_tokens=EXCLUDED.max_context_tokens,
	          web_search_enabled=EXCLUDED.web_search_enabled, web_search_base_url=EXCLUDED.web_search_base_url,
	          web_search_result_count=EXCLUDED.web_search_result_count,
	          system_prompt=EXCLUDED.system_prompt,
	          default_agent_id=EXCLUDED.default_agent_id,
	          updated_at=EXCLUDED.updated_at`
}
func (postgresDialect) SeedContentDedupLockSQL() string {
	return `INSERT INTO content_dedup_lock (id, in_progress) VALUES (1, false) ON CONFLICT (id) DO NOTHING`
}
func (postgresDialect) CreateSchemaSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
			doc_length INT NOT NULL, embedding BYTEA NOT NULL,
			norm_embedding DOUBLE PRECISION NOT NULL DEFAULT 0,
			pagerank DOUBLE PRECISION NOT NULL DEFAULT 0,
			host TEXT NOT NULL DEFAULT '', version INT NOT NULL DEFAULT 1,
			crawled_at TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '', simhash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INT NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
		// See the sqlite dialect's idx_postings_doc_id comment: doc_id is
		// only the trailing column of postings' primary key, so a
		// per-document DELETE can't seek it directly without this index.
		`CREATE INDEX IF NOT EXISTS idx_postings_doc_id ON postings(doc_id)`,
		`CREATE TABLE IF NOT EXISTS document_aliases (
			alias_url TEXT PRIMARY KEY, canonical_id TEXT NOT NULL,
			reason TEXT NOT NULL, created_at TEXT NOT NULL, host TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_aliases_canonical_id ON document_aliases(canonical_id)`,
		`CREATE TABLE IF NOT EXISTS document_versions (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			version INT NOT NULL, title TEXT, text TEXT,
			doc_length INT NOT NULL, crawled_at TEXT NOT NULL,
			PRIMARY KEY (doc_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS document_embeddings (
			doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			provider TEXT NOT NULL, embedding BYTEA NOT NULL,
			norm_embedding DOUBLE PRECISION NOT NULL DEFAULT 0,
			PRIMARY KEY (doc_id, provider)
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
			respect_robots BOOLEAN, user_agent TEXT,
			cookie TEXT NOT NULL DEFAULT '', basic_auth_user TEXT NOT NULL DEFAULT '', basic_auth_pass TEXT NOT NULL DEFAULT '',
			link_scope TEXT NOT NULL DEFAULT '',
			allowed_domains TEXT NOT NULL DEFAULT '[]', blocked_domains TEXT NOT NULL DEFAULT '[]', follow_indexed_domains BOOLEAN NOT NULL DEFAULT false,
			use_sitemap BOOLEAN, interval_minutes INT NOT NULL,
			fetch_timeout_seconds INT NOT NULL DEFAULT 0, min_text_length INT NOT NULL DEFAULT 0,
			crawl_delay_ms INT NOT NULL DEFAULT 0, max_response_kb INT NOT NULL DEFAULT 0,
			prioritize_unindexed BOOLEAN NOT NULL DEFAULT false, recurring BOOLEAN NOT NULL DEFAULT true, max_runs INTEGER NOT NULL DEFAULT 0, run_count INTEGER NOT NULL DEFAULT 0, renderer TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, in_progress BOOLEAN NOT NULL DEFAULT false, job_id TEXT NOT NULL DEFAULT '', last_run_at TEXT,
			next_run_at TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS embedding_http_endpoints (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
			dimensions INT NOT NULL, rate_limit_per_second DOUBLE PRECISION NOT NULL DEFAULT 0,
			enabled BOOLEAN NOT NULL DEFAULT true,
			chunk_size_tokens INT NOT NULL DEFAULT 0, tokenize_url TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		// See the sqlite dialect's chat_endpoint comment: a single sentinel
		// row (id = chatEndpointRowID), upserted in place, not a growing
		// table.
		`CREATE TABLE IF NOT EXISTS chat_endpoint (
			id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
			rag_result_count INT NOT NULL DEFAULT 0,
			max_context_tokens INT NOT NULL DEFAULT 0,
			web_search_enabled BOOLEAN NOT NULL DEFAULT false,
			web_search_base_url TEXT NOT NULL DEFAULT '',
			web_search_result_count INT NOT NULL DEFAULT 0,
			system_prompt TEXT NOT NULL DEFAULT '',
			default_agent_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// See the sqlite dialect's mcp_servers comment.
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, transport TEXT NOT NULL,
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false
		)`,
		// See the sqlite dialect's own agents table comment.
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			system_prompt TEXT NOT NULL DEFAULT '', mcp_server_ids TEXT NOT NULL DEFAULT '[]',
			enabled BOOLEAN NOT NULL DEFAULT true
		)`,
		// See the sqlite dialect's content_dedup_lock comment.
		`CREATE TABLE IF NOT EXISTS content_dedup_lock (
			id INT PRIMARY KEY, in_progress BOOLEAN NOT NULL DEFAULT false
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
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin', user_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		// See the sqlite dialect's users comment.
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL, custom_prompt TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		// See the sqlite dialect's user_mcp_servers comment.
		`CREATE TABLE IF NOT EXISTS user_mcp_servers (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			id TEXT NOT NULL, name TEXT NOT NULL, transport TEXT NOT NULL DEFAULT 'http',
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false,
			PRIMARY KEY (user_id, id)
		)`,
		// See the sqlite dialect's chats comment.
		`CREATE TABLE IF NOT EXISTS chats (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
			history TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chats_user_id ON chats(user_id)`,
		// See the sqlite dialect's uploaded_files comment. chat_id is
		// NULLable (see that same comment for why).
		`CREATE TABLE IF NOT EXISTS uploaded_files (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			chat_id TEXT REFERENCES chats(id) ON DELETE CASCADE,
			filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			size INT NOT NULL DEFAULT 0, data BYTEA NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_user_id ON uploaded_files(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_chat_id ON uploaded_files(chat_id)`,
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
