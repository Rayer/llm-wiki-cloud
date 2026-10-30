from __future__ import annotations

import copy
import io
import json
from pathlib import Path
import sys
import tempfile
import tarfile
import unittest
import zipfile
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "deploy/engine"))
import gat4_ci_artifact as artifact


class GateAuthCiArtifactTests(unittest.TestCase):
    def fixture(self):
        files = {
            "Dockerfile": b"FROM scratch\n",
            "VERSION": b"1.0.0\n",
            "cloudbuild.yaml": b"steps: []\nimages: []\n",
            "go.mod": b"module github.com/rayer/gateauth\n\ngo 1.26.0\n",
            "go.sum": b"example.invalid dependency hash\n",
            "cmd/auth/main.go": b"package main\nfunc main() {}\n",
        }
        modes = {name: 0o644 for name in files}
        modes["cmd/auth/main.go"] = 0o755
        source_stream = io.BytesIO()
        with tarfile.open(fileobj=source_stream, mode="w") as archive:
            for name in sorted(files):
                header = tarfile.TarInfo(name)
                header.mode = modes[name]
                header.size = len(files[name])
                archive.addfile(header, io.BytesIO(files[name]))
        source_tar = source_stream.getvalue()
        tree_files = artifact._tree_files(source_tar)
        source_id = {
            "repository_id": artifact.REPOSITORY_ID,
            "revision": "1" * 40,
            "tree_sha256": artifact._tree_digest(tree_files),
            "artifact_id": "77",
            "run_id": 88,
            "run_attempt": 2,
        }
        image_prefix = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth"
        image_digest = "sha256:" + "c" * 64
        image_tag = f"{image_prefix}:{source_id['revision']}"
        manifest = {
            "schema": "gateauth-ci-build-v1",
            "component": "auth",
            "product_version": "1.0.0",
            "source": {
                "repository_id": f"github:{source_id['repository_id']}",
                "revision": source_id["revision"],
                "tree_sha256": source_id["tree_sha256"],
            },
            "ci": {"workflow_id": 12, "run_id": source_id["run_id"],
                   "run_attempt": source_id["run_attempt"]},
            "payload": {
                "source_archive_file": "source.tar",
                "source_archive_sha256": artifact.sha256_bytes(source_tar),
                "source_archive_bytes": len(source_tar),
            },
            "build": {
                "project_id": "llm-wiki-cloud",
                "project_number": "580854833715",
                "location": "global",
                "build_id": "11111111-2222-3333-4444-555555555555",
                "status": "SUCCESS",
                "inputs_digest": artifact._inputs_digest(
                    source_tar, tree_files, source_id["revision"], image_prefix, "1.0.0"),
            },
            "artifact": {"image": f"{image_prefix}@{image_digest}",
                         "image_digest": image_digest},
            "contracts": {"auth_config_schema_versions": [1, 2, 3],
                          "lwc_consumer_schema_version": 1},
        }
        envelope = {
            "repository_id": artifact.REPOSITORY_ID,
            "run_id": source_id["run_id"],
            "run_attempt": source_id["run_attempt"],
            "artifact_id": int(source_id["artifact_id"]),
            "artifact_sha256": "",
            "artifact_bytes": 0,
            "workflow_id": 12,
        }

        def make_zip(manifest_value=manifest, source_data=source_tar, extra=None):
            output = io.BytesIO()
            with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as package:
                package.writestr("source.tar", source_data)
                package.writestr("build-manifest.json", json.dumps(
                    manifest_value, sort_keys=True, separators=(",", ":")).encode())
                if extra is not None:
                    package.writestr(extra[0], extra[1])
            data = output.getvalue()
            actual_envelope = {**envelope, "artifact_sha256": artifact.sha256_bytes(data),
                               "artifact_bytes": len(data)}
            return data, actual_envelope

        package_zip, envelope = make_zip()
        expected_source = {**source_id}
        return package_zip, envelope, expected_source, manifest, source_tar, image_prefix, make_zip

    def test_package_verifies_external_envelope_and_same_inner_source_payload(self):
        package_zip, envelope, source, manifest, source_tar, image_prefix, _ = self.fixture()
        checked = artifact.verify_package(package_zip, envelope, source,
                                          "llm-wiki-cloud", image_prefix)
        self.assertEqual(checked, manifest)
        self.assertEqual(manifest["payload"]["source_archive_sha256"],
                         artifact.sha256_bytes(source_tar))
        self.assertNotIn("artifact_id", manifest)
        self.assertNotIn("artifact_sha256", manifest)

    def test_outer_locator_digest_and_size_mismatches_fail_closed(self):
        package_zip, envelope, source, _, _, image_prefix, _ = self.fixture()
        for changed in (
            {**envelope, "artifact_id": envelope["artifact_id"] + 1},
            {**envelope, "run_id": envelope["run_id"] + 1},
            {**envelope, "run_attempt": envelope["run_attempt"] + 1},
            {**envelope, "artifact_sha256": "sha256:" + "f" * 64},
            {**envelope, "artifact_bytes": envelope["artifact_bytes"] + 1},
        ):
            with self.subTest(changed=changed):
                with self.assertRaises(artifact.ArtifactError):
                    artifact.verify_package(package_zip, changed, source,
                                            "llm-wiki-cloud", image_prefix)

    def test_payload_manifest_and_extra_zip_entries_fail_closed(self):
        package_zip, envelope, source, manifest, source_tar, image_prefix, make_zip = self.fixture()
        changed_tar = source_tar[:-1] + bytes([source_tar[-1] ^ 1])
        changed_zip, changed_envelope = make_zip(source_data=changed_tar)
        with self.assertRaisesRegex(artifact.ArtifactError, "checksum or size"):
            artifact.verify_package(changed_zip, changed_envelope, source,
                                   "llm-wiki-cloud", image_prefix)

        changed_manifest = copy.deepcopy(manifest)
        changed_manifest["source"]["revision"] = "f" * 40
        changed_zip, changed_envelope = make_zip(manifest_value=changed_manifest)
        with self.assertRaisesRegex(artifact.ArtifactError, "source does not match"):
            artifact.verify_package(changed_zip, changed_envelope, source,
                                   "llm-wiki-cloud", image_prefix)

        changed_zip, changed_envelope = make_zip(extra=("unexpected.txt", b"extra"))
        with self.assertRaisesRegex(artifact.ArtifactError, "exactly"):
            artifact.verify_package(changed_zip, changed_envelope, source,
                                   "llm-wiki-cloud", image_prefix)

    def test_manifest_build_image_and_tree_identity_are_checked(self):
        _, envelope, source, manifest, _, image_prefix, make_zip = self.fixture()
        for mutate, detail in (
            (lambda value: value["ci"].update(workflow_id=13), "workflow identity"),
            (lambda value: value["ci"].update(run_id=source["run_id"] + 1), "workflow identity"),
            (lambda value: value["build"].update(status="WORKING"), "provider build identity"),
            (lambda value: value["artifact"].update(image_digest="sha256:" + "d" * 64), "image digest"),
            (lambda value: value["source"].update(tree_sha256="sha256:" + "e" * 64), "source does not match"),
            (lambda value: value["build"].update(inputs_digest="sha256:" + "e" * 64), "inputs digest"),
        ):
            altered = copy.deepcopy(manifest)
            mutate(altered)
            package_zip, package_envelope = make_zip(manifest_value=altered)
            with self.subTest(detail=detail):
                with self.assertRaisesRegex(artifact.ArtifactError, detail):
                    artifact.verify_package(package_zip, package_envelope, source,
                                            "llm-wiki-cloud", image_prefix)

    def test_private_download_binds_repository_run_artifact_and_raw_zip_bytes(self):
        package_zip, outer, source, _, _, image_prefix, _ = self.fixture()
        artifact_name = f"{artifact.ARTIFACT_PREFIX}{source['revision']}-{source['run_attempt']}"
        repo = {"id": artifact.REPOSITORY_ID, "private": True,
                "default_branch": "main", "full_name": artifact.REPOSITORY_NAME}
        run = {
            "id": source["run_id"],
            "repository": {"id": artifact.REPOSITORY_ID},
            "head_repository": {"id": artifact.REPOSITORY_ID},
            "run_attempt": source["run_attempt"],
            "head_sha": source["revision"], "head_branch": "main",
            "event": "workflow_dispatch", "path": artifact.WORKFLOW_PATH,
            "status": "completed", "conclusion": "success", "workflow_id": outer["workflow_id"],
        }
        listed = {"id": int(source["artifact_id"]), "name": artifact_name,
                  "digest": outer["artifact_sha256"], "size_in_bytes": len(package_zip),
                  "workflow_run": {"id": source["run_id"],
                                   "repository_id": artifact.REPOSITORY_ID,
                                   "head_sha": source["revision"], "head_branch": "main"}}
        details = {
            **listed,
            "expired": False,
            "workflow_run": {"id": source["run_id"],
                             "repository_id": artifact.REPOSITORY_ID,
                             "head_sha": source["revision"]},
        }

        def api_read(_token, path):
            if path == f"/repositories/{artifact.REPOSITORY_ID}":
                return repo
            if path == f"/repos/{artifact.REPOSITORY_NAME}/actions/runs/{source['run_id']}":
                return run
            if path == f"/repos/{artifact.REPOSITORY_NAME}/actions/runs/{source['run_id']}/artifacts":
                return {"total_count": 1, "artifacts": [listed]}
            if path == f"/repos/{artifact.REPOSITORY_NAME}/actions/artifacts/{source['artifact_id']}":
                return details
            raise AssertionError(f"unexpected API path: {path}")

        with tempfile.TemporaryDirectory() as raw:
            package_path = Path(raw) / "package.zip"
            envelope_path = Path(raw) / "envelope.json"
            with patch.object(artifact, "_api_json", side_effect=api_read), patch.object(
                    artifact, "_download_zip", return_value=package_zip) as download:
                saved = artifact.download_artifact(
                    source["repository_id"], source["revision"], source["tree_sha256"],
                    int(source["artifact_id"]), source["run_id"], source["run_attempt"],
                    package_path, envelope_path, token="synthetic-read-only-token",
                )
            self.assertEqual(saved, outer)
            self.assertEqual(package_path.read_bytes(), package_zip)
            self.assertEqual(json.loads(envelope_path.read_text()), outer)
            self.assertEqual(download.call_count, 1)
            self.assertEqual(artifact.load_package(package_path, envelope_path, source,
                                                   "llm-wiki-cloud", image_prefix)["schema"],
                             "gateauth-ci-build-v1")

        run["conclusion"] = "failure"
        with patch.object(artifact, "_api_json", side_effect=api_read), patch.object(
                artifact, "_download_zip") as download:
            with self.assertRaisesRegex(artifact.ArtifactError, "successful exact-SHA"):
                artifact.download_artifact(
                    source["repository_id"], source["revision"], source["tree_sha256"],
                    int(source["artifact_id"]), source["run_id"], source["run_attempt"],
                    "unused.zip", "unused.json", token="synthetic-read-only-token",
                )
            download.assert_not_called()


if __name__ == "__main__":
    unittest.main()
