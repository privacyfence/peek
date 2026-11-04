"""Example Peek page backend: answers GET /api/rand with a random number from openssl."""

import hmac, json, os, subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN = os.environ.get("PEEK_BACKEND_TOKEN", "")

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        # Only answer requests relayed by Peek: it adds this per-container token.
        got = self.headers.get("X-Peek-Backend-Token", "")
        if not TOKEN or not hmac.compare_digest(got, TOKEN):
            return self.reply(401, {"error": "unauthorized"})
        if self.path.split("?")[0] != "/api/rand":
            return self.reply(404, {"error": "not found"})
        out = subprocess.run(["openssl", "rand", "-hex", "16"],
                             capture_output=True, text=True, check=True)
        self.reply(200, {"random": out.stdout.strip()})

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

port = int(os.environ.get("PORT", "8080"))
print(f"rand backend listening on {port}", flush=True)
ThreadingHTTPServer(("", port), Handler).serve_forever()
