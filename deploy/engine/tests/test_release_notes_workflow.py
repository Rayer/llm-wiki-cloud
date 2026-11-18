import base64
import copy
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
from contextlib import redirect_stdout
from io import BytesIO, StringIO
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
import release_notes_capture
import engine
from test_release_notes import (MemoryNotesStore, canonical, note, operation_fixture, plan_fixture,
                                initial_schema2_migration_fixture, run_capture_cli_with_service_url_failure)


ROOT = Path(__file__).resolve().parents[3]
DOWNLOAD_ACTION = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
PINNED_DOWNLOAD_SOURCE = ROOT / "scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts"
ARTIFACT_SDK_BOUNDARY = ROOT / "deploy/engine/tests/lwc478_artifact_sdk_boundary.cjs"
ARTIFACT_SDK_NODE_MODULES = ROOT / "deploy/engine/tests/artifact-sdk/node_modules"
# Use the checked-out tree so source fixtures work in shallow PR merge checkouts.
IDENTITY_FIXTURE_SOURCE = "HEAD"
AUTH_SOURCE_FIXTURE = {
    "repository_id": "github:1439745097",
    "revision": "c9ff48fed905a10225805cd816376cf99b582cbf",
    "tree_sha256": "sha256:" + "f" * 64,
    "artifact_id": "13393843472",
    "run_id": 41980211267,
    "run_attempt": 1,
}
_SOURCE_IDENTITY_FIXTURES = {}


def producer_identity_fixture(component, auth_source=None):
    """Use engine.source_identity() with a bounded Git inventory for offline plans."""
    if auth_source is not None:
        return engine.source_identity(component, IDENTITY_FIXTURE_SOURCE, auth_source)
    if component in _SOURCE_IDENTITY_FIXTURES:
        return copy.deepcopy(_SOURCE_IDENTITY_FIXTURES[component])
    profile = engine.PROFILES[component]
    original_run = engine.run
    tracked = original_run(["git", "ls-tree", "-r", "--name-only", IDENTITY_FIXTURE_SOURCE]).splitlines()
    input_files = [path for path in tracked
                   if any(path == input_root or path.startswith(input_root.rstrip("/") + "/")
                          for input_root in profile["inputs"])]
    input_files = [path for path in input_files
                   if not (component == "frontend" and ("/tests/" in path or path.endswith("AGENTS.md")))]
    if not input_files:
        raise AssertionError(f"No tracked source identity input found for {component}")
    input_file = input_files[0]
    blob = original_run(["git", "rev-parse", f"{IDENTITY_FIXTURE_SOURCE}:{input_file}"])

    def bounded_run(args, **kwargs):
        if args[:2] == ["git", "ls-tree"]:
            return input_file
        if args[:2] == ["go", "list"]:
            return ""
        if args[:2] == ["git", "ls-files"]:
            return ""
        if args[:2] == ["git", "rev-parse"]:
            return blob
        if args[:2] == ["git", "hash-object"]:
            return blob
        raise AssertionError(f"Unexpected source_identity command: {args[:2]}")

    with patch.object(engine, "run", side_effect=bounded_run):
        value = engine.source_identity(component, IDENTITY_FIXTURE_SOURCE)
    _SOURCE_IDENTITY_FIXTURES[component] = copy.deepcopy(value)
    return value


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
            "identities": {"bff": producer_identity_fixture("bff"),
                           "frontend": producer_identity_fixture("frontend")},
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

    def make_package_for_plan(self, root, plan_bytes, *, environment, version, text):
        package_environment = "prod" if environment == "production" else "dev"
        body = note(version, "en", text)
        manifest = {
            "schema_version": 1,
            "product": "llm-wiki-cloud",
            "product_version": version,
            "environment": package_environment,
            "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
            "release_note_snapshot_ref": "LWC-A-62:test-only",
            "release_note_snapshot_sha256": "sha256:" + "f" * 64,
            "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
            "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
        }
        root.mkdir(parents=True)
        (root / "release-notes-manifest.json").write_bytes(canonical(manifest))
        (root / "en.json").write_bytes(body)
        return root, manifest, body

    def make_plan_file(self, root, plan_bytes=None):
        path = root / "plan.json"
        path.write_bytes(plan_bytes if plan_bytes is not None else self.plan_bytes)
        return path

    def make_ready_handoff(self, root, selected=("bff", "frontend"), schema=3,
                           environment="development", independent_auth=False):
        root.mkdir(parents=True, exist_ok=True)
        producer_sha = "b" * 40
        production = environment == "production"
        branch = "main" if production else "develop"
        package_environment = "prod" if production else "dev"
        auth_source = AUTH_SOURCE_FIXTURE if independent_auth else None
        plan = {
            "schema": schema,
            "tag": "v2026.11.5",
            "source": producer_sha if production else "a" * 40,
            "branch": branch,
            "executor_sha": producer_sha,
            "normalized": {"environment": environment, "auth": {"runtime_inputs": {"schema_version": 4}}},
            "identities": {component: producer_identity_fixture(
                component, auth_source if component == "auth" else None)
                           for component in selected},
            "dev_reference": "731" if production else None,
            "selected": list(selected),
        }
        if schema == 2:
            plan["engine"] = producer_sha
            plan["engine_content"] = "e" * 64
        else:
            plan["release_source_mode"] = "independent" if independent_auth else "legacy_monorepo"
        if auth_source is not None:
            plan["auth_source"] = dict(auth_source)
        if schema == 4:
            plan["release_version"] = "2026.11.4"
        plan["id"] = release_notes_workflow._plan_id(plan)
        plan_bytes = canonical(plan)
        state = {"plan": plan["id"], "status": "ready", "components": {}, "sequence": 5,
                 "checkpoint_schema": 1, "builds": {}, "executor_sha": producer_sha}
        state_bytes = canonical(state)
        result = {
            "release": plan["tag"], "attempt": plan["id"], "stage": "ready", "status": "ready",
            "component": selected[-1], "reason": "completed", "mutation_may_have_happened": False,
            "prior": None, "candidate": None,
            "expected": {"source": plan["source"], "identity": "c" * 64},
            "observed": {"component_status": "unstarted"}, "last_verified_checkpoint": 5,
        }
        result_bytes = canonical(result)
        notes_evidence = {
            "schema_version": 1,
            "stages": [{
                "schema_version": 1, "action": "prepare", "environment": package_environment, "stage": "validate",
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
        ready_name = f"lwc-ready-{environment}-{run_id}-{attempt}"
        result_name = f"lwc-result-{environment}-{run_id}-{attempt}"
        ready_digest = "sha256:" + "d" * 64
        result_digest = "sha256:" + "e" * 64
        metadata = {"id": ready_id, "name": ready_name, "expired": False, "digest": ready_digest,
                    "expires_at": "2027-02-12T00:00:00Z",
                    "workflow_run": {"id": run_id, "head_branch": branch, "head_sha": producer_sha}}
        run = {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
               "path": (".github/workflows/promote-production.yml" if production
                        else ".github/workflows/deploy-dev.yml"),
               "status": "completed", "conclusion": "failure",
               "head_branch": branch, "head_sha": producer_sha}
        artifact_list = {"artifacts": [{
            "id": result_id, "name": result_name, "expired": False, "digest": result_digest,
            "expires_at": "2027-02-12T00:00:00Z",
            "workflow_run": {"id": run_id, "head_branch": branch, "head_sha": producer_sha},
        }]}
        result_metadata = {**artifact_list["artifacts"][0]}
        return {"plan": plan, "plan_bytes": plan_bytes, "state_bytes": state_bytes,
                "result_bytes": result_bytes, "metadata": metadata, "run": run,
                "artifact_list": artifact_list, "result_metadata": result_metadata,
                "ready_id": str(ready_id), "result_id": str(result_id),
                "ready_dir": ready_dir, "result_dir": result_dir}

    def make_package_producer_run(self, run_id=604, attempt=1, environment="development",
                                  head_sha="c" * 40):
        destination = release_notes_workflow.DESTINATIONS[environment]
        return {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
                "path": destination["workflow_path"], "status": "in_progress",
                "head_branch": destination["branch"], "head_sha": head_sha}

    def rewrite_ready_plan(self, fixture, plan):
        plan = copy.deepcopy(plan)
        plan["id"] = release_notes_workflow._plan_id(plan)
        plan_bytes = canonical(plan)
        state = json.loads((fixture["ready_dir"] / "state.json").read_text())
        state["plan"] = plan["id"]
        state_bytes = canonical(state)
        result = json.loads((fixture["ready_dir"] / "result.json").read_text())
        result["attempt"] = plan["id"]
        result["expected"]["source"] = plan["source"]
        result_bytes = canonical(result)
        evidence = json.loads((fixture["result_dir"] / "release-notes-evidence.json").read_text())
        evidence["stages"][0]["plan_sha256"] = hashlib.sha256(plan_bytes).hexdigest()
        for directory in (fixture["ready_dir"], fixture["result_dir"]):
            (directory / "plan.json").write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
        (fixture["result_dir"] / "release-notes-evidence.json").write_bytes(canonical(evidence))
        fixture["plan"] = plan
        fixture["plan_bytes"] = plan_bytes

    def make_historical_result(self, root):
        source = "a" * 40
        identity = producer_identity_fixture("bff")
        candidate = {"revision": "revision-historical-fixture"}
        plan = {
            "schema": 3, "source": source, "executor_sha": source, "branch": "develop",
            "tag": "2026.11.1-dev.6", "normalized": {
                "environment": "development", "bff": {"service_name": "synthetic-bff"}},
            "identities": {"bff": identity}, "dev_reference": None, "selected": ["bff"],
        }
        plan["id"] = release_notes_workflow._plan_id(plan)
        plan_bytes = canonical(plan)
        state = {"plan": plan["id"], "status": "success", "sequence": 4,
                 "components": {"bff": {"status": "verified", "candidate": candidate}}}
        state_bytes = canonical(state)
        result = {
            "release": plan["tag"], "attempt": plan["id"], "stage": "success", "status": "success",
            "component": "bff", "expected": {"source": source,
                "identity": hashlib.sha256(canonical(identity)).hexdigest()},
            "observed": {"component_status": "verified"}, "candidate": candidate,
            "last_verified_checkpoint": state["sequence"],
        }
        result_bytes = canonical(result)
        receipt = {"component": "bff", "identity": identity,
                   "artifact": {"image": "registry.example/bff@sha256:" + "c" * 64}}
        receipt_bytes = canonical(receipt)
        result_id, run_id, attempt = 9901, 9902, 1
        head_sha = source
        artifact_name = f"lwc-result-development-{run_id}-{attempt}"
        metadata = {
            "id": result_id, "name": artifact_name, "expired": False,
            "digest": "sha256:" + "d" * 64, "expires_at": "2027-02-12T00:00:00Z",
            "workflow_run": {"id": run_id, "head_branch": "develop", "head_sha": head_sha},
        }
        run = {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
               "path": release_notes_workflow.WORKFLOW_PATH, "status": "completed",
               "conclusion": "success", "head_branch": "develop", "head_sha": head_sha}
        result_dir = root / "historical-result-fixture"
        (result_dir / "receipts").mkdir(parents=True)
        (result_dir / "plan.json").write_bytes(plan_bytes)
        (result_dir / "state.json").write_bytes(state_bytes)
        (result_dir / "result.json").write_bytes(result_bytes)
        (result_dir / "receipts" / "bff.json").write_bytes(receipt_bytes)
        return {
            "result_id": str(result_id), "run_id": str(run_id), "attempt": str(attempt),
            "head_sha": head_sha, "source_sha": source, "plan": plan, "plan_bytes": plan_bytes,
            "state_bytes": state_bytes, "result_bytes": result_bytes, "receipt_bytes": receipt_bytes,
            "metadata": metadata, "run": run, "result_dir": result_dir,
        }

    def make_native_ready_handoff(self, root):
        producer_sha = "b" * 40
        plan = {
            "schema": 3, "tag": "v2026.11.5", "source": "a" * 40,
            "branch": "develop", "executor_sha": producer_sha,
            "normalized": {"environment": "development", "gcp": {"project_id": "fixture-project"}},
            "identities": {"bff": producer_identity_fixture("bff")},
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
                    "workflow_run": {"id": run_id, "head_branch": "develop", "head_sha": producer_sha}}
        run = {"id": run_id, "run_attempt": attempt, "event": "workflow_dispatch",
               "path": ".github/workflows/deploy-dev.yml", "status": "completed",
               "conclusion": "failure", "head_branch": "develop", "head_sha": producer_sha}
        artifact_list = {"artifacts": [{
            "id": result_id, "name": result_name, "expired": False,
            "digest": result_digest, "expires_at": "2027-02-12T00:00:00Z",
            "workflow_run": {"id": run_id, "head_branch": "develop", "head_sha": producer_sha},
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
                "${{ steps.caller.outputs.environment }}": "development",
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
                "workflow_run": {"id": 604, "head_branch": "develop", "head_sha": "c" * 40},
            }
            package_receipt = release_notes_workflow.verify_uploaded_package(
                package_dir, ready_plan_dir / "plan.json", upload_metadata,
                expected_artifact_id="803", expected_name=upload_metadata["name"],
                action_digest=raw_digest, run=self.make_package_producer_run(), run_id="604", run_attempt="1",
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
            request_id = "1" * 32
            dispatch, _ = release_notes_workflow.build_dispatch_payload(
                package, ready_artifact_id="701", retention_days=90, request_id=request_id)
            archive_b64 = dispatch["inputs"]["package_zip_base64"]
            event_path = root / "event.json"
            event_path.write_bytes(canonical(dispatch))
            plan_path = self.make_plan_file(root)
            destination = root / "staged-package"
            output = root / "github-output"
            environment = os.environ.copy()
            environment["GITHUB_OUTPUT"] = str(output)
            environment.pop("LWC_RELEASE_NOTES_PACKAGE_B64", None)
            completed = subprocess.run([
                sys.executable, str(ROOT / "deploy/engine/release_notes_workflow.py"),
                "prepare-package", "--plan", str(plan_path), "--package-dir", str(destination),
                "--environment", "dev", "--request-id", request_id, "--ready-artifact-id", "701",
                "--retention-days", "90",
                "--event-path", str(event_path),
            ], cwd=ROOT, env=environment, text=True, capture_output=True, check=False)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertNotIn(archive_b64, completed.stdout + completed.stderr)
            self.assertNotIn(self.body.decode(), completed.stdout + completed.stderr)
            self.assertEqual((destination / "en.json").read_bytes(), self.body)
            self.assertEqual(output.read_text(), "product_version=1.2.3\n")

            event = json.loads(event_path.read_text())
            for field, value in (("prepare_release_notes_package", False),
                                 ("prepare_release_notes_package", 1),
                                 ("operation", "deploy"), ("release_notes_action", "run"),
                                 ("release_notes_package_request_id", "2" * 32), ("artifact_id", "702"),
                                 ("release_notes_package_retention_days", "90"),
                                 ("release_notes_package_retention_days", "30")):
                with self.subTest(field=field):
                    bad_path = root / f"bad-{field}.json"
                    bad_event = {"inputs": {**event["inputs"], field: value}}
                    bad_path.write_bytes(canonical(bad_event))
                    with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                                "workflow-event-invalid"):
                        release_notes_workflow._read_package_input_from_event(bad_path, request_id, "701", "90")

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
                "release_tag": fixture["plan"]["tag"], "release_version": "", "components": "bff,frontend",
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

    def test_production_current_package_uses_authenticated_main_wrapper_and_native_plan_contract(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_ready_handoff(root, schema=4, environment="production")
            ready_output = root / "production-ready-output"
            ready = release_notes_workflow.validate_ready_handoff(
                fixture["metadata"], fixture["run"], fixture["artifact_list"],
                fixture["result_metadata"], expected_artifact_id=fixture["ready_id"],
                expected_result_artifact_id=fixture["result_id"],
                ready_directory=fixture["ready_dir"], result_directory=fixture["result_dir"],
                output_path=str(ready_output), environment="production")
            self.assertEqual(ready["environment"], "production")
            self.assertEqual(ready["release_tag"], fixture["plan"]["tag"])
            self.assertNotIn("source_sha", ready)
            self.assertNotIn("source_sha", ready["resume_inputs"])

            package, manifest, body = self.make_package_for_plan(
                root / "production-package", fixture["plan_bytes"], environment="production",
                version="2026.11.4", text="Production-only payload sentinel")
            authored_manifest = (package / "release-notes-manifest.json").read_bytes()
            request_id = "9" * 32
            dispatch, version = release_notes_workflow.build_dispatch_payload(
                package, ready_artifact_id=fixture["ready_id"], retention_days=90,
                environment="production", request_id=request_id)
            self.assertEqual(version, "2026.11.4")
            self.assertEqual(dispatch["ref"], "main")
            self.assertEqual(dispatch["inputs"]["prepare_release_notes_package"], "true")
            self.assertEqual(dispatch["inputs"]["artifact_id"], fixture["ready_id"])
            self.assertEqual(dispatch["inputs"]["release_notes_package_request_id"], request_id)
            with zipfile.ZipFile(BytesIO(base64.b64decode(
                    dispatch["inputs"]["package_zip_base64"], validate=True))) as archive:
                self.assertIn("Production-only payload sentinel", archive.read("en.json").decode())

            event_path = root / "original-production-event.json"
            event_path.write_bytes(canonical(dispatch))
            staged = root / "staged-production-package"
            producer_output = root / "production-package-output"
            staged_result = release_notes_workflow.prepare_package_from_event(
                event_path, fixture["ready_dir"] / "plan.json", staged, str(producer_output),
                request_id, fixture["ready_id"], "90", environment="production")
            self.assertEqual(staged_result["product_version"], "2026.11.4")
            self.assertEqual((staged / "release-notes-manifest.json").read_bytes(), authored_manifest)
            self.assertEqual((staged / "en.json").read_bytes(), body)
            self.assertNotIn("Production-only payload sentinel", producer_output.read_text())

            raw_digest = "d" * 64
            package_run = self.make_package_producer_run(604, 2, "production")
            package_name = "lwc-release-notes-2026.11.4-production-604-2"
            upload_metadata = {
                "id": 803, "name": package_name, "digest": "sha256:" + raw_digest,
                "expired": False, "expires_at": "2027-02-12T00:00:00Z",
                "workflow_run": {"id": 604, "head_branch": "main", "head_sha": "c" * 40},
            }
            uploaded = release_notes_workflow.verify_uploaded_package(
                staged, fixture["ready_dir"] / "plan.json", upload_metadata,
                expected_artifact_id="803", expected_name=package_name, action_digest=raw_digest,
                run=package_run, run_id="604", run_attempt="2", ready_artifact_id=fixture["ready_id"],
                environment="production", output_path=str(root / "production-upload-output"))
            resume_inputs = uploaded["resume_inputs"]
            self.assertEqual(uploaded["environment"], "prod")
            self.assertEqual(resume_inputs, {
                "operation": "release", "release_tag": fixture["plan"]["tag"],
                "release_version": "2026.11.4", "components": "bff,frontend",
                "artifact_id": fixture["ready_id"], "release_source_mode": "legacy_monorepo",
                "auth_schema_version": "4", "dev_artifact_id": "731",
                "release_notes_artifact_id": "803", "release_notes_action": "run",
            })
            self.assertTrue(all(type(value) is str for value in resume_inputs.values()))
            self.assertFalse(any(key in resume_inputs for key in (
                "source_sha", "force", "auth_repository_id", "auth_revision", "auth_tree_sha256",
                "auth_artifact_id", "auth_run_id", "auth_run_attempt")))
            self.assertTrue(all(type(value) is str for value in dispatch["inputs"].values()))
            self.assertNotIn("Production-only payload sentinel", json.dumps(uploaded))

            successful_run = {**package_run, "status": "completed", "conclusion": "success"}
            consumer_receipt = release_notes_workflow.validate_current_package_artifact(
                upload_metadata, successful_run, expected_artifact_id="803", environment="production",
                output_path=str(root / "production-consumer-output"))
            self.assertEqual(consumer_receipt["package_run_id"], "604")
            self.assertEqual(consumer_receipt["package_run_attempt"], "2")
            self.assertEqual(consumer_receipt["head_sha"], "c" * 40)

            product_version, files, native_manifest = release_notes.validate_package(
                staged, expected_environment="prod", expected_artifact_digest="sha256:" + raw_digest,
                artifact_metadata_digest="sha256:" + raw_digest, plan_bytes=fixture["plan_bytes"])
            self.assertEqual(product_version, "2026.11.4")
            self.assertEqual(files, {"en": body})
            self.assertEqual(native_manifest, manifest)

            for label, mutate in (
                ("cross-environment artifact", lambda m, r: m.update(name="lwc-release-notes-2026.11.4-development-604-2")),
                ("wrong workflow", lambda m, r: r.update(path=".github/workflows/deploy-dev.yml")),
                ("wrong ref", lambda m, r: r.update(head_branch="develop")),
                ("wrong head", lambda m, r: r.update(head_sha="e" * 40)),
                ("wrong attempt", lambda m, r: r.update(run_attempt=3)),
                ("failed producer", lambda m, r: r.update(conclusion="failure")),
            ):
                invalid_metadata = copy.deepcopy(upload_metadata)
                invalid_run = copy.deepcopy(successful_run)
                mutate(invalid_metadata, invalid_run)
                with self.subTest(label=label), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.validate_current_package_artifact(
                        invalid_metadata, invalid_run, expected_artifact_id="803", environment="production")

            wrong_manifest = root / "wrong-production-package"
            wrong_manifest.mkdir()
            wrong_value = {**manifest, "release_plan_sha256": "f" * 64}
            (wrong_manifest / "release-notes-manifest.json").write_bytes(canonical(wrong_value))
            (wrong_manifest / "en.json").write_bytes(body)
            with self.assertRaises(release_notes.ReleaseNotesError):
                release_notes.validate_package(
                    wrong_manifest, expected_environment="prod", expected_artifact_digest=None,
                    artifact_metadata_digest=None, plan_bytes=fixture["plan_bytes"])

            with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.resolve_caller_destination(
                    "Rayer/llm-wiki-cloud",
                    "Rayer/llm-wiki-cloud/.github/workflows/promote-production.yml@refs/heads/main",
                    "refs/heads/develop")

    def test_production_history_and_wrong_ready_identity_are_rejected(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            def source_mismatch(value):
                self.rewrite_ready_plan(value, {**value["plan"], "source": "f" * 40})

            def runtime_mutation(value):
                for directory in (value["ready_dir"], value["result_dir"]):
                    state_path = directory / "state.json"
                    state = json.loads(state_path.read_text())
                    state.update(status="runtime_success", components={"bff": {"status": "verified"}})
                    state_path.write_bytes(canonical(state))

            for label, mutate in (
                ("development Ready name", lambda value: value["metadata"].update(
                    name="lwc-ready-development-600-1")),
                ("development wrapper", lambda value: value["run"].update(
                    path=".github/workflows/deploy-dev.yml", head_branch="develop")),
                ("wrong attempt", lambda value: value["run"].update(run_attempt=2)),
                ("wrong producer head", lambda value: value["metadata"]["workflow_run"].update(
                    head_sha="e" * 40)),
                ("Production plan source mismatch", source_mismatch),
                ("runtime mutated checkpoint", runtime_mutation),
            ):
                case_root = root / label.replace(" ", "-")
                case_root.mkdir()
                fresh = self.make_ready_handoff(case_root, schema=4, environment="production")
                mutate(fresh)
                with self.subTest(label=label), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.validate_ready_handoff(
                        fresh["metadata"], fresh["run"], fresh["artifact_list"], fresh["result_metadata"],
                        expected_artifact_id=fresh["ready_id"], expected_result_artifact_id=fresh["result_id"],
                        ready_directory=fresh["ready_dir"], result_directory=fresh["result_dir"],
                        output_path=None, environment="production")

            history_root = root / "history-source"
            history_root.mkdir()
            fixture = self.make_ready_handoff(history_root, schema=4, environment="production")
            history_package = self.make_package(root / "history-package")
            with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                release_notes_workflow.build_dispatch_payload(
                    history_package, history_result_artifact_id="9901", history_operation="deploy",
                    retention_days=90, environment="production")

    def test_ready_package_producer_accepts_frontend_and_worker_only_schema4_plans(self):
        for selected in (("frontend",), ("worker",)):
            with self.subTest(selected=selected), TemporaryDirectory() as temporary:
                root = Path(temporary)
                fixture = self.make_ready_handoff(root, selected=selected, schema=4)
                output = root / "github-output"
                resume = release_notes_workflow.validate_ready_handoff(
                    fixture["metadata"], fixture["run"], fixture["artifact_list"], fixture["result_metadata"],
                    expected_artifact_id=fixture["ready_id"],
                    expected_result_artifact_id=fixture["result_id"],
                    ready_directory=fixture["ready_dir"], result_directory=fixture["result_dir"],
                    output_path=str(output))
                self.assertTrue(output.is_file())
                self.assertEqual(resume["components"], selected[0])
                self.assertEqual(resume["resume_inputs"]["components"], selected[0])
                self.assertEqual(resume["resume_inputs"]["release_version"], "2026.11.4")
                self.assertEqual(resume["resume_inputs"]["artifact_id"], fixture["ready_id"])

                package = root / "package"
                package.mkdir()
                body = note("2026.11.4", "en", f"Synthetic {selected[0]} Ready package")
                manifest = {
                    "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "2026.11.4",
                    "environment": "dev",
                    "release_plan_sha256": hashlib.sha256(fixture["plan_bytes"]).hexdigest(),
                    "release_note_snapshot_ref": "LWC-A-62:test-only",
                    "release_note_snapshot_sha256": "sha256:" + "f" * 64,
                    "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
                    "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
                }
                (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
                (package / "en.json").write_bytes(body)
                action_digest = "a" * 64
                metadata = {
                    "id": 803, "name": "lwc-release-notes-2026.11.4-development-604-1",
                    "digest": "sha256:" + action_digest, "expired": False,
                    "expires_at": "2027-02-12T00:00:00Z", "workflow_run": {"id": 604, "head_branch": "develop", "head_sha": "c" * 40},
                }
                receipt_output = root / f"package-output-{selected[0]}"
                receipt = release_notes_workflow.verify_uploaded_package(
                    package, fixture["ready_dir"] / "plan.json", metadata,
                    expected_artifact_id="803", expected_name=metadata["name"],
                    action_digest=action_digest, run=self.make_package_producer_run(), run_id="604", run_attempt="1",
                    ready_artifact_id=fixture["ready_id"], output_path=str(receipt_output))
                self.assertEqual(receipt["ready_artifact_id"], fixture["ready_id"])
                self.assertTrue(receipt_output.is_file())

                # Only the canonical selected identities may cross the Ready boundary.
                widened = dict(fixture["plan"])
                widened["identities"] = {**fixture["plan"]["identities"],
                                          "bff": {"profile": "unselected-bff", "digest": "not-selected"}}
                with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                            "release-plan-not-bff-dev"):
                    release_notes_workflow._validate_development_plan(canonical(widened))
                self.assertEqual(receipt["resume_inputs"]["components"], selected[0])
                self.assertEqual(receipt["resume_inputs"]["release_notes_artifact_id"], "803")

                invalid_plan = dict(fixture["plan"], identities={})
                with self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow._validate_development_plan(canonical(invalid_plan))

    def test_ready_and_package_readers_accept_only_producer_identity_shapes(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            legacy_directory = root / "legacy"
            legacy_directory.mkdir()
            for schema in (2, 3, 4):
                with self.subTest(schema=schema):
                    fixture = self.make_ready_handoff(
                        legacy_directory / str(schema), selected=("bff",), schema=schema)
                    accepted = release_notes_workflow._validate_development_plan(fixture["plan_bytes"])
                    self.assertEqual(accepted["identities"]["bff"],
                                     producer_identity_fixture("bff"))

            auth_root = root / "auth-independent"
            auth_root.mkdir()
            fixture = self.make_ready_handoff(
                auth_root, selected=("auth",), schema=4, independent_auth=True)
            plan = fixture["plan"]
            expected_auth_identity = engine.source_identity(
                "auth", IDENTITY_FIXTURE_SOURCE, AUTH_SOURCE_FIXTURE)
            self.assertEqual(plan["identities"]["auth"], expected_auth_identity)
            ready_output = auth_root / "ready-output"
            ready = release_notes_workflow.validate_ready_handoff(
                fixture["metadata"], fixture["run"], fixture["artifact_list"], fixture["result_metadata"],
                expected_artifact_id=fixture["ready_id"],
                expected_result_artifact_id=fixture["result_id"],
                ready_directory=fixture["ready_dir"], result_directory=fixture["result_dir"],
                output_path=str(ready_output))
            self.assertEqual(ready["components"], "auth")
            self.assertEqual(ready["resume_inputs"]["auth_artifact_id"], AUTH_SOURCE_FIXTURE["artifact_id"])
            self.assertEqual(ready["resume_inputs"]["auth_run_id"], str(AUTH_SOURCE_FIXTURE["run_id"]))
            self.assertTrue(ready_output.is_file())

            # This local package payload and artifact transport metadata are synthetic; the plan bytes are the
            # exact schema-4 producer identity returned by the same Ready reader above.
            package = auth_root / "package"
            package.mkdir()
            body = note("2026.11.4", "en", "Synthetic local Notes payload")
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "2026.11.4",
                "environment": "dev",
                "release_plan_sha256": hashlib.sha256(fixture["plan_bytes"]).hexdigest(),
                "release_note_snapshot_ref": "LWC-A-62:test-only",
                "release_note_snapshot_sha256": "sha256:" + "f" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
                "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
            }
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            action_digest = "a" * 64
            metadata = {
                "id": 803, "name": "lwc-release-notes-2026.11.4-development-604-1",
                "digest": "sha256:" + action_digest, "expired": False,
                "expires_at": "2027-02-12T00:00:00Z",
                "workflow_run": {"id": 604, "head_branch": "develop", "head_sha": "c" * 40},
            }
            uploaded = release_notes_workflow.verify_uploaded_package(
                package, fixture["ready_dir"] / "plan.json", metadata,
                expected_artifact_id="803", expected_name=metadata["name"], action_digest=action_digest,
                run=self.make_package_producer_run(), run_id="604", run_attempt="1",
                ready_artifact_id=fixture["ready_id"], output_path=str(auth_root / "package-output"))
            self.assertEqual(uploaded["product_version"], "2026.11.4")
            self.assertEqual(uploaded["resume_inputs"]["auth_revision"], AUTH_SOURCE_FIXTURE["revision"])

            invalid_cases = []
            bad = copy.deepcopy(plan)
            bad["identities"]["auth"] = "sha256:" + "A" * 64
            invalid_cases.append(("uppercase digest", bad))
            bad = copy.deepcopy(plan)
            bad["identities"]["auth"] = "a" * 64
            invalid_cases.append(("missing digest prefix", bad))
            bad = copy.deepcopy(plan)
            del bad["auth_source"]
            invalid_cases.append(("missing independent provenance", bad))
            bad = copy.deepcopy(plan)
            bad["auth_source"]["repository_id"] = "not-a-github-repository-id"
            invalid_cases.append(("malformed independent provenance", bad))
            bad = copy.deepcopy(plan)
            bad["identities"]["auth"] = producer_identity_fixture("auth")
            invalid_cases.append(("independent auth without digest identity", bad))
            legacy_auth = self.make_ready_handoff(
                auth_root / "schema2-auth", selected=("auth",), schema=2)
            bad = copy.deepcopy(legacy_auth["plan"])
            bad["identities"]["auth"] = "sha256:" + "d" * 64
            invalid_cases.append(("schema-2 string identity", bad))
            legacy_bff = self.make_ready_handoff(
                auth_root / "legacy-bff", selected=("bff",), schema=3)
            bad = copy.deepcopy(legacy_bff["plan"])
            bad["identities"]["bff"] = "sha256:" + "d" * 64
            invalid_cases.append(("string identity on non-Auth component", bad))
            bad = copy.deepcopy(legacy_bff["plan"])
            bad["identities"]["bff"]["inputs"] = "a" * 64
            invalid_cases.append(("component input digest mismatch", bad))
            for invalid_path in (".", "bad\x00path"):
                bad = copy.deepcopy(legacy_bff["plan"])
                identity = bad["identities"]["bff"]
                identity["files"][0][0] = invalid_path
                identity["inputs"] = hashlib.sha256(json.dumps(
                    identity["files"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
                bad["id"] = engine.plan_id(bad)
                with self.subTest(invalid_source_path=repr(invalid_path)):
                    self.assertEqual(identity["inputs"], hashlib.sha256(json.dumps(
                        identity["files"], sort_keys=True, separators=(",", ":")).encode()).hexdigest())
                    self.assertFalse(release_notes_workflow._valid_source_identity_map(identity))
                invalid_cases.append((f"non-Git source path {invalid_path!r}", bad))
            bad = copy.deepcopy(legacy_bff["plan"])
            bad["release_source_mode"] = "unsupported"
            invalid_cases.append(("unknown release source mode", bad))
            bad = copy.deepcopy(legacy_auth["plan"])
            bad["release_source_mode"] = "legacy_monorepo"
            invalid_cases.append(("schema-2 with a schema-3 mode field", bad))
            for label, invalid_plan in invalid_cases:
                with self.subTest(invalid_identity=label), self.assertRaisesRegex(
                        release_notes_workflow.PackageWorkflowError, "release-plan-not-bff-dev"):
                    release_notes_workflow._validate_development_plan(canonical(invalid_plan))

    def test_component_only_ready_resume_capture_and_publication_do_not_prepare_bff(self):
        for component in ("frontend", "worker"):
            with self.subTest(component=component), TemporaryDirectory() as temporary:
                root = Path(temporary)
                fixture = self.make_ready_handoff(root, selected=(component,), schema=4)
                plan = fixture["plan"]
                plan["normalized"].update({
                    "gcp": {"project_id": "fixture-project", "region": "us-central1"},
                    "bff": {"service_name": "bff-dev"},
                    "frontend": {"environment": "development", "static_root": "public"},
                    "worker": {"job_name": "pipeline-dev", "bucket": "fixture-bucket"},
                })
                plan["id"] = engine.plan_id(plan)
                for directory in (fixture["ready_dir"], fixture["result_dir"]):
                    engine.write(directory / "plan.json", plan)
                plan_bytes = (fixture["ready_dir"] / "plan.json").read_bytes()
                state = json.loads(fixture["state_bytes"])
                state["plan"] = plan["id"]
                state_bytes = None
                result = json.loads(fixture["result_bytes"])
                result.update(release=plan["tag"], attempt=plan["id"], expected={"source": plan["source"]})
                for directory in (fixture["ready_dir"], fixture["result_dir"]):
                    engine.write(directory / "state.json", state)
                    engine.write(directory / "result.json", result)
                state_bytes = (fixture["ready_dir"] / "state.json").read_bytes()
                result_bytes = (fixture["ready_dir"] / "result.json").read_bytes()
                evidence_path = fixture["result_dir"] / "release-notes-evidence.json"
                evidence = json.loads(evidence_path.read_bytes())
                evidence["stages"][0]["plan_sha256"] = hashlib.sha256(plan_bytes).hexdigest()
                evidence_path.write_bytes(canonical(evidence))
                fixture.update(plan=plan, plan_bytes=plan_bytes, state_bytes=state_bytes,
                               result_bytes=result_bytes)

                receipts = fixture["ready_dir"] / "receipts"
                receipts.mkdir()
                if component == "frontend":
                    artifact = {"deployment_id": "fixture-frontend-deployment"}
                    receipt = {"schema": 2, "component": component,
                               "identity": plan["identities"][component], "build_sha": plan["source"],
                               "artifact": artifact,
                               "target_config": engine.digest(plan["normalized"]["frontend"])}
                    (fixture["ready_dir"] / "frontend.tgz").write_bytes(b"synthetic frontend artifact")
                else:
                    artifact = {"image": "registry.example/pipeline@sha256:" + "a" * 64,
                                "pipeline_config": {"fixture": True}}
                    receipt = {"schema": 2, "component": component,
                               "identity": plan["identities"][component], "build_sha": plan["source"],
                               "artifact": artifact, "target_config": None}
                (receipts / f"{component}.json").write_bytes(canonical(receipt))

                # Exercise the same producer output contract used by workflow_dispatch.
                package_dir = root / "package"
                package_dir.mkdir()
                body = note("2026.11.4", "en", f"Synthetic {component} package")
                manifest = {
                    "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "2026.11.4",
                    "environment": "dev", "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
                    "release_note_snapshot_ref": "LWC-A-62:test-only",
                    "release_note_snapshot_sha256": "sha256:" + "f" * 64,
                    "delivered_scope_evidence_sha256": "sha256:" + "e" * 64,
                    "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
                }
                (package_dir / "release-notes-manifest.json").write_bytes(canonical(manifest))
                (package_dir / "en.json").write_bytes(body)
                ready_result = release_notes_workflow.validate_ready_handoff(
                    fixture["metadata"], fixture["run"], fixture["artifact_list"],
                    fixture["result_metadata"], expected_artifact_id=fixture["ready_id"],
                    expected_result_artifact_id=fixture["result_id"],
                    ready_directory=fixture["ready_dir"], result_directory=fixture["result_dir"],
                    output_path=str(root / "ready-output"))
                upload_metadata = {
                    "id": 803, "name": "lwc-release-notes-2026.11.4-development-604-1",
                    "digest": "sha256:" + "a" * 64, "expired": False,
                    "expires_at": "2027-02-12T00:00:00Z", "workflow_run": {"id": 604, "head_branch": "develop", "head_sha": "c" * 40},
                }
                package_receipt = release_notes_workflow.verify_uploaded_package(
                    package_dir, fixture["ready_dir"] / "plan.json", upload_metadata,
                    expected_artifact_id="803", expected_name=upload_metadata["name"],
                    action_digest="a" * 64, run=self.make_package_producer_run(), run_id="604", run_attempt="1",
                    ready_artifact_id=fixture["ready_id"], output_path=str(root / "package-output"))
                resume_inputs = package_receipt["resume_inputs"]
                self.assertEqual({key: resume_inputs[key] for key in ready_result["resume_inputs"]},
                                 ready_result["resume_inputs"])
                self.assertEqual(resume_inputs["components"], component)
                self.assertEqual(resume_inputs["artifact_id"], fixture["ready_id"])
                self.assertEqual(resume_inputs["release_notes_artifact_id"], "803")

                requested_plan = dict(plan, executor_sha="c" * 40)
                requested_plan["id"] = engine.plan_id(requested_plan)
                resumed_dir = root / "resumed"
                resumed_dir.mkdir()
                engine.write(resumed_dir / "plan.json", requested_plan)
                resumed = engine.Engine(resumed_dir, executor_sha=requested_plan["executor_sha"])
                with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}, clear=False), \
                        patch.object(engine.Providers, "usable", lambda _provider, _component, _artifact: None), \
                        patch.object(engine.Providers, "valid_image", lambda _provider, _component, _image: None), \
                        patch.object(engine.Providers, "prepare_pipeline_config",
                                     lambda _provider: {"fixture": "pipeline-config"}), \
                        patch.object(engine.Providers, "prepare",
                                     side_effect=AssertionError("selected receipt must avoid product build")):
                    resumed.prepare(reuse=str(fixture["ready_dir"]))
                    resumed.result()
                self.assertEqual((resumed_dir / "plan.json").read_bytes(), plan_bytes)
                self.assertEqual(resumed.state["status"], "ready")
                self.assertEqual(resumed.state["components"], {})
                self.assertFalse((resumed_dir / "receipts/bff.json").exists())

                candidate = ({"deployment_id": artifact["deployment_id"]} if component == "frontend"
                             else {"execution_id": "synthetic-pipeline-execution"})
                state = {"plan": plan["id"], "status": "success", "sequence": 20,
                         "checkpoint_schema": 1, "builds": {},
                         "components": {component: {"status": "verified", "candidate": candidate}}}
                identity_digest = hashlib.sha256(canonical(plan["identities"][component])).hexdigest()
                result = {"release": plan["tag"], "attempt": plan["id"], "stage": "success",
                          "status": "success", "component": component,
                          "expected": {"source": plan["source"], "identity": identity_digest},
                          "observed": {"component_status": "verified"}, "candidate": candidate,
                          "last_verified_checkpoint": state["sequence"]}
                (resumed_dir / "state.json").write_bytes(canonical(state))
                (resumed_dir / "result.json").write_bytes(canonical(result))
                observed_build = {"environment": "dev", "product_version": "1.0.0",
                                  "commit": "d" * 40, "branch": "develop", "tag": "v1.0.0",
                                  "image_tag": "bff-v1.0.0", "service": "bff-dev",
                                  "revision": "bff-current"}
                bff_image = "registry.example/bff@sha256:" + "b" * 64
                service_object = {
                    "metadata": {"name": "bff-dev", "uid": "fixture-bff-uid", "generation": 31},
                    "status": {"traffic": [{"revisionName": "bff-current", "percent": 100}],
                               "uri": "https://fixture-bff.invalid"},
                }
                revision_object = {
                    "metadata": {"name": "bff-current"},
                    "status": {"conditions": [{"type": "Ready", "status": "True"}],
                               "imageDigest": bff_image},
                    "spec": {"containers": [{"image": bff_image}]},
                }
                fake_bin = root / "fake-bin"
                fake_bin.mkdir()
                command_log = root / "fake-gcloud-commands.jsonl"
                fake_gcloud = fake_bin / "gcloud"
                fake_gcloud.write_text(
                    "#!/usr/bin/env python3\n"
                    "import json, os, sys\n"
                    "with open(os.environ['GCP_COMMAND_LOG'], 'a', encoding='utf-8') as stream: "
                    "stream.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                    "if len(sys.argv) > 2 and sys.argv[2] == 'services': print(" + repr(json.dumps(service_object)) + ")\n"
                    "elif len(sys.argv) > 2 and sys.argv[2] == 'revisions': print(" + repr(json.dumps(revision_object)) + ")\n"
                    "else: sys.exit(1)\n"
                )
                fake_gcloud.chmod(0o755)

                class VersionHandler(http.server.BaseHTTPRequestHandler):
                    def do_GET(self):
                        self.server.requests.append((self.path, self.headers.get("Cache-Control")))
                        if self.path != "/api/v1/public/version":
                            self.send_error(404)
                            return
                        encoded = canonical(observed_build)
                        self.send_response(200)
                        self.send_header("Cache-Control", "no-store")
                        self.send_header("Content-Type", "application/json")
                        self.send_header("Content-Length", str(len(encoded)))
                        self.end_headers()
                        self.wfile.write(encoded)

                    def log_message(self, *_args):
                        pass

                server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), VersionHandler)
                server.requests = []
                server_thread = threading.Thread(target=server.serve_forever, daemon=True)
                server_thread.start()
                # urllib.request.urlopen lazily caches a process-global opener.
                # This fixture temporarily installs proxy settings so only its
                # owned localhost request bypasses the blocked proxy; restore
                # the exact prior opener with the environment on every exit.
                previous_urlopen_opener = urllib.request._opener
                real_urlopen = urllib.request.urlopen

                def local_build_info_urlopen(request, *args, **kwargs):
                    if not isinstance(request, urllib.request.Request) or not request.full_url.startswith(
                            "https://fixture-bff.invalid/"):
                        raise AssertionError("unexpected nonfixture BFF readback URL")
                    local_request = urllib.request.Request(
                        request.full_url.replace("https://fixture-bff.invalid",
                                                 f"http://127.0.0.1:{server.server_port}"),
                        headers=dict(request.header_items()), method=request.get_method())
                    return real_urlopen(local_request, *args, **kwargs)

                try:
                    with patch.dict(os.environ, {
                            "PATH": str(fake_bin) + os.pathsep + os.environ.get("PATH", ""),
                            "GCP_COMMAND_LOG": str(command_log),
                            "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
                            "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
                            "GCE_METADATA_HOST": "127.0.0.1:9",
                            "HTTP_PROXY": "http://127.0.0.1:9", "HTTPS_PROXY": "http://127.0.0.1:9",
                            "ALL_PROXY": "http://127.0.0.1:9", "NO_PROXY": "localhost,127.0.0.1",
                            "http_proxy": "http://127.0.0.1:9", "https_proxy": "http://127.0.0.1:9",
                            "all_proxy": "http://127.0.0.1:9", "no_proxy": "localhost,127.0.0.1",
                    }, clear=False), \
                            patch.object(release_notes_capture.urllib.request, "urlopen",
                                         side_effect=local_build_info_urlopen):
                        marker, _ = release_notes_capture.capture(resumed_dir, "release", "605", 1)
                finally:
                    try:
                        server.shutdown()
                        server.server_close()
                        server_thread.join(timeout=2)
                    finally:
                        urllib.request.install_opener(previous_urlopen_opener)
                self.assertIs(urllib.request._opener, previous_urlopen_opener)
                self.assertEqual(server.requests, [("/api/v1/public/version", "no-cache")])
                gcloud_commands = [json.loads(line) for line in command_log.read_text().splitlines()]
                self.assertEqual([command[1] for command in gcloud_commands], ["services", "revisions"])
                self.assertFalse(any("deploy" in command for command in gcloud_commands))
                self.assertEqual([entry["component"] for entry in marker["component_builds"]], [component])
                self.assertNotIn("bff", [entry["component"] for entry in marker["component_builds"]])

                store = MemoryNotesStore()
                store.active_marker = marker
                publication = release_notes.publish_package(
                    store, {"en": body}, product_version="2026.11.4",
                    published_at="2026-11-16T01:00:00Z", marker=marker,
                    transition_plan_sha256=hashlib.sha256(plan_bytes).hexdigest())
                published_index = release_notes.validate_index(store.index)
                self.assertGreater(publication.index_generation, 0)
                self.assertEqual(published_index["current_deployment"], marker)
                self.assertEqual(published_index["versions"][0]["product_version"], "2026.11.4")
                self.assertNotIn("bff", [entry["component"] for entry in marker["component_builds"]])

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
                "workflow_run": {"id": 802, "head_branch": "develop", "head_sha": "c" * 40},
            }
            output = root / "github-output"
            verified = release_notes_workflow.verify_uploaded_package(
                package, plan_path, metadata, expected_artifact_id="801", expected_name=metadata["name"],
                action_digest=action_digest, run=self.make_package_producer_run(802), run_id="802", run_attempt="1", ready_artifact_id="701",
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
                    action_digest="d" * 64, run=self.make_package_producer_run(802), run_id="802", run_attempt="1",
                    ready_artifact_id="701", output_path=str(output))
            for invalid in ("sha256:" + action_digest, "", "C" * 64):
                with self.subTest(invalid=invalid[:8]), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.verify_uploaded_package(
                        package, plan_path, metadata, expected_artifact_id="801", expected_name=metadata["name"],
                        action_digest=invalid, run=self.make_package_producer_run(802), run_id="802", run_attempt="1",
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
            inputs = body["inputs"]
            workflow = yaml.safe_load((ROOT / ".github/workflows/deploy-dev.yml").read_text())
            trigger = workflow.get("on", workflow.get(True, {}))
            declared_inputs = trigger["workflow_dispatch"]["inputs"]
            self.assertEqual(len(declared_inputs), 25)
            self.assertTrue(set(inputs) <= set(declared_inputs))
            self.assertEqual(inputs["release_notes_package_request_id"], "1" * 32)
            self.assertNotIn("release_notes_package_retention_days", inputs)
            self.assertEqual(inputs["prepare_release_notes_package"], "true")
            self.assertTrue(all(type(value) is str for value in inputs.values()))
            self.assertEqual(inputs["operation"], "release")
            self.assertEqual(inputs["release_notes_action"], "stop")
            self.assertEqual(inputs["artifact_id"], "701")
            self.assertEqual(len(base64.b64decode(inputs["package_zip_base64"], validate=True)),
                             len(release_notes_workflow.package_directory_archive(package)))
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                        "dispatch-input-invalid"):
                release_notes_workflow.build_dispatch_payload(
                    package, ready_artifact_id="701", retention_days=30, request_id="2" * 32)

    def test_parent_dispatch_routes_authenticated_ready_to_its_registered_wrapper(self):
        with TemporaryDirectory() as temporary:
            for environment in ("development", "production"):
                with self.subTest(environment=environment):
                    root = Path(temporary) / environment
                    root.mkdir()
                    ready_root = root / "ready"
                    ready_root.mkdir()
                    fixture = self.make_ready_handoff(root / "ready", schema=4,
                                                      environment=environment)
                    package, _, _ = self.make_package_for_plan(
                        root / "package", self.plan_bytes, environment=environment,
                        version="1.2.3", text="Local synthetic package receipt fixture")
                    destination = release_notes_workflow.DESTINATIONS[environment]
                    workflow_name = Path(destination["workflow_path"]).name
                    branch = destination["branch"]
                    artifact_digest = "sha256:" + "b" * 64
                    package_run = {
                        "id": 802, "run_attempt": 1,
                        "display_title": "Prepare Release Notes package " + "a" * 32,
                        "path": f"{destination['workflow_path']}@refs/heads/{branch}",
                        "event": "workflow_dispatch", "head_branch": branch, "head_sha": "c" * 40,
                        "status": "completed", "conclusion": "success",
                        "html_url": "https://github.test/run/802",
                    }
                    expected_name = f"lwc-release-notes-1.2.3-{environment}-802-1"
                    responses = [
                        fixture["metadata"],
                        fixture["run"],
                        fixture["artifact_list"],
                        None,
                        {"workflow_runs": [package_run]},
                        {"artifacts": [{"id": 801, "name": expected_name}]},
                        {"id": 801, "name": expected_name, "digest": artifact_digest,
                         "expired": False, "expires_at": "2026-12-14T00:00:00Z",
                         "workflow_run": {"id": 802, "head_branch": branch, "head_sha": "c" * 40}},
                    ]
                    request_id = "a" * 32
                    with patch.object(release_notes_workflow.uuid, "uuid4",
                                      return_value=type("RequestId", (), {"hex": request_id})()), \
                         patch.object(release_notes_workflow, "_gh_api", side_effect=responses) as api:
                        receipt = release_notes_workflow.dispatch_package(
                            "Rayer/llm-wiki-cloud", package, ready_artifact_id=fixture["ready_id"],
                            retention_days=90)
                    self.assertEqual(receipt["status"], "ready")
                    self.assertEqual(receipt["destination"], environment)
                    self.assertEqual(receipt["ref"], branch)
                    self.assertEqual(receipt["workflow_path"], destination["workflow_path"])
                    self.assertEqual(receipt["artifact_id"], "801")
                    self.assertEqual(receipt["artifact_name"], expected_name)
                    self.assertEqual(receipt["artifact_digest"], artifact_digest)
                    self.assertEqual(receipt["ready_artifact_id"], fixture["ready_id"])
                    self.assertEqual(receipt["requested_retention_days"], 90)
                    self.assertNotIn("Local synthetic package receipt fixture", json.dumps(receipt))
                    post_call = api.call_args_list[3]
                    self.assertEqual(post_call.args[1], f"actions/workflows/{workflow_name}/dispatches")
                    body = json.loads(post_call.kwargs["body"])
                    self.assertEqual(body["ref"], branch)
                    self.assertEqual(body["inputs"]["release_notes_package_request_id"], request_id)
                    self.assertTrue(body["inputs"]["prepare_release_notes_package"])
                    self.assertEqual(body["inputs"]["operation"], "release")
                    self.assertEqual(body["inputs"]["release_notes_action"], "stop")
                    self.assertEqual(body["inputs"]["artifact_id"], fixture["ready_id"])
                    self.assertTrue(body["inputs"]["package_zip_base64"])
                    self.assertEqual(post_call.kwargs["method"], "POST")
                    self.assertEqual(api.call_args_list[4].args[1],
                                     f"actions/workflows/{workflow_name}/runs?branch={branch}&event=workflow_dispatch&per_page=100")
                    self.assertNotIn("release_notes_package_retention_days", body["inputs"])
                    self.assertNotIn(body["inputs"]["package_zip_base64"], json.dumps(receipt))

    def test_gh_api_writer_uses_stdin_and_never_shells_package_json(self):
        package_request = b'{"ref":"develop","inputs":{"prepare_release_notes_package":true,"package_zip_base64":"private"}}'
        completed = type("Completed", (), {"returncode": 0, "stdout": b"", "stderr": b""})()
        with patch.object(release_notes_workflow.subprocess, "run", return_value=completed) as run:
            self.assertIsNone(release_notes_workflow._gh_api(
                "Rayer/llm-wiki-cloud", "actions/workflows/deploy-dev.yml/dispatches",
                method="POST", body=package_request))
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["gh", "api",
                                   "repos/Rayer/llm-wiki-cloud/actions/workflows/deploy-dev.yml/dispatches",
                                   "--method", "POST", "--input", "-"])
        self.assertEqual(kwargs["input"], package_request)
        self.assertIs(kwargs["capture_output"], True)
        self.assertIs(kwargs["check"], False)
        self.assertIs(kwargs.get("shell", False), False)

    def make_historical_package(self, package_dir, plan_bytes):
        package_dir.mkdir()
        body = note("2026.11.1", "en", "Synthetic fixture only; never published content")
        manifest = {
            "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "2026.11.1",
            "environment": "dev", "release_plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
            "release_note_snapshot_ref": "LWC-A-58:synthetic-test-reference",
            "release_note_snapshot_sha256": "sha256:" + "a" * 64,
            "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
            "original_published_at": "2026-11-01T22:58:30+08:00",
            "historical_source_record_ref": "LWC-A-58:synthetic-test-reference",
            "historical_source_record_sha256": "sha256:" + "c" * 64,
            "imported_at": "2026-11-15T09:00:00+08:00",
            "files": {"en": {"path": "en.json", "sha256": hashlib.sha256(body).hexdigest()}},
        }
        (package_dir / "release-notes-manifest.json").write_bytes(canonical(manifest))
        (package_dir / "en.json").write_bytes(body)
        return manifest, body

    def test_historical_result_admission_binds_exact_successful_dev_operation(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_historical_result(root)
            output = root / "github-output"
            binding_path = root / "history-binding.json"
            binding = release_notes_workflow.validate_history_result_handoff(
                fixture["metadata"], fixture["run"], fixture["result_dir"],
                expected_artifact_id=fixture["result_id"], operation="deploy",
                output_path=str(output), binding_path=binding_path)
            self.assertEqual(binding["source_kind"], "historical-result")
            self.assertEqual(binding["result_artifact_id"], fixture["result_id"])
            self.assertEqual(binding["run_id"], fixture["run_id"])
            self.assertEqual(binding["head_sha"], fixture["head_sha"])
            self.assertEqual(binding["source_sha"], fixture["source_sha"])
            self.assertEqual(binding["plan_sha256"], hashlib.sha256(fixture["plan_bytes"]).hexdigest())
            self.assertEqual(json.loads(binding_path.read_text()), binding)
            self.assertEqual(binding_path.stat().st_mode & 0o777, 0o600)
            self.assertIn(f"plan_sha256={binding['plan_sha256']}\n", output.read_text())

            for label, mutate in (
                ("wrong artifact id", lambda meta, run: meta.update(id=int(fixture["result_id"]) + 1)),
                ("expired artifact", lambda meta, run: meta.update(expired=True)),
                ("wrong result name", lambda meta, run: meta.update(name="lwc-ready-development-9902-1")),
                ("wrong run attempt", lambda meta, run: run.update(run_attempt=2)),
                ("failed workflow", lambda meta, run: run.update(conclusion="failure")),
                ("wrong source head", lambda meta, run: run.update(head_sha="e" * 40)),
                ("non-dev branch", lambda meta, run: run.update(head_branch="main")),
            ):
                metadata = json.loads(json.dumps(fixture["metadata"]))
                run = dict(fixture["run"])
                mutate(metadata, run)
                with self.subTest(label=label), self.assertRaises(release_notes_workflow.PackageWorkflowError):
                    release_notes_workflow.validate_history_result_handoff(
                        metadata, run, fixture["result_dir"], expected_artifact_id=fixture["result_id"],
                        operation="deploy")

            with self.assertRaisesRegex(release_notes.ReleaseNotesError, "native-operation-unsuccessful"):
                release_notes_workflow.validate_history_result_handoff(
                    fixture["metadata"], fixture["run"], fixture["result_dir"],
                    expected_artifact_id=fixture["result_id"], operation="rollback")
            sidecar = fixture["result_dir"] / "release-notes-operation.json"
            sidecar.write_bytes(b"synthetic sidecar")
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                        "history-operation-sidecar-present"):
                release_notes_workflow.validate_history_result_handoff(
                    fixture["metadata"], fixture["run"], fixture["result_dir"],
                    expected_artifact_id=fixture["result_id"], operation="deploy")

    def test_parent_authentic_a58_dev_history_passes_full_run_admission(self):
        metadata_path = os.environ.get("LWC478_AUTHENTIC_A58_ARTIFACT_METADATA")
        run_path = os.environ.get("LWC478_AUTHENTIC_A58_WORKFLOW_RUN")
        result_directory = os.environ.get("LWC478_AUTHENTIC_A58_RESULT_DIRECTORY")
        if not all((metadata_path, run_path, result_directory)):
            self.skipTest("Parent authentic A58 inputs are supplied only to retained source qualification")

        metadata = json.loads(Path(metadata_path).read_bytes())
        run = json.loads(Path(run_path).read_bytes())
        with TemporaryDirectory() as temporary:
            output = Path(temporary) / "github-output"
            binding_path = Path(temporary) / "history-binding.json"
            binding = release_notes_workflow.validate_history_result_handoff(
                metadata, run, Path(result_directory), expected_artifact_id="13087604252",
                operation="deploy", output_path=str(output), binding_path=binding_path)

            self.assertEqual(binding["result_artifact_id"], "13087604252")
            self.assertEqual(binding["result_artifact_name"], "lwc-result-development-41315481313-1")
            self.assertEqual(binding["run_id"], "41315481313")
            self.assertEqual(binding["run_attempt"], "1")
            self.assertEqual(binding["head_sha"], "21668758391871a9e4e99cf9319a8aebb1a09051")
            self.assertEqual(binding["source_sha"], "21668758391871a9e4e99cf9319a8aebb1a09051")
            self.assertEqual(binding["plan_sha256"],
                             "96ed3330b62ca1bd73dba047fe072f051e1aa71a7569d788214cd1178cb708ad")
            self.assertEqual(json.loads(binding_path.read_bytes()), binding)
            self.assertIn(f"plan_sha256={binding['plan_sha256']}\n", output.read_text())

    def test_marker_and_explicit_skip_execute_complete_and_record_stage_evidence(self):
        for action, operation in (("marker", "deploy"), ("skip", "release")):
            with self.subTest(action=action), TemporaryDirectory() as temporary:
                root = Path(temporary)
                previous_fixture = operation_fixture(operation=operation, service_generation=42)
                current_fixture = operation_fixture(operation=operation, service_generation=43)
                previous_marker = release_notes.normalize_operation(
                    *previous_fixture[:4], environment="dev", workflow_operation=operation,
                    run_id="701", run_attempt=1, service="svc", receipt_bytes=previous_fixture[4])[0]
                current_marker = release_notes.normalize_operation(
                    *current_fixture[:4], environment="dev", workflow_operation=operation,
                    run_id="701", run_attempt=1, service="svc", receipt_bytes=current_fixture[4])[0]

                body = note("1.2.3", "en")
                digest = release_notes.digest_bytes(body)
                object_path = release_notes.exact_object_path("1.2.3", "en", digest)
                store = MemoryNotesStore()
                store.objects[object_path] = (body, 7)
                store.next_generation = 8
                store.generation = 11
                store.active_marker = current_marker
                store.active_marker_matches = lambda marker, **_kwargs: store.active_marker == marker
                store.index = canonical({
                    "schema_version": release_notes.SCHEMA_VERSION,
                    "versions": [{
                        "product_version": "1.2.3",
                        "published_at": "2026-11-01T22:58:30+08:00",
                        "locales": {"en": {"sha256": digest, "object_path": object_path}},
                    }],
                    "current_deployment": previous_marker,
                })

                (root / "receipts").mkdir()
                (root / "plan.json").write_bytes(current_fixture[0])
                (root / "state.json").write_bytes(current_fixture[1])
                (root / "result.json").write_bytes(current_fixture[2])
                (root / "receipts" / "bff.json").write_bytes(current_fixture[4])
                (root / "release-notes-operation.json").write_bytes(current_fixture[3] + b"\n")

                class Handler(http.server.BaseHTTPRequestHandler):
                    def do_GET(self):
                        index = release_notes.validate_index(store.index)
                        if self.path == "/api/v1/public/release-notes":
                            current_row = next(row for row in index["versions"]
                                               if row["product_version"] == current_marker["product_version"])
                            value = {
                                "schema_version": release_notes.SCHEMA_VERSION,
                                "environment": "dev",
                                "history": [],
                                "runtime_product_version": current_marker["product_version"],
                                "current_product_version": current_marker["product_version"],
                                "current_status": "available",
                                "current": {"product_version": current_row["product_version"],
                                            "published_at": current_row["published_at"], "locales": ["en"]},
                                "component_details_status": "unavailable", "current_components": [],
                                "compatibility_aliases": [],
                            }
                        else:
                            self.send_error(404)
                            return
                        encoded = canonical(value)
                        self.send_response(200)
                        self.send_header("Cache-Control", "no-store")
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(encoded)

                    def log_message(self, _format, *_args):
                        pass

                server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    args = release_notes_publish.parser().parse_args([
                        "--action", action, "--directory", str(root), "--environment", "dev",
                        "--bucket", "synthetic-no-cloud", "--plan", str(root / "plan.json"),
                        "--operation", operation, "--run-id", "701", "--run-attempt", "1",
                        "--product-status", "released",
                    ])
                    with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                            patch.object(release_notes_capture, "active_service_url",
                                         return_value=f"http://127.0.0.1:{server.server_port}"):
                        try:
                            evidence = release_notes_publish.execute(args)
                        except UnboundLocalError:
                            # The publication is already committed; this assertion makes the old
                            # unhandled evidence-serialization regression observable as such.
                            self.assertEqual(store.writes, 1)
                            self.assertFalse((root / "release-notes-evidence.json").exists())
                            raise
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join(timeout=2)

                self.assertEqual(evidence["status"], "verified")
                self.assertEqual(evidence["stage"], "exact_readback")
                self.assertEqual(evidence["currentness"], "verified")
                self.assertEqual(store.writes, 1)
                updated_index = json.loads(store.index)
                self.assertEqual(updated_index["current_deployment"], current_marker)
                self.assertEqual(updated_index["versions"][0]["published_at"],
                                 "2026-11-01T22:58:30+08:00")
                recorded = json.loads((root / "release-notes-evidence.json").read_bytes())["stages"]
                self.assertEqual(len(recorded), 1)
                self.assertEqual(recorded[0]["action"], action)
                self.assertEqual(recorded[0]["status"], "verified")
                self.assertEqual(recorded[0]["index_generation"], store.generation)
                self.assertIsNone(recorded[0]["historical_package_artifact_name"])

    def test_historical_package_event_upload_receipt_and_resend_identity_are_bound(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_historical_result(root)
            binding = release_notes_workflow.validate_history_result_handoff(
                fixture["metadata"], fixture["run"], fixture["result_dir"],
                expected_artifact_id=fixture["result_id"], operation="deploy")
            package = root / "package"
            self.make_historical_package(package, fixture["plan_bytes"])
            request_id = "7" * 32
            event, version = release_notes_workflow.build_dispatch_payload(
                package, history_result_artifact_id=fixture["result_id"], history_operation="deploy",
                retention_days=90, request_id=request_id)
            self.assertEqual(version, "2026.11.1")
            self.assertEqual(event["inputs"]["release_notes_operation"], "deploy")
            self.assertNotIn("release_notes_package_retention_days", event["inputs"])
            workflow = yaml.safe_load((ROOT / ".github/workflows/deploy-dev.yml").read_text())
            trigger = workflow.get("on", workflow.get(True, {}))
            self.assertTrue(set(event["inputs"]) <= set(trigger["workflow_dispatch"]["inputs"]))
            archive_b64 = event["inputs"]["package_zip_base64"]
            event_path = root / "event.json"
            event_path.write_bytes(canonical(event))
            output = root / "prepare-output"
            staged = root / "staged-package"
            prepared = release_notes_workflow.prepare_package_from_event(
                event_path, fixture["result_dir"] / "plan.json", staged, str(output), request_id,
                "", "90", fixture["result_id"], "deploy")
            self.assertEqual(prepared["source_kind"], "historical-result")
            self.assertEqual(prepared["release_plan_sha256"], binding["plan_sha256"])
            self.assertIn("source_kind=historical-result\n", output.read_text())
            self.assertNotIn(archive_b64, json.dumps(prepared))

            for label, changes in (
                ("both source IDs", {"artifact_id": "9900"}),
                ("wrong retained result", {"release_notes_result_artifact_id": "9900"}),
                ("wrong native operation", {"release_notes_operation": "rollback"}),
                ("legacy retention field", {"release_notes_package_retention_days": "90"}),
            ):
                bad_event = {"inputs": {**event["inputs"], **changes}}
                bad_path = root / f"bad-{label.replace(' ', '-')}.json"
                bad_path.write_bytes(canonical(bad_event))
                with self.subTest(label=label), self.assertRaisesRegex(
                        release_notes_workflow.PackageWorkflowError, "workflow-event-invalid"):
                    release_notes_workflow._read_package_input_from_event(
                        bad_path, request_id, "", "90", fixture["result_id"], "deploy")

            upload_run, upload_attempt, package_artifact_id = "9903", "2", "9904"
            expected_name = release_notes.historical_package_artifact_name(
                prepared["product_version"], "deploy", fixture["result_id"], upload_run, upload_attempt)
            action_digest = "e" * 64
            metadata = {"id": int(package_artifact_id), "name": expected_name,
                        "digest": "sha256:" + action_digest, "expired": False,
                        "expires_at": "2027-02-12T00:00:00Z", "workflow_run": {"id": int(upload_run), "head_branch": "develop", "head_sha": "c" * 40}}
            receipt = release_notes_workflow.verify_uploaded_package(
                staged, fixture["result_dir"] / "plan.json", metadata,
                expected_artifact_id=package_artifact_id, expected_name=expected_name,
                action_digest=action_digest, run=self.make_package_producer_run(
                    int(upload_run), int(upload_attempt)), run_id=upload_run, run_attempt=upload_attempt,
                ready_artifact_id="", history_result_artifact_id=fixture["result_id"],
                history_operation="deploy", history_binding=binding, output_path=str(root / "receipt-output"))
            self.assertEqual(receipt["source_kind"], "historical-result")
            self.assertEqual(receipt["history_result_artifact_id"], fixture["result_id"])
            self.assertEqual(receipt["history_operation"], "deploy")
            self.assertIsNone(receipt["ready_artifact_id"])
            self.assertIsNone(receipt["resume_inputs"])
            self.assertNotIn("Synthetic fixture only", json.dumps(receipt))
            for label, mutate in (
                ("missing archive digest", lambda value: value.pop("result_sha256")),
                ("wrong source", lambda value: value.update(source_sha="f" * 40)),
                ("wrong operation run", lambda value: value.update(run_attempt="3")),
                ("wrong plan", lambda value: value.update(plan_id="f" * 64)),
            ):
                bad_binding = dict(binding)
                mutate(bad_binding)
                with self.subTest(label=label), self.assertRaisesRegex(
                        release_notes_workflow.PackageWorkflowError, "history-result-binding-mismatch"):
                    release_notes_workflow.verify_uploaded_package(
                        staged, fixture["result_dir"] / "plan.json", metadata,
                        expected_artifact_id=package_artifact_id, expected_name=expected_name,
                        action_digest=action_digest, run=self.make_package_producer_run(
                    int(upload_run), int(upload_attempt)), run_id=upload_run, run_attempt=upload_attempt,
                        ready_artifact_id="", history_result_artifact_id=fixture["result_id"],
                        history_operation="deploy", history_binding=bad_binding)

            package_run = {"id": int(upload_run), "run_attempt": int(upload_attempt),
                           "event": "workflow_dispatch", "path": release_notes_workflow.WORKFLOW_PATH,
                           "status": "completed", "conclusion": "success", "head_branch": "develop",
                           "head_sha": "b" * 40}
            metadata["workflow_run"].update(head_branch="develop", head_sha=package_run["head_sha"])
            resend = release_notes_workflow.validate_resend_package_artifact(
                metadata, package_run, expected_artifact_id=package_artifact_id,
                result_artifact_id=fixture["result_id"], operation="deploy")
            self.assertTrue(resend["historical"])
            self.assertEqual(resend["artifact_name"], expected_name)
            self.assertEqual(resend["package_run_id"], upload_run)
            self.assertEqual(resend["package_run_attempt"], upload_attempt)
            legacy_metadata = dict(metadata, name="lwc-release-notes-1.2.3-development-9903-2")
            legacy = release_notes_workflow.validate_resend_package_artifact(
                legacy_metadata, package_run, expected_artifact_id=package_artifact_id,
                result_artifact_id=fixture["result_id"], operation="deploy")
            self.assertFalse(legacy["historical"])
            self.assertIsNone(legacy["history_result_artifact_id"])
            for wrong_id, wrong_operation in (("9905", "deploy"), (fixture["result_id"], "release")):
                with self.subTest(wrong_id=wrong_id, wrong_operation=wrong_operation), \
                        self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                               "historical-artifact-binding-mismatch"):
                    release_notes_workflow.validate_resend_package_artifact(
                        metadata, package_run, expected_artifact_id=package_artifact_id,
                        result_artifact_id=wrong_id, operation=wrong_operation)

            body, version = release_notes_workflow.build_dispatch_payload(
                package, history_result_artifact_id=fixture["result_id"], history_operation="deploy",
                retention_days=90, request_id="8" * 32)
            self.assertEqual(version, "2026.11.1")
            self.assertEqual(body["inputs"]["artifact_id"], "")
            self.assertEqual(body["inputs"]["release_notes_result_artifact_id"], fixture["result_id"])
            self.assertEqual(body["inputs"]["release_notes_operation"], "deploy")
            self.assertNotIn("release_notes_package_retention_days", body["inputs"])
            self.assertNotIn("Synthetic fixture only", json.dumps(body))
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                        "package-source-ambiguous"):
                release_notes_workflow.build_dispatch_payload(
                    package, ready_artifact_id="9900",
                    history_result_artifact_id=fixture["result_id"], history_operation="deploy",
                    retention_days=90, request_id="9" * 32)

    def test_history_only_resend_preserves_current_marker_and_uses_original_time(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.make_historical_result(root)
            run_dir = root / "retained-result"
            (run_dir / "receipts").mkdir(parents=True)
            for name, data in (("plan.json", fixture["plan_bytes"]),
                               ("state.json", fixture["state_bytes"]),
                               ("result.json", fixture["result_bytes"])):
                (run_dir / name).write_bytes(data)
            (run_dir / "receipts" / "bff.json").write_bytes(fixture["receipt_bytes"])
            package_dir = root / "package"
            manifest, body = self.make_historical_package(package_dir, fixture["plan_bytes"])
            package_artifact_id, package_run_id, package_attempt = "9910", "9911", "1"
            package_name = release_notes.historical_package_artifact_name(
                manifest["product_version"], "deploy", fixture["result_id"], package_run_id, package_attempt)

            store = MemoryNotesStore()
            current_fixture = operation_fixture(operation="deploy", service_generation=44,
                                                revision="rev-current", product_version="1.0.0")
            current_marker = release_notes.normalize_operation(
                *current_fixture[:4], environment="dev", workflow_operation="deploy",
                run_id="701", run_attempt=1, service="svc", receipt_bytes=current_fixture[4])[0]
            store.active_marker = current_marker
            release_notes.publish_marker_only(store, current_marker)
            marker_bytes_before = canonical(json.loads(store.index)["current_deployment"])
            marker_token_before = release_notes.current_deployment_token_bytes(store.index)
            served_body = [body]

            class Handler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    if self.path == "/api/v1/public/release-notes":
                        index = release_notes.validate_index(store.index)
                        value = {"schema_version": 1, "environment": "dev",
                                 "runtime_product_version": "1.0.0", "current_status": "missing",
                                 "current_product_version": "1.0.0",
                                 "history": [{"product_version": row["product_version"],
                                              "published_at": row["published_at"],
                                              "locales": sorted(row["locales"])}
                                             for row in index["versions"]
                                             if row["product_version"] != "1.0.0"],
                                 "component_details_status": "unavailable", "current_components": [],
                                 "compatibility_aliases": []}
                    elif self.path.startswith("/api/v1/public/release-notes/2026.11.1?locale=en"):
                        value = {**json.loads(served_body[0]), "requested_product_version": "2026.11.1",
                                 "published_at": "2026-11-01T22:58:30+08:00",
                                 "requested_locale": "en", "locale_fallback": False, "is_current": False,
                                 "canonical_product_version": "2026.11.1", "legacy_alias": False,
                                 "legacy_fallback": False}
                    else:
                        self.send_error(404)
                        return
                    encoded = canonical(value)
                    self.send_response(200)
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(encoded)

                def log_message(self, _format, *_args):
                    pass

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                args = release_notes_publish.parser().parse_args([
                    "--action", "resend", "--directory", str(run_dir), "--package-dir", str(package_dir),
                    "--environment", "dev", "--bucket", "synthetic-no-cloud", "--artifact-id", package_artifact_id,
                    "--result-artifact-id", fixture["result_id"], "--artifact-digest", "sha256:" + "f" * 64,
                    "--artifact-metadata-digest", "sha256:" + "f" * 64, "--artifact-name", package_name,
                    "--package-run-id", package_run_id, "--package-run-attempt", package_attempt,
                    "--plan", str(run_dir / "plan.json"), "--operation", "deploy",
                    "--run-id", fixture["run_id"], "--run-attempt", fixture["attempt"],
                    "--product-status", "released",
                ])
                with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                        patch.object(release_notes_capture, "active_service_url",
                                     return_value=f"http://127.0.0.1:{server.server_port}"):
                    evidence = release_notes_publish.execute(args)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)

            self.assertEqual(evidence["status"], "verified")
            self.assertEqual(evidence["currentness"], "missing")
            self.assertEqual(evidence["product_version"], "2026.11.1")
            self.assertEqual(evidence["published_at"], "2026-11-01T22:58:30+08:00")
            index = json.loads(store.index)
            self.assertEqual(canonical(index["current_deployment"]), marker_bytes_before)
            row = next(row for row in index["versions"] if row["product_version"] == "2026.11.1")
            self.assertEqual(row["published_at"], "2026-11-01T22:58:30+08:00")
            self.assertEqual(release_notes.current_deployment_token_bytes(store.index), marker_token_before)

            # Interleave a legitimate same-version correction after A's
            # publish returns but before A's first index snapshot.  The
            # executor must surface failed readback, never a verified receipt
            # for B under A's package digest.
            winner_body = note("2026.11.1", "en", "Synthetic concurrent editor B")
            real_publish = release_notes.publish_package

            def publish_a_then_b(target, *args, **kwargs):
                result = real_publish(target, *args, **kwargs)
                real_publish(target, {"en": winner_body}, product_version="2026.11.1",
                             published_at="2026-11-01T22:58:30+08:00", marker=None, historical=True)
                served_body[0] = winner_body
                return result

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                        patch.object(release_notes_capture, "active_service_url",
                                     return_value=f"http://127.0.0.1:{server.server_port}"), \
                        patch.object(release_notes, "publish_package", side_effect=publish_a_then_b):
                    with self.assertRaisesRegex(release_notes.ReleaseNotesError,
                                                "reader-content-readback-mismatch") as failed_readback:
                        release_notes_publish.execute(args)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)
            self.assertEqual(failed_readback.exception.stage, "exact_readback")
            final_index = release_notes.validate_index(store.index)
            final_row = next(row for row in final_index["versions"]
                             if row["product_version"] == "2026.11.1")
            self.assertEqual(store.read_content(final_row["locales"]["en"]["object_path"]), winner_body)
            self.assertEqual(final_row["published_at"], "2026-11-01T22:58:30+08:00")
            self.assertEqual(release_notes.current_deployment_token_bytes(store.index), marker_token_before)
            evidence = json.loads((run_dir / "release-notes-evidence.json").read_bytes())
            self.assertEqual(evidence["stages"][-1]["status"], "failed")
            self.assertEqual(evidence["stages"][-1]["stage"], "exact_readback")
            self.assertEqual(evidence["stages"][-1]["safe_class"], "reader-content-readback-mismatch")

    def test_legacy_alias_resend_uses_canonical_reader_body_without_changing_marker_or_index(self):
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            store, marker, plan_sha, _ = initial_schema2_migration_fixture()
            store.active_marker = marker
            canonical_body = note(release_notes.PUBLIC_RELEASE_VERSION, "en", "Canonical public note")
            release_notes.publish_package(
                store, {"en": canonical_body}, product_version=release_notes.PUBLIC_RELEASE_VERSION,
                published_at="2026-11-15T01:14:01Z", marker=marker, transition_plan_sha256=plan_sha)
            private_index = release_notes.validate_index(store.index)
            old_body = note(release_notes.LEGACY_RELEASE_VERSION, "en", "Original legacy note")
            old_digest = release_notes.digest_bytes(old_body)
            old_path = release_notes.exact_object_path(release_notes.LEGACY_RELEASE_VERSION, "en", old_digest)
            store.objects[old_path] = (old_body, store.next_generation)
            store.next_generation += 1
            private_index["versions"].append({
                "product_version": release_notes.LEGACY_RELEASE_VERSION,
                "published_at": "2026-11-01T22:58:30+08:00",
                "locales": {"en": {"sha256": old_digest, "object_path": old_path}},
            })
            store.index = canonical(private_index)
            store.generation += 1

            fixture = operation_fixture(operation="release", service_generation=44,
                                        revision="rev-legacy", product_version="1.0.0")
            run_dir = root / "retained-legacy-release"
            (run_dir / "receipts").mkdir(parents=True)
            for name, data in (("plan.json", fixture[0]), ("state.json", fixture[1]),
                               ("result.json", fixture[2])):
                (run_dir / name).write_bytes(data)
            (run_dir / "receipts" / "bff.json").write_bytes(fixture[4])
            (run_dir / "release-notes-operation.json").write_bytes(fixture[3] + b"\n")
            publication = {
                "schema_version": 1, "environment": "dev", "operation": "release",
                "run_id": "701", "run_attempt": 1,
                "plan_sha256": release_notes.digest_bytes(fixture[0]),
                "state_sha256": release_notes.digest_bytes(fixture[1]),
                "result_sha256": release_notes.digest_bytes(fixture[2]),
                "receipt_sha256": release_notes_publish.selected_receipt_digest(
                    json.loads(fixture[0]), fixture[4], {}),
                "published_at": "2026-11-01T22:58:30+08:00",
            }
            (run_dir / "release-notes-publication.json").write_bytes(canonical(publication) + b"\n")
            package_dir = root / "legacy-package"
            package_dir.mkdir()
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.0.0",
                "environment": "dev", "release_plan_sha256": release_notes.digest_bytes(fixture[0]),
                "release_note_snapshot_ref": "LWC-A-test:legacy", "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": old_digest}},
            }
            (package_dir / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package_dir / "en.json").write_bytes(old_body)
            original_index = store.index
            original_generation = store.generation
            original_marker = json.loads(original_index)["current_deployment"]
            original_writes = store.writes

            class Response:
                status = 200
                headers = {"Cache-Control": "no-store"}
                def __init__(self, value): self.data = canonical(value)
                def read(self, _size=-1): return self.data
                def __enter__(self): return self
                def __exit__(self, *_args): return False

            class Handler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    current = release_notes.validate_index(store.index)
                    if self.path == "/api/v1/public/release-notes":
                        current_row = next(row for row in current["versions"]
                                           if row["product_version"] == release_notes.PUBLIC_RELEASE_VERSION)
                        value = {
                            "schema_version": 1, "environment": "dev", "runtime_product_version": "1.0.0",
                            "current_status": "available", "current_product_version": release_notes.PUBLIC_RELEASE_VERSION,
                            "current": {"product_version": current_row["product_version"],
                                        "published_at": current_row["published_at"], "locales": ["en"]},
                            "history": [], "component_details_status": "complete",
                            "current_components": [{
                                "component": "bff", "product_version": marker["product_version"],
                                "commit": marker["commit"], "image_tag": marker["image_tag"],
                                "image_digest": marker["image_digest"], "revision": marker["revision"],
                            }],
                            "compatibility_aliases": [{"requested_version": "1.0.0",
                                                       "canonical_version": "2026.11.4",
                                                       "legacy_row_version": "1.0.0"}],
                        }
                    elif self.path == "/api/v1/public/release-notes/1.0.0?locale=en":
                        value = {**json.loads(canonical_body), "published_at": "2026-11-15T01:14:01Z",
                                 "requested_locale": "en", "locale_fallback": False,
                                 "is_current": True, "requested_product_version": "1.0.0",
                                 "canonical_product_version": "2026.11.4", "legacy_alias": True,
                                 "legacy_fallback": False}
                    else:
                        self.send_error(404)
                        return
                    encoded = canonical(value)
                    self.send_response(200)
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(encoded)

                def log_message(self, _format, *_args): pass

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                args = release_notes_publish.parser().parse_args([
                    "--action", "resend", "--directory", str(run_dir), "--package-dir", str(package_dir),
                    "--environment", "dev", "--bucket", "synthetic-no-cloud", "--artifact-id", "9920",
                    "--result-artifact-id", "9921", "--artifact-digest", "sha256:" + "f" * 64,
                    "--artifact-metadata-digest", "sha256:" + "f" * 64,
                    "--artifact-name", "lwc-release-notes-1.0.0-development-701-1",
                    "--plan", str(run_dir / "plan.json"), "--operation", "release",
                    "--run-id", "701", "--run-attempt", "1", "--product-status", "released",
                ])
                with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                        patch.object(release_notes_capture, "active_service_url",
                                     return_value=f"http://127.0.0.1:{server.server_port}"), \
                        patch.object(release_notes_capture, "active_marker_matches", return_value=False):
                    evidence = release_notes_publish.execute(args)
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)

            self.assertEqual(evidence["status"], "verified")
            self.assertEqual(evidence["currentness"], "verified")
            self.assertEqual(evidence["reader_api_readback"]["reader_requested_product_version"], "1.0.0")
            self.assertEqual(evidence["reader_api_readback"]["reader_canonical_product_version"], "2026.11.4")
            self.assertTrue(evidence["reader_api_readback"]["reader_legacy_alias"])
            self.assertEqual(evidence["legacy_alias_receipt"]["action"], "carried")
            self.assertEqual(store.index, original_index)
            self.assertEqual(store.generation, original_generation)
            self.assertEqual(store.writes, original_writes)
            self.assertEqual(json.loads(store.index)["current_deployment"], original_marker)


R10_CARRIER_ROOT = Path(os.environ.get(
    "LWC478_R10_CARRIER_DIR",
    "/Users/rayer/.hermes/profiles/lwc-tpm/artifacts/lwc-2026.11.4/dev/owner-uat-followups/conditional-production",
))
R10_AUTHENTIC_METADATA_ROOT = Path(os.environ.get(
    "LWC478_R10_AUTHENTIC_METADATA_DIR",
    "/Users/rayer/.hermes/profiles/lwc-tpm/artifacts/lwc-2026.11.4/review-inputs/LWC-478-pr239-7213d4f3",
))


@unittest.skipUnless(R10_CARRIER_ROOT.is_dir() and R10_AUTHENTIC_METADATA_ROOT.is_dir(),
                     "R10 authentic external carriers and run metadata are not supplied")
class R10NativeReceiptRecoveryTests(unittest.TestCase):
    PROD_RESULT_ID = "14270957011"
    PROD_RUN_ID = "43955019304"
    PROD_RUN_ATTEMPT = 1
    PROD_PACKAGE_ID = "14268698408"
    PROD_RESULT_SHA256 = "eaf0148799e22b1e4db1e93557b4c87d318a748d148c67190639ad1839410ed6"
    DEV_RESULT_SHA256 = "306f681c57f19a9888bfb1c45c86ce564b33edb21d3ead2923cd36987fb67cfc"
    PROD_PACKAGE_SHA256 = "0aa6cc2222100e464512f5fdb16df43e362adecf5f52240b993c8157679aeaf6"

    def copy_result_carrier(self, name, destination):
        path = R10_CARRIER_ROOT / name
        archive_bytes = path.read_bytes()
        self.assertLessEqual(len(archive_bytes), 40 * 1024 * 1024)
        expected_sha = self.PROD_RESULT_SHA256 if name.startswith("production-final-") else self.DEV_RESULT_SHA256
        self.assertEqual(hashlib.sha256(archive_bytes).hexdigest(), expected_sha)
        with zipfile.ZipFile(BytesIO(archive_bytes)) as archive:
            names = set(archive.namelist())
            required = {"plan.json", "state.json", "result.json", "receipts/frontend.json"}
            if name.startswith("production-final-"):
                required.add("receipts/bff.json")
                self.assertIn("release-notes-evidence.json", names)
            self.assertTrue(required <= names)
            self.assertNotIn("release-notes-publication.json", names)
            self.assertNotIn("release-notes-operation.json", names)
            for member in sorted(required | ({"release-notes-evidence.json"} if
                                             "release-notes-evidence.json" in names else set())):
                info = archive.getinfo(member)
                self.assertLessEqual(info.file_size, release_notes.MAX_OPERATION_ARTIFACT_BYTES)
                self.assertNotEqual((info.external_attr >> 16) & 0o170000, 0o120000)
                target = destination / member
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(archive.read(member))
        return path

    def copy_package_carrier(self, destination):
        path = R10_CARRIER_ROOT / "production-package-14268698408.zip"
        archive_bytes = path.read_bytes()
        self.assertEqual(hashlib.sha256(archive_bytes).hexdigest(), self.PROD_PACKAGE_SHA256)
        expected = {"release-notes-manifest.json", "en.json", "zh-TW.json"}
        with zipfile.ZipFile(BytesIO(archive_bytes)) as archive:
            self.assertEqual(set(archive.namelist()), expected)
            for member in sorted(expected):
                info = archive.getinfo(member)
                self.assertLessEqual(info.file_size, release_notes.MAX_CONTENT_BYTES + 32 * 1024)
                self.assertNotEqual((info.external_attr >> 16) & 0o170000, 0o120000)
                (destination / member).write_bytes(archive.read(member))
        return path

    def target_observation(self, result_directory):
        plan = json.loads((result_directory / "plan.json").read_bytes())
        state = json.loads((result_directory / "state.json").read_bytes())
        environment = {"development": "dev", "production": "prod"}[plan["normalized"]["environment"]]
        service = plan["normalized"]["bff"]["service_name"]
        bff_candidate = state.get("components", {}).get("bff", {}).get("candidate", {})
        bff_receipt_path = result_directory / "receipts/bff.json"
        if bff_receipt_path.exists():
            image = json.loads(bff_receipt_path.read_bytes())["artifact"]["image"]
            revision = bff_candidate["revision"]
            build_info = {
                "environment": environment, "product_version": plan["release_version"],
                "commit": plan["source"], "branch": plan["branch"], "tag": plan["tag"],
                "image_tag": "bff-" + plan["release_version"], "service": service, "revision": revision,
            }
        else:
            revision = "synthetic-current-bff-revision"
            image = "registry.example/bff@sha256:" + "a" * 64
            build_info = {
                "environment": environment, "product_version": "2026.11.3",
                "commit": "b" * 40, "branch": plan["branch"], "tag": "bff-current-2026.11.3",
                "image_tag": "bff-v2026.11.3", "service": service, "revision": revision,
            }
        return {
            "service": service, "service_uid": "synthetic-target-readback-uid", "service_generation": 114,
            "revision": revision, "image": image, "image_digest": image.rsplit("@", 1)[1],
            "traffic": [{"revisionName": revision, "percent": 100}], "build_info": build_info,
            "url": "https://synthetic-current-target.invalid",
        }

    def test_authentic_dev_and_production_frontend_candidates_normalize_through_native_consumer(self):
        fixtures = (
            ("frontend-final-14220263997.zip", "development", "9001", 1),
            ("production-final-14270957011.zip", "production", self.PROD_RUN_ID, self.PROD_RUN_ATTEMPT),
        )
        for name, environment, run_id, attempt in fixtures:
            with self.subTest(carrier=name), TemporaryDirectory(prefix="lwc478-r10-carrier-") as temp:
                result_directory = Path(temp)
                carrier = self.copy_result_carrier(name, result_directory)
                plan_bytes = (result_directory / "plan.json").read_bytes()
                state = json.loads((result_directory / "state.json").read_bytes())
                candidate = state["components"]["frontend"]["candidate"]
                receipt = json.loads((result_directory / "receipts/frontend.json").read_bytes())
                self.assertIn("deployment", candidate)
                self.assertNotIn("deployment_id", candidate)
                self.assertNotIn("deployment_id", receipt["artifact"])
                self.assertEqual(receipt["identity"], json.loads(plan_bytes)["identities"]["frontend"])
                retained = SimpleNamespace(directory=result_directory, plan=str(result_directory / "plan.json"),
                                            environment={"development": "dev", "production": "prod"}[environment],
                                            operation="release", run_id=run_id, run_attempt=attempt)
                native = release_notes_publish.validate_retained_operation(result_directory, retained)
                self.assertEqual(native["plan"]["selected"], json.loads(plan_bytes)["selected"])
                with patch.object(release_notes_capture, "observe_service",
                                  return_value=self.target_observation(result_directory)):
                    marker, synthetic_sidecar = release_notes_capture.capture(
                        result_directory, "release", run_id, attempt, write_sidecar=False)
                build = marker["component_builds"][0]
                self.assertEqual(build["deployment_id"], candidate["deployment"])
                self.assertEqual(build["source_sha"], receipt["build_sha"])
                self.assertEqual(build["build_config_sha256"], receipt["target_config"])
                self.assertEqual(build["receipt_sha256"], release_notes.digest_bytes(
                    (result_directory / "receipts/frontend.json").read_bytes()))
                self.assertTrue(synthetic_sidecar)
                self.assertFalse((result_directory / "release-notes-operation.json").exists())
                if name.startswith("production-final-"):
                    serving = json.loads((R10_CARRIER_ROOT / "production-post-notes-failure-serving.json").read_bytes())
                    active_frontend_ids = {value.get("deploymentId") or value.get("deployment")
                                           for value in serving["aliases"].values()}
                    self.assertEqual(active_frontend_ids, {candidate["deployment"]})
                else:
                    reproduction = json.loads((R10_CARRIER_ROOT / "DEV-notes-coverage-gap-reproduction.json").read_bytes())
                    self.assertEqual(reproduction["error"], "selected-component-receipt-invalid")
                    self.assertEqual(reproduction["DEV_native_candidate"], candidate)

    def authentic_runtime_metadata(self):
        root = R10_AUTHENTIC_METADATA_ROOT
        return tuple(json.loads((root / name).read_bytes()) for name in (
            "authentic-result_metadata.json", "authentic-run.json",
            "authentic-original-runtime-jobs.json", "authentic-package_metadata.json",
            "authentic-package_run.json"))

    def run_official_cli(self, script, arguments):
        environment = os.environ.copy()
        environment.update({
            "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
            "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
            "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
            "GCE_METADATA_HOST": "127.0.0.1:9",
            "AWS_EC2_METADATA_DISABLED": "true",
            "HTTP_PROXY": "http://127.0.0.1:9", "HTTPS_PROXY": "http://127.0.0.1:9",
            "ALL_PROXY": "http://127.0.0.1:9", "http_proxy": "http://127.0.0.1:9",
            "https_proxy": "http://127.0.0.1:9", "all_proxy": "http://127.0.0.1:9",
            "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
        })
        for name in ("GH_TOKEN", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN", "ACTIONS_RUNTIME_URL"):
            environment.pop(name, None)
        return subprocess.run([sys.executable, str(script), *map(str, arguments)], cwd=ROOT,
                              env=environment, text=True, capture_output=True, check=False)

    def make_runtime_completion(self, result_directory, package_directory, proof_path=None):
        result_metadata, run, jobs, package_metadata, package_run = self.authentic_runtime_metadata()
        result = release_notes_workflow.validate_original_runtime_completion(
            result_metadata, run, jobs, package_metadata, package_run,
            result_directory=result_directory, package_directory=package_directory,
            expected_result_artifact_id=self.PROD_RESULT_ID, expected_package_artifact_id=self.PROD_PACKAGE_ID,
            environment="production", operation="release", repository="Rayer/llm-wiki-cloud",
            output_path=str(proof_path) if proof_path else None)
        return result, result_metadata, run, jobs, package_metadata, package_run

    def recovery_cli_args(self, result_directory, package_directory, proof_path):
        _, _, _, package_metadata, package_run = self.authentic_runtime_metadata()
        package_digest = package_metadata["digest"]
        return [
            "--action", "resend", "--directory", str(result_directory), "--package-dir", str(package_directory),
            "--environment", "prod", "--bucket", "synthetic-no-cloud", "--artifact-id", self.PROD_PACKAGE_ID,
            "--result-artifact-id", self.PROD_RESULT_ID, "--artifact-digest", package_digest,
            "--artifact-metadata-digest", package_digest, "--artifact-name", package_metadata["name"],
            "--package-run-id", str(package_run["id"]), "--package-run-attempt", str(package_run["run_attempt"]),
            "--runtime-completion-evidence", str(proof_path),
            "--plan", str(result_directory / "plan.json"), "--operation", "release",
            "--run-id", self.PROD_RUN_ID, "--run-attempt", str(self.PROD_RUN_ATTEMPT),
            "--product-status", "released",
        ]

    def recovery_args(self, result_directory, package_directory, proof_path):
        return release_notes_publish.parser().parse_args(
            self.recovery_cli_args(result_directory, package_directory, proof_path))

    def test_original_successful_step_time_recovers_both_missing_sidecars_and_verifies_notes_reader(self):
        with TemporaryDirectory(prefix="lwc478-r10-recovery-") as temp:
            root = Path(temp)
            result_directory = root / "release"
            package_directory = root / "package"
            result_directory.mkdir()
            package_directory.mkdir()
            carrier = self.copy_result_carrier("production-final-14270957011.zip", result_directory)
            package_carrier = self.copy_package_carrier(package_directory)
            self.assertFalse((result_directory / "release-notes-publication.json").exists())
            self.assertFalse((result_directory / "release-notes-operation.json").exists())

            metadata_root = R10_AUTHENTIC_METADATA_ROOT
            original_failure_stages = json.loads(
                (result_directory / "release-notes-evidence.json").read_bytes())["stages"]
            verify_operation = self.run_official_cli(
                ROOT / "deploy/engine/release_notes_publish.py", [
                    "--action", "verify-operation", "--directory", result_directory,
                    "--environment", "prod", "--bucket", "synthetic-no-cloud",
                    "--plan", result_directory / "plan.json", "--result-artifact-id", self.PROD_RESULT_ID,
                    "--operation", "release", "--run-id", self.PROD_RUN_ID,
                    "--run-attempt", str(self.PROD_RUN_ATTEMPT), "--product-status", "released",
                ])
            self.assertEqual(verify_operation.returncode, 0, verify_operation.stdout + verify_operation.stderr)
            self.assertEqual(json.loads(verify_operation.stdout), {
                "action": "verify-operation", "stage": "product_release",
                "status": "verified", "product_status": "released",
            })
            appended_evidence = json.loads((result_directory / "release-notes-evidence.json").read_bytes())
            self.assertEqual([stage["action"] for stage in appended_evidence["stages"]],
                             ["prepare", "record-publication", "verify-operation"])
            self.assertEqual(appended_evidence["stages"][:len(original_failure_stages)], original_failure_stages)
            appended_verify = appended_evidence["stages"][-1]
            self.assertEqual(appended_verify["event_sequence"], 3)
            self.assertEqual(appended_verify["stage_attempt"], 1)
            self.assertEqual(appended_verify["run_id"], self.PROD_RUN_ID)
            self.assertEqual(appended_verify["run_attempt"], self.PROD_RUN_ATTEMPT)
            self.assertFalse((result_directory / "release-notes-publication.json").exists())
            self.assertFalse((result_directory / "release-notes-operation.json").exists())

            package_outputs = root / "package-github-output"
            package_validation = self.run_official_cli(
                ROOT / "deploy/engine/release_notes_workflow.py", [
                    "validate-resend-package-artifact",
                    "--metadata", metadata_root / "authentic-package_metadata.json",
                    "--run", metadata_root / "authentic-package_run.json",
                    "--artifact-id", self.PROD_PACKAGE_ID,
                    "--result-artifact-id", self.PROD_RESULT_ID,
                    "--operation", "release", "--environment", "production",
                    "--repository", "Rayer/llm-wiki-cloud", "--output", package_outputs,
                ])
            self.assertEqual(package_validation.returncode, 0,
                             package_validation.stdout + package_validation.stderr)
            self.assertIn("artifact_name=lwc-release-notes-2026.11.4-production-43954659089-1",
                          package_outputs.read_text())
            self.assertIn("package_run_id=43954659089", package_outputs.read_text())

            proof_path = result_directory / "runtime-completion.json"
            runtime_validation = self.run_official_cli(
                ROOT / "deploy/engine/release_notes_workflow.py", [
                    "validate-original-runtime-completion",
                    "--result-metadata", metadata_root / "authentic-result_metadata.json",
                    "--run", metadata_root / "authentic-run.json",
                    "--jobs", metadata_root / "authentic-original-runtime-jobs.json",
                    "--package-metadata", metadata_root / "authentic-package_metadata.json",
                    "--package-run", metadata_root / "authentic-package_run.json",
                    "--result-directory", result_directory, "--package-directory", package_directory,
                    "--result-artifact-id", self.PROD_RESULT_ID,
                    "--package-artifact-id", self.PROD_PACKAGE_ID,
                    "--environment", "production", "--operation", "release",
                    "--repository", "Rayer/llm-wiki-cloud", "--output", proof_path,
                ])
            self.assertEqual(runtime_validation.returncode, 0,
                             runtime_validation.stdout + runtime_validation.stderr)
            proof = json.loads(proof_path.read_bytes())
            self.assertEqual(json.loads(runtime_validation.stdout), proof)
            original_time = json.loads((R10_CARRIER_ROOT / "production-partial-release-time.json").read_bytes())[
                "runtime_success_completed_at"]
            self.assertEqual(proof["runtime_step_completed_at"], original_time)
            self.assertEqual(proof["plan_sha256"], release_notes.digest_bytes((result_directory / "plan.json").read_bytes()))
            self.assertFalse((result_directory / "release-notes-publication.json").exists())
            self.assertFalse((result_directory / "release-notes-operation.json").exists())

            observation = self.target_observation(result_directory)
            with patch.object(release_notes_capture, "observe_service", return_value=observation):
                marker, _ = release_notes_capture.capture(
                    result_directory, "release", self.PROD_RUN_ID, self.PROD_RUN_ATTEMPT, write_sidecar=False)
            self.assertEqual(marker["component_builds"][0]["deployment_id"],
                             json.loads((result_directory / "state.json").read_bytes())[
                                 "components"]["frontend"]["candidate"]["deployment"])

            class ControlledNotesStore(MemoryNotesStore):
                def active_marker_matches(self, candidate, *, directory, operation, run_id, run_attempt):
                    return release_notes_capture.active_marker_matches(
                        directory, operation, run_id, run_attempt, candidate)

            store = ControlledNotesStore()
            content_files = {locale: (package_directory / f"{locale}.json").read_bytes()
                             for locale in ("en", "zh-TW")}

            class Handler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    index = release_notes.validate_index(store.index)
                    version = marker["release_version"]
                    current_row = next(row for row in index["versions"] if row["product_version"] == version)
                    if self.path == "/api/v1/public/release-notes":
                        details, components = release_notes_publish._public_components(marker)
                        value = {
                            "schema_version": 1, "environment": "prod",
                            "runtime_product_version": marker["product_version"], "current_status": "available",
                            "current_product_version": version,
                            "current": {"product_version": current_row["product_version"],
                                        "published_at": current_row["published_at"],
                                        "locales": sorted(current_row["locales"])},
                            "history": [{"product_version": row["product_version"],
                                         "published_at": row["published_at"],
                                         "locales": sorted(row["locales"])}
                                        for row in index["versions"] if row["product_version"] != version],
                            "component_details_status": details, "current_components": components,
                            "compatibility_aliases": [],
                        }
                    else:
                        value = None
                        for locale, body in content_files.items():
                            endpoint = f"/api/v1/public/release-notes/{version}?locale={locale}"
                            if self.path == endpoint:
                                value = {
                                    **json.loads(body), "published_at": current_row["published_at"],
                                    "requested_locale": locale, "locale_fallback": False, "is_current": True,
                                    "requested_product_version": version, "canonical_product_version": version,
                                    "legacy_alias": False, "legacy_fallback": False,
                                }
                                break
                        if value is None:
                            self.send_error(404)
                            return
                    encoded = canonical(value)
                    self.send_response(200)
                    self.send_header("Cache-Control", "no-store")
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(encoded)

                def log_message(self, _format, *_args):
                    pass

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                publisher_cli_args = self.recovery_cli_args(result_directory, package_directory, proof_path)
                active_url = f"http://127.0.0.1:{server.server_port}"
                publisher_stdout = StringIO()
                with patch.dict(os.environ, {"GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                                             "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
                                             "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
                                             "GCE_METADATA_HOST": "127.0.0.1:9",
                                             "HTTP_PROXY": "http://127.0.0.1:9",
                                             "HTTPS_PROXY": "http://127.0.0.1:9",
                                             "ALL_PROXY": "http://127.0.0.1:9",
                                             "http_proxy": "http://127.0.0.1:9",
                                             "https_proxy": "http://127.0.0.1:9",
                                             "all_proxy": "http://127.0.0.1:9",
                                             "NO_PROXY": "127.0.0.1,localhost",
                                             "no_proxy": "127.0.0.1,localhost"}), \
                        patch.object(release_notes_capture, "observe_service", return_value=observation), \
                        patch.object(release_notes_capture, "provider_json",
                                     side_effect=AssertionError("provider access is blocked in this local fixture")), \
                        patch.object(release_notes_capture, "build_info",
                                     side_effect=AssertionError("provider access is blocked in this local fixture")), \
                        patch.object(release_notes_capture, "active_service_url", return_value=active_url), \
                        patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                        patch.object(sys, "argv", [str(ROOT / "deploy/engine/release_notes_publish.py"),
                                                    *publisher_cli_args]), \
                        redirect_stdout(publisher_stdout):
                    publisher_exit = release_notes_publish.main()
            finally:
                server.shutdown()
                server.server_close()
                thread.join(timeout=2)

            self.assertEqual(publisher_exit, 0, publisher_stdout.getvalue())
            self.assertIn('"status": "verified"', publisher_stdout.getvalue())
            evidence_file = json.loads((result_directory / "release-notes-evidence.json").read_bytes())
            self.assertEqual([stage["action"] for stage in evidence_file["stages"]],
                             ["prepare", "record-publication", "verify-operation", "resend"])
            evidence = evidence_file["stages"][-1]
            self.assertEqual(evidence["status"], "verified")
            self.assertEqual(evidence["currentness"], "verified")
            self.assertEqual(evidence["published_at"], "2026-11-16T21:00:38Z")
            self.assertEqual(evidence["published_at_source"], "runtime-step-completion")
            self.assertEqual(evidence["runtime_step_completed_at"], original_time)
            self.assertEqual(evidence["runtime_completion_evidence_sha256"],
                             release_notes.digest_bytes(proof_path.read_bytes()))
            self.assertEqual(evidence["content_sha256"], {
                locale: release_notes.digest_bytes(body) for locale, body in content_files.items()})
            self.assertEqual(set(evidence["reader_content_sha256"]), {"en", "zh-TW"})
            self.assertEqual(evidence["reader_api_readback"]["reader_current_status"], "available")
            self.assertEqual(evidence["reader_api_readback"]["reader_current_product_version"], "2026.11.4")
            self.assertEqual(evidence["reader_api_readback"]["reader_verified_versions"], ["2026.11.4"])
            self.assertEqual(evidence["reader_api_readback"]["reader_current_flags"], {"en": True, "zh-TW": True})
            self.assertEqual(json.loads(store.index)["current_deployment"], marker)
            self.assertEqual([row["product_version"] for row in release_notes.validate_index(store.index)["versions"]],
                             ["2026.11.4"])
            publication = json.loads((result_directory / "release-notes-publication.json").read_bytes())
            self.assertEqual(publication["published_at"], original_time)
            self.assertTrue((result_directory / "release-notes-operation.json").is_file())
            self.assertEqual(hashlib.sha256(carrier.read_bytes()).hexdigest(), self.PROD_RESULT_SHA256)
            self.assertEqual(hashlib.sha256(package_carrier.read_bytes()).hexdigest(), self.PROD_PACKAGE_SHA256)

    def test_runtime_completion_rejects_foreign_repository_and_attempt(self):
        with TemporaryDirectory(prefix="lwc478-r10-invalid-proof-") as temp:
            root = Path(temp)
            result_directory, package_directory = root / "release", root / "package"
            result_directory.mkdir()
            package_directory.mkdir()
            self.copy_result_carrier("production-final-14270957011.zip", result_directory)
            self.copy_package_carrier(package_directory)
            _, result_metadata, run, jobs, package_metadata, package_run = self.make_runtime_completion(
                result_directory, package_directory)

            foreign_run = copy.deepcopy(run)
            foreign_run["repository"]["full_name"] = "Rayer/other-repository"
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-result-identity-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    result_metadata, foreign_run, jobs, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

            wrong_source_run = copy.deepcopy(run)
            wrong_source_metadata = copy.deepcopy(result_metadata)
            wrong_source_run["head_sha"] = "f" * 40
            wrong_source_metadata["workflow_run"]["head_sha"] = "f" * 40
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-plan-identity-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    wrong_source_metadata, wrong_source_run, jobs, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

            wrong_attempt_jobs = copy.deepcopy(jobs)
            wrong_attempt_jobs["jobs"][0]["run_attempt"] += 1
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-release-job-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    result_metadata, run, wrong_attempt_jobs, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

            wrong_job_name = copy.deepcopy(jobs)
            wrong_job_name["jobs"][0]["name"] = "release"
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-release-job-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    result_metadata, run, wrong_job_name, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

            unsuccessful_runtime_step = copy.deepcopy(jobs)
            next(step for step in unsuccessful_runtime_step["jobs"][0]["steps"]
                 if step.get("name") == release_notes_workflow.RUNTIME_STEP_NAME)["conclusion"] = "failure"
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-step-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    result_metadata, run, unsuccessful_runtime_step, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

            failure_path = result_directory / "release-notes-evidence.json"
            failure_evidence = json.loads(failure_path.read_bytes())
            failure_evidence["stages"][-1]["safe_class"] = "unrelated-publication-failure"
            failure_path.write_bytes(canonical(failure_evidence) + b"\n")
            with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                       "runtime-completion-release-failure-invalid"):
                release_notes_workflow.validate_original_runtime_completion(
                    result_metadata, run, jobs, package_metadata, package_run,
                    result_directory=result_directory, package_directory=package_directory,
                    expected_result_artifact_id=self.PROD_RESULT_ID,
                    expected_package_artifact_id=self.PROD_PACKAGE_ID,
                    environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

    def test_production_current_package_uses_its_exact_main_wrapper_and_rejects_history(self):
        result_metadata, run, jobs, package_metadata, package_run = self.authentic_runtime_metadata()
        accepted = release_notes_workflow.validate_resend_package_artifact(
            package_metadata, package_run, expected_artifact_id=self.PROD_PACKAGE_ID,
            result_artifact_id=self.PROD_RESULT_ID, operation="release",
            environment="production", repository="Rayer/llm-wiki-cloud")
        self.assertFalse(accepted["historical"])
        self.assertEqual(accepted["artifact_name"], package_metadata["name"])
        self.assertEqual(accepted["package_run_id"], str(package_run["id"]))
        self.assertEqual(accepted["package_run_attempt"], "1")

        for label, environment, operation in (
                ("wrong environment", "development", "release"),
                ("wrong operation", "production", "deploy")):
            with self.subTest(label=label), self.assertRaisesRegex(
                    release_notes_workflow.PackageWorkflowError, "package-artifact-not-qualified"):
                release_notes_workflow.validate_resend_package_artifact(
                    package_metadata, package_run, expected_artifact_id=self.PROD_PACKAGE_ID,
                    result_artifact_id=self.PROD_RESULT_ID, operation=operation,
                    environment=environment, repository="Rayer/llm-wiki-cloud")

        wrong_branch = copy.deepcopy(package_run)
        wrong_branch["head_branch"] = "develop"
        wrong_branch["repository"]["full_name"] = "Rayer/llm-wiki-cloud"
        with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                   "package-artifact-not-qualified"):
            release_notes_workflow.validate_resend_package_artifact(
                package_metadata, wrong_branch, expected_artifact_id=self.PROD_PACKAGE_ID,
                result_artifact_id=self.PROD_RESULT_ID, operation="release",
                environment="production", repository="Rayer/llm-wiki-cloud")

        foreign_repository = copy.deepcopy(package_run)
        foreign_repository["repository"]["full_name"] = "Rayer/other-repository"
        with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                   "package-artifact-not-qualified"):
            release_notes_workflow.validate_resend_package_artifact(
                package_metadata, foreign_repository, expected_artifact_id=self.PROD_PACKAGE_ID,
                result_artifact_id=self.PROD_RESULT_ID, operation="release",
                environment="production", repository="Rayer/llm-wiki-cloud")

        production_history = copy.deepcopy(package_metadata)
        production_history["name"] = (
            "lwc-release-notes-2026.11.4-development-history-release-"
            f"{self.PROD_RESULT_ID}-{package_run['id']}-{package_run['run_attempt']}"
        )
        with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                   "package-artifact-not-qualified"):
            release_notes_workflow.validate_resend_package_artifact(
                production_history, package_run, expected_artifact_id=self.PROD_PACKAGE_ID,
                result_artifact_id=self.PROD_RESULT_ID, operation="release",
                environment="production", repository="Rayer/llm-wiki-cloud")

    def test_runtime_completion_rejects_unbound_verify_operation_tail(self):
        for label in ("wrong run identity", "boolean schema version", "unknown event after verification"):
            with self.subTest(label=label), TemporaryDirectory(prefix="lwc478-r10-verify-tail-") as temp:
                root = Path(temp)
                result_directory, package_directory = root / "release", root / "package"
                result_directory.mkdir()
                package_directory.mkdir()
                self.copy_result_carrier("production-final-14270957011.zip", result_directory)
                self.copy_package_carrier(package_directory)
                verified = self.run_official_cli(
                    ROOT / "deploy/engine/release_notes_publish.py", [
                        "--action", "verify-operation", "--directory", result_directory,
                        "--environment", "prod", "--bucket", "synthetic-no-cloud",
                        "--plan", result_directory / "plan.json", "--result-artifact-id", self.PROD_RESULT_ID,
                        "--operation", "release", "--run-id", self.PROD_RUN_ID,
                        "--run-attempt", str(self.PROD_RUN_ATTEMPT), "--product-status", "released",
                    ])
                self.assertEqual(verified.returncode, 0, verified.stdout + verified.stderr)
                evidence_path = result_directory / "release-notes-evidence.json"
                evidence = json.loads(evidence_path.read_bytes())
                if label == "wrong run identity":
                    evidence["stages"][-1]["run_id"] = "43955019305"
                elif label == "boolean schema version":
                    evidence["stages"][-1]["schema_version"] = True
                else:
                    evidence["stages"].append({"action": "unexpected-resend-stage", "status": "verified"})
                evidence_path.write_bytes(canonical(evidence) + b"\n")
                result_metadata, run, jobs, package_metadata, package_run = self.authentic_runtime_metadata()
                with self.assertRaisesRegex(release_notes_workflow.PackageWorkflowError,
                                           "runtime-completion-release-failure-invalid"):
                    release_notes_workflow.validate_original_runtime_completion(
                        result_metadata, run, jobs, package_metadata, package_run,
                        result_directory=result_directory, package_directory=package_directory,
                        expected_result_artifact_id=self.PROD_RESULT_ID,
                        expected_package_artifact_id=self.PROD_PACKAGE_ID,
                        environment="production", operation="release", repository="Rayer/llm-wiki-cloud")

    def test_arbitrary_timestamp_does_not_replace_missing_original_publication_record(self):
        with TemporaryDirectory(prefix="lwc478-r10-unbound-time-") as temp:
            root = Path(temp)
            result_directory, package_directory = root / "release", root / "package"
            result_directory.mkdir()
            package_directory.mkdir()
            self.copy_result_carrier("production-final-14270957011.zip", result_directory)
            self.copy_package_carrier(package_directory)
            args = self.recovery_args(result_directory, package_directory, "")
            args.published_at = "2026-11-16T21:00:38Z"
            with patch.object(release_notes_publish, "GCSCLIStore", return_value=MemoryNotesStore()):
                with self.assertRaisesRegex(release_notes.ReleaseNotesError,
                                            "retained-publication-time-unavailable"):
                    release_notes_publish.execute(args)
            self.assertFalse((result_directory / "release-notes-publication.json").exists())
            self.assertFalse((result_directory / "release-notes-operation.json").exists())

    def test_stale_target_rejects_recovery_before_notes_upload(self):
        with TemporaryDirectory(prefix="lwc478-r10-stale-target-") as temp:
            root = Path(temp)
            result_directory, package_directory = root / "release", root / "package"
            result_directory.mkdir()
            package_directory.mkdir()
            self.copy_result_carrier("production-final-14270957011.zip", result_directory)
            self.copy_package_carrier(package_directory)
            proof_path = result_directory / "runtime-completion.json"
            self.make_runtime_completion(result_directory, package_directory, proof_path)
            current = self.target_observation(result_directory)
            stale = copy.deepcopy(current)
            stale["service_generation"] += 1
            args = self.recovery_args(result_directory, package_directory, proof_path)
            with patch.dict(os.environ, {"GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud"}), \
                    patch.object(release_notes_capture, "observe_service", side_effect=[current, stale]), \
                    patch.object(release_notes_publish, "GCSCLIStore",
                                 side_effect=AssertionError("Notes storage access is blocked in this fixture")):
                with self.assertRaisesRegex(release_notes.ReleaseNotesError, "release-transition-stale"):
                    release_notes_publish.execute(args)
            self.assertFalse((result_directory / "release-notes-publication.json").exists())
            self.assertTrue((result_directory / "release-notes-operation.json").is_file())

    def test_recovery_caller_is_the_actual_production_wrapper_and_keeps_attempt_binding(self):
        production = yaml.safe_load((ROOT / ".github/workflows/promote-production.yml").read_text())
        reusable = production["jobs"]["release"]
        self.assertEqual(reusable["uses"], "./.github/workflows/cd.yml")
        self.assertEqual(reusable["secrets"], "inherit")

        cd = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        steps = cd["jobs"]["notes-resend"]["steps"]
        verify_operation = next(step for step in steps
                                if step.get("name") == "Verify the retained product operation succeeded")
        validation = next(step for step in steps if step.get("id") == "runtime-completion")
        package_validation = next(step for step in steps
                                  if step.get("name") == "Inspect exact TPM Release Notes artifact")
        package_download = next(step for step in steps
                                if step.get("name") == "Download exact TPM Release Notes artifact")
        provider_auth = next(step for step in steps
                             if step.get("uses", "").startswith("google-github-actions/auth@"))
        publisher = next(step for step in steps if step.get("name") == "Resend Notes without product deployment")
        self.assertLess(steps.index(verify_operation), steps.index(package_validation))
        self.assertLess(steps.index(package_validation), steps.index(package_download))
        self.assertLess(steps.index(package_download), steps.index(validation))
        self.assertLess(steps.index(validation), next(
            index for index, step in enumerate(steps) if step.get("name") == "Resend Notes without product deployment"))
        self.assertLess(steps.index(validation), steps.index(provider_auth))
        self.assertLess(steps.index(provider_auth), steps.index(publisher))
        self.assertIn(
            "actions/runs/$ORIGINAL_RUN_ID/attempts/$ORIGINAL_RUN_ATTEMPT/jobs?per_page=100",
            validation["run"])
        self.assertIn("validate-original-runtime-completion", validation["run"])
        self.assertIn('--environment "$RESULT_ENVIRONMENT"', package_validation["run"])
        self.assertIn('--repository "$GITHUB_REPOSITORY"', package_validation["run"])
        self.assertIn("--runtime-completion-evidence", publisher["run"])
        self.assertNotIn("--published-at", publisher["run"])

if __name__ == "__main__":
    unittest.main()
