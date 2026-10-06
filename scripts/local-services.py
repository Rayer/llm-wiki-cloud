#!/usr/bin/env python3
"""Own native local services for one git worktree without touching port owners."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time

SERVICES = {"auth", "bff", "frontend"}
STARTUP_TIMEOUT_SECONDS = 45
STOPPING = False


def record_path(state: Path) -> Path:
    return state / "services.json"


def startup_path(state: Path) -> Path:
    return state / "startup.json"


def read_startup(state: Path) -> dict | None:
    try:
        return json.loads(startup_path(state).read_text())
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return None


def read_record(state: Path) -> dict | None:
    try:
        return json.loads(record_path(state).read_text())
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return None


def command_line(pid: int) -> str:
    result = subprocess.run(["ps", "-p", str(pid), "-o", "args="], check=False, capture_output=True, text=True)
    return result.stdout.strip()


def record_is_ours(state: Path, record: dict) -> bool:
    try:
        pid = int(record["pid"])
        token = str(record["token"])
    except (KeyError, TypeError, ValueError):
        return False
    args = command_line(pid)
    return bool(args and str(Path(__file__).resolve()) in args and "daemon" in args and token in args and str(state.resolve()) in args)


def write_record(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".services-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def service_command(name: str, root: Path, env: dict[str, str]) -> tuple[list[str], Path, dict[str, str]]:
    bff = root / "apps" / "bff"
    if name == "auth":
        return ["go", "run", "./cmd/auth"], bff, env | {"PORT": env["AUTH_PORT"]}
    if name == "bff":
        return ["go", "run", "./cmd/bff"], bff, env | {"PORT": env["BFF_PORT"]}
    if name == "frontend":
        return ["npm", "run", "dev", "--", "--hostname", "localhost", "--port", env["FRONTEND_PORT"]], root / "apps" / "frontend", env | {"NODE_ENV": "development"}
    raise ValueError(f"unknown service {name}")


def readiness_addresses(name: str) -> tuple[str, ...]:
    # Next binds to "localhost", which can resolve to ::1 only on macOS.
    return ("127.0.0.1", "::1") if name == "frontend" else ("127.0.0.1",)


def wait_for_service_readiness(children: dict[str, subprocess.Popen], env: dict[str, str], timeout: float) -> tuple[str, str] | None:
    pending = set(children)
    deadline = time.monotonic() + timeout
    while pending:
        if STOPPING:
            return sorted(pending)[0], "startup interrupted by stop request"
        for name in list(pending):
            child = children[name]
            code = child.poll()
            if code is not None:
                return name, f"process exited with code {code} before readiness"
            try:
                port = int(env[f"{name.upper()}_PORT"])
            except (KeyError, TypeError, ValueError):
                return name, "service port is not configured for readiness check"
            for address in readiness_addresses(name):
                try:
                    with socket.create_connection((address, port), timeout=0.2):
                        pending.remove(name)
                    break
                except OSError:
                    continue
        if not pending:
            return None
        if time.monotonic() >= deadline:
            name = sorted(pending)[0]
            return name, f"did not accept loopback connections within {timeout:g}s"
        time.sleep(0.1)
    return None


def write_startup(state: Path, token: str, status: str, **details: str) -> None:
    write_record(startup_path(state), {"token": token, "state": status, **details})


def daemon(state: Path, token: str, names: list[str], startup_timeout: float = STARTUP_TIMEOUT_SECONDS) -> int:
    global STOPPING
    root = Path(os.environ["LOCAL_CLOUD_REPO_ROOT"]).resolve()
    state = state.resolve()
    children: dict[str, subprocess.Popen] = {}
    logs = []
    log_paths: dict[str, str] = {}

    def stop_handler(_signum, _frame):
        global STOPPING
        STOPPING = True

    signal.signal(signal.SIGTERM, stop_handler)
    signal.signal(signal.SIGINT, stop_handler)
    write_startup(state, token, "starting")
    try:
        for name in names:
            child_env = os.environ.copy()
            child_env.pop("LOCAL_LOGIN_EMAIL", None)
            child_env.pop("LOCAL_LOGIN_PASSWORD", None)
            log_paths[name] = str(state / f"{name}.log")
            try:
                command, cwd, child_env = service_command(name, root, child_env)
                log_file = (state / f"{name}.log").open("ab", buffering=0)
                logs.append(log_file)
                children[name] = subprocess.Popen(command, cwd=cwd, env=child_env, stdin=subprocess.DEVNULL, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True)
            except (OSError, ValueError, KeyError) as exc:
                write_startup(state, token, "failed", service=name, reason=f"process could not start: {exc}", log=log_paths[name])
                return 1
        write_record(state / "children.json", {"token": token, "children": {name: child.pid for name, child in children.items()}})
        failed = wait_for_service_readiness(children, os.environ, startup_timeout)
        if failed is not None:
            name, reason = failed
            write_startup(state, token, "failed", service=name, reason=reason, log=log_paths.get(name, str(state / f"{name}.log")))
            return 1
        write_startup(state, token, "ready")
        while not STOPPING:
            for name, child in list(children.items()):
                if child.poll() is not None:
                    # Keep the supervisor alive so one component can be restarted
                    # from its debugger without stopping unrelated services.
                    del children[name]
            if not children:
                return 1
            time.sleep(0.25)
    finally:
        for child in children.values():
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        deadline = time.monotonic() + 10
        for child in children.values():
            try:
                child.wait(timeout=max(0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait()
        record = read_record(state)
        if record and record.get("token") == token:
            record_path(state).unlink(missing_ok=True)
        child_record = state / "children.json"
        try:
            value = json.loads(child_record.read_text())
            if value.get("token") == token:
                child_record.unlink(missing_ok=True)
        except (FileNotFoundError, json.JSONDecodeError, OSError):
            pass
        for log_file in logs:
            log_file.close()
    return 0


def start(state: Path, names: list[str]) -> int:
    state.mkdir(parents=True, exist_ok=True)
    record = read_record(state)
    if record and record_is_ours(state, record):
        print(f"local services already supervised (pid {record['pid']})")
        return 0
    if record:
        record_path(state).unlink(missing_ok=True)
    token = os.urandom(16).hex()
    args = [sys.executable, str(Path(__file__).resolve()), "daemon", "--state-dir", str(state.resolve()), "--token", token, "--service-list", ",".join(names)]
    log_file = (state / "supervisor.log").open("ab", buffering=0)
    proc = subprocess.Popen(args, cwd=os.environ["LOCAL_CLOUD_REPO_ROOT"], env=os.environ.copy(), stdin=subprocess.DEVNULL, stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True)
    log_file.close()
    write_record(record_path(state), {"pid": proc.pid, "token": token, "services": names})
    deadline = time.monotonic() + STARTUP_TIMEOUT_SECONDS + 5
    while time.monotonic() < deadline:
        startup = read_startup(state)
        if startup and startup.get("token") == token:
            if startup.get("state") == "ready" and proc.poll() is None:
                print(f"started local services: {', '.join(names)} (supervisor pid {proc.pid})")
                print(f"logs: {state}/*.log")
                return 0
            if startup.get("state") == "failed":
                proc.wait(timeout=12)
                record = read_record(state)
                if record and record.get("token") == token:
                    record_path(state).unlink(missing_ok=True)
                print(f"local service {startup.get('service', 'supervisor')} failed startup: {startup.get('reason', 'unknown failure')}; see {startup.get('log', state / 'supervisor.log')}", file=sys.stderr)
                return 1
        if proc.poll() is not None:
            record = read_record(state)
            if record and record.get("token") == token:
                record_path(state).unlink(missing_ok=True)
            print(f"local service supervisor exited before readiness; see {state / 'supervisor.log'}", file=sys.stderr)
            return proc.returncode or 1
        time.sleep(0.1)
    try:
        os.kill(proc.pid, signal.SIGTERM)
        proc.wait(timeout=12)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.kill(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    record = read_record(state)
    if record and record.get("token") == token:
        record_path(state).unlink(missing_ok=True)
    print(f"local service startup timed out after {STARTUP_TIMEOUT_SECONDS}s; see {state / 'supervisor.log'}", file=sys.stderr)
    return 1


def stop(state: Path) -> int:
    record = read_record(state)
    if not record:
        print("no local service supervisor is recorded for this worktree")
        return 0
    if not record_is_ours(state, record):
        print("recorded supervisor is no longer this worktree's process; leaving other processes untouched")
        record_path(state).unlink(missing_ok=True)
        return 0
    pid = int(record["pid"])
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if not record_is_ours(state, record):
            print(f"stopped local services supervised by pid {pid}")
            return 0
        time.sleep(0.2)
    if record_is_ours(state, record):
        os.kill(pid, signal.SIGKILL)
    print(f"forced local service supervisor pid {pid} to stop")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("start", "stop", "daemon"))
    parser.add_argument("services", nargs="*", help="services to start")
    parser.add_argument("--state-dir", default=os.environ.get("LOCAL_CLOUD_STATE_DIR", ""))
    parser.add_argument("--token", default="")
    parser.add_argument("--service-list", default="")
    args = parser.parse_args()
    if not args.state_dir:
        parser.error("LOCAL_CLOUD_STATE_DIR or --state-dir is required")
    state = Path(args.state_dir)
    if args.action == "daemon":
        names = [name for name in args.service_list.split(",") if name]
        if not names:
            # argparse accepts arguments after daemon as services for older starts.
            names = [name for name in args.services if name]
        if not args.token or not names or any(name not in SERVICES for name in names):
            parser.error("daemon requires --token and valid --service-list")
        return daemon(state, args.token, names)
    names = args.services or ["auth", "bff", "frontend"]
    if any(name not in SERVICES for name in names) or len(set(names)) != len(names):
        parser.error("services must be unique values from auth, bff, frontend")
    if args.action == "start":
        return start(state, names)
    return stop(state)


if __name__ == "__main__":
    raise SystemExit(main())
