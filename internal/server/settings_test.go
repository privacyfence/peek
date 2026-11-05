package server

import (
	"context"
	"encoding/json"
	webui "github.com/privacyfence/peek/internal/web"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/privacyfence/peek/internal/db"
	"github.com/privacyfence/peek/internal/models"
)

func TestSettingKeysAreSorted(t *testing.T) {
	s := &Server{}
	got := s.settingKeys(map[string]string{
		"retention_days": "30",
		"max_upload":     "1024",
		"s3_endpoint":    "https://example.test",
	})
	want := []string{"max_upload", "retention_days", "s3_endpoint"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settingKeys() = %+v, want %+v", got, want)
	}
}

func TestGetSettingsRowsAreSorted(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetSetting(context.Background(), "s3_endpoint", "https://example.test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "auth_token_login_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "max_upload", "1024"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}
	rec := httptest.NewRecorder()

	s.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var rows []settingsRow
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	want := []string{"auth_token_login_enabled", "max_upload", "s3_endpoint"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %+v, want %+v", keys, want)
	}
}

func TestUpdateSettingsRejectsUnknownKeysBeforeWriting(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetSetting(context.Background(), "max_upload", "1024"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"max_upload":"2048","not_a_setting":"x"}`))
	req = withAPIToken(req, &models.Token{Name: "admin", IsAdmin: true})
	rec := httptest.NewRecorder()

	s.handleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown setting: not_a_setting") {
		t.Fatalf("response did not identify unknown setting: %s", rec.Body.String())
	}
	got, err := store.GetSetting(context.Background(), "max_upload")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1024" {
		t.Fatalf("max_upload was partially updated to %q", got)
	}
}

func TestUpdateSettingsRejectsInvalidValuesBeforeWriting(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetSetting(context.Background(), "max_upload", "1024"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "retention_days", "7"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"max_upload":"0","retention_days":"30"}`))
	req = withAPIToken(req, &models.Token{Name: "admin", IsAdmin: true})
	rec := httptest.NewRecorder()

	s.handleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max_upload must be a positive integer") {
		t.Fatalf("response did not identify invalid setting: %s", rec.Body.String())
	}
	if got := mustGetSetting(t, store, "max_upload"); got != "1024" {
		t.Fatalf("max_upload was partially updated to %q", got)
	}
	if got := mustGetSetting(t, store, "retention_days"); got != "7" {
		t.Fatalf("retention_days was partially updated to %q", got)
	}
}

func TestNormalizeSettingsValues(t *testing.T) {
	s := &Server{}
	tests := []struct {
		key   string
		value string
		want  string
	}{
		{key: "auth_token_login_enabled", value: "yes", want: "true"},
		{key: "oauth_google_enabled", value: "off", want: ""},
		{key: "oauth_oidc_issuer_url", value: "https://8.8.8.8/issuer/", want: "https://8.8.8.8/issuer"},
		{key: "max_upload", value: "2048", want: "2048"},
		{key: "max_total_size", value: "0", want: "0"},
		{key: "max_uploads_per_token", value: "12", want: "12"},
		{key: "retention_days", value: "30", want: "30"},
		{key: "storage", value: "S3", want: "s3"},
		{key: "s3_bucket", value: " peek ", want: "peek"},
	}
	for _, tt := range tests {
		got, err := s.normalizeSettingValue(tt.key, tt.value)
		if err != nil {
			t.Fatalf("normalizeSettingValue(%q, %q): %v", tt.key, tt.value, err)
		}
		if got != tt.want {
			t.Fatalf("normalizeSettingValue(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
		}
	}
}

func TestNormalizeOIDCIssuerRejectsUnsafeURLs(t *testing.T) {
	s := &Server{}
	for _, value := range []string{
		"http://sso.example.test",
		"https://127.0.0.1/issuer",
		"https://169.254.169.254/issuer",
		"https://sso.example.test/issuer?tenant=one",
		"https://user@sso.example.test/issuer",
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := s.normalizeSettingValue("oauth_oidc_issuer_url", value); err == nil {
				t.Fatalf("normalizeSettingValue accepted %q as %q", value, got)
			}
		})
	}
}

func TestNormalizeOIDCIssuerAllowsPrivateWithExplicitOptIn(t *testing.T) {
	s := &Server{oidcAllowPrivateIssuer: true}
	for _, value := range []string{
		"http://127.0.0.1:8080/realms/peek/",
		"http://localhost:8080/realms/peek",
	} {
		t.Run(value, func(t *testing.T) {
			got, err := s.normalizeSettingValue("oauth_oidc_issuer_url", value)
			if err != nil {
				t.Fatalf("normalizeSettingValue(%q): %v", value, err)
			}
			if strings.HasSuffix(got, "/") {
				t.Fatalf("issuer was not normalized: %q", got)
			}
		})
	}
}

func TestDashboardSettingsInvalidS3EndpointRendersInlineError(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.CreateAccount(context.Background(), "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := webui.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, renderer: renderer, secret: strings.Repeat("0", 64)}

	form := url.Values{
		"csrf":        {"csrf-token"},
		"s3_endpoint": {"http://8.8.8.8"},
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(account.ID, 10), sessionTTL)})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()

	s.handleDashboardSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want inline render", loc)
	}
	if body := rec.Body.String(); !strings.Contains(body, "invalid s3 endpoint: ") || !strings.Contains(body, "Save settings") {
		t.Fatalf("body missing inline error on the admin page: %s", body)
	}
}

func TestDashboardSettingsRejectsInvalidValuesBeforeWriting(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.CreateAccount(context.Background(), "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "auth_token_login_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "max_upload", "1024"); err != nil {
		t.Fatal(err)
	}
	renderer, err := webui.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, renderer: renderer, secret: strings.Repeat("0", 64)}

	form := url.Values{
		"csrf":       {"csrf-token"},
		"max_upload": {"0"},
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(account.ID, 10), sessionTTL)})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()

	s.handleDashboardSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "max_upload must be a positive integer") {
		t.Fatalf("body missing inline error: %s", rec.Body.String())
	}
	if got := mustGetSetting(t, store, "auth_token_login_enabled"); got != "true" {
		t.Fatalf("auth_token_login_enabled was partially updated to %q", got)
	}
	if got := mustGetSetting(t, store, "max_upload"); got != "1024" {
		t.Fatalf("max_upload was partially updated to %q", got)
	}
}

func TestDashboardSettingsPreservesHiddenOAuthAndS3Settings(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.CreateAccount(context.Background(), "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}
	if err := store.SetSetting(context.Background(), "oauth_google_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "oauth_google_client_id", "google-client"); err != nil {
		t.Fatal(err)
	}
	if err := s.encryptedSetSetting(context.Background(), "oauth_google_client_secret", "google-secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "storage", "s3"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(context.Background(), "s3_endpoint", "https://storage.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := s.encryptedSetSetting(context.Background(), "s3_secret_key", "s3-secret"); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"csrf":    {"csrf-token"},
		"storage": {"file"},
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(account.ID, 10), sessionTTL)})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()

	s.handleDashboardSettings(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := mustGetSetting(t, store, "oauth_google_enabled"); got != "" {
		t.Fatalf("oauth_google_enabled = %q, want disabled", got)
	}
	if got := mustGetSetting(t, store, "oauth_google_client_id"); got != "google-client" {
		t.Fatalf("oauth_google_client_id = %q", got)
	}
	if got, err := s.encryptedGetSetting(context.Background(), "oauth_google_client_secret"); err != nil || got != "google-secret" {
		t.Fatalf("oauth_google_client_secret = %q, err = %v", got, err)
	}
	if got := mustGetSetting(t, store, "storage"); got != "file" {
		t.Fatalf("storage = %q, want file", got)
	}
	if got := mustGetSetting(t, store, "s3_endpoint"); got != "https://storage.example.test" {
		t.Fatalf("s3_endpoint = %q", got)
	}
	if got, err := s.encryptedGetSetting(context.Background(), "s3_secret_key"); err != nil || got != "s3-secret" {
		t.Fatalf("s3_secret_key = %q, err = %v", got, err)
	}
}

func TestDashboardSettingsConvertsMegabyteLimits(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.CreateAccount(context.Background(), "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}

	form := url.Values{
		"csrf":                     {"csrf-token"},
		"max_upload_mb":            {"3"},
		"max_total_size_mb":        {"512"},
		"max_storage_per_token_mb": {"128"},
		"max_uploads_per_token":    {"12"},
		"retention_days":           {"30"},
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(account.ID, 10), sessionTTL)})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()

	s.handleDashboardSettings(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := mustGetSetting(t, store, "max_upload"); got != "3145728" {
		t.Fatalf("max_upload = %q", got)
	}
	if got := mustGetSetting(t, store, "max_total_size"); got != "536870912" {
		t.Fatalf("max_total_size = %q", got)
	}
	if got := mustGetSetting(t, store, "max_storage_per_token"); got != "134217728" {
		t.Fatalf("max_storage_per_token = %q", got)
	}
	if got := mustGetSetting(t, store, "max_uploads_per_token"); got != "12" {
		t.Fatalf("max_uploads_per_token = %q", got)
	}
	if got := mustGetSetting(t, store, "retention_days"); got != "30" {
		t.Fatalf("retention_days = %q", got)
	}
}

func TestDashboardSettingsRejectsMalformedForm(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	account, err := store.CreateAccount(context.Background(), "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, secret: strings.Repeat("0", 64)}

	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader("csrf=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(account.ID, 10), sessionTTL)})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()

	s.handleDashboardSettings(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect location %q: %v", loc, err)
	}
	if got := u.Query().Get("err"); got != "invalid_session" {
		t.Fatalf("err query = %q, location = %q", got, loc)
	}
}

func mustGetSetting(t *testing.T, store *db.Store, key string) string {
	t.Helper()
	got, err := store.GetSetting(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestNormalizeSettingRejectsInvalidS3Bucket(t *testing.T) {
	s := &Server{}
	_, err := s.normalizeSettingValue("s3_bucket", "a/b")
	if err == nil || !strings.Contains(err.Error(), "invalid s3 bucket") {
		t.Fatalf("error = %v", err)
	}
}

func setCovSettingsRows(t *testing.T, s *Server, token string) (int, []settingsRow) {
	t.Helper()
	rec := doRequest(t, s, http.MethodGet, "/api/settings", nil, asBearer(token))
	var rows []settingsRow
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
	}
	return rec.Code, rows
}

func TestSetCovGetSettingsLabelsUnknownKeysAndMasksSecrets(t *testing.T) {
	s, token := newHandlerTestServer(t)
	ctx := context.Background()
	if err := s.store.SetSetting(ctx, "zz_custom", "custom"); err != nil {
		t.Fatal(err)
	}
	if err := s.encryptedSetSetting(ctx, "s3_secret_key", "hunter2"); err != nil {
		t.Fatal(err)
	}
	code, rows := setCovSettingsRows(t, s, token)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	byKey := map[string]settingsRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if r := byKey["zz_custom"]; r.Label != "zz_custom" || r.Value != "custom" {
		t.Fatalf("unknown key row = %+v", r)
	}
	if r := byKey["s3_secret_key"]; r.Value != "••••" {
		t.Fatalf("secret row value = %q, want mask", r.Value)
	}
}

func TestSetCovGetSettingsDBFailure(t *testing.T) {
	s, token := newHandlerTestServer(t)
	dropTable(t, s.store, "settings")
	if code, _ := setCovSettingsRows(t, s, token); code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
}

func TestSetCovUpdateSettingsAPIErrors(t *testing.T) {
	s, token := newHandlerTestServer(t)
	post := func(body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
		return doRequest(t, s, http.MethodPut, "/api/settings", strings.NewReader(body), opts...)
	}
	if rec := post(`{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", rec.Code)
	}
	// The handler itself also refuses a request that carries no API token.
	direct := httptest.NewRecorder()
	s.handleUpdateSettings(direct, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{}`)))
	if direct.Code != http.StatusInternalServerError {
		t.Fatalf("direct anonymous status = %d", direct.Code)
	}
	if rec := post(`{not json`, asBearer(token), withJSON()); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad json") {
		t.Fatalf("bad json: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"retention_days":"30"}`, asBearer(token), withJSON()); rec.Code != http.StatusOK {
		t.Fatalf("valid update: %d %s", rec.Code, rec.Body.String())
	}
	addFailTrigger(t, s.store, "settings", "UPDATE")
	addFailTrigger(t, s.store, "settings", "INSERT")
	if rec := post(`{"retention_days":"31"}`, asBearer(token), withJSON()); rec.Code != http.StatusInternalServerError {
		t.Fatalf("write failure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSetCovNormalizeSettingValue(t *testing.T) {
	s := &Server{}
	tests := []struct {
		key, value string
		want       string
		wantErr    string
	}{
		{"nope", "x", "", "unknown setting: nope"},
		{"auth_token_login_enabled", "maybe", "", "must be a boolean"},
		{"auth_token_login_enabled", " Off ", "", ""},
		{"auth_token_login_enabled", "YES", "true", ""},
		{"max_upload", "abc", "", "must be an integer"},
		{"max_upload", "", "", "must be an integer"},
		{"max_upload", "-1", "", "positive integer"},
		{"max_upload", "5", "5", ""},
		{"max_total_size", "x", "", "must be an integer"},
		{"max_total_size", "-5", "", "non-negative integer"},
		{"max_total_size", "0", "0", ""},
		{"max_storage_per_token", "-2", "", "non-negative integer"},
		{"retention_days", "x", "", "non-negative integer"},
		{"retention_days", "", "", "non-negative integer"},
		{"retention_days", "-1", "", "non-negative integer"},
		{"retention_days", "7", "7", ""},
		{"max_uploads_per_token", "3", "3", ""},
		{"oauth_oidc_issuer_url", "", "", ""},
		{"oauth_oidc_issuer_url", "ftp://issuer.example.com", "", "http"},
		{"storage", "FILE", "file", ""},
		{"storage", "tape", "", "must be file or s3"},
		{"s3_endpoint", "", "", ""},
		{"s3_endpoint", "http://127.0.0.1:9", "", "invalid s3 endpoint"},
		{"s3_bucket", "", "", ""},
		{"s3_bucket", "Bad_Bucket", "", "invalid s3 bucket"},
		{"s3_region", " auto ", "auto", ""},
	}
	for _, tc := range tests {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			got, err := s.normalizeSettingValue(tc.key, tc.value)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestSetCovDashboardMegabytesToBytes(t *testing.T) {
	for _, tc := range []struct{ in, want, wantErr string }{
		{"abc", "", "must be an integer"},
		{"", "", "must be an integer"},
		{"-3", "-3", ""},
		{"2", strconv.FormatInt(2*dashboardMegabyte, 10), ""},
		{strconv.FormatInt(maxDashboardMegabytes+1, 10), "", "too large"},
	} {
		got, err := dashboardMegabytesToBytes("max_upload", tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%q: err = %v", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q, %v", tc.in, got, err)
		}
	}
	if _, err := normalizeNonNegativeInt64Setting("k", "x"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := normalizePositiveInt64Setting("k", "x"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestSetCovDashboardSettingsErrorPaths(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newViewer(t, s, "setcov-admin@example.test", "Admin", true)
	user := newViewer(t, s, "setcov-user@example.test", "User", false)
	ctx := context.Background()

	rec := dashboardPost(t, s, user.ID, "/dashboard/settings", url.Values{}, true)
	assertDashboardRedirect(t, rec, "/login")

	rec = dashboardPost(t, s, admin.ID, "/dashboard/settings", url.Values{"oauth_google_enabled": {"maybe"}}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must be a boolean") {
		t.Fatalf("bool error: %d", rec.Code)
	}

	rec = dashboardPost(t, s, admin.ID, "/dashboard/settings", url.Values{"max_upload_mb": {"abc"}}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_upload must be an integer") {
		t.Fatalf("megabyte error: %d", rec.Code)
	}

	// A blank secret keeps the stored value.
	if err := s.encryptedSetSetting(ctx, "s3_secret_key", "keep-me"); err != nil {
		t.Fatal(err)
	}
	rec = dashboardPost(t, s, admin.ID, "/dashboard/settings", url.Values{"s3_secret_key": {""}, "s3_bucket": {"mybucket"}}, true)
	assertDashboardRedirect(t, rec, "/admin?ok=settings_saved")
	if got := s.settingString(ctx, "s3_secret_key"); got != "keep-me" {
		t.Fatalf("secret = %q", got)
	}
	if got := s.settingString(ctx, "s3_bucket"); got != "mybucket" {
		t.Fatalf("bucket = %q", got)
	}

	addFailTrigger(t, s.store, "settings", "UPDATE")
	addFailTrigger(t, s.store, "settings", "INSERT")
	rec = dashboardPost(t, s, admin.ID, "/dashboard/settings", url.Values{"s3_bucket": {"other"}}, true)
	assertDashboardRedirect(t, rec, "/admin?err=settings_failed")
}

func TestSetCovDashboardSettingsHelpers(t *testing.T) {
	s, _, _, _ := newFaultServer(t)
	dropTable(t, s.store, "settings")
	m := s.dashboardSettingsMap(context.Background())
	if len(m) != len(settingsMeta) {
		t.Fatalf("map has %d keys, want %d", len(m), len(settingsMeta))
	}
	if m["max_upload"] != "" {
		t.Fatalf("max_upload = %q", m["max_upload"])
	}

	if got := dashboardSettingMegabytes(map[string]string{"max_upload": "1"}, "max_upload"); got != 1 {
		t.Fatalf("1 byte rounds up to %d MB, want 1", got)
	}
	if got := dashboardSettingMegabytes(map[string]string{"max_upload": strconv.FormatInt(dashboardMegabyte, 10)}, "max_upload"); got != 1 {
		t.Fatalf("exact MB = %d", got)
	}
	if got := dashboardSettingMegabytes(map[string]string{"max_upload": "0"}, "max_upload"); got != 0 {
		t.Fatalf("zero = %d", got)
	}
	if got := dashboardSettingInt64(map[string]string{"retention_days": "junk"}, "retention_days"); got != 0 {
		t.Fatalf("bad value = %d", got)
	}
	if got := dashboardSettingInt64(map[string]string{"retention_days": "-4"}, "retention_days"); got != 0 {
		t.Fatalf("negative value = %d", got)
	}
	if !settingValueBool(" Yes ") || settingValueBool("nope") {
		t.Fatal("settingValueBool mismatch")
	}
	if maxInt64(5, 3) != 5 || maxInt64(3, 5) != 5 {
		t.Fatal("maxInt64 mismatch")
	}
	panel := dashboardSettingsPanel(map[string]string{"oauth_github_client_secret": "stored"}, nil)
	if secret := panel.Auth.GitHub.ClientSecret; secret.Value != "" || !strings.Contains(secret.Description, "leave blank") {
		t.Fatalf("panel secret row = %+v", secret)
	}
}

func TestSetCovDashboardSubmittedSettingValue(t *testing.T) {
	v, ok, err := dashboardSubmittedSettingValue("max_upload", map[string][]string{"max_upload_mb": {"2"}})
	if err != nil || !ok || v != strconv.FormatInt(2*dashboardMegabyte, 10) {
		t.Fatalf("mb form: %q %v %v", v, ok, err)
	}
	if _, ok, _ := dashboardSubmittedSettingValue("retention_days", nil); ok {
		t.Fatal("missing key reported as submitted")
	}
	if v, ok, _ := dashboardSubmittedSettingValue("retention_days", map[string][]string{"retention_days": {}}); !ok || v != "" {
		t.Fatalf("empty values: %q %v", v, ok)
	}
}

func TestSMTPSettingNormalization(t *testing.T) {
	s := &Server{}
	tests := []struct {
		key, in, want string
		wantErr       string
	}{
		{"smtp_host", "  smtp.example.com ", "smtp.example.com", ""},
		{"smtp_host", "", "", ""},
		{"smtp_host", "smtp://host", "", "smtp_host must be a host name without scheme or port"},
		{"smtp_host", "host:587", "", "smtp_host must be a host name without scheme or port"},
		{"smtp_host", "a b", "", "smtp_host must be a host name without scheme or port"},
		{"smtp_host", "u@host", "", "smtp_host must be a host name without scheme or port"},
		{"smtp_port", "", "", ""},
		{"smtp_port", "587", "587", ""},
		{"smtp_port", "65535", "65535", ""},
		{"smtp_port", "0", "", "smtp_port must be between 1 and 65535"},
		{"smtp_port", "65536", "", "smtp_port must be between 1 and 65535"},
		{"smtp_port", "abc", "", "smtp_port must be between 1 and 65535"},
		{"smtp_tls", "", "", ""},
		{"smtp_tls", "STARTTLS", "starttls", ""},
		{"smtp_tls", "tls", "tls", ""},
		{"smtp_tls", "none", "none", ""},
		{"smtp_tls", "ssl", "", "smtp_tls must be starttls, tls or none"},
		{"smtp_username", "  user ", "user", ""},
		{"smtp_password", "  pw ", "pw", ""},
		{"smtp_from", "", "", ""},
		{"smtp_from", "peek@example.com", "peek@example.com", ""},
		{"smtp_from", "Peek <peek@example.com>", "", "smtp_from must be an email address"},
		{"smtp_from", "not-an-address", "", "smtp_from must be an email address"},
	}
	for _, tt := range tests {
		got, err := s.normalizeSettingValue(tt.key, tt.in)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("%s(%q) err = %v, want %q", tt.key, tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s(%q) = %q, %v; want %q", tt.key, tt.in, got, err, tt.want)
		}
	}
}

func TestSMTPPasswordStoredEncrypted(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	ctx := context.Background()
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)

	form := url.Values{"csrf": {"csrf-token"}, "smtp_password": {"relay-secret"}}
	rec := doRequest(t, s, http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()),
		asSession(s, admin.ID),
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
		})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	raw, err := s.store.GetSetting(ctx, "smtp_password")
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || raw == "relay-secret" {
		t.Fatalf("stored value = %q, want ciphertext", raw)
	}
	got, err := s.encryptedGetSetting(ctx, "smtp_password")
	if err != nil || got != "relay-secret" {
		t.Fatalf("decrypted = %q, %v", got, err)
	}
}

func TestDashboardSavesOIDCSignup(t *testing.T) {
	s := newTestServer(t)
	renderer, err := webui.NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	s.renderer = renderer
	ctx := context.Background()
	admin, err := s.store.CreateAccount(ctx, "admin@example.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	oidcSignupOn(t, s, "example.test")
	if err := s.store.SetSetting(ctx, authOIDCSignupSetting, ""); err != nil {
		t.Fatal(err)
	}
	// The dashboard form always posts every bool, so it carries the SSO switch too.
	form := url.Values{
		authOIDCSignupSetting:         {"true"},
		authAllowedEmailDomainSetting: {"example.test"},
		"oauth_oidc_enabled":          {"true"},
		"oauth_oidc_issuer_url":       {"https://sso.example.test"},
		"oauth_oidc_client_id":        {"client-id"},
	}
	rec := googleOrgDashboardPost(t, s, admin.ID, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !s.oidcSignupEnabled(ctx) {
		t.Fatal("auth_oidc_signup not saved")
	}
	panel := dashboardSettingsPanel(map[string]string{authOIDCSignupSetting: "true"}, nil)
	if panel.Auth.OIDCSignup.Key != authOIDCSignupSetting || !panel.Auth.OIDCSignup.IsBool || panel.Auth.OIDCSignup.Label != "SSO sign-up" {
		t.Fatalf("panel row = %+v", panel.Auth.OIDCSignup)
	}
	// An unchecked box on the next save turns it off again.
	form.Del(authOIDCSignupSetting)
	rec = googleOrgDashboardPost(t, s, admin.ID, form)
	if rec.Code != http.StatusSeeOther || s.oidcSignupEnabled(ctx) {
		t.Fatalf("status = %d enabled = %v", rec.Code, s.oidcSignupEnabled(ctx))
	}
}

func TestAPIRejectsInvalidOIDCSignup(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Server{store: store, secret: strings.Repeat("0", 64)}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"auth_oidc_signup":"true"}`))
	req = withAPIToken(req, &models.Token{Name: "admin", IsAdmin: true})
	rec := httptest.NewRecorder()
	s.handleUpdateSettings(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "auth_oidc_signup requires auth_allowed_email_domain") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got, err := store.GetSetting(context.Background(), authOIDCSignupSetting); err == nil {
		t.Fatalf("setting was written: %q", got)
	}
}

func TestAPIRejectsLockedKey(t *testing.T) {
	s := newTestServer(t)
	s.locked = map[string]LockedSetting{authAllowedEmailDomainSetting: {Value: "corp.test", Env: "PEEK_ALLOWED_EMAIL_DOMAIN"}}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"retention_days":"5","auth_allowed_email_domain":"other.test"}`))
	req = withAPIToken(req, &models.Token{Name: "admin", IsAdmin: true})
	rec := httptest.NewRecorder()
	s.handleUpdateSettings(rec, req)
	want := "auth_allowed_email_domain is set by PEEK_ALLOWED_EMAIL_DOMAIN on the server"
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	// Nothing in the body was written.
	if v, err := s.store.GetSetting(context.Background(), "retention_days"); err == nil {
		t.Fatalf("retention_days written: %q", v)
	}
}

func TestDashboardKeepsLockedBool(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	admin, err := s.store.CreateAccount(ctx, "admin@corp.test", "Admin", true)
	if err != nil {
		t.Fatal(err)
	}
	s.locked = map[string]LockedSetting{
		authAllowedEmailDomainSetting: {Value: "corp.test", Env: "PEEK_ALLOWED_EMAIL_DOMAIN"},
		"oauth_oidc_enabled":          {Value: "true", Env: "PEEK_OIDC_ISSUER_URL"},
		"oauth_oidc_issuer_url":       {Value: "https://sso.example.test", Env: "PEEK_OIDC_ISSUER_URL"},
		"oauth_oidc_client_id":        {Value: "cid", Env: "PEEK_OIDC_CLIENT_ID"},
		"oauth_oidc_client_secret":    {Value: "sec", Env: "PEEK_OIDC_CLIENT_SECRET"},
		authOIDCSignupSetting:         {Value: "true", Env: "PEEK_OIDC_SIGNUP"},
	}
	if err := s.applyLockedSettings(ctx); err != nil {
		t.Fatal(err)
	}
	// The form posts no locked bool (its switches are disabled) and a stale domain.
	rec := googleOrgDashboardPost(t, s, admin.ID, url.Values{
		authAllowedEmailDomainSetting: {"other.test"},
		"retention_days":              {"9"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !s.oidcSignupEnabled(ctx) || !s.settingBool(ctx, "oauth_oidc_enabled") {
		t.Fatal("a locked bool was reset by the dashboard save")
	}
	if v, _ := s.store.GetSetting(ctx, authAllowedEmailDomainSetting); v != "corp.test" {
		t.Fatalf("locked domain = %q", v)
	}
	if v, _ := s.store.GetSetting(ctx, "retention_days"); v != "9" {
		t.Fatalf("unlocked setting not saved: %q", v)
	}
}

func TestGetSettingsShowsLockedBy(t *testing.T) {
	s := newTestServer(t)
	s.locked = map[string]LockedSetting{
		"oauth_google_enabled":   {Value: "true", Env: "PEEK_GOOGLE_CLIENT_ID"},
		"oauth_google_client_id": {Value: "gid", Env: "PEEK_GOOGLE_CLIENT_ID"},
	}
	if err := s.applyLockedSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var rows []settingsRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	locked := map[string]string{}
	for _, r := range rows {
		if r.LockedBy != "" {
			locked[r.Key] = r.LockedBy
		}
	}
	if len(locked) != 2 || locked["oauth_google_enabled"] != "PEEK_GOOGLE_CLIENT_ID" || locked["oauth_google_client_id"] != "PEEK_GOOGLE_CLIENT_ID" {
		t.Fatalf("locked_by = %v", locked)
	}
	panel := dashboardSettingsPanel(map[string]string{}, s.lockedEnvNames())
	if panel.Auth.Google.Enabled.LockedBy != "PEEK_GOOGLE_CLIENT_ID" || panel.Auth.GitHub.Enabled.LockedBy != "" {
		t.Fatalf("panel locks = %+v", panel.Auth.Google.Enabled)
	}
}

func TestMaxBackendDataImportSetting(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	for _, tc := range []struct{ in, want, wantErr string }{
		{"abc", "", "must be an integer"},
		{"0", "", "positive integer"},
		{"-1", "", "positive integer"},
		{" 5 ", "5", ""},
	} {
		got, err := s.normalizeSettingValue("max_backend_data_import", tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q: got %q, %v, want error %q", tc.in, got, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v, want %q", tc.in, got, err, tc.want)
		}
	}
}
