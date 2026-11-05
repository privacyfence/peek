package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

const backendUsage = "usage: peek backend <slug> | set|remove|env|restart|logs|call|token-calls|data ..."

// backendVersionSegment validates a --version value (a positive integer or
// "latest") for use as one URL path segment.
func backendVersionSegment(v string) (string, error) {
	if v == "" || v == "latest" {
		return "latest", nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("invalid version %q: use a positive number or latest", v)
	}
	return strconv.Itoa(n), nil
}

func parsePort(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return n, nil
}

// backendFlags holds the flags shared by the backend subcommands.
type backendFlags struct {
	version    string
	port       int
	tail       int
	value      string
	valueSet   bool
	valueStdin bool
	pos        []string

	allowTokenCalls bool
	method          string
	data            string
	dataSet         bool
	headers         []string
	output          string
	yes             bool
}

func parseBackendFlags(args []string, allowed ...string) (backendFlags, error) {
	f := backendFlags{version: "latest"}
	ok := func(flag string) bool {
		for _, a := range allowed {
			if a == flag {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			f.pos = append(f.pos, a)
			continue
		}
		if !ok(a) {
			return f, fmt.Errorf("unknown flag: %s", a)
		}
		if a == "--value-stdin" {
			f.valueStdin = true
			continue
		}
		if a == "--yes" {
			f.yes = true
			continue
		}
		if a == "--allow-token-calls" {
			f.allowTokenCalls = true
			continue
		}
		if i+1 >= len(args) {
			return f, fmt.Errorf("%s requires a value", a)
		}
		v := args[i+1]
		i++
		switch a {
		case "--version":
			f.version = v
		case "--port":
			n, err := parsePort(v)
			if err != nil {
				return f, err
			}
			f.port = n
		case "--tail":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 2000 {
				return f, fmt.Errorf("--tail must be between 1 and 2000")
			}
			f.tail = n
		case "--value":
			f.value = v
			f.valueSet = true
		case "--method":
			f.method = v
		case "--data":
			f.data = v
			f.dataSet = true
		case "-o":
			f.output = v
		case "-H":
			f.headers = append(f.headers, v)
		}
	}
	return f, nil
}

func cmdBackend(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", backendUsage)
	}
	switch args[0] {
	case "set":
		return cmdBackendSet(args[1:])
	case "remove":
		return cmdBackendRemove(args[1:])
	case "env":
		return cmdBackendEnv(args[1:])
	case "restart":
		return cmdBackendRestart(args[1:])
	case "logs":
		return cmdBackendLogs(args[1:])
	case "call":
		return cmdBackendCall(args[1:])
	case "token-calls":
		return cmdBackendTokenCalls(args[1:])
	case "data":
		return cmdBackendData(args[1:])
	}
	return cmdBackendShow(args)
}

// backendClient resolves the slug segment and builds the API client.
func backendClient(slug string) (string, *client, error) {
	seg, err := slugPath(slug)
	if err != nil {
		return "", nil, err
	}
	cfg, err := LoadConfig()
	if err != nil {
		return "", nil, err
	}
	c, err := newClient(cfg)
	if err != nil {
		return "", nil, err
	}
	return seg, c, nil
}

func cmdBackendShow(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("%s", backendUsage)
	}
	seg, c, err := backendClient(args[0])
	if err != nil {
		return err
	}
	var out struct {
		Enabled    bool `json:"enabled"`
		TokenCalls bool `json:"token_calls"`
		Versions   []struct {
			Version int    `json:"version"`
			Image   string `json:"image"`
			Port    int    `json:"port"`
			Running bool   `json:"running"`
		} `json:"versions"`
		Env []struct {
			Name string `json:"name"`
		} `json:"env"`
	}
	if err := c.getJSON("/api/uploads/"+seg+"/backend", &out); err != nil {
		return err
	}
	if !out.Enabled {
		fmt.Println("backends are disabled on this server")
		return nil
	}
	if len(out.Versions) == 0 {
		fmt.Println("no backend")
	} else {
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tIMAGE\tPORT\tRUNNING")
		for _, v := range out.Versions {
			running := "no"
			if v.Running {
				running = "yes"
			}
			fmt.Fprintf(tw, "%d\t%s\t%d\t%s\n", v.Version, v.Image, v.Port, running)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if out.TokenCalls {
		fmt.Println("token calls: on")
	} else {
		fmt.Println("token calls: off")
	}
	if len(out.Env) > 0 {
		fmt.Println("ENV")
		for _, e := range out.Env {
			fmt.Println(e.Name)
		}
	}
	return nil
}

func cmdBackendSet(args []string) error {
	f, err := parseBackendFlags(args, "--version", "--port", "--allow-token-calls")
	if err != nil {
		return err
	}
	if len(f.pos) != 2 {
		return fmt.Errorf("usage: peek backend set <slug> <image> [--version <n>|latest] [--port <n>] [--allow-token-calls]")
	}
	ver, err := backendVersionSegment(f.version)
	if err != nil {
		return err
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	in := map[string]any{"image": f.pos[1]}
	if f.port != 0 {
		in["port"] = f.port
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.req("PUT", "/api/uploads/"+seg+"/versions/"+ver+"/backend", strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := decodeResp(resp, nil); err != nil {
		return err
	}
	fmt.Printf("backend set on %s (version %s): %s\n", f.pos[0], ver, f.pos[1])
	if f.allowTokenCalls {
		return putBackendTokenCalls(c, seg, f.pos[0], true)
	}
	return nil
}

// putBackendTokenCalls turns token calls on or off for a page and reports it.
func putBackendTokenCalls(c *client, seg, slug string, enabled bool) error {
	b, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	resp, err := c.req("PUT", "/api/uploads/"+seg+"/backend/token-calls", strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := decodeResp(resp, nil); err != nil {
		return err
	}
	if enabled {
		fmt.Printf("token calls enabled for %s\n", slug)
	} else {
		fmt.Printf("token calls disabled for %s\n", slug)
	}
	return nil
}

func cmdBackendTokenCalls(args []string) error {
	const usage = "usage: peek backend token-calls on|off <slug>"
	f, err := parseBackendFlags(args)
	if err != nil {
		return err
	}
	if len(f.pos) != 2 || (f.pos[0] != "on" && f.pos[0] != "off") {
		return fmt.Errorf("%s", usage)
	}
	seg, c, err := backendClient(f.pos[1])
	if err != nil {
		return err
	}
	return putBackendTokenCalls(c, seg, f.pos[1], f.pos[0] == "on")
}

const maxCallData = 1 << 20

func cmdBackendCall(args []string) error {
	const usage = `usage: peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]`
	f, err := parseBackendFlags(args, "--version", "--method", "--data", "-H")
	if err != nil {
		return err
	}
	if len(f.pos) != 2 {
		return fmt.Errorf("%s", usage)
	}
	ver, err := backendVersionSegment(f.version)
	if err != nil {
		return err
	}
	method := f.method
	if method == "" {
		method = "GET"
		if f.dataSet {
			method = "POST"
		}
	}
	method = strings.ToUpper(method)
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("--method must be GET, POST, PUT, PATCH or DELETE")
	}
	rawPath, query, hasQuery := strings.Cut(strings.TrimPrefix(f.pos[1], "/"), "?")
	segs := strings.Split(rawPath, "/")
	for i, s := range segs {
		if s == "." || s == ".." {
			return fmt.Errorf("path must not contain . or .. segments")
		}
		segs[i] = url.PathEscape(s)
	}
	header := http.Header{}
	for _, h := range f.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok {
			return fmt.Errorf(`-H must be "Name: value"`)
		}
		name = strings.TrimSpace(name)
		if !strings.EqualFold(name, "Content-Type") && !strings.EqualFold(name, "Accept") {
			return fmt.Errorf("only Content-Type and Accept headers are passed to the backend")
		}
		header.Set(name, strings.TrimSpace(value))
	}
	var body io.Reader
	size := int64(-1)
	if f.dataSet {
		var r io.Reader
		switch {
		case f.data == "-":
			r = os.Stdin
		case strings.HasPrefix(f.data, "@"):
			fh, err := os.Open(f.data[1:])
			if err != nil {
				return err
			}
			defer fh.Close()
			r = fh
		default:
			r = strings.NewReader(f.data)
		}
		b, err := io.ReadAll(io.LimitReader(r, maxCallData+1))
		if err != nil {
			return err
		}
		if len(b) > maxCallData {
			return fmt.Errorf("--data is larger than 1 MiB")
		}
		body = bytes.NewReader(b)
		size = int64(len(b))
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", "application/json")
		}
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	target := "/api/uploads/" + seg + "/versions/" + ver + "/svc/" + strings.Join(segs, "/")
	if hasQuery {
		target += "?" + query
	}
	resp, err := c.reqWith(longHTTPClient, method, target, body, size, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok && resp.Header.Get("X-Peek-Relay") != "backend" {
		return decodeErrorResp(resp)
	}
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("backend returned %s", resp.Status)
	}
	return nil
}

func cmdBackendRemove(args []string) error {
	f, err := parseBackendFlags(args, "--version")
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return fmt.Errorf("usage: peek backend remove <slug> [--version <n>|latest]")
	}
	ver, err := backendVersionSegment(f.version)
	if err != nil {
		return err
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	if err := c.del("/api/uploads/"+seg+"/versions/"+ver+"/backend", nil); err != nil {
		return err
	}
	fmt.Printf("backend removed from %s (version %s)\n", f.pos[0], ver)
	return nil
}

func cmdBackendEnv(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: peek backend env set|unset <slug> <NAME> [flags]")
	}
	switch args[0] {
	case "set":
		return cmdBackendEnvSet(args[1:])
	case "unset":
		return cmdBackendEnvUnset(args[1:])
	}
	// peek backend env <slug> lists the variable names, like peek backend <slug>.
	return cmdBackendShow(args)
}

func cmdBackendEnvSet(args []string) error {
	f, err := parseBackendFlags(args, "--value", "--value-stdin")
	if err != nil {
		return err
	}
	if len(f.pos) != 2 || f.valueSet == f.valueStdin {
		return fmt.Errorf("usage: peek backend env set <slug> <NAME> (--value-stdin | --value <v>)")
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	value := f.value
	if f.valueStdin {
		b, err := io.ReadAll(io.LimitReader(bufio.NewReader(os.Stdin), 1<<20))
		if err != nil {
			return err
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	} else {
		fmt.Fprintln(os.Stderr, "warning: --value leaks into shell history; prefer --value-stdin")
	}
	b, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	resp, err := c.req("PUT", "/api/uploads/"+seg+"/backend/env/"+url.PathEscape(f.pos[1]), strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := decodeResp(resp, nil); err != nil {
		return err
	}
	fmt.Printf("set %s on %s; run 'peek backend restart %s' to apply\n", f.pos[1], f.pos[0], f.pos[0])
	return nil
}

func cmdBackendEnvUnset(args []string) error {
	f, err := parseBackendFlags(args)
	if err != nil {
		return err
	}
	if len(f.pos) != 2 {
		return fmt.Errorf("usage: peek backend env unset <slug> <NAME>")
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	if err := c.del("/api/uploads/"+seg+"/backend/env/"+url.PathEscape(f.pos[1]), nil); err != nil {
		return err
	}
	fmt.Printf("unset %s on %s; run 'peek backend restart %s' to apply\n", f.pos[1], f.pos[0], f.pos[0])
	return nil
}

func cmdBackendRestart(args []string) error {
	f, err := parseBackendFlags(args)
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return fmt.Errorf("usage: peek backend restart <slug>")
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	if err := c.postJSON("/api/uploads/"+seg+"/backend/restart", nil, nil); err != nil {
		return err
	}
	fmt.Printf("restarted backends of %s\n", f.pos[0])
	return nil
}

func cmdBackendLogs(args []string) error {
	f, err := parseBackendFlags(args, "--version", "--tail")
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return fmt.Errorf("usage: peek backend logs <slug> [--version <n>|latest] [--tail <n>]")
	}
	ver, err := backendVersionSegment(f.version)
	if err != nil {
		return err
	}
	seg, c, err := backendClient(f.pos[0])
	if err != nil {
		return err
	}
	path := "/api/uploads/" + seg + "/versions/" + ver + "/backend/logs"
	if f.tail > 0 {
		path += "?tail=" + strconv.Itoa(f.tail)
	}
	resp, err := c.req("GET", path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeErrorResp(resp)
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}
