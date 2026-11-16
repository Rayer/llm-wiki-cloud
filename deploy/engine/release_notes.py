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
import unicodedata
from typing import Callable, Protocol


SCHEMA_VERSION = 1
INDEX_SCHEMA_VERSION = 2
MARKER_SCHEMA_VERSION = 2
LEGACY_RELEASE_VERSION = "1.0.0"
PUBLIC_RELEASE_VERSION = "2026.11.4"
HISTORICAL_MANIFEST_FIELDS = frozenset({
    "original_published_at", "historical_source_record_ref",
    "historical_source_record_sha256", "imported_at",
})
HISTORICAL_PACKAGE_NAME_RE = re.compile(
    r"^lwc-release-notes-.+-development-history-(?:release|deploy|rollback|reactivate|promote)-"
    r"[1-9][0-9]*-[1-9][0-9]*-[1-9][0-9]*$"
)
MAX_CONTENT_BYTES = 64 * 1024
MAX_INDEX_BYTES = 1024 * 1024
MAX_OPERATION_ARTIFACT_BYTES = 16 * 1024 * 1024
MAX_VERSIONS = 1000
VERSION_RE = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$")
SHA_RE = re.compile(r"^[0-9a-f]{64}$")
MARKER_V1_FIELDS = {
    "schema_version", "environment", "product_version", "commit", "branch", "tag", "image_tag",
    "image_digest", "service", "service_uid", "service_generation", "revision", "operation",
    "deployment_evidence_digest",
}
MARKER_V2_FIELDS = MARKER_V1_FIELDS | {
    "release_version", "operation_run_id", "operation_run_attempt", "component_builds", "previous_release",
}
SIDECAR_FIELDS = {
    "schema_version", "environment", "operation", "run_id", "run_attempt", "plan_sha256",
    "state_sha256", "result_sha256", "service", "service_uid", "service_generation", "revision",
    "image_digest", "traffic", "build_info",
}
BUILD_INFO_FIELDS = {"environment", "product_version", "commit", "branch", "tag", "image_tag", "service", "revision"}
CONTENT_FIELDS = {"schema_version", "product", "product_version", "locale", "entries"}
ENTRY_FIELDS = {"title", "what_changed", "user_benefit", "where_to_use", "limitations"}
INDEX_V1_FIELDS = {"schema_version", "versions", "current_deployment"}
INDEX_V2_FIELDS = INDEX_V1_FIELDS | {"legacy_aliases"}
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


def validate_package(directory, *, expected_environment: str, expected_artifact_digest: str | None,
                     artifact_metadata_digest: str | None, plan_bytes: bytes) -> tuple[str, dict[str, bytes], dict]:
    """Validate a TPM package before storage; both digests may be None before upload."""
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
    manifest = _json_object(manifest_bytes, "validate", 32 * 1024,
                            manifest_fields | HISTORICAL_MANIFEST_FIELDS)
    present_historical = set(manifest).intersection(HISTORICAL_MANIFEST_FIELDS)
    _require(manifest_fields <= set(manifest) and
             (not present_historical or present_historical == HISTORICAL_MANIFEST_FIELDS) and
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
    if expected_artifact_digest is not None or artifact_metadata_digest is not None:
        _require(isinstance(expected_artifact_digest, str) and isinstance(artifact_metadata_digest, str),
                 "validate", "artifact-digest-mismatch")
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


def historical_package_artifact_name(product_version: str, operation: str,
                                     result_artifact_id: str, package_run_id: str,
                                     package_run_attempt: str) -> str:
    """Name a private historical package artifact with its exact source result identity."""
    _require(valid_version(product_version) and operation in TRANSITION_OPS and
             all(isinstance(value, str) and re.fullmatch(r"[1-9][0-9]*", value) is not None
                 for value in (result_artifact_id, package_run_id, package_run_attempt)),
             "validate", "historical-artifact-identity-invalid")
    return (f"lwc-release-notes-{product_version}-development-history-"
            f"{operation}-{result_artifact_id}-{package_run_id}-{package_run_attempt}")


def is_historical_package_artifact_name(artifact_name: str) -> bool:
    return isinstance(artifact_name, str) and HISTORICAL_PACKAGE_NAME_RE.fullmatch(artifact_name) is not None


def validate_historical_package_artifact_binding(manifest: dict, *, artifact_name: str,
                                                 result_artifact_id: str, operation: str,
                                                 package_run_id: str,
                                                 package_run_attempt: str) -> str:
    """Require a historical package upload identity to name its exact resend source."""
    if not isinstance(manifest, dict) or not HISTORICAL_MANIFEST_FIELDS <= set(manifest):
        raise ReleaseNotesError("validate", "historical-provenance-required")
    expected = historical_package_artifact_name(
        manifest.get("product_version", ""), operation, result_artifact_id,
        package_run_id, package_run_attempt)
    _require(isinstance(artifact_name, str) and artifact_name == expected,
             "validate", "historical-artifact-binding-mismatch")
    return expected


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
    index = _json_object(data, "index_update", MAX_INDEX_BYTES, INDEX_V2_FIELDS)
    schema = index.get("schema_version")
    _require(type(schema) is int and schema in (SCHEMA_VERSION, INDEX_SCHEMA_VERSION) and
             (INDEX_V1_FIELDS >= set(index) and {"schema_version", "versions"} <= set(index)
              if schema == SCHEMA_VERSION else set(index) == INDEX_V2_FIELDS) and
             isinstance(index.get("versions"), list) and
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
    if "current_deployment" in index and index["current_deployment"] is not None:
        _validate_marker(index["current_deployment"])
        expected_marker_schema = SCHEMA_VERSION if schema == SCHEMA_VERSION else MARKER_SCHEMA_VERSION
        _require(index["current_deployment"]["schema_version"] == expected_marker_schema,
                 "index_update", "index-marker-schema-mismatch")
    if schema == INDEX_SCHEMA_VERSION:
        aliases = index.get("legacy_aliases")
        _require(isinstance(aliases, list) and len(aliases) <= 1, "index_update", "invalid-legacy-aliases")
        for alias in aliases:
            _validate_alias(alias)
    return index


def current_deployment_token_bytes(index_bytes: bytes) -> bytes | None:
    """Return the exact current_deployment JSON token from a validated index."""
    text = index_bytes.decode("utf-8")
    decoder = json.JSONDecoder(object_pairs_hook=_unique_object)

    def skip_space(position: int) -> int:
        while position < len(text) and text[position] in " \t\r\n":
            position += 1
        return position

    position = skip_space(0)
    if position >= len(text) or text[position] != "{":
        raise ReleaseNotesError("index_update", "invalid-index")
    position += 1
    while True:
        position = skip_space(position)
        if position >= len(text):
            raise ReleaseNotesError("index_update", "invalid-index")
        if text[position] == "}":
            return None
        try:
            member, key_end = decoder.raw_decode(text, position)
        except (json.JSONDecodeError, ValueError):
            raise ReleaseNotesError("index_update", "invalid-index") from None
        if not isinstance(member, str):
            raise ReleaseNotesError("index_update", "invalid-index")
        position = skip_space(key_end)
        if position >= len(text) or text[position] != ":":
            raise ReleaseNotesError("index_update", "invalid-index")
        value_start = skip_space(position + 1)
        try:
            _, value_end = decoder.raw_decode(text, value_start)
        except (json.JSONDecodeError, ValueError):
            raise ReleaseNotesError("index_update", "invalid-index") from None
        if member == "current_deployment":
            return text[value_start:value_end].encode("utf-8")
        position = skip_space(value_end)
        if position < len(text) and text[position] == ",":
            position += 1
            continue
        if position < len(text) and text[position] == "}":
            return None
        raise ReleaseNotesError("index_update", "invalid-index")


def _validate_marker(marker: dict):
    _require(isinstance(marker, dict) and type(marker.get("schema_version")) is int and
             (set(marker) == MARKER_V1_FIELDS if marker.get("schema_version") == SCHEMA_VERSION else
              set(marker) == MARKER_V2_FIELDS if marker.get("schema_version") == MARKER_SCHEMA_VERSION else False) and
             marker.get("environment") in ("local", "dev", "prod") and valid_version(marker.get("product_version")) and
             re.fullmatch(r"[0-9a-f]{40}", marker.get("commit", "")) is not None and
             _bounded_text(marker.get("branch"), 128) and _bounded_text(marker.get("tag"), 128, empty=True) and
             all(_bounded_text(marker.get(key), 256) for key in
                 ("image_tag", "service", "service_uid", "revision")) and type(marker.get("service_generation")) is int and
             marker.get("service_generation") > 0 and
             re.fullmatch(r"sha256:[0-9a-f]{64}", marker.get("image_digest", "")) is not None and
             re.fullmatch(r"sha256:[0-9a-f]{64}", marker.get("deployment_evidence_digest", "")) is not None and
             marker.get("operation") in TRANSITION_OPS, "index_update", "invalid-current-marker")
    if marker["schema_version"] == MARKER_SCHEMA_VERSION:
        _require(valid_version(marker.get("release_version")) and
                 isinstance(marker.get("operation_run_id"), str) and
                 re.fullmatch(r"[1-9][0-9]{0,19}", marker["operation_run_id"]) is not None and
                 type(marker.get("operation_run_attempt")) is int and 1 <= marker["operation_run_attempt"] <= 10000 and
                 isinstance(marker.get("component_builds"), list) and len(marker["component_builds"]) <= 2 and
                 (marker.get("previous_release") is None or _valid_previous_release(marker["previous_release"])),
                 "index_update", "invalid-current-marker")
        components = [entry.get("component") if isinstance(entry, dict) else None for entry in marker["component_builds"]]
        _require(all(isinstance(component, str) for component in components) and
                 components == sorted(set(components)) and all(_valid_component_build(entry) for entry in marker["component_builds"]),
                 "index_update", "invalid-component-builds")


ALIAS_FIELDS = {
    "environment", "requested_version", "canonical_version", "legacy_row_version", "source_marker_sha256",
    "source_index_generation", "transition_plan_sha256", "transition_run_id", "transition_run_attempt",
    "creation_marker_sha256", "alias_record_sha256",
}


def _canonical_json(value: dict) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def _valid_component_build(entry: dict) -> bool:
    if not isinstance(entry, dict) or entry.get("component") not in ("frontend", "worker"):
        return False
    if entry["component"] == "frontend":
        required = {"component", "deployment_id", "receipt_sha256"}
        allowed = required | {"source_sha", "build_config_sha256"}
        return (required <= set(entry) <= allowed and _bounded_text(entry.get("deployment_id"), 256) and
                SHA_RE.fullmatch(entry.get("receipt_sha256", "")) is not None and
                ("source_sha" not in entry or re.fullmatch(r"[0-9a-f]{40}", entry["source_sha"]) is not None) and
                ("build_config_sha256" not in entry or SHA_RE.fullmatch(entry["build_config_sha256"]) is not None))
    required = {"component", "job_name", "image_digest", "receipt_sha256"}
    allowed = required | {"source_sha"}
    return (required <= set(entry) <= allowed and _bounded_text(entry.get("job_name"), 128) and
            re.fullmatch(r"sha256:[0-9a-f]{64}", entry.get("image_digest", "")) is not None and
            SHA_RE.fullmatch(entry.get("receipt_sha256", "")) is not None and
            ("source_sha" not in entry or re.fullmatch(r"[0-9a-f]{40}", entry["source_sha"]) is not None))


def _valid_previous_release(value: dict) -> bool:
    if not isinstance(value, dict) or set(value) != {"release_version", "bff", "component_builds", "marker_sha256"}:
        return False
    bff = value.get("bff")
    fields = {"product_version", "commit", "branch", "tag", "image_tag", "image_digest", "service", "service_uid", "service_generation", "revision"}
    return (valid_version(value.get("release_version")) and isinstance(bff, dict) and set(bff) == fields and
            valid_version(bff.get("product_version")) and re.fullmatch(r"[0-9a-f]{40}", bff.get("commit", "")) is not None and
            _bounded_text(bff.get("branch"), 128) and _bounded_text(bff.get("tag"), 128, empty=True) and
            all(_bounded_text(bff.get(key), 256) for key in ("image_tag", "service", "service_uid", "revision")) and
            re.fullmatch(r"sha256:[0-9a-f]{64}", bff.get("image_digest", "")) is not None and
            type(bff.get("service_generation")) is int and bff["service_generation"] > 0 and
            isinstance(value.get("component_builds"), list) and len(value["component_builds"]) <= 2 and
            all(isinstance(item, dict) and isinstance(item.get("component"), str) for item in value["component_builds"]) and
            [item["component"] for item in value["component_builds"]] ==
            sorted({item["component"] for item in value["component_builds"]}) and
            all(_valid_component_build(item) for item in value["component_builds"]) and
            SHA_RE.fullmatch(value.get("marker_sha256", "")) is not None)


def _bounded_text(value: object, maximum: int, *, empty: bool = False) -> bool:
    return (isinstance(value, str) and len(value) <= maximum and
            (empty or bool(value.strip())) and
            all(unicodedata.category(character) != "Cc" for character in value))


def _validate_alias(alias: dict):
    _require(isinstance(alias, dict) and set(alias) == ALIAS_FIELDS and alias.get("environment") == "dev" and
             alias.get("requested_version") == LEGACY_RELEASE_VERSION and
             alias.get("canonical_version") == PUBLIC_RELEASE_VERSION and
             alias.get("legacy_row_version") == LEGACY_RELEASE_VERSION and
             SHA_RE.fullmatch(alias.get("source_marker_sha256", "")) is not None and
             type(alias.get("source_index_generation")) is int and alias["source_index_generation"] > 0 and
             SHA_RE.fullmatch(alias.get("transition_plan_sha256", "")) is not None and
             isinstance(alias.get("transition_run_id"), str) and re.fullmatch(r"[1-9][0-9]{0,19}", alias["transition_run_id"]) is not None and
             type(alias.get("transition_run_attempt")) is int and 1 <= alias["transition_run_attempt"] <= 10000 and
             SHA_RE.fullmatch(alias.get("creation_marker_sha256", "")) is not None and
             SHA_RE.fullmatch(alias.get("alias_record_sha256", "")) is not None and
             digest_bytes(_canonical_json({key: value for key, value in alias.items() if key != "alias_record_sha256"})) == alias["alias_record_sha256"],
             "index_update", "invalid-legacy-alias")


def previous_release_from_marker(marker: dict, marker_bytes: bytes | None = None) -> dict:
    """Project the exact prior current marker into the closed one-step rollback witness."""
    _validate_marker(marker)
    marker_bytes = marker_bytes if marker_bytes is not None else _canonical_json(marker)
    if marker["schema_version"] == MARKER_SCHEMA_VERSION:
        release_version = marker["release_version"]
        component_builds = marker["component_builds"]
    else:
        release_version = marker["product_version"]
        component_builds = []
    bff = {key: marker[key] for key in (
        "product_version", "commit", "branch", "tag", "image_tag", "image_digest",
        "service", "service_uid", "service_generation", "revision")}
    return {"release_version": release_version, "bff": bff,
            "component_builds": component_builds, "marker_sha256": digest_bytes(marker_bytes)}


def bind_previous_release(marker: dict, previous: dict | None, previous_bytes: bytes | None = None) -> dict:
    if marker.get("schema_version") != MARKER_SCHEMA_VERSION:
        return marker
    marker["previous_release"] = (previous_release_from_marker(previous, previous_bytes)
                                  if previous is not None else None)
    _validate_marker(marker)
    return marker


def _with_index_v2(index: dict) -> dict:
    if index.get("schema_version") == INDEX_SCHEMA_VERSION:
        return index
    return {"schema_version": INDEX_SCHEMA_VERSION,
            "versions": index.get("versions", []),
            "current_deployment": index.get("current_deployment"),
            "legacy_aliases": []}


def _create_alias_record(index: dict, source_generation: int, marker: dict,
                         transition_plan_sha256: str | None) -> dict | None:
    previous = index.get("current_deployment")
    if (index.get("schema_version") != SCHEMA_VERSION or not isinstance(previous, dict) or
            previous.get("schema_version") != SCHEMA_VERSION or marker.get("schema_version") != MARKER_SCHEMA_VERSION or
            marker.get("environment") != "dev" or marker.get("release_version") != PUBLIC_RELEASE_VERSION or
            previous.get("product_version") != LEGACY_RELEASE_VERSION or
            not isinstance(transition_plan_sha256, str) or SHA_RE.fullmatch(transition_plan_sha256) is None or
            marker.get("operation") not in ("release", "deploy", "reactivate", "promote")):
        return None
    if not isinstance(marker.get("previous_release"), dict) or marker["previous_release"].get("release_version") != LEGACY_RELEASE_VERSION:
        return None
    marker_bytes = _canonical_json(marker)
    values = {
        "environment": "dev", "requested_version": LEGACY_RELEASE_VERSION,
        "canonical_version": PUBLIC_RELEASE_VERSION, "legacy_row_version": LEGACY_RELEASE_VERSION,
        "source_marker_sha256": marker["previous_release"]["marker_sha256"],
        "source_index_generation": source_generation,
        "transition_plan_sha256": transition_plan_sha256,
        "transition_run_id": marker["operation_run_id"],
        "transition_run_attempt": marker["operation_run_attempt"],
        "creation_marker_sha256": digest_bytes(marker_bytes),
    }
    values["alias_record_sha256"] = digest_bytes(_canonical_json(values))
    _validate_alias(values)
    return values


def _initial_alias_transition(index: dict, marker: dict | None) -> bool:
    previous = index.get("current_deployment") if isinstance(index, dict) else None
    return (isinstance(marker, dict) and index.get("schema_version") == SCHEMA_VERSION and
            isinstance(previous, dict) and previous.get("schema_version") == SCHEMA_VERSION and
            previous.get("environment") == "dev" and previous.get("product_version") == LEGACY_RELEASE_VERSION and
            marker.get("schema_version") == MARKER_SCHEMA_VERSION and marker.get("environment") == "dev" and
            marker.get("release_version") == PUBLIC_RELEASE_VERSION and
            isinstance(marker.get("previous_release"), dict) and
            marker["previous_release"].get("release_version") == LEGACY_RELEASE_VERSION)


def normalize_operation(plan_bytes: bytes, state_bytes: bytes, result_bytes: bytes, sidecar_bytes: bytes,
                        *, environment: str, workflow_operation: str, run_id: str, run_attempt: int,
                        service: str, receipt_bytes: bytes | None,
                        component_receipts: dict[str, bytes] | None = None) -> tuple[dict, dict]:
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
    selected = plan.get("selected")
    bff_selected = isinstance(selected, list) and "bff" in selected
    bff_reused = (workflow_operation == "release" and plan.get("schema") == 4 and
                  isinstance(selected, list) and bool(selected) and "bff" not in selected and
                  set(selected) <= {"frontend", "worker"})
    identity = identities.get("bff")
    plan_id = plan.get("id")
    plan_tag = plan.get("tag")
    plan_source = plan.get("source")
    state_sequence = state.get("sequence")
    component = state.get("components", {}).get("bff") if isinstance(state.get("components"), dict) else None
    bff_target = normalized.get("bff")
    if (bff_selected and not isinstance(identity, dict)) or not isinstance(plan_id, str) or not plan_id or \
            not isinstance(plan_tag, str) or not plan_tag or not isinstance(plan_source, str) or \
            re.fullmatch(r"[0-9a-f]{40}", plan_source) is None or type(state_sequence) is not int or state_sequence < 0 or \
            not isinstance(bff_target, dict) or bff_target.get("service_name") != service or \
            (bff_selected and (not isinstance(identity, dict) or not isinstance(component, dict))) or \
            (not bff_selected and (not bff_reused or identity is not None or component is not None)):
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
             workflow_operation in TRANSITION_OPS and isinstance(selected, list) and
             result_component in plan["selected"] and isinstance(result_entry, dict) and isinstance(result_identity, dict) and
             state.get("plan") == plan_id and
             (component is None or component.get("status") in ("verified", "rolled_back")) and
             result.get("release") == plan_tag and result.get("attempt") == plan_id and
             expected.get("source") == plan_source and
             expected.get("identity") == digest_bytes(
                 json.dumps(result_identity, sort_keys=True, separators=(",", ":")).encode()),
             "index_update", "native-operation-unsuccessful")
    receipt = None
    if bff_selected:
        _require(isinstance(receipt_bytes, bytes), "index_update", "selected-target-evidence-unavailable")
        receipt = _json_object(receipt_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
        artifact = receipt.get("artifact")
        image = artifact.get("image") if isinstance(artifact, dict) else None
        _require(receipt.get("component") == "bff" and receipt.get("identity") == identity and
                 isinstance(image, str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image) is not None,
                 "index_update", "selected-target-evidence-invalid")
    else:
        _require(workflow_operation == "release" and bff_reused and plan.get("release_version") and
                 valid_version(plan.get("release_version")), "index_update", "bff-reuse-not-supported")
    if workflow_operation == "rollback":
        _require(bff_selected, "index_update", "rollback-bff-selection-required")
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
                 (component is None or component.get("status") == "verified") and result_entry.get("status") == "verified" and
                 observed_result.get("component_status") == "verified" and result.get("candidate") == result_entry.get("candidate"),
                 "index_update", "successful-operation-required")
        # BFF checkpoints retain config and revision in candidate; its image identity lives in the receipt.
        if bff_selected:
            candidate = component.get("candidate")
            _require(isinstance(candidate, dict) and isinstance(candidate.get("revision"), str) and
                     candidate.get("revision") == sidecar.get("revision") and
                     receipt["artifact"]["image"].rsplit("@", 1)[-1] == sidecar.get("image_digest"),
                     "index_update", "selected-target-readback-mismatch")
        elif bff_reused:
            for selected_component in selected:
                selected_state = state.get("components", {}).get(selected_component)
                _require(isinstance(selected_state, dict) and selected_state.get("status") == "verified",
                         "index_update", "selected-component-operation-unsuccessful")
    evidence_digest = digest_bytes(json.dumps(sidecar, sort_keys=True, separators=(",", ":")).encode("utf-8"))
    schema = MARKER_SCHEMA_VERSION if plan.get("schema") == 4 else SCHEMA_VERSION
    marker = {
        "schema_version": schema, "environment": environment, "product_version": build["product_version"],
        "commit": build["commit"], "branch": build["branch"], "tag": build["tag"], "image_tag": build["image_tag"],
        "image_digest": sidecar["image_digest"], "service": service, "service_uid": sidecar["service_uid"],
        "service_generation": sidecar["service_generation"], "revision": sidecar["revision"],
        "operation": workflow_operation, "deployment_evidence_digest": "sha256:" + evidence_digest,
    }
    if schema == MARKER_SCHEMA_VERSION:
        marker["release_version"] = plan["release_version"]
        marker["operation_run_id"] = run_id
        marker["operation_run_attempt"] = run_attempt
        marker["component_builds"] = _component_builds(plan, state, component_receipts or {})
        marker["previous_release"] = None
    _validate_marker(marker)
    return marker, {"plan": plan, "state": state, "result": result, "sidecar": sidecar,
                    "receipt": receipt}


def _component_builds(plan: dict, state: dict, receipts: dict[str, bytes]) -> list[dict]:
    selected = set(plan.get("selected", []))
    output = []
    for component in ("frontend", "worker"):
        if component not in selected:
            continue
        receipt_bytes = receipts.get(component)
        _require(isinstance(receipt_bytes, bytes), "index_update", "selected-component-receipt-unavailable")
        receipt = _json_object(receipt_bytes, "index_update", MAX_OPERATION_ARTIFACT_BYTES)
        _require(receipt.get("component") == component and receipt.get("identity") == plan.get("identities", {}).get(component),
                 "index_update", "selected-component-receipt-invalid")
        result_entry = state.get("components", {}).get(component, {})
        if component == "frontend":
            candidate = result_entry.get("candidate") if isinstance(result_entry, dict) else None
            artifact = receipt.get("artifact")
            _require((candidate is None or isinstance(candidate, dict)) and
                     (artifact is None or isinstance(artifact, dict)),
                     "index_update", "selected-component-receipt-invalid")
            deployment_ids = []
            for source, field in ((candidate, "deployment"), (candidate, "deployment_id"),
                                  (artifact, "deployment_id")):
                if isinstance(source, dict) and field in source:
                    deployment_id = source[field]
                    _require(isinstance(deployment_id, str) and 1 <= len(deployment_id) <= 256,
                             "index_update", "selected-component-receipt-invalid")
                    deployment_ids.append(deployment_id)
            _require(bool(deployment_ids) and len(set(deployment_ids)) == 1,
                     "index_update", "selected-component-receipt-invalid")
            deployment_id = deployment_ids[0]
            item = {"component": "frontend", "deployment_id": deployment_id,
                    "receipt_sha256": digest_bytes(receipt_bytes)}
            source_sha = receipt.get("build_sha")
            if isinstance(source_sha, str) and re.fullmatch(r"[0-9a-f]{40}", source_sha):
                item["source_sha"] = source_sha
            config_sha = receipt.get("target_config")
            if isinstance(config_sha, str) and SHA_RE.fullmatch(config_sha):
                item["build_config_sha256"] = config_sha
        else:
            artifact = receipt.get("artifact")
            image = artifact.get("image") if isinstance(artifact, dict) else None
            worker = plan.get("normalized", {}).get("worker")
            job_name = worker.get("job_name") if isinstance(worker, dict) else None
            _require(isinstance(job_name, str) and 1 <= len(job_name) <= 128 and
                     isinstance(image, str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image) is not None,
                     "index_update", "selected-component-receipt-invalid")
            item = {"component": "worker", "job_name": job_name,
                    "image_digest": image.rsplit("@", 1)[1],
                    "receipt_sha256": digest_bytes(receipt_bytes)}
            source_sha = receipt.get("build_sha")
            if isinstance(source_sha, str) and re.fullmatch(r"[0-9a-f]{40}", source_sha):
                item["source_sha"] = source_sha
        output.append(item)
    return output


def validate_native_operation(plan_bytes: bytes, state_bytes: bytes, result_bytes: bytes, receipt_bytes: bytes | None,
                              *, environment: str, workflow_operation: str, run_id: str,
                              run_attempt: int,
                              component_receipts: dict[str, bytes] | None = None) -> dict:
    """Verify the retained product operation without querying or changing a provider."""
    plan = _json_object(plan_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    state = _json_object(state_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    result = _json_object(result_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
    normalized = plan.get("normalized")
    identities = plan.get("identities")
    plan_id, plan_tag, plan_source = plan.get("id"), plan.get("tag"), plan.get("source")
    target_environment = {"development": "dev", "production": "prod"}.get(
        normalized.get("environment") if isinstance(normalized, dict) else None)
    selected = plan.get("selected")
    bff_selected = isinstance(selected, list) and "bff" in selected
    bff_reused = (workflow_operation == "release" and plan.get("schema") == 4 and
                  isinstance(selected, list) and bool(selected) and "bff" not in selected and
                  set(selected) <= {"frontend", "worker"})
    identity = identities.get("bff") if isinstance(identities, dict) else None
    sequence = state.get("sequence")
    components = state.get("components")
    component = components.get("bff") if isinstance(components, dict) else None
    target = normalized.get("bff") if isinstance(normalized, dict) else None
    if workflow_operation not in TRANSITION_OPS or not re.fullmatch(r"[1-9][0-9]*", run_id) or \
            type(run_attempt) is not int or run_attempt < 1:
        raise ReleaseNotesError("product_release", "operation-evidence-mismatch")
    _require(target_environment == environment and isinstance(target, dict) and
             ((isinstance(identity, dict) and bff_selected) or (identity is None and bff_reused)) and
             isinstance(plan_id, str) and bool(plan_id) and
             isinstance(plan_tag, str) and bool(plan_tag) and isinstance(plan_source, str) and
             re.fullmatch(r"[0-9a-f]{40}", plan_source) is not None and
             isinstance(selected, list) and
             type(sequence) is int and sequence >= 0 and (isinstance(component, dict) if bff_selected else component is None) and
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
        _require(bff_selected, "product_release", "rollback-bff-selection-required")
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
        # The native BFF producer does not copy receipt.image into candidate; keep the receipt identity check above.
        candidate = component.get("candidate") if component is not None else None
        _require(state.get("status") == "success" and result.get("stage") == "success" and
                 result.get("status") == "success" and
                 (component is None or component.get("status") == "verified") and
                 result_entry.get("status") == "verified" and observed.get("component_status") == "verified" and
                 result.get("candidate") == result_entry.get("candidate") and
                 (not bff_selected or (isinstance(candidate, dict) and isinstance(candidate.get("revision"), str) and
                                       bool(candidate.get("revision")))) and
                 (bff_selected or bff_reused),
                 "product_release", "native-operation-unsuccessful")
        if bff_reused:
            for selected_component in selected:
                selected_state = components.get(selected_component) if isinstance(components, dict) else None
                _require(isinstance(selected_state, dict) and selected_state.get("status") == "verified",
                         "product_release", "selected-component-operation-unsuccessful")
    component_receipts = component_receipts or {}
    builds = _component_builds(plan, state, component_receipts) if plan.get("schema") == 4 else []
    if bff_selected:
        receipt = _json_object(receipt_bytes, "product_release", MAX_OPERATION_ARTIFACT_BYTES)
        artifact = receipt.get("artifact")
        image = artifact.get("image") if isinstance(artifact, dict) else None
        _require(receipt.get("component") == "bff" and receipt.get("identity") == identity and
                 isinstance(image, str) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image) is not None,
                 "product_release", "selected-target-evidence-invalid")
    else:
        _require(receipt_bytes is None and workflow_operation == "release" and
                 valid_version(plan.get("release_version")),
                 "product_release", "bff-reuse-not-supported")
    return {"plan": plan, "state": state, "result": result,
            "receipt": receipt if bff_selected else None, "component_builds": builds,
            "bff_reused": bff_reused}


def marker_transition_allowed(previous: dict | None, candidate: dict,
                              previous_marker_bytes: bytes | None = None, *,
                              allow_same_bff_generation: bool = False) -> bool:
    if previous is None:
        return True
    _validate_marker(previous)
    _validate_marker(candidate)
    if previous == candidate:
        return True
    if previous.get("schema_version") == SCHEMA_VERSION and candidate.get("schema_version") == SCHEMA_VERSION:
        if previous["service_uid"] != candidate["service_uid"]:
            return True
        return candidate["service_generation"] > previous["service_generation"]
    if previous.get("schema_version") == SCHEMA_VERSION and candidate.get("schema_version") == MARKER_SCHEMA_VERSION:
        expected = previous_release_from_marker(previous, previous_marker_bytes)
        return candidate.get("previous_release") == expected
    if previous.get("schema_version") != MARKER_SCHEMA_VERSION or candidate.get("schema_version") != MARKER_SCHEMA_VERSION:
        return False
    old_run = (int(previous["operation_run_id"]), previous["operation_run_attempt"])
    new_run = (int(candidate["operation_run_id"]), candidate["operation_run_attempt"])
    if new_run <= old_run or candidate.get("previous_release") != previous_release_from_marker(previous, previous_marker_bytes):
        return False
    if previous["service_uid"] != candidate["service_uid"]:
        return True
    if candidate["service_generation"] < previous["service_generation"]:
        return False
    if candidate["service_generation"] == previous["service_generation"]:
        same_bff = all(previous.get(key) == candidate.get(key) for key in (
            "product_version", "commit", "branch", "tag", "image_tag", "image_digest", "service",
            "service_uid", "service_generation", "revision"))
        return (allow_same_bff_generation and same_bff and
                previous.get("component_builds") != candidate.get("component_builds") and
                bool(candidate.get("component_builds")))
    return True


def _marker_bff_identity(marker: dict) -> tuple:
    return tuple(marker.get(key) for key in (
        "environment", "product_version", "commit", "branch", "tag", "image_tag",
        "image_digest", "service", "service_uid", "service_generation", "revision"))


def _validate_bff_reuse(previous: dict | None, candidate: dict) -> None:
    _require(candidate.get("operation") == "release" and
             (previous is None or (previous.get("schema_version") in (SCHEMA_VERSION, MARKER_SCHEMA_VERSION) and
                                   _marker_bff_identity(previous) == _marker_bff_identity(candidate))),
             "index_update", "release-bff-reuse-mismatch")


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
    legacy_alias_receipt: dict | None = None


def publish_package(store: NotesStore, files: dict[str, bytes], *, product_version: str,
                    published_at: str, marker: dict | None, historical: bool = False,
                    transition_plan_sha256: str | None = None,
                    bff_reused: bool = False,
                    allow_alias_creation: bool = True,
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
            expected_version = marker.get("release_version", marker["product_version"])
            _require(expected_version == product_version, "validate", "package-release-version-mismatch")
        _require(_valid_timestamp(published_at), "validate", "invalid-published-at")
        prepared = {locale: {key: item[key] for key in ("sha256", "object_path", "generation")}
                    for locale, item in validated.items()}
    except ReleaseNotesError:
        raise
    except Exception:
        raise ReleaseNotesError("content_upload", "content-stage-failed") from None

    def locale_state(locales: dict) -> tuple:
        return tuple(sorted((locale, entry["sha256"], entry["object_path"])
                            for locale, entry in locales.items()))

    unobserved = object()
    observed_version_state = unobserved
    intended_version_state = None
    generation_conflict_seen = False
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
        previous_marker_bytes = current_deployment_token_bytes(current_bytes) if current_bytes is not None else None
        if marker is not None:
            try:
                _prepare_marker_transition(marker, previous_marker, previous_marker_bytes)
            except ReleaseNotesError:
                raise
            try:
                active_matches = store.active_marker_matches(marker)
            except Exception:
                raise ReleaseNotesError("index_update", "release-transition-unavailable") from None
            if bff_reused:
                _validate_bff_reuse(previous_marker, marker)
            if not active_matches or not marker_transition_allowed(
                    previous_marker, marker, previous_marker_bytes,
                    allow_same_bff_generation=bff_reused):
                raise ReleaseNotesError("index_update", "release-transition-stale")
        old_aliases = list(index.get("legacy_aliases", []))
        initial_alias_transition = _initial_alias_transition(index, marker)
        alias_source_index = index
        target_schema2 = marker is not None and marker.get("schema_version") == MARKER_SCHEMA_VERSION
        if target_schema2 or index.get("schema_version") == INDEX_SCHEMA_VERSION:
            index = _with_index_v2(index)
        alias_action = "carried" if index.get("legacy_aliases") else None
        if initial_alias_transition and (not allow_alias_creation or historical):
            raise ReleaseNotesError("index_update", "legacy-alias-creation-not-authorized")
        if initial_alias_transition and not index["legacy_aliases"]:
            alias = _create_alias_record(alias_source_index, generation, marker, transition_plan_sha256)
            if alias is None:
                raise ReleaseNotesError("index_update", "legacy-alias-creation-evidence-invalid")
            index["legacy_aliases"] = [alias]
            alias_action = "created"
        if old_aliases and index.get("legacy_aliases") != old_aliases:
            raise ReleaseNotesError("index_update", "legacy-alias-immutable-conflict")
        versions = index["versions"]
        existing = next((row for row in versions if row["product_version"] == product_version), None)
        current_version_state = None if existing is None else locale_state(existing["locales"])
        if observed_version_state is unobserved:
            observed_version_state = current_version_state
            intended_locales = dict(existing["locales"]) if existing else {}
            intended_locales.update({locale: {"sha256": item["sha256"], "object_path": item["object_path"]}
                                     for locale, item in prepared.items()})
            intended_version_state = locale_state(intended_locales)
        elif generation_conflict_seen and current_version_state not in (
                observed_version_state, intended_version_state):
            # A conditional write lost and this version changed to different bytes meanwhile.
            # Keep the winner and let the existing index_update failure path record the conflict.
            raise ReleaseNotesError("index_update", "generation-conflict")
        if existing:
            old_locales = existing["locales"]
            updated_locales = dict(old_locales)
            updated_locales.update({locale: {"sha256": item["sha256"], "object_path": item["object_path"]}
                                    for locale, item in prepared.items()})
            aliases = index.get("legacy_aliases", [])
            if any(alias.get("legacy_row_version") == product_version for alias in aliases) and \
                    updated_locales != old_locales:
                raise ReleaseNotesError("index_update", "legacy-alias-row-immutable")
            if updated_locales == old_locales and (marker is None or previous_marker == marker):
                alias_receipt = _legacy_alias_receipt(index, "carried" if index.get("legacy_aliases") else None,
                                                      generation, generation, marker=marker,
                                                      transition_plan_sha256=transition_plan_sha256)
                return PublishResult("exact_readback", generation,
                                     {locale: item["generation"] for locale, item in prepared.items()}, alias_receipt)
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
                generation_conflict_seen = True
                continue
            raise ReleaseNotesError("index_update", "index-write-failed") from None
        try:
            readback, readback_generation = store.read_index()
        except Exception:
            raise ReleaseNotesError("exact_readback", "index-readback-unavailable") from None
        _require(readback == serialized and readback_generation == new_generation, "exact_readback", "index-readback-mismatch")
        validated_index = validate_index(readback)
        alias_receipt = _legacy_alias_receipt(validated_index, alias_action, generation, new_generation,
                                              marker=marker, transition_plan_sha256=transition_plan_sha256)
        return PublishResult("exact_readback", new_generation,
                             {locale: item["generation"] for locale, item in prepared.items()}, alias_receipt)
    raise ReleaseNotesError("index_update", "index-conflict-exhausted")


def _prepare_marker_transition(marker: dict, previous: dict | None,
                               previous_marker_bytes: bytes | None = None):
    if marker.get("schema_version") != MARKER_SCHEMA_VERSION:
        return
    if isinstance(previous, dict) and previous.get("schema_version") == MARKER_SCHEMA_VERSION:
        replay_candidate = dict(marker)
        replay_previous = dict(previous)
        replay_candidate["previous_release"] = replay_previous["previous_release"]
        if replay_candidate == replay_previous:
            marker["previous_release"] = previous["previous_release"]
            return
    if marker.get("operation") == "rollback":
        _require(isinstance(previous, dict) and previous.get("schema_version") == MARKER_SCHEMA_VERSION and
                 isinstance(previous.get("previous_release"), dict), "index_update", "rollback-target-unavailable")
        target = previous["previous_release"]
        _require(all(marker.get(key) == target["bff"].get(key) for key in (
            "product_version", "commit", "branch", "tag", "image_tag", "image_digest", "service",
            "service_uid", "revision")) and
                 type(marker.get("service_generation")) is int and
                 type(previous.get("service_generation")) is int and
                 marker["service_generation"] > previous["service_generation"],
                 "index_update", "rollback-target-mismatch")
        marker["release_version"] = target["release_version"]
        _require(marker.get("component_builds") in ([], target["component_builds"]),
                 "index_update", "rollback-component-target-mismatch")
        marker["component_builds"] = target["component_builds"]
    elif previous and previous.get("schema_version") == MARKER_SCHEMA_VERSION:
        selected = {item["component"]: item for item in marker.get("component_builds", [])}
        merged = {item["component"]: item for item in previous.get("component_builds", [])}
        merged.update(selected)
        marker["component_builds"] = [merged[key] for key in sorted(merged)]
    bind_previous_release(marker, previous, previous_marker_bytes)


def _legacy_alias_receipt(index: dict, action: str | None, before: int, after: int, *,
                          marker: dict | None = None,
                          transition_plan_sha256: str | None = None) -> dict | None:
    aliases = index.get("legacy_aliases", []) if index.get("schema_version") == INDEX_SCHEMA_VERSION else []
    if not aliases:
        return None
    alias = aliases[0]
    result = {"action": action or "carried", "environment": alias["environment"],
              "requested_version": alias["requested_version"], "canonical_version": alias["canonical_version"],
              "legacy_row_version": alias["legacy_row_version"], "alias_record_sha256": alias["alias_record_sha256"],
              "creation_marker_sha256": alias["creation_marker_sha256"],
              "alias_source_index_generation": alias["source_index_generation"],
              "index_generation_before": before, "index_generation_after": after}
    if (set(result) != {"action", "environment", "requested_version", "canonical_version", "legacy_row_version",
                        "alias_record_sha256", "creation_marker_sha256", "alias_source_index_generation",
                        "index_generation_before", "index_generation_after"} or
            result["action"] not in ("created", "carried") or
            result["environment"] != "dev" or result["requested_version"] != LEGACY_RELEASE_VERSION or
            result["canonical_version"] != PUBLIC_RELEASE_VERSION or result["legacy_row_version"] != LEGACY_RELEASE_VERSION or
            not all(isinstance(result[key], str) and SHA_RE.fullmatch(result[key]) is not None
                    for key in ("alias_record_sha256", "creation_marker_sha256")) or
            any(type(result[key]) is not int or result[key] < 1 for key in
                ("alias_source_index_generation", "index_generation_before", "index_generation_after")) or
            result["index_generation_after"] < result["index_generation_before"]):
        raise ReleaseNotesError("exact_readback", "legacy-alias-receipt-invalid")
    if result["action"] == "created" and (
            result["alias_source_index_generation"] != before or after <= before or
            not isinstance(marker, dict) or marker.get("environment") != alias["environment"] or
            marker.get("release_version") != alias["canonical_version"] or
            marker.get("operation_run_id") != alias["transition_run_id"] or
            marker.get("operation_run_attempt") != alias["transition_run_attempt"] or
            transition_plan_sha256 != alias["transition_plan_sha256"] or
            digest_bytes(_canonical_json(marker)) != alias["creation_marker_sha256"]):
        raise ReleaseNotesError("exact_readback", "legacy-alias-creation-receipt-mismatch")
    return result


def publish_marker_only(store: NotesStore, marker: dict, *, transition_plan_sha256: str | None = None,
                        bff_reused: bool = False,
                        allow_alias_creation: bool = False,
                        max_cas_attempts: int = 3) -> PublishResult:
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
        previous_marker_bytes = current_deployment_token_bytes(current_bytes) if current_bytes is not None else None
        _prepare_marker_transition(marker, previous, previous_marker_bytes)
        if bff_reused:
            _validate_bff_reuse(previous, marker)
        try:
            active_matches = store.active_marker_matches(marker)
        except Exception:
            raise ReleaseNotesError("index_update", "release-transition-unavailable") from None
        if not active_matches or not marker_transition_allowed(
                previous, marker, previous_marker_bytes, allow_same_bff_generation=bff_reused):
            raise ReleaseNotesError("index_update", "release-transition-stale")
        if previous == marker:
            alias_receipt = _legacy_alias_receipt(index, "carried" if index.get("legacy_aliases") else None,
                                                  generation, generation, marker=marker,
                                                  transition_plan_sha256=transition_plan_sha256)
            return PublishResult("exact_readback", generation, {}, alias_receipt)
        previous_aliases = list(index.get("legacy_aliases", []))
        initial_alias_transition = _initial_alias_transition(index, marker)
        alias_source_index = index
        if marker.get("schema_version") == MARKER_SCHEMA_VERSION or index.get("schema_version") == INDEX_SCHEMA_VERSION:
            index = _with_index_v2(index)
        alias_action = "carried" if index.get("legacy_aliases") else None
        if initial_alias_transition and not allow_alias_creation:
            raise ReleaseNotesError("index_update", "legacy-alias-creation-not-authorized")
        if initial_alias_transition and not index["legacy_aliases"]:
            alias = _create_alias_record(alias_source_index, generation, marker, transition_plan_sha256)
            if alias is None:
                raise ReleaseNotesError("index_update", "legacy-alias-creation-evidence-invalid")
            index["legacy_aliases"] = [alias]
            alias_action = "created"
        if previous_aliases and index.get("legacy_aliases") != previous_aliases:
            raise ReleaseNotesError("index_update", "legacy-alias-immutable-conflict")
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
        validated_index = validate_index(readback)
        alias_receipt = _legacy_alias_receipt(validated_index, alias_action, generation, new_generation,
                                              marker=marker, transition_plan_sha256=transition_plan_sha256)
        return PublishResult("exact_readback", new_generation, {}, alias_receipt)
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
