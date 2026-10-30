"""One canonical CaC-to-catalog prepare path for every frontend builder."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
GIT_SHA = re.compile(r"^(?:[a-f0-9]{40}|[a-f0-9]{64})$")
CATALOG_SHA = re.compile(r"^[a-f0-9]{64}$")


class I18nPrepareError(RuntimeError):
    pass


def run_stage(args, *, cwd, env, timeout, stage):
    try:
        result = subprocess.run(args, cwd=cwd, env=env, capture_output=True,
                                text=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        raise I18nPrepareError(stage + " timed out") from None
    except OSError:
        raise I18nPrepareError(stage + " could not start") from None
    if result.returncode != 0:
        raise I18nPrepareError(stage + " failed")
    return result.stdout


def current_revision(output, *, source=False):
    pointer = Path(output) / ("i18n/CURRENT" if source else "CURRENT")
    if pointer.is_symlink() or not pointer.is_file():
        raise I18nPrepareError("i18n source revision pointer is unavailable")
    revision = pointer.read_text(encoding="ascii").strip()
    if not CATALOG_SHA.fullmatch(revision):
        raise I18nPrepareError("i18n source revision pointer is invalid")
    return revision


def prepare(environment, output, frontend_root, *, source_sha="", mode="auto", fixture="", env=None):
    if environment not in ("local", "development", "production"):
        raise I18nPrepareError("i18n environment is invalid")
    if source_sha and not GIT_SHA.fullmatch(source_sha):
        raise I18nPrepareError("i18n source revision is invalid")
    if mode not in ("auto", "offline") or (mode == "offline" and fixture):
        raise I18nPrepareError("i18n stage mode is invalid")
    target_environment = {"development": "dev", "production": "prod"}.get(environment, environment)
    root = ROOT.resolve()
    frontend_root = Path(frontend_root).resolve()
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    source_output = output / "source"
    catalog_output = output / "catalog"
    child_env = dict(os.environ if env is None else env)
    child_env.update({
        "GOMAXPROCS": "2",
        "GOFLAGS": "-p=2",
        "LWC_REPOSITORY_ROOT": str(root),
    })
    producer = ["go", "run", "./cmd/pipeline_config", "prepare", "--target", "i18n",
                "--environment", target_environment, "--output", str(source_output)]
    if source_sha:
        producer.extend(("--source-sha", source_sha))
    run_stage(producer, cwd=root / "apps/bff", env=child_env, timeout=600, stage="i18n source assembly")
    revision = current_revision(source_output, source=True)
    revision_dir = source_output / "i18n/revisions" / revision
    translator = [
        "go", "run", "./cmd/pipeline_config", "translate-i18n",
        "--config", str(revision_dir / "pipeline.json"),
        "--input", str(revision_dir / "translation-input.json"),
        "--source-manifest", str(revision_dir / "source-manifest.json"),
        "--base", str(revision_dir / "base.json"),
        "--output", str(catalog_output),
        "--frontend-root", str(frontend_root),
        "--repository-root", str(root),
        "--cache", str(output / "cache"),
    ]
    if mode == "offline":
        translator.append("--offline")
    elif fixture:
        translator.extend(("--fixture", str(Path(fixture).resolve())))
    result = run_stage(translator, cwd=root / "apps/bff", env=child_env,
                       timeout=16 * 60 + 60, stage="i18n catalog translation")
    try:
        summary = json.loads([line for line in result.splitlines() if line.strip()][-1])
    except (IndexError, json.JSONDecodeError):
        raise I18nPrepareError("i18n catalog identity is invalid") from None
    bundle_revision = summary.get("bundle_revision")
    if summary.get("catalog_revision") != revision or not isinstance(bundle_revision, str) or \
            not CATALOG_SHA.fullmatch(bundle_revision):
        raise I18nPrepareError("i18n catalog identity does not match its source revision")
    if current_revision(catalog_output) != bundle_revision:
        raise I18nPrepareError("i18n catalog package is incomplete")
    index = frontend_root / "src/generated/i18n/catalog.ts"
    packaged_catalog = frontend_root / "src/generated/i18n/revisions" / bundle_revision / "catalog-data.json"
    public_manifest = frontend_root / "public/i18n/catalogs" / (bundle_revision + ".manifest.json")
    if index.is_symlink() or not index.is_file() or bundle_revision not in index.read_text(encoding="utf-8"):
        raise I18nPrepareError("i18n frontend module does not reference the prepared catalog")
    artifact = catalog_output / "revisions" / bundle_revision / "catalog-data.json"
    if packaged_catalog.is_symlink() or not packaged_catalog.is_file() or \
            packaged_catalog.read_bytes() != artifact.read_bytes():
        raise I18nPrepareError("i18n frontend catalog package is incomplete")
    if public_manifest.is_symlink() or not public_manifest.is_file():
        raise I18nPrepareError("i18n public manifest is unavailable")
    summary.update({"source_revision": source_sha or "git-head", "source_catalog_revision": revision})
    return summary


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--environment", choices=("local", "development", "production"), required=True)
    parser.add_argument("--source-sha", default="")
    parser.add_argument("--output", required=True)
    parser.add_argument("--frontend-root", default=str(ROOT / "apps/frontend"))
    parser.add_argument("--mode", choices=("auto", "offline"), default="auto")
    parser.add_argument("--fixture", default="")
    args = parser.parse_args()
    try:
        # --root is accepted so a caller can verify an explicit checkout; paths remain anchored to this file.
        if Path(args.root).resolve() != ROOT.resolve():
            raise I18nPrepareError("i18n root does not match this checkout")
        result = prepare(args.environment, args.output, args.frontend_root,
                         source_sha=args.source_sha, mode=args.mode, fixture=args.fixture)
    except I18nPrepareError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
