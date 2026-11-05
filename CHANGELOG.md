# Changelog

All notable user-visible changes to Peek are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and Peek uses [Semantic Versioning](https://semver.org/). Earlier releases are described in their [GitHub release notes](https://github.com/privacyfence/peek/releases); upgrade-relevant changes per version are in [docs/operations.md](docs/operations.md#upgrades-and-rollbacks).

## [Unreleased]

### Added

- API tokens can call a page's backend through `/api/uploads/<slug>/versions/<n|latest>/svc/<path>`, under the same access rule as the page, once the owner turns it on with `peek backend token-calls on <slug>` (or `peek backend set --allow-token-calls`). Backends get `X-Peek-Caller` (`browser` or `token`), and `peek stats` shows token calls per caller and version.
- `peek backend call <slug> <path>` calls a page's backend with your login.
- `peek backend data export` and `import` download and replace a page's `/data` volume, which `peekd backup` does not include. Imports are capped by the new `max_backend_data_import` setting (default 256 MiB).
- `examples/random-number`: a minimal page backend (one button, a Python server that returns `openssl rand -hex 16`) with a line-by-line walkthrough of the Dockerfile and the backend token.

## [0.18.1] - 2026-11-04

### Fixed

- Page backends work with Docker Engine 29 and later. peekd asked for Engine API 1.43 on every call, which those engines refuse (`client version 1.43 is too old. Minimum supported API version is 1.44`), so peekd would not start with backends on. It now asks the engine's `/version` once and uses 1.43 or the engine's minimum, whichever is newer.

## [0.18.0] - 2026-10-31

### Added

- SSO sign-up: the first SSO sign-in of anyone in the allowed email domain creates the account, like Google org sign-in.
- Sign-in from the environment: `PEEK_GOOGLE_CLIENT_ID`/`_SECRET`, `PEEK_OIDC_ISSUER_URL`/`_CLIENT_ID`/`_SECRET`, `PEEK_ALLOWED_EMAIL_DOMAIN`, `PEEK_GOOGLE_ORG_SIGNUP` and `PEEK_OIDC_SIGNUP` set the sign-in settings at every start and lock them on the Admin page.
- `PEEK_PROVISIONING_KEY_FILE` registers a provisioning key at every start, and replacing the file rotates it in place, so PrivacyFence can provision accounts without `peek provisioner create`.
- `PEEK_ADMIN_EMAILS` names the admins by email, who become admins at their Google or SSO sign-in. First-run `/setup` is skipped when admin emails are set, or when sign-in and accounts come from the environment.

## [0.17.0] - 2026-10-30

### Added

- Pages can be installed as apps from a phone's home screen (Chrome on Android, Safari on iOS): each page link serves a web app manifest. A page sets its own favicon and theme color with a `data:` URI `<link rel="icon">` and `<meta name="theme-color">` in its head; the icon also becomes the tab favicon.

## [0.16.1] - 2026-10-28

### Fixed

- Signing in with Google or another OAuth provider for the first time no longer lands back on the login page: the account is created and signed in in one step, with a short "account ready" page. Visiting /login while signed in now goes to the dashboard.

## [0.16.0] - 2026-10-28

### Added

- Live reload: an open `/p/<slug>` page reloads by itself when a new version is uploaded, through a Server-Sent Events stream at `/api/uploads/<slug>/events`. It waits while the tab is hidden or a comment is being written; `/p/<slug>/v/<n>` links stay on their version. Reverse proxies must not buffer `text/event-stream` responses (see docs/operations.md).

## [0.15.2] - 2026-10-27

### Changed

- Releases are cut by the **Cut release tag** workflow, and the GitHub Release is created once with all its files after the build succeeds; publishing a release in the GitHub UI no longer works with immutable releases.

### Fixed

- Login redirects now reject `/\` as well as `//`, so a crafted `next` value cannot send a user off-site; every cookie is now HttpOnly, including the remembered commenter name; object storage paths are additionally checked to stay inside the storage directory.

## [0.15.1] - 2026-10-27

### Added

- Build provenance attestations for release archives and container images; check an archive with `gh attestation verify <archive> -R privacyfence/peek`.
- `SECURITY.md` with the security policy and private vulnerability reporting.
- Documentation for `peek.params` / `peek.setParams`, which keep a page's filters in the viewer's address bar.
- `peek <command> --help` and `peekd --help`, which list the subcommands and the `PEEK_*` environment variables.

### Changed

- Peek moved to the privacyfence GitHub organization. The Go module path is now `github.com/privacyfence/peek` (`go install github.com/privacyfence/peek/cmd/peekd@latest` works from the first release after the move), the container image is `ghcr.io/privacyfence/peek`, and the docs are at https://privacyfence.github.io/peek/. `ghcr.io/andras-tkcs/peek` gets no new images; switch image references to the new name. Old GitHub URLs redirect.
- A new logo and UI theme: the logo's navy, teal-blue and mint palette on PrivacyFence's design rules (colours only as tokens, WCAG AA contrast, 44 px controls, larger radii), with a dark theme that follows the system setting.
- The README, LICENSE and docs footer say that Peek was originally created by Shy Alter (puemos) and is improved and maintained in this repository by Andras Takacs.
- `peekd` refuses to start when `secret.key` is shorter than 32 bytes instead of replacing it, and writes new keys atomically.
- The startup log field `metrics_auth` is now `metrics_token_set`.
- A release tag must point at a commit on `main` and pass vet, race tests and govulncheck before anything is published, and pre-release tags no longer move the `:latest` image.
- The `:main` image is published only after CI passes on `main`.
- Popups opened by uploaded pages are no longer sandboxed (`allow-popups-to-escape-sandbox`).

### Fixed

- The contact-form example and the README backend commands.
- Slug examples in the README and the agent skills.
- The `--metrics-token` help text.
