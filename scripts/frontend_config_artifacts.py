#!/usr/bin/env python3
"""Validate and stage the public frontend config artifact handoff."""

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import urlsplit


CONFIG_NAME = "frontend-config.json"
MANIFEST_NAME = "manifest.json"
GENERATOR_WORKFLOW = ".github/workflows/generate-frontend-config.yml"
MANIFEST_KEYS = {"schema_version", "environment", "source_sha", "config_file", "config_sha256"}


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def read_json(data):
    return json.loads(data.decode("utf-8"), object_pairs_hook=_object)


def validate_config(data):
    if not 0 < len(data) <= 4096:
        raise ValueError("config size")
    config = read_json(data)
    if not isinstance(config, dict) or set(config) != {"schema_version", "api_url", "auth_url"}:
        raise ValueError("config fields")
    if isinstance(config["schema_version"], bool) or config["schema_version"] != 1:
        raise ValueError("config schema_version")
    for key in ("api_url", "auth_url"):
        value = config[key]
        if not isinstance(value, str) or not value or value != value.strip():
            raise ValueError(f"config {key}")
        parsed = urlsplit(value)
        if (parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password
                or parsed.query or parsed.fragment):
            raise ValueError(f"config {key}")
    return config


def prepare_artifact(environment, source_sha, actual_source_sha, config_path, output_dir):
    if environment not in ("dev", "prod"):
        raise ValueError("environment")
    if not re.fullmatch(r"[0-9a-f]{40}", source_sha) or actual_source_sha != source_sha:
        raise ValueError("source checkout does not match workflow SHA")
    config_bytes = Path(config_path).read_bytes()
    validate_config(config_bytes)

    output = Path(output_dir)
    output.mkdir(parents=True, exist_ok=True)
    for child in output.iterdir():
        if child.is_dir() and not child.is_symlink():
            raise ValueError("artifact output directory is not empty")
        child.unlink()
    (output / CONFIG_NAME).write_bytes(config_bytes)
    manifest = {
        "schema_version": 1,
        "environment": environment,
        "source_sha": source_sha,
        "config_file": CONFIG_NAME,
        "config_sha256": hashlib.sha256(config_bytes).hexdigest(),
    }
    (output / MANIFEST_NAME).write_text(json.dumps(manifest, sort_keys=True) + "\n", encoding="utf-8")
    return manifest


def validate_artifact_metadata(metadata, run, artifact_id, environment):
    if not re.fullmatch(r"[1-9][0-9]*", str(artifact_id)):
        raise ValueError("artifact ID")
    if not isinstance(metadata, dict) or not isinstance(run, dict):
        raise ValueError("artifact or run metadata")
    if (str(metadata.get("id")) != str(artifact_id)
            or metadata.get("name") != f"frontend-config-{environment}"
            or metadata.get("expired") is not False):
        raise ValueError("artifact identity, name, or expiry")
    workflow_run = metadata.get("workflow_run")
    run_id = run.get("id")
    if isinstance(run_id, bool) or not isinstance(run_id, int) or run_id < 1:
        raise ValueError("generation run ID")
    if (not isinstance(workflow_run, dict) or str(workflow_run.get("id")) != str(run.get("id"))
            or workflow_run.get("head_sha") != run.get("head_sha")):
        raise ValueError("artifact generation run identity")
    path = run.get("path")
    if isinstance(path, str):
        path = path.split("@", 1)[0]
    if (run.get("event") != "workflow_dispatch" or run.get("status") != "completed" or run.get("conclusion") != "success"
            or path != GENERATOR_WORKFLOW):
        raise ValueError("generation workflow did not complete successfully")
    source_sha = run.get("head_sha")
    if not isinstance(source_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", source_sha):
        raise ValueError("generation checkout SHA")
    return {"run_id": str(run["id"]), "source_sha": source_sha}


def validate_artifact_directory(directory, environment, source_sha):
    if environment not in ("dev", "prod") or not re.fullmatch(r"[0-9a-f]{40}", source_sha):
        raise ValueError("expected environment or source SHA")
    root = Path(directory)
    entries = list(root.iterdir())
    if {entry.name for entry in entries} != {CONFIG_NAME, MANIFEST_NAME}:
        raise ValueError("artifact must contain only frontend-config.json and manifest.json")
    if any(entry.is_symlink() or not entry.is_file() for entry in entries):
        raise ValueError("artifact entries must be regular files")

    config_bytes = (root / CONFIG_NAME).read_bytes()
    validate_config(config_bytes)
    manifest = read_json((root / MANIFEST_NAME).read_bytes())
    if not isinstance(manifest, dict) or set(manifest) != MANIFEST_KEYS:
        raise ValueError("manifest fields")
    if (isinstance(manifest["schema_version"], bool) or manifest["schema_version"] != 1
            or manifest["environment"] != environment
            or manifest["source_sha"] != source_sha or manifest["config_file"] != CONFIG_NAME
            or manifest["config_sha256"] != hashlib.sha256(config_bytes).hexdigest()):
        raise ValueError("manifest does not match selected artifact")
    return manifest


def _read(path):
    return read_json(Path(path).read_bytes())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("prepare")
    prepare.add_argument("--environment", required=True)
    prepare.add_argument("--source-sha", required=True)
    prepare.add_argument("--config", required=True)
    prepare.add_argument("--output", required=True)
    inspect = commands.add_parser("inspect")
    inspect.add_argument("--artifact-metadata", required=True)
    inspect.add_argument("--run-metadata", required=True)
    inspect.add_argument("--artifact-id", required=True)
    inspect.add_argument("--environment", required=True)
    validate = commands.add_parser("validate")
    validate.add_argument("--directory", required=True)
    validate.add_argument("--environment", required=True)
    validate.add_argument("--source-sha", required=True)
    args = parser.parse_args(argv)

    try:
        if args.command == "prepare":
            actual_sha = subprocess.check_output(
                ["git", "rev-parse", "--verify", "HEAD"], text=True, stderr=subprocess.DEVNULL
            ).strip()
            manifest = prepare_artifact(args.environment, args.source_sha, actual_sha, args.config, args.output)
            print(json.dumps(manifest, sort_keys=True))
        elif args.command == "inspect":
            metadata = _read(args.artifact_metadata)
            run = _read(args.run_metadata)
            identity = validate_artifact_metadata(metadata, run, args.artifact_id, args.environment)
            for key, value in identity.items():
                print(f"{key}={value}")
        else:
            manifest = validate_artifact_directory(args.directory, args.environment, args.source_sha)
            print(manifest["config_sha256"])
    except (OSError, UnicodeError, ValueError, json.JSONDecodeError, subprocess.CalledProcessError) as error:
        print(f"invalid frontend config artifact: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
