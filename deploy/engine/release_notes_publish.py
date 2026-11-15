#!/usr/bin/env python3
"""Publish TPM-authored Release Notes through the existing deployment workflow.

Cloud access is lazy and isolated in GCSCLIStore; importing this module is safe
for offline tests. Every object is validated/read back before advancing stage.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from types import SimpleNamespace
from urllib.parse import quote
from urllib.request import Request, urlopen

import release_notes
import release_notes_capture


class GenerationConflict(Exception):
    safe_class = "generation-conflict"


PUBLICATION_FIELDS = {
    "schema_version", "environment", "operation", "run_id", "run_attempt",
    "plan_sha256", "state_sha256", "result_sha256", "receipt_sha256", "published_at",
}


class GCSCLIStore:
    def __init__(self, bucket: str):
        if not bucket or "/" in bucket:
            raise release_notes.ReleaseNotesError("validate", "target-bucket-invalid")
        self.bucket = bucket

    def _uri(self, path: str) -> str:
        return f"gs://{self.bucket}/{path}"

    def _run(self, args: list[str], stage: str, safe_class: str) -> subprocess.CompletedProcess:
        try:
            result = subprocess.run(["gcloud", "storage", *args], capture_output=True, check=False)
        except OSError:
            raise release_notes.ReleaseNotesError(stage, safe_class) from None
        if result.returncode != 0:
            raise release_notes.ReleaseNotesError(stage, safe_class)
        return result

    def _generation(self, path: str) -> int | None:
        try:
            result = subprocess.run(["gcloud", "storage", "objects", "describe", self._uri(path), "--format=json"],
                                    capture_output=True, check=False)
        except OSError:
            raise release_notes.ReleaseNotesError("index_update", "index-read-failed") from None
        if result.returncode != 0:
            if b"not found" in result.stderr.lower() or b"404" in result.stderr:
                return None
            raise release_notes.ReleaseNotesError("index_update", "index-read-failed")
        try:
            value = json.loads(result.stdout)
            generation = int(value.get("generation"))
        except (ValueError, TypeError, json.JSONDecodeError, AttributeError):
            raise release_notes.ReleaseNotesError("index_update", "index-generation-invalid") from None
        if generation < 1:
            raise release_notes.ReleaseNotesError("index_update", "index-generation-invalid")
        return generation

    def _cat(self, path: str, stage: str, safe_class: str) -> bytes | None:
        try:
            result = subprocess.run(["gcloud", "storage", "cat", self._uri(path)], capture_output=True, check=False)
        except OSError:
            raise release_notes.ReleaseNotesError(stage, safe_class) from None
        if result.returncode != 0:
            if b"not found" in result.stderr.lower() or b"404" in result.stderr:
                return None
            raise release_notes.ReleaseNotesError(stage, safe_class)
        return result.stdout

    def read_index(self) -> tuple[bytes | None, int]:
        path = "release-notes/v1/index.json"
        for _ in range(3):
            before = self._generation(path)
            if before is None:
                after = self._generation(path)
                if after is None:
                    return None, 0
                continue
            data = self._cat(path, "index_update", "index-read-failed")
            after = self._generation(path)
            if data is not None and before == after:
                return data, before
        raise release_notes.ReleaseNotesError("index_update", "index-snapshot-unstable")

    def create_content(self, path: str, data: bytes) -> int:
        with tempfile.TemporaryDirectory(prefix="lwc-notes-") as directory:
            local = Path(directory) / "content.json"
            local.write_bytes(data)
            try:
                subprocess.run(["gcloud", "storage", "cp", "--if-generation-match=0", "--cache-control=no-store",
                                "--content-type=application/json", str(local), self._uri(path)],
                               capture_output=True, check=True)
            except (OSError, subprocess.CalledProcessError):
                existing = self.read_content(path)
                if existing != data:
                    raise release_notes.ReleaseNotesError("content_upload", "content-create-failed") from None
            generation = self._generation(path)
            if generation is None:
                raise release_notes.ReleaseNotesError("exact_readback", "content-generation-unavailable")
            return generation

    def read_content(self, path: str) -> bytes:
        body = self._cat(path, "exact_readback", "content-readback-unavailable")
        if body is None:
            raise release_notes.ReleaseNotesError("exact_readback", "content-object-missing")
        return body

    def compare_and_swap_index(self, data: bytes, generation: int) -> int:
        path = "release-notes/v1/index.json"
        with tempfile.TemporaryDirectory(prefix="lwc-notes-index-") as directory:
            local = Path(directory) / "index.json"
            local.write_bytes(data)
            try:
                subprocess.run(["gcloud", "storage", "cp", f"--if-generation-match={generation}",
                                "--cache-control=no-store", "--content-type=application/json",
                                str(local), self._uri(path)], capture_output=True, check=True)
            except (OSError, subprocess.CalledProcessError):
                current = self._generation(path)
                if current != generation:
                    raise GenerationConflict()
                raise release_notes.ReleaseNotesError("index_update", "index-write-failed") from None
            result = self._generation(path)
            if result is None or result <= generation:
                raise release_notes.ReleaseNotesError("index_update", "index-generation-invalid")
            return result

    def active_marker_matches(self, marker: dict, *, directory: Path, operation: str,
                              run_id: str, run_attempt: int) -> bool:
        return release_notes_capture.active_marker_matches(directory, operation, run_id, run_attempt, marker)


class PublisherStore:
    def __init__(self, store: GCSCLIStore, directory: Path, operation: str, run_id: str, run_attempt: int):
        self.store, self.directory = store, directory
        self.operation, self.run_id, self.run_attempt = operation, run_id, run_attempt

    def __getattr__(self, name):
        if name in ("read_index", "create_content", "read_content", "compare_and_swap_index"):
            return getattr(self.store, name)
        raise AttributeError(name)

    def active_marker_matches(self, marker: dict) -> bool:
        return self.store.active_marker_matches(marker, directory=self.directory, operation=self.operation,
                                                run_id=self.run_id, run_attempt=self.run_attempt)


def write_evidence(directory: Path, value: dict) -> None:
    path = directory / "release-notes-evidence.json"
    if path.exists():
        try:
            previous = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            raise release_notes.ReleaseNotesError("exact_readback", "stage-evidence-corrupt") from None
        if not isinstance(previous, dict) or previous.get("schema_version") != 1 or not isinstance(previous.get("stages"), list):
            raise release_notes.ReleaseNotesError("exact_readback", "stage-evidence-corrupt")
        evidence = previous
    else:
        evidence = {"schema_version": 1, "stages": []}
    if any(not isinstance(item, dict) for item in evidence["stages"]):
        raise release_notes.ReleaseNotesError("exact_readback", "stage-evidence-corrupt")
    record = dict(value)
    record["event_sequence"] = len(evidence["stages"]) + 1
    record["stage_attempt"] = 1 + sum(
        item.get("action") == record.get("action") and item.get("stage") == record.get("stage")
        for item in evidence["stages"])
    record["recorded_at"] = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    evidence["stages"].append(record)
    temporary_path = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=directory,
                                         prefix=".release-notes-evidence-", delete=False) as temporary:
            temporary_path = Path(temporary.name)
            temporary.write(json.dumps(evidence, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.chmod(temporary_path, 0o600)
        os.replace(temporary_path, path)
    except OSError:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)
        raise release_notes.ReleaseNotesError("exact_readback", "stage-evidence-write-failed") from None


def operation_marker(directory: Path, args) -> tuple[dict, bytes]:
    sidecar_path = directory / "release-notes-operation.json"
    if sidecar_path.is_symlink():
        raise release_notes.ReleaseNotesError("index_update", "operation-evidence-mismatch")
    if not sidecar_path.exists():
        if args.action == "resend":
            validate_retained_operation(directory, args)
            return None, b""
        return release_notes_capture.capture(directory, args.operation, args.run_id, args.run_attempt)
    sidecar_bytes = release_notes_capture.read_bytes(sidecar_path)
    plan_bytes = release_notes_capture.read_bytes(Path(args.plan))
    state_bytes = release_notes_capture.read_bytes(directory / "state.json")
    result_bytes = release_notes_capture.read_bytes(directory / "result.json")
    receipt_bytes = release_notes_capture.read_bytes(directory / "receipts" / "bff.json")
    sidecar = release_notes._json_object(sidecar_bytes.removesuffix(b"\n"), "index_update",
                                         release_notes.MAX_INDEX_BYTES, release_notes.SIDECAR_FIELDS)
    canonical_sidecar = json.dumps(sidecar, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    if sidecar_bytes != canonical_sidecar:
        raise release_notes.ReleaseNotesError("index_update", "operation-evidence-mismatch")
    marker, _ = release_notes.normalize_operation(plan_bytes, state_bytes, result_bytes,
        canonical_sidecar[:-1],
        environment=args.environment, workflow_operation=args.operation, run_id=args.run_id,
        run_attempt=args.run_attempt, service=sidecar.get("service", ""), receipt_bytes=receipt_bytes)
    return marker, sidecar_bytes


def validate_retained_operation(directory: Path, args) -> dict:
    """Validate immutable operation bytes without provider access or recovery writes."""
    try:
        return release_notes.validate_native_operation(
            Path(args.plan).read_bytes(), (directory / "state.json").read_bytes(),
            (directory / "result.json").read_bytes(), (directory / "receipts" / "bff.json").read_bytes(),
            environment=args.environment, workflow_operation=args.operation,
            run_id=args.run_id, run_attempt=args.run_attempt)
    except OSError:
        raise release_notes.ReleaseNotesError("product_release", "retained-operation-evidence-unavailable") from None


def publication_record(directory: Path, args, plan_bytes: bytes) -> tuple[dict | None, bytes]:
    """Read and validate the original post-release timestamp, if this artifact has one."""
    path = directory / "release-notes-publication.json"
    if path.is_symlink():
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid")
    if not path.exists():
        return None, b""
    if not path.is_file():
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid")
    try:
        data = path.read_bytes()
        state_bytes = (directory / "state.json").read_bytes()
        result_bytes = (directory / "result.json").read_bytes()
        receipt_bytes = (directory / "receipts" / "bff.json").read_bytes()
    except OSError:
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid") from None
    record = release_notes._json_object(data.removesuffix(b"\n"), "product_release",
                                        16 * 1024, PUBLICATION_FIELDS)
    expected = {
        "schema_version": 1, "environment": args.environment, "operation": "release",
        "run_id": args.run_id, "run_attempt": args.run_attempt,
        "plan_sha256": release_notes.digest_bytes(plan_bytes),
        "state_sha256": release_notes.digest_bytes(state_bytes),
        "result_sha256": release_notes.digest_bytes(result_bytes),
        "receipt_sha256": release_notes.digest_bytes(receipt_bytes),
    }
    if set(record) != PUBLICATION_FIELDS or any(record.get(key) != value for key, value in expected.items()) or \
            args.operation != "release" or not release_notes._valid_timestamp(record.get("published_at")):
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid")
    release_notes.validate_native_operation(
        plan_bytes, state_bytes, result_bytes, receipt_bytes,
        environment=args.environment, workflow_operation=args.operation,
        run_id=args.run_id, run_attempt=args.run_attempt)
    canonical = json.dumps(record, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    if data != canonical:
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid")
    return record, data


def write_publication_record(directory: Path, args, plan_bytes: bytes) -> tuple[dict, bytes]:
    if args.operation != "release" or args.product_status != "released" or \
            not release_notes._valid_timestamp(args.published_at):
        raise release_notes.ReleaseNotesError("product_release", "publication-time-input-invalid")
    state_bytes = release_notes_capture.read_bytes(directory / "state.json")
    result_bytes = release_notes_capture.read_bytes(directory / "result.json")
    receipt_bytes = release_notes_capture.read_bytes(directory / "receipts" / "bff.json")
    release_notes.validate_native_operation(
        plan_bytes, state_bytes, result_bytes, receipt_bytes,
        environment=args.environment, workflow_operation=args.operation,
        run_id=args.run_id, run_attempt=args.run_attempt)
    record = {
        "schema_version": 1, "environment": args.environment, "operation": args.operation,
        "run_id": args.run_id, "run_attempt": args.run_attempt,
        "plan_sha256": release_notes.digest_bytes(plan_bytes),
        "state_sha256": release_notes.digest_bytes(state_bytes),
        "result_sha256": release_notes.digest_bytes(result_bytes),
        "receipt_sha256": release_notes.digest_bytes(receipt_bytes),
        "published_at": args.published_at,
    }
    data = json.dumps(record, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    path = directory / "release-notes-publication.json"
    if path.is_symlink():
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-invalid")
    if path.exists():
        old = release_notes_capture.read_bytes(path)
        if old != data:
            raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-conflict")
        return record, data
    temporary_path = None
    try:
        with tempfile.NamedTemporaryFile("wb", dir=directory, prefix=".release-notes-publication-", delete=False) as temporary:
            temporary_path = Path(temporary.name)
            temporary.write(data)
            temporary.flush()
            os.fsync(temporary.fileno())
        os.chmod(temporary_path, 0o600)
        os.replace(temporary_path, path)
    except OSError:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)
        raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-write-failed") from None
    return record, data


def record_capture_failure(directory: Path, *, environment: str, operation: str, run_id: str,
                           run_attempt: int, release_notes_action: str, product_status: str,
                           artifact_id: str, artifact_digest: str, failure_stage: str,
                           safe_class: str) -> dict:
    """Persist a capture failure through the retained publisher evidence log."""
    plan_bytes = release_notes_capture.read_bytes(directory / "plan.json")
    state_bytes = release_notes_capture.read_bytes(directory / "state.json")
    result_bytes = release_notes_capture.read_bytes(directory / "result.json")
    receipt_bytes = release_notes_capture.read_bytes(directory / "receipts" / "bff.json")
    release_notes.validate_native_operation(
        plan_bytes, state_bytes, result_bytes, receipt_bytes,
        environment=environment, workflow_operation=operation,
        run_id=run_id, run_attempt=run_attempt)
    if product_status != "released" or release_notes_action not in ("run", "retry", "skip", "marker"):
        raise release_notes.ReleaseNotesError("product_release", "capture-context-invalid")
    if (release_notes_action in ("run", "retry", "skip") and operation != "release") or \
            (release_notes_action == "marker" and operation not in ("deploy", "rollback", "reactivate")):
        raise release_notes.ReleaseNotesError("product_release", "capture-context-invalid")
    if failure_stage not in ("index_update", "exact_readback") or \
            re.fullmatch(r"[a-z0-9-]{1,80}", safe_class) is None:
        failure_stage, safe_class = "service_readback", "capture-error-report-invalid"

    evidence = {
        "schema_version": 1, "action": "capture", "environment": environment,
        "stage": failure_stage, "status": "failed", "safe_class": safe_class,
        "product_status": product_status, "release_notes_action": release_notes_action,
        "plan_sha256": release_notes.digest_bytes(plan_bytes),
        "state_sha256": release_notes.digest_bytes(state_bytes),
        "result_sha256": release_notes.digest_bytes(result_bytes),
        "receipt_sha256": release_notes.digest_bytes(receipt_bytes),
        "artifact_id": artifact_id or None, "artifact_digest": artifact_digest or None,
        "operation": operation, "run_id": run_id, "run_attempt": run_attempt,
        "next_actions": ["retry", "skip", "stop"],
        "owner_choice_required": True, "automatic_skip": False,
        "product_rollback": False, "product_redeploy": False,
        "decision_actor": None, "decision_time": None,
    }
    if release_notes_action in ("run", "retry"):
        publication, publication_bytes = publication_record(
            directory, SimpleNamespace(environment=environment, operation=operation,
                                       run_id=run_id, run_attempt=run_attempt), plan_bytes)
        if publication is None:
            raise release_notes.ReleaseNotesError("product_release", "publication-time-evidence-unavailable")
        evidence["published_at"] = publication["published_at"]
        evidence["publication_evidence_sha256"] = release_notes.digest_bytes(publication_bytes)
    write_evidence(directory, evidence)
    return evidence


def verify_reader_api(directory: Path, expected_environment: str, marker: dict, product_version: str,
                      locales: list[str], *, historical: bool) -> dict:
    base = release_notes_capture.active_service_url(directory).rstrip("/")
    request = Request(base + "/api/v1/public/release-notes", headers={"Cache-Control": "no-cache"})
    try:
        with urlopen(request, timeout=20) as response:
            if response.status != 200 or response.headers.get("Cache-Control", "").lower() != "no-store":
                raise release_notes.ReleaseNotesError("exact_readback", "reader-index-readback-invalid")
            index = json.loads(response.read(2 * release_notes.MAX_INDEX_BYTES + 1).decode("utf-8"))
    except release_notes.ReleaseNotesError:
        raise
    except Exception:
        raise release_notes.ReleaseNotesError("exact_readback", "reader-index-readback-unavailable") from None
    if not isinstance(index, dict) or index.get("schema_version") != release_notes.SCHEMA_VERSION or \
            index.get("environment") != expected_environment or not isinstance(index.get("history"), list):
        raise release_notes.ReleaseNotesError("exact_readback", "reader-index-readback-invalid")
    if marker is not None and not historical:
        if index.get("runtime_product_version") != marker["product_version"] or \
                index.get("current_product_version") != marker["product_version"] or \
                index.get("current_status") not in ("available", "missing"):
            raise release_notes.ReleaseNotesError("exact_readback", "reader-current-readback-mismatch")
    for locale in locales:
        request = Request(base + "/api/v1/public/release-notes/" + quote(product_version, safe="") +
                          "?locale=" + quote(locale, safe=""),
                          headers={"Cache-Control": "no-cache"})
        try:
            with urlopen(request, timeout=20) as response:
                if response.status != 200 or response.headers.get("Cache-Control", "").lower() != "no-store":
                    raise release_notes.ReleaseNotesError("exact_readback", "reader-content-readback-invalid")
                content = json.loads(response.read(release_notes.MAX_CONTENT_BYTES * 2).decode("utf-8"))
        except release_notes.ReleaseNotesError:
            raise
        except Exception:
            raise release_notes.ReleaseNotesError("exact_readback", "reader-content-readback-unavailable") from None
        if not isinstance(content, dict) or content.get("schema_version") != release_notes.SCHEMA_VERSION or \
                content.get("product_version") != product_version or not isinstance(content.get("entries"), list):
            raise release_notes.ReleaseNotesError("exact_readback", "reader-content-readback-mismatch")
    return {"reader_environment": index.get("environment"), "reader_current_status": index.get("current_status"),
            "reader_current_product_version": index.get("current_product_version"),
            "reader_verified_versions": [product_version] if locales else []}


def execute(args) -> dict:
    directory = Path(args.directory)
    directory.mkdir(parents=True, exist_ok=True)
    plan_bytes = Path(args.plan).read_bytes()
    store = None
    stage = "validate"
    generations = {}
    content_files = {}
    manifest = {}
    historical_package = False
    publication = None
    publication_bytes = b""
    publication_time_source = None
    product_version = ""
    marker = None
    published_at = args.published_at
    try:
        if args.action == "record-publication":
            publication, publication_bytes = write_publication_record(directory, args, plan_bytes)
            evidence = {
                "schema_version": 1, "action": args.action, "environment": args.environment,
                "stage": "product_release", "status": "verified", "product_status": "released",
                "plan_sha256": release_notes.digest_bytes(plan_bytes),
                "publication_evidence_sha256": release_notes.digest_bytes(publication_bytes),
                "operation": args.operation, "run_id": args.run_id, "run_attempt": args.run_attempt,
                "published_at": publication["published_at"],
            }
            write_evidence(directory, evidence)
            return evidence
        if args.action in ("prepare", "publish", "resend"):
            product_version, content_files, manifest = release_notes.validate_package(
                args.package_dir, expected_environment=args.environment,
                expected_artifact_digest=args.artifact_digest,
                artifact_metadata_digest=args.artifact_metadata_digest, plan_bytes=plan_bytes)
            historical_fields = release_notes.HISTORICAL_MANIFEST_FIELDS
            historical_package = historical_fields <= set(manifest)
            if historical_package:
                if args.action != "resend" or args.environment != "dev":
                    raise release_notes.ReleaseNotesError("validate", "historical-package-resend-required")
                release_notes.validate_historical_package_artifact_binding(
                    manifest, artifact_name=args.artifact_name,
                    result_artifact_id=args.result_artifact_id, operation=args.operation,
                    package_run_id=args.package_run_id,
                    package_run_attempt=args.package_run_attempt)
                sidecar_path = directory / "release-notes-operation.json"
                if sidecar_path.is_symlink() or sidecar_path.exists():
                    raise release_notes.ReleaseNotesError("service_readback", "historical-operation-sidecar-present")
            elif release_notes.is_historical_package_artifact_name(args.artifact_name):
                raise release_notes.ReleaseNotesError("validate", "historical-provenance-required")
            store = GCSCLIStore(args.bucket)
            if args.action in ("publish", "resend"):
                publication, publication_bytes = publication_record(directory, args, plan_bytes)
                if historical_package:
                    published_at = manifest["original_published_at"]
                    publication_time_source = "historical-manifest"
                elif publication is not None:
                    published_at = publication["published_at"]
                    publication_time_source = "retained-release-operation"
                else:
                    raise release_notes.ReleaseNotesError("product_release", "retained-publication-time-unavailable")
            stage = "content_upload"
            generations = release_notes.upload_content(store, content_files, product_version=product_version)
        if args.action == "verify-operation":
            stage = "product_release"
            bundle = validate_retained_operation(directory, args)
            evidence = {
                "schema_version": 1, "action": "verify-operation", "environment": args.environment,
                "stage": "product_release", "status": "verified", "product_status": "released",
                "plan_sha256": release_notes.digest_bytes(Path(args.plan).read_bytes()),
                "result_artifact_id": args.result_artifact_id or None,
                "operation": args.operation, "run_id": args.run_id, "run_attempt": args.run_attempt,
                "result_sha256": release_notes.digest_bytes((directory / "result.json").read_bytes()),
                "identity_sha256": release_notes.digest_bytes(
                    json.dumps(bundle["plan"]["identities"]["bff"], sort_keys=True, separators=(",", ":")).encode()),
            }
        elif args.action in ("publish", "skip", "marker", "resend"):
            if store is None:
                store = GCSCLIStore(args.bucket)
            stage = "service_readback"
            marker, sidecar_bytes = operation_marker(directory, args)
            if args.action == "resend" and marker is not None and not release_notes_capture.active_marker_matches(
                    directory, args.operation, args.run_id, args.run_attempt, marker):
                marker = None
            publisher = PublisherStore(store, directory, args.operation, args.run_id, args.run_attempt)
            if args.action in ("skip", "marker"):
                stage = "index_update"
                result = release_notes.publish_marker_only(publisher, marker)
            else:
                stage = "index_update"
                result = release_notes.publish_package(
                    publisher, content_files, product_version=product_version,
                    published_at=published_at, marker=marker,
                    historical=args.action == "resend" and marker is None)
            stage = "exact_readback"
            api_readback = verify_reader_api(directory, args.environment, marker,
                product_version or (marker or {}).get("product_version", ""), sorted(content_files),
                historical=args.action == "resend" and marker is None)
            evidence = {
                "schema_version": 1, "action": args.action, "environment": args.environment,
                "stage": "exact_readback", "status": "verified", "product_status": args.product_status,
                "plan_sha256": release_notes.digest_bytes(plan_bytes), "artifact_id": args.artifact_id or None,
                "result_artifact_id": args.result_artifact_id or None,
                "artifact_digest": args.artifact_digest, "product_version": product_version or marker["product_version"],
                "manifest_provenance_sha256": release_notes.digest_bytes(
                    json.dumps({key: value for key, value in manifest.items() if key not in ("files",)},
                               sort_keys=True, separators=(",", ":")).encode()),
                "content_sha256": {locale: release_notes.digest_bytes(data) for locale, data in content_files.items()},
                "content_generations": generations, "index_generation": result.index_generation,
                "operation_sidecar_sha256": release_notes.digest_bytes(sidecar_bytes) if sidecar_bytes else None,
                "publication_evidence_sha256": release_notes.digest_bytes(publication_bytes) if publication_bytes else None,
                "reader_api_readback": api_readback,
                "operation": args.operation, "run_id": args.run_id, "run_attempt": args.run_attempt,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
                "published_at": published_at,
                "published_at_source": publication_time_source,
                "currentness": "verified" if marker is not None else "history-only",
                "imported_at": manifest.get("imported_at") if publication_time_source == "historical-manifest" else None,
                "historical_source_record_ref": manifest.get("historical_source_record_ref") if publication_time_source == "historical-manifest" else None,
                "historical_package_artifact_name": args.artifact_name if historical_package else None,
                "historical_result_artifact_id": args.result_artifact_id if historical_package else None,
            }
        elif args.action == "skip-before-release":
            evidence = {
                "schema_version": 1, "action": args.action, "environment": args.environment,
                "stage": "content_upload", "status": "skipped", "product_status": "not_started",
                "plan_sha256": release_notes.digest_bytes(plan_bytes), "artifact_id": args.artifact_id or None,
                "result_artifact_id": args.result_artifact_id or None,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
                "owner_choice_required": False, "automatic_skip": False,
            }
        elif args.action == "skip-after-release":
            release_notes.validate_native_operation(
                plan_bytes, (directory / "state.json").read_bytes(),
                (directory / "result.json").read_bytes(),
                (directory / "receipts" / "bff.json").read_bytes(),
                environment=args.environment, workflow_operation=args.operation,
                run_id=args.run_id, run_attempt=args.run_attempt)
            evidence = {
                "schema_version": 1, "action": args.action, "environment": args.environment,
                "stage": "owner_disposition", "status": "skipped", "product_status": "released",
                "plan_sha256": release_notes.digest_bytes(plan_bytes), "artifact_id": args.artifact_id or None,
                "operation": args.operation, "run_id": args.run_id, "run_attempt": args.run_attempt,
                "result_artifact_id": args.result_artifact_id or None,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
                "notes_currentness": "not_claimed", "owner_choice_required": False,
                "automatic_skip": False, "product_rollback": False, "product_redeploy": False,
            }
        elif args.action == "stop":
            if args.product_status == "released":
                release_notes.validate_native_operation(
                    plan_bytes, (directory / "state.json").read_bytes(),
                    (directory / "result.json").read_bytes(),
                    (directory / "receipts" / "bff.json").read_bytes(),
                    environment=args.environment, workflow_operation=args.operation,
                    run_id=args.run_id, run_attempt=args.run_attempt)
            evidence = {
                "schema_version": 1, "action": "stop", "environment": args.environment,
                "stage": "stopped", "status": "stopped", "product_status": args.product_status,
                "plan_sha256": release_notes.digest_bytes(plan_bytes), "artifact_id": args.artifact_id or None,
                "operation": args.operation, "run_id": args.run_id, "run_attempt": args.run_attempt,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
                "owner_choice_required": False, "automatic_skip": False,
                "product_rollback": False, "product_redeploy": False,
            }
        elif args.action == "artifact-failure":
            evidence = {
                "schema_version": 1, "action": "prepare", "environment": args.environment,
                "stage": "validate", "status": "failed", "safe_class": args.failure_class,
                "product_status": "not_started", "plan_sha256": release_notes.digest_bytes(plan_bytes),
                "artifact_id": args.artifact_id or None, "next_actions": ["retry", "skip", "stop"],
                "owner_choice_required": True, "automatic_skip": False,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
            }
        else:
            evidence = {
                "schema_version": 1, "action": "prepare", "environment": args.environment,
                "stage": "content_upload", "status": "verified", "product_status": "not_started",
                "plan_sha256": release_notes.digest_bytes(plan_bytes), "artifact_id": args.artifact_id,
                "artifact_digest": args.artifact_digest, "product_version": product_version,
                "content_sha256": {locale: release_notes.digest_bytes(data) for locale, data in content_files.items()},
                "content_generations": generations,
                "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
            }
        write_evidence(directory, evidence)
        return evidence
    except release_notes.ReleaseNotesError as error:
        evidence = {
            "schema_version": 1, "action": args.action, "environment": args.environment,
            "stage": error.stage or stage, "status": "failed", "safe_class": error.safe_class,
            "product_status": args.product_status, "plan_sha256": release_notes.digest_bytes(plan_bytes),
            "artifact_id": args.artifact_id or None, "result_artifact_id": args.result_artifact_id or None,
            "artifact_digest": args.artifact_digest or None,
            "content_sha256": {locale: release_notes.digest_bytes(data) for locale, data in content_files.items()},
            "content_generations": generations, "next_actions": ["retry", "skip", "stop"],
            "owner_choice_required": True, "automatic_skip": False,
            "decision_actor": args.decision_actor or None, "decision_time": args.decision_time or None,
        }
        write_evidence(directory, evidence)
        raise
    except (OSError, ValueError, TypeError):
        evidence = {
            "schema_version": 1, "action": args.action, "environment": args.environment,
            "stage": stage, "status": "failed", "safe_class": "stage-input-invalid",
            "product_status": args.product_status, "plan_sha256": release_notes.digest_bytes(plan_bytes),
            "result_artifact_id": args.result_artifact_id or None,
            "artifact_id": args.artifact_id, "artifact_digest": args.artifact_digest or None,
            "next_actions": ["retry", "skip", "stop"], "owner_choice_required": True,
            "automatic_skip": False,
        }
        write_evidence(directory, evidence)
        raise release_notes.ReleaseNotesError(stage, "stage-input-invalid") from None


def parser():
    result = argparse.ArgumentParser()
    result.add_argument("--action", required=True, choices=("prepare", "publish", "skip", "marker", "resend", "verify-operation", "record-publication", "skip-before-release", "skip-after-release", "stop", "artifact-failure"))
    result.add_argument("--directory", required=True, help="retained workflow artifact directory")
    result.add_argument("--package-dir", default="")
    result.add_argument("--environment", required=True, choices=("dev", "prod"))
    result.add_argument("--bucket", required=True)
    result.add_argument("--artifact-id", default="")
    result.add_argument("--result-artifact-id", default="")
    result.add_argument("--artifact-digest", default="")
    result.add_argument("--artifact-metadata-digest", default="")
    result.add_argument("--artifact-name", default="")
    result.add_argument("--package-run-id", default="")
    result.add_argument("--package-run-attempt", default="")
    result.add_argument("--plan", required=True)
    result.add_argument("--operation", required=True, choices=sorted(release_notes.TRANSITION_OPS))
    result.add_argument("--run-id", required=True)
    result.add_argument("--run-attempt", required=True, type=int)
    result.add_argument("--product-status", default="not_started",
                        choices=("not_started", "in_progress", "released", "failed"))
    result.add_argument("--published-at", default="")
    result.add_argument("--decision-actor", default=os.getenv("GITHUB_ACTOR", ""))
    result.add_argument("--decision-time", default=os.getenv("GITHUB_RUN_STARTED_AT") or datetime.now(timezone.utc).isoformat())
    result.add_argument("--failure-class", default="artifact-unavailable",
                        choices=("artifact-id-required", "artifact-metadata-unavailable", "artifact-metadata-invalid", "artifact-download-failed"))
    return result


def main():
    args = parser().parse_args()
    try:
        evidence = execute(args)
    except release_notes.ReleaseNotesError as error:
        print(json.dumps({"stage": error.stage, "status": "failed", "safe_class": error.safe_class}, sort_keys=True))
        return 1
    print(json.dumps({key: evidence[key] for key in ("action", "stage", "status", "product_status", "index_generation")
                      if key in evidence}, sort_keys=True))
    if evidence.get("status") == "failed":
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
