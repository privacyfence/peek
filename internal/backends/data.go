package backends

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strconv"
	"strings"
)

// ErrDataBusy is returned while a page's /data volume is being exported or imported:
// Ensure refuses to start that page's backends, and a second export or import is refused.
var ErrDataBusy = errors.New("backend data export or import in progress")

// ArchiveError is an archive Peek refuses to import; its message is shown to the user.
type ArchiveError struct{ Msg string }

func (e *ArchiveError) Error() string { return e.Msg }

// DataHelperName is the name of the never-started container that mounts a page's
// /data volume for export and import.
func DataHelperName(instance string, uploadID int64) string {
	return fmt.Sprintf("peek-%s-data-u%d-io", instance, uploadID)
}

// ValidateDataArchive reads a whole tar archive and returns an *ArchiveError for anything Peek does not import.
func ValidateDataArchive(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return &ArchiveError{"not a valid tar archive"}
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if path.IsAbs(h.Name) || name == ".." || strings.HasPrefix(name, "../") {
			return &ArchiveError{fmt.Sprintf("archive entry %q is outside /data", h.Name)}
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(name), h.Linkname))
			if path.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
				return &ArchiveError{fmt.Sprintf("archive entry %q links outside /data", h.Name)}
			}
		default:
			return &ArchiveError{fmt.Sprintf("archive entry %q is not a file, directory or symlink", h.Name)}
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return &ArchiveError{"not a valid tar archive"}
		}
	}
}

// claimData marks a page's data as busy. The returned func ends the operation.
func (m *Manager) claimData(uploadID int64) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errClosed
	}
	if m.dataBusy[uploadID] != nil {
		return nil, ErrDataBusy
	}
	return m.setBusyLocked(uploadID), nil
}

func (m *Manager) setBusyLocked(uploadID int64) func() {
	ch := make(chan struct{})
	m.dataBusy[uploadID] = ch
	return func() {
		m.mu.Lock()
		delete(m.dataBusy, uploadID)
		m.mu.Unlock()
		close(ch)
	}
}

// waitData is like claimData but waits for a running operation to end. It
// works on a closed manager, so deleting a page succeeds during shutdown.
func (m *Manager) waitData(ctx context.Context, uploadID int64) (func(), error) {
	for {
		m.mu.Lock()
		ch := m.dataBusy[uploadID]
		if ch == nil {
			release := m.setBusyLocked(uploadID)
			m.mu.Unlock()
			return release, nil
		}
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// withDataHelper creates a never-started container that mounts the page's volume, runs fn
// with its id and removes it afterwards.
func (m *Manager) withDataHelper(ctx context.Context, spec Spec, fn func(id string) error) error {
	if err := m.ensureImage(ctx, spec.Image, spec.RegistryAuth); err != nil {
		return err
	}
	name := DataHelperName(m.instance, spec.UploadID)
	if err := m.rt.RemoveContainer(ctx, name); err != nil {
		return fmt.Errorf("remove stale data helper %s: %w", name, err)
	}
	id, err := m.rt.CreateContainer(ctx, ContainerSpec{
		Name:  name,
		Image: spec.Image,
		Labels: map[string]string{
			"peek.managed":   "true",
			"peek.instance":  m.instance,
			"peek.upload_id": strconv.FormatInt(spec.UploadID, 10),
			"peek.role":      "data-io",
		},
		Network:     m.network,
		Volume:      VolumeName(m.instance, spec.UploadID),
		MemoryBytes: spec.MemoryBytes,
		NanoCPUs:    spec.NanoCPUs,
	})
	if err != nil {
		return fmt.Errorf("create data helper %s: %w", name, err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orphanStopTimeout)
		defer cancel()
		if err := m.rt.RemoveContainer(rmCtx, id); err != nil {
			slog.Warn("backend data helper remove", "upload_id", spec.UploadID, "container", id, "err", err)
		}
	}()
	return fn(id)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ExportData stops the page's backends and writes its /data volume to w as a tar archive
// with entry names relative to /data. It returns the number of bytes written.
func (m *Manager) ExportData(ctx context.Context, spec Spec, w io.Writer) (int64, error) {
	release, err := m.claimData(spec.UploadID)
	if err != nil {
		return 0, err
	}
	defer release()
	if err := m.StopUpload(ctx, spec.UploadID); err != nil {
		return 0, err
	}
	cw := &countingWriter{w: w}
	err = m.withDataHelper(ctx, spec, func(id string) error {
		rc, err := m.rt.CopyFromContainer(ctx, id, "/data")
		if err != nil {
			return err
		}
		defer rc.Close()
		tr := tar.NewReader(rc)
		tw := tar.NewWriter(cw)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read data archive: %w", err)
			}
			if h.Name == "data" || h.Name == "data/" {
				continue
			}
			rel, ok := strings.CutPrefix(h.Name, "data/")
			if !ok {
				return fmt.Errorf("unexpected archive entry %q", h.Name)
			}
			h.Name = rel
			if h.Typeflag == tar.TypeLink {
				h.Linkname = strings.TrimPrefix(h.Linkname, "data/")
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		}
		return tw.Close()
	})
	return cw.n, err
}

// ImportData stops the page's backends, deletes the page's /data volume, and extracts
// archive into a fresh one, so the volume afterwards holds exactly the archive.
// The caller validates archive with ValidateDataArchive first.
func (m *Manager) ImportData(ctx context.Context, spec Spec, archive io.Reader) error {
	release, err := m.claimData(spec.UploadID)
	if err != nil {
		return err
	}
	defer release()
	if err := m.StopUpload(ctx, spec.UploadID); err != nil {
		return err
	}
	if err := m.rt.RemoveContainer(ctx, DataHelperName(m.instance, spec.UploadID)); err != nil {
		return fmt.Errorf("remove stale data helper: %w", err)
	}
	if err := m.rt.RemoveVolume(ctx, VolumeName(m.instance, spec.UploadID)); err != nil {
		return err
	}
	return m.withDataHelper(ctx, spec, func(id string) error {
		rc, err := m.rt.CopyFromContainer(ctx, id, "/data")
		if err != nil {
			return err
		}
		h, err := tar.NewReader(rc).Next()
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("read data volume owner: %w", err)
		}
		if h.Name != "data" && h.Name != "data/" {
			return fmt.Errorf("unexpected archive entry %q", h.Name)
		}
		uid, gid := h.Uid, h.Gid

		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(rewriteOwner(pw, archive, uid, gid))
		}()
		err = m.rt.CopyToContainer(ctx, id, "/data", pr)
		pr.CloseWithError(err)
		return err
	})
}

// rewriteOwner copies a tar archive to w with every entry owned by uid:gid.
func rewriteOwner(w io.Writer, archive io.Reader, uid, gid int) error {
	tr := tar.NewReader(archive)
	tw := tar.NewWriter(w)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return tw.Close()
		}
		if err != nil {
			return err
		}
		h.Uid, h.Gid, h.Uname, h.Gname = uid, gid, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}
