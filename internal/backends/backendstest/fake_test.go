package backendstest

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/privacyfence/peek/internal/backends"
)

func TestFakeRuntimeContainerLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFakeRuntime()

	if err := f.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.EnsureNetwork(ctx, "net"); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.ImageExists(ctx, "alpine"); ok || err != nil {
		t.Fatalf("ImageExists before pull = %v, %v", ok, err)
	}
	if err := f.PullImage(ctx, "alpine", "auth"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.ImageExists(ctx, "alpine"); !ok {
		t.Fatal("ImageExists after pull = false")
	}

	spec := backends.ContainerSpec{Name: "web", Image: "alpine", Env: []string{"A=1"}, Labels: map[string]string{"app": "x", "tier": "web"}, Cmd: []string{"sleep"}}
	id, err := f.CreateContainer(ctx, spec)
	if err != nil || id != "c1" {
		t.Fatalf("CreateContainer = %q, %v", id, err)
	}
	spec.Labels["app"] = "changed"
	spec.Env[0] = "A=2"
	if got, _ := f.Spec("web"); got.Labels["app"] != "x" || got.Env[0] != "A=1" {
		t.Fatalf("stored spec shares caller memory: %+v", got)
	}
	if _, err := f.CreateContainer(ctx, backends.ContainerSpec{Name: "web"}); err == nil {
		t.Fatal("CreateContainer with a taken name succeeded")
	}
	id2, err := f.CreateContainer(ctx, backends.ContainerSpec{Name: "db", Labels: map[string]string{"app": "x"}})
	if err != nil || id2 != "c2" {
		t.Fatalf("second CreateContainer = %q, %v", id2, err)
	}
	if info, ok := f.Container(id); !ok || info.State != "created" || info.Name != "web" {
		t.Fatalf("Container(c1) = %+v, %v", info, ok)
	}

	if err := f.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.Container("web"); info.State != "running" {
		t.Fatalf("state after start = %q", info.State)
	}
	if ip, err := f.ContainerIP(ctx, id, "net"); ip != "127.0.0.1" || err != nil {
		t.Fatalf("ContainerIP = %q, %v", ip, err)
	}

	list, err := f.ListContainers(ctx, map[string]string{"app": "x"})
	if err != nil || len(list) != 2 {
		t.Fatalf("ListContainers(app=x) = %v, %v", list, err)
	}
	list, _ = f.ListContainers(ctx, map[string]string{"app": "x", "tier": "web"})
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListContainers(app=x,tier=web) = %v", list)
	}
	if list, _ := f.ListContainers(ctx, map[string]string{"app": "y"}); len(list) != 0 {
		t.Fatalf("ListContainers(app=y) = %v", list)
	}

	f.LogsData = []byte("hello\n")
	logs, err := f.ContainerLogs(ctx, id, 10)
	if err != nil || string(logs) != "hello\n" {
		t.Fatalf("ContainerLogs = %q, %v", logs, err)
	}

	if err := f.StopContainer(ctx, id, time.Second); err != nil {
		t.Fatal(err)
	}
	if info, _ := f.Container(id); info.State != "exited" {
		t.Fatalf("state after stop = %q", info.State)
	}
	if err := f.StopContainer(ctx, "missing", time.Second); err != nil {
		t.Fatalf("StopContainer(missing) = %v", err)
	}
	if err := f.RemoveContainer(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveContainer(ctx, "missing"); err != nil {
		t.Fatalf("RemoveContainer(missing) = %v", err)
	}
	if _, ok := f.Container(id); ok {
		t.Fatal("container still present after remove")
	}
	if _, ok := f.Spec(id); ok {
		t.Fatal("Spec still present after remove")
	}
	if err := f.RemoveVolume(ctx, "vol"); err != nil {
		t.Fatal(err)
	}

	for _, err := range []error{
		f.StartContainer(ctx, id),
		func() error { _, err := f.ContainerIP(ctx, id, "net"); return err }(),
		func() error { _, err := f.ContainerLogs(ctx, id, 1); return err }(),
	} {
		if !backends.IsNotFound(err) {
			t.Errorf("call on removed container = %v, want 404", err)
		}
	}

	f.Lock()
	defer f.Unlock()
	checks := []struct {
		name      string
		got, want []string
	}{
		{"Networks", f.Networks, []string{"net"}},
		{"Pulled", f.Pulled, []string{"alpine"}},
		{"Started", f.Started, []string{"c1", "c1"}},
		{"Stopped", f.Stopped, []string{"c1", "missing"}},
		{"Removed", f.Removed, []string{"web", "missing"}},
		{"VolumesRemoved", f.VolumesRemoved, []string{"vol"}},
	}
	for _, c := range checks {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(f.Created) != 2 || f.Created[0].Name != "web" || f.Created[1].Name != "db" {
		t.Errorf("Created = %+v", f.Created)
	}
}

func TestFakeRuntimeConfiguredErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	f := NewFakeRuntime()
	f.PingErr, f.NetworkErr, f.PullErr, f.CreateErr, f.ListErr = boom, boom, boom, boom, boom

	if err := f.Ping(ctx); !errors.Is(err, boom) {
		t.Errorf("Ping = %v", err)
	}
	if err := f.EnsureNetwork(ctx, "n"); !errors.Is(err, boom) {
		t.Errorf("EnsureNetwork = %v", err)
	}
	if err := f.PullImage(ctx, "alpine", ""); !errors.Is(err, boom) {
		t.Errorf("PullImage = %v", err)
	}
	if ok, _ := f.ImageExists(ctx, "alpine"); ok {
		t.Error("failed pull marked the image present")
	}
	if _, err := f.CreateContainer(ctx, backends.ContainerSpec{Name: "x"}); !errors.Is(err, boom) {
		t.Errorf("CreateContainer = %v", err)
	}
	if _, err := f.ListContainers(ctx, nil); !errors.Is(err, boom) {
		t.Errorf("ListContainers = %v", err)
	}
	if len(f.Created) != 0 || len(f.Pulled) != 1 {
		t.Errorf("Created = %v, Pulled = %v", f.Created, f.Pulled)
	}

	f.CreateErr = nil
	id, _ := f.CreateContainer(ctx, backends.ContainerSpec{Name: "x"})
	f.StartErr = boom
	if err := f.StartContainer(ctx, id); !errors.Is(err, boom) {
		t.Errorf("StartContainer = %v", err)
	}
	if info, _ := f.Container(id); info.State != "created" {
		t.Errorf("state after failed start = %q", info.State)
	}
}

func TestFakeRuntimeStartHook(t *testing.T) {
	ctx := context.Background()
	f := NewFakeRuntime()
	id, _ := f.CreateContainer(ctx, backends.ContainerSpec{Name: "x"})
	hookErr := errors.New("hook")
	var seen string
	f.StartHook = func(_ context.Context, got string) error {
		seen = got
		return hookErr
	}
	if err := f.StartContainer(ctx, id); !errors.Is(err, hookErr) {
		t.Fatalf("StartContainer = %v", err)
	}
	if seen != id || len(f.Started) != 0 {
		t.Fatalf("hook saw %q, Started = %v", seen, f.Started)
	}
	f.StartHook = func(context.Context, string) error { return nil }
	if err := f.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestFakeRuntimeAddContainer(t *testing.T) {
	ctx := context.Background()
	f := &FakeRuntime{}
	id := f.AddContainer(backends.ContainerInfo{Name: "left", Labels: map[string]string{"peek.instance": "a"}})
	if id != "c1" {
		t.Fatalf("AddContainer id = %q", id)
	}
	if got := f.AddContainer(backends.ContainerInfo{ID: "c2", Name: "named", State: "exited"}); got != "c2" {
		t.Fatalf("AddContainer kept id = %q", got)
	}
	created, err := f.CreateContainer(ctx, backends.ContainerSpec{Name: "new"})
	if err != nil || created != "c3" {
		t.Fatalf("CreateContainer after AddContainer = %q, %v", created, err)
	}
	if err := f.PullImage(ctx, "alpine", ""); err != nil {
		t.Fatal(err)
	}

	all := f.Containers()
	if len(all) != 3 {
		t.Fatalf("Containers = %v", all)
	}
	all[0].Name = "mutated"
	if info, _ := f.Container("c1"); info.Name != "left" || info.State != "running" {
		t.Fatalf("Container(c1) = %+v", info)
	}
	list, _ := f.ListContainers(ctx, map[string]string{"peek.instance": "a"})
	if len(list) != 1 || list[0].ID != "c1" {
		t.Fatalf("ListContainers = %v", list)
	}
}

func TestFakeArchive(t *testing.T) {
	ctx := context.Background()
	f := NewFakeRuntime()
	f.DataUID, f.DataGID = 1000, 1001

	if _, err := f.CopyFromContainer(ctx, "nope", "/data"); !backends.IsNotFound(err) {
		t.Fatalf("CopyFromContainer unknown = %v", err)
	}
	if err := f.CopyToContainer(ctx, "nope", "/data", strings.NewReader("")); !backends.IsNotFound(err) {
		t.Fatalf("CopyToContainer unknown = %v", err)
	}

	id, err := f.CreateContainer(ctx, backends.ContainerSpec{Name: "helper", Volume: "vol"})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := f.CopyFromContainer(ctx, id, "/data")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(rc)
	h, err := tr.Next()
	if err != nil || h.Name != "data/" || h.Typeflag != tar.TypeDir || h.Uid != 1000 || h.Gid != 1001 {
		t.Fatalf("default entry = %+v, %v", h, err)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("second entry err = %v", err)
	}
	_ = rc.Close()

	f.Volumes["vol"] = []byte("custom")
	rc, _ = f.CopyFromContainer(ctx, id, "/data")
	if got, _ := io.ReadAll(rc); string(got) != "custom" {
		t.Fatalf("volume stream = %q", got)
	}

	if err := f.CopyToContainer(ctx, id, "/data", strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	if string(f.Imported["vol"]) != "payload" {
		t.Fatalf("Imported = %q", f.Imported["vol"])
	}

	f.ArchiveErr = errors.New("boom")
	if _, err := f.CopyFromContainer(ctx, id, "/data"); err == nil {
		t.Error("CopyFromContainer ignored ArchiveErr")
	}
	if err := f.CopyToContainer(ctx, id, "/data", strings.NewReader("")); err == nil {
		t.Error("CopyToContainer ignored ArchiveErr")
	}
	f.ArchiveErr = nil

	_ = f.RemoveVolume(ctx, "vol")
	if _, ok := f.Volumes["vol"]; ok {
		t.Error("RemoveVolume kept the volume contents")
	}
	_ = f.RemoveContainer(ctx, "helper")

	want := []string{
		"create helper",
		"copyfrom c1 /data",
		"copyfrom c1 /data",
		"copyto c1 /data",
		"copyfrom c1 /data",
		"copyto c1 /data",
		"rmvol vol",
		"rmctr helper",
	}
	// the unknown-container calls come first
	got := f.Calls
	if len(got) < len(want)+2 || !slices.Equal(got[2:], want) {
		t.Fatalf("Calls = %v", got)
	}
}
