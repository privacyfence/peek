# Plan: API-token calls to page backends, token-call stats and `/data` export/import (issue #52)

## Goal

Issue: https://github.com/privacyfence/peek/issues/52.

Today a page backend can only be called from a browser through `/svc/<slug>/v/<n>/...` with a session cookie, so scheduled agents and other non-browser clients (PrivacyFence, `privacyfence-sync`, PrivacyFence Cockpit, Claude Code routines) can neither read nor write the data a backend collects, nor wake a backend on a schedule. After this change:

- An API token can call a page's backend through `ANY /api/uploads/{slug}/versions/{n|latest}/svc/{path...}` under the same access rule as `/svc`, once the page owner has turned token calls on for that page (`peek backend token-calls on <slug>` or `peek backend set ... --allow-token-calls`). Token calls are off for every existing and new page until the owner opts in.
- The backend learns that a call came from a token (`X-Peek-Caller: token`), and on signed-in and restricted pages also which account made it. Public-page backends only get a pseudonymous per-account id.
- Owners see token calls per caller and version in `peek stats <slug>` and `GET /api/uploads/{slug}/stats`, without inflating visit numbers.
- `peek backend call <slug> <path>` calls a backend from the shell with the stored login.
- Owners (and admins) can export and import a page's `/data` volume (`peek backend data export|import`), which `peekd backup` does not cover. Import replaces the volume and is capped by the new admin setting `max_backend_data_import` (default 256 MiB).
- The backend docs say that public visibility exposes the backend to anyone and that the backend must check `X-Peek-Backend-Token` itself.

## Current state

- `internal/server/backend_proxy.go:26` `handleBackendProxy` does everything inline: 404 when `s.backends == nil` (line 27), reads the body with a 1 MiB cap (`backendMaxBody`, lines 31-44), requires `X-Peek-Svc: 1` and a same-origin `Sec-Fetch-Site` (lines 46-53), rate limits with `s.backendLimiter.allow(s.rateLimitKey(r))` (line 54), loads the upload and parses `{n}` as a positive number only (lines 59-70), checks `s.viewerCanAccessUpload(r, u)` (line 71, cookie session only), loads `GetUploadBackend` (line 76), builds `s.backendSpec` and calls `s.backends.Ensure` (lines 85-94), computes the forwarded path with `strings.TrimPrefix(r.URL.EscapedPath(), "/svc/"+slug+"/v/"+rawVersion)` (line 97), forwards only `Content-Type` and `Accept` (line 106), sets `X-Peek-Backend-Token`, `X-Peek-Upload-Id`, `X-Peek-Version`, `X-Peek-Viewer` = `hmacSHA256(s.secret, "backend-viewer:"+uploadID+":"+vid)[:32]` from the `hn_vid` cookie (lines 111-118), and `X-Peek-User-Id`/`X-Peek-User-Email` from `s.webAuth(r)` only on signed-in/restricted pages (lines 119-126). Responses: 504 on timeout, 502 otherwise, 8 MiB cap (`backendMaxResponse`), `Cache-Control: no-store`, `nosniff`, `Content-Security-Policy: sandbox; default-src 'none'` (lines 128-165). The 30 s timeout is `s.backendClient`'s.
- `internal/server/routes.go:31-40` registers the token-gated backend management routes; `routes.go:47-51` registers `/svc/{slug}/v/{n}/{path...}` for GET/POST/PUT/PATCH/DELETE.
- `internal/server/api_backends.go:54` `backendUpload` (token, owner-or-admin, 403 `not owner`), `:73` `requireBackendsEnabled` (409), `:82` `backendVersion` (`latest` or positive number, 404 `version not found`), `:136` `handleGetBackend` (JSON `backendInfoOut{enabled, versions, env}`), audit via `s.auditRequest(r, owner.Name, "backend.set", auditDetail(...))`.
- `internal/server/middleware.go:24` `bodyLimitExempt` exempts only `/svc/` and the two upload POSTs from the global 64 KiB body cap; `:87` `authToken` puts a `*models.Token` (AccountID, Email, Name, IsAdmin) in the context, read with `requireAPIToken`. `internal/server/account_policy.go:45` `tokenAccount(t)` turns it into a `*models.Account`. `internal/server/access.go:18` `accountCanViewRestricted(ctx, a, u)`; `internal/server/comments.go:287` `viewerCanAccessUpload`.
- `internal/peekd/run.go:505-507`: `http.Server` has `ReadTimeout: 10s`, `WriteTimeout: 30s`. `internal/server/page_events.go:129-131` shows the `http.NewResponseController(w).SetWriteDeadline` pattern to lift that per handler; `statusRecorder.Unwrap` (middleware.go) makes it reach the real writer.
- `internal/server/api_stats.go:10` `handleStats` (owner or admin) returns `slug, name, total_visits, unique_visitors, recent`. `internal/cli/stats_comments_command.go:13` `cmdStats` prints it.
- Settings: `internal/server/settings.go:22` `settingsMeta`, `:149` `normalizeSettingValue`, `:385` `dashboardSettingUsesMegabytes`, `:429-435` the admin **Limits** tab rows (rendered generically by `internal/web/templates/admin.gohtml:256`); `internal/server/runtime_settings.go:10` `initDefaultSettings` seeds defaults; `settingInt64(ctx, key, def)`.
- Schema: migrations live in `internal/db/migrations.go:9` (last is 17, `provisioning`); the same DDL is also in the fresh-install schema `internal/db/schema.go` (the `upload_backends` block is at :162). `internal/db/backends.go` holds the backend store functions; `internal/db/migrations_test.go:321` `TestMigration16AddsUploadBackends` is the pattern for a migration test. `models.UploadBackend` is at `internal/models/models.go:126`.
- `internal/backends/runtime.go:12` `Runtime` interface; `internal/backends/docker.go:69` `do` always JSON-encodes a non-nil body; `:324` `CreateContainer` adds the fixed hardening (read-only rootfs, `/data` volume mount, caps dropped). `internal/backends/manager.go:148` `VolumeName` (`peek-<instance>-data-u<id>`, one per page), `:173` `Ensure`, `:265` `start` (image check/pull inline at the top), `:351` `StopUpload`, `:356` `RemoveUploadData`. `Start` (`:154`) removes every container labelled `peek.managed=true` + `peek.instance=<instance>`. `internal/backends/backendstest/fake.go` is the in-memory `FakeRuntime`. `internal/backends/docker_integration_test.go:20` `TestDockerIntegration` runs against real Docker in CI's `backend-integration` job (`-run '^TestDockerIntegration'`). The example image (`examples/contact-form/Dockerfile`) runs as `65532:65532` and ships an owned, empty `/data` so a fresh volume is writable.
- CLI: `internal/cli/backend_command.go` (`cmdBackend` dispatch, `parseBackendFlags`, `backendClient`, `backendVersionSegment`); `internal/cli/client.go:74` `httpClient` has a 30 s timeout, `:88` `req`, `:145` `decodeErrorResp`; tests in `internal/cli/backend_command_test.go` (`backendServer`, `runBackend` helpers).
- Docs: `docs/operations.md:121-146` "Page Backends" (volumes not in backup at :144, audited actions and relay limits at :146, body-limit sentence at :156, migration notes from :168), `README.md:128-140` (backend headers table) and `:169-175` (CLI list), `docs/src/content/docs/reference.md:95-100` (backend settings table), `:155-161` (CLI list), `:236` (`/svc` route row), `docs/src/content/docs/using-peek.md:62-89,189-192`, `skills/peek/SKILL.md:114-120`.
- There is no ADR directory in this project, so this plan has no ADR section.

## Design

Every decision below is final. The interview answers (decisions of the user) are marked with their question number.

### D1. Schema: migration 18 `backend_token_calls`

Add to `migrations` in `internal/db/migrations.go` (plain SQL migration, like 16 and 17) and append the same DDL to `internal/db/schema.go`:

```sql
CREATE TABLE IF NOT EXISTS upload_backend_settings (
	upload_id INTEGER PRIMARY KEY REFERENCES uploads(id) ON DELETE CASCADE,
	allow_token_calls INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS backend_token_calls (
	upload_id INTEGER NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	calls INTEGER NOT NULL DEFAULT 0,
	last_call_at INTEGER NOT NULL,
	last_status INTEGER NOT NULL,
	identified INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (upload_id, version, account_id)
);
CREATE INDEX IF NOT EXISTS idx_backend_token_calls_upload ON backend_token_calls(upload_id, last_call_at DESC);
```

Migration entry: `{18, "backend_token_calls", <the SQL above>}`.

- Q2 answer (opt-in per page): the flag is stored once per page, like `upload_backend_env`, so a new version keeps it. No row means off. `backend_token_calls` rows are keyed per version but reference only `uploads`, so pruning a version keeps its counts (visits are kept the same way).

New model in `internal/models/models.go` (next to `UploadBackend`):

```go
// BackendTokenCall counts one account's API-token calls to one page version's backend.
type BackendTokenCall struct {
	UploadID   int64
	Version    int
	AccountID  int64
	Email      string // the account's email; "" for automation accounts
	Calls      int64
	LastCallAt time.Time
	LastStatus int  // HTTP status the backend answered the last call with
	Identified bool // some call was made while the page was signed-in or restricted, so the backend got the account's identity
}
```

New store functions in `internal/db/backends.go`:

```go
// UploadBackendTokenCalls reports whether the page's owner allowed API-token calls to its backend. No row means false.
func (s *Store) UploadBackendTokenCalls(ctx context.Context, uploadID int64) (bool, error)
// SetUploadBackendTokenCalls upserts the page's token-call flag.
func (s *Store) SetUploadBackendTokenCalls(ctx context.Context, uploadID int64, enabled bool) error
// RecordBackendTokenCall adds one call by accountID to a page version's counter and stores its time and status.
// identified says whether this call sent the account's identity to the backend; once set it stays set.
func (s *Store) RecordBackendTokenCall(ctx context.Context, uploadID int64, version int, accountID int64, status int, identified bool) error
// ListBackendTokenCalls returns a page's token-call counters, newest last call first, at most limit rows.
func (s *Store) ListBackendTokenCalls(ctx context.Context, uploadID int64, limit int) ([]models.BackendTokenCall, error)
```

SQL:
- `UploadBackendTokenCalls`: `SELECT allow_token_calls FROM upload_backend_settings WHERE upload_id=?`; `sql.ErrNoRows` → `false, nil`.
- `SetUploadBackendTokenCalls`: `INSERT INTO upload_backend_settings(upload_id,allow_token_calls,updated_at) VALUES(?,?,?) ON CONFLICT(upload_id) DO UPDATE SET allow_token_calls=excluded.allow_token_calls, updated_at=excluded.updated_at` with `1`/`0` and `time.Now().Unix()`.
- `RecordBackendTokenCall`: `INSERT INTO backend_token_calls(upload_id,version,account_id,calls,last_call_at,last_status,identified) VALUES(?,?,?,1,?,?,?) ON CONFLICT(upload_id,version,account_id) DO UPDATE SET calls=calls+1, last_call_at=excluded.last_call_at, last_status=excluded.last_status, identified=MAX(identified, excluded.identified)` (`identified` as 1/0).
- `ListBackendTokenCalls`: `SELECT c.upload_id,c.version,c.account_id,COALESCE(a.email,''),c.calls,c.last_call_at,c.last_status,c.identified FROM backend_token_calls c LEFT JOIN accounts a ON a.id=c.account_id WHERE c.upload_id=? ORDER BY c.last_call_at DESC, c.version DESC, c.account_id LIMIT ?`.

### D2. Backends: `/data` export/import in `internal/backends`

Q6 answer: export/import is part of this change. Q8 answer: import **replaces** the volume. Q12 answer: both export and import stop the page's backends first, so a SQLite file is never copied or replaced mid-write.

**Runtime** (`internal/backends/runtime.go`), two new interface methods:

```go
CopyFromContainer(ctx context.Context, id, path string) (io.ReadCloser, error) // tar stream of path, as GET /containers/{id}/archive sends it
CopyToContainer(ctx context.Context, id, path string, archive io.Reader) error  // extracts a tar into path, keeping each entry's uid/gid
```

**Docker** (`internal/backends/docker.go`):
- `do`: when `body` is an `io.Reader`, send it as is and do not set `Content-Type: application/json`; any other non-nil body stays JSON-encoded as today. The caller passes `Content-Type` through `header`.
- `CopyFromContainer`: `GET /containers/{id}/archive` with query `path=<path>`; returns `resp.Body`.
- `CopyToContainer`: `PUT /containers/{id}/archive` with query `path=<path>` only, header `Content-Type: application/x-tar`, body `archive`; drain and close the response like `call`.
- No `copyUIDGID`: moby resolves the image's `User` (for example `65532:65532`) through the host's passwd for it and fails for users the host does not know. The Engine keeps the uid/gid of each tar header instead, so `ImportData` rewrites them (below).

**Archive format.** An archive Peek exports or accepts is a plain (uncompressed) tar whose entry names are relative to `/data` (what `tar -C <dir> -cf data.tar .` produces; a leading `./` is fine). The Engine's `GET .../archive?path=/data` stream instead prefixes every entry with `data/`, so export rewrites it.

New file `internal/backends/data.go`:

```go
// ErrDataBusy is returned while a page's /data volume is being exported or imported:
// Ensure refuses to start that page's backends, and a second export or import is refused.
var ErrDataBusy = errors.New("backend data export or import in progress")

// ArchiveError is an archive Peek refuses to import; its message is shown to the user.
type ArchiveError struct{ Msg string }
func (e *ArchiveError) Error() string { return e.Msg }

// DataHelperName is the name of the never-started container that mounts a page's
// /data volume for export and import.
func DataHelperName(instance string, uploadID int64) string // fmt.Sprintf("peek-%s-data-u%d-io", instance, uploadID)

// ValidateDataArchive reads a whole tar archive and returns an *ArchiveError for anything Peek does not import.
func ValidateDataArchive(r io.Reader) error

// ExportData stops the page's backends and writes its /data volume to w as a tar archive
// with entry names relative to /data. It returns the number of bytes written.
func (m *Manager) ExportData(ctx context.Context, spec Spec, w io.Writer) (int64, error)

// ImportData stops the page's backends, deletes the page's /data volume, and extracts
// archive into a fresh one, so the volume afterwards holds exactly the archive.
// The caller validates archive with ValidateDataArchive first.
func (m *Manager) ImportData(ctx context.Context, spec Spec, archive io.Reader) error
```

`ValidateDataArchive` rules, checked per header from `tar.NewReader(r).Next()` until `io.EOF` (reading each entry's content to the end with `io.Copy(io.Discard, tr)`):
- a read error other than `io.EOF` → `&ArchiveError{"not a valid tar archive"}`;
- `name := path.Clean(strings.TrimPrefix(h.Name, "./"))`; when `path.IsAbs(h.Name)`, or `name == ".."`, or `strings.HasPrefix(name, "../")` → `&ArchiveError{fmt.Sprintf("archive entry %q is outside /data", h.Name)}`;
- `h.Typeflag` not one of `tar.TypeReg`, `tar.TypeDir`, `tar.TypeSymlink` → `&ArchiveError{fmt.Sprintf("archive entry %q is not a file, directory or symlink", h.Name)}`;
- for `tar.TypeSymlink`: when `path.IsAbs(h.Linkname)`, or `path.Clean(path.Join(path.Dir(name), h.Linkname))` is `..` or starts with `../` → `&ArchiveError{fmt.Sprintf("archive entry %q links outside /data", h.Name)}`.
- An archive with no entries is valid (it empties `/data`).

`Manager` changes (`internal/backends/manager.go`):
- New field `dataBusy map[int64]chan struct{}` (under `m.mu`; the channel is closed when the operation ends), initialised in `NewManager`.
- `Ensure`: in the loop, right after the `m.closed` check and still holding `m.mu`, `if m.dataBusy[spec.UploadID] != nil { m.mu.Unlock(); return Endpoint{}, ErrDataBusy }`.
- `RemoveUploadData` (page delete, retention, GDPR) must not race a data operation, or the helper still mounting the volume makes `RemoveVolume` fail and the deleted page's volume stays forever. It becomes: `release, err := m.waitData(ctx, uploadID)` → `defer release()` → `StopUpload` → `m.rt.RemoveContainer(ctx, DataHelperName(...))` → `RemoveVolume`.
- Extract the image check/pull at the top of `start` into `func (m *Manager) ensureImage(ctx context.Context, image, registryAuth string) error` (same errors as today) and call it from `start`.
- In `data.go`: `func (m *Manager) claimData(uploadID int64) (func(), error)`: under `m.mu`, `errClosed` when closed, `ErrDataBusy` when `m.dataBusy[uploadID] != nil`, else store a new channel and return a release func that, under `m.mu`, deletes the entry and closes the channel.
- `func (m *Manager) waitData(ctx context.Context, uploadID int64) (func(), error)`: like `claimData`, but while another operation holds the page it unlocks, waits for that channel to close (or returns `ctx.Err()`), and tries again; it does not check `m.closed` (deleting a page must work during shutdown too).
- `func (m *Manager) withDataHelper(ctx context.Context, spec Spec, fn func(id string) error) error`: `ensureImage(ctx, spec.Image, spec.RegistryAuth)`; `RemoveContainer(ctx, DataHelperName(...))` (stale helper); `CreateContainer` with `ContainerSpec{Name: DataHelperName(m.instance, spec.UploadID), Image: spec.Image, Labels: {"peek.managed": "true", "peek.instance": m.instance, "peek.upload_id": <id>, "peek.role": "data-io"}, Network: m.network, Volume: VolumeName(m.instance, spec.UploadID), MemoryBytes: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs}` (the runtime adds the usual hardening); never start it; always remove it afterwards with `context.WithTimeout(context.WithoutCancel(ctx), orphanStopTimeout)`, logging a removal failure with `slog.Warn("backend data helper remove", ...)`; return `fn(id)`'s error.
- `ExportData`: `claimData` → `defer release()` → `StopUpload(ctx, spec.UploadID)` → `withDataHelper` whose fn reads `CopyFromContainer(ctx, id, "/data")` and rewrites it into `w` through `tar.NewWriter`: drop the entry named `data` or `data/`; strip the leading `data/` from every other `Name` (and from `Linkname` of `tar.TypeLink` entries); any entry not under `data/` → `fmt.Errorf("unexpected archive entry %q", h.Name)`; copy each entry's content; `Close` the writer. Count the bytes written to `w` with a small counting writer and return the count.
- `ImportData`: `claimData` → `defer release()` → `StopUpload` → `m.rt.RemoveContainer(ctx, DataHelperName(...))` (a helper left by an earlier failed removal would keep the volume in use) → `m.rt.RemoveVolume(ctx, VolumeName(...))` → `withDataHelper` whose fn (1) reads the owner of the fresh volume's root: `CopyFromContainer(ctx, id, "/data")`, the first header must be named `data` or `data/` (else `fmt.Errorf("unexpected archive entry %q", h.Name)`), take its `Uid`/`Gid`, close the stream; (2) calls `CopyToContainer(ctx, id, "/data", r)` where `r` is the read side of an `io.Pipe` fed by a goroutine that copies `archive` entry by entry through `tar.NewWriter`, setting every header's `Uid`/`Gid` to that owner and `Uname`/`Gname` to `""` (close the pipe with the goroutine's error). Creating the helper makes the Engine create a fresh volume, populated from the image's own `/data` directory, root ownership included (this is what makes it writable for a non-root image, see `examples/contact-form/Dockerfile`), so the helper always uses the page's latest backend image and the imported files get the same owner as `/data`.
- `Start` already removes leftover helpers (they carry `peek.managed` and `peek.instance`).

**Fake** (`internal/backends/backendstest/fake.go`):
- `Volumes map[string][]byte`: per volume name, the tar stream `CopyFromContainer` returns for a container mounting that volume, entries prefixed `data/` as the Engine sends them. A volume without an entry returns what the Engine sends for an empty volume: a tar with a single `data/` directory entry owned by `DataUID`/`DataGID`.
- `DataUID, DataGID int`: the owner of that default `data/` entry (zero value 0).
- `Imported map[string][]byte`: per volume name, the bytes the last `CopyToContainer` received.
- `Calls []string`: every runtime call in order, one line each: `"create <name>"`, `"start <id>"`, `"stop <id>"`, `"rmctr <id-or-name>"`, `"rmvol <name>"`, `"copyfrom <id> <path>"`, `"copyto <id> <path>"` (appended by the existing methods too, next to their current per-method slices, which stay).
- `ArchiveErr error`: fails both methods.
- Both methods return a 404 `notFound("container", id)` for an unknown container. `RemoveVolume` also deletes `Volumes[name]`. `NewFakeRuntime` initialises both maps.
- `CopyToContainer` reads the whole archive and stores the bytes in `Imported[volume]`.

### D3. Server: shared relay core and the token route

**Relay core** in `internal/server/backend_proxy.go`. Split `handleBackendProxy` into:

```go
// backendCaller is who a relayed call comes from, as the backend is told.
type backendCaller struct {
	kind    string          // "browser" or "token": the X-Peek-Caller value
	viewer  string          // X-Peek-Viewer value, already hashed; "" sends none
	account *models.Account // sent as X-Peek-User-Id/-Email on signed-in and restricted pages only; nil sends none
}

// readBackendBody reads a relay request body under backendMaxBody, writing the 413/400 itself.
func readBackendBody(w http.ResponseWriter, r *http.Request) ([]byte, bool)

// relayToBackend starts the page version's backend and relays one call to it. rest is the
// still-escaped path to forward, starting with "/". It writes the whole response and
// reports the backend's status and whether the call reached the backend.
func (s *Server) relayToBackend(w http.ResponseWriter, r *http.Request, u *models.Upload, version int, rest string, body []byte, caller backendCaller) (status int, relayed bool)

// escapedPathAfter returns the still-escaped path after the first n segments, starting with "/"
// ("/" when nothing follows). The decoded PathValue would turn %3F into "?".
func escapedPathAfter(r *http.Request, n int) string

// browserViewerHash and tokenCallerHash are the X-Peek-Viewer values of the two caller kinds.
func (s *Server) browserViewerHash(uploadID int64, viewerID string) string // hmacSHA256(s.secret, "backend-viewer:"+id+":"+viewerID)[:32], unchanged
func (s *Server) tokenCallerHash(uploadID, accountID int64) string         // hmacSHA256(s.secret, "backend-token-caller:"+id+":"+accountID)[:32]
```

`relayToBackend` holds lines 76-165 of today's handler unchanged in behaviour (backend lookup with 404 `this page version has no backend`, `backendSpec`, `Ensure` with 502 `backend failed to start`, forward `Content-Type`/`Accept`, `X-Peek-Backend-Token`, `X-Peek-Upload-Id`, `X-Peek-Version`, response handling and headers), plus:
- it sets `X-Peek-Caller: <caller.kind>` on every relayed request;
- it sets `X-Peek-Viewer: <caller.viewer>` when non-empty;
- it sets `X-Peek-User-Id`/`X-Peek-User-Email` from `caller.account` only when `u.Visibility` is `signed-in` or `restricted` (the exact condition of today's line 119; email only when non-empty);
- it sets the response header `X-Peek-Relay: backend` on every response that came from the backend (next to `Cache-Control`), so a client can tell a backend's error status from Peek's own; it returns `(resp.StatusCode, true)` then, and `(0, false)` for every response Peek wrote itself.
- `escapedPathAfter(r, 4)` replaces the `TrimPrefix` of line 97 for `/svc/{slug}/v/{n}/...`.

`handleBackendProxy` keeps its order: backends-nil 404 → `readBackendBody` → `X-Peek-Svc`/`Sec-Fetch-Site` checks → rate limit → upload/version parsing → `viewerCanAccessUpload` → `relayToBackend(..., escapedPathAfter(r, 4), body, backendCaller{kind: "browser", viewer: <browserViewerHash when rawViewerID(r) != "">, account: <s.webAuth(r) account when ok>})`. The only browser-visible changes are the new `X-Peek-Caller: browser` request header and the `X-Peek-Relay: backend` response header.

**Token route**, new file `internal/server/backend_token_proxy.go`, `func (s *Server) handleBackendTokenCall(w http.ResponseWriter, r *http.Request)`, registered in `routes.go` (after the backend management routes) for each of GET, POST, PUT, PATCH, DELETE as `"<METHOD> /api/uploads/{slug}/versions/{n}/svc/{path...}"` wrapped in `s.authToken`. Order:
1. `tok, ok := requireAPIToken(w, r)`.
2. `s.backends == nil` → 404 `backends are not enabled on this server`.
3. `readBackendBody`.
4. Rate limit: `s.backendLimiter.allow(s.rateLimitKey(r))` → 429 `too many backend requests, slow down`.
5. `s.store.GetUpload(ctx, slug)` → 404 `not found`.
6. `s.tokenCanCallBackend(ctx, tok, u)` false → 403 `access denied` (before the version lookup, so a token without access cannot probe which versions exist).
7. `version, ok := s.backendVersion(w, r, u)` (accepts `latest`; 404 `version not found`).
8. `s.store.UploadBackendTokenCalls(ctx, u.ID)`: error → 500 `db error`; false → 403 `token calls are not enabled for this backend`.
9. `status, relayed := s.relayToBackend(w, r, u, version, escapedPathAfter(r, 6), body, backendCaller{kind: "token", viewer: s.tokenCallerHash(u.ID, tok.AccountID), account: tokenAccount(tok)})`.
10. When `relayed`: `s.store.RecordBackendTokenCall(context.WithoutCancel(r.Context()), u.ID, version, tok.AccountID, status, identified)` where `identified` is `u.Visibility` signed-in or restricted (the condition under which the identity headers were sent); an error is logged with `slog.Warn("backend token call not recorded", "slug", u.Slug, "err", err)` and never changes the response.

```go
// tokenCanCallBackend applies the /svc access rule to the token's account: public pages
// to any token, signed-in pages to any active account, restricted pages to the owner,
// admins and grantees (accountCanViewRestricted).
func (s *Server) tokenCanCallBackend(ctx context.Context, t *models.Token, u *models.Upload) bool
```
Unknown visibility → false. The account's active state is already checked by `authToken`.

No `X-Peek-Svc` / `Sec-Fetch-Site` checks on this route: it is not cookie-authenticated, so CSRF does not apply (issue text).

**Identity headers (Q13 answer: same as the browser path).** On public pages the token route sends `X-Peek-Caller: token` and `X-Peek-Viewer` = `tokenCallerHash`, and no `X-Peek-User-*`. On signed-in and restricted pages it also sends `X-Peek-User-Id` and (when the account has one) `X-Peek-User-Email` of the token's account. The answer's option text named the hash input `backend-viewer:<upload>:acct-<id>`; the plan uses the separate HMAC domain `backend-token-caller:<upload>:<account id>` instead, because the browser viewer id comes from an unvalidated cookie (`rawViewerID`, `internal/server/pages.go:157`), so a browser could set `hn_vid=acct-5` and pose as account 5's token. The decision itself (a pseudonymous, per-page, account-derived id) is unchanged.

**Calls refused by Peek are not counted** (403 off/denied, 404, 429, start failures); calls the backend answered with any status are.

**Body limit** (`internal/server/middleware.go`): `bodyLimitExempt` becomes exactly (the data branch is added by p4, see D5):

```go
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
```

### D4. Server: the opt-in API

- Route: `PUT /api/uploads/{slug}/backend/token-calls` → `s.authToken(s.handleSetBackendTokenCalls)` in `api_backends.go`.
- Body: `{"enabled": true|false}` decoded with `decodeJSON(w, r, &body, smallJSONBodyLimit)` into `struct{ Enabled *bool \`json:"enabled"\` }`; a decode error → 400 `bad json`; `Enabled == nil` → 400 `enabled must be true or false`.
- Access: `backendUpload` (owner or admin) and `requireBackendsEnabled` (409), like `handleSetBackend`.
- Effect: `SetUploadBackendTokenCalls`; db error → 500 `db error`; audit `s.auditRequest(r, owner.Name, "backend.token_calls", auditDetail("slug", u.Slug, "enabled", strconv.FormatBool(enabled)))`; response `{"status":"ok","token_calls":<enabled>}`.
- `backendInfoOut` gains `TokenCalls bool \`json:"token_calls"\``, filled by `handleGetBackend` from `UploadBackendTokenCalls` (db error → 500 `db error`).
- The browser `/svc` path ignores the flag.
- The audit log records only these on/off changes; there is no per-call audit entry (Q4 answer).

### D5. Server: `/data` export and import routes, and the size setting

- Q10 answer: new runtime setting `max_backend_data_import`, default 256 MiB.
  - `settingsMeta`: `"max_backend_data_import": {Label: "Max backend data import (bytes)", Description: "Largest /data archive an owner can import into a page backend"}`.
  - `normalizeSettingValue`: `case "max_upload", "max_backend_data_import": return normalizePositiveInt64Setting(key, value)` (extend the existing `max_upload` case).
  - `dashboardSettingUsesMegabytes`: add `"max_backend_data_import"` to the megabyte keys.
  - Limits tab (`dashboardSettingsPanel`), after `max_storage_per_token`: `dashboardMegabyteLimit(raw, "max_backend_data_import", "maxBackendDataImport", "Backend data import", "Largest /data archive an owner can import into a page backend", 1, maxInt64(10240, dashboardSettingMegabytes(raw, "max_backend_data_import")), 1)`.
  - `internal/server/runtime_settings.go`: `const defaultBackendDataImportMax int64 = 256 << 20`, and `"max_backend_data_import": strconv.FormatInt(defaultBackendDataImportMax, 10)` in `initDefaultSettings`' `defaults`.
- New file `internal/server/backend_data.go`:

```go
const (
	backendDataTransferTimeout = 30 * time.Minute // read/write deadline of one export or import request
	backendDataImportTimeout   = 10 * time.Minute // bound of the import itself, detached from the client
)

// latestBackendSpec returns the spec of the page's highest-version backend, writing 404
// "this page has no backend" (or 500 "db error") itself.
func (s *Server) latestBackendSpec(w http.ResponseWriter, r *http.Request, u *models.Upload) (backends.Spec, bool)
func (s *Server) handleExportBackendData(w http.ResponseWriter, r *http.Request)
func (s *Server) handleImportBackendData(w http.ResponseWriter, r *http.Request)
```

`latestBackendSpec`: `ListUploadBackends` (ordered by version) → last row → `s.backendSpec(ctx, &row)`; a spec error → 500 `could not read backend settings`.

Routes (`routes.go`): `GET /api/uploads/{slug}/backend/data` → `s.authToken(s.handleExportBackendData)`; `PUT /api/uploads/{slug}/backend/data` → `s.authToken(s.handleImportBackendData)`. There is no `{n}`: the volume belongs to the page.

**Export** (`handleExportBackendData`): `backendUpload` → `requireBackendsEnabled` → `latestBackendSpec` → lift both deadlines with `rc := http.NewResponseController(w)`, `rc.SetWriteDeadline(time.Now().Add(backendDataTransferTimeout))` (ignore `http.ErrNotSupported`, as `page_events.go` does) → `s.backends.ExportData(r.Context(), spec, lw)` where `lw` is a writer that, on its first `Write`, sets `Content-Type: application/x-tar`, `Content-Disposition: attachment; filename="<slug>-data.tar"`, `Cache-Control: no-store` and `WriteHeader(200)`. Errors:
- nothing written yet: `errors.Is(err, backends.ErrDataBusy)` → 409 `another data export or import is running for this page`; anything else → log `slog.Error("backend data export failed", "slug", ..., "err", err)` and 502 `could not export backend data`;
- after the first byte: log the same line and `panic(http.ErrAbortHandler)` so the client sees a broken download, not a short tar.
- Success: audit `backend.data.export` with `auditDetail("slug", u.Slug, "bytes", strconv.FormatInt(n, 10))`.

**Import** (`handleImportBackendData`): `backendUpload` → `requireBackendsEnabled` → `latestBackendSpec` → `limit := s.settingInt64(ctx, "max_backend_data_import", defaultBackendDataImportMax)`; `r.ContentLength > limit` → 413 `archive too large` → `rc.SetReadDeadline(time.Now().Add(backendDataTransferTimeout))` and `rc.SetWriteDeadline(time.Now().Add(backendDataTransferTimeout + backendDataImportTimeout))` (ignore `http.ErrNotSupported`; the server's 30 s `WriteTimeout` starts when the headers are read, so without this a long import would lose its response) → spool the body to `os.CreateTemp(s.dataDir, ".backend-data-import-*.tar")` (always closed and removed with `defer`) with `io.Copy(tmp, http.MaxBytesReader(w, r.Body, limit))`: `*http.MaxBytesError` → 413 `archive too large`, other errors → 400 `bad request body`; 0 bytes → 400 `empty archive` → seek to 0, `backends.ValidateDataArchive(tmp)`: `*backends.ArchiveError` → 400 with its message, other error → 400 `not a valid tar archive` → seek to 0 → `ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), backendDataImportTimeout)` → `s.backends.ImportData(ctx, spec, tmp)`: `ErrDataBusy` → 409 `another data export or import is running for this page`, other → log `slog.Error("backend data import failed", ...)` and 502 `could not import backend data` → audit `backend.data.import` with `auditDetail("slug", u.Slug, "bytes", <n>)` → `jsonOK(w, map[string]any{"status": "ok", "bytes": n})`.

The existing data is only touched after the whole archive was received and validated, so an oversized, aborted or malformed upload never changes the volume. A failure inside `ImportData` after the old volume was removed leaves `/data` empty or partial; the CLI warns and suggests an export first (D7), and the docs say so.

`bodyLimitExempt` gets the data branch before the final `return`:
```go
	if r.Method == http.MethodPut && len(seg) == 6 && seg[1] == "api" && seg[2] == "uploads" && seg[4] == "backend" && seg[5] == "data" {
		return true // data import; the handler applies max_backend_data_import
	}
```

**Busy backends on the relay**: in `relayToBackend`, an `Ensure` error with `errors.Is(err, backends.ErrDataBusy)` → 503 `backend data is being exported or imported, try again shortly` (before the generic 502 branch, without the `backend start failed` log line).

Access to export/import is owner or admin (`backendUpload`), behind `requireBackendsEnabled`, like every other backend management route. Export has no size limit.

### D6. Server: token calls in stats

Q4 answer: a separate counter, shown to owners in stats. Q13-stats answer: the email is shown under the same rule as the identity headers.

`handleStats` (`internal/server/api_stats.go`) adds `"token_calls"` to its JSON: always an array (empty when none), from `ListBackendTokenCalls(ctx, u.ID, 100)` (db error → 500 `db error`), each element:

```go
type tokenCallRow struct {
	Caller     string `json:"caller"`          // s.tokenCallerHash(u.ID, c.AccountID): the X-Peek-Viewer the backend received
	Email      string `json:"email,omitempty"` // only when shown, see below
	Version    int    `json:"version"`
	Calls      int64  `json:"calls"`
	LastCallAt int64  `json:"last_call_at"` // unix seconds
	LastStatus int    `json:"last_status"`
}
```

`Email` is filled when `c.Identified` is true (some call by that account to that version was made while the page was signed-in or restricted, so the backend already got the email), or when the requesting token is an admin's (`owner.IsAdmin`); otherwise it is left empty and omitted. Using the visibility at call time, not the current one, keeps an owner from switching a public page to signed-in to see who called it while it was public. Visit numbers (`total_visits`, `unique_visitors`, `recent`) are unchanged; `visits` stays human-only.

### D7. CLI

All in `internal/cli`. Usage string: `const backendUsage = "usage: peek backend <slug> | set|remove|env|restart|logs|call|token-calls|data ..."`.

**HTTP helper** (`client.go`):
```go
// longHTTPClient has no overall timeout: backend calls (cold starts) and /data transfers
// can outlast httpClient's 30 s, and the server bounds both itself.
var longHTTPClient = &http.Client{}

// reqWith sends one request through hc with the bearer token. size is the body length
// (-1 when unknown); header values are set on the request.
func (c *client) reqWith(hc *http.Client, method, path string, body io.Reader, size int64, header http.Header) (*http.Response, error)
```

**Flags** (`parseBackendFlags`), new entries: `--allow-token-calls` (boolean, like `--value-stdin`; field `allowTokenCalls bool`), `--method <m>` (`method string`), `--data <v>` (`data string`, `dataSet bool`), `-H <"Name: value">` (repeatable, `headers []string`). p7 adds `-o <file>` (`output string`) and `--yes` (boolean, `yes bool`).

**`peek backend set ... --allow-token-calls`**: `cmdBackendSet` accepts the flag (usage `peek backend set <slug> <image> [--version <n>|latest] [--port <n>] [--allow-token-calls]`); after the set succeeds and its line is printed, it sends `PUT /api/uploads/<seg>/backend/token-calls` with `{"enabled":true}` and prints `token calls enabled for <slug>`.

**`peek backend token-calls on|off <slug>`** (`cmdBackendTokenCalls`): usage `peek backend token-calls on|off <slug>`; `PUT /api/uploads/<seg>/backend/token-calls` with `{"enabled":true|false}`; prints `token calls enabled for <slug>` or `token calls disabled for <slug>`.

**`peek backend <slug>`** (`cmdBackendShow`): decodes `token_calls` and, when backends are enabled, prints `token calls: on` or `token calls: off` after the versions table (or after `no backend`), before `ENV`.

**`peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]...`** (`cmdBackendCall`), Q11 answer:
- Version: `backendVersionSegment(f.version)` (default `latest`).
- Method: default `GET`, or `POST` when `--data` is given; uppercase the value; not one of GET, POST, PUT, PATCH, DELETE → error `--method must be GET, POST, PUT, PATCH or DELETE`.
- Path: strip one leading `/`; split off the first `?` as the raw query (passed through unchanged); split the rest on `/`; a segment `.` or `..` → error `path must not contain . or .. segments`; escape each segment with `url.PathEscape` and join with `/`. URL: `/api/uploads/<seg>/versions/<ver>/svc/<escaped path>` plus `?<query>` when present.
- Body: `--data @file` reads the file, `--data -` reads stdin, anything else is the literal value; at most 1 MiB is read (`io.LimitReader(.., 1<<20+1)`), more → error `--data is larger than 1 MiB`.
- Headers: each `-H` is `Name: value`; a value without `:` → error `-H must be "Name: value"`; a name other than `Content-Type` or `Accept` (case-insensitive) → error `only Content-Type and Accept headers are passed to the backend`. With `--data` and no `Content-Type` header, send `Content-Type: application/json`.
- Sent with `c.reqWith(longHTTPClient, ...)`.
- Output: a 2xx response's body goes to stdout unchanged, exit 0. A non-2xx response with `X-Peek-Relay: backend`: body to stdout unchanged, then return `fmt.Errorf("backend returned %s", resp.Status)`. A non-2xx response without that header (Peek's own error): return `decodeErrorResp(resp)` (for example `token calls are not enabled for this backend`), nothing on stdout.

**`peek stats <slug>`** (`cmdStats`): decodes `token_calls` and, when non-empty, prints after the visits:
```
\ntoken calls:
  CALLER                            VERSION  CALLS   LAST CALL         STATUS
  privacyfence-bot@example.com      3        42      2026-11-04 07:00  200
  caller 3f9a12bc…                  3        7       2026-11-03 18:12  500
```
using `fmt.Printf("  %-32s  %-7s  %-6s  %-16s  %s\n", "CALLER", "VERSION", "CALLS", "LAST CALL", "STATUS")` for the header and `fmt.Printf("  %-32s  %-7d  %-6d  %-16s  %d\n", who, version, calls, when, status)` per row, where `who` is the email when present, else `"caller " + caller[:8] + "…"` (whole `caller` when shorter than 8), and `when` is `time.Unix(last_call_at, 0).Format("2006-01-02 15:04")`.

**`peek backend data export <slug> [-o <file>]`** and **`peek backend data import <slug> <file> [--yes]`** (`cmdBackendData` in new file `internal/cli/backend_data_command.go`, dispatched as `case "data"` from `cmdBackend`), usage `usage: peek backend data export <slug> [-o <file>] | import <slug> <file> [--yes]`:
- export: prints `stopping backends for <slug> to take a consistent copy` to stderr first (Q12 answer); output file defaults to `<slug>-data.tar` in the current directory, `-o -` writes to stdout; a file is opened with `os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)` (exists → error `<file> already exists`); `GET /api/uploads/<seg>/backend/data` with `reqWith(longHTTPClient, ...)`; non-2xx → `decodeErrorResp`, and the half-written file is removed; on a copy error the file is removed too; success prints `wrote <n> bytes to <file>` to stderr (not for `-o -`).
- import: opens `<file>` (errors as returned by `os.Open`); unless `--yes`: when `stdinIsTerminal()` is false → error `refusing to replace /data of <slug> without confirmation; pass --yes`; else prints to stderr `This replaces all of /data for <slug>: files that are not in <file> are deleted. Run 'peek backend data export <slug>' first to keep a copy. Continue? [y/N] ` and reads one line from `confirmInput`; `y` or `yes` (case-insensitive, trimmed) continues, anything else → error `import cancelled`. Then `PUT /api/uploads/<seg>/backend/data` with the file as body, its size from `Stat`, header `Content-Type: application/x-tar`, through `reqWith(longHTTPClient, ...)`; non-2xx → `decodeErrorResp`; success decodes `{"bytes":n}` and prints `imported <n> bytes into /data of <slug>`.
- Test seams (package vars in `backend_data_command.go`): `var stdinIsTerminal = func() bool { fi, err := os.Stdin.Stat(); return err == nil && fi.Mode()&os.ModeCharDevice != 0 }` and `var confirmInput io.Reader = os.Stdin`.
- No `--merge` mode (Q8 answer).

### D8. Docs and changelog

Docs and the changelog line are written in the retirement phase (p8), with the exact contents listed in its brief. Upgrade note for migration 18 in `docs/operations.md` → "Upgrades And Rollbacks", next to the migration 17 paragraph: `Migration 18 (backend_token_calls) adds the upload_backend_settings and backend_token_calls tables. Older binaries ignore both, so after a rollback token calls to backends stop working (the route does not exist) and the counters stay in the database until the upgrade is re-applied.`

### D9. What was rejected

- Opt-in per version (Q2 option 2) and always-on (Q2 option 3): rejected by the user; per page keeps the flag across versions and keeps existing backends closed to tokens.
- A per-call audit entry or counting token calls as visits (Q4 options 2-4): rejected; the audit log is admin-only and per-call rows grow with every poll; visits would inflate page views.
- `/data` merge import (Q8 options 2-3): rejected; stale SQLite `-wal`/`-journal` files could corrupt an imported database.
- A fixed import limit or none (Q10 options 2-3): rejected for an admin setting.
- Sending identity headers on public pages, or nothing extra (Q13 options 2-3), and always showing emails in stats, or no identities at all (Q13-stats options 2-3): rejected.
- Hashing the token caller with the browser's `backend-viewer:` domain: rejected, it is forgeable through the `hn_vid` cookie (see D3).
- Starting the helper container or running a shell in it to tar `/data`: rejected; the Engine's archive API works on a created, never-started container and needs nothing from the image.
- `copyUIDGID=1` on the Engine's archive PUT: rejected; moby looks the image's user up in the host's passwd and fails for numeric or container-only users (D2).
- Streaming the import body straight into the Engine: rejected; the old data would be deleted before the upload is known to be complete and valid.

## Manual steps

- `manual_before`: none. Docker is already required for backends; every phase builds and tests against `backendstest.FakeRuntime`, and CI's `backend-integration` job runs the new real-Docker test.
- `manual_after`: one item, also written to `after.md` next to this plan.
  1. **ma1-real-docker-smoke**: on a machine with Docker, build the branch, run peekd with backends on, and exercise token calls and `/data` export/import end to end against the example backend:
     1. `mise install && mise run build`.
     2. `docker build -t localhost/peek-example-contact:local examples/contact-form`.
     3. Start peekd with backends: `rm -rf data && PEEK_BACKENDS=docker ./bin/peekd --addr :7700 --data ./data --base-url http://localhost:7700`. Open http://localhost:7700/setup and create the first admin, then on http://localhost:7700/admin → **Backends** set **Allowed backend images** to `localhost/` and save. Docs: https://github.com/privacyfence/peek/blob/main/docs/operations.md#page-backends.
     4. `./bin/peek login --host http://localhost:7700`, then `./bin/peek upload examples/contact-form/index.html --visibility signed-in --backend localhost/peek-example-contact:local` and note the slug it prints.
     5. `./bin/peek backend call <slug> /api/healthz` → expect the error `token calls are not enabled for this backend`.
     6. `./bin/peek backend token-calls on <slug>`, then `./bin/peek backend call <slug> /api/healthz` → expect `ok`. `./bin/peek backend <slug>` shows `token calls: on`; `./bin/peek stats <slug>` lists your email with 1 call and status 200.
     7. `./bin/peek backend data export <slug> -o before.tar` (stderr shows the stopping notice), then `mkdir d && echo hi > d/hello.txt && tar -C d -cf in.tar .` and `./bin/peek backend data import <slug> in.tar --yes`; `./bin/peek backend data export <slug> -o after.tar && tar -tvf after.tar` lists `hello.txt` owned by uid 65532 (the numeric owner column, `tar --numeric-owner -tvf after.tar`) and nothing from `before.tar` that `in.tar` lacks.
     8. `./bin/peek backend call <slug> /api/healthz` → `ok` again (the backend cold-starts after the import).
     **Report:** `ma1 real-docker smoke: PASS (token call refused while off, ok when on, stats row shown, export/import round trip replaced /data)` or `FAIL at step <n>: <what happened>`.

## Risks and open questions

Why there are no more questions (from the interview):
- Scope is settled by the answers: token route + per-page opt-in (Q2), token-call counter (Q4), `/data` export/import included (Q6), CLI `peek backend call` (Q11). SaaS work is excluded by AGENTS.md.
- Behaviour: access follows the existing `/svc` rule (`viewerCanAccessUpload` / `accountCanViewRestricted`); limits, timeouts and response headers reuse the shared relay core. Import replaces the volume (Q8) with confirmation or `--yes`. Export and import stop the page's backends first (Q12). Export/import access is owner or admin via `backendUpload`.
- Names and strings, routes, CLI commands and flags, audit actions (`backend.token_calls`, `backend.data.export`, `backend.data.import`), the setting key `max_backend_data_import`, error texts, the `X-Peek-Caller` values, the export notice and the stats `caller`/`email` fields are fixed in Design following existing patterns.
- Data and schema: migration 18 adds `upload_backend_settings` and `backend_token_calls`; the runtime setting is seeded like the other limits (Q10).
- Security: token calls are off by default (Q2); authorization under the `/svc` rule; identity headers follow the browser visibility rule (Q13); stats show caller emails under the same rule and a pseudonymous hash otherwise, with emails visible to admins through their existing stats access (Q13-stats); CSRF headers are dropped only on the bearer route; the relay forwards only Content-Type and Accept; imports are capped and spooled before touching data.
- Prerequisites/credentials: none; Docker is already required for backends, and tests use `backendstest.FakeRuntime`.
- After-merge steps: the CHANGELOG `## [Unreleased]` entry and docs in the retirement phase; `mise run check-full` because `internal/server`, `internal/db` and `internal/backends` change; a real-Docker smoke test of token calls and data export/import is a `manual_after` item needing no user decision.

Risks a worker should recognise, and stop with `status=blocked` on:
- **Docker archive semantics (p2).** The design assumes that `GET /containers/{id}/archive?path=/data` on a created, never-started container returns entries prefixed `data/` with the `data/` root first; that `PUT ...?path=/data` into the `/data` volume works despite `ReadonlyRootfs: true` and keeps each header's uid/gid; and that creating the helper creates a removed volume again and fills it, root ownership included, from the image's `/data`. `TestDockerIntegrationDataExportImport` checks all of this. CI's `ci.yml` cannot be dispatched (only `pages.yml` is in `ci.dispatchable`), so its `backend-integration` job first runs when the PR opens; p2 runs the same command locally when `docker info` works and otherwise reports `integration not run` in its result, and `final_checks` requires the job to be green on the PR. If the test fails on one of these assumptions (for example `container rootfs is marked read-only`, entries not under `data/`, a root or file uid other than 65532), stop: the design must change, do not work around it in the test. Podman's compatible API is assumed to behave the same; it is not tested in CI.
- **Page delete waits for a data operation.** `RemoveUploadData` waits for a running export or import of that page (D2), so a delete during a long download waits until it ends or the delete's context ends. That is the price of not leaking the volume.
- **Forgeable viewer id (decision taken).** The Q13 option text named the hash input `backend-viewer:<upload>:acct-<id>`; the plan uses a separate `backend-token-caller:` HMAC domain so a browser cookie cannot impersonate a token caller (D3). The behaviour the user chose is unchanged.
- **Relay refactor (p3).** `relayToBackend` must keep every existing `TestBackendProxy*` test passing unchanged except for added assertions. If an existing test needs its expectations changed (other than the two new headers), stop.
- **Server timeouts.** `WriteTimeout: 30s` still bounds token calls that cold-start a backend (the same as `/svc` today); only the data routes lift the deadlines. If the export/import tests show `SetReadDeadline`/`SetWriteDeadline` returning something other than nil or `http.ErrNotSupported` through `statusRecorder`, stop.
- **Partial import.** A failure inside `ImportData` after the old volume is removed leaves `/data` empty or partial. This is accepted (Q8 chose replace); the CLI prompt and the docs tell owners to export first.
- **Export holds backends down.** While a download runs, `Ensure` for that page answers `ErrDataBusy` (503 on the relays). That is the price of a consistent copy (Q12), documented in operations.md.
- **Seeded default setting (p4).** Seeding `max_backend_data_import` adds a row to every database. Tests that compare the exact list of seeded or returned settings must be updated to include it; if a test outside `internal/server` breaks because of it, stop.
- **Coverage floors.** `scripts/coverage-gate.sh` requires 95% total and 93% for `internal/server` and `internal/db`. Each phase tests its error branches (db errors via the existing fault helpers, `failOn` in `internal/db`), and `mise run check-full` must pass at the end of each phase that touches those packages.
- **Path escaping.** `escapedPathAfter` counts segments of `r.URL.EscapedPath()`; a slug is a single segment, so the count is fixed. If a test shows an escaped slash (`%2F`) in a slug shifting the segments, stop.
- **Plan review notes.** An independent review (Opus, fresh context) raised 17 findings; all were applied. Two were applied differently from the reviewer's suggestion: (a) instead of adding a `./` root entry to imported archives, the root of `/data` keeps the owner the Engine gives it when it recreates the volume from the image, and the integration test asserts that owner is 65532; (b) instead of a "remove after release" flag, `RemoveUploadData` waits for the running data operation (`waitData`), which is simpler to test and cannot drop the removal.

## Implementation manifest

```yaml
plan_slug: backend-token-calls
feature_branch: feat/backend-token-calls
tracking_issue: 52
max_parallel: 3
manual_after:
  - id: ma1-real-docker-smoke
    title: Real-Docker smoke test of token calls and /data export/import with the example backend
    why: CI exercises the server only against the fake runtime and the data path only at the backends package; this checks the whole chain (CLI, server, manager, Docker) on a real engine, including a cold start after import.
verify_after_merge:
  - go test -count=1 -run 'TokenCall|Migration18' ./internal/db
  - go test -count=1 -run 'DataArchive|ExportData|ImportData|DataBusy|DataOps|RemoveUploadData' ./internal/backends/...
  - go test -count=1 -run 'BackendProxy|BackendTokenCall|BackendAPI|BackendData|Stats|BodyLimitExempt|Settings' ./internal/server
  - go test -count=1 -run 'Backend|Stats|ReqWith' ./internal/cli
final_checks:
  - "CHANGELOG.md has the new lines under '## [Unreleased]' and no new version heading"
  - "grep -n 'X-Peek-Caller' README.md docs/src/content/docs/using-peek.md docs/operations.md examples/contact-form/README.md finds the header in each file"
  - "grep -n 'max_backend_data_import' docs/operations.md docs/src/content/docs/reference.md finds the setting in both"
  - "grep -n 'backend data export' README.md docs/src/content/docs/reference.md docs/src/content/docs/using-peek.md skills/peek/SKILL.md finds the command in each file"
  - "the PR's backend-integration job (which runs TestDockerIntegrationDataExportImport against real Docker) is green; this is the first CI run of that test"
  - "mise run check-full passes on the feature branch"
phases:
  - id: p1-db-token-calls
    title: Migration 18 and store functions for the token-call opt-in and counters
    depends_on: []
    complexity: S
    touches:
      - internal/db/migrations.go
      - internal/db/schema.go
      - internal/db/backends.go
      - internal/db/backends_test.go
      - internal/db/migrations_test.go
      - internal/models/models.go
    brief: |
      Implement Design D1 of the plan exactly (tables, the `identified` column, model, signatures, SQL).
      1. internal/db/migrations.go: append `{18, "backend_token_calls", <SQL>}` to `migrations` with the SQL from D1 (both tables and the index). It runs through the generic `m.sql != ""` branch; add no special case.
      2. internal/db/schema.go: append the same DDL (both CREATE TABLE statements and the CREATE INDEX) after the provisioning tables, inside the schema string.
      3. internal/models/models.go: add `BackendTokenCall` exactly as in D1, after `BackendEnvVar`.
      4. internal/db/backends.go: add `UploadBackendTokenCalls`, `SetUploadBackendTokenCalls`, `RecordBackendTokenCall` and `ListBackendTokenCalls` with the signatures, doc comments and SQL from D1. Scan `last_call_at` with `time.Unix(ts, 0)` and `identified` as an int into the bool.
      5. internal/db/migrations_test.go: add `TestMigration18AddsBackendTokenCalls` copying `TestMigration16AddsUploadBackends` (`migCovPrepare(t, 18, "DROP TABLE backend_token_calls", "DROP TABLE upload_backend_settings")`, then check `schema_migrations` has id 18 once and both tables exist).
      6. internal/db/backends_test.go: add exactly these tests: `TestUploadBackendTokenCallsDefaultsOff` (no row → false, nil); `TestSetUploadBackendTokenCallsUpsert` (on, off, on again); `TestRecordBackendTokenCallCounts` (two calls by account A on v1, statuses 200 then 500, identified false then true → calls=2, last_status=500, Identified=true; a third call with identified false keeps Identified=true; one call by account B on v2; `ListBackendTokenCalls(ctx, id, 100)` returns B's row first when its call is newer — set `last_call_at` with `s.Exec` to make the order deterministic — and carries each account's email; an automation account without email gives Email ""); `TestListBackendTokenCallsLimit` (limit 1 → one row); `TestBackendTokenCallsCascadeOnUploadDelete` (deleting the upload removes the rows of both tables); `TestBackendTokenCallStoreErrors` (each of the four functions returns an error when its table fails, using the existing `failOn` helper from internal/db/faults_test.go as provisioned_accounts_test.go does).
      7. Run `mise run fmt-check`, `mise run vet`, `mise run test`, then `mise run check-full` (this phase touches internal/db). Coverage of internal/db must stay at or above 93%.
      Stop with status=blocked if `migCovPrepare` cannot prepare migration 18 the same way as 16/17, or if internal/db/release_fixture_test.go fails because of the new migration.
    acceptance:
      - "go test -count=1 -v -run '^(TestMigration18AddsBackendTokenCalls|TestUploadBackendTokenCallsDefaultsOff|TestSetUploadBackendTokenCallsUpsert|TestRecordBackendTokenCallCounts|TestListBackendTokenCallsLimit|TestBackendTokenCallsCascadeOnUploadDelete|TestBackendTokenCallStoreErrors)$' ./internal/db | grep -c '^--- PASS' prints 7"
      - "grep -n 'backend_token_calls' internal/db/schema.go internal/db/migrations.go finds the table in both files"
      - "mise run check-full passes"
  - id: p2-backends-data-io
    title: Runtime archive methods, Manager ExportData/ImportData with the busy guard, and the fake
    depends_on: []
    complexity: M
    touches:
      - internal/backends/runtime.go
      - internal/backends/docker.go
      - internal/backends/docker_test.go
      - internal/backends/manager.go
      - internal/backends/manager_test.go
      - internal/backends/data.go
      - internal/backends/data_test.go
      - internal/backends/docker_integration_test.go
      - internal/backends/backendstest/fake.go
      - internal/backends/backendstest/fake_test.go
    brief: |
      Implement Design D2 of the plan exactly (names, messages, labels, archive format, call order, uid/gid rewrite, the busy guard and RemoveUploadData waiting for it).
      1. internal/backends/runtime.go: add `CopyFromContainer` and `CopyToContainer` to `Runtime` with the signatures and comments from D2 (import "io").
      2. internal/backends/docker.go: change `do` so an `io.Reader` body is sent as is without the JSON Content-Type (type switch on `body`: nil, io.Reader, default JSON). Add `CopyFromContainer` (GET `/containers/{id}/archive`, query `path`) and `CopyToContainer` (PUT same path, query `path` only — no `copyUIDGID` —, header `Content-Type: application/x-tar`; drain and close the response). Use `url.PathEscape(id)` like the other methods.
      3. internal/backends/docker_test.go (package backends): extend it the way it tests the other methods with a fake engine: `TestDockerCopyFromContainer` (path, query, streamed body, non-2xx → `*APIError`) and `TestDockerCopyToContainer` (query is exactly `path=/data`, `Content-Type: application/x-tar`, exact body bytes, non-2xx → `*APIError`).
      4. internal/backends/manager.go: add the `dataBusy map[int64]chan struct{}` field (initialise in `NewManager`), the `ErrDataBusy` check in `Ensure` right after the `m.closed` check, extract `ensureImage(ctx, image, registryAuth string) error` from the top of `start` (same error texts), and change `RemoveUploadData` to the D2 version (waitData → StopUpload → remove helper by name → RemoveVolume).
      5. New internal/backends/data.go: `ErrDataBusy`, `ArchiveError`, `DataHelperName`, `ValidateDataArchive`, `claimData`, `waitData`, `withDataHelper`, `ExportData`, `ImportData` exactly as in D2.
      6. internal/backends/backendstest/fake.go: add `Volumes`, `DataUID`, `DataGID`, `Imported`, `Calls`, `ArchiveErr`, both methods, the `Calls` lines in the existing methods, and the `RemoveVolume` change from D2; initialise the maps in `NewFakeRuntime` and lazily where `AddContainer` does. internal/backends/backendstest/fake_test.go: `TestFakeArchive` (known/unknown container, the default stream for a volume without an entry is a tar with one `data/` dir entry owned by DataUID/DataGID, Imported records the bytes, ArchiveErr fails both, RemoveVolume clears Volumes, Calls has the lines in order).
      7. New internal/backends/data_test.go, `package backends_test` like manager_test.go (backendstest imports backends, so an internal test package would be an import cycle; use only exported API and the hooks in export_test.go). Write exactly these tests: `TestValidateDataArchive` (table: valid files/dirs/symlinks, `./` prefix, empty archive OK; `/abs`, `../x`, `a/../../x`, a hardlink, a fifo, a symlink to `/etc` and one to `../../x`, and garbage bytes, each with its exact D2 message); `TestExportDataRewritesEntries` (fake volume with `data/`, `data/a.txt`, `data/sub/`, `data/sub/b.txt` → exported names `a.txt`, `sub/`, `sub/b.txt` with contents; the returned count equals the bytes written); `TestExportDataStopsBackendsAndRemovesHelper` (a running backend of the page is stopped first; the helper has name `DataHelperName`, the D2 labels and the page volume, is never started, and is removed afterwards — assert on `Calls`); `TestImportDataReplacesVolume` (with `DataUID`/`DataGID` 65532: `Calls` contains, as a subsequence in this order, `stop <backend id>`, `rmctr <helper name>`, `rmvol <volume>`, `create <helper name>`, `copyfrom <helper id> /data`, `copyto <helper id> /data`, `rmctr <helper id>`; the tar in `Imported[volume]` has the archive's names and contents with every Uid/Gid 65532 and empty Uname/Gname); `TestDataBusyBlocksEnsure` (hold an export open by passing ExportData an `io.Writer` whose first Write blocks on a channel; while blocked, `Ensure` for that page returns `ErrDataBusy`, `Ensure` for another page works, and a second ExportData and an ImportData for the page return `ErrDataBusy`; after release `Ensure` works); `TestRemoveUploadDataWaitsForDataOp` (with an export held open as above, `RemoveUploadData` in a goroutine has not called `rmvol` yet; after release it finishes and `Calls` shows the helper removed before `rmvol`; with a cancelled context while still blocked it returns `context.Canceled`); `TestDataOpsAfterClose` (both return an error after `Close`, and `RemoveUploadData` still works); `TestDataOpErrors` (image pull failure, `CreateErr`, `ArchiveErr`, an export entry outside `data/` → `unexpected archive entry`, an import whose first `/data` header is not `data/`; each leaves no helper container in `fake.Containers()` and releases the guard so a later `Ensure` works).
      8. internal/backends/docker_integration_test.go: add `TestDockerIntegrationDataExportImport` (same skip/env guards and setup as `TestDockerIntegration`, instance `itest002`, upload id 2, image from `PEEK_DOCKER_TEST_IMAGE`, network and volume cleanup in t.Cleanup). Steps: ImportData a tar with `notes/` and `notes/hello.txt` = "hi" made with uid/gid 1000; ExportData and require `notes/hello.txt` with "hi" and both entries with Uid 65532; then create a plain container with `rt.CreateContainer` (the image, the page volume, a unique name, removed in t.Cleanup), read the first header of `rt.CopyFromContainer(id, "/data")` and require name `data/` and Uid 65532 (the volume root is writable for the backend); ImportData a tar with only `other.txt`; ExportData and require `other.txt` present and `notes/hello.txt` absent; finally `rt.ListContainers` with label `peek.instance=itest002` must return none.
      9. Run `mise run fmt-check`, `mise run vet`, `mise run test`, `mise run race`, then `mise run check-full`. Then, when `docker info` succeeds, run the integration command from CONTRIBUTING.md → Tests (`docker build -t peek-example-contact:ci examples/contact-form && PEEK_DOCKER_TEST=1 PEEK_DOCKER_TEST_IMAGE=peek-example-contact:ci go test -count=1 -run '^TestDockerIntegration' ./internal/backends`) and report its result; when Docker is not available, say `integration not run` in your result (ci.yml cannot be dispatched; the PR's backend-integration job runs it).
      Stop with status=blocked if the integration test shows the Engine behaving differently from D2's assumptions (read-only rootfs refusal, entries not under `data/`, a root or file Uid other than 65532, or the volume not recreated); do not change the design or weaken the test.
    acceptance:
      - "go test -count=1 -v -run '^(TestValidateDataArchive|TestExportDataRewritesEntries|TestExportDataStopsBackendsAndRemovesHelper|TestImportDataReplacesVolume|TestDataBusyBlocksEnsure|TestRemoveUploadDataWaitsForDataOp|TestDataOpsAfterClose|TestDataOpErrors)$' ./internal/backends | grep -c '^--- PASS' prints 8"
      - "go test -count=1 -v -run '^(TestDockerCopyFromContainer|TestDockerCopyToContainer)$' ./internal/backends | grep -c '^--- PASS' prints 2"
      - "go test -count=1 -v -run '^TestFakeArchive$' ./internal/backends/backendstest | grep -c '^--- PASS' prints 1"
      - "go test -count=1 -race ./internal/backends/... passes"
      - "go test -list '^TestDockerIntegrationDataExportImport$' ./internal/backends prints TestDockerIntegrationDataExportImport"
      - "the phase result states the local integration command passed, or says integration not run because docker info failed"
      - "mise run check-full passes"
  - id: p6-cli-token-calls
    title: CLI backend call, token-calls on/off, --allow-token-calls, show state and stats section
    depends_on: []
    complexity: M
    touches:
      - internal/cli/backend_command.go
      - internal/cli/backend_command_test.go
      - internal/cli/client.go
      - internal/cli/client_test.go
      - internal/cli/stats_comments_command.go
      - internal/cli/stats_comments_command_test.go
      - internal/cli/commands.go
    brief: |
      Implement the CLI parts of Design D7 except `peek backend data` (that is p7). The server side lands in other phases; test only against httptest servers. Use the JSON shapes from D4 and D6 exactly.
      1. internal/cli/client.go: add `longHTTPClient` and `reqWith` exactly as in D7 (set `req.ContentLength = size` when size >= 0; set the bearer token and every header in `header`). internal/cli/client_test.go: `TestReqWith` checks the token, headers and ContentLength reach the server.
      2. internal/cli/backend_command.go: set `backendUsage` to the string from D7. Extend `parseBackendFlags` with `--allow-token-calls` (boolean), `--method`, `--data`, `-H` (repeatable) as in D7. Add `case "call"` → `cmdBackendCall` and `case "token-calls"` → `cmdBackendTokenCalls` to `cmdBackend`. Implement `cmdBackendCall`, `cmdBackendTokenCalls`, the `--allow-token-calls` follow-up request in `cmdBackendSet` (and its new usage string), and the `token calls: on|off` line in `cmdBackendShow`, with every message from D7.
      3. internal/cli/stats_comments_command.go: decode `token_calls` and print the section exactly as in D7.
      4. internal/cli/commands.go: in the help text (the `peek backend` lines, around line 129-135), add `[--allow-token-calls]` to the `peek backend set` line and add, after the `logs` line and in this order: `  peek backend token-calls on|off <slug>   allow or refuse API-token calls to the backend`, `  peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]   call the backend with your token`, `  peek backend data export <slug> [-o <file>]   download the page's /data volume as a tar archive`, `  peek backend data import <slug> <file> [--yes]   replace the page's /data volume with a tar archive`.
      5. internal/cli/backend_command_test.go: add a helper `backendCallServer(t *testing.T, status int, respHeader map[string]string, body string) chan callReq` next to `backendServer`, where `callReq` is `{Method, URI, Body string; Header http.Header}`; it checks the bearer token like `backendServer`, records each request, sets `respHeader`, and answers status/body. Write exactly these tests: `TestBackendCallRequest` (table: default GET to `/api/uploads/my-page/versions/latest/svc/api/items`; path with and without leading `/`; `?a=1&b=2` passed through; a segment with a space escaped as `%20`; `--version 3`; `--data '{"a":1}'` → POST with `Content-Type: application/json` and that body; `--data @file`; `--data -` from stdin; `--method put` → PUT; `-H "Accept: text/plain"` sent); `TestBackendCallRejectsBadInput` (table: `.` and `..` segments, bad method, `-H "X-Foo: 1"`, `-H nocolon`, `--data` over 1 MiB, missing args → each exact D7 message and no request sent); `TestBackendCallOutput` (2xx body on stdout and nil error; 500 with `X-Peek-Relay: backend` → body on stdout and error `backend returned 500 Internal Server Error`; 403 without it and body `{"error":"token calls are not enabled for this backend"}` → that error and empty stdout); `TestBackendTokenCallsCommand` (on/off → PUT `/api/uploads/my-page/backend/token-calls` with `{"enabled":true}` / `{"enabled":false}` and the D7 output lines; bad args → the usage); `TestBackendSetAllowTokenCalls` (two requests in order, the second the token-calls PUT, and both output lines); `TestBackendShowTokenCalls` (prints `token calls: on`, and `token calls: off` when false).
      6. internal/cli/stats_comments_command_test.go: `TestStatsTokenCalls` (one row with email and one with only `caller` print as in D7) and `TestStatsWithoutTokenCalls` (missing `token_calls` and `[]` print no section).
      7. Run `mise run fmt-check`, `mise run vet`, `mise run test`, `mise run coverage` (the 95% total floor includes internal/cli), then `mise run check`.
      Stop with status=blocked if `parseBackendFlags`' treatment of every `-`-prefixed argument as a flag makes `--data -` or `-H` impossible without changing how existing flags parse.
    acceptance:
      - "go test -count=1 -v -run '^(TestBackendCallRequest|TestBackendCallRejectsBadInput|TestBackendCallOutput|TestBackendTokenCallsCommand|TestBackendSetAllowTokenCalls|TestBackendShowTokenCalls|TestStatsTokenCalls|TestStatsWithoutTokenCalls|TestReqWith)$' ./internal/cli | grep -c '^--- PASS' prints 9"
      - "go test -count=1 ./internal/cli passes"
      - "mise run coverage passes"
      - "mise run check passes"
  - id: p3-server-token-route
    title: Shared relay core, the API-token backend route and the opt-in API
    depends_on: [p1-db-token-calls]
    complexity: M
    touches:
      - internal/server/backend_proxy.go
      - internal/server/backend_proxy_test.go
      - internal/server/backend_token_proxy.go
      - internal/server/backend_token_proxy_test.go
      - internal/server/api_backends.go
      - internal/server/api_backends_test.go
      - internal/server/routes.go
      - internal/server/middleware.go
      - internal/server/middleware_test.go
    brief: |
      Implement Design D3 and D4 of the plan exactly (names, order of checks, statuses, messages, headers, the `identified` argument).
      1. internal/server/backend_proxy.go: add `backendCaller`, `readBackendBody`, `relayToBackend`, `escapedPathAfter`, `browserViewerHash`, `tokenCallerHash`, and rewrite `handleBackendProxy` on top of them as D3 says. Behaviour of the browser path stays the same apart from the `X-Peek-Caller: browser` request header and the `X-Peek-Relay: backend` response header.
      2. New internal/server/backend_token_proxy.go: `handleBackendTokenCall` and `tokenCanCallBackend`, in the order of D3 steps 1-10 (access check before the version lookup; the counter written with `context.WithoutCancel`).
      3. internal/server/api_backends.go: `handleSetBackendTokenCalls` and the `TokenCalls` field of `backendInfoOut`, filled in `handleGetBackend` (D4).
      4. internal/server/routes.go: register the five token relay routes and `PUT /api/uploads/{slug}/backend/token-calls` next to the other backend routes, all wrapped in `s.authToken`.
      5. internal/server/middleware.go: replace `bodyLimitExempt` with the D3 version (splitting `r.URL.EscapedPath()`) and update its doc comment to "an upload, backend relay or token backend relay request".
      6. internal/server/backend_proxy_test.go: keep every existing test unchanged; add `TestBackendProxyCallerAndRelayHeaders` (the browser relay sends `X-Peek-Caller: browser`; a relayed response carries `X-Peek-Relay: backend`; Peek's own 403 and 404 errors do not).
      7. New internal/server/backend_token_proxy_test.go (reuse `startTestBackend`, `proxyFixture`, `newBackendTestServer` and the token helpers api_backends_test.go uses, `backendReq`). Write exactly these tests: `TestBackendTokenCallAccess` (table over visibility public/signed-in/restricted × caller owner/admin/user-grantee/other account/automation token: allowed exactly where D3 says, else 403 `access denied`; on a restricted page an unknown version also gives 403 for a caller without access); `TestBackendTokenCallOptIn` (flag off: every caller 403 `token calls are not enabled for this backend` and the backend saw nothing); `TestBackendTokenCallHeaders` (public page: `X-Peek-Caller: token`, `X-Peek-Viewer` equal to `s.tokenCallerHash(u.ID, accountID)`, no `X-Peek-User-*`, no `X-Peek-Svc` needed; signed-in and restricted: `X-Peek-User-Id`/`-Email` of the token account; response has `X-Peek-Relay: backend`); `TestBackendTokenCallPathAndVersion` (`versions/latest/svc/...` reaches the newest version; escaped path and query forwarded, copying `TestBackendProxyKeepsEscapedPath`); `TestBackendTokenCallErrors` (missing token 401; unknown page 404 `not found`; unknown version 404 `version not found`; version without backend 404 `this page version has no backend`; backends off 404 `backends are not enabled on this server`); `TestBackendTokenCallBodyLimit` (a body of exactly 1 MiB reaches the backend through the full `s.Handler()`, which proves the middleware exemption; 1 MiB + 1 gives 413); `TestBackendTokenCallRateLimit` (copy `TestBackendProxyRateLimit`); `TestBackendTokenCallCounter` (two relayed calls on a public page, the second answered 500 → calls=2, last_status=500, Identified=false; a call on a signed-in page → Identified=true; refused calls (403 off, 403 denied, 429) are not counted; with `backend_token_calls` dropped via `s.store.Exec` the backend's response is still returned).
      8. internal/server/api_backends_test.go: `TestBackendAPITokenCalls` (PUT by owner and by admin → 200 `{"status":"ok","token_calls":true}`, GET backend shows `token_calls` true, then false after PUT false; another account 403 `not owner`; backends off 409; bad json 400 `bad json`; `{}` → 400 `enabled must be true or false`; db errors → 500 `db error` for the PUT and for GET backend, with `upload_backend_settings` dropped) and extend `TestBackendAPIWritesAuditActions` with `backend.token_calls` and `enabled=true`.
      9. internal/server/middleware_test.go: `TestBodyLimitExempt` (exempt: `/svc/x/v/1/a`, `POST /api/upload`, `GET /api/uploads/x/versions/1/svc/a`, `POST /api/uploads/x/versions/latest/svc/`; not exempt: `/api/uploads/x/versions/1/backend`, `/api/uploads/x/versions`, `/api/uploads/x/stats`, and `POST /api/uploads/a%2Fversions%2F1%2Fsvc%2Fz/grants` built with `httptest.NewRequest`).
      10. Run `mise run fmt-check`, `mise run vet`, `mise run test`, `mise run race`, then `mise run check-full`; internal/server coverage must stay at or above 93%.
      Stop with status=blocked if an existing `TestBackendProxy*` assertion has to change (adding assertions is fine), or if the Go mux reports a pattern conflict for the new routes.
    acceptance:
      - "git diff main -- internal/server/backend_proxy_test.go shows no removed lines inside existing test functions"
      - "go test -count=1 -v -run '^(TestBackendProxyCallerAndRelayHeaders|TestBackendTokenCallAccess|TestBackendTokenCallOptIn|TestBackendTokenCallHeaders|TestBackendTokenCallPathAndVersion|TestBackendTokenCallErrors|TestBackendTokenCallBodyLimit|TestBackendTokenCallRateLimit|TestBackendTokenCallCounter|TestBackendAPITokenCalls|TestBodyLimitExempt)$' ./internal/server | grep -c '^--- PASS' prints 11"
      - "go test -count=1 -run 'BackendProxy|BackendAPI' ./internal/server passes"
      - "mise run check-full passes"
  - id: p7-cli-data
    title: CLI backend data export and import
    depends_on: [p6-cli-token-calls]
    complexity: S
    touches:
      - internal/cli/backend_data_command.go
      - internal/cli/backend_data_command_test.go
      - internal/cli/backend_command.go
    brief: |
      Implement `peek backend data export|import` exactly as in Design D7 (messages, file handling, confirmation, test seams). p6 already added `reqWith`, `longHTTPClient`, the usage strings and the help lines.
      1. internal/cli/backend_command.go: add `-o` (value, field `output string`) and `--yes` (boolean, field `yes bool`) to `parseBackendFlags`, and `case "data": return cmdBackendData(args[1:])` to `cmdBackend`.
      2. New internal/cli/backend_data_command.go: `cmdBackendData`, `cmdBackendDataExport`, `cmdBackendDataImport`, `stdinIsTerminal`, `confirmInput`.
      3. New internal/cli/backend_data_command_test.go (httptest server via `configureTestClient`; `t.Chdir(t.TempDir())` for the default file name; override `stdinIsTerminal` and `confirmInput` and restore them with t.Cleanup). Write exactly these tests: `TestBackendDataExport` (default file `my-page-data.tar` with the served bytes and mode 0600; stderr has `stopping backends for my-page to take a consistent copy` and `wrote <n> bytes to my-page-data.tar`; `-o out.tar`; `-o -` writes the bytes to stdout); `TestBackendDataExportErrors` (existing file → `already exists` and the file is untouched; server `{"error":"this page has no backend"}` → that error and no file left behind); `TestBackendDataImport` (`--yes` → PUT `/api/uploads/my-page/backend/data`, `Content-Type: application/x-tar`, Content-Length equal to the file size, body equal to the file, output `imported <n> bytes into /data of my-page`; terminal + `y` → request sent; terminal + `n` → `import cancelled` and no request); `TestBackendDataImportErrors` (no `--yes` and not a terminal → the refusing error and no request; missing file → error; server 413 `{"error":"archive too large"}` → that error; wrong args → the D7 usage).
      4. Run `mise run fmt-check`, `mise run vet`, `mise run test`, `mise run coverage`, then `mise run check`.
    acceptance:
      - "go test -count=1 -v -run '^(TestBackendDataExport|TestBackendDataExportErrors|TestBackendDataImport|TestBackendDataImportErrors)$' ./internal/cli | grep -c '^--- PASS' prints 4"
      - "mise run coverage passes"
      - "mise run check passes"
  - id: p4-server-data-routes
    title: Server /data export and import routes, the max_backend_data_import setting, and 503 while busy
    depends_on: [p2-backends-data-io, p3-server-token-route]
    complexity: M
    touches:
      - internal/server/backend_data.go
      - internal/server/backend_data_test.go
      - internal/server/backend_proxy.go
      - internal/server/backend_proxy_test.go
      - internal/server/routes.go
      - internal/server/middleware.go
      - internal/server/middleware_test.go
      - internal/server/settings.go
      - internal/server/settings_test.go
      - internal/server/runtime_settings.go
      - internal/server/runtime_settings_test.go
      - internal/server/web_admin_test.go
    brief: |
      Implement Design D5 of the plan exactly (routes, order of checks, statuses, messages, headers, deadlines, audit actions, setting).
      1. internal/server/settings.go and internal/server/runtime_settings.go: the `max_backend_data_import` setting as listed in D5 (meta, normalization, megabyte key, Limits row, `defaultBackendDataImportMax` and its seeded default).
      2. New internal/server/backend_data.go: the two consts, `latestBackendSpec`, `handleExportBackendData` (the first-write header writer and the abort-after-first-byte rule) and `handleImportBackendData` (both deadlines, spool under `s.dataDir`, validate, detached import context).
      3. internal/server/routes.go: register `GET` and `PUT /api/uploads/{slug}/backend/data` wrapped in `s.authToken`, next to the other backend routes.
      4. internal/server/middleware.go: add the data branch to `bodyLimitExempt` exactly as in D5. internal/server/middleware_test.go: extend `TestBodyLimitExempt` with `PUT /api/uploads/x/backend/data` exempt, and `GET /api/uploads/x/backend/data` and `PUT /api/uploads/x/backend/env/A` not exempt.
      5. internal/server/backend_proxy.go: in `relayToBackend`, map `backends.ErrDataBusy` from `Ensure` to 503 `backend data is being exported or imported, try again shortly`. internal/server/backend_proxy_test.go: add `TestBackendRelaysBusyDuringDataOp`: start `s.backends.ExportData(ctx, spec, w)` in a goroutine with a `w` whose first Write blocks on a channel, wait until it blocks, then require that `/svc` and the token route both answer 503 with that message; release the channel, wait for ExportData to return, and require both relays to work again.
      6. New internal/server/backend_data_test.go (use `newBackendTestServer`, `allowBackendImages`, `backendFixture`, the FakeRuntime's `Volumes`/`Imported`/`ArchiveErr`/`Calls`, and build tars with archive/tar). Write exactly these tests: `TestBackendDataExport` (owner and admin → 200, `Content-Type: application/x-tar`, `Content-Disposition: attachment; filename="<slug>-data.tar"`, body entries relative to /data; audit `backend.data.export` with `bytes=`); `TestBackendDataExportErrors` (another account 403 `not owner`; backends off 409; page without backend 404 `this page has no backend`; `ArchiveErr` → 502 `could not export backend data`; a concurrent data op → 409 `another data export or import is running for this page`); `TestBackendDataImport` (owner → 200 `{"status":"ok","bytes":n}`, the tar in `Imported[volume]` has the uploaded names and contents; audit `backend.data.import`; no `.backend-data-import-*` files left in the data dir); `TestBackendDataImportRefusals` (with the setting set small via `s.store.SetSetting`: Content-Length over it → 413 `archive too large`; a chunked body over it (`ContentLength = -1`) → 413; empty body → 400 `empty archive`; garbage → 400 `not a valid tar archive`; a `../x` entry → 400 `archive entry "../x" is outside /data`; in every case `Calls` has no `rmvol` line, so the old data was not touched, and no temp files are left); `TestBackendDataImportErrors` (another account 403; backends off 409; no backend 404; `ArchiveErr` → 502 `could not import backend data`; a concurrent data op → 409).
      7. internal/server/settings_test.go, runtime_settings_test.go, web_admin_test.go: `TestMaxBackendDataImportSetting` (normalizes like `max_upload`: positive integers only), `TestMaxBackendDataImportSeeded` (seeded to `268435456`), and `TestAdminLimitsBackendDataImport` (the Limits tab renders `setting-max_backend_data_import`, and posting `max_backend_data_import_mb=10` stores `10485760`). Update any existing test that compares the exact list of seeded or listed settings to include the new key.
      8. Run `mise run fmt-check`, `mise run vet`, `mise run test`, `mise run race`, then `mise run check-full`; internal/server coverage must stay at or above 93%.
      Stop with status=blocked if `SetReadDeadline`/`SetWriteDeadline` through `statusRecorder` returns an error other than `http.ErrNotSupported` in tests, or if a test outside internal/server breaks because of the seeded setting.
    acceptance:
      - "go test -count=1 -v -run '^(TestBackendDataExport|TestBackendDataExportErrors|TestBackendDataImport|TestBackendDataImportRefusals|TestBackendDataImportErrors|TestBackendRelaysBusyDuringDataOp|TestBodyLimitExempt|TestMaxBackendDataImportSetting|TestMaxBackendDataImportSeeded|TestAdminLimitsBackendDataImport)$' ./internal/server | grep -c '^--- PASS' prints 10"
      - "go test -count=1 -run 'BackendProxy|BackendTokenCall|Settings' ./internal/server passes"
      - "grep -n 'max_backend_data_import' internal/server/settings.go internal/server/runtime_settings.go finds the key in both files"
      - "mise run check-full passes"
  - id: p5-server-stats-token-calls
    title: Token-call counters in the stats API
    depends_on: [p3-server-token-route]
    complexity: S
    touches:
      - internal/server/api_stats.go
      - internal/server/api_stats_test.go
    brief: |
      Implement Design D6 of the plan exactly.
      1. internal/server/api_stats.go: in `handleStats`, read `ListBackendTokenCalls(r.Context(), u.ID, 100)` (db error → 500 `db error`) and add `"token_calls"` (always an array) built from `tokenCallRow`, with `Caller: s.tokenCallerHash(u.ID, c.AccountID)` and `Email` only when `c.Identified` or `owner.IsAdmin`.
      2. internal/server/api_stats_test.go: seed counters with `s.store.RecordBackendTokenCall`. Write exactly these tests: `TestStatsTokenCallsPseudonymous` (owner, counter recorded with identified=false: rows have `caller` equal to `s.tokenCallerHash`, `version`, `calls`, `last_call_at`, `last_status`, and no `email` key — also after the page is switched to signed-in); `TestStatsTokenCallsIdentified` (counter recorded with identified=true: `email` present for the owner); `TestStatsTokenCallsAdminSeesEmail` (admin token, identified=false: `email` present); `TestStatsTokenCallsEmpty` (`"token_calls":[]` and the visit fields unchanged); `TestStatsTokenCallsDBError` (`backend_token_calls` dropped via `s.store.Exec` → 500 `db error`).
      3. Run `mise run fmt-check`, `mise run vet`, `mise run test`, then `mise run check-full`.
    acceptance:
      - "go test -count=1 -v -run '^(TestStatsTokenCallsPseudonymous|TestStatsTokenCallsIdentified|TestStatsTokenCallsAdminSeesEmail|TestStatsTokenCallsEmpty|TestStatsTokenCallsDBError)$' ./internal/server | grep -c '^--- PASS' prints 5"
      - "mise run check-full passes"
  - id: p8-docs-retire
    title: Reference and operations docs, README, skill, example README, changelog
    depends_on: [p4-server-data-routes, p5-server-stats-token-calls, p7-cli-data]
    complexity: S
    touches:
      - docs/operations.md
      - README.md
      - docs/src/content/docs/reference.md
      - docs/src/content/docs/using-peek.md
      - skills/peek/SKILL.md
      - examples/contact-form/README.md
      - CHANGELOG.md
    brief: |
      Document the feature as built. Write Markdown as natural flowing lines (no manual wrapping, AGENTS.md). Use the exact names, routes, messages and headers from the plan's Design.
      1. docs/operations.md, "Page Backends":
         - After the paragraph about `peek backend` management, add a paragraph on token calls: `ANY /api/uploads/{slug}/versions/{n|latest}/svc/{path...}` with `Authorization: Bearer <token>` relays to the backend under the `/svc` access rule (public: any token; signed-in: any active account; restricted: owner, admins, grantees); it is off per page until the owner runs `peek backend token-calls on <slug>` or `peek backend set ... --allow-token-calls`, otherwise 403 `token calls are not enabled for this backend`; no `X-Peek-Svc`/`Sec-Fetch-Site` needed; same 1 MiB / 8 MiB / 30 s limits, rate limit and on-demand start, so a scheduled job can wake a backend; the backend gets `X-Peek-Caller: token` (browser calls send `X-Peek-Caller: browser`), and on public pages only a pseudonymous per-account `X-Peek-Viewer`; `peek stats` counts calls per caller and version (the email only for calls made while the page was signed-in or restricted, and always for admins), separate from visits; calls Peek refuses are not counted.
         - Replace the volume paragraph's backup sentence: `peekd backup` still does not include volumes, but owners and admins can export and import a page's volume with `peek backend data export <slug> [-o <file>]` and `peek backend data import <slug> <file> [--yes]` (`GET`/`PUT /api/uploads/{slug}/backend/data`); both stop the page's backends first (an export briefly interrupts in-flight calls; the relays answer 503 `backend data is being exported or imported, try again shortly` until it ends, a page delete waits for it, and the next request cold-starts the backend); the archive is a plain tar relative to `/data`; import replaces the whole volume, the files get the owner of `/data` in the backend image, and a failed import can leave `/data` empty, so export first; `max_backend_data_import` (default 256 MiB, Admin → Limits) caps imports with 413 `archive too large`; export has no limit.
         - Add a security sentence: public visibility exposes the backend to anyone who can open the page, and Peek does not check `X-Peek-Backend-Token` for the backend; the backend must reject requests without it itself.
         - Extend the audited actions list with `backend.token_calls`, `backend.data.export` and `backend.data.import`.
         - In the sentence starting "Request bodies are limited to 64 KiB", add the token relay (1 MiB) and the data import (`max_backend_data_import`).
         - "Upgrades And Rollbacks": add the migration 18 paragraph from Design D8 just before the migration 17 paragraph.
      2. README.md: in the backend headers table add a row `| X-Peek-Caller | browser for calls from the page, token for API-token calls. |`; the `X-Peek-Viewer` row says that for token calls it is a stable per-page id of the calling account; the `X-Peek-Backend-Token` row adds that a public page makes the backend reachable by anyone, so the backend must check this itself. The CLI list (around line 169-175) gets the four new commands and the `--allow-token-calls` flag, worded as in internal/cli/commands.go. Add one short example after the backend command block: `peek backend token-calls on <slug>`, `peek backend call <slug> /api/items`, and `curl -H "Authorization: Bearer $PEEK_TOKEN" https://peek.example.com/api/uploads/<slug>/versions/latest/svc/api/items`. In the security table row "Calling a backend around Peek" (around line 254), add that API tokens reach a backend only through `/api/uploads/<slug>/versions/<n>/svc/`, under the same access check, only after the owner turned token calls on, and marked with `X-Peek-Caller: token`.
      3. docs/src/content/docs/reference.md: the CLI list (around line 155-161) as in commands.go; the API table gets rows for `GET\|POST\|PUT\|PATCH\|DELETE /api/uploads/<slug>/versions/<n\|latest>/svc/<path>` (token; `/svc` access rule; needs token calls on), `PUT /api/uploads/<slug>/backend/token-calls` (token, owner or admin; JSON `{"enabled":true\|false}`), `GET /api/uploads/<slug>/backend/data` and `PUT /api/uploads/<slug>/backend/data` (token, owner or admin; tar archive); the `/svc` row (line ~236) adds that other clients use the token route; the stats row (`GET /api/uploads/<slug>/stats`, line ~201) adds that it includes `token_calls`; the settings table gets, right after the `backend_registry_password` row, `| \`max_backend_data_import\` | integer bytes, at least 1 | Largest /data archive an owner can import into a page backend. Default 268435456 (256 MiB); shown in MB on the Admin Limits tab. |`.
      4. docs/src/content/docs/using-peek.md "Add A Backend": a short paragraph on calling a backend from scripts (`peek backend token-calls on`, `peek backend call`, the curl form, `X-Peek-Caller`), on `peek backend data export|import`, and the public-visibility sentence; the `X-Peek-*` header sentence (line ~89) adds `X-Peek-Caller`; the command list near line 189-192 gets `call`, `token-calls` and `data`.
      5. skills/peek/SKILL.md: add the new commands to the backend command block (line ~114-117) and one sentence on token calls and data export/import to the paragraph after it.
      6. examples/contact-form/README.md: in the paragraph about headers (line ~5), add that Peek also sends `X-Peek-Caller` (`browser` or `token`), and that `GET /api/submissions` can be read by a script with `peek backend call <slug> /api/submissions` on a signed-in or restricted page once token calls are on.
      7. CHANGELOG.md, under `## [Unreleased]`, add a `### Added` section with these three lines (code names in backticks):
         - `API tokens can call a page's backend through /api/uploads/<slug>/versions/<n|latest>/svc/<path>, under the same access rule as the page, once the owner turns it on with peek backend token-calls on <slug> (or peek backend set --allow-token-calls). Backends get X-Peek-Caller (browser or token), and peek stats shows token calls per caller and version.`
         - `peek backend call <slug> <path> calls a page's backend with your login.`
         - `peek backend data export and import download and replace a page's /data volume, which peekd backup does not include. Imports are capped by the new max_backend_data_import setting (default 256 MiB).`
      8. Check the docs site still builds: when `pnpm` is available run `pnpm install --dir docs && pnpm docs:build && node scripts/check-docs-links.mjs docs/dist`; otherwise dispatch `gh workflow run pages.yml --ref feat/backend-token-calls` and wait for that run to pass.
      9. Run `mise run check-full`.
    acceptance:
      - "grep -n 'X-Peek-Caller' README.md docs/operations.md docs/src/content/docs/using-peek.md examples/contact-form/README.md finds it in each file"
      - "grep -n 'backend.token_calls' docs/operations.md finds the audit action"
      - "grep -n 'Migration 18' docs/operations.md finds the upgrade note"
      - "grep -n 'max_backend_data_import' docs/operations.md docs/src/content/docs/reference.md finds it in both"
      - "grep -n 'peek backend call' README.md docs/src/content/docs/reference.md docs/src/content/docs/using-peek.md skills/peek/SKILL.md finds it in each file"
      - "sed -n '/## \\[Unreleased\\]/,/## \\[0.18.1\\]/p' CHANGELOG.md shows the three Added lines"
      - "the docs build (pnpm docs:build plus check-docs-links, or the pages.yml run) passes"
      - "mise run check-full passes"
```
