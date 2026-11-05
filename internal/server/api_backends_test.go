package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/models"
)

const backendTestImage = "ghcr.io/acme/contact-form:1"

// backendFixture returns a backend-enabled server, the fake runtime, a
// two-version page and its owner's bearer token. The test image is allowed.
func backendFixture(t *testing.T, slug string) (*Server, *models.Upload, string) {
	t.Helper()
	s, _, _ := newBackendTestServer(t)
	return backendFixtureOn(t, s, slug)
}

func backendFixtureOn(t *testing.T, s *Server, slug string) (*Server, *models.Upload, string) {
	t.Helper()
	ctx := context.Background()
	if err := s.encryptedSetSetting(ctx, "backend_allowed_images", "ghcr.io/acme/"); err != nil {
		t.Fatal(err)
	}
	u := newTwoVersionPage(t, s, slug, models.UploadVisibilityPublic)
	tok := "owner-" + slug
	if err := s.store.CreateTokenForAccount(ctx, tok, u.OwnerAccountID, "owner"); err != nil {
		t.Fatalf("owner token: %v", err)
	}
	return s, u, tok
}

func backendReq(t *testing.T, s *Server, method, path, token, body string) (int, string) {
	t.Helper()
	opts := []func(*http.Request){withJSON()}
	if token != "" {
		opts = append(opts, asBearer(token))
	}
	rec := doRequest(t, s, method, path, strings.NewReader(body), opts...)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func setBackendJSON(image string, port int) string {
	b, _ := json.Marshal(backendSetBody{Image: image, Port: port})
	return string(b)
}

func TestBackendAPIAccess(t *testing.T) {
	s, u, tok := backendFixture(t, "b-auth")
	newAccountWithToken(t, s, "other@example.test", "other-tok", false)
	base := "/api/uploads/" + u.Slug
	routes := []struct{ method, path, body string }{
		{http.MethodGet, base + "/backend", ""},
		{http.MethodPut, base + "/versions/1/backend", setBackendJSON(backendTestImage, 0)},
		{http.MethodDelete, base + "/versions/1/backend", ""},
		{http.MethodGet, base + "/versions/1/backend/logs", ""},
		{http.MethodPut, base + "/backend/env/FOO", `{"value":"x"}`},
		{http.MethodDelete, base + "/backend/env/FOO", ""},
		{http.MethodPost, base + "/backend/restart", ""},
	}
	for _, rt := range routes {
		if code, b := backendReq(t, s, rt.method, rt.path, "", rt.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: %d %s", rt.method, rt.path, code, b)
		}
		if code, b := backendReq(t, s, rt.method, rt.path, "other-tok", rt.body); code != http.StatusForbidden || !strings.Contains(b, "not owner") {
			t.Errorf("%s %s as other: %d %s", rt.method, rt.path, code, b)
		}
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/versions/1/backend", tok, setBackendJSON(backendTestImage, 0)); code != http.StatusOK {
		t.Fatalf("owner PUT: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/versions/2/backend", backendTestAdminToken, setBackendJSON(backendTestImage, 9000)); code != http.StatusOK {
		t.Fatalf("admin PUT: %d %s", code, b)
	}
	if code, _ := backendReq(t, s, http.MethodGet, "/api/uploads/missing/backend", backendTestAdminToken, ""); code != http.StatusNotFound {
		t.Fatalf("missing page: %d", code)
	}
}

func TestBackendAPISetAndGet(t *testing.T) {
	s, u, tok := backendFixture(t, "b-set")
	base := "/api/uploads/" + u.Slug
	code, b := backendReq(t, s, http.MethodPut, base+"/versions/1/backend", tok, setBackendJSON(backendTestImage, 0))
	if code != http.StatusOK || !strings.Contains(b, `"port":8080`) || !strings.Contains(b, `"running":false`) {
		t.Fatalf("PUT: %d %s", code, b)
	}
	code, b = backendReq(t, s, http.MethodGet, base+"/backend", tok, "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, b)
	}
	var info backendInfoOut
	if err := json.Unmarshal([]byte(b), &info); err != nil || !info.Enabled || len(info.Versions) != 1 ||
		info.Versions[0].Version != 1 || info.Versions[0].Image != backendTestImage || info.Versions[0].Port != 8080 || len(info.Env) != 0 {
		t.Fatalf("GET body = %s (%v)", b, err)
	}
	if !strings.Contains(b, `"env":[]`) {
		t.Fatalf("env should be an empty array: %s", b)
	}
}

func TestBackendAPIValidatesInput(t *testing.T) {
	s, u, tok := backendFixture(t, "b-valid")
	path := "/api/uploads/" + u.Slug + "/versions/1/backend"
	cases := []struct {
		name, body, msg string
	}{
		{"invalid ref", setBackendJSON("Not A Ref!", 0), "invalid backend image reference"},
		{"not allowed", setBackendJSON("docker.io/evil/app:1", 0), "backend image is not allowed; ask an admin to add its registry to the allowed backend images"},
		{"port too high", setBackendJSON(backendTestImage, 70000), "backend port must be between 1 and 65535"},
		{"port negative", setBackendJSON(backendTestImage, -1), "backend port must be between 1 and 65535"},
		{"bad json", `{`, "bad json"},
	}
	for _, c := range cases {
		code, b := backendReq(t, s, http.MethodPut, path, tok, c.body)
		if code != http.StatusBadRequest || !strings.Contains(b, c.msg) {
			t.Errorf("%s: %d %s", c.name, code, b)
		}
	}
	// With nothing allowed, even a valid reference is refused.
	if err := s.encryptedSetSetting(context.Background(), "backend_allowed_images", ""); err != nil {
		t.Fatal(err)
	}
	if code, b := backendReq(t, s, http.MethodPut, path, tok, setBackendJSON(backendTestImage, 0)); code != http.StatusBadRequest || !strings.Contains(b, "not allowed") {
		t.Fatalf("empty allow list: %d %s", code, b)
	}
}

func TestBackendAPIResolvesVersion(t *testing.T) {
	s, u, tok := backendFixture(t, "b-ver")
	base := "/api/uploads/" + u.Slug
	if code, b := backendReq(t, s, http.MethodPut, base+"/versions/latest/backend", tok, setBackendJSON(backendTestImage, 0)); code != http.StatusOK || !strings.Contains(b, `"version":2`) {
		t.Fatalf("latest: %d %s", code, b)
	}
	if bk, err := s.store.GetUploadBackend(context.Background(), u.ID, 2); err != nil || bk.Image != backendTestImage {
		t.Fatalf("stored backend = %+v, %v", bk, err)
	}
	for _, n := range []string{"3", "0", "abc"} {
		if code, b := backendReq(t, s, http.MethodPut, base+"/versions/"+n+"/backend", tok, setBackendJSON(backendTestImage, 0)); code != http.StatusNotFound || !strings.Contains(b, "version not found") {
			t.Errorf("version %s: %d %s", n, code, b)
		}
	}
}

func TestBackendAPIPutStopsRunningBackend(t *testing.T) {
	s, fake, _ := newBackendTestServer(t)
	s, u, tok := backendFixtureOn(t, s, "b-put-run")
	ctx := context.Background()
	port := newBackendListener(t)
	if err := s.store.SetUploadBackend(ctx, models.UploadBackend{UploadID: u.ID, Version: 1, Image: backendTestImage, Port: port}); err != nil {
		t.Fatal(err)
	}
	row, _ := s.store.GetUploadBackend(ctx, u.ID, 1)
	spec, err := s.backendSpec(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.backends.Ensure(ctx, spec); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	path := "/api/uploads/" + u.Slug
	code, b := backendReq(t, s, http.MethodGet, path+"/backend", tok, "")
	if code != http.StatusOK || !strings.Contains(b, `"running":true`) {
		t.Fatalf("GET running: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, path+"/versions/1/backend", tok, setBackendJSON("ghcr.io/acme/contact-form:2", port)); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, b)
	}
	if len(fake.Stopped) != 1 {
		t.Fatalf("Stopped = %v, want one container stopped", fake.Stopped)
	}
	if s.backends.Running(u.ID, 1) {
		t.Fatal("backend still running after PUT")
	}
	if bk, _ := s.store.GetUploadBackend(ctx, u.ID, 1); bk.Image != "ghcr.io/acme/contact-form:2" {
		t.Fatalf("stored image = %q", bk.Image)
	}
}

func TestBackendAPIDelete(t *testing.T) {
	s, u, tok := backendFixture(t, "b-del")
	path := "/api/uploads/" + u.Slug + "/versions/1/backend"
	if code, b := backendReq(t, s, http.MethodDelete, path, tok, ""); code != http.StatusNotFound || !strings.Contains(b, "no backend for this version") {
		t.Fatalf("DELETE none: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, path, tok, setBackendJSON(backendTestImage, 0)); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodDelete, path, tok, ""); code != http.StatusOK || !strings.Contains(b, `"status":"ok"`) {
		t.Fatalf("DELETE: %d %s", code, b)
	}
	if _, err := s.store.GetUploadBackend(context.Background(), u.ID, 1); err == nil {
		t.Fatal("backend row still present")
	}
}

func TestBackendAPIEnvValidation(t *testing.T) {
	s, u, tok := backendFixture(t, "b-envval")
	base := "/api/uploads/" + u.Slug + "/backend/env/"
	nameMsg := "invalid variable name; use A-Z, 0-9 and _, not PORT or PEEK_*"
	for _, name := range []string{"PORT", "PEEK_X", "lower", "1ABC", "A-B", strings.Repeat("A", 65)} {
		if code, b := backendReq(t, s, http.MethodPut, base+name, tok, `{"value":"x"}`); code != http.StatusBadRequest || !strings.Contains(b, nameMsg) {
			t.Errorf("name %q: %d %s", name, code, b)
		}
	}
	long := `{"value":"` + strings.Repeat("a", 4097) + `"}`
	if code, b := backendReq(t, s, http.MethodPut, base+"BIG", tok, long); code != http.StatusBadRequest || !strings.Contains(b, "value too long") {
		t.Fatalf("long value: %d %s", code, b)
	}
	ok4096 := `{"value":"` + strings.Repeat("a", 4096) + `"}`
	if code, b := backendReq(t, s, http.MethodPut, base+"BIG", tok, ok4096); code != http.StatusOK {
		t.Fatalf("4096-byte value: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"FOO", tok, `nope`); code != http.StatusBadRequest || !strings.Contains(b, "bad json") {
		t.Fatalf("bad json: %d %s", code, b)
	}
}

func TestBackendAPIEnvLimit(t *testing.T) {
	s, u, tok := backendFixture(t, "b-envmax")
	base := "/api/uploads/" + u.Slug + "/backend/env/"
	for i := 0; i < 50; i++ {
		name := "VAR_" + string(rune('A'+i/26)) + string(rune('A'+i%26))
		if code, b := backendReq(t, s, http.MethodPut, base+name, tok, `{"value":"x"}`); code != http.StatusOK {
			t.Fatalf("var %d: %d %s", i, code, b)
		}
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"ONE_TOO_MANY", tok, `{"value":"x"}`); code != http.StatusBadRequest || !strings.Contains(b, "too many variables (max 50)") {
		t.Fatalf("51st: %d %s", code, b)
	}
	// Updating an existing variable at the limit still works.
	if code, b := backendReq(t, s, http.MethodPut, base+"VAR_AA", tok, `{"value":"y"}`); code != http.StatusOK {
		t.Fatalf("update at limit: %d %s", code, b)
	}
}

func TestBackendAPIEnvEncryptedAndHidden(t *testing.T) {
	s, u, tok := backendFixture(t, "b-envenc")
	base := "/api/uploads/" + u.Slug
	code, b := backendReq(t, s, http.MethodPut, base+"/backend/env/SMTP_PASSWORD", tok, `{"value":"hunter2-secret"}`)
	if code != http.StatusOK || !strings.Contains(b, `"restart_required":true`) {
		t.Fatalf("set: %d %s", code, b)
	}
	vars, err := s.store.ListUploadBackendEnv(context.Background(), u.ID)
	if err != nil || len(vars) != 1 {
		t.Fatalf("stored vars = %v, %v", vars, err)
	}
	if vars[0].Value == "hunter2-secret" || strings.Contains(vars[0].Value, "hunter2") {
		t.Fatal("env value stored in plaintext")
	}
	if dec, err := decryptSecret(s.secret, vars[0].Value); err != nil || dec != "hunter2-secret" {
		t.Fatalf("decrypt = %q, %v", dec, err)
	}
	code, b = backendReq(t, s, http.MethodGet, base+"/backend", tok, "")
	if code != http.StatusOK || strings.Contains(b, "hunter2") || strings.Contains(b, vars[0].Value) || !strings.Contains(b, `"name":"SMTP_PASSWORD"`) {
		t.Fatalf("GET leaks or lacks env: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodDelete, base+"/backend/env/SMTP_PASSWORD", tok, ""); code != http.StatusOK || !strings.Contains(b, `"restart_required":true`) {
		t.Fatalf("unset: %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodDelete, base+"/backend/env/SMTP_PASSWORD", tok, ""); code != http.StatusNotFound || !strings.Contains(b, "no such variable") {
		t.Fatalf("unset again: %d %s", code, b)
	}
}

func TestBackendAPILogs(t *testing.T) {
	s, fake, _ := newBackendTestServer(t)
	s, u, tok := backendFixtureOn(t, s, "b-logs")
	ctx := context.Background()
	base := "/api/uploads/" + u.Slug + "/versions/1/backend/logs"
	if code, b := backendReq(t, s, http.MethodGet, base, tok, ""); code != http.StatusNotFound || !strings.Contains(b, "backend is not running") {
		t.Fatalf("not running: %d %s", code, b)
	}
	port := newBackendListener(t)
	if _, err := s.backends.Ensure(ctx, backends.Spec{UploadID: u.ID, Version: 1, Image: backendTestImage, Port: port}); err != nil {
		t.Fatal(err)
	}
	fake.LogsData = []byte("hello from the backend\n")
	rec := doRequest(t, s, http.MethodGet, base+"?tail=5", nil, asBearer(tok))
	if rec.Code != http.StatusOK || rec.Body.String() != "hello from the backend\n" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("logs: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	for _, tail := range []string{"0", "2001", "abc", "-3"} {
		if code, b := backendReq(t, s, http.MethodGet, base+"?tail="+tail, tok, ""); code != http.StatusBadRequest || !strings.Contains(b, "tail must be between 1 and 2000") {
			t.Errorf("tail %s: %d %s", tail, code, b)
		}
	}
	if code, _ := backendReq(t, s, http.MethodGet, base+"?tail=2000", tok, ""); code != http.StatusOK {
		t.Fatalf("tail 2000: %d", code)
	}
}

func TestBackendAPIRestartStopsAllVersions(t *testing.T) {
	s, fake, _ := newBackendTestServer(t)
	s, u, tok := backendFixtureOn(t, s, "b-restart")
	ctx := context.Background()
	port := newBackendListener(t)
	for v := 1; v <= 2; v++ {
		if _, err := s.backends.Ensure(ctx, backends.Spec{UploadID: u.ID, Version: v, Image: backendTestImage, Port: port}); err != nil {
			t.Fatal(err)
		}
	}
	if code, b := backendReq(t, s, http.MethodPost, "/api/uploads/"+u.Slug+"/backend/restart", tok, ""); code != http.StatusOK || !strings.Contains(b, `"status":"ok"`) {
		t.Fatalf("restart: %d %s", code, b)
	}
	if len(fake.Stopped) != 2 || s.backends.Running(u.ID, 1) || s.backends.Running(u.ID, 2) {
		t.Fatalf("Stopped = %v, want both versions stopped", fake.Stopped)
	}
}

func TestBackendAPIWritesAuditActions(t *testing.T) {
	s, u, tok := backendFixture(t, "b-audit")
	base := "/api/uploads/" + u.Slug
	steps := []struct{ method, path, body, action string }{
		{http.MethodPut, base + "/versions/1/backend", setBackendJSON(backendTestImage, 0), "backend.set"},
		{http.MethodPut, base + "/backend/env/FOO", `{"value":"x"}`, "backend.env.set"},
		{http.MethodDelete, base + "/backend/env/FOO", "", "backend.env.unset"},
		{http.MethodPost, base + "/backend/restart", "", "backend.restart"},
		{http.MethodPut, base + "/backend/token-calls", `{"enabled":true}`, "backend.token_calls"},
		{http.MethodDelete, base + "/versions/1/backend", "", "backend.remove"},
	}
	for _, st := range steps {
		if code, b := backendReq(t, s, st.method, st.path, tok, st.body); code != http.StatusOK {
			t.Fatalf("%s: %d %s", st.action, code, b)
		}
	}
	entries, err := s.store.ListAuditLog(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	details := map[string]string{}
	for _, e := range entries {
		details[e.Action] = e.Detail
	}
	for _, st := range steps {
		d, ok := details[st.action]
		if !ok || !strings.Contains(d, "slug=b-audit") {
			t.Errorf("audit %s missing or without slug: %q", st.action, d)
		}
	}
	if d := details["backend.token_calls"]; !strings.Contains(d, "enabled=true") {
		t.Errorf("backend.token_calls detail = %q", d)
	}
	if d := details["backend.set"]; !strings.Contains(d, "version=1") || !strings.Contains(d, "image="+backendTestImage) || !strings.Contains(d, "port=8080") {
		t.Errorf("backend.set detail = %q", d)
	}
	if d := details["backend.env.set"]; !strings.Contains(d, "name=FOO") || strings.Contains(d, "value") {
		t.Errorf("backend.env.set detail = %q", d)
	}
}

func TestBackendAPIDisabledServer(t *testing.T) {
	s, adminTok := newHandlerTestServer(t)
	u := newTwoVersionPage(t, s, "b-off", models.UploadVisibilityPublic)
	base := "/api/uploads/" + u.Slug
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPut, base + "/versions/1/backend", setBackendJSON(backendTestImage, 0)},
		{http.MethodDelete, base + "/versions/1/backend", ""},
		{http.MethodPut, base + "/backend/env/FOO", `{"value":"x"}`},
		{http.MethodPost, base + "/backend/restart", ""},
	} {
		if code, b := backendReq(t, s, rt.method, rt.path, adminTok, rt.body); code != http.StatusConflict || !strings.Contains(b, "backends are not enabled on this server") {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, code, b)
		}
	}
	code, b := backendReq(t, s, http.MethodGet, base+"/backend", adminTok, "")
	if code != http.StatusOK || !strings.Contains(b, `"enabled":false`) {
		t.Fatalf("GET: %d %s", code, b)
	}
	// Cleanup still works: a stored variable can be unset and logs are 404.
	if err := s.store.SetUploadBackendEnv(context.Background(), u.ID, "OLD", "x"); err != nil {
		t.Fatal(err)
	}
	if code, b := backendReq(t, s, http.MethodDelete, base+"/backend/env/OLD", adminTok, ""); code != http.StatusOK {
		t.Fatalf("unset when off: %d %s", code, b)
	}
	if code, _ := backendReq(t, s, http.MethodGet, base+"/versions/1/backend/logs", adminTok, ""); code != http.StatusNotFound {
		t.Fatalf("logs when off: %d", code)
	}
}

func TestBackendAPITokenCalls(t *testing.T) {
	s, u, tok := backendFixture(t, "b-tcalls")
	base := "/api/uploads/" + u.Slug
	newAccountWithToken(t, s, "admin@example.test", "admin-tok", true)
	newAccountWithToken(t, s, "other@example.test", "other-tok", false)

	getFlag := func() bool {
		code, b := backendReq(t, s, http.MethodGet, base+"/backend", tok, "")
		if code != http.StatusOK {
			t.Fatalf("GET backend = %d %s", code, b)
		}
		var out backendInfoOut
		if err := json.Unmarshal([]byte(b), &out); err != nil {
			t.Fatal(err)
		}
		return out.TokenCalls
	}
	if getFlag() {
		t.Fatal("token_calls on by default")
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", tok, `{"enabled":true}`); code != http.StatusOK || b != `{"status":"ok","token_calls":true}` {
		t.Fatalf("owner PUT = %d %s", code, b)
	}
	if !getFlag() {
		t.Fatal("token_calls not on after PUT")
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", "admin-tok", `{"enabled":false}`); code != http.StatusOK || b != `{"status":"ok","token_calls":false}` {
		t.Fatalf("admin PUT = %d %s", code, b)
	}
	if getFlag() {
		t.Fatal("token_calls still on")
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", "other-tok", `{"enabled":true}`); code != http.StatusForbidden || !strings.Contains(b, "not owner") {
		t.Fatalf("other PUT = %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", tok, `{`); code != http.StatusBadRequest || !strings.Contains(b, "bad json") {
		t.Fatalf("bad json = %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", tok, `{}`); code != http.StatusBadRequest || !strings.Contains(b, "enabled must be true or false") {
		t.Fatalf("empty = %d %s", code, b)
	}
	if _, err := s.store.Exec(`DROP TABLE upload_backend_settings`); err != nil {
		t.Fatal(err)
	}
	if code, b := backendReq(t, s, http.MethodPut, base+"/backend/token-calls", tok, `{"enabled":true}`); code != http.StatusInternalServerError || !strings.Contains(b, "db error") {
		t.Fatalf("db error PUT = %d %s", code, b)
	}
	if code, b := backendReq(t, s, http.MethodGet, base+"/backend", tok, ""); code != http.StatusInternalServerError || !strings.Contains(b, "db error") {
		t.Fatalf("db error GET = %d %s", code, b)
	}

	off, offTok := newHandlerTestServer(t)
	if code, b := backendReq(t, off, http.MethodPut, "/api/uploads/any/backend/token-calls", offTok, `{"enabled":true}`); code != http.StatusConflict && code != http.StatusNotFound {
		t.Fatalf("backends off = %d %s", code, b)
	}
}
