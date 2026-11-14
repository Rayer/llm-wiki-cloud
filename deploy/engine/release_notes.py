"""Release Notes package validation and operation-bound index publication.

This module is pure by default: callers provide object-store and active-service
readback adapters. Importing it never initializes cloud credentials.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import re
from typing import Callable, Protocol


SCHEMA_VERSION = 1
MAX_CONTENT_BYTES = 64 * 1024
MAX_INDEX_BYTES = 1024 * 1024
MAX_OPERATION_ARTIFACT_BYTES = 16 * 1024 * 1024
MAX_VERSIONS = 1000
VERSION_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$")
SHA_RE = re.compile(r"^[0-9a-f]{64}$")
MARKER_FIELDS = {
    "schema_version", "environment", "product_version", "commit", "branch", "tag", "image_tag",
    "image_digest", "service", "service_uid", "service_generation", "revision", "operation",
    "deployment_evidence_digest",
}
SIDECAR_FIELDS = {
    "schema_version", "environment", "operation", "run_id", "run_attempt", "plan_sha256",
    "state_sha256", "result_sha256", "service", "service_uid", "service_generation", "revision",
    "image_digest", "traffic", "build_info",
}
BUILD_INFO_FIELDS = {"environment", "product_version", "commit", "branch", "tag", "image_tag", "service", "revision"}
CONTENT_FIELDS = {"schema_version", "product", "product_version", "locale", "entries"}
ENTRY_FIELDS = {"title", "what_changed", "user_benefit", "where_to_use", "limitations"}
INDEX_FIELDS = {"schema_version", "versions", "current_deployment"}
ROW_FIELDS = {"product_version", "published_at", "locales"}
LOCALE_FILE_FIELDS = {"sha256", "object_path"}
TRANSITION_OPS = {"release", "deploy", "reactivate", "promote", "rollback"}
STAGES = {"validate", "content_upload", "product_release", "index_update", "exact_readback"}


class ReleaseNotesError(Exception):
    def __init__(self, stage: str, safe_class: str):
        self.stage = stage
        self.safe_class = safe_class
        super().__init__(safe_class)


def _require(condition: bool, stage: str, safe_class: str):
    if not condition:
        raise ReleaseNotesError(stage, safe_class)


def _json_object(data: bytes, stage: str, limit: int, fields: set[str] | None = None) -> dict:
    _require(isinstance(data, bytes) and 0 < len(data) <= limit, stage, "invalid-size")
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError):
        raise ReleaseNotesError(stage, "invalid-json") from None
    _require(isinstance(value, dict), stage, "invalid-shape")
    if fields is not None:
        _require(set(value) <= fields, stage, "unknown-fields")
    return value


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def _valid_timestamp(value) -> bool:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return parsed.tzinfo is not None and parsed.utcoffset() is not None
    except (AttributeError, TypeError, ValueError):
        return False


def digest_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def valid_version(value) -> bool:
    if not isinstance(value, str) or VERSION_RE.fullmatch(value) is None:
        return False
    match = VERSION_RE.fullmatch(value)
    for part in (match.group(4) or "").split("."):
        if part.isdigit() and len(part) > 1 and part.startswith("0"):
            return False
    return True


def exact_object_path(version: str, locale: str, digest: str) -> str:
    return f"release-notes/v1/versions/{version}/{locale}/{digest}.json"


def _validate_text(value, limit: int) -> bool:
    return isinstance(value, str) and bool(value.strip()) and len(value) <= limit


def validate_content(data: bytes, expected_version: str, expected_locale: str) -> dict:
    content = _json_object(data, "validate", MAX_CONTENT_BYTES, CONTENT_FIELDS)
    _require(set(content) == CONTENT_FIELDS and type(content.get("schema_version")) is int and content.get("schema_version") == SCHEMA_VERSION and
             content.get("product") == "llm-wiki-cloud" and content.get("product_version") == expected_version and
             valid_version(expected_version) and content.get("locale") == expected_locale and
             expected_locale in ("en", "zh-TW") and isinstance(content.get("entries"), list) and
             1 <= len(content["entries"]) <= 20, "validate", "invalid-content-contract")
    for entry in content["entries"]:
        _require(isinstance(entry, dict) and set(entry) == ENTRY_FIELDS and
                 _validate_text(entry["title"], 160) and _validate_text(entry["what_changed"], 2000) and
                 _validate_text(entry["user_benefit"], 2000) and _validate_text(entry["where_to_use"], 2000) and
                 isinstance(entry["limitations"], list) and len(entry["limitations"]) <= 6 and
                 all(_validate_text(item, 1000) for item in entry["limitations"]),
                 "validate", "invalid-entry")
    return content


def validate_package(directory, *, expected_environment: str, expected_artifact_digest: str,
                     artifact_metadata_digest: str, plan_bytes: bytes) -> tuple[str, dict[str, bytes], dict]:
    """Validate only the retained TPM-authored package before any storage write."""
    from pathlib import Path

    root = Path(directory)
    _require(root.is_dir() and not root.is_symlink(), "validate", "package-unavailable")
    manifest_path = root / "release-notes-manifest.json"
    try:
        manifest_bytes = manifest_path.read_bytes()
    except OSError:
        raise ReleaseNotesError("validate", "package-manifest-unavailable") from None
    manifest_fields = {
        "schema_version", "product", "product_version", "environment", "release_plan_sha256",
        "release_note_snapshot_ref", "release_note_snapshot_sha256", "delivered_scope_evidence_sha256", "files",
    }
    historical_fields = {"original_published_at", "historical_source_record_ref", "historical_source_record_sha256", "imported_at"}
    manifest = _json_object(manifest_bytes, "validate", 32 * 1024, manifest_fields | historical_fields)
    present_historical = set(manifest).intersection(historical_fields)
    _require(manifest_fields <= set(manifest) and (not present_historical or present_historical == historical_fields) and
             type(manifest.get("schema_version")) is int and manifest.get("schema_version") == SCHEMA_VERSION and
             manifest.get("product") == "llm-wiki-cloud" and valid_version(manifest.get("product_version")) and
             manifest.get("environment") == expected_environment and
             manifest.get("release_plan_sha256") == digest_bytes(plan_bytes) and
             isinstance(manifest.get("release_note_snapshot_ref"), str) and manifest["release_note_snapshot_ref"] and
             re.fullmatch(r"sha256:[0-9a-f]{64}", manifest.get("release_note_snapshot_sha256", "")) is not None and
             re.fullmatch(r"sha256:[0-9a-f]{64}", manifest.get("delivered_scope_evidence_sha256", "")) is not None and
             isinstance(manifest.get("files"), dict) and 1 <= len(manifest["files"]) <= 2 and
             set(manifest["files"]) <= {"en", "zh-TW"}, "validate", "invalid-package-manifest")
    if present_historical:
        _require(_valid_timestamp(manifest["original_published_at"]) and _valid_timestamp(manifest["imported_at"]) and
                 isinstance(manifest["historical_source_record_ref"], str) and manifest["historical_source_record_ref"].strip() and
                 re.fullmatch(r"sha256:[0-9a-f]{64}", manifest["historical_source_record_sha256"]) is not None,
                 "validate", "historical-provenance-invalid")
    artifact_digest = expected_artifact_digest.removeprefix("sha256:")
    metadata_digest = artifact_metadata_digest.removeprefix("sha256:")
    _require(SHA_RE.fullmatch(artifact_digest) is not None and SHA_RE.fullmatch(metadata_digest) is not None and
             artifact_digest == metadata_digest, "validate", "artifact-digest-mismatch")
    package_files = {"release-notes-manifest.json"}
    files = {}
    for locale, entry in manifest["files"].items():
        _require(isinstance(entry, dict) and set(entry) == {"path", "sha256"} and entry.get("path") == f"{locale}.json" and
                 isinstance(entry.get("sha256"), str) and SHA_RE.fullmatch(entry["sha256"]) is not None,
                 "validate", "invalid-package-file-entry")
        path = root / entry["path"]
        _require(path.parent == root and path.is_file() and not path.is_symlink(), "validate", "package-file-unavailable")
        body = path.read_bytes()
        _require(digest_bytes(body) == entry["sha256"], "validate", "package-file-digest-mismatch")
        validate_content(body, manifest["product_version"], locale)
        package_files.add(entry["path"])
        files[locale] = body
    actual = set()
    for path in root.iterdir():
        _require(not path.is_symlink() and path.is_file(), "validate", "package-path-invalid")
        actual.add(path.name)
    _require(actual == package_files, "validate", "unexpected-package-file")
    return manifest["product_version"], files, manifest


def upload_content(store: NotesStore, files: dict[str, bytes], *, product_version: str) -> dict[str, int]:
    """Validate the whole set before writing, then create and read back immutable bodies."""
    prepared = {}
    try:
        for locale, body in files.items():
            validate_content(body, product_version, locale)
            digest = digest_bytes(body)
            prepared[exact_object_path(product_version, locale, digest)] = body
        _require(bool(prepared) and len(prepared) <= 2, "validate", "no-supported-locale")
    except ReleaseNotesError:
        raise
    except Exception:
        raise ReleaseNotesError("validate", "invalid-package-content") from None

    generations = {}
    for path, body in prepared.items():
        try:
            generation = store.create_content(path, body)
            if store.read_content(path) != body:
                raise ReleaseNotesError("exact_readback", "content-readback-mismatch")
            generations[path] = generation
        except ReleaseNotesError:
            raise
        except Exception:
            raise ReleaseNotesError("content_upload", "content-stage-failed") from None
    return generations


def validate_index(data: bytes) -> dict:
    index = _json_object(data, "index_update", MAX_INDEX_BYTES, INDEX_FIELDS)
    _require(type(index.get("schema_version")) is int and index.get("schema_version") == SCHEMA_VERSION and isinstance(index.get("versions"), list) and
             len(index["versions"]) <= MAX_VERSIONS, "index_update", "invalid-index")
    seen = set()
    for row in index["versions"]:
        _require(isinstance(row, dict) and set(row) == ROW_FIELDS and valid_version(row.get("product_version")) and
                 row["product_version"] not in seen and isinstance(row.get("locales"), dict) and
                 1 <= len(row["locales"]) <= 2, "index_update", "invalid-version-row")
        seen.add(row["product_version"])
        _require(_valid_timestamp(row["published_at"]), "index_update", "invalid-published-at")
        for locale, entry in row["locales"].items():
            _require(locale in ("en", "zh-TW") and isinstance(entry, dict) and set(entry) == LOCALE_FILE_FIELDS and
                     isinstance(entry.get("sha256"), str) and SHA_RE.fullmatch(entry["sha256"]) is not None and
                     entry.get("object_path") == exact_object_path(row["product_version"], locale, entry["sha256"]),
                     "index_update", "invalid-locale-entry")
    if "current_deployment" in index:
        _validate_marker(index["current_deployment"])
    return index


def _validate_marker(marker: dict):
    _require(isinstance(marker, dict) and set(marker) == MARKER_FIELDS and type(marker.get("schema_version")) is int and marker.get("schema_version") == SCHEMA_VERSION and
             marker.get("environment") in ("local", "dev", "prod") and valid_version(marker.get("product_version")) and
             all(isinstance(marker.get(key), str) and marker.get(key) for key in
                 ("commit", "branch", "image_tag", "service", "service_uid", "revision")) and
             isinstance(marker.get("tag"), str) and type(marker.get("service_generation")) is int and
             marker.get("service_generation") > 0 and
             re.fullmatch(r"sha256:[0-9a-f]{64}", marker.get("image_digest", "")) is not None and
             re.fullmatch(r"sha256:[0-9a-f]{64}", marker.get("deployment_evidence_digest", "")) is not None and
             marker.get("operation") in TRANSITION_OPS, "index_update", "invalid-current-marker")


def normalize_operation(plan_bytes: bytes, state_bytes: bytes, result_bytes: bytes, sidecar_bytes: bytes,
                        *, environment: str, workflow_operation: str, run_id: str, run_attempt: int,
                        service: str, receipt_bytes: bytes) -> tuple[dict, dict]:
    plan = _json_object(plan_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
    state = _json_object(state_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
    result = _json_object(result_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
    sidecar = _json_object(sidecar_bytes, "index_update", MAX_INDEX_BYTES, SIDECAR_FIELDS)
    normalized = plan.get("normalized")
    identities = plan.get("identities")
    if not isinstance(normalized, dict) or not isinstance(identities, dict):
        raise ReleaseNotesError("index_update", "operation-evidence-mismatch")
    plan_environment = {"development": "dev", "production": "prod", "dev": "dev", "prod": "prod", "local": "local"}.get(
        normalized.get("environment"))
    identity = identities.get("bff")
    plan_id = plan.get("id")
    plan_tag = plan.get("tag")
    plan_source = plan.get("source")
    state_sequence = state.get("sequence")
    component = state.get("components", {}).get("bff") if isinstance(state.get("components"), dict) else None
    bff_target = normalized.get("bff")
    if not isinstance(identity, dict) or not isinstance(plan_id, str) or not plan_id or \
            not isinstance(plan_tag, str) or not plan_tag or not isinstance(plan_source, str) or \
            re.fullmatch(r"[0-9a-f]{40}", plan_source) is None or type(state_sequence) is not int or state_sequence < 0 or \
            not isinstance(component, dict) or not isinstance(bff_target, dict) or bff_target.get("service_name") != service:
        raise ReleaseNotesError("index_update", "operation-evidence-mismatch")
    if not re.fullmatch(r"[1-9][0-9]*", run_id) or type(run_attempt) is not int or run_attempt < 1:
        raise ReleaseNotesError("index_update", "operation-evidence-mismatch")
    _require(set(sidecar) == SIDECAR_FIELDS and type(sidecar.get("schema_version")) is int and sidecar.get("schema_version") == SCHEMA_VERSION and
             sidecar.get("environment") == environment == plan_environment and sidecar.get("operation") == workflow_operation and
             sidecar.get("run_id") == run_id and type(sidecar.get("run_attempt")) is int and sidecar.get("run_attempt") == run_attempt and
             sidecar.get("service") == service and sidecar.get("plan_sha256") == digest_bytes(plan_bytes) and
             sidecar.get("state_sha256") == digest_bytes(state_bytes) and sidecar.get("result_sha256") == digest_bytes(result_bytes),
             "index_update", "operation-evidence-mismatch")
    build = sidecar.get("build_info")
    _require(isinstance(build, dict) and set(build) == BUILD_INFO_FIELDS and build.get("environment") == environment and
             valid_version(build.get("product_version")) and build.get("service") == service and
             build.get("revision") == sidecar.get("revision") and build.get("revision") not in ("", "unknown") and
             all(isinstance(build.get(key), str) for key in ("commit", "branch", "tag", "image_tag")) and
             re.fullmatch(r"[0-9a-f]{40}", build.get("commit", "")) is not None and
             build.get("branch") not in ("", "unknown") and
             build.get("image_tag") not in ("", "unknown"), "index_update", "runtime-build-identity-unavailable")
    _require(sidecar.get("service_uid") and isinstance(sidecar.get("service_uid"), str) and
             type(sidecar.get("service_generation")) is int and sidecar.get("service_generation") > 0 and
             isinstance(sidecar.get("image_digest"), str) and re.fullmatch(r"sha256:[0-9a-f]{64}", sidecar["image_digest"]) is not None,
             "index_update", "service-transition-unavailable")
    traffic = sidecar.get("traffic")
    _require(isinstance(traffic, list) and len(traffic) == 1 and isinstance(traffic[0], dict) and
             traffic[0].get("revisionName") == sidecar.get("revision") and traffic[0].get("percent") == 100 and
             not traffic[0].get("tag"), "index_update", "active-traffic-unverified")
    expected = result.get("expected")
    observed_result = result.get("observed")
    result_component = result.get("component")
    result_entry = state.get("components", {}).get(result_component) if isinstance(result_component, str) and isinstance(state.get("components"), dict) else None
    result_identity = identities.get(result_component) if isinstance(result_component, str) else None
    _require(isinstance(expected, dict) and isinstance(observed_result, dict) and
             workflow_operation in TRANSITION_OPS and isinstance(plan.get("selected"), list) and "bff" in plan["selected"] and
             result_component in plan["selected"] and isinstance(result_entry, dict) and isinstance(result_identity, dict) and
             state.get("plan") == plan_id and component.get("status") in ("verified", "rolled_back") and
             result.get("release") == plan_tag and result.get("attempt") == plan_id and
             expected.get("source") == plan_source and
             expected.get("identity") == digest_bytes(
                 json.dumps(result_identity, sort_keys=True, separators=(",", ":")).encode()),
             "index_update", "native-operation-unsuccessful")
    receipt = _json_object(receipt_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
    artifact = receipt.get("artifact")
    image = artifact.get("image") if isinstance(artifact, dict) else None
    _require(receipt.get("component") == "bff" and receipt.get("identity") == identity and
             isinstance(image, str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image) is not None,
             "index_update", "selected-target-evidence-invalid")
    if workflow_operation == "rollback":
        entry = component
        prior = entry.get("prior")
        _require(result.get("stage") == "rolled_back" and result.get("status") == "rolled_back" and
                 result_entry.get("status") == "rolled_back" and observed_result.get("component_status") == "rolled_back" and
                 type(result.get("last_verified_checkpoint")) is int and result.get("last_verified_checkpoint") == state_sequence and state.get("status") == "rolled_back" and
                 entry.get("status") == "rolled_back" and result.get("prior") == result_entry.get("prior") and result.get("candidate") == result_entry.get("candidate"),
                 "index_update", "explicit-rollback-evidence-invalid")
        _require(isinstance(prior, dict) and set(prior) == {"revision", "image", "traffic"} and
                 sidecar.get("revision") == prior.get("revision") and
                 isinstance(prior.get("image"), str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", prior["image"]) is not None and
                 prior["image"].rsplit("@", 1)[-1] == sidecar.get("image_digest") and
                 isinstance(prior.get("traffic"), list) and
                 len(prior["traffic"]) == 1 and isinstance(prior["traffic"][0], dict) and
                 prior["traffic"][0].get("revisionName") == prior.get("revision") and
                 prior["traffic"][0].get("percent") == 100 and not prior["traffic"][0].get("tag"),
                 "index_update", "rollback-prior-readback-mismatch")
    else:
        _require(result.get("stage") == "success" and result.get("status") == "success" and state.get("status") == "success" and
                 type(result.get("last_verified_checkpoint")) is int and result.get("last_verified_checkpoint") == state_sequence and
                 component.get("status") == "verified" and result_entry.get("status") == "verified" and
                 observed_result.get("component_status") == "verified" and result.get("candidate") == result_entry.get("candidate"),
                 "index_update", "successful-operation-required")
        candidate = component.get("candidate")
        _require(isinstance(candidate, dict) and isinstance(candidate.get("revision"), str) and
                 candidate.get("revision") == sidecar.get("revision") and
                 isinstance(candidate.get("image"), str) and
                 candidate["image"].rsplit("@", 1)[-1] == sidecar.get("image_digest") and
                 receipt["artifact"]["image"].rsplit("@", 1)[-1] == sidecar.get("image_digest"),
                 "index_update", "selected-target-readback-mismatch")
    evidence_digest = digest_bytes(json.dumps(sidecar, sort_keys=True, separators=(",", ":")).encode("utf-8"))
    marker = {
        "schema_version": SCHEMA_VERSION, "environment": environment, "product_version": build["product_version"],
        "commit": build["commit"], "branch": build["branch"], "tag": build["tag"], "image_tag": build["image_tag"],
        "image_digest": sidecar["image_digest"], "service": service, "service_uid": sidecar["service_uid"],
        "service_generation": sidecar["service_generation"], "revision": sidecar["revision"],
        "operation": workflow_operation, "deployment_evidence_digest": "sha256:" + evidence_digest,
    }
    _validate_marker(marker)
    return marker, {"plan": plan, "state": state, "result": result, "sidecar": sidecar,
                    "receipt": receipt}


def validate_native_operation(plan_bytes: bytes, state_bytes: bytes, result_bytes: bytes, receipt_bytes: bytes,
                              *, environment: str, workflow_operation: str, run_id: str,
                              run_attempt: int) -> dict:
    """Verify the retained product operation without querying or changing a provider."""
    plan = _json_object(plan_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    state = _json_object(state_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    result = _json_object(result_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    normalized = plan.get("normalized")
    identities = plan.get("identities")
    plan_id, plan_tag, plan_source = plan.get("id"), plan.get("tag"), plan.get("source")
    target_environment = {"development": "dev", "production": "prod"}.get(
        normalized.get("environment") if isinstance(normalized, dict) else None)
    identity = identities.get("bff") if isinstance(identities, dict) else None
    sequence = state.get("sequence")
    components = state.get("components")
    component = components.get("bff") if isinstance(components, dict) else None
    target = normalized.get("bff") if isinstance(normalized, dict) else None
    if workflow_operation not in TRANSITION_OPS or not re.fullmatch(r"[1-9][0-9]*", run_id) or \
            type(run_attempt) is not int or run_attempt < 1:
        raise ReleaseNotesError("product_release", "operation-evidence-mismatch")
    _require(target_environment == environment and isinstance(target, dict) and
             isinstance(identity, dict) and isinstance(plan_id, str) and bool(plan_id) and
             isinstance(plan_tag, str) and bool(plan_tag) and isinstance(plan_source, str) and
             re.fullmatch(r"[0-9a-f]{40}", plan_source) is not None and
             isinstance(plan.get("selected"), list) and "bff" in plan["selected"] and
             type(sequence) is int and sequence >= 0 and isinstance(component, dict) and
             state.get("plan") == plan_id and target.get("service_name"),
             "product_release", "operation-evidence-mismatch")
    expected = result.get("expected")
    observed = result.get("observed")
    result_component = result.get("component")
    result_entry = components.get(result_component) if isinstance(components, dict) and isinstance(result_component, str) else None
    result_identity = identities.get(result_component) if isinstance(identities, dict) and isinstance(result_component, str) else None
    _require(result.get("release") == plan_tag and result.get("attempt") == plan_id and
             isinstance(plan.get("selected"), list) and result_component in plan["selected"] and
             isinstance(result_entry, dict) and isinstance(result_identity, dict) and
             isinstance(expected, dict) and expected.get("source") == plan_source and
             expected.get("identity") == digest_bytes(
                 json.dumps(result_identity, sort_keys=True, separators=(",", ":")).encode()) and
             type(result.get("last_verified_checkpoint")) is int and
             result.get("last_verified_checkpoint") == sequence and
             isinstance(observed, dict),
             "product_release", "native-operation-evidence-invalid")
    if workflow_operation == "rollback":
        prior = component.get("prior")
        _require(state.get("status") == "rolled_back" and result.get("stage") == "rolled_back" and
                 result.get("status") == "rolled_back" and component.get("status") == "rolled_back" and
                 result_entry.get("status") == "rolled_back" and
                 observed.get("component_status") == "rolled_back" and
                 result.get("prior") == result_entry.get("prior") and result.get("candidate") == result_entry.get("candidate") and
                 isinstance(prior, dict) and set(prior) == {"revision", "image", "traffic"} and
                 isinstance(prior.get("traffic"), list) and len(prior["traffic"]) == 1 and
                 isinstance(prior["traffic"][0], dict) and
                 prior["traffic"][0].get("revisionName") == prior.get("revision") and
                 prior["traffic"][0].get("percent") == 100 and not prior["traffic"][0].get("tag"),
                 "product_release", "native-operation-unsuccessful")
    else:
        candidate = component.get("candidate")
        _require(state.get("status") == "success" and result.get("stage") == "success" and
                 result.get("status") == "success" and component.get("status") == "verified" and
                 result_entry.get("status") == "verified" and observed.get("component_status") == "verified" and
                 result.get("candidate") == result_entry.get("candidate") and
                 isinstance(candidate, dict) and bool(candidate.get("revision")),
                 "product_release", "native-operation-unsuccessful")
    receipt = _json_object(receipt_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    artifact = receipt.get("artifact")
    image = artifact.get("image") if isinstance(artifact, dict) else None
    _require(receipt.get("component") == "bff" and receipt.get("identity") == identity and
             isinstance(image, str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image) is not None,
             "product_release", "selected-target-evidence-invalid")
    if workflow_operation != "rollback":
        candidate = component.get("candidate")
        _require(isinstance(candidate, dict) and candidate.get("image") == image,
                 "product_release", "selected-target-evidence-invalid")
    return {"plan": plan, "state": state, "result": result, "receipt": receipt}


def marker_transition_allowed(previous: dict | None, candidate: dict) -> bool:
    if previous is None:
        return True
    _validate_marker(previous)
    _validate_marker(candidate)
    if previous == candidate:
        return True
    if previous["service_uid"] != candidate["service_uid"]:
        return True
    if candidate["service_generation"] < previous["service_generation"]:
        return False
    if candidate["service_generation"] == previous["service_generation"]:
        return False
    return True


class NotesStore(Protocol):
    def read_index(self) -> tuple[bytes | None, int]: ...
    def create_content(self, path: str, data: bytes) -> int: ...
    def compare_and_swap_index(self, data: bytes, generation: int) -> int: ...
    def read_content(self, path: str) -> bytes: ...
    def active_marker_matches(self, marker: dict) -> bool: ...


@dataclass
class PublishResult:
    stage: str
    index_generation: int | None
    content_generations: dict[str, int]


def publish_package(store: NotesStore, files: dict[str, bytes], *, product_version: str,
                    published_at: str, marker: dict | None, historical: bool = False,
                    max_cas_attempts: int = 3) -> PublishResult:
    """Create immutable locale bodies then merge a single row with GCS CAS."""
    try:
        upload_generations = upload_content(store, files, product_version=product_version)
        validated = {}
        for locale, body in files.items():
            digest = digest_bytes(body)
            path = exact_object_path(product_version, locale, digest)
            validated[locale] = {"sha256": digest, "object_path": path, "body": body,
                                 "generation": upload_generations[path]}
        _require(marker is not None or historical, "validate", "release-transition-required")
        if marker is not None:
            _validate_marker(marker)
            _require(marker["product_version"] == product_version, "validate", "package-runtime-version-mismatch")
        _require(_valid_timestamp(published_at), "validate", "invalid-published-at")
        prepared = {locale: {key: item[key] for key in ("sha256", "object_path", "generation")}
                    for locale, item in validated.items()}
    except ReleaseNotesError:
        raise
    except Exception:
        raise ReleaseNotesError("content_upload", "content-stage-failed") from None

    for _ in range(max_cas_attempts):
        try:
            current_bytes, generation = store.read_index()
        except Exception:
            raise ReleaseNotesError("index_update", "index-read-failed") from None
        if current_bytes is None:
            index = {"schema_version": SCHEMA_VERSION, "versions": []}
        else:
            index = validate_index(current_bytes)
        previous_marker = index.get("current_deployment")
        if marker is not None:
            try:
                active_matches = store.active_marker_matches(marker)
            except Exception:
                raise ReleaseNotesError("index_update", "release-transition-unavailable") from None
            if not active_matches or not marker_transition_allowed(previous_marker, marker):
                raise ReleaseNotesError("index_update", "release-transition-stale")
        versions = index["versions"]
        existing = next((row for row in versions if row["product_version"] == product_version), None)
        if existing:
            old_locales = existing["locales"]
            updated_locales = dict(old_locales)
            updated_locales.update({locale: {"sha256": item["sha256"], "object_path": item["object_path"]}
                                    for locale, item in prepared.items()})
            if updated_locales == old_locales and (marker is None or previous_marker == marker):
                return PublishResult("exact_readback", generation, {locale: item["generation"] for locale, item in prepared.items()})
            existing["locales"] = updated_locales
            # Corrections and history backfill never fabricate a new product release time.
        else:
            versions.append({"product_version": product_version, "published_at": published_at,
                             "locales": {locale: {"sha256": item["sha256"], "object_path": item["object_path"]}
                                         for locale, item in prepared.items()}})
        versions.sort(key=lambda row: (datetime.fromisoformat(row["published_at"].replace("Z", "+00:00")).astimezone(timezone.utc),
                                       row["product_version"]), reverse=True)
        if marker is not None:
            index["current_deployment"] = marker
        serialized = json.dumps(index, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
        _require(len(serialized) <= MAX_INDEX_BYTES, "index_update", "index-too-large")
        try:
            new_generation = store.compare_and_swap_index(serialized, generation)
        except Exception as error:
            if getattr(error, "safe_class", "") == "generation-conflict":
                continue
            raise ReleaseNotesError("index_update", "index-write-failed") from None
        try:
            readback, readback_generation = store.read_index()
        except Exception:
            raise ReleaseNotesError("exact_readback", "index-readback-unavailable") from None
        _require(readback == serialized and readback_generation == new_generation, "exact_readback", "index-readback-mismatch")
        validate_index(readback)
        return PublishResult("exact_readback", new_generation, {locale: item["generation"] for locale, item in prepared.items()})
    raise ReleaseNotesError("index_update", "index-conflict-exhausted")


def publish_marker_only(store: NotesStore, marker: dict, *, max_cas_attempts: int = 3) -> PublishResult:
    """Publish verified runtime state without touching content or publication time."""
    _validate_marker(marker)
    for _ in range(max_cas_attempts):
        try:
            current_bytes, generation = store.read_index()
            index = {"schema_version": SCHEMA_VERSION, "versions": []} if current_bytes is None else validate_index(current_bytes)
        except ReleaseNotesError:
            raise
        except Exception:
            raise ReleaseNotesError("index_update", "index-read-failed") from None
        previous = index.get("current_deployment")
        try:
            active_matches = store.active_marker_matches(marker)
        except Exception:
            raise ReleaseNotesError("index_update", "release-transition-unavailable") from None
        if not active_matches or not marker_transition_allowed(previous, marker):
            raise ReleaseNotesError("index_update", "release-transition-stale")
        if previous == marker:
            return PublishResult("exact_readback", generation, {})
        index["current_deployment"] = marker
        serialized = json.dumps(index, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
        try:
            new_generation = store.compare_and_swap_index(serialized, generation)
        except Exception as error:
            if getattr(error, "safe_class", "") == "generation-conflict":
                continue
            raise ReleaseNotesError("index_update", "index-write-failed") from None
        try:
            readback, readback_generation = store.read_index()
        except Exception:
            raise ReleaseNotesError("exact_readback", "index-readback-unavailable") from None
        _require(readback == serialized and readback_generation == new_generation, "exact_readback", "index-readback-mismatch")
        validate_index(readback)
        return PublishResult("exact_readback", new_generation, {})
    raise ReleaseNotesError("index_update", "index-conflict-exhausted")


def failure_disposition(stage: str, product_status: str) -> dict:
    """Machine-readable evidence; a caller still requires an explicit Owner choice."""
    _require(stage in STAGES and product_status in ("not_started", "in_progress", "released", "failed"),
             "validate", "invalid-failure-disposition")
    return {
        "schema_version": SCHEMA_VERSION,
        "stage": stage,
        "status": "failed",
        "product_status": product_status,
        "owner_choice_required": True,
        "allowed_next_actions": ["retry", "skip", "stop"],
        "automatic_skip": False,
        "product_rollback": False,
        "product_redeploy": False,
    }
