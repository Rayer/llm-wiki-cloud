#!/usr/bin/env python3
"""Run isolated password/demo/Google bootstrap recovery against owned loopback services."""

from __future__ import annotations

import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from datetime import datetime, timezone
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[4]
APP = ROOT / "apps" / "bff"
AUTH = ROOT / "gateauth"
HELPER = Path(__file__).resolve().parent
REPORT_PATH = HELPER / "report.json"
PROJECT = "llm-wiki-cloud"
DATABASE = "llm-wiki-cloud-local"
BUCKET = "llm-wiki-cloud-local"
GCS_SERVER_MODULE = "github.com/fsouza/fake-gcs-server@v1.52.1"
REGISTRATION_FIXTURE = "local_scopes/{scope}/system/settings"


def required_tmpdir() -> Path:
    value = os.environ.get("TMPDIR", "").strip()
    if not value:
        raise SystemExit("TMPDIR must be explicitly set to an existing writable private scratch directory; no fallback is used")
    try:
        path = Path(value).expanduser().resolve(strict=True)
    except OSError:
        raise SystemExit("TMPDIR must name an existing writable private scratch directory")
    if not path.is_dir() or not os.access(path, os.W_OK | os.X_OK):
        raise SystemExit("TMPDIR must name an existing writable private scratch directory")
    return path


SCRATCH_ROOT = required_tmpdir()
SCOPE = "gat4-bootstrap-" + __import__("secrets").token_hex(10)
RUN = Path(tempfile.mkdtemp(prefix="gat4-bootstrap-recovery-", dir=SCRATCH_ROOT))
os.chmod(RUN, 0o700)
os.environ["TMPDIR"] = str(RUN)
BIN = RUN / "bin"
BIN.mkdir(mode=0o700)
CONFIG = RUN / "config"
CONFIG.mkdir(mode=0o700)
LOGS = RUN / "logs"
LOGS.mkdir(mode=0o700)
CLOUDSDK = RUN / "cloudsdk"
CLOUDSDK.mkdir(mode=0o700)
AUDIT_AUTH_1 = RUN / "auth-first-firestore-audit.log"
AUDIT_AUTH_2 = RUN / "auth-restarted-firestore-audit.log"
AUDIT_GOOGLE = RUN / "google-test-firestore-audit.log"
TERMINAL = LOGS / "terminal.log"
TERMINAL.touch(mode=0o600)
os.chmod(TERMINAL, 0o600)

SAFE_KEYS = {
    "PATH", "HOME", "LANG", "LC_ALL", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN",
    "GOROOT", "GOCACHE", "GOMODCACHE", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
    "SSL_CERT_FILE", "SSL_CERT_DIR",
}
ENV = {k: v for k, v in os.environ.items() if k in SAFE_KEYS}
ENV.update({
    "TMPDIR": str(RUN),
    "CLOUDSDK_CONFIG": str(CLOUDSDK),
    "CLOUDSDK_CORE_PROJECT": PROJECT,
    "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
    "GOOGLE_CLOUD_PROJECT": PROJECT,
    "GCE_METADATA_HOST": "127.0.0.1:1",
    "GCE_METADATA_IP": "127.0.0.1",
    "NO_PROXY": "localhost,127.0.0.1,::1",
    "no_proxy": "localhost,127.0.0.1,::1",
    "PYTHONDONTWRITEBYTECODE": "1",
})
ENV.pop("CLOUDSDK_AUTH_ACCESS_TOKEN", None)
ENV.pop("GCLOUD_PROJECT", None)
ENV.pop("GOOGLE_OAUTH_ACCESS_TOKEN", None)
ENV["GOCACHE"] = str(RUN / "go-cache")
ENV.setdefault("GOMODCACHE", str(Path.home() / "go" / "pkg" / "mod"))

PORTS: dict[str, int] = {}
for port_name in ("firestore", "storage", "auth", "bff"):
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        candidate = int(probe.getsockname()[1])
    while candidate in PORTS.values():
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            candidate = int(probe.getsockname()[1])
    PORTS[port_name] = candidate
FIRESTORE_ORIGIN = f"http://127.0.0.1:{PORTS['firestore']}"
STORAGE_ORIGIN = f"http://127.0.0.1:{PORTS['storage']}"
AUTH_ORIGIN = f"http://localhost:{PORTS['auth']}"
BFF_ORIGIN = f"http://localhost:{PORTS['bff']}"
JWT_KEY_FILE = RUN / "jwt-key"
FIRESTORE_REST_ROOT = f"{FIRESTORE_ORIGIN}/v1/projects/{PROJECT}/databases/{DATABASE}/documents"
BINARIES: dict[str, Path] = {}
PROCESSES: list[dict] = []
REPORT: dict = {
    "schema": "gat4-bootstrap-recovery-v1",
    "started_at": datetime.now(timezone.utc).isoformat(),
    "source": {},
    "commands": [],
    "http": [],
    "fixtures": [],
    "checks": {},
    "processes": [],
    "not_run": [
        "External Google OIDC credentials/provider/browser login; Google coverage uses the repository's existing local fake-provider test seam and real emulator persistence.",
        "Browser automation and frontend unit tests; callers and tests are source-inspected only.",
        "Cloud services, live accounts, IAM, deployment, publication, production readback, and provider/LLM calls.",
        "GAT-4 acceptance outside identity/bootstrap parts of acceptance 1 and acceptance 5.",
    ],
}
PRIMARY_PRIVATE: dict = {}
EXPECTED_HTTP_NON2XX = {
    "password_first_bootstrap_auth_outage": {500, 502, 503},
    "mismatch_target_rejected": {409},
}


def safe_text(text: str) -> str:
    text = re.sub(r"(?im)^.*(?:verification code|user code).*?$", "[code redacted]", text)
    text = re.sub(r"(?i)(Bearer\s+)[^\s\"']+", r"\1[redacted]", text)
    text = re.sub(r"(?i)(access_token|refresh_token|token|cookie|password|client_secret)(\s*[:=]\s*\"?)[^\s,\"}]+",
                  r"\1\2[redacted]", text)
    return text


def log(line: str) -> None:
    with TERMINAL.open("a", encoding="utf-8") as output:
        output.write(safe_text(line.rstrip()) + "\n")
    os.chmod(TERMINAL, 0o600)


def secure_write(path: Path, data: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name(path.name + ".tmp")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, "wb") as output:
        output.write(data)
        output.flush()
        os.fsync(output.fileno())
    os.chmod(temp, mode)
    os.replace(temp, path)
    os.chmod(path, mode)


def command(label: str, args: list[str], cwd: Path, env: dict[str, str] | None = None,
            timeout: int = 240) -> subprocess.CompletedProcess:
    result = subprocess.run(args, cwd=cwd, env=env or ENV, capture_output=True, text=True,
                            timeout=timeout, check=False)
    REPORT["commands"].append({"label": label, "argv": [str(item) for item in args],
                               "cwd": str(cwd.relative_to(ROOT) if cwd.is_relative_to(ROOT) else cwd),
                               "exit_code": result.returncode})
    log(f"$ {label} -> exit {result.returncode}")
    if result.stdout:
        log(result.stdout[-6000:])
    if result.stderr:
        log(result.stderr[-6000:])
    if result.returncode:
        raise RuntimeError(f"{label} failed with exit {result.returncode}")
    return result


def start_service(name: str, args: list[str], cwd: Path, env: dict[str, str], port: int | None = None) -> dict:
    logfile = LOGS / f"{name}.log"
    logfile.touch(mode=0o600)
    os.chmod(logfile, 0o600)
    sink = logfile.open("ab", buffering=0)
    proc = subprocess.Popen(args, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                            stdout=sink, stderr=subprocess.STDOUT, start_new_session=True)
    sink.close()
    item = {"name": name, "proc": proc, "port": port, "log": logfile}
    PROCESSES.append(item)
    log(f"started {name} pid={proc.pid}")
    return item


def port_open(port: int) -> bool:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.2):
            return True
    except OSError:
        return False


def wait_port(item: dict, timeout: float = 60) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if item["proc"].poll() is not None:
            raise RuntimeError(f"{item['name']} exited during startup")
        if item["port"] is not None and port_open(item["port"]):
            return
        time.sleep(0.15)
    raise TimeoutError(f"{item['name']} did not open its owned loopback listener")


def http_call(method: str, url: str, body: dict | bytes | None = None,
              headers: dict[str, str] | None = None, timeout: int = 20) -> tuple[int, bytes, dict]:
    raw = json.dumps(body).encode() if isinstance(body, dict) else body
    request_headers = dict(headers or {})
    if isinstance(body, dict):
        request_headers.setdefault("Content-Type", "application/json")
    request = Request(url, data=raw, method=method, headers=request_headers)
    try:
        with urlopen(request, timeout=timeout) as response:
            return response.status, response.read(4 * 1024 * 1024), dict(response.headers)
    except HTTPError as response:
        return response.code, response.read(1024 * 1024), dict(response.headers)


def http_json(method: str, url: str, body: dict | None = None,
              headers: dict[str, str] | None = None) -> tuple[int, dict, dict]:
    status, content, response_headers = http_call(method, url, body, headers)
    try:
        value = json.loads(content) if content else {}
    except json.JSONDecodeError:
        value = {}
    return status, value if isinstance(value, dict) else {"items": value}, response_headers


def event(label: str, status: int, *, user_id: str = "", project_id: str = "",
          error_code: str = "", cookie_present: bool | None = None) -> None:
    row = {"label": label, "status": status}
    if user_id:
        row["user_id"] = user_id
    if project_id:
        row["default_project_id"] = project_id
    if error_code:
        row["error_code"] = error_code
    if cookie_present is not None:
        row["refresh_cookie_present"] = cookie_present
    REPORT["http"].append(row)


def wait_http(url: str, item: dict, timeout: int = 45) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if item["proc"].poll() is not None:
            tail = item["log"].read_text(errors="replace")[-1000:]
            raise RuntimeError(f"{item['name']} exited before HTTP readiness: {safe_text(tail)}")
        try:
            status, _, _ = http_call("GET", url, timeout=2)
            if status < 500:
                return
        except (OSError, URLError, TimeoutError):
            pass
        time.sleep(0.2)
    raise TimeoutError(f"{item['name']} HTTP readiness timed out")


def firestore_url(path: str) -> str:
    return FIRESTORE_REST_ROOT + "/" + quote(path, safe="/")


def firestore_get(path: str) -> tuple[int, dict]:
    status, body, _ = http_json("GET", firestore_url(path))
    return status, body


def firestore_patch(path: str, fields: dict, masks: list[str] | None = None) -> int:
    suffix = ""
    if masks:
        suffix = "?" + "&".join("updateMask.fieldPaths=" + quote(mask, safe="") for mask in masks)
    status, _, _ = http_json("PATCH", firestore_url(path) + suffix, {"fields": fields})
    return status


def firestore_list(path: str) -> list[dict]:
    all_docs: list[dict] = []
    next_token = ""
    while True:
        suffix = "?pageSize=1000"
        if next_token:
            suffix += "&pageToken=" + quote(next_token, safe="")
        status, response, _ = http_json("GET", firestore_url(path) + suffix)
        if status == 404:
            return all_docs
        if status != 200:
            raise RuntimeError(f"Firestore emulator list failed at collection segment {path.split('/')[-1]}")
        all_docs.extend(response.get("documents", []))
        next_token = response.get("nextPageToken", "")
        if not next_token:
            return all_docs


def fs_encode(value):
    if value is None:
        return {"nullValue": None}
    if isinstance(value, bool):
        return {"booleanValue": value}
    if isinstance(value, int):
        return {"integerValue": str(value)}
    if isinstance(value, float):
        return {"doubleValue": value}
    if isinstance(value, str):
        return {"stringValue": value}
    if isinstance(value, list):
        return {"arrayValue": {"values": [fs_encode(x) for x in value]}}
    if isinstance(value, dict):
        return {"mapValue": {"fields": {k: fs_encode(v) for k, v in value.items()}}}
    raise TypeError(type(value))


def fs_decode(value):
    if "stringValue" in value:
        return value["stringValue"]
    if "booleanValue" in value:
        return value["booleanValue"]
    if "integerValue" in value:
        return int(value["integerValue"])
    if "doubleValue" in value:
        return value["doubleValue"]
    if "timestampValue" in value:
        return value["timestampValue"]
    if "nullValue" in value:
        return None
    if "arrayValue" in value:
        return [fs_decode(x) for x in value["arrayValue"].get("values", [])]
    if "mapValue" in value:
        return {k: fs_decode(v) for k, v in value["mapValue"].get("fields", {}).items()}
    return value


def fs_fields(data: dict) -> dict:
    return {key: fs_encode(value) for key, value in data.items()}


def fs_data(document: dict) -> dict:
    return {key: fs_decode(value) for key, value in document.get("fields", {}).items()}


def local_path(collection_path: str) -> str:
    return f"local_scopes/{SCOPE}/{collection_path}"


def count_identity_projects(user_id: str, project_id: str) -> dict:
    projects = firestore_list(local_path("projects"))
    matches = []
    for doc in projects:
        data = fs_data(doc)
        if data.get("user_id") == user_id and data.get("project_id") == project_id:
            matches.append({"document_id": doc["name"].rsplit("/", 1)[-1], "data": data})
    metadata = firestore_list(local_path(f"users/{user_id}/projects"))
    target_metadata = [doc for doc in metadata if doc["name"].rsplit("/", 1)[-1] == project_id]
    return {
        "registry_count": len(matches),
        "registry": matches,
        "metadata_count": len(target_metadata),
        "metadata": [{"document_id": doc["name"].rsplit("/", 1)[-1], "data": fs_data(doc)} for doc in target_metadata],
    }


def query_projects(token: str) -> tuple[int, list[dict]]:
    status, result, _ = http_json("GET", BFF_ORIGIN + "/api/v1/projects", headers={"Authorization": "Bearer " + token})
    return status, result.get("items", []) if isinstance(result, dict) else []


def bootstrap(token: str, label: str) -> tuple[int, dict]:
    status, result, _ = http_json("POST", BFF_ORIGIN + "/api/v1/onboarding/default-project",
                                 {"schema_version": 1}, {"Authorization": "Bearer " + token})
    event(label, status, project_id=str(result.get("default_project_id", "")),
          error_code=str(result.get("error", "")))
    return status, result


def local_auth_env(audit_path: Path) -> dict[str, str]:
    return ENV | {
        "LWC_APP_CONFIG_PATH": str(CONFIG / "auth" / "auth.json"),
        "PORT": str(PORTS["auth"]),
        "FIRESTORE_EMULATOR_HOST": f"127.0.0.1:{PORTS['firestore']}",
        "LOCAL_CLOUD_SCOPE": SCOPE,
        "GAT4_FIRESTORE_AUDIT_FILE": str(audit_path),
    }


def stop_service(item: dict, timeout: float = 8) -> dict:
    proc = item["proc"]
    if proc.poll() is None:
        try:
            os.killpg(proc.pid, signal.SIGTERM)
            proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait(timeout=5)
    closed = item["port"] is None or not port_open(item["port"])
    record = {"name": item["name"], "pid": proc.pid, "exit_code": proc.returncode,
              "listener_closed": closed}
    REPORT["processes"].append(record)
    log(f"stopped {item['name']} pid={proc.pid} exit={proc.returncode} listener_closed={closed}")
    return record


def kill_all() -> None:
    for item in reversed(PROCESSES):
        if item.get("stopped"):
            continue
        stop_service(item)
        item["stopped"] = True


def prepare_sources() -> None:
    required = ["go", "gcloud", "pkl"]
    missing = [name for name in required if not shutil.which(name)]
    if missing:
        raise RuntimeError("missing local build tools: " + ",".join(missing))
    java21 = next((path for path in (Path("/opt/homebrew/opt/openjdk@21/bin"),
                                     Path("/opt/homebrew/opt/openjdk/bin")) if path.is_dir()), None)
    if java21 is None:
        raise RuntimeError("Java 21 installation required for local Firestore emulator")
    env = ENV | {"PATH": str(java21) + os.pathsep + ENV.get("PATH", "")}
    command("build GateAuth cmd/auth", ["go", "build", "-trimpath", "-o", str(BIN / "gateauth"), "./cmd/auth"], AUTH)
    BINARIES["auth"] = BIN / "gateauth"
    command("build BFF cmd/bff", ["go", "build", "-trimpath", "-o", str(BIN / "bff"), "./cmd/bff"], APP)
    BINARIES["bff"] = BIN / "bff"
    command("build local BFF pipeline worker", ["go", "build", "-trimpath", "-o", str(BIN / "olw_worker"), "./cmd/olw_worker"], APP)
    BINARIES["worker"] = BIN / "olw_worker"
    command("install pinned in-memory GCS emulator", ["go", "install", GCS_SERVER_MODULE],
            ROOT, ENV | {"GOBIN": str(BIN)}, timeout=300)
    BINARIES["storage"] = BIN / "fake-gcs-server"
    REPORT["source"] = {
        "tested_lwc_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "gateauth_tree": subprocess.check_output(["git", "rev-parse", "HEAD:gateauth"], cwd=ROOT, text=True).strip(),
        "bff_tree": subprocess.check_output(["git", "rev-parse", "HEAD:apps/bff"], cwd=ROOT, text=True).strip(),
        "bff_onboarding_blob": subprocess.check_output(["git", "rev-parse", "HEAD:apps/bff/internal/handler/v1/onboarding.go"], cwd=ROOT, text=True).strip(),
        "bff_authconsumer_blob": subprocess.check_output(["git", "rev-parse", "HEAD:apps/bff/internal/authconsumer/client.go"], cwd=ROOT, text=True).strip(),
        "google_test_blob": subprocess.check_output(["git", "rev-parse", "HEAD:gateauth/internal/auth/oauth_flow_firestore_test.go"], cwd=ROOT, text=True).strip(),
        "go_version": subprocess.check_output(["go", "version"], cwd=ROOT, text=True).strip(),
        "gcs_emulator": "fake-gcs-server v1.52.1, in-memory backend; explicit loopback storage origin",
    }
    verify_component_source_invariant()
    secure_write(JWT_KEY_FILE, os.urandom(32).hex().encode() + b"\n")


def verify_component_source_invariant() -> None:
    current_auth = subprocess.check_output(["git", "rev-parse", "HEAD:gateauth"], cwd=ROOT, text=True).strip()
    base = "a945375776a29c192ed1042161915f676540b08d"
    prior_auth = subprocess.check_output(["git", "rev-parse", f"{base}:gateauth"], cwd=ROOT, text=True).strip()
    REPORT["source"]["gateauth_tree_matches_prior_cli_base"] = current_auth == prior_auth
    REPORT["source"]["tested_go_business_tree"] = current_auth
    if current_auth != prior_auth:
        raise RuntimeError("GateAuth Go source changed from the declared bootstrap qualification base; re-check source attribution")
    main_ref = "701b1257075fb13bb468f68860206f3b1de5b4e5"
    paths = ("gateauth/cmd/auth", "gateauth/internal", "apps/bff/cmd/bff", "apps/bff/internal",
             "gateauth/go.mod", "gateauth/go.sum", "apps/bff/go.mod", "apps/bff/go.sum")
    tested = {path: subprocess.check_output(["git", "rev-parse", f"HEAD:{path}"], cwd=ROOT, text=True).strip()
              for path in paths}
    main_source = {path: subprocess.check_output(["git", "rev-parse", f"{main_ref}:{path}"], cwd=ROOT, text=True).strip()
                   for path in paths}
    REPORT["source"]["main_source_reference"] = main_ref
    REPORT["source"]["tested_go_component_trees"] = tested
    REPORT["source"]["main_go_component_trees"] = main_source
    REPORT["source"]["tested_go_source_matches_main_reference"] = tested == main_source
    if tested != main_source:
        raise RuntimeError("tested GateAuth/BFF Go component source differs from Main 701b125; confirm runtime source identity")


def prepare_configs() -> None:
    pkl = shutil.which("pkl")
    if not pkl:
        raise RuntimeError("pkl executable unavailable")
    pipeline_dir = RUN / "pipeline"
    pipeline_dir.mkdir(mode=0o700)
    values = {
        "PKL_BIN": pkl,
        "PKL_CACHE_DIR": str(RUN / "pkl-cache"),
        "LWC_REPOSITORY_ROOT": str(ROOT),
        "LOCAL_CLOUD_SCOPE": SCOPE,
        "LOCAL_CLOUD_JWT_SECRET_FILE": str(JWT_KEY_FILE),
        "LOCAL_CLOUD_WORKER_PATH": str(BIN / "olw_worker"),
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
        "FRONTEND_PORT": str(PORTS["bff"]),
        "AUTH_DEMO_USER_ID": "gat4-bootstrap-demo",
        "AUTH_DEMO_USER_EMAIL": "gat4-bootstrap-demo@example.test",
        "AUTH_DEMO_USER_ROLE": "member",
        "AUTH_SERVICE_URL": AUTH_ORIGIN,
        "ALLOWED_HOSTS": "localhost,127.0.0.1",
        "ALLOWED_ORIGINS": BFF_ORIGIN,
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
                 "--environment", "local", "--output", str(CONFIG)], APP, env, timeout=240)
        path = CONFIG / "bff.json" if target == "bff" else CONFIG / "auth" / "auth.json"
        if not path.is_file() or path.stat().st_mode & 0o777 != 0o600:
            raise RuntimeError(f"CaC produced missing or nonprivate {target} config")
    REPORT["config"] = {
        "producer": "apps/bff/cmd/pipeline_config prepare --target {bff,auth} --environment local",
        "scope": SCOPE,
        "auth_origin": AUTH_ORIGIN,
        "bff_origin": BFF_ORIGIN,
        "storage_origin": STORAGE_ORIGIN,
        "firestore_origin": FIRESTORE_ORIGIN,
        "auth_config_mode": "0o600",
        "bff_config_mode": "0o600",
        "jwt_key_mode": oct(JWT_KEY_FILE.stat().st_mode & 0o777),
        "auth_port_input": PORTS["auth"],
        "auth_listen_port_from_materialized_config": json.loads((CONFIG / "auth" / "auth.json").read_text()).get("port"),
    }


def start_emulators_and_apps() -> None:
    java21 = next(path for path in (Path("/opt/homebrew/opt/openjdk@21/bin"),
                                     Path("/opt/homebrew/opt/openjdk/bin")) if path.is_dir())
    emulator_env = ENV | {"PATH": str(java21) + os.pathsep + ENV.get("PATH", "")}
    fs = start_service("firestore", [shutil.which("gcloud"), "beta", "emulators", "firestore", "start",
                                     f"--host-port=127.0.0.1:{PORTS['firestore']}"], ROOT, emulator_env,
                       PORTS["firestore"])
    wait_port(fs, 90)
    storage = start_service("storage", [str(BIN / "fake-gcs-server"), "-backend", "memory", "-host", "127.0.0.1",
                                        "-public-host", f"127.0.0.1:{PORTS['storage']}", "-port", str(PORTS["storage"]),
                                        "-scheme", "http", "-external-url", STORAGE_ORIGIN, "-log-level", "error"],
                            ROOT, ENV, PORTS["storage"])
    wait_port(storage)
    status, _, _ = http_json("POST", f"{STORAGE_ORIGIN}/storage/v1/b?{urlencode({'project': PROJECT})}",
                             {"name": BUCKET, "location": "US-CENTRAL1"})
    if status not in (200, 201, 409):
        raise RuntimeError(f"synthetic storage bucket creation returned HTTP {status}")
    REPORT["fixtures"].append({"type": "synthetic_in_memory_object_storage", "bucket_seeded": True, "created_status": status})
    cfg_path = CONFIG / "auth" / "auth.json"
    secure_write(AUDIT_AUTH_1, b"")
    auth1 = start_service("auth-first", [str(BIN / "gateauth")], APP,
                          local_auth_env(AUDIT_AUTH_1), PORTS["auth"])
    wait_http(AUTH_ORIGIN + "/api/v1/public/version", auth1)
    REPORT["fixtures"].append({"type": "seeded_registration_policy", "path": REGISTRATION_FIXTURE,
                               "values": {"registration_enabled": True, "email_registration_enabled": True,
                                          "google_registration_enabled": True}})
    fs_patch_path = local_path("system/settings")
    status = firestore_patch(fs_patch_path, {
        "registration_enabled": {"booleanValue": True},
        "email_registration_enabled": {"booleanValue": True},
        "google_registration_enabled": {"booleanValue": True},
    })
    if status not in (200, 201):
        raise RuntimeError(f"registration policy fixture write returned HTTP {status}")
    bff_env = ENV | {
        "LWC_BFF_CONFIG_PATH": str(CONFIG / "bff.json"),
        "PORT": str(PORTS["bff"]),
        "FIRESTORE_EMULATOR_HOST": f"127.0.0.1:{PORTS['firestore']}",
        "LOCAL_CLOUD_SCOPE": SCOPE,
        "STORAGE_EMULATOR_HOST": STORAGE_ORIGIN,
    }
    bff = start_service("bff", [str(BIN / "bff")], APP, bff_env, PORTS["bff"])
    wait_http(BFF_ORIGIN + "/api/v1/public/version", bff, 60)
    REPORT["fixtures"].append({"type": "synthetic_demo_identity", "user_id": "gat4-bootstrap-demo",
                               "role": "member", "creation": "Auth process startup ensureDemoAccountAtStartup"})
    auth_log = next(item["log"] for item in PROCESSES if item["name"] == "auth-first")
    expected_listener = f"Auth service listening on 127.0.0.1:{PORTS['auth']}"
    listener_lines = [line.split("Auth service listening on", 1)[1].strip()
                      for line in auth_log.read_text(errors="replace").splitlines()
                      if "Auth service listening on" in line]
    if f"127.0.0.1:{PORTS['auth']}" not in listener_lines:
        raise RuntimeError("Auth runtime PORT listener did not match the configured localhost origin")
    REPORT["config"]["auth_listen_port_from_materialized_config"] = json.loads(cfg_path.read_text()).get("port")
    REPORT["config"]["auth_runtime_listen_PORT_env"] = PORTS["auth"]
    REPORT["config"]["auth_observed_listen_line"] = expected_listener


def register(email: str, password: str, label: str) -> tuple[str, str, str]:
    status, result, _ = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/register", {"email": email, "password": password})
    user_id = str(result.get("user_id", ""))
    project_id = str(result.get("default_project_id", ""))
    token = str(result.get("token", ""))
    event(label, status, user_id=user_id, project_id=project_id)
    if status != 201 or not user_id or not token or not project_id:
        raise RuntimeError(f"{label} failed identity registration status={status}")
    return user_id, project_id, token


def login(email: str, password: str, label: str) -> tuple[str, str, bool]:
    status, result, headers = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/login", {"email": email, "password": password})
    user_id = str((result.get("user") or {}).get("id", ""))
    token = str(result.get("access_token", ""))
    cookie = any(name.lower() == "set-cookie" and "refresh_token=" in value
                 for name, value in headers.items())
    event(label, status, user_id=user_id, cookie_present=cookie)
    if status != 200 or not user_id or not token:
        raise RuntimeError(f"{label} failed Auth login status={status}")
    return user_id, token, cookie


def auth_identity(user_id: str) -> dict:
    status, document = firestore_get(local_path(f"users/{user_id}"))
    if status != 200:
        raise RuntimeError("Auth identity document could not be read back from the owned emulator")
    return fs_data(document)


def assert_no_product_documents(user_id: str) -> bool:
    registry = firestore_list(local_path("projects"))
    registry_matches = [doc for doc in registry if fs_data(doc).get("user_id") == user_id]
    metadata = firestore_list(local_path(f"users/{user_id}/projects"))
    return not registry_matches and not metadata


def registered_password_recovery() -> dict:
    entropy = __import__("secrets").token_hex(8)
    email = f"gat4-bootstrap-{entropy}@example.test"
    password = __import__("secrets").token_urlsafe(24)
    user_id, project_id, registration_token = register(email, password, "password_register")
    PRIMARY_PRIVATE.update({"user_id": user_id, "email": email, "password": password})
    initial_user = auth_identity(user_id)
    no_product_before = assert_no_product_documents(user_id)
    if project_id != "default" or initial_user.get("default_project") != project_id or not no_product_before:
        raise RuntimeError("registration did not commit the identity-only default pointer boundary")
    prebootstrap_user_id, prebootstrap_token, prebootstrap_cookie = login(
        email, password, "password_login_before_first_bootstrap")
    if prebootstrap_user_id != user_id or not prebootstrap_cookie:
        raise RuntimeError("initial login did not preserve the just-registered identity and session")
    prebootstrap_list_status, prebootstrap_projects = query_projects(prebootstrap_token)
    event("password_project_list_before_bootstrap", prebootstrap_list_status)
    if prebootstrap_list_status != 200 or any(item.get("id") == project_id for item in prebootstrap_projects):
        raise RuntimeError("fresh identity project list did not remain empty before BFF bootstrap")

    # A controlled shutdown creates a real BFF-to-Auth outage after the identity commit.
    auth_before = next(item for item in PROCESSES if item["name"] == "auth-first")
    stop_service(auth_before)
    auth_before["stopped"] = True
    status, result = bootstrap(prebootstrap_token, "password_first_bootstrap_auth_outage")
    if status < 500 or not assert_no_product_documents(user_id):
        raise RuntimeError(f"owned Auth outage did not produce a retryable BFF failure (HTTP {status})")

    secure_write(AUDIT_AUTH_2, b"")
    restarted = start_service("auth-restarted", [str(BIN / "gateauth")], APP,
                              local_auth_env(AUDIT_AUTH_2), PORTS["auth"])
    wait_http(AUTH_ORIGIN + "/api/v1/public/version", restarted, 60)
    login_id, login_token, refresh_cookie = login(email, password, "password_login_after_auth_restart")
    if login_id != user_id or not refresh_cookie:
        raise RuntimeError("login recovery did not return the original user and a refresh session cookie")
    status, result = bootstrap(login_token, "password_bootstrap_retry_after_login")
    if status != 200 or result.get("default_project_id") != project_id or result.get("status") != "ready":
        raise RuntimeError(f"password recovery bootstrap failed status={status}")

    list_status, listed = query_projects(login_token)
    listed_default = [item for item in listed if item.get("id") == project_id]
    pair = count_identity_projects(user_id, project_id)
    if list_status != 200 or len(listed_default) != 1 or pair["registry_count"] != 1 or pair["metadata_count"] != 1:
        raise RuntimeError("password bootstrap did not produce exactly one product pair and list the default Project")
    return {
        "email_domain": "example.test",
        "registered_user_id": user_id,
        "login_user_id": login_id,
        "registration_default_project_id": project_id,
        "auth_identity_pointer": initial_user.get("default_project"),
        "identity_only_before_bootstrap": no_product_before,
        "prebootstrap_login_user_id": prebootstrap_user_id,
        "prebootstrap_login_refresh_cookie_present": prebootstrap_cookie,
        "prebootstrap_project_list_status": prebootstrap_list_status,
        "prebootstrap_project_ids": [item.get("id") for item in prebootstrap_projects],
        "unmodified_web_caller_would_see_empty_list": not prebootstrap_projects,
        "login_refresh_cookie_present": refresh_cookie,
        "first_bootstrap_http_status": next(x["status"] for x in REPORT["http"] if x["label"] == "password_first_bootstrap_auth_outage"),
        "failure_class": "owned Auth HTTP process outage between Auth identity commit and BFF bootstrap; not a Firestore outage",
        "retry_http_status": status,
        "listed_default_project": listed_default[0],
        "pair_counts": {"registry": pair["registry_count"], "legacy_metadata": pair["metadata_count"]},
        "registry_document_id": pair["registry"][0]["document_id"],
        "metadata_document_id": pair["metadata"][0]["document_id"],
        "metadata": pair["metadata"][0]["data"],
        "registry": pair["registry"][0]["data"],
        "product_documents_absent_after_first_failure": True,
    }


def concurrent_bootstrap_case() -> dict:
    entropy = __import__("secrets").token_hex(8)
    email, password = f"gat4-bootstrap-concurrent-{entropy}@example.test", __import__("secrets").token_urlsafe(24)
    user_id, project_id, _ = register(email, password, "concurrent_password_register")
    login_id, token, cookie = login(email, password, "concurrent_password_login")
    if user_id != login_id or not cookie or not assert_no_product_documents(user_id):
        raise RuntimeError("concurrency fixture identity was not product-free before BFF initialization")
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        futures = [pool.submit(bootstrap, token, f"concurrent_bootstrap_{index}") for index in range(6)]
        results = [future.result() for future in futures]
    if any(status != 200 or body.get("default_project_id") != project_id for status, body in results):
        raise RuntimeError("concurrent bootstrap requests did not all return the same default Project identity")
    repeated_status, repeated = bootstrap(token, "concurrent_followup_bootstrap")
    pair = count_identity_projects(user_id, project_id)
    if repeated_status != 200 or repeated.get("default_project_id") != project_id or pair["registry_count"] != 1 or pair["metadata_count"] != 1:
        raise RuntimeError("concurrent/repeated bootstrap did not leave exactly one registry+metadata pair")
    listed_status, listed = query_projects(token)
    if listed_status != 200 or len([x for x in listed if x.get("id") == project_id]) != 1:
        raise RuntimeError("concurrent Project is not visible through the actual BFF Project list")
    return {"user_id": user_id, "project_id": project_id, "concurrent_calls": len(results),
            "successful_statuses": [status for status, _ in results], "repeat_status": repeated_status,
            "registry_count": pair["registry_count"], "metadata_count": pair["metadata_count"],
            "listed_once": True}


def preservation_case(primary: dict) -> dict:
    user_id = primary["registered_user_id"]
    project_id = primary["registration_default_project_id"]
    # Re-authenticate through the real Auth route; no token is persisted in the report.
    email = PRIMARY_PRIVATE["email"]
    password = PRIMARY_PRIVATE["password"]
    login_id, token, cookie = login(email, password, "password_login_for_idempotence")
    if login_id != user_id or not cookie:
        raise RuntimeError("idempotence login did not preserve Auth identity")
    registry_path = local_path(f"projects/{user_id}_{project_id}")
    meta_path = local_path(f"users/{user_id}/projects/{project_id}")
    registry_status, registry_doc = firestore_get(registry_path)
    meta_status, meta_doc = firestore_get(meta_path)
    if registry_status != 200 or meta_status != 200:
        raise RuntimeError("product-created docs absent before preservation control")
    registry_before = fs_data(registry_doc)
    metadata_before = fs_data(meta_doc)
    sentinel_binding = {
        "binding_id": "synthetic-preservation-binding",
        "host": AUTH_ORIGIN,
        "wiki_id": "synthetic-wiki-preserve",
        "project_id": project_id,
        "status": "active",
        "environment": "local",
    }
    sentinel_profile = {"profile_id": "synthetic-preservation-profile", "marker": "retain"}
    registry_with_sentinels = dict(registry_before)
    registry_with_sentinels.update({"sync_binding": sentinel_binding, "profile": sentinel_profile})
    if firestore_patch(registry_path, {"sync_binding": fs_encode(sentinel_binding),
                                       "profile": fs_encode(sentinel_profile)},
                       masks=["sync_binding", "profile"]) != 200:
        raise RuntimeError("could not add post-bootstrap preservation sentinels")
    before_repeat_status, before_repeat_body = bootstrap(token, "password_existing_project_idempotent_repeat")
    after_status, after_doc = firestore_get(registry_path)
    meta_after_status, meta_after_doc = firestore_get(meta_path)
    after_registry, after_metadata = fs_data(after_doc), fs_data(meta_after_doc)
    stable_fields = ("user_id", "project_id", "name", "status", "created_at", "sync_binding", "profile")
    registry_preserved = all(after_registry.get(k) == registry_with_sentinels.get(k) for k in stable_fields)
    metadata_preserved = after_metadata == metadata_before
    if (before_repeat_status != 200 or before_repeat_body.get("default_project_id") != project_id or
            after_status != 200 or meta_after_status != 200 or not registry_preserved or not metadata_preserved):
        raise RuntimeError("idempotent bootstrap changed existing name/time/status/binding/profile/metadata")
    return {
        "fixture_condition": "synthetic binding/profile sentinels added only after the real first bootstrap created both documents",
        "repeat_status": before_repeat_status,
        "registry_fields_preserved": list(stable_fields),
        "registry_values_unchanged": registry_preserved,
        "legacy_metadata_unchanged": metadata_preserved,
    }


def mismatch_case() -> dict:
    entropy = __import__("secrets").token_hex(8)
    email, password = f"gat4-bootstrap-conflict-{entropy}@example.test", __import__("secrets").token_urlsafe(24)
    user_id, project_id, _ = register(email, password, "conflict_password_register")
    login_id, token, cookie = login(email, password, "conflict_password_login")
    if user_id != login_id or not cookie or not assert_no_product_documents(user_id):
        raise RuntimeError("conflict-control identity was not product-free before setup")
    metadata_path = local_path(f"users/{user_id}/projects/{project_id}")
    status = firestore_patch(metadata_path, fs_fields({
        "user_id": "synthetic-conflicting-owner",
        "project_id": project_id,
        "name": "Preserve this conflict",
        "created_at": "2026-10-29T00:00:00Z",
    }))
    if status not in (200, 201):
        raise RuntimeError("owned conflict fixture could not be seeded")
    seeded_status, seeded_doc = firestore_get(metadata_path)
    seeded = fs_data(seeded_doc)
    before_registry = firestore_list(local_path("projects"))
    before_registry_count = len([doc for doc in before_registry if fs_data(doc).get("user_id") == user_id])
    if seeded_status != 200:
        raise RuntimeError("could not read back exact synthetic mismatch fixture")
    bootstrap_status, body = bootstrap(token, "mismatch_target_rejected")
    after_registry = firestore_list(local_path("projects"))
    after_registry_count = len([doc for doc in after_registry if fs_data(doc).get("user_id") == user_id])
    _, after_doc = firestore_get(metadata_path)
    unchanged = fs_data(after_doc) == seeded
    if bootstrap_status != 409 or body.get("error") != "setup_conflict" or after_registry_count != before_registry_count or not unchanged:
        raise RuntimeError(f"target mismatch did not return 409 without overwrite (HTTP {bootstrap_status})")
    return {
        "user_id": user_id,
        "project_id": project_id,
        "fixture_type": "synthetic target metadata owner mismatch",
        "bootstrap_status": bootstrap_status,
        "error": body.get("error"),
        "registry_write_count_delta": after_registry_count - before_registry_count,
        "conflicting_metadata_unchanged": unchanged,
    }


def demo_case() -> dict:
    status, result, headers = http_json("POST", AUTH_ORIGIN + "/api/v1/auth/demo", {})
    user = result.get("user") or {}
    user_id, token = str(user.get("id", "")), str(result.get("access_token", ""))
    role = str(user.get("role", ""))
    cookie = any(name.lower() == "set-cookie" and "refresh_token=" in value
                 for name, value in headers.items())
    event("demo_login", status, user_id=user_id, cookie_present=cookie)
    if status != 200 or not user_id or not token or role.lower() == "admin" or not cookie:
        raise RuntimeError("real Auth demo login did not return fixed non-admin identity/session")
    before_user = auth_identity(user_id)
    no_product_before = assert_no_product_documents(user_id)
    project_id = str(before_user.get("default_project", ""))
    if project_id != "default" or not no_product_before:
        raise RuntimeError("demo startup/login did not preserve identity-only default pointer")
    bootstrap_status, body = bootstrap(token, "demo_default_project_bootstrap")
    list_status, listed = query_projects(token)
    project_items = [item for item in listed if item.get("id") == project_id]
    pair = count_identity_projects(user_id, project_id)
    if bootstrap_status != 200 or body.get("default_project_id") != project_id or list_status != 200 or len(project_items) != 1 or pair["registry_count"] != 1 or pair["metadata_count"] != 1:
        raise RuntimeError("demo login bootstrap did not initialize/list one default Project")
    return {
        "user_id": user_id,
        "role": role,
        "non_admin": True,
        "refresh_cookie_present": cookie,
        "startup_identity_only_before_bootstrap": no_product_before,
        "default_project_id": project_id,
        "bootstrap_status": bootstrap_status,
        "project_list_status": list_status,
        "listed_default_project_once": True,
        "registry_count": pair["registry_count"],
        "metadata_count": pair["metadata_count"],
    }


def run_google_business_seam() -> dict:
    overlay_map = {
        str(AUTH / "internal" / "auth" / "gat4_bootstrap_overlay_test.go"): str(HELPER / "google_overlay_test.go")
    }
    overlay_path = HELPER / "google-overlay.json"
    secure_write(overlay_path, json.dumps({"Replace": overlay_map}, indent=2).encode() + b"\n")
    report_path = RUN / "google-result.json"
    test_env = ENV | {
        "FIRESTORE_EMULATOR_HOST": f"127.0.0.1:{PORTS['firestore']}",
        "GOOGLE_CLOUD_PROJECT": PROJECT,
        "LOCAL_CLOUD_SCOPE": SCOPE,
        "GAT4_FIRESTORE_AUDIT_FILE": str(AUDIT_GOOGLE),
        "GAT4_BOOTSTRAP_PROJECT": PROJECT,
        "GAT4_BOOTSTRAP_DATABASE": DATABASE,
        "GAT4_BOOTSTRAP_SCOPE": SCOPE,
        "GAT4_BOOTSTRAP_JWT_SECRET_FILE": str(JWT_KEY_FILE),
        "GAT4_BOOTSTRAP_BFF_ORIGIN": BFF_ORIGIN,
        "GAT4_BOOTSTRAP_RESULT_FILE": str(report_path),
        "GAT4_BOOTSTRAP_EMAIL_NONCE": __import__("secrets").token_hex(8),
    }
    secure_write(AUDIT_GOOGLE, b"")
    command("Google JIT/new + existing identity business flow with overlay and actual BFF", [
        "go", "test", "-overlay=" + str(overlay_path), "./internal/auth",
        "-run", "^TestGAT4BootstrapGoogleBusinessFlow$", "-count=1", "-v",
    ], AUTH, test_env, timeout=240)
    if not report_path.is_file():
        raise RuntimeError("Google business test did not produce structured nonsecret result")
    result = json.loads(report_path.read_text())
    REPORT["fixtures"].append({
        "type": "existing_google_auth_identity",
        "user_id": result["existing_login"]["user_id"],
        "seeded_via": "production IdentityRepository.ProvisionExternalUser test seam before real callback",
        "product_documents_before_bootstrap": True,
    })
    REPORT["fixtures"].append({
        "type": "new_google_jit_identity",
        "user_id": result["new_jit"]["user_id"],
        "creation": "real callback JIT branch with existing local fake provider and emulator",
        "product_documents_before_bootstrap": True,
    })
    return result


def assess_auth_audit(path: Path) -> dict:
    text = path.read_text(errors="replace") if path.exists() else ""
    lines = [line for line in text.splitlines() if line.startswith("rpc=")]
    positive = [line for line in lines if "collections=" in line and re.search(r"collections=[^ ]*(users|system|auth_cli_sessions|external_identities|auth_email_reservations)", line)]
    forbidden = []
    field_count = 0
    for line in lines:
        if "forbidden=" in line:
            field_count += 1
            values = line.split("forbidden=", 1)[1].strip()
            if values:
                forbidden.extend(x for x in values.split(",") if x)
    coverage = bool(lines) and field_count == len(lines)
    reason = None
    if not lines:
        reason = "no_rpc_records"
    elif not positive:
        reason = "no_positive_identity_collection_evidence"
    elif not coverage:
        reason = "incomplete_forbidden_collection_evidence"
    elif forbidden:
        reason = "forbidden_project_or_binding_access"
    return {"path": str(path), "mode": oct(path.stat().st_mode & 0o777) if path.exists() else None,
            "rpc_count": len(lines), "identity_collection_rpc_count": len(positive),
            "rpc_lines_with_forbidden_field": field_count, "forbidden_field_coverage_complete": coverage,
            "forbidden_collections": sorted(set(forbidden)), "evidence_accepted": reason is None,
            "rejection_reason": reason}


def audit_negative_controls() -> dict:
    directory = RUN / "audit-negative-controls"
    directory.mkdir(mode=0o700)
    blank, header = directory / "blank.log", directory / "header-only.log"
    secure_write(blank, b"")
    secure_write(header, b"# GAT4 Firestore RPC audit v1; collection names only\n")
    blank_result, header_result = assess_auth_audit(blank), assess_auth_audit(header)
    result = {
        "blank_log_evidence_accepted": blank_result["evidence_accepted"],
        "blank_log_rejection_reason": blank_result["rejection_reason"],
        "blank_log_mode": blank_result["mode"],
        "header_only_log_evidence_accepted": header_result["evidence_accepted"],
        "header_only_log_rejection_reason": header_result["rejection_reason"],
        "header_only_log_mode": header_result["mode"],
        "both_controls_rejected": not blank_result["evidence_accepted"] and not header_result["evidence_accepted"],
    }
    if not result["both_controls_rejected"]:
        raise RuntimeError("Auth audit guard accepted blank/header-only evidence")
    return result


def frontend_source_review() -> dict:
    files = {
        "auth_context": ROOT / "apps/frontend/src/lib/auth.tsx",
        "workspace_provider": ROOT / "apps/frontend/src/components/WorkspaceProvider.tsx",
        "google_completion": ROOT / "apps/frontend/src/components/GoogleCompletionClient.tsx",
        "shell": ROOT / "apps/frontend/src/components/Shell.tsx",
    }
    texts = {name: path.read_text() for name, path in files.items()}
    test_files = {
        "demo_auth": ROOT / "apps/frontend/tests/lwc-366-demo-auth.test.tsx",
        "google_hydration": ROOT / "apps/frontend/tests/lwc-317-google-hydration.test.tsx",
        "demo_session": ROOT / "apps/frontend/tests/lwc-135-demo-session.test.mjs",
    }
    tests = {name: path.read_text() for name, path in test_files.items()}
    source_bootstrap_call = any("/api/v1/onboarding/default-project" in text for text in texts.values())
    return {
        "source_files": {name: {"path": str(path.relative_to(ROOT)),
                                "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                         for name, path in files.items()},
        "auth_apply_session_persists_auth_response": "persistAuthSession(" in texts["auth_context"],
        "password_register_login_demo_use_auth_apply": all(token in texts["auth_context"] for token in (
            "const login =", "const loginAsDemo =", "const register =", "applyAuthResponse(")),
        "workspace_reentry_only_loads_projects": "void loadProjects();" in texts["workspace_provider"],
        "bootstrap_endpoint_called_by_reviewed_frontend_callers": source_bootstrap_call,
        "pending_or_retry_state_found": bool(re.search(r"product_setup_pending|onboarding/default-project", "\n".join(texts.values()))),
        "google_completion_hydrates_then_routes": "getHydratedSession()" in texts["google_completion"] and "router.replace(consumeGoogleLoginReturnTo())" in texts["google_completion"],
        "jit_event_only_opens_project_rename": "setRenameTarget" in texts["shell"],
        "reviewed_test_files": {name: {"path": str(path.relative_to(ROOT)),
                                        "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                                for name, path in test_files.items()},
        "demo_unit_test_covers_auth_storage_not_bootstrap": "readStoredDemoSession" in tests["demo_auth"] and "default-project" not in tests["demo_auth"],
        "google_unit_test_covers_cookie_hydration_not_bootstrap": "google/complete" in tests["google_hydration"] and "default-project" not in tests["google_hydration"],
        "demo_session_test_covers_auth_state_not_bootstrap": "persistAuthSession" in tests["demo_session"] and "default-project" not in tests["demo_session"],
        "reviewed_unit_tests_exercised_by_this_harness": False,
        "review_kind": "source-only; not a browser run",
        "recovery_call_gap": not source_bootstrap_call,
    }


def stop_probe_services() -> None:
    kill_all()


def finalize_report(exit_code: int, error: str = "") -> None:
    REPORT["finished_at"] = datetime.now(timezone.utc).isoformat()
    REPORT["scratch_path"] = str(RUN)
    REPORT["terminal_log"] = str(TERMINAL)
    REPORT["auth_firestore_audits"] = [
        assess_auth_audit(AUDIT_AUTH_1),
        assess_auth_audit(AUDIT_AUTH_2),
        assess_auth_audit(AUDIT_GOOGLE),
    ]
    REPORT["auth_audit_negative_controls"] = audit_negative_controls()
    REPORT["frontend_source_review"] = frontend_source_review()
    REPORT["cleanup"] = {
        "all_owned_child_processes_exited": all(item["proc"].poll() is not None for item in PROCESSES),
        "all_owned_listeners_closed": all(item["port"] is None or not port_open(item["port"]) for item in PROCESSES),
        "process_records": REPORT["processes"],
    }
    REPORT["exit_counts"] = {
        "commands_total": len(REPORT["commands"]),
        "commands_passed": sum(item["exit_code"] == 0 for item in REPORT["commands"]),
        "commands_failed": sum(item["exit_code"] != 0 for item in REPORT["commands"]),
        "recorded_http_events": len(REPORT["http"]),
        "http_non_2xx": sum(item["status"] < 200 or item["status"] >= 300 for item in REPORT["http"]),
        "expected_http_non2xx_controls": [
            {"label": row["label"], "status": row["status"]}
            for row in REPORT["http"] if row["label"] in EXPECTED_HTTP_NON2XX and
            row["status"] in EXPECTED_HTTP_NON2XX[row["label"]]
        ],
        "unexpected_http_non2xx": [
            {"label": row["label"], "status": row["status"]}
            for row in REPORT["http"] if (row["status"] < 200 or row["status"] >= 300) and
            (row["label"] not in EXPECTED_HTTP_NON2XX or
             row["status"] not in EXPECTED_HTTP_NON2XX[row["label"]])
        ],
    }
    REPORT["harness"] = {
        "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "google_overlay_sha256": hashlib.sha256((HELPER / "google_overlay_test.go").read_bytes()).hexdigest(),
        "overlay_map_sha256": hashlib.sha256((HELPER / "google-overlay.json").read_bytes()).hexdigest() if (HELPER / "google-overlay.json").exists() else None,
    }
    REPORT["report_path"] = str(REPORT_PATH)
    REPORT["harness_exit_code"] = exit_code
    if error:
        REPORT["failure"] = safe_text(error)
    temp = REPORT_PATH.with_suffix(".tmp")
    temp.write_text(json.dumps(REPORT, indent=2, sort_keys=True) + "\n")
    os.replace(temp, REPORT_PATH)
    os.chmod(REPORT_PATH, 0o644)


def main() -> int:
    exit_code, error = 0, ""
    try:
        prepare_sources()
        prepare_configs()
        start_emulators_and_apps()

        # Product registration is Auth-only; no product document is seeded before the HTTP bootstrap.
        policy_path = local_path("system/settings")
        REPORT["fixtures"].append({"type": "registration_policy", "path": policy_path,
                                   "seeded": True, "purpose": "allow synthetic password and Google identity registration"})
        primary = registered_password_recovery()
        REPORT["checks"]["password_identity_commit_failure_reentry_retry"] = primary
        preservation = primary_preservation_through_login()
        REPORT["checks"]["existing_project_preservation"] = preservation
        REPORT["checks"]["concurrent_bootstrap"] = concurrent_bootstrap_case()
        REPORT["checks"]["mismatch_rejected_without_writes"] = mismatch_case()
        REPORT["checks"]["demo_identity_bootstrap"] = demo_case()
        REPORT["checks"]["google_jit_and_existing_login"] = run_google_business_seam()
        audits = [assess_auth_audit(AUDIT_AUTH_1), assess_auth_audit(AUDIT_AUTH_2), assess_auth_audit(AUDIT_GOOGLE)]
        if any(not item["evidence_accepted"] for item in audits) or any(item["forbidden_collections"] for item in audits):
            raise RuntimeError("Auth Firestore audit did not prove positive identity RPCs and zero forbidden product collections")
        REPORT["qualification"] = {
            "identity_commit_only_before_bootstrap": True,
            "password_failure_login_retry": "PASS: owned Auth HTTP outage; same user/default Project recovered by real login and BFF bootstrap",
            "concurrent_repeated_bootstrap": "PASS: six simultaneous HTTP requests plus repeat leave one registry and one legacy metadata document",
            "mismatch": "PASS: actual BFF returns 409 setup_conflict with zero registry write and unchanged conflicting metadata",
            "preservation": "PASS: product-created docs retain name, created_at, status, profile, sync_binding, and metadata through repeat",
            "demo": "PASS: actual Auth startup/demo login fixed non-admin identity bootstraps same default Project",
            "google": "PASS: existing local Google callback business seam with real emulator identity persistence and actual BFF bootstrap for JIT and existing identity",
            "auth_audit": "PASS: real RPC positives and zero forbidden Project/binding collections; blank/header-only controls rejected",
            "frontend": "NOT RUN in browser; source review found no frontend bootstrap endpoint caller, so pending/re-entry UX is not qualified",
            "scope": "identity/bootstrap portion of acceptance 1 and acceptance 5 only",
            "qualification_complete": True,
        }
        REPORT["frontend_source_review"] = frontend_source_review()
        if REPORT["frontend_source_review"]["recovery_call_gap"]:
            REPORT["qualification"]["qualification_complete"] = False
    except Exception as exc:
        exit_code, error = 1, f"{type(exc).__name__}: {exc}"
        REPORT["qualification"] = {"qualification_complete": False, "scope": "identity/bootstrap portion of acceptance 1 and acceptance 5 only"}
    finally:
        try:
            stop_probe_services()
        except Exception as exc:
            exit_code, error = 1, error or f"cleanup error: {type(exc).__name__}: {exc}"
        try:
            finalize_report(exit_code, error)
        except Exception as exc:
            print(f"report generation failed: {type(exc).__name__}: {exc}", file=sys.stderr)
            return 1
    print(json.dumps({"exit_code": exit_code, "report": str(REPORT_PATH), "scratch": str(RUN),
                      "terminal_log": str(TERMINAL)}))
    return exit_code


def primary_preservation_through_login() -> dict:
    # Private email/password are kept only in this process for the separately authenticated repeat.
    if not PRIMARY_PRIVATE:
        raise RuntimeError("private password recovery inputs are unavailable")
    return preservation_case(REPORT["checks"]["password_identity_commit_failure_reentry_retry"])


if __name__ == "__main__":
    try:
        # explicit TMPDIR is required before process setup; fail without creating output otherwise.
        os.chmod(RUN, 0o700)
        result = main()
    except KeyboardInterrupt:
        kill_all()
        raise
    raise SystemExit(result)
