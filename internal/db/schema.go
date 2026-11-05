package db

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	email TEXT UNIQUE,
	name TEXT NOT NULL,
	password_hash TEXT NOT NULL DEFAULT '',
	is_admin INTEGER NOT NULL DEFAULT 0,
	disabled INTEGER NOT NULL DEFAULT 0,
	session_epoch INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tokens (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
	token TEXT UNIQUE NOT NULL,
	name TEXT NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL DEFAULT 0,
	hint TEXT,
	last_used_at INTEGER
);

CREATE TABLE IF NOT EXISTS uploads (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT UNIQUE NOT NULL,
	owner_account_id INTEGER NOT NULL REFERENCES accounts(id),
	owner_token_id INTEGER REFERENCES tokens(id),
	filename TEXT NOT NULL,
	size INTEGER NOT NULL,
	visibility TEXT NOT NULL DEFAULT 'restricted',
	password_hash TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS upload_versions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	object_key TEXT NOT NULL,
	size INTEGER NOT NULL,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	UNIQUE(upload_id, version)
);

CREATE TABLE IF NOT EXISTS comments (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	element_selector TEXT NOT NULL,
	element_text TEXT NOT NULL DEFAULT '',
	anchor_kind TEXT NOT NULL DEFAULT '',
	author_name TEXT NOT NULL,
	author_cookie TEXT NOT NULL,
	body TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 1,
	account_id INTEGER,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_comments_upload ON comments(upload_id);

CREATE TABLE IF NOT EXISTS visits (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	visitor_cookie TEXT NOT NULL,
	visitor_name TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL DEFAULT 1,
	visited_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_visits_upload ON visits(upload_id);

CREATE TABLE IF NOT EXISTS visitors (
	cookie TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	last_seen INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_identities (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	provider TEXT NOT NULL,
	provider_user_id TEXT NOT NULL,
	email TEXT NOT NULL,
	name TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(provider, provider_user_id)
);

CREATE INDEX IF NOT EXISTS idx_oauth_account ON oauth_identities(account_id);

CREATE TABLE IF NOT EXISTS invites (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	token_hash TEXT UNIQUE NOT NULL,
	token_cipher TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL,
	created_by_account_id INTEGER NOT NULL REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used_at INTEGER NOT NULL DEFAULT 0,
	revoked_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_invites_email ON invites(email);

CREATE TABLE IF NOT EXISTS cli_login_devices (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	device_hash TEXT UNIQUE NOT NULL,
	user_code TEXT UNIQUE NOT NULL,
	status TEXT NOT NULL,
	account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	consumed_at INTEGER NOT NULL DEFAULT 0,
	request_ip TEXT NOT NULL DEFAULT '',
	request_user_agent TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log(created_at DESC);

CREATE TABLE IF NOT EXISTS upload_grants (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('user','group')),
	email TEXT NOT NULL,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	UNIQUE(upload_id, kind, email)
);
CREATE INDEX IF NOT EXISTS idx_upload_grants_upload ON upload_grants(upload_id);

CREATE TABLE IF NOT EXISTS upload_slug_aliases (
	slug TEXT PRIMARY KEY,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_upload_slug_aliases_upload ON upload_slug_aliases(upload_id);
CREATE TABLE IF NOT EXISTS upload_backends (
	upload_id INTEGER NOT NULL,
	version INTEGER NOT NULL,
	image TEXT NOT NULL,
	port INTEGER NOT NULL DEFAULT 8080,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	PRIMARY KEY (upload_id, version),
	FOREIGN KEY (upload_id, version) REFERENCES upload_versions(upload_id, version) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS upload_backend_env (
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (upload_id, name)
);
CREATE TABLE IF NOT EXISTS provisioners (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	key_hash TEXT NOT NULL UNIQUE,
	hint TEXT,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	last_used_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS provisioned_accounts (
	provisioner_id INTEGER NOT NULL REFERENCES provisioners(id) ON DELETE CASCADE,
	external_id TEXT NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	created_account INTEGER NOT NULL DEFAULT 0,
	token_id INTEGER REFERENCES tokens(id) ON DELETE SET NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (provisioner_id, external_id),
	UNIQUE (provisioner_id, account_id)
);
CREATE INDEX IF NOT EXISTS idx_provisioned_accounts_token ON provisioned_accounts(token_id);
CREATE TABLE IF NOT EXISTS upload_backend_settings (
	upload_id INTEGER PRIMARY KEY REFERENCES uploads(id) ON DELETE CASCADE,
	allow_token_calls INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS backend_token_calls (
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	calls INTEGER NOT NULL DEFAULT 0,
	last_call_at INTEGER NOT NULL,
	last_status INTEGER NOT NULL,
	identified INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (upload_id, version, account_id)
);
CREATE INDEX IF NOT EXISTS idx_backend_token_calls_upload ON backend_token_calls(upload_id, last_call_at DESC);
`
