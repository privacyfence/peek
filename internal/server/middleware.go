package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/privacyfence/peek/internal/db"
	"github.com/privacyfence/peek/internal/models"
)

type contextKey string

const apiTokenContextKey contextKey = "api-token"

// maxRequestBodyBytes caps request bodies on every route except the two upload
// routes, which apply their own limit from the max_upload setting. 64 KiB matches
// the largest JSON body any handler accepts (defaultJSONBodyLimit).
const maxRequestBodyBytes = defaultJSONBodyLimit

// bodyLimitExempt reports whether r is an upload, backend relay or token backend
// relay request, whose handlers set their own MaxBytesReader.
func bodyLimitExempt(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/svc/") {
		return true
	}
	if r.Method == http.MethodPost && (p == "/api/upload" || p == "/dashboard/upload") {
		return true
	}
	seg := strings.Split(r.URL.EscapedPath(), "/") // "", "api", "uploads", slug, ...; escaped, as the mux matches it, so %2F in a slug cannot shift segments
	// API-token backend relay; relayToBackend applies the 1 MiB limit.
	return len(seg) >= 7 && seg[1] == "api" && seg[2] == "uploads" && seg[4] == "versions" && seg[6] == "svc"
}

func requestTooLargeError(w http.ResponseWriter, r *http.Request) {
	if wantsJSONError(r) {
		jsonError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	http.Error(w, "Request body too large.", http.StatusRequestEntityTooLarge)
}

func (s *Server) withMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if s.secure {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		// Global rate limit to protect against request floods.
		if !s.globalLimiter.allow(s.rateLimitKey(r)) {
			rateLimitError(w, r)
			return
		}
		if r.Body != nil && r.Body != http.NoBody && !bodyLimitExempt(r) {
			if r.ContentLength > maxRequestBodyBytes {
				requestTooLargeError(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		reqTotal.Add(1)
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(rw, r)
		if rw.status >= 400 {
			reqErrors.Add(1)
		}
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer's Flush and
// SetWriteDeadline.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// authToken gates an endpoint behind a bearer token (any valid user token).
func (s *Server) authToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			jsonError(w, http.StatusUnauthorized, "missing token")
			return
		}
		t, err := s.store.GetToken(r.Context(), tok)
		if err != nil {
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err := s.tokenAccountActive(r.Context(), t); err != nil {
			writeTokenPolicyError(w, err)
			return
		}
		s.recordTokenUse(r, t)
		next(w, withAPIToken(r, t))
	}
}

// recordTokenUse stores when t was last used, at most once per
// db.TokenLastUsedInterval, so a busy key does not write on every request.
func (s *Server) recordTokenUse(r *http.Request, t *models.Token) {
	now := time.Now()
	if t.LastUsedAt > 0 && now.Unix()-t.LastUsedAt < db.TokenLastUsedInterval {
		return
	}
	if err := s.store.TouchTokenLastUsed(r.Context(), t.ID, now); err != nil {
		slog.Warn("token last-used update failed", "token_id", t.ID, "err", err)
	}
}

func (s *Server) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			jsonError(w, http.StatusUnauthorized, "missing token")
			return
		}
		t, err := s.store.GetToken(r.Context(), tok)
		if err != nil {
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err := s.tokenAccountActive(r.Context(), t); err != nil {
			writeTokenPolicyError(w, err)
			return
		}
		if !t.IsAdmin {
			jsonError(w, http.StatusForbidden, "admin only")
			return
		}
		s.recordTokenUse(r, t)
		next(w, withAPIToken(r, t))
	}
}

func withAPIToken(r *http.Request, t *models.Token) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), apiTokenContextKey, t))
}

func apiToken(r *http.Request) (*models.Token, bool) {
	t, ok := r.Context().Value(apiTokenContextKey).(*models.Token)
	return t, ok && t != nil
}

func requireAPIToken(w http.ResponseWriter, r *http.Request) (*models.Token, bool) {
	t, ok := apiToken(r)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "auth context missing")
		return nil, false
	}
	return t, true
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}
