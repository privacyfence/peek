# Contact-form example backend

A one-page contact form with a container backend. The page (`index.html`) calls `peek.fetch("/api/contact", ...)`; Peek relays the call to this container (`main.go`), which sends an email through your SMTP server and keeps a copy of every submission in `/data/submissions.jsonl`.

The backend only answers requests that carry the `X-Peek-Backend-Token` header Peek adds, so it cannot be called around Peek. `GET /api/submissions` returns the stored submissions, and only when Peek sets `X-Peek-User-Email`, which happens on signed-in and restricted pages. Peek also sends `X-Peek-Caller` (`browser` or `token`). A script can read the submissions with `peek backend call <slug> /api/submissions` on a signed-in or restricted page once token calls are on.

## Build and push

```sh
docker build -t ghcr.io/acme/contact-form:1.0 examples/contact-form
docker push ghcr.io/acme/contact-form:1.0
```

The image has to be on a registry prefix an admin allowed: on the Admin page, Backends tab, add `ghcr.io/acme/` to the allowed backend images.

## Upload and configure

```sh
peek upload examples/contact-form/index.html --visibility public --name contact --backend ghcr.io/acme/contact-form:1.0
```

Note the slug: line of the output (for example contact-k7m2xq) and use it as <slug> below.

```sh
peek backend env set <slug> SMTP_HOST --value smtp.example.com
peek backend env set <slug> SMTP_PORT --value 587
peek backend env set <slug> MAIL_FROM --value forms@example.com
peek backend env set <slug> MAIL_TO --value team@example.com
printf '%s' "$SMTP_PASSWORD" | peek backend env set <slug> SMTP_PASSWORD --value-stdin
peek backend restart <slug>
```

Variables: `SMTP_HOST`, `SMTP_PORT` (default 25), `SMTP_USERNAME` and `SMTP_PASSWORD` (optional, enables auth), `MAIL_FROM`, `MAIL_TO`. The backend also reads `PORT` (default 8080), `PEEK_BACKEND_TOKEN` (set by Peek) and `DATA_DIR` (default `/data`).

## Try it locally with Mailpit

The compose file has a `backends` profile that runs peekd with `PEEK_BACKENDS=docker` next to [Mailpit](https://mailpit.axllent.org), a catch-all SMTP server with a web inbox.

```sh
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)   # Linux only
docker compose --profile backends up --build
```

Then:

1. Build the image locally: `docker build -t contact-form:dev examples/contact-form`.
2. Log in at `http://localhost:7722` and allow the image: on the Admin page, Backends tab, set the allowed backend images to `contact-form`.
3. Upload the page: `peek login --host http://localhost:7722`, then `peek upload examples/contact-form/index.html --visibility public --name contact --backend contact-form:dev`, and note the `slug:` line of the output.
4. Point the backend at Mailpit: `peek backend env set <slug> SMTP_HOST --value mailpit`, `SMTP_PORT` `1025`, `MAIL_FROM` `forms@example.com`, `MAIL_TO` `team@example.com`, then `peek backend restart <slug>`.
5. Open `http://localhost:7722/p/<slug>`, send the form, and read the email at `http://localhost:8025`.

The backend container stops after the idle timeout (15 minutes by default) and starts again on the next submission.
