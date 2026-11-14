#!/usr/bin/env python3
"""Offline-safe workflow and operator helpers for private Release Notes artifacts."""

from __future__ import annotations

import argparse
import base64
from datetime import datetime
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import time
import uuid
import zipfile

import release_notes


WORKFLOW_PATH = ".github/workflows/deploy-dev.yml"
READY_WORKFLOW_PATH = ".github/workflows/deploy-dev.yml"
MAX_DISPATCH_ZIP_BYTES = 40 * 1024
MAX_DISPATCH_BASE64_CHARS = 4 * ((MAX_DISPATCH_ZIP_BYTES + 2) // 3)
MAX_MANIFEST_BYTES = 32 * 1024
MAX_LOCALE_BYTES = 64 * 1024
MAX_PACKAGE_BYTES = MAX_MANIFEST_BYTES + 2 * MAX_LOCALE_BYTES
MAX_EVENT_BYTES = 72 * 1024
MAX_READY_FILE_BYTES = 16 * 1024 * 1024
MAX_NOTES_EVIDENCE_BYTES = 256 * 1024
PACKAGE_FILES = {"release-notes-manifest.json", "en.json", "zh-TW.json"}
READY_NAME_RE = re.compile(r"^lwc-ready-development-([1-9][0-9]*)-([1-9][0-9]*)$")
ARTIFACT_DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
ACTION_DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
ID_RE = re.compile(r"^[1-9][0-9]*$")
REQUEST_ID_RE = re.compile(r"^[0-9a-f]{32}$")
REPOSITORY_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
ALLOWED_RETENTION_DAYS = {30, 90}
COMPONENT_ORDER = ("documentimport", "exportjob", "auth", "bff", "worker", "frontend")


class PackageWorkflowError(Exception):
    def __init__(self, safe_class: str):
        super().__init__(safe_class)
        self.safe_class = safe_class


def _fail(safe_class: str):
    raise PackageWorkflowError(safe_class)


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def _valid_timestamp(value: object) -> bool:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return parsed.tzinfo is not None and parsed.utcoffset() is not None
    except (AttributeError, TypeError, ValueError):
        return False


def _json_object(data: bytes):
    try:
        value = json.loads(data, object_pairs_hook=_unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError):
        _fail("package-json-invalid")
    if not isinstance(value, dict):
        _fail("package-json-invalid")
    return value


def _read_json(path: Path):
    try:
        if path.is_symlink() or not path.is_file():
            _fail("metadata-unavailable")
        return _json_object(path.read_bytes())
    except OSError:
        _fail("metadata-unavailable")


def _read_package_input_from_event(event_path: Path, expected_request_id: str,
                                  expected_ready_artifact_id: str,
                                  expected_retention_days: str) -> str:
    if (not REQUEST_ID_RE.fullmatch(expected_request_id) or
            not ID_RE.fullmatch(expected_ready_artifact_id) or
            expected_retention_days not in {str(value) for value in ALLOWED_RETENTION_DAYS}):
        _fail("workflow-event-invalid")
    try:
        if event_path.is_symlink() or not event_path.is_file() or event_path.stat().st_size > MAX_EVENT_BYTES:
            _fail("workflow-event-unavailable")
        event = _json_object(event_path.read_bytes())
    except OSError:
        _fail("workflow-event-unavailable")
    inputs = event.get("inputs")
    prepare_mode = inputs.get("prepare_release_notes_package") if isinstance(inputs, dict) else None
    if (not isinstance(inputs, dict) or
            not (prepare_mode is True or (type(prepare_mode) is str and prepare_mode == "true")) or
            inputs.get("operation") != "release" or
            inputs.get("release_notes_action") != "stop" or
            inputs.get("release_notes_package_request_id") != expected_request_id or
            inputs.get("artifact_id") != expected_ready_artifact_id or
            inputs.get("release_notes_package_retention_days") != expected_retention_days):
        _fail("workflow-event-invalid")
    archive_b64 = inputs.get("package_zip_base64")
    if not isinstance(archive_b64, str) or not archive_b64:
        _fail("dispatch-package-size-invalid")
    if len(archive_b64) > MAX_DISPATCH_BASE64_CHARS:
        _fail("dispatch-package-size-invalid")
    return archive_b64


def _write_github_output(path: str | None, values: dict[str, str]) -> None:
    target = path or os.environ.get("GITHUB_OUTPUT")
    if not target:
        _fail("workflow-output-unavailable")
    try:
        with Path(target).open("a", encoding="utf-8") as stream:
            for key, value in values.items():
                if "\n" in value or "\r" in value:
                    _fail("workflow-output-invalid")
                stream.write(f"{key}={value}\n")
    except OSError:
        _fail("workflow-output-unavailable")


def bff_selected_from_plan(plan_bytes: bytes) -> bool:
    plan = _json_object(plan_bytes)
    selected = plan.get("selected")
    if (not isinstance(selected, list) or not selected or
            any(not isinstance(item, str) or not item for item in selected) or
            len(selected) != len(set(selected)) or
            selected != [component for component in COMPONENT_ORDER if component in selected]):
        _fail("plan-selection-invalid")
    return "bff" in selected


def _workflow_path_matches(actual: object, expected: str) -> bool:
    return actual in (expected, f"{expected}@refs/heads/develop")


def write_bff_selection(plan_path: Path, output_path: str | None) -> bool:
    try:
        if plan_path.is_symlink() or not plan_path.is_file():
            _fail("plan-unavailable")
        selected = bff_selected_from_plan(plan_path.read_bytes())
    except OSError:
        _fail("plan-unavailable")
    _write_github_output(output_path, {"bff_selected": "true" if selected else "false"})
    return selected


def _package_directory_files(directory: Path) -> dict[str, bytes]:
    if directory.is_symlink() or not directory.is_dir():
        _fail("package-unavailable")
    files = {}
    try:
        for entry in directory.iterdir():
            if entry.is_symlink() or not entry.is_file() or entry.name not in PACKAGE_FILES:
                _fail("package-path-invalid")
            limit = MAX_MANIFEST_BYTES if entry.name == "release-notes-manifest.json" else MAX_LOCALE_BYTES
            data = entry.read_bytes()
            if len(data) > limit:
                _fail("package-file-too-large")
            files[entry.name] = data
    except OSError:
        _fail("package-unavailable")
    if "release-notes-manifest.json" not in files or not (set(files) - {"release-notes-manifest.json"}):
        _fail("package-files-incomplete")
    if sum(map(len, files.values())) > MAX_PACKAGE_BYTES:
        _fail("package-too-large")
    return files


def package_directory_archive(directory: Path) -> bytes:
    files = _package_directory_files(directory)
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for name in sorted(files):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (stat.S_IFREG | 0o600) << 16
            archive.writestr(info, files[name])
    payload = output.getvalue()
    if len(payload) > MAX_DISPATCH_ZIP_BYTES:
        _fail("dispatch-package-too-large")
    return payload


def _extract_package_archive(payload: bytes, destination: Path) -> None:
    if not payload or len(payload) > MAX_DISPATCH_ZIP_BYTES:
        _fail("dispatch-package-size-invalid")
    try:
        archive = zipfile.ZipFile(io.BytesIO(payload), "r")
    except (OSError, zipfile.BadZipFile):
        _fail("dispatch-package-invalid")
    try:
        infos = archive.infolist()
        names = [item.filename for item in infos]
        if (len(infos) not in (2, 3) or len(names) != len(set(names)) or
                "release-notes-manifest.json" not in names or
                any(name not in PACKAGE_FILES for name in names)):
            _fail("dispatch-package-paths-invalid")
        total = 0
        content = {}
        for item in infos:
            name = item.filename
            mode = (item.external_attr >> 16) & 0xFFFF
            file_type = stat.S_IFMT(mode)
            limit = MAX_MANIFEST_BYTES if name == "release-notes-manifest.json" else MAX_LOCALE_BYTES
            if (item.is_dir() or PurePosixPath(name).name != name or "\\" in name or
                    item.flag_bits & 0x1 or item.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED) or
                    file_type not in (0, stat.S_IFREG) or item.file_size < 0 or
                    item.file_size > limit or item.compress_size < 0 or item.compress_size > len(payload)):
                _fail("dispatch-package-entry-invalid")
            total += item.file_size
            if total > MAX_PACKAGE_BYTES:
                _fail("dispatch-package-too-large")
            try:
                chunks = []
                read_total = 0
                with archive.open(item, "r") as stream:
                    while True:
                        chunk = stream.read(min(8192, limit + 1 - read_total))
                        if not chunk:
                            break
                        chunks.append(chunk)
                        read_total += len(chunk)
                        if read_total > limit or read_total > item.file_size:
                            _fail("dispatch-package-entry-too-large")
                data = b"".join(chunks)
            except (OSError, RuntimeError, zipfile.BadZipFile):
                _fail("dispatch-package-invalid")
            if len(data) != item.file_size:
                _fail("dispatch-package-invalid")
            content[name] = data
    finally:
        archive.close()
    if not (set(content) - {"release-notes-manifest.json"}):
        _fail("dispatch-package-files-incomplete")
    try:
        destination.mkdir(mode=0o700, parents=True, exist_ok=False)
        for name, data in content.items():
            target = destination / name
            descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(data)
    except OSError:
        _fail("package-output-unavailable")


def _validate_development_plan(plan_bytes: bytes) -> dict:
    plan = _json_object(plan_bytes)
    normalized = plan.get("normalized")
    source = plan.get("source")
    if (not isinstance(normalized, dict) or normalized.get("environment") != "development" or
            not isinstance(source, str) or re.fullmatch(r"[0-9a-f]{40}", source) is None or
            not isinstance(plan.get("id"), str) or not plan.get("id") or
            not isinstance(plan.get("tag"), str) or not plan.get("tag") or
            not isinstance(plan.get("identities"), dict) or not isinstance(plan["identities"].get("bff"), dict) or
            not bff_selected_from_plan(plan_bytes)):
        _fail("release-plan-not-bff-dev")
    return plan


def _validate_ready_run(metadata: dict, run: dict, expected_artifact_id: str) -> tuple[str, str, str]:
    if not ID_RE.fullmatch(expected_artifact_id):
        _fail("ready-artifact-id-invalid")
    name = metadata.get("name")
    match = READY_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    workflow_run = metadata.get("workflow_run")
    run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    run_attempt = match.group(2) if match else ""
    if (type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or not match or run_id != match.group(1) or
            type(run.get("id")) is not int or run.get("id") != int(run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") != int(match.group(2)) or
            run.get("event") != "workflow_dispatch" or not _workflow_path_matches(run.get("path"), READY_WORKFLOW_PATH) or
            run.get("status") != "completed" or run.get("conclusion") != "failure" or
            run.get("head_branch") != "develop" or
            not isinstance(run.get("head_sha"), str) or re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None or
            not isinstance(metadata.get("digest"), str) or ARTIFACT_DIGEST_RE.fullmatch(metadata["digest"]) is None or
            not _valid_timestamp(metadata.get("expires_at"))):
        _fail("ready-artifact-not-qualified")
    return run_id, run_attempt, name


def find_ready_result_artifact(metadata: dict, run: dict, artifact_list: dict,
                               expected_artifact_id: str) -> dict:
    run_id, run_attempt, ready_name = _validate_ready_run(metadata, run, expected_artifact_id)
    artifacts = artifact_list.get("artifacts") if isinstance(artifact_list, dict) else None
    result_name = f"lwc-result-development-{run_id}-{run_attempt}"
    matches = [item for item in artifacts if isinstance(item, dict) and item.get("name") == result_name] \
        if isinstance(artifacts, list) else []
    if len(matches) != 1:
        _fail("ready-result-artifact-not-unique")
    result_artifact = matches[0]
    result_run = result_artifact.get("workflow_run")
    if (type(result_artifact.get("id")) is not int or result_artifact["id"] < 1 or
            result_artifact.get("expired") is not False or
            not isinstance(result_artifact.get("digest"), str) or
            ARTIFACT_DIGEST_RE.fullmatch(result_artifact["digest"]) is None or
            not _valid_timestamp(result_artifact.get("expires_at")) or
            not isinstance(result_run, dict) or str(result_run.get("id")) != run_id):
        _fail("ready-result-artifact-invalid")
    return {"ready_artifact_id": expected_artifact_id, "ready_artifact_name": ready_name,
            "run_id": run_id, "run_attempt": run_attempt,
            "result_artifact_id": str(result_artifact["id"]), "result_artifact_name": result_name,
            "result_artifact_digest": result_artifact["digest"]}


def _plan_id(plan: dict) -> str:
    schema = plan.get("schema")
    if schema == 2:
        identity = {key: value for key, value in plan.items() if key != "id"}
    elif schema == 3:
        fields = ("source", "branch", "tag", "normalized", "identities", "dev_reference", "selected")
        if any(field not in plan for field in fields):
            _fail("ready-plan-invalid")
        identity = {key: plan[key] for key in fields}
        if "release_source_mode" in plan:
            identity["release_source_mode"] = plan["release_source_mode"]
        if "auth_source" in plan:
            identity["auth_source"] = plan["auth_source"]
    else:
        _fail("ready-plan-invalid")
    return hashlib.sha256(json.dumps(identity, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def _read_artifact_file(directory: Path, name: str, limit: int) -> bytes:
    try:
        if directory.is_symlink() or not directory.is_dir():
            _fail("ready-artifact-files-invalid")
        path = directory / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
            _fail("ready-artifact-files-invalid")
        return path.read_bytes()
    except OSError:
        _fail("ready-artifact-files-invalid")


def _resume_inputs(plan: dict, ready_artifact_id: str, notes_artifact_id: str) -> dict:
    normalized = plan.get("normalized")
    auth = normalized.get("auth") if isinstance(normalized, dict) else None
    runtime_inputs = auth.get("runtime_inputs") if isinstance(auth, dict) else None
    schema_version = runtime_inputs.get("schema_version") if isinstance(runtime_inputs, dict) else 4
    source_mode = plan.get("release_source_mode", "legacy_monorepo")
    if type(schema_version) is not int or schema_version not in (3, 4) or \
            source_mode not in ("independent", "legacy_monorepo"):
        _fail("ready-plan-invalid")
    inputs = {
        "operation": "release",
        "source_sha": plan["source"],
        "release_tag": plan["tag"],
        "components": ",".join(plan["selected"]),
        "artifact_id": ready_artifact_id,
        "release_source_mode": source_mode,
        "auth_schema_version": str(schema_version),
        "dev_artifact_id": str(plan.get("dev_reference") or ""),
    }
    if notes_artifact_id:
        inputs["release_notes_artifact_id"] = notes_artifact_id
        inputs["release_notes_action"] = "run"
    auth_source = plan.get("auth_source")
    if auth_source is not None:
        repository_id = auth_source.get("repository_id") if isinstance(auth_source, dict) else None
        if not isinstance(repository_id, str) or not repository_id.startswith("github:") or \
                ID_RE.fullmatch(repository_id.removeprefix("github:")) is None:
            _fail("ready-plan-invalid")
        inputs.update({
            "auth_repository_id": repository_id.removeprefix("github:"),
            "auth_revision": str(auth_source.get("revision", "")),
            "auth_tree_sha256": str(auth_source.get("tree_sha256", "")),
            "auth_artifact_id": str(auth_source.get("artifact_id", "")),
            "auth_run_id": str(auth_source.get("run_id", "")),
            "auth_run_attempt": str(auth_source.get("run_attempt", "")),
        })
        if (re.fullmatch(r"[0-9a-f]{40}", inputs["auth_revision"]) is None or
                ARTIFACT_DIGEST_RE.fullmatch(inputs["auth_tree_sha256"]) is None or
                any(not ID_RE.fullmatch(inputs[key]) for key in
                    ("auth_artifact_id", "auth_run_id", "auth_run_attempt"))):
            _fail("ready-plan-invalid")
    return inputs


def validate_ready_handoff(metadata: dict, run: dict, artifact_list: dict,
                           result_metadata: dict, *, expected_artifact_id: str,
                           expected_result_artifact_id: str, ready_directory: Path,
                           result_directory: Path, output_path: str | None) -> dict:
    discovered = find_ready_result_artifact(metadata, run, artifact_list, expected_artifact_id)
    if discovered["result_artifact_id"] != expected_result_artifact_id:
        _fail("ready-result-artifact-id-mismatch")
    result_workflow_run = result_metadata.get("workflow_run")
    if (type(result_metadata.get("id")) is not int or
            result_metadata.get("id") != int(expected_result_artifact_id) or
            result_metadata.get("name") != discovered["result_artifact_name"] or
            result_metadata.get("digest") != discovered["result_artifact_digest"] or
            result_metadata.get("expired") is not False or
            not _valid_timestamp(result_metadata.get("expires_at")) or
            not isinstance(result_workflow_run, dict) or
            str(result_workflow_run.get("id")) != discovered["run_id"]):
        _fail("ready-result-artifact-invalid")

    plan_bytes = _read_artifact_file(ready_directory, "plan.json", MAX_READY_FILE_BYTES)
    state_bytes = _read_artifact_file(ready_directory, "state.json", MAX_READY_FILE_BYTES)
    result_bytes = _read_artifact_file(ready_directory, "result.json", MAX_READY_FILE_BYTES)
    result_plan_bytes = _read_artifact_file(result_directory, "plan.json", MAX_READY_FILE_BYTES)
    result_state_bytes = _read_artifact_file(result_directory, "state.json", MAX_READY_FILE_BYTES)
    final_result_bytes = _read_artifact_file(result_directory, "result.json", MAX_READY_FILE_BYTES)
    notes_evidence_bytes = _read_artifact_file(result_directory, "release-notes-evidence.json",
                                               MAX_NOTES_EVIDENCE_BYTES)
    if (plan_bytes != result_plan_bytes or state_bytes != result_state_bytes or
            result_bytes != final_result_bytes):
        _fail("ready-result-artifact-diverged")

    plan = _json_object(plan_bytes)
    _validate_development_plan(plan_bytes)
    if (plan.get("branch") != "develop" or
            not isinstance(plan.get("id"), str) or re.fullmatch(r"[0-9a-f]{64}", plan["id"]) is None or
            plan.get("id") != _plan_id(plan)):
        _fail("ready-plan-invalid")
    run_sha = run.get("head_sha")
    if plan.get("schema") == 3 and plan.get("executor_sha") != run_sha:
        _fail("ready-plan-producer-mismatch")

    state = _json_object(state_bytes)
    if (state.get("plan") != plan["id"] or state.get("status") != "ready" or
            state.get("components") != {} or type(state.get("sequence")) is not int or
            isinstance(state.get("sequence"), bool) or state["sequence"] < 1 or
            type(state.get("checkpoint_schema", 1)) is not int or state.get("checkpoint_schema", 1) != 1 or
            not isinstance(state.get("builds", {}), dict)):
        _fail("ready-checkpoint-not-preruntime")

    engine_result = _json_object(result_bytes)
    expected = engine_result.get("expected")
    observed = engine_result.get("observed")
    if (engine_result.get("release") != plan["tag"] or engine_result.get("attempt") != plan["id"] or
            engine_result.get("stage") != "ready" or engine_result.get("status") != "ready" or
            engine_result.get("reason") != "completed" or
            engine_result.get("component") != plan["selected"][-1] or
            engine_result.get("mutation_may_have_happened") is not False or
            not isinstance(expected, dict) or expected.get("source") != plan["source"] or
            not isinstance(observed, dict) or observed.get("component_status") != "unstarted" or
            engine_result.get("last_verified_checkpoint") != state["sequence"] or
            "failure_diagnostic" in engine_result):
        _fail("ready-checkpoint-not-preruntime")

    notes_evidence = _json_object(notes_evidence_bytes)
    stages = notes_evidence.get("stages")
    missing_package = stages[0] if isinstance(stages, list) and len(stages) == 1 and isinstance(stages[0], dict) else None
    plan_sha256 = hashlib.sha256(plan_bytes).hexdigest()
    if (notes_evidence.get("schema_version") != 1 or missing_package is None or
            missing_package.get("action") != "prepare" or missing_package.get("environment") != "dev" or
            missing_package.get("stage") != "validate" or missing_package.get("status") != "failed" or
            missing_package.get("safe_class") != "artifact-id-required" or
            missing_package.get("product_status") != "not_started" or
            missing_package.get("plan_sha256") != plan_sha256 or
            missing_package.get("artifact_id") is not None or
            missing_package.get("next_actions") != ["retry", "skip", "stop"] or
            missing_package.get("owner_choice_required") is not True or
            missing_package.get("automatic_skip") is not False or
            missing_package.get("event_sequence") != 1 or missing_package.get("stage_attempt") != 1 or
            not _valid_timestamp(missing_package.get("recorded_at"))):
        _fail("ready-notes-gate-not-resumable")

    resume_inputs = _resume_inputs(plan, expected_artifact_id, "")
    result = {**discovered, "plan_id": plan["id"], "plan_sha256": plan_sha256,
              "source_sha": plan["source"], "release_tag": plan["tag"],
              "components": ",".join(plan["selected"]), "resume_inputs": resume_inputs}
    output = {key: str(value) for key, value in result.items()
              if key != "resume_inputs"}
    for key, value in resume_inputs.items():
        output[f"resume_{key}"] = str(value)
    _write_github_output(output_path, output)
    return result


def _validate_package_from_plan(package_dir: Path, plan_path: Path, *, verify_artifact_digest: bool = False,
                                artifact_digest: str | None = None,
                                metadata_digest: str | None = None) -> tuple[str, dict, bytes]:
    try:
        if plan_path.is_symlink() or not plan_path.is_file():
            _fail("plan-unavailable")
        plan_bytes = plan_path.read_bytes()
    except OSError:
        _fail("plan-unavailable")
    _validate_development_plan(plan_bytes)
    product_version, files, manifest = release_notes.validate_package(
        package_dir, expected_environment="dev",
        expected_artifact_digest=artifact_digest if verify_artifact_digest else None,
        artifact_metadata_digest=metadata_digest if verify_artifact_digest else None,
        plan_bytes=plan_bytes)
    return product_version, manifest, plan_bytes


def prepare_package(archive_b64: str, plan_path: Path, destination: Path, output_path: str | None,
                    request_id: str) -> dict:
    if not REQUEST_ID_RE.fullmatch(request_id):
        _fail("dispatch-request-id-invalid")
    if not isinstance(archive_b64, str) or not archive_b64 or len(archive_b64) > MAX_DISPATCH_BASE64_CHARS:
        _fail("dispatch-package-size-invalid")
    try:
        archive_bytes = base64.b64decode(archive_b64, validate=True)
    except (ValueError, base64.binascii.Error):
        _fail("dispatch-package-invalid")
    if len(archive_bytes) > MAX_DISPATCH_ZIP_BYTES:
        _fail("dispatch-package-size-invalid")
    _extract_package_archive(archive_bytes, destination)
    product_version, manifest, plan_bytes = _validate_package_from_plan(destination, plan_path)
    result = {"product_version": product_version,
              "manifest_sha256": hashlib.sha256((destination / "release-notes-manifest.json").read_bytes()).hexdigest(),
              "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
              "locale_count": len(manifest["files"])}
    _write_github_output(output_path, {"product_version": product_version})
    return result


def prepare_package_from_event(event_path: Path, plan_path: Path, destination: Path,
                               output_path: str | None, request_id: str,
                               ready_artifact_id: str, retention_days: str) -> dict:
    archive_b64 = _read_package_input_from_event(
        event_path, request_id, ready_artifact_id, retention_days)
    return prepare_package(archive_b64, plan_path, destination, output_path, request_id)


def normalize_action_digest(action_digest: str) -> str:
    if not isinstance(action_digest, str) or ACTION_DIGEST_RE.fullmatch(action_digest) is None:
        _fail("uploaded-artifact-action-digest-invalid")
    return "sha256:" + action_digest


def verify_uploaded_package(package_dir: Path, plan_path: Path, metadata: dict, *,
                            expected_artifact_id: str, expected_name: str, action_digest: str,
                            run_id: str, run_attempt: str, ready_artifact_id: str,
                            output_path: str | None) -> dict:
    workflow_run = metadata.get("workflow_run")
    normalized_action_digest = normalize_action_digest(action_digest)
    if (not ID_RE.fullmatch(expected_artifact_id) or
            not ID_RE.fullmatch(run_id) or not ID_RE.fullmatch(run_attempt) or
            not ID_RE.fullmatch(ready_artifact_id) or
            type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("name") != expected_name or
            metadata.get("digest") != normalized_action_digest or metadata.get("expired") is not False or
            not _valid_timestamp(metadata.get("expires_at")) or
            not isinstance(workflow_run, dict) or str(workflow_run.get("id")) != run_id):
        _fail("uploaded-artifact-metadata-mismatch")
    product_version, manifest, plan_bytes = _validate_package_from_plan(
        package_dir, plan_path, verify_artifact_digest=True,
        artifact_digest=normalized_action_digest, metadata_digest=metadata.get("digest"))
    expected_name_for_run = f"lwc-release-notes-{product_version}-development-{run_id}-{run_attempt}"
    if expected_name != expected_name_for_run:
        _fail("uploaded-artifact-name-mismatch")
    plan = _json_object(plan_bytes)
    resume_inputs = _resume_inputs(plan, ready_artifact_id, expected_artifact_id)
    result = {
        "artifact_id": expected_artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": normalized_action_digest,
        "action_digest_raw": action_digest,
        "product_version": product_version,
        "environment": "dev",
        "ready_artifact_id": ready_artifact_id,
        "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
        "resume_inputs": resume_inputs,
        "manifest_sha256": hashlib.sha256((package_dir / "release-notes-manifest.json").read_bytes()).hexdigest(),
        "locale_count": len(manifest["files"]),
        "expires_at": metadata.get("expires_at"),
    }
    _write_github_output(output_path, {
        "product_version": product_version,
        "artifact_id": expected_artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": normalized_action_digest,
        "action_digest_raw": action_digest,
        "resume_inputs": json.dumps(resume_inputs, sort_keys=True, separators=(",", ":")),
    })
    return result


def _manifest_identity(package_dir: Path) -> str:
    files = _package_directory_files(package_dir)
    manifest = _json_object(files["release-notes-manifest.json"])
    version = manifest.get("product_version")
    if (manifest.get("environment") != "dev" or not release_notes.valid_version(version) or
            not isinstance(manifest.get("files"), dict) or
            not 1 <= len(manifest["files"]) <= 2 or set(manifest["files"]) - {"en", "zh-TW"}):
        _fail("package-manifest-invalid")
    expected_files = {"release-notes-manifest.json"} | {f"{locale}.json" for locale in manifest["files"]}
    if set(files) != expected_files:
        _fail("package-files-mismatch")
    return version


def build_dispatch_payload(package_dir: Path, *, ready_artifact_id: str, retention_days: int,
                           request_id: str | None = None) -> tuple[dict, str]:
    if (not ID_RE.fullmatch(ready_artifact_id) or retention_days not in ALLOWED_RETENTION_DAYS):
        _fail("dispatch-input-invalid")
    version = _manifest_identity(package_dir)
    archive = package_directory_archive(package_dir)
    archive_b64 = base64.b64encode(archive).decode("ascii")
    request_id = request_id or uuid.uuid4().hex
    if not REQUEST_ID_RE.fullmatch(request_id):
        _fail("dispatch-request-id-invalid")
    inputs = {
        "artifact_id": ready_artifact_id,
        "prepare_release_notes_package": True,
        "operation": "release",
        "release_notes_action": "stop",
        "package_zip_base64": archive_b64,
        "release_notes_package_retention_days": str(retention_days),
        "release_notes_package_request_id": request_id,
    }
    body = {"ref": "develop", "inputs": inputs}
    encoded_body = json.dumps(body, separators=(",", ":")).encode("utf-8")
    if len(encoded_body) > 65535:
        _fail("dispatch-payload-too-large")
    return body, version


def _gh_api(repository: str, endpoint: str, *, method: str = "GET", body: bytes | None = None):
    command = ["gh", "api", f"repos/{repository}/{endpoint}"]
    if method != "GET":
        command.extend(("--method", method, "--input", "-"))
    try:
        completed = subprocess.run(command, input=body, capture_output=True, check=False)
    except OSError:
        _fail("github-cli-unavailable")
    if completed.returncode != 0:
        _fail("github-api-request-failed")
    if method == "POST":
        return None
    try:
        return json.loads(completed.stdout)
    except (json.JSONDecodeError, UnicodeDecodeError):
        _fail("github-api-response-invalid")


def dispatch_package(repository: str, package_dir: Path, *, ready_artifact_id: str,
                     retention_days: int, timeout_seconds: int = 900) -> dict:
    if not REPOSITORY_RE.fullmatch(repository) or not 30 <= timeout_seconds <= 1800:
        _fail("dispatch-input-invalid")
    request_id = uuid.uuid4().hex
    body, version = build_dispatch_payload(package_dir, ready_artifact_id=ready_artifact_id,
                                           retention_days=retention_days, request_id=request_id)
    body_bytes = json.dumps(body, separators=(",", ":")).encode("utf-8")
    _gh_api(repository, f"actions/workflows/deploy-dev.yml/dispatches",
            method="POST", body=body_bytes)
    title = f"Prepare Release Notes package {request_id}"
    endpoint = "actions/workflows/deploy-dev.yml/runs?branch=develop&event=workflow_dispatch&per_page=100"
    deadline = time.monotonic() + timeout_seconds
    run = None
    while time.monotonic() < deadline:
        response = _gh_api(repository, endpoint)
        runs = response.get("workflow_runs") if isinstance(response, dict) else None
        if not isinstance(runs, list):
            _fail("workflow-run-list-invalid")
        matches = [item for item in runs if isinstance(item, dict) and item.get("display_title") == title and
                   _workflow_path_matches(item.get("path"), WORKFLOW_PATH) and item.get("event") == "workflow_dispatch" and
                   item.get("head_branch") == "develop"]
        if len(matches) > 1:
            _fail("workflow-run-ambiguous")
        if matches:
            run = matches[0]
            if run.get("status") == "completed":
                break
        time.sleep(5)
    if not run:
        _fail("workflow-run-not-found")
    if run.get("status") != "completed":
        _fail("workflow-run-timeout")
    run_id = str(run.get("id", ""))
    run_attempt = str(run.get("run_attempt", ""))
    if (not ID_RE.fullmatch(run_id) or not ID_RE.fullmatch(run_attempt) or
            not _workflow_path_matches(run.get("path"), WORKFLOW_PATH) or run.get("event") != "workflow_dispatch" or
            run.get("head_branch") != "develop" or not isinstance(run.get("head_sha"), str) or
            re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None):
        _fail("workflow-run-identity-invalid")
    run_url = run.get("html_url") if isinstance(run.get("html_url"), str) else ""
    if run.get("conclusion") != "success":
        return {"status": "failed", "workflow_run_id": run_id, "workflow_run_attempt": run_attempt,
                "workflow_run_url": run_url, "conclusion": run.get("conclusion")}
    expected_name = f"lwc-release-notes-{version}-development-{run_id}-{run_attempt}"
    artifact_list = _gh_api(repository, f"actions/runs/{run_id}/artifacts?per_page=100")
    artifacts = artifact_list.get("artifacts") if isinstance(artifact_list, dict) else None
    if not isinstance(artifacts, list):
        _fail("workflow-artifact-list-invalid")
    matches = [item for item in artifacts if isinstance(item, dict) and item.get("name") == expected_name]
    if len(matches) != 1:
        _fail("workflow-artifact-not-unique")
    artifact_id = str(matches[0].get("id", ""))
    metadata = _gh_api(repository, f"actions/artifacts/{artifact_id}")
    digest = metadata.get("digest") if isinstance(metadata, dict) else None
    workflow_run = metadata.get("workflow_run") if isinstance(metadata, dict) else None
    if (not ID_RE.fullmatch(artifact_id) or type(metadata.get("id")) is not int or
            metadata.get("id") != int(artifact_id) or metadata.get("name") != expected_name or
            metadata.get("expired") is not False or not _valid_timestamp(metadata.get("expires_at")) or
            not isinstance(digest, str) or
            ARTIFACT_DIGEST_RE.fullmatch(digest) is None or not isinstance(workflow_run, dict) or
            str(workflow_run.get("id")) != run_id):
        _fail("workflow-artifact-receipt-invalid")
    return {
        "status": "ready",
        "environment": "dev",
        "product_version": version,
        "ready_artifact_id": ready_artifact_id,
        "requested_retention_days": retention_days,
        "workflow_run_id": run_id,
        "workflow_run_attempt": run_attempt,
        "workflow_run_url": run_url,
        "artifact_id": artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": digest,
        "expires_at": metadata.get("expires_at"),
    }


def _argument_parser():
    parser = argparse.ArgumentParser(description="Validate or dispatch a private TPM-authored Release Notes package.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    select = subparsers.add_parser("select-bff")
    select.add_argument("--plan", required=True)
    select.add_argument("--output", default="")
    ready_id = subparsers.add_parser("ready-run-id")
    ready_id.add_argument("--metadata", required=True)
    ready_id.add_argument("--artifact-id", required=True)
    ready = subparsers.add_parser("validate-ready")
    ready.add_argument("--metadata", required=True)
    ready.add_argument("--run", required=True)
    ready.add_argument("--artifacts", required=True)
    ready.add_argument("--result-metadata", required=True)
    ready.add_argument("--ready-directory", required=True)
    ready.add_argument("--result-directory", required=True)
    ready.add_argument("--artifact-id", required=True)
    ready.add_argument("--result-artifact-id", required=True)
    ready.add_argument("--output", default="")
    ready_result = subparsers.add_parser("find-ready-result")
    ready_result.add_argument("--metadata", required=True)
    ready_result.add_argument("--run", required=True)
    ready_result.add_argument("--artifacts", required=True)
    ready_result.add_argument("--artifact-id", required=True)
    ready_result.add_argument("--output", default="")
    prepare = subparsers.add_parser("prepare-package")
    prepare.add_argument("--plan", required=True)
    prepare.add_argument("--package-dir", required=True)
    prepare.add_argument("--environment", choices=("dev",), required=True)
    prepare.add_argument("--request-id", required=True)
    prepare.add_argument("--ready-artifact-id", required=True)
    prepare.add_argument("--retention-days", choices=tuple(str(x) for x in sorted(ALLOWED_RETENTION_DAYS)), required=True)
    prepare.add_argument("--event-path", default="")
    prepare.add_argument("--output", default="")
    validate_event = subparsers.add_parser("validate-package-event")
    validate_event.add_argument("--event-path", required=True)
    validate_event.add_argument("--request-id", required=True)
    validate_event.add_argument("--ready-artifact-id", required=True)
    validate_event.add_argument("--retention-days", choices=tuple(str(x) for x in sorted(ALLOWED_RETENTION_DAYS)), required=True)
    verify = subparsers.add_parser("verify-upload")
    verify.add_argument("--package-dir", required=True)
    verify.add_argument("--plan", required=True)
    verify.add_argument("--metadata", required=True)
    verify.add_argument("--artifact-id", required=True)
    verify.add_argument("--artifact-name", required=True)
    verify.add_argument("--action-digest", required=True)
    verify.add_argument("--run-id", required=True)
    verify.add_argument("--run-attempt", required=True)
    verify.add_argument("--ready-artifact-id", required=True)
    verify.add_argument("--output", default="")
    dispatch = subparsers.add_parser("dispatch")
    dispatch.add_argument("--repository", required=True)
    dispatch.add_argument("--ready-artifact-id", required=True)
    dispatch.add_argument("--package-dir", required=True)
    dispatch.add_argument("--retention-days", choices=("30", "90"), default="90")
    dispatch.add_argument("--timeout-seconds", type=int, default=900)
    return parser


def main(argv=None):
    args = _argument_parser().parse_args(argv)
    try:
        if args.command == "select-bff":
            selected = write_bff_selection(Path(args.plan), args.output)
            result = {"bff_selected": selected}
        elif args.command == "ready-run-id":
            metadata = _read_json(Path(args.metadata))
            if (not ID_RE.fullmatch(args.artifact_id) or type(metadata.get("id")) is not int or
                    metadata.get("id") != int(args.artifact_id) or metadata.get("expired") is not False):
                _fail("ready-artifact-unavailable")
            match = READY_NAME_RE.fullmatch(metadata.get("name", ""))
            workflow_run = metadata.get("workflow_run")
            run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
            if not match or run_id != match.group(1):
                _fail("ready-artifact-name-invalid")
            result = {"run_id": run_id}
            print(run_id)
            return 0
        elif args.command == "validate-ready":
            metadata = _read_json(Path(args.metadata))
            run = _read_json(Path(args.run))
            artifacts = _read_json(Path(args.artifacts))
            result_metadata = _read_json(Path(args.result_metadata))
            result = validate_ready_handoff(
                metadata, run, artifacts, result_metadata,
                expected_artifact_id=args.artifact_id,
                expected_result_artifact_id=args.result_artifact_id,
                ready_directory=Path(args.ready_directory), result_directory=Path(args.result_directory),
                output_path=args.output)
        elif args.command == "find-ready-result":
            result = find_ready_result_artifact(
                _read_json(Path(args.metadata)), _read_json(Path(args.run)),
                _read_json(Path(args.artifacts)), args.artifact_id)
            _write_github_output(args.output, {key: str(value) for key, value in result.items()})
        elif args.command == "prepare-package":
            event_value = args.event_path or os.environ.get("GITHUB_EVENT_PATH", "")
            if not event_value:
                _fail("workflow-event-unavailable")
            result = prepare_package_from_event(Path(event_value), Path(args.plan), Path(args.package_dir),
                                                args.output, args.request_id, args.ready_artifact_id,
                                                args.retention_days)
        elif args.command == "validate-package-event":
            _read_package_input_from_event(Path(args.event_path), args.request_id,
                                           args.ready_artifact_id, args.retention_days)
            result = {"status": "valid"}
        elif args.command == "verify-upload":
            metadata = _read_json(Path(args.metadata))
            result = verify_uploaded_package(
                Path(args.package_dir), Path(args.plan), metadata,
                expected_artifact_id=args.artifact_id, expected_name=args.artifact_name,
                action_digest=args.action_digest, run_id=args.run_id,
                run_attempt=args.run_attempt, ready_artifact_id=args.ready_artifact_id,
                output_path=args.output)
        elif args.command == "dispatch":
            result = dispatch_package(
                args.repository, Path(args.package_dir), ready_artifact_id=args.ready_artifact_id,
                retention_days=int(args.retention_days), timeout_seconds=args.timeout_seconds)
        else:
            _fail("command-invalid")
    except release_notes.ReleaseNotesError as error:
        print(json.dumps({"status": "failed", "stage": error.stage, "safe_class": error.safe_class}, sort_keys=True))
        return 1
    except PackageWorkflowError as error:
        print(json.dumps({"status": "failed", "safe_class": error.safe_class}, sort_keys=True))
        return 1
    except Exception:
        # Package text, private provenance, and command output never enter diagnostics.
        print(json.dumps({"status": "failed", "safe_class": "workflow-input-invalid"}, sort_keys=True))
        return 1
    if args.command != "ready-run-id":
        print(json.dumps(result, sort_keys=True))
    return 1 if isinstance(result, dict) and result.get("status") == "failed" else 0


if __name__ == "__main__":
    raise SystemExit(main())
