#!/usr/bin/env python3
"""Download and validate the frozen GateAuth CI source/image package."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import re
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import PurePosixPath


REPOSITORY_ID = 1439745097
REPOSITORY_NAME = "Rayer/gateauth"
WORKFLOW_PATH = ".github/workflows/publish.yml"
ARTIFACT_PREFIX = "gateauth-ci-package-"
IMAGE_PREFIX = "asia-east1-docker.pkg.dev/llm-wiki-cloud/gateauth-ci-images/gateauth"
RETAINED_IMAGE_PREFIX = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth"
MAX_ARTIFACT_BYTES = 256 * 1024 * 1024
MAX_SOURCE_BYTES = 128 * 1024 * 1024
MAX_MANIFEST_BYTES = 1024 * 1024
SHA_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
BUILD_ID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.I)
VERSION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.+-]*$")
API_ROOT = "https://api.github.com"


class ArtifactError(ValueError):
    pass


def _reject_duplicate_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ArtifactError("JSON contains a duplicate field")
        result[key] = value
    return result


def json_bytes(data: bytes, label: str):
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=_reject_duplicate_pairs)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ArtifactError(f"{label} is not valid UTF-8 JSON") from exc


def sha256_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def canonical_json(value) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def _positive_int(value) -> bool:
    return type(value) is int and value > 0


def _safe_archive_name(name: str) -> bool:
    path = PurePosixPath(name)
    return bool(name and path.as_posix() == name and not path.is_absolute() and
                ".." not in path.parts and "." not in path.parts and "\\" not in name)


def _tree_files(source_tar: bytes) -> dict[str, tuple[int, bytes]]:
    if not source_tar or len(source_tar) > MAX_SOURCE_BYTES:
        raise ArtifactError("source archive size is invalid")
    files = {}
    seen = set()
    total = 0
    try:
        import tarfile

        with tarfile.open(fileobj=io.BytesIO(source_tar), mode="r:*") as archive:
            for member in archive.getmembers():
                name = member.name[:-1] if member.isdir() and member.name.endswith("/") else member.name
                if not _safe_archive_name(name) or name in seen:
                    raise ArtifactError("source archive contains an unsafe or duplicate path")
                seen.add(name)
                if member.isdir():
                    continue
                if not member.isfile():
                    raise ArtifactError("source archive contains a non-regular file")
                total += member.size
                if member.size < 0 or total > MAX_SOURCE_BYTES:
                    raise ArtifactError("source archive contents exceed the 128 MiB limit")
                stream = archive.extractfile(member)
                if stream is None:
                    raise ArtifactError("source archive file cannot be read")
                body = stream.read(MAX_SOURCE_BYTES + 1)
                if len(body) != member.size:
                    raise ArtifactError("source archive file size does not match its header")
                # Git stores only the executable bit; archive headers may inherit umask bits.
                files[name] = (0o755 if member.mode & 0o111 else 0o644, body)
    except ArtifactError:
        raise
    except Exception as exc:
        raise ArtifactError("source archive is invalid") from exc
    return files


def _tree_digest(files: dict[str, tuple[int, bytes]]) -> str:
    digest = hashlib.sha256()
    for name in sorted(files):
        if not _safe_archive_name(name):
            raise ArtifactError("source tree contains an unsafe path")
        mode, body = files[name]
        encoded = name.encode("utf-8")
        digest.update(len(encoded).to_bytes(8, "big"))
        digest.update(encoded)
        digest.update((mode & 0o7777).to_bytes(4, "big"))
        digest.update(len(body).to_bytes(8, "big"))
        digest.update(body)
    return "sha256:" + digest.hexdigest()


def _inputs_digest(source_tar: bytes, files: dict[str, tuple[int, bytes]], revision: str,
                   image_prefix: str, product_version: str) -> str:
    required = ("Dockerfile", "cloudbuild.yaml", "VERSION", "go.mod", "go.sum")
    if any(name not in files for name in required):
        raise ArtifactError("source archive is missing a required build input")
    image_tag = f"{image_prefix}:{revision}"
    descriptor = {
        "schema": "gateauth-ci-inputs-v1",
        "source_archive_sha256": sha256_bytes(source_tar),
        "source_tree_sha256": _tree_digest(files),
        "source_revision": revision,
        "image_tag": image_tag,
        "product_version": product_version,
        "build_files": {name: sha256_bytes(files[name][1]) for name in required},
    }
    return sha256_bytes(canonical_json(descriptor))


def _check_manifest(manifest, envelope, expected_source, expected_project_id,
                    image_prefix) -> tuple[dict, dict[str, tuple[int, bytes]]]:
    required = {"schema", "component", "product_version", "source", "ci", "payload",
                "build", "artifact", "contracts"}
    if not isinstance(manifest, dict) or set(manifest) != required:
        raise ArtifactError("build manifest has an unsupported shape")
    if manifest["schema"] != "gateauth-ci-build-v1" or manifest["component"] != "auth":
        raise ArtifactError("build manifest schema or component is invalid")
    product_version = manifest["product_version"]
    if not isinstance(product_version, str) or not VERSION_RE.fullmatch(product_version):
        raise ArtifactError("product version is invalid")

    source = manifest["source"]
    if not isinstance(source, dict) or set(source) != {"repository_id", "revision", "tree_sha256"}:
        raise ArtifactError("manifest source identity has an unsupported shape")
    if (source.get("repository_id") != f"github:{expected_source['repository_id']}" or
            source.get("revision") != expected_source["revision"] or
            source.get("tree_sha256") != expected_source["tree_sha256"] or
            not REVISION_RE.fullmatch(str(source.get("revision", ""))) or
            not SHA_RE.fullmatch(str(source.get("tree_sha256", "")))):
        raise ArtifactError("manifest source does not match the immutable Auth source tuple")

    ci = manifest["ci"]
    if not isinstance(ci, dict) or set(ci) != {"workflow_id", "run_id", "run_attempt"}:
        raise ArtifactError("manifest CI identity has an unsupported shape")
    if (ci.get("workflow_id") != envelope["workflow_id"] or
            ci.get("run_id") != expected_source["run_id"] or
            ci.get("run_attempt") != expected_source["run_attempt"] or
            not all(_positive_int(ci.get(key)) for key in ci)):
        raise ArtifactError("manifest workflow identity does not match the external run")

    payload = manifest["payload"]
    if (not isinstance(payload, dict) or
            set(payload) != {"source_archive_file", "source_archive_sha256", "source_archive_bytes"} or
            payload.get("source_archive_file") != "source.tar" or
            not SHA_RE.fullmatch(str(payload.get("source_archive_sha256", ""))) or
            not _positive_int(payload.get("source_archive_bytes")) or
            payload["source_archive_bytes"] > MAX_SOURCE_BYTES):
        raise ArtifactError("manifest source archive descriptor is invalid")

    build = manifest["build"]
    if (not isinstance(build, dict) or
            set(build) != {"project_id", "project_number", "location", "build_id", "status", "inputs_digest"} or
            build.get("project_id") != expected_project_id or
            not isinstance(build.get("project_number"), str) or
            not re.fullmatch(r"[1-9][0-9]{5,19}", build["project_number"]) or
            build.get("location") != "global" or
            not BUILD_ID_RE.fullmatch(str(build.get("build_id", ""))) or
            build.get("status") != "SUCCESS" or
            not SHA_RE.fullmatch(str(build.get("inputs_digest", "")))):
        raise ArtifactError("manifest provider build identity is invalid")

    artifact = manifest["artifact"]
    if (not isinstance(artifact, dict) or set(artifact) != {"image", "image_digest"} or
            not SHA_RE.fullmatch(str(artifact.get("image_digest", ""))) or
            artifact.get("image") != f"{image_prefix}@{artifact['image_digest']}"):
        raise ArtifactError("manifest image digest or registry identity is invalid")

    contracts = manifest["contracts"]
    if (not isinstance(contracts, dict) or
            set(contracts) != {"auth_config_schema_versions", "lwc_consumer_schema_version"} or
            contracts.get("auth_config_schema_versions") != [1, 2, 3] or
            contracts.get("lwc_consumer_schema_version") != 1):
        raise ArtifactError("manifest consumer contract versions are unsupported")

    return manifest


def verify_package(package_zip: bytes, envelope: dict, expected_source: dict,
                   expected_project_id: str, image_prefix: str) -> dict:
    if not isinstance(envelope, dict) or set(envelope) != {
            "repository_id", "run_id", "run_attempt", "artifact_id", "artifact_sha256",
            "artifact_bytes", "workflow_id"}:
        raise ArtifactError("external artifact envelope has an unsupported shape")
    ids = ("repository_id", "run_id", "run_attempt", "artifact_id", "workflow_id")
    if not all(_positive_int(envelope.get(key)) for key in ids):
        raise ArtifactError("external artifact envelope identity is invalid")
    if (envelope["repository_id"] != expected_source["repository_id"] or
            envelope["run_id"] != expected_source["run_id"] or
            envelope["run_attempt"] != expected_source["run_attempt"] or
            str(envelope["artifact_id"]) != expected_source["artifact_id"]):
        raise ArtifactError("external artifact envelope does not match the source tuple")
    if (not SHA_RE.fullmatch(str(envelope.get("artifact_sha256", ""))) or
            not _positive_int(envelope.get("artifact_bytes")) or
            envelope["artifact_bytes"] > MAX_ARTIFACT_BYTES or
            envelope["artifact_bytes"] != len(package_zip) or
            envelope["artifact_sha256"] != sha256_bytes(package_zip)):
        raise ArtifactError("downloaded ZIP digest or byte size differs from the API envelope")

    try:
        with zipfile.ZipFile(io.BytesIO(package_zip), "r") as package:
            infos = package.infolist()
            if len(infos) != 2 or {item.filename for item in infos} != {"source.tar", "build-manifest.json"}:
                raise ArtifactError("CI package must contain exactly source.tar and build-manifest.json")
            if len({item.filename for item in infos}) != len(infos):
                raise ArtifactError("CI package contains duplicate paths")
            for info in infos:
                if (not _safe_archive_name(info.filename) or info.is_dir() or
                        info.flag_bits & 0x1 or info.file_size < 0):
                    raise ArtifactError("CI package contains an unsafe ZIP entry")
                mode = (info.external_attr >> 16) & 0xFFFF
                if stat.S_IFMT(mode) not in (0, stat.S_IFREG):
                    raise ArtifactError("CI package contains a non-regular ZIP entry")
                size_limit = MAX_SOURCE_BYTES if info.filename == "source.tar" else MAX_MANIFEST_BYTES
                if info.file_size > size_limit:
                    raise ArtifactError("CI package entry exceeds its size limit")
            if sum(item.file_size for item in infos) > MAX_ARTIFACT_BYTES:
                raise ArtifactError("CI package expands beyond the accepted size limit")
            if package.testzip() is not None:
                raise ArtifactError("CI package ZIP CRC check failed")
            source_tar = package.read("source.tar")
            manifest_bytes = package.read("build-manifest.json")
    except ArtifactError:
        raise
    except (OSError, zipfile.BadZipFile, KeyError, RuntimeError) as exc:
        raise ArtifactError("CI package ZIP is invalid") from exc

    if len(manifest_bytes) > MAX_MANIFEST_BYTES:
        raise ArtifactError("build manifest exceeds the 1 MiB limit")
    manifest = json_bytes(manifest_bytes, "build manifest")
    manifest = _check_manifest(manifest, envelope, expected_source, expected_project_id, image_prefix)
    payload = manifest["payload"]
    if (len(source_tar) != payload["source_archive_bytes"] or
            sha256_bytes(source_tar) != payload["source_archive_sha256"]):
        raise ArtifactError("source.tar checksum or size differs from the manifest")
    files = _tree_files(source_tar)
    if _tree_digest(files) != manifest["source"]["tree_sha256"]:
        raise ArtifactError("source tree digest differs from the manifest")
    if (files.get("go.mod", (0, b""))[1].decode("utf-8", "replace").splitlines()[:1] !=
            ["module github.com/rayer/gateauth"]):
        raise ArtifactError("source archive has the wrong Go module")
    try:
        version = files["VERSION"][1].decode("ascii").strip()
    except (KeyError, UnicodeDecodeError) as exc:
        raise ArtifactError("source archive VERSION is invalid") from exc
    if version != manifest["product_version"]:
        raise ArtifactError("manifest product version differs from source")
    image_tag = f"{image_prefix}:{manifest['source']['revision']}"
    if manifest["build"]["inputs_digest"] != _inputs_digest(
            source_tar, files, manifest["source"]["revision"], image_prefix, version):
        raise ArtifactError("Cloud Build inputs digest differs from the source payload")
    return manifest


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class _SafeArtifactRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlsplit(newurl)
        host = (parsed.hostname or "").lower()
        allowed = (host.endswith(".blob.core.windows.net") or
                   host.endswith(".actions.githubusercontent.com") or
                   host.endswith(".githubusercontent.com"))
        if parsed.scheme != "https" or not allowed or parsed.username or parsed.password:
            raise ArtifactError("artifact download redirect host is not allowed")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _api_json(token: str, path: str):
    if not path.startswith("/") or ".." in path.split("/"):
        raise ArtifactError("GitHub API path is invalid")
    request = urllib.request.Request(
        API_ROOT + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json",
                 "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "lwc-gat4-artifact-reader"},
    )
    opener = urllib.request.build_opener(_NoRedirect())
    try:
        with opener.open(request, timeout=30) as response:
            if response.status != 200:
                raise ArtifactError("GitHub API read did not return 200")
            data = response.read(2 * 1024 * 1024 + 1)
    except urllib.error.HTTPError as exc:
        raise ArtifactError(f"GitHub API read returned HTTP {exc.code}") from None
    except (OSError, urllib.error.URLError) as exc:
        raise ArtifactError("GitHub API read failed") from exc
    if len(data) > 2 * 1024 * 1024:
        raise ArtifactError("GitHub API response exceeds its size limit")
    return json_bytes(data, "GitHub API response")


def _download_zip(token: str, artifact_id: int, expected_bytes: int) -> bytes:
    path = f"/repos/{urllib.parse.quote(REPOSITORY_NAME, safe='/')}/actions/artifacts/{artifact_id}/zip"
    request = urllib.request.Request(
        API_ROOT + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json",
                 "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "lwc-gat4-artifact-reader"},
    )
    opener = urllib.request.build_opener(_NoRedirect())
    try:
        with opener.open(request, timeout=30):
            raise ArtifactError("GitHub artifact download endpoint did not return a redirect")
    except urllib.error.HTTPError as exc:
        if exc.code != 302:
            raise ArtifactError(f"GitHub artifact download returned HTTP {exc.code}") from None
        location = exc.headers.get("Location")
        exc.close()
    except urllib.error.URLError as exc:
        raise ArtifactError("GitHub artifact download request failed") from exc
    parsed = urllib.parse.urlsplit(location or "")
    host = (parsed.hostname or "").lower()
    if (parsed.scheme != "https" or parsed.username or parsed.password or
            not (host.endswith(".blob.core.windows.net") or
                 host.endswith(".actions.githubusercontent.com") or
                 host.endswith(".githubusercontent.com"))):
        raise ArtifactError("GitHub artifact download URL is invalid")
    download_opener = urllib.request.build_opener(_SafeArtifactRedirect())
    try:
        with download_opener.open(urllib.request.Request(location), timeout=60) as response:
            if response.status != 200:
                raise ArtifactError("artifact download did not return 200")
            length = response.headers.get("Content-Length")
            if length and (not length.isdigit() or int(length) != expected_bytes):
                raise ArtifactError("artifact download content length differs from API metadata")
            chunks = []
            size = 0
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                size += len(chunk)
                if size > MAX_ARTIFACT_BYTES or size > expected_bytes:
                    raise ArtifactError("artifact download exceeds its API byte size")
                chunks.append(chunk)
    except ArtifactError:
        raise
    except (OSError, urllib.error.URLError) as exc:
        raise ArtifactError("artifact bytes could not be downloaded") from exc
    data = b"".join(chunks)
    if len(data) != expected_bytes:
        raise ArtifactError("artifact byte size differs from API metadata")
    return data


def download_artifact(repository_id: int, revision: str, tree_sha256: str,
                      artifact_id: int, run_id: int, run_attempt: int,
                      package_path, envelope_path, token: str | None = None) -> dict:
    if repository_id != REPOSITORY_ID:
        raise ArtifactError("repository ID does not match the frozen private source")
    if (not REVISION_RE.fullmatch(revision) or not SHA_RE.fullmatch(tree_sha256) or
            not all(_positive_int(value) for value in (artifact_id, run_id, run_attempt))):
        raise ArtifactError("independent Auth source tuple is malformed")
    token = token or os.environ.get("GATEAUTH_SOURCE_READ_TOKEN", "")
    if not token:
        raise ArtifactError("private GateAuth source reader token is unavailable")

    repository = _api_json(token, f"/repositories/{repository_id}")
    if (not isinstance(repository, dict) or repository.get("id") != repository_id or
            repository.get("private") is not True or repository.get("default_branch") != "main" or
            str(repository.get("full_name", "")).casefold() != REPOSITORY_NAME.casefold()):
        raise ArtifactError("private GateAuth repository identity does not match the frozen target")

    run = _api_json(token, f"/repos/{REPOSITORY_NAME}/actions/runs/{run_id}")
    workflow_id = run.get("workflow_id") if isinstance(run, dict) else None
    run_repository = run.get("repository") if isinstance(run, dict) else None
    head_repository = run.get("head_repository") if isinstance(run, dict) else None
    if (not isinstance(run, dict) or not isinstance(run_repository, dict) or
            not isinstance(head_repository, dict) or run.get("id") != run_id or
            run_repository.get("id") != repository_id or
            head_repository.get("id") != repository_id or
            run.get("run_attempt") != run_attempt or run.get("head_sha") != revision or
            run.get("head_branch") != "main" or run.get("event") != "workflow_dispatch" or
            run.get("path") != WORKFLOW_PATH or run.get("status") != "completed" or
            run.get("conclusion") != "success" or not _positive_int(workflow_id)):
        raise ArtifactError("GateAuth CI run is not the successful exact-SHA main publication")

    artifacts = _api_json(token, f"/repos/{REPOSITORY_NAME}/actions/runs/{run_id}/artifacts")
    expected_name = f"{ARTIFACT_PREFIX}{revision}-{run_attempt}"
    if (not isinstance(artifacts, dict) or artifacts.get("total_count") != 1 or
            not isinstance(artifacts.get("artifacts"), list) or len(artifacts["artifacts"]) != 1):
        raise ArtifactError("GateAuth CI run must own exactly one package artifact")
    listed = artifacts["artifacts"][0]
    listed_run = listed.get("workflow_run") if isinstance(listed, dict) else None
    if (not isinstance(listed, dict) or listed.get("id") != artifact_id or
            listed.get("name") != expected_name or not isinstance(listed_run, dict) or
            listed_run.get("id") != run_id or listed_run.get("repository_id") != repository_id or
            listed_run.get("head_sha") != revision or listed_run.get("head_branch") != "main"):
        raise ArtifactError("GateAuth source artifact locator does not match the run")

    artifact = _api_json(token, f"/repos/{REPOSITORY_NAME}/actions/artifacts/{artifact_id}")
    workflow_run = artifact.get("workflow_run") if isinstance(artifact, dict) else None
    artifact_digest = artifact.get("digest") if isinstance(artifact, dict) else None
    artifact_bytes = artifact.get("size_in_bytes") if isinstance(artifact, dict) else None
    if (not isinstance(artifact, dict) or artifact.get("id") != artifact_id or
            artifact.get("name") != expected_name or artifact.get("expired") is not False or
            not isinstance(workflow_run, dict) or workflow_run.get("id") != run_id or
            workflow_run.get("repository_id") != repository_id or workflow_run.get("head_sha") != revision or
            not SHA_RE.fullmatch(str(artifact_digest or "")) or not _positive_int(artifact_bytes) or
            artifact_bytes > MAX_ARTIFACT_BYTES or listed.get("digest") != artifact_digest or
            listed.get("size_in_bytes") != artifact_bytes):
        raise ArtifactError("GateAuth source artifact metadata is incomplete or mismatched")

    package_zip = _download_zip(token, artifact_id, artifact_bytes)
    if sha256_bytes(package_zip) != artifact_digest:
        raise ArtifactError("downloaded ZIP digest differs from the GitHub API artifact digest")
    envelope = {
        "repository_id": repository_id,
        "run_id": run_id,
        "run_attempt": run_attempt,
        "artifact_id": artifact_id,
        "artifact_sha256": artifact_digest,
        "artifact_bytes": artifact_bytes,
        "workflow_id": workflow_id,
    }
    package_path = os.fspath(package_path)
    envelope_path = os.fspath(envelope_path)
    os.makedirs(os.path.dirname(package_path) or ".", exist_ok=True)
    os.makedirs(os.path.dirname(envelope_path) or ".", exist_ok=True)
    package_tmp = package_path + ".tmp"
    envelope_tmp = envelope_path + ".tmp"
    try:
        with open(package_tmp, "wb") as stream:
            stream.write(package_zip)
        with open(envelope_tmp, "w", encoding="utf-8") as stream:
            json.dump(envelope, stream, sort_keys=True, indent=2)
            stream.write("\n")
        os.replace(package_tmp, package_path)
        os.replace(envelope_tmp, envelope_path)
    finally:
        for path in (package_tmp, envelope_tmp):
            try:
                os.unlink(path)
            except FileNotFoundError:
                pass
    return envelope


def load_package(package_path, envelope_path, expected_source: dict,
                 expected_project_id: str, image_prefix: str) -> dict:
    try:
        with open(package_path, "rb") as stream:
            package_zip = stream.read(MAX_ARTIFACT_BYTES + 1)
        with open(envelope_path, "rb") as stream:
            envelope_bytes = stream.read(64 * 1024 + 1)
        if len(envelope_bytes) > 64 * 1024:
            raise ArtifactError("external artifact envelope exceeds its size limit")
        envelope = json_bytes(envelope_bytes, "artifact envelope")
    except (OSError, ArtifactError) as exc:
        raise ArtifactError("downloaded source package or API envelope is unavailable") from exc
    if len(package_zip) > MAX_ARTIFACT_BYTES:
        raise ArtifactError("downloaded source package exceeds the size limit")
    return verify_package(package_zip, envelope, expected_source, expected_project_id, image_prefix)


def _parser():
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="command", required=True)
    download = commands.add_parser("download")
    download.add_argument("--repository-id", type=int, required=True)
    download.add_argument("--revision", required=True)
    download.add_argument("--tree-sha256", required=True)
    download.add_argument("--artifact-id", type=int, required=True)
    download.add_argument("--run-id", type=int, required=True)
    download.add_argument("--run-attempt", type=int, required=True)
    download.add_argument("--package", required=True)
    download.add_argument("--envelope", required=True)
    return root


def main(argv=None) -> int:
    args = _parser().parse_args(argv)
    try:
        download_artifact(args.repository_id, args.revision, args.tree_sha256,
                          args.artifact_id, args.run_id, args.run_attempt,
                          args.package, args.envelope)
    except ArtifactError as exc:
        print(f"GateAuth CI source: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
