package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errReader) Close() error {
	return nil
}

func TestDecodeRespUsesStructuredAPIError(t *testing.T) {
	err := decodeResp(errorResponse(http.StatusForbidden, `{"error":"not owner"}`), nil)
	if err == nil || err.Error() != "not owner" {
		t.Fatalf("error = %v, want not owner", err)
	}
}

func TestDecodeRespFallsBackToPlainErrorBody(t *testing.T) {
	err := decodeResp(errorResponse(http.StatusBadGateway, "upstream unavailable"), nil)
	if err == nil || err.Error() != "502 Bad Gateway: upstream unavailable" {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRespFallsBackToStatusWhenErrorBodyEmpty(t *testing.T) {
	err := decodeResp(errorResponse(http.StatusUnauthorized, ""), nil)
	if err == nil || err.Error() != "401 Unauthorized" {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeRespReportsErrorBodyReadFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 Internal Server Error",
		Body:       errReader{},
	}
	err := decodeResp(resp, nil)
	if err == nil || !strings.Contains(err.Error(), "read error response") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfigReturnsEmptyWhenConfigIsMissing(t *testing.T) {
	setTestConfigHome(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "" || cfg.Token != "" {
		t.Fatalf("config = %+v, want empty", cfg)
	}
}

func TestLoadConfigReportsConfigReadFailure(t *testing.T) {
	setTestConfigHome(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err = LoadConfig()
	if err == nil {
		t.Fatal("expected config read failure")
	}
}

func errorResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestSaveConfigTightensExistingFileMode(t *testing.T) {
	setTestConfigHome(t)
	p, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(&Config{Host: "https://peek.example.com", Token: "secret"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestConfigPathWithoutHomeUsesRelativeDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(t.TempDir())
	p, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if p != filepath.Join("peek", "config.json") {
		t.Fatalf("path = %q", p)
	}
}

func TestConfigPathReportsMkdirFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocker)
	if _, err := configPath(); err == nil {
		t.Fatal("expected a mkdir error")
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig should report the path error")
	}
	if err := SaveConfig(&Config{}); err == nil {
		t.Fatal("SaveConfig should report the path error")
	}
}

func TestLoadConfigRejectsBadJSON(t *testing.T) {
	setTestConfigHome(t)
	writeBadConfig(t)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected a JSON error")
	}
}

func TestSaveConfigReportsWriteFailure(t *testing.T) {
	setTestConfigHome(t)
	breakSaveConfig(t)
	if err := SaveConfig(&Config{Host: "h"}); err == nil {
		t.Fatal("expected a write error")
	}
}

func TestNewClientRequiresHostAndToken(t *testing.T) {
	t.Setenv("PEEK_HOST", "")
	t.Setenv("PEEK_TOKEN", "")
	if _, err := newClient(&Config{}); err == nil || !strings.Contains(err.Error(), "host not set") {
		t.Fatalf("error = %v", err)
	}
	if _, err := newClient(&Config{Host: "http://h"}); err == nil || !strings.Contains(err.Error(), "token not set") {
		t.Fatalf("error = %v", err)
	}
	c, err := newClient(&Config{Host: "http://h/", Token: "t"})
	if err != nil || c.host != "http://h" {
		t.Fatalf("client = %+v, %v", c, err)
	}
}

func TestClientRequestBuildErrors(t *testing.T) {
	c := &client{host: "http://bad\x01host", token: "t"}
	if err := c.getJSON("/x", nil); err == nil {
		t.Fatal("getJSON: expected a build error")
	}
	if err := c.postJSON("/x", map[string]string{"a": "b"}, nil); err == nil {
		t.Fatal("postJSON: expected a build error")
	}
	if err := c.del("/x", nil); err == nil {
		t.Fatal("del: expected a build error")
	}
	if err := postJSONNoAuth("http://bad\x01host", "/x", nil, nil); err == nil {
		t.Fatal("postJSONNoAuth: expected a build error")
	}
}

func TestClientTransportErrors(t *testing.T) {
	c := &client{host: closedServerURL(t), token: "t"}
	if err := c.getJSON("/x", nil); err == nil {
		t.Fatal("getJSON: expected a transport error")
	}
	if err := c.postJSON("/x", map[string]string{"a": "b"}, nil); err == nil {
		t.Fatal("postJSON: expected a transport error")
	}
	if err := c.postJSON("/x", nil, nil); err == nil {
		t.Fatal("postJSON without body: expected a transport error")
	}
	if err := c.del("/x", nil); err == nil {
		t.Fatal("del: expected a transport error")
	}
	if err := postJSONNoAuth(c.host, "/x", nil, nil); err == nil {
		t.Fatal("postJSONNoAuth: expected a transport error")
	}
}

func TestPostJSONRejectsUnmarshalableBody(t *testing.T) {
	c := &client{host: "http://unused.invalid", token: "t"}
	if err := c.postJSON("/x", make(chan int), nil); err == nil {
		t.Fatal("expected a marshal error")
	}
	if err := postJSONNoAuth("http://unused.invalid", "/x", make(chan int), nil); err == nil {
		t.Fatal("postJSONNoAuth: expected a marshal error")
	}
}

func TestSaveConfigReportsChmodFailure(t *testing.T) {
	setTestConfigHome(t)
	p, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	// A self-referential symlink makes chmod fail with ELOOP, not "not exist".
	if err := os.Symlink(filepath.Base(p), p); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(&Config{Host: "h"}); err == nil {
		t.Fatal("expected a chmod error")
	}
}

func TestReqWith(t *testing.T) {
	var gotAuth, gotCT, gotX string
	var gotLen int64
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotX, gotLen = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.ContentLength
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer ts.Close()
	c := &client{host: ts.URL, token: "tok"}
	h := http.Header{"Content-Type": {"application/x-tar"}, "Accept": {"text/plain"}}
	resp, err := c.reqWith(longHTTPClient, "PUT", "/x", strings.NewReader("hello"), 5, h)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer tok" || gotCT != "application/x-tar" || gotX != "text/plain" || gotLen != 5 || gotBody != "hello" {
		t.Fatalf("auth=%q ct=%q accept=%q len=%d body=%q", gotAuth, gotCT, gotX, gotLen, gotBody)
	}
}
