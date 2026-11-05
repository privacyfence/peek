package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type dataReq struct {
	Method, URI, CType string
	Length             int64
	Body               []byte
}

func dataServer(t *testing.T, status int, body string) chan dataReq {
	t.Helper()
	seen := make(chan dataReq, 16)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		seen <- dataReq{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.ContentLength, b}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	configureTestClient(t, ts.URL)
	return seen
}

func setConfirm(t *testing.T, terminal bool, input string) {
	t.Helper()
	oldT, oldI := stdinIsTerminal, confirmInput
	stdinIsTerminal = func() bool { return terminal }
	confirmInput = strings.NewReader(input)
	t.Cleanup(func() { stdinIsTerminal, confirmInput = oldT, oldI })
}

func TestBackendDataExport(t *testing.T) {
	t.Chdir(t.TempDir())
	seen := dataServer(t, 200, "TARBYTES")

	stdout, stderr, err := runBackend(t, "data", "export", "my-page")
	if err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.Method != "GET" || r.URI != "/api/uploads/my-page/backend/data" {
		t.Fatalf("request = %+v", r)
	}
	b, err := os.ReadFile("my-page-data.tar")
	if err != nil || string(b) != "TARBYTES" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if fi, _ := os.Stat("my-page-data.tar"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, want := range []string{"stopping backends for my-page to take a consistent copy", "wrote 8 bytes to my-page-data.tar"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	}

	if _, _, err := runBackend(t, "data", "export", "my-page", "-o", "out.tar"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile("out.tar"); string(b) != "TARBYTES" {
		t.Fatalf("out.tar = %q", b)
	}

	stdout, stderr, err = runBackend(t, "data", "export", "my-page", "-o", "-")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "TARBYTES" || strings.Contains(stderr, "wrote") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}
}

func TestBackendDataExportErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	seen := dataServer(t, 404, `{"error":"this page has no backend"}`)

	if err := os.WriteFile("my-page-data.tar", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runBackend(t, "data", "export", "my-page")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile("my-page-data.tar"); string(b) != "old" {
		t.Fatalf("file changed: %q", b)
	}
	if len(seen) != 0 {
		t.Fatal("request sent for existing file")
	}

	_, _, err = runBackend(t, "data", "export", "my-page", "-o", "new.tar")
	if err == nil || err.Error() != "this page has no backend" {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat("new.tar"); !os.IsNotExist(statErr) {
		t.Fatalf("file left behind: %v", statErr)
	}
}

func TestBackendDataImport(t *testing.T) {
	t.Chdir(t.TempDir())
	content := []byte("some tar bytes")
	if err := os.WriteFile("in.tar", content, 0o600); err != nil {
		t.Fatal(err)
	}
	seen := dataServer(t, 200, `{"bytes":14}`)

	stdout, _, err := runBackend(t, "data", "import", "my-page", "in.tar", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	r := <-seen
	if r.Method != "PUT" || r.URI != "/api/uploads/my-page/backend/data" || r.CType != "application/x-tar" ||
		r.Length != int64(len(content)) || string(r.Body) != string(content) {
		t.Fatalf("request = %+v", r)
	}
	if want := "imported 14 bytes into /data of my-page\n"; stdout != want {
		t.Fatalf("stdout = %q", stdout)
	}

	setConfirm(t, true, "y\n")
	stdout, stderr, err := runBackend(t, "data", "import", "my-page", "in.tar")
	if err != nil {
		t.Fatal(err)
	}
	<-seen
	if !strings.Contains(stdout, "imported 14 bytes") || !strings.Contains(stderr, "Continue? [y/N] ") {
		t.Fatalf("stdout = %q stderr = %q", stdout, stderr)
	}

	setConfirm(t, true, "n\n")
	_, _, err = runBackend(t, "data", "import", "my-page", "in.tar")
	if err == nil || err.Error() != "import cancelled" {
		t.Fatalf("err = %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("request sent after cancel")
	}
}

func TestBackendDataImportErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("in.tar", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := dataServer(t, 413, `{"error":"archive too large"}`)

	setConfirm(t, false, "")
	_, _, err := runBackend(t, "data", "import", "my-page", "in.tar")
	if err == nil || err.Error() != "refusing to replace /data of my-page without confirmation; pass --yes" {
		t.Fatalf("err = %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("request sent without confirmation")
	}

	if _, _, err := runBackend(t, "data", "import", "my-page", "missing.tar", "--yes"); err == nil {
		t.Fatal("missing file: want error")
	}

	_, _, err = runBackend(t, "data", "import", "my-page", "in.tar", "--yes")
	if err == nil || err.Error() != "archive too large" {
		t.Fatalf("err = %v", err)
	}
	<-seen

	for _, args := range [][]string{
		{"data"}, {"data", "bogus"}, {"data", "import", "my-page"}, {"data", "export"}, {"data", "export", "a", "b"},
	} {
		_, _, err := runBackend(t, args...)
		if err == nil || err.Error() != backendDataUsage {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}
