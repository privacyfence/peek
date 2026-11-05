package server

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/backends/backendstest"
	"github.com/privacyfence/peek/internal/models"
)

type tarEntry struct{ name, body string }

func buildTar(t *testing.T, prefix string, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		name := prefix + e.name
		h := &tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if strings.HasSuffix(name, "/") {
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

// dataFixture is a backend-enabled server with a page whose version 1 has a backend,
// plus the owner's token and the page volume's name.
func dataFixture(t *testing.T, slug string) (*Server, *backendstest.FakeRuntime, *models.Upload, string, string) {
	t.Helper()
	s, fake, _ := newBackendTestServer(t)
	_, u, tok := backendFixtureOn(t, s, slug)
	ctx := context.Background()
	if err := s.store.SetUploadBackend(ctx, models.UploadBackend{UploadID: u.ID, Version: 1, Image: backendTestImage, Port: 8080}); err != nil {
		t.Fatal(err)
	}
	instance, err := s.store.GetSetting(ctx, backendInstanceSetting)
	if err != nil {
		t.Fatal(err)
	}
	return s, fake, u, tok, backends.VolumeName(instance, u.ID)
}

func dataReq(t *testing.T, s *Server, method, slug, token string, body io.Reader, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	if token != "" {
		opts = append(opts, asBearer(token))
	}
	return doRequest(t, s, method, "/api/uploads/"+slug+"/backend/data", body, opts...)
}

func auditDetails(t *testing.T, s *Server) map[string]string {
	t.Helper()
	entries, err := s.store.ListAuditLog(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range entries {
		m[e.Action] = e.Detail
	}
	return m
}

func noSpoolFiles(t *testing.T, s *Server) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(s.dataDir, ".backend-data-import-*"))
	if len(left) != 0 {
		t.Errorf("spool files left behind: %v", left)
	}
}

// holdExport starts an export of the page that stays in flight until release is closed.
func holdExport(t *testing.T, s *Server, u *models.Upload) (release func()) {
	t.Helper()
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.backends.ExportData(ctx, spec, hw)
	}()
	<-hw.started
	return func() {
		close(hw.release)
		<-done
	}
}

func TestBackendDataExport(t *testing.T) {
	s, fake, u, tok, vol := dataFixture(t, "d-export")
	fake.Volumes[vol] = buildTar(t, "data/", tarEntry{"", ""}, tarEntry{"a.txt", "hello"}, tarEntry{"sub/", ""}, tarEntry{"sub/b.txt", "world"})
	newAccountWithToken(t, s, "admin@example.test", "admin-tok", true)
	for _, who := range []string{tok, "admin-tok"} {
		rec := dataReq(t, s, http.MethodGet, u.Slug, who, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "application/x-tar" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="d-export-data.tar"` {
			t.Errorf("Content-Disposition = %q", got)
		}
		got := readTar(t, rec.Body.Bytes())
		want := map[string]string{"a.txt": "hello", "sub/": "", "sub/b.txt": "world"}
		if len(got) != len(want) {
			t.Fatalf("entries = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("entry %q = %q, want %q", k, got[k], v)
			}
		}
	}
	if d := auditDetails(t, s)["backend.data.export"]; !strings.Contains(d, "slug=d-export") || !strings.Contains(d, "bytes=") {
		t.Errorf("audit detail = %q", d)
	}
}

func TestBackendDataExportErrors(t *testing.T) {
	s, fake, u, tok, _ := dataFixture(t, "d-exp-err")
	newAccountWithToken(t, s, "other@example.test", "other-tok", false)
	if rec := dataReq(t, s, http.MethodGet, u.Slug, "other-tok", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not owner") {
		t.Errorf("other = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dataReq(t, s, http.MethodGet, u.Slug, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d", rec.Code)
	}

	fake.ArchiveErr = io.ErrUnexpectedEOF
	if rec := dataReq(t, s, http.MethodGet, u.Slug, tok, nil); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not export backend data") {
		t.Errorf("archive error = %d %s", rec.Code, rec.Body.String())
	}
	fake.ArchiveErr = nil

	release := holdExport(t, s, u)
	rec := dataReq(t, s, http.MethodGet, u.Slug, tok, nil)
	release()
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another data export or import is running for this page") {
		t.Errorf("busy = %d %s", rec.Code, rec.Body.String())
	}

	// A page without a backend.
	other := newTwoVersionPage(t, s, "d-exp-none", models.UploadVisibilityPublic)
	if err := s.store.CreateTokenForAccount(context.Background(), "none-tok", other.OwnerAccountID, "n"); err != nil {
		t.Fatal(err)
	}
	if rec := dataReq(t, s, http.MethodGet, other.Slug, "none-tok", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "this page has no backend") {
		t.Errorf("no backend = %d %s", rec.Code, rec.Body.String())
	}

	// Backends off.
	off, offTok := newHandlerTestServer(t)
	if rec := dataReq(t, off, http.MethodGet, "any", offTok, nil); rec.Code != http.StatusConflict && rec.Code != http.StatusNotFound {
		t.Errorf("backends off = %d", rec.Code)
	}
}

func TestBackendDataExportAbortsAfterFirstByte(t *testing.T) {
	s, fake, u, tok, vol := dataFixture(t, "d-abort")
	// A truncated archive: the header of a.txt is complete but its content is cut off.
	full := buildTar(t, "data/", tarEntry{"a.txt", strings.Repeat("x", 4096)})
	fake.Volumes[vol] = full[:600]
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Fatalf("recover = %v, want http.ErrAbortHandler", r)
		}
	}()
	dataReq(t, s, http.MethodGet, u.Slug, tok, nil)
	t.Fatal("handler did not abort")
}

func TestBackendDataImport(t *testing.T) {
	s, fake, u, tok, vol := dataFixture(t, "d-import")
	archive := buildTar(t, "./", tarEntry{"notes/", ""}, tarEntry{"notes/hello.txt", "hi"})
	rec := dataReq(t, s, http.MethodPut, u.Slug, tok, bytes.NewReader(archive))
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	if want := `{"bytes":` + itoa(len(archive)) + `,"status":"ok"}`; strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("body = %s, want %s", rec.Body.String(), want)
	}
	got := readTar(t, fake.Imported[vol])
	if got["./notes/hello.txt"] != "hi" || len(got) != 2 {
		t.Errorf("imported entries = %v", got)
	}
	if d := auditDetails(t, s)["backend.data.import"]; !strings.Contains(d, "slug=d-import") || !strings.Contains(d, "bytes="+itoa(len(archive))) {
		t.Errorf("audit detail = %q", d)
	}
	noSpoolFiles(t, s)
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestBackendDataImportRefusals(t *testing.T) {
	s, fake, u, tok, _ := dataFixture(t, "d-refuse")
	if err := s.store.SetSetting(context.Background(), "max_backend_data_import", "2048"); err != nil {
		t.Fatal(err)
	}
	big := buildTar(t, "", tarEntry{"big.bin", strings.Repeat("x", 4096)})
	chunked := func(r *http.Request) { r.ContentLength = -1 }
	for _, tc := range []struct {
		name string
		body []byte
		opt  func(*http.Request)
		code int
		msg  string
	}{
		{"content length over", big, nil, http.StatusRequestEntityTooLarge, "archive too large"},
		{"chunked over", big, chunked, http.StatusRequestEntityTooLarge, "archive too large"},
		{"empty", nil, nil, http.StatusBadRequest, "empty archive"},
		{"garbage", bytes.Repeat([]byte("not a tar archive"), 40), nil, http.StatusBadRequest, "not a valid tar archive"},
		{"traversal", buildTar(t, "", tarEntry{"../x", "evil"}), nil, http.StatusBadRequest, `archive entry \"../x\" is outside /data`},
	} {
		var opts []func(*http.Request)
		if tc.opt != nil {
			opts = append(opts, tc.opt)
		}
		var body io.Reader = bytes.NewReader(tc.body)
		if tc.opt != nil {
			body = io.NopCloser(body)
		}
		rec := dataReq(t, s, http.MethodPut, u.Slug, tok, body, opts...)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s = %d %s, want %d %s", tc.name, rec.Code, rec.Body.String(), tc.code, tc.msg)
		}
	}
	fake.Lock()
	calls := slices.Clone(fake.Calls)
	fake.Unlock()
	for _, c := range calls {
		if strings.HasPrefix(c, "rmvol ") {
			t.Errorf("volume removed by a refused import: %v", calls)
		}
	}
	noSpoolFiles(t, s)
}

func TestBackendDataImportErrors(t *testing.T) {
	s, fake, u, tok, _ := dataFixture(t, "d-imp-err")
	archive := buildTar(t, "", tarEntry{"a.txt", "a"})
	newAccountWithToken(t, s, "other@example.test", "other-tok", false)
	if rec := dataReq(t, s, http.MethodPut, u.Slug, "other-tok", bytes.NewReader(archive)); rec.Code != http.StatusForbidden {
		t.Errorf("other = %d %s", rec.Code, rec.Body.String())
	}

	fake.ArchiveErr = io.ErrUnexpectedEOF
	if rec := dataReq(t, s, http.MethodPut, u.Slug, tok, bytes.NewReader(archive)); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "could not import backend data") {
		t.Errorf("archive error = %d %s", rec.Code, rec.Body.String())
	}
	fake.ArchiveErr = nil

	release := holdExport(t, s, u)
	rec := dataReq(t, s, http.MethodPut, u.Slug, tok, bytes.NewReader(archive))
	release()
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another data export or import is running for this page") {
		t.Errorf("busy = %d %s", rec.Code, rec.Body.String())
	}

	other := newTwoVersionPage(t, s, "d-imp-none", models.UploadVisibilityPublic)
	if err := s.store.CreateTokenForAccount(context.Background(), "none-tok", other.OwnerAccountID, "n"); err != nil {
		t.Fatal(err)
	}
	if rec := dataReq(t, s, http.MethodPut, other.Slug, "none-tok", bytes.NewReader(archive)); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "this page has no backend") {
		t.Errorf("no backend = %d %s", rec.Code, rec.Body.String())
	}

	off, offTok := newHandlerTestServer(t)
	if rec := dataReq(t, off, http.MethodPut, "any", offTok, bytes.NewReader(archive)); rec.Code != http.StatusConflict && rec.Code != http.StatusNotFound {
		t.Errorf("backends off = %d", rec.Code)
	}
	noSpoolFiles(t, s)
}
