"""Exact-wheel CLI wire gate: only loopback HTTP and temporary synthetic vaults.

Run with the Python interpreter containing the Dockerfile's hash-pinned wheel.
LWC_FLASH_RED=1 runs the unwrapped CLI to prove the migration assertions fail.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import sqlite3
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import synto

assert synto.__version__ == "0.7.0"
wrapper = Path(__file__).resolve().parents[1] / "synto_execution.py"
requests = []


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.reply({"data": []})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        requests.append(body)
        # Covers ingest analysis/term extraction and compile drafting/refinement.
        content = json.dumps({
            "summary": "Alpha explains a synthetic mechanism.",
            "concepts": [{"name": "Alpha", "aliases": []}],
            "suggested_topics": ["Alpha"], "quality": "high",
            "terms": [], "source_segment_id": "test", "model": "test",
            "tags": [], "title": "Alpha", "answer": "Alpha explains a synthetic mechanism.",
            "content": "# Alpha\n\nAlpha explains a synthetic mechanism.",
        }) if body.get("response_format") else "# Alpha\n\nAlpha explains a synthetic mechanism."
        self.reply({"choices": [{"message": {"content": content}}], "usage": {"prompt_tokens": 7, "completion_tokens": 3}})

    def reply(self, body):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


server = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
threading.Thread(target=server.serve_forever, daemon=True).start()
url = f"http://127.0.0.1:{server.server_port}/v1"
policy = """
[pipeline]
auto_approve = true
auto_commit = false
auto_maintain = false
relation_extraction = false
article_max_tokens = 32768
max_concepts_per_source = 8
ingest_parallel = false
"""


def run(vault, *args, fail=False):
    script = "from synto.cli import cli; cli()" if os.environ.get("LWC_FLASH_RED") else wrapper.read_text()
    # Test-only network fence: a malformed fixture must never reach a provider.
    script = """import socket
_original_connect = socket.socket.connect
def _loopback_only(self, address):
    if address[0] not in ('127.0.0.1', '::1'):
        raise RuntimeError('offline gate forbids non-loopback connection')
    return _original_connect(self, address)
socket.socket.connect = _loopback_only
""" + script
    env = {"PATH": os.environ["PATH"], "XDG_CONFIG_HOME": str(vault / "isolated"), "DEEPSEEK_API_KEY": "fake"}
    result = subprocess.run([sys.executable, "-c", script, *args, "--vault", str(vault)], cwd=vault, env=env, text=True, capture_output=True, timeout=60)
    if not fail and result.returncode:
        raise AssertionError(result.stdout + result.stderr)
    if fail:
        assert result.returncode, "invalid config accepted"
    return result


def exercise(config, expected, filename="synto.toml", migrate=False, model="deepseek-flash", preserved=None):
    with tempfile.TemporaryDirectory(prefix="lwc331-wire-") as temp:
        vault = Path(temp).resolve()
        (vault / "raw").mkdir()
        (vault / "raw/source.md").write_text("# Alpha\n\nAlpha explains how a synthetic mechanism works. Alpha is the central concept.")
        path = vault / filename
        path.write_text(config + policy)
        before = path.read_bytes()
        if migrate:
            # Real pinned migration of a synthetic legacy state, no user data.
            (vault / ".olw").mkdir()
            sqlite3.connect(vault / ".olw/state.db").close()
            run(vault, "migrate-olw")
        configs = {p: p.read_bytes() for p in vault.glob("*.toml")}
        requests.clear()
        result = run(vault, "run", "--auto-approve")
        assert requests, result.stdout + result.stderr
        assert (vault / "wiki/Alpha.md").is_file(), "CLI failed to compile/publish synthetic article"
        observed = tuple(body.get("thinking", {}).get("type") for body in requests)
        assert expected == observed, (expected, observed, result.stdout, result.stderr)
        for body in requests:
            assert body["model"] == model, body
            if model == "deepseek-flash":
                assert body.get("thinking") in ({"type": "disabled"}, {"type": "enabled"}), body
            else:
                assert "thinking" not in body, body
            for key, value in (preserved or {}).items():
                assert body.get(key) == value, body
        with sqlite3.connect(vault / ".synto/state.db") as db:
            runs = db.execute("SELECT fast_model, heavy_model, pipeline_json FROM compile_runs").fetchall()
        assert runs and all(fast == heavy == model for fast, heavy, _ in runs), runs
        assert all(json.loads(pipeline)["fast_model"] == model and json.loads(pipeline)["heavy_model"] == model for _, _, pipeline in runs), runs
        assert path.read_bytes() == before
        assert all(p.read_bytes() == data for p, data in configs.items())
        print(f"PASS {filename} migrate={migrate}: {len(requests)} real HTTP requests, thinking={list(observed)}")


legacy = f'[provider]\nname = "deepseek"\nurl = "{url}"\n[models]\nfast = "deepseek-chat"\nheavy = "deepseek-reasoner"\n'
exercise(legacy, ("disabled", "enabled"))
exercise(legacy, ("disabled", "enabled"), "wiki.toml", migrate=True)
# New template uses explicit thinking, despite the canonical model default.
modern = f'''[providers.default]
name = "deepseek"
url = "{url}"
[models.fast]
provider = "default"
model = "deepseek-flash"
ctx = 16384
[models.fast.options]
thinking = {{ type = "disabled" }}
[models.heavy]
provider = "default"
model = "deepseek-flash"
ctx = 32768
[models.heavy.options]
thinking = {{ type = "enabled" }}
'''
exercise(modern, ("disabled", "enabled"))
# Explicit provider options and role options win over alias/default intent.
precedence = modern.replace('thinking = { type = "enabled" }', 'thinking = { type = "disabled" }').replace('[models.fast]', '[providers.default.options]\nthinking = { type = "enabled" }\nmax_tokens = 777\ntop_p = 0.6\n[models.fast]')
precedence = precedence.replace('model = "deepseek-flash"', 'model = "deepseek-reasoner"')
exercise(precedence, ("disabled", "disabled"), preserved={"max_tokens": 777, "top_p": 0.6})
# Role names do not dictate thinking: reasoner fast and chat heavy retain intent.
exercise(legacy.replace('fast = "deepseek-chat"', 'fast = "deepseek-reasoner"').replace('heavy = "deepseek-reasoner"', 'heavy = "deepseek-chat"'), ("enabled", "disabled"))
# Aliased modern V4 models preserve their explicitly disabled/enabled policies.
exercise(modern.replace('model = "deepseek-flash"', 'model = "deepseek-v4-pro"'), ("disabled", "enabled"))
# Other OpenAI-compatible providers are byte-for-byte unaffected by this policy.
foreign = legacy.replace('name = "deepseek"', 'name = "openai"').replace('deepseek-chat', 'foreign-model').replace('deepseek-reasoner', 'foreign-model')
exercise(foreign, (None, None), model="foreign-model")
for invalid in (
    modern.replace('model = "deepseek-flash"', 'model = "unknown-model"'),
    modern.replace('type = "disabled"', 'type = "automatic"'),
    modern.replace('[models.fast.options]', '[models.fast.options]\nreasoning_effort = "unknown"'),
):
    with tempfile.TemporaryDirectory(prefix="lwc331-invalid-") as temp:
        vault = Path(temp).resolve()
        (vault / "synto.toml").write_text(invalid + policy)
        requests.clear()
        run(vault, "run", "--auto-approve", fail=True)
        assert not requests, "invalid DeepSeek config reached inference"
print("PASS fail-closed unknown model/thinking/effort before inference")
server.shutdown()

# The real pinned cache and router must separate migrated thinking intentions,
# both within one router and across invocations using the same retained DB.
import runpy
import httpx
from synto.cache import LLMCache
from synto.client_factory import build_router
from synto.config import Config
from synto.state import StateDB

runpy.run_path(str(wrapper), run_name="flash_cache_gate")
os.environ["LWC331_TEST_API_KEY"] = "fake"
with tempfile.TemporaryDirectory(prefix="lwc331-cache-") as temp:
    db = StateDB(Path(temp) / "state.db")
    cache = LLMCache(db)
    calls = []

    def capture(request):
        body = json.loads(request.content)
        calls.append(body)
        return httpx.Response(200, json={"choices": [{"message": {"content": str(len(calls))}}]})

    def invoke(ep):
        return ep.client.generate("identical prompt", model=ep.model, options=ep.options)

    provider = {"default": {"name": "deepseek", "url": url, "api_key_env": "LWC331_TEST_API_KEY"}}
    cfg = Config(vault=temp, providers=provider, models={"fast": "deepseek-chat", "heavy": "deepseek-reasoner"})
    router = build_router(cfg, cache=cache)
    fast, heavy = router.endpoint("fast"), router.endpoint("heavy")
    assert fast.client is not heavy.client, "different thinking policies share one client/cache namespace"
    for ep in (fast, heavy):
        ep.client._client.close()
        ep.client._client = httpx.Client(transport=httpx.MockTransport(capture))
    assert invoke(fast) == "1" and invoke(heavy) == "2"
    assert invoke(fast) == "1" and invoke(heavy) == "2" and len(calls) == 2
    router.close()
    for effort in ("low", "high"):
        cfg = Config(vault=temp, providers=provider, models={"heavy": {"model": "deepseek-reasoner", "options": {"reasoning_effort": effort}}})
        router = build_router(cfg, cache=cache)
        ep = router.endpoint("heavy")
        ep.client._client.close()
        ep.client._client = httpx.Client(transport=httpx.MockTransport(capture))
        invoke(ep)
        router.close()
    assert len(calls) == 4, "retained cache collapsed distinct explicit effort policies"
    assert all(body["model"] == "deepseek-flash" for body in calls)
    assert [body["thinking"]["type"] for body in calls] == ["disabled", "enabled", "enabled", "enabled"]
    db.close()
print("PASS pinned cache isolates thinking/effort while retaining same-policy hits")

# Resume a real partial ingest checkpoint saved with chat, then switch to
# reasoner. Both chunks must reach analysis rather than resume the old intent.
from synto.pipeline import ingest
with tempfile.TemporaryDirectory(prefix="lwc331-checkpoint-") as temp:
    vault = Path(temp).resolve()
    db = StateDB(vault / "state.db")
    old_cfg = Config(vault=vault, providers=provider, models={"fast": "deepseek-chat"})
    new_cfg = Config(vault=vault, providers=provider, models={"fast": "deepseek-reasoner"})
    retained = {"summary": "old non-thinking analysis", "concepts": [], "suggested_topics": [], "quality": "high"}
    units = [("Alpha first part", ["segment-a"]), ("Alpha second part", ["segment-b"])]
    chunk_size = old_cfg.resolve_role("fast").ctx // 2
    old_hash = ingest._checkpoint_hash("same-content", old_cfg, [])
    db.upsert_ingest_chunk("raw/source.md", old_hash, 0, 2, chunk_size, json.dumps(retained))
    router = build_router(new_cfg, cache=LLMCache(db))
    ep = router.endpoint("fast")
    calls = []
    def analyze(request):
        calls.append(json.loads(request.content))
        fresh = dict(retained, summary="fresh thinking analysis")
        return httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(fresh)}}]})
    ep.client._client.close()
    ep.client._client = httpx.Client(transport=httpx.MockTransport(analyze))
    result = ingest._analyze_body_with_checkpoints("Alpha first part Alpha second part", [], vault / "raw/source.md", "same-content", router, new_cfg, db, units=units)
    assert len(calls) == 2, "resumed a completed chunk from a different thinking policy"
    assert all(body["thinking"] == {"type": "enabled"} for body in calls)
    assert "old non-thinking" not in result.summary
    router.close()
    db.close()
print("PASS retained ingest checkpoint cannot resume another thinking policy")
