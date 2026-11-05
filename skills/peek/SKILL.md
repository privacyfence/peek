---
name: peek
description: Share an HTML file to a self-hosted peek server and get a live preview URL, then read the comments reviewers leave on it. Use when the user wants to share, preview, or get a link to an HTML page, upload HTML for review, or read feedback/comments on a shared page. Triggers on "share html", "preview html", "upload html", "publish page", "get a link to this page", "read comments", "peek". For running or administering a peek server, use the peek-server skill instead.
---

# peek — Share HTML and collect feedback

peek is a self-hosted HTML sharing server. You upload an HTML file and get a unique, shareable URL. Reviewers open the link and leave comments pinned to a specific element, anchored to selected text, or on the whole page. This skill covers the **consumer** side: installing the `peek` CLI, uploading pages, and reading the comments back.

> Running or administering the server (tokens, email, deployment) is covered by the **peek-server** skill.

## When to use this skill

- The user asks to **share**, **preview**, or **get a link to** an HTML file or snippet.
- You (the agent) generate HTML and want to give the user a live link to view it.
- The user wants to **read the comments / feedback** left on a page they shared.
- The user mentions **peek** or a self-hosted HTML sharing server.

Do NOT use this skill for deploying production apps, hosting static sites long-term, or serving non-HTML content.

## Install the CLI

```sh
curl -fsSL https://raw.githubusercontent.com/privacyfence/peek/main/install.sh | sh
```

This downloads a prebuilt `peek` binary for your OS/arch and installs it (`/usr/local/bin`, falling back to `~/.local/bin`). Overrides:

- `PEEK_VERSION=v0.14.0` — install a specific release (v0.10.0 or newer) instead of the latest.
- `PEEK_INSTALL_DIR=~/bin` — choose the install directory.

Verify with `peek version`.

## Configure

Point the CLI at a peek server and authenticate. If the server supports browser login, this opens the browser approval flow. Otherwise, paste an access token at the hidden prompt:

```sh
peek login --host https://peek.example.com
```

The browser asks for the code that `peek login` prints; type it yourself, never use a code someone sent you. Browser login saves a normal Peek API token locally after approval. For token-only servers, use `peek login --token-stdin` or `peek login --token-file <path>`. For automation, set `PEEK_TOKEN` per command; OAuth-enabled servers require the browser flow for human CLI login. The token never lands in your shell history unless you explicitly use `--token`. Config is saved to `<user-config-dir>/peek/config.json` (`~/.config/peek` on Linux, `~/Library/Application Support/peek` on macOS). You can also set `PEEK_HOST` and `PEEK_TOKEN` per command (handy for CI/agents).

`PEEK_TOKEN` can also be a key the user creates in the Peek web dashboard under **API keys** (it starts with `peek_`). Ask the user to create one there when the CLI login is not possible, and to put it in the environment rather than pasting it into the conversation.

If the user doesn't have a server, token, or invite yet, point them to the **peek-server** skill.

## Core operations

### Upload an HTML file and get a share link

```sh
peek upload path/to/page.html                       # restricted: you, admins and grantees
peek upload path/to/page.html --visibility public   # anyone with the link
```

New pages are restricted by default. Output includes the shareable URL and the slug:

```
uploaded: https://peek.example.com/p/page-k7m2xq
slug:     page-k7m2xq
version:  1
visibility: restricted
```

### Upload raw HTML without a file

Write to a temp file, then upload:

```sh
cat > /tmp/preview.html << 'EOF'
<!doctype html>
<html><body><h1>Preview</h1></body></html>
EOF
peek upload /tmp/preview.html --visibility public
```

Or POST it straight from memory:

```sh
echo '<!doctype html><html><body><h1>Hello</h1></body></html>' | \
  curl -s -X POST -H "Authorization: Bearer $PEEK_TOKEN" \
  -H "Content-Type: text/html" \
  --data-binary @- "$PEEK_HOST/api/upload?filename=preview.html&visibility=public" | jq -r .url
```

### Restrict a page to named people

```sh
peek upload page.html --visibility restricted
peek grant <slug> alice@example.com          # one user
peek grant <slug> team@example.com --group   # a Google group
peek grants <slug>                           # list grants
peek revoke <slug> alice@example.com         # remove (add --group for groups)
```

Restricted pages admit only the owner, admins, and signed-in viewers matching a grant. Group grants need Google group lookup configured on the server. When the server has SMTP email configured, each new grantee gets an email with the link, and switching a page to restricted emails everyone who already has a grant. Grantees see the page in the dashboard's shared-with-me rows (view only). You can also switch an existing page with `peek visibility <slug> restricted`. Owners can manage grants in the dashboard's **Access** dialog, and rename a page with `peek rename <slug> <new name>` (old links redirect).

### Upload a new version of a page

```sh
peek upload page.html --slug <slug>
peek versions <slug>
```

The link stays the same and serves the latest version; `/p/<slug>/v/<n>` serves version `n`. `--slug` cannot be combined with `--visibility` or `--name`.

### Give a page a backend (send email, keep data, use secrets)

A page cannot do these by itself. If the server has page backends enabled, attach a Docker image and call it from the page with `peek.fetch`:

```sh
peek upload page.html --slug <slug> --backend ghcr.io/acme/contact-form:1.2   # --backend-port <n> if not 8080
printf '%s' "$SMTP_PASSWORD" | peek backend env set <slug> SMTP_PASSWORD --value-stdin
peek backend restart <slug>      # apply variable changes
peek backend <slug>              # versions, image, port, running, variable names
peek backend logs <slug>         # last log lines of the running container
peek backend token-calls on <slug>        # let API tokens call the backend
peek backend call <slug> /api/items       # call it from the shell with your login
peek backend data export <slug> -o data.tar   # also: data import <slug> data.tar
```

In the page, `peek.fetch(path, init)` works like `fetch` and resolves to a `Response`: `const res = await peek.fetch("/api/contact", {method: "POST", headers: {"content-type": "application/json"}, body: JSON.stringify(data)})`. The path starts with `/`; methods are GET, POST, PUT, PATCH, DELETE; only `content-type` and `accept` headers are sent; bodies up to 1 MiB. The image must come from a registry prefix the admin allowed, or the upload is refused. Prefer `--value-stdin` over `--value`, which leaks into shell history. The container listens on `$PORT`, has a read-only root filesystem and a writable `/data`, and must reject requests without the `X-Peek-Backend-Token` header equal to `$PEEK_BACKEND_TOKEN`. On signed-in and restricted pages it also gets `X-Peek-User-Email`. A new version without `--backend` keeps the previous version's backend; `peek backend remove <slug>` removes it. With token calls on, scripts reach the backend through `peek backend call` or `/api/uploads/<slug>/versions/latest/svc/<path>` and it sees `X-Peek-Caller: token`; `peek backend data export` and `import` download and replace the `/data` volume (import replaces everything, so export first). `examples/contact-form` in the Peek repository is a complete example.

### Read the comments on a page

```sh
peek comments <slug>
peek comments <slug> --version <n>
```

Comments belong to the version they were written on; the latest version is shown unless `--version` is given.

Prints every comment with its author, the on-page element/text it's anchored to, and the body. You can only read comments for **your own** uploads.

### Post a comment as your account

```sh
curl -s -X POST -H "Authorization: Bearer $PEEK_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"body":"Looks good"}' \
  "$PEEK_HOST/api/uploads/<slug>/account-comments"
```

Posts under your token's account name (any `name` in the body is ignored). It works only for **your own** uploads (admins: any). Optional fields: `selector`, `element_text`, `anchor_kind`.

### List your uploads

```sh
peek list
```

### View visit analytics

```sh
peek stats <slug>
```

Total visits, unique visitors, and a recent-visit log.

### Delete an upload

```sh
peek delete <slug>
```

### Export or delete all owned upload data

```sh
peek export <slug>   # JSON export with upload metadata, visits, and comments
peek delete-all      # delete every upload owned by the current token
```

## Reading feedback in the browser

Open the share URL (`/p/<slug>`) and click the comments panel. Reviewers select text, click an element, or leave a page-level note; each anchored comment gets a numbered on-page pin. The panel has an **Export** button with **Markdown** and **JSON** options; Markdown copies the whole thread with the element/quote it's anchored to, ready to paste into an issue or hand to an agent, while JSON copies the visible comments array for structured reuse.

## Agent workflow

When you generate HTML and want to share it:

1. Write the HTML to a temp file.
2. Run `peek upload /tmp/preview.html --visibility public`.
3. Extract the URL from the output (the `uploaded:` line) and give it to the user.
4. Later, run `peek comments <slug>` to read the feedback they collected.

```sh
peek upload /tmp/preview.html --visibility public | grep '^uploaded:' | awk '{print $2}'
```

If `peek` isn't configured, fall back to curl with env vars:

```sh
curl -s -X POST \
  -H "Authorization: Bearer ${PEEK_TOKEN}" \
  -H "Content-Type: text/html" \
  --data-binary @/tmp/preview.html \
  "${PEEK_HOST}/api/upload?filename=preview.html&visibility=public"
```

The JSON response has the shape `{"slug":"...","url":"https://..."}`.

## Security notes for agents

- Never put the token in a URL or commit it to a repo. Use `peek login`, the config file, or the `PEEK_TOKEN` env var.
- Uploaded HTML is sandboxed in an opaque-origin iframe — it cannot touch the server or other visitors.
- Only token holders can upload, and `peek comments` only returns comments for uploads you own.
- Use `peek export <slug>` when the user asks for a portable data copy of one upload, and `peek delete-all` only when they explicitly want to remove all uploads owned by the configured token.
