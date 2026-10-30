from __future__ import annotations

import copy
import io
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[3]
GITHUB_RUNTIME_CREDENTIAL_KEYS = (
    "GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN", "ACTIONS_RUNTIME_URL",
    "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL",
)
GITHUB_TEST_ENV = {"GITHUB_ACTIONS": "false", **{key: "" for key in GITHUB_RUNTIME_CREDENTIAL_KEYS}}
sys.path.insert(0, str(ROOT / "deploy/engine"))
sys.path.insert(0, str(ROOT / "scripts"))
sys.path.insert(0, str(ROOT / "deploy/components"))
import engine
import receipt_contract
import gat4_ci_artifact
from support import read, write
artifact_spec = importlib.util.spec_from_file_location(
    "gat4_artifact", ROOT / "scripts/gat4-local/artifact.py"
)
artifact = importlib.util.module_from_spec(artifact_spec)
assert artifact_spec.loader is not None
artifact_spec.loader.exec_module(artifact)
tree_digest = artifact.tree_digest


class IndependentProducerTests(unittest.TestCase):
    def isolate_github_runtime(self, env: dict[str, str]) -> dict[str, str]:
        env.update(GITHUB_TEST_ENV)
        return env

    def clear_github_runtime(self, env: dict[str, str]) -> dict[str, str]:
        for key in tuple(env):
            if key.startswith(("GITHUB_", "ACTIONS_")) or key == "GH_TOKEN":
                env.pop(key)
        return self.isolate_github_runtime(env)

    def install_artifact_transport_trap(self, binary: Path, root: Path) -> Path:
        real_node = shutil.which("node")
        if not real_node:
            raise AssertionError("node runtime is required to trap Actions artifact transport")
        trap_log = root / "artifact-transport-trap.log"
        trap = binary / "node"
        trap.write_text(
            "#!/bin/sh\n"
            "case \"${1:-}\" in */deploy/engine/artifacts.cjs)\n"
            "  printf 'blocked artifact transport\\n' >> \"$GAT4_ARTIFACT_TRANSPORT_TRAP_LOG\"\n"
            "  exit 97\n"
            "  ;;\n"
            f"  *) exec {real_node!r} \"$@\" ;;\n"
            "esac\n"
        )
        trap.chmod(0o755)
        return trap_log

    def synthetic_auth_inputs(self, root: Path, source_sha: str) -> Path:
        descriptor = root / "synthetic-auth-inputs.json"
        env = dict(os.environ)
        for key in tuple(env):
            if key.startswith(("GOOGLE_", "GCLOUD_", "CLOUDSDK_", "GCP_")):
                env.pop(key)
        self.clear_github_runtime(env)
        env.update({
            "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
            "GCE_METADATA_HOST": "127.0.0.1:1",
            "GAT4_TEST_SOURCE_SHA": source_sha,
            "GAT4_TEST_AUTH_DESCRIPTOR_OUTPUT": str(descriptor),
            "GOWORK": "off",
            "GOFLAGS": "-mod=readonly",
            "GOTOOLCHAIN": "local",
        })
        subprocess.run(
            ["go", "test", "./cmd/pipeline_config", "-run",
             "^TestGAT4SyntheticAuthDescriptorProducer$", "-count=1"],
            cwd=ROOT / "apps/bff", env=env, check=True, capture_output=True, text=True,
            timeout=180,
        )
        if not descriptor.is_file():
            raise AssertionError("synthetic SDK did not produce an Auth input descriptor")
        return descriptor

    def admit_with_synthetic_descriptor(self, args, fixture_root: Path):
        descriptor = self.synthetic_auth_inputs(fixture_root, args.source)
        real_run = engine.run
        intercepted = []

        def run_with_synthetic_auth_descriptor(command, *run_args, **run_kwargs):
            parts = [str(part) for part in command]
            if (parts[:4] == ["go", "run", "./cmd/pipeline_config", "prepare"] and
                    "--descriptor" in parts and "--target" in parts and
                    parts[parts.index("--target") + 1] == "auth"):
                output = Path(parts[parts.index("--output") + 1])
                output.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(descriptor, output / "auth-inputs.json")
                intercepted.append(output)
                return ""
            return real_run(command, *run_args, **run_kwargs)

        env = dict(os.environ)
        for key in tuple(env):
            if key.startswith(("GOOGLE_", "GCLOUD_", "CLOUDSDK_", "GCP_")):
                env.pop(key)
        self.clear_github_runtime(env)
        env.update({
            "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
            "GCE_METADATA_HOST": "127.0.0.1:1",
            "GITHUB_REPOSITORY_ID": "101",
        })
        with patch.dict(os.environ, env, clear=True), patch.object(
                engine, "run", side_effect=run_with_synthetic_auth_descriptor):
            plan = engine.admit(args)
        if len(intercepted) != 1:
            raise AssertionError(f"expected one injected Auth descriptor, got {len(intercepted)}")
        return plan

    def make_auth_source(self, root: Path) -> tuple[Path, dict]:
        checkout = root / "auth-checkout"
        shutil.copytree(ROOT / "gateauth", checkout)
        schema = checkout / "contracts/gateauth-ci-build-v1.schema.json"
        schema.write_text("{}\n", encoding="utf-8")
        subprocess.run(["git", "init", "--quiet", str(checkout)], check=True)
        subprocess.run(["git", "-C", str(checkout), "add", "."], check=True)
        subprocess.run([
            "git", "-C", str(checkout), "-c", "user.name=Fixture", "-c",
            "user.email=fixture@example.invalid", "commit", "--quiet", "-m",
            "synthetic independent GateAuth source",
        ], check=True)
        revision = subprocess.check_output(
            ["git", "-C", str(checkout), "rev-parse", "HEAD"], text=True
        ).strip()
        archive = subprocess.check_output(
            ["git", "-C", str(checkout), "archive", "--format=tar", revision]
        )
        (root / "source.tar").write_bytes(archive)
        source_root = root / "auth-source"
        source_root.mkdir()
        with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as bundle:
            bundle.extractall(source_root, filter="data")
        source = {
            "repository_id": f"github:{gat4_ci_artifact.REPOSITORY_ID}",
            "revision": revision,
            "tree_sha256": tree_digest(source_root),
            "artifact_id": "9001",
            "run_id": 9002,
            "run_attempt": 1,
        }
        return source_root, source

    def make_ci_package(self, root: Path, auth_source: dict, normalized: dict):
        source_tar = (root / "source.tar").read_bytes()
        files = gat4_ci_artifact._tree_files(source_tar)
        image_prefix = normalized["gcp"]["artifact_registry"] + "/llm-wiki-auth"
        image_tag = f"{image_prefix}:{auth_source['revision']}"
        image_digest = "sha256:" + "c" * 64
        build_id = "11111111-2222-3333-4444-555555555555"
        build = {
            "id": build_id,
            "projectId": normalized["gcp"]["project_id"],
            "location": "global",
            "name": f"projects/{normalized['gcp']['project_id']}/locations/global/builds/{build_id}",
            "status": "SUCCESS",
            "substitutions": {
                "_IMAGE": image_tag,
                "_APP_VERSION": "1.0.0",
                "_GIT_SHA": auth_source["revision"],
                "_GIT_BRANCH": "main",
            },
            "results": {"images": [{"name": image_tag, "digest": image_digest}]},
        }
        build_path = root / "cloud-build.json"
        write(build_path, build)
        manifest = {
            "schema": "gateauth-ci-build-v1",
            "component": "auth",
            "product_version": "1.0.0",
            "source": {
                "repository_id": auth_source["repository_id"],
                "revision": auth_source["revision"],
                "tree_sha256": auth_source["tree_sha256"],
            },
            "ci": {"workflow_id": 100, "run_id": auth_source["run_id"],
                   "run_attempt": auth_source["run_attempt"]},
            "payload": {"source_archive_file": "source.tar",
                        "source_archive_sha256": gat4_ci_artifact.sha256_bytes(source_tar),
                        "source_archive_bytes": len(source_tar)},
            "build": {
                "project_id": normalized["gcp"]["project_id"],
                "project_number": "580854833715",
                "location": "global",
                "build_id": build_id,
                "status": "SUCCESS",
                "inputs_digest": gat4_ci_artifact._inputs_digest(
                    source_tar, files, auth_source["revision"], image_prefix, "1.0.0"),
            },
            "artifact": {"image": f"{image_prefix}@{image_digest}",
                         "image_digest": image_digest},
            "contracts": {"auth_config_schema_versions": [1, 2, 3],
                          "lwc_consumer_schema_version": 1},
        }
        zip_path = root / "gateauth-ci-package.zip"
        import zipfile
        with zipfile.ZipFile(zip_path, "w", compression=zipfile.ZIP_STORED) as package:
            package.writestr("source.tar", source_tar)
            package.writestr("build-manifest.json", json.dumps(
                manifest, sort_keys=True, separators=(",", ":")).encode())
        zip_bytes = zip_path.read_bytes()
        envelope = {
            "repository_id": gat4_ci_artifact.REPOSITORY_ID,
            "run_id": auth_source["run_id"],
            "run_attempt": auth_source["run_attempt"],
            "artifact_id": int(auth_source["artifact_id"]),
            "artifact_sha256": gat4_ci_artifact.sha256_bytes(zip_bytes),
            "artifact_bytes": len(zip_bytes),
            "workflow_id": 100,
        }
        envelope_path = root / "gateauth-ci-envelope.json"
        write(envelope_path, envelope)
        return zip_path, envelope_path, build_path, image_digest

    def make_lwc_successor(self, root: Path, parent_sha: str) -> tuple[str, dict[str, str]]:
        object_directory = root / "lwc-test-objects"
        object_directory.mkdir()
        common_dir = Path(subprocess.check_output(
            ["git", "rev-parse", "--git-common-dir"], cwd=ROOT, text=True
        ).strip())
        if not common_dir.is_absolute():
            common_dir = ROOT / common_dir
        git_env = {
            "GIT_OBJECT_DIRECTORY": str(object_directory),
            "GIT_ALTERNATE_OBJECT_DIRECTORIES": str((common_dir / "objects").resolve()),
            "GIT_AUTHOR_NAME": "GAT4 Test",
            "GIT_AUTHOR_EMAIL": "gat4-test@example.invalid",
            "GIT_COMMITTER_NAME": "GAT4 Test",
            "GIT_COMMITTER_EMAIL": "gat4-test@example.invalid",
        }
        env = dict(os.environ, **git_env)
        tree_sha = subprocess.check_output(
            ["git", "rev-parse", f"{parent_sha}^{{tree}}"], cwd=ROOT, env=env, text=True
        ).strip()
        successor_sha = subprocess.check_output(
            ["git", "commit-tree", tree_sha, "-p", parent_sha], cwd=ROOT, env=env,
            input="same-tree LWC successor for source identity tests\n", text=True,
        ).strip()
        self.assertEqual(subprocess.check_output(
            ["git", "rev-parse", f"{successor_sha}^{{tree}}"], cwd=ROOT, env=env, text=True
        ).strip(), tree_sha)
        self.assertEqual(subprocess.check_output(
            ["git", "rev-parse", f"{successor_sha}^"], cwd=ROOT, env=env, text=True
        ).strip(), parent_sha)
        return successor_sha, git_env

    def admit_plan(self, directory: Path, *, name: str, source_sha: str,
                   components: list[str], auth_source: dict | None = None,
                   reuse: Path | None = None):
        args = type("Args", (), {
            "environment": "development",
            "source": source_sha,
            "executor_sha": subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
            ).strip(),
            "components": ",".join(components),
            "tag": "gat4-local-synthetic-" + name,
            "dev_reference": None,
            "reuse": str(reuse) if reuse else None,
            "dev": None,
            "release_source_mode": "independent",
            "auth_repository_id": auth_source["repository_id"].split(":", 1)[1]
                if auth_source else None,
            "auth_revision": auth_source["revision"] if auth_source else None,
            "auth_tree_sha256": auth_source["tree_sha256"] if auth_source else None,
            "auth_artifact_id": auth_source["artifact_id"] if auth_source else None,
            "auth_run_id": str(auth_source["run_id"]) if auth_source else None,
            "auth_run_attempt": str(auth_source["run_attempt"]) if auth_source else None,
        })()
        plan = self.admit_with_synthetic_descriptor(args, directory.parent)
        directory.mkdir(parents=True, exist_ok=True)
        write(directory / "plan.json", plan)
        return plan

    def fake_gcloud(self, directory: Path):
        binary = directory / "bin"
        binary.mkdir()
        log = directory / "provider-trap.jsonl"
        gcloud = binary / "gcloud"
        gcloud.write_text(f'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
with Path(os.environ["GAT4_PROVIDER_TRAP_LOG"]).open("a") as stream:
    stream.write(json.dumps({{"argv": args, "source_sha": os.environ.get("SOURCE_SHA"),
        "source_ref": os.environ.get("SOURCE_REF"),
        "auth_source_root": os.environ.get("AUTH_SOURCE_ROOT")}}) + "\\n")
if args[:2] == ["builds", "submit"]:
    print("canonical Auth build must not be submitted for an independent CI image", file=sys.stderr)
    raise SystemExit(97)
elif args[:2] == ["builds", "describe"]:
    print(Path(os.environ["GAT4_PROVIDER_BUILD_JSON"]).read_text())
elif args[:4] == ["artifacts", "docker", "images", "describe"]:
    print(os.environ["GAT4_IMAGE_DIGEST"])
else:
    print("unexpected provider command", file=sys.stderr)
    raise SystemExit(98)
''')
        gcloud.chmod(0o755)
        return binary, log

    def test_real_auth_provider_and_dev_receipt_producer_use_two_sources(self):
        lwc_sha = subprocess.check_output(["git", "rev-parse", "HEAD"],
                                          cwd=ROOT, text=True).strip()
        with tempfile.TemporaryDirectory(prefix="gat4-independent-producer-") as raw:
            root = Path(raw)
            source_root, auth_source = self.make_auth_source(root)
            directory = root / "auth-release"
            plan = self.admit_plan(directory, name="auth", source_sha=lwc_sha,
                                   components=["auth"], auth_source=auth_source)
            normalized = plan["normalized"]
            package_path, envelope_path, build_path, image_digest = self.make_ci_package(
                root, auth_source, normalized)
            binary, provider_log = self.fake_gcloud(root)
            artifact_trap_log = self.install_artifact_transport_trap(binary, root)
            metadata_path = directory / "dev-source-metadata.json"
            env = {
                "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                "TMPDIR": os.environ.get("TMPDIR", "/tmp"),
                "GAT4_PROVIDER_TRAP_LOG": str(provider_log),
                "GAT4_ARTIFACT_TRANSPORT_TRAP_LOG": str(artifact_trap_log),
                "GATEAUTH_CI_PACKAGE_PATH": str(package_path),
                "GATEAUTH_CI_ENVELOPE_PATH": str(envelope_path),
                "GAT4_PROVIDER_BUILD_JSON": str(build_path),
                "GAT4_IMAGE_DIGEST": image_digest,
                "GITHUB_REPOSITORY_ID": "101",
                "GITHUB_RUN_ID": "1003",
                "GITHUB_RUN_ATTEMPT": "1",
                "GAT4_RELEASE_SOURCE_MODE": "independent",
                "GAT4_DEV_RECEIPT_MODE": "independent",
                "GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE": str(metadata_path),
                "FAKE_GCP_PROJECT": normalized["gcp"]["project_id"],
            }
            env.update(GITHUB_TEST_ENV)
            with patch.dict(os.environ, env, clear=False):
                run = engine.Engine(directory)
                run.provider.gcp_project_number = lambda: "580854833715"
                auth_submit_calls = []

                def reject_canonical_auth_submit(component):
                    auth_submit_calls.append(component)
                    raise AssertionError("independent Auth prepare reached canonical submit_build")

                run.provider.submit_build = reject_canonical_auth_submit
                run.prepare()
                self.assertEqual(auth_submit_calls, [])

            auth_receipt = read(directory / "receipts/auth.json")
            dev_receipt = read(directory / "images/dev-receipt.json")
            run.provider.usable = lambda _component, _artifact: None
            self.assertEqual(auth_receipt["schema"], 3)
            self.assertEqual(auth_receipt["build_sha"], lwc_sha)
            self.assertEqual(auth_receipt["auth_source"], {
                "repository_id": auth_source["repository_id"],
                "revision": auth_source["revision"],
                "tree_sha256": auth_source["tree_sha256"],
                "artifact_id": auth_source["artifact_id"],
                "build_run_id": auth_source["run_id"],
                "build_run_attempt": auth_source["run_attempt"],
            })
            self.assertEqual(run.barrier()["auth"], auth_receipt["artifact"])
            auth_path = directory / "receipts/auth.json"
            dev_path = directory / "images/dev-receipt.json"
            auth_original = read(auth_path)
            dev_original = read(dev_path)
            malformed_schema = copy.deepcopy(auth_original)
            malformed_schema["schema"] = 2
            malformed_schema.pop("auth_source")
            write(auth_path, malformed_schema)
            with self.assertRaises(engine.Breakpoint) as schema_failure:
                run.barrier()
            self.assertEqual(schema_failure.exception.reason, "artifact-auth-receipt-incompatible")
            malformed_revision = copy.deepcopy(auth_original)
            malformed_revision["auth_source"]["revision"] = "f" * 40
            write(auth_path, malformed_revision)
            with self.assertRaises(engine.Breakpoint) as revision_failure:
                run.barrier()
            self.assertEqual(revision_failure.exception.reason, "artifact-auth-receipt-incompatible")
            write(auth_path, auth_original)
            dev_path.unlink()
            with self.assertRaises(engine.Breakpoint) as missing_pair:
                run.barrier()
            self.assertEqual(missing_pair.exception.reason, "artifact-auth-dev-pair-missing")
            malformed_dev = copy.deepcopy(dev_original)
            malformed_dev["source"]["auth"]["revision"] = "f" * 40
            write(dev_path, malformed_dev)
            with self.assertRaises(engine.Breakpoint) as mixed_pair:
                run.barrier()
            self.assertEqual(mixed_pair.exception.reason, "artifact-auth-dev-pair-incompatible")
            write(dev_path, dev_original)
            self.assertEqual(run.barrier()["auth"], auth_original["artifact"])
            self.assertEqual(dev_receipt["schema"], "lwc-306-dev-image-receipt-v2")
            self.assertNotEqual(dev_receipt["source"]["lwc"]["sha"],
                                dev_receipt["source"]["auth"]["revision"])
            self.assertEqual(dev_receipt["source"]["auth"]["artifact_digest"],
                             auth_receipt["artifact"]["image"].rsplit("@", 1)[1])
            receipt_contract.validate_receipt_pair(
                auth_receipt, dev_receipt, "independent",
                expected_auth_config_inputs=auth_receipt["auth_config_inputs"],
                expected_lwc_source=dev_receipt["source"]["lwc"],
                expected_auth_source=dev_receipt["source"]["auth"],
                expected_components=["auth"],
            )
            calls = [json.loads(line) for line in provider_log.read_text().splitlines()]
            self.assertFalse(any(call["argv"][:2] == ["builds", "submit"] for call in calls),
                             "canonical consumer rebuilt the independent Auth image")
            self.assertEqual(sum(call["argv"][:2] == ["builds", "describe"] for call in calls), 1)
            self.assertTrue(any(call["argv"][:4] == ["artifacts", "docker", "images", "describe"]
                                for call in calls), "canonical consumer skipped registry digest readback")

            newer_lwc_sha, test_git_env = self.make_lwc_successor(root, lwc_sha)
            object_env_patch = patch.dict(os.environ, test_git_env, clear=False)
            object_env_patch.start()
            self.addCleanup(object_env_patch.stop)
            self.assertNotEqual(newer_lwc_sha, lwc_sha)
            selected_auth_release = root / "selected-auth-release"
            selected_auth_plan = self.admit_plan(
                selected_auth_release, name="auth-reuse", source_sha=newer_lwc_sha,
                components=["auth"], auth_source=auth_source, reuse=directory)
            selected_auth_engine = engine.Engine(selected_auth_release)
            selected_auth_engine.provider.gcp_project_number = lambda: "580854833715"
            auth_build_calls = []

            def reject_auth_rebuild():
                auth_build_calls.append("auth")
                raise AssertionError("selected Auth reuse unexpectedly started a new Auth build")

            selected_auth_engine.provider.prepare_independent_auth = reject_auth_rebuild
            provider_calls_before_reuse = [json.loads(line) for line in provider_log.read_text().splitlines()]
            with patch.dict(os.environ, {
                "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                "TMPDIR": os.environ.get("TMPDIR", "/tmp"),
                "GAT4_PROVIDER_TRAP_LOG": str(provider_log),
                "GAT4_ARTIFACT_TRANSPORT_TRAP_LOG": str(artifact_trap_log),
                "GAT4_IMAGE_DIGEST": image_digest,
                **GITHUB_TEST_ENV,
                "GITHUB_REPOSITORY_ID": "101",
                "GITHUB_RUN_ID": "1005",
                "GITHUB_RUN_ATTEMPT": "1",
                "GAT4_RELEASE_SOURCE_MODE": "independent",
                "GAT4_DEV_RECEIPT_MODE": "independent",
                "GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE": str(
                    selected_auth_release / "dev-source-metadata.json"),
                "FAKE_GCP_PROJECT": normalized["gcp"]["project_id"],
            }, clear=False):
                selected_auth_engine.prepare(reuse=directory)
                self.assertEqual(selected_auth_engine.barrier()["auth"], auth_receipt["artifact"])
            self.assertEqual(selected_auth_plan["selected"], ["auth"])
            self.assertEqual(auth_build_calls, [])
            selected_auth_receipt = read(selected_auth_release / "receipts/auth.json")
            self.assertEqual(selected_auth_receipt["build_sha"], newer_lwc_sha)
            self.assertEqual(selected_auth_receipt["auth_config_inputs"]["source_sha"], newer_lwc_sha)
            self.assertEqual(selected_auth_receipt["auth_source"], auth_receipt["auth_source"])
            self.assertEqual(selected_auth_receipt["build"], auth_receipt["build"])
            self.assertEqual(selected_auth_receipt["artifact"], auth_receipt["artifact"])
            selected_auth_dev = read(selected_auth_release / "images/dev-receipt.json")
            receipt_contract.validate_receipt_pair(
                selected_auth_receipt, selected_auth_dev, "independent",
                expected_auth_config_inputs=selected_auth_receipt["auth_config_inputs"],
                expected_lwc_source=selected_auth_dev["source"]["lwc"],
                expected_auth_source=selected_auth_dev["source"]["auth"],
                expected_components=["auth"],
            )
            provider_calls_after_reuse = [json.loads(line) for line in provider_log.read_text().splitlines()]
            self.assertEqual(sum(call["argv"][:2] == ["builds", "describe"]
                                 for call in provider_calls_after_reuse),
                             sum(call["argv"][:2] == ["builds", "describe"]
                                 for call in provider_calls_before_reuse))

            bff_release = root / "bff-release"
            bff_plan = self.admit_plan(bff_release, name="bff", source_sha=newer_lwc_sha,
                                       components=["bff"], reuse=directory)
            build_calls = []
            bff_engine = engine.Engine(bff_release)

            def prepare_bff_without_auth(component):
                build_calls.append(component)
                bff_engine.state["builds"][component] = {
                    "project_id": normalized["gcp"]["project_id"], "location": "global",
                    "build_id": "synthetic-bff-provider-trap", "identity_verified": True,
                    "status": "SUCCESS", "last_observed_status": "SUCCESS",
                    "poll_outcome": "succeeded",
                }
                return {"image": normalized["gcp"]["artifact_registry"] +
                        "/llm-wiki-bff@sha256:" + "e" * 64}
            bff_engine.prepare_container = prepare_bff_without_auth
            bff_engine.provider.usable = lambda _component, _artifact: None
            with patch.dict(os.environ, {
                "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                "GAT4_ARTIFACT_TRANSPORT_TRAP_LOG": str(artifact_trap_log),
                "GAT4_IMAGE_DIGEST": image_digest,
                **GITHUB_TEST_ENV,
                "GITHUB_REPOSITORY_ID": "101",
                "GITHUB_RUN_ID": "1004",
                "GITHUB_RUN_ATTEMPT": "1",
                "GAT4_RELEASE_SOURCE_MODE": "independent",
                "GAT4_DEV_RECEIPT_MODE": "independent",
                "GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE": str(bff_release / "dev-source-metadata.json"),
            }, clear=False):
                bff_engine.prepare(reuse=directory)
            self.assertEqual(bff_plan["selected"], ["bff"])
            self.assertEqual(build_calls, ["bff"])
            bff_auth_receipt = read(bff_release / "receipts/auth.json")
            self.assertEqual(bff_auth_receipt["build_sha"], newer_lwc_sha)
            self.assertEqual(bff_auth_receipt["artifact"]["image"], auth_receipt["artifact"]["image"])
            bff_dev_receipt = read(bff_release / "images/dev-receipt.json")
            receipt_contract.validate_receipt_pair(
                bff_auth_receipt, bff_dev_receipt, "independent",
                expected_auth_config_inputs=bff_auth_receipt["auth_config_inputs"],
                expected_lwc_source=bff_dev_receipt["source"]["lwc"],
                expected_auth_source=bff_dev_receipt["source"]["auth"],
                expected_components=["bff"],
            )
            self.assertEqual(bff_auth_receipt["auth_source"]["revision"],
                             auth_receipt["auth_source"]["revision"])
            self.assertNotEqual(bff_dev_receipt["source"]["lwc"]["sha"],
                                dev_receipt["source"]["lwc"]["sha"])
            self.assertFalse(artifact_trap_log.exists(),
                             "synthetic source reuse attempted Actions artifact transport")

    def test_admission_fails_closed_for_missing_or_mixed_auth_tuple(self):
        source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        base = {
            "environment": "development", "source": source, "executor_sha": source,
            "tag": "gat4-local-source-negative", "components": "auth", "dev_reference": None,
            "reuse": None, "dev": None, "release_source_mode": "independent",
        }
        with tempfile.TemporaryDirectory(prefix="gat4-independent-admission-negative-") as raw:
            fixture_root = Path(raw)
            with self.assertRaises(engine.Breakpoint) as missing:
                self.admit_with_synthetic_descriptor(type("Args", (), base)(), fixture_root)
        self.assertEqual(missing.exception.reason, "independent-auth-source-required")
        mixed = {
            **base,
            "auth_repository_id": "202",
            "auth_revision": source,
            "auth_tree_sha256": "sha256:" + "e" * 64,
            "auth_artifact_id": "9001",
            "auth_run_id": "9002",
            "auth_run_attempt": "1",
        }
        with tempfile.TemporaryDirectory(prefix="gat4-independent-admission-mixed-") as raw:
            fixture_root = Path(raw)
            with self.assertRaises(engine.Breakpoint) as wrong_source:
                self.admit_with_synthetic_descriptor(type("Args", (), mixed)(), fixture_root)
        self.assertEqual(wrong_source.exception.reason, "independent-auth-source-invalid")


if __name__ == "__main__":
    unittest.main()
