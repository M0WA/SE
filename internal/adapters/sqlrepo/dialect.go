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
	UpsertChatVisionSettingsSQL() string
	// SeedContentDedupLockSQL atomically inserts content_dedup_lock's
	// sentinel row (id=1, in_progress=false) if missing -- a
	// SELECT-then-INSERT would race the same way CreateSchemaSQL's doc
	// comment describes, so this is one insert-or-noop statement per dialect.
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
func (sqliteDialect) UpsertChatVisionSettingsSQL() string {
	return `INSERT INTO chat_vision_settings (id, similarity_enabled, similarity_provider_id, caption_enabled, caption_base_url, caption_api_key, caption_model, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	        ON CONFLICT(id) DO UPDATE SET
	          similarity_enabled=excluded.similarity_enabled, similarity_provider_id=excluded.similarity_provider_id,
	          caption_enabled=excluded.caption_enabled, caption_base_url=excluded.caption_base_url,
	          caption_api_key=excluded.caption_api_key, caption_model=excluded.caption_model,
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
		// documents.url has existed since this table's first release, so
		// (unlike links.to_id below) this index needs no ALTER-based
		// migration to add the column itself -- just the ensureIndex
		// backstop in migrate() for a database that predates the index.
		// Used both for direct url lookups and, since this change, as the
		// join key insertLinksBatch/ResolvePendingLinks use to resolve a
		// link's target in a batch-scoped join rather than a full scan.
		`CREATE INDEX IF NOT EXISTS idx_documents_url ON documents(url)`,
		`CREATE TABLE IF NOT EXISTS postings (
			term TEXT NOT NULL, doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			term_freq INTEGER NOT NULL, PRIMARY KEY (term, doc_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_postings_term ON postings(term)`,
		// doc_id is only the trailing column of postings' (term, doc_id)
		// key, so a per-doc_id DELETE (every re-crawl, in SaveDocument)
		// can't seek it directly -- confirmed via EXPLAIN ANALYZE taking
		// 600ms+ on a 9.7M-row production table. This index makes it direct.
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
		// backend (domain.ChatEndpoint) -- unlike embedding_http_endpoints'
		// list of many providers, this is one sentinel row
		// (id = chatEndpointRowID) upserted in place, not a growing table.
		`CREATE TABLE IF NOT EXISTS chat_endpoint (
			id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
			api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
			rag_result_count INTEGER NOT NULL DEFAULT 0,
			max_context_tokens INTEGER NOT NULL DEFAULT 0,
			web_search_enabled BOOLEAN NOT NULL DEFAULT false,
			web_search_base_url TEXT NOT NULL DEFAULT '',
			web_search_result_count INTEGER NOT NULL DEFAULT 20,
			system_prompt TEXT NOT NULL DEFAULT '',
			default_agent_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// chat_vision_settings holds the single admin-configured
		// domain.ChatVisionSettings row (id = chatVisionRowID) -- same
		// sentinel-row shape as chat_endpoint, for the same reason (one
		// active configuration). Deliberately its own table, not new
		// columns on chat_endpoint or embedding_http_endpoints: both
		// Similarity and Caption are independent of search's embedding
		// endpoints and of the chat completion endpoint, even when
		// Similarity ends up referencing the same underlying model as
		// search (see domain.ChatVisionSettings.SimilarityProviderID).
		`CREATE TABLE IF NOT EXISTS chat_vision_settings (
			id TEXT PRIMARY KEY,
			similarity_enabled BOOLEAN NOT NULL DEFAULT false,
			similarity_provider_id TEXT NOT NULL DEFAULT '',
			caption_enabled BOOLEAN NOT NULL DEFAULT false,
			caption_base_url TEXT NOT NULL DEFAULT '',
			caption_api_key TEXT NOT NULL DEFAULT '',
			caption_model TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// mcp_servers lists admin-configured MCP server connections
		// (domain.MCPServer) -- grows like embedding_http_endpoints, unlike
		// chat_endpoint's sentinel row. Replaces the old chat_hooks table
		// (removed in favor of real MCP tool-calling); an already-deployed
		// chat_hooks table is left in place, untouched (see repository.go's
		// migrate() for why no DROP TABLE).
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, transport TEXT NOT NULL,
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false
		)`,
		// agents holds admin-defined domain.Agent rows: Description for a
		// planner/picker, SystemPrompt injected into the agent's own
		// conversation, plus an optional MCPServerIDs allow-list
		// (JSON []string, same convention as mcp_servers.args) scoping
		// which mcp_servers rows it may use.
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			system_prompt TEXT NOT NULL DEFAULT '', mcp_server_ids TEXT NOT NULL DEFAULT '[]',
			enabled BOOLEAN NOT NULL DEFAULT true
		)`,
		// content_dedup_lock is a single sentinel row (id=1) whose
		// in_progress flag TryAcquireContentDedupLock/ReleaseContentDedupLock
		// claim/clear via a conditional UPDATE -- a real DB row so every
		// process sees it (see ports.ContentDedupRepository). Seeded once at
		// migration time (migrateDocumentColumns), since CREATE TABLE alone
		// leaves no row for the UPDATE to match.
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
		// document_jobs is crawl_jobs' single-file sibling for the admin
		// "Document" upload feature (see domain.DocumentJob) -- always
		// exactly one file, so unlike crawl_jobs there's no per-page child
		// table. data holds the raw uploaded/imported bytes inline, same
		// convention as uploaded_files.data, so an image can be previewed
		// and text content re-extracted without re-uploading. doc_id is the
		// resulting documents.id once indexed, empty until then.
		`CREATE TABLE IF NOT EXISTS document_jobs (
			id TEXT PRIMARY KEY, filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0, data BLOB NOT NULL,
			source TEXT NOT NULL DEFAULT 'upload', index_vocabulary BOOLEAN NOT NULL DEFAULT true,
			status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', doc_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_jobs_created_at ON document_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin', user_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		// users lists every account (domain.User) -- there is no separate
		// hardcoded admin account; is_admin marks which rows also get
		// /admin/* access. username is unique at the DB layer, not just
		// checked-then-inserted at the application layer, to close that race.
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL, is_admin BOOLEAN NOT NULL DEFAULT false,
			custom_prompt TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		// user_mcp_servers is mcp_servers' per-user sibling -- same
		// domain.MCPServer fields, but owned by user_id (cascade-deleted
		// with it) with (user_id, id) as the primary key, since two users
		// may each mint a server called "web-tools" (ports.UserMCPServerStore
		// only checks uniqueness per owner). transport is restricted to
		// "http" -- "stdio" (real command execution) is never handed to a
		// non-admin user (see ports.UserMCPServerStore). Created after
		// users since its FK needs that table to exist -- SQLite doesn't
		// check this at CREATE TABLE time, but Postgres/MySQL do.
		`CREATE TABLE IF NOT EXISTS user_mcp_servers (
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			id TEXT NOT NULL, name TEXT NOT NULL, transport TEXT NOT NULL DEFAULT 'http',
			command TEXT NOT NULL DEFAULT '', args TEXT NOT NULL DEFAULT '[]',
			base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true, prompt TEXT NOT NULL DEFAULT '',
			gated_by_web_search BOOLEAN NOT NULL DEFAULT false,
			PRIMARY KEY (user_id, id)
		)`,
		// chats holds domain.PersistedChat rows -- a chat a user pinned to
		// persist across reloads, owned by (cascade-deleted with) user_id,
		// id a random opaque token (randomChatID) like uploaded_files.id.
		// history is the full transcript, JSON-encoded, not a normalized
		// per-message table -- always read/written whole. Created before
		// uploaded_files since its chat_id FK needs this table to exist first.
		`CREATE TABLE IF NOT EXISTS chats (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
			history TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chats_user_id ON chats(user_id)`,
		// uploaded_files is user_mcp_servers' sibling for domain.UploadedFile
		// -- owned by (cascade-deleted with) user_id, id a random opaque
		// token (randomFileID), not a name-derived slug, since it appears
		// directly in a download URL where unguessability matters like a
		// session token. data holds the file's raw bytes inline in this
		// shared database -- no separate blob store. chat_id ties a file to
		// the chat it was produced during -- NULLable so a file created
		// before chats existed doesn't fail the FK ('' can't match a real
		// chats.id, NULL is exempt). Its ON DELETE CASCADE only actually
		// fires under Postgres; SQLite doesn't enforce FKs here, so
		// Repository.DeleteChat deletes a chat's files explicitly instead
		// of relying on it.
		`CREATE TABLE IF NOT EXISTS uploaded_files (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			chat_id TEXT REFERENCES chats(id) ON DELETE CASCADE,
			filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0, data BLOB NOT NULL,
			created_at TEXT NOT NULL
		)`,
		// idx_uploaded_files_chat_id is deliberately NOT listed here -- a
		// column added via a later ALTER TABLE can't get its index created
		// unconditionally in this static list (see
		// ensureUploadedFilesChatIDIndex's doc comment).
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_user_id ON uploaded_files(user_id)`,
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
func (mysqlDialect) UpsertChatVisionSettingsSQL() string {
	return `INSERT INTO chat_vision_settings (id, similarity_enabled, similarity_provider_id, caption_enabled, caption_base_url, caption_api_key, caption_model, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	        ON DUPLICATE KEY UPDATE
	          similarity_enabled=VALUES(similarity_enabled), similarity_provider_id=VALUES(similarity_provider_id),
	          caption_enabled=VALUES(caption_enabled), caption_base_url=VALUES(caption_base_url),
	          caption_api_key=VALUES(caption_api_key), caption_model=VALUES(caption_model),
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
		// See the sqlite dialect's idx_documents_url comment. url is TEXT
		// (unbounded) in MySQL, unlike VARCHAR(n) columns elsewhere in this
		// schema -- MySQL requires an explicit prefix length to index a
		// TEXT/BLOB column at all ("BLOB/TEXT column ... used in key
		// specification without a key length" otherwise); 255 mirrors the
		// prefix length MySQL itself defaults an implicit index to for a
		// VARCHAR(255) column, ample for a host+path prefix to disambiguate
		// on already-indexed hosts.
		`CREATE INDEX idx_documents_url ON documents(url(255))`,
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
			web_search_result_count INT NOT NULL DEFAULT 20,
			system_prompt TEXT NOT NULL,
			default_agent_id TEXT NOT NULL,
			updated_at VARCHAR(64) NOT NULL
		) ENGINE=InnoDB`,
		// See the sqlite dialect's chat_vision_settings comment.
		`CREATE TABLE IF NOT EXISTS chat_vision_settings (
			id VARCHAR(20) PRIMARY KEY,
			similarity_enabled BOOLEAN NOT NULL DEFAULT false,
			similarity_provider_id VARCHAR(20) NOT NULL DEFAULT '',
			caption_enabled BOOLEAN NOT NULL DEFAULT false,
			caption_base_url TEXT NOT NULL,
			caption_api_key TEXT NOT NULL,
			caption_model VARCHAR(255) NOT NULL,
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
		// See the sqlite dialect's document_jobs comment.
		`CREATE TABLE IF NOT EXISTS document_jobs (
			id VARCHAR(64) PRIMARY KEY, filename VARCHAR(255) NOT NULL, content_type VARCHAR(255) NOT NULL DEFAULT '',
			size INT NOT NULL DEFAULT 0, data LONGBLOB NOT NULL,
			source VARCHAR(16) NOT NULL DEFAULT 'upload', index_vocabulary BOOLEAN NOT NULL DEFAULT true,
			status VARCHAR(32) NOT NULL, error TEXT NOT NULL, doc_id VARCHAR(64) NOT NULL DEFAULT '',
			created_at VARCHAR(64) NOT NULL, started_at VARCHAR(64), finished_at VARCHAR(64)
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_document_jobs_created_at ON document_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token VARCHAR(64) PRIMARY KEY, expires_at VARCHAR(64) NOT NULL,
			role VARCHAR(16) NOT NULL DEFAULT 'admin', user_id VARCHAR(20) NOT NULL DEFAULT ''
		) ENGINE=InnoDB`,
		`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
		// See the sqlite dialect's users comment.
		`CREATE TABLE IF NOT EXISTS users (
			id VARCHAR(20) PRIMARY KEY, username VARCHAR(255) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL, is_admin BOOLEAN NOT NULL DEFAULT false,
			custom_prompt TEXT NOT NULL DEFAULT '',
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
		// VARCHAR(32) (16-byte random token, hex-encoded), unlike other
		// VARCHAR(20) slug-derived ids. chat_id is NULLable (see that
		// comment for why).
		`CREATE TABLE IF NOT EXISTS uploaded_files (
			id VARCHAR(32) NOT NULL, user_id VARCHAR(20) NOT NULL, chat_id VARCHAR(32),
			filename VARCHAR(255) NOT NULL, content_type VARCHAR(255) NOT NULL DEFAULT '',
			size INT NOT NULL DEFAULT 0, data LONGBLOB NOT NULL,
			created_at VARCHAR(64) NOT NULL,
			PRIMARY KEY (id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
		) ENGINE=InnoDB`,
		// idx_uploaded_files_chat_id is deliberately NOT listed here --
		// see ensureUploadedFilesChatIDIndex's own doc comment.
		`CREATE INDEX idx_uploaded_files_user_id ON uploaded_files(user_id)`,
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
func (postgresDialect) UpsertChatVisionSettingsSQL() string {
	return `INSERT INTO chat_vision_settings (id, similarity_enabled, similarity_provider_id, caption_enabled, caption_base_url, caption_api_key, caption_model, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	        ON CONFLICT (id) DO UPDATE SET
	          similarity_enabled=EXCLUDED.similarity_enabled, similarity_provider_id=EXCLUDED.similarity_provider_id,
	          caption_enabled=EXCLUDED.caption_enabled, caption_base_url=EXCLUDED.caption_base_url,
	          caption_api_key=EXCLUDED.caption_api_key, caption_model=EXCLUDED.caption_model,
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
		// See the sqlite dialect's idx_documents_url comment.
		`CREATE INDEX IF NOT EXISTS idx_documents_url ON documents(url)`,
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
			web_search_result_count INT NOT NULL DEFAULT 20,
			system_prompt TEXT NOT NULL DEFAULT '',
			default_agent_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		// See the sqlite dialect's chat_vision_settings comment.
		`CREATE TABLE IF NOT EXISTS chat_vision_settings (
			id TEXT PRIMARY KEY,
			similarity_enabled BOOLEAN NOT NULL DEFAULT false,
			similarity_provider_id TEXT NOT NULL DEFAULT '',
			caption_enabled BOOLEAN NOT NULL DEFAULT false,
			caption_base_url TEXT NOT NULL DEFAULT '',
			caption_api_key TEXT NOT NULL DEFAULT '',
			caption_model TEXT NOT NULL DEFAULT '',
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
		// See the sqlite dialect's document_jobs comment.
		`CREATE TABLE IF NOT EXISTS document_jobs (
			id TEXT PRIMARY KEY, filename TEXT NOT NULL, content_type TEXT NOT NULL DEFAULT '',
			size INT NOT NULL DEFAULT 0, data BYTEA NOT NULL,
			source TEXT NOT NULL DEFAULT 'upload', index_vocabulary BOOLEAN NOT NULL DEFAULT true,
			status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', doc_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_document_jobs_created_at ON document_jobs(created_at)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY, expires_at TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin', user_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		// See the sqlite dialect's users comment.
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL, is_admin BOOLEAN NOT NULL DEFAULT false,
			custom_prompt TEXT NOT NULL DEFAULT '',
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
		// idx_uploaded_files_chat_id is deliberately NOT listed here --
		// see ensureUploadedFilesChatIDIndex's own doc comment.
		`CREATE INDEX IF NOT EXISTS idx_uploaded_files_user_id ON uploaded_files(user_id)`,
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
