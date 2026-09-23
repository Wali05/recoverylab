"""Expose the length of a local JSON array as {"count": N}.

Usage: python array-count.py http://127.0.0.1:8080/items 8091 [json-pointer]
The optional pointer selects an array inside the GET response.
"""

import json
import sys
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def select(document, pointer):
    if pointer == "":
        return document
    if not pointer.startswith("/"):
        raise ValueError("JSON pointer must be empty or start with /")
    for token in pointer[1:].split("/"):
        token = token.replace("~1", "/").replace("~0", "~")
        document = document[int(token)] if isinstance(document, list) else document[token]
    return document


def main():
    if len(sys.argv) not in (3, 4):
        raise SystemExit("usage: array-count.py <local-GET-url> <listen-port> [json-pointer]")
    upstream, port = sys.argv[1], int(sys.argv[2])
    pointer = sys.argv[3] if len(sys.argv) == 4 else ""
    if not upstream.startswith(("http://127.0.0.1:", "http://localhost:")):
        raise SystemExit("upstream must be on loopback")

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != "/count":
                self.send_error(404)
                return
            try:
                with urllib.request.urlopen(upstream, timeout=5) as response:
                    items = select(json.load(response), pointer)
                if not isinstance(items, list):
                    raise TypeError("selected JSON value is not an array")
                body = json.dumps({"count": len(items)}).encode()
            except Exception as error:
                self.send_error(502, str(error))
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
