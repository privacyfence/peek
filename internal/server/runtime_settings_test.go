package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/privacyfence/peek/internal/db"
)

func TestDecryptedStoreSettingDecryptsSecretValues(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	secret := "0000000000000000000000000000000000000000000000000000000000000000"
	encrypted, err := encryptSecret(secret, "access-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "s3_secret_key", encrypted); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "s3_endpoint", "https://example.com"); err != nil {
		t.Fatal(err)
	}

	if got := decryptedStoreSetting(context.Background(), store, secret, "s3_secret_key"); got != "access-secret" {
		t.Fatalf("secret setting = %q", got)
	}
	if got := decryptedStoreSetting(context.Background(), store, secret, "s3_endpoint"); got != "https://example.com" {
		t.Fatalf("plain setting = %q", got)
	}
}

func setCovStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSetCovInitDefaultSettingsKeepsExistingKeys(t *testing.T) {
	ctx := context.Background()
	store := setCovStore(t)
	if err := store.SetSetting(ctx, "max_upload", "7"); err != nil {
		t.Fatal(err)
	}
	if err := initDefaultSettings(ctx, store, strings.Repeat("0", 64), 100, 200, 3, nil); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSetting(t, store, "max_upload"); got != "7" {
		t.Fatalf("existing max_upload overwritten with %q", got)
	}
	if got := mustGetSetting(t, store, "max_total_size"); got != "200" {
		t.Fatalf("max_total_size = %q", got)
	}
}

func TestSetCovInitDefaultSettingsSecretKeyEncryption(t *testing.T) {
	ctx := context.Background()
	secret := strings.Repeat("0", 64)
	store := setCovStore(t)
	defaults := map[string]string{"s3_secret_key": "plain", "s3_bucket": "bucket"}
	if err := initDefaultSettings(ctx, store, secret, 1, 0, 0, defaults); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSetting(t, store, "s3_secret_key"); got == "plain" || got == "" {
		t.Fatalf("secret stored as %q, want ciphertext", got)
	}
	if got := decryptedStoreSetting(ctx, store, secret, "s3_secret_key"); got != "plain" {
		t.Fatalf("decrypted = %q", got)
	}
	if got := mustGetSetting(t, store, "s3_bucket"); got != "bucket" {
		t.Fatalf("s3_bucket = %q", got)
	}

	bad := setCovStore(t)
	err := initDefaultSettings(ctx, bad, "not-hex", 1, 0, 0, map[string]string{"s3_secret_key": "plain"})
	if err == nil || !strings.Contains(err.Error(), "invalid secret key") {
		t.Fatalf("err = %v, want invalid secret key", err)
	}
}

func TestSetCovInitDefaultSettingsReportsWriteFailure(t *testing.T) {
	store := setCovStore(t)
	addFailTrigger(t, store, "settings", "INSERT")
	if err := initDefaultSettings(context.Background(), store, strings.Repeat("0", 64), 1, 0, 0, nil); err == nil {
		t.Fatal("expected defaults write error")
	}
	// A failing s3 default write is reported as well.
	store2 := setCovStore(t)
	ctx := context.Background()
	if err := initDefaultSettings(ctx, store2, "", 1, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	addFailTrigger(t, store2, "settings", "INSERT")
	if err := initDefaultSettings(ctx, store2, "", 1, 0, 0, map[string]string{"s3_bucket": "b"}); err == nil {
		t.Fatal("expected s3 default write error")
	}
}

func TestSetCovSettingIntFallsBackOnBadValues(t *testing.T) {
	s, _, _, _ := newFaultServer(t)
	ctx := context.Background()
	if err := s.store.SetSetting(ctx, "retention_days", "abc"); err != nil {
		t.Fatal(err)
	}
	if got := s.settingInt64(ctx, "retention_days", 42); got != 42 {
		t.Fatalf("settingInt64 = %d, want default 42", got)
	}
	if got := s.settingInt(ctx, "retention_days", 9); got != 9 {
		t.Fatalf("settingInt = %d, want default 9", got)
	}
	if err := s.store.SetSetting(ctx, "retention_days", "12"); err != nil {
		t.Fatal(err)
	}
	if got := s.settingInt64(ctx, "retention_days", 42); got != 12 {
		t.Fatalf("settingInt64 = %d", got)
	}
	if got := s.settingInt(ctx, "retention_days", 9); got != 12 {
		t.Fatalf("settingInt = %d", got)
	}
}

func TestSetCovDecryptedStoreSettingMissingOrUndecryptable(t *testing.T) {
	ctx := context.Background()
	secret := strings.Repeat("0", 64)
	store := setCovStore(t)
	if got := decryptedStoreSetting(ctx, store, secret, "s3_secret_key"); got != "" {
		t.Fatalf("missing setting = %q", got)
	}
	if err := store.SetSetting(ctx, "s3_secret_key", "legacy"); err != nil {
		t.Fatal(err)
	}
	if got := decryptedStoreSetting(ctx, store, "not-hex", "s3_secret_key"); got != "" {
		t.Fatalf("bad secret = %q, want empty", got)
	}
}

func TestSetCovEncryptedSettingsWithBadSecret(t *testing.T) {
	s, _, _, _ := newFaultServer(t)
	s.secret = "not-hex"
	if err := s.encryptedSetSetting(context.Background(), "s3_secret_key", "value"); err == nil {
		t.Fatal("expected encrypt error")
	}
	// A blank secret value is stored without encrypting.
	if err := s.encryptedSetSetting(context.Background(), "s3_secret_key", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetCovEncryptedGetAllSettings(t *testing.T) {
	s, _, _, _ := newFaultServer(t)
	ctx := context.Background()
	if err := s.encryptedSetSetting(ctx, "s3_secret_key", "good"); err != nil {
		t.Fatal(err)
	}
	other, err := encryptSecret(strings.Repeat("1", 64), "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(ctx, "oauth_github_client_secret", other); err != nil {
		t.Fatal(err)
	}
	got, err := s.encryptedGetAllSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["s3_secret_key"] != "good" {
		t.Fatalf("s3_secret_key = %q", got["s3_secret_key"])
	}
	if got["oauth_github_client_secret"] != "" {
		t.Fatalf("undecryptable secret = %q, want blank", got["oauth_github_client_secret"])
	}

	s.secret = "not-hex"
	got, err = s.encryptedGetAllSettings(ctx)
	if err != nil || got["s3_secret_key"] != "" {
		t.Fatalf("bad secret: got=%v err=%v", got, err)
	}

	dropTable(t, s.store, "settings")
	if _, err := s.encryptedGetAllSettings(ctx); err == nil {
		t.Fatal("expected db error")
	}
}

func TestMaxBackendDataImportSeeded(t *testing.T) {
	ctx := context.Background()
	store := setCovStore(t)
	if err := initDefaultSettings(ctx, store, strings.Repeat("0", 64), 100, 200, 3, nil); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSetting(t, store, "max_backend_data_import"); got != "268435456" {
		t.Fatalf("max_backend_data_import = %q", got)
	}
}
