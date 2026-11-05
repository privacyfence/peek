package backends

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	startTimeout = 5 * time.Minute
	stopTimeout  = 10 * time.Second
	// orphanStopTimeout bounds stopping a container whose start finished after
	// the caller that removed it from the manager gave up waiting.
	orphanStopTimeout = 30 * time.Second
)

// Package variables so tests can shorten them.
var (
	readinessTimeout  = 30 * time.Second
	readinessInterval = 200 * time.Millisecond
	reapInterval      = time.Minute
)

// ErrNotRunning is returned for a backend the manager is not running.
var ErrNotRunning = errors.New("backend is not running")

var errClosed = errors.New("backend manager is closed")

// Config configures a Manager.
type Config struct {
	Runtime  Runtime
	Network  string
	Instance string // 8 hex chars; scopes names, labels and cleanup to this peekd
	Limits   func(ctx context.Context) Limits
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error) // readiness probe; nil = (&net.Dialer{}).DialContext
	Now      func() time.Time                                                  // nil = time.Now
}

// Limits are the runtime-adjustable bounds on running backends. A zero
// IdleTimeout never stops idle backends; a zero MaxRunning has no cap.
type Limits struct {
	IdleTimeout time.Duration
	MaxRunning  int
}

// Spec is one page version's backend, resolved from settings by the server.
type Spec struct {
	UploadID     int64
	Version      int
	Image        string
	Port         int
	Env          map[string]string
	MemoryBytes  int64
	NanoCPUs     int64
	RegistryAuth string
}

// Endpoint is where a running backend listens and the token it expects.
type Endpoint struct {
	URL   string // "http://<ip>:<port>"
	Token string // value of PEEK_BACKEND_TOKEN in the container
}

// Manager starts page backend containers on demand, tracks them, and stops
// them when idle, evicted, replaced or closed. All state is under one mutex,
// and no Runtime call happens while it is held.
type Manager struct {
	rt       Runtime
	network  string
	instance string
	limits   func(ctx context.Context) Limits
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	onWait   func() // test hook: called before Ensure waits for a start in progress
	now      func() time.Time

	mu      sync.Mutex
	entries map[key]*entry
	closed  bool
	// dataBusy holds a channel, closed when the operation ends, for every page
	// whose /data volume is being exported or imported.
	dataBusy map[int64]chan struct{}

	// orphans tracks goroutines that stop a container whose start outlived
	// the caller that removed it; Close waits for them.
	orphans sync.WaitGroup
}

type key struct {
	uploadID int64
	version  int
}

type entry struct {
	ready     chan struct{} // closed when the start attempt finished
	err       error         // start error; read only after ready is closed
	id, image string
	endpoint  Endpoint
	lastUsed  time.Time
	key       key
}

// started reports whether e finished starting successfully. Callers hold
// m.mu, or have seen ready closed.
func (e *entry) started() bool {
	select {
	case <-e.ready:
		return e.err == nil
	default:
		return false
	}
}

// NewManager returns a Manager for cfg. Call Start before Ensure.
func NewManager(cfg Config) *Manager {
	m := &Manager{
		rt:       cfg.Runtime,
		network:  cfg.Network,
		instance: cfg.Instance,
		limits:   cfg.Limits,
		dial:     cfg.Dial,
		now:      cfg.Now,
		entries:  map[key]*entry{},
		dataBusy: map[int64]chan struct{}{},
	}
	if m.limits == nil {
		m.limits = func(context.Context) Limits { return Limits{} }
	}
	if m.dial == nil {
		m.dial = (&net.Dialer{}).DialContext
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// ContainerName is the container name of one page version's backend.
func ContainerName(instance string, uploadID int64, version int) string {
	return fmt.Sprintf("peek-%s-u%d-v%d", instance, uploadID, version)
}

// VolumeName is the /data volume shared by every version of a page.
func VolumeName(instance string, uploadID int64) string {
	return fmt.Sprintf("peek-%s-data-u%d", instance, uploadID)
}

// Start ensures the network exists and removes every container a previous run
// of this peekd instance left behind.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.rt.EnsureNetwork(ctx, m.network); err != nil {
		return fmt.Errorf("ensure network %s: %w", m.network, err)
	}
	leftovers, err := m.rt.ListContainers(ctx, map[string]string{"peek.managed": "true", "peek.instance": m.instance})
	if err != nil {
		return fmt.Errorf("list backend containers: %w", err)
	}
	for _, c := range leftovers {
		if err := m.rt.RemoveContainer(ctx, c.ID); err != nil {
			return fmt.Errorf("remove leftover backend %s: %w", c.Name, err)
		}
		slog.Info("backend removed", "reason", "leftover", "container", c.Name)
	}
	return nil
}

// Ensure returns the endpoint of spec's backend, starting its container when
// it is not running. Concurrent calls for one page version share one start; a
// changed image replaces the running container.
func (m *Manager) Ensure(ctx context.Context, spec Spec) (Endpoint, error) {
	k := key{spec.UploadID, spec.Version}
	limits := m.limits(ctx)
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return Endpoint{}, errClosed
		}
		if m.dataBusy[spec.UploadID] != nil {
			m.mu.Unlock()
			return Endpoint{}, ErrDataBusy
		}
		e := m.entries[k]
		if e != nil && e.image != spec.Image {
			delete(m.entries, k)
			m.mu.Unlock()
			if err := m.stopWhenReady(ctx, e); err != nil {
				return Endpoint{}, err
			}
			continue
		}
		if e != nil {
			m.mu.Unlock()
			if m.onWait != nil {
				m.onWait()
			}
			select {
			case <-e.ready:
			case <-ctx.Done():
				return Endpoint{}, ctx.Err()
			}
			if e.err != nil {
				return Endpoint{}, e.err
			}
			m.mu.Lock()
			e.lastUsed = m.now()
			m.mu.Unlock()
			return e.endpoint, nil
		}

		evicted := m.evictLocked(limits.MaxRunning)
		e = &entry{ready: make(chan struct{}), image: spec.Image, key: k}
		m.entries[k] = e
		m.mu.Unlock()

		startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), startTimeout)
		for _, old := range evicted {
			m.stopAndRemove(startCtx, old, "evicted")
		}
		id, endpoint, err := m.start(startCtx, spec)
		cancel()

		m.mu.Lock()
		if err != nil {
			if m.entries[k] == e {
				delete(m.entries, k)
			}
			e.err = err
			close(e.ready)
			m.mu.Unlock()
			return Endpoint{}, err
		}
		e.id, e.endpoint, e.lastUsed = id, endpoint, m.now()
		close(e.ready)
		m.mu.Unlock()
		slog.Info("backend started", "upload_id", spec.UploadID, "version", spec.Version, "image", spec.Image)
		return endpoint, nil
	}
}

// evictLocked removes least recently used started entries from the map until
// one more fits under maxRunning, and returns them for stopping. Entries
// still starting are counted but never evicted. The caller holds m.mu.
func (m *Manager) evictLocked(maxRunning int) []*entry {
	var evicted []*entry
	for maxRunning > 0 && len(m.entries) >= maxRunning {
		var lru *entry
		for _, e := range m.entries {
			if e.started() && (lru == nil || e.lastUsed.Before(lru.lastUsed)) {
				lru = e
			}
		}
		if lru == nil {
			break
		}
		delete(m.entries, lru.key)
		evicted = append(evicted, lru)
	}
	return evicted
}

// start pulls, creates, starts and probes one backend container. On a failure
// after the container was created, it removes the container.
func (m *Manager) start(ctx context.Context, spec Spec) (string, Endpoint, error) {
	if err := m.ensureImage(ctx, spec.Image, spec.RegistryAuth); err != nil {
		return "", Endpoint{}, err
	}
	name := ContainerName(m.instance, spec.UploadID, spec.Version)
	if err := m.rt.RemoveContainer(ctx, name); err != nil {
		return "", Endpoint{}, fmt.Errorf("remove stale container %s: %w", name, err)
	}
	token, err := newToken()
	if err != nil {
		return "", Endpoint{}, err
	}
	uploadID, version := strconv.FormatInt(spec.UploadID, 10), strconv.Itoa(spec.Version)
	id, err := m.rt.CreateContainer(ctx, ContainerSpec{
		Name:  name,
		Image: spec.Image,
		Env: append(sortedEnv(spec.Env),
			"PORT="+strconv.Itoa(spec.Port),
			"PEEK_BACKEND_TOKEN="+token,
			"PEEK_UPLOAD_ID="+uploadID,
			"PEEK_VERSION="+version,
		),
		Labels: map[string]string{
			"peek.managed":   "true",
			"peek.instance":  m.instance,
			"peek.upload_id": uploadID,
			"peek.version":   version,
		},
		Network:     m.network,
		Volume:      VolumeName(m.instance, spec.UploadID),
		MemoryBytes: spec.MemoryBytes,
		NanoCPUs:    spec.NanoCPUs,
	})
	if err != nil {
		return "", Endpoint{}, fmt.Errorf("create container %s: %w", name, err)
	}
	ip, err := m.run(ctx, id, spec.Port)
	if err != nil {
		if rmErr := m.rt.RemoveContainer(ctx, id); rmErr != nil {
			slog.Warn("backend stop", "upload_id", spec.UploadID, "version", spec.Version, "container", id, "err", rmErr)
		}
		return "", Endpoint{}, err
	}
	return id, Endpoint{URL: "http://" + net.JoinHostPort(ip, strconv.Itoa(spec.Port)), Token: token}, nil
}

// ensureImage pulls image when the engine does not have it.
func (m *Manager) ensureImage(ctx context.Context, image, registryAuth string) error {
	exists, err := m.rt.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	if !exists {
		return m.rt.PullImage(ctx, image, registryAuth)
	}
	return nil
}

// run starts a created container and waits until it accepts connections on
// port, returning its address on the backend network.
func (m *Manager) run(ctx context.Context, id string, port int) (string, error) {
	if err := m.rt.StartContainer(ctx, id); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}
	ip, err := m.rt.ContainerIP(ctx, id, m.network)
	if err != nil {
		return "", err
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	deadline := time.Now().Add(readinessTimeout)
	for {
		dialCtx, cancel := context.WithDeadline(ctx, deadline)
		conn, err := m.dial(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			return ip, nil
		}
		wait := time.Until(deadline)
		if wait <= 0 || ctx.Err() != nil {
			return "", fmt.Errorf("backend did not listen on port %d within %s", port, readinessTimeout)
		}
		timer := time.NewTimer(min(wait, readinessInterval))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
}

// StopVersion stops one page version's backend.
func (m *Manager) StopVersion(ctx context.Context, uploadID int64, version int) error {
	return m.stopMatching(ctx, "stopped", func(k key) bool { return k == key{uploadID, version} })
}

// StopUpload stops the backends of every version of a page.
func (m *Manager) StopUpload(ctx context.Context, uploadID int64) error {
	return m.stopMatching(ctx, "stopped", func(k key) bool { return k.uploadID == uploadID })
}

// RemoveUploadData stops a page's backends and deletes its /data volume. It
// waits for a running data export or import of the page first, because the
// helper container of that operation still mounts the volume.
func (m *Manager) RemoveUploadData(ctx context.Context, uploadID int64) error {
	release, err := m.waitData(ctx, uploadID)
	if err != nil {
		return err
	}
	defer release()
	if err := m.StopUpload(ctx, uploadID); err != nil {
		return err
	}
	if err := m.rt.RemoveContainer(ctx, DataHelperName(m.instance, uploadID)); err != nil {
		return fmt.Errorf("remove data helper: %w", err)
	}
	return m.rt.RemoveVolume(ctx, VolumeName(m.instance, uploadID))
}

// Logs returns the last tail log lines of a running backend, or ErrNotRunning.
func (m *Manager) Logs(ctx context.Context, uploadID int64, version int, tail int) ([]byte, error) {
	m.mu.Lock()
	e := m.entries[key{uploadID, version}]
	running := e != nil && e.started()
	m.mu.Unlock()
	if !running {
		return nil, ErrNotRunning
	}
	return m.rt.ContainerLogs(ctx, e.id, tail)
}

// Running reports whether a page version's backend has started and is tracked.
func (m *Manager) Running(uploadID int64, version int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key{uploadID, version}]
	return e != nil && e.started()
}

// ReapIdle stops started backends that have had no request for longer than
// the idle timeout.
func (m *Manager) ReapIdle(ctx context.Context) {
	idle := m.limits(ctx).IdleTimeout
	if idle <= 0 {
		return
	}
	m.mu.Lock()
	now := m.now()
	var reaped []*entry
	for k, e := range m.entries {
		if e.started() && now.Sub(e.lastUsed) > idle {
			delete(m.entries, k)
			reaped = append(reaped, e)
		}
	}
	m.mu.Unlock()
	for _, e := range reaped {
		m.stopAndRemove(ctx, e, "idle")
	}
}

// Run calls ReapIdle every minute until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.ReapIdle(ctx)
		}
	}
}

// Close stops and removes every tracked container. Ensure fails afterwards.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	if err := m.stopMatching(ctx, "shutdown", func(key) bool { return true }); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		m.orphans.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopMatching removes the matching entries from the map, then stops them
// outside the lock, waiting for those still starting.
func (m *Manager) stopMatching(ctx context.Context, reason string, match func(key) bool) error {
	m.mu.Lock()
	var stopping []*entry
	for k, e := range m.entries {
		if match(k) {
			delete(m.entries, k)
			stopping = append(stopping, e)
		}
	}
	m.mu.Unlock()
	var firstErr error
	for _, e := range stopping {
		if firstErr != nil {
			m.stopOrphan(e, reason)
			continue
		}
		select {
		case <-e.ready:
			if e.err == nil {
				m.stopAndRemove(ctx, e, reason)
			}
		case <-ctx.Done():
			firstErr = ctx.Err()
			m.stopOrphan(e, reason)
		}
	}
	return firstErr
}

// stopWhenReady waits for an entry already removed from the map to finish
// starting and stops it. When ctx ends first, the stop is handed to a
// goroutine Close waits for, so the container is not leaked.
func (m *Manager) stopWhenReady(ctx context.Context, e *entry) error {
	select {
	case <-e.ready:
		if e.err == nil {
			// Detached so a viewer closing the tab does not leave the old
			// container running.
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orphanStopTimeout)
			m.stopAndRemove(stopCtx, e, "replaced")
			cancel()
		}
		return nil
	case <-ctx.Done():
		m.stopOrphan(e, "replaced")
		return ctx.Err()
	}
}

// stopOrphan stops e in the background once its start finishes.
func (m *Manager) stopOrphan(e *entry, reason string) {
	m.orphans.Add(1)
	go func() {
		defer m.orphans.Done()
		<-e.ready
		if e.err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), orphanStopTimeout)
		defer cancel()
		m.stopAndRemove(ctx, e, reason)
	}()
}

// stopAndRemove stops and removes a started entry's container, logging
// failures. e is no longer in the map and its ready channel is closed.
func (m *Manager) stopAndRemove(ctx context.Context, e *entry, reason string) {
	attrs := []any{"upload_id", e.key.uploadID, "version", e.key.version, "container", e.id}
	if err := m.rt.StopContainer(ctx, e.id, stopTimeout); err != nil {
		slog.Warn("backend stop", append(attrs, "err", err)...)
	}
	if err := m.rt.RemoveContainer(ctx, e.id); err != nil {
		slog.Warn("backend stop", append(attrs, "err", err)...)
	}
	slog.Info("backend stopped", append([]any{"reason", reason}, attrs...)...)
}

// sortedEnv returns spec env as sorted KEY=value pairs. PORT and PEEK_* are
// set by the manager and dropped here.
func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env)+4)
	for k, v := range env {
		if k == "PORT" || strings.HasPrefix(k, "PEEK_") {
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate backend token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
