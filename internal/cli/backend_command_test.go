package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type backendReq struct {
	Method string
	URI    string
	Body   string
}

// backendServer records every request and answers with status and body.
func backendServer(t *testing.T, status int, ctype, body string) chan backendReq {
	t.Helper()
	seen := make(chan backendReq, 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		seen <- backendReq{r.Method, r.URL.RequestURI(), string(b)}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	configureTestClient(t, ts.URL)
	return seen
}

func runBackend(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout string
	stderr, err := captureStderr(t, func() error {
		var e error
		stdout, e = captureStdout(t, func() error { return cmdBackend(args) })
		return e
	})
	return stdout, stderr, err
}

func TestBackendShowTable(t *testing.T) {
	seen := backendServer(t, 200, "application/json", `{"enabled":true,"versions":[{"version":1,"image":"ghcr.io/a/b:1","port":8080,"running":true},{"version":2,"image":"ghcr.io/a/b:2","port":9000,"running":false}],"env":[{"name":"SMTP_HOST"}]}`)
	out, _, err := runBackend(t, "my-page")
	if err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.Method != "GET" || r.URI != "/api/uploads/my-page/backend" {
		t.Fatalf("req = %+v", r)
	}
	for _, want := range []string{"VERSION", "IMAGE", "PORT", "RUNNING", "ghcr.io/a/b:1", "yes", "no", "ENV", "SMTP_HOST"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	// "peek backend env <slug>" lists the same thing.
	if _, _, err := runBackend(t, "env", "my-page"); err != nil {
		t.Fatal(err)
	}
}

func TestBackendShowEmptyAndDisabled(t *testing.T) {
	backendServer(t, 200, "application/json", `{"enabled":true,"versions":[],"env":[]}`)
	out, _, err := runBackend(t, "p")
	if err != nil || strings.TrimSpace(out) != "no backend\ntoken calls: off" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	backendServer(t, 200, "application/json", `{"enabled":false,"versions":[],"env":[]}`)
	out, _, err = runBackend(t, "p")
	if err != nil || !strings.Contains(out, "backends are disabled on this server") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBackendSet(t *testing.T) {
	seen := backendServer(t, 200, "application/json", `{}`)
	out, _, err := runBackend(t, "set", "p", "ghcr.io/a/b:1", "--port", "9000")
	if err != nil || !strings.Contains(out, "ghcr.io/a/b:1") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	r := <-seen
	if r.Method != "PUT" || r.URI != "/api/uploads/p/versions/latest/backend" || r.Body != `{"image":"ghcr.io/a/b:1","port":9000}` {
		t.Fatalf("req = %+v", r)
	}
	if _, _, err := runBackend(t, "set", "p", "img", "--version", "3"); err != nil {
		t.Fatal(err)
	}
	r = <-seen
	if r.URI != "/api/uploads/p/versions/3/backend" || r.Body != `{"image":"img"}` {
		t.Fatalf("req = %+v", r)
	}
}

func TestBackendRemove(t *testing.T) {
	seen := backendServer(t, 200, "application/json", `{"status":"ok"}`)
	if _, _, err := runBackend(t, "remove", "p"); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.Method != "DELETE" || r.URI != "/api/uploads/p/versions/latest/backend" {
		t.Fatalf("req = %+v", r)
	}
	if _, _, err := runBackend(t, "remove", "p", "--version", "2"); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.URI != "/api/uploads/p/versions/2/backend" {
		t.Fatalf("req = %+v", r)
	}
}

func TestBackendEnvSetStdinAndValueWarning(t *testing.T) {
	seen := backendServer(t, 200, "application/json", `{"status":"ok","restart_required":true}`)
	withStdin(t, "s3cret\n")
	out, stderr, err := runBackend(t, "env", "set", "p", "SMTP_PASSWORD", "--value-stdin")
	if err != nil || stderr != "" || !strings.Contains(out, "SMTP_PASSWORD") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
	if r := <-seen; r.Method != "PUT" || r.URI != "/api/uploads/p/backend/env/SMTP_PASSWORD" || r.Body != `{"value":"s3cret"}` {
		t.Fatalf("req = %+v", r)
	}
	_, stderr, err = runBackend(t, "env", "set", "p", "A", "--value", "v")
	if err != nil || !strings.Contains(stderr, "warning: --value leaks into shell history; prefer --value-stdin") {
		t.Fatalf("stderr=%q err=%v", stderr, err)
	}
	if r := <-seen; r.Body != `{"value":"v"}` {
		t.Fatalf("req = %+v", r)
	}
}

func TestBackendEnvUnsetRestartLogs(t *testing.T) {
	seen := backendServer(t, 200, "text/plain", "line one\nline two\n")
	if _, _, err := runBackend(t, "env", "unset", "p", "A"); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.Method != "DELETE" || r.URI != "/api/uploads/p/backend/env/A" {
		t.Fatalf("req = %+v", r)
	}
	if _, _, err := runBackend(t, "restart", "p"); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.Method != "POST" || r.URI != "/api/uploads/p/backend/restart" {
		t.Fatalf("req = %+v", r)
	}
	out, _, err := runBackend(t, "logs", "p", "--tail", "50")
	if err != nil || out != "line one\nline two\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if r := <-seen; r.Method != "GET" || r.URI != "/api/uploads/p/versions/latest/backend/logs?tail=50" {
		t.Fatalf("req = %+v", r)
	}
	if _, _, err := runBackend(t, "logs", "p", "--version", "2"); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; r.URI != "/api/uploads/p/versions/2/backend/logs" {
		t.Fatalf("req = %+v", r)
	}
}

func TestBackendServerErrors(t *testing.T) {
	backendServer(t, 404, "application/json", `{"error":"backend is not running"}`)
	if _, _, err := runBackend(t, "logs", "p"); err == nil || !strings.Contains(err.Error(), "backend is not running") {
		t.Fatalf("err = %v", err)
	}
	for _, args := range [][]string{
		{"set", "p", "img"}, {"remove", "p"}, {"restart", "p"}, {"p"},
		{"env", "unset", "p", "A"},
	} {
		if _, _, err := runBackend(t, args...); err == nil {
			t.Fatalf("%v: expected error", args)
		}
	}
	withStdin(t, "v")
	if _, _, err := runBackend(t, "env", "set", "p", "A", "--value-stdin"); err == nil {
		t.Fatal("env set: expected error")
	}
}

func TestBackendUsageErrors(t *testing.T) {
	configureTestClient(t, "http://127.0.0.1:1")
	cases := [][]string{
		nil,
		{"-x"},
		{"a", "b"},
		{"bad/slug"},
		{"set", "p"},
		{"set", "p", "img", "--bogus"},
		{"set", "p", "img", "--port"},
		{"set", "p", "img", "--port", "0"},
		{"set", "p", "img", "--version", "0"},
		{"set", "bad/slug", "img"},
		{"remove"},
		{"remove", "p", "--version", "x"},
		{"remove", "bad/slug"},
		{"env"},
		{"env", "set", "p", "A"},
		{"env", "set", "p", "A", "--value", "x", "--value-stdin"},
		{"env", "set", "bad/slug", "A", "--value", "x"},
		{"env", "unset", "p"},
		{"env", "unset", "bad/slug", "A"},
		{"restart"},
		{"restart", "bad/slug"},
		{"logs"},
		{"logs", "p", "--tail", "0"},
		{"logs", "p", "--tail", "5000"},
		{"logs", "p", "--version", "x"},
		{"logs", "bad/slug"},
	}
	for _, args := range cases {
		if _, _, err := runBackend(t, args...); err == nil {
			t.Fatalf("%v: expected error", args)
		}
	}
}

func TestBackendEnvFailures(t *testing.T) {
	withStdin(t, "v")
	cases := map[string]func() error{
		"show":     func() error { return cmdBackend([]string{"p"}) },
		"set":      func() error { return cmdBackend([]string{"set", "p", "img"}) },
		"remove":   func() error { return cmdBackend([]string{"remove", "p"}) },
		"envset":   func() error { return cmdBackend([]string{"env", "set", "p", "A", "--value", "x"}) },
		"envunset": func() error { return cmdBackend([]string{"env", "unset", "p", "A"}) },
		"restart":  func() error { return cmdBackend([]string{"restart", "p"}) },
		"logs":     func() error { return cmdBackend([]string{"logs", "p"}) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { covEnvFailures(t, fn) })
	}
}

type callReq struct {
	Method string
	URI    string
	Body   string
	Header http.Header
}

// backendCallServer records every request and answers with status, respHeader and body.
func backendCallServer(t *testing.T, status int, respHeader map[string]string, body string) chan callReq {
	t.Helper()
	seen := make(chan callReq, 64)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		seen <- callReq{r.Method, r.URL.RequestURI(), string(b), r.Header.Clone()}
		for k, v := range respHeader {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	configureTestClient(t, ts.URL)
	return seen
}

func TestBackendCallRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(file, []byte(`{"f":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const base = "/api/uploads/my-page/versions/latest/svc/"
	cases := []struct {
		name        string
		args        []string
		stdin       string
		method, uri string
		body, ctype string
		accept      string
	}{
		{name: "default", args: []string{"my-page", "api/items"}, method: "GET", uri: base + "api/items"},
		{name: "leading slash", args: []string{"my-page", "/api/items"}, method: "GET", uri: base + "api/items"},
		{name: "query", args: []string{"my-page", "api/items?a=1&b=2"}, method: "GET", uri: base + "api/items?a=1&b=2"},
		{name: "escape", args: []string{"my-page", "a b/c"}, method: "GET", uri: base + "a%20b/c"},
		{name: "version", args: []string{"my-page", "x", "--version", "3"}, method: "GET", uri: "/api/uploads/my-page/versions/3/svc/x"},
		{name: "data", args: []string{"my-page", "x", "--data", `{"a":1}`}, method: "POST", uri: base + "x", body: `{"a":1}`, ctype: "application/json"},
		{name: "file", args: []string{"my-page", "x", "--data", "@" + file}, method: "POST", uri: base + "x", body: `{"f":1}`, ctype: "application/json"},
		{name: "stdin", args: []string{"my-page", "x", "--data", "-"}, stdin: `{"s":1}`, method: "POST", uri: base + "x", body: `{"s":1}`, ctype: "application/json"},
		{name: "method", args: []string{"my-page", "x", "--method", "put"}, method: "PUT", uri: base + "x"},
		{name: "header", args: []string{"my-page", "x", "-H", "Accept: text/plain"}, method: "GET", uri: base + "x", accept: "text/plain"},
		{name: "content type", args: []string{"my-page", "x", "--data", "a=b", "-H", "content-type: text/plain"}, method: "POST", uri: base + "x", body: "a=b", ctype: "text/plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := backendCallServer(t, 200, nil, "ok")
			if tc.stdin != "" {
				withStdin(t, tc.stdin)
			}
			out, _, err := runBackend(t, append([]string{"call"}, tc.args...)...)
			if err != nil || out != "ok" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			r := <-seen
			if r.Method != tc.method || r.URI != tc.uri || r.Body != tc.body {
				t.Fatalf("req = %+v", r)
			}
			if got := r.Header.Get("Content-Type"); got != tc.ctype {
				t.Fatalf("content-type = %q, want %q", got, tc.ctype)
			}
			if got := r.Header.Get("Accept"); tc.accept != "" && got != tc.accept {
				t.Fatalf("accept = %q", got)
			}
		})
	}
}

func TestBackendCallRejectsBadInput(t *testing.T) {
	big := strings.Repeat("x", 1<<20+1)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"dot", []string{"p", "a/./b"}, "path must not contain . or .. segments"},
		{"dotdot", []string{"p", "../b"}, "path must not contain . or .. segments"},
		{"method", []string{"p", "x", "--method", "TRACE"}, "--method must be GET, POST, PUT, PATCH or DELETE"},
		{"header name", []string{"p", "x", "-H", "X-Foo: 1"}, "only Content-Type and Accept headers are passed to the backend"},
		{"header colon", []string{"p", "x", "-H", "nocolon"}, `-H must be "Name: value"`},
		{"big", []string{"p", "x", "--data", big}, "--data is larger than 1 MiB"},
		{"missing", []string{"p"}, `usage: peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := backendCallServer(t, 200, nil, "")
			out, _, err := runBackend(t, append([]string{"call"}, tc.args...)...)
			if err == nil || err.Error() != tc.want || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if len(seen) != 0 {
				t.Fatal("request was sent")
			}
		})
	}
}

func TestBackendCallOutput(t *testing.T) {
	backendCallServer(t, 200, nil, `{"ok":true}`)
	out, _, err := runBackend(t, "call", "p", "x")
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	backendCallServer(t, 500, map[string]string{"X-Peek-Relay": "backend"}, "boom")
	out, _, err = runBackend(t, "call", "p", "x")
	if err == nil || err.Error() != "backend returned 500 Internal Server Error" || out != "boom" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	backendCallServer(t, 403, nil, `{"error":"token calls are not enabled for this backend"}`)
	out, _, err = runBackend(t, "call", "p", "x")
	if err == nil || err.Error() != "token calls are not enabled for this backend" || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBackendTokenCallsCommand(t *testing.T) {
	seen := backendCallServer(t, 200, nil, `{"status":"ok"}`)
	for _, tc := range []struct{ arg, body, out string }{
		{"on", `{"enabled":true}`, "token calls enabled for my-page"},
		{"off", `{"enabled":false}`, "token calls disabled for my-page"},
	} {
		out, _, err := runBackend(t, "token-calls", tc.arg, "my-page")
		if err != nil || strings.TrimSpace(out) != tc.out {
			t.Fatalf("out=%q err=%v", out, err)
		}
		r := <-seen
		if r.Method != "PUT" || r.URI != "/api/uploads/my-page/backend/token-calls" || r.Body != tc.body {
			t.Fatalf("req = %+v", r)
		}
	}
	for _, args := range [][]string{{"token-calls"}, {"token-calls", "maybe", "p"}, {"token-calls", "on"}, {"token-calls", "--bad"}} {
		_, _, err := runBackend(t, args...)
		if err == nil {
			t.Fatalf("%v: expected error", args)
		}
		if args[len(args)-1] != "--bad" && err.Error() != "usage: peek backend token-calls on|off <slug>" {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestBackendSetAllowTokenCalls(t *testing.T) {
	seen := backendCallServer(t, 200, nil, `{}`)
	out, _, err := runBackend(t, "set", "p", "img:1", "--allow-token-calls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "backend set on p (version latest): img:1") || !strings.Contains(out, "token calls enabled for p") {
		t.Fatalf("out = %q", out)
	}
	if r := <-seen; r.URI != "/api/uploads/p/versions/latest/backend" {
		t.Fatalf("first = %+v", r)
	}
	if r := <-seen; r.Method != "PUT" || r.URI != "/api/uploads/p/backend/token-calls" || r.Body != `{"enabled":true}` {
		t.Fatalf("second = %+v", r)
	}
}

func TestBackendShowTokenCalls(t *testing.T) {
	backendServer(t, 200, "application/json", `{"enabled":true,"token_calls":true,"versions":[],"env":[]}`)
	out, _, err := runBackend(t, "p")
	if err != nil || !strings.Contains(out, "token calls: on") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	backendServer(t, 200, "application/json", `{"enabled":true,"token_calls":false,"versions":[],"env":[]}`)
	out, _, err = runBackend(t, "p")
	if err != nil || !strings.Contains(out, "token calls: off") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
