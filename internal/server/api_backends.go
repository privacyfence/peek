package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/models"
)

const (
	defaultBackendPort    = 8080
	backendMaxEnvVars     = 50
	backendMaxEnvValue    = 4096
	backendDefaultLogTail = 200
	backendMaxLogTail     = 2000
)

var backendEnvNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

type backendSetBody struct {
	Image string `json:"image"`
	Port  int    `json:"port"`
}

type backendEnvBody struct {
	Value string `json:"value"`
}

type backendVersionOut struct {
	Version int    `json:"version"`
	Image   string `json:"image"`
	Port    int    `json:"port"`
	Running bool   `json:"running"`
}

type backendEnvOut struct {
	Name      string `json:"name"`
	UpdatedAt int64  `json:"updated_at"`
}

type backendInfoOut struct {
	Enabled  bool                `json:"enabled"`
	Versions []backendVersionOut `json:"versions"`
	Env      []backendEnvOut     `json:"env"`
	// TokenCalls is whether API tokens may call the page's backend.
	TokenCalls bool `json:"token_calls"`
}

// backendUpload loads the page named in the path and checks that the caller owns it or is an admin.
func (s *Server) backendUpload(w http.ResponseWriter, r *http.Request) (*models.Token, *models.Upload, bool) {
	owner, ok := requireAPIToken(w, r)
	if !ok {
		return nil, nil, false
	}
	u, err := s.store.GetUpload(r.Context(), r.PathValue("slug"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "not found")
		return nil, nil, false
	}
	if u.OwnerAccountID != owner.AccountID && !owner.IsAdmin {
		jsonError(w, http.StatusForbidden, "not owner")
		return nil, nil, false
	}
	return owner, u, true
}

// requireBackendsEnabled answers 409 and returns false when backends are off.
func (s *Server) requireBackendsEnabled(w http.ResponseWriter) bool {
	if s.backends == nil {
		jsonError(w, http.StatusConflict, "backends are not enabled on this server")
		return false
	}
	return true
}

// backendVersion resolves {n} ("latest" or a positive number) to an existing
// version of the page, writing the 404 itself on failure.
func (s *Server) backendVersion(w http.ResponseWriter, r *http.Request, u *models.Upload) (int, bool) {
	raw := r.PathValue("n")
	n := u.LatestVersion
	if raw != "latest" {
		var err error
		n, err = strconv.Atoi(raw)
		if err != nil || n < 1 {
			jsonError(w, http.StatusNotFound, "version not found")
			return 0, false
		}
	}
	if _, err := s.store.GetUploadVersion(r.Context(), u.ID, n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "version not found")
		} else {
			jsonError(w, http.StatusInternalServerError, "db error")
		}
		return 0, false
	}
	return n, true
}

// validateBackendInput checks a backend image and port. Port 0 means the
// default. A non-zero status is the HTTP status to answer with msg.
func (s *Server) validateBackendInput(ctx context.Context, image string, port int) (int, int, string) {
	if backends.ValidateImageRef(image) != nil {
		return 0, http.StatusBadRequest, "invalid backend image reference"
	}
	if !s.backendImageAllowed(ctx, image) {
		return 0, http.StatusBadRequest, "backend image is not allowed; ask an admin to add its registry to the allowed backend images"
	}
	if port == 0 {
		port = defaultBackendPort
	}
	if port < 1 || port > 65535 {
		return 0, http.StatusBadRequest, "backend port must be between 1 and 65535"
	}
	return port, 0, ""
}

func (s *Server) backendVersionOut(b models.UploadBackend) backendVersionOut {
	return backendVersionOut{
		Version: b.Version,
		Image:   b.Image,
		Port:    b.Port,
		Running: s.backends != nil && s.backends.Running(b.UploadID, b.Version),
	}
}

func (s *Server) handleGetBackend(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.backendUpload(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListUploadBackends(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	vars, err := s.store.ListUploadBackendEnv(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	tokenCalls, err := s.store.UploadBackendTokenCalls(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	out := backendInfoOut{
		TokenCalls: tokenCalls,
		Enabled:    s.backends != nil,
		Versions:   make([]backendVersionOut, 0, len(rows)),
		Env:        make([]backendEnvOut, 0, len(vars)),
	}
	for _, b := range rows {
		out.Versions = append(out.Versions, s.backendVersionOut(b))
	}
	for _, v := range vars {
		out.Env = append(out.Env, backendEnvOut{Name: v.Name, UpdatedAt: v.UpdatedAt.Unix()})
	}
	jsonOK(w, out)
}

func (s *Server) handleSetBackendTokenCalls(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, smallJSONBodyLimit); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if body.Enabled == nil {
		jsonError(w, http.StatusBadRequest, "enabled must be true or false")
		return
	}
	if err := s.store.SetUploadBackendTokenCalls(r.Context(), u.ID, *body.Enabled); err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	s.auditRequest(r, owner.Name, "backend.token_calls", auditDetail("slug", u.Slug, "enabled", strconv.FormatBool(*body.Enabled)))
	jsonOK(w, map[string]any{"status": "ok", "token_calls": *body.Enabled})
}

func (s *Server) handleSetBackend(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	version, ok := s.backendVersion(w, r, u)
	if !ok {
		return
	}
	var body backendSetBody
	if err := decodeJSON(w, r, &body, smallJSONBodyLimit); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	body.Image = strings.TrimSpace(body.Image)
	port, status, msg := s.validateBackendInput(r.Context(), body.Image, body.Port)
	if status != 0 {
		jsonError(w, status, msg)
		return
	}
	b := models.UploadBackend{UploadID: u.ID, Version: version, Image: body.Image, Port: port, CreatedByAccountID: owner.AccountID}
	if err := s.store.SetUploadBackend(r.Context(), b); err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	// Stop after the write so a request in between cannot restart the old image.
	if err := s.backends.StopVersion(r.Context(), u.ID, version); err != nil {
		jsonError(w, http.StatusInternalServerError, "backend saved but the running container could not be stopped")
		return
	}
	s.auditRequest(r, owner.Name, "backend.set", auditDetail("slug", u.Slug, "version", strconv.Itoa(version), "image", body.Image, "port", strconv.Itoa(port)))
	jsonOK(w, s.backendVersionOut(b))
}

func (s *Server) handleRemoveBackend(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	version, ok := s.backendVersion(w, r, u)
	if !ok {
		return
	}
	if err := s.store.DeleteUploadBackend(r.Context(), u.ID, version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "no backend for this version")
			return
		}
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := s.backends.StopVersion(r.Context(), u.ID, version); err != nil {
		jsonError(w, http.StatusInternalServerError, "backend removed but the running container could not be stopped")
		return
	}
	s.auditRequest(r, owner.Name, "backend.remove", auditDetail("slug", u.Slug, "version", strconv.Itoa(version)))
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleBackendLogs(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.backendUpload(w, r)
	if !ok {
		return
	}
	version, ok := s.backendVersion(w, r, u)
	if !ok {
		return
	}
	tail := backendDefaultLogTail
	if raw := r.URL.Query().Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > backendMaxLogTail {
			jsonError(w, http.StatusBadRequest, "tail must be between 1 and 2000")
			return
		}
		tail = n
	}
	if s.backends == nil {
		jsonError(w, http.StatusNotFound, "backend is not running")
		return
	}
	logs, err := s.backends.Logs(r.Context(), u.ID, version, tail)
	if err != nil {
		if errors.Is(err, backends.ErrNotRunning) {
			jsonError(w, http.StatusNotFound, "backend is not running")
			return
		}
		jsonError(w, http.StatusInternalServerError, "could not read backend logs")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(logs)
}

func (s *Server) handleSetBackendEnv(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	name := r.PathValue("name")
	if !backendEnvNamePattern.MatchString(name) || name == "PORT" || strings.HasPrefix(name, "PEEK_") {
		jsonError(w, http.StatusBadRequest, "invalid variable name; use A-Z, 0-9 and _, not PORT or PEEK_*")
		return
	}
	var body backendEnvBody
	if err := decodeJSON(w, r, &body, smallJSONBodyLimit); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body.Value) > backendMaxEnvValue {
		jsonError(w, http.StatusBadRequest, "value too long")
		return
	}
	vars, err := s.store.ListUploadBackendEnv(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	exists := false
	for _, v := range vars {
		if v.Name == name {
			exists = true
			break
		}
	}
	if !exists && len(vars) >= backendMaxEnvVars {
		jsonError(w, http.StatusBadRequest, "too many variables (max 50)")
		return
	}
	enc, err := encryptSecret(s.secret, body.Value)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not encrypt value")
		return
	}
	if err := s.store.SetUploadBackendEnv(r.Context(), u.ID, name, enc); err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	s.auditRequest(r, owner.Name, "backend.env.set", auditDetail("slug", u.Slug, "name", name))
	jsonOK(w, map[string]any{"status": "ok", "restart_required": true})
}

func (s *Server) handleUnsetBackendEnv(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := s.store.DeleteUploadBackendEnv(r.Context(), u.ID, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "no such variable")
			return
		}
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	s.auditRequest(r, owner.Name, "backend.env.unset", auditDetail("slug", u.Slug, "name", name))
	jsonOK(w, map[string]any{"status": "ok", "restart_required": true})
}

func (s *Server) handleRestartBackend(w http.ResponseWriter, r *http.Request) {
	owner, u, ok := s.backendUpload(w, r)
	if !ok || !s.requireBackendsEnabled(w) {
		return
	}
	if err := s.backends.StopUpload(r.Context(), u.ID); err != nil {
		jsonError(w, http.StatusInternalServerError, "could not stop the backend")
		return
	}
	s.auditRequest(r, owner.Name, "backend.restart", auditDetail("slug", u.Slug))
	jsonOK(w, map[string]string{"status": "ok"})
}
