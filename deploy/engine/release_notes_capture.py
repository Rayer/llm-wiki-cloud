#!/usr/bin/env python3
"""Capture operation-bound BFF service evidence after an existing release step.

No deployment or configuration mutation is performed by this command.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import subprocess
import urllib.request

import release_notes


def read_bytes(path: Path) -> bytes:
    try:
        if path.is_symlink() or not path.is_file() or path.stat().st_size > release_notes.MAX_OPERATION_ARTIFACT_BYTES:
            raise OSError("regular artifact file required")
        return path.read_bytes()
    except OSError:
        raise release_notes.ReleaseNotesError("index_update", "native-operation-artifact-unavailable") from None


def read_object(data: bytes, name: str) -> dict:
    value = release_notes._json_object(data, "index_update", release_notes.MAX_INDEX_BYTES)
    if not isinstance(value, dict):
        raise release_notes.ReleaseNotesError("index_update", name + "-shape-invalid")
    return value


def provider_json(command: list[str], environment: dict[str, str] | None = None) -> dict:
    try:
        result = subprocess.run(command, capture_output=True, check=False, text=True, env=environment)
    except OSError:
        raise release_notes.ReleaseNotesError("index_update", "service-readback-unavailable") from None
    if result.returncode != 0:
        raise release_notes.ReleaseNotesError("index_update", "service-readback-unavailable")
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError:
        raise release_notes.ReleaseNotesError("index_update", "service-readback-invalid") from None
    if not isinstance(value, dict):
        raise release_notes.ReleaseNotesError("index_update", "service-readback-invalid")
    return value


def build_info(url: str) -> dict:
    request = urllib.request.Request(url.rstrip("/") + "/api/v1/public/version", headers={"Cache-Control": "no-cache"})
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            if response.status != 200 or response.headers.get("Cache-Control", "").lower() != "no-store":
                raise release_notes.ReleaseNotesError("index_update", "runtime-build-readback-unavailable")
            data = response.read(16 * 1024 + 1)
    except release_notes.ReleaseNotesError:
        raise
    except Exception:
        raise release_notes.ReleaseNotesError("index_update", "runtime-build-readback-unavailable") from None
    value = release_notes._json_object(data, "index_update", 16 * 1024, release_notes.BUILD_INFO_FIELDS)
    if set(value) != release_notes.BUILD_INFO_FIELDS:
        raise release_notes.ReleaseNotesError("index_update", "runtime-build-readback-invalid")
    return value


def observe_service(plan: dict, environment: str) -> dict:
    bff = plan.get("normalized", {}).get("bff")
    gcp = plan.get("normalized", {}).get("gcp")
    if not isinstance(bff, dict) or not isinstance(gcp, dict):
        raise release_notes.ReleaseNotesError("index_update", "target-service-identity-unavailable")
    service, region, project = bff.get("service_name"), gcp.get("region"), gcp.get("project_id")
    if not all(isinstance(value, str) and value for value in (service, region, project)):
        raise release_notes.ReleaseNotesError("index_update", "target-service-identity-unavailable")
    service_object = provider_json(["gcloud", "run", "services", "describe", service, "--project", project,
                                    "--region", region, "--format=json", "--quiet"])
    metadata = service_object.get("metadata")
    status = service_object.get("status")
    traffic = status.get("traffic") if isinstance(status, dict) else None
    if not isinstance(metadata, dict) or metadata.get("name") != service or not metadata.get("uid") or \
            type(metadata.get("generation")) is not int or metadata["generation"] < 1 or \
            not isinstance(traffic, list) or len(traffic) != 1 or not isinstance(traffic[0], dict) or \
            traffic[0].get("percent") != 100 or traffic[0].get("tag"):
        raise release_notes.ReleaseNotesError("index_update", "service-transition-unavailable")
    revision = traffic[0].get("revisionName")
    if not isinstance(revision, str) or not revision:
        raise release_notes.ReleaseNotesError("index_update", "active-revision-unavailable")
    revision_object = provider_json(["gcloud", "run", "revisions", "describe", revision, "--project", project,
                                    "--region", region, "--format=json", "--quiet"])
    revision_meta = revision_object.get("metadata")
    revision_status = revision_object.get("status")
    conditions = revision_status.get("conditions") if isinstance(revision_status, dict) else None
    containers = revision_object.get("spec", {}).get("containers") if isinstance(revision_object.get("spec"), dict) else None
    ready = isinstance(conditions, list) and any(isinstance(condition, dict) and condition.get("type") == "Ready" and condition.get("status") == "True" for condition in conditions)
    if not isinstance(revision_meta, dict) or revision_meta.get("name") != revision or not ready or \
            not isinstance(containers, list) or len(containers) != 1 or not isinstance(containers[0], dict):
        raise release_notes.ReleaseNotesError("index_update", "active-revision-unverified")
    image = revision_status.get("imageDigest") if isinstance(revision_status, dict) else None
    if not isinstance(image, str) or "@sha256:" not in image or containers[0].get("image") != image:
        raise release_notes.ReleaseNotesError("index_update", "active-image-unverified")
    image_digest = image.rsplit("@", 1)[1]
    if re.fullmatch(r"sha256:[0-9a-f]{64}", image_digest) is None:
        raise release_notes.ReleaseNotesError("index_update", "active-image-unverified")
    service_url = status.get("url") if isinstance(status, dict) else None
    if not isinstance(service_url, str) or not service_url.startswith("https://"):
        service_url = status.get("uri") if isinstance(status, dict) else None
    if not isinstance(service_url, str) or not service_url.startswith("https://"):
        raise release_notes.ReleaseNotesError("index_update", "service-url-unavailable")
    build = build_info(service_url)
    if build.get("environment") != environment or build.get("service") != service or build.get("revision") != revision:
        raise release_notes.ReleaseNotesError("index_update", "runtime-service-identity-mismatch")
    return {"service": service, "service_uid": metadata["uid"], "service_generation": metadata["generation"],
            "revision": revision, "image": image, "image_digest": image_digest, "traffic": traffic,
            "build_info": build, "url": service_url}


def capture(directory: Path, operation: str, run_id: str, run_attempt: int, *, write_sidecar: bool = True) -> tuple[dict, bytes]:
    plan_bytes = read_bytes(directory / "plan.json")
    state_bytes = read_bytes(directory / "state.json")
    result_bytes = read_bytes(directory / "result.json")
    receipt_bytes = read_bytes(directory / "receipts" / "bff.json")
    plan, state, result = (read_object(data, name) for name, data in
                           (("plan", plan_bytes), ("state", state_bytes), ("result", result_bytes)))
    target = plan.get("normalized", {}).get("environment")
    environment = {"development": "dev", "production": "prod"}.get(target)
    if environment not in ("dev", "prod"):
        raise release_notes.ReleaseNotesError("index_update", "target-environment-invalid")
    observed = observe_service(plan, environment)
    service, revision = observed["service"], observed["revision"]
    image, image_digest = observed["image"], observed["image_digest"]
    metadata = {"uid": observed["service_uid"], "generation": observed["service_generation"]}
    traffic, build = observed["traffic"], observed["build_info"]
    if operation == "rollback":
        prior = state.get("components", {}).get("bff", {}).get("prior")
        if not isinstance(prior, dict) or prior.get("revision") != revision or prior.get("image") != image:
            raise release_notes.ReleaseNotesError("index_update", "rollback-prior-readback-mismatch")
    else:
        candidate = state.get("components", {}).get("bff", {}).get("candidate") or {}
        if candidate.get("revision") and candidate["revision"] != revision:
            raise release_notes.ReleaseNotesError("index_update", "selected-target-readback-mismatch")
    sidecar = {
        "schema_version": 1,
        "environment": environment,
        "operation": operation,
        "run_id": run_id,
        "run_attempt": run_attempt,
        "plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
        "state_sha256": hashlib.sha256(state_bytes).hexdigest(),
        "result_sha256": hashlib.sha256(result_bytes).hexdigest(),
        "service": service,
        "service_uid": metadata["uid"],
        "service_generation": metadata["generation"],
        "revision": revision,
        "image_digest": image_digest,
        "traffic": traffic,
        "build_info": build,
    }
    marker, _ = release_notes.normalize_operation(plan_bytes, state_bytes, result_bytes,
        json.dumps(sidecar, sort_keys=True, separators=(",", ":")).encode("utf-8"),
        environment=environment, workflow_operation=operation, run_id=run_id,
        run_attempt=run_attempt, service=service, receipt_bytes=receipt_bytes)
    output = json.dumps(sidecar, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    sidecar_path = directory / "release-notes-operation.json"
    if write_sidecar and sidecar_path.exists():
        raise release_notes.ReleaseNotesError("index_update", "operation-evidence-already-exists")
    if write_sidecar:
        sidecar_path.write_bytes(output)
    return marker, output


def active_marker_matches(directory: Path, operation: str, run_id: str, run_attempt: int, marker: dict) -> bool:
    """Freshly compare service identity without attaching it to the retained operation."""
    try:
        plan = read_object(read_bytes(directory / "plan.json"), "plan")
        target = plan.get("normalized", {}).get("environment")
        environment = {"development": "dev", "production": "prod"}.get(target)
        if environment not in ("dev", "prod"):
            return False
        active = observe_service(plan, environment)
    except release_notes.ReleaseNotesError:
        return False
    build = active["build_info"]
    return active["service_uid"] == marker.get("service_uid") and \
        active["service_generation"] == marker.get("service_generation") and active["revision"] == marker.get("revision") and \
        active["image_digest"] == marker.get("image_digest") and active["service"] == marker.get("service") and \
        all(build.get(key) == marker.get(marker_key) for key, marker_key in (
            ("environment", "environment"), ("product_version", "product_version"), ("commit", "commit"),
            ("branch", "branch"), ("tag", "tag"), ("image_tag", "image_tag"), ("service", "service"),
            ("revision", "revision")))


def active_service_url(directory: Path) -> str:
    plan = read_object(read_bytes(directory / "plan.json"), "plan")
    bff = plan.get("normalized", {}).get("bff")
    gcp = plan.get("normalized", {}).get("gcp")
    if not isinstance(bff, dict) or not isinstance(gcp, dict):
        raise release_notes.ReleaseNotesError("exact_readback", "service-url-unavailable")
    service, region, project = bff.get("service_name"), gcp.get("region"), gcp.get("project_id")
    if not all(isinstance(value, str) and value for value in (service, region, project)):
        raise release_notes.ReleaseNotesError("exact_readback", "service-url-unavailable")
    service_object = provider_json(["gcloud", "run", "services", "describe", service, "--project", project,
                                    "--region", region, "--format=json", "--quiet"])
    status = service_object.get("status")
    url = status.get("url") if isinstance(status, dict) else None
    if not isinstance(url, str) or not url.startswith("https://"):
        url = status.get("uri") if isinstance(status, dict) else None
    if not isinstance(url, str) or not url.startswith("https://"):
        raise release_notes.ReleaseNotesError("exact_readback", "service-url-unavailable")
    return url


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", required=True)
    parser.add_argument("--operation", required=True, choices=sorted(release_notes.TRANSITION_OPS))
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--run-attempt", required=True, type=int)
    args = parser.parse_args()
    if not re.fullmatch(r"[1-9][0-9]*", args.run_id) or args.run_attempt < 1:
        raise SystemExit("invalid workflow run identity")
    try:
        marker, sidecar = capture(Path(args.directory), args.operation, args.run_id, args.run_attempt)
    except release_notes.ReleaseNotesError as error:
        print(json.dumps({"stage": error.stage, "safe_class": error.safe_class}, sort_keys=True))
        raise SystemExit(1)
    print(json.dumps({"stage": "service_readback", "status": "verified",
                      "operation": args.operation, "revision": marker["revision"],
                      "service_generation": marker["service_generation"],
                      "sidecar_sha256": hashlib.sha256(sidecar).hexdigest()}, sort_keys=True))


if __name__ == "__main__":
    main()
