package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/privacyfence/peek/internal/db"
	"github.com/privacyfence/peek/internal/models"
	"github.com/privacyfence/peek/internal/uploadquota"
)

func TestExportUploadReportsVisitQueryFailure(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := "owner-token"
	if err := store.CreateToken(context.Background(), token, "owner", false, 0); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUploadChecked(context.Background(), "page", owner.AccountID, owner.ID, "page.html", 42, "public", uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(`DROP TABLE visits`); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	req := httptest.NewRequest(http.MethodGet, "/api/uploads/page/export", nil)
	req.SetPathValue("slug", "page")
	req = withAPIToken(req, owner)
	rec := httptest.NewRecorder()

	s.handleExportUpload(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "db error" {
		t.Fatalf("body = %+v", body)
	}
}

func TestHandleViewsReturnsAlignedBuckets(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "peek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := "owner-token"
	if err := store.CreateToken(context.Background(), token, "owner", false, 0); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUploadChecked(context.Background(), "page", owner.AccountID, owner.ID, "page.html", 42, "public", uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}
	upload, err := store.GetUpload(context.Background(), "page")
	if err != nil {
		t.Fatal(err)
	}
	today := bucketStart(time.Now(), 86400).Unix()
	for i, ts := range []int64{today - 86400, today} {
		cookie := "visitor-" + string(rune('a'+i))
		if _, err := store.Exec(`INSERT INTO visits(upload_id,visitor_cookie,visitor_name,ip,user_agent,visited_at) VALUES(?,?,?,?,?,?)`,
			upload.ID, cookie, "", "", "", ts+3600); err != nil {
			t.Fatal(err)
		}
	}

	s := &Server{store: store}
	req := httptest.NewRequest(http.MethodGet, "/api/uploads/page/views", nil)
	req.SetPathValue("slug", "page")
	rec := httptest.NewRecorder()

	s.handleViews(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Total   int `json:"total"`
		Unique  int `json:"unique"`
		Buckets []struct {
			T int64 `json:"t"`
			N int   `json:"n"`
		} `json:"buckets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Total != 2 || body.Unique != 2 {
		t.Fatalf("counts = total %d unique %d", body.Total, body.Unique)
	}
	if len(body.Buckets) != 7 {
		t.Fatalf("bucket count = %d", len(body.Buckets))
	}
	for _, bucket := range body.Buckets {
		if bucket.N > 0 {
			return
		}
	}
	t.Fatalf("expected at least one non-zero bucket: %+v", body.Buckets)
}

func TestHandleViewsRespectsPrivateVisibility(t *testing.T) {
	s := newTestServer(t)
	owner, err := s.store.CreateAccount(context.Background(), "owner@example.test", "Owner", false)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.store.CreateAccount(context.Background(), "viewer@example.test", "Viewer", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateUploadChecked(context.Background(), "private", owner.ID, 0, "private.html", 42, "signed-in", uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/uploads/private/views", nil)
	req.SetPathValue("slug", "private")
	rec := httptest.NewRecorder()
	s.handleViews(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/uploads/private/views", nil)
	req.SetPathValue("slug", "private")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: makeWebSession(s.secret, strconv.FormatInt(viewer.ID, 10), sessionTTL)})
	rec = httptest.NewRecorder()
	s.handleViews(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active viewer status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestExportMarksVerifiedComments(t *testing.T) {
	s, adminToken := newHandlerTestServer(t)
	ctx := context.Background()
	admin, err := s.store.GetToken(ctx, adminToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateUploadChecked(ctx, "page", admin.AccountID, admin.ID, "page.html", 42, "public", uploadquota.Limits{}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.store.GetUpload(ctx, "page")
	if _, err := s.store.AddComment(ctx, u.ID, 1, "", "", "page", "Admin", "c1", "signed", admin.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.AddComment(ctx, u.ID, 1, "", "", "page", "Typed", "c2", "typed", 0); err != nil {
		t.Fatal(err)
	}
	rec := doRequest(t, s, http.MethodGet, "/api/uploads/page/export", nil, asBearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Comments []struct {
			Author   string `json:"author"`
			Verified bool   `json:"verified"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range out.Comments {
		got[c.Author] = c.Verified
	}
	if len(got) != 2 || !got["Admin"] || got["Typed"] {
		t.Fatalf("comments = %+v", out.Comments)
	}
}

// apiCovFixture returns a full server, a restricted page, and the owner's bearer token.
func apiCovFixture(t *testing.T, slug string) (*Server, *models.Upload, string) {
	t.Helper()
	return grantsFixture(t, slug)
}

func apiCovStatus(t *testing.T, s *Server, method, path, token string, body string, want int) string {
	t.Helper()
	rec := doRequest(t, s, method, path, strings.NewReader(body), asBearer(token), withJSON())
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

func TestAPICovHandlersRequireAuthContext(t *testing.T) {
	s := &Server{}
	handlers := map[string]http.HandlerFunc{
		"stats":         s.handleStats,
		"export":        s.handleExportUpload,
		"create token":  s.handleCreateToken,
		"delete token":  s.handleDeleteToken,
		"list grants":   s.handleListGrants,
		"add grant":     s.handleAddGrant,
		"remove grant":  s.handleRemoveGrant,
		"rename":        s.handleRenameUpload,
		"list versions": s.handleListVersions,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "auth context missing") {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAPICovStatsAndExportAccess(t *testing.T) {
	s, u, ownerTok := apiCovFixture(t, "cov-stats")
	newAccountWithToken(t, s, "stranger@example.test", "stranger-token", false)
	if err := s.store.RecordVisit(context.Background(), u.ID, 1, "cookie-1", "Visitor", "10.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"stats", "export"} {
		apiCovStatus(t, s, http.MethodGet, "/api/uploads/missing/"+suffix, ownerTok, "", http.StatusNotFound)
		apiCovStatus(t, s, http.MethodGet, "/api/uploads/cov-stats/"+suffix, "stranger-token", "", http.StatusForbidden)
	}
	var stats struct {
		Total  int `json:"total_visits"`
		Unique int `json:"unique_visitors"`
		Recent []struct {
			Name string `json:"name"`
		} `json:"recent"`
	}
	body := apiCovStatus(t, s, http.MethodGet, "/api/uploads/cov-stats/stats", ownerTok, "", http.StatusOK)
	if err := json.Unmarshal([]byte(body), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Unique != 1 || len(stats.Recent) != 1 || stats.Recent[0].Name != "Visitor" {
		t.Fatalf("stats = %+v", stats)
	}
	apiCovStatus(t, s, http.MethodGet, "/api/uploads/cov-stats/export", ownerTok, "", http.StatusOK)
}

func TestAPICovStatsExportViewsDBFailures(t *testing.T) {
	// Each fault leaves the earlier queries of the handler working.
	faults := []struct {
		name   string
		inject func(t *testing.T, s *Server)
		path   string
	}{
		{"stats count visits", func(t *testing.T, s *Server) { dropTable(t, s.store, "visits") }, "stats"},
		{"stats recent visits", apiCovBreakVisitedAt, "stats"},
		{"export count visits", func(t *testing.T, s *Server) { dropTable(t, s.store, "visits") }, "export"},
		{"export recent visits", apiCovBreakVisitedAt, "export"},
		{"export comments", func(t *testing.T, s *Server) { dropTable(t, s.store, "comments") }, "export"},
		{"export versions", apiCovBreakVersionKey, "export"},
		{"export grants", func(t *testing.T, s *Server) { dropTable(t, s.store, "upload_grants") }, "export"},
		{"views count visits", func(t *testing.T, s *Server) { dropTable(t, s.store, "visits") }, "views"},
		{"views buckets", apiCovBreakVisitedAt, "views"},
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			s, u, ownerTok := apiCovFixture(t, "cov-fault")
			f.inject(t, s)
			path := "/api/uploads/cov-fault/" + f.path
			rec := doRequest(t, s, http.MethodGet, path, nil, asBearer(ownerTok), asSession(s, u.OwnerAccountID))
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "db error") {
				t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

func apiCovBreakVisitedAt(t *testing.T, s *Server) {
	t.Helper()
	if _, err := s.store.Exec(`ALTER TABLE visits RENAME COLUMN visited_at TO visited_when`); err != nil {
		t.Fatal(err)
	}
}

func apiCovBreakVersionKey(t *testing.T, s *Server) {
	t.Helper()
	if _, err := s.store.Exec(`ALTER TABLE upload_versions RENAME COLUMN object_key TO object_ref`); err != nil {
		t.Fatal(err)
	}
}

func TestAPICovViewsNotFound(t *testing.T) {
	s, _ := newHandlerTestServer(t)
	if rec := doRequest(t, s, http.MethodGet, "/api/uploads/nope/views", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

type statsTokenCall struct {
	Caller     string  `json:"caller"`
	Email      *string `json:"email"`
	Version    int     `json:"version"`
	Calls      int64   `json:"calls"`
	LastCallAt int64   `json:"last_call_at"`
	LastStatus int     `json:"last_status"`
}

func statsTokenCalls(t *testing.T, s *Server, slug, token string) ([]statsTokenCall, string) {
	t.Helper()
	body := apiCovStatus(t, s, http.MethodGet, "/api/uploads/"+slug+"/stats", token, "", http.StatusOK)
	var out struct {
		Calls []statsTokenCall `json:"token_calls"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Calls, body
}

func TestStatsTokenCallsPseudonymous(t *testing.T) {
	s, u, tok := apiCovFixture(t, "tc-pseudo")
	caller := newAccountWithToken(t, s, "caller@example.test", "caller-token", false)
	if err := s.store.RecordBackendTokenCall(context.Background(), u.ID, 1, caller.ID, 201, false); err != nil {
		t.Fatal(err)
	}
	check := func() {
		calls, body := statsTokenCalls(t, s, u.Slug, tok)
		if len(calls) != 1 {
			t.Fatalf("calls = %s", body)
		}
		c := calls[0]
		if c.Caller != s.tokenCallerHash(u.ID, caller.ID) || c.Version != 1 || c.Calls != 1 || c.LastCallAt == 0 || c.LastStatus != 201 {
			t.Fatalf("row = %+v", c)
		}
		if c.Email != nil || strings.Contains(body, `"email"`) || strings.Contains(body, "caller@example.test") {
			t.Fatalf("email leaked: %s", body)
		}
	}
	check()
	if err := s.store.SetUploadVisibility(context.Background(), u.ID, "signed-in"); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestStatsTokenCallsIdentified(t *testing.T) {
	s, u, tok := apiCovFixture(t, "tc-ident")
	caller := newAccountWithToken(t, s, "caller@example.test", "caller-token", false)
	if err := s.store.RecordBackendTokenCall(context.Background(), u.ID, 1, caller.ID, 200, true); err != nil {
		t.Fatal(err)
	}
	calls, body := statsTokenCalls(t, s, u.Slug, tok)
	if len(calls) != 1 || calls[0].Email == nil || *calls[0].Email != "caller@example.test" {
		t.Fatalf("calls = %s", body)
	}
}

func TestStatsTokenCallsAdminSeesEmail(t *testing.T) {
	s, u, _ := apiCovFixture(t, "tc-admin")
	newAccountWithToken(t, s, "admin@example.test", "admin-token", true)
	caller := newAccountWithToken(t, s, "caller@example.test", "caller-token", false)
	if err := s.store.RecordBackendTokenCall(context.Background(), u.ID, 1, caller.ID, 200, false); err != nil {
		t.Fatal(err)
	}
	calls, body := statsTokenCalls(t, s, u.Slug, "admin-token")
	if len(calls) != 1 || calls[0].Email == nil || *calls[0].Email != "caller@example.test" {
		t.Fatalf("calls = %s", body)
	}
}

func TestStatsTokenCallsEmpty(t *testing.T) {
	s, u, tok := apiCovFixture(t, "tc-empty")
	if err := s.store.RecordVisit(context.Background(), u.ID, 1, "cookie-1", "Visitor", "10.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	body := apiCovStatus(t, s, http.MethodGet, "/api/uploads/tc-empty/stats", tok, "", http.StatusOK)
	if !strings.Contains(body, `"token_calls":[]`) || !strings.Contains(body, `"total_visits":1`) ||
		!strings.Contains(body, `"unique_visitors":1`) || !strings.Contains(body, `"name":"Visitor"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestStatsTokenCallsDBError(t *testing.T) {
	s, _, tok := apiCovFixture(t, "tc-dberr")
	if _, err := s.store.Exec(`DROP TABLE backend_token_calls`); err != nil {
		t.Fatal(err)
	}
	body := apiCovStatus(t, s, http.MethodGet, "/api/uploads/tc-dberr/stats", tok, "", http.StatusInternalServerError)
	if !strings.Contains(body, "db error") {
		t.Fatalf("body = %s", body)
	}
}
