#!/usr/bin/env python3
"""Server-side latency probe: fake env + raw http.client (keep-alive)."""
import http.client
import json
import os
import subprocess
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ENV_PORT = 18098
FORGE_DIR = os.path.expanduser("~/workspace/tools/makefun/github-core-clone-01-v3")
TOKEN = "test-token-123"
state = {"done": False, "port": None}


class EnvHandler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n))
        state["port"] = body["port"]
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true}')

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"done": state["done"]}).encode())


def main():
    srv = ThreadingHTTPServer(("127.0.0.1", ENV_PORT), EnvHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    env = dict(os.environ)
    env["MAKEFUN_ENV_URL"] = f"http://127.0.0.1:{ENV_PORT}"
    env["MAKEFUN_SHOP_TOKEN"] = TOKEN
    proc = subprocess.Popen([os.path.join(FORGE_DIR, "forge")], cwd=FORGE_DIR,
                            env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            if state["port"]:
                break
            time.sleep(0.1)
        port = state["port"]
        print("forge port:", port)

        def call(method, path, body=None):
            c = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
            headers = {"Authorization": f"Bearer {TOKEN}"}
            data = json.dumps(body).encode() if body is not None else None
            if data:
                headers["Content-Type"] = "application/json"
            c.request(method, path, body=data, headers=headers)
            r = c.getresponse()
            b = r.read()
            c.close()
            return r.status, b

        s, b = call("POST", "/api/v3/user/repos", {"name": "probe", "description": ""})
        assert s == 201, b[:200]
        s, b = call("POST", "/api/v3/repos/adaiversen/probe/issues",
                    {"title": "bench", "body": "body"})
        assert s == 201, b[:200]
        issue = json.loads(b)
        path = f"/api/v3/repos/adaiversen/probe/issues/{issue['number']}"

        # Warm + timed read: 2000 sequential GETs on ONE keep-alive conn.
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        headers = {"Authorization": f"Bearer {TOKEN}"}
        for _ in range(5):
            c.request("GET", path, headers=headers)
            r = c.getresponse(); r.read()
        times = []
        for _ in range(2000):
            t0 = time.perf_counter()
            c.request("GET", path, headers=headers)
            r = c.getresponse()
            body = r.read()
            assert r.status == 200, (r.status, body[:100])
            times.append((time.perf_counter() - t0) * 1000)
        times.sort()
        print(f"read: n=2000 p50={times[1000]:.3f}ms p99={times[1980]:.3f}ms mean={sum(times)/len(times):.3f}ms total={sum(times):.0f}ms")
        c.close()

        # Write: 200 sequential POSTs, one keep-alive conn.
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        times = []
        for i in range(200):
            data = json.dumps({"title": f"t{i}", "body": "b"}).encode()
            h = dict(headers); h["Content-Type"] = "application/json"
            t0 = time.perf_counter()
            c.request("POST", "/api/v3/repos/adaiversen/probe/issues", body=data, headers=h)
            r = c.getresponse()
            body = r.read()
            assert r.status == 201, (r.status, body[:100])
            times.append((time.perf_counter() - t0) * 1000)
        times.sort()
        print(f"write: n=200 p50={times[100]:.3f}ms p99={times[198]:.3f}ms mean={sum(times)/len(times):.3f}ms total={sum(times):.0f}ms")
        c.close()
        print("PROBE OK")
    finally:
        state["done"] = True
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        srv.shutdown()


if __name__ == "__main__":
    main()
