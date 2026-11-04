package backends

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// minDockerAPIVersion is the Engine API version Peek's requests are written for. An engine
	// that no longer accepts it (Docker Engine 29 requires at least 1.44) is spoken to in its own
	// minimum version instead; see apiVersion.
	minDockerAPIVersion = "1.43"

	maxLogBytes      = 1 << 20
	maxAPIErrorBytes = 64 << 10
	pidsLimit        = 256
)

// DockerRuntime drives the Docker Engine API (and Podman's compatible API)
// over a unix socket or TCP.
type DockerRuntime struct {
	client *http.Client

	mu      sync.Mutex
	version string // path prefix such as "v1.44", settled by apiVersion
}

var _ Runtime = (*DockerRuntime)(nil)

// NewDockerRuntime returns a runtime for host, which is unix:///path/to/socket
// or tcp://host:port. It does not dial.
func NewDockerRuntime(host string) (*DockerRuntime, error) {
	var network, addr string
	switch {
	case strings.HasPrefix(host, "unix://") && len(host) > len("unix://"):
		network, addr = "unix", strings.TrimPrefix(host, "unix://")
	case strings.HasPrefix(host, "tcp://") && len(host) > len("tcp://"):
		network, addr = "tcp", strings.TrimPrefix(host, "tcp://")
	default:
		return nil, fmt.Errorf("unsupported docker host %q (use unix:// or tcp://)", host)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &DockerRuntime{client: &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			MaxIdleConns:    4,
			IdleConnTimeout: 30 * time.Second,
		},
	}}, nil
}

// do sends one request and returns the response when its status is 2xx.
// Other statuses are returned as *APIError and the body is closed.
func (d *DockerRuntime) do(ctx context.Context, method, path string, query url.Values, body any, header http.Header) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	target := "http://docker/" + d.apiVersion(ctx) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp)
	}
	return resp, nil
}

// apiVersion returns the version prefix for API paths. The first call asks the engine's
// unversioned /version: requests then use minDockerAPIVersion, or the engine's MinAPIVersion
// when that is newer. An engine that answers without a usable version (an error status, or a
// socket proxy that does not forward /version) gets minDockerAPIVersion for good; when the
// engine cannot be reached at all, minDockerAPIVersion is used and the next call asks again.
func (d *DockerRuntime) apiVersion(ctx context.Context) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.version != "" {
		return d.version
	}
	v, answered := d.negotiateVersion(ctx)
	if answered {
		d.version = "v" + v
	}
	return "v" + v
}

// negotiateVersion asks the engine for its API versions. answered is false only when no
// HTTP response came back.
func (d *DockerRuntime) negotiateVersion(ctx context.Context) (version string, answered bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return minDockerAPIVersion, false
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return minDockerAPIVersion, false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAPIErrorBytes))
		return minDockerAPIVersion, true
	}
	var info struct {
		MinAPIVersion string `json:"MinAPIVersion"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIErrorBytes)).Decode(&info); err != nil {
		return minDockerAPIVersion, true
	}
	return pickAPIVersion(info.MinAPIVersion), true
}

// pickAPIVersion is minDockerAPIVersion, raised to engineMin when that is a newer version.
func pickAPIVersion(engineMin string) string {
	if newerAPIVersion(engineMin, minDockerAPIVersion) {
		return engineMin
	}
	return minDockerAPIVersion
}

// newerAPIVersion reports whether a is a valid "major.minor" version newer than b.
func newerAPIVersion(a, b string) bool {
	am, an, ok := parseAPIVersion(a)
	if !ok {
		return false
	}
	bm, bn, ok := parseAPIVersion(b)
	if !ok {
		return false
	}
	return am > bm || (am == bm && an > bn)
}

func parseAPIVersion(v string) (major, minor int, ok bool) {
	maj, min, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(maj)
	b, err2 := strconv.Atoi(min)
	if err1 != nil || err2 != nil || a < 0 || b < 0 {
		return 0, 0, false
	}
	return a, b, true
}

func decodeAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorBytes))
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &payload)
	msg := payload.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

// call runs a request whose response body is not needed.
func (d *DockerRuntime) call(ctx context.Context, method, path string, query url.Values, body any) error {
	resp, err := d.do(ctx, method, path, query, body, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// callIgnoring is call, treating the listed statuses as success.
func (d *DockerRuntime) callIgnoring(ctx context.Context, method, path string, query url.Values, status ...int) error {
	err := d.call(ctx, method, path, query, nil)
	if apiErr, ok := err.(*APIError); ok {
		for _, s := range status {
			if apiErr.Status == s {
				return nil
			}
		}
	}
	return err
}

func (d *DockerRuntime) Ping(ctx context.Context) error {
	return d.call(ctx, http.MethodGet, "/_ping", nil, nil)
}

func (d *DockerRuntime) ImageExists(ctx context.Context, ref string) (bool, error) {
	err := d.call(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil)
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (d *DockerRuntime) PullImage(ctx context.Context, ref, registryAuth string) error {
	query := url.Values{"fromImage": {Repository(ref)}, "tag": {pullTag(ref)}}
	var header http.Header
	if registryAuth != "" {
		header = http.Header{"X-Registry-Auth": {registryAuth}}
	}
	resp, err := d.do(ctx, http.MethodPost, "/images/create", query, nil, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Pull failures arrive inside a 200 stream of JSON objects.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, msg.Error)
		}
	}
}

// pullTag is the tag query value for ref: the digest, the tag, or latest.
func pullTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return "latest"
}

func (d *DockerRuntime) EnsureNetwork(ctx context.Context, name string) error {
	err := d.call(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil)
	if err == nil {
		return nil
	}
	if !IsNotFound(err) {
		return err
	}
	return d.call(ctx, http.MethodPost, "/networks/create", nil, map[string]any{
		"Name":   name,
		"Driver": "bridge",
		"Labels": map[string]string{"peek.managed": "true"},
	})
}

type createBody struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCpus       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Mounts         []mount           `json:"Mounts"`
	RestartPolicy  restartPolicy     `json:"RestartPolicy"`
	Init           bool              `json:"Init"`
	LogConfig      logConfig         `json:"LogConfig"`
}

type mount struct {
	Type   string `json:"Type"`
	Source string `json:"Source"`
	Target string `json:"Target"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type logConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

func (d *DockerRuntime) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	body := createBody{
		Image:  spec.Image,
		Env:    spec.Env,
		Labels: spec.Labels,
		Cmd:    spec.Cmd,
		HostConfig: hostConfig{
			NetworkMode:    spec.Network,
			Memory:         spec.MemoryBytes,
			MemorySwap:     spec.MemoryBytes,
			NanoCpus:       spec.NanoCPUs,
			PidsLimit:      pidsLimit,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": "rw,nosuid,size=64m"},
			Mounts:         []mount{{Type: "volume", Source: spec.Volume, Target: "/data"}},
			RestartPolicy:  restartPolicy{Name: "no"},
			Init:           true,
			LogConfig:      logConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
		},
	}
	resp, err := d.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {spec.Name}}, body, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}
	return out.ID, nil
}

func (d *DockerRuntime) StartContainer(ctx context.Context, id string) error {
	return d.callIgnoring(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, http.StatusNotModified)
}

func (d *DockerRuntime) ContainerIP(ctx context.Context, id, network string) (string, error) {
	resp, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode container: %w", err)
	}
	ip := out.NetworkSettings.Networks[network].IPAddress
	if ip == "" {
		return "", fmt.Errorf("container %s has no address on network %q", id, network)
	}
	return ip, nil
}

func (d *DockerRuntime) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(timeout.Seconds()))}}
	return d.callIgnoring(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, http.StatusNotModified, http.StatusNotFound)
}

func (d *DockerRuntime) RemoveContainer(ctx context.Context, id string) error {
	return d.callIgnoring(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}}, http.StatusNotFound)
}

func (d *DockerRuntime) ListContainers(ctx context.Context, labels map[string]string) ([]ContainerInfo, error) {
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	filters, err := json.Marshal(map[string][]string{"label": pairs})
	if err != nil {
		return nil, err
	}
	resp, err := d.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	out := make([]ContainerInfo, 0, len(raw))
	for _, c := range raw {
		info := ContainerInfo{ID: c.ID, Labels: c.Labels, State: c.State}
		if len(c.Names) > 0 {
			info.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, info)
	}
	return out, nil
}

func (d *DockerRuntime) ContainerLogs(ctx context.Context, id string, tail int) ([]byte, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	resp, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return demuxLogs(resp.Body, maxLogBytes)
}

// demuxLogs reads the engine's multiplexed log stream (8-byte header: byte 0
// is the stream, bytes 4-7 the big-endian payload length) into one slice of at
// most limit bytes.
func demuxLogs(r io.Reader, limit int) ([]byte, error) {
	var out []byte
	var header [8]byte
	for len(out) < limit {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return out, err
		}
		n := int64(binary.BigEndian.Uint32(header[4:]))
		take := min(n, int64(limit-len(out)))
		chunk := make([]byte, take)
		if got, err := io.ReadFull(r, chunk); err != nil {
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				out = append(out, chunk[:got]...)
				break
			}
			return out, err
		}
		out = append(out, chunk...)
		if take < n {
			break
		}
	}
	return out, nil
}

func (d *DockerRuntime) RemoveVolume(ctx context.Context, name string) error {
	return d.callIgnoring(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, http.StatusNotFound)
}
