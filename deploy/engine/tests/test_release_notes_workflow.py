import hashlib
import json
import os
import shutil
import sys
import subprocess
import textwrap
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
from unittest.mock import patch
import zipfile

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_notes
import release_notes_publish
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


if __name__ == "__main__":
    unittest.main()
