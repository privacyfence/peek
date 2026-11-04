package backends

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
	Header http.Header
}

// fakeDocker plays the Docker API: handler gets every request and the
// recorded copy is appended to reqs.
func fakeDocker(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*DockerRuntime, *[]recorded) {
	t.Helper()
	// An engine that accepts 1.24 and newer, so requests use minDockerAPIVersion.
	return fakeDockerVersion(t, status(200, `{"ApiVersion":"1.51","MinAPIVersion":"1.24"}`), handler)
}

// fakeDockerVersion is fakeDocker whose unversioned /version answers with version; the
// /version requests are not recorded.
func fakeDockerVersion(t *testing.T, version, handler func(w http.ResponseWriter, r *http.Request)) (*DockerRuntime, *[]recorded) {
	t.Helper()
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			version(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		reqs = append(reqs, recorded{r.Method, r.URL.EscapedPath(), r.URL.Query(), string(body), r.Header.Clone()})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	rt, err := NewDockerRuntime("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return rt, &reqs
}

func status(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func TestNewDockerRuntimeHosts(t *testing.T) {
	if _, err := NewDockerRuntime("unix:///tmp/x.sock"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"https://x", "", "unix://", "tcp://"} {
		_, err := NewDockerRuntime(host)
		if err == nil || !strings.Contains(err.Error(), "unsupported docker host") {
			t.Errorf("NewDockerRuntime(%q) = %v", host, err)
		}
	}
}

func TestPingUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.43/_ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "OK") })
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	rt, err := NewDockerRuntime("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPingAndErrors(t *testing.T) {
	rt, reqs := fakeDocker(t, status(200, "OK"))
	if err := rt.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.Method != "GET" || r.Path != "/v1.43/_ping" {
		t.Fatalf("request %+v", r)
	}

	rt, _ = fakeDocker(t, status(500, `{"message":"boom"}`))
	err := rt.Ping(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Message != "boom" {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "docker api: 500: boom" || IsNotFound(err) {
		t.Fatalf("err = %v notfound=%v", err, IsNotFound(err))
	}

	rt, _ = fakeDocker(t, status(404, `not json`))
	err = rt.Ping(context.Background())
	if !IsNotFound(err) || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("err = %v", err)
	}
	if IsNotFound(errors.New("x")) {
		t.Fatal("plain error is not a 404")
	}

	rt, _ = fakeDocker(t, status(200, ""))
	rt.client.Transport.(*http.Transport).DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no daemon")
	}
	if err := rt.Ping(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestImageExists(t *testing.T) {
	rt, reqs := fakeDocker(t, status(200, "{}"))
	ok, err := rt.ImageExists(context.Background(), "ghcr.io/a/b:1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if r := (*reqs)[0]; r.Method != "GET" || r.Path != "/v1.43/images/ghcr.io%2Fa%2Fb:1/json" {
		t.Fatalf("request %+v", r)
	}
	rt, _ = fakeDocker(t, status(404, `{"message":"no such image"}`))
	if ok, err := rt.ImageExists(context.Background(), "x"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	rt, _ = fakeDocker(t, status(500, `{"message":"boom"}`))
	if _, err := rt.ImageExists(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPullImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	tests := []struct{ ref, from, tag string }{
		{"alpine", "alpine", "latest"},
		{"ghcr.io/a/b:1.2", "ghcr.io/a/b", "1.2"},
		{"localhost:5000/app", "localhost:5000/app", "latest"},
		{"ghcr.io/a/b@" + digest, "ghcr.io/a/b", digest},
		{"ghcr.io/a/b:1@" + digest, "ghcr.io/a/b", digest},
	}
	for _, tt := range tests {
		rt, reqs := fakeDocker(t, status(200, `{"status":"Pulling"}`+"\n"+`{"status":"Done"}`))
		if err := rt.PullImage(context.Background(), tt.ref, ""); err != nil {
			t.Fatalf("%s: %v", tt.ref, err)
		}
		r := (*reqs)[0]
		if r.Method != "POST" || r.Path != "/v1.43/images/create" || r.Query.Get("fromImage") != tt.from || r.Query.Get("tag") != tt.tag {
			t.Errorf("%s: request %+v", tt.ref, r)
		}
		if _, ok := r.Header["X-Registry-Auth"]; ok {
			t.Errorf("%s: unexpected X-Registry-Auth", tt.ref)
		}
	}

	rt, reqs := fakeDocker(t, status(200, "{\"status\":\"Pulling\"}\n{\"error\":\"denied\"}\n"))
	err := rt.PullImage(context.Background(), "x/y", "AUTH")
	if err == nil || !strings.Contains(err.Error(), "denied") || !strings.Contains(err.Error(), "pull x/y") {
		t.Fatalf("err = %v", err)
	}
	if got := (*reqs)[0].Header.Get("X-Registry-Auth"); got != "AUTH" {
		t.Fatalf("X-Registry-Auth = %q", got)
	}

	rt, _ = fakeDocker(t, status(200, `{"status":`))
	if err := rt.PullImage(context.Background(), "x", ""); err == nil {
		t.Fatal("expected decode error")
	}
	rt, _ = fakeDocker(t, status(404, `{"message":"pull access denied"}`))
	if err := rt.PullImage(context.Background(), "x", ""); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureNetwork(t *testing.T) {
	rt, reqs := fakeDocker(t, status(200, "{}"))
	if err := rt.EnsureNetwork(context.Background(), "peek-backends"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Method != "GET" || (*reqs)[0].Path != "/v1.43/networks/peek-backends" {
		t.Fatalf("requests %+v", *reqs)
	}

	rt, reqs = fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			status(404, `{"message":"no such network"}`)(w, r)
			return
		}
		status(201, `{"Id":"n1"}`)(w, r)
	})
	if err := rt.EnsureNetwork(context.Background(), "peek-backends"); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 2 || (*reqs)[1].Method != "POST" || (*reqs)[1].Path != "/v1.43/networks/create" {
		t.Fatalf("requests %+v", *reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte((*reqs)[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	labels, _ := body["Labels"].(map[string]any)
	if body["Name"] != "peek-backends" || body["Driver"] != "bridge" || labels["peek.managed"] != "true" {
		t.Fatalf("body %v", body)
	}

	rt, _ = fakeDocker(t, status(500, `{"message":"boom"}`))
	if err := rt.EnsureNetwork(context.Background(), "n"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateContainer(t *testing.T) {
	rt, reqs := fakeDocker(t, status(201, `{"Id":"abc123"}`))
	id, err := rt.CreateContainer(context.Background(), ContainerSpec{
		Name: "peek-1-u1-v1", Image: "alpine", Env: []string{"A=b"},
		Labels: map[string]string{"peek.managed": "true"}, Network: "peek-backends",
		Volume: "peek-1-data-u1", MemoryBytes: 256 << 20, NanoCPUs: 500_000_000,
	})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	r := (*reqs)[0]
	if r.Method != "POST" || r.Path != "/v1.43/containers/create" || r.Query.Get("name") != "peek-1-u1-v1" {
		t.Fatalf("request %+v", r)
	}
	for _, want := range []string{
		`"ReadonlyRootfs":true`, `"CapDrop":["ALL"]`, `"no-new-privileges:true"`, `"PidsLimit":256`,
		`"Mounts":[{"Type":"volume","Source":"peek-1-data-u1","Target":"/data"}]`,
		`"NetworkMode":"peek-backends"`, `"Memory":268435456`, `"MemorySwap":268435456`, `"NanoCpus":500000000`,
		`"Tmpfs":{"/tmp":"rw,nosuid,size=64m"}`, `"RestartPolicy":{"Name":"no"}`, `"Init":true`,
		`"max-size":"10m"`, `"Image":"alpine"`, `"Env":["A=b"]`,
	} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body missing %s:\n%s", want, r.Body)
		}
	}
	if strings.Contains(r.Body, `"Cmd"`) {
		t.Errorf("Cmd should be omitted:\n%s", r.Body)
	}

	rt, reqs = fakeDocker(t, status(201, `{"Id":"x"}`))
	if _, err := rt.CreateContainer(context.Background(), ContainerSpec{Name: "n", Image: "i", Cmd: []string{"sleep", "1"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*reqs)[0].Body, `"Cmd":["sleep","1"]`) {
		t.Errorf("Cmd missing:\n%s", (*reqs)[0].Body)
	}

	rt, _ = fakeDocker(t, status(409, `{"message":"conflict"}`))
	if _, err := rt.CreateContainer(context.Background(), ContainerSpec{}); err == nil {
		t.Fatal("expected error")
	}
	rt, _ = fakeDocker(t, status(201, `nope`))
	if _, err := rt.CreateContainer(context.Background(), ContainerSpec{}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestStartStopRemove(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name   string
		call   func(*DockerRuntime) error
		method string
		path   string
		ok     []int
		bad    int
	}
	ops := []op{
		{"start", func(r *DockerRuntime) error { return r.StartContainer(ctx, "c1") }, "POST", "/v1.43/containers/c1/start", []int{204, 304}, 500},
		{"stop", func(r *DockerRuntime) error { return r.StopContainer(ctx, "c1", 10*time.Second) }, "POST", "/v1.43/containers/c1/stop", []int{204, 304, 404}, 500},
		{"remove", func(r *DockerRuntime) error { return r.RemoveContainer(ctx, "c1") }, "DELETE", "/v1.43/containers/c1", []int{204, 404}, 500},
		{"volume", func(r *DockerRuntime) error { return r.RemoveVolume(ctx, "v1") }, "DELETE", "/v1.43/volumes/v1", []int{204, 404}, 409},
	}
	for _, o := range ops {
		for _, code := range o.ok {
			rt, reqs := fakeDocker(t, status(code, ""))
			if err := o.call(rt); err != nil {
				t.Errorf("%s %d: %v", o.name, code, err)
			}
			r := (*reqs)[0]
			wantPath := o.path
			if o.name == "volume" {
				wantPath = "/v1.43/volumes/v1"
			}
			if r.Method != o.method || r.Path != wantPath {
				t.Errorf("%s: request %+v", o.name, r)
			}
			switch o.name {
			case "stop":
				if r.Query.Get("t") != "10" {
					t.Errorf("stop query %v", r.Query)
				}
			case "remove":
				if r.Query.Get("force") != "1" {
					t.Errorf("remove query %v", r.Query)
				}
			}
		}
		rt, _ := fakeDocker(t, status(o.bad, `{"message":"boom"}`))
		if err := o.call(rt); err == nil {
			t.Errorf("%s: expected error for %d", o.name, o.bad)
		}
	}
	// 404 on start is a real error.
	rt, _ := fakeDocker(t, status(404, `{"message":"no such container"}`))
	if err := rt.StartContainer(ctx, "c1"); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestContainerIP(t *testing.T) {
	ctx := context.Background()
	rt, reqs := fakeDocker(t, status(200, `{"NetworkSettings":{"Networks":{"peek-backends":{"IPAddress":"172.20.0.5"},"other":{"IPAddress":""}}}}`))
	ip, err := rt.ContainerIP(ctx, "c1", "peek-backends")
	if err != nil || ip != "172.20.0.5" {
		t.Fatalf("ip=%q err=%v", ip, err)
	}
	if r := (*reqs)[0]; r.Method != "GET" || r.Path != "/v1.43/containers/c1/json" {
		t.Fatalf("request %+v", r)
	}
	if _, err := rt.ContainerIP(ctx, "c1", "other"); err == nil || !strings.Contains(err.Error(), `has no address on network "other"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := rt.ContainerIP(ctx, "c1", "missing"); err == nil {
		t.Fatal("expected error")
	}
	rt, _ = fakeDocker(t, status(404, `{"message":"gone"}`))
	if _, err := rt.ContainerIP(ctx, "c1", "n"); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	rt, _ = fakeDocker(t, status(200, `nope`))
	if _, err := rt.ContainerIP(ctx, "c1", "n"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestListContainers(t *testing.T) {
	rt, reqs := fakeDocker(t, status(200, `[{"Id":"a","Names":["/peek-1-u1-v1"],"Labels":{"peek.managed":"true"},"State":"running"},{"Id":"b","Names":[],"State":"exited"}]`))
	got, err := rt.ListContainers(context.Background(), map[string]string{"peek.instance": "ab12", "peek.managed": "true"})
	if err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.Method != "GET" || r.Path != "/v1.43/containers/json" || r.Query.Get("all") != "1" {
		t.Fatalf("request %+v", r)
	}
	if f := r.Query.Get("filters"); f != `{"label":["peek.instance=ab12","peek.managed=true"]}` {
		t.Fatalf("filters = %s", f)
	}
	if len(got) != 2 || got[0].ID != "a" || got[0].Name != "peek-1-u1-v1" || got[0].State != "running" || got[0].Labels["peek.managed"] != "true" || got[1].Name != "" {
		t.Fatalf("got %+v", got)
	}
	rt, _ = fakeDocker(t, status(200, `nope`))
	if _, err := rt.ListContainers(context.Background(), nil); err == nil {
		t.Fatal("expected decode error")
	}
	rt, _ = fakeDocker(t, status(500, `{"message":"boom"}`))
	if _, err := rt.ListContainers(context.Background(), nil); err == nil {
		t.Fatal("expected error")
	}
}

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestContainerLogs(t *testing.T) {
	stream := append(frame(1, "out line\n"), frame(2, "err line\n")...)
	rt, reqs := fakeDocker(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(stream) })
	got, err := rt.ContainerLogs(context.Background(), "c1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "out line\nerr line\n" {
		t.Fatalf("got %q", got)
	}
	r := (*reqs)[0]
	if r.Method != "GET" || r.Path != "/v1.43/containers/c1/logs" || r.Query.Get("tail") != "50" ||
		r.Query.Get("stdout") != "1" || r.Query.Get("stderr") != "1" || r.Query.Get("timestamps") != "1" {
		t.Fatalf("request %+v", r)
	}
	rt, _ = fakeDocker(t, status(404, `{"message":"gone"}`))
	if _, err := rt.ContainerLogs(context.Background(), "c1", 1); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestDemuxLogs(t *testing.T) {
	// Truncated header and truncated payload keep what was read.
	got, err := demuxLogs(strings.NewReader(string(frame(1, "ab"))+"\x01\x00"), 100)
	if err != nil || string(got) != "ab" {
		t.Fatalf("got %q err=%v", got, err)
	}
	trunc := frame(1, "abcdef")
	got, err = demuxLogs(strings.NewReader(string(trunc[:10])), 100)
	if err != nil || string(got) != "ab" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// The limit caps the output.
	got, err = demuxLogs(strings.NewReader(string(frame(1, "abcdef"))+string(frame(1, "ghi"))), 4)
	if err != nil || string(got) != "abcd" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// Exact-fit frames end at the limit.
	got, err = demuxLogs(strings.NewReader(string(frame(1, "abcd"))+string(frame(1, "ghi"))), 4)
	if err != nil || string(got) != "abcd" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// Read errors surface.
	if _, err := demuxLogs(errReader{}, 10); err == nil {
		t.Fatal("expected read error")
	}
	if _, err := demuxLogs(io.MultiReader(strings.NewReader(string(frame(1, "abcdef"))[:8]), errReader{}), 10); err == nil {
		t.Fatal("expected payload read error")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestAPIVersionFollowsEngineMinimum(t *testing.T) {
	// Docker Engine 29 refuses requests older than API 1.44.
	versionCalls := 0
	version := func(w http.ResponseWriter, _ *http.Request) {
		versionCalls++
		_, _ = io.WriteString(w, `{"ApiVersion":"1.52","MinAPIVersion":"1.44"}`)
	}
	rt, reqs := fakeDockerVersion(t, version, status(200, "OK"))
	for range 3 {
		if err := rt.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range *reqs {
		if r.Path != "/v1.44/_ping" {
			t.Fatalf("request %+v", r)
		}
	}
	if versionCalls != 1 {
		t.Fatalf("/version asked %d times, want once", versionCalls)
	}
}

func TestAPIVersionWithoutUsableAnswer(t *testing.T) {
	for name, version := range map[string]func(http.ResponseWriter, *http.Request){
		"error status": status(404, `{"message":"page not found"}`),
		"bad json":     status(200, "not json"),
		"no minimum":   status(200, `{"ApiVersion":"1.45"}`),
		"bad minimum":  status(200, `{"MinAPIVersion":"latest"}`),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			counted := func(w http.ResponseWriter, r *http.Request) { calls++; version(w, r) }
			rt, reqs := fakeDockerVersion(t, counted, status(200, "OK"))
			for range 2 {
				if err := rt.Ping(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if r := (*reqs)[0]; r.Path != "/v1.43/_ping" {
				t.Fatalf("request %+v", r)
			}
			if calls != 1 {
				t.Fatalf("/version asked %d times, want once (the answer is kept)", calls)
			}
		})
	}
}

func TestAPIVersionAsksAgainWhenEngineUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	rt, err := NewDockerRuntime("tcp://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Ping(context.Background()); err == nil {
		t.Fatal("ping to a closed port succeeded")
	}
	if rt.version != "" {
		t.Fatalf("version %q kept although the engine never answered", rt.version)
	}
}

func TestAPIVersionCancelledContext(t *testing.T) {
	rt, _ := fakeDocker(t, status(200, "OK"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rt.Ping(ctx); err == nil {
		t.Fatal("ping with a cancelled context succeeded")
	}
	if rt.version != "" {
		t.Fatalf("version %q kept from a cancelled request", rt.version)
	}
}

func TestPickAPIVersion(t *testing.T) {
	for engineMin, want := range map[string]string{
		"1.24":  "1.43",
		"1.43":  "1.43",
		"1.44":  "1.44",
		"1.100": "1.100",
		"2.0":   "2.0",
		"":      "1.43",
		"1":     "1.43",
		"1.x":   "1.43",
		"-1.50": "1.43",
	} {
		if got := pickAPIVersion(engineMin); got != want {
			t.Errorf("pickAPIVersion(%q) = %q, want %q", engineMin, got, want)
		}
	}
	if newerAPIVersion("1.44", "bad") {
		t.Error("a version is newer than an invalid one")
	}
}
