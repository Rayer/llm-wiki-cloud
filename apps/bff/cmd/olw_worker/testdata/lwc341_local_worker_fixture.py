"""Seed and verify only a temporary Local LWC-341 worker vault."""

import hashlib
import json
import os
import runpy
import sqlite3
import sys
from pathlib import Path

import synto
from synto.config import Config
from synto.indexer import generate_index_json
from synto.models import WikiArticleRecord
from synto.state import StateDB
from synto.vault import parse_note, write_note

if synto.__version__ != "0.7.0":
    raise RuntimeError(f"expected pinned Synto 0.7.0, got {synto.__version__}")

ADAPTER = Path(__file__).resolve().parents[1] / "synto_execution.py"
LWC = runpy.run_path(str(ADAPTER), run_name="lwc341_local_worker_fixture")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def seed(vault, operation_file, action):
    vault = Path(vault)
    (vault / ".synto").mkdir(parents=True)
    (vault / ".olw").mkdir(parents=True)
    (vault / "wiki/.drafts").mkdir(parents=True)
    (vault / "cache").mkdir(parents=True)
    (vault / "wiki.toml").write_text('[vault]\nname = "fixture"\n')
    for name, content in {
        "id_map.json": '{"concept":{},"source":{},"redirects":{}}',
        "concepts.jsonl": "",
        "dormant_concepts.jsonl": "",
        "raw_status.json": "{}",
        "suggested_queries.json": '{"version":2,"queries":[],"candidates":[],"updated_at":""}',
    }.items():
        (vault / "cache" / name).write_text(content)
    (vault / "synto.toml").write_text(
        "[pipeline]\nauto_commit = false\nauto_maintain = false\n"
        "relation_extraction = false\n\n"
        "[providers.default]\nname = \"deepseek\"\nurl = \"https://fixture.example/v1\"\n\n"
        "[models.fast]\nprovider = \"default\"\nmodel = \"deepseek-chat\"\n\n"
        "[models.heavy]\nprovider = \"default\"\nmodel = \"deepseek-chat\"\n"
    )
    (vault / ".synto/INDEX.json").write_text('{"articles":[]}')
    with sqlite3.connect(vault / ".olw/state.db") as legacy_db:
        legacy_db.execute("CREATE TABLE raw_notes (path TEXT PRIMARY KEY, content_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'new', ingested_at TEXT, error TEXT)")
    config = Config(vault=vault)
    db = StateDB(config.state_db_path)
    for name in ("A", "B"):
        published_entity = "01JAZ5N7Y3K8M2Q4R6T9VWXAC8" if name == "A" else "01JAZ5N7Y3K8M2Q4R6T9VWXAC9"
        candidate_entity = "01JAZ5N7Y3K8M2Q4R6T9VWXAD0" if name == "A" else "01JAZ5N7Y3K8M2Q4R6T9VWXAD1"
        published_body = f"published {name} original"
        candidate_body = f"candidate {name} body"
        published_path = vault / f"wiki/{name}.md"
        candidate_path = vault / f"wiki/.drafts/{name}.md"
        write_note(published_path, {"title": name, "status": "published"}, published_body)
        write_note(candidate_path, {"title": name, "status": "draft", "confidence": 0.9}, candidate_body)
        db.upsert_article(WikiArticleRecord(
            path=f"wiki/{name}.md", title=name, sources=[],
            content_hash=compile_hash(published_body), status="published",
            article_id=f"published-article-{name}", entity_id=published_entity,
        ))
        db.upsert_article(WikiArticleRecord(
            path=f"wiki/.drafts/{name}.md", title=name, sources=[],
            content_hash=compile_hash(candidate_body), status="draft",
            article_id=f"candidate-article-{name}", entity_id=candidate_entity,
        ))
    generate_index_json(config, db)
    os.environ["USER_ID"] = "fixture-user"
    os.environ["LWC_PROJECT_ID"] = "fixture-project"
    operation = {
        "operation_id": f"operation-local-{action}",
        "payload_sha256": "a" * 64 if action == "keep_published" else "b" * 64,
        "action": action,
        "conflict_id": "",
        "path": "wiki/A.md",
        "storage_view": "observed-local-fixture-snapshot",
    }
    snapshot = LWC["_lwc341_snapshot"](config, db, operation)
    operation["conflict_id"] = snapshot["conflict_id"]
    operation["expected"] = {
        "conflict_id": snapshot["conflict_id"],
        "published_sha256": snapshot["published_sha256"],
        "candidate_sha256": snapshot["candidate_sha256"],
        "published_identity": snapshot["published_identity"],
        "candidate_identity": snapshot["candidate_identity"],
        "index_sha256": snapshot["index_sha256"],
        "min_confidence": 0.0,
    }
    operation["seed_index_sha256"] = digest((vault / ".synto/INDEX.json").read_bytes())
    Path(operation_file).write_text(json.dumps(operation, sort_keys=True))
    db.close()


def compile_hash(body):
    return LWC["compile_pipeline"]._content_hash(body)


def verify(vault, operation_file, stale=False):
    vault = Path(vault)
    operation = json.loads(Path(operation_file).read_text())
    config = Config(vault=vault)
    db = StateDB(config.state_db_path)
    row = db._conn.execute(
        "SELECT payload_sha256,result_json FROM lwc341_operations WHERE principal_id=? AND operation_id=?",
        ("fixture-user", operation["operation_id"]),
    ).fetchone()
    attempt = db._conn.execute(
        "SELECT result_json FROM lwc341_operation_attempts WHERE principal_id=? AND operation_id=? AND payload_sha256=?",
        ("fixture-user", operation["operation_id"], operation["payload_sha256"]),
    ).fetchone()
    if stale:
        assert row is None, row
        assert attempt is not None and json.loads(attempt[0])["status"] == "stale", attempt
        assert parse_note(vault / "wiki/A.md")[1] == "published A original"
        assert parse_note(vault / "wiki/.drafts/A.md")[1] == "changed after comparison"
    else:
        assert row is not None and row[0] == operation["payload_sha256"], row
        result = json.loads(row[1])
        assert result["status"] == "committed" and result["action"] == operation["action"], result
        assert attempt is not None and json.loads(attempt[0]) == result, attempt
        assert not (vault / "wiki/.drafts/A.md").exists()
        published = vault / "wiki/A.md"
        meta, body = parse_note(published)
        article = db.get_article("wiki/A.md")
        expected_article = "published-article-A" if operation["action"] == "keep_published" else "candidate-article-A"
        expected_entity = "01JAZ5N7Y3K8M2Q4R6T9VWXAC8" if operation["action"] == "keep_published" else "01JAZ5N7Y3K8M2Q4R6T9VWXAD0"
        assert article.status == "published" and article.article_id == expected_article, article
        assert article.entity_id == expected_entity, article
        assert article.content_hash == compile_hash(body), (article.content_hash, compile_hash(body))
        assert result["output_sha256"] == digest(published.read_bytes()), result
        assert result["content_hash"] == compile_hash(body), result
        assert db.get_article("wiki/.drafts/A.md") is None
        if operation["action"] == "keep_published":
            assert body == "published A original", body
        else:
            assert body == "candidate A body" and meta["status"] == "published", (meta, body)
        index = json.loads((vault / ".synto/INDEX.json").read_text())
        assert any(item["path"] == "articles/A.md" and item.get("id") == expected_article for item in index["articles"]), index

    assert parse_note(vault / "wiki/B.md")[1] == "published B original"
    assert parse_note(vault / "wiki/.drafts/B.md")[1] == "candidate B body"
    assert db.get_article("wiki/B.md").entity_id == "01JAZ5N7Y3K8M2Q4R6T9VWXAC9"
    assert db.get_article("wiki/.drafts/B.md").entity_id == "01JAZ5N7Y3K8M2Q4R6T9VWXAD1"
    assert not (vault / ".lwc-worker-publish-journal.json").exists()
    assert not (vault / ".olw/lwc-worker-lease.json").exists()
    assert not list(vault.glob(".lwc-worker-stage-*"))
    assert not list(vault.glob(".lwc-worker-backup-*"))
    db.close()


def verify_failed(vault, operation_file):
    vault = Path(vault)
    operation = json.loads(Path(operation_file).read_text())
    config = Config(vault=vault)
    db = StateDB(config.state_db_path)
    assert parse_note(vault / "wiki/A.md")[1] == "published A original"
    assert parse_note(vault / "wiki/.drafts/A.md")[1] == "candidate A body"
    assert db.get_article("wiki/A.md").entity_id == "01JAZ5N7Y3K8M2Q4R6T9VWXAC8"
    assert db.get_article("wiki/.drafts/A.md").entity_id == "01JAZ5N7Y3K8M2Q4R6T9VWXAD0"
    assert (vault / ".synto/INDEX.json").read_bytes() and digest((vault / ".synto/INDEX.json").read_bytes()) == operation["seed_index_sha256"]
    tables = {row[0] for row in db._conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if "lwc341_operations" in tables:
        assert db._conn.execute(
            "SELECT 1 FROM lwc341_operations WHERE principal_id=? AND operation_id=?",
            ("fixture-user", operation["operation_id"]),
        ).fetchone() is None
    if "lwc341_operation_attempts" in tables:
        assert db._conn.execute(
            "SELECT 1 FROM lwc341_operation_attempts WHERE principal_id=? AND operation_id=?",
            ("fixture-user", operation["operation_id"]),
        ).fetchone() is None
    assert not (vault / ".lwc-worker-publish-journal.json").exists()
    assert not (vault / ".olw/lwc-worker-lease.json").exists()
    assert not list(vault.glob(".lwc-worker-stage-*"))
    assert not list(vault.glob(".lwc-worker-backup-*"))
    db.close()


if __name__ == "__main__":
    mode, vault, operation_file, *args = sys.argv[1:]
    if mode == "seed":
        seed(vault, operation_file, args[0])
    elif mode == "verify":
        verify(vault, operation_file)
    elif mode == "verify-stale":
        verify(vault, operation_file, stale=True)
    elif mode == "verify-failed":
        verify_failed(vault, operation_file)
    else:
        raise ValueError(f"unknown fixture mode {mode}")
