package db

import (
	"fmt"
	"strings"
	"time"
)

var migrations = []struct {
	id   int
	desc string
	sql  string
}{
	{1, "token_hashes", ""}, // handled by migrateTokenHashes (legacy compat)
	{2, "token_expiry", `-- ALTER TABLE handled idempotently below`},
	{3, "audit_log", `
CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log(created_at DESC);`},
	{4, "account_password_hash", `-- ALTER TABLE handled idempotently below`},
	{5, "comment_anchor_kind", `-- ALTER TABLE handled idempotently below`},
	{6, "upload_visibility", `-- ALTER TABLE handled idempotently below`},
	{7, "upload_versions", ""}, // handled by migrateUploadVersions
	{8, "upload_grants", `
CREATE TABLE IF NOT EXISTS upload_grants (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('user','group')),
	email TEXT NOT NULL,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	UNIQUE(upload_id, kind, email)
);
CREATE INDEX IF NOT EXISTS idx_upload_grants_upload ON upload_grants(upload_id);`},
	{9, "upload_slug_aliases", `
CREATE TABLE IF NOT EXISTS upload_slug_aliases (
	slug TEXT PRIMARY KEY,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_upload_slug_aliases_upload ON upload_slug_aliases(upload_id);`},
	{10, "token_hint_last_used", `-- ALTER TABLE handled idempotently below`},
	{11, "cli_login_request_context", `-- ALTER TABLE handled idempotently below`},
	{12, "account_session_epoch", `-- ALTER TABLE handled idempotently below`},
	{13, "comment_account_id", `-- ALTER TABLE handled idempotently below`},
	{14, "visibility_signed_in", `UPDATE uploads SET visibility='signed-in' WHERE visibility='private'`},
	{15, "drop_password_visibility", `UPDATE uploads SET visibility='restricted', password_hash='' WHERE visibility='password'`},
	{16, "upload_backends", `
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
);`},
	{17, "provisioning", `
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
CREATE INDEX IF NOT EXISTS idx_provisioned_accounts_token ON provisioned_accounts(token_id);`},
	{18, "backend_token_calls", `
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
CREATE INDEX IF NOT EXISTS idx_backend_token_calls_upload ON backend_token_calls(upload_id, last_call_at DESC);`},
}

func (s *Store) runMigrations() error {
	// Create the migrations tracking table.
	if _, err := s.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		id INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}

	// Run legacy migration first (pre-migration-system).
	if err := s.migrateTokenHashes(); err != nil {
		return err
	}
	// Mark migration 1 as applied if not already.
	if err := s.markMigrationApplied(1); err != nil {
		return err
	}

	for _, m := range migrations {
		applied, err := s.isMigrationApplied(m.id)
		if err != nil {
			return fmt.Errorf("read migration %d (%s): %w", m.id, m.desc, err)
		}
		if applied {
			continue
		}
		if m.id == 2 {
			if err := s.migrateTokenExpiry(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 4 {
			if err := s.migrateAccountPasswordHash(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 5 {
			if err := s.migrateCommentAnchorKind(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 6 {
			if err := s.migrateUploadVisibility(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 7 {
			if err := s.migrateUploadVersions(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 10 {
			if err := s.migrateTokenHintLastUsed(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 11 {
			if err := s.migrateCLILoginRequestContext(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 12 {
			if err := s.migrateAccountSessionEpoch(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.id == 13 {
			if err := s.migrateCommentAccountID(); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		} else if m.sql != "" {
			if _, err := s.Exec(m.sql); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.id, m.desc, err)
			}
		}
		if err := s.markMigrationApplied(m.id); err != nil {
			return fmt.Errorf("mark migration %d (%s): %w", m.id, m.desc, err)
		}
	}
	return nil
}

func (s *Store) isMigrationApplied(id int) (bool, error) {
	var n int
	err := s.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=?`, id).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) markMigrationApplied(id int) error {
	_, err := s.Exec(`INSERT OR IGNORE INTO schema_migrations(id, applied_at) VALUES(?, ?)`,
		id, time.Now().Unix())
	return err
}

// migrateTokenExpiry adds the expires_at column to the tokens table for
// existing installs that predate it.
func (s *Store) migrateTokenExpiry() error {
	_, err := s.Exec(`ALTER TABLE tokens ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		if isDuplicateColumnError(err) {
			return nil
		}
		return err
	}
	return nil
}

// migrateAccountPasswordHash adds optional local-login password hashes to
// accounts. Empty means the account cannot use password login.
func (s *Store) migrateAccountPasswordHash() error {
	_, err := s.Exec(`ALTER TABLE accounts ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		if isDuplicateColumnError(err) {
			return nil
		}
		return err
	}
	return nil
}

// migrateCommentAnchorKind records whether a comment is anchored to selected
// text, a whole element, or the page.
func (s *Store) migrateCommentAnchorKind() error {
	_, err := s.Exec(`ALTER TABLE comments ADD COLUMN anchor_kind TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		if isDuplicateColumnError(err) {
			return nil
		}
		return err
	}
	return nil
}

// migrateUploadVisibility records explicit page access behavior. Existing
// password hashes remain password-gated; existing passwordless rows become
// private because the new default is password-only creation.
func (s *Store) migrateUploadVisibility() error {
	_, err := s.Exec(`ALTER TABLE uploads ADD COLUMN visibility TEXT NOT NULL DEFAULT 'password'`)
	if err != nil && !isDuplicateColumnError(err) {
		return err
	}
	_, err = s.Exec(`UPDATE uploads SET visibility=CASE WHEN password_hash <> '' THEN 'password' ELSE 'private' END`)
	return err
}

// migrateUploadVersions adds per-page versions. Every existing upload gets a
// version 1 whose object key is its slug, so existing objects need no move.
// created_by_account_id stays NULL because uploads.owner_account_id may not
// exist yet on very old databases (migrateUploadsOwner runs later).
func (s *Store) migrateUploadVersions() error {
	if _, err := s.Exec(`CREATE TABLE IF NOT EXISTS upload_versions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	object_key TEXT NOT NULL,
	size INTEGER NOT NULL,
	created_by_account_id INTEGER REFERENCES accounts(id),
	created_at INTEGER NOT NULL,
	UNIQUE(upload_id, version)
)`); err != nil {
		return err
	}
	for _, table := range []string{"comments", "visits"} {
		_, err := s.Exec(`ALTER TABLE ` + table + ` ADD COLUMN version INTEGER NOT NULL DEFAULT 1`)
		if err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	_, err := s.Exec(`INSERT OR IGNORE INTO upload_versions(upload_id,version,object_key,size,created_by_account_id,created_at)
		SELECT id,1,slug,size,NULL,created_at FROM uploads`)
	return err
}

// migrateTokenHintLastUsed adds the dashboard's key hint and last-use time to
// tokens. Existing rows keep NULL for both: only hashes are stored, so their
// hints cannot be recovered.
func (s *Store) migrateTokenHintLastUsed() error {
	for _, stmt := range []string{
		`ALTER TABLE tokens ADD COLUMN hint TEXT`,
		`ALTER TABLE tokens ADD COLUMN last_used_at INTEGER`,
	} {
		if _, err := s.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

// migrateTokenHashes upgrades any legacy plaintext token rows to hashes, so an
// existing install keeps working after the move to hashed-at-rest tokens.
func (s *Store) migrateTokenHashes() error {
	rows, err := s.Query(`SELECT id, token FROM tokens`)
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		tok string
	}
	var legacy []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.tok); err != nil {
			rows.Close()
			return err
		}
		if !isHashed(r.tok) {
			legacy = append(legacy, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range legacy {
		if _, err := s.Exec(`UPDATE tokens SET token=? WHERE id=?`, HashToken(r.tok), r.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateAccounts() error {
	hasAccountID, _, err := s.columnInfo("tokens", "account_id")
	if err != nil {
		return err
	}
	if !hasAccountID {
		if _, err := s.Exec(`ALTER TABLE tokens ADD COLUMN account_id INTEGER REFERENCES accounts(id)`); err != nil {
			return err
		}
	}

	rows, err := s.Query(`SELECT id,name,is_admin FROM tokens WHERE account_id IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type tokenRow struct {
		id      int64
		name    string
		isAdmin int64
	}
	var tokens []tokenRow
	for rows.Next() {
		var tr tokenRow
		if err := rows.Scan(&tr.id, &tr.name, &tr.isAdmin); err != nil {
			return err
		}
		tokens = append(tokens, tr)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, tr := range tokens {
		now := time.Now().Unix()
		res, err := s.Exec(`INSERT INTO accounts(email,name,is_admin,disabled,created_at,updated_at) VALUES(NULL,?,?,?,?,?)`,
			tr.name, tr.isAdmin, 0, now, now)
		if err != nil {
			return err
		}
		accountID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := s.Exec(`UPDATE tokens SET account_id=? WHERE id=?`, accountID, tr.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateUploadsOwner() (err error) {
	hasOwnerAccount, _, err := s.columnInfo("uploads", "owner_account_id")
	if err != nil {
		return err
	}
	_, ownerTokenNotNull, err := s.columnInfo("uploads", "owner_token_id")
	if err != nil {
		return err
	}
	if hasOwnerAccount && !ownerTokenNotNull {
		return nil
	}

	if _, err := s.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if _, onErr := s.Exec(`PRAGMA foreign_keys=ON`); err == nil && onErr != nil {
			err = onErr
		}
	}()
	tx, err := s.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE uploads_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		owner_account_id INTEGER NOT NULL REFERENCES accounts(id),
		owner_token_id INTEGER REFERENCES tokens(id),
		filename TEXT NOT NULL,
		size INTEGER NOT NULL,
		visibility TEXT NOT NULL DEFAULT 'password',
		password_hash TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}

	copySQL := `INSERT INTO uploads_new(id,slug,owner_account_id,owner_token_id,filename,size,visibility,password_hash,created_at)
		SELECT u.id,u.slug,t.account_id,u.owner_token_id,u.filename,u.size,u.visibility,u.password_hash,u.created_at
		FROM uploads u JOIN tokens t ON t.id=u.owner_token_id`
	if hasOwnerAccount {
		copySQL = `INSERT INTO uploads_new(id,slug,owner_account_id,owner_token_id,filename,size,visibility,password_hash,created_at)
			SELECT u.id,u.slug,COALESCE(u.owner_account_id,t.account_id),u.owner_token_id,u.filename,u.size,u.visibility,u.password_hash,u.created_at
			FROM uploads u LEFT JOIN tokens t ON t.id=u.owner_token_id`
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE uploads`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE uploads_new RENAME TO uploads`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) migrateInviteTokenCipher() error {
	hasCipher, _, err := s.columnInfo("invites", "token_cipher")
	if err != nil {
		return err
	}
	if hasCipher {
		return nil
	}
	_, err = s.Exec(`ALTER TABLE invites ADD COLUMN token_cipher TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) columnInfo(table, column string) (exists, notNull bool, err error) {
	rows, err := s.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var nn, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk); err != nil {
			return false, false, err
		}
		if name == column {
			return true, nn == 1, nil
		}
	}
	return false, false, rows.Err()
}

// migrateCLILoginRequestContext records where a `peek login` request came from,
// so the approval page can show it. Existing rows keep ”.
func (s *Store) migrateCLILoginRequestContext() error {
	for _, stmt := range []string{
		`ALTER TABLE cli_login_devices ADD COLUMN request_ip TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cli_login_devices ADD COLUMN request_user_agent TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}
	return nil
}

// migrateAccountSessionEpoch adds the counter that signs an account out of
// every dashboard session when it changes. Existing accounts start at 0, which
// keeps their current sessions valid.
func (s *Store) migrateAccountSessionEpoch() error {
	_, err := s.Exec(`ALTER TABLE accounts ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}

// migrateCommentAccountID records which signed-in account wrote a comment.
// NULL means the author name was typed by the viewer.
func (s *Store) migrateCommentAccountID() error {
	if _, err := s.Exec(`ALTER TABLE comments ADD COLUMN account_id INTEGER`); err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}
