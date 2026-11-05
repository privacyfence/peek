// Package backends runs page backend containers through a container engine.
package backends

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Runtime is the container engine Peek drives. DockerRuntime implements it for
// the Docker Engine API (and Podman's compatible API).
type Runtime interface {
	Ping(ctx context.Context) error
	ImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref, registryAuth string) error
	EnsureNetwork(ctx context.Context, name string) error
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) // returns the container id
	StartContainer(ctx context.Context, id string) error
	ContainerIP(ctx context.Context, id, network string) (string, error)
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	RemoveContainer(ctx context.Context, id string) error // force; a missing container is not an error
	ListContainers(ctx context.Context, labels map[string]string) ([]ContainerInfo, error)
	ContainerLogs(ctx context.Context, id string, tail int) ([]byte, error)
	RemoveVolume(ctx context.Context, name string) error                           // a missing volume is not an error
	CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, error) // tar stream of path, as GET /containers/{id}/archive sends it
	CopyToContainer(ctx context.Context, id, path string, archive io.Reader) error // extracts a tar into path, keeping each entry's uid/gid
}

// ContainerSpec is what the manager asks for. The runtime adds the fixed
// hardening; callers cannot change it.
type ContainerSpec struct {
	Name        string
	Image       string
	Env         []string // "KEY=value"
	Labels      map[string]string
	Network     string
	Volume      string // named volume mounted at /data
	MemoryBytes int64
	NanoCPUs    int64
	Cmd         []string // empty = the image's default; only tests set it
}

// ContainerInfo describes one container returned by ListContainers.
type ContainerInfo struct {
	ID     string
	Name   string // without the leading "/"
	Labels map[string]string
	State  string
}

// APIError is a non-2xx answer from the engine.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("docker api: %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is an engine 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404
}
