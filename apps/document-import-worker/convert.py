"""Run the pinned MarkItDown CLI with process-local resource and network guards."""

import resource
import runpy
import socket
import sys


def deny_connection(*_args, **_kwargs):
    raise OSError("document conversion network access is disabled")


socket.socket.connect = deny_connection
socket.socket.connect_ex = deny_connection
socket.create_connection = deny_connection

if hasattr(resource, "RLIMIT_AS"):
    soft_limit, hard_limit = resource.getrlimit(resource.RLIMIT_AS)
    memory_limit = 768 << 20
    if hard_limit != resource.RLIM_INFINITY:
        memory_limit = min(memory_limit, hard_limit)
    try:
        resource.setrlimit(resource.RLIMIT_AS, (memory_limit, memory_limit))
    except (OSError, ValueError):
        # macOS may reserve more virtual address space before script startup;
        # the production Linux Job still requires this process limit to apply.
        if sys.platform != "darwin":
            raise
if hasattr(resource, "RLIMIT_CPU"):
    resource.setrlimit(resource.RLIMIT_CPU, (130, 130))
if hasattr(resource, "RLIMIT_CORE"):
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))

if len(sys.argv) == 2 and sys.argv[1] == "--network-guard-selftest":
    try:
        socket.create_connection(("example.com", 80), timeout=1)
    except OSError as error:
        if str(error) == "document conversion network access is disabled":
            print("network blocked")
            raise SystemExit(0)
    raise SystemExit("network guard self-test failed")

sys.argv[0] = "markitdown"
runpy.run_module("markitdown", run_name="__main__")
