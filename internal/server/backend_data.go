package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/models"
)

const (
	backendDataTransferTimeout = 30 * time.Minute // read/write deadline of one export or import request
	backendDataImportTimeout   = 10 * time.Minute // bound of the import itself, detached from the client
)

const backendDataBusyMessage = "another data export or import is running for this page"

// latestBackendSpec returns the spec of the page's highest-version backend, writing 404
// "this page has no backend" (or 500 "db error") itself.
func (s *Server) latestBackendSpec(w http.ResponseWriter, r *http.Request, u *models.Upload) (backends.Spec, bool) {
	rows, err := s.store.ListUploadBackends(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return backends.Spec{}, false
	}
	if len(rows) == 0 {
		jsonError(w, http.StatusNotFound, "this page has no backend")
		return backends.Spec{}, false
	}
	spec, err := s.backendSpec(r.Context(), &rows[len(rows)-1])
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not read backend settings")
		return backends.Spec{}, false
	}
	return spec, true
}

// exportWriter sends the download headers on its first Write.
type exportWriter struct {
	w       http.ResponseWriter
	slug    string
	started bool
}

func (e *exportWriter) Write(p []byte) (int, error) {
	if !e.started {
		e.started = true
		h := e.w.Header()
		h.Set("Content-Type", "application/x-tar")
		h.Set("Content-Disposition", `attachment; filename="`+e.slug+`-data.tar"`)
		h.Set("Cache-Control", "no-store")
		e.w.WriteHeader(http.StatusOK)
	}
	return e.w.Write(p)
}

func (s *Server) handleExportBackendData(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	spec, ok := s.latestBackendSpec(w, r, u)
	if !ok {
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(backendDataTransferTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Warn("backend data export deadline", "slug", u.Slug, "err", err)
	}
	lw := &exportWriter{w: w, slug: u.Slug}
	n, err := s.backends.ExportData(r.Context(), spec, lw)
	if err != nil {
		if lw.started {
			slog.Error("backend data export failed", "slug", u.Slug, "err", err)
			panic(http.ErrAbortHandler)
		}
		if errors.Is(err, backends.ErrDataBusy) {
			jsonError(w, http.StatusConflict, backendDataBusyMessage)
			return
		}
		slog.Error("backend data export failed", "slug", u.Slug, "err", err)
		jsonError(w, http.StatusBadGateway, "could not export backend data")
		return
	}
	s.auditRequest(r, owner.Name, "backend.data.export", auditDetail("slug", u.Slug, "bytes", strconv.FormatInt(n, 10)))
}

func (s *Server) handleImportBackendData(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	spec, ok := s.latestBackendSpec(w, r, u)
	if !ok {
		return
	}
	limit := s.settingInt64(r.Context(), "max_backend_data_import", defaultBackendDataImportMax)
	if r.ContentLength > limit {
		jsonError(w, http.StatusRequestEntityTooLarge, "archive too large")
		return
	}
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Now().Add(backendDataTransferTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Warn("backend data import deadline", "slug", u.Slug, "err", err)
	}
	if err := rc.SetWriteDeadline(time.Now().Add(backendDataTransferTimeout + backendDataImportTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Warn("backend data import deadline", "slug", u.Slug, "err", err)
	}

	tmp, err := os.CreateTemp(s.dataDir, ".backend-data-import-*.tar")
	if err != nil {
		slog.Error("backend data import spool", "slug", u.Slug, "err", err)
		jsonError(w, http.StatusInternalServerError, "could not store the archive")
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			jsonError(w, http.StatusRequestEntityTooLarge, "archive too large")
		} else {
			jsonError(w, http.StatusBadRequest, "bad request body")
		}
		return
	}
	if n == 0 {
		jsonError(w, http.StatusBadRequest, "empty archive")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		jsonError(w, http.StatusInternalServerError, "could not store the archive")
		return
	}
	if err := backends.ValidateDataArchive(tmp); err != nil {
		var ae *backends.ArchiveError
		if errors.As(err, &ae) {
			jsonError(w, http.StatusBadRequest, ae.Msg)
		} else {
			jsonError(w, http.StatusBadRequest, "not a valid tar archive")
		}
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		jsonError(w, http.StatusInternalServerError, "could not store the archive")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backendDataImportTimeout)
	defer cancel()
	if err := s.backends.ImportData(ctx, spec, tmp); err != nil {
		if errors.Is(err, backends.ErrDataBusy) {
			jsonError(w, http.StatusConflict, backendDataBusyMessage)
			return
		}
		slog.Error("backend data import failed", "slug", u.Slug, "err", err)
		jsonError(w, http.StatusBadGateway, "could not import backend data")
		return
	}
	s.auditRequest(r, owner.Name, "backend.data.import", auditDetail("slug", u.Slug, "bytes", strconv.FormatInt(n, 10)))
	jsonOK(w, map[string]any{"status": "ok", "bytes": n})
}
