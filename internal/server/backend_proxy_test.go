package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/privacyfence/peek/internal/backends/backendstest"
	"github.com/privacyfence/peek/internal/models"
)

// seenRequest is what the test backend received.
type seenRequest struct {
	Method   string
	Path     string
	RawPath  string
	RawQuery string
	Body     string
	Header   http.Header
}

type testBackend struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last seenRequest
}

func (b *testBackend) seen() seenRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func (b *testBackend) port() int {
	u, _ := url.Parse(b.srv.URL)
	p, _ := strconv.Atoi(u.Port())
	return p
}

// startTestBackend starts an HTTP server that records the last request and
// answers with handler (or a default 201 JSON answer).
func startTestBackend(t *testing.T, handler http.HandlerFunc) *testBackend {
	t.Helper()
	b := &testBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.last = seenRequest{r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery, string(body), r.Header.Clone()}
		b.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=999")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"sent"}`))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// proxyFixture is a backend-enabled server with a page (visibility given)
// whose version 1 has a backend pointing at tb.
func proxyFixture(t *testing.T, slug, visibility string, tb *testBackend) (*Server, *backendstest.FakeRuntime, *models.Upload) {
	t.Helper()
	s, fake, _ := newBackendTestServer(t)
	allowBackendImages(t, s)
	u := newTwoVersionPage(t, s, slug, visibility)
	if err := s.store.SetUploadBackend(context.Background(), models.UploadBackend{UploadID: u.ID, Version: 1, Image: backendTestImage, Port: tb.port()}); err != nil {
		t.Fatal(err)
	}
	return s, fake, u
}

func svcHeaders() func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-Peek-Svc", "1") }
}

func svcReq(t *testing.T, s *Server, method, path string, body io.Reader, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, s, method, path, body, append([]func(*http.Request){svcHeaders()}, opts...)...)
}

func envOf(spec []string, key string) string {
	for _, kv := range spec {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestBackendProxyRelaysPublicPage(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, fake, u := proxyFixture(t, "px-public", models.UploadVisibilityPublic, tb)

	rec := svcReq(t, s, http.MethodPost, "/svc/px-public/v/1/api/contact?x=1&y=2", strings.NewReader(`{"a":1}`), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Cookie", "secret=1")
		r.Header.Set("Authorization", "Bearer nope")
		r.Header.Set("X-Peek-User-Email", "forged@example.test")
		r.Header.Set("X-Other", "dropped")
	})
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"status":"sent"}` {
		t.Fatalf("relay = %d %s", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if h.Get("Content-Type") != "application/json" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("response headers = %v", h)
	}
	got := tb.seen()
	if got.Method != http.MethodPost || got.Path != "/api/contact" || got.RawQuery != "x=1&y=2" || got.Body != `{"a":1}` {
		t.Fatalf("backend saw %+v", got)
	}
	if got.Header.Get("Content-Type") != "application/json" || got.Header.Get("Accept") != "application/json" {
		t.Fatalf("content-type/accept not forwarded: %v", got.Header)
	}
	for _, name := range []string{"Cookie", "Authorization", "X-Peek-User-Email", "X-Peek-User-Id", "X-Peek-Viewer", "X-Other"} {
		if v := got.Header.Get(name); v != "" {
			t.Errorf("%s forwarded: %q", name, v)
		}
	}
	fake.Lock()
	token := envOf(fake.Created[0].Env, "PEEK_BACKEND_TOKEN")
	fake.Unlock()
	if token == "" || got.Header.Get("X-Peek-Backend-Token") != token {
		t.Fatalf("token header %q, container env %q", got.Header.Get("X-Peek-Backend-Token"), token)
	}
	if got.Header.Get("X-Peek-Upload-Id") != strconv.FormatInt(u.ID, 10) || got.Header.Get("X-Peek-Version") != "1" {
		t.Fatalf("id headers = %v", got.Header)
	}

	// Viewer id only with the hn_vid cookie, and it is stable per page.
	svcReq(t, s, http.MethodGet, "/svc/px-public/v/1/api/x", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: visitorCookie, Value: "vid-1"})
	})
	v1 := tb.seen().Header.Get("X-Peek-Viewer")
	if len(v1) != 32 {
		t.Fatalf("X-Peek-Viewer = %q", v1)
	}
	svcReq(t, s, http.MethodGet, "/svc/px-public/v/1/api/x", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: visitorCookie, Value: "vid-1"})
	})
	if tb.seen().Header.Get("X-Peek-Viewer") != v1 {
		t.Fatal("viewer id not stable")
	}
}

func TestBackendProxyUserHeaders(t *testing.T) {
	for _, tc := range []struct {
		vis      string
		wantUser bool
	}{
		{models.UploadVisibilitySignedIn, true},
		{models.UploadVisibilityPublic, false},
	} {
		tb := startTestBackend(t, nil)
		slug := "px-user-" + tc.vis
		s, _, _ := proxyFixture(t, slug, tc.vis, tb)
		acct, err := s.store.CreateAccount(context.Background(), "viewer@example.test", "Viewer", false)
		if err != nil {
			t.Fatal(err)
		}
		rec := svcReq(t, s, http.MethodGet, "/svc/"+slug+"/v/1/api/me", nil, asSession(s, acct.ID))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: relay = %d %s", tc.vis, rec.Code, rec.Body.String())
		}
		h := tb.seen().Header
		if tc.wantUser {
			if h.Get("X-Peek-User-Id") != strconv.FormatInt(acct.ID, 10) || h.Get("X-Peek-User-Email") != "viewer@example.test" {
				t.Errorf("%s: user headers = %v", tc.vis, h)
			}
		} else if h.Get("X-Peek-User-Id") != "" || h.Get("X-Peek-User-Email") != "" {
			t.Errorf("%s: user identity leaked to backend: %v", tc.vis, h)
		}
	}
}

func TestBackendProxyKeepsEscapedPath(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-escape", models.UploadVisibilityPublic, tb)
	rec := svcReq(t, s, http.MethodGet, "/svc/px-escape/v/1/api/a%3Fb", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("relay = %d %s", rec.Code, rec.Body.String())
	}
	got := tb.seen()
	if got.RawPath != "/api/a%3Fb" || got.Path != "/api/a?b" || got.RawQuery != "" {
		t.Fatalf("backend saw path=%q raw=%q query=%q", got.Path, got.RawPath, got.RawQuery)
	}
}

func TestBackendProxyRejectsUnsafeRequests(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-guard", models.UploadVisibilityPublic, tb)
	const forbidden = "backend requests must come from the page viewer"

	rec := doRequest(t, s, http.MethodGet, "/svc/px-guard/v/1/api/x", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), forbidden) {
		t.Fatalf("missing X-Peek-Svc = %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, s, http.MethodGet, "/svc/px-guard/v/1/api/x", nil, func(r *http.Request) { r.Header.Set("X-Peek-Svc", "0") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("X-Peek-Svc=0 = %d", rec.Code)
	}
	rec = svcReq(t, s, http.MethodGet, "/svc/px-guard/v/1/api/x", nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), forbidden) {
		t.Fatalf("cross-site = %d %s", rec.Code, rec.Body.String())
	}
	rec = svcReq(t, s, http.MethodGet, "/svc/px-guard/v/1/api/x", nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") })
	if rec.Code != http.StatusCreated {
		t.Fatalf("same-origin = %d", rec.Code)
	}
}

func TestBackendProxyAccessMatchesComments(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-signed", models.UploadVisibilitySignedIn, tb)
	rec := svcReq(t, s, http.MethodGet, "/svc/px-signed/v/1/api/x", nil)
	want := doRequest(t, s, http.MethodGet, "/api/uploads/px-signed/comments", nil)
	if rec.Code != want.Code || rec.Body.String() != want.Body.String() {
		t.Fatalf("proxy = %d %q, comments = %d %q", rec.Code, rec.Body.String(), want.Code, want.Body.String())
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestBackendProxyNotFoundCases(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-nf", models.UploadVisibilityPublic, tb)
	for _, tc := range []struct{ path, msg string }{
		{"/svc/px-nf/v/2/api/x", "this page version has no backend"},
		{"/svc/missing/v/1/api/x", "not found"},
		{"/svc/px-nf/v/0/api/x", "not found"},
		{"/svc/px-nf/v/abc/api/x", "not found"},
	} {
		rec := svcReq(t, s, http.MethodGet, tc.path, nil)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s = %d %s, want 404 %q", tc.path, rec.Code, rec.Body.String(), tc.msg)
		}
	}
}

func TestBackendProxyOff(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	rec := svcReq(t, s, http.MethodGet, "/svc/any/v/1/api/x", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "backends are not enabled on this server") {
		t.Fatalf("off = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackendProxyRequestBodyLimit(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-big", models.UploadVisibilityPublic, tb)
	rec := svcReq(t, s, http.MethodPost, "/svc/px-big/v/1/api/x", strings.NewReader(strings.Repeat("a", 2<<20)))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("2 MiB body = %d %s", rec.Code, rec.Body.String())
	}
	// Unknown length (chunked): the reader cap applies.
	req := httptest.NewRequest(http.MethodPost, "/svc/px-big/v/1/api/x", io.NopCloser(strings.NewReader(strings.Repeat("a", 2<<20))))
	req.ContentLength = -1
	req.Header.Set("X-Peek-Svc", "1")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 2 MiB body = %d", rr.Code)
	}
	rec = svcReq(t, s, http.MethodPost, "/svc/px-big/v/1/api/x", strings.NewReader(strings.Repeat("a", 1<<20)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("1 MiB body = %d %s", rec.Code, rec.Body.String())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestBackendProxyBadBody(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-badbody", models.UploadVisibilityPublic, tb)
	req := httptest.NewRequest(http.MethodPost, "/svc/px-badbody/v/1/api/x", errReader{})
	req.ContentLength = -1
	req.Header.Set("X-Peek-Svc", "1")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "bad request body") {
		t.Fatalf("bad body = %d %s", rr.Code, rr.Body.String())
	}
}

func TestBackendProxyResponseTooLarge(t *testing.T) {
	tb := startTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 9<<20)))
	})
	s, _, _ := proxyFixture(t, "px-resp", models.UploadVisibilityPublic, tb)
	rec := svcReq(t, s, http.MethodGet, "/svc/px-resp/v/1/api/x", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "backend response too large") {
		t.Fatalf("9 MiB response = %d", rec.Code)
	}
}

func TestBackendProxyUnavailableAndTimeout(t *testing.T) {
	tb := startTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
	})
	s, _, _ := proxyFixture(t, "px-down", models.UploadVisibilityPublic, tb)

	s.backendClient.Timeout = 100 * time.Millisecond
	rec := svcReq(t, s, http.MethodGet, "/svc/px-down/v/1/api/x", nil)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "backend timed out") {
		t.Fatalf("slow backend = %d %s", rec.Code, rec.Body.String())
	}

	tb.srv.CloseClientConnections()
	tb.srv.Close()
	s.backendClient.Timeout = 5 * time.Second
	rec = svcReq(t, s, http.MethodGet, "/svc/px-down/v/1/api/x", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "backend unavailable") {
		t.Fatalf("closed backend = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackendProxyStartFailure(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, fake, _ := proxyFixture(t, "px-startfail", models.UploadVisibilityPublic, tb)
	fake.Lock()
	fake.CreateErr = io.ErrClosedPipe
	fake.Unlock()
	rec := svcReq(t, s, http.MethodGet, "/svc/px-startfail/v/1/api/x", nil)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "backend failed to start") {
		t.Fatalf("start failure = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackendProxyRateLimit(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-rate", models.UploadVisibilityPublic, tb)
	var last *httptest.ResponseRecorder
	for i := 0; i < 241; i++ {
		// Version 2 has no backend: cheap 404s still count against the limiter.
		last = svcReq(t, s, http.MethodGet, "/svc/px-rate/v/2/api/x", nil)
		if i < 240 && last.Code == http.StatusTooManyRequests {
			t.Fatalf("limited early at request %d", i+1)
		}
	}
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "too many backend requests, slow down") {
		t.Fatalf("request 241 = %d %s", last.Code, last.Body.String())
	}
}

func TestBackendProxyCallerAndRelayHeaders(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "px-caller", models.UploadVisibilityPublic, tb)
	rec := svcReq(t, s, http.MethodGet, "/svc/px-caller/v/1/api/x", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("relay = %d %s", rec.Code, rec.Body.String())
	}
	if got := tb.seen().Header.Get("X-Peek-Caller"); got != "browser" {
		t.Errorf("X-Peek-Caller = %q", got)
	}
	if got := rec.Header().Get("X-Peek-Relay"); got != "backend" {
		t.Errorf("X-Peek-Relay = %q", got)
	}
	for _, path := range []string{"/svc/px-caller/v/2/api/x", "/svc/missing/v/1/api/x"} {
		rec = svcReq(t, s, http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound || rec.Header().Get("X-Peek-Relay") != "" {
			t.Errorf("%s = %d relay=%q", path, rec.Code, rec.Header().Get("X-Peek-Relay"))
		}
	}
	rs, _, _ := proxyFixture(t, "px-caller-r", models.UploadVisibilityRestricted, tb)
	rec = svcReq(t, rs, http.MethodGet, "/svc/px-caller-r/v/1/api/x", nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("X-Peek-Relay") != "" {
		t.Errorf("denied = %d relay=%q", rec.Code, rec.Header().Get("X-Peek-Relay"))
	}
	acct, err := rs.store.CreateAccount(context.Background(), "nobody@example.test", "Nobody", false)
	if err != nil {
		t.Fatal(err)
	}
	rec = svcReq(t, rs, http.MethodGet, "/svc/px-caller-r/v/1/api/x", nil, asSession(rs, acct.ID))
	if rec.Code != http.StatusForbidden || rec.Header().Get("X-Peek-Relay") != "" {
		t.Errorf("forbidden = %d relay=%q", rec.Code, rec.Header().Get("X-Peek-Relay"))
	}
}

// holdingWriter blocks its first Write until release is closed, so a data
// export stays in flight.
type holdingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newHoldingWriter() *holdingWriter {
	return &holdingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (h *holdingWriter) Write(p []byte) (int, error) {
	h.once.Do(func() {
		close(h.started)
		<-h.release
	})
	return len(p), nil
}

func TestBackendRelaysBusyDuringDataOp(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, u, _ := tokenCallFixture(t, "px-busy", models.UploadVisibilityPublic, tb)
	ctx := context.Background()
	b, err := s.store.GetUploadBackend(ctx, u.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.backendSpec(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	hw := newHoldingWriter()
	done := make(chan error, 1)
	go func() {
		_, err := s.backends.ExportData(ctx, spec, hw)
		done <- err
	}()
	<-hw.started

	const busy = "backend data is being exported or imported, try again shortly"
	rec := svcReq(t, s, http.MethodGet, "/svc/px-busy/v/1/api/x", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), busy) {
		t.Errorf("/svc during export = %d %s", rec.Code, rec.Body.String())
	}
	rec = tokenCall(t, s, http.MethodGet, "/api/uploads/px-busy/versions/1/svc/api/x", "owner-px-busy", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), busy) {
		t.Errorf("token route during export = %d %s", rec.Code, rec.Body.String())
	}

	close(hw.release)
	if err := <-done; err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	rec = svcReq(t, s, http.MethodGet, "/svc/px-busy/v/1/api/x", nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("/svc after export = %d %s", rec.Code, rec.Body.String())
	}
	rec = tokenCall(t, s, http.MethodGet, "/api/uploads/px-busy/versions/1/svc/api/x", "owner-px-busy", nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("token route after export = %d %s", rec.Code, rec.Body.String())
	}
}
