"""LWC execution policy for the SHA-pinned Synto 0.7.0; never writes config.

Config.resolve_role is the shared seam after provider/profile/CLI precedence.
OpenAICompatClient ignores think and merges options last into the wire payload.
"""
import hashlib
import json
import httpx
import ipaddress
import math
import logging
import os
import sqlite3
import socket
import uuid
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

import synto
from synto.config import Config, ResolvedModel
from synto.openai_compat_client import LLMError, OpenAICompatClient
from synto.providers import ProviderInfo, PROVIDER_REGISTRY
from synto.pipeline import compile as compile_pipeline, ingest
from synto.pipeline.orchestrator import PipelineOrchestrator, PipelineReport
from synto.vault import parse_note

if synto.__version__ != "0.7.0":
    raise RuntimeError("LWC execution adapter requires pinned Synto 0.7.0")

# Synto calls this builder only for a newly generated draft. Add identity to
# that compile_run before serialization; publish_drafts keeps the frontmatter
# intact, and carried articles never pass through this seam.
_build_wiki_frontmatter = compile_pipeline.build_wiki_frontmatter


def _build_wiki_frontmatter_with_generator(*args, **kwargs):
    lineage = kwargs.get("lineage")
    if isinstance(lineage, list):
        kwargs = dict(kwargs)
        kwargs["lineage"] = [
            {
                **entry,
                "generator": {
                    "implementation": "synto",
                    "version": synto.__version__,
                },
            }
            if isinstance(entry, dict)
            and isinstance(entry.get("compile_run"), str)
            and entry["compile_run"].strip()
            else entry
            for entry in lineage
        ]
    return _build_wiki_frontmatter(*args, **kwargs)


compile_pipeline.build_wiki_frontmatter = _build_wiki_frontmatter_with_generator

# Synto 0.7.0 publishes to the same relative path without checking whether the
# target already exists. Keep the guard in this LWC adapter so every LWC
# approve_drafts/publish_drafts caller shares it without changing the pinned
# third-party package. A held candidate stays in .drafts for read-only review.
_publish_drafts = compile_pipeline.publish_drafts


def _publish_drafts_without_path_collisions(
    config, db, paths=None, notes="", min_confidence=0.0
):
    candidates = paths
    if candidates is None:
        candidates = list(config.drafts_dir.rglob("*.md")) if config.drafts_dir.exists() else []

    publishable = []
    for draft_path in candidates:
        draft_path = Path(draft_path)
        try:
            relative = draft_path.resolve().relative_to(config.drafts_dir.resolve())
        except ValueError:
            # Preserve Synto's existing validation/error behavior for paths
            # outside its draft root.
            publishable.append(draft_path)
            continue
        target = config.wiki_dir / relative
        published_rel = (Path("wiki") / relative).as_posix()
        if target.exists() or target.is_symlink() or db.get_article(published_rel) is not None:
            logging.getLogger("synto").warning(
                "Held back path collision: %s (published file or row exists)",
                published_rel,
            )
            continue
        publishable.append(draft_path)

    return _publish_drafts(
        config,
        db,
        paths=publishable,
        notes=notes,
        min_confidence=min_confidence,
    )


compile_pipeline.publish_drafts = _publish_drafts_without_path_collisions


def _lwc341_ensure_state(db):
    db._conn.executescript(
        """
        CREATE TABLE IF NOT EXISTS lwc341_conflicts (
            conflict_id TEXT PRIMARY KEY,
            path TEXT NOT NULL,
            published_sha256 TEXT NOT NULL,
            candidate_sha256 TEXT NOT NULL,
            published_identity_json TEXT NOT NULL,
            candidate_identity_json TEXT NOT NULL,
            confidence REAL NOT NULL,
            min_confidence REAL NOT NULL,
            first_seen_run TEXT NOT NULL,
            last_seen_run TEXT NOT NULL,
            status TEXT NOT NULL,
            action TEXT
        );
        CREATE TABLE IF NOT EXISTS lwc341_run_state (
            singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
            run_id TEXT NOT NULL,
            new_conflicts_json TEXT NOT NULL,
            pending_conflicts_json TEXT NOT NULL,
            min_confidence REAL NOT NULL
        );
        CREATE TABLE IF NOT EXISTS lwc341_operations (
            principal_id TEXT NOT NULL,
            operation_id TEXT NOT NULL,
            payload_sha256 TEXT NOT NULL,
            result_json TEXT NOT NULL,
            created_at TEXT NOT NULL,
            PRIMARY KEY (principal_id, operation_id)
        );
        CREATE TABLE IF NOT EXISTS lwc341_operation_attempts (
            principal_id TEXT NOT NULL,
            operation_id TEXT NOT NULL,
            payload_sha256 TEXT NOT NULL,
            result_json TEXT NOT NULL,
            created_at TEXT NOT NULL,
            PRIMARY KEY (principal_id, operation_id, payload_sha256)
        );
        """
    )


def _lwc341_article_identity(db, relative_path):
    article = db.get_article(relative_path)
    if article is None:
        return {"exists": False}
    return {
        "exists": True,
        "status": article.status,
        "content_hash": article.content_hash,
        "article_id": article.article_id or "",
        "entity_id": article.entity_id or "",
    }


def _lwc341_confidence(meta):
    try:
        value = float(meta.get("confidence", 1.0))
    except (TypeError, ValueError):
        return 0.0
    if not math.isfinite(value) or value < 0.0 or value > 1.0:
        return 0.0
    return value


def _lwc341_conflict_id(project_id, relative_path, published_sha256, candidate_sha256):
    raw = "\0".join((project_id, relative_path, published_sha256, candidate_sha256))
    return hashlib.sha256(raw.encode()).hexdigest()


def _lwc341_snapshot(config, db, operation):
    relative_path = operation.get("path", "")
    if not relative_path.startswith("wiki/") or relative_path.startswith("wiki/.drafts/") or ".." in Path(relative_path).parts or "\\" in relative_path:
        raise ValueError("invalid concept conflict path")
    published_path = config.wiki_dir / relative_path.removeprefix("wiki/")
    candidate_path = config.drafts_dir / relative_path.removeprefix("wiki/")
    for path in (published_path, candidate_path):
        if path.is_symlink() or not path.is_file():
            return None
        if not path.resolve().is_relative_to(config.vault.resolve()):
            return None
    published_bytes = published_path.read_bytes()
    candidate_bytes = candidate_path.read_bytes()
    published_sha256 = hashlib.sha256(published_bytes).hexdigest()
    candidate_sha256 = hashlib.sha256(candidate_bytes).hexdigest()
    published_meta, published_body = parse_note(published_path)
    candidate_meta, candidate_body = parse_note(candidate_path)
    published_rel = str(published_path.resolve().relative_to(config.vault.resolve()))
    candidate_rel = str(candidate_path.resolve().relative_to(config.vault.resolve()))
    published_identity = _lwc341_article_identity(db, published_rel)
    candidate_identity = _lwc341_article_identity(db, candidate_rel)
    index_path = config.vault / ".synto" / "INDEX.json"
    index_sha256 = hashlib.sha256(index_path.read_bytes()).hexdigest() if index_path.is_file() and not index_path.is_symlink() else ""
    confidence = _lwc341_confidence(candidate_meta)
    project_id = os.environ.get("LWC_PROJECT_ID", "")
    return {
        "project_id": project_id,
        "path": relative_path,
        "published_sha256": published_sha256,
        "candidate_sha256": candidate_sha256,
        "published_identity": published_identity,
        "candidate_identity": candidate_identity,
        "published_body_sha256": compile_pipeline._content_hash(published_body),
        "candidate_body_sha256": compile_pipeline._content_hash(candidate_body),
        "published_meta": published_meta,
        "candidate_meta": candidate_meta,
        "index_sha256": index_sha256,
        "confidence": confidence,
        "conflict_id": _lwc341_conflict_id(project_id, relative_path, published_sha256, candidate_sha256),
    }


def _lwc341_local_source_matches(source_vault, operation):
    expected = operation.get("expected", {})
    source = Path(source_vault).resolve()
    published_rel = operation.get("path", "")
    if not published_rel.startswith("wiki/"):
        return False
    candidate_rel = "wiki/.drafts/" + published_rel.removeprefix("wiki/")
    for relative, key in ((published_rel, "published_sha256"), (candidate_rel, "candidate_sha256")):
        path = source / relative
        if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(source):
            return False
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected.get(key):
            return False

    state_path = source / ".synto/state.db"
    identities = {}
    if state_path.is_file() and not state_path.is_symlink():
        try:
            connection = sqlite3.connect(state_path.as_uri() + "?mode=ro", uri=True)
            try:
                for relative in (published_rel, candidate_rel):
                    row = connection.execute(
                        "SELECT status,content_hash,COALESCE(article_id,''),COALESCE(entity_id,'') FROM wiki_articles WHERE path=?",
                        (relative,),
                    ).fetchone()
                    identities[relative] = (
                        {"exists": False} if row is None else {
                            "exists": True, "status": row[0], "content_hash": row[1],
                            "article_id": row[2], "entity_id": row[3],
                        }
                    )
            finally:
                connection.close()
        except sqlite3.Error:
            return False
    else:
        identities = {published_rel: {"exists": False}, candidate_rel: {"exists": False}}
    if identities[published_rel] != expected.get("published_identity") or identities[candidate_rel] != expected.get("candidate_identity"):
        return False

    index_path = source / ".synto/INDEX.json"
    if index_path.is_symlink() or not index_path.is_file():
        index_sha256 = ""
    else:
        index_sha256 = hashlib.sha256(index_path.read_bytes()).hexdigest()
    return index_sha256 == expected.get("index_sha256", "")


def _lwc341_refresh_conflicts(config, db, run_id, min_confidence):
    _lwc341_ensure_state(db)
    project_id = os.environ.get("LWC_PROJECT_ID", "")
    pending = []
    new = []
    active_ids = set()
    if config.drafts_dir.exists():
        for candidate_path in sorted(config.drafts_dir.rglob("*.md")):
            if candidate_path.is_symlink() or not candidate_path.is_file() or not candidate_path.resolve().is_relative_to(config.vault.resolve()):
                continue
            relative = candidate_path.resolve().relative_to(config.vault.resolve()).as_posix()
            target = config.wiki_dir / relative.removeprefix("wiki/.drafts/")
            if not target.is_file() or target.is_symlink():
                continue
            meta, _ = parse_note(candidate_path)
            confidence = _lwc341_confidence(meta)
            if confidence < min_confidence:
                continue
            published_bytes = target.read_bytes()
            candidate_bytes = candidate_path.read_bytes()
            published_sha256 = hashlib.sha256(published_bytes).hexdigest()
            candidate_sha256 = hashlib.sha256(candidate_bytes).hexdigest()
            published_rel = target.resolve().relative_to(config.vault.resolve()).as_posix()
            candidate_rel = relative
            conflict_id = _lwc341_conflict_id(project_id, published_rel, published_sha256, candidate_sha256)
            published_identity = _lwc341_article_identity(db, published_rel)
            candidate_identity = _lwc341_article_identity(db, candidate_rel)
            prior = db._conn.execute(
                "SELECT first_seen_run, status FROM lwc341_conflicts WHERE conflict_id = ?",
                (conflict_id,),
            ).fetchone()
            first_seen = prior[0] if prior and prior[1] == "pending" else run_id
            was_new = not prior or prior[1] != "pending"
            db._conn.execute(
                """INSERT INTO lwc341_conflicts
                   (conflict_id,path,published_sha256,candidate_sha256,published_identity_json,candidate_identity_json,
                    confidence,min_confidence,first_seen_run,last_seen_run,status,action)
                   VALUES (?,?,?,?,?,?,?,?,?,?,?,NULL)
                   ON CONFLICT(conflict_id) DO UPDATE SET
                    published_identity_json=excluded.published_identity_json,
                    candidate_identity_json=excluded.candidate_identity_json,
                    confidence=excluded.confidence,min_confidence=excluded.min_confidence,
                    last_seen_run=excluded.last_seen_run,status='pending',action=NULL""",
                (conflict_id, published_rel, published_sha256, candidate_sha256,
                 json.dumps(published_identity, sort_keys=True, separators=(",", ":")),
                 json.dumps(candidate_identity, sort_keys=True, separators=(",", ":")),
                 confidence, min_confidence, first_seen, run_id, "pending"),
            )
            active_ids.add(conflict_id)
            pending.append(conflict_id)
            if was_new:
                new.append(conflict_id)
    prior_pending = db._conn.execute(
        "SELECT conflict_id FROM lwc341_conflicts WHERE status='pending'"
    ).fetchall()
    for (conflict_id,) in prior_pending:
        if conflict_id not in active_ids:
            db._conn.execute(
                "UPDATE lwc341_conflicts SET status='stale' WHERE conflict_id=?",
                (conflict_id,),
            )
    db._conn.execute(
        """INSERT INTO lwc341_run_state(singleton,run_id,new_conflicts_json,pending_conflicts_json,min_confidence)
           VALUES (1,?,?,?,?)
           ON CONFLICT(singleton) DO UPDATE SET run_id=excluded.run_id,
             new_conflicts_json=excluded.new_conflicts_json,
             pending_conflicts_json=excluded.pending_conflicts_json,
             min_confidence=excluded.min_confidence""",
        (run_id, json.dumps(new), json.dumps(pending), min_confidence),
    )
    db._conn.commit()


def _lwc341_store_attempt(db, operation, status, result):
    _lwc341_ensure_state(db)
    encoded = json.dumps(result, sort_keys=True, separators=(",", ":"))
    db._conn.execute(
        """INSERT INTO lwc341_operation_attempts(principal_id,operation_id,payload_sha256,result_json,created_at)
           VALUES (?,?,?,?,?) ON CONFLICT(principal_id,operation_id,payload_sha256) DO NOTHING""",
        (os.environ.get("USER_ID", ""), operation["operation_id"], operation["payload_sha256"], encoded, datetime.now(timezone.utc).isoformat()),
    )
    db._conn.commit()
    return result


def _lwc341_resolve(config, db, operation):
    _lwc341_ensure_state(db)
    operation_id = operation.get("operation_id", "")
    payload_sha256 = operation.get("payload_sha256", "")
    if not operation_id or len(operation_id) > 96 or len(payload_sha256) != 64:
        raise ValueError("invalid concept conflict operation identity")
    principal_id = os.environ.get("USER_ID", "")
    if not principal_id:
        raise ValueError("concept conflict principal is unavailable")
    prior = db._conn.execute(
        "SELECT payload_sha256,result_json FROM lwc341_operations WHERE principal_id=? AND operation_id=?",
        (principal_id, operation_id),
    ).fetchone()
    if prior:
        if prior[0] == payload_sha256:
            result = json.loads(prior[1])
            result["_lwc341_replayed"] = True
            return result
        return _lwc341_store_attempt(db, operation, "payload_mismatch", {
            "operation_id": operation_id, "payload_sha256": payload_sha256,
            "status": "payload_mismatch", "action": operation.get("action", ""),
        })

    snapshot = _lwc341_snapshot(config, db, operation)
    status = "stale"
    if snapshot is None:
        return _lwc341_store_attempt(db, operation, status, {
            "operation_id": operation_id, "payload_sha256": payload_sha256,
            "status": status, "action": operation.get("action", ""),
        })
    expected = operation.get("expected", {})
    if operation.get("storage_view", "") != os.environ.get("LWC_CONFLICT_STORAGE_VIEW", "") or any(
        snapshot.get(key) != expected.get(expected_key)
        for key, expected_key in (
            ("conflict_id", "conflict_id"),
            ("published_sha256", "published_sha256"),
            ("candidate_sha256", "candidate_sha256"),
            ("published_identity", "published_identity"),
            ("candidate_identity", "candidate_identity"),
            ("index_sha256", "index_sha256"),
        )
    ):
        return _lwc341_store_attempt(db, operation, status, {
            "operation_id": operation_id, "payload_sha256": payload_sha256,
            "status": status, "action": operation.get("action", ""),
        })

    run_state = db._conn.execute(
        "SELECT min_confidence FROM lwc341_run_state WHERE singleton=1"
    ).fetchone()
    threshold = run_state[0] if run_state else 0.0
    expected = operation.get("expected", {})
    if expected.get("min_confidence") != threshold:
        return _lwc341_store_attempt(db, operation, status, {
            "operation_id": operation_id, "payload_sha256": payload_sha256,
            "status": status, "action": operation.get("action", ""),
        })
    if snapshot["confidence"] < threshold:
        return _lwc341_store_attempt(db, operation, "low_confidence", {
            "operation_id": operation_id, "payload_sha256": payload_sha256,
            "status": "low_confidence", "action": operation["action"],
        })

    published_rel = operation["path"]
    candidate_rel = "wiki/.drafts/" + published_rel.removeprefix("wiki/")
    candidate_path = config.vault / candidate_rel
    published_path = config.vault / published_rel
    action = operation.get("action")
    if action == "keep_published":
        published_row = db.get_article(published_rel)
        if published_row is None:
            candidate_row = db.get_article(candidate_rel)
            identity = (candidate_row.entity_id or candidate_row.article_id) if candidate_row else None
            if candidate_row is None or not identity:
                return _lwc341_store_attempt(db, operation, "identity_unavailable", {
                    "operation_id": operation_id, "payload_sha256": payload_sha256,
                    "status": "identity_unavailable", "action": action,
                })
            published_meta, published_body = parse_note(published_path)
            published_row = candidate_row.model_copy(update={
                "path": published_rel,
                "title": str(published_meta.get("title", candidate_row.title)),
                "content_hash": compile_pipeline._content_hash(published_body),
                "status": "published",
                "updated_at": datetime.now(timezone.utc),
            })
            db.delete_article(candidate_rel)
            db.upsert_article(published_row)
        db.delete_article(candidate_rel)
        candidate_path.unlink()
    elif action == "publish_candidate":
        candidate_row = db.get_article(candidate_rel)
        affected = _publish_drafts(
            config, db, paths=[candidate_path], min_confidence=threshold
        )
        if not affected:
            return _lwc341_store_attempt(db, operation, "low_confidence", {
                "operation_id": operation_id, "payload_sha256": payload_sha256,
                "status": "low_confidence", "action": action,
            })
        # Synto 0.7.0's publisher moves the draft row, but its follow-up body
        # hash upsert does not carry article/entity IDs. Preserve this selected
        # candidate's identity on the published row for the INDEX export.
        published_row = db.get_article(published_rel)
        if candidate_row is not None and published_row is not None:
            db.upsert_article(published_row.model_copy(update={
                "article_id": candidate_row.article_id,
                "entity_id": candidate_row.entity_id,
            }))
    else:
        raise ValueError("unsupported concept conflict action")

    output_bytes = published_path.read_bytes()
    _, output_body = parse_note(published_path)
    result = {
        "operation_id": operation_id,
        "payload_sha256": payload_sha256,
        "status": "committed",
        "action": action,
        "conflict_id": operation["conflict_id"],
        "input_sha256": snapshot["candidate_sha256"],
        "output_sha256": hashlib.sha256(output_bytes).hexdigest(),
        "content_hash": compile_pipeline._content_hash(output_body),
    }
    db._conn.execute(
        "UPDATE lwc341_conflicts SET status='resolved', action=? WHERE conflict_id=?",
        (action, operation["conflict_id"]),
    )
    run_state = db._conn.execute(
        "SELECT new_conflicts_json,pending_conflicts_json FROM lwc341_run_state WHERE singleton=1"
    ).fetchone()
    if run_state:
        remaining_new = [item for item in json.loads(run_state[0]) if item != operation["conflict_id"]]
        remaining_pending = [item for item in json.loads(run_state[1]) if item != operation["conflict_id"]]
        db._conn.execute(
            "UPDATE lwc341_run_state SET new_conflicts_json=?,pending_conflicts_json=? WHERE singleton=1",
            (json.dumps(remaining_new), json.dumps(remaining_pending)),
        )
    encoded = json.dumps(result, sort_keys=True, separators=(",", ":"))
    created_at = datetime.now(timezone.utc).isoformat()
    db._conn.execute(
        "INSERT INTO lwc341_operations(principal_id,operation_id,payload_sha256,result_json,created_at) VALUES (?,?,?,?,?)",
        (principal_id, operation_id, payload_sha256, encoded, created_at),
    )
    db._conn.execute(
        """INSERT INTO lwc341_operation_attempts(principal_id,operation_id,payload_sha256,result_json,created_at)
           VALUES (?,?,?,?,?)""",
        (principal_id, operation_id, payload_sha256, encoded, created_at),
    )
    db._conn.commit()
    return result


_orchestrator_run = PipelineOrchestrator.run


def _run_with_lwc341_conflict_inventory(self, *args, **kwargs):
    operation_json = os.environ.get("LWC_CONCEPT_RESOLVE_JSON", "").strip()
    if operation_json:
        operation = json.loads(operation_json)
        result = _lwc341_resolve(self.config, self.db, operation)
        replayed = result.pop("_lwc341_replayed", False)
        source_vault = os.environ.get("LWC_CONFLICT_SOURCE_VAULT", "").strip()
        if result.get("status") == "committed" and not replayed and source_vault and not _lwc341_local_source_matches(source_vault, operation):
            raise RuntimeError("local concept conflict snapshot changed before publication")
        return PipelineReport(published=1 if result.get("status") == "committed" and not replayed and operation.get("action") == "publish_candidate" else 0)
    report = _orchestrator_run(self, *args, **kwargs)
    if not kwargs.get("dry_run", False):
        min_confidence = float(kwargs.get("min_confidence", 0.0))
        run_id = os.environ.get("LWC_EXECUTION_ID", "") or uuid.uuid4().hex
        _lwc341_refresh_conflicts(self.config, self.db, run_id, min_confidence)
    return report


PipelineOrchestrator.run = _run_with_lwc341_conflict_inventory

_resolve_role = Config.resolve_role
_MODELS = {
    "deepseek-chat": "disabled",
    "deepseek-reasoner": "enabled",
    "deepseek-v4-flash": "enabled",
    "deepseek-v4-pro": "enabled",
    "deepseek-flash": "enabled",
}


class _FlashResolvedModel(ResolvedModel):
    @property
    def connection_key(self):
        # The pinned router shares clients by connection; each thinking policy
        # needs its own client namespace before OpenAICompatClient's cache lookup.
        return (*super().connection_key, self.options["thinking"]["type"], self.options.get("reasoning_effort"))

    @property
    def cache_namespace(self):
        policy = json.dumps([self.options["thinking"], self.options.get("reasoning_effort")], sort_keys=True)
        return hashlib.sha256((super().cache_namespace + "\0" + policy).encode()).hexdigest()


def _resolve_flash(self, role, *, api_key_env=None):
    resolved = _resolve_role(self, role, api_key_env=api_key_env)
    if resolved.provider_kind != "deepseek":
        return resolved
    # options are merged last by the pinned client, including a model override.
    model = resolved.options.get("model", resolved.model)
    if model not in _MODELS or role == "embed":
        raise ValueError("unsupported DeepSeek execution model/role")
    options = dict(resolved.options)
    thinking = options.get("thinking", {"type": _MODELS[model]})
    if not isinstance(thinking, dict) or thinking not in (
        {"type": "enabled"}, {"type": "disabled"}
    ):
        raise ValueError("unsupported DeepSeek thinking configuration")
    if "reasoning_effort" in options and options["reasoning_effort"] not in ("low", "high", "max"):
        raise ValueError("unsupported DeepSeek reasoning effort")
    options["thinking"] = thinking
    if "model" in options:
        options["model"] = "deepseek-flash"
    return _FlashResolvedModel(**(vars(resolved) | {"model": "deepseek-flash", "options": options}))


Config.resolve_role = _resolve_flash

_profile_raw = os.environ.get("LWC_PROFILE_CONFIG", "")
if _profile_raw:
    _profile = json.loads(_profile_raw)
    _profile_keys_legacy = {"id", "revision", "provider", "protocol", "endpoint", "model"}
    _profile_keys_v1 = {"schema_version", "snapshot_digest", "role_map_version", "id", "revision", "provider", "protocol", "endpoint", "model"}
    _profile_keys_v2 = {"schema_version", "snapshot_digest", "role_map_version", "id", "revision", "provider", "protocol", "endpoint", "models"}
    if not isinstance(_profile, dict):
        raise RuntimeError("invalid resolved Project LLM profile")
    _profile_legacy = "schema_version" not in _profile and set(_profile) == _profile_keys_legacy
    if _profile_legacy:
        # Exact legacy producer payload from the base worker: one model, with
        # no synthetic snapshot or role-map metadata.
        legacy_model = _profile["model"]
        if not isinstance(legacy_model, str) or not legacy_model.strip():
            raise RuntimeError("invalid legacy Project LLM model")
        _profile.pop("model")
        _profile["models"] = {"fast": legacy_model, "heavy": legacy_model}
    elif type(_profile.get("schema_version")) is not int:
        raise RuntimeError("invalid resolved Project LLM profile version")
    elif _profile.get("schema_version") == 1 and set(_profile) == _profile_keys_v1:
        legacy_model = _profile["model"]
        if not isinstance(legacy_model, str) or not legacy_model.strip():
            raise RuntimeError("invalid legacy Project LLM model")
        _profile.pop("model")
        _profile["models"] = {"fast": legacy_model, "heavy": legacy_model}
        _profile["schema_version"] = 2
    if not _profile_legacy and (_profile.get("schema_version") != 2 or set(_profile) != _profile_keys_v2):
        raise RuntimeError("invalid resolved Project LLM profile")
    if (not isinstance(_profile["id"], str) or not _profile["id"]
            or type(_profile["revision"]) is not int or _profile["revision"] < 1
            or not isinstance(_profile["provider"], str) or not isinstance(_profile["endpoint"], str)
            or not isinstance(_profile["models"], dict) or set(_profile["models"]) != {"fast", "heavy"}
            or not all(isinstance(_profile["models"][role], str) and _profile["models"][role] == _profile["models"][role].strip() and 0 < len(_profile["models"][role]) <= 200 for role in ("fast", "heavy"))
            or (not _profile_legacy and (not isinstance(_profile["snapshot_digest"], str) or len(_profile["snapshot_digest"]) != 64
                or any(ch not in "0123456789abcdef" for ch in _profile["snapshot_digest"])
                or type(_profile["role_map_version"]) is not int or _profile["role_map_version"] != 1))):
        raise RuntimeError("invalid resolved Project LLM profile")
    _profile_url = urlsplit(_profile["endpoint"])
    if _profile_url.scheme != "https" or not _profile_url.hostname or _profile_url.username or _profile_url.password or _profile_url.query or _profile_url.fragment:
        raise RuntimeError("invalid resolved Project LLM endpoint")
    _profile_host = _profile_url.hostname.rstrip(".").lower()
    _profile_call_success_marker_path = os.environ.get("LWC_PROFILE_CALL_SUCCESS_MARKER_PATH", "")
    _profile_call_attempt_marker_path = os.environ.get("LWC_PROFILE_CALL_ATTEMPT_MARKER_PATH", "")
    _original_getaddrinfo = socket.getaddrinfo

    def _mark_profile_marker(path, label):
        if not path:
            return False
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
        try:
            fd = os.open(path, flags, 0o600)
        except FileExistsError:
            return False
        except OSError as exc:
            raise RuntimeError(f"Project profile {label} marker is unavailable") from exc
        os.close(fd)
        return True

    def _mark_profile_call_success():
        _mark_profile_marker(_profile_call_success_marker_path, "worked")

    def _is_profile_quota_response(response):
        if response.status_code != 429:
            return False
        try:
            envelope = response.json()
        except Exception:
            return False
        error = envelope.get("error") if isinstance(envelope, dict) else None
        code = error.get("code") if isinstance(error, dict) else None
        return isinstance(code, str) and code.strip().lower() in _QUOTA_CODES

    def _clear_profile_attempt_for_quota(owned_by_request):
        if not owned_by_request:
            return
        try:
            os.unlink(_profile_call_attempt_marker_path)
        except FileNotFoundError:
            return
        except OSError as exc:
            raise RuntimeError("Project profile quota attempt marker could not be cleared") from exc

    _profile_httpx_send = httpx.Client.send

    def _send_with_profile_call_success_marker(self, request, *args, **kwargs):
        profile_generation = (
            request.url.host.rstrip(".").lower() == _profile_host
            and request.method.upper() != "GET"
        )
        owns_attempt_marker = _mark_profile_marker(_profile_call_attempt_marker_path, "attempt") if profile_generation else False
        response = _profile_httpx_send(self, request, *args, **kwargs)
        if profile_generation and 200 <= response.status_code < 300:
            # This per-batch marker is monotonic: any generation response that
            # completed means a later stage cannot safely replay the batch.
            _mark_profile_call_success()
        elif profile_generation and _is_profile_quota_response(response):
            # Clear only the provisional marker created by this exact request.
            _clear_profile_attempt_for_quota(owns_attempt_marker)
        return response

    httpx.Client.send = _send_with_profile_call_success_marker

    def _public_profile_dns(host, *args, **kwargs):
        if isinstance(host, str) and host.rstrip(".").lower() == _profile_host:
            results = _original_getaddrinfo(host, *args, **kwargs)
            if not results:
                raise OSError("Project LLM endpoint DNS lookup failed")
            for result in results:
                try:
                    address = ipaddress.ip_address(result[4][0].split("%", 1)[0])
                except (ValueError, IndexError, TypeError) as exc:
                    raise OSError("Project LLM endpoint DNS response is invalid") from exc
                if not address.is_global:
                    raise OSError("Project LLM endpoint resolved to a non-public address")
            # Return the exact validated sockaddr records; socket.create_connection
            # uses them directly, so a second DNS lookup cannot rebind the host.
            return results
        return _original_getaddrinfo(host, *args, **kwargs)

    socket.getaddrinfo = _public_profile_dns
    if _profile["protocol"] == "anthropic_messages":
        _profile_kind = "lwc_anthropic_compat"
        PROVIDER_REGISTRY[_profile_kind] = ProviderInfo(
            _profile_kind, "LWC Anthropic-compatible", _profile["endpoint"], True, False, False, False,
            120.0, "LWC_PROFILE_API_KEY", anthropic_compat=True,
        )
    elif _profile["protocol"] == "openai_chat_completions":
        _profile_kind = "custom"
    else:
        raise RuntimeError("unsupported resolved Project LLM protocol")

    _resolve_profile_base = Config.resolve_role

    def _resolve_profile(self, role, *, api_key_env=None):
        resolved = _resolve_profile_base(self, role, api_key_env=api_key_env)
        if role == "embed":
            return resolved
        if role not in ("fast", "heavy"):
            raise ValueError("unsupported Project LLM role")
        api_key = os.environ.get("LWC_PROFILE_API_KEY", "")
        profile_values = vars(resolved).copy()
        profile_values.update({
            "provider_kind": _profile_kind,
            "url": _profile["endpoint"],
            "api_key": api_key or None,
            "api_key_env": "LWC_PROFILE_API_KEY" if api_key else None,
            "model": _profile["models"][role],
            "think": None,
            "supports_json_mode": _profile["protocol"] == "openai_chat_completions",
            "supports_embeddings": False,
            "azure": False,
            "anthropic_compat": _profile["protocol"] == "anthropic_messages",
            "options": {key: value for key, value in resolved.options.items()
                        if key != "model" and key not in ("thinking", "reasoning_effort")},
            "headers": {},
        })
        return ResolvedModel(**profile_values)

    Config.resolve_role = _resolve_profile

# Synto 0.7.0 intentionally collapses all HTTP 429 responses to rate limits.
# Preserve its native protocol clients but recognize only provider-confirmed
# account quota codes for a clean shared-pool retry.
_QUOTA_CODES = {"insufficient_quota", "quota_exhausted", "quota_exceeded", "billing_hard_limit_reached"}
_quota_marker_path = os.environ.get("LWC_PROFILE_QUOTA_MARKER_PATH", "")

def _is_explicit_quota(exc):
    response = getattr(exc, "response", None)
    if response is None or response.status_code != 429:
        return False
    try:
        envelope = response.json()
    except Exception:
        return False
    error = envelope.get("error") if isinstance(envelope, dict) else None
    code = error.get("code") if isinstance(error, dict) else None
    return isinstance(code, str) and code.strip().lower() in _QUOTA_CODES

def _mark_explicit_quota():
    if not _quota_marker_path:
        return
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(_quota_marker_path, flags, 0o600)
    os.close(fd)

def _wrap_provider_error(original):
    def wrapped(self, exc, context=""):
        if _is_explicit_quota(exc):
            _mark_explicit_quota()
            return LLMError("shared provider quota exhausted")
        return original(self, exc, context)
    return wrapped

OpenAICompatClient._wrap_error = _wrap_provider_error(OpenAICompatClient._wrap_error)
from synto.anthropic_compat_client import AnthropicCompatClient
AnthropicCompatClient._wrap_error = _wrap_provider_error(AnthropicCompatClient._wrap_error)

# The worker supplies a private copy of the vault's base schema only when it
# pinned nonempty Profile guidance. Profile text is injected below only into
# compile_article prompts, outside Synto's schema-size budget.
_base_schema_path = os.environ.get("LWC_BASE_SCHEMA_PATH")
if _base_schema_path:
    Config.schema_path = property(lambda self: Path(_base_schema_path))
_profile_guidance = os.environ.get("LWC_PROFILE_GUIDANCE", "")
if _profile_guidance:
    _request_structured = compile_pipeline.request_structured

    def _request_structured_with_profile_guidance(*args, **kwargs):
        if kwargs.get("stage") == "compile_article" and isinstance(kwargs.get("prompt"), str):
            kwargs["prompt"] += (
                "\n\nPROJECT PROFILE COMPILE GUIDANCE\n"
                "Follow these project-specific requirements for this article, including over inferred output-language defaults when applicable:\n"
                + _profile_guidance
            )
        return _request_structured(*args, **kwargs)

    compile_pipeline.request_structured = _request_structured_with_profile_guidance
# New compile/checkpoint provenance must name the same effective model as the
# role endpoint; this does not rewrite old rows or force a pipeline execution.
Config.model_name = lambda self, role: self.resolve_role(role).model

# Resumable ingest chunks have a second, independent cache in this exact wheel.
# Preserve its content/prompt/account identity and add only effective policy.
_ingest_checkpoint_hash = ingest._checkpoint_hash


def _flash_checkpoint_hash(content_hash, config, prompt_contexts, source_type="notes"):
    digest = _ingest_checkpoint_hash(content_hash, config, prompt_contexts, source_type)
    fast = config.resolve_role("fast")
    if fast.provider_kind != "deepseek":
        return digest
    policy = json.dumps([fast.options["thinking"], fast.options.get("reasoning_effort")], sort_keys=True)
    return hashlib.sha256((digest + "\0" + policy).encode()).hexdigest()


ingest._checkpoint_hash = _flash_checkpoint_hash

if __name__ == "__main__":
    from synto.cli import cli

    cli(prog_name="synto")
