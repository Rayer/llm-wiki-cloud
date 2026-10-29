#!/usr/bin/env python3
"""Loopback-only reverse proxy that drops one successful trusted decision reply."""

import argparse
import http.client
import json
import re
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


DECISION_ROUTE = re.compile(r"^/api/v1/auth/consumers/lwc/mcp-consent/transactions/[^/]+/decision$")
HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade"}


class State:
    def __init__(self, upstream_port):
        self.upstream_port = upstream_port
        self.lock = threading.Lock()
        self.drop_next = False
        self.dropped = 0
        self.last_upstream_status = 0


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        return

    def do_GET(self):
        if self.path == "/__control/status":
            with self.server.state.lock:
                body = json.dumps({"dropped_decisions": self.server.state.dropped,
                                   "last_upstream_status": self.server.state.last_upstream_status,
                                   "drop_armed": self.server.state.drop_next}).encode("utf-8")
            self._reply(200, body, "application/json")
            return
        self._proxy()

    def do_POST(self):
        if self.path == "/__control/arm":
            with self.server.state.lock:
                self.server.state.drop_next = True
            self._reply(200, b'{"armed":true}', "application/json")
            return
        self._proxy()

    def do_DELETE(self):
        self._proxy()

    def do_PATCH(self):
        self._proxy()

    def _proxy(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length) if length else None
            connection = http.client.HTTPConnection("127.0.0.1", self.server.state.upstream_port, timeout=60)
            headers = {key: value for key, value in self.headers.items() if key.lower() not in HOP_HEADERS and key.lower() != "content-length"}
            if self.headers.get("Host"):
                headers["Host"] = self.headers["Host"]
            connection.request(self.command, self.path, body=body, headers=headers)
            response = connection.getresponse()
            response_body = response.read()
            status = response.status
            should_drop = False
            if self.command == "POST" and DECISION_ROUTE.fullmatch(self.path) and status == 200:
                with self.server.state.lock:
                    self.server.state.last_upstream_status = status
                    if self.server.state.drop_next:
                        self.server.state.drop_next = False
                        self.server.state.dropped += 1
                        should_drop = True
            connection.close()
            if should_drop:
                print("decision_response_dropped=true upstream_status=200", flush=True)
                self.close_connection = True
                try:
                    self.connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                self.connection.close()
                return

            self.send_response(status)
            for key, value in response.getheaders():
                if key.lower() not in HOP_HEADERS and key.lower() != "content-length":
                    self.send_header(key, value)
            self.send_header("Content-Length", str(len(response_body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(response_body)
            self.close_connection = True
        except Exception:
            self.close_connection = True
            try:
                self._reply(502, b'{"error":"local_auth_proxy_unavailable"}', "application/json")
            except OSError:
                pass

    def _reply(self, status, body, content_type):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True


class Server(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen-port", type=int, default=62383)
    parser.add_argument("--upstream-port", type=int, default=62387)
    args = parser.parse_args()
    if not (1 <= args.listen_port <= 65535 and 1 <= args.upstream_port <= 65535):
        parser.error("ports must be between 1 and 65535")
    server = Server(("127.0.0.1", args.listen_port), Handler)
    server.state = State(args.upstream_port)
    print(f"auth_fault_proxy_ready=127.0.0.1:{args.listen_port} upstream=127.0.0.1:{args.upstream_port}", flush=True)
    try:
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
