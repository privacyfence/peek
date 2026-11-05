// Package backendstest provides an in-memory backends.Runtime for tests.
package backendstest

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/privacyfence/peek/internal/backends"
)

// FakeRuntime is an in-memory, mutex-guarded backends.Runtime. Containers live
// in a map; ContainerIP always answers "127.0.0.1", so a test points a backend
// Port at an httptest.Server it started.
//
// The exported fields record calls and configure failures. Set them before the
// code under test runs, or read and write them while holding the runtime
// through Lock and Unlock.
type FakeRuntime struct {
	mu sync.Mutex

	// Images answers ImageExists; a successful PullImage adds the ref.
	Images map[string]bool
	// Networks lists every name passed to EnsureNetwork.
	Networks []string
	// Pulled lists every ref passed to PullImage, including failed pulls.
	Pulled []string
	// Created lists the spec of every successful CreateContainer.
	Created []backends.ContainerSpec
	// Started, Stopped and Removed list the ids (or names) passed to
	// StartContainer, StopContainer and RemoveContainer.
	Started []string
	Stopped []string
	Removed []string
	// VolumesRemoved lists every name passed to RemoveVolume.
	VolumesRemoved []string
	// Volumes holds, per volume name, the tar stream CopyFromContainer returns
	// for a container mounting that volume, entries prefixed "data/" as the
	// Engine sends them. A volume without an entry yields a tar with a single
	// "data/" directory entry owned by DataUID/DataGID.
	Volumes map[string][]byte
	// DataUID and DataGID own the default "data/" entry.
	DataUID, DataGID int
	// Imported holds, per volume name, the bytes the last CopyToContainer received.
	Imported map[string][]byte
	// Calls lists every runtime call in order: "create <name>", "start <id>",
	// "stop <id>", "rmctr <id-or-name>", "rmvol <name>", "copyfrom <id> <path>"
	// and "copyto <id> <path>".
	Calls []string
	// LogsData is what ContainerLogs returns for an existing container.
	LogsData []byte

	PingErr    error
	NetworkErr error
	PullErr    error
	CreateErr  error
	StartErr   error
	ListErr    error
	// ArchiveErr fails CopyFromContainer and CopyToContainer.
	ArchiveErr error

	// StartHook, when set, runs at the start of StartContainer without the
	// runtime's lock held. A test can block in it to hold a start open, and a
	// non-nil result fails the start.
	StartHook func(ctx context.Context, id string) error

	containers map[string]*fakeContainer
	nextID     int
}

type fakeContainer struct {
	info backends.ContainerInfo
	spec backends.ContainerSpec
}

var _ backends.Runtime = (*FakeRuntime)(nil)

// NewFakeRuntime returns an empty FakeRuntime with no images.
func NewFakeRuntime() *FakeRuntime {
	return &FakeRuntime{
		Images:     map[string]bool{},
		Volumes:    map[string][]byte{},
		Imported:   map[string][]byte{},
		containers: map[string]*fakeContainer{},
	}
}

// Lock locks the runtime so a test can read or change the exported fields
// while the code under test may be calling it.
func (f *FakeRuntime) Lock() { f.mu.Lock() }

// Unlock undoes Lock.
func (f *FakeRuntime) Unlock() { f.mu.Unlock() }

// AddContainer registers a container that exists before the code under test
// runs, such as a leftover from a crashed process. An empty ID gets the next
// generated one; the id is returned.
func (f *FakeRuntime) AddContainer(info backends.ContainerInfo) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.containers == nil {
		f.containers = map[string]*fakeContainer{}
	}
	if info.ID == "" {
		info.ID = f.newID()
	}
	if info.State == "" {
		info.State = "running"
	}
	f.containers[info.ID] = &fakeContainer{info: copyInfo(info)}
	return info.ID
}

// Containers returns a copy of every container the runtime holds.
func (f *FakeRuntime) Containers() []backends.ContainerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backends.ContainerInfo, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, copyInfo(c.info))
	}
	return out
}

// Container returns the container with the given id or name.
func (f *FakeRuntime) Container(idOrName string) (backends.ContainerInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(idOrName)
	if c == nil {
		return backends.ContainerInfo{}, false
	}
	return copyInfo(c.info), true
}

// Ping returns PingErr.
func (f *FakeRuntime) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.PingErr
}

// ImageExists reports Images[ref].
func (f *FakeRuntime) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Images[ref], nil
}

// PullImage records ref and, unless PullErr is set, marks the image present.
func (f *FakeRuntime) PullImage(_ context.Context, ref, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Pulled = append(f.Pulled, ref)
	if f.PullErr != nil {
		return f.PullErr
	}
	if f.Images == nil {
		f.Images = map[string]bool{}
	}
	f.Images[ref] = true
	return nil
}

// EnsureNetwork records name and returns NetworkErr.
func (f *FakeRuntime) EnsureNetwork(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks = append(f.Networks, name)
	return f.NetworkErr
}

// CreateContainer stores a "created" container with id "c1", "c2", ... and
// records its spec. A name already in use is a 409 like the Engine's.
func (f *FakeRuntime) CreateContainer(_ context.Context, spec backends.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "create "+spec.Name)
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	if spec.Name != "" && f.find(spec.Name) != nil {
		return "", &backends.APIError{Status: http.StatusConflict, Message: fmt.Sprintf("container name %q is already in use", spec.Name)}
	}
	if f.containers == nil {
		f.containers = map[string]*fakeContainer{}
	}
	id := f.newID()
	spec = copySpec(spec)
	f.containers[id] = &fakeContainer{
		info: copyInfo(backends.ContainerInfo{ID: id, Name: spec.Name, Labels: spec.Labels, State: "created"}),
		spec: spec,
	}
	f.Created = append(f.Created, spec)
	return id, nil
}

// StartContainer runs StartHook, then marks the container "running". It
// returns StartErr, or a 404 for an unknown container.
func (f *FakeRuntime) StartContainer(ctx context.Context, id string) error {
	f.mu.Lock()
	hook := f.StartHook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, id); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Started = append(f.Started, id)
	f.Calls = append(f.Calls, "start "+id)
	if f.StartErr != nil {
		return f.StartErr
	}
	c := f.find(id)
	if c == nil {
		return notFound("container", id)
	}
	c.info.State = "running"
	return nil
}

// ContainerIP returns "127.0.0.1" for a known container.
func (f *FakeRuntime) ContainerIP(_ context.Context, id, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.find(id) == nil {
		return "", notFound("container", id)
	}
	return "127.0.0.1", nil
}

// StopContainer records id and marks a known container "exited"; an unknown
// one is not an error, as with the Engine.
func (f *FakeRuntime) StopContainer(_ context.Context, id string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Stopped = append(f.Stopped, id)
	f.Calls = append(f.Calls, "stop "+id)
	if c := f.find(id); c != nil {
		c.info.State = "exited"
	}
	return nil
}

// RemoveContainer records id and deletes the container with that id or name;
// an unknown one is not an error.
func (f *FakeRuntime) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Removed = append(f.Removed, id)
	f.Calls = append(f.Calls, "rmctr "+id)
	if c := f.find(id); c != nil {
		delete(f.containers, c.info.ID)
	}
	return nil
}

// ListContainers returns the containers that carry every given label.
func (f *FakeRuntime) ListContainers(_ context.Context, labels map[string]string) ([]backends.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	var out []backends.ContainerInfo
	for _, c := range f.containers {
		if hasLabels(c.info.Labels, labels) {
			out = append(out, copyInfo(c.info))
		}
	}
	return out, nil
}

// ContainerLogs returns a copy of LogsData for a known container.
func (f *FakeRuntime) ContainerLogs(_ context.Context, id string, _ int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.find(id) == nil {
		return nil, notFound("container", id)
	}
	return append([]byte(nil), f.LogsData...), nil
}

// RemoveVolume records name and forgets the volume's contents.
func (f *FakeRuntime) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.VolumesRemoved = append(f.VolumesRemoved, name)
	f.Calls = append(f.Calls, "rmvol "+name)
	delete(f.Volumes, name)
	return nil
}

// CopyFromContainer returns the tar stream of the volume the container mounts.
func (f *FakeRuntime) CopyFromContainer(_ context.Context, id, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "copyfrom "+id+" "+path)
	if f.ArchiveErr != nil {
		return nil, f.ArchiveErr
	}
	c := f.find(id)
	if c == nil {
		return nil, notFound("container", id)
	}
	if data, ok := f.Volumes[c.spec.Volume]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "data/", Mode: 0o755, Uid: f.DataUID, Gid: f.DataGID})
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

// CopyToContainer stores the archive bytes in Imported for the container's volume.
func (f *FakeRuntime) CopyToContainer(_ context.Context, id, path string, archive io.Reader) error {
	f.mu.Lock()
	f.Calls = append(f.Calls, "copyto "+id+" "+path)
	err, c := f.ArchiveErr, f.find(id)
	var volume string
	if c != nil {
		volume = c.spec.Volume
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if c == nil {
		return notFound("container", id)
	}
	data, err := io.ReadAll(archive)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Imported == nil {
		f.Imported = map[string][]byte{}
	}
	f.Imported[volume] = data
	return nil
}

// Spec returns the create spec of the container with the given id or name.
func (f *FakeRuntime) Spec(idOrName string) (backends.ContainerSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(idOrName)
	if c == nil {
		return backends.ContainerSpec{}, false
	}
	return copySpec(c.spec), true
}

func (f *FakeRuntime) newID() string {
	for {
		f.nextID++
		id := fmt.Sprintf("c%d", f.nextID)
		if _, taken := f.containers[id]; !taken {
			return id
		}
	}
}

func (f *FakeRuntime) find(idOrName string) *fakeContainer {
	if c, ok := f.containers[idOrName]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.info.Name != "" && c.info.Name == idOrName {
			return c
		}
	}
	return nil
}

func hasLabels(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func notFound(kind, id string) error {
	return &backends.APIError{Status: http.StatusNotFound, Message: fmt.Sprintf("no such %s: %s", kind, id)}
}

func copyInfo(info backends.ContainerInfo) backends.ContainerInfo {
	info.Labels = maps.Clone(info.Labels)
	return info
}

func copySpec(spec backends.ContainerSpec) backends.ContainerSpec {
	spec.Env = append([]string(nil), spec.Env...)
	spec.Cmd = append([]string(nil), spec.Cmd...)
	spec.Labels = maps.Clone(spec.Labels)
	return spec
}
