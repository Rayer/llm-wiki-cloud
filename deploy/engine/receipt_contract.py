#!/usr/bin/env python3
"""Strict local reader for the versioned Auth and DEV image receipts."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path


COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
SHA_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REPOSITORY_ID_RE = re.compile(
    r"^(?:github|gitlab):(?:[0-9]+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$"
)
COMPONENTS = {"auth", "bff", "worker", "exportjob"}


class ReceiptError(ValueError):
    pass


def _pairs_no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ReceiptError(f"duplicate receipt key {key!r}")
        result[key] = value
    return result


def load_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=_pairs_no_duplicates)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ReceiptError(f"cannot read valid receipt JSON: {path}") from exc


def _object(value, label):
    if not isinstance(value, dict):
        raise ReceiptError(f"{label} must be an object")
    return value


def _positive_int(value):
    return type(value) is int and value > 0


def image_digest(image):
    if not isinstance(image, str):
        raise ReceiptError("receipt image is missing")
    match = re.search(r"@sha256:([0-9a-f]{64})$", image)
    if match is None:
        raise ReceiptError("receipt image must have an immutable SHA-256 digest")
    return "sha256:" + match.group(1)


def _validate_config(config):
    config = _object(config, "receipt config")
    if (config.get("environment") != "development" or
            config.get("path") != "deploy/environments/development.yaml" or
            not SHA_RE.fullmatch(str(config.get("fingerprint", "")))):
        raise ReceiptError("DEV config fingerprint or environment is invalid")


def _validate_components_and_images(dev, mode):
    components = dev.get("components")
    images = _object(dev.get("images"), "DEV images")
    if not isinstance(components, list) or any(not isinstance(item, str) or item not in COMPONENTS for item in components):
        raise ReceiptError("DEV components are malformed")
    if len(components) != len(set(components)):
        raise ReceiptError("DEV components contain duplicates")
    expected_images = set(components)
    if mode == "independent":
        expected_images.add("auth")
    if set(images) != expected_images:
        raise ReceiptError("DEV image map does not match selected components and required Auth pairing")
    for component, image in images.items():
        image_digest(image)
        if not image.rsplit("@", 1)[0].endswith("/" + {
            "auth": "llm-wiki-auth", "bff": "llm-wiki-bff",
            "worker": "olw-pipeline", "exportjob": "llm-wiki-bff-export-job",
        }[component]):
            raise ReceiptError(f"DEV image for {component} uses the wrong component repository")
    return components, images


def validate_dev_receipt(dev, mode, expected_lwc_source, expected_auth_source=None,
                         expected_components=None):
    dev = _object(dev, "DEV receipt")
    expected_lwc_source = _object(expected_lwc_source, "expected LWC source")
    _validate_config(dev.get("config"))
    components, images = _validate_components_and_images(dev, mode)
    if expected_components is not None and components != expected_components:
        raise ReceiptError("DEV selected components do not match the release plan")
    if mode in ("legacy", "legacy_monorepo"):
        if dev.get("schema") != "lwc-306-dev-image-receipt-v1":
            raise ReceiptError("legacy_monorepo_source accepts only DEV receipt v1")
        if set(dev) != {"schema", "source", "config", "components", "images"}:
            raise ReceiptError("legacy DEV receipt has unknown or missing fields")
        source = _object(dev.get("source"), "legacy LWC source")
        if set(source) != {"sha", "ref", "workflow_path", "event", "run_id", "run_attempt"}:
            raise ReceiptError("legacy LWC source has unknown or missing fields")
        if (source.get("sha") != expected_lwc_source.get("sha") or
                source.get("ref") != "develop" or
                source.get("workflow_path") != ".github/workflows/deploy-dev.yml" or
                source.get("event") != "workflow_dispatch" or
                source.get("run_id") != expected_lwc_source.get("run_id") or
                source.get("run_attempt") != expected_lwc_source.get("run_attempt") or
                not COMMIT_RE.fullmatch(str(source.get("sha", ""))) or
                not _positive_int(source.get("run_id")) or not _positive_int(source.get("run_attempt"))):
            raise ReceiptError("legacy DEV source does not match the selected LWC run")
        if set(images) != set(components):
            raise ReceiptError("legacy DEV image map must contain selected components only")
        if expected_lwc_source.get("repository_id") not in (None, ""):
            if not REPOSITORY_ID_RE.fullmatch(str(expected_lwc_source["repository_id"])):
                raise ReceiptError("expected LWC repository identity is invalid")
        return True

    if mode != "independent":
        raise ReceiptError(f"unknown receipt mode {mode!r}")
    if dev.get("schema") != "lwc-306-dev-image-receipt-v2":
        raise ReceiptError("independent mode accepts only DEV receipt v2")
    if set(dev) != {"schema", "source", "config", "components", "images"}:
        raise ReceiptError("independent DEV receipt has unknown or missing fields")
    source = _object(dev.get("source"), "dual-source identity")
    if set(source) != {"lwc", "auth"}:
        raise ReceiptError("dual-source identity has unknown or missing fields")
    lwc = _object(source.get("lwc"), "LWC source")
    auth = _object(source.get("auth"), "Auth source")
    if set(lwc) != {"repository_id", "sha", "run_id", "run_attempt"}:
        raise ReceiptError("LWC source has unknown or missing fields")
    if set(auth) != {"repository_id", "revision", "tree_sha256", "run_id", "run_attempt", "artifact_id", "artifact_digest"}:
        raise ReceiptError("Auth source has unknown or missing fields")
    for value in (lwc, auth):
        if not REPOSITORY_ID_RE.fullmatch(str(value.get("repository_id", ""))):
            raise ReceiptError("source repository identity is not immutable")
        if not _positive_int(value.get("run_id")) or not _positive_int(value.get("run_attempt")):
            raise ReceiptError("source workflow run identity is invalid")
    if lwc["repository_id"] == auth["repository_id"]:
        raise ReceiptError("LWC and Auth repository identities must be distinct")
    if (not COMMIT_RE.fullmatch(str(lwc.get("sha", ""))) or
            not COMMIT_RE.fullmatch(str(auth.get("revision", ""))) or
            lwc["sha"] == auth["revision"] or
            not SHA_RE.fullmatch(str(auth.get("tree_sha256", ""))) or
            not SHA_RE.fullmatch(str(auth.get("artifact_digest", ""))) or
            not isinstance(auth.get("artifact_id"), str) or not auth["artifact_id"] or
            auth["artifact_id"] in ("local-snapshot", "local_snapshot")):
        raise ReceiptError("independent Auth source/build identity is invalid")
    if (lwc.get("repository_id") != expected_lwc_source.get("repository_id") or
            lwc.get("sha") != expected_lwc_source.get("sha") or
            lwc.get("run_id") != expected_lwc_source.get("run_id") or
            lwc.get("run_attempt") != expected_lwc_source.get("run_attempt")):
        raise ReceiptError("LWC source does not match the selected workflow run")
    if expected_auth_source is not None:
        expected_auth_source = _object(expected_auth_source, "expected Auth source")
        if auth != expected_auth_source:
            raise ReceiptError("Auth source does not match the explicit immutable input tuple")
    if images["auth"] and image_digest(images["auth"]) != auth["artifact_digest"]:
        raise ReceiptError("Auth image digest does not match the Auth source artifact digest")
    return True


def validate_receipt_pair(auth, dev, mode, expected_auth_config_inputs=None,
                          expected_lwc_source=None, expected_auth_source=None,
                          expected_components=None):
    auth = _object(auth, "Auth component receipt")
    dev = _object(dev, "DEV receipt")
    schema = 2 if mode in ("legacy", "legacy_monorepo") else 3
    expected_keys = {
        "schema", "component", "identity", "build_sha", "artifact", "target_config",
        "build", "auth_config_inputs", "auth_materialization_schema",
    }
    if schema == 3:
        expected_keys.add("auth_source")
    if type(auth.get("schema")) is not int or auth["schema"] != schema or set(auth) != expected_keys:
        raise ReceiptError("Auth component receipt has an unsupported schema or unknown/missing fields")
    if (auth.get("component") != "auth" or not SHA_RE.fullmatch(str(auth.get("identity", ""))) or
            not COMMIT_RE.fullmatch(str(auth.get("build_sha", ""))) or auth.get("target_config") is not None):
        raise ReceiptError("Auth component identity is malformed")
    build = _object(auth.get("build"), "Auth build identity")
    if (build.get("identity_verified") is not True or build.get("status") != "SUCCESS" or
            not isinstance(build.get("build_id"), str) or not build["build_id"]):
        raise ReceiptError("Auth build identity is not verified")
    if expected_lwc_source is None:
        source = dev.get("source", {})
        expected_lwc_source = source.get("lwc", {}) if mode == "independent" else {
            "sha": source.get("sha"), "run_id": source.get("run_id"),
            "run_attempt": source.get("run_attempt"),
        }
    validate_dev_receipt(dev, mode, expected_lwc_source, expected_auth_source,
                         expected_components)
    image = _object(auth.get("artifact"), "Auth artifact").get("image")
    auth_digest = image_digest(image)
    inputs = _object(auth.get("auth_config_inputs"), "Auth config input snapshot")
    if (type(auth.get("auth_materialization_schema")) is not int or
            auth.get("auth_materialization_schema") != 1 or
            type(inputs.get("schema_version")) is not int or inputs.get("schema_version") not in (1, 2, 3) or
            inputs.get("target") != "auth"):
        raise ReceiptError("Auth materialization schema is missing")
    if expected_auth_config_inputs is not None and inputs != expected_auth_config_inputs:
        raise ReceiptError("Auth config input snapshot differs from the expected snapshot")

    if mode in ("legacy", "legacy_monorepo"):
        if auth.get("schema") != 2 or auth.get("component") != "auth":
            raise ReceiptError("legacy_monorepo_source accepts only Auth receipt schema 2")
        if not COMMIT_RE.fullmatch(str(auth.get("build_sha", ""))) or auth.get("build_sha") != dev["source"]["sha"]:
            raise ReceiptError("legacy Auth build_sha does not match the LWC source SHA")
        if inputs.get("source_sha") != auth["build_sha"]:
            raise ReceiptError("legacy Auth config inputs do not match the LWC source")
        if dev["images"].get("auth") != image or auth_digest != image_digest(dev["images"].get("auth")):
            raise ReceiptError("legacy Auth receipt image does not match the DEV image")
        return True

    if mode != "independent":
        raise ReceiptError(f"unknown receipt mode {mode!r}")
    if auth.get("schema") != 3 or auth.get("component") != "auth":
        raise ReceiptError("independent mode accepts only Auth receipt schema 3")
    if not COMMIT_RE.fullmatch(str(auth.get("build_sha", ""))) or auth.get("build_sha") != dev["source"]["lwc"]["sha"]:
        raise ReceiptError("Auth receipt build_sha does not match the LWC release SHA")
    if inputs.get("source_sha") != dev["source"]["lwc"]["sha"]:
        raise ReceiptError("Auth config inputs do not match the LWC release SHA")
    source = _object(auth.get("auth_source"), "Auth component source")
    if set(source) != {"repository_id", "revision", "tree_sha256", "artifact_id", "build_run_id", "build_run_attempt"}:
        raise ReceiptError("Auth component source has unknown or missing fields")
    if (not REPOSITORY_ID_RE.fullmatch(str(source.get("repository_id", ""))) or
            not COMMIT_RE.fullmatch(str(source.get("revision", ""))) or
            not SHA_RE.fullmatch(str(source.get("tree_sha256", ""))) or
            not isinstance(source.get("artifact_id"), str) or not source["artifact_id"] or
            source["artifact_id"] in ("local-snapshot", "local_snapshot") or
            not _positive_int(source.get("build_run_id")) or
            not _positive_int(source.get("build_run_attempt"))):
        raise ReceiptError("Auth component source identity is invalid")
    auth_dev = dev["source"]["auth"]
    comparisons = (
        ("repository_id", "repository_id"), ("revision", "revision"),
        ("tree_sha256", "tree_sha256"), ("artifact_id", "artifact_id"),
        ("build_run_id", "run_id"), ("build_run_attempt", "run_attempt"),
    )
    for auth_field, dev_field in comparisons:
        if source.get(auth_field) != auth_dev.get(dev_field):
            raise ReceiptError(f"Auth component {auth_field} does not match the DEV source")
    if auth_digest != auth_dev.get("artifact_digest") or image != dev["images"].get("auth"):
        raise ReceiptError("independent Auth artifact digest does not match the paired DEV image")
    if expected_auth_source is not None and auth_dev != expected_auth_source:
        raise ReceiptError("Auth source does not match the explicit immutable input tuple")
    return True


def _cmd_validate(args):
    dev = load_json(Path(args.dev_receipt))
    expected_lwc = json.loads(args.expected_lwc_json)
    expected_auth = json.loads(args.expected_auth_json) if args.expected_auth_json else None
    expected_components = json.loads(args.expected_components_json) if args.expected_components_json else None
    if args.pair:
        auth = load_json(Path(args.auth_receipt))
        validate_receipt_pair(auth, dev, args.mode,
                              load_json(Path(args.expected_auth_config)) if args.expected_auth_config else None,
                              expected_lwc, expected_auth, expected_components)
    else:
        validate_dev_receipt(dev, args.mode, expected_lwc, expected_auth, expected_components)
    print("GAT4_RECEIPT_VALIDATION=PASS mode=" + args.mode)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    validate = commands.add_parser("validate")
    validate.add_argument("--dev-receipt", required=True)
    validate.add_argument("--mode", choices=("legacy_monorepo", "independent"), required=True)
    validate.add_argument("--expected-lwc-json", required=True)
    validate.add_argument("--expected-auth-json")
    validate.add_argument("--expected-components-json")
    validate.add_argument("--pair", action="store_true")
    validate.add_argument("--auth-receipt")
    validate.add_argument("--expected-auth-config")
    args = parser.parse_args()
    if args.pair and not args.auth_receipt:
        parser.error("--pair requires --auth-receipt")
    try:
        _cmd_validate(args)
    except ReceiptError as exc:
        parser.exit(1, f"receipt validation failed: {exc}\n")


if __name__ == "__main__":
    main()
