import base64
import http.server
import hashlib
import json
import os
import shutil
import sys
import subprocess
import textwrap
import threading
import unittest
from io import BytesIO
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import patch
import urllib.request
import zipfile

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_notes
import release_notes_workflow
import release_notes_publish
import engine
from test_release_notes import canonical, note, operation_fixture, plan_fixture, run_capture_cli_with_service_url_failure


ROOT = Path(__file__).resolve().parents[3]
DOWNLOAD_ACTION = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
PINNED_DOWNLOAD_SOURCE = ROOT / "scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts"
ARTIFACT_SDK_BOUNDARY = ROOT / "deploy/engine/tests/lwc478_artifact_sdk_boundary.cjs"
ARTIFACT_SDK_NODE_MODULES = ROOT / "deploy/engine/tests/artifact-sdk/node_modules"


class ReleaseNotesWorkflowLayoutTests(unittest.TestCase):
    def run_artifact_sdk(self, *arguments):
        environment = os.environ.copy()
        environment.update({
            "NODE_PATH": str(ARTIFACT_SDK_NODE_MODULES),
            "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
            "GCE_METADATA_HOST": "127.0.0.1:9",
            "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
            "AWS_EC2_METADATA_DISABLED": "true",
            "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "",
            "http_proxy": "", "https_proxy": "", "all_proxy": "",
            "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
        })
        completed = subprocess.run(
            ["node", "--experimental-strip-types", str(ARTIFACT_SDK_BOUNDARY), *map(str, arguments)],
            cwd=ROOT, env=environment, text=True, capture_output=True, check=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result_lines = [line.removeprefix("LWC478_RESULT ") for line in completed.stdout.splitlines()
                        if line.startswith("LWC478_RESULT ")]
        self.assertEqual(len(result_lines), 1, completed.stdout)
        return json.loads(result_lines[0])

    def download_result_with_offline_transport(self, archive_path, destination):
        harness = textwrap.dedent(r"""
            const fs = require('node:fs');
            const vm = require('node:vm');
            const {execFileSync} = require('node:child_process');
            const actionSource = fs.readFileSync(process.argv[2], 'utf8')
              .replace('main().catch(error => {', 'globalThis.__run = main().catch(error => {');
            const archivePath = process.argv[3];
            const destination = process.argv[4];
            const artifact = {id: 9001, name: 'lwc-result-development-42-1', expired: false,
              workflow_run: {id: 42}};
            const cliProcess = {argv: ['node', 'artifacts.cjs', 'download-result', '9001', destination, 'development'],
              env: {GITHUB_REPOSITORY: 'test/repo', GH_TOKEN: 'TEST_ONLY'}, exitCode: 0};
            class Client {
              async downloadArtifact(id, options) {
                if (id !== 9001 || options.findBy.token !== 'TEST_ONLY') throw Error('wrong exact artifact request');
                fs.mkdirSync(options.path, {recursive: true});
                execFileSync('unzip', ['-q', archivePath, '-d', options.path]);
              }
            }
            const sandbox = {
              require: name => name === '@actions/artifact' ? {DefaultArtifactClient: Client} : require(name),
              process: cliProcess,
              AbortSignal,
              fetch: async url => {
                const endpoint = url.split('/repos/test/repo/')[1];
                if (endpoint === 'actions/artifacts/9001') return {ok: true, json: async () => artifact};
                if (endpoint === 'actions/runs/42') return {ok: true, json: async () => ({
                  event: 'workflow_dispatch', path: '.github/workflows/recover-deployment.yml'})};
                throw Error('unexpected test-only artifact API request');
              },
              console: {log() {}, error() {}},
            };
            vm.runInNewContext(actionSource, sandbox);
            sandbox.__run.then(() => {
              if (cliProcess.exitCode !== 0) process.exitCode = cliProcess.exitCode;
            }).catch(() => { process.exitCode = 1; });
        """)
        with TemporaryDirectory() as temporary:
            harness_path = Path(temporary) / "download-result.js"
            harness_path.write_text(harness)
            completed = subprocess.run([
                "node", str(harness_path), str(ROOT / "deploy/engine/artifacts.cjs"),
                str(archive_path), str(destination),
            ], cwd=ROOT, text=True, capture_output=True, check=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)

    def test_exact_id_download_merges_package_files_at_the_validator_root(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        for job_name in ("release", "notes-resend"):
            with self.subTest(job=job_name):
                step = next(step for step in workflow["jobs"][job_name]["steps"]
                            if step.get("name") == "Download exact TPM Release Notes artifact")
                self.assertEqual(step["uses"], DOWNLOAD_ACTION)
                self.assertIn("artifact-ids", step["with"])
                self.assertTrue(step["with"]["merge-multiple"])

        # This is the path branch from the exact pinned action source. With an
        # artifact ID and merge-multiple=true, extraction is into resolvedPath;
        # without it, the action nests files under the artifact name.
        source = PINNED_DOWNLOAD_SOURCE.read_text()
        self.assertIn("isSingleArtifactDownload || inputs.mergeMultiple", source)
        self.assertIn("? resolvedPath\n          : path.join(resolvedPath, artifact.name)", source)

        plan_bytes = canonical(plan_fixture())
        body = note()
        manifest = {
            "schema_version": 1,
            "product": "llm-wiki-cloud",
            "product_version": "1.2.3",
            "environment": "dev",
            "release_plan_sha256": release_notes.digest_bytes(plan_bytes),
            "release_note_snapshot_ref": "LWC-A-57:4-123",
            "release_note_snapshot_sha256": "sha256:" + "a" * 64,
            "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
            "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
        }
        digest = "sha256:" + "c" * 64
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = root / "package"
            package.mkdir()
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            archive = root / "lwc-release-notes-artifact.zip"
            with zipfile.ZipFile(archive, "w") as output:
                for path in package.iterdir():
                    output.write(path, path.name)

            destination = root / "release-notes-package"
            destination.mkdir()
            # Mirror the path decision made by the inspected action before its
            # artifact transport extracts the archive.
            extraction_path = destination if workflow["jobs"]["release"]["steps"][
                next(i for i, step in enumerate(workflow["jobs"]["release"]["steps"])
                     if step.get("name") == "Download exact TPM Release Notes artifact")
            ]["with"]["merge-multiple"] else destination / "lwc-release-notes-artifact-name"
            extraction_path.mkdir(parents=True, exist_ok=True)
            with zipfile.ZipFile(archive) as zipped:
                zipped.extractall(extraction_path)
            self.assertTrue((destination / "release-notes-manifest.json").is_file())
            version, files, _ = release_notes.validate_package(
                destination, expected_environment="dev", expected_artifact_digest=digest,
                artifact_metadata_digest=digest, plan_bytes=plan_bytes)
            self.assertEqual(version, "1.2.3")
            self.assertEqual(files, {"en": body})

    def test_post_release_capture_failures_enter_retained_publisher_evidence(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        release_steps = workflow["jobs"]["release"]["steps"]
        capture_steps = [step for step in release_steps
                         if "release_notes_capture.py" in step.get("run", "")]
        self.assertEqual(len(capture_steps), 3)
        contexts = ('"$RELEASE_NOTES_ACTION"', "skip", "marker")
        for step, context in zip(capture_steps, contexts):
            self.assertIn(f"--release-notes-action {context}", step["run"])
            self.assertIn('--product-status released', step["run"])
            self.assertIn('--environment "${{ inputs.environment == \'production\' && \'prod\' || \'dev\' }}"',
                          step["run"])

        capture = (ROOT / "deploy/engine/release_notes_capture.py").read_text()
        self.assertIn("release_notes_publish.record_capture_failure(", capture)
        self.assertIn("evidence_recorded", capture)
        publisher = (ROOT / "deploy/engine/release_notes_publish.py").read_text()
        self.assertIn('"next_actions": ["retry", "skip", "stop"]', publisher)
        self.assertIn('evidence["published_at"] = publication["published_at"]', publisher)

    def test_result_upload_single_root_survives_archive_download_and_native_verification(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        for job_name, step_name in (("release", "Retain final result even after failure"),
                                    ("notes-resend", "Retain independent Notes resend evidence")):
            step = next(step for step in workflow["jobs"][job_name]["steps"] if step.get("name") == step_name)
            self.assertEqual(step["with"]["path"], "${{ runner.temp }}/release")
            self.assertNotIn("release-notes-package", step["with"]["path"])

        plan_bytes, state_bytes, result_bytes, _, receipt_bytes, _ = operation_fixture()
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            upload_root = root / "release"
            (upload_root / "receipts").mkdir(parents=True)
            (upload_root / "plan.json").write_bytes(plan_bytes)
            (upload_root / "state.json").write_bytes(state_bytes)
            (upload_root / "result.json").write_bytes(result_bytes)
            (upload_root / "receipts" / "bff.json").write_bytes(receipt_bytes)

            # A scalar upload path makes the selected release directory the
            # archive root; this mirrors upload-artifact's relative path set.
            scalar_archive = root / "scalar-result.zip"
            with zipfile.ZipFile(scalar_archive, "w") as archive:
                for path in sorted(upload_root.rglob("*")):
                    if path.is_file():
                        archive.write(path, path.relative_to(upload_root).as_posix())
            with zipfile.ZipFile(scalar_archive) as archive:
                self.assertEqual(set(archive.namelist()), {
                    "plan.json", "state.json", "result.json", "receipts/bff.json"})
            restored = root / "restored-release"
            self.download_result_with_offline_transport(scalar_archive, restored)

            verify_args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(restored), "--environment", "dev",
                "--bucket", "unused", "--plan", str(restored / "plan.json"), "--operation", "rollback",
                "--run-id", "701", "--run-attempt", "1", "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                evidence = release_notes_publish.execute(verify_args)
            self.assertEqual(evidence["status"], "verified")
            self.assertEqual(evidence["operation"], "rollback")

            # The previous sibling-path search rooted the same files one level
            # down, so the unchanged result reader could not find plan.json.
            sibling_archive = root / "sibling-result.zip"
            with zipfile.ZipFile(sibling_archive, "w") as archive:
                archive.write(upload_root / "plan.json", "release/plan.json")
                archive.write(upload_root / "state.json", "release/state.json")
                archive.write(upload_root / "result.json", "release/result.json")
                archive.write(upload_root / "receipts" / "bff.json", "release/receipts/bff.json")
                archive.writestr("release-notes-package/manifest.json", "test-only sibling")
            restored_sibling = root / "restored-siblings"
            self.download_result_with_offline_transport(sibling_archive, restored_sibling)
            self.assertFalse((restored_sibling / "plan.json").exists())
            self.assertTrue((restored_sibling / "release" / "plan.json").is_file())
            with self.assertRaises(FileNotFoundError):
                (restored_sibling / "plan.json").read_bytes()

    def test_pinned_uploader_and_retained_sdk_roundtrip_package_and_native_results(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        for job_name, step_name in (("release", "Retain final result even after failure"),
                                    ("notes-resend", "Retain independent Notes resend evidence")):
            upload_step = next(step for step in workflow["jobs"][job_name]["steps"]
                               if step.get("name") == step_name)
            self.assertEqual(upload_step["uses"], "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02")
            self.assertEqual(upload_step["with"]["path"], "${{ runner.temp }}/release")
        release_download = next(step for step in workflow["jobs"]["release"]["steps"]
                                if step.get("name") == "Download exact TPM Release Notes artifact")
        self.assertEqual(release_download["uses"], DOWNLOAD_ACTION)
        self.assertTrue(release_download["with"]["merge-multiple"])
        sdk_lock = json.loads((ROOT / "deploy/engine/tests/artifact-sdk/package-lock.json").read_text())
        self.assertEqual(sdk_lock["packages"]["node_modules/@actions/artifact"]["version"], "2.3.2")

        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            plan_bytes = canonical(plan_fixture())
            body = note()
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3",
                "environment": "dev", "release_plan_sha256": release_notes.digest_bytes(plan_bytes),
                "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
            }
            package = root / "notes-package"
            package.mkdir()
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            notes_archive = root / "notes.zip"
            notes_upload = self.run_artifact_sdk(
                "upload", package, notes_archive, "lwc-release-notes-test-701-1")
            self.assertEqual(notes_upload["rootDirectory"], str(package))
            notes_restored = root / "notes-restored"
            notes_download = self.run_artifact_sdk(
                "download", notes_archive, notes_restored, notes_upload["archiveSha256"], "9001")
            self.assertFalse(notes_download["digestMismatch"])
            version, locales, _ = release_notes.validate_package(
                notes_restored, expected_environment="dev", expected_artifact_digest=notes_upload["archiveSha256"],
                artifact_metadata_digest=notes_upload["archiveSha256"], plan_bytes=plan_bytes)
            self.assertEqual(version, "1.2.3")
            self.assertEqual(locales, {"en": body})

            plan, state, result, _, receipt, _ = operation_fixture()
            result_root = root / "release"
            (result_root / "receipts").mkdir(parents=True)
            (result_root / "plan.json").write_bytes(plan)
            (result_root / "state.json").write_bytes(state)
            (result_root / "result.json").write_bytes(result)
            (result_root / "receipts" / "bff.json").write_bytes(receipt)
            result_archive = root / "result.zip"
            result_upload = self.run_artifact_sdk(
                "upload", result_root, result_archive, "lwc-result-development-701-1")
            self.assertEqual(result_upload["rootDirectory"], str(result_root))
            with zipfile.ZipFile(result_archive) as archive:
                self.assertEqual(set(archive.namelist()),
                                 {"plan.json", "state.json", "result.json", "receipts/bff.json"})
            result_restored = root / "result-restored"
            result_download = self.run_artifact_sdk(
                "download", result_archive, result_restored, result_upload["archiveSha256"], "9001")
            self.assertFalse(result_download["digestMismatch"])
            verify_args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(result_restored), "--environment", "dev",
                "--bucket", "unused", "--plan", str(result_restored / "plan.json"), "--operation", "rollback",
                "--run-id", "701", "--run-attempt", "1", "--result-artifact-id", "9001",
                "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                verified = release_notes_publish.execute(verify_args)
            self.assertEqual(verified["status"], "verified")

            digest_mismatch = self.run_artifact_sdk(
                "download", result_archive, root / "digest-mismatch", "sha256:" + "0" * 64, "9001")
            self.assertTrue(digest_mismatch["digestMismatch"])

            wrong_root = root / "wrong-root"
            wrong_release = wrong_root / "release"
            (wrong_release / "receipts").mkdir(parents=True)
            (wrong_release / "plan.json").write_bytes(plan)
            (wrong_release / "state.json").write_bytes(state)
            (wrong_release / "result.json").write_bytes(result)
            (wrong_release / "receipts" / "bff.json").write_bytes(receipt)
            sibling_package = wrong_root / "release-notes-package"
            sibling_package.mkdir()
            (sibling_package / "manifest.json").write_text("wrong-root sibling")
            wrong_archive = root / "wrong-root.zip"
            self.run_artifact_sdk("upload", wrong_root, wrong_archive, "lwc-result-development-701-1")
            wrong_restored = root / "wrong-root-restored"
            self.run_artifact_sdk("download", wrong_archive, wrong_restored,
                                  "sha256:" + hashlib.sha256(wrong_archive.read_bytes()).hexdigest(), "9001")
            with zipfile.ZipFile(wrong_archive) as archive:
                self.assertIn("release/plan.json", archive.namelist())
                self.assertIn("release-notes-package/manifest.json", archive.namelist())
                self.assertNotIn("plan.json", archive.namelist())
            wrong_root_args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(wrong_restored), "--environment", "dev",
                "--bucket", "unused", "--plan", str(wrong_restored / "release" / "plan.json"),
                "--operation", "rollback", "--run-id", "701", "--run-attempt", "1",
                "--result-artifact-id", "9001", "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")), \
                    self.assertRaises(release_notes.ReleaseNotesError) as rejected:
                release_notes_publish.execute(wrong_root_args)
            self.assertEqual(rejected.exception.safe_class, "retained-operation-evidence-unavailable")

            bad_result = json.loads(result)
            bad_result["expected"]["identity"] = "0" * 64
            bad_root = root / "wrong-identity"
            (bad_root / "receipts").mkdir(parents=True)
            (bad_root / "plan.json").write_bytes(plan)
            (bad_root / "state.json").write_bytes(state)
            (bad_root / "result.json").write_bytes(canonical(bad_result))
            (bad_root / "receipts" / "bff.json").write_bytes(receipt)
            bad_archive = root / "wrong-identity.zip"
            bad_upload = self.run_artifact_sdk("upload", bad_root, bad_archive,
                                               "lwc-result-development-701-1")
            bad_restored = root / "wrong-identity-restored"
            self.run_artifact_sdk("download", bad_archive, bad_restored, bad_upload["archiveSha256"], "9001")
            bad_verify_args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(bad_restored), "--environment", "dev",
                "--bucket", "unused", "--plan", str(bad_restored / "plan.json"), "--operation", "rollback",
                "--run-id", "701", "--run-attempt", "1", "--result-artifact-id", "9001",
                "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")), \
                    self.assertRaises(release_notes.ReleaseNotesError) as rejected:
                release_notes_publish.execute(bad_verify_args)
            self.assertEqual(rejected.exception.safe_class, "native-operation-evidence-invalid")

    def test_supplied_authentic_prior_artifact_roundtrips_without_rewriting_original(self):
        handoff_path = os.environ.get("LWC478_AUTHENTIC_ARTIFACT_HANDOFF")
        if not handoff_path:
            self.skipTest("authentic prior artifact is supplied only to retained source qualification")
        handoff = json.loads(Path(handoff_path).read_text())
        self.assertIn("NOT_native_qualification", handoff["kind"])
        metadata_bytes = Path(handoff["metadata_path"]).read_bytes()
        self.assertEqual(hashlib.sha256(metadata_bytes).hexdigest(), handoff["metadata_sha256"])
        metadata = json.loads(metadata_bytes)
        matches = [item for item in metadata["artifacts"] if item.get("id") == 14112458604]
        self.assertEqual(len(matches), 1)
        artifact = matches[0]
        self.assertEqual(set(artifact), set(handoff["artifact"]))
        self.assertEqual(artifact["id"], 14112458604)
        self.assertEqual(artifact["name"], "lwc-result-development-43591600617-1")
        self.assertEqual(artifact["digest"], handoff["observed_zip_digest"])
        source_archive = Path(handoff["zip_path"])
        source_digest = "sha256:" + hashlib.sha256(source_archive.read_bytes()).hexdigest()
        self.assertEqual(source_digest, handoff["observed_zip_digest"])

        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive_copy = root / "authentic-old-result.zip"
            shutil.copyfile(source_archive, archive_copy)
            restored = root / "authentic-old-result-restored"
            result = self.run_artifact_sdk(
                "download", archive_copy, restored, handoff["observed_zip_digest"], str(artifact["id"]))
            self.assertFalse(result["digestMismatch"])
            self.assertFalse((restored / "release").exists())
            for name in handoff["root_entries_verified"]:
                self.assertTrue((restored / name).is_file(), name)
            with zipfile.ZipFile(archive_copy) as archive:
                members = archive.namelist()
            self.assertEqual(len(members), handoff["member_count"])
            self.assertTrue(all((restored / name).is_file() for name in members))
            plan = json.loads((restored / "plan.json").read_bytes())
            self.assertEqual(plan["source"], artifact["workflow_run"]["head_sha"])
            self.assertEqual(plan["normalized"]["environment"], "development")
            self.assertEqual(artifact["workflow_run"]["head_branch"], "develop")
            operation_attempt = int(artifact["name"].rsplit("-", 1)[1])
            verify_args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(restored), "--environment", "dev",
                "--bucket", "unused", "--plan", str(restored / "plan.json"), "--operation", "release",
                "--run-id", str(artifact["workflow_run"]["id"]), "--run-attempt", str(operation_attempt),
                "--result-artifact-id", str(artifact["id"]), "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                verified = release_notes_publish.execute(verify_args)
            self.assertEqual(verified["status"], "verified")
            self.assertEqual(verified["result_artifact_id"], str(artifact["id"]))
        self.assertEqual(hashlib.sha256(source_archive.read_bytes()).hexdigest(), handoff["observed_zip_digest"].removeprefix("sha256:"))

    def test_parent_authentic_native_operations_pass_notes_readers_and_reach_capture(self):
        counterexample_path = os.environ.get("LWC478_AUTHENTIC_NATIVE_COUNTEREXAMPLE")
        if not counterexample_path:
            self.skipTest("parent-provided authentic native artifacts are supplied only to retained source qualification")
        counterexample = json.loads(Path(counterexample_path).read_text())
        artifact_base = Path(counterexample_path).parent / "authentic-native-operations"

        for row in counterexample["rows"]:
            with self.subTest(artifact=row["artifact_id"]), TemporaryDirectory() as temporary:
                root = Path(temporary)
                source_root = artifact_base / row["label"] / "extracted"
                (root / "receipts").mkdir()
                for relative_path, expected_digest in row["file_hashes"].items():
                    source = source_root / relative_path
                    destination = root / relative_path
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    data = source.read_bytes()
                    self.assertEqual(hashlib.sha256(data).hexdigest(), expected_digest)
                    destination.write_bytes(data)

                plan_bytes = (root / "plan.json").read_bytes()
                state_bytes = (root / "state.json").read_bytes()
                result_bytes = (root / "result.json").read_bytes()
                receipt_bytes = (root / "receipts" / "bff.json").read_bytes()
                plan = json.loads(plan_bytes)
                candidate = json.loads(state_bytes)["components"]["bff"]["candidate"]
                self.assertEqual(plan["source"], row["source"])
                self.assertFalse(row["candidate_image_present"])
                self.assertNotIn("image", candidate)

                metadata = json.loads((artifact_base / row["label"] / "artifact-metadata.json").read_text())
                artifact = metadata
                self.assertEqual(artifact["id"], row["artifact_id"])
                run_id = str(artifact["workflow_run"]["id"])
                run_attempt = int(artifact["name"].rsplit("-", 1)[1])
                operation = "release" if row["label"] == "authentic4e" else "deploy"

                native = release_notes.validate_native_operation(
                    plan_bytes, state_bytes, result_bytes, receipt_bytes, environment="dev",
                    workflow_operation=operation, run_id=run_id, run_attempt=run_attempt)
                self.assertEqual(native["plan"]["source"], row["source"])
                self.assertIn("image", native["receipt"]["artifact"])

                verify_args = release_notes_publish.parser().parse_args([
                    "--action", "verify-operation", "--directory", str(root), "--environment", "dev",
                    "--bucket", "unused", "--plan", str(root / "plan.json"), "--operation", operation,
                    "--run-id", run_id, "--run-attempt", str(run_attempt),
                    "--result-artifact-id", str(row["artifact_id"]), "--product-status", "released",
                ])
                with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                    verified = release_notes_publish.execute(verify_args)
                self.assertEqual(verified["status"], "verified")

                if operation == "release":
                    publication_args = release_notes_publish.parser().parse_args([
                        "--action", "record-publication", "--directory", str(root), "--environment", "dev",
                        "--bucket", "unused", "--plan", str(root / "plan.json"), "--operation", operation,
                        "--run-id", run_id, "--run-attempt", str(run_attempt), "--product-status", "released",
                        "--published-at", "2026-11-14T10:00:00Z",
                    ])
                    publication = release_notes_publish.execute(publication_args)
                    self.assertEqual(publication["status"], "verified")

                resend_context = SimpleNamespace(
                    action="resend", directory=root, environment="dev", operation=operation,
                    run_id=run_id, run_attempt=run_attempt, plan=str(root / "plan.json"))
                marker, sidecar_bytes = release_notes_publish.operation_marker(root, resend_context)
                self.assertIsNone(marker)
                self.assertEqual(sidecar_bytes, b"")

                capture_action = "run" if operation == "release" else "marker"
                capture_attempt = run_capture_cli_with_service_url_failure(
                    root, operation=operation, action=capture_action, run_id=run_id,
                    artifact_id=str(row["artifact_id"]), artifact_digest=artifact["digest"])
                self.assertEqual(capture_attempt.returncode, 1, capture_attempt.stderr)
                capture_failure = json.loads(capture_attempt.stdout)
                self.assertEqual(capture_failure["stage"], "index_update")
                self.assertEqual(capture_failure["safe_class"], "service-url-unavailable")
                self.assertTrue(capture_failure["evidence_recorded"])
                self.assertFalse((root / "release-notes-operation.json").exists())
                evidence = json.loads((root / "release-notes-evidence.json").read_text())["stages"]
                self.assertEqual(evidence[-1]["action"], "capture")
                self.assertEqual(evidence[-1]["operation"], operation)
                self.assertEqual(evidence[-1]["run_id"], run_id)
                self.assertEqual(evidence[-1]["artifact_id"], str(row["artifact_id"]))
                if operation == "release":
                    self.assertEqual(evidence[-1]["published_at"], "2026-11-14T10:00:00Z")




class ReleaseNotesWorkflowInputTests(unittest.TestCase):
    def setUp(self):
        self.plan = {
            "schema": 3,
            "id": "plan-701",
            "tag": "2026.11.4",
            "source": "a" * 40,
            "normalized": {"environment": "development"},
            "identities": {"bff": {"profile": "bff", "digest": "inputs"}},
            "selected": ["bff", "frontend"],
        }
        self.plan_bytes = canonical(self.plan)
        self.body = note("1.2.3", "en", "Private package payload sentinel")
        self.manifest = {
            "schema_version": 1,
            "product": "llm-wiki-cloud",
            "product_version": "1.2.3",
            "environment": "dev",
            "release_plan_sha256": hashlib.sha256(self.plan_bytes).hexdigest(),
            "release_note_snapshot_ref": "LWC-A-62:approved-snapshot-17",
            "release_note_snapshot_sha256": "sha256:" + "f" * 64,
            "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
            "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(self.body).hexdigest()}},
        }
        self.manifest_bytes = canonical(self.manifest)

    def make_package(self, root):
        root.mkdir(parents=True)
        (root / "release-notes-manifest.json").write_bytes(self.manifest_bytes)
        (root / "en.json").write_bytes(self.body)
        return root

    def make_plan_file(self, root, plan_bytes=None):
        path = root / "plan.json"
        path.write_bytes(plan_bytes if plan_bytes is not None else self.plan_bytes)
        return path

    def make_ready_handoff(self, root):
        producer_sha = "b" * 40
        plan = {
            "schema": 3,
            "tag": "v2026.11.5",
            "source": "a" * 40,
            "branch": "develop",
            "executor_sha": producer_sha,
            "normalized": {"environment": "development", "auth": {"runtime_inputs": {"schema_version": 4}}},
            "identities": {"bff": {"profile": "bff", "digest": "bff-inputs"},
                           "frontend": {"profile": "frontend", "digest": "frontend-inputs"}},
            "dev_reference": None,
            "selected": ["bff", "frontend"],
            "release_source_mode": "legacy_monorepo",
        }
        plan["id"] = release_notes_workflow._plan_id(plan)
        plan_bytes = canonical(plan)
        state = {"plan": plan["id"], "status": "ready", "components": {}, "sequence": 5,
                 "checkpoint_schema": 1, "builds": {}, "executor_sha": producer_sha}
        state_bytes = canonical(state)
        result = {
            "release": plan["tag"], "attempt": plan["id"], "stage": "ready", "status": "ready",
            "component": "frontend", "reason": "completed", "mutation_may_have_happened": False,
            "prior": None, "candidate": None,
            "expected": {"source": plan["source"], "identity": "c" * 64},
            "observed": {"component_status": "unstarted"}, "last_verified_checkpoint": 5,
        }
        result_bytes = canonical(result)
        notes_evidence = {
            "schema_version": 1,
            "stages": [{
                "schema_version": 1, "action": "prepare", "environment": "dev", "stage": "validate",
                "status": "failed", "safe_class": "artifact-id-required", "product_status": "not_started",
                "plan_sha256": hashlib.sha256(plan_bytes).hexdigest(), "artifact_id": None,
                "next_actions": ["retry", "skip", "stop"], "owner_choice_required": True,
                "automatic_skip": False, "event_sequence": 1, "stage_attempt": 1,
                "recorded_at": "2026-11-14T17:00:00Z",
            }],
        }
        evidence_bytes = canonical(notes_evidence)
        ready_dir = root / "ready"
        result_dir = root / "result"
        ready_dir.mkdir()
        result_dir.mkdir()
        for directory in (ready_dir, result_dir):
            (directory / "plan.json").write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
        (result_dir / "release-notes-evidence.json").write_bytes(evidence_bytes)
        ready_id, result_id, run_id, attempt = 701, 702, 600, 1
        ready_name = f"lwc-ready-development-{run_id}-{attempt}"
        result_name = f"lwc-result-development-{run_id}-{attempt}"
        ready_digest = "sha256:" + "d" * 64
        result_digest = "sha256:" + "e" * 64
        metadata = {"id": ready_id, "name": ready_name, "expired": False, "digest": ready_digest,
                    "expires_at": "2027-02-12T00:00:00Z", "workflow_run": {"id": run_id}}
        run = {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
               "path": ".github/workflows/deploy-dev.yml", "status": "completed", "conclusion": "failure",
               "head_branch": "develop", "head_sha": producer_sha}
        artifact_list = {"artifacts": [{
            "id": result_id, "name": result_name, "expired": False, "digest": result_digest,
            "expires_at": "2027-02-12T00:00:00Z", "workflow_run": {"id": run_id},
        }]}
        result_metadata = {**artifact_list["artifacts"][0]}
        return {"plan": plan, "plan_bytes": plan_bytes, "state_bytes": state_bytes,
                "result_bytes": result_bytes, "metadata": metadata, "run": run,
                "artifact_list": artifact_list, "result_metadata": result_metadata,
                "ready_id": str(ready_id), "result_id": str(result_id),
                "ready_dir": ready_dir, "result_dir": result_dir}

    def make_native_ready_handoff(self, root):
        producer_sha = "b" * 40
        plan = {
            "schema": 3, "tag": "v2026.11.5", "source": "a" * 40,
            "branch": "develop", "executor_sha": producer_sha,
            "normalized": {"environment": "development", "gcp": {"project_id": "fixture-project"}},
            "identities": {"bff": {"profile": "bff", "digest": "native-fixture-inputs"}},
            "dev_reference": None, "selected": ["bff"],
            "release_source_mode": "legacy_monorepo",
        }
        plan["id"] = engine.plan_id(plan)
        ready_dir = root / "native-ready"
        ready_dir.mkdir()
        engine.write(ready_dir / "plan.json", plan)
        native = engine.Engine(ready_dir, executor_sha=producer_sha)

        def prepare_container(component):
            self.assertEqual(component, "bff")
            native.state["builds"][component] = {
                "project_id": "fixture-project", "location": engine.CLOUD_BUILD_LOCATION,
                "build_id": "11111111-1111-1111-1111-111111111111",
                "identity_verified": True, "status": "SUCCESS",
                "last_observed_status": "SUCCESS", "poll_outcome": "success",
                "reported_project_id": "fixture-project", "reported_location": engine.CLOUD_BUILD_LOCATION,
            }
            native.save()
            return {"image": "us-docker.pkg.dev/fixture/bff@sha256:" + "c" * 64}

        native.prepare_container = prepare_container
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}, clear=False), \
                patch.object(engine.Providers, "usable", lambda _provider, _component, _artifact: None):
            native.prepare()
            native.result()
        plan_bytes = (ready_dir / "plan.json").read_bytes()
        state_bytes = (ready_dir / "state.json").read_bytes()
        result_bytes = (ready_dir / "result.json").read_bytes()
        self.assertEqual(native.state["status"], "ready")
        self.assertEqual(native.state["components"], {})

        failure_args = release_notes_publish.parser().parse_args([
            "--action", "artifact-failure", "--directory", str(ready_dir),
            "--environment", "dev", "--bucket", "lwc-wiki-data-dev",
            "--plan", str(ready_dir / "plan.json"), "--operation", "release",
            "--run-id", "600", "--run-attempt", "1", "--failure-class", "artifact-id-required",
        ])
        evidence = release_notes_publish.execute(failure_args)
        self.assertEqual((evidence["safe_class"], evidence["product_status"]),
                         ("artifact-id-required", "not_started"))

        result_dir = root / "native-result"
        result_dir.mkdir()
        for name in ("plan.json", "state.json", "result.json"):
            shutil.copyfile(ready_dir / name, result_dir / name)
        shutil.copyfile(ready_dir / "release-notes-evidence.json",
                        result_dir / "release-notes-evidence.json")
        ready_id, result_id, run_id, attempt = 701, 702, 600, 1
        ready_name = f"lwc-ready-development-{run_id}-{attempt}"
        result_name = f"lwc-result-development-{run_id}-{attempt}"
        ready_digest = "sha256:" + "d" * 64
        result_digest = "sha256:" + "e" * 64
        metadata = {"id": ready_id, "name": ready_name, "expired": False,
                    "digest": ready_digest, "expires_at": "2027-02-12T00:00:00Z",
                    "workflow_run": {"id": run_id}}
        run = {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
               "path": ".github/workflows/deploy-dev.yml", "status": "completed",
               "conclusion": "failure", "head_branch": "develop", "head_sha": producer_sha}
        artifact_list = {"artifacts": [{
            "id": result_id, "name": result_name, "expired": False,
            "digest": result_digest, "expires_at": "2027-02-12T00:00:00Z",
            "workflow_run": {"id": run_id},
        }]}
        return {"plan": plan, "plan_bytes": plan_bytes, "state_bytes": state_bytes,
                "result_bytes": result_bytes, "metadata": metadata, "run": run,
                "artifact_list": artifact_list,
                "result_metadata": artifact_list["artifacts"][0],
                "ready_id": str(ready_id), "result_id": str(result_id),
                "ready_dir": ready_dir, "result_dir": result_dir}

    def test_ready_plan_workflow_shell_token_transport_and_native_same_plan_resume(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_native_ready_handoff(root)
            workflow = yaml.safe_load((ROOT / ".github/workflows/prepare-release-notes-package.yml").read_text())
            job = workflow["jobs"]["package"]
            step = next(item for item in job["steps"] if item.get("id") == "ready-plan")
            self.assertEqual(job["permissions"], {"contents": "read", "actions": "write"})
            self.assertEqual(step["env"]["GH_TOKEN"], "${{ github.token }}")

            runner = root / "runner"
            runner.mkdir()
            ready_plan_dir = runner / "ready-plan"
            result_dir = runner / "ready-result"
            shutil.copytree(fixture["ready_dir"], ready_plan_dir)
            shutil.copytree(fixture["result_dir"], result_dir)
            (runner / "ready-artifact-metadata.json").write_bytes(canonical(fixture["metadata"]))
            (runner / "ready-workflow-run.json").write_bytes(canonical(fixture["run"]))
            (runner / "ready-run-artifacts.json").write_bytes(canonical(fixture["artifact_list"]))
            github_output = runner / "github-output"
            github_output.write_text("")

            expected_path = f"/repos/test/repo/actions/artifacts/{fixture['result_id']}"
            expected_token = "token synthetic-github-token"

            class MetadataHandler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    self.server.requests.append((self.path, self.headers.get("Authorization")))
                    if self.path != expected_path or self.headers.get("Authorization") != expected_token:
                        self.send_error(403)
                        return
                    payload = canonical(fixture["result_metadata"])
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)

                def log_message(self, *_args):
                    pass

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MetadataHandler)
            server.requests = []
            server_thread = threading.Thread(target=server.serve_forever, daemon=True)
            server_thread.start()
            self.addCleanup(server_thread.join)
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)

            fake_bin = root / "bin"
            fake_bin.mkdir()
            gh = fake_bin / "gh"
            gh.write_text(textwrap.dedent("""\
                #!/usr/bin/env python3
                import os, sys, urllib.request
                token = os.environ.get("GH_TOKEN", "")
                if not token:
                    sys.stderr.write("To use GitHub CLI in a GitHub Actions workflow, set the GH_TOKEN environment variable\\n")
                    raise SystemExit(4)
                if sys.argv[1:] != ["api", os.environ["EXPECTED_ENDPOINT"]]:
                    raise SystemExit(2)
                request = urllib.request.Request(
                    os.environ["CONTROLLED_API_BASE"] + "/" + sys.argv[2],
                    headers={"Authorization": "token " + token, "Accept": "application/vnd.github+json"})
                with urllib.request.urlopen(request, timeout=5) as response:
                    sys.stdout.buffer.write(response.read())
                """))
            gh.chmod(0o755)

            expression_values = {
                "${{ github.token }}": "synthetic-github-token",
                "${{ inputs.ready_artifact_id }}": fixture["ready_id"],
                "${{ steps.ready.outputs.result_artifact_id }}": fixture["result_id"],
            }
            effective_env = {
                "PATH": str(fake_bin) + os.pathsep + os.environ.get("PATH", ""),
                "HOME": str(root / "home"), "GH_CONFIG_DIR": str(root / "gh-config"),
                "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "test/repo",
                "RUNNER_TEMP": str(runner), "GITHUB_OUTPUT": str(github_output),
                "EXPECTED_ENDPOINT": f"repos/test/repo/actions/artifacts/{fixture['result_id']}",
                "CONTROLLED_API_BASE": f"http://127.0.0.1:{server.server_port}",
                "GH_NO_UPDATE_NOTIFIER": "1", "GH_PROMPT_DISABLED": "1",
                "HTTP_PROXY": "http://127.0.0.1:9", "HTTPS_PROXY": "http://127.0.0.1:9",
                "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
            }
            for name, value in step["env"].items():
                self.assertIn(value, expression_values, name)
                effective_env[name] = expression_values[value]
            self.assertNotIn("GITHUB_TOKEN", effective_env)
            completed = subprocess.run(["bash", "-c", step["run"]], cwd=ROOT,
                                       env=effective_env, text=True, capture_output=True, check=False)
            self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
            self.assertEqual(server.requests, [(expected_path, expected_token)])
            self.assertIn(f"resume_artifact_id={fixture['ready_id']}\n", github_output.read_text())

            # Verify a synthetic package receipt and resume the exact retained Ready ID.
            package_dir = root / "package"
            package_dir.mkdir()
            body = note("1.2.3", "en", "Local synthetic package receipt fixture")
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3",
                "environment": "dev", "release_plan_sha256": hashlib.sha256(fixture["plan_bytes"]).hexdigest(),
                "release_note_snapshot_ref": "LWC-A-62:test-only",
                "release_note_snapshot_sha256": "sha256:" + "f" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
                "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
            }
            (package_dir / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package_dir / "en.json").write_bytes(body)
            raw_digest = "f" * 64
            upload_metadata = {
                "id": 803, "name": "lwc-release-notes-1.2.3-development-604-1", "expired": False,
                "digest": "sha256:" + raw_digest, "expires_at": "2027-02-12T00:00:00Z",
                "workflow_run": {"id": 604},
            }
            package_receipt = release_notes_workflow.verify_uploaded_package(
                package_dir, ready_plan_dir / "plan.json", upload_metadata,
                expected_artifact_id="803", expected_name=upload_metadata["name"],
                action_digest=raw_digest, run_id="604", run_attempt="1",
                ready_artifact_id=fixture["ready_id"], output_path=str(runner / "package-receipt"))
            resume_inputs = package_receipt["resume_inputs"]
            self.assertEqual(resume_inputs["artifact_id"], fixture["ready_id"])
            self.assertEqual(resume_inputs["release_notes_artifact_id"], "803")

            resumed_plan = dict(fixture["plan"], executor_sha="c" * 40)
            resumed_plan["id"] = engine.plan_id(resumed_plan)
            resumed_dir = root / "resumed"
            resumed_dir.mkdir()
            engine.write(resumed_dir / "plan.json", resumed_plan)
            resumed = engine.Engine(resumed_dir, executor_sha=resumed_plan["executor_sha"])
            with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}, clear=False), \
                    patch.object(engine.Providers, "usable", lambda _provider, _component, _artifact: None):
                ready_artifacts = {fixture["ready_id"]: fixture["ready_dir"]}
                resumed.prepare(reuse=str(ready_artifacts[resume_inputs["artifact_id"]]))
                resumed.result()
            self.assertEqual((resumed_dir / "plan.json").read_bytes(), fixture["plan_bytes"])
            self.assertEqual(resumed.state["status"], "ready")
            self.assertEqual(resumed.state["components"], {})

    def test_canonical_plan_selection_distinguishes_bff_frontend_worker_and_full_input(self):
        self.assertTrue(release_notes_workflow.bff_selected_from_plan(self.plan_bytes))
        self.assertFalse(release_notes_workflow.bff_selected_from_plan(canonical({"selected": ["frontend"]})))
        self.assertFalse(release_notes_workflow.bff_selected_from_plan(canonical({"selected": ["worker"]})))
        canonical_all_selected = canonical({"selected": ["documentimport", "exportjob", "auth", "bff", "worker", "frontend"]})
        self.assertTrue(release_notes_workflow.bff_selected_from_plan(canonical_all_selected))
        for selected in ("bff", ["frontend", "frontend"], ["worker", 3]):
            with self.subTest(selected=selected), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.bff_selected_from_plan(canonical({"selected": selected}))
        for selected in ([], ["frontend", "bff"], ["unknown"]):
            with self.subTest(selected=selected), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.bff_selected_from_plan(canonical({"selected": selected}))

    def test_selection_output_comes_from_plan_not_raw_component_input(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            plan_path = self.make_plan_file(root)
            output = root / "github-output"
            self.assertTrue(release_notes_workflow.write_bff_selection(plan_path, str(output)))
            self.assertEqual(output.read_text(), "bff_selected=true\n")
            plan_path.write_bytes(canonical({"selected": ["frontend"]}))
            self.assertFalse(release_notes_workflow.write_bff_selection(plan_path, str(output)))
            self.assertEqual(output.read_text(), "bff_selected=true\nbff_selected=false\n")

    def test_private_package_archive_roundtrips_through_existing_validator(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = self.make_package(root / "source")
            archive = release_notes_workflow.package_directory_archive(source)
            output = root / "unpacked"
            result = release_notes_workflow.prepare_package(
                base64.b64encode(archive).decode("ascii"), self.make_plan_file(root), output,
                str(root / "github-output"), "1" * 32)
            product_version, files, manifest = release_notes.validate_package(
                output, expected_environment="dev", expected_artifact_digest=None,
                artifact_metadata_digest=None, plan_bytes=self.plan_bytes)
            self.assertEqual(product_version, "1.2.3")
            self.assertEqual(files, {"en": self.body})
            self.assertEqual(manifest, self.manifest)
            self.assertEqual(result["product_version"], "1.2.3")
            self.assertEqual((root / "github-output").read_text(), "product_version=1.2.3\n")
            self.assertEqual((output / "en.json").stat().st_mode & 0o777, 0o600)

    def test_private_package_event_payload_is_not_runner_env_argv_or_output(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            archive_b64 = base64.b64encode(release_notes_workflow.package_directory_archive(package)).decode("ascii")
            request_id = "1" * 32
            event_path = root / "event.json"
            event_path.write_bytes(canonical({"inputs": {
                "package_zip_base64": archive_b64,
                "request_id": request_id,
                "ready_artifact_id": "701",
                "retention_days": "90",
            }}))
            plan_path = self.make_plan_file(root)
            destination = root / "staged-package"
            output = root / "github-output"
            environment = os.environ.copy()
            environment["GITHUB_OUTPUT"] = str(output)
            environment.pop("LWC_RELEASE_NOTES_PACKAGE_B64", None)
            completed = subprocess.run([
                sys.executable, str(ROOT / "deploy/engine/release_notes_workflow.py"),
                "prepare-package", "--plan", str(plan_path), "--package-dir", str(destination),
                "--environment", "dev", "--request-id", request_id, "--event-path", str(event_path),
            ], cwd=ROOT, env=environment, text=True, capture_output=True, check=False)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertNotIn(archive_b64, completed.stdout + completed.stderr)
            self.assertNotIn(self.body.decode(), completed.stdout + completed.stderr)
            self.assertEqual((destination / "en.json").read_bytes(), self.body)
            self.assertEqual(output.read_text(), "product_version=1.2.3\n")

    def test_manual_request_correlation_is_closed_and_invalid_values_are_not_staged(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            archive = release_notes_workflow.package_directory_archive(package)
            destination = root / "invalid-request"
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                        "dispatch-request-id-invalid"):
                release_notes_workflow.prepare_package(
                    base64.b64encode(archive).decode("ascii"), self.make_plan_file(root), destination,
                    str(root / "github-output"), "bad request id")
            self.assertFalse(destination.exists())

    def test_package_preupload_validation_still_requires_exact_plan_and_manifest_hashes(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            wrong_plan = canonical({**self.plan, "source": "b" * 40})
            path = self.make_plan_file(root, wrong_plan)
            with self.assertRaisesRegex(release_notes.ReleaseNotesError, "invalid-package-manifest"):
                release_notes_workflow._validate_package_from_plan(package, path)
            with self.assertRaisesRegex(release_notes.ReleaseNotesError, "artifact-digest-mismatch"):
                release_notes.validate_package(package, expected_environment="dev",
                    expected_artifact_digest="sha256:" + "a" * 64, artifact_metadata_digest=None,
                    plan_bytes=self.plan_bytes)

    def test_package_input_rejects_path_traversal_duplicate_and_oversize(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            bad_archives = []
            for entries in (
                [("release-notes-manifest.json", self.manifest_bytes), ("../en.json", self.body)],
                [("release-notes-manifest.json", self.manifest_bytes), ("en.json", self.body), ("en.json", self.body)],
            ):
                output = BytesIO()
                with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
                    for name, data in entries:
                        archive.writestr(name, data)
                bad_archives.append(output.getvalue())
            for index, payload in enumerate(bad_archives):
                destination = root / f"bad-{index}"
                with self.subTest(index=index), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow._extract_package_archive(payload, destination)
            with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.prepare_package(
                    "A" * (release_notes_workflow.MAX_DISPATCH_BASE64_CHARS + 1),
                    self.make_plan_file(root), root / "too-large", "", "1" * 32)

    def test_failed_ready_is_qualified_only_by_exact_missing_package_final_result_and_resume_identity(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_ready_handoff(root)
            discovered = release_notes_workflow.find_ready_result_artifact(
                fixture["metadata"], fixture["run"], fixture["artifact_list"], fixture["ready_id"])
            self.assertEqual(discovered["result_artifact_id"], fixture["result_id"])
            output = root / "github-output"
            accepted = release_notes_workflow.validate_ready_handoff(
                fixture["metadata"], fixture["run"], fixture["artifact_list"], fixture["result_metadata"],
                expected_artifact_id=fixture["ready_id"],
                expected_result_artifact_id=fixture["result_id"],
                ready_directory=fixture["ready_dir"], result_directory=fixture["result_dir"],
                output_path=str(output))
            self.assertEqual(accepted["plan_sha256"], hashlib.sha256(fixture["plan_bytes"]).hexdigest())
            self.assertEqual(accepted["resume_inputs"], {
                "operation": "release", "source_sha": fixture["plan"]["source"],
                "release_tag": fixture["plan"]["tag"], "components": "bff,frontend",
                "artifact_id": fixture["ready_id"], "release_source_mode": "legacy_monorepo",
                "auth_schema_version": "4", "dev_artifact_id": "",
            })
            self.assertIn(f"resume_artifact_id={fixture['ready_id']}\n", output.read_text())

            for name, mutate in (
                ("successful aggregate", lambda value: value["run"].update(conclusion="success")),
                ("non-workflow producer", lambda value: value["run"].update(path=".github/workflows/recover-deployment.yml")),
                ("different result artifact", lambda value: value["artifact_list"]["artifacts"][0].update(name="lwc-result-development-600-2")),
                ("result and Ready plan diverge", lambda value: (value["result_dir"] / "plan.json").write_bytes(canonical({**value["plan"], "source": "f" * 40}))),
                ("runtime already started", lambda value: (value["result_dir"] / "state.json").write_bytes(canonical({
                    "plan": value["plan"]["id"], "status": "runtime_success", "components": {"bff": {"status": "verified"}},
                    "sequence": 6, "checkpoint_schema": 1, "builds": {}, "executor_sha": value["plan"]["executor_sha"],
                }))),
                ("notes failure has another cause", lambda value: (value["result_dir"] / "release-notes-evidence.json").write_bytes(canonical({
                    "schema_version": 1, "stages": [{"action": "prepare", "environment": "dev", "stage": "validate",
                        "status": "failed", "safe_class": "artifact-metadata-invalid", "product_status": "not_started",
                        "plan_sha256": hashlib.sha256(value["plan_bytes"]).hexdigest(), "artifact_id": None,
                        "next_actions": ["retry", "skip", "stop"], "owner_choice_required": True, "automatic_skip": False,
                        "event_sequence": 1, "stage_attempt": 1, "recorded_at": "2026-11-14T17:00:00Z"}]
                }))),
            ):
                with self.subTest(name=name):
                    case_root = root / name.replace(" ", "-")
                    case_root.mkdir()
                    fresh = self.make_ready_handoff(case_root)
                    mutate(fresh)
                    with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                        release_notes_workflow.validate_ready_handoff(
                            fresh["metadata"], fresh["run"], fresh["artifact_list"], fresh["result_metadata"],
                            expected_artifact_id=fresh["ready_id"],
                            expected_result_artifact_id=fresh["result_id"],
                            ready_directory=fresh["ready_dir"], result_directory=fresh["result_dir"], output_path=None)
            for key, value in (("head_branch", "main"), ("status", "in_progress"),
                               ("head_sha", "invalid")):
                case_root = root / f"bad-run-{key}"
                case_root.mkdir()
                fresh = self.make_ready_handoff(case_root)
                fresh["run"][key] = value
                with self.subTest(key=key), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.find_ready_result_artifact(
                        fresh["metadata"], fresh["run"], fresh["artifact_list"], fresh["ready_id"])
            case_root = root / "bad-metadata"
            case_root.mkdir()
            invalid = self.make_ready_handoff(case_root)
            invalid["metadata"]["id"] = True
            with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.find_ready_result_artifact(
                    invalid["metadata"], invalid["run"], invalid["artifact_list"], invalid["ready_id"])

    def test_upload_verification_binds_action_digest_api_metadata_name_and_source_plan(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            plan_path = self.make_plan_file(root)
            action_digest = "c" * 64
            artifact_digest = "sha256:" + action_digest
            metadata = {
                "id": 801,
                "name": "lwc-release-notes-1.2.3-development-802-1",
                "digest": artifact_digest,
                "expired": False,
                "expires_at": "2026-12-14T00:00:00Z",
                "workflow_run": {"id": 802},
            }
            output = root / "github-output"
            verified = release_notes_workflow.verify_uploaded_package(
                package, plan_path, metadata, expected_artifact_id="801", expected_name=metadata["name"],
                action_digest=action_digest, run_id="802", run_attempt="1", ready_artifact_id="701",
                output_path=str(output))
            self.assertEqual(verified["artifact_id"], "801")
            self.assertEqual(verified["ready_artifact_id"], "701")
            self.assertEqual(verified["artifact_digest"], artifact_digest)
            self.assertEqual(verified["action_digest_raw"], action_digest)
            self.assertEqual(verified["resume_inputs"]["artifact_id"], "701")
            self.assertEqual(verified["resume_inputs"]["release_notes_artifact_id"], "801")
            self.assertEqual(verified["resume_inputs"]["release_notes_action"], "run")
            self.assertNotIn("Private package payload sentinel", json.dumps(verified))
            self.assertIn("artifact_digest=" + artifact_digest, output.read_text())
            with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.verify_uploaded_package(
                    package, plan_path, metadata, expected_artifact_id="801", expected_name=metadata["name"],
                    action_digest="d" * 64, run_id="802", run_attempt="1",
                    ready_artifact_id="701", output_path=str(output))
            for invalid in ("sha256:" + action_digest, "", "C" * 64):
                with self.subTest(invalid=invalid[:8]), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.verify_uploaded_package(
                        package, plan_path, metadata, expected_artifact_id="801", expected_name=metadata["name"],
                        action_digest=invalid, run_id="802", run_attempt="1",
                        ready_artifact_id="701", output_path=str(output))

    def test_dispatch_payload_contains_private_archive_without_exposing_content(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            body, version = release_notes_workflow.build_dispatch_payload(
                package, ready_artifact_id="701", retention_days=90, request_id="1" * 32)
            serialized = json.dumps(body)
            self.assertEqual(body["ref"], "develop")
            self.assertEqual(version, "1.2.3")
            self.assertNotIn("Private package payload sentinel", serialized)
            self.assertLessEqual(len(serialized.encode()), 65535)
            self.assertEqual(body["inputs"]["request_id"], "1" * 32)
            self.assertEqual(body["inputs"]["retention_days"], "90")

    def test_parent_dispatch_posts_private_input_via_gh_stdin_and_returns_exact_artifact_receipt(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = self.make_package(root / "package")
            artifact_digest = "sha256:" + "b" * 64
            expected_name = "lwc-release-notes-1.2.3-development-802-1"
            responses = [
                None,
                {"workflow_runs": [{
                    "id": 802, "run_attempt": 1, "display_title": "Prepare Release Notes package " + "a" * 32,
                    "path": ".github/workflows/prepare-release-notes-package.yml@refs/heads/develop", "event": "workflow_dispatch",
                    "head_branch": "develop", "head_sha": "c" * 40, "status": "completed",
                    "conclusion": "success", "html_url": "https://github.test/run/802",
                }]},
                {"artifacts": [{"id": 801, "name": expected_name}]},
                {"id": 801, "name": expected_name, "digest": artifact_digest, "expired": False,
                 "expires_at": "2026-12-14T00:00:00Z", "workflow_run": {"id": 802}},
            ]
            with patch.object(release_notes_workflow.uuid, "uuid4", return_value=type("RequestId", (), {"hex": "a" * 32})()), \
                 patch.object(release_notes_workflow, "_gh_api", side_effect=responses) as api:
                receipt = release_notes_workflow.dispatch_package(
                    "Rayer/llm-wiki-cloud", package, ready_artifact_id="701", retention_days=90)
            self.assertEqual(receipt["status"], "ready")
            self.assertEqual(receipt["artifact_id"], "801")
            self.assertEqual(receipt["artifact_name"], expected_name)
            self.assertEqual(receipt["artifact_digest"], artifact_digest)
            self.assertEqual(receipt["ready_artifact_id"], "701")
            self.assertEqual(receipt["requested_retention_days"], 90)
            self.assertNotIn("Private package payload sentinel", json.dumps(receipt))
            endpoint, = api.call_args_list[0].args[1:2]
            body = json.loads(api.call_args_list[0].kwargs["body"])
            self.assertEqual(endpoint, "actions/workflows/prepare-release-notes-package.yml/dispatches")
            self.assertEqual(body["ref"], "develop")
            self.assertEqual(body["inputs"]["request_id"], "a" * 32)
            self.assertEqual(api.call_args_list[0].kwargs["method"], "POST")
            self.assertEqual(body["inputs"]["retention_days"], "90")

    def test_gh_api_writer_uses_stdin_and_never_shells_package_json(self):
        package_request = b'{"ref":"develop","inputs":{"package_zip_base64":"private"}}'
        completed = type("Completed", (), {"returncode": 0, "stdout": b"", "stderr": b""})()
        with patch.object(release_notes_workflow.subprocess, "run", return_value=completed) as run:
            self.assertIsNone(release_notes_workflow._gh_api(
                "Rayer/llm-wiki-cloud", "actions/workflows/prepare-release-notes-package.yml/dispatches",
                method="POST", body=package_request))
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["gh", "api",
                                   "repos/Rayer/llm-wiki-cloud/actions/workflows/prepare-release-notes-package.yml/dispatches",
                                   "--method", "POST", "--input", "-"])
        self.assertEqual(kwargs["input"], package_request)
        self.assertIs(kwargs["capture_output"], True)
        self.assertIs(kwargs["check"], False)
        self.assertIs(kwargs.get("shell", False), False)


if __name__ == "__main__":
    unittest.main()
