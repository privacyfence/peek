package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migCovPrepare opens a fully migrated store, forgets every migration with
// id >= from, applies the sabotage statements and returns the database path.
func migCovPrepare(t *testing.T, from int, sabotage ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peek.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if from > 0 {
		if _, err := raw.Exec(`DELETE FROM schema_migrations WHERE id >= ?`, from); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range sabotage {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return path
}

// migCovWide replaces an indexed table with one at SQLite's 2000-column limit,
// so any ALTER TABLE ADD COLUMN on it fails with "too many columns".
func migCovWide(table string, named ...string) []string {
	cols := []string{"id INTEGER PRIMARY KEY"}
	cols = append(cols, named...)
	for i := len(cols); i < 2000; i++ {
		cols = append(cols, fmt.Sprintf("c%d INTEGER", i))
	}
	return []string{
		`DROP TABLE ` + table,
		`CREATE TABLE ` + table + ` (` + strings.Join(cols, ", ") + `)`,
	}
}

func migCovOpenFails(t *testing.T, path, want string) {
	t.Helper()
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatalf("Open succeeded, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Open error %q does not contain %q", err, want)
	}
}

// migCovBreak replaces an unindexed table with an empty view so ALTER TABLE
// and INSERT on it fail, while the schema's CREATE TABLE IF NOT EXISTS no-ops.
func migCovBreak(table string) []string {
	return []string{
		`DROP TABLE ` + table,
		`CREATE VIEW ` + table + ` AS SELECT 1 AS id, 'x' AS token, 1 AS owner_account_id, NULL AS owner_token_id WHERE 0`,
	}
}

func TestOpenFailsOnUnusableDatabasePath(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open on a directory should fail")
	}

	garbage := filepath.Join(t.TempDir(), "peek.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("this is not a sqlite database. ", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(garbage); err == nil {
		s.Close()
		t.Fatal("Open on a non-database file should fail")
	}
}

func TestRunMigrationsWrapsPerMigrationErrors(t *testing.T) {
	commentsTrap := migCovWide("comments", "upload_id")
	tests := []struct {
		name     string
		from     int
		sabotage []string
		want     string
	}{
		{"2 token_expiry", 2, migCovBreak("tokens"), "migration 2 (token_expiry)"},
		{"4 account_password_hash", 4, migCovBreak("accounts"), "migration 4 (account_password_hash)"},
		{"5 comment_anchor_kind", 5, commentsTrap, "migration 5 (comment_anchor_kind)"},
		{"6 upload_visibility", 6, migCovBreak("uploads"), "migration 6 (upload_visibility)"},
		{"7 upload_versions", 7, migCovBreak("upload_versions"), "migration 7 (upload_versions)"},
		{"10 token_hint_last_used", 10, migCovBreak("tokens"), "migration 10 (token_hint_last_used)"},
		{"11 cli_login_request_context", 11, migCovBreak("cli_login_devices"), "migration 11 (cli_login_request_context)"},
		{"12 account_session_epoch", 12, migCovBreak("accounts"), "migration 12 (account_session_epoch)"},
		{"13 comment_account_id", 13, commentsTrap, "migration 13 (comment_account_id)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migCovOpenFails(t, migCovPrepare(t, tt.from, tt.sabotage...), tt.want)
		})
	}
}

func TestRunMigrationsReportsBookkeepingFailures(t *testing.T) {
	t.Run("mark migration 1", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`DROP TABLE schema_migrations`,
			`CREATE VIEW schema_migrations AS SELECT 1 AS id, 1 AS applied_at WHERE 0`)
		migCovOpenFails(t, path, "schema_migrations")
	})
	t.Run("read migration", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`DROP TABLE schema_migrations`,
			`CREATE VIEW schema_migrations AS SELECT abs(-9223372036854775808) AS id, 1 AS applied_at`,
			`CREATE TRIGGER schema_migrations_ins INSTEAD OF INSERT ON schema_migrations BEGIN SELECT 1; END`)
		migCovOpenFails(t, path, "read migration 1")
	})
	t.Run("mark migration N", func(t *testing.T) {
		path := migCovPrepare(t, 3,
			`CREATE TRIGGER schema_migrations_block BEFORE INSERT ON schema_migrations WHEN NEW.id = 3 BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		migCovOpenFails(t, path, "mark migration 3 (audit_log)")
	})
	t.Run("legacy token scan", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`DROP TABLE schema_migrations`,
			`DROP TABLE tokens`,
			`CREATE TABLE tokens (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '')`)
		migCovOpenFails(t, path, "token")
	})
	t.Run("legacy token rehash", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
			`INSERT INTO tokens(account_id,token,name,created_at) VALUES(1,'plaintext-token','a',1)`,
			`CREATE TRIGGER tokens_upd BEFORE UPDATE ON tokens BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		migCovOpenFails(t, path, "blocked")
	})
}

func TestOpenFailsInPostMigrationSteps(t *testing.T) {
	t.Run("migrateAccounts insert", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
			`INSERT INTO tokens(account_id,token,name,created_at) VALUES(1,'`+HashToken("x")+`','a',1)`,
			`UPDATE tokens SET account_id=NULL`,
			`CREATE TRIGGER accounts_ins BEFORE INSERT ON accounts BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		migCovOpenFails(t, path, "blocked")
	})
	t.Run("migrateAccounts link", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
			`INSERT INTO tokens(account_id,token,name,created_at) VALUES(1,'`+HashToken("x")+`','a',1)`,
			`UPDATE tokens SET account_id=NULL`,
			`CREATE TRIGGER tokens_upd BEFORE UPDATE ON tokens BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
		migCovOpenFails(t, path, "blocked")
	})
	t.Run("migrateAccounts add column", func(t *testing.T) {
		migCovOpenFails(t, migCovPrepare(t, 0, migCovBreak("tokens")...), "view")
	})
	t.Run("migrateUploadsOwner create", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`CREATE TABLE uploads_new (id INTEGER)`,
			`DROP TABLE uploads`,
			`CREATE TABLE uploads (id INTEGER PRIMARY KEY, slug TEXT UNIQUE NOT NULL, owner_token_id INTEGER NOT NULL, filename TEXT NOT NULL, size INTEGER NOT NULL, visibility TEXT NOT NULL DEFAULT 'password', password_hash TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`)
		migCovOpenFails(t, path, "uploads_new")
	})
	t.Run("migrateUploadsOwner copy", func(t *testing.T) {
		path := migCovPrepare(t, 0,
			`DROP TABLE uploads`,
			`CREATE TABLE uploads (id INTEGER PRIMARY KEY, slug TEXT UNIQUE NOT NULL, owner_account_id INTEGER, owner_token_id INTEGER NOT NULL, filename TEXT NOT NULL, size INTEGER NOT NULL, visibility TEXT NOT NULL DEFAULT 'password', password_hash TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
			`INSERT INTO uploads(slug,owner_account_id,owner_token_id,filename,size,created_at) VALUES('orphan',NULL,999,'p.html',1,1)`)
		migCovOpenFails(t, path, "owner_account_id")
	})
	t.Run("migrateInviteTokenCipher", func(t *testing.T) {
		path := migCovPrepare(t, 0, migCovWide("invites", "email")...)
		migCovOpenFails(t, path, "too many columns")
	})
	t.Run("owner index", func(t *testing.T) {
		path := migCovPrepare(t, 0, migCovBreak("uploads")...)
		migCovOpenFails(t, path, "views may not be indexed")
	})
}

func TestLegacyUploadsWithOwnerAccountAreRebuilt(t *testing.T) {
	path := migCovPrepare(t, 0,
		`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
		`INSERT INTO tokens(id,account_id,token,name,created_at) VALUES(7,1,'`+HashToken("x")+`','a',1)`,
		`DROP TABLE uploads`,
		`CREATE TABLE uploads (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT UNIQUE NOT NULL, owner_account_id INTEGER REFERENCES accounts(id), owner_token_id INTEGER NOT NULL, filename TEXT NOT NULL, size INTEGER NOT NULL, visibility TEXT NOT NULL DEFAULT 'password', password_hash TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
		`INSERT INTO uploads(slug,owner_account_id,owner_token_id,filename,size,created_at) VALUES('with-owner',1,7,'a.html',1,1)`,
		`INSERT INTO uploads(slug,owner_account_id,owner_token_id,filename,size,created_at) VALUES('token-only',NULL,7,'b.html',1,1)`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.QueryRow(`SELECT COUNT(*) FROM uploads WHERE owner_account_id=1`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rebuilt uploads owned by account 1: n=%d err=%v", n, err)
	}
	if _, notNull, err := s.columnInfo("uploads", "owner_token_id"); err != nil || notNull {
		t.Fatalf("owner_token_id should be nullable after rebuild: notNull=%v err=%v", notNull, err)
	}
}

func TestLegacyInvitesWithoutTokenCipherGainColumn(t *testing.T) {
	path := migCovPrepare(t, 0,
		`DROP TABLE invites`,
		`CREATE TABLE invites (id INTEGER PRIMARY KEY AUTOINCREMENT, token_hash TEXT UNIQUE NOT NULL, email TEXT NOT NULL, created_by_account_id INTEGER NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER NOT NULL DEFAULT 0, revoked_at INTEGER NOT NULL DEFAULT 0)`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, _, err := s.columnInfo("invites", "token_cipher"); err != nil || !ok {
		t.Fatalf("token_cipher column: ok=%v err=%v", ok, err)
	}
}

func TestLegacyTokenHashMigrationHashesPlaintext(t *testing.T) {
	path := migCovPrepare(t, 0,
		`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
		`INSERT INTO tokens(account_id,token,name,created_at) VALUES(1,'plaintext-token','a',1)`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var tok string
	if err := s.QueryRow(`SELECT token FROM tokens`).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	if tok != HashToken("plaintext-token") {
		t.Fatalf("token not hashed: %q", tok)
	}
}

func TestMigrationsFailOnClosedStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	steps := map[string]func() error{
		"runMigrations":                 s.runMigrations,
		"migrateTokenExpiry":            s.migrateTokenExpiry,
		"migrateAccountPasswordHash":    s.migrateAccountPasswordHash,
		"migrateCommentAnchorKind":      s.migrateCommentAnchorKind,
		"migrateUploadVisibility":       s.migrateUploadVisibility,
		"migrateUploadVersions":         s.migrateUploadVersions,
		"migrateTokenHintLastUsed":      s.migrateTokenHintLastUsed,
		"migrateTokenHashes":            s.migrateTokenHashes,
		"migrateAccounts":               s.migrateAccounts,
		"migrateUploadsOwner":           s.migrateUploadsOwner,
		"migrateInviteTokenCipher":      s.migrateInviteTokenCipher,
		"migrateCLILoginRequestContext": s.migrateCLILoginRequestContext,
		"migrateAccountSessionEpoch":    s.migrateAccountSessionEpoch,
		"migrateCommentAccountID":       s.migrateCommentAccountID,
		"markMigrationApplied":          func() error { return s.markMigrationApplied(1) },
		"columnInfo":                    func() error { _, _, err := s.columnInfo("tokens", "id"); return err },
	}
	for name, step := range steps {
		if err := step(); err == nil {
			t.Errorf("%s on a closed store should fail", name)
		}
	}
}

func TestMigrationRenamesPrivateVisibilityToSignedIn(t *testing.T) {
	path := migCovPrepare(t, 14,
		`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
		`INSERT INTO uploads(slug,owner_account_id,filename,size,visibility,created_at) VALUES('was-private',1,'a.html',1,'private',1)`,
		`INSERT INTO uploads(slug,owner_account_id,filename,size,visibility,created_at) VALUES('was-public',1,'b.html',1,'public',1)`,
		`INSERT INTO uploads(slug,owner_account_id,filename,size,visibility,created_at) VALUES('was-restricted',1,'c.html',1,'restricted',1)`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for slug, want := range map[string]string{"was-private": "signed-in", "was-public": "public", "was-restricted": "restricted"} {
		var got string
		if err := s.QueryRow(`SELECT visibility FROM uploads WHERE slug=?`, slug).Scan(&got); err != nil || got != want {
			t.Fatalf("%s visibility = %q (err %v), want %q", slug, got, err, want)
		}
	}
}

func TestMigration15PasswordPagesBecomeRestricted(t *testing.T) {
	path := migCovPrepare(t, 15,
		`INSERT INTO accounts(id,name,created_at,updated_at) VALUES(1,'a',1,1)`,
		`INSERT INTO uploads(slug,owner_account_id,filename,size,visibility,password_hash,created_at) VALUES('was-password',1,'a.html',1,'password','$2a$hash',1)`,
		`INSERT INTO uploads(slug,owner_account_id,filename,size,visibility,created_at) VALUES('was-public',1,'b.html',1,'public',1)`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for slug, want := range map[string]string{"was-password": "restricted", "was-public": "public"} {
		var vis, hash string
		if err := s.QueryRow(`SELECT visibility, password_hash FROM uploads WHERE slug=?`, slug).Scan(&vis, &hash); err != nil || vis != want || hash != "" {
			t.Fatalf("%s = %q/%q (err %v), want %q with empty hash", slug, vis, hash, err, want)
		}
	}
}

func TestMigration16AddsUploadBackends(t *testing.T) {
	path := migCovPrepare(t, 16, `DROP TABLE upload_backends`, `DROP TABLE upload_backend_env`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=16`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration 16 recorded %d times (err %v)", n, err)
	}
	for _, table := range []string{"upload_backends", "upload_backend_env"} {
		if err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (err %v)", table, err)
		}
	}
}

func TestMigration17CreatesProvisioningTables(t *testing.T) {
	path := migCovPrepare(t, 17, `DROP TABLE provisioned_accounts`, `DROP TABLE provisioners`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=17`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration 17 recorded %d times (err %v)", n, err)
	}
	for _, table := range []string{"provisioners", "provisioned_accounts"} {
		if err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (err %v)", table, err)
		}
	}
}

func TestMigration18AddsBackendTokenCalls(t *testing.T) {
	path := migCovPrepare(t, 18, `DROP TABLE backend_token_calls`, `DROP TABLE upload_backend_settings`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id=18`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration 18 recorded %d times (err %v)", n, err)
	}
	for _, table := range []string{"backend_token_calls", "upload_backend_settings"} {
		if err := s.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (err %v)", table, err)
		}
	}
}
