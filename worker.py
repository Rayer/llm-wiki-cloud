#!/usr/bin/env python3
"""
OLW Cloud Run Worker — Firestore lock → GCS sync → OLW pipeline → GCS sync.

Uses an OpenAI-compatible LLM provider endpoint.
API key is read from GCP Secret Manager at runtime.
Lock is managed via Firestore (native mode, free tier).
"""

import base64
import json
import os
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib import request, error as urllib_error


# ── Config ──────────────────────────────────────────────────────────
PROJECT = os.environ["GCP_PROJECT"]  # llm-wiki-cloud
BUCKET = os.environ["BUCKET"]
USER_ID = os.environ["USER_ID"]
PROJECT_ID = os.environ["PROJECT_ID"]
LOCK_TTL_MIN = 15

FIRESTORE_BASE = (
    f"https://firestore.googleapis.com/v1/projects/{PROJECT}"
    "/databases/(default)/documents"
)
LOCK_ID = f"{USER_ID}__{PROJECT_ID}"
LOCK_URL = f"{FIRESTORE_BASE}/locks/{LOCK_ID}"

RAW_PATH = f"users/{USER_ID}/projects/{PROJECT_ID}/raw"
WIKI_PATH = f"users/{USER_ID}/projects/{PROJECT_ID}/wiki"

DATA_DIR = Path("/data")


# ── Helpers ──────────────────────────────────────────────────────────

def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    """Run a command and return the result. Fails on non-zero exit."""
    return subprocess.run(cmd, check=True, text=True, **kwargs)


def get_access_token() -> str:
    """Get GCP access token from the default service account."""
    result = run(
        ["gcloud", "auth", "print-access-token"],
        capture_output=True,
    )
    return result.stdout.strip()


def firestore_request(method: str, url: str, token: str, body: dict | None = None) -> dict:
    """Make an authenticated Firestore REST API request."""
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    data = json.dumps(body).encode() if body else None
    req = request.Request(url, data=data, headers=headers, method=method)
    try:
        with request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())
    except urllib_error.HTTPError as e:
        if e.code == 404:
            return {}
        raise


def read_secret(name: str) -> str:
    """Read a secret from GCP Secret Manager."""
    result = run(
        [
            "gcloud", "secrets", "versions", "access", "latest",
            "--secret", name,
            "--project", PROJECT,
        ],
        capture_output=True,
    )
    return result.stdout.strip()


# ── Lock ─────────────────────────────────────────────────────────────

def acquire_lock() -> str:
    """Acquire Firestore lock. Returns the token for later release."""
    token = get_access_token()
    now = datetime.now(timezone.utc)
    now_iso = now.strftime("%Y-%m-%dT%H:%M:%SZ")
    expire_at = (now + timedelta(minutes=LOCK_TTL_MIN)).strftime("%Y-%m-%dT%H:%M:%SZ")

    # Check existing lock
    existing = firestore_request("GET", LOCK_URL, token)
    if existing:
        fields = existing.get("fields", {})
        status = fields.get("status", {}).get("stringValue", "")
        expires = fields.get("expires_at", {}).get("timestampValue", "")
        if status == "active" and expires >= now_iso:
            print(f"ERROR: Project locked until {expires}", file=sys.stderr)
            sys.exit(1)
        if status == "active":
            print(f"Stale lock (expired {expires}), taking over...")

    # Create/update lock
    body = {
        "fields": {
            "status": {"stringValue": "active"},
            "worker": {"stringValue": f"cloud-run-{os.getpid()}"},
            "locked_at": {"timestampValue": now_iso},
            "expires_at": {"timestampValue": expire_at},
        }
    }
    patch_url = f"{LOCK_URL}?updateMask.fieldPaths=status&updateMask.fieldPaths=worker&updateMask.fieldPaths=locked_at&updateMask.fieldPaths=expires_at"
    firestore_request("PATCH", patch_url, token, body)
    print(f"Lock acquired (expires {expire_at}).")
    return token


def release_lock(token: str):
    """Release Firestore lock."""
    body = {"fields": {"status": {"stringValue": "released"}}}
    url = f"{LOCK_URL}?updateMask.fieldPaths=status"
    try:
        firestore_request("PATCH", url, token, body)
        print("Lock released.")
    except Exception as e:
        print(f"Warning: lock release failed: {e}", file=sys.stderr)


# ── OLW Pipeline ─────────────────────────────────────────────────────

LLM_PROVIDER = os.environ.get("LLM_PROVIDER", "deepseek").lower()

PROVIDERS = {
    "deepseek": {
        "secret": "deepseek-apikey",
        "url": "https://api.deepseek.com/v1",
        "fast_model": "deepseek-chat",
        "heavy_model": "deepseek-reasoner",
    },
    "gemini": {
        "secret": "gemini-api-key",
        "url": "https://generativelanguage.googleapis.com/v1beta/openai",
        "fast_model": "gemini-2.5-flash",
        "heavy_model": "gemini-2.5-flash",
    },
}

if LLM_PROVIDER not in PROVIDERS:
    print(
        f"ERROR: Unsupported LLM_PROVIDER={LLM_PROVIDER!r}. "
        f"Supported providers: {', '.join(sorted(PROVIDERS))}",
        file=sys.stderr,
    )
    sys.exit(1)

PROVIDER_CONFIG = PROVIDERS[LLM_PROVIDER]
PROVIDER_URL = PROVIDER_CONFIG["url"]
FAST_MODEL = PROVIDER_CONFIG["fast_model"]
HEAVY_MODEL = PROVIDER_CONFIG["heavy_model"]

WIKI_TOML = f"""[models]
fast = "{FAST_MODEL}"
heavy = "{HEAVY_MODEL}"

[provider]
name = "custom"
url = "{PROVIDER_URL}"
timeout = 120
fast_ctx = 8192
heavy_ctx = 32768

[pipeline]
auto_approve = false
auto_commit = false
auto_maintain = true
watch_debounce = 3.0
max_concepts_per_source = 8
ingest_parallel = false
article_max_tokens = 16384
concept_draft_soft_cap = 4096
inline_source_citations = false
source_citation_style = "legend-only"
draft_media = "reference"
graph_quality_checks = true
"""


def patch_olw_healthcheck():
    """Monkey-patch OLW's healthcheck — Gemini /models returns 404."""
    if LLM_PROVIDER != "gemini":
        return
    import obsidian_llm_wiki.openai_compat_client as occ
    occ.OpenAICompatClient.healthcheck = lambda self: True
    occ.OpenAICompatClient.require_healthy = lambda self: None
    print(f"Health check patched for {LLM_PROVIDER}.")


def setup_olw_config(api_key: str, provider_url: str):
    """Write OLW global + vault config files."""
    # Global config
    global_config = Path.home() / ".config" / "olw"
    global_config.mkdir(parents=True, exist_ok=True)
    (global_config / "config.toml").write_text(
        f'vault = "/data"\n'
        f'fast_model = "{FAST_MODEL}"\n'
        f'heavy_model = "{HEAVY_MODEL}"\n'
        f'provider_name = "custom"\n'
        f'provider_url = "{provider_url}"\n'
        f'api_key = "{api_key}"\n'
    )

    # Vault directories
    for d in ["wiki/.drafts", "wiki/sources", ".olw/chroma"]:
        (DATA_DIR / d).mkdir(parents=True, exist_ok=True)

    # Vault config (wiki.toml)
    wiki_toml = DATA_DIR / "wiki.toml"
    if not wiki_toml.exists():
        wiki_toml.write_text(WIKI_TOML)
        print("wiki.toml created (fresh run).")
    else:
        current_wiki_toml = wiki_toml.read_text()
        if (
            f'fast = "{FAST_MODEL}"' not in current_wiki_toml
            or f'heavy = "{HEAVY_MODEL}"' not in current_wiki_toml
            or f'url = "{provider_url}"' not in current_wiki_toml
        ):
            wiki_toml.write_text(WIKI_TOML)
            print(f"wiki.toml updated for {LLM_PROVIDER}.")
        else:
            print("wiki.toml exists (restored from GCS).")


def run_olw():
    """Import and run OLW pipeline directly (in-process)."""
    from obsidian_llm_wiki.cli import _load_config, _load_deps
    from obsidian_llm_wiki.pipeline.lock import pipeline_lock
    from obsidian_llm_wiki.pipeline.orchestrator import PipelineOrchestrator

    config = _load_config(str(DATA_DIR))
    client, db = _load_deps(config)

    with pipeline_lock(config.vault) as acquired:
        if not acquired:
            print("Pipeline already running — lock held.", file=sys.stderr)
            sys.exit(1)
        orchestrator = PipelineOrchestrator(config, client, db)
        report = orchestrator.run(auto_approve=True)

    # Print summary
    print(f"Ingested: {report.ingested}")
    print(f"Compiled: {report.compiled}")
    print(f"Published: {report.published}")
    print(f"Lint issues: {report.lint_issues}")

    wiki_sources = list((DATA_DIR / "wiki" / "sources").glob("*.md"))
    print(f"Wiki files generated: {len(wiki_sources)}")


# ── Execution tracking ────────────────────────────────────────────────

def record_execution_start(token: str) -> str:
    """Write an execution start record to Firestore. Returns doc ID."""
    now = datetime.now(timezone.utc)
    body = {
        "fields": {
            "user_id": {"stringValue": USER_ID},
            "project_id": {"stringValue": PROJECT_ID},
            "started_at": {"timestampValue": now.strftime("%Y-%m-%dT%H:%M:%SZ")},
            "status": {"stringValue": "running"},
        }
    }
    # Firestore auto-generates document ID when POSTing to collection
    url = f"{FIRESTORE_BASE}/executions"
    resp = firestore_request("POST", url, token, body)
    # Extract document ID from response path
    doc_id = resp.get("name", "").split("/")[-1] if resp else ""
    print(f"Execution started: {doc_id}")
    return doc_id


def record_execution_end(token: str, doc_id: str, success: bool):
    """Update execution record with completion data."""
    if not doc_id:
        return
    now = datetime.now(timezone.utc)
    body = {
        "fields": {
            "finished_at": {"timestampValue": now.strftime("%Y-%m-%dT%H:%M:%SZ")},
            "status": {"stringValue": "completed" if success else "failed"},
        }
    }
    url = f"{FIRESTORE_BASE}/executions/{doc_id}?updateMask.fieldPaths=finished_at&updateMask.fieldPaths=status"
    try:
        firestore_request("PATCH", url, token, body)
        print(f"Execution ended: {doc_id} ({'completed' if success else 'failed'})")
    except Exception as e:
        print(f"Warning: execution end record failed: {e}", file=sys.stderr)


def push_pipeline_metrics(started_at: datetime, success: bool):
    """Push pipeline execution metrics to Grafana Cloud Prometheus."""
    prom_user = os.environ.get("GRAFANA_PROM_USER", "3301522")
    prom_key = os.environ.get("GRAFANA_PROM_API_KEY", "")
    if not prom_key:
        print("GRAFANA_PROM_API_KEY not set — skipping Prometheus push")
        return

    duration = (datetime.now(timezone.utc) - started_at).total_seconds()
    status = "completed" if success else "failed"

    # Prometheus text exposition format
    body = (
        "# HELP lwc_pipeline_duration_seconds Duration of pipeline execution\n"
        "# TYPE lwc_pipeline_duration_seconds gauge\n"
        f'lwc_pipeline_duration_seconds{{user_id="{USER_ID}",project_id="{PROJECT_ID}",status="{status}"}} {duration}\n'
        "# HELP lwc_pipeline_runs_total Total pipeline runs\n"
        "# TYPE lwc_pipeline_runs_total counter\n"
        f'lwc_pipeline_runs_total{{user_id="{USER_ID}",project_id="{PROJECT_ID}",status="{status}"}} 1\n'
        "# HELP lwc_pipeline_last_success_timestamp Last successful run timestamp\n"
        "# TYPE lwc_pipeline_last_success_timestamp gauge\n"
        f'lwc_pipeline_last_success_timestamp{{user_id="{USER_ID}",project_id="{PROJECT_ID}"}} {started_at.timestamp()}\n'
    )

    prom_url = "https://prometheus-prod-49-prod-ap-northeast-0.grafana.net/api/prom/push"
    try:
        credentials = prom_user + ":" + prom_key
        auth = base64.b64encode(credentials.encode()).decode()
        req = request.Request(
            prom_url, data=body.encode(), method="POST",
            headers={
                "Authorization": f"Basic {auth}",
                "Content-Type": "text/plain",
            }
        )
        with request.urlopen(req, timeout=10) as resp:
            print(f"Prometheus push: {resp.status} ({status}, duration={duration:.1f}s)")
    except Exception as e:
        print(f"Warning: Prometheus push failed: {e}", file=sys.stderr)


# ── Main ─────────────────────────────────────────────────────────────

def main():
    print("=== OLW Cloud Run Worker ===")
    print(f"User:    {USER_ID}")
    print(f"Project: {PROJECT_ID}")
    print(f"Bucket:  {BUCKET}")
    print(f"LLM provider: {LLM_PROVIDER}")

    # 1. Acquire lock
    lock_token = acquire_lock()
    execution_id = record_execution_start(lock_token)
    pipeline_started_at = datetime.now(timezone.utc)
    success = False
    try:
        # 2. Sync from GCS
        print("Syncing data from GCS...")
        DATA_DIR.mkdir(parents=True, exist_ok=True)
        for d in ["raw", "wiki", ".olw"]:
            (DATA_DIR / d).mkdir(parents=True, exist_ok=True)

        subprocess.run(
            ["gsutil", "-m", "rsync", "-r",
             f"gs://{BUCKET}/{RAW_PATH}/", str(DATA_DIR / "raw/")],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["gsutil", "-m", "rsync", "-r",
             f"gs://{BUCKET}/{WIKI_PATH}/", str(DATA_DIR / "wiki/")],
            check=True, capture_output=True,
        )
        # Restore OLW state
        result = subprocess.run(
            ["gsutil", "-m", "rsync", "-r",
             f"gs://{BUCKET}/users/{USER_ID}/projects/{PROJECT_ID}/.olw/",
             str(DATA_DIR / ".olw/")],
            capture_output=True,
        )
        if result.returncode == 0:
            print("State restored.")
        else:
            print("No prior state (fresh run).")

        raw_files = list((DATA_DIR / "raw").glob("*.md"))
        print(f"Raw files: {len(raw_files)}")

        # 3. Read provider key from Secret Manager
        secret_name = PROVIDER_CONFIG["secret"]
        print(f"Reading {LLM_PROVIDER} API key from Secret Manager ({secret_name})...")
        api_key = read_secret(secret_name)
        print(f"{LLM_PROVIDER} API key: {'acquired' if api_key else 'MISSING'}")

        # 4. Setup OLW
        setup_olw_config(api_key, PROVIDER_URL)
        patch_olw_healthcheck()

        print(f"Provider: custom/{LLM_PROVIDER} ({PROVIDER_URL})")
        print(f"{LLM_PROVIDER} key: {'set' if api_key else 'MISSING'}")

        # 5. Run OLW
        print("Running OLW pipeline...")
        run_olw()

        # 6. Sync results back
        print("Syncing results back to GCS...")
        subprocess.run(
            ["gsutil", "-m", "rsync", "-r",
             str(DATA_DIR / "wiki/"), f"gs://{BUCKET}/{WIKI_PATH}/"],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["gsutil", "-m", "rsync", "-r",
             str(DATA_DIR / ".olw/"),
             f"gs://{BUCKET}/users/{USER_ID}/projects/{PROJECT_ID}/.olw/"],
            check=True, capture_output=True,
        )
        # Fix Content-Type charset for GCS web console
        subprocess.run(
            ["gsutil", "-m", "setmeta", "-r",
             "-h", "Content-Type:text/markdown; charset=utf-8",
             f"gs://{BUCKET}/{WIKI_PATH}/**/*.md"],
            capture_output=True,
        )

        print("=== Done ===")
        success = True

    finally:
        release_lock(lock_token)
        record_execution_end(lock_token, execution_id, success)
        push_pipeline_metrics(pipeline_started_at, success)


if __name__ == "__main__":
    main()
