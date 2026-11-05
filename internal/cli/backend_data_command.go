package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const backendDataUsage = "usage: peek backend data export <slug> [-o <file>] | import <slug> <file> [--yes]"

// Test seams for the import confirmation.
var stdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

var confirmInput io.Reader = os.Stdin

func cmdBackendData(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", backendDataUsage)
	}
	switch args[0] {
	case "export":
		return cmdBackendDataExport(args[1:])
	case "import":
		return cmdBackendDataImport(args[1:])
	}
	return fmt.Errorf("%s", backendDataUsage)
}

func cmdBackendDataExport(args []string) error {
	f, err := parseBackendFlags(args, "-o")
	if err != nil {
		return err
	}
	if len(f.pos) != 1 {
		return fmt.Errorf("%s", backendDataUsage)
	}
	slug := f.pos[0]
	seg, c, err := backendClient(slug)
	if err != nil {
		return err
	}
	name := f.output
	if name == "" {
		name = slug + "-data.tar"
	}
	var out io.Writer = os.Stdout
	var file *os.File
	if name != "-" {
		file, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("%s already exists", name)
			}
			return err
		}
		out = file
	}
	fail := func(err error) error {
		if file != nil {
			_ = file.Close()
			_ = os.Remove(name)
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "stopping backends for %s to take a consistent copy\n", slug)
	resp, err := c.reqWith(longHTTPClient, "GET", "/api/uploads/"+seg+"/backend/data", nil, -1, nil)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(decodeErrorResp(resp))
	}
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return fail(err)
	}
	if file != nil {
		if err := file.Close(); err != nil {
			_ = os.Remove(name)
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes to %s\n", n, name)
	}
	return nil
}

func cmdBackendDataImport(args []string) error {
	f, err := parseBackendFlags(args, "--yes")
	if err != nil {
		return err
	}
	if len(f.pos) != 2 {
		return fmt.Errorf("%s", backendDataUsage)
	}
	slug, name := f.pos[0], f.pos[1]
	seg, c, err := backendClient(slug)
	if err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return err
	}
	if !f.yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("refusing to replace /data of %s without confirmation; pass --yes", slug)
		}
		fmt.Fprintf(os.Stderr, "This replaces all of /data for %s: files that are not in %s are deleted. Run 'peek backend data export %s' first to keep a copy. Continue? [y/N] ", slug, name, slug)
		line, _ := bufio.NewReader(confirmInput).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return fmt.Errorf("import cancelled")
		}
	}
	header := http.Header{"Content-Type": {"application/x-tar"}}
	resp, err := c.reqWith(longHTTPClient, "PUT", "/api/uploads/"+seg+"/backend/data", file, fi.Size(), header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeErrorResp(resp)
	}
	var out struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	fmt.Printf("imported %d bytes into /data of %s\n", out.Bytes, slug)
	return nil
}
