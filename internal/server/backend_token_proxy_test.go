package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/privacyfence/peek/internal/models"
)

// tokenCallFixture is proxyFixture with token calls turned on, plus the page owner's token.
func tokenCallFixture(t *testing.T, slug, visibility string, tb *testBackend) (*Server, *models.Upload, string) {
	t.Helper()
	s, _, u := proxyFixture(t, slug, visibility, tb)
	if err := s.store.SetUploadBackendTokenCalls(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	tok := "owner-" + slug
	if err := s.store.CreateTokenForAccount(context.Background(), tok, u.OwnerAccountID, "owner"); err != nil {
		t.Fatal(err)
	}
	return s, u, tok
}

func tokenCall(t *testing.T, s *Server, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	var opts []func(*http.Request)
	if token != "" {
		opts = append(opts, asBearer(token))
	}
	return doRequest(t, s, method, path, body, opts...)
}

func TestBackendTokenCallAccess(t *testing.T) {
	for _, vis := range []string{models.UploadVisibilityPublic, models.UploadVisibilitySignedIn, models.UploadVisibilityRestricted} {
		tb := startTestBackend(t, nil)
		slug := "tk-acc-" + vis
		s, u, ownerTok := tokenCallFixture(t, slug, vis, tb)
		ctx := context.Background()
		newAccountWithToken(t, s, "admin@example.test", "admin-tok", true)
		grantee := newAccountWithToken(t, s, "grantee@example.test", "grantee-tok", false)
		newAccountWithToken(t, s, "other@example.test", "other-tok", false)
		auto, err := s.store.CreateAccount(ctx, "", "automation", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store.CreateTokenForAccount(ctx, "auto-tok", auto.ID, "auto"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.store.AddUploadGrant(ctx, u.ID, models.UploadGrantUser, grantee.Email, u.OwnerAccountID); err != nil {
			t.Fatal(err)
		}
		restricted := vis == models.UploadVisibilityRestricted
		for _, tc := range []struct {
			name, tok string
			allowed   bool
		}{
			{"owner", ownerTok, true},
			{"admin", "admin-tok", true},
			{"grantee", "grantee-tok", true},
			{"other", "other-tok", !restricted},
			{"automation", "auto-tok", !restricted},
		} {
			rec := tokenCall(t, s, http.MethodGet, "/api/uploads/"+slug+"/versions/1/svc/x", tc.tok, nil)
			if tc.allowed && rec.Code != http.StatusCreated {
				t.Errorf("%s/%s = %d %s, want relayed", vis, tc.name, rec.Code, rec.Body.String())
			}
			if !tc.allowed && (rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "access denied")) {
				t.Errorf("%s/%s = %d %s, want 403 access denied", vis, tc.name, rec.Code, rec.Body.String())
			}
		}
		if restricted {
			rec := tokenCall(t, s, http.MethodGet, "/api/uploads/"+slug+"/versions/9/svc/x", "other-tok", nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("unknown version for caller without access = %d, want 403", rec.Code)
			}
		}
	}
}

func TestBackendTokenCallOptIn(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, u, ownerTok := tokenCallFixture(t, "tk-off", models.UploadVisibilityPublic, tb)
	if err := s.store.SetUploadBackendTokenCalls(context.Background(), u.ID, false); err != nil {
		t.Fatal(err)
	}
	newAccountWithToken(t, s, "admin@example.test", "admin-tok", true)
	newAccountWithToken(t, s, "other@example.test", "other-tok", false)
	for _, tok := range []string{ownerTok, "admin-tok", "other-tok"} {
		rec := tokenCall(t, s, http.MethodGet, "/api/uploads/tk-off/versions/1/svc/x", tok, nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "token calls are not enabled for this backend") {
			t.Errorf("%s = %d %s", tok, rec.Code, rec.Body.String())
		}
	}
	if got := tb.seen(); got.Method != "" {
		t.Fatalf("backend saw %+v", got)
	}
}

func TestBackendTokenCallHeaders(t *testing.T) {
	for _, vis := range []string{models.UploadVisibilityPublic, models.UploadVisibilitySignedIn, models.UploadVisibilityRestricted} {
		tb := startTestBackend(t, nil)
		slug := "tk-hdr-" + vis
		s, u, tok := tokenCallFixture(t, slug, vis, tb)
		rec := tokenCall(t, s, http.MethodPost, "/api/uploads/"+slug+"/versions/1/svc/api/x", tok, strings.NewReader("hi"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", vis, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Peek-Relay") != "backend" {
			t.Errorf("%s: X-Peek-Relay = %q", vis, rec.Header().Get("X-Peek-Relay"))
		}
		h := tb.seen().Header
		if h.Get("X-Peek-Caller") != "token" {
			t.Errorf("%s: caller = %q", vis, h.Get("X-Peek-Caller"))
		}
		if want := s.tokenCallerHash(u.ID, u.OwnerAccountID); h.Get("X-Peek-Viewer") != want {
			t.Errorf("%s: viewer = %q, want %q", vis, h.Get("X-Peek-Viewer"), want)
		}
		if vis == models.UploadVisibilityPublic {
			if h.Get("X-Peek-User-Id") != "" || h.Get("X-Peek-User-Email") != "" {
				t.Errorf("public: identity leaked: %v", h)
			}
			continue
		}
		if h.Get("X-Peek-User-Id") != strconv.FormatInt(u.OwnerAccountID, 10) || h.Get("X-Peek-User-Email") != "owner-"+slug+"@example.test" {
			t.Errorf("%s: user headers = %v", vis, h)
		}
	}
}

func TestBackendTokenCallPathAndVersion(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, u, tok := tokenCallFixture(t, "tk-path", models.UploadVisibilityPublic, tb)
	rec := tokenCall(t, s, http.MethodGet, "/api/uploads/tk-path/versions/1/svc/api/a%3Fb?x=1&y=2", tok, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("relay = %d %s", rec.Code, rec.Body.String())
	}
	got := tb.seen()
	if got.RawPath != "/api/a%3Fb" || got.Path != "/api/a?b" || got.RawQuery != "x=1&y=2" {
		t.Fatalf("backend saw path=%q raw=%q query=%q", got.Path, got.RawPath, got.RawQuery)
	}
	// latest is version 2, which gets its own backend.
	tb2 := startTestBackend(t, nil)
	if err := s.store.SetUploadBackend(context.Background(), models.UploadBackend{UploadID: u.ID, Version: 2, Image: backendTestImage, Port: tb2.port()}); err != nil {
		t.Fatal(err)
	}
	rec = tokenCall(t, s, http.MethodGet, "/api/uploads/tk-path/versions/latest/svc/", tok, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("latest = %d %s", rec.Code, rec.Body.String())
	}
	if g := tb2.seen(); g.Path != "/" || g.Header.Get("X-Peek-Version") != "2" {
		t.Fatalf("latest backend saw %+v", g)
	}
}

func TestBackendTokenCallErrors(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, tok := tokenCallFixture(t, "tk-err", models.UploadVisibilityPublic, tb)
	for _, tc := range []struct {
		name, path, tok string
		code            int
		msg             string
	}{
		{"no token", "/api/uploads/tk-err/versions/1/svc/x", "", http.StatusUnauthorized, ""},
		{"unknown page", "/api/uploads/nope/versions/1/svc/x", tok, http.StatusNotFound, "not found"},
		{"unknown version", "/api/uploads/tk-err/versions/9/svc/x", tok, http.StatusNotFound, "version not found"},
		{"no backend", "/api/uploads/tk-err/versions/2/svc/x", tok, http.StatusNotFound, "this page version has no backend"},
	} {
		rec := tokenCall(t, s, http.MethodGet, tc.path, tc.tok, nil)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s = %d %s, want %d %q", tc.name, rec.Code, rec.Body.String(), tc.code, tc.msg)
		}
		if rec.Header().Get("X-Peek-Relay") != "" {
			t.Errorf("%s: Peek's own error carries X-Peek-Relay", tc.name)
		}
	}

	off, offTok := newHandlerTestServer(t)
	rec := tokenCall(t, off, http.MethodGet, "/api/uploads/any/versions/1/svc/x", offTok, nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "backends are not enabled on this server") {
		t.Errorf("backends off = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackendTokenCallBodyLimit(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, tok := tokenCallFixture(t, "tk-big", models.UploadVisibilityPublic, tb)
	rec := tokenCall(t, s, http.MethodPost, "/api/uploads/tk-big/versions/1/svc/x", tok, strings.NewReader(strings.Repeat("a", 1<<20)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("1 MiB = %d %s", rec.Code, rec.Body.String())
	}
	if len(tb.seen().Body) != 1<<20 {
		t.Fatalf("backend got %d bytes", len(tb.seen().Body))
	}
	rec = tokenCall(t, s, http.MethodPost, "/api/uploads/tk-big/versions/1/svc/x", tok, strings.NewReader(strings.Repeat("a", 1<<20+1)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB + 1 = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/uploads/tk-big/versions/1/svc/x", io.NopCloser(strings.NewReader(strings.Repeat("a", 1<<20+1))))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 1 MiB + 1 = %d", rr.Code)
	}
}

func TestBackendTokenCallRateLimit(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, tok := tokenCallFixture(t, "tk-rate", models.UploadVisibilityPublic, tb)
	var last *httptest.ResponseRecorder
	for i := 0; i < 241; i++ {
		// Version 2 has no backend: cheap 404s still count against the limiter.
		last = tokenCall(t, s, http.MethodGet, "/api/uploads/tk-rate/versions/2/svc/x", tok, nil)
		if i < 240 && last.Code == http.StatusTooManyRequests {
			t.Fatalf("limited early at request %d", i+1)
		}
	}
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "too many backend requests, slow down") {
		t.Fatalf("request 241 = %d %s", last.Code, last.Body.String())
	}
}

func TestBackendTokenCallCounter(t *testing.T) {
	ctx := context.Background()
	var n atomic.Int32
	tb := startTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	s, u, tok := tokenCallFixture(t, "tk-cnt", models.UploadVisibilityPublic, tb)
	for i := 0; i < 2; i++ {
		tokenCall(t, s, http.MethodGet, "/api/uploads/tk-cnt/versions/1/svc/x", tok, nil)
	}
	rows, err := s.store.ListBackendTokenCalls(ctx, u.ID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err %v", rows, err)
	}
	if r := rows[0]; r.Calls != 2 || r.LastStatus != 500 || r.Identified || r.Version != 1 || r.AccountID != u.OwnerAccountID {
		t.Fatalf("row = %+v", r)
	}

	// A signed-in page marks the counter identified.
	tb2 := startTestBackend(t, nil)
	s2, u2, tok2 := tokenCallFixture(t, "tk-cnt2", models.UploadVisibilitySignedIn, tb2)
	tokenCall(t, s2, http.MethodGet, "/api/uploads/tk-cnt2/versions/1/svc/x", tok2, nil)
	rows, err = s2.store.ListBackendTokenCalls(ctx, u2.ID, 10)
	if err != nil || len(rows) != 1 || !rows[0].Identified || rows[0].Calls != 1 {
		t.Fatalf("signed-in rows = %+v, err %v", rows, err)
	}

	// Refused calls are not counted.
	newAccountWithToken(t, s2, "other@example.test", "other-tok", false)
	if err := s2.store.SetUploadBackendTokenCalls(ctx, u2.ID, false); err != nil {
		t.Fatal(err)
	}
	if rec := tokenCall(t, s2, http.MethodGet, "/api/uploads/tk-cnt2/versions/1/svc/x", tok2, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("off = %d", rec.Code)
	}
	tb3 := startTestBackend(t, nil)
	s3, u3, _ := tokenCallFixture(t, "tk-cnt3", models.UploadVisibilityRestricted, tb3)
	newAccountWithToken(t, s3, "other@example.test", "other-tok", false)
	if rec := tokenCall(t, s3, http.MethodGet, "/api/uploads/tk-cnt3/versions/1/svc/x", "other-tok", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("denied = %d", rec.Code)
	}
	for i := 0; i < 241; i++ {
		tokenCall(t, s3, http.MethodGet, "/api/uploads/tk-cnt3/versions/1/svc/x", "other-tok", nil)
	}
	if rec := tokenCall(t, s3, http.MethodGet, "/api/uploads/tk-cnt3/versions/1/svc/x", "other-tok", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("limited = %d", rec.Code)
	}
	for _, c := range []struct {
		s *Server
		u *models.Upload
	}{{s2, u2}, {s3, u3}} {
		rows, err := c.s.store.ListBackendTokenCalls(ctx, c.u.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if c.u == u2 {
			want = 1
		}
		if len(rows) != want {
			t.Errorf("%s rows = %+v, want %d", c.u.Slug, rows, want)
		}
	}

	// A counter that cannot be written does not change the response.
	if _, err := s.store.Exec(`DROP TABLE backend_token_calls`); err != nil {
		t.Fatal(err)
	}
	rec := tokenCall(t, s, http.MethodGet, "/api/uploads/tk-cnt/versions/1/svc/x", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("with broken counter = %d %s", rec.Code, rec.Body.String())
	}
}
