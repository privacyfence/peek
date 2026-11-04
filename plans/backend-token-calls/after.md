# After implementing Page backends: API-token calls, per-page opt-in, token-call stats and /data export/import (#52)

## 1. Real-Docker smoke test of token calls and /data export/import

Why: CI tests the server only against the fake container runtime, and tests the real-Docker data path only inside the backends package. This step runs the whole chain (CLI, server, manager, Docker) against a real engine, including the cold start after an import. Allow about 20 minutes. You need a machine with Docker running and the feature branch `feat/backend-token-calls` checked out.

1. Install the toolchain and build: `mise install && mise run build`. You should get `./bin/peek` and `./bin/peekd`.
2. Build the example backend image: `docker build -t localhost/peek-example-contact:local examples/contact-form`.
3. Start peekd with backends on, using a fresh data directory: `rm -rf data && PEEK_BACKENDS=docker ./bin/peekd --addr :7700 --data ./data --base-url http://localhost:7700`. Leave it running in its own terminal.
4. Open http://localhost:7700/setup and create the first admin account.
5. Open http://localhost:7700/admin, go to the **Backends** tab, set **Allowed backend images** to `localhost/` and save. Reference: https://github.com/privacyfence/peek/blob/feat/backend-token-calls/docs/operations.md#page-backends
6. In a second terminal, log in with `./bin/peek login --host http://localhost:7700` and approve the login in the browser.
7. Upload the example page with its backend: `./bin/peek upload examples/contact-form/index.html --visibility signed-in --backend localhost/peek-example-contact:local`. Note the slug it prints.
8. Run `./bin/peek backend call <slug> /api/healthz`. Expected: it fails with `token calls are not enabled for this backend`.
9. Run `./bin/peek backend token-calls on <slug>`, then `./bin/peek backend call <slug> /api/healthz`. Expected: `ok`. Then `./bin/peek backend <slug>` shows `token calls: on`, and `./bin/peek stats <slug>` has a "token calls" section that lists your email with 1 call and status 200.
10. Run `./bin/peek backend data export <slug> -o before.tar`. Expected: stderr shows `stopping backends for <slug> to take a consistent copy`, then `wrote <n> bytes to before.tar`.
11. Build a test archive: `mkdir d && echo hi > d/hello.txt && tar -C d -cf in.tar .`. Then run `./bin/peek backend data import <slug> in.tar --yes`. Expected: `imported <n> bytes into /data of <slug>`.
12. Run `./bin/peek backend data export <slug> -o after.tar && tar --numeric-owner -tvf after.tar`. Expected: `hello.txt` is listed with owner `65532/65532`, and nothing from `before.tar` that `in.tar` lacks is listed.
13. Run `./bin/peek backend call <slug> /api/healthz` again. Expected: `ok` (the backend cold-starts after the import).
14. Clean up: stop peekd with Ctrl-C, then delete `before.tar`, `after.tar`, `in.tar` and `d/`.

**Report:** `ma1 real-docker smoke: PASS (token call refused while off, ok when on, stats row shown, export/import round trip replaced /data)` or `ma1 real-docker smoke: FAIL at step <n>: <what happened>`
