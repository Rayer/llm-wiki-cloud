from __future__ import annotations

import copy
import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
PROFILE_TMPDIR = Path(tempfile.gettempdir())
HELPER_PATH = ROOT / "scripts/gat4-local/artifact.py"
spec = importlib.util.spec_from_file_location("gat4_artifact", HELPER_PATH)
artifact = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(artifact)


def read_fixture(name: str) -> dict:
    wrapper = json.loads((ROOT / "gateauth/contracts/gateauth-artifact/v1" / name).read_text())
    assert wrapper["_fixture"]["status"] == "NOT PUBLISHED"
    return wrapper["receipt"]


class DevReceiptWriterTests(unittest.TestCase):
    registry = "us-central1-docker.pkg.dev/fixture-project/llm-wiki-cloud"
    auth_digest = "sha256:" + "c" * 64

    @classmethod
    def setUpClass(cls):
        PROFILE_TMPDIR.mkdir(parents=True, exist_ok=True)

    def run_writer(self, mode=None, metadata=None, metadata_file_set=True, image=None, selector=None, selected_components=None):
        legacy = mode in ("legacy", "legacy-explicit")
        source_sha = "a" * 40 if legacy else "b" * 40
        selected_components = selected_components or ["auth"]
        expected_auth = read_fixture(
            "legacy-auth-receipt.fixture.json" if legacy else "independent-auth-receipt.fixture.json"
        )
        image = image or f"{self.registry}/llm-wiki-auth@{self.auth_digest}"
        expected_auth["artifact"]["image"] = image
        if not legacy:
            if metadata is None:
                metadata = {
                    "lwc": {
                        "repository_id": "github:101",
                        "sha": source_sha,
                        "run_id": 1003,
                        "run_attempt": 1,
                    },
                    "auth": {
                        "repository_id": "github:202",
                        "revision": "c" * 40,
                        "tree_sha256": "sha256:" + "e" * 64,
                        "run_id": 1002,
                        "run_attempt": 1,
                        "artifact_id": "synthetic-fixture-artifact",
                        "artifact_digest": self.auth_digest,
                    },
                }
            metadata = copy.deepcopy(metadata)

        with tempfile.TemporaryDirectory(prefix="gat4-dev-receipt-", dir=PROFILE_TMPDIR) as raw:
            temp = Path(raw)
            artifact_dir = temp / "artifacts"
            image_dir = artifact_dir / "images"
            image_dir.mkdir(parents=True)
            plan_path = temp / "plan.json"
            plan_path.write_text(json.dumps({
                "normalized": {
                    "selected_components": selected_components,
                    "gcp": {"artifact_registry": self.registry},
                    "evidence": {"config_fingerprint": "sha256:" + "d" * 64},
                }
            }))
            component_images = {
                "auth": image,
                "bff": f"{self.registry}/llm-wiki-bff@sha256:" + "f" * 64,
            }
            for component in selected_components:
                (image_dir / f"{component}-image-{source_sha}.txt").write_text(component_images[component] + "\n")
            metadata_path = temp / "source-metadata.json"
            if metadata is not None and metadata_file_set:
                metadata_path.write_text(json.dumps(metadata))

            trap_bin = temp / "trap-bin"
            trap_bin.mkdir()
            trap_log = temp / "provider-cli-reached.log"
            for command in ("gh", "gcloud", "terraform", "vercel", "docker", "podman", "kubectl", "firebase", "aws"):
                wrapper = trap_bin / command
                wrapper.write_text(
                    "#!/bin/sh\nprintf 'blocked\\n' >> \"$GAT4_PROVIDER_TRAP_LOG\"\nexit 97\n"
                )
                wrapper.chmod(0o755)

            env = os.environ.copy()
            for key in ("GAT4_DEV_RECEIPT_MODE", "GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE"):
                env.pop(key, None)
            env.update({
                "ROOT": str(ROOT),
                "ENVIRONMENT": "development",
                "SOURCE_SHA": source_sha,
                "PLAN_PATH": str(plan_path),
                "ARTIFACT_DIR": str(artifact_dir),
                "GITHUB_RUN_ID": "1003" if not legacy else "1001",
                "GITHUB_RUN_ATTEMPT": "1",
                "PATH": str(trap_bin) + os.pathsep + env["PATH"],
                "GAT4_PROVIDER_TRAP_LOG": str(trap_log),
                "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
                "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
                "GCE_METADATA_HOST": "127.0.0.1:1",
                "GCE_METADATA_IP": "127.0.0.1",
                "NO_PROXY": "127.0.0.1,localhost",
                "TMPDIR": str(PROFILE_TMPDIR),
                "PYTHONDONTWRITEBYTECODE": "1",
            })
            if metadata_file_set and metadata is not None:
                env["GAT4_DEV_RECEIPT_SOURCE_METADATA_FILE"] = str(metadata_path)
            if mode == "independent":
                env["GAT4_DEV_RECEIPT_MODE"] = "independent"
            elif mode == "legacy-explicit":
                env["GAT4_DEV_RECEIPT_MODE"] = "legacy_monorepo"
            if selector is not None:
                env["GAT4_DEV_RECEIPT_MODE"] = selector

            completed = subprocess.run(
                ["bash", "-c", 'source "$ROOT/deploy/components/common.sh"; record_dev_receipt'],
                env=env,
                text=True,
                capture_output=True,
            )
            receipt_path = image_dir / "dev-receipt.json"
            receipt = json.loads(receipt_path.read_text()) if receipt_path.exists() else None
            provider_call = trap_log.read_text() if trap_log.exists() else ""
            self.assertEqual(provider_call, "", "a cloud/provider CLI was reached")
            return completed, receipt, expected_auth

    def test_legacy_v1_writer_output_is_unchanged_and_reader_accepts_it(self):
        expected = {
            "schema": "lwc-306-dev-image-receipt-v1",
            "source": {
                "sha": "a" * 40,
                "ref": "develop",
                "workflow_path": ".github/workflows/deploy-dev.yml",
                "event": "workflow_dispatch",
                "run_id": 1001,
                "run_attempt": 1,
            },
            "config": {
                "environment": "development",
                "path": "deploy/environments/development.yaml",
                "fingerprint": "sha256:" + "d" * 64,
            },
            "components": ["auth"],
            "images": {"auth": f"{self.registry}/llm-wiki-auth@{self.auth_digest}"},
        }
        for mode in ("legacy", "legacy-explicit"):
            with self.subTest(mode=mode):
                completed, receipt, auth = self.run_writer(mode=mode)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                self.assertEqual(receipt, expected)
                self.assertTrue(artifact.validate_receipt_pair(
                    auth, receipt, "legacy", expected_auth_config_inputs=auth["auth_config_inputs"]
                ))

    def test_independent_v2_writer_output_pairs_with_existing_reader(self):
        completed, receipt, auth = self.run_writer(mode="independent")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(receipt["schema"], "lwc-306-dev-image-receipt-v2")
        self.assertEqual(receipt["source"]["lwc"], {
            "repository_id": "github:101", "sha": "b" * 40, "run_id": 1003, "run_attempt": 1
        })
        self.assertEqual(receipt["config"]["fingerprint"], "sha256:" + "d" * 64)
        self.assertEqual(receipt["components"], ["auth"])
        self.assertEqual(receipt["images"]["auth"], auth["artifact"]["image"])
        self.assertTrue(artifact.validate_receipt_pair(auth, receipt, "independent"))

    def test_metadata_alone_selects_independent_mode(self):
        completed, receipt, auth = self.run_writer(mode="independent-auto")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(receipt["schema"], "lwc-306-dev-image-receipt-v2")
        self.assertTrue(artifact.validate_receipt_pair(auth, receipt, "independent"))

    def test_independent_pairs_auth_image_without_requiring_auth_selected(self):
        completed, receipt, auth = self.run_writer(mode="independent", selected_components=["bff"])
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(receipt["components"], ["bff"])
        self.assertEqual(receipt["images"]["auth"], auth["artifact"]["image"])
        self.assertIn("bff", receipt["images"])
        self.assertTrue(artifact.validate_receipt_pair(auth, receipt, "independent"))

    def test_independent_rejects_missing_metadata_for_explicit_mode(self):
        completed, receipt, _ = self.run_writer(mode="independent", metadata=None, metadata_file_set=False)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("source metadata file is required", completed.stderr)
        self.assertIsNone(receipt)

    def test_independent_rejects_incomplete_and_mixed_sources(self):
        cases = []
        incomplete = {
            "lwc": {"repository_id": "github:101", "sha": "b" * 40, "run_id": 1003, "run_attempt": 1},
            "auth": {"repository_id": "github:202", "revision": "c" * 40, "tree_sha256": "sha256:" + "e" * 64,
                     "run_id": 1002, "run_attempt": 1, "artifact_digest": self.auth_digest},
        }
        cases.append(("missing artifact ID", incomplete))
        same_repo = self.valid_metadata()
        same_repo["auth"]["repository_id"] = same_repo["lwc"]["repository_id"]
        cases.append(("same repository", same_repo))
        mixed_revision = self.valid_metadata()
        mixed_revision["auth"]["revision"] = mixed_revision["lwc"]["sha"]
        cases.append(("mixed revision", mixed_revision))
        mixed_sha = self.valid_metadata()
        mixed_sha["lwc"]["sha"] = "f" * 40
        cases.append(("wrong LWC SHA", mixed_sha))
        mixed_run = self.valid_metadata()
        mixed_run["lwc"]["run_id"] = 1004
        cases.append(("wrong LWC run", mixed_run))
        for label, metadata in cases:
            with self.subTest(label=label):
                completed, receipt, _ = self.run_writer(mode="independent", metadata=metadata)
                self.assertNotEqual(completed.returncode, 0)
                self.assertIsNone(receipt)

    def test_independent_rejects_image_digest_mixing_and_malformed_digest(self):
        wrong_artifact = self.valid_metadata()
        wrong_artifact["auth"]["artifact_digest"] = "sha256:" + "9" * 64
        completed, receipt, _ = self.run_writer(mode="independent", metadata=wrong_artifact)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("artifact digest does not match", completed.stderr)
        self.assertIsNone(receipt)

        malformed_artifact = self.valid_metadata()
        malformed_artifact["auth"]["artifact_digest"] = "sha256:short"
        completed, receipt, _ = self.run_writer(mode="independent", metadata=malformed_artifact)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIsNone(receipt)

        completed, receipt, _ = self.run_writer(
            mode="independent", image=f"{self.registry}/llm-wiki-auth@sha256:short"
        )
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("exact configured repository", completed.stderr)
        self.assertIsNone(receipt)

    def test_independent_rejects_local_snapshot_and_legacy_schema_substitution(self):
        local = self.valid_metadata()
        local["kind"] = "local_snapshot"
        completed, receipt, _ = self.run_writer(mode="independent", metadata=local)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIsNone(receipt)

        legacy_schema = self.valid_metadata()
        legacy_schema["schema"] = "lwc-306-dev-image-receipt-v1"
        completed, receipt, _ = self.run_writer(mode="independent", metadata=legacy_schema)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIsNone(receipt)

    def test_explicit_legacy_mode_rejects_independent_metadata(self):
        completed, receipt, _ = self.run_writer(
            mode="legacy-explicit", metadata=self.valid_metadata()
        )
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("cannot use independent source metadata", completed.stderr)
        self.assertIsNone(receipt)

    def test_unknown_mode_is_rejected(self):
        completed, receipt, _ = self.run_writer(
            mode="independent-auto", selector="unexpected"
        )
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("DEV receipt mode is invalid", completed.stderr)
        self.assertIsNone(receipt)

    @classmethod
    def valid_metadata(cls):
        return {
            "lwc": {
                "repository_id": "github:101", "sha": "b" * 40, "run_id": 1003, "run_attempt": 1
            },
            "auth": {
                "repository_id": "github:202", "revision": "c" * 40, "tree_sha256": "sha256:" + "e" * 64,
                "run_id": 1002, "run_attempt": 1, "artifact_id": "synthetic-fixture-artifact",
                "artifact_digest": "sha256:" + "c" * 64,
            },
        }


if __name__ == "__main__":
    unittest.main()
