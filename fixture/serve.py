#!/usr/bin/env python3
"""Serves fixture/ locally (fixture/check.sh uses it when given no URL, and so does .github/workflows/release.yml):
python3 fixture/serve.py <port-file>

Binds a free port on 127.0.0.1 itself (port 0: the OS picks one) and writes the port number to <port-file> only
after binding. There is no gap between choosing the port and binding it, so whatever answers on it is this server.

Replaces {{ORIGIN}} in robots.txt and sitemap.xml with this server's origin (http://127.0.0.1:<port>): a Sitemap:
line in robots.txt and a <loc> in a sitemap must be absolute URLs (RFC 9309 §2.2.4, sitemaps.org), which can only
be written once the serving origin is known. Every other file is returned as it is. Standard library only.
"""
import http.server
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ORIGIN = ""  # known only after binding


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=HERE, **kwargs)

    def log_message(self, *args):
        pass

    def send_head(self):
        path = self.translate_path(self.path)
        if os.path.basename(path) in ("robots.txt", "sitemap.xml") and os.path.isfile(path):
            with open(path, "rb") as f:
                body = f.read().replace(b"{{ORIGIN}}", ORIGIN.encode())
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; charset=utf-8" if path.endswith(".txt") else "application/xml")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            return _Body(body)
        return super().send_head()


class _Body:
    def __init__(self, b):
        self.b = b

    def read(self, *_):
        b, self.b = self.b, b""
        return b

    def close(self):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
ORIGIN = f"http://127.0.0.1:{server.server_address[1]}"
with open(sys.argv[1] + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.replace(sys.argv[1] + ".tmp", sys.argv[1])  # never let a half-written file be read
server.serve_forever()
