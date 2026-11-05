package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestAdminPageRendersForAdmin(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)

	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Create invite", "Save settings", "Users"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin page missing %q: %s", want, body)
		}
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("admin page has no Content-Security-Policy")
	}
}

func TestAdminPageRendersFlash(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)

	rec := doRequest(t, s, http.MethodGet, "/admin?ok="+flashSettingsSaved, nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), flashMessages[flashSettingsSaved]) {
		t.Fatalf("status = %d, success flash not rendered", rec.Code)
	}
	rec = doRequest(t, s, http.MethodGet, "/admin?err="+flashBadUser, nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), flashMessages[flashBadUser]) {
		t.Fatalf("status = %d, error flash not rendered", rec.Code)
	}
}

func TestAdminPageNotFoundForNonAdmin(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	user := newAccountWithToken(t, s, "user@example.test", "user-token", false)

	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, user.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /admin as non-admin = %d, want 404", rec.Code)
	}
}

func TestAdminPageRedirectsAnonymous(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	newAccountWithToken(t, s, "admin@example.test", "admin-token", true)

	rec := doRequest(t, s, http.MethodGet, "/admin", nil)
	assertDashboardRedirect(t, rec, "/login")
}

func TestAdminPageRedirectsToSetupWhenRequired(t *testing.T) {
	s, store, _, _ := newFaultServer(t)
	if _, err := store.Exec("DELETE FROM accounts"); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(t, s, http.MethodGet, "/admin", nil)
	assertDashboardRedirect(t, rec, "/setup")
}

func TestDashboardShowsAdminLinkOnlyForAdmins(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	user := newAccountWithToken(t, s, "user@example.test", "user-token", false)

	rec := doRequest(t, s, http.MethodGet, "/dashboard", nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard as admin = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/admin"`) {
		t.Fatalf("admin dashboard missing Admin link: %s", body)
	}
	for _, unwanted := range []string{"Save settings", "Create invite", "Invitations"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("dashboard still renders admin content %q", unwanted)
		}
	}

	rec = doRequest(t, s, http.MethodGet, "/dashboard", nil, asSession(s, user.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard as user = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `href="/admin"`) {
		t.Fatal("non-admin dashboard shows Admin link")
	}
}

func TestAdminFlashRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	adminError(rec, httptest.NewRequest(http.MethodPost, "/dashboard/settings", nil), flashSettingsFailed)
	assertDashboardRedirect(t, rec, "/admin?err="+flashSettingsFailed)

	rec = httptest.NewRecorder()
	adminOK(rec, httptest.NewRequest(http.MethodPost, "/dashboard/settings", nil), flashSettingsSaved)
	assertDashboardRedirect(t, rec, "/admin?ok="+flashSettingsSaved)

	rec = httptest.NewRecorder()
	adminFlash(rec, httptest.NewRequest(http.MethodPost, "/dashboard/settings", nil), url.Values{"err": {flashBadUser}})
	assertDashboardRedirect(t, rec, "/admin?err="+flashBadUser)
}

func TestAdminPageCSRFGenerationFailure(t *testing.T) {
	s, _, _, adminID := newFaultServer(t)
	stubSecureRandomRead(t, func([]byte) (int, error) { return 0, errors.New("rand failed") })
	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, adminID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestParseAdminFormRedirectsToAdminOnBadForm(t *testing.T) {
	s, _, _, _ := newFaultServer(t)

	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader("csrf=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	if s.parseAdminForm(rec, req) {
		t.Fatal("parseAdminForm accepted a malformed body")
	}
	assertDashboardRedirect(t, rec, "/admin?err="+flashInvalidSession)

	req = httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(url.Values{"csrf": {"wrong"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec = httptest.NewRecorder()
	if s.parseAdminForm(rec, req) {
		t.Fatal("parseAdminForm accepted a CSRF mismatch")
	}
	assertDashboardRedirect(t, rec, "/admin?err="+flashInvalidSession)
}

func TestParseAdminFormReportsCSRFGenerationFailure(t *testing.T) {
	s, _, _, _ := newFaultServer(t)
	stubSecureRandomRead(t, func([]byte) (int, error) { return 0, errors.New("rand down") })
	form := url.Values{"csrf": {"csrf-token"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
	rec := httptest.NewRecorder()
	if s.parseAdminForm(rec, req) {
		t.Fatal("parseAdminForm succeeded without entropy")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestAdminGoogleOrgHidesInvites(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	ctx := context.Background()
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	for k, v := range map[string]string{
		authAllowedEmailDomainSetting: "example.test",
		"oauth_google_enabled":        "true",
		"oauth_google_client_id":      "google-client",
		authGoogleOrgSignupSetting:    "true",
	} {
		if err := s.store.SetSetting(ctx, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	if err := s.encryptedSetSetting(ctx, "oauth_google_client_secret", "google-secret"); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "invite") {
		t.Fatal("admin page mentions invites under Google org sign-in")
	}
}

func TestAdminShowsEmailTab(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	body := rec.Body.String()
	for _, want := range []string{`name="smtp_host"`, `action="/dashboard/email/test"`, `<option value="starttls" selected>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin page missing %s", want)
		}
	}
}

var backendSettingKeys = []string{
	"backend_allowed_images", "backend_memory_mb", "backend_cpu_millis", "backend_idle_minutes",
	"backend_max_running", "backend_registry_server", "backend_registry_username", "backend_registry_password",
}

func TestAdminBackendsTab(t *testing.T) {
	const offLine = "Backends are off"
	check := func(t *testing.T, s *Server, wantOff bool) {
		t.Helper()
		admin := newAccountWithToken(t, s, "admin@example.test", "admin-tab-token", true)
		rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin = %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "settingsTab === 'backends'") {
			t.Fatal("admin page has no Backends tab")
		}
		for _, k := range backendSettingKeys {
			if !strings.Contains(body, `name="`+k+`"`) {
				t.Fatalf("admin page missing input %s", k)
			}
		}
		if got := strings.Contains(body, offLine); got != wantOff {
			t.Fatalf("contains %q = %v, want %v", offLine, got, wantOff)
		}
	}
	t.Run("off", func(t *testing.T) {
		s, _ := newHandlerTestServer(t)
		check(t, s, true)
	})
	t.Run("on", func(t *testing.T) {
		s, _, _ := newBackendTestServer(t)
		check(t, s, false)
	})
}

func TestAdminBackendsSettingsSave(t *testing.T) {
	s, _, _ := newBackendTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-save-token", true)
	post := func(value string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {"csrf-token"}, "backend_memory_mb": {value}}
		return doRequest(t, s, http.MethodPost, "/dashboard/settings", strings.NewReader(form.Encode()),
			asSession(s, admin.ID),
			func(r *http.Request) {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "csrf-token"})
			})
	}
	if rec := post("512"); rec.Code >= 400 {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := s.store.GetSetting(context.Background(), "backend_memory_mb"); got != "512" {
		t.Fatalf("backend_memory_mb = %q, want 512", got)
	}
	rec := post("1")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "backend_memory_mb must be between") {
		t.Fatalf("out-of-range save = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := s.store.GetSetting(context.Background(), "backend_memory_mb"); got != "512" {
		t.Fatalf("backend_memory_mb changed to %q", got)
	}
}

func TestAdminPageListsProvisionersWithoutKey(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	f := newProvisionedFixture(t, s, "PrivacyFence", "u@example.test", "e1")

	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "PrivacyFence") || !strings.Contains(body, "…"+f.Provisioner.Hint) {
		t.Fatalf("GET /admin = %d, provisioner not listed: %s", rec.Code, body)
	}
	if strings.Contains(body, f.Key) {
		t.Fatal("admin page leaks the provisioning key")
	}
	if !strings.Contains(body, "Managed by PrivacyFence") {
		t.Fatal("admin page missing Managed by badge")
	}
	if strings.Contains(body, "No provisioning keys.") {
		t.Fatal("empty text shown with a key present")
	}
}

func TestAdminPageShowsNoProvisioningKeys(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)

	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No provisioning keys.") {
		t.Fatalf("GET /admin = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Managed by") {
		t.Fatal("Managed by badge without a provisioner")
	}
}

func TestFileProvisionerHasNoRevoke(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	s.provisioningKeyName = "PrivacyFence"
	if err := s.seedProvisioner(context.Background(), "PrivacyFence", seedKeyA); err != nil {
		t.Fatal(err)
	}
	other := newProvisionedFixture(t, s, "Other", "u@example.test", "e1")

	body := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID)).Body.String()
	if !strings.Contains(body, "Registered from PEEK_PROVISIONING_KEY_FILE: replace the file and restart to rotate it.") {
		t.Fatal("sentence missing")
	}
	if strings.Count(body, "/revoke\"") < 1 || !strings.Contains(body, "/dashboard/provisioners/"+strconv.FormatInt(other.Provisioner.ID, 10)+"/revoke") {
		t.Fatal("other provisioner lost its Revoke")
	}
	list, _ := s.store.ListProvisioners(context.Background())
	for _, p := range list {
		if p.Name == "PrivacyFence" && strings.Contains(body, "/dashboard/provisioners/"+strconv.FormatInt(p.ID, 10)+"/revoke") {
			t.Fatal("file provisioner has a Revoke form")
		}
	}
}

func TestRevokeFileProvisionerRefused(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	s.provisioningKeyName = "PrivacyFence"
	if err := s.seedProvisioner(context.Background(), "PrivacyFence", seedKeyA); err != nil {
		t.Fatal(err)
	}
	p, err := s.store.GetProvisionerByKey(context.Background(), seedKeyA)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleDashboardProvisionerRevoke(rec, dashboardProvisionerRevokeRequest(s, admin.ID, strconv.FormatInt(p.ID, 10)))
	if rec.Header().Get("Location") != "/admin?err=provisioner_from_file" {
		t.Fatalf("location = %q", rec.Header().Get("Location"))
	}
	if _, err := s.store.GetProvisionerByKey(context.Background(), seedKeyA); err != nil {
		t.Fatalf("provisioner was deleted: %v", err)
	}
	page := doRequest(t, s, http.MethodGet, rec.Header().Get("Location"), nil, asSession(s, admin.ID)).Body.String()
	if !strings.Contains(page, "registered from PEEK_PROVISIONING_KEY_FILE; replace the file and restart instead.") {
		t.Fatal("flash message not shown")
	}
}

func TestAdminLimitsBackendDataImport(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	admin := newAccountWithToken(t, s, "admin@example.test", "admin-limits-token", true)
	rec := doRequest(t, s, http.MethodGet, "/admin", nil, asSession(s, admin.ID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "setting-max_backend_data_import") {
		t.Fatalf("GET /admin = %d, limits row missing", rec.Code)
	}
	rec = dashboardPost(t, s, admin.ID, "/dashboard/settings", url.Values{"max_backend_data_import_mb": {"10"}}, true)
	if rec.Code >= 400 {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := s.store.GetSetting(context.Background(), "max_backend_data_import"); got != "10485760" {
		t.Fatalf("max_backend_data_import = %q", got)
	}
}
