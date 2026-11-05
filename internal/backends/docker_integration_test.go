package backends

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const integrationSocket = "/var/run/docker.sock"

// TestDockerIntegration drives a real Docker daemon with the contact-form
// example image: start, health check with and without the token, logs, close.
func TestDockerIntegration(t *testing.T) {
	if os.Getenv("PEEK_DOCKER_TEST") != "1" {
		t.Skip("set PEEK_DOCKER_TEST=1 to run against a Docker daemon")
	}
	image := os.Getenv("PEEK_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Fatal("PEEK_DOCKER_TEST_IMAGE is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, err := NewDockerRuntime("unix://" + integrationSocket)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	network := "peek-it-" + hex.EncodeToString(suffix)
	const instance = "itest001"
	t.Cleanup(func() { deleteNetwork(t, network) })

	m := NewManager(Config{Runtime: rt, Network: network, Instance: instance})
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	closed := false
	closeManager := func() error {
		closed = true
		return m.Close(ctx)
	}
	t.Cleanup(func() {
		if !closed {
			_ = closeManager()
		}
		_ = rt.RemoveVolume(context.Background(), VolumeName(instance, 1))
	})

	ep, err := m.Ensure(ctx, Spec{
		UploadID:    1,
		Version:     1,
		Image:       image,
		Port:        8080,
		Env:         map[string]string{"MAIL_TO": "to@example.test", "MAIL_FROM": "from@example.test", "SMTP_HOST": "smtp.example.test"},
		MemoryBytes: 128 << 20,
		NanoCPUs:    250_000_000,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	get := func(token string) (int, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("X-Peek-Backend-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	if code, body := get(ep.Token); code != http.StatusOK || body != "ok" {
		t.Fatalf("healthz with token = %d %q, want 200 \"ok\"", code, body)
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("healthz without token = %d, want 401", code)
	}
	if _, err := m.Logs(ctx, 1, 1, 50); err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if err := closeManager(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	left, err := rt.ListContainers(ctx, map[string]string{"peek.instance": instance})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("containers left after Close: %+v", left)
	}
	if err := rt.RemoveVolume(ctx, VolumeName(instance, 1)); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
}

// TestDockerIntegrationDataExportImport imports a tar into a page's /data volume on a real
// Docker daemon, exports it again and checks ownership, replacement and helper cleanup.
func TestDockerIntegrationDataExportImport(t *testing.T) {
	if os.Getenv("PEEK_DOCKER_TEST") != "1" {
		t.Skip("set PEEK_DOCKER_TEST=1 to run against a Docker daemon")
	}
	image := os.Getenv("PEEK_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Fatal("PEEK_DOCKER_TEST_IMAGE is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rt, err := NewDockerRuntime("unix://" + integrationSocket)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	network := "peek-it-" + hex.EncodeToString(suffix)
	const instance = "itest002"
	const uploadID = 2
	t.Cleanup(func() { deleteNetwork(t, network) })
	t.Cleanup(func() { _ = rt.RemoveVolume(context.Background(), VolumeName(instance, uploadID)) })

	m := NewManager(Config{Runtime: rt, Network: network, Instance: instance})
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	spec := Spec{UploadID: uploadID, Version: 1, Image: image, Port: 8080, MemoryBytes: 128 << 20, NanoCPUs: 250_000_000}

	makeTar := func(entries map[string]string, order []string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, name := range order {
			h := &tar.Header{Name: name, Mode: 0o644, Uid: 1000, Gid: 1000}
			if strings.HasSuffix(name, "/") {
				h.Typeflag, h.Mode = tar.TypeDir, 0o755
			} else {
				h.Typeflag, h.Size = tar.TypeReg, int64(len(entries[name]))
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				if _, err := tw.Write([]byte(entries[name])); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	export := func() (map[string]*tar.Header, map[string]string) {
		var buf bytes.Buffer
		if _, err := m.ExportData(ctx, spec, &buf); err != nil {
			t.Fatalf("ExportData: %v", err)
		}
		hdrs := map[string]*tar.Header{}
		bodies := map[string]string{}
		tr := tar.NewReader(&buf)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(tr)
			hdrs[h.Name], bodies[h.Name] = h, string(b)
		}
		return hdrs, bodies
	}

	first := makeTar(map[string]string{"notes/hello.txt": "hi"}, []string{"notes/", "notes/hello.txt"})
	if err := m.ImportData(ctx, spec, bytes.NewReader(first)); err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	got, bodies := export()
	hello, notes := got["notes/hello.txt"], got["notes/"]
	if hello == nil || bodies["notes/hello.txt"] != "hi" || notes == nil {
		t.Fatalf("exported entries = %v", got)
	}
	if hello.Uid != 65532 || notes.Uid != 65532 {
		t.Errorf("owner after import = %d / %d, want 65532", hello.Uid, notes.Uid)
	}

	// The volume root must be writable for the backend's user.
	id, err := rt.CreateContainer(ctx, ContainerSpec{
		Name:   "peek-itest002-root-check",
		Image:  image,
		Volume: VolumeName(instance, uploadID),
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	rootCheckGone := false
	removeRootCheck := func() {
		if !rootCheckGone {
			rootCheckGone = true
			_ = rt.RemoveContainer(context.Background(), id)
		}
	}
	t.Cleanup(removeRootCheck)
	rc, err := rt.CopyFromContainer(ctx, id, "/data")
	if err != nil {
		t.Fatal(err)
	}
	rootHdr, err := tar.NewReader(rc).Next()
	_ = rc.Close()
	if err != nil || rootHdr.Name != "data/" || rootHdr.Uid != 65532 {
		t.Fatalf("volume root header = %+v, %v", rootHdr, err)
	}
	removeRootCheck() // a container using the volume would block the replacing import

	second := makeTar(map[string]string{"other.txt": "x"}, []string{"other.txt"})
	if err := m.ImportData(ctx, spec, bytes.NewReader(second)); err != nil {
		t.Fatalf("second ImportData: %v", err)
	}
	got, _ = export()
	if got["other.txt"] == nil || got["notes/hello.txt"] != nil {
		t.Fatalf("exported entries after replace = %v", got)
	}

	left, err := rt.ListContainers(ctx, map[string]string{"peek.instance": instance})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("containers left: %+v", left)
	}
}

// deleteNetwork removes the test network with a raw DELETE /networks/{name};
// Runtime has no such method on purpose.
func deleteNetwork(t *testing.T, name string) {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", integrationSocket)
			},
		},
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequest(http.MethodDelete, "http://docker/networks/"+name, nil)
	if err != nil {
		t.Errorf("delete network: %v", err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("delete network %s: %v", name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("delete network %s: %d %s", name, resp.StatusCode, body)
	}
}
