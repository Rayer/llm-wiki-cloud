#!/usr/bin/env python3
"""Build and validate the local-only GateAuth OCI artifact contract."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import re
import ssl
import struct
import sys
import tarfile
from pathlib import Path, PurePosixPath


REPO_ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = REPO_ROOT / "apps/bff/deploy/contracts/gateauth-artifact-v1.schema.json"
CONFIG_RELATIVE = Path("contracts/gateauth-artifact/v1/local-auth-config.test.json")
MANIFEST_NAME = "gateauth-artifact-v1.json"
ARCHIVE_NAME = "gateauth-image.oci.tar"
SHA_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
REMOTE_ID_RE = re.compile(
    r"^(?:github|gitlab):(?:[0-9]+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$"
)


class ContractError(ValueError):
    pass


def sha256_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def tree_digest(root: Path) -> str:
    if not root.is_dir():
        raise ContractError(f"source tree is not a directory: {root}")
    digest = hashlib.sha256()
    files = []
    for path in root.rglob("*"):
        if path.is_symlink():
            raise ContractError(f"source tree contains an unsupported symlink: {path}")
        if path.is_file():
            files.append(path)
    for path in sorted(files, key=lambda p: p.relative_to(root).as_posix()):
        name = path.relative_to(root).as_posix().encode("utf-8")
        mode = path.stat().st_mode & 0o7777
        data = path.read_bytes()
        digest.update(len(name).to_bytes(8, "big"))
        digest.update(name)
        digest.update(mode.to_bytes(4, "big"))
        digest.update(len(data).to_bytes(8, "big"))
        digest.update(data)
    return "sha256:" + digest.hexdigest()


def _reject_duplicate_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ContractError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def load_json_bytes(data: bytes, label: str):
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=_reject_duplicate_pairs)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ContractError(f"{label} is not valid UTF-8 JSON: {exc}") from exc


def load_json(path: Path):
    try:
        return load_json_bytes(path.read_bytes(), str(path))
    except OSError as exc:
        raise ContractError(f"cannot read {path}: {exc}") from exc


def _schema_check(value, schema, path="$"):
    allowed = {
        "$schema", "title", "description", "type", "required", "additionalProperties",
        "properties", "const", "enum", "pattern", "minLength", "items", "minItems"
    }
    unknown = set(schema) - allowed
    if unknown:
        raise ContractError(f"unsupported JSON Schema keywords at {path}: {sorted(unknown)}")
    if "type" in schema:
        expected = schema["type"]
        valid = {
            "object": lambda v: isinstance(v, dict),
            "array": lambda v: isinstance(v, list),
            "string": lambda v: isinstance(v, str),
            "integer": lambda v: type(v) is int,
            "boolean": lambda v: type(v) is bool,
            "null": lambda v: v is None,
        }
        if expected not in valid or not valid[expected](value):
            raise ContractError(f"{path} must be {expected}")
    if "const" in schema and value != schema["const"]:
        raise ContractError(f"{path} must equal {schema['const']!r}")
    if "enum" in schema and value not in schema["enum"]:
        raise ContractError(f"{path} is not an allowed value")
    if isinstance(value, str):
        if len(value) < schema.get("minLength", 0):
            raise ContractError(f"{path} is too short")
        if "pattern" in schema and re.fullmatch(schema["pattern"], value) is None:
            raise ContractError(f"{path} does not match its required pattern")
    if isinstance(value, list):
        if len(value) < schema.get("minItems", 0):
            raise ContractError(f"{path} has too few items")
        if "items" in schema:
            for index, item in enumerate(value):
                _schema_check(item, schema["items"], f"{path}[{index}]")
    if isinstance(value, dict):
        required = schema.get("required", [])
        missing = set(required) - set(value)
        if missing:
            raise ContractError(f"{path} is missing required keys: {sorted(missing)}")
        properties = schema.get("properties", {})
        if schema.get("additionalProperties") is False:
            extras = set(value) - set(properties)
            if extras:
                raise ContractError(f"{path} has unknown keys: {sorted(extras)}")
        for key, child_schema in properties.items():
            if key in value:
                _schema_check(value[key], child_schema, f"{path}.{key}")


def _safe_relative(name: str) -> bool:
    path = PurePosixPath(name)
    return not path.is_absolute() and ".." not in path.parts and name not in ("", ".")


def _tar_files(data: bytes, label: str) -> dict[str, bytes]:
    files = {}
    seen = set()
    try:
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
            for member in archive.getmembers():
                if not _safe_relative(member.name):
                    raise ContractError(f"{label} has unsafe path {member.name!r}")
                if member.name in seen:
                    raise ContractError(f"{label} has duplicate path {member.name!r}")
                seen.add(member.name)
                if member.isdir():
                    continue
                if not member.isfile():
                    raise ContractError(f"{label} contains a non-regular entry {member.name!r}")
                stream = archive.extractfile(member)
                if stream is None:
                    raise ContractError(f"{label} cannot read {member.name!r}")
                files[member.name] = stream.read()
    except (tarfile.TarError, OSError) as exc:
        raise ContractError(f"{label} is not a valid tar archive: {exc}") from exc
    return files


def _load_blob(files: dict[str, bytes], descriptor: dict, label: str) -> bytes:
    if not isinstance(descriptor, dict):
        raise ContractError(f"{label} descriptor is invalid")
    digest = descriptor.get("digest")
    if not isinstance(digest, str) or SHA_RE.fullmatch(digest) is None:
        raise ContractError(f"{label} has an invalid OCI digest")
    path = "blobs/sha256/" + digest.removeprefix("sha256:")
    blob = files.get(path)
    if blob is None:
        raise ContractError(f"{label} blob is missing")
    if sha256_bytes(blob) != digest or descriptor.get("size") != len(blob):
        raise ContractError(f"{label} blob digest or size mismatch")
    return blob


def _validate_linux_binary(binary: bytes):
    if len(binary) < 64 or binary[:4] != b"\x7fELF":
        raise ContractError("image auth payload is not an ELF executable")
    if binary[4] != 2 or binary[5] != 1:
        raise ContractError("image auth payload is not a little-endian 64-bit ELF executable")
    machine = struct.unpack_from("<H", binary, 18)[0]
    if machine != 62:
        raise ContractError("image auth payload is not Linux amd64")
    phoff = struct.unpack_from("<Q", binary, 32)[0]
    phentsize = struct.unpack_from("<H", binary, 54)[0]
    phnum = struct.unpack_from("<H", binary, 56)[0]
    if phentsize < 4 or phoff + phentsize * phnum > len(binary):
        raise ContractError("image auth payload has an invalid ELF program header table")
    for index in range(phnum):
        p_type = struct.unpack_from("<I", binary, phoff + index * phentsize)[0]
        if p_type == 3:
            raise ContractError("image auth payload has a dynamic interpreter; expected a static binary")


def _validate_oci(archive_path: Path, manifest: dict, artifacts_dir: Path):
    outer = _tar_files(archive_path.read_bytes(), "OCI archive")
    layout = load_json_bytes(outer.get("oci-layout", b""), "oci-layout")
    if layout != {"imageLayoutVersion": "1.0.0"}:
        raise ContractError("OCI layout version is invalid")
    index = load_json_bytes(outer.get("index.json", b""), "OCI index")
    if not isinstance(index, dict) or index.get("schemaVersion") != 2 or len(index.get("manifests", [])) != 1:
        raise ContractError("OCI index must have schemaVersion 2 and one image manifest")
    image_descriptor = index["manifests"][0]
    if image_descriptor.get("mediaType") != "application/vnd.oci.image.manifest.v1+json":
        raise ContractError("OCI index does not describe an OCI image manifest")
    if image_descriptor.get("digest") != manifest["artifact"]["digest"]:
        raise ContractError("OCI manifest digest does not match artifact.digest")
    image_data = _load_blob(outer, image_descriptor, "OCI image manifest")
    image = load_json_bytes(image_data, "OCI image manifest")
    if not isinstance(image, dict) or image.get("schemaVersion") != 2 or image.get("mediaType") != "application/vnd.oci.image.manifest.v1+json":
        raise ContractError("OCI image manifest schema or media type is invalid")
    config_desc = image.get("config", {})
    if config_desc.get("mediaType") != "application/vnd.oci.image.config.v1+json":
        raise ContractError("OCI image config media type is invalid")
    config_data = _load_blob(outer, config_desc, "OCI image config")
    config = load_json_bytes(config_data, "OCI image config")
    if not isinstance(config, dict) or config.get("architecture") != "amd64" or config.get("os") != "linux":
        raise ContractError("OCI image platform is not linux/amd64")
    runtime = config.get("config", {})
    if runtime.get("Entrypoint") != ["/auth"] or runtime.get("User") != "65532:65532":
        raise ContractError("OCI runtime entrypoint or non-root user is invalid")
    env = runtime.get("Env", [])
    if "SSL_CERT_FILE=/etc/ssl/cert.pem" not in env or "LWC_APP_CONFIG_PATH=/config/auth.json" not in env:
        raise ContractError("OCI runtime does not declare CA and mounted config paths")
    layers = image.get("layers")
    if not isinstance(layers, list) or len(layers) != 1:
        raise ContractError("OCI image must have one root filesystem layer")
    layer_desc = layers[0]
    if layer_desc.get("mediaType") != "application/vnd.oci.image.layer.v1.tar":
        raise ContractError("OCI root filesystem layer media type is invalid")
    layer_data = _load_blob(outer, layer_desc, "OCI root filesystem layer")
    rootfs = config.get("rootfs", {})
    if rootfs.get("type") != "layers" or rootfs.get("diff_ids") != [sha256_bytes(layer_data)]:
        raise ContractError("OCI root filesystem diff ID does not match its layer")
    layer = _tar_files(layer_data, "OCI root filesystem layer")
    linux_binary = artifacts_dir / manifest["build"]["linux_binary"]["file"]
    if layer.get("auth") != linux_binary.read_bytes():
        raise ContractError("OCI image binary does not match auth-linux-amd64")
    _validate_linux_binary(layer["auth"])
    if not layer.get("etc/ssl/cert.pem"):
        raise ContractError("OCI scratch image has no CA certificate bundle")


def validate_remote_identity(manifest: dict):
    source = manifest.get("source")
    if not isinstance(source, dict):
        raise ContractError("remote artifact is missing source identity")
    if source.get("kind") == "local_snapshot":
        raise ContractError("remote validator rejects local_snapshot source")
    if source.get("kind") != "repository":
        raise ContractError("remote source kind must be repository")
    if REMOTE_ID_RE.fullmatch(str(source.get("repository_id", ""))) is None:
        raise ContractError("remote repository_id must be an immutable provider numeric/object ID")
    if COMMIT_RE.fullmatch(str(source.get("revision", ""))) is None:
        raise ContractError("remote revision must be a full lowercase commit SHA")
    if SHA_RE.fullmatch(str(source.get("tree_sha256", ""))) is None:
        raise ContractError("remote source tree digest is invalid")
    build = manifest.get("build", {})
    if not isinstance(build, dict) or SHA_RE.fullmatch(str(build.get("config_digest", ""))) is None:
        raise ContractError("remote config digest is invalid")
    artifact = manifest.get("artifact", {})
    if not isinstance(artifact, dict) or SHA_RE.fullmatch(str(artifact.get("digest", ""))) is None:
        raise ContractError("remote artifact digest is invalid")


def validate_artifact(
    manifest_path: Path,
    artifacts_dir: Path,
    source_root: Path,
    config_input: Path,
    mode: str = "local",
):
    manifest = load_json(manifest_path)
    if mode == "remote":
        validate_remote_identity(manifest)
        return manifest
    if mode != "local":
        raise ContractError(f"unknown validation mode {mode!r}")
    schema = load_json(SCHEMA_PATH)
    _schema_check(manifest, schema)
    source = manifest["source"]
    actual_tree = tree_digest(source_root)
    if source["tree_sha256"] != actual_tree or source["revision"] != actual_tree:
        raise ContractError("source tree digest or local revision does not match gateauth/")
    if COMMIT_RE.fullmatch(source["origin_lwc_revision"]) is None:
        raise ContractError("origin_lwc_revision is not a full LWC commit SHA")
    go_mod = (source_root / "go.mod").read_text(encoding="utf-8").splitlines()
    if not go_mod or go_mod[0].strip() != "module " + manifest["build"]["module_path"]:
        raise ContractError("manifest module_path does not match gateauth/go.mod")
    if sha256_file(source_root / "go.sum") != manifest["build"]["module_sum_sha256"]:
        raise ContractError("module sum digest does not match gateauth/go.sum")
    if (source_root / "VERSION").read_text(encoding="utf-8").strip() != manifest["build"]["version"]:
        raise ContractError("manifest version does not match gateauth/VERSION")
    if sha256_file(config_input) != manifest["build"]["config_digest"]:
        raise ContractError("config input digest does not match the manifest")
    build = manifest["build"]
    for record in (build["native_binary"], build["linux_binary"], build["test_log"]):
        path = artifacts_dir / record["file"]
        if not path.is_file() or sha256_file(path) != record["sha256"]:
            raise ContractError(f"artifact output digest mismatch: {record['file']}")
    _validate_linux_binary((artifacts_dir / build["linux_binary"]["file"]).read_bytes())
    archive_path = artifacts_dir / manifest["artifact"]["file"]
    if not archive_path.is_file() or sha256_file(archive_path) != manifest["artifact"]["archive_sha256"]:
        raise ContractError("OCI archive SHA-256 does not match artifact.archive_sha256")
    _validate_oci(archive_path, manifest, artifacts_dir)
    return manifest


def _json_bytes(value) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")


def _tar_add(archive: tarfile.TarFile, name: str, data: bytes, mode: int):
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mode = mode
    info.uid = 0
    info.gid = 0
    info.uname = ""
    info.gname = ""
    info.mtime = 0
    archive.addfile(info, io.BytesIO(data))


def _ca_bundle() -> bytes:
    defaults = ssl.get_default_verify_paths()
    candidates = [
        Path(defaults.cafile) if defaults.cafile else None,
        Path("/etc/ssl/cert.pem"),
        Path("/etc/ssl/certs/ca-certificates.crt"),
        Path("/usr/local/etc/openssl@3/cert.pem"),
    ]
    for candidate in candidates:
        if candidate and candidate.is_file():
            data = candidate.read_bytes()
            if b"BEGIN CERTIFICATE" in data:
                return data
    raise ContractError("no host CA certificate bundle found; a local trust bundle is required")


def create_oci_archive(out_dir: Path, version: str, source_revision: str) -> str:
    binary = (out_dir / "auth-linux-amd64").read_bytes()
    _validate_linux_binary(binary)
    layer_stream = io.BytesIO()
    with tarfile.open(fileobj=layer_stream, mode="w", format=tarfile.USTAR_FORMAT) as layer:
        _tar_add(layer, "auth", binary, 0o755)
        for directory in ("etc", "etc/ssl"):
            info = tarfile.TarInfo(directory)
            info.type = tarfile.DIRTYPE
            info.mode = 0o755
            info.uid = 0
            info.gid = 0
            info.uname = ""
            info.gname = ""
            info.mtime = 0
            layer.addfile(info)
        _tar_add(layer, "etc/ssl/cert.pem", _ca_bundle(), 0o644)
    layer_data = layer_stream.getvalue()
    layer_digest = sha256_bytes(layer_data)
    config = {
        "architecture": "amd64",
        "os": "linux",
        "created": "1970-01-01T00:00:00Z",
        "config": {
            "User": "65532:65532",
            "WorkingDir": "/",
            "Env": [
                "PATH=/",
                "SSL_CERT_FILE=/etc/ssl/cert.pem",
                "LWC_APP_CONFIG_PATH=/config/auth.json",
            ],
            "ExposedPorts": {"8080/tcp": {}},
            "Entrypoint": ["/auth"],
            "Labels": {
                "org.opencontainers.image.title": "GateAuth",
                "org.opencontainers.image.version": version,
                "org.opencontainers.image.revision": source_revision,
                "org.opencontainers.image.source": "local-snapshot:gateauth-module",
            },
        },
        "rootfs": {"type": "layers", "diff_ids": [layer_digest]},
        "history": [{"created": "1970-01-01T00:00:00Z", "created_by": "GAT-4 local OCI packager"}],
    }
    config_data = _json_bytes(config)
    config_digest = sha256_bytes(config_data)
    image = {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "config": {
            "mediaType": "application/vnd.oci.image.config.v1+json",
            "digest": config_digest,
            "size": len(config_data),
        },
        "layers": [{
            "mediaType": "application/vnd.oci.image.layer.v1.tar",
            "digest": layer_digest,
            "size": len(layer_data),
        }],
    }
    image_data = _json_bytes(image)
    image_digest = sha256_bytes(image_data)
    index = {
        "schemaVersion": 2,
        "manifests": [{
            "mediaType": "application/vnd.oci.image.manifest.v1+json",
            "digest": image_digest,
            "size": len(image_data),
            "platform": {"architecture": "amd64", "os": "linux"},
            "annotations": {"org.opencontainers.image.ref.name": "local-snapshot/gateauth:" + version},
        }],
    }
    blobs = {
        "blobs/sha256/" + config_digest.removeprefix("sha256:"): config_data,
        "blobs/sha256/" + layer_digest.removeprefix("sha256:"): layer_data,
        "blobs/sha256/" + image_digest.removeprefix("sha256:"): image_data,
    }
    archive_path = out_dir / ARCHIVE_NAME
    with tarfile.open(archive_path, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        _tar_add(archive, "oci-layout", _json_bytes({"imageLayoutVersion": "1.0.0"}), 0o644)
        _tar_add(archive, "index.json", _json_bytes(index), 0o644)
        for name in sorted(blobs):
            _tar_add(archive, name, blobs[name], 0o644)
    return image_digest


def create_manifest(args):
    source_root = Path(args.source_root).resolve()
    out_dir = Path(args.artifacts_dir).resolve()
    config_input = Path(args.config_input).resolve()
    tree = tree_digest(source_root)
    config = load_json(config_input)
    if not isinstance(config, dict) or config.get("environment") != "local" or config.get("target") != "auth":
        raise ContractError("local config input must be a synthetic local Auth JSON object")
    if config_input != (source_root / CONFIG_RELATIVE).resolve():
        raise ContractError("build config input must be the checked-in synthetic local fixture")
    if COMMIT_RE.fullmatch(args.origin_lwc_revision) is None:
        raise ContractError("origin LWC revision must be a full lowercase commit SHA")
    if not (out_dir / "go-test.txt").is_file():
        raise ContractError("go-test.txt is missing; run the versioned build entry first")
    image_digest = create_oci_archive(out_dir, args.version, tree)
    manifest = {
        "schema": "lwc-gateauth-artifact-v1",
        "component": "auth",
        "source": {
            "kind": "local_snapshot",
            "repository_id": "local-snapshot:gateauth-module",
            "revision": tree,
            "tree_sha256": tree,
            "origin_lwc_revision": args.origin_lwc_revision,
        },
        "build": {
            "module_path": "github.com/rayer/gateauth",
            "go_version": args.go_version,
            "module_sum_sha256": sha256_file(source_root / "go.sum"),
            "config_schema_version": config["schema_version"],
            "config_digest": sha256_file(config_input),
            "version": args.version,
            "test_log": {"file": "go-test.txt", "sha256": sha256_file(out_dir / "go-test.txt")},
            "native_binary": {
                "file": "auth",
                "sha256": sha256_file(out_dir / "auth"),
                "goos": args.native_goos,
                "goarch": args.native_goarch,
            },
            "linux_binary": {
                "file": "auth-linux-amd64",
                "sha256": sha256_file(out_dir / "auth-linux-amd64"),
                "goos": "linux",
                "goarch": "amd64",
            },
        },
        "artifact": {
            "kind": "oci_image_archive",
            "file": ARCHIVE_NAME,
            "digest": image_digest,
            "digest_kind": "oci_image_manifest",
            "archive_sha256": sha256_file(out_dir / ARCHIVE_NAME),
        },
    }
    manifest_path = out_dir / MANIFEST_NAME
    manifest_path.write_bytes(_json_bytes(manifest))
    validate_artifact(manifest_path, out_dir, source_root, config_input)
    print("GAT4_OCI_EXTRACTION_VALIDATION=PASS")
    print("GAT4_SOURCE_TREE_SHA256=" + tree)
    print("GAT4_OCI_IMAGE_MANIFEST_DIGEST=" + image_digest)
    print("GAT4_OCI_ARCHIVE_SHA256=" + manifest["artifact"]["archive_sha256"])


def validate_remote_identity(manifest: dict):
    source = manifest.get("source")
    if not isinstance(source, dict):
        raise ContractError("remote artifact is missing source identity")
    if source.get("kind") == "local_snapshot":
        raise ContractError("remote validator rejects local_snapshot source")
    if source.get("kind") != "repository":
        raise ContractError("remote source kind must be repository")
    if REMOTE_ID_RE.fullmatch(str(source.get("repository_id", ""))) is None:
        raise ContractError("remote repository_id must be an immutable provider numeric/object ID")
    if COMMIT_RE.fullmatch(str(source.get("revision", ""))) is None:
        raise ContractError("remote revision must be a full lowercase commit SHA")
    if SHA_RE.fullmatch(str(source.get("tree_sha256", ""))) is None:
        raise ContractError("remote source tree digest is invalid")
    build = manifest.get("build", {})
    if not isinstance(build, dict) or SHA_RE.fullmatch(str(build.get("config_digest", ""))) is None:
        raise ContractError("remote config digest is invalid")
    artifact = manifest.get("artifact", {})
    if not isinstance(artifact, dict) or SHA_RE.fullmatch(str(artifact.get("digest", ""))) is None:
        raise ContractError("remote artifact digest is invalid")


def validate_artifact(
    manifest_path: Path,
    artifacts_dir: Path,
    source_root: Path,
    config_input: Path,
    mode: str = "local",
):
    manifest = load_json(manifest_path)
    if mode == "remote":
        validate_remote_identity(manifest)
        return manifest
    if mode != "local":
        raise ContractError(f"unknown validation mode {mode!r}")
    schema = load_json(SCHEMA_PATH)
    _schema_check(manifest, schema)
    source = manifest["source"]
    actual_tree = tree_digest(source_root)
    if source["tree_sha256"] != actual_tree or source["revision"] != actual_tree:
        raise ContractError("source tree digest or local revision does not match gateauth/")
    if COMMIT_RE.fullmatch(source["origin_lwc_revision"]) is None:
        raise ContractError("origin_lwc_revision is not a full LWC commit SHA")
    go_mod = (source_root / "go.mod").read_text(encoding="utf-8").splitlines()
    if not go_mod or go_mod[0].strip() != "module " + manifest["build"]["module_path"]:
        raise ContractError("manifest module_path does not match gateauth/go.mod")
    if sha256_file(source_root / "go.sum") != manifest["build"]["module_sum_sha256"]:
        raise ContractError("module sum digest does not match gateauth/go.sum")
    if (source_root / "VERSION").read_text(encoding="utf-8").strip() != manifest["build"]["version"]:
        raise ContractError("manifest version does not match gateauth/VERSION")
    if sha256_file(config_input) != manifest["build"]["config_digest"]:
        raise ContractError("config input digest does not match the manifest")
    build = manifest["build"]
    for record in (build["native_binary"], build["linux_binary"], build["test_log"]):
        path = artifacts_dir / record["file"]
        if not path.is_file() or sha256_file(path) != record["sha256"]:
            raise ContractError(f"artifact output digest mismatch: {record['file']}")
    _validate_linux_binary((artifacts_dir / build["linux_binary"]["file"]).read_bytes())
    archive_path = artifacts_dir / manifest["artifact"]["file"]
    if not archive_path.is_file() or sha256_file(archive_path) != manifest["artifact"]["archive_sha256"]:
        raise ContractError("OCI archive SHA-256 does not match artifact.archive_sha256")
    _validate_oci(archive_path, manifest, artifacts_dir)
    return manifest


def _image_digest(image: str) -> str:
    match = re.search(r"@sha256:([0-9a-f]{64})$", image or "")
    if match is None:
        raise ContractError("image is not pinned to a full SHA-256 digest")
    return match.group(1)


def validate_receipt_pair(auth: dict, dev: dict, mode: str, expected_auth_config_inputs=None):
    if mode == "legacy":
        if auth.get("schema") != 2 or dev.get("schema") != "lwc-306-dev-image-receipt-v1":
            raise ContractError("legacy mode accepts only Auth receipt schema 2 and Dev receipt v1")
        if auth.get("component") != "auth":
            raise ContractError("legacy receipt component is not auth")
        source = dev.get("source", {})
        image = auth.get("artifact", {}).get("image")
        if not COMMIT_RE.fullmatch(str(auth.get("build_sha", ""))) or auth.get("build_sha") != source.get("sha"):
            raise ContractError("legacy Auth build_sha does not match the LWC Dev source SHA")
        if source.get("ref") != "develop" or source.get("workflow_path") != ".github/workflows/deploy-dev.yml" or source.get("event") != "workflow_dispatch":
            raise ContractError("legacy Dev source is not the expected development workflow")
        if not isinstance(image, str) or dev.get("images", {}).get("auth") != image:
            raise ContractError("legacy Auth receipt image does not match the Dev image")
        _image_digest(image)
        inputs = auth.get("auth_config_inputs")
        if not isinstance(inputs, dict) or inputs.get("source_sha") != auth["build_sha"] or auth.get("auth_materialization_schema") != 1:
            raise ContractError("legacy Auth config input snapshot does not match its LWC source")
        if expected_auth_config_inputs is not None and inputs != expected_auth_config_inputs:
            raise ContractError("legacy Auth config inputs differ from the expected snapshot")
        return True
    if mode != "independent":
        raise ContractError(f"unknown receipt mode {mode!r}")
    if auth.get("schema") != 3 or dev.get("schema") != "lwc-306-dev-image-receipt-v2":
        raise ContractError("independent mode accepts only Auth receipt schema 3 and Dev receipt v2")
    if auth.get("component") != "auth":
        raise ContractError("independent receipt component is not auth")
    lwc = dev.get("source", {}).get("lwc", {})
    auth_dev = dev.get("source", {}).get("auth", {})
    auth_source = auth.get("auth_source", {})
    for source in (lwc, auth_dev, auth_source):
        if REMOTE_ID_RE.fullmatch(str(source.get("repository_id", ""))) is None:
            raise ContractError("independent source repository ID is not immutable")
    if lwc["repository_id"] == auth_dev["repository_id"]:
        raise ContractError("LWC and Auth repository identities must be independent")
    if auth_source.get("repository_id") != auth_dev["repository_id"]:
        raise ContractError("Auth receipt repository ID does not match the independent Dev source")
    if not COMMIT_RE.fullmatch(str(lwc.get("sha", ""))) or auth.get("build_sha") != lwc["sha"]:
        raise ContractError("Auth receipt build_sha does not match the LWC source SHA")
    if auth.get("auth_config_inputs", {}).get("source_sha") != lwc["sha"]:
        raise ContractError("Auth config input LWC source does not match the Dev source")
    revision = auth_source.get("revision")
    if not COMMIT_RE.fullmatch(str(revision)) or auth_dev.get("revision") != revision:
        raise ContractError("Auth source revision does not match the independent Dev source")
    if revision == lwc["sha"]:
        raise ContractError("Auth source revision is mixed with the LWC source SHA")
    tree = auth_source.get("tree_sha256")
    if not SHA_RE.fullmatch(str(tree)) or auth_dev.get("tree_sha256") != tree:
        raise ContractError("Auth source tree digest does not match the independent Dev source")
    for auth_field, dev_field in (
        ("artifact_id", "artifact_id"),
        ("build_run_id", "run_id"),
        ("build_run_attempt", "run_attempt"),
    ):
        if auth_source.get(auth_field) != auth_dev.get(dev_field):
            raise ContractError(f"Auth source {auth_field} does not match the independent Dev source")
    artifact_id = auth_source.get("artifact_id")
    if not isinstance(artifact_id, str) or not artifact_id or artifact_id == "local-snapshot":
        raise ContractError("independent Auth artifact ID is missing")
    for source, run_field, attempt_field in (
        (auth_source, "build_run_id", "build_run_attempt"),
        (auth_dev, "run_id", "run_attempt"),
        (lwc, "run_id", "run_attempt"),
    ):
        run_id = source.get(run_field)
        attempt = source.get(attempt_field)
        if type(run_id) is not int or run_id <= 0 or type(attempt) is not int or attempt <= 0:
            raise ContractError("independent build run identity must use positive integer IDs and attempts")
    image = auth.get("artifact", {}).get("image")
    image_digest = _image_digest(image)
    if dev.get("images", {}).get("auth") != image:
        raise ContractError("independent Auth image reference does not match the Dev receipt")
    artifact_digest = auth_dev.get("artifact_digest")
    if not SHA_RE.fullmatch(str(artifact_digest)) or image_digest != artifact_digest.removeprefix("sha256:"):
        raise ContractError("independent Auth artifact digest does not match its pinned image digest")
    if auth.get("auth_materialization_schema") != 1 or not isinstance(auth.get("auth_config_inputs"), dict):
        raise ContractError("independent Auth config materialization snapshot is missing")
    return True


def _cmd_tree_digest(args):
    print(tree_digest(Path(args.root)))


def _cmd_verify(args):
    validate_artifact(
        Path(args.manifest),
        Path(args.artifacts_dir),
        Path(args.source_root),
        Path(args.config_input),
        args.mode,
    )
    print("GAT4_ARTIFACT_VALIDATION=PASS mode=" + args.mode)


def _parser():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    tree = subparsers.add_parser("tree-digest", help="print the normalized GateAuth source tree digest")
    tree.add_argument("--root", required=True)
    tree.set_defaults(run=_cmd_tree_digest)
    create = subparsers.add_parser("create", help="package compiled binaries and write the local manifest")
    for name in ("source-root", "config-input", "artifacts-dir", "origin-lwc-revision", "go-version", "native-goos", "native-goarch", "version"):
        create.add_argument("--" + name, required=True)
    create.set_defaults(run=create_manifest)
    verify = subparsers.add_parser("verify", help="validate source, local manifest, binaries, and OCI archive")
    for name in ("manifest", "artifacts-dir", "source-root", "config-input"):
        verify.add_argument("--" + name, required=True)
    verify.add_argument("--mode", choices=("local", "remote"), default="local")
    verify.set_defaults(run=_cmd_verify)
    return parser


def main(argv=None):
    args = _parser().parse_args(argv)
    try:
        args.run(args)
    except (ContractError, OSError, KeyError, TypeError, ValueError, IndexError, AttributeError) as exc:
        print(f"GAT4_LOCAL_VALIDATION=FAIL: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
