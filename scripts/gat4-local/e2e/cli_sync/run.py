#!/usr/bin/env python3
"""Run isolated GateAuth/BFF/lwc-sync CLI qualification on numeric loopback."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import queue
import re
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[4]
APP = ROOT / "apps" / "bff"
BASELINE = "4d290cb7d2f5d9d8be9523f0ddc02b8ce1b621c9"
SCRATCH_ROOT = Path("/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch")
REPORT_PATH = Path(__file__).resolve().parent / "report.json"
GCS_SERVER_MODULE = "github.com/fsouza/fake-gcs-server@v1.52.1"
SCOPE = "worktree-" + __import__("secrets").token_hex(12)
PROJECT = "llm-wiki-cloud"
DATABASE = "llm-wiki-cloud-local"
BUCKET = "llm-wiki-cloud-local"

SCRATCH = Path(tempfile.mkdtemp(prefix="gat4-cli-sync-", dir=SCRATCH_ROOT))
os.chmod(SCRATCH, 0o700)
BIN = SCRATCH / "bin"
BIN.mkdir(mode=0o700)
CONFIG = SCRATCH / "cac"
CONFIG.mkdir(mode=0o700)
LOG_DIR = SCRATCH / "logs"
LOG_DIR.mkdir(mode=0o700)
REPORT: dict = {
    "schema": "gat4-cli-local-qualification-v1",
    "started_at": datetime.now(timezone.utc).isoformat(),
    "scratch_path": str(SCRATCH),
    "source": {},
    "builds": [],
    "fixture_setup": [],
    "commands": [],
    "checks": {},
    "not_run": [
        "Google OIDC validation; local typed synthetic OIDC validator only",
        "provider or LLM calls",
        "cloud services, live accounts, deployment, remote repository, publication, and production readback",
        "GAT-4 acceptance outside CLI acceptance 2 and compatibility acceptance 7",
    ],
}
TERMINAL_LOG = LOG_DIR / "terminal.log"
TERMINAL_LOG.touch(mode=0o600)
os.chmod(TERMINAL_LOG, 0o600)
PROCESSES: list[dict] = []
HTTP_SERVERS: list[ThreadingHTTPServer] = []
PROBE_SERVERS: list[dict] = []
PORTS: dict[str, int] = {}
WEB_TOKEN = ""
USER_ID = ""
AUTH_ORIGIN = ""
BFF_ORIGIN = ""
STORAGE_ORIGIN = ""
FIRESTORE_ORIGIN = ""


def secure_write(path: Path, data: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name(path.name + ".tmp")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "wb") as out:
        out.write(data)
        out.flush()
        os.fsync(out.fileno())
    os.chmod(temp, mode)
    os.replace(temp, path)
    os.chmod(path, mode)


def free_port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return int(probe.getsockname()[1])


def get_ports() -> None:
    used: set[int] = set()
    for name in ("firestore", "storage", "auth", "bff", "frontend", "probe404", "probe302"):
        port = free_port()
        while port in used:
            port = free_port()
        PORTS[name] = port
        used.add(port)


def base_env() -> dict[str, str]:
    keep = {
        "PATH", "HOME", "LANG", "LC_ALL", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN",
        "GOROOT", "GOCACHE", "GOMODCACHE", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
        "SSL_CERT_FILE", "SSL_CERT_DIR",
    }
    env = {key: value for key, value in os.environ.items() if key in keep}
    env.update({
        "TMPDIR": str(SCRATCH),
        "CLOUDSDK_CONFIG": str(SCRATCH / "cloudsdk"),
        "CLOUDSDK_CORE_PROJECT": PROJECT,
        "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
        "GOOGLE_CLOUD_PROJECT": PROJECT,
        "GCE_METADATA_HOST": "127.0.0.1:1",
        "GCE_METADATA_IP": "127.0.0.1",
        "NO_PROXY": "localhost,127.0.0.1,::1",
        "no_proxy": "localhost,127.0.0.1,::1",
        "PYTHONDONTWRITEBYTECODE": "1",
    })
    env.pop("CLOUDSDK_AUTH_ACCESS_TOKEN", None)
    env.pop("GCLOUD_PROJECT", None)
    Path(env["CLOUDSDK_CONFIG"]).mkdir(mode=0o700)
    return env


ENV = base_env()


def safe_text(value: str) -> str:
    value = re.sub(r"(?im)^.*(?:驗證碼|user code).*?$", "[pairing code redacted]", value)
    value = re.sub(r"(?i)(user_code=)[A-Z0-9]+", r"\1[redacted]", value)
    value = re.sub(r"(?i)(Bearer\s+)[^\s\"']+", r"\1[redacted]", value)
    value = re.sub(r"(?i)(access_token|refresh_token|polling_secret|session_id|password)(\s*[:=]\s*\"?)[^\s,\"}]+", r"\1\2[redacted]", value)
    return value


def terminal(line: str) -> None:
    with TERMINAL_LOG.open("a", encoding="utf-8") as out:
        out.write(safe_text(line.rstrip()) + "\n")
    os.chmod(TERMINAL_LOG, 0o600)


def command(label: str, argv: list[str], env: dict[str, str] | None = None,
            cwd: Path = ROOT, timeout: int = 180) -> subprocess.CompletedProcess:
    result = subprocess.run(argv, cwd=cwd, env=env or ENV, capture_output=True,
                            text=True, timeout=timeout, check=False)
    row = {"label": label, "exit_code": result.returncode}
    if result.returncode:
        row["stderr_tail"] = safe_text(result.stderr[-1600:])
    REPORT["builds"].append(row)
    terminal(f"$ {label} -> exit {result.returncode}")
    if result.stdout:
        terminal(safe_text(result.stdout[-8000:]))
    if result.stderr:
        terminal(safe_text(result.stderr[-8000:]))
    if result.returncode:
        raise RuntimeError(f"{label} failed with exit {result.returncode}")
    return result


def service(name: str, argv: list[str], env: dict[str, str], cwd: Path = ROOT) -> dict:
    path = LOG_DIR / f"{name}.log"
    path.touch(mode=0o600)
    os.chmod(path, 0o600)
    sink = path.open("ab", buffering=0)
    proc = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                            stdout=sink, stderr=subprocess.STDOUT, start_new_session=True)
    sink.close()
    item = {"name": name, "proc": proc, "log": path, "port": None}
    PROCESSES.append(item)
    terminal(f"started {name} pid={proc.pid}")
    return item


def wait_port(port: int, item: dict, timeout: float = 45) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if item["proc"].poll() is not None:
            tail = item["log"].read_text(errors="replace")[-1200:]
            raise RuntimeError(f"{item['name']} exited during startup: {safe_text(tail)}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.25):
                return
        except OSError:
            time.sleep(0.15)
    raise TimeoutError(f"{item['name']} did not listen on numeric loopback port {port}")


def wait_http(url: str, item: dict, timeout: float = 30) -> int:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if item["proc"].poll() is not None:
            raise RuntimeError(f"{item['name']} exited before HTTP readiness")
        try:
            status, _, _ = http_call("GET", url)
            return status
        except (OSError, URLError, TimeoutError):
            time.sleep(0.2)
    raise TimeoutError(f"{item['name']} HTTP readiness timed out")


def http_call(method: str, url: str, body: dict | bytes | None = None,
              headers: dict[str, str] | None = None) -> tuple[int, bytes, dict]:
    raw = json.dumps(body).encode() if isinstance(body, dict) else body
    hdrs = dict(headers or {})
    if isinstance(body, dict):
        hdrs.setdefault("Content-Type", "application/json")
    req = Request(url, data=raw, method=method, headers=hdrs)
    try:
        with urlopen(req, timeout=20) as response:
            return response.status, response.read(8 * 1024 * 1024), dict(response.headers)
    except HTTPError as response:
        return response.code, response.read(1024 * 1024), dict(response.headers)


def http_json(method: str, url: str, body: dict | None = None,
              headers: dict[str, str] | None = None) -> tuple[int, dict, dict]:
    status, content, hdrs = http_call(method, url, body, headers)
    try:
        parsed = json.loads(content) if content else {}
    except json.JSONDecodeError:
        parsed = {}
    return status, parsed, hdrs


def firestore_path(doc_path: str) -> str:
    return (f"{FIRESTORE_ORIGIN}/v1/projects/{PROJECT}/databases/{DATABASE}/documents/"
            + quote(doc_path, safe="/"))


def firestore_put(doc_path: str, fields: dict[str, dict]) -> None:
    status, _, _ = http_json("PATCH", firestore_path(doc_path), {"fields": fields})
    if status not in (200, 201):
        raise RuntimeError(f"Firestore emulator fixture write failed: HTTP {status} ({doc_path.split('/')[-2]})")


def fs_string(value: str) -> dict:
    return {"stringValue": value}


def seed_fixtures() -> None:
    firestore_put(f"local_scopes/{SCOPE}/system/settings", {
        "registration_enabled": {"booleanValue": True},
        "email_registration_enabled": {"booleanValue": True},
        "google_registration_enabled": {"booleanValue": False},
    })
    REPORT["fixture_setup"].append({"type": "registration_policy", "seeded": True,
                                   "collection": "local_scopes/{scope}/system"})
    for project_id, owner_id, doc_id, name in (
        ("synthetic-project", "__USER__", "", "Allowed synthetic Project"),
        ("synthetic-unowned", "synthetic-other-owner", "synthetic-other-owner_synthetic-unowned", "Unowned synthetic Project"),
        ("synthetic-proxy-project", "__USER__", "", "Legacy proxy Project"),
    ):
        if owner_id == "__USER__":
            continue
        firestore_put(f"local_scopes/{SCOPE}/projects/{doc_id}", {
            "user_id": fs_string(owner_id), "project_id": fs_string(project_id),
            "name": fs_string(name), "status": fs_string("ready"),
        })
        REPORT["fixture_setup"].append({"type": "project_acl", "owner": "synthetic-other-owner",
                                       "project_id": project_id, "seeded": True})


def seed_owned_projects(user_id: str) -> None:
    for project_id, name in (("synthetic-project", "Allowed synthetic Project"),
                             ("synthetic-proxy-project", "Legacy proxy Project")):
        firestore_put(f"local_scopes/{SCOPE}/projects/{user_id}_{project_id}", {
            "user_id": fs_string(user_id), "project_id": fs_string(project_id),
            "name": fs_string(name), "status": fs_string("ready"),
            "created_at": {"timestampValue": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")},
        })
        REPORT["fixture_setup"].append({"type": "project_acl", "owner": "registered synthetic user",
                                       "project_id": project_id, "seeded": True})


def prepare_config() -> None:
    key_path = SCRATCH / "jwt-key"
    secure_write(key_path, os.urandom(32).hex().encode() + b"\n")
    pipeline_dir = SCRATCH / "local-cac"
    pipeline_dir.mkdir(mode=0o700)
    worker = BIN / "olw_worker"
    values = {
        "PKL_BIN": "/Users/rayer/.local/bin/pkl",
        "PKL_CACHE_DIR": str(SCRATCH / "pkl-cache"),
        "LWC_REPOSITORY_ROOT": str(ROOT),
        "LOCAL_CLOUD_SCOPE": SCOPE,
        "LOCAL_CLOUD_JWT_SECRET_FILE": str(key_path),
        "LOCAL_CLOUD_WORKER_PATH": str(worker),
        "LOCAL_CLOUD_PIPELINE_CONFIG_PATH": str(pipeline_dir / "synthetic-synto.toml"),
        "LOCAL_CLOUD_PIPELINE_BINDINGS_PATH": str(pipeline_dir / "synthetic-bindings.json"),
        "LOCAL_CLOUD_PIPELINE_CONFIG_DIR": str(CONFIG),
        "LOCAL_CLOUD_BFF_CONFIG_PATH": str(CONFIG / "bff.json"),
        "LOCAL_CLOUD_AUTH_CONFIG_PATH": str(CONFIG / "auth" / "auth.json"),
        "GCP_PROJECT": PROJECT,
        "GOOGLE_CLOUD_PROJECT": PROJECT,
        "BUCKET": BUCKET,
        "FIRESTORE_DATABASE_ID": DATABASE,
        "BFF_PORT": str(PORTS["bff"]),
        "AUTH_PORT": str(PORTS["auth"]),
        "FRONTEND_PORT": str(PORTS["frontend"]),
        "AUTH_SERVICE_URL": f"http://localhost:{PORTS['auth']}",
        "ALLOWED_HOSTS": "localhost,127.0.0.1",
        "ALLOWED_ORIGINS": f"http://localhost:{PORTS['frontend']}",
        "DEEPSEEK_API_KEY": "synthetic-no-provider-call",
        "LLM_API_KEY": "synthetic-no-provider-call",
        "TYPESAFE_API_KEY": "synthetic-no-provider-call",
        "TYPESAFE_JEV_API_KEY": "",
        "PIPELINE_DEMO_USER_IDS": "",
        "PYTHONDONTWRITEBYTECODE": "1",
    }
    env = ENV | values
    for target in ("bff", "auth"):
        command(f"CaC prepare target={target} environment=local",
                ["go", "run", "./cmd/pipeline_config", "prepare", "--target", target,
                 "--environment", "local", "--output", str(CONFIG)], env, APP)
        config_path = CONFIG / ("bff.json" if target == "bff" else "auth/auth.json")
        if not config_path.is_file() or config_path.stat().st_mode & 0o777 != 0o600:
            raise RuntimeError(f"CaC produced missing or insecure {target} config")
    REPORT["config"] = {
        "producer": "apps/bff/cmd/pipeline_config prepare --target {bff,auth} --environment local",
        "auth_config_mode": "0600", "bff_config_mode": "0600",
        "auth_origin": f"http://localhost:{PORTS['auth']}",
        "api_origin": f"http://localhost:{PORTS['bff']}",
        "local_cloud_scope": SCOPE,
    }


def build_sources() -> None:
    command("install pinned GCS emulator",
            ["go", "install", GCS_SERVER_MODULE], ENV | {"GOBIN": str(BIN)})
    command("build GateAuth cmd/auth", ["go", "build", "-trimpath", "-o", str(BIN / "gateauth"), "./cmd/auth"],
            ENV, ROOT / "gateauth")
    command("build BFF cmd/bff", ["go", "build", "-trimpath", "-o", str(BIN / "bff"), "./cmd/bff"], ENV, APP)
    command("build current lwc-sync cmd/sync", ["go", "build", "-trimpath", "-o", str(BIN / "lwc-sync-v2"), "./cmd/sync"], ENV, APP)
    command("build real GCS SDK diagnostic helper",
            ["go", "build", "-trimpath", "-o", str(BIN / "sdk_probe"),
             str(Path(__file__).resolve().parent / "sdk_probe.go")], ENV, APP)
    command("build local pipeline worker required by BFF runtime", ["go", "build", "-trimpath", "-o", str(BIN / "olw_worker"), "./cmd/olw_worker"], ENV, APP)
    legacy_root = SCRATCH / "baseline4d290"
    legacy_root.mkdir(mode=0o700)
    archive = subprocess.run(["git", "archive", "--format=tar", BASELINE, "apps/bff"], cwd=ROOT,
                             capture_output=True, check=True).stdout
    with tarfile.open(fileobj=__import__("io").BytesIO(archive), mode="r:") as tar:
        # The archive comes directly from the pinned local Git tree; this also
        # keeps the runner compatible with the installed Python 3.9 runtime.
        tar.extractall(legacy_root)
    command("build baseline 4d290 CLI v1", ["go", "build", "-trimpath", "-o", str(BIN / "lwc-sync-v1"), "./cmd/sync"],
            ENV, legacy_root / "apps" / "bff")
    REPORT["source"] = {
        "candidate_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "gateauth_tree": subprocess.check_output(["git", "rev-parse", "HEAD:gateauth"], cwd=ROOT, text=True).strip(),
        "bff_tree": subprocess.check_output(["git", "rev-parse", "HEAD:apps/bff/cmd/bff"], cwd=ROOT, text=True).strip(),
        "current_cli_tree": subprocess.check_output(["git", "rev-parse", "HEAD:apps/bff/cmd/sync"], cwd=ROOT, text=True).strip(),
        "legacy_cli_commit": BASELINE,
        "legacy_cli_tree": subprocess.check_output(["git", "rev-parse", f"{BASELINE}:apps/bff/cmd/sync"], cwd=ROOT, text=True).strip(),
        "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
        "gcs_emulator": "fake-gcs-server v1.52.1, in-memory backend; XML public host pinned to numeric loopback",
    }


def spawn_storage() -> None:
    global STORAGE_ORIGIN
    port = PORTS["storage"]
    STORAGE_ORIGIN = f"http://127.0.0.1:{port}"
    item = service("storage", [str(BIN / "fake-gcs-server"), "-backend", "memory",
                                "-host", "127.0.0.1", "-public-host", f"127.0.0.1:{port}",
                                "-port", str(port), "-scheme", "http", "-external-url", STORAGE_ORIGIN,
                                "-log-level", "error"], ENV)
    item["port"] = port
    wait_port(port, item)
    status, response, _ = http_json("POST", f"{STORAGE_ORIGIN}/storage/v1/b?{urlencode({'project': PROJECT})}",
                                    {"name": BUCKET, "location": "US-CENTRAL1"})
    if status not in (200, 201, 409):
        raise RuntimeError(f"synthetic storage bucket setup returned HTTP {status}")
    REPORT["fixture_setup"].append({"type": "synthetic_object_storage", "backend": "memory",
                                   "bucket": BUCKET, "seeded_bucket": True, "created_status": status,
                                   "persistence": "service lifetime; actual product writes are read by a fresh CLI process",
                                   "generation_pinned_reads_supported": True})


def spawn_firestore() -> None:
    port = PORTS["firestore"]
    globals()["FIRESTORE_ORIGIN"] = f"http://127.0.0.1:{port}"
    emulator_env = ENV | {"PATH": "/opt/homebrew/opt/openjdk@21/bin:" + ENV["PATH"]}
    item = service("firestore", ["/opt/homebrew/bin/gcloud", "beta", "emulators", "firestore", "start",
                                  f"--host-port=127.0.0.1:{port}"], emulator_env)
    item["port"] = port
    wait_port(port, item, timeout=75)


def spawn_auth() -> None:
    env = ENV | {
        "LWC_APP_CONFIG_PATH": str(CONFIG / "auth" / "auth.json"),
        "PORT": str(PORTS["auth"]),
        "FIRESTORE_EMULATOR_HOST": f"127.0.0.1:{PORTS['firestore']}",
        "GAT4_FIRESTORE_AUDIT_FILE": str(SCRATCH / "auth-firestore-audit.log"),
    }
    secure_write(Path(env["GAT4_FIRESTORE_AUDIT_FILE"]), b"")
    item = service("auth", [str(BIN / "gateauth")], env, APP)
    item["port"] = PORTS["auth"]
    wait_http(AUTH_ORIGIN + "/api/v1/public/version", item)


def spawn_bff() -> None:
    env = ENV | {
        "LWC_BFF_CONFIG_PATH": str(CONFIG / "bff.json"),
        "PORT": str(PORTS["bff"]),
        "FIRESTORE_EMULATOR_HOST": f"127.0.0.1:{PORTS['firestore']}",
        "STORAGE_EMULATOR_HOST": STORAGE_ORIGIN,
    }
    item = service("bff", [str(BIN / "bff")], env, APP)
    item["port"] = PORTS["bff"]
    wait_http(BFF_ORIGIN + "/api/v1/public/version", item, timeout=60)


def create_user() -> tuple[str, str]:
    email = f"gat4-{__import__('secrets').token_hex(6)}@example.test"
    password = __import__("secrets").token_urlsafe(24)
    status, registered, _ = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/register",
                                      {"email": email, "password": password})
    REPORT["http_setup"] = [{"label": "Auth password registration", "status": status}]
    if status != 201 or not registered.get("user_id"):
        raise RuntimeError(f"real Auth registration returned HTTP {status}")
    status, login, _ = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/login",
                                 {"email": email, "password": password})
    REPORT["http_setup"].append({"label": "Auth password login", "status": status})
    if status != 200 or not login.get("access_token") or login.get("user", {}).get("id") != registered["user_id"]:
        raise RuntimeError(f"real Auth login returned HTTP {status}")
    REPORT["identity"] = {"user_id": registered["user_id"], "registered_and_logged_in_via_auth": True}
    return registered["user_id"], login["access_token"]


def context(name: str) -> Path:
    path = SCRATCH / "xdg" / name
    path.mkdir(parents=True, mode=0o700)
    os.chmod(path, 0o700)
    return path


def cli_env(xdg: Path) -> dict[str, str]:
    return ENV | {"XDG_CONFIG_HOME": str(xdg)}


def record_cli(label: str, proc: subprocess.CompletedProcess, pid: int | None = None) -> None:
    stdout = safe_text(proc.stdout or "")
    stderr = safe_text(proc.stderr or "")
    REPORT["commands"].append({"label": label, "exit_code": proc.returncode, "pid": pid,
                               "stdout": stdout[-4000:], "stderr": stderr[-1600:]})
    terminal(f"$ {label} -> exit {proc.returncode} pid={pid}")
    if stdout:
        terminal(stdout)
    if stderr:
        terminal(stderr)


def run_cli(label: str, binary: Path, args: list[str], xdg: Path, timeout: int = 90) -> subprocess.CompletedProcess:
    proc = subprocess.run([str(binary), *args], cwd=APP, env=cli_env(xdg), capture_output=True,
                          text=True, timeout=timeout, check=False)
    record_cli(label, proc)
    return proc


def login_cli(label: str, binary: Path, xdg: Path, extra: list[str]) -> None:
    proc = subprocess.Popen([str(binary), "auth", "login", *extra, "--no-browser"], cwd=APP,
                            env=cli_env(xdg), stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            start_new_session=True)
    lines: list[str] = []
    messages: queue.Queue[bytes | None] = queue.Queue()

    def drain() -> None:
        assert proc.stdout is not None
        for line in proc.stdout:
            messages.put(line)
        messages.put(None)

    reader = threading.Thread(target=drain, daemon=True)
    reader.start()
    pairing_code = None
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline and pairing_code is None:
        try:
            line = messages.get(timeout=0.5)
        except queue.Empty:
            if proc.poll() is not None:
                break
            continue
        if line is None:
            break
        decoded = line.decode(errors="replace")
        lines.append(decoded)
        match = re.search(r"驗證碼\s*[：:]\s*([A-Z0-9]{8})", decoded)
        if match:
            pairing_code = match.group(1)
    if not pairing_code:
        proc.terminate()
        proc.wait(timeout=5)
        reader.join(timeout=2)
        while True:
            try:
                queued = messages.get_nowait()
            except queue.Empty:
                break
            if queued is not None:
                lines.append(queued.decode(errors="replace"))
        output = safe_text("".join(lines))
        record_cli(label, subprocess.CompletedProcess([], proc.returncode or 1, output, "pairing code absent"), proc.pid)
        raise RuntimeError(f"{label} did not expose its actual Auth pairing code")
    status, _, _ = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/cli/pairing/decision",
                             {"user_code": pairing_code, "decision": "approve"},
                             {"Authorization": "Bearer " + WEB_TOKEN})
    lines.append(f"[harness Auth pairing approval HTTP {status}]\n")
    if status != 200:
        proc.terminate()
        proc.wait(timeout=5)
        reader.join(timeout=2)
        record_cli(label, subprocess.CompletedProcess([], 1, "".join(lines), "Auth pairing approval failed"), proc.pid)
        raise RuntimeError(f"actual Auth pairing decision returned HTTP {status}")
    try:
        proc.wait(timeout=20)
    except subprocess.TimeoutExpired:
        proc.terminate()
        proc.wait(timeout=5)
        raise RuntimeError(f"{label} failed to redeem its approved pairing")
    reader.join(timeout=2)
    while True:
        try:
            queued = messages.get_nowait()
        except queue.Empty:
            break
        if queued is not None:
            lines.append(queued.decode(errors="replace"))
    completed = subprocess.CompletedProcess([], proc.returncode, "".join(lines), "")
    record_cli(label, completed, proc.pid)
    if proc.returncode:
        raise RuntimeError(f"{label} failed after Auth approved the pairing")


def config_files(xdg: Path) -> tuple[Path, Path]:
    folder = xdg / "lwc-sync"
    return folder / "config.json", folder / "credentials.json"


def credential_snapshot(path: Path) -> dict:
    data = json.loads(path.read_text())
    return {key: data.get(key) for key in ("auth_host", "session_id", "user_id", "role", "auth_version")}


def private_copy_context(name: str, source_xdg: Path, config_doc: dict | None = None) -> Path:
    new = context(name)
    source_config, source_credentials = config_files(source_xdg)
    target_config, target_credentials = config_files(new)
    target_config.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    data = json.loads(source_config.read_text()) if config_doc is None else config_doc
    secure_write(target_config, (json.dumps(data, separators=(",", ":")) + "\n").encode())
    secure_write(target_credentials, source_credentials.read_bytes())
    return new


def project_json(proc: subprocess.CompletedProcess) -> list[dict]:
    if proc.returncode:
        return []
    try:
        return json.loads(proc.stdout).get("projects", [])
    except (json.JSONDecodeError, AttributeError):
        return []


def binding_metadata(vault: Path) -> dict:
    path = vault / ".lwc-sync.json"
    return json.loads(path.read_text())


def make_vault(name: str, raw: bytes = b"") -> Path:
    vault = SCRATCH / "vaults" / name
    (vault / "raw").mkdir(parents=True, mode=0o700)
    if raw:
        secure_write(vault / "raw" / "synthetic-note.md", raw, 0o600)
    return vault


def copy_binding(vault: Path, binding: dict) -> None:
    secure_write(vault / ".lwc-sync.json", (json.dumps(binding, indent=2) + "\n").encode(), 0o600)


def logs_after(path: Path, offset: int) -> str:
    with path.open("rb") as src:
        src.seek(offset)
        return src.read().decode(errors="replace")


def http_statuses_after(path: Path, offset: int, route: str) -> list[int]:
    statuses = []
    for line in logs_after(path, offset).splitlines():
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get("route") == route and isinstance(entry.get("status_code"), int):
            statuses.append(entry["status_code"])
    return statuses


def read_log(path: Path) -> str:
    try:
        return path.read_text(errors="replace")
    except OSError:
        return ""


def http_probe(label: str, url: str, token: str = "") -> int:
    headers = {"Authorization": "Bearer " + token} if token else {}
    status, _, _ = http_call("GET", url, headers=headers)
    REPORT["http_setup"].append({"label": label, "status": status})
    return status


def start_probe_server(name: str, status_code: int, location: str = "") -> None:
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            self.server.probe_hits += 1
            self.send_response(status_code)
            if location:
                self.send_header("Location", location)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"error":"synthetic_api_probe"}')

        def log_message(self, *_: object) -> None:
            return

    server = ThreadingHTTPServer(("127.0.0.1", PORTS[name]), Handler)
    server.daemon_threads = True
    server.probe_hits = 0
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    HTTP_SERVERS.append(server)
    PROBE_SERVERS.append({"name": name, "server": server, "status": status_code})
    REPORT.setdefault("probe_servers", []).append({"name": name, "port": PORTS[name], "status": status_code})


def storage_readback(object_prefix: str, expected: bytes) -> dict:
    query = urlencode({"prefix": object_prefix, "maxResults": "10"})
    status, listing, _ = http_json("GET", f"{STORAGE_ORIGIN}/storage/v1/b/{BUCKET}/o?{query}")
    if status != 200:
        raise RuntimeError(f"synthetic storage listing returned HTTP {status}")
    objects = listing.get("items", [])
    if len(objects) != 1 or objects[0].get("name") != object_prefix:
        raise RuntimeError("synthetic storage did not persist exactly the expected raw object")
    media = (f"{STORAGE_ORIGIN}/download/storage/v1/b/{BUCKET}/o/"
             f"{quote(object_prefix, safe='')}?alt=media")
    read_status, data, _ = http_call("GET", media)
    if read_status != 200 or data != expected:
        raise RuntimeError(f"synthetic storage direct readback mismatched (HTTP {read_status})")
    attrs = objects[0]
    return {"name": attrs.get("name"), "generation": attrs.get("generation"),
            "size": attrs.get("size"), "sha256_metadata": (attrs.get("metadata") or {}).get("sha256"),
            "readback_sha256": hashlib.sha256(data).hexdigest(),
            "storage_http_list": status, "storage_http_read": read_status}


def storage_sdk_probe(prefix: str) -> dict:
    result = subprocess.run([str(BIN / "sdk_probe"), BUCKET, prefix],
                            cwd=APP, env=ENV | {"STORAGE_EMULATOR_HOST": STORAGE_ORIGIN},
                            capture_output=True, text=True, timeout=60, check=False)
    output = safe_text(result.stdout[-3000:])
    error = safe_text(result.stderr[-1000:])
    terminal(f"$ independent real GCS SDK probe -> exit {result.returncode}")
    if output:
        terminal(output)
    if error:
        terminal(error)
    try:
        parsed = json.loads(result.stdout) if result.stdout else {}
    except json.JSONDecodeError:
        parsed = {}
    return {"exit_code": result.returncode, "result": parsed,
            "stderr": error if result.returncode else ""}


def capture_auth_audit() -> dict:
    path = SCRATCH / "auth-firestore-audit.log"
    text = read_log(path)
    lines = [line for line in text.splitlines() if line.startswith("rpc=")]
    collection_lines = [line for line in lines if "collections=" in line and
                        re.search(r"collections=[^ ]*(users|system|auth_cli_pairings|auth_cli_sessions)", line)]
    forbidden = []
    for line in lines:
        if "forbidden=" in line:
            values = line.split("forbidden=", 1)[1].strip()
            if values:
                forbidden.extend(value for value in values.split(",") if value)
    evidence_ok = bool(lines and collection_lines)
    return {"rpc_count": len(lines), "positive_collection_count": len(collection_lines),
            "positive_evidence": evidence_ok, "forbidden_collections": forbidden,
            "blank_or_header_only_rejected": not evidence_ok,
            "file_mode": oct(path.stat().st_mode & 0o777) if path.exists() else None}


def stop_owned() -> None:
    for server in HTTP_SERVERS:
        try:
            server.shutdown()
            server.server_close()
        except OSError:
            pass
    for item in reversed(PROCESSES):
        proc = item["proc"]
        if proc.poll() is None:
            try:
                os.killpg(proc.pid, signal.SIGTERM)
                proc.wait(timeout=12)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait(timeout=5)
                except (ProcessLookupError, subprocess.TimeoutExpired):
                    pass
    closures = []
    for item in PROCESSES:
        port = item.get("port")
        closed = None
        if port:
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                    closed = False
            except OSError:
                closed = True
        closures.append({"name": item["name"], "pid": item["proc"].pid,
                         "exit_code": item["proc"].poll(), "listener_closed": closed})
    probe_closures = []
    for item in PROBE_SERVERS:
        server = item["server"]
        try:
            with socket.create_connection(("127.0.0.1", server.server_port), timeout=0.3):
                closed = False
        except OSError:
            closed = True
        probe_closures.append({"name": item["name"], "status": item["status"],
                               "hits": server.probe_hits, "listener_closed": closed})
    REPORT["cleanup"] = {"owned_processes": closures,
                         "all_child_processes_exited": all(row["exit_code"] is not None for row in closures),
                         "probe_servers": probe_closures,
                         "all_owned_listeners_closed": all(row["listener_closed"] is True for row in closures + probe_closures)}


def write_report() -> None:
    REPORT["finished_at"] = datetime.now(timezone.utc).isoformat()
    REPORT["terminal_log"] = str(TERMINAL_LOG)
    REPORT["service_logs"] = {p.stem: str(p) for p in LOG_DIR.glob("*.log")}
    REPORT["harness"] = {
        "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "storage_sdk_probe_sha256": hashlib.sha256((Path(__file__).resolve().parent / "sdk_probe.go").read_bytes()).hexdigest(),
    }
    build_codes = [row["exit_code"] for row in REPORT["builds"]]
    cli_codes = [row["exit_code"] for row in REPORT["commands"]]
    REPORT["exit_counts"] = {
        "build_steps": {"total": len(build_codes), "passed": sum(code == 0 for code in build_codes),
                        "failed": sum(code != 0 for code in build_codes)},
        "cli_commands": {"total": len(cli_codes), "passed": sum(code == 0 for code in cli_codes),
                         "nonzero_expected_controls": sum(code != 0 for code in cli_codes)},
    }
    REPORT["auth_firestore_audit"] = capture_auth_audit()
    REPORT["report_path"] = str(REPORT_PATH)
    encoded = (json.dumps(REPORT, indent=2, sort_keys=True) + "\n").encode()
    secure_write(REPORT_PATH, encoded, 0o644)
    os.chmod(REPORT_PATH, 0o644)


def qualify() -> None:
    global AUTH_ORIGIN, BFF_ORIGIN, WEB_TOKEN, USER_ID
    get_ports()
    AUTH_ORIGIN = f"http://localhost:{PORTS['auth']}"
    BFF_ORIGIN = f"http://localhost:{PORTS['bff']}"
    REPORT["ports"] = dict(PORTS)
    REPORT["processes"] = {"service_bind_address": "127.0.0.1", "auth_origin": AUTH_ORIGIN,
                           "api_origin": BFF_ORIGIN, "storage_origin": f"http://127.0.0.1:{PORTS['storage']}",
                           "firestore_origin": f"http://127.0.0.1:{PORTS['firestore']}"}
    build_sources()
    prepare_config()
    spawn_storage()
    spawn_firestore()
    seed_fixtures()
    spawn_auth()
    USER_ID, WEB_TOKEN = create_user()
    seed_owned_projects(USER_ID)
    spawn_bff()

    v2_xdg = context("v2")
    login_cli("v2 CLI actual Auth pairing/login", BIN / "lwc-sync-v2", v2_xdg,
              ["--host", AUTH_ORIGIN, "--api-host", BFF_ORIGIN])
    v2_config, v2_credentials = config_files(v2_xdg)
    cfg = json.loads(v2_config.read_text())
    creds = json.loads(v2_credentials.read_text())
    REPORT["v2_local_files"] = {
        "config_keys": sorted(cfg), "config_auth_host_matches": cfg.get("auth_host") == AUTH_ORIGIN,
        "config_api_host_matches": cfg.get("api_host") == BFF_ORIGIN,
        "config_mode": oct(v2_config.stat().st_mode & 0o777),
        "credentials_keys": sorted(creds), "credentials_auth_host_matches": creds.get("auth_host") == AUTH_ORIGIN,
        "api_host_absent_from_credentials": "api_host" not in creds,
        "credentials_mode": oct(v2_credentials.stat().st_mode & 0o777),
        "session_id_present": bool(creds.get("session_id")), "user_id_matches_registered": creds.get("user_id") == USER_ID,
    }
    if v2_config.stat().st_mode & 0o777 != 0o600 or v2_credentials.stat().st_mode & 0o777 != 0o600:
        raise RuntimeError("CLI configuration or credentials were not mode 0600")
    if creds.get("auth_host") != AUTH_ORIGIN or cfg.get("api_host") != BFF_ORIGIN:
        raise RuntimeError("CLI failed to persist the distinct Auth and API locators")

    projects = run_cli("v2 projects list through explicit BFF API host", BIN / "lwc-sync-v2",
                       ["projects", "--json"], v2_xdg)
    ids = sorted(project["id"] for project in project_json(projects))
    REPORT["checks"]["v2_project_acl_list"] = {"project_ids": ids,
                                               "allowed_only": ids == ["synthetic-project", "synthetic-proxy-project"],
                                               "unowned_absent": "synthetic-unowned" not in ids}
    if not REPORT["checks"]["v2_project_acl_list"]["allowed_only"]:
        raise RuntimeError("v2 BFF Project list did not enforce the seeded owner ACL")

    denied_vault = make_vault("denied")
    bff_log = next(item["log"] for item in PROCESSES if item["name"] == "bff")
    denied_start = bff_log.stat().st_size
    denied = run_cli("v2 direct init against unowned Project", BIN / "lwc-sync-v2",
                     ["init", "--vault", str(denied_vault), "--host", AUTH_ORIGIN,
                      "--project-id", "synthetic-unowned"], v2_xdg)
    denied_statuses = http_statuses_after(bff_log, denied_start, "/api/v1/auth/cli/bindings")
    REPORT["checks"]["v2_unowned_direct_operation_denied"] = {"exit_code": denied.returncode,
                                                              "denied": denied.returncode != 0,
                                                              "bff_binding_route_statuses": denied_statuses,
                                                              "bff_observed_http_403": 403 in denied_statuses}
    if denied.returncode == 0 or 403 not in denied_statuses:
        raise RuntimeError("direct binding operation against unowned Project unexpectedly succeeded")

    raw = b"Synthetic GAT-4 CLI sync evidence.\n"
    vault = make_vault("primary", raw)
    init = run_cli("v2 create/bind local vault", BIN / "lwc-sync-v2",
                   ["init", "--vault", str(vault), "--host", AUTH_ORIGIN,
                    "--project-id", "synthetic-project"], v2_xdg)
    if init.returncode:
        raise RuntimeError("v2 binding creation did not succeed")
    first_binding = binding_metadata(vault)
    original_identity = {k: first_binding.get(k) for k in ("host", "binding_id", "wiki_id", "project_id")}
    if original_identity["host"] != AUTH_ORIGIN or original_identity["project_id"] != "synthetic-project":
        raise RuntimeError("vault identity did not preserve the Auth issuer and selected Project")
    REPORT["v2_vault"] = {"metadata_keys": sorted(first_binding), "identity": original_identity,
                          "metadata_mode": oct((vault / ".lwc-sync.json").stat().st_mode & 0o777)}

    upload = run_cli("v2 actual raw sync upload to BFF", BIN / "lwc-sync-v2",
                     ["push", "--vault", str(vault), "--sync"], v2_xdg, timeout=120)
    if upload.returncode:
        prefix = f"local_scopes/{SCOPE}/users/{USER_ID}/projects/synthetic-project/raw/"
        REPORT["checks"]["storage_sdk_diagnostic"] = storage_sdk_probe(prefix)
        try:
            REPORT["checks"]["storage_http_diagnostic"] = storage_readback(prefix + "synthetic-note.md", raw)
        except Exception as probe_error:
            REPORT["checks"]["storage_http_diagnostic"] = {"error": safe_text(str(probe_error))}
        raise RuntimeError("v2 actual sync upload failed")
    object_name = f"local_scopes/{SCOPE}/users/{USER_ID}/projects/synthetic-project/raw/synthetic-note.md"
    storage = storage_readback(object_name, raw)
    REPORT["checks"]["persisted_storage_sync"] = {"cli_upload_exit": upload.returncode,
                                                  "storage": storage,
                                                  "actual_product_write_and_read": storage["readback_sha256"] == hashlib.sha256(raw).hexdigest()}

    readback_vault = make_vault("readback")
    copy_binding(readback_vault, first_binding)
    readback = run_cli("v2 fresh-process remote sync readback", BIN / "lwc-sync-v2",
                       ["push", "--vault", str(readback_vault), "--sync"], v2_xdg, timeout=120)
    readback_file = readback_vault / "raw" / "synthetic-note.md"
    REPORT["checks"]["cli_readback_matches_persisted_content"] = {
        "exit_code": readback.returncode, "file_exists": readback_file.is_file(),
        "sha256": hashlib.sha256(readback_file.read_bytes()).hexdigest() if readback_file.is_file() else None,
        "matches": readback.returncode == 0 and readback_file.is_file() and readback_file.read_bytes() == raw,
        "restart_session_preserved": credential_snapshot(v2_credentials).get("session_id") == creds.get("session_id"),
    }
    if not REPORT["checks"]["cli_readback_matches_persisted_content"]["matches"]:
        raise RuntimeError("fresh CLI process did not read the persisted sync content")

    before_refresh = json.loads(v2_credentials.read_text())
    refresh = run_cli("v2 explicit credential refresh at Auth", BIN / "lwc-sync-v2",
                      ["auth", "refresh", "--host", AUTH_ORIGIN], v2_xdg)
    after_refresh = json.loads(v2_credentials.read_text())
    REPORT["checks"]["refresh_stays_at_auth"] = {"exit_code": refresh.returncode,
                                                 "auth_host": after_refresh.get("auth_host"),
                                                 "refresh_rotated": before_refresh.get("refresh_token") != after_refresh.get("refresh_token"),
                                                 "credentials_mode": oct(v2_credentials.stat().st_mode & 0o777),
                                                 "api_host_absent_from_credentials": "api_host" not in after_refresh}
    if (refresh.returncode or not REPORT["checks"]["refresh_stays_at_auth"]["refresh_rotated"] or
            REPORT["checks"]["refresh_stays_at_auth"]["credentials_mode"] != "0o600"):
        raise RuntimeError("Auth refresh did not rotate the local CLI credentials")

    bad = json.loads(v2_credentials.read_text())
    old_refresh = bad["refresh_token"]
    bad["access_token"] = "synthetic-invalid-access-token"
    secure_write(v2_credentials, (json.dumps(bad, separators=(",", ":")) + "\n").encode())
    upstream = http_probe("BFF rejects scoped invalid access-token control", BFF_ORIGIN + "/api/v1/auth/cli/projects",
                          "synthetic-invalid-access-token")
    auth_log = next(item["log"] for item in PROCESSES if item["name"] == "auth")
    refresh_start = auth_log.stat().st_size
    refreshed_list = run_cli("v2 BFF 401 causes one Auth refresh then succeeds", BIN / "lwc-sync-v2",
                             ["projects", "--json"], v2_xdg)
    auth_delta = logs_after(auth_log, refresh_start)
    refresh_calls = len(re.findall(r'POST\s+"?/api/v1/auth/cli/refresh', auth_delta))
    rotated_after_401 = json.loads(v2_credentials.read_text())
    REPORT["checks"]["bff_401_refresh_once"] = {"captured_bff_401": upstream == 401,
                                                "cli_exit_code": refreshed_list.returncode,
                                                "auth_refresh_requests": refresh_calls,
                                                "refresh_token_rotated": old_refresh != rotated_after_401.get("refresh_token"),
                                                "credentials_mode": oct(v2_credentials.stat().st_mode & 0o777),
                                                "success_after_refresh": refreshed_list.returncode == 0}
    if (upstream != 401 or refreshed_list.returncode or refresh_calls != 1 or
            old_refresh == rotated_after_401.get("refresh_token") or
            REPORT["checks"]["bff_401_refresh_once"]["credentials_mode"] != "0o600"):
        raise RuntimeError("BFF 401 did not produce exactly one successful Auth refresh")

    restart = run_cli("v2 CLI restart continues same session and locator", BIN / "lwc-sync-v2",
                      ["auth", "status", "--host", AUTH_ORIGIN], v2_xdg)
    current_creds = credential_snapshot(v2_credentials)
    REPORT["checks"]["cli_restart_continuity"] = {"exit_code": restart.returncode,
                                                   "session_id_unchanged": current_creds.get("session_id") == creds.get("session_id"),
                                                   "auth_host_unchanged": current_creds.get("auth_host") == AUTH_ORIGIN,
                                                   "vault_identity_unchanged": binding_metadata(vault) == first_binding}
    if restart.returncode or not REPORT["checks"]["cli_restart_continuity"]["session_id_unchanged"]:
        raise RuntimeError("fresh CLI process did not continue the same Auth session")

    # A current v2 binary with the legacy auth_host-only config must proxy via Auth.
    legacy_v2_xdg = private_copy_context("v2-legacy-config", v2_xdg, {"auth_host": AUTH_ORIGIN})
    auth_log_before = auth_log.stat().st_size
    legacy_v2_list = run_cli("current v2 legacy auth_host-only config through Auth proxy",
                             BIN / "lwc-sync-v2", ["projects", "--json"], legacy_v2_xdg)
    proxy_delta = logs_after(auth_log, auth_log_before)
    legacy_config_path, legacy_cred_path = config_files(legacy_v2_xdg)
    REPORT["checks"]["v2_legacy_config_proxy"] = {
        "exit_code": legacy_v2_list.returncode, "auth_host_only_config": sorted(json.loads(legacy_config_path.read_text())) == ["auth_host"],
        "credentials_mode": oct(legacy_cred_path.stat().st_mode & 0o777),
        "auth_proxy_route_observed": "/api/v1/auth/cli/projects" in proxy_delta,
        "listed_allowed_projects": sorted(p["id"] for p in project_json(legacy_v2_list)) == ["synthetic-project", "synthetic-proxy-project"],
    }
    if legacy_v2_list.returncode:
        raise RuntimeError("current CLI with legacy auth_host-only config failed through Auth proxy")

    # Reauthorize is an explicit replacement generation in the frozen baseline.
    before_reauth = binding_metadata(vault)
    reauth = run_cli("v2 explicit binding reauthorization", BIN / "lwc-sync-v2",
                     ["binding", "reauthorize", "--vault", str(vault), "--project-id", "synthetic-project"], v2_xdg)
    after_reauth = binding_metadata(vault)
    stable = all(before_reauth.get(field) == after_reauth.get(field) for field in ("host", "wiki_id", "project_id"))
    changed = before_reauth.get("binding_id") != after_reauth.get("binding_id")
    REPORT["checks"]["reauthorization_identity"] = {
        "exit_code": reauth.returncode, "issuer_host_preserved": before_reauth.get("host") == after_reauth.get("host"),
        "wiki_id_preserved": before_reauth.get("wiki_id") == after_reauth.get("wiki_id"),
        "project_id_preserved": before_reauth.get("project_id") == after_reauth.get("project_id"),
        "binding_id_preserved": not changed, "baseline_generation_rotated": changed,
        "before_identity": {k: before_reauth.get(k) for k in ("host", "binding_id", "wiki_id", "project_id")},
        "after_identity": {k: after_reauth.get(k) for k in ("host", "binding_id", "wiki_id", "project_id")},
        "new_metadata_matches_product_response": after_reauth.get("binding_id") != "" and stable,
        "interpretation": "baseline explicit reauthorization replaces the binding ID; coordinator directed this be reported as an interpretation conflict, not a new product defect",
    }
    if reauth.returncode or not stable:
        raise RuntimeError("explicit reauthorization did not preserve stable host/wiki/project identity")
    reauthorized_sync = run_cli("v2 sync continues after explicit reauthorization", BIN / "lwc-sync-v2",
                               ["push", "--vault", str(vault), "--sync"], v2_xdg, timeout=120)
    REPORT["checks"]["reauthorized_binding_usable"] = {"exit_code": reauthorized_sync.returncode,
                                                       "success": reauthorized_sync.returncode == 0}
    if reauthorized_sync.returncode:
        raise RuntimeError("replacement binding could not continue syncing the same vault")

    # Build and exercise an actual old client from the requested baseline.
    v1_xdg = context("v1-baseline")
    login_cli("baseline 4d290 actual CLI v1 Auth pairing/login", BIN / "lwc-sync-v1", v1_xdg,
              ["--host", AUTH_ORIGIN])
    v1_config, v1_credentials = config_files(v1_xdg)
    v1_auth_log = next(item["log"] for item in PROCESSES if item["name"] == "auth")
    v1_proxy_start = v1_auth_log.stat().st_size
    v1_list = run_cli("baseline 4d290 CLI v1 projects through stateless Auth proxy",
                      BIN / "lwc-sync-v1", ["projects", "--json"], v1_xdg)
    v1_ids = sorted(p["id"] for p in project_json(v1_list))
    v1_vault = make_vault("v1-proxy")
    v1_init = run_cli("baseline 4d290 CLI v1 binding create through Auth proxy",
                      BIN / "lwc-sync-v1", ["init", "--vault", str(v1_vault), "--host", AUTH_ORIGIN,
                                             "--project-id", "synthetic-proxy-project"], v1_xdg)
    v1_meta = binding_metadata(v1_vault) if (v1_vault / ".lwc-sync.json").is_file() else {}
    v1_config_doc = json.loads(v1_config.read_text()) if v1_config.exists() else {}
    v1_cred_doc = json.loads(v1_credentials.read_text()) if v1_credentials.exists() else {}
    v1_proxy_delta = logs_after(v1_auth_log, v1_proxy_start)
    REPORT["checks"]["actual_v1_auth_proxy"] = {
        "source_commit": BASELINE, "list_exit_code": v1_list.returncode, "listed_only_owned_projects": v1_ids == ["synthetic-project", "synthetic-proxy-project"],
        "binding_exit_code": v1_init.returncode, "binding_created_through_auth_locator": v1_init.returncode == 0,
        "config_keys": sorted(v1_config_doc), "auth_host_matches": v1_config_doc.get("auth_host") == AUTH_ORIGIN,
        "api_host_absent": "api_host" not in v1_config_doc and "api_host" not in v1_cred_doc,
        "credentials_mode": oct(v1_credentials.stat().st_mode & 0o777) if v1_credentials.exists() else None,
        "auth_proxy_project_route_observed": "/api/v1/auth/cli/projects" in v1_proxy_delta,
        "auth_proxy_binding_route_observed": "/api/v1/auth/cli/bindings" in v1_proxy_delta,
        "vault_identity": {k: v1_meta.get(k) for k in ("host", "binding_id", "wiki_id", "project_id")},
    }
    if (v1_list.returncode or v1_init.returncode or
            not REPORT["checks"]["actual_v1_auth_proxy"]["auth_proxy_project_route_observed"] or
            not REPORT["checks"]["actual_v1_auth_proxy"]["auth_proxy_binding_route_observed"]):
        raise RuntimeError("baseline CLI v1 did not use the real Auth compatibility proxy")

    # Explicit v2 api_host errors and redirects must be returned as errors, never fall back.
    start_probe_server("probe404", 404)
    no_fallback_xdg = private_copy_context("v2-explicit-api-no-fallback", v2_xdg,
                                           {"auth_host": AUTH_ORIGIN, "api_host": f"http://localhost:{PORTS['probe404']}"})
    auth_before_404 = auth_log.stat().st_size
    no_fallback = run_cli("v2 explicit api_host 404 has no Auth fallback", BIN / "lwc-sync-v2",
                          ["projects", "--json"], no_fallback_xdg)
    auth_delta_404 = logs_after(auth_log, auth_before_404)
    start_probe_server("probe302", 302, AUTH_ORIGIN + "/api/v1/auth/cli/projects?page_size=100")
    no_redirect_xdg = private_copy_context("v2-explicit-api-no-redirect", v2_xdg,
                                           {"auth_host": AUTH_ORIGIN, "api_host": f"http://localhost:{PORTS['probe302']}"})
    auth_before_302 = auth_log.stat().st_size
    no_redirect = run_cli("v2 explicit api_host redirect is not followed", BIN / "lwc-sync-v2",
                          ["projects", "--json"], no_redirect_xdg)
    auth_delta_302 = logs_after(auth_log, auth_before_302)
    REPORT["checks"]["v2_explicit_api_no_fallback"] = {
        "api_404_exit_code": no_fallback.returncode, "api_404_not_fallback_success": no_fallback.returncode != 0,
        "api_404_probe_hits": next(p.probe_hits for p in HTTP_SERVERS if p.server_port == PORTS["probe404"]),
        "auth_product_route_not_hit_after_404": "/api/v1/auth/cli/projects" not in auth_delta_404,
        "redirect_exit_code": no_redirect.returncode, "redirect_not_followed": no_redirect.returncode != 0,
        "redirect_probe_hits": next(p.probe_hits for p in HTTP_SERVERS if p.server_port == PORTS["probe302"]),
        "auth_product_route_not_hit_after_redirect": "/api/v1/auth/cli/projects" not in auth_delta_302,
    }
    if (no_fallback.returncode == 0 or no_redirect.returncode == 0 or
            REPORT["checks"]["v2_explicit_api_no_fallback"]["api_404_probe_hits"] < 1 or
            REPORT["checks"]["v2_explicit_api_no_fallback"]["redirect_probe_hits"] < 1):
        raise RuntimeError("v2 explicit API host unexpectedly fell back or followed a redirect")

    revoke = run_cli("v2 product binding revoke", BIN / "lwc-sync-v2",
                     ["binding", "revoke", "--project-id", "synthetic-project",
                      "--binding-id", after_reauth["binding_id"]], v2_xdg)
    revoke_sync_start = bff_log.stat().st_size
    rejected = run_cli("v2 subsequent raw sync access rejected after revoke", BIN / "lwc-sync-v2",
                       ["push", "--vault", str(vault), "--sync"], v2_xdg, timeout=120)
    revoke_sync_statuses = http_statuses_after(bff_log, revoke_sync_start, "/api/v1/sync/raw")
    cli_saw_forbidden = "HTTP 403" in (rejected.stderr or "")
    bff_saw_forbidden = 403 in revoke_sync_statuses
    REPORT["checks"]["revoke_rejects_subsequent_sync"] = {"revoke_exit_code": revoke.returncode,
                                                        "subsequent_sync_exit_code": rejected.returncode,
                                                        "rejected": rejected.returncode != 0,
                                                        "cli_observed_http_403": cli_saw_forbidden,
                                                        "bff_sync_route_statuses": revoke_sync_statuses,
                                                        "bff_observed_http_403": bff_saw_forbidden}
    if revoke.returncode or rejected.returncode == 0 or not cli_saw_forbidden or not bff_saw_forbidden:
        raise RuntimeError("BFF binding revoke did not reject the subsequent actual sync request")

    audit = capture_auth_audit()
    REPORT["checks"]["auth_firestore_audit"] = audit
    if not audit["positive_evidence"]:
        raise RuntimeError("Auth Firestore audit was blank/header-only and cannot substantiate zero forbidden access")
    if audit["forbidden_collections"]:
        raise RuntimeError("Auth Firestore audit found forbidden Project/binding collection access")
    REPORT["checks"]["auth_stateless_proxy_zero_product_firestore"] = {
        "positive_auth_firestore_rpc_and_collection_evidence": True,
        "forbidden_project_or_binding_collections": audit["forbidden_collections"],
        "v1_and_v2_legacy_auth_proxy_exercised": True,
    }
    REPORT["qualification"] = {
        "cli_acceptance_2": "PARTIAL: pairing, ACL, binding, sync, restart, refresh, and revoke pass; migration/restart preserves binding identity, while explicit baseline reauthorization rotates binding ID with host/Wiki/Project stable, so ID-preservation wording remains an interpretation conflict (not a product defect)",
        "cli_acceptance_7": "PASS: actual baseline CLI v1 and current v2 legacy config used Auth proxy; explicit v2 api_host produced no Auth fallback or redirect",
        "persisted_sync": "PASS: product wrote to loopback GCS emulator and a fresh CLI process read exact bytes back",
        "qualification_complete": False,
    }


def main() -> int:
    result = 0
    try:
        qualify()
    except Exception as exc:
        result = 1
        REPORT["failure"] = {"type": type(exc).__name__, "message": safe_text(str(exc))}
        terminal(f"FAIL: {type(exc).__name__}: {safe_text(str(exc))}")
    finally:
        stop_owned()
        REPORT["harness_exit_code"] = result
        try:
            write_report()
        except Exception as exc:
            result = 1
            terminal(f"report write failed: {type(exc).__name__}")
    print(json.dumps({"exit_code": result, "report": str(REPORT_PATH),
                      "terminal_log": str(TERMINAL_LOG), "scratch": str(SCRATCH)}, sort_keys=True))
    return result


if __name__ == "__main__":
    raise SystemExit(main())
