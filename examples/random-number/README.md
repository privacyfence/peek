# Random-number example backend

The smallest useful page backend: a page with one button, and a Python backend that answers `GET /api/rand` with the output of `openssl rand -hex 16`. Use it as a starting point for your own backend; for a larger one that sends email and keeps data, see [contact-form](../contact-form).

| File | What it is |
| --- | --- |
| `index.html` | The page. The button calls `peek.fetch("/api/rand")` and shows the result. |
| `server.py` | The backend: a standard-library Python HTTP server listening on `$PORT`. |
| `Dockerfile` | Builds the image Peek runs for the page. |

## How the pieces fit

Peek does not use a Compose file for a backend. It starts the container itself, from an image name, the first time the page calls `peek.fetch`, and stops it after the idle timeout (15 minutes by default). So everything the backend needs must be in the image, and the image's `CMD` must start the server without extra arguments.

1. The page calls `peek.fetch("/api/rand")`. The path is the path inside the container.
2. Peek checks that the viewer may see the page, starts the container if it is not running, and relays the request to `http://<container>:$PORT/api/rand`, adding the `X-Peek-Backend-Token` header.
3. `server.py` checks the token, runs `openssl rand -hex 16`, and answers `{"random": "<32 hex characters>"}`.
4. Peek hands the response back to the page, which shows it.

## The token

You do not create, store or configure the token. Every time peekd starts the container it generates a new random token, passes it in as the `PEEK_BACKEND_TOKEN` environment variable, and puts the same value in the `X-Peek-Backend-Token` header of every request it relays. The backend reads the variable once at startup and compares the header with it:

```python
TOKEN = os.environ.get("PEEK_BACKEND_TOKEN", "")
...
got = self.headers.get("X-Peek-Backend-Token", "")
if not TOKEN or not hmac.compare_digest(got, TOKEN):
    return self.reply(401, {"error": "unauthorized"})
```

- `hmac.compare_digest` takes the same time whether the values match or not, so the token cannot be guessed from response times.
- `if not TOKEN` makes the backend refuse everything when the variable is missing, instead of accepting an empty header.
- The token changes on every container start, so never copy it anywhere else. `PORT` and `PEEK_*` names are reserved; `peek backend env set` cannot set them.
- Peek does not check the token for you. Without this check, any other container on the backend network could call the backend directly and skip Peek's access check.

## The Dockerfile

- `FROM python:3.13-slim@sha256:...`: a small Debian image with Python. It already includes the `openssl` program, so nothing else is installed. The digest pins the exact image.
- `RUN mkdir /data && chown 65532:65532 /data`: Peek mounts a volume at `/data`, the only writable place besides `/tmp`. An owned, empty directory in the image makes the volume writable by the non-root user. This backend keeps no data, so the line only matters once you add some (a SQLite file, for example).
- `COPY server.py /app/server.py`: copies the Python file from this folder into the image.
- `ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1`: Python does not try to write `.pyc` files (the root filesystem is read-only), and `print` output shows up in `peek backend logs` straight away.
- `USER 65532:65532`: runs as an unprivileged user, not root.
- `EXPOSE 8080`: documentation only. Peek tells the server its port through `PORT` (8080 unless you upload with `--backend-port`).
- `CMD ["python", "/app/server.py"]`: the command the container runs. Peek never passes its own command.

`COPY` only sees files in the build context, the folder given to `docker build`, so build from this folder (or pass it as the last argument).

## Try it without Peek

```sh
docker build -t random-number:dev examples/random-number
docker run --rm -e PEEK_BACKEND_TOKEN=test123 -p 8080:8080 random-number:dev
```

In another terminal:

```sh
curl -H 'X-Peek-Backend-Token: test123' http://localhost:8080/api/rand   # {"random": "..."}
curl http://localhost:8080/api/rand                                      # {"error": "unauthorized"}
```

## Try it with Peek locally

The repository's compose file has a `backends` profile that runs peekd with `PEEK_BACKENDS=docker` on port 7722.

```sh
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)   # Linux only
docker compose --profile backends up --build
```

Then:

1. Build the image on the same Docker engine: `docker build -t random-number:dev examples/random-number`. A local image needs no registry; Peek only pulls an image the engine does not have.
2. Log in at `http://localhost:7722` and allow the image: on the Admin page, Backends tab, set the allowed backend images to `random-number`.
3. Upload the page: `peek login --host http://localhost:7722`, then `peek upload examples/random-number/index.html --visibility public --name random-number --backend random-number:dev`, and note the `slug:` line of the output.
4. Open `http://localhost:7722/p/<slug>` and press the button. The first press takes a few seconds while the container starts.

On a shared server, push the image to a registry instead (`docker build -t ghcr.io/acme/random-number:1.0 examples/random-number && docker push ghcr.io/acme/random-number:1.0`) and have an admin allow the prefix (`ghcr.io/acme/`).

## Changing it

- New backend code: build under a new tag and set it with `peek backend set <slug> random-number:dev2`. Uploading new HTML without `--backend` keeps the previous version's backend.
- Logs: `peek backend logs <slug>`.
- Settings or secrets: `peek backend env set <slug> NAME --value ...` (or `--value-stdin`), then `peek backend restart <slug>`.
