package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareCovSetsHSTSOnlyWhenSecure(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	if got := doRequest(t, s, http.MethodGet, "/healthz", nil).Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS without secure = %q", got)
	}
	s.secure = true
	if got := doRequest(t, s, http.MethodGet, "/healthz", nil).Header().Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
		t.Fatalf("HSTS with secure = %q", got)
	}
}

func TestMiddlewareCovAuthAdminRejections(t *testing.T) {
	s, adminToken := newHandlerTestServer(t)
	newAccountWithToken(t, s, "member@example.test", "member-token-0123456789", false)
	disabled := newAccountWithToken(t, s, "gone@example.test", "gone-token-0123456789", true)
	if err := s.store.SetAccountDisabled(t.Context(), disabled.ID, true); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"invalid", "nope-nope-nope-nope", http.StatusUnauthorized},
		{"disabled account", "gone-token-0123456789", http.StatusForbidden},
		{"non-admin", "member-token-0123456789", http.StatusForbidden},
		{"admin", adminToken, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts []func(*http.Request)
			if tc.token != "" {
				opts = append(opts, asBearer(tc.token))
			}
			if rec := doRequest(t, s, http.MethodGet, "/api/tokens", nil, opts...); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestMiddlewareCovAuthTokenRejectsDisabledAccount(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	a := newAccountWithToken(t, s, "off@example.test", "off-token-0123456789", false)
	if err := s.store.SetAccountDisabled(t.Context(), a.ID, true); err != nil {
		t.Fatal(err)
	}
	if rec := doRequest(t, s, http.MethodGet, "/api/uploads", nil, asBearer("off-token-0123456789")); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMiddlewareCovTokenLastUsedFailureDoesNotBlockRequest(t *testing.T) {
	s, adminToken := newHandlerTestServer(t)
	addFailTrigger(t, s.store, "tokens", "UPDATE")
	if rec := doRequest(t, s, http.MethodGet, "/api/tokens", nil, asBearer(adminToken)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestBodyLimitExemptsBackendRelay(t *testing.T) {
	tb := startTestBackend(t, nil)
	s, _, _ := proxyFixture(t, "mw-svc", "public", tb)
	big := strings.NewReader(strings.Repeat("a", 1<<20))
	rec := svcReq(t, s, http.MethodPost, "/svc/mw-svc/v/1/api/x", big)
	if rec.Code != http.StatusCreated {
		t.Fatalf("1 MiB POST to /svc = %d %s, want it relayed", rec.Code, rec.Body.String())
	}
	if got := len(tb.seen().Body); got != 1<<20 {
		t.Fatalf("backend received %d bytes, want %d", got, 1<<20)
	}
}

func TestBodyLimitExempt(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		want           bool
	}{
		{http.MethodGet, "/svc/x/v/1/a", true},
		{http.MethodPost, "/api/upload", true},
		{http.MethodGet, "/api/uploads/x/versions/1/svc/a", true},
		{http.MethodPost, "/api/uploads/x/versions/latest/svc/", true},
		{http.MethodPut, "/api/uploads/x/versions/1/backend", false},
		{http.MethodPost, "/api/uploads/x/versions", false},
		{http.MethodPost, "/api/uploads/x/stats", false},
		{http.MethodPost, "/api/uploads/a%2Fversions%2F1%2Fsvc%2Fz/grants", false},
	} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if got := bodyLimitExempt(r); got != tc.want {
			t.Errorf("%s %s exempt = %v, want %v", tc.method, tc.target, got, tc.want)
		}
	}
}
