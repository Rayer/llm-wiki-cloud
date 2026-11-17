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
READY_WORKFLOW_PATH = WORKFLOW_PATH
DESTINATIONS = {
    "development": {
        "branch": "develop",
        "workflow_path": ".github/workflows/deploy-dev.yml",
        "package_environment": "dev",
    },
    "production": {
        "branch": "main",
        "workflow_path": ".github/workflows/promote-production.yml",
        "package_environment": "prod",
    },
}
MAX_DISPATCH_ZIP_BYTES = 40 * 1024
MAX_DISPATCH_BASE64_CHARS = 4 * ((MAX_DISPATCH_ZIP_BYTES + 2) // 3)
MAX_MANIFEST_BYTES = 32 * 1024
MAX_LOCALE_BYTES = 64 * 1024
MAX_PACKAGE_BYTES = MAX_MANIFEST_BYTES + 2 * MAX_LOCALE_BYTES
MAX_EVENT_BYTES = 72 * 1024
MAX_READY_FILE_BYTES = 16 * 1024 * 1024
MAX_NOTES_EVIDENCE_BYTES = 256 * 1024
PACKAGE_FILES = {"release-notes-manifest.json", "en.json", "zh-TW.json"}
READY_NAME_RE = re.compile(r"^lwc-ready-(development|production)-([1-9][0-9]*)-([1-9][0-9]*)$")
CURRENT_RESULT_NAME_RE = re.compile(r"^lwc-result-(development|production)-([1-9][0-9]*)-([1-9][0-9]*)$")
RESULT_NAME_RE = re.compile(r"^lwc-result-development-([1-9][0-9]*)-([1-9][0-9]*)$")
CURRENT_PACKAGE_NAME_RE = re.compile(
    r"^lwc-release-notes-(?P<version>(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)-"
    r"(?P<environment>development|production)-(?P<run_id>[1-9][0-9]*)-(?P<attempt>[1-9][0-9]*)$")
PACKAGE_NAME_RE = re.compile(
    r"^lwc-release-notes-(?P<version>(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)-development-(?:(?P<history>history)-(?P<operation>release|deploy|rollback|reactivate|promote)-(?P<result_id>[1-9][0-9]*)-)?(?P<run_id>[1-9][0-9]*)-(?P<attempt>[1-9][0-9]*)$")
ARTIFACT_DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
RUNTIME_STEP_NAME = "Execute bounded runtime or explicit recovery"
RUNTIME_JOB_NAME = "release / release"
RUNTIME_COMPLETED_AT_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z$")
ACTION_DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")
ID_RE = re.compile(r"^[1-9][0-9]*$")
REQUEST_ID_RE = re.compile(r"^[0-9a-f]{32}$")
REPOSITORY_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
ALLOWED_RETENTION_DAYS = {30, 90}
DISPATCH_PACKAGE_RETENTION_DAYS = 90
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


def _package_source(ready_artifact_id: str, history_result_artifact_id: str,
                    history_operation: str) -> dict:
    ready_artifact_id = ready_artifact_id or ""
    history_result_artifact_id = history_result_artifact_id or ""
    if bool(ready_artifact_id) == bool(history_result_artifact_id):
        _fail("package-source-ambiguous" if ready_artifact_id else "package-source-required")
    if ready_artifact_id:
        if not ID_RE.fullmatch(ready_artifact_id) or history_operation:
            _fail("package-source-ambiguous")
        return {"kind": "ready", "artifact_id": ready_artifact_id}
    if (not ID_RE.fullmatch(history_result_artifact_id) or
            history_operation not in release_notes.TRANSITION_OPS):
        _fail("history-source-invalid")
    return {"kind": "historical-result", "artifact_id": history_result_artifact_id,
            "operation": history_operation}


def _read_package_input_from_event(event_path: Path, expected_request_id: str,
                                  expected_ready_artifact_id: str,
                                  expected_retention_days: str,
                                  expected_history_result_artifact_id: str = "",
                                  expected_history_operation: str = "",
                                  environment: str = "development") -> str:
    source = _package_source(expected_ready_artifact_id, expected_history_result_artifact_id,
                             expected_history_operation)
    if environment not in DESTINATIONS or (environment == "production" and source["kind"] != "ready"):
        _fail("workflow-event-invalid")
    if (not REQUEST_ID_RE.fullmatch(expected_request_id) or
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
            inputs.get("artifact_id", "") != (expected_ready_artifact_id or "") or
            "release_notes_package_retention_days" in inputs):
        _fail("workflow-event-invalid")
    history_result_artifact_id = inputs.get("release_notes_result_artifact_id", "")
    if history_result_artifact_id != (expected_history_result_artifact_id or ""):
        _fail("workflow-event-invalid")
    event_history_operation = inputs.get("release_notes_operation", "")
    if ((expected_history_result_artifact_id and event_history_operation != expected_history_operation) or
            (not expected_history_result_artifact_id and event_history_operation not in ("", "release"))):
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


def _write_private_json(path: Path, value: dict) -> None:
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(value, stream, sort_keys=True, separators=(",", ":"))
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
    except OSError:
        _fail("history-binding-output-unavailable")


def selected_components_from_plan(plan_bytes: bytes) -> list[str]:
    plan = _json_object(plan_bytes)
    selected = plan.get("selected")
    if (not isinstance(selected, list) or not selected or
            any(not isinstance(item, str) or not item for item in selected) or
            len(selected) != len(set(selected)) or
            selected != [component for component in COMPONENT_ORDER if component in selected]):
        _fail("plan-selection-invalid")
    return selected


def bff_selected_from_plan(plan_bytes: bytes) -> bool:
    return "bff" in selected_components_from_plan(plan_bytes)


def _workflow_path_matches(actual: object, expected: str, branch: str = "develop") -> bool:
    return actual in (expected, f"{expected}@refs/heads/{branch}")


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


def _validate_plan_for_environment(plan_bytes: bytes, environment: str) -> dict:
    plan = _json_object(plan_bytes)
    destination = DESTINATIONS.get(environment)
    if destination is None:
        _fail("release-plan-environment-invalid")
    normalized = plan.get("normalized")
    source = plan.get("source")
    selected = selected_components_from_plan(plan_bytes)
    identities = plan.get("identities")
    if (not isinstance(normalized, dict) or normalized.get("environment") != environment or
            (environment == "production" and plan.get("branch") != destination["branch"]) or
            not isinstance(source, str) or re.fullmatch(r"[0-9a-f]{40}", source) is None or
            not isinstance(plan.get("id"), str) or not plan.get("id") or
            not isinstance(plan.get("tag"), str) or not plan.get("tag") or
            not isinstance(identities, dict) or
            set(identities) != set(selected) or
            any(not isinstance(identities.get(component), dict) for component in selected) or
            (plan.get("schema") == 4 and not release_notes.valid_version(plan.get("release_version")))):
        _fail("release-plan-not-bff-dev")
    return plan


def _validate_development_plan(plan_bytes: bytes) -> dict:
    return _validate_plan_for_environment(plan_bytes, "development")


def _validate_ready_run(metadata: dict, run: dict, expected_artifact_id: str,
                        environment: str = "development") -> tuple[str, str, str]:
    if not ID_RE.fullmatch(expected_artifact_id):
        _fail("ready-artifact-id-invalid")
    destination = DESTINATIONS.get(environment)
    if destination is None:
        _fail("ready-artifact-not-qualified")
    name = metadata.get("name")
    match = READY_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    workflow_run = metadata.get("workflow_run")
    run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    run_attempt = match.group(3) if match else ""
    head_sha = run.get("head_sha")
    if (not isinstance(workflow_run, dict) or
            type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or not match or match.group(1) != environment or
            run_id != match.group(2) or
            type(run.get("id")) is not int or run.get("id") != int(run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") != int(run_attempt) or
            run.get("event") != "workflow_dispatch" or
            not _workflow_path_matches(run.get("path"), destination["workflow_path"], destination["branch"]) or
            run.get("status") != "completed" or run.get("conclusion") != "failure" or
            run.get("head_branch") != destination["branch"] or
            not isinstance(head_sha, str) or re.fullmatch(r"[0-9a-f]{40}", head_sha) is None or
            workflow_run.get("head_branch") != destination["branch"] or
            workflow_run.get("head_sha") != head_sha or
            not isinstance(metadata.get("digest"), str) or ARTIFACT_DIGEST_RE.fullmatch(metadata["digest"]) is None or
            not _valid_timestamp(metadata.get("expires_at"))):
        _fail("ready-artifact-not-qualified")
    return run_id, run_attempt, name


def _validate_history_result_metadata(metadata: dict, run: dict,
                                      expected_artifact_id: str) -> tuple[str, str, str, str]:
    if (not isinstance(metadata, dict) or not isinstance(run, dict) or
            not ID_RE.fullmatch(expected_artifact_id)):
        _fail("history-result-artifact-id-invalid")
    name = metadata.get("name")
    match = RESULT_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    workflow_run = metadata.get("workflow_run")
    run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    run_attempt = match.group(2) if match else ""
    head_sha = run.get("head_sha")
    if (not isinstance(workflow_run, dict) or
            type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or not match or run_id != match.group(1) or
            type(run.get("id")) is not int or run.get("id") != int(run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") != int(run_attempt) or
            run.get("event") != "workflow_dispatch" or
            not _workflow_path_matches(run.get("path"), WORKFLOW_PATH) or
            run.get("status") != "completed" or run.get("conclusion") != "success" or
            run.get("head_branch") != "develop" or
            not isinstance(head_sha, str) or re.fullmatch(r"[0-9a-f]{40}", head_sha) is None or
            not isinstance(metadata.get("digest"), str) or
            ARTIFACT_DIGEST_RE.fullmatch(metadata["digest"]) is None or
            not _valid_timestamp(metadata.get("expires_at")) or
            workflow_run.get("head_branch") != run.get("head_branch") or
            workflow_run.get("head_sha") != head_sha):
        _fail("history-result-artifact-not-qualified")
    return run_id, run_attempt, name, head_sha


def history_result_run_id(metadata: dict, expected_artifact_id: str,
                          output_path: str | None = None) -> dict:
    if not ID_RE.fullmatch(expected_artifact_id):
        _fail("history-result-artifact-id-invalid")
    name = metadata.get("name")
    match = RESULT_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    workflow_run = metadata.get("workflow_run")
    run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    if (type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or not match or run_id != match.group(1) or
            not isinstance(metadata.get("digest"), str) or
            ARTIFACT_DIGEST_RE.fullmatch(metadata["digest"]) is None or
            not _valid_timestamp(metadata.get("expires_at"))):
        _fail("history-result-artifact-not-qualified")
    result = {"run_id": run_id, "run_attempt": match.group(2), "artifact_name": name}
    if output_path:
        _write_github_output(output_path, result)
    return result


def validate_history_result_handoff(metadata: dict, run: dict, result_directory: Path, *,
                                    expected_artifact_id: str, operation: str,
                                    output_path: str | None = None,
                                    binding_path: Path | None = None) -> dict:
    """Bind a genuine successful DEV result artifact to its native operation bytes."""
    if operation not in release_notes.TRANSITION_OPS:
        _fail("history-operation-invalid")
    run_id, run_attempt, name, head_sha = _validate_history_result_metadata(
        metadata, run, expected_artifact_id)
    plan_bytes = _read_artifact_file(result_directory, "plan.json", MAX_READY_FILE_BYTES)
    state_bytes = _read_artifact_file(result_directory, "state.json", MAX_READY_FILE_BYTES)
    result_bytes = _read_artifact_file(result_directory, "result.json", MAX_READY_FILE_BYTES)
    receipt_bytes = _read_artifact_file(result_directory, "receipts/bff.json", MAX_READY_FILE_BYTES)
    sidecar_path = result_directory / "release-notes-operation.json"
    if sidecar_path.is_symlink() or sidecar_path.exists():
        _fail("history-operation-sidecar-present")

    plan = _json_object(plan_bytes)
    _validate_development_plan(plan_bytes)
    if (plan.get("branch") != "develop" or
            not isinstance(plan.get("id"), str) or re.fullmatch(r"[0-9a-f]{64}", plan["id"]) is None or
            plan.get("id") != _plan_id(plan)):
        _fail("history-plan-invalid")
    if plan.get("schema") in (3, 4):
        producer_sha = plan.get("executor_sha")
    elif plan.get("schema") == 2:
        producer_sha = plan.get("source")
    else:
        producer_sha = None
    if producer_sha != head_sha:
        _fail("history-result-producer-mismatch")

    native = release_notes.validate_native_operation(
        plan_bytes, state_bytes, result_bytes, receipt_bytes,
        environment="dev", workflow_operation=operation,
        run_id=run_id, run_attempt=int(run_attempt))
    binding = {
        "source_kind": "historical-result",
        "result_artifact_id": expected_artifact_id,
        "result_artifact_name": name,
        "operation": operation,
        "run_id": run_id,
        "run_attempt": run_attempt,
        "head_sha": head_sha,
        "source_sha": native["plan"]["source"],
        "plan_id": native["plan"]["id"],
        "plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
        "state_sha256": hashlib.sha256(state_bytes).hexdigest(),
        "result_sha256": hashlib.sha256(result_bytes).hexdigest(),
        "bff_receipt_sha256": hashlib.sha256(receipt_bytes).hexdigest(),
    }
    if output_path:
        _write_github_output(output_path, {key: str(value) for key, value in binding.items()})
    if binding_path:
        _write_private_json(binding_path, binding)
    return binding


def validate_resend_package_artifact(metadata: dict, run: dict, *, expected_artifact_id: str,
                                     result_artifact_id: str, operation: str,
                                     environment: str = "development",
                                     repository: str | None = None,
                                     output_path: str | None = None) -> dict:
    """Validate a private package artifact's run identity and optional history binding."""
    if (not isinstance(metadata, dict) or not isinstance(run, dict) or
            not ID_RE.fullmatch(expected_artifact_id) or not ID_RE.fullmatch(result_artifact_id) or
            operation not in release_notes.TRANSITION_OPS or environment not in DESTINATIONS):
        _fail("package-artifact-input-invalid")
    workflow_run = metadata.get("workflow_run")
    name = metadata.get("name")
    current_match = CURRENT_PACKAGE_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    history_match = PACKAGE_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    historical = bool(history_match and history_match.group("history"))
    if current_match and current_match.group("environment") == environment:
        match = current_match
        run_id, run_attempt = match.group("run_id"), match.group("attempt")
        package_environment = environment
        package_branch = DESTINATIONS[environment]["branch"]
        package_workflow = DESTINATIONS[environment]["workflow_path"]
    elif historical and environment == "development":
        match = history_match
        run_id, run_attempt = match.group("run_id"), match.group("attempt")
        package_environment = "development"
        package_branch = DESTINATIONS[package_environment]["branch"]
        package_workflow = DESTINATIONS[package_environment]["workflow_path"]
    else:
        _fail("package-artifact-not-qualified")
    if package_environment == "production" and operation != "release":
        _fail("package-artifact-not-qualified")
    run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    if (not isinstance(workflow_run, dict) or
            type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or
            not ID_RE.fullmatch(run_id) or run_id != match.group("run_id") or
            type(run.get("id")) is not int or run.get("id") != int(run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") != int(run_attempt) or
            run.get("event") != "workflow_dispatch" or
            not _workflow_path_matches(run.get("path"), package_workflow, package_branch) or
            run.get("status") != "completed" or run.get("conclusion") != "success" or
            run.get("head_branch") != package_branch or
            not isinstance(run.get("head_sha"), str) or re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None or
            not isinstance(metadata.get("digest"), str) or
            ARTIFACT_DIGEST_RE.fullmatch(metadata["digest"]) is None or
            not _valid_timestamp(metadata.get("expires_at")) or
            workflow_run.get("head_branch") != package_branch or
            workflow_run.get("head_sha") != run.get("head_sha") or
            (repository is not None and
             (not REPOSITORY_RE.fullmatch(repository) or
              not isinstance(run.get("repository"), dict) or
              run["repository"].get("full_name") != repository))):
        _fail("package-artifact-not-qualified")
    if historical and (match.group("operation") != operation or match.group("result_id") != result_artifact_id):
        _fail("historical-artifact-binding-mismatch")
    result = {
        "artifact_id": expected_artifact_id,
        "artifact_name": name,
        "artifact_digest": metadata["digest"],
        "expires_at": metadata["expires_at"],
        "package_run_id": run_id,
        "package_run_attempt": match.group("attempt"),
        "head_sha": run["head_sha"],
        "historical": historical,
        "history_result_artifact_id": match.group("result_id") if historical else None,
        "history_operation": match.group("operation") if historical else None,
    }
    if output_path:
        outputs = {key: "" if value is None else str(value) for key, value in result.items()}
        outputs.update({"run_id": run_id, "run_attempt": match.group("attempt"),
                        "digest": metadata["digest"]})
        _write_github_output(output_path, outputs)
    return result


def validate_original_runtime_completion(result_metadata: dict, run: dict, jobs_response: dict,
                                         package_metadata: dict, package_run: dict, *,
                                         result_directory: Path, package_directory: Path,
                                         expected_result_artifact_id: str, expected_package_artifact_id: str,
                                         environment: str, operation: str, repository: str,
                                         output_path: str | None = None) -> dict:
    """Bind recovery time to the successful runtime step of the exact retained production release."""
    if (environment != "production" or operation != "release" or
            not ID_RE.fullmatch(expected_result_artifact_id) or
            not ID_RE.fullmatch(expected_package_artifact_id) or
            not REPOSITORY_RE.fullmatch(repository)):
        _fail("runtime-completion-scope-invalid")
    if not all(isinstance(value, dict) for value in
               (result_metadata, run, jobs_response, package_metadata, package_run)):
        _fail("runtime-completion-metadata-invalid")

    result_name = result_metadata.get("name")
    result_match = CURRENT_RESULT_NAME_RE.fullmatch(result_name) if isinstance(result_name, str) else None
    result_workflow_run = result_metadata.get("workflow_run")
    run_id = str(result_workflow_run.get("id")) if isinstance(result_workflow_run, dict) else ""
    attempt = result_match.group(3) if result_match else ""
    destination = DESTINATIONS[environment]
    if (type(result_metadata.get("id")) is not int or
            result_metadata["id"] != int(expected_result_artifact_id) or
            result_metadata.get("expired") is not False or not result_match or
            not isinstance(result_metadata.get("digest"), str) or
            ARTIFACT_DIGEST_RE.fullmatch(result_metadata["digest"]) is None or
            not _valid_timestamp(result_metadata.get("expires_at")) or
            result_match.group(1) != environment or not ID_RE.fullmatch(run_id) or
            run_id != result_match.group(2) or
            not isinstance(result_workflow_run, dict) or
            type(run.get("id")) is not int or run.get("id") != int(run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") < int(attempt) or
            run.get("event") != "workflow_dispatch" or run.get("status") != "completed" or
            run.get("conclusion") not in ("success", "failure") or
            not _workflow_path_matches(run.get("path"), ".github/workflows/promote-production.yml",
                                       destination["branch"]) or
            run.get("head_branch") != destination["branch"] or
            not isinstance(run.get("head_sha"), str) or re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None or
            not isinstance(run.get("repository"), dict) or run["repository"].get("full_name") != repository or
            result_workflow_run.get("id") != run.get("id") or
            result_workflow_run.get("head_branch") != run.get("head_branch") or
            result_workflow_run.get("head_sha") != run.get("head_sha")):
        _fail("runtime-completion-result-identity-invalid")

    jobs = jobs_response.get("jobs")
    if (not isinstance(jobs, list) or type(jobs_response.get("total_count")) is not int or
            jobs_response["total_count"] != len(jobs)):
        _fail("runtime-completion-jobs-invalid")
    release_jobs = [job for job in jobs if isinstance(job, dict) and job.get("name") == RUNTIME_JOB_NAME]
    if len(release_jobs) != 1:
        _fail("runtime-completion-release-job-invalid")
    release_job = release_jobs[0]
    if (type(release_job.get("id")) is not int or release_job["id"] < 1 or
            release_job.get("run_id") != int(run_id) or
            release_job.get("run_attempt") != int(attempt) or
            release_job.get("status") != "completed" or
            release_job.get("conclusion") not in ("success", "failure")):
        _fail("runtime-completion-release-job-invalid")
    steps = release_job.get("steps")
    matching_steps = [step for step in steps if isinstance(step, dict) and
                      step.get("name") == RUNTIME_STEP_NAME] if isinstance(steps, list) else []
    if len(matching_steps) != 1:
        _fail("runtime-completion-step-invalid")
    runtime_step = matching_steps[0]
    completed_at = runtime_step.get("completed_at")
    if (runtime_step.get("status") != "completed" or runtime_step.get("conclusion") != "success" or
            not isinstance(completed_at, str) or not RUNTIME_COMPLETED_AT_RE.fullmatch(completed_at) or
            not _valid_timestamp(completed_at)):
        _fail("runtime-completion-step-invalid")

    result_directory = Path(result_directory)
    plan_bytes = _read_artifact_file(result_directory, "plan.json", MAX_READY_FILE_BYTES)
    state_bytes = _read_artifact_file(result_directory, "state.json", MAX_READY_FILE_BYTES)
    result_bytes = _read_artifact_file(result_directory, "result.json", MAX_READY_FILE_BYTES)
    plan = _json_object(plan_bytes)
    _validate_plan_for_environment(plan_bytes, environment)
    if (plan.get("branch") != destination["branch"] or plan.get("executor_sha") != run.get("head_sha") or
            type(plan.get("release_version")) is not str or
            not isinstance(plan.get("selected"), list)):
        _fail("runtime-completion-plan-identity-invalid")
    state = _json_object(state_bytes)
    if state.get("executor_sha") != plan.get("executor_sha") or state.get("plan") != plan.get("id"):
        _fail("runtime-completion-state-identity-invalid")
    selected = selected_components_from_plan(plan_bytes)
    receipt_bytes = (_read_artifact_file(result_directory, "receipts/bff.json", MAX_READY_FILE_BYTES)
                     if "bff" in selected else None)
    component_receipts = {
        component: _read_artifact_file(result_directory, f"receipts/{component}.json", MAX_READY_FILE_BYTES)
        for component in ("frontend", "worker") if component in selected
    }
    try:
        release_notes.validate_native_operation(
            plan_bytes, state_bytes, result_bytes, receipt_bytes,
            environment="prod", workflow_operation=operation, run_id=run_id,
            run_attempt=int(attempt), component_receipts=component_receipts)
    except release_notes.ReleaseNotesError:
        _fail("runtime-completion-operation-invalid")

    package_binding = validate_resend_package_artifact(
        package_metadata, package_run, expected_artifact_id=expected_package_artifact_id,
        result_artifact_id=expected_result_artifact_id, operation=operation,
        environment=environment, repository=repository)
    package_name = package_binding["artifact_name"]
    package_name_match = CURRENT_PACKAGE_NAME_RE.fullmatch(package_name)
    if (package_binding["historical"] or not package_name_match or
            package_name_match.group("version") != plan["release_version"]):
        _fail("runtime-completion-package-identity-invalid")
    try:
        package_version, package_files, package_manifest = release_notes.validate_package(
            package_directory, expected_environment="prod",
            expected_artifact_digest=package_binding["artifact_digest"],
            artifact_metadata_digest=package_binding["artifact_digest"], plan_bytes=plan_bytes)
    except release_notes.ReleaseNotesError:
        _fail("runtime-completion-package-invalid")
    if package_version != plan["release_version"] or package_manifest.get("product_version") != package_version:
        _fail("runtime-completion-package-identity-invalid")

    for sidecar_name in ("release-notes-publication.json", "release-notes-operation.json"):
        sidecar_path = result_directory / sidecar_name
        if sidecar_path.is_symlink() or sidecar_path.exists():
            _fail("runtime-completion-sidecar-state-invalid")
    failure_bytes = _read_artifact_file(result_directory, "release-notes-evidence.json", MAX_NOTES_EVIDENCE_BYTES)
    failure_evidence = _json_object(failure_bytes)
    stages = failure_evidence.get("stages") if isinstance(failure_evidence, dict) else None
    if (not isinstance(stages, list) or failure_evidence.get("schema_version") != 1 or
            any(not isinstance(stage, dict) for stage in stages)):
        _fail("runtime-completion-release-failure-invalid")
    plan_sha256 = hashlib.sha256(plan_bytes).hexdigest()
    package_files_sha256 = {locale: hashlib.sha256(data).hexdigest()
                            for locale, data in sorted(package_files.items())}
    prepares = [stage for stage in stages if
                stage.get("action") == "prepare" and stage.get("environment") == "prod" and
                stage.get("status") == "verified" and stage.get("stage") == "content_upload" and
                stage.get("product_status") == "not_started" and stage.get("plan_sha256") == plan_sha256 and
                stage.get("artifact_id") == expected_package_artifact_id and
                stage.get("artifact_digest") == package_binding["artifact_digest"] and
                stage.get("content_sha256") == package_files_sha256]
    publication_failures = [stage for stage in stages if stage.get("action") == "record-publication"]
    if (not prepares or not publication_failures or stages[-1] is not publication_failures[-1] or
            publication_failures[-1].get("environment") != "prod" or
            publication_failures[-1].get("status") != "failed" or
            publication_failures[-1].get("stage") != "index_update" or
            publication_failures[-1].get("safe_class") != "selected-component-receipt-invalid" or
            publication_failures[-1].get("product_status") != "released" or
            publication_failures[-1].get("plan_sha256") != plan_sha256 or
            publication_failures[-1].get("artifact_id") is not None or
            publication_failures[-1].get("artifact_digest") is not None or
            publication_failures[-1].get("result_artifact_id") is not None or
            publication_failures[-1].get("owner_choice_required") is not True or
            publication_failures[-1].get("automatic_skip") is not False):
        _fail("runtime-completion-release-failure-invalid")

    selected_receipts = {}
    if receipt_bytes is not None:
        selected_receipts["bff"] = hashlib.sha256(receipt_bytes).hexdigest()
    selected_receipts.update({component: hashlib.sha256(data).hexdigest()
                              for component, data in component_receipts.items()})
    canonical_digest = lambda value: hashlib.sha256(json.dumps(
        value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    evidence = {
        "schema_version": 1, "source": "github-actions-run-attempt-jobs", "repository": repository,
        "workflow_path": ".github/workflows/promote-production.yml",
        "workflow_ref": f"refs/heads/{destination['branch']}", "workflow_sha": run["head_sha"],
        "environment": environment, "operation": operation, "run_id": run_id,
        "run_attempt": int(attempt), "result_artifact_id": expected_result_artifact_id,
        "result_artifact_name": result_name, "result_artifact_digest": result_metadata["digest"],
        "package_artifact_id": expected_package_artifact_id,
        "package_artifact_name": package_name, "package_artifact_digest": package_binding["artifact_digest"],
        "package_run_id": package_binding["package_run_id"],
        "package_run_attempt": package_binding["package_run_attempt"],
        "package_manifest_sha256": hashlib.sha256(
            (Path(package_directory) / "release-notes-manifest.json").read_bytes()).hexdigest(),
        "package_files_sha256": {locale: hashlib.sha256(data).hexdigest()
                                 for locale, data in sorted(package_files.items())},
        "plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
        "state_sha256": hashlib.sha256(state_bytes).hexdigest(),
        "result_sha256": hashlib.sha256(result_bytes).hexdigest(), "receipts_sha256": selected_receipts,
        "job_id": str(release_job["id"]), "job_name": release_job["name"],
        "job_conclusion": release_job["conclusion"], "runtime_step_name": RUNTIME_STEP_NAME,
        "runtime_step_conclusion": runtime_step["conclusion"], "runtime_step_completed_at": completed_at,
        "run_metadata_sha256": canonical_digest(run), "jobs_metadata_sha256": canonical_digest(jobs_response),
        "result_metadata_sha256": canonical_digest(result_metadata),
        "package_metadata_sha256": canonical_digest(package_metadata),
        "package_run_metadata_sha256": canonical_digest(package_run),
    }
    if output_path:
        _write_private_json(Path(output_path), evidence)
    return evidence


def find_ready_result_artifact(metadata: dict, run: dict, artifact_list: dict,
                               expected_artifact_id: str,
                               environment: str = "development") -> dict:
    run_id, run_attempt, ready_name = _validate_ready_run(
        metadata, run, expected_artifact_id, environment)
    artifacts = artifact_list.get("artifacts") if isinstance(artifact_list, dict) else None
    result_name = f"lwc-result-{environment}-{run_id}-{run_attempt}"
    result_match = CURRENT_RESULT_NAME_RE.fullmatch(result_name)
    if (not result_match or result_match.group(1) != environment or
            result_match.group(2) != run_id or result_match.group(3) != run_attempt):
        _fail("ready-result-artifact-invalid")
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
            not isinstance(result_run, dict) or str(result_run.get("id")) != run_id or
            result_run.get("head_branch") != run.get("head_branch") or
            result_run.get("head_sha") != run.get("head_sha")):
        _fail("ready-result-artifact-invalid")
    return {"ready_artifact_id": expected_artifact_id, "ready_artifact_name": ready_name,
            "environment": environment, "run_id": run_id, "run_attempt": run_attempt,
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
    elif schema == 4:
        fields = ("source", "branch", "tag", "normalized", "identities", "dev_reference", "selected", "release_version")
        if (any(field not in plan for field in fields) or
                not release_notes.valid_version(plan.get("release_version"))):
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


def _resume_inputs(plan: dict, ready_artifact_id: str, notes_artifact_id: str,
                   environment: str = "development") -> dict:
    destination = DESTINATIONS.get(environment)
    if destination is None:
        _fail("ready-plan-invalid")
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
        "release_tag": plan["tag"],
        "release_version": str(plan.get("release_version") or ""),
        "components": ",".join(plan["selected"]),
        "artifact_id": ready_artifact_id,
        "release_source_mode": source_mode,
        "auth_schema_version": str(schema_version),
        "dev_artifact_id": str(plan.get("dev_reference") or ""),
    }
    if notes_artifact_id:
        inputs["release_notes_artifact_id"] = notes_artifact_id
        inputs["release_notes_action"] = "run"
    if environment == "development":
        inputs["source_sha"] = plan["source"]
    auth_source = plan.get("auth_source") if environment == "development" else None
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
                           result_directory: Path, output_path: str | None,
                           environment: str = "development") -> dict:
    destination = DESTINATIONS.get(environment)
    if destination is None:
        _fail("ready-artifact-not-qualified")
    discovered = find_ready_result_artifact(
        metadata, run, artifact_list, expected_artifact_id, environment)
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
            str(result_workflow_run.get("id")) != discovered["run_id"] or
            result_workflow_run.get("head_branch") != destination["branch"] or
            result_workflow_run.get("head_sha") != run.get("head_sha")):
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
    _validate_plan_for_environment(plan_bytes, environment)
    if (not isinstance(plan.get("id"), str) or re.fullmatch(r"[0-9a-f]{64}", plan["id"]) is None or
            plan.get("id") != _plan_id(plan)):
        _fail("ready-plan-invalid")
    run_sha = run.get("head_sha")
    if plan.get("schema") in (3, 4) and plan.get("executor_sha") != run_sha:
        _fail("ready-plan-producer-mismatch")
    if environment == "production" and (
            plan.get("source") != run_sha or
            (plan.get("schema") == 2 and plan.get("source") != run_sha)):
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
            missing_package.get("action") != "prepare" or
            missing_package.get("environment") != destination["package_environment"] or
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

    resume_inputs = _resume_inputs(plan, expected_artifact_id, "", environment)
    result = {**discovered, "plan_id": plan["id"], "plan_sha256": plan_sha256,
              "release_tag": plan["tag"],
              "components": ",".join(plan["selected"]), "resume_inputs": resume_inputs}
    if environment == "development":
        result["source_sha"] = plan["source"]
    output = {key: str(value) for key, value in result.items()
              if key != "resume_inputs"}
    for key, value in resume_inputs.items():
        output[f"resume_{key}"] = str(value)
    _write_github_output(output_path, output)
    return result


def _validate_package_from_plan(package_dir: Path, plan_path: Path, *,
                                environment: str = "development",
                                verify_artifact_digest: bool = False,
                                artifact_digest: str | None = None,
                                metadata_digest: str | None = None) -> tuple[str, dict, bytes]:
    try:
        if plan_path.is_symlink() or not plan_path.is_file():
            _fail("plan-unavailable")
        plan_bytes = plan_path.read_bytes()
    except OSError:
        _fail("plan-unavailable")
    _validate_plan_for_environment(plan_bytes, environment)
    destination = DESTINATIONS[environment]
    product_version, files, manifest = release_notes.validate_package(
        package_dir, expected_environment=destination["package_environment"],
        expected_artifact_digest=artifact_digest if verify_artifact_digest else None,
        artifact_metadata_digest=metadata_digest if verify_artifact_digest else None,
        plan_bytes=plan_bytes)
    plan = _json_object(plan_bytes)
    if plan.get("schema") == 4 and product_version != plan.get("release_version"):
        _fail("package-release-version-mismatch")
    return product_version, manifest, plan_bytes


def prepare_package(archive_b64: str, plan_path: Path, destination: Path, output_path: str | None,
                    request_id: str, source: dict | None = None,
                    environment: str = "development") -> dict:
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
    product_version, manifest, plan_bytes = _validate_package_from_plan(
        destination, plan_path, environment=environment)
    if source is not None:
        has_historical_fields = release_notes.HISTORICAL_MANIFEST_FIELDS <= set(manifest)
        if source.get("kind") == "historical-result" and not has_historical_fields:
            _fail("historical-provenance-required")
        if source.get("kind") == "ready" and has_historical_fields:
            _fail("historical-source-required")
    result = {"product_version": product_version,
              "manifest_sha256": hashlib.sha256((destination / "release-notes-manifest.json").read_bytes()).hexdigest(),
              "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
              "locale_count": len(manifest["files"]),
              "source_kind": source["kind"] if source else "ready"}
    outputs = {"product_version": product_version}
    if result["source_kind"] == "historical-result":
        outputs["source_kind"] = result["source_kind"]
    _write_github_output(output_path, outputs)
    return result


def prepare_package_from_event(event_path: Path, plan_path: Path, destination: Path,
                               output_path: str | None, request_id: str,
                               ready_artifact_id: str, retention_days: str,
                               history_result_artifact_id: str = "",
                               history_operation: str = "",
                               environment: str = "development") -> dict:
    archive_b64 = _read_package_input_from_event(
        event_path, request_id, ready_artifact_id, retention_days,
        history_result_artifact_id, history_operation, environment)
    source = _package_source(ready_artifact_id, history_result_artifact_id, history_operation)
    return prepare_package(archive_b64, plan_path, destination, output_path, request_id,
                           source=source, environment=environment)


def normalize_action_digest(action_digest: str) -> str:
    if not isinstance(action_digest, str) or ACTION_DIGEST_RE.fullmatch(action_digest) is None:
        _fail("uploaded-artifact-action-digest-invalid")
    return "sha256:" + action_digest


def _validate_package_run(metadata: dict, run: dict, *, expected_artifact_id: str,
                          environment: str, expected_name: str | None = None,
                          expected_digest: str | None = None,
                          run_id: str | None = None, run_attempt: str | None = None,
                          completed: bool) -> dict:
    destination = DESTINATIONS.get(environment)
    if destination is None or not isinstance(metadata, dict) or not isinstance(run, dict):
        _fail("package-artifact-not-qualified")
    name = metadata.get("name")
    current_match = CURRENT_PACKAGE_NAME_RE.fullmatch(name) if isinstance(name, str) else None
    if current_match:
        if current_match.group("environment") != environment:
            _fail("package-artifact-not-qualified")
        artifact_run_id = current_match.group("run_id")
        artifact_attempt = current_match.group("attempt")
        version = current_match.group("version")
        historical = False
    else:
        history_match = PACKAGE_NAME_RE.fullmatch(name) if isinstance(name, str) else None
        if environment != "development" or not history_match:
            _fail("package-artifact-not-qualified")
        artifact_run_id = history_match.group("run_id")
        artifact_attempt = history_match.group("attempt")
        version = history_match.group("version")
        historical = history_match.group("history") == "history"
    workflow_run = metadata.get("workflow_run")
    resolved_run_id = str(workflow_run.get("id")) if isinstance(workflow_run, dict) else ""
    digest = metadata.get("digest")
    run_status_ok = (run.get("status") == "completed" and run.get("conclusion") == "success") \
        if completed else run.get("status") == "in_progress"
    if (not ID_RE.fullmatch(expected_artifact_id) or
            type(metadata.get("id")) is not int or metadata.get("id") != int(expected_artifact_id) or
            metadata.get("expired") is not False or
            not isinstance(digest, str) or ARTIFACT_DIGEST_RE.fullmatch(digest) is None or
            not _valid_timestamp(metadata.get("expires_at")) or
            (expected_digest is not None and digest != expected_digest) or
            (expected_name is not None and name != expected_name) or
            not ID_RE.fullmatch(resolved_run_id) or resolved_run_id != artifact_run_id or
            type(run.get("id")) is not int or run.get("id") != int(resolved_run_id) or
            type(run.get("run_attempt")) is not int or run.get("run_attempt") != int(artifact_attempt) or
            (run_id is not None and resolved_run_id != run_id) or
            (run_attempt is not None and artifact_attempt != run_attempt) or
            run.get("event") != "workflow_dispatch" or
            not _workflow_path_matches(run.get("path"), destination["workflow_path"], destination["branch"]) or
            run.get("head_branch") != destination["branch"] or
            not isinstance(run.get("head_sha"), str) or re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None or
            not run_status_ok or
            not isinstance(workflow_run, dict) or
            workflow_run.get("head_branch") != destination["branch"] or
            workflow_run.get("head_sha") != run.get("head_sha") or
            (type(workflow_run.get("run_attempt")) is int and
             workflow_run.get("run_attempt") != int(artifact_attempt))):
        _fail("package-artifact-not-qualified")
    return {
        "artifact_id": expected_artifact_id,
        "artifact_name": name,
        "artifact_digest": digest,
        "expires_at": metadata["expires_at"],
        "package_run_id": resolved_run_id,
        "package_run_attempt": artifact_attempt,
        "head_sha": run["head_sha"],
        "environment": destination["package_environment"],
        "historical": historical,
        "product_version": version,
    }


def validate_current_package_artifact(metadata: dict, run: dict, *,
                                      expected_artifact_id: str, environment: str,
                                      output_path: str | None = None) -> dict:
    """Require a successful private package from its matching registered wrapper and ref."""
    receipt = _validate_package_run(metadata, run, expected_artifact_id=expected_artifact_id,
                                    environment=environment, completed=True)
    if receipt["historical"]:
        _fail("package-artifact-not-qualified")
    outputs = {key: str(value) for key, value in receipt.items()
               if value is not None and key not in ("historical", "product_version", "environment")}
    outputs.update({"run_id": receipt["package_run_id"],
                    "run_attempt": receipt["package_run_attempt"],
                    "digest": receipt["artifact_digest"]})
    if output_path:
        _write_github_output(output_path, outputs)
    return receipt


def resolve_caller_destination(repository: str, workflow_ref: str, ref: str,
                               output_path: str | None = None) -> dict:
    """Resolve only the authenticated registered DEV/develop or Production/main caller."""
    if not REPOSITORY_RE.fullmatch(repository) or not isinstance(workflow_ref, str):
        _fail("package-caller-invalid")
    for environment, destination in DESTINATIONS.items():
        expected_workflow_ref = (
            f"{repository}/{destination['workflow_path']}@refs/heads/{destination['branch']}"
        )
        expected_ref = f"refs/heads/{destination['branch']}"
        if workflow_ref.casefold() == expected_workflow_ref.casefold() and ref == expected_ref:
            result = {"environment": environment, "package_environment": destination["package_environment"],
                      "branch": destination["branch"], "workflow_path": destination["workflow_path"]}
            if output_path:
                _write_github_output(output_path, result)
            return result
    _fail("package-caller-invalid")


def _validate_history_binding(binding: dict, *, result_artifact_id: str,
                              operation: str, plan_bytes: bytes) -> dict:
    fields = {
        "source_kind", "result_artifact_id", "result_artifact_name", "operation",
        "run_id", "run_attempt", "head_sha", "source_sha", "plan_id", "plan_sha256",
        "state_sha256", "result_sha256", "bff_receipt_sha256",
    }
    plan = _json_object(plan_bytes)
    result_name = binding.get("result_artifact_name") if isinstance(binding, dict) else None
    result_match = RESULT_NAME_RE.fullmatch(result_name) if isinstance(result_name, str) else None
    if (not isinstance(binding, dict) or set(binding) != fields or
            binding.get("source_kind") != "historical-result" or
            binding.get("result_artifact_id") != result_artifact_id or
            binding.get("operation") != operation or not result_match or
            not isinstance(binding.get("run_id"), str) or
            not ID_RE.fullmatch(binding["run_id"]) or
            not isinstance(binding.get("run_attempt"), str) or
            not ID_RE.fullmatch(binding["run_attempt"]) or
            binding["run_id"] != result_match.group(1) or
            binding["run_attempt"] != result_match.group(2) or
            not isinstance(binding.get("head_sha"), str) or
            re.fullmatch(r"[0-9a-f]{40}", binding["head_sha"]) is None or
            not isinstance(binding.get("source_sha"), str) or
            re.fullmatch(r"[0-9a-f]{40}", binding["source_sha"]) is None or
            not isinstance(binding.get("plan_id"), str) or
            re.fullmatch(r"[0-9a-f]{64}", binding["plan_id"]) is None or
            any(not isinstance(binding.get(field), str) or
                re.fullmatch(r"[0-9a-f]{64}", binding[field]) is None
                for field in ("plan_sha256", "state_sha256", "result_sha256", "bff_receipt_sha256")) or
            binding["plan_sha256"] != hashlib.sha256(plan_bytes).hexdigest() or
            binding["plan_id"] != plan.get("id") or binding["source_sha"] != plan.get("source") or
            plan.get("branch") != "develop" or
            (plan.get("schema") in (3, 4) and plan.get("executor_sha") != binding["head_sha"]) or
            (plan.get("schema") == 2 and plan.get("source") != binding["head_sha"]) or
            plan.get("schema") not in (2, 3, 4)):
        _fail("history-result-binding-mismatch")
    return binding


def verify_uploaded_package(package_dir: Path, plan_path: Path, metadata: dict, *,
                            expected_artifact_id: str, expected_name: str, action_digest: str,
                            run: dict, run_id: str, run_attempt: str, ready_artifact_id: str,
                            environment: str = "development",
                            history_result_artifact_id: str = "", history_operation: str = "",
                            history_binding: dict | None = None,
                            output_path: str | None = None) -> dict:
    normalized_action_digest = normalize_action_digest(action_digest)
    source = _package_source(ready_artifact_id, history_result_artifact_id, history_operation)
    if source["kind"] == "historical-result" and environment != "development":
        _fail("uploaded-artifact-metadata-mismatch")
    producer = _validate_package_run(
        metadata, run, expected_artifact_id=expected_artifact_id, environment=environment,
        expected_name=expected_name, expected_digest=normalized_action_digest,
        run_id=run_id, run_attempt=run_attempt, completed=False)
    product_version, manifest, plan_bytes = _validate_package_from_plan(
        package_dir, plan_path, environment=environment, verify_artifact_digest=True,
        artifact_digest=normalized_action_digest, metadata_digest=metadata.get("digest"))
    if source["kind"] == "ready":
        expected_name_for_run = (
            f"lwc-release-notes-{product_version}-{environment}-{run_id}-{run_attempt}")
        if history_binding is not None or release_notes.HISTORICAL_MANIFEST_FIELDS <= set(manifest):
            _fail("history-binding-unexpected")
    else:
        if not release_notes.HISTORICAL_MANIFEST_FIELDS <= set(manifest):
            _fail("historical-provenance-required")
        history_binding = _validate_history_binding(
            history_binding, result_artifact_id=history_result_artifact_id,
            operation=history_operation, plan_bytes=plan_bytes)
        expected_name_for_run = release_notes.historical_package_artifact_name(
            product_version, history_operation, history_result_artifact_id, run_id, run_attempt)
    if expected_name != expected_name_for_run:
        _fail("uploaded-artifact-name-mismatch")
    plan = _json_object(plan_bytes)
    resume_inputs = _resume_inputs(plan, ready_artifact_id, expected_artifact_id, environment) \
        if source["kind"] == "ready" else None
    result = {
        "artifact_id": expected_artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": normalized_action_digest,
        "action_digest_raw": action_digest,
        "product_version": product_version,
        "environment": producer["environment"],
        "ready_artifact_id": ready_artifact_id or None,
        "source_kind": source["kind"],
        "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
        "resume_inputs": resume_inputs,
        "manifest_sha256": hashlib.sha256((package_dir / "release-notes-manifest.json").read_bytes()).hexdigest(),
        "locale_count": len(manifest["files"]),
        "expires_at": metadata.get("expires_at"),
    }
    if source["kind"] == "historical-result":
        result["historical_binding"] = history_binding
        result["history_result_artifact_id"] = history_result_artifact_id
        result["history_operation"] = history_operation
    outputs = {
        "product_version": product_version,
        "artifact_id": expected_artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": normalized_action_digest,
        "action_digest_raw": action_digest,
        "source_kind": source["kind"],
    }
    if resume_inputs is not None:
        outputs["resume_inputs"] = json.dumps(resume_inputs, sort_keys=True, separators=(",", ":"))
    if source["kind"] == "historical-result":
        outputs.update({
            "history_result_artifact_id": history_result_artifact_id,
            "history_operation": history_operation,
            "history_run_id": history_binding["run_id"],
            "history_run_attempt": history_binding["run_attempt"],
            "history_head_sha": history_binding["head_sha"],
            "history_source_sha": history_binding["source_sha"],
            "history_plan_id": history_binding["plan_id"],
            "history_plan_sha256": history_binding["plan_sha256"],
        })
    _write_github_output(output_path, outputs)
    return result


def _manifest_identity(package_dir: Path, environment: str = "development") -> str:
    destination = DESTINATIONS.get(environment)
    if destination is None:
        _fail("package-manifest-invalid")
    files = _package_directory_files(package_dir)
    manifest = _json_object(files["release-notes-manifest.json"])
    version = manifest.get("product_version")
    if (manifest.get("environment") != destination["package_environment"] or
            not release_notes.valid_version(version) or
            not isinstance(manifest.get("files"), dict) or
            not 1 <= len(manifest["files"]) <= 2 or set(manifest["files"]) - {"en", "zh-TW"}):
        _fail("package-manifest-invalid")
    expected_files = {"release-notes-manifest.json"} | {f"{locale}.json" for locale in manifest["files"]}
    if set(files) != expected_files:
        _fail("package-files-mismatch")
    return version


def build_dispatch_payload(package_dir: Path, *, ready_artifact_id: str = "",
                           history_result_artifact_id: str = "", history_operation: str = "",
                           retention_days: int,
                           environment: str = "development",
                           request_id: str | None = None) -> tuple[dict, str]:
    source = _package_source(ready_artifact_id, history_result_artifact_id, history_operation)
    destination = DESTINATIONS.get(environment)
    if destination is None or (environment == "production" and source["kind"] != "ready"):
        _fail("dispatch-input-invalid")
    if type(retention_days) is not int or retention_days != DISPATCH_PACKAGE_RETENTION_DAYS:
        _fail("dispatch-input-invalid")
    version = _manifest_identity(package_dir, environment)
    archive = package_directory_archive(package_dir)
    archive_b64 = base64.b64encode(archive).decode("ascii")
    request_id = request_id or uuid.uuid4().hex
    if not REQUEST_ID_RE.fullmatch(request_id):
        _fail("dispatch-request-id-invalid")
    inputs = {
        "artifact_id": ready_artifact_id,
        "prepare_release_notes_package": "true",
        "operation": "release",
        "release_notes_action": "stop",
        "package_zip_base64": archive_b64,
        "release_notes_package_request_id": request_id,
        "release_notes_result_artifact_id": history_result_artifact_id,
    }
    if source["kind"] == "historical-result":
        package_files = _package_directory_files(package_dir)
        manifest = _json_object(package_files["release-notes-manifest.json"])
        # The package's exact plan is intentionally validated by the canonical
        # workflow after it downloads the specified retained result artifact.
        if not release_notes.HISTORICAL_MANIFEST_FIELDS <= set(manifest):
            _fail("historical-provenance-required")
        inputs["release_notes_operation"] = history_operation
    body = {"ref": destination["branch"], "inputs": inputs}
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


def dispatch_package(repository: str, package_dir: Path, *, ready_artifact_id: str = "",
                     history_result_artifact_id: str = "", history_operation: str = "",
                     retention_days: int, timeout_seconds: int = 900) -> dict:
    if not REPOSITORY_RE.fullmatch(repository) or not 30 <= timeout_seconds <= 1800:
        _fail("dispatch-input-invalid")
    source = _package_source(ready_artifact_id, history_result_artifact_id, history_operation)
    if source["kind"] == "ready":
        ready_metadata = _gh_api(repository, f"actions/artifacts/{ready_artifact_id}")
        ready_name = ready_metadata.get("name") if isinstance(ready_metadata, dict) else None
        ready_match = READY_NAME_RE.fullmatch(ready_name) if isinstance(ready_name, str) else None
        if not ready_match:
            _fail("ready-artifact-not-qualified")
        environment = ready_match.group(1)
        ready_run_id = ready_match.group(2)
        ready_run = _gh_api(repository, f"actions/runs/{ready_run_id}")
        ready_artifacts = _gh_api(repository, f"actions/runs/{ready_run_id}/artifacts?per_page=100")
        find_ready_result_artifact(ready_metadata, ready_run, ready_artifacts,
                                   ready_artifact_id, environment)
    else:
        environment = "development"
    destination = DESTINATIONS[environment]
    request_id = uuid.uuid4().hex
    body, version = build_dispatch_payload(package_dir, ready_artifact_id=ready_artifact_id,
                                           history_result_artifact_id=history_result_artifact_id,
                                           history_operation=history_operation,
                                           retention_days=retention_days, environment=environment,
                                           request_id=request_id)
    body_bytes = json.dumps(body, separators=(",", ":")).encode("utf-8")
    _gh_api(repository, f"actions/workflows/{Path(destination['workflow_path']).name}/dispatches",
            method="POST", body=body_bytes)
    title = f"Prepare Release Notes package {request_id}"
    workflow_name = Path(destination["workflow_path"]).name
    endpoint = (f"actions/workflows/{workflow_name}/runs?branch={destination['branch']}"
                "&event=workflow_dispatch&per_page=100")
    deadline = time.monotonic() + timeout_seconds
    run = None
    while time.monotonic() < deadline:
        response = _gh_api(repository, endpoint)
        runs = response.get("workflow_runs") if isinstance(response, dict) else None
        if not isinstance(runs, list):
            _fail("workflow-run-list-invalid")
        matches = [item for item in runs if isinstance(item, dict) and item.get("display_title") == title and
                   _workflow_path_matches(item.get("path"), destination["workflow_path"], destination["branch"]) and
                   item.get("event") == "workflow_dispatch" and
                   item.get("head_branch") == destination["branch"]]
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
            not _workflow_path_matches(run.get("path"), destination["workflow_path"], destination["branch"]) or
            run.get("event") != "workflow_dispatch" or run.get("head_branch") != destination["branch"] or
            not isinstance(run.get("head_sha"), str) or
            re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]) is None):
        _fail("workflow-run-identity-invalid")
    run_url = run.get("html_url") if isinstance(run.get("html_url"), str) else ""
    if run.get("conclusion") != "success":
        return {"status": "failed", "workflow_run_id": run_id, "workflow_run_attempt": run_attempt,
                "workflow_run_url": run_url, "conclusion": run.get("conclusion")}
    expected_name = (release_notes.historical_package_artifact_name(
        version, history_operation, history_result_artifact_id, run_id, run_attempt)
        if source["kind"] == "historical-result"
        else f"lwc-release-notes-{version}-{environment}-{run_id}-{run_attempt}")
    artifact_list = _gh_api(repository, f"actions/runs/{run_id}/artifacts?per_page=100")
    artifacts = artifact_list.get("artifacts") if isinstance(artifact_list, dict) else None
    if not isinstance(artifacts, list):
        _fail("workflow-artifact-list-invalid")
    matches = [item for item in artifacts if isinstance(item, dict) and item.get("name") == expected_name]
    if len(matches) != 1:
        _fail("workflow-artifact-not-unique")
    artifact_id = str(matches[0].get("id", ""))
    metadata = _gh_api(repository, f"actions/artifacts/{artifact_id}")
    package_receipt = _validate_package_run(
        metadata, run, expected_artifact_id=artifact_id, environment=environment,
        expected_name=expected_name, completed=True)
    if package_receipt["historical"] != (source["kind"] == "historical-result"):
        _fail("workflow-artifact-receipt-invalid")
    receipt = {
        "status": "ready",
        "environment": destination["package_environment"],
        "destination": environment,
        "workflow_path": destination["workflow_path"],
        "ref": destination["branch"],
        "request_id": request_id,
        "product_version": version,
        "source_kind": source["kind"],
        "ready_artifact_id": ready_artifact_id or None,
        "requested_retention_days": retention_days,
        "workflow_run_id": run_id,
        "workflow_run_attempt": run_attempt,
        "workflow_run_url": run_url,
        "artifact_id": artifact_id,
        "artifact_name": expected_name,
        "artifact_digest": package_receipt["artifact_digest"],
        "expires_at": package_receipt["expires_at"],
    }
    if source["kind"] == "historical-result":
        receipt["history_result_artifact_id"] = history_result_artifact_id
        receipt["history_operation"] = history_operation
    return receipt


def _argument_parser():
    parser = argparse.ArgumentParser(description="Validate or dispatch a private TPM-authored Release Notes package.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    select = subparsers.add_parser("select-bff")
    select.add_argument("--plan", required=True)
    select.add_argument("--output", default="")
    ready_id = subparsers.add_parser("ready-run-id")
    ready_id.add_argument("--metadata", required=True)
    ready_id.add_argument("--artifact-id", required=True)
    ready_id.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    history_id = subparsers.add_parser("history-result-run-id")
    history_id.add_argument("--metadata", required=True)
    history_id.add_argument("--artifact-id", required=True)
    history_id.add_argument("--output", default="")
    history = subparsers.add_parser("validate-history-result")
    history.add_argument("--metadata", required=True)
    history.add_argument("--run", required=True)
    history.add_argument("--result-directory", required=True)
    history.add_argument("--artifact-id", required=True)
    history.add_argument("--operation", choices=sorted(release_notes.TRANSITION_OPS), required=True)
    history.add_argument("--binding-output", required=True)
    history.add_argument("--output", default="")
    ready = subparsers.add_parser("validate-ready")
    ready.add_argument("--metadata", required=True)
    ready.add_argument("--run", required=True)
    ready.add_argument("--artifacts", required=True)
    ready.add_argument("--result-metadata", required=True)
    ready.add_argument("--ready-directory", required=True)
    ready.add_argument("--result-directory", required=True)
    ready.add_argument("--artifact-id", required=True)
    ready.add_argument("--result-artifact-id", required=True)
    ready.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    ready.add_argument("--output", default="")
    ready_result = subparsers.add_parser("find-ready-result")
    ready_result.add_argument("--metadata", required=True)
    ready_result.add_argument("--run", required=True)
    ready_result.add_argument("--artifacts", required=True)
    ready_result.add_argument("--artifact-id", required=True)
    ready_result.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    ready_result.add_argument("--output", default="")
    prepare = subparsers.add_parser("prepare-package")
    prepare.add_argument("--plan", required=True)
    prepare.add_argument("--package-dir", required=True)
    prepare.add_argument("--environment", choices=("dev", "prod"), required=True)
    prepare.add_argument("--request-id", required=True)
    prepare.add_argument("--ready-artifact-id", default="")
    prepare.add_argument("--history-result-artifact-id", default="")
    prepare.add_argument("--history-operation", default="")
    prepare.add_argument("--retention-days", choices=tuple(str(x) for x in sorted(ALLOWED_RETENTION_DAYS)), required=True)
    prepare.add_argument("--event-path", default="")
    prepare.add_argument("--output", default="")
    validate_event = subparsers.add_parser("validate-package-event")
    validate_event.add_argument("--event-path", required=True)
    validate_event.add_argument("--request-id", required=True)
    validate_event.add_argument("--ready-artifact-id", default="")
    validate_event.add_argument("--history-result-artifact-id", default="")
    validate_event.add_argument("--history-operation", default="")
    validate_event.add_argument("--retention-days", choices=tuple(str(x) for x in sorted(ALLOWED_RETENTION_DAYS)), required=True)
    validate_event.add_argument("--environment", choices=("dev", "prod"), required=True)
    verify = subparsers.add_parser("verify-upload")
    verify.add_argument("--package-dir", required=True)
    verify.add_argument("--plan", required=True)
    verify.add_argument("--metadata", required=True)
    verify.add_argument("--artifact-id", required=True)
    verify.add_argument("--artifact-name", required=True)
    verify.add_argument("--action-digest", required=True)
    verify.add_argument("--run-id", required=True)
    verify.add_argument("--run-attempt", required=True)
    verify.add_argument("--run", required=True)
    verify.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    verify.add_argument("--ready-artifact-id", default="")
    verify.add_argument("--history-result-artifact-id", default="")
    verify.add_argument("--history-operation", default="")
    verify.add_argument("--history-binding", default="")
    verify.add_argument("--output", default="")
    package_artifact = subparsers.add_parser("validate-resend-package-artifact")
    package_artifact.add_argument("--metadata", required=True)
    package_artifact.add_argument("--run", required=True)
    package_artifact.add_argument("--artifact-id", required=True)
    package_artifact.add_argument("--result-artifact-id", required=True)
    package_artifact.add_argument("--operation", choices=sorted(release_notes.TRANSITION_OPS), required=True)
    package_artifact.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    package_artifact.add_argument("--repository", required=True)
    package_artifact.add_argument("--output", default="")
    runtime_completion = subparsers.add_parser("validate-original-runtime-completion")
    runtime_completion.add_argument("--result-metadata", required=True)
    runtime_completion.add_argument("--run", required=True)
    runtime_completion.add_argument("--jobs", required=True)
    runtime_completion.add_argument("--package-metadata", required=True)
    runtime_completion.add_argument("--package-run", required=True)
    runtime_completion.add_argument("--result-directory", required=True)
    runtime_completion.add_argument("--package-directory", required=True)
    runtime_completion.add_argument("--result-artifact-id", required=True)
    runtime_completion.add_argument("--package-artifact-id", required=True)
    runtime_completion.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    runtime_completion.add_argument("--operation", choices=sorted(release_notes.TRANSITION_OPS), required=True)
    runtime_completion.add_argument("--repository", required=True)
    runtime_completion.add_argument("--output", required=True)
    current_package_artifact = subparsers.add_parser("validate-current-package-artifact")
    current_package_artifact.add_argument("--metadata", required=True)
    current_package_artifact.add_argument("--run", required=True)
    current_package_artifact.add_argument("--artifact-id", required=True)
    current_package_artifact.add_argument("--environment", choices=tuple(DESTINATIONS), required=True)
    current_package_artifact.add_argument("--output", default="")
    caller = subparsers.add_parser("resolve-caller")
    caller.add_argument("--repository", required=True)
    caller.add_argument("--workflow-ref", required=True)
    caller.add_argument("--ref", required=True)
    caller.add_argument("--output", default="")
    dispatch = subparsers.add_parser("dispatch")
    dispatch.add_argument("--repository", required=True)
    dispatch.add_argument("--ready-artifact-id", default="")
    dispatch.add_argument("--history-result-artifact-id", default="")
    dispatch.add_argument("--history-operation", default="")
    dispatch.add_argument("--package-dir", required=True)
    dispatch.add_argument("--retention-days", choices=(str(DISPATCH_PACKAGE_RETENTION_DAYS),),
                          default=str(DISPATCH_PACKAGE_RETENTION_DAYS))
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
            if not match or match.group(1) != args.environment or run_id != match.group(2):
                _fail("ready-artifact-name-invalid")
            result = {"run_id": run_id}
            print(run_id)
            return 0
        elif args.command == "history-result-run-id":
            result = history_result_run_id(_read_json(Path(args.metadata)), args.artifact_id, args.output)
        elif args.command == "validate-history-result":
            result = validate_history_result_handoff(
                _read_json(Path(args.metadata)), _read_json(Path(args.run)), Path(args.result_directory),
                expected_artifact_id=args.artifact_id, operation=args.operation,
                output_path=args.output, binding_path=Path(args.binding_output))
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
                output_path=args.output, environment=args.environment)
        elif args.command == "find-ready-result":
            result = find_ready_result_artifact(
                _read_json(Path(args.metadata)), _read_json(Path(args.run)),
                _read_json(Path(args.artifacts)), args.artifact_id, args.environment)
            _write_github_output(args.output, {key: str(value) for key, value in result.items()})
        elif args.command == "prepare-package":
            event_value = args.event_path or os.environ.get("GITHUB_EVENT_PATH", "")
            if not event_value:
                _fail("workflow-event-unavailable")
            environment = "development" if args.environment == "dev" else "production"
            result = prepare_package_from_event(Path(event_value), Path(args.plan), Path(args.package_dir),
                                                args.output, args.request_id, args.ready_artifact_id,
                                                args.retention_days, args.history_result_artifact_id,
                                                args.history_operation, environment)
        elif args.command == "validate-package-event":
            environment = "development" if args.environment == "dev" else "production"
            _read_package_input_from_event(Path(args.event_path), args.request_id,
                                           args.ready_artifact_id, args.retention_days,
                                           args.history_result_artifact_id, args.history_operation,
                                           environment)
            result = {"status": "valid"}
        elif args.command == "verify-upload":
            metadata = _read_json(Path(args.metadata))
            run = _read_json(Path(args.run))
            history_binding = _read_json(Path(args.history_binding)) if args.history_binding else None
            result = verify_uploaded_package(
                Path(args.package_dir), Path(args.plan), metadata,
                expected_artifact_id=args.artifact_id, expected_name=args.artifact_name,
                action_digest=args.action_digest, run=run, run_id=args.run_id,
                run_attempt=args.run_attempt, ready_artifact_id=args.ready_artifact_id,
                environment=args.environment,
                history_result_artifact_id=args.history_result_artifact_id,
                history_operation=args.history_operation, history_binding=history_binding,
                output_path=args.output)
        elif args.command == "validate-resend-package-artifact":
            result = validate_resend_package_artifact(
                _read_json(Path(args.metadata)), _read_json(Path(args.run)),
                expected_artifact_id=args.artifact_id,
                result_artifact_id=args.result_artifact_id, operation=args.operation,
                environment=args.environment, repository=args.repository,
                output_path=args.output)
        elif args.command == "validate-original-runtime-completion":
            result = validate_original_runtime_completion(
                _read_json(Path(args.result_metadata)), _read_json(Path(args.run)),
                _read_json(Path(args.jobs)), _read_json(Path(args.package_metadata)),
                _read_json(Path(args.package_run)),
                result_directory=Path(args.result_directory), package_directory=Path(args.package_directory),
                expected_result_artifact_id=args.result_artifact_id,
                expected_package_artifact_id=args.package_artifact_id,
                environment=args.environment, operation=args.operation, repository=args.repository,
                output_path=args.output)
        elif args.command == "validate-current-package-artifact":
            result = validate_current_package_artifact(
                _read_json(Path(args.metadata)), _read_json(Path(args.run)),
                expected_artifact_id=args.artifact_id, environment=args.environment,
                output_path=args.output)
        elif args.command == "resolve-caller":
            result = resolve_caller_destination(
                args.repository, args.workflow_ref, args.ref, args.output)
        elif args.command == "dispatch":
            result = dispatch_package(
                args.repository, Path(args.package_dir), ready_artifact_id=args.ready_artifact_id,
                history_result_artifact_id=args.history_result_artifact_id,
                history_operation=args.history_operation,
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
