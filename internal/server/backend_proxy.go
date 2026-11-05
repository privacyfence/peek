package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/models"
)

const (
	backendMaxBody     = 1 << 20
	backendMaxResponse = 8 << 20
)

// backendCaller is who a relayed call comes from, as the backend is told.
type backendCaller struct {
	kind    string          // "browser" or "token": the X-Peek-Caller value
	viewer  string          // X-Peek-Viewer value, already hashed; "" sends none
	account *models.Account // sent as X-Peek-User-Id/-Email on signed-in and restricted pages only; nil sends none
}

// browserViewerHash is the X-Peek-Viewer value of a browser caller.
func (s *Server) browserViewerHash(uploadID int64, viewerID string) string {
	return hmacSHA256(s.secret, "backend-viewer:"+strconv.FormatInt(uploadID, 10)+":"+viewerID)[:32]
}

// tokenCallerHash is the X-Peek-Viewer value of an API-token caller. It uses its own HMAC
// domain because the browser viewer id comes from an unvalidated cookie.
func (s *Server) tokenCallerHash(uploadID, accountID int64) string {
	return hmacSHA256(s.secret, "backend-token-caller:"+strconv.FormatInt(uploadID, 10)+":"+strconv.FormatInt(accountID, 10))[:32]
}

// escapedPathAfter returns the still-escaped path after the first n segments, starting
// with "/" ("/" when nothing follows). The decoded PathValue would turn %3F into "?".
func escapedPathAfter(r *http.Request, n int) string {
	seg := strings.Split(r.URL.EscapedPath(), "/") // seg[0] is "" before the leading slash
	if len(seg) <= n+1 {
		return "/"
	}
	return "/" + strings.Join(seg[n+1:], "/")
}

// readBackendBody reads a relay request body under backendMaxBody, writing the 413/400 itself.
func readBackendBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > backendMaxBody {
		jsonError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, backendMaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			jsonError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			jsonError(w, http.StatusBadRequest, "bad request body")
		}
		return nil, false
	}
	return body, true
}

// handleBackendProxy relays a page's peek.fetch call to its backend container
// after the same access check that guards the page.
func (s *Server) handleBackendProxy(w http.ResponseWriter, r *http.Request) {
	if s.backends == nil {
		jsonError(w, http.StatusNotFound, "backends are not enabled on this server")
		return
	}
	body, ok := readBackendBody(w, r)
	if !ok {
		return
	}

	if r.Header.Get("X-Peek-Svc") != "1" {
		jsonError(w, http.StatusForbidden, "backend requests must come from the page viewer")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		jsonError(w, http.StatusForbidden, "backend requests must come from the page viewer")
		return
	}
	if !s.backendLimiter.allow(s.rateLimitKey(r)) {
		jsonError(w, http.StatusTooManyRequests, "too many backend requests, slow down")
		return
	}

	u, err := s.store.GetUpload(r.Context(), r.PathValue("slug"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	version, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || version < 1 {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.viewerCanAccessUpload(r, u) {
		jsonError(w, s.uploadAccessStatus(r, u), uploadAccessRequiredMessage(u))
		return
	}
	caller := backendCaller{kind: "browser"}
	if vid := rawViewerID(r); vid != "" {
		caller.viewer = s.browserViewerHash(u.ID, vid)
	}
	if a, ok := s.webAuth(r); ok {
		caller.account = a
	}
	s.relayToBackend(w, r, u, version, escapedPathAfter(r, 4), body, caller)
}

// relayToBackend starts the page version's backend and relays one call to it. rest is the
// still-escaped path to forward, starting with "/". It writes the whole response and
// reports the backend's status and whether the call reached the backend.
func (s *Server) relayToBackend(w http.ResponseWriter, r *http.Request, u *models.Upload, version int, rest string, body []byte, caller backendCaller) (int, bool) {
	b, err := s.store.GetUploadBackend(r.Context(), u.ID, version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, http.StatusNotFound, "this page version has no backend")
		} else {
			jsonError(w, http.StatusInternalServerError, "db error")
		}
		return 0, false
	}
	spec, err := s.backendSpec(r.Context(), b)
	var ep backends.Endpoint
	if err == nil {
		ep, err = s.backends.Ensure(r.Context(), spec)
	}
	if err != nil {
		slog.Error("backend start failed", "slug", u.Slug, "version", version, "err", err)
		jsonError(w, http.StatusBadGateway, "backend failed to start")
		return 0, false
	}

	target := ep.URL + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		jsonError(w, http.StatusBadGateway, "backend unavailable")
		return 0, false
	}
	for _, h := range []string{"Content-Type", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("X-Peek-Backend-Token", ep.Token)
	req.Header.Set("X-Peek-Upload-Id", strconv.FormatInt(u.ID, 10))
	req.Header.Set("X-Peek-Version", strconv.Itoa(version))
	req.Header.Set("X-Peek-Caller", caller.kind)
	if caller.viewer != "" {
		req.Header.Set("X-Peek-Viewer", caller.viewer)
	}
	if u.Visibility == models.UploadVisibilitySignedIn || u.Visibility == models.UploadVisibilityRestricted {
		if a := caller.account; a != nil {
			req.Header.Set("X-Peek-User-Id", strconv.FormatInt(a.ID, 10))
			if a.Email != "" {
				req.Header.Set("X-Peek-User-Email", a.Email)
			}
		}
	}

	resp, err := s.backendClient.Do(req)
	if err != nil {
		backendRelayError(w, err)
		return 0, false
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, backendMaxResponse+1))
	if err != nil {
		backendRelayError(w, err)
		return 0, false
	}
	if len(out) > backendMaxResponse {
		jsonError(w, http.StatusBadGateway, "backend response too large")
		return 0, false
	}
	h := w.Header()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Cache-Control", "no-store")
	h.Set("X-Peek-Relay", "backend")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
	return resp.StatusCode, true
}

func backendRelayError(w http.ResponseWriter, err error) {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		jsonError(w, http.StatusGatewayTimeout, "backend timed out")
	} else {
		jsonError(w, http.StatusBadGateway, "backend unavailable")
	}
}
