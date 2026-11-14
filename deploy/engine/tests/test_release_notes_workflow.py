import sys
import subprocess
import textwrap
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch
import zipfile

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_notes
import release_notes_publish
from test_release_notes import canonical, note, operation_fixture, plan_fixture


ROOT = Path(__file__).resolve().parents[3]
DOWNLOAD_ACTION = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
PINNED_DOWNLOAD_SOURCE = ROOT / "scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts"


class ReleaseNotesWorkflowLayoutTests(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
