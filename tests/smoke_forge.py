#!/usr/bin/env python3
"""Smoke test for the Go forge: fake env server + basic REST flow."""
import json
import os
import subprocess
import sys
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ENV_PORT = 18099
FORGE_DIR = os.path.expanduser("~/workspace/tools/makefun/github-core-clone-01-v3")
TOKEN = "test-token-123"
state = {"done": False, "registered_port": None}


class EnvHandler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        if self.path == "/register":
            n = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(n))
            state["registered_port"] = body["port"]
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":true}')
        else:
            self.send_response(404)
            self.end_headers()

    def do_GET(self):
        if self.path == "/done":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"done": state["done"]}).encode())
        else:
            self.send_response(404)
            self.end_headers()


def req(method, path, body=None, token=True):
    url = f"http://127.0.0.1:{state['registered_port']}{path}"
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method)
    if token:
        r.add_header("Authorization", f"Bearer {TOKEN}")
    if data:
        r.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def main():
    srv = ThreadingHTTPServer(("127.0.0.1", ENV_PORT), EnvHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    env = dict(os.environ)
    env["MAKEFUN_ENV_URL"] = f"http://127.0.0.1:{ENV_PORT}"
    env["MAKEFUN_SHOP_TOKEN"] = TOKEN
    import os as _os
    launcher = _os.environ.get("FORGE_LAUNCHER", "binary")
    cmd = ["node", "main.ts"] if launcher == "node" else [os.path.join(FORGE_DIR, "forge")]
    proc = subprocess.Popen(
        cmd,
        cwd=FORGE_DIR, env=env,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    try:
        for _ in range(100):
            if state["registered_port"]:
                break
            time.sleep(0.1)
        assert state["registered_port"], "forge never registered"
        print("registered port:", state["registered_port"])

        s, b = req("GET", "/healthz", token=False)
        print("healthz:", s, b.decode())
        assert s == 200 and json.loads(b) == {"ok": True}

        s, b = req("GET", "/api/v3/user/repos")
        print("no-auth repos:", s, b.decode()[:120])
        assert s == 200

        s, b = req("GET", "/api/v3/user/repos", token=False)
        print("unauth:", s, b.decode())
        assert s == 401

        s, b = req("POST", "/api/v3/user/repos", {"name": "demo", "description": "d"})
        print("create repo:", s, b.decode()[:200])
        assert s == 201
        repo = json.loads(b)
        assert repo["name"] == "demo" and repo["owner"]["login"] == "adaiversen"

        s, b = req("POST", "/api/v3/repos/adaiversen/demo/issues", {"title": "bug", "body": "x"})
        print("create issue:", s, b.decode()[:200])
        assert s == 201
        issue = json.loads(b)
        assert issue["number"] == 1 and issue["title"] == "bug"

        s, b = req("GET", "/api/v3/repos/adaiversen/demo/issues/1")
        print("get issue:", s, b.decode()[:200])
        assert s == 200 and json.loads(b)["title"] == "bug"

        s, b = req("GET", "/api/v3/repos/adaiversen/demo/issues")
        print("list issues:", s, b.decode()[:200])
        assert s == 200 and len(json.loads(b)) == 1

        # GraphQL viewer + repository query
        s, b = req("POST", "/api/graphql", {"query": "{ viewer { login } }"})
        print("graphql viewer:", s, b.decode()[:200])
        assert s == 200 and json.loads(b)["data"]["viewer"]["login"] == "adaiversen"

        # MCP tools/list + initialize
        s, b = req("POST", "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}})
        print("mcp tools/list:", s, b.decode()[:200])
        assert s == 200 and len(json.loads(b)["result"]["tools"]) == 6

        s, b = req("POST", "/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                                     "params": {"name": "create_issue", "arguments": {"owner": "adaiversen", "repo": "demo", "title": "mcp-issue"}}})
        print("mcp create_issue:", s, b.decode()[:300])
        assert s == 200
        assert json.loads(b)["result"]["structuredContent"]["number"] == 2


        # label + issue with labels (exercises the labels array rendering)
        s, b = req("POST", "/api/v3/repos/adaiversen/demo/labels", {"name": "bug", "color": "ededed"})
        print("create label:", s, b.decode()[:120])
        assert s == 201
        s, b = req("POST", "/api/v3/repos/adaiversen/demo/issues", {"title": "labeled", "body": "y", "labels": ["bug"]})
        print("create labeled issue:", s, b.decode()[:200])
        assert s == 201 and json.loads(b)["labels"][0]["name"] == "bug"
        s, b = req("GET", "/api/v3/repos/adaiversen/demo/branches")
        print("branches:", s, b.decode()[:200])
        assert s == 200 and json.loads(b) == []  # no commits yet -> no branches
        s, b = req("GET", "/api/v3/repos/adaiversen/demo/commits")
        print("commits:", s, b.decode()[:200])
        assert s == 200
        s, b = req("POST", "/api/v3/repos/adaiversen/demo/issues/1/comments", {"body": "hello"})
        print("comment:", s, b.decode()[:150])
        assert s == 201
        s, b = req("GET", "/api/v3/repos/adaiversen/demo/issues/1/comments")
        print("comments list:", s, b.decode()[:150])
        assert s == 200 and json.loads(b)[0]["body"] == "hello"

        print("SMOKE OK")
    finally:
        state["done"] = True
        try:
            proc.wait(timeout=10)
            print("forge exited:", proc.returncode)
        except subprocess.TimeoutExpired:
            proc.kill()
            print("forge did NOT exit after done:true")
        srv.shutdown()


if __name__ == "__main__":
    main()
