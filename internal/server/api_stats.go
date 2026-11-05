package server

import (
	"net/http"
	"time"

	"github.com/privacyfence/peek/internal/models"
)

// tokenCallRow is one API-token call counter in the stats response.
type tokenCallRow struct {
	Caller     string `json:"caller"` // the X-Peek-Viewer the backend received
	Email      string `json:"email,omitempty"`
	Version    int    `json:"version"`
	Calls      int64  `json:"calls"`
	LastCallAt int64  `json:"last_call_at"` // unix seconds
	LastStatus int    `json:"last_status"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	owner, ok := requireAPIToken(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	u, err := s.store.GetUpload(r.Context(), slug)
	if err != nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if u.OwnerAccountID != owner.AccountID && !owner.IsAdmin {
		jsonError(w, http.StatusForbidden, "not owner")
		return
	}
	total, unique, err := s.store.CountVisits(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	recent, err := s.store.RecentVisits(r.Context(), u.ID, 50)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	calls, err := s.store.ListBackendTokenCalls(r.Context(), u.ID, 100)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	type vrow struct {
		Name      string `json:"name"`
		IP        string `json:"ip"`
		UA        string `json:"user_agent"`
		Timestamp int64  `json:"visited_at"`
	}
	rows := make([]vrow, 0, len(recent))
	for _, v := range recent {
		rows = append(rows, vrow{Name: v.VisitorName, IP: v.IP, UA: v.UserAgent, Timestamp: v.VisitedAt.Unix()})
	}
	callRows := make([]tokenCallRow, 0, len(calls))
	for _, c := range calls {
		row := tokenCallRow{
			Caller: s.tokenCallerHash(u.ID, c.AccountID), Version: c.Version, Calls: c.Calls,
			LastCallAt: c.LastCallAt.Unix(), LastStatus: c.LastStatus,
		}
		// The email is shown under the identity-header rule: only when the backend already got it, or to admins.
		if c.Identified || owner.IsAdmin {
			row.Email = c.Email
		}
		callRows = append(callRows, row)
	}
	jsonOK(w, map[string]any{
		"slug":            slug,
		"name":            u.Name,
		"total_visits":    total,
		"unique_visitors": unique,
		"recent":          rows,
		"token_calls":     callRows,
	})
}

func (s *Server) handleExportUpload(w http.ResponseWriter, r *http.Request) {
	owner, ok := requireAPIToken(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	u, err := s.store.GetUpload(r.Context(), slug)
	if err != nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if u.OwnerAccountID != owner.AccountID && !owner.IsAdmin {
		jsonError(w, http.StatusForbidden, "not owner")
		return
	}
	total, unique, err := s.store.CountVisits(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	recent, err := s.store.RecentVisits(r.Context(), u.ID, 500)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	comments, err := s.store.ListComments(r.Context(), u.ID, 0)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}

	type exportComment struct {
		Author     string `json:"author"`
		Verified   bool   `json:"verified"`
		Body       string `json:"body"`
		Selector   string `json:"selector"`
		Text       string `json:"element_text"`
		AnchorKind string `json:"anchor_kind"`
		CreatedAt  int64  `json:"created_at"`
		Version    int    `json:"version"`
	}
	type exportVisit struct {
		Name      string `json:"name"`
		IP        string `json:"ip"`
		UA        string `json:"user_agent"`
		Timestamp int64  `json:"visited_at"`
		Version   int    `json:"version"`
	}
	type exportVersion struct {
		Version   int   `json:"version"`
		Size      int64 `json:"size"`
		CreatedAt int64 `json:"created_at"`
	}
	versions, err := s.store.ListUploadVersions(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	vers := make([]exportVersion, 0, len(versions))
	for _, v := range versions {
		vers = append(vers, exportVersion{Version: v.Version, Size: v.Size, CreatedAt: v.CreatedAt.Unix()})
	}
	export := map[string]any{
		"slug":            slug,
		"name":            u.Name,
		"size":            u.Size,
		"visibility":      u.Visibility,
		"created_at":      u.CreatedAt.Unix(),
		"total_visits":    total,
		"unique_visitors": unique,
		"latest_version":  u.LatestVersion,
		"versions":        vers,
	}
	cmts := make([]exportComment, 0, len(comments))
	for _, c := range comments {
		cmts = append(cmts, exportComment{
			Author: c.AuthorName, Verified: c.AccountID != 0, Body: c.Body, Selector: c.ElementSelector,
			Text: c.ElementText, AnchorKind: commentAnchorKind(c.ElementSelector, c.ElementText, c.AnchorKind),
			CreatedAt: c.CreatedAt.Unix(), Version: c.Version,
		})
	}
	export["comments"] = cmts
	visits := make([]exportVisit, 0, len(recent))
	for _, v := range recent {
		visits = append(visits, exportVisit{
			Name: v.VisitorName, IP: v.IP, UA: v.UserAgent, Timestamp: v.VisitedAt.Unix(), Version: v.Version,
		})
	}
	export["visits"] = visits
	grants, err := s.store.ListUploadGrants(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	type exportGrant struct {
		Kind      string `json:"kind"`
		Email     string `json:"email"`
		CreatedAt int64  `json:"created_at"`
	}
	gr := make([]exportGrant, 0, len(grants))
	for _, g := range grants {
		gr = append(gr, exportGrant{Kind: g.Kind, Email: g.Email, CreatedAt: g.CreatedAt.Unix()})
	}
	export["grants"] = gr

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+slug+`-export.json"`)
	jsonOK(w, export)
}

func (s *Server) handleViews(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	u, err := s.store.GetUpload(r.Context(), slug)
	if err != nil {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if !s.viewerCanAccessUpload(r, u) {
		jsonError(w, s.uploadAccessStatus(r, u), uploadAccessRequiredMessage(u))
		return
	}
	total, unique, err := s.store.CountVisits(r.Context(), u.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	const daySeconds = 86400
	const viewBucketCount = 7
	since := bucketStart(time.Now().Add(-time.Duration(viewBucketCount-1)*24*time.Hour), daySeconds)
	buckets, err := s.store.VisitBuckets(r.Context(), u.ID, since, daySeconds)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "db error")
		return
	}
	type bucketJSON struct {
		T int64 `json:"t"`
		N int   `json:"n"`
	}
	filled := fillBuckets(buckets, since, daySeconds, viewBucketCount)
	jb := make([]bucketJSON, len(filled))
	for i, b := range filled {
		jb[i] = bucketJSON{T: b.Time.Unix(), N: b.Count}
	}
	jsonOK(w, map[string]any{
		"total":   total,
		"unique":  unique,
		"buckets": jb,
	})
}

func bucketStart(t time.Time, intervalSec int64) time.Time {
	return time.Unix((t.Unix()/intervalSec)*intervalSec, 0)
}

type bucket struct {
	Time  time.Time
	Count int
}

func fillBuckets(rows []models.VisitBucket, since time.Time, intervalSec int64, count int) []bucket {
	m := make(map[int64]int, len(rows))
	for _, r := range rows {
		m[r.Time.Unix()] = r.Count
	}
	out := make([]bucket, count)
	for i := 0; i < count; i++ {
		t := since.Add(time.Duration(i) * time.Duration(intervalSec) * time.Second)
		out[i] = bucket{Time: t, Count: m[t.Unix()]}
	}
	return out
}
