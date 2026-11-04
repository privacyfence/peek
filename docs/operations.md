# Internal Operations

Peek is intended to be run by one company or trusted team as internal infrastructure. This guide is the operator checklist for that model.

## Recommended Topology

Run `peekd` behind a TLS-terminating reverse proxy such as Caddy, nginx, Traefik, or your platform ingress. Set `--base-url` / `PEEK_BASE_URL` to the externally visible `https://...` URL so generated links, secure cookies, and HSTS are correct.

Viewer pages hold an open Server-Sent Events connection (`/api/uploads/<slug>/events`, keep-alive every 20 seconds) and reload when a new version is uploaded. The proxy must not buffer `text/event-stream` responses and must allow idle reads of at least 30 seconds; nginx honours the `X-Accel-Buffering: no` header Peek sends. Peek caps open streams at 1000 per process; viewers over the cap get the page without live reload. Shutdown closes the streams at once, and browsers reconnect when Peek is back.

Use one primary `peekd` process per data directory. Peek stores mutable state in SQLite plus uploaded objects. Horizontal scaling is not the default operating model; if the project ever needs that, the storage, session, and migration model should be revisited deliberately instead of hidden behind ad hoc process replication.

## Persistent State

The data directory contains:

- `peek.db`: SQLite database with accounts, tokens, comments, settings, visits, audit logs, and upload metadata.
- `secret.key`: the signing/encryption secret generated on first run when `PEEK_SECRET` is not provided.
- `uploads/`: file-backed upload objects when `PEEK_STORAGE=file`, including `uploads/versions/<slug>/` for versions after the first.

Peek creates the data directory and `uploads/` as `0700` and `peek.db` (with its `-wal`/`-shm`), `secret.key`, `setup.key`, uploaded objects and `peekd backup` output as `0600`. On start it tightens `peek.db` and `secret.key` but does not change an existing directory's mode; it logs a warning if the data directory is accessible to other users. After upgrading a non-Docker install, run `chmod -R go-rwx /var/lib/peek`. Backup agents must run as the peekd user (uid 65532 in the container image) or as root.

Back up the whole data directory for simple deployments. For database-consistent snapshots while the server may be running, use:

```sh
PEEK_DATA=/var/lib/peek peekd backup /backups/peek-$(date +%F).db
```

`peekd backup` refuses to overwrite an existing destination file.

peekd refuses to start when secret.key exists but is shorter than 32 bytes, instead of replacing it: restore the file from the same backup as peek.db, or delete it only if losing every session, page view link, pending invite and encrypted value (S3 keys, OAuth and SMTP secrets, backend environment variables) is acceptable.

To restore from a backup:

1. Stop peekd.
2. Copy the backup over `<data>/peek.db` and delete `<data>/peek.db-wal` and `<data>/peek.db-shm` if present.
3. Restore `<data>/secret.key` and `<data>/uploads/` (or the S3 bucket) from the same point in time.
4. Start peekd.
5. Run the operator smoke test (`/healthz`, `/readyz`, `peek list`, open one page).

If you use S3 storage, also back up or lifecycle-manage the bucket according to your internal retention policy. The SQLite backup contains object metadata, comments, auth state, settings, and analytics, not the remote object bytes themselves.

## Startup Configuration

Peek fails fast for invalid operational config: `base-url` must be an absolute `http` or `https` URL, `max-upload` must be greater than zero, storage must be `file` or `s3`, and total-size / retention limits cannot be negative.

Minimum production-like configuration:

```sh
peekd \
  --addr 127.0.0.1:7700 \
  --data /var/lib/peek \
  --base-url https://peek.example.com \
  --trusted-proxy
```

Set `--trusted-proxy` only when a reverse proxy you control appends to `X-Forwarded-For`. Peek then honours the header only on connections from `--trusted-proxy-cidrs` / `PEEK_TRUSTED_PROXY_CIDRS` (default `127.0.0.0/8,::1/128`, a proxy on the same host) and uses the rightmost address that is not one of those proxies. When the proxy runs in another container, pod or platform edge, set the CIDRs to the addresses it connects from; Peek logs one warning naming the peer when it ignores the header. Without `--trusted-proxy`, every request behind a proxy has the proxy's address, so the whole organisation shares one rate-limit bucket (for example 10 sign-in attempts per minute).

Besides per-IP limits, Peek locks password sign-in for an email address after 10 failed attempts for 15 minutes. Anyone can trigger this lock, so a locked admin can still sign in with SSO or an API token.

Prefer `_FILE` variables or plain environment variables over `--secret`, `--s3-secret-key` and `--metrics-token` on the command line; flags are visible in `ps` and peekd prints a warning.

## First Run And Access

On first run, Peek creates a one-time setup URL in the logs and stores the setup code in the data directory. Use it to create the first admin. After an account exists, the setup file is removed and the setup route is closed.

To start fully configured from the environment, with no setup code and no clicks on the Admin page, set the sign-in and the admins instead. Secrets come from files:

```sh
PEEK_BASE_URL=https://peek.example.com \
PEEK_OIDC_ISSUER_URL=https://sso.example.com/oidc \
PEEK_OIDC_CLIENT_ID=peek \
PEEK_OIDC_CLIENT_SECRET_FILE=/run/secrets/peek_oidc_secret \
PEEK_ALLOWED_EMAIL_DOMAIN=example.com \
PEEK_OIDC_SIGNUP=true \
PEEK_ADMIN_EMAILS=alice@example.com \
PEEK_PROVISIONING_KEY_FILE=/run/secrets/peek_provisioning_key \
peekd --data ./data
```

Those sign-in settings are then locked on the Admin page, `/setup` is skipped, and `alice@example.com` becomes admin at the first SSO sign-in. See [Configure sign-in from the environment](src/content/docs/auth-access.md#configure-sign-in-from-the-environment).

For company usage, prefer OAuth/SSO with invite-only signup. Google Workspace organizations can use `auth_google_org_signup` instead of invites. Admins can configure Google, GitHub, or generic OpenID Connect on the **Admin** page (the **Admin** button next to **Sign out**), create invite links, and promote or disable users later. Direct bearer tokens remain appropriate for internal automation, CI jobs, and agents. Users can create their own from **Dashboard → API keys**. API tokens follow the same account rules as dashboard sign-in: a token is refused with 403 when its account is disabled, when its email does not match the allowed email domain, or, while OAuth login is enabled, when a non-admin account has no linked OAuth identity (the token a provisioning key minted for an account is the one exception to the OAuth rule). Tokens from `peek token create` / `POST /api/tokens` belong to automation accounts without an email; only disabling applies to them. Tokens from `peek login` expire 90 days after approval; dashboard keys and admin tokens do not expire unless created with `expires_hours`.

A directory such as PrivacyFence can create and connect accounts through a provisioning key: see [the Provisioning keys section](src/content/docs/auth-access.md#provisioning-keys) of the auth documentation. The key is shown once by `peek provisioner create`; keep it in the key holder's secret store, and revoke it with `peek provisioner revoke <id>` if it leaks.

## Page Access

Each page has one of three visibilities: `public` (anyone with the link), `signed-in` (any signed-in Peek account; renamed from `private`, existing pages are migrated), and `restricted` (only the owner, admins, and signed-in viewers matching a grant). New pages are `restricted` unless the uploader chooses otherwise. Page passwords were removed. Grants are user emails or Google group emails, managed with `peek grant`, `peek revoke` and `peek grants`, or the grants API, or the dashboard's **Access** dialog. Grants are stored for any visibility but only enforced while the page is `restricted`. They are read from the database on every view of `/p/...`, the comment API and the views API.

A signed-in viewer without a matching grant gets a 403 "Access denied" page naming the account they are signed in as, and the denial is written to the audit log as `page.access.denied`. Viewers who are not signed in are sent to the login page.

The uploaded HTML is served to the sandboxed iframe through a view token that is minted only after the access check. Every load of that view URL re-checks access, so revoking a grant, or changing visibility applies to the frame on its next load; a frame already displayed keeps what it has rendered. The view URL works only in the browser that opened the page (it is bound to the `hn_vid` cookie) and only inside the page's iframe. Renaming a page redirects its old links (302) to the new one. Pages already open under the old link must be reloaded before comments work again. Page links are the page name plus a random six-character suffix; links created before this release keep their old form.

## Google Group Grants

Group grants need Google Workspace lookups, configured at startup with a service account that has domain-wide delegation:

- `PEEK_GOOGLE_SA_KEY_FILE` (`--google-sa-key-file`): path to the service account JSON key file.
- `PEEK_GOOGLE_ADMIN_EMAIL` (`--google-admin-email`): the Workspace admin user the service account impersonates. Both settings must be set together.
- `PEEK_GOOGLE_GROUP_CACHE_TTL` (`--google-group-cache-ttl`, default `5m`, between `1s` and `1h`): how long a membership answer is cached in memory.

The only scope needed is the read-only `https://www.googleapis.com/auth/admin.directory.group.member.readonly`; authorize exactly that scope for the service account's client ID under domain-wide delegation in the Workspace admin console. Without these settings, adding a group grant is refused and user grants keep working.

The key file is not stored in SQLite, so it is not in `peekd backup` output. Back it up and rotate it separately; rotating means replacing the file and restarting `peekd`.

Membership changes take up to the cache TTL to apply. Group grants are checked one after another, and each lookup times out after 5 s, so a page with several group grants can take up to 5 s per group longer to open when the Directory API is slow. A lookup that fails denies access through that group.

The dashboard's shared-with-me list checks at most 25 groups per render, each a cached lookup of up to 5 s, so on a slow Directory API a dashboard render can take up to 25 × 5 s the first time. Groups past the first 25 are not checked and a warning is logged.

## Email

Peek can email people when a restricted page is shared with them. Admins configure it on the **Email** tab of the **Admin** page, using these runtime settings: `smtp_host`, `smtp_port`, `smtp_tls`, `smtp_username`, `smtp_password` and `smtp_from`. Email is on only when both `smtp_host` and `smtp_from` are set; with either blank, sharing works as before and no mail is sent. The password is encrypted at rest, a blank password field on the form keeps the saved one, and values are trimmed, so a password cannot start or end with a space.

`smtp_tls` is `starttls` (the default; STARTTLS is required and the connection is never downgraded), `tls` (implicit TLS from the first byte, usually port 465) or `none` (no encryption, for a local relay only). A blank `smtp_port` means 587, or 465 for `tls`. When `smtp_username` is set Peek authenticates with PLAIN, which the Go SMTP client refuses without TLS unless the host is `localhost`, so `none` with credentials works only against a local relay.

**Send test email** on the tab sends a message to your own address using the saved settings, so save first. The result is audited as `email.test`.

Peek sends a plain-text email with the page link when a grant is added to a restricted page (from the dashboard, the API or the CLI) and, to everyone who already has a grant, when a page is switched to restricted. It sends nothing on removing a grant, on adding a grant that already exists, or when the page is not restricted. The sharer's own address is skipped, and when an allowed email domain is set, only recipients in that domain are mailed. Group grants mail the group address. Each send is bounded by 15 s. A failed send never fails the share: it is logged as `share notification failed` and audited as `upload.share.notify` with `result=failed` (and `result=sent`, the recipient count and the skipped count otherwise), and the dashboard tells the sharer that the notification could not be sent.

## Page Backends

A page version can come with a backend: a Docker image that Peek runs as a container and reaches through `/svc/<slug>/v/<n>/...` (the page calls it with `peek.fetch`). The feature is off by default and needs a Docker or Podman API.

Startup settings:

- `PEEK_BACKENDS` (`--backends`, default `off`): `off` or `docker`. With `docker`, peekd pings the engine at startup and refuses to start when it is not reachable.
- `PEEK_DOCKER_HOST` (`--docker-host`, default `unix:///var/run/docker.sock`): `unix://` or `tcp://` address of the engine API. Proxy environment variables are ignored for it.
- Engine API version: on its first call peekd asks the engine's `/version` and speaks API 1.43, or the engine's minimum when that is newer (Docker Engine 29 accepts 1.44 and later). A socket proxy in front of the engine should let `GET /version` through; when it does not, peekd stays on 1.43.
- `PEEK_BACKEND_NETWORK` (`--backend-network`, default `peek-backends`): the bridge network peekd creates for backend containers. peekd reaches containers by their address on this network and never publishes ports. When peekd itself runs in a container it must be attached to the same network, otherwise it cannot reach the backends (in `docker-compose.yml`, the `backends` profile does this).

**The Docker socket is root on the host.** Anyone who controls peekd, or who can make it start an image you allowed, controls the host. Peek never lets page owners choose mounts, capabilities, network mode or privileges, but run backends on rootless Docker or Podman, or on a dedicated VM, and keep the allowed image list short. The socket must be readable and writable by the peekd user: the published image runs as uid 65532, so add the socket's group with `group_add` (compose: `export DOCKER_GID=$(stat -c %g /var/run/docker.sock)`) or point `PEEK_DOCKER_HOST` at a socket proxy or a rootless socket owned by that user. On macOS and Windows Docker Desktop a host-run peekd cannot reach container addresses; run peekd in a container on the backend network there.

Runtime settings, on the **Backends** tab of the **Admin** page (the tab says so when backends are off):

- `backend_allowed_images`: comma-separated registry prefixes ending in `/` (`ghcr.io/acme/`) or exact repositories (`ghcr.io/acme/contact-form`). Empty allows no backend. The check happens when an owner attaches an image, so changing it does not stop backends that already exist.
- `backend_memory_mb` (default 256) and `backend_cpu_millis` (default 500, 1000 is one core): limits per container. Containers also get a PID limit, a read-only root filesystem with a 64 MB `/tmp`, all capabilities dropped, `no-new-privileges`, no restart policy and rotated logs.
- `backend_idle_minutes` (default 15): a backend that received no request for this long is stopped; the next request starts it again, so the first one pays the container start time.
- `backend_max_running` (default 10): when more would run, the least recently used backend is stopped.
- `backend_registry_server`, `backend_registry_username` and `backend_registry_password` (encrypted at rest): credentials used to pull images from that one registry. Public images need none.

Images are pulled on the first start when the engine does not have them. Owners manage their backends with `peek backend`; environment variables are stored encrypted, never shown again, and apply on the next container start (`peek backend restart`). `peek backend logs <slug>` shows the last lines of a running container. Backends do not use the **Email** tab's SMTP settings: owners set their backend's own SMTP variables.

Each page has a named volume `peek-<instance>-data-u<id>` mounted at `/data` in its backends. Volumes live in the container engine, not in the peekd data directory, so `peekd backup` and a copy of the data directory do not include them; back them up with the engine's own tools if a backend keeps data that matters. The volume is deleted together with the page (also by retention cleanup). Pruning an old version stops its container but keeps the volume. `<instance>` is a random id stored in the `backend_instance_id` setting; at startup and shutdown peekd removes the containers carrying its own instance label, so several peekd instances can share one engine.

Backend actions are audited: `backend.set`, `backend.remove`, `backend.env.set`, `backend.env.unset` and `backend.restart`. Start failures are logged as `backend start failed` and answered with 502; the relay answers 504 when a backend takes longer than 30 s, limits request bodies to 1 MiB and responses to 8 MiB, and rate limits each client address. The relay forwards no cookies, and tells the backend who is calling only through the `X-Peek-*` headers described in the README.

## Quotas, Retention, And Storage

Use runtime settings for normal day-to-day limits: maximum upload size, total storage cap, per-token count/storage limits, retention days, auth options, and S3 credentials. Storage backend selection itself is a startup concern and requires restart because it changes where object bytes are read and written.

Re-uploading to an existing page with `peek upload --slug` adds a version. Every version counts toward the total storage cap and the per-owner storage cap (charged to the page owner, not the uploader), but versions do not count toward the per-owner upload count, which counts pages.

Retention cleanup runs in the background. It deletes a page when its newest version is older than the configured number of days, and prunes older versions individually together with the comments written on them. The newest version of a page is never pruned. Visits are kept. `0` disables automatic cleanup. A version uploaded while its page is being deleted can leave one orphaned object in storage. Keep retention aligned with your company’s data handling policy rather than treating Peek as an unlimited archive.

S3 endpoints must use HTTPS and must not resolve to private, link-local or metadata addresses; `--s3-allow-private-endpoint` / `PEEK_S3_ALLOW_PRIVATE_ENDPOINT=true` lifts both, for MinIO or internal stores. The SSRF guard also blocks CGNAT (`100.64.0.0/10`), NAT64/6to4/Teredo, benchmarking, multicast and reserved ranges. Bucket names must be DNS-compatible (3–63 lowercase letters, digits, dots, hyphens); a stored bucket that does not match stops startup with `invalid s3 bucket`.

Request bodies are limited to 64 KiB on every route except page uploads, which use the maximum upload size, and the backend relay (`/svc/`), which allows 1 MiB.

## Observability

Use `/healthz` for liveness and `/readyz` for readiness; readiness verifies the database is reachable. `peekd healthcheck` calls `/healthz` and can be used in container or service-manager probes.

Peek logs structured JSON to stderr. Set `PEEK_LOG_LEVEL=debug`, `info`, or `warn`. Audit events are persisted in SQLite and important operational failures are logged with structured fields. Audit details are space-separated key=value fields; a value containing a space, =, a quote, a backslash or a control character is written as a Go-quoted string.

`/metrics` exposes Prometheus text metrics. Set `PEEK_METRICS_TOKEN` (or `PEEK_METRICS_TOKEN_FILE`, at least 16 characters) to require `Authorization: Bearer <token>`; configure Prometheus with `authorization: { credentials_file: … }`. Without a metrics token, `/metrics` requires an admin API token; it is never served anonymously. `/readyz` returns `not ready` without details; the cause is logged as `readiness check failed`.

## Upgrades And Rollbacks

Before upgrading, take a SQLite backup and confirm you know where uploaded object bytes live. Run the new binary against a staging copy of the data directory when changing storage, auth, retention, or database-related code. Security fixes and how to report a vulnerability: [SECURITY.md](../SECURITY.md).

### v0.18.1

Page backends work with Docker Engine 29 and later: peekd asks the engine's `/version` once and uses Engine API 1.43, or the engine's minimum when that is newer (Docker Engine 29 requires 1.44). No migration and no configuration change. A socket proxy in front of the engine should let `GET /version` through; without it peekd stays on 1.43 as before. A rollback to v0.18.0 brings back `client version 1.43 is too old` on Docker Engine 29 with backends on.

### v0.18.0

Peek can now start fully configured from the environment: sign-in settings, admin emails and a provisioning key file. Settings set this way are locked on the Admin page, and SSO sign-in in the allowed email domain creates the account. No migration, and nothing changes unless you set the new variables; a rollback to v0.17.0 ignores them and unlocks the Admin page settings.

### v0.17.0

Page links now serve a web app manifest and the page's own favicon, so pages can be installed from a phone's home screen. No migration and no configuration change; a rollback only removes the manifest and custom favicons. Installing needs Peek served over HTTPS.

### v0.16.1

Hotfix for first-time OAuth sign-in, which bounced new users back to `/login`. No migration and no configuration change; a rollback to v0.16.0 brings the bounce back.

### v0.16.0

Viewer pages keep a Server-Sent Events connection open to `/api/uploads/<slug>/events` and reload when a new version is uploaded. No migration; a rollback only turns live reload off. A reverse proxy that buffers responses delays the events until its buffer fills, so make sure it does not buffer `text/event-stream` (nginx honours the `X-Accel-Buffering: no` header Peek sends) and allows idle reads of at least 30 seconds.

### v0.15.1

The container image moved to `ghcr.io/privacyfence/peek`; `ghcr.io/andras-tkcs/peek` receives no new tags. Change the image in your compose file or unit before pulling, and verify attestations with `gh attestation verify ... -R privacyfence/peek`. The data volume and its layout are unchanged.

Migration 17 (`provisioning`) adds the `provisioners` and `provisioned_accounts` tables for provisioning keys. Older binaries ignore both tables, so a rollback keeps accounts and tokens working but provisioning keys stop working until the upgrade is re-applied.

### v0.14.0

Migration 16 (`upload_backends`) adds the `upload_backends` and `upload_backend_env` tables for page backends. Older binaries ignore both tables, so a rollback keeps pages working but their backends stop being served. Containers started by the newer binary are labelled `peek.managed=true`; remove leftovers with `docker ps -a --filter label=peek.managed=true` if you roll back while backends are running.

### v0.13.0

Migration 14 (`visibility_signed_in`) renames the `private` visibility to `signed-in` for existing pages (`UPDATE uploads SET visibility='signed-in' WHERE visibility='private'`). Older binaries do not know `signed-in`, so roll back by restoring the pre-upgrade backup.

Migration 15 (`drop_password_visibility`) turns every password page into a restricted page and clears its stored password hash. Those pages are visible to their owner and admins only until they are shared with people or groups, and no email is sent for the conversion. A rollback cannot restore them as password pages; restore the pre-upgrade backup if you need the old passwords. The CLI's `--password` and `--password-stdin` flags are gone, and the default visibility of a new upload is `restricted`.

### v0.10.0

Migration 11 (`cli_login_request_context`) adds `request_ip` and `request_user_agent` to `cli_login_devices`, shown on the CLI login approval page.

Migration 12 (`account_session_epoch`) adds `session_epoch` to `accounts`; dashboard sessions from before the upgrade stay valid until the account uses **Sign out other sessions** or is disabled. Sessions created by the new binary are not accepted by older binaries, so after a rollback everyone signs in again.

Migration 13 (`comment_account_id`) adds a nullable `account_id` to `comments`. Comments written before it are shown as unverified. Older binaries ignore the column, so a rollback keeps comments readable but new comments lose the verified marker.

The unversioned `/raw/<slug>` route is removed and uploaded HTML is served with a CSP `sandbox`. Pages opened before the upgrade must be reloaded. The `hn_csrf` cookie is now HttpOnly.

With `--trusted-proxy`, `X-Forwarded-For` is trusted only from `PEEK_TRUSTED_PROXY_CIDRS` (default loopback). Deployments whose proxy connects from another address must set it, or all clients share the proxy's rate-limit bucket.

API tokens follow the allowed email domain and OAuth-required rules (see First Run And Access); check which personal API keys that affects before upgrading.

### v0.9.0

Migration 10 (`token_hint_last_used`) adds `hint` and `last_used_at` to `tokens`. Tokens created before it show `—` instead of their last four characters on the dashboard. Older binaries ignore both columns, so tokens keep working after a rollback, but older binaries still cannot revoke a token that uploaded a page.

### v0.8.0

v0.8.0 adds migrations 7 (`upload_versions`, which also adds a `version` column to comments and visits and backfills one version per existing page) and 8 (`upload_grants`). Existing objects are not moved and existing `private` pages keep their meaning.

Migration 9 (`upload_slug_aliases`) keeps the old links of renamed pages reserved. Do not roll back to an older binary once a page has been renamed: older binaries do not reserve old links, so a new upload could overwrite a renamed page's first version. Restore the pre-upgrade backup and object storage instead.

Rollback is simplest when the database has not been migrated by a newer binary. If a release includes migrations, treat rollback as restoring the pre-upgrade backup plus object storage state.

## Local Operator Smoke Test

After deployment or upgrade:

```sh
peekd healthcheck --addr https://peek.example.com
peek login --host https://peek.example.com
printf '<!doctype html><h1>Peek smoke</h1>' > /tmp/peek-smoke.html
peek upload /tmp/peek-smoke.html --visibility public
peek list
```

Then open the uploaded page, add a comment, and verify the dashboard shows the upload and visit stats.
