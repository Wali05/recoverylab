"""Expose the length of the Spring sample app's /ledger array as JSON."""

import json
import sys
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

upstream = f"http://127.0.0.1:{sys.argv[1]}/ledger"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/count":
            self.send_error(404)
            return

        try:
            with urllib.request.urlopen(upstream, timeout=5) as response:
                ledger = json.load(response)
            if not isinstance(ledger, list):
                raise ValueError("ledger is not a JSON array")
            body = json.dumps({"count": len(ledger)}).encode()
        except Exception as error:
            self.send_error(502, str(error))
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[2])), Handler).serve_forever()
