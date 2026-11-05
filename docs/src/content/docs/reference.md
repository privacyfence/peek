---
title: Reference
description: Server flags, environment variables, runtime settings, CLI configuration, and operational endpoints.
kicker: peekd reference
---

## Server Parameters

| Flag | Env | Default | Notes |
| --- | --- | --- | --- |
| `--addr` | `PEEK_ADDR` | `:7700` | Listen address. |
| `--data` | `PEEK_DATA` | `./data` | Data directory for SQLite, generated secret, and file uploads. |
| `--base-url` | `PEEK_BASE_URL` | `http://localhost:7700` | Public absolute URL. Use HTTPS in production. |
| `--secret` | `PEEK_SECRET` | generated | HMAC/encryption secret. Provide one when multiple processes must share cookie/settings secrets. Or `PEEK_SECRET_FILE` with the path to a file (Docker/Kubernetes secrets); setting both the variable and `_FILE` is an error. |
| `--max-upload` | `PEEK_MAX_UPLOAD` | `2097152` | Maximum upload size in bytes. Must be greater than zero. |
| `--max-total-size` | `PEEK_MAX_TOTAL_SIZE` | `0` | Server-wide storage cap in bytes. `0` means unlimited. |
| `--retention-days` | `PEEK_RETENTION_DAYS` | `0` | Delete uploads older than N days. `0` disables. |
| `--trusted-proxy` | `PEEK_TRUSTED_PROXY` | `false` | Trust `X-Forwarded-For` for analytics and audit IP fields. |
| `--trusted-proxy-cidrs` | `PEEK_TRUSTED_PROXY_CIDRS` | `127.0.0.0/8,::1/128` | Proxies allowed to set `X-Forwarded-For` (with `--trusted-proxy`). |
| `--metrics-token` | `PEEK_METRICS_TOKEN` | empty | Bearer token required on `/metrics`, at least 16 characters. Or `PEEK_METRICS_TOKEN_FILE` with the path to a file; setting both is an error. When empty, `/metrics` requires an admin API token; it is never anonymous. |
| `--storage` | `PEEK_STORAGE` | `file` | Storage backend: `file` or `s3`. |
| `--s3-endpoint` | `PEEK_S3_ENDPOINT` | empty | S3-compatible endpoint URL. |
| `--s3-bucket` | `PEEK_S3_BUCKET` | empty | S3 bucket name. Must be DNS-compatible (3–63 lowercase letters, digits, dots, hyphens); the SSRF guard also blocks CGNAT, NAT64/6to4/Teredo, benchmarking, multicast and reserved ranges. |
| `--s3-region` | `PEEK_S3_REGION` | `us-east-1` | S3 signing region. |
| `--s3-access-key` | `PEEK_S3_ACCESS_KEY` | empty | S3 access key ID. |
| `--s3-secret-key` | `PEEK_S3_SECRET_KEY` | empty | S3 secret access key. Or `PEEK_S3_SECRET_KEY_FILE` with the path to a file; setting both the variable and `_FILE` is an error. |
| `--s3-allow-private-endpoint` | `PEEK_S3_ALLOW_PRIVATE_ENDPOINT` | `false` | Allows HTTP/private/link-local endpoints for controlled private deployments. |
| `--google-sa-key-file` | `PEEK_GOOGLE_SA_KEY_FILE` | empty | Path to a Google service account JSON key with domain-wide delegation, for Google group grants. Requires `--google-admin-email`. |
| `--google-admin-email` | `PEEK_GOOGLE_ADMIN_EMAIL` | empty | Workspace admin user the service account impersonates. Requires `--google-sa-key-file`. |
| `--google-group-cache-ttl` | `PEEK_GOOGLE_GROUP_CACHE_TTL` | `5m` | How long group membership answers are cached. Between `1s` and `1h`. |
| `--oidc-allow-private-issuer` | `PEEK_OIDC_ALLOW_PRIVATE_ISSUER` | `false` | Allows HTTP/private OIDC issuers for local SSO testing. |
| `--google-client-id` | `PEEK_GOOGLE_CLIENT_ID` | empty | Google OAuth client ID. Turns Google login on and locks the Google settings on the Admin page. Needs the secret too. |
| `--google-client-secret` | `PEEK_GOOGLE_CLIENT_SECRET` | empty | Google OAuth client secret. Or `PEEK_GOOGLE_CLIENT_SECRET_FILE` with the path to a file; setting both is an error. |
| `--oidc-issuer-url` | `PEEK_OIDC_ISSUER_URL` | empty | OpenID Connect issuer URL. Turns SSO login on and locks the SSO settings on the Admin page. Needs the client ID and secret too. |
| `--oidc-client-id` | `PEEK_OIDC_CLIENT_ID` | empty | OpenID Connect client ID. |
| `--oidc-client-secret` | `PEEK_OIDC_CLIENT_SECRET` | empty | OpenID Connect client secret. Or `PEEK_OIDC_CLIENT_SECRET_FILE` with the path to a file; setting both is an error. |
| `--allowed-email-domain` | `PEEK_ALLOWED_EMAIL_DOMAIN` | empty | Restricts human sign-in to this email domain and locks `auth_allowed_email_domain`. |
| `--google-org-signup` | `PEEK_GOOGLE_ORG_SIGNUP` | `false` | Google org sign-in. Locks `auth_google_org_signup` when true. |
| `--oidc-signup` | `PEEK_OIDC_SIGNUP` | `false` | SSO sign-up: the first SSO sign-in in the allowed domain creates the account. Locks `auth_oidc_signup` when true. |
| `--provisioning-key-file` | `PEEK_PROVISIONING_KEY_FILE` | empty | Path to a file holding a provisioning key (`peekprov_`, at least 40 characters), registered or rotated at every start. |
| `--provisioning-key-name` | `PEEK_PROVISIONING_KEY_NAME` | `PrivacyFence` | Name of the provisioner registered from the key file, 1 to 100 characters. |
| `--admin-emails` | `PEEK_ADMIN_EMAILS` | empty | Comma-separated emails that become admins at their Google or SSO sign-in. Also skips first-run `/setup`. |
| `--backends` | `PEEK_BACKENDS` | `off` | Page backends: `off` or `docker`. With `docker`, peekd must reach the engine at startup. |
| `--docker-host` | `PEEK_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker or Podman API address, `unix://` or `tcp://`. |
| `--backend-network` | `PEEK_BACKEND_NETWORK` | `peek-backends` | Docker network backend containers join. peekd must be able to reach it. |

Two settings have no flag:

| Env | Default | Notes |
| --- | --- | --- |
| `PEEK_LOG_LEVEL` | `info` | `debug`, `info` or `warn`. Any other value falls back to info. |
| `PEEK_HEALTHCHECK_ADDR` | `PEEK_ADDR` | The address `peekd healthcheck` probes. |

## peekd Subcommands

| Command | Notes |
| --- | --- |
| `peekd --version` | Prints the version. |
| `peekd backup [--data <dir>] [path]` | Writes a consistent SQLite snapshot via `VACUUM INTO` (default `<data>/peek-backup.db`), safe while the server runs. |
| `peekd healthcheck [--addr <host:port\|url>]` | Exits 0 when `/healthz` answers 200. The address defaults to `PEEK_HEALTHCHECK_ADDR`, then `PEEK_ADDR`. |

`peekd --help` lists every flag and subcommand.

## Runtime Settings

Admins can change these from the **Admin** page (the **Admin** button next to **Sign out**) or the settings API. A setting that a server flag or variable sets is locked: see [Configure sign-in from the environment](/peek/docs/auth-access/#configure-sign-in-from-the-environment).

| Key | Type | Notes |
| --- | --- | --- |
| `auth_token_login_enabled` | boolean | Allows web login with an access token when OAuth is not required. |
| `auth_allowed_email_domain` | email domain | Optional exact email domain for human sign-in, invites, and CLI browser approval. Empty allows any domain. |
| `auth_google_org_signup` | boolean | Google-only org sign-in: first sign-in of a Google Workspace user in the allowed domain creates the account, and invites are inactive. Requires Google login and `auth_allowed_email_domain`. |
| `oauth_google_enabled` | boolean | Enables Google login if credentials are configured. |
| `oauth_google_client_id` | string | Google OAuth web client ID. |
| `oauth_google_client_secret` | secret string | Google OAuth web client secret. |
| `auth_oidc_signup` | boolean | SSO sign-up: the first SSO sign-in of anyone in the allowed email domain creates the account. Requires SSO login and `auth_allowed_email_domain`, and cannot be combined with `auth_google_org_signup`. |
| `oauth_github_enabled` | boolean | Enables GitHub login if credentials are configured. |
| `oauth_github_client_id` | string | GitHub OAuth app client ID. |
| `oauth_github_client_secret` | secret string | GitHub OAuth app client secret. |
| `oauth_oidc_enabled` | boolean | Enables generic OpenID Connect SSO if issuer and credentials are configured. |
| `oauth_oidc_issuer_url` | URL | OpenID Connect issuer URL. Must be HTTPS and publicly routable unless `PEEK_OIDC_ALLOW_PRIVATE_ISSUER=true`. |
| `oauth_oidc_client_id` | string | OpenID Connect client ID. |
| `oauth_oidc_client_secret` | secret string | OpenID Connect client secret. |
| `storage` | `file` or `s3` | Startup setting; restart to apply backend changes. |
| `s3_endpoint` | URL | Validated to block unsafe endpoints unless private endpoints are explicitly allowed. |
| `s3_bucket` | string | Bucket name for uploaded HTML bytes. |
| `s3_region` | string | S3 signing region. |
| `s3_access_key` | string | S3 access key ID. |
| `s3_secret_key` | secret string | S3 secret access key. |
| `smtp_host` | host name | SMTP relay host, such as `smtp-relay.gmail.com`, without scheme or port. Email is on only when this and `smtp_from` are set. |
| `smtp_port` | integer | SMTP port, 1–65535. Blank means 587, or 465 with `smtp_tls` set to `tls`. |
| `smtp_tls` | `starttls`, `tls` or `none` | `starttls` (default) requires STARTTLS, `tls` is implicit TLS, `none` is for a local relay only. |
| `smtp_username` | string | Blank means no authentication. PLAIN auth needs TLS unless the host is localhost. |
| `smtp_password` | secret string | Password or app password for the SMTP username. |
| `smtp_from` | email address | Sender address of Peek emails. Email is off while it is blank. |
| `backend_allowed_images` | comma-separated list | Registry prefixes ending in `/` (`ghcr.io/acme/`) or exact repositories. Empty allows no backend. |
| `backend_memory_mb` | integer 16–65536 | Memory limit per backend container. Default 256. |
| `backend_cpu_millis` | integer 50–64000 | CPU limit per backend container; 1000 is one core. Default 500. |
| `backend_idle_minutes` | integer 1–1440 | Stop a backend after this many minutes without requests. Default 15. |
| `backend_max_running` | integer 1–1000 | Stop the least recently used backend when more would run. Default 10. |
| `backend_registry_server` | string | Registry host the credentials below apply to, such as `ghcr.io`. |
| `backend_registry_username` | string | Username for pulling private backend images. |
| `backend_registry_password` | secret string | Password or token for pulling private backend images. |
| `max_backend_data_import` | integer bytes, at least 1 | Largest /data archive an owner can import into a page backend. Default 268435456 (256 MiB); shown in MB on the Admin Limits tab. |
| `max_upload` | bytes | Maximum individual upload size. |
| `max_total_size` | bytes | Server-wide storage cap; `0` means unlimited. |
| `max_uploads_per_token` | count | Per-owner upload count cap; `0` means unlimited. |
| `max_storage_per_token` | bytes | Per-owner storage cap; `0` means unlimited. |
| `retention_days` | days | Auto-delete threshold; `0` disables cleanup. |

## CLI Configuration

The CLI stores config under the user config directory in `peek/config.json`. Override per command with:

```sh
PEEK_HOST=https://peek.example.com
PEEK_TOKEN=...
```

Token input paths, from safest to least safe:

```text
peek login
peek login --token-stdin
peek login --token-file <path>
peek login --token <value>
```

`peek login` only opens the browser for a verification URL on the configured host; it warns when the host uses plain `http://` and is not loopback. `peek config set --host <new>` clears the saved token when the host changes; run `peek login` afterwards.

## CLI Commands

```text
peek — Peek CLI

Usage:
  peek login [--host <url>]             sign in with browser login when available
  peek login --token-stdin              read an access token from stdin
  peek login --oauth                    always use browser login; fails when the server has none
  peek config set --host <url>          set host (use 'login' / --token-stdin for the token)
  peek config show
  peek upload <file.html> [--name <filename>]
  peek upload <file.html> --visibility public|signed-in
  peek upload <file.html> --visibility restricted
  peek upload <file.html> --slug <slug>        upload a new version of an existing page
  peek upload <file.html> --backend <image> [--backend-port <n>]  attach a backend container
  peek list
  peek delete <slug>
  peek visibility <slug> public
  peek visibility <slug> signed-in
  peek visibility <slug> restricted
  peek rename <slug> <new name>        rename a page; its link follows the name and old links redirect
  peek grant <slug> <email> [--group]  give a user (or Google group) access to a restricted page
  peek revoke <slug> <email> [--group] remove a grant
  peek grants <slug>                   list grants on a page
  peek backend <slug>                  show backends and environment variable names
  peek backend set <slug> <image> [--version <n>|latest] [--port <n>] [--allow-token-calls]
  peek backend remove <slug> [--version <n>|latest]
  peek backend env set <slug> <NAME> --value <v>|--value-stdin   set a backend environment variable
  peek backend env unset <slug> <NAME>
  peek backend restart <slug>          stop the backend so it restarts with new variables
  peek backend logs <slug> [--version <n>|latest] [--tail <n>]
  peek backend token-calls on|off <slug>   allow or refuse API-token calls to the backend
  peek backend call <slug> <path> [--method M] [--data <json|@file|->] [--version <n>|latest] [-H "Name: value"]   call the backend with your token
  peek backend data export <slug> [-o <file>]   download the page's /data volume as a tar archive
  peek backend data import <slug> <file> [--yes]   replace the page's /data volume with a tar archive
  peek stats <slug>
  peek versions <slug>                 list versions of one of your pages
  peek comments <slug> [--version <n>]  list comments (latest version by default)
  peek export <slug>                   export all data for an upload
  peek delete-all                      delete all your uploads
  peek token create --name <name>      create a new user token (admin only)
  peek token list                      list tokens (admin only)
  peek token revoke <id>               revoke a token by id (admin only)
  peek provisioner create --name <n>   create a provisioning key (admin only)
  peek provisioner list                list provisioning keys (admin only)
  peek provisioner revoke <id>         revoke a provisioning key (admin only)
  peek version                         print the CLI version

Token input (most secure first):
  peek login                           browser login when available; otherwise hidden prompt
  peek login --token-stdin             read token from a pipe
  peek login --token-file <path>       read token from a file
  peek login --token <token>           discouraged: exposed in history & 'ps'
  PEEK_TOKEN=…  (env override)          handy for CI
  peek config set --token-stdin        read token from a pipe
  peek config set --token-file <path>  read token from a file
  peek config set --token <token>      discouraged: exposed in history & 'ps'

Environment overrides:
  PEEK_HOST, PEEK_TOKEN
```

`--slug` uploads a new version of an existing page you own (admins can target any page). It cannot be combined with `--visibility` or `--name`. A new upload is `restricted` unless `--visibility` says otherwise. `grant` and `revoke` take a user email by default and a Google group email with `--group`; group grants need the `PEEK_GOOGLE_*` server settings. `grants` lists a page's grants. `versions` lists a page's versions with size, time and URL. `comments` shows the latest version's comments unless `--version` is given. Page names (the `--name` value or the file name) are limited to 200 characters. The former `--password` flags were removed together with page passwords.

## Page And API Routes

| Route | Auth | Purpose |
| --- | --- | --- |
| `/p/<slug>` | page access | Latest version of a page. |
| `/p/<slug>/v/<n>` | page access | Version `n` of a page, with a banner when it is not the latest. |
| `POST /api/upload` | token | Upload a page. Add `slug=<slug>` (form field, or query parameter for raw HTML) to add a version. |
| `GET /api/uploads` | token | List your pages (admins see all). |
| `DELETE /api/uploads/<slug>` | token | Delete a page (owner or admin). |
| `POST /api/uploads/<slug>/visibility` | token | Change visibility. JSON body `{"visibility":"public\|signed-in\|restricted"}`. |
| `GET /api/uploads/<slug>/stats` | token | View statistics for a page; includes `token_calls`. |
| `GET /api/uploads/<slug>/export` | token | Export all data for a page. |
| `GET /api/uploads/<slug>/views` | page access | Total and unique view counts and a 7-day view histogram for a page. |
| `GET /api/uploads/<slug>/events` | page access | Server-Sent Events stream of `version` events carrying the page's latest version number; the viewer uses it to reload when a new version is uploaded. |
| `DELETE /api/uploads-by-owner` | token | Delete all of the caller's pages (`peek delete-all`). |
| `GET /raw/<slug>/v/<n>` | page access | Raw HTML of version `n`, served with a sandboxing CSP. |
| `GET /api/auth/providers` | none | Enabled login providers. |
| `POST /api/cli/login/start` | none | Start a browser login for the CLI. |
| `POST /api/cli/login/poll` | none | Poll a browser login for the CLI. |
| `POST /api/tokens` | admin token | Create an access token. |
| `GET /api/tokens` | admin token | List access tokens. |
| `DELETE /api/tokens/<id>` | admin token | Revoke an access token. |
| `GET /api/settings` | admin token | Read the runtime settings (secrets are not returned). |
| `PUT /api/settings` | admin token | Update runtime settings. |
| `GET /api/audit` | admin token | Read the audit log. |
| `GET /api/uploads/<slug>/versions` | token | List versions (owner or admin). |
| `GET /api/uploads/<slug>/grants` | token | List grants (owner or admin). |
| `POST /api/uploads/<slug>/grants` | token | Add a grant. JSON body `{"kind":"user\|group","email":"..."}`. |
| `DELETE /api/uploads/<slug>/grants` | token | Remove a grant. Same JSON body. |
| `POST /api/uploads/<slug>/name` | token | Rename a page. JSON body `{"name":"..."}`; returns the new `slug`, `url` and `name`. The link follows the name; old links redirect. |
| `GET /api/uploads/<slug>/comments?v=<n>` | page access | Comments for version `n`; defaults to the latest. |
| `POST /api/uploads/<slug>/comments` | page access | Add a comment as a viewer of the page. |
| `POST /api/uploads/<slug>/account-comments` | token | Add a comment under the token's account name; see [Account Comment API](#account-comment-api). |
| `GET /api/uploads/<slug>/backend` | token | Backends per version (`image`, `port`, `running`) and variable names (owner or admin). Values are never returned. |
| `PUT /api/uploads/<slug>/versions/<n\|latest>/backend` | token | Set a version's backend. JSON body `{"image":"...","port":8080}`. |
| `DELETE /api/uploads/<slug>/versions/<n\|latest>/backend` | token | Remove a version's backend. |
| `GET /api/uploads/<slug>/versions/<n\|latest>/backend/logs?tail=<n>` | token | Last log lines (1–2000, default 200) of a running backend, as plain text. |
| `PUT /api/uploads/<slug>/backend/env/<NAME>` | token | Set a variable. JSON body `{"value":"..."}`; applies on the next container start. |
| `DELETE /api/uploads/<slug>/backend/env/<NAME>` | token | Remove a variable. |
| `POST /api/uploads/<slug>/backend/restart` | token | Stop the page's backends; they start again on the next request. |
| `GET\|POST\|PUT\|PATCH\|DELETE /api/uploads/<slug>/versions/<n\|latest>/svc/<path>` | token | Relay to the version's backend for scripts and agents. Same access rule as `/svc`; needs token calls turned on for the page. |
| `PUT /api/uploads/<slug>/backend/token-calls` | token | Turn token calls on or off (owner or admin). JSON body `{"enabled":true\|false}`. |
| `GET /api/uploads/<slug>/backend/data` | token | Download the page's `/data` volume as a tar archive (owner or admin). Stops the page's backends first. |
| `PUT /api/uploads/<slug>/backend/data` | token | Replace the page's `/data` volume with the tar archive in the body (owner or admin). Capped by `max_backend_data_import`. |
| `POST\|GET /api/provisioners`, `DELETE /api/provisioners/<id>` | admin token | Admin: create (returns the key once), list and revoke provisioning keys. |
| `GET /api/provisioning/self` | provisioning key | Provisioning key: the key holder's name and account count. |
| `PUT /api/provisioning/accounts/<external_id>` | provisioning key | Provisioning key: create or adopt the account for an email. |
| `POST /api/provisioning/accounts/<external_id>/token` | provisioning key | Provisioning key: mint the account's token, replacing the previous one. |
| `DELETE /api/provisioning/accounts/<external_id>` | provisioning key | Provisioning key: release the account and revoke its token. |
| `GET\|POST\|PUT\|PATCH\|DELETE /svc/<slug>/v/<n>/<path>` | page access | Relay to the version's backend; used by `peek.fetch`, not called directly. Same access check as the page, and requires the `X-Peek-Svc: 1` header. Other clients use the token route. |

## Account Comment API

`POST /api/uploads/{slug}/account-comments` posts a comment under the account name of the bearer token, so API clients do not appear as "anonymous". The comment lands on the page's latest version, or on version `n` when called with `?v=<n>` (404 `version not found` for an unknown version), the same as the browser comment route. It is separate from the browser comment route, so a server without it answers 404 or 405 instead of silently posting as "anonymous".

Authenticate with `Authorization: Bearer <token>`. The token's account must own the upload or be an admin, otherwise the answer is 403 `not owner`.

The request body is JSON. `body` is required and may be up to 4000 characters. Optional fields are `selector` (up to 500 characters), `element_text` (up to 200 characters) and `anchor_kind`. `anchor_kind` `text` needs both `selector` and `element_text`, `element` needs a `selector`, `page` needs no `selector`, and an empty value is inferred from the other fields. Any other value or an inconsistent combination gives 400 `invalid anchor kind`. A `name` field is accepted but ignored: the author is always the token's account name.

A successful call answers 200 with a single comment object with `id`, `selector`, `element_text`, `anchor_kind`, `author`, `verified`, `body`, `created_at` (Unix seconds) and `version`. `verified` is true for comments written by a signed-in account or through this API, false when the author name was typed by a viewer. Errors are 400 (bad JSON, missing body or invalid anchor), 401 (missing or invalid token), 403 (not the owner, or account disabled), 404 (unknown slug), 413 (a field is too long) and 429 (too many comments).

```sh
curl -s -X POST -H "Authorization: Bearer $PEEK_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"body":"Looks good","selector":"#hero","element_text":"Hero copy"}' \
  "$PEEK_HOST/api/uploads/<slug>/account-comments"
```

## Provisioning API

An admin creates a provisioning key with `peek provisioner create` or `POST /api/provisioners` (body `{"name":"PrivacyFence"}`, 1 to 100 characters, else 400 `name required`; the 201 answer is `{"id","name","key"}`). `GET /api/provisioners` lists `id`, `name`, `hint`, `created_at`, `last_used_at` and `accounts`, never the key. `DELETE /api/provisioners/{id}` answers `{"revoked": id}`, 400 `invalid id` or 404 `not found`, and deletes the tokens the key minted. See [Provisioning Keys](/peek/docs/auth-access/#provisioning-keys) for the trust model.

The key holder authenticates with `Authorization: Bearer peekprov_...`. A missing key gives 401 `missing token` and an unknown one 401 `invalid token`; ordinary API tokens are refused on these routes and provisioning keys on every other route. `{external_id}` is the key holder's stable id for the person and must match `^[A-Za-z0-9._@-]{1,200}$`, else 400 `invalid external id`. Bodies are JSON; a bad one gives 400 `invalid json`.

| Route | Request | Answer |
| --- | --- | --- |
| `GET /api/provisioning/self` | none | 200 `{"name","accounts"}` |
| `PUT /api/provisioning/accounts/{external_id}` | `{"email","name"}` | 200 `{"external_id","account_id","email","name","created","adopted","disabled","has_token","token_expires_at"}` |
| `POST /api/provisioning/accounts/{external_id}/token` | `{"expires_days": 90}` | 201 `{"token","token_id","expires_at"}` |
| `DELETE /api/provisioning/accounts/{external_id}` | none | 200 `{"released": true}` |

`PUT` creates the account or adopts the one with that email, and is idempotent. `email` is trimmed and lower-cased and must have exactly one `@` with text on both sides and at most 254 bytes, else 400 `invalid email`. `name` is optional (the part of the email before `@` is used for a new account) and at most 100 characters, else 400 `name too long`. `created` is true only on the request that created the account, `adopted` is true when the account existed before, and `has_token` is true while the held token has not expired. Errors: 403 `account email domain is not allowed`, 409 `account is a Peek admin; connect it by hand`, and 409 `account is linked to another external id or email`.

`POST .../token` mints a token named after the provisioner and replaces the previous one. `expires_days` is 1 to 365 and defaults to 90 when absent or 0, else 400 `expires_days must be 1 to 365`. Errors: 404 `not found` (no link for this key), 403 `account disabled`, 403 `account email domain is not allowed` and 409 `account is a Peek admin; connect it by hand`. `DELETE` answers 404 `not found` without a link and otherwise deletes the token and the link; the account stays.

Every call is audited as `provisioning.account_upsert`, `provisioning.token_issue` or `provisioning.account_release` with the actor `provisioner:<name>`. No audit entry or log line carries a key, a token, an email or a name.

## Operational Endpoints

| Endpoint | Purpose |
| --- | --- |
| `/healthz` | Liveness. |
| `/readyz` | Readiness, including database reachability. Failures answer `not ready` without details; the cause is logged. |
| `/metrics` | Prometheus text metrics. Requires `Authorization: Bearer <token>`: the `--metrics-token` value when set, otherwise an admin API token. |
| `/setup` | First-run setup only. Closes after the first account exists. |
| `/oauth/google/callback` | Google OAuth callback. |
| `/oauth/github/callback` | GitHub OAuth callback. |
| `/oauth/oidc/callback` | Generic OpenID Connect callback. |

## Operator Checks

Health:

```sh
peekd healthcheck --addr https://peek.example.com
curl -fsS https://peek.example.com/readyz
```

Backup:

```sh
PEEK_DATA=/var/lib/peek peekd backup /backups/peek-$(date +%F).db
```

Smoke upload:

```sh
peek login --host https://peek.example.com
printf '<!doctype html><h1>Peek smoke</h1>' > /tmp/peek-smoke.html
peek upload /tmp/peek-smoke.html --visibility public
peek list
```
