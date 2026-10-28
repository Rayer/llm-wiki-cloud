from __future__ import annotations

import copy
import importlib.util
import io
import json
import os
import shutil
import tarfile
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
HELPER_PATH = ROOT / "scripts/gat4-local/artifact.py"
spec = importlib.util.spec_from_file_location("gat4_artifact", HELPER_PATH)
artifact = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(artifact)


def read_fixture(name):
    path = ROOT / "gateauth/contracts/gateauth-artifact/v1" / name
    wrapper = json.loads(path.read_text(encoding="utf-8"))
    assert wrapper["_fixture"]["status"] == "NOT PUBLISHED"
    return wrapper["receipt"]


class ArtifactTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        tmpdir = Path(os.environ["TMPDIR"])
        cls.out = tmpdir / "gat4-local/out"
        cls.manifest_path = cls.out / "gateauth-artifact-v1.json"
        cls.source_root = ROOT / "gateauth"
        cls.config_input = cls.source_root / "contracts/gateauth-artifact/v1/local-auth-config.test.json"
        if not cls.manifest_path.is_file():
            raise RuntimeError("run scripts/gat4-local/build-auth.sh before Python contract tests")
        cls.original_manifest = cls.manifest_path.read_bytes()

    def validate(self, manifest_path=None, artifacts_dir=None, source_root=None, config_input=None, mode="local"):
        return artifact.validate_artifact(
            manifest_path or self.manifest_path,
            artifacts_dir or self.out,
            source_root or self.source_root,
            config_input or self.config_input,
            mode,
        )

    def with_manifest(self, change, run):
        original = self.manifest_path.read_bytes()
        manifest = json.loads(original)
        change(manifest)
        self.manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        try:
            run()
        finally:
            self.manifest_path.write_bytes(original)

    def test_local_artifact_success_and_extraction(self):
        manifest = self.validate()
        self.assertEqual(manifest["schema"], "lwc-gateauth-artifact-v1")
        self.assertEqual(manifest["source"]["kind"], "local_snapshot")
        self.assertEqual(manifest["artifact"]["kind"], "oci_image_archive")
        self.assertEqual(manifest["artifact"]["digest_kind"], "oci_image_manifest")
        self.assertNotEqual(manifest["artifact"]["digest"], manifest["artifact"]["archive_sha256"])

    def test_manifest_schema_rejects_unknown_fields(self):
        def run():
            with self.assertRaisesRegex(artifact.ContractError, "unknown keys"):
                self.validate()

        self.with_manifest(lambda m: m.update({"published": True}), run)

    def test_tampered_native_binary_is_rejected(self):
        path = self.out / "auth"
        original = path.read_bytes()
        path.write_bytes(original + b"tampered")
        try:
            with self.assertRaisesRegex(artifact.ContractError, "output digest mismatch"):
                self.validate()
        finally:
            path.write_bytes(original)

    def test_tampered_archive_is_rejected(self):
        path = self.out / artifact.ARCHIVE_NAME
        original = path.read_bytes()
        path.write_bytes(original + b"tampered")
        try:
            with self.assertRaisesRegex(artifact.ContractError, "archive SHA-256"):
                self.validate()
        finally:
            path.write_bytes(original)

    def test_tampered_image_manifest_is_rejected_after_archive_rehash(self):
        path = self.out / artifact.ARCHIVE_NAME
        original_archive = path.read_bytes()
        original_manifest = self.manifest_path.read_bytes()
        files = artifact._tar_files(original_archive, "OCI archive")
        index = json.loads(files["index.json"])
        descriptor = index["manifests"][0]
        blob_path = "blobs/sha256/" + descriptor["digest"].removeprefix("sha256:")
        files[blob_path] += b" "
        rebuilt = io.BytesIO()
        with tarfile.open(fileobj=rebuilt, mode="w", format=tarfile.USTAR_FORMAT) as tar:
            for name, data in sorted(files.items()):
                artifact._tar_add(tar, name, data, 0o644)
        path.write_bytes(rebuilt.getvalue())
        manifest = json.loads(original_manifest)
        manifest["artifact"]["archive_sha256"] = artifact.sha256_file(path)
        self.manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        try:
            with self.assertRaisesRegex(artifact.ContractError, "blob digest or size mismatch"):
                self.validate()
        finally:
            path.write_bytes(original_archive)
            self.manifest_path.write_bytes(original_manifest)

    def test_tampered_config_input_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            config = Path(temp) / "local-config.json"
            config.write_bytes(self.config_input.read_bytes() + b" ")
            with self.assertRaisesRegex(artifact.ContractError, "config input digest"):
                self.validate(config_input=config)

    def test_tampered_source_tree_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            tree = Path(temp) / "gateauth"
            shutil.copytree(self.source_root, tree)
            target = tree / "VERSION"
            target.write_text(target.read_text(encoding="utf-8") + "tampered", encoding="utf-8")
            with self.assertRaisesRegex(artifact.ContractError, "source tree digest"):
                self.validate(source_root=tree)

    def test_wrong_source_identity_is_rejected(self):
        def run():
            with self.assertRaisesRegex(artifact.ContractError, "repository_id"):
                self.validate()

        self.with_manifest(lambda m: m["source"].update({"repository_id": "owner/gateauth"}), run)

    def test_short_or_fake_sha_is_rejected(self):
        def run():
            with self.assertRaisesRegex(artifact.ContractError, "required pattern"):
                self.validate()

        self.with_manifest(lambda m: m["source"].update({"revision": "sha256:abc"}), run)

    def test_config_digest_mismatch_is_rejected(self):
        def run():
            with self.assertRaisesRegex(artifact.ContractError, "config input digest"):
                self.validate()

        self.with_manifest(
            lambda m: m["build"].update({"config_digest": "sha256:" + "0" * 64}),
            run,
        )

    def test_missing_source_is_rejected(self):
        def run():
            with self.assertRaisesRegex(artifact.ContractError, "missing required keys"):
                self.validate()

        self.with_manifest(lambda m: m.pop("source"), run)

    def test_remote_validator_rejects_local_snapshot(self):
        with self.assertRaisesRegex(artifact.ContractError, "rejects local_snapshot"):
            self.validate(mode="remote")

    def test_remote_validator_rejects_mutable_repository_slug(self):
        manifest = json.loads(self.original_manifest)
        manifest["source"] = {
            "kind": "repository",
            "repository_id": "owner/gateauth",
            "revision": "a" * 40,
            "tree_sha256": "sha256:" + "b" * 64,
        }
        with self.assertRaisesRegex(artifact.ContractError, "immutable provider"):
            artifact.validate_remote_identity(manifest)

    def test_legacy_auth_and_dev_receipts_pair_by_lwc_source_and_image(self):
        auth = read_fixture("legacy-auth-receipt.fixture.json")
        dev = read_fixture("legacy-dev-receipt.fixture.json")
        self.assertTrue(artifact.validate_receipt_pair(
            auth, dev, "legacy", expected_auth_config_inputs=auth["auth_config_inputs"]
        ))

    def test_legacy_receipts_reject_mixed_source_or_config(self):
        auth = read_fixture("legacy-auth-receipt.fixture.json")
        dev = read_fixture("legacy-dev-receipt.fixture.json")
        bad_auth = copy.deepcopy(auth)
        bad_auth["build_sha"] = "f" * 40
        with self.assertRaisesRegex(artifact.ContractError, "build_sha"):
            artifact.validate_receipt_pair(bad_auth, dev, "legacy")
        with self.assertRaisesRegex(artifact.ContractError, "config inputs"):
            artifact.validate_receipt_pair(auth, dev, "legacy", expected_auth_config_inputs={})

    def test_independent_auth_and_dev_receipts_pair_two_source_identities(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        self.assertNotEqual(dev["source"]["lwc"]["repository_id"], dev["source"]["auth"]["repository_id"])
        self.assertNotEqual(dev["source"]["lwc"]["sha"], dev["source"]["auth"]["revision"])
        self.assertTrue(artifact.validate_receipt_pair(auth, dev, "independent"))

    def test_independent_receipts_reject_auth_lwc_source_mixing(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        mixed = copy.deepcopy(dev)
        mixed["source"]["auth"]["revision"] = mixed["source"]["lwc"]["sha"]
        with self.assertRaisesRegex(artifact.ContractError, "Auth source revision"):
            artifact.validate_receipt_pair(auth, mixed, "independent")

    def test_independent_receipts_reject_mutable_repo_slug(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        bad["source"]["auth"]["repository_id"] = "owner/gateauth"
        with self.assertRaisesRegex(artifact.ContractError, "repository ID is not immutable"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_receipts_reject_tree_digest_mismatch(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        bad["source"]["auth"]["tree_sha256"] = "sha256:" + "9" * 64
        with self.assertRaisesRegex(artifact.ContractError, "tree digest"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_receipts_reject_build_run_mismatch(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        bad["source"]["auth"]["run_id"] += 1
        with self.assertRaisesRegex(artifact.ContractError, "build_run_id"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_receipts_reject_image_digest_mismatch(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        bad["source"]["auth"]["artifact_digest"] = "sha256:" + "9" * 64
        with self.assertRaisesRegex(artifact.ContractError, "artifact digest"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_receipts_reject_dev_image_mismatch(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        bad["images"]["auth"] = bad["images"]["auth"].replace("c" * 64, "9" * 64)
        with self.assertRaisesRegex(artifact.ContractError, "image reference"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_receipts_reject_missing_artifact_digest(self):
        auth = read_fixture("independent-auth-receipt.fixture.json")
        dev = read_fixture("independent-dev-receipt.fixture.json")
        bad = copy.deepcopy(dev)
        del bad["source"]["auth"]["artifact_digest"]
        with self.assertRaisesRegex(artifact.ContractError, "artifact digest"):
            artifact.validate_receipt_pair(auth, bad, "independent")

    def test_independent_mode_rejects_legacy_receipts(self):
        auth = read_fixture("legacy-auth-receipt.fixture.json")
        dev = read_fixture("legacy-dev-receipt.fixture.json")
        with self.assertRaisesRegex(artifact.ContractError, "only Auth receipt schema 3"):
            artifact.validate_receipt_pair(auth, dev, "independent")


if __name__ == "__main__":
    unittest.main()
