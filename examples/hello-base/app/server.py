#!/usr/bin/env python3
"""A tiny HTTP server, the workload of the example base: answers the greeting and the request path."""
import os
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = f"{os.environ.get('GREETING', 'hello')}: {self.path}\n".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
