"""Disposable Synto 0.7.0 fixture for the LWC-341 publication guard."""

import hashlib
import json
import os
import runpy
import sys
import tempfile
from pathlib import Path

ROOT = Path(tempfile.mkdtemp(prefix="lwc341-", dir=os.environ["TMPDIR"])).resolve()


def audit(event, args):
    if event.startswith("socket.") or event == "subprocess.Popen":
        raise RuntimeError(f"fixture denied {event}")
    paths = []
    if event == "open":
        filename, mode, flags = args
        if isinstance(filename, (str, bytes)) and (
            any(char in (mode or "") for char in "wax+")
            or flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC)
        ):
            paths = [filename]
    elif event in ("os.mkdir", "os.remove", "os.rmdir", "os.chmod", "os.truncate"):
        paths = [args[0]]
    elif event in ("os.rename", "os.link", "os.symlink"):
        paths = list(args[:2])
    elif event == "sqlite3.connect" and args[0] != ":memory:" and "mode=ro" not in str(args[0]):
        paths = [args[0]]
    for filename in paths:
        if not Path(os.fsdecode(filename)).resolve().is_relative_to(ROOT):
            raise RuntimeError(f"fixture denied write outside scratch: {filename}")


sys.addaudithook(audit)
sys.dont_write_bytecode = True

import synto
from synto.config import Config
from synto.models import WikiArticleRecord
from synto.state import StateDB
from synto.vault import parse_note, write_note
from synto.pipeline import compile as compile_pipeline
from synto.indexer import generate_index_json

if synto.__version__ != "0.7.0":
    raise RuntimeError(f"expected pinned Synto 0.7.0, got {synto.__version__}")

adapter = Path(__file__).resolve().parents[1] / "synto_execution.py"
lwc = runpy.run_path(str(adapter), run_name="lwc341_adapter")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def seed(case, published=True, published_row=True, confidence=0.9):
    vault = ROOT / case
    (vault / ".synto").mkdir(parents=True)
    (vault / "wiki/.drafts").mkdir(parents=True)
    (vault / "synto.toml").write_text(
        "[pipeline]\nauto_commit = false\nauto_maintain = false\n"
    )
    config = Config.from_vault(vault)
    db = StateDB(config.state_db_path)
    target = vault / "wiki/Alpha.md"
    draft_name = "Beta.md" if case == "different_path" else "Alpha.md"
    draft = vault / "wiki/.drafts" / draft_name
    old_body = "PUBLISHED ORIGINAL — disposable fixture"
    draft_body = "CANDIDATE BODY — disposable fixture"
    if published:
        write_note(target, {"title": "Alpha", "status": "published"}, old_body)
        if published_row:
            db.upsert_article(WikiArticleRecord(
                path="wiki/Alpha.md", title="Alpha", sources=[],
                content_hash=sha(old_body.encode()), status="published",
            ))
    write_note(draft, {
        "title": "Alpha", "status": "draft", "confidence": confidence,
    }, draft_body)
    db.upsert_article(WikiArticleRecord(
        path=str(draft.relative_to(vault)), title="Alpha", sources=[],
        content_hash=sha(draft_body.encode()), status="draft",
    ))
    return vault, config, db, target, draft, old_body, draft_body


def test_collision(case, published_row):
    vault, config, db, target, draft, old_body, _ = seed(
        case, published_row=published_row
    )
    before = target.read_bytes()
    target_row_before = db.get_article("wiki/Alpha.md")
    draft_row_before = db.get_article(str(draft.relative_to(vault)))
    affected = compile_pipeline.publish_drafts(config, db, paths=[draft])
    assert not affected, (case, affected)
    assert target.read_bytes() == before, case
    assert draft.exists(), case
    assert parse_note(target)[1] == old_body, case
    target_row_after = db.get_article("wiki/Alpha.md")
    assert (target_row_after.status if target_row_after else None) == (
        target_row_before.status if target_row_before else None
    ), case
    assert db.get_article(str(draft.relative_to(vault))).status == draft_row_before.status, case
    db.close()


test_collision("tracked_collision", published_row=True)
test_collision("orphan_published_file", published_row=False)

vault, config, db, target, draft, _, draft_body = seed(
    "new_target", published=False
)
affected = compile_pipeline.publish_drafts(config, db, paths=[draft])
assert affected == [target]
assert not draft.exists()
assert target.exists() and parse_note(target)[1] == draft_body
assert db.get_article("wiki/Alpha.md").status == "published"
db.close()

vault, config, db, target, draft, _, _ = seed(
    "low_confidence", published=False, confidence=0.1
)
affected = compile_pipeline.publish_drafts(
    config, db, paths=[draft], min_confidence=0.9
)
assert not affected and draft.exists() and not target.exists()
db.close()

vault, config, db, target, draft, old_body, draft_body = seed(
    "different_path", published_row=True
)
affected = compile_pipeline.publish_drafts(config, db, paths=[draft])
assert len(affected) == 1 and affected[0].name == "Beta.md"
assert parse_note(target)[1] == old_body
assert parse_note(vault / "wiki/Beta.md")[1] == draft_body
db.close()

from unittest.mock import patch
from synto.pipeline.orchestrator import PipelineOrchestrator

vault, config, db, target, draft, old_body, _ = seed("auto_approve_entry")
before = target.read_bytes()
with patch(
    "synto.pipeline.orchestrator._run_compile",
    return_value=([draft], [], {}),
):
    report = PipelineOrchestrator(config, None, db).run(auto_approve=True, max_rounds=1)
assert report.published == 0 and report.held_back == 1
assert target.read_bytes() == before and parse_note(target)[1] == old_body
assert draft.exists()
db.close()


def seed_pair(vault_name, missing_published_row=False, confidence=0.9):
    vault = ROOT / vault_name
    (vault / ".synto").mkdir(parents=True)
    (vault / "wiki/.drafts").mkdir(parents=True)
    (vault / "synto.toml").write_text(
        "[pipeline]\nauto_commit = false\nauto_maintain = false\n"
    )
    (vault / ".synto/INDEX.json").write_text('{"fixture":true}')
    config = Config.from_vault(vault)
    db = StateDB(config.state_db_path)
    for name in ("A", "B"):
        target = vault / f"wiki/{name}.md"
        draft = vault / f"wiki/.drafts/{name}.md"
        old_body = f"published {name} original"
        candidate_body = f"candidate {name} body"
        write_note(target, {"title": name, "status": "published"}, old_body)
        write_note(draft, {"title": name, "status": "draft", "confidence": confidence}, candidate_body)
        if not (name == "A" and missing_published_row):
            db.upsert_article(WikiArticleRecord(
                path=f"wiki/{name}.md", title=name, sources=[],
                content_hash=sha(old_body.encode()), status="published",
                article_id=f"article-{name}", entity_id=f"entity-{name}",
            ))
        db.upsert_article(WikiArticleRecord(
            path=f"wiki/.drafts/{name}.md", title=name, sources=[],
            content_hash=sha(candidate_body.encode()), status="draft",
            article_id=f"candidate-article-{name}", entity_id=f"candidate-entity-{name}",
        ))
    return vault, config, db


os.environ["USER_ID"] = "fixture-user"
os.environ["LWC_PROJECT_ID"] = "fixture-project"
os.environ["LWC_CONFLICT_STORAGE_VIEW"] = "legacy"

# A cold-start scan persists pre-existing eligible A/B without publishing;
# the second run after reopening reports no new discoveries and keeps both pending.
vault, config, db = seed_pair("restart_ledger", missing_published_row=True)
before_a = (vault / "wiki/A.md").read_bytes()
before_b = (vault / "wiki/B.md").read_bytes()
lwc["_lwc341_refresh_conflicts"](config, db, "run-1", 0.8)
first_run = db._conn.execute(
    "SELECT new_conflicts_json,pending_conflicts_json FROM lwc341_run_state WHERE singleton=1"
).fetchone()
assert len(__import__("json").loads(first_run[0])) == 2
assert len(__import__("json").loads(first_run[1])) == 2
db.close()
db = StateDB(config.state_db_path)
lwc["_lwc341_refresh_conflicts"](config, db, "run-2", 0.8)
second_run = db._conn.execute(
    "SELECT new_conflicts_json,pending_conflicts_json FROM lwc341_run_state WHERE singleton=1"
).fetchone()
assert __import__("json").loads(second_run[0]) == []
assert len(__import__("json").loads(second_run[1])) == 2
assert (vault / "wiki/A.md").read_bytes() == before_a
assert (vault / "wiki/B.md").read_bytes() == before_b

def worker_operation(db, config, name, action, operation_id, payload):
    path = f"wiki/{name}.md"
    operation = {
        "operation_id": operation_id,
        "payload_sha256": payload,
        "action": action,
        "conflict_id": "",
        "path": path,
        "storage_view": "legacy",
    }
    snapshot = lwc["_lwc341_snapshot"](config, db, operation)
    operation["conflict_id"] = snapshot["conflict_id"]
    operation["expected"] = {
        "conflict_id": snapshot["conflict_id"],
        "published_sha256": snapshot["published_sha256"],
        "candidate_sha256": snapshot["candidate_sha256"],
        "published_identity": snapshot["published_identity"],
        "candidate_identity": snapshot["candidate_identity"],
        "index_sha256": snapshot["index_sha256"],
        "min_confidence": 0.8,
    }
    return operation, snapshot

# Resolve only A after restart, repairing its missing published row from the
# candidate's durable Synto identity; B and all of its state remain unchanged.
candidate_b = (vault / "wiki/.drafts/B.md").read_bytes()
published_b_row = db.get_article("wiki/B.md").model_dump()
candidate_b_row = db.get_article("wiki/.drafts/B.md").model_dump()
operation_a, snapshot_a = worker_operation(db, config, "A", "keep_published", "operation-a", "a" * 64)
assert lwc["_lwc341_local_source_matches"](vault, operation_a)
result_a = lwc["_lwc341_resolve"](config, db, operation_a)
assert result_a["status"] == "committed"
assert result_a["input_sha256"] == snapshot_a["candidate_sha256"]
assert (vault / "wiki/A.md").read_bytes() == before_a
assert not (vault / "wiki/.drafts/A.md").exists()
assert db.get_article("wiki/A.md").status == "published"
assert db.get_article("wiki/A.md").entity_id == "candidate-entity-A"
assert db.get_article("wiki/.drafts/A.md") is None
generate_index_json(config, db)
index_a = json.loads((vault / ".synto/INDEX.json").read_text())
assert any(
    article["path"] == "wiki/A.md" and article["entity_id"] == "candidate-entity-A"
    for article in index_a["articles"]
)
assert (vault / "wiki/.drafts/B.md").read_bytes() == candidate_b
assert db.get_article("wiki/B.md").model_dump() == published_b_row
assert db.get_article("wiki/.drafts/B.md").model_dump() == candidate_b_row
remaining = db._conn.execute(
    "SELECT pending_conflicts_json FROM lwc341_run_state WHERE singleton=1"
).fetchone()[0]
assert len(__import__("json").loads(remaining)) == 1

# Reopening and replaying A returns the durable receipt before stale checking;
# a different payload for the same operation ID is rejected without a write.
db.close()
db = StateDB(config.state_db_path)
replayed = lwc["_lwc341_resolve"](config, db, operation_a)
assert replayed == result_a
mismatch = dict(operation_a, payload_sha256="b" * 64)
assert lwc["_lwc341_resolve"](config, db, mismatch)["status"] == "payload_mismatch"
assert (vault / "wiki/.drafts/B.md").read_bytes() == candidate_b

# A new operation with the old candidate snapshot is stale and preserves B.
stale_b, _ = worker_operation(db, config, "B", "publish_candidate", "operation-b-stale", "c" * 64)
assert lwc["_lwc341_local_source_matches"](vault, stale_b)
(vault / "wiki/.drafts/B.md").write_bytes(candidate_b + b"changed after compare")
assert not lwc["_lwc341_local_source_matches"](vault, stale_b)
assert lwc["_lwc341_resolve"](config, db, stale_b)["status"] == "stale"
assert (vault / "wiki/B.md").read_bytes() == before_b
db.close()

# Low confidence cannot be resolved through either action.
vault_low, config_low, db_low = seed_pair("low_confidence_resolve", confidence=0.1)
lwc["_lwc341_refresh_conflicts"](config_low, db_low, "low-run", 0.8)
low_operation, _ = worker_operation(db_low, config_low, "A", "publish_candidate", "low-op", "d" * 64)
assert lwc["_lwc341_resolve"](config_low, db_low, low_operation)["status"] == "low_confidence"
low_keep_operation, _ = worker_operation(db_low, config_low, "A", "keep_published", "low-keep-op", "f" * 64)
assert lwc["_lwc341_resolve"](config_low, db_low, low_keep_operation)["status"] == "low_confidence"
assert (vault_low / "wiki/.drafts/A.md").exists()
assert parse_note(vault_low / "wiki/A.md")[1] == "published A original"
db_low.close()

# Publishing a candidate follows Synto metadata transforms and records separate
# full input/output hashes plus the body hash computed from the written page.
vault_pub, config_pub, db_pub = seed_pair("publish_candidate", missing_published_row=False)
candidate_path = vault_pub / "wiki/.drafts/A.md"
candidate_path.write_text(
    "---\ntitle: A\nstatus: draft\nupdated: 2001-01-01\nconfidence: 0.9\ntags: [\"Alpha Tag\", \"alpha tag\"]\n---\n"
    "Candidate body\n<!-- synto-auto: review annotation -->\n"
)
old_candidate_body = parse_note(candidate_path)[1]
db_pub.upsert_article(WikiArticleRecord(
    path="wiki/.drafts/A.md", title="A", sources=[],
    content_hash=compile_pipeline._content_hash(old_candidate_body), status="draft",
    article_id="candidate-article-A", entity_id="candidate-entity-A",
))
lwc["_lwc341_refresh_conflicts"](config_pub, db_pub, "publish-run", 0.8)
publish_op, publish_snapshot = worker_operation(db_pub, config_pub, "A", "publish_candidate", "publish-op", "e" * 64)
published_result = lwc["_lwc341_resolve"](config_pub, db_pub, publish_op)
published_path = vault_pub / "wiki/A.md"
published_bytes = published_path.read_bytes()
published_meta, published_body = parse_note(published_path)
assert published_result["status"] == "committed"
assert published_result["input_sha256"] == publish_snapshot["candidate_sha256"]
assert published_result["output_sha256"] == sha(published_bytes)
assert published_result["output_sha256"] != published_result["input_sha256"]
assert published_result["content_hash"] == compile_pipeline._content_hash(published_body)
assert published_meta["status"] == "published" and published_meta["updated"] != "2001-01-01"
assert "synto-auto" not in published_body
assert db_pub.get_article("wiki/A.md").content_hash == compile_pipeline._content_hash(published_body)
assert db_pub.get_article("wiki/A.md").article_id == "candidate-article-A"
assert db_pub.get_article("wiki/A.md").entity_id == "candidate-entity-A"
assert not candidate_path.exists()
db_pub.close()

# Publishing a candidate also succeeds when the published DB row was absent;
# the resulting published row and Synto identity index follow the candidate.
vault_pub_orphan, config_pub_orphan, db_pub_orphan = seed_pair(
    "publish_candidate_missing_row", missing_published_row=True
)
lwc["_lwc341_refresh_conflicts"](config_pub_orphan, db_pub_orphan, "publish-orphan-run", 0.8)
orphan_op, orphan_snapshot = worker_operation(
    db_pub_orphan, config_pub_orphan, "A", "publish_candidate", "publish-orphan-op", "1" * 64
)
orphan_result = lwc["_lwc341_resolve"](config_pub_orphan, db_pub_orphan, orphan_op)
orphan_target = vault_pub_orphan / "wiki/A.md"
orphan_target_bytes = orphan_target.read_bytes()
orphan_meta, orphan_body = parse_note(orphan_target)
orphan_row = db_pub_orphan.get_article("wiki/A.md")
assert orphan_result["status"] == "committed"
assert orphan_result["input_sha256"] == orphan_snapshot["candidate_sha256"]
assert orphan_result["output_sha256"] == sha(orphan_target_bytes)
assert orphan_row.status == "published"
assert orphan_row.content_hash == compile_pipeline._content_hash(orphan_body)
assert orphan_row.article_id == "candidate-article-A"
assert orphan_row.entity_id == "candidate-entity-A"
assert db_pub_orphan.get_article("wiki/.drafts/A.md") is None
generate_index_json(config_pub_orphan, db_pub_orphan)
orphan_index = json.loads((vault_pub_orphan / ".synto/INDEX.json").read_text())
assert any(
    article["path"] == "wiki/A.md" and article["entity_id"] == "candidate-entity-A"
    for article in orphan_index["articles"]
)
db_pub_orphan.close()

# A pre-existing non-collision draft that compile did not return is not included
# in auto-approve's path list and remains untouched on a cold-start run.
vault_old = ROOT / "existing_noncollision"
(vault_old / ".synto").mkdir(parents=True)
(vault_old / "wiki/.drafts").mkdir(parents=True)
(vault_old / "synto.toml").write_text("[pipeline]\nauto_commit = false\nauto_maintain = false\n")
config_old = Config.from_vault(vault_old)
db_old = StateDB(config_old.state_db_path)
old_draft = vault_old / "wiki/.drafts/old.md"
write_note(old_draft, {"title": "old", "status": "draft"}, "existing draft")
db_old.upsert_article(WikiArticleRecord(
    path="wiki/.drafts/old.md", title="old", sources=[],
    content_hash=sha(b"existing draft"), status="draft",
))
with patch("synto.pipeline.orchestrator._run_compile", return_value=([], [], {})):
    report = PipelineOrchestrator(config_old, None, db_old).run(auto_approve=True, max_rounds=1)
assert report.published == 0 and old_draft.exists() and not (vault_old / "wiki/old.md").exists()
db_old.close()

print("LWC-341 disposable publication guard cases passed")
