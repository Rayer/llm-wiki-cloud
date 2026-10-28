from __future__ import annotations

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
sys.path.insert(0, str(ROOT / "deploy/engine"))
sys.path.insert(0, str(ROOT / "scripts"))
sys.path.insert(0, str(ROOT / "deploy/components"))
import engine
import receipt_contract
from support import read, write
artifact_spec = importlib.util.spec_from_file_location(
    "gat4_artifact", ROOT / "scripts/gat4-local/artifact.py"
)
artifact = importlib.util.module_from_spec(artifact_spec)
assert artifact_spec.loader is not None
artifact_spec.loader.exec_module(artifact)
tree_digest = artifact.tree_digest


class IndependentProducerTests(unittest.TestCase):
    def synthetic_auth_inputs(self, root: Path, source_sha: str) -> Path:
        descriptor = root / "synthetic-auth-inputs.json"
        env = dict(os.environ)
        for key in tuple(env):
            if key.startswith(("GOOGLE_", "GCLOUD_", "CLOUDSDK_", "GCP_")):
                env.pop(key)
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
        source_root = root / "auth-source"
        source_root.mkdir()
        with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as bundle:
            bundle.extractall(source_root, filter="data")
        source = {
            "repository_id": "github:202",
            "revision": revision,
            "tree_sha256": tree_digest(source_root),
            "artifact_id": "9001",
            "run_id": 9002,
            "run_attempt": 1,
        }
        return source_root, source

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
build_id = "11111111-2222-3333-4444-555555555555"
project = os.environ.get("FAKE_GCP_PROJECT", "fixture-project")
if args[:2] == ["builds", "submit"]:
    print(json.dumps({{"id": build_id, "projectId": project, "location": "global",
        "name": f"projects/{{project}}/locations/global/builds/{{build_id}}", "status": "WORKING"}}))
elif args[:2] == ["builds", "describe"]:
    print(json.dumps({{"id": build_id, "projectId": project, "location": "global",
        "name": f"projects/{{project}}/locations/global/builds/{{build_id}}", "status": "SUCCESS"}}))
elif args[:4] == ["artifacts", "docker", "images", "describe"]:
    print("sha256:{"c" * 64}")
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
            binary, provider_log = self.fake_gcloud(root)
            metadata_path = directory / "dev-source-metadata.json"
            env = {
                "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                "TMPDIR": os.environ.get("TMPDIR", "/tmp"),
                "GAT4_PROVIDER_TRAP_LOG": str(provider_log),
                "AUTH_SOURCE_ROOT": str(source_root),
                "GITHUB_REPOSITORY_ID": "101",
                "GITHUB_RUN_ID": "1003",
                "GITHUB_RUN_ATTEMPT": "1",
                "GAT4_RELEASE_SOURCE_MODE": "independent",
                "GAT4_DEV_RECEIPT_MODE": "independent",
                "GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE": str(metadata_path),
                "FAKE_GCP_PROJECT": normalized["gcp"]["project_id"],
            }
            with patch.dict(os.environ, env, clear=False):
                os.environ.pop("GITHUB_ACTIONS", None)
                run = engine.Engine(directory)
                run.provider.gcp_project_number = lambda: "580854833715"
                run.prepare()

            auth_receipt = read(directory / "receipts/auth.json")
            dev_receipt = read(directory / "images/dev-receipt.json")
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
            build = next(call for call in calls if call["argv"][:2] == ["builds", "submit"])
            self.assertEqual(build["argv"][2], str(source_root))
            self.assertEqual(build["source_sha"], auth_source["revision"])
            self.assertEqual(build["source_ref"], "")
            self.assertEqual(build["auth_source_root"], str(source_root))
            self.assertIn("/llm-wiki-auth:" + auth_source["revision"],
                          next(value for value in build["argv"] if value.startswith("--substitutions=")))
            self.assertIn("_GIT_SHA=" + auth_source["revision"],
                          next(value for value in build["argv"] if value.startswith("--substitutions=")))
            self.assertTrue(all("auth-source" in call["auth_source_root"]
                                for call in calls if call["auth_source_root"]))

            newer_lwc_sha = lwc_sha
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
