import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

from normalize_cli_release_event import normalize_event
from validate_cli_source import validate


ROOT = Path(__file__).resolve().parents[3]
WORKFLOW = ROOT / ".github" / "workflows" / "cli-release.yml"


class CLIReleaseEventTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        subprocess.run(["git", "config", "user.name", "CLI Release Event Test"], cwd=self.root, check=True)
        subprocess.run(["git", "config", "user.email", "cli-release-event@example.invalid"], cwd=self.root, check=True)
        (self.root / "README.md").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["git", "add", "README.md"], cwd=self.root, check=True)
        subprocess.run(["git", "commit", "-qm", "fixture"], cwd=self.root, check=True)
        self.sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.root, text=True).strip()

    def tearDown(self):
        self.temporary.cleanup()

    def tag(self, version):
        ref = f"refs/tags/cli/v{version}"
        subprocess.run(["git", "tag", f"cli/v{version}"], cwd=self.root, check=True)
        return ref

    def validate_tag_push(self, version):
        ref = self.tag(version)
        identity = normalize_event("push", ref, self.sha)
        source = validate(
            self.root,
            identity["publish"] == "true",
            identity["version"],
            identity["channel"],
            identity["source_revision"],
            ref,
            self.sha,
        )
        self.assertEqual(source, self.sha)
        return identity

    def test_push_tags_map_stable_prerelease_and_build_metadata(self):
        cases = (
            ("1.2.3", "stable"),
            ("1.3.0-rc.1", "prerelease"),
            ("1.2.3+build-ci.5", "stable"),
            ("1.3.0-rc.2+build-ci.5", "prerelease"),
        )
        for version, channel in cases:
            with self.subTest(version=version):
                identity = self.validate_tag_push(version)
                self.assertEqual(identity["channel"], channel)
                self.assertEqual(identity["publish"], "true")

    def test_invalid_tag_version_is_rejected_by_shared_validator(self):
        ref = self.tag("1.2")
        identity = normalize_event("push", ref, self.sha)
        with self.assertRaisesRegex(ValueError, "valid full SemVer"):
            validate(self.root, True, identity["version"], identity["channel"], identity["source_revision"], ref, self.sha)

    def test_push_requires_cli_version_tag_and_full_source_sha(self):
        with self.assertRaisesRegex(ValueError, "refs/tags/cli/v"):
            normalize_event("push", "refs/tags/v1.2.3", self.sha)
        with self.assertRaisesRegex(ValueError, "full Git SHA"):
            normalize_event("push", "refs/tags/cli/v1.2.3", "not-a-sha")

    def test_normalizer_cli_writes_event_outputs(self):
        output = self.root / "GITHUB_OUTPUT"
        result = subprocess.run(
            [
                sys.executable,
                str(ROOT / "apps" / "bff" / "scripts" / "normalize_cli_release_event.py"),
                "--event", "push",
                "--ref", "refs/tags/cli/v1.2.3",
                "--sha", self.sha,
                "--output", str(output),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            output.read_text(encoding="utf-8"),
            f"publish=true\nversion=1.2.3\nchannel=stable\nsource_revision={self.sha}\n",
        )

    def test_manual_defaults_remain_nonpublishing_dev_on_selected_commit(self):
        identity = normalize_event("workflow_dispatch", "refs/heads/feature", self.sha)
        self.assertEqual(identity, {
            "publish": "false",
            "version": "dev",
            "channel": "dev",
            "source_revision": self.sha,
        })
        validate(self.root, False, "dev", "dev", identity["source_revision"], "refs/heads/feature", self.sha)

    def test_manual_tag_publish_preserves_inputs_and_validator_identity(self):
        ref = self.tag("1.2.3")
        identity = normalize_event(
            "workflow_dispatch", ref, self.sha, "true", "1.2.3", "stable", ""
        )
        validate(self.root, True, identity["version"], identity["channel"], identity["source_revision"], ref, self.sha)

    def test_manual_source_mismatch_and_dirty_tree_still_fail(self):
        ref = self.tag("1.2.3")
        mismatch = normalize_event(
            "workflow_dispatch", ref, self.sha, "true", "1.2.3", "stable", "1" * 40
        )
        with self.assertRaisesRegex(ValueError, "source_revision must exactly match"):
            validate(self.root, True, mismatch["version"], mismatch["channel"], mismatch["source_revision"], ref, self.sha)

        (self.root / "dirty.txt").write_text("dirty\n", encoding="utf-8")
        default = normalize_event("workflow_dispatch", "refs/heads/feature", self.sha)
        with self.assertRaisesRegex(ValueError, "not clean"):
            validate(self.root, False, default["version"], default["channel"], default["source_revision"], "refs/heads/feature", self.sha)

    def test_unsafe_manual_output_values_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "unsafe characters"):
            normalize_event("workflow_dispatch", "refs/heads/feature", self.sha, "false", "dev\npublish=true", "dev", "")

    def test_workflow_wires_tag_and_manual_callers_into_validator_and_publisher(self):
        workflow = yaml.load(WORKFLOW.read_text(encoding="utf-8"), Loader=yaml.BaseLoader)
        triggers = workflow["on"]
        self.assertEqual(triggers["push"]["tags"], ["cli/v*"])
        inputs = triggers["workflow_dispatch"]["inputs"]
        self.assertEqual(inputs["publish"]["default"], "false")
        self.assertEqual(inputs["version"]["default"], "dev")
        self.assertEqual(inputs["channel"]["default"], "dev")

        validate_job = workflow["jobs"]["validate"]
        steps = {step.get("id"): step for step in validate_job["steps"] if step.get("id")}
        self.assertIn("normalize_cli_release_event.py", steps["release"]["run"])
        self.assertEqual(steps["release"]["env"]["EVENT_NAME"], "${{ github.event_name }}")
        self.assertEqual(steps["release"]["env"]["EVENT_REF"], "${{ github.ref }}")
        self.assertEqual(steps["release"]["env"]["EVENT_SHA"], "${{ github.sha }}")
        self.assertIn("validate_cli_source.py", steps["identity"]["run"])
        self.assertEqual(steps["identity"]["env"]["PUBLISH"], "${{ steps.release.outputs.publish }}")
        self.assertEqual(validate_job["outputs"]["source_sha"], "${{ steps.identity.outputs.source_sha }}")

        publisher = workflow["jobs"]["publish"]
        self.assertEqual(publisher["if"], "${{ needs.validate.outputs.publish == 'true' }}")
        self.assertEqual(publisher["needs"], ["validate", "build", "package", "native-smoke"])
        self.assertEqual(publisher["permissions"], {"contents": "read", "id-token": "write"})
        auth = next(step for step in publisher["steps"] if "google-github-actions/auth" in step.get("uses", ""))
        self.assertEqual(auth["with"]["workload_identity_provider"], "${{ secrets.WIF_PROVIDER }}")
        self.assertEqual(auth["with"]["service_account"], "${{ secrets.WIF_SERVICE_ACCOUNT }}")
        bucket_check = next(step for step in publisher["steps"] if step.get("name") == "Require configured release resource references")
        self.assertEqual(bucket_check["env"]["RELEASE_BUCKET"], "llm-wiki-frontend-config-dev")


if __name__ == "__main__":
    unittest.main()
