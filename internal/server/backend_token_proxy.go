package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/privacyfence/peek/internal/models"
)

// tokenCanCallBackend applies the /svc access rule to the token's account: public pages
// to any token, signed-in pages to any active account, restricted pages to the owner,
// admins and grantees (accountCanViewRestricted).
func (s *Server) tokenCanCallBackend(ctx context.Context, t *models.Token, u *models.Upload) bool {
	switch u.Visibility {
	case models.UploadVisibilityPublic, models.UploadVisibilitySignedIn:
		return true
	case models.UploadVisibilityRestricted:
		return s.accountCanViewRestricted(ctx, tokenAccount(t), u)
	default:
		return false
	}
}

// handleBackendTokenCall relays an API-token call to a page version's backend once the
// page owner has turned token calls on.
func (s *Server) handleBackendTokenCall(w http.ResponseWriter, r *http.Request) {
	tok, ok := requireAPIToken(w, r)
	if !ok {
		return
	}
	if s.backends == nil {
		jsonError(w, http.StatusNotFound, "backends are not enabled on this server")
		return
	}
	body, ok := readBackendBody(w, r)
	if !ok {
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
	// Before the version lookup, so a token without access cannot probe which versions exist.
	if !s.tokenCanCallBackend(r.Context(), tok, u) {
		jsonError(w, http.StatusForbidden, "access denied")
		return
	}
	version, ok := s.backendVersion(w, r, u)
	if !ok {
		return
	}
	allowed, err := s.store.UploadBackendTokenCalls(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	if !allowed {
		jsonError(w, http.StatusForbidden, "token calls are not enabled for this backend")
		return
	}
	caller := backendCaller{kind: "token", viewer: s.tokenCallerHash(u.ID, tok.AccountID), account: tokenAccount(tok)}
	status, relayed := s.relayToBackend(w, r, u, version, escapedPathAfter(r, 6), body, caller)
	if !relayed {
		return
	}
	identified := u.Visibility == models.UploadVisibilitySignedIn || u.Visibility == models.UploadVisibilityRestricted
	if err := s.store.RecordBackendTokenCall(context.WithoutCancel(r.Context()), u.ID, version, tok.AccountID, status, identified); err != nil {
		slog.Warn("backend token call not recorded", "slug", u.Slug, "err", err)
	}
}
