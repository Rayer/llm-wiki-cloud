import subprocess
import shutil
import tempfile
import unittest
from pathlib import Path

from validate_cli_source import validate


class ValidateCLISourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        subprocess.run(["git", "config", "user.name", "CLI Release Test"], cwd=self.root, check=True)
        subprocess.run(["git", "config", "user.email", "cli-release-test@example.invalid"], cwd=self.root, check=True)
        (self.root / "README.md").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["git", "add", "README.md"], cwd=self.root, check=True)
        subprocess.run(["git", "commit", "-qm", "fixture"], cwd=self.root, check=True)
        self.sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.root, text=True).strip()

    def tearDown(self):
        self.temporary.cleanup()

    def test_build_only_is_dev_and_uses_selected_commit(self):
        self.assertEqual(validate(self.root, False, "dev", "dev", "", "refs/heads/main", self.sha), self.sha)

    def test_publish_requires_matching_tag_and_source(self):
        subprocess.run(["git", "tag", "cli/v1.2.3"], cwd=self.root, check=True)
        self.assertEqual(validate(self.root, True, "1.2.3", "stable", self.sha, "refs/tags/cli/v1.2.3", self.sha), self.sha)
        with self.assertRaisesRegex(ValueError, "matching cli/v"):
            validate(self.root, True, "1.2.3", "stable", self.sha, "refs/heads/main", self.sha)

    def test_prerelease_and_dirty_source_rules(self):
        subprocess.run(["git", "tag", "cli/v1.3.0-rc.1"], cwd=self.root, check=True)
        validate(self.root, True, "1.3.0-rc.1", "prerelease", self.sha, "refs/tags/cli/v1.3.0-rc.1", self.sha)
        with self.assertRaisesRegex(ValueError, "prerelease channel"):
            validate(self.root, True, "1.3.0-rc.1", "stable", self.sha, "refs/tags/cli/v1.3.0-rc.1", self.sha)
        (self.root / "dirty.txt").write_text("dirty\n", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "not clean"):
            validate(self.root, False, "dev", "dev", self.sha, "refs/heads/main", self.sha)

    def make_fixture(self, with_git=True):
        root = Path(tempfile.mkdtemp(dir=self.temporary.name))
        bff = root / "apps" / "bff"
        scripts = bff / "scripts"
        sync = bff / "cmd" / "sync"
        scripts.mkdir(parents=True)
        sync.mkdir(parents=True)
        source = Path(__file__).resolve().parents[1]
        shutil.copy(source / "Makefile", bff / "Makefile")
        for name in ("validate_cli_source.py", "package_cli_release.py"):
            shutil.copy(source / "scripts" / name, scripts / name)
        (bff / "go.mod").write_text("module cli-fixture\n\ngo 1.23.0\n", encoding="utf-8")
        (sync / "main.go").write_text(
            'package main\nimport "fmt"\nvar version = "dev"\nvar commit = "unknown"\nfunc main() { fmt.Printf("lwc-sync %s %s\\n", version, commit) }\n',
            encoding="utf-8",
        )
        if with_git:
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.name", "CLI Make Test"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "cli-make-test@example.invalid"], cwd=root, check=True)
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "fixture"], cwd=root, check=True)
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip() if with_git else "0123456789abcdef0123456789abcdef01234567"
        return root, sha

    def run_make(self, root, target, *variables, expect_success):
        result = subprocess.run(
            ["make", "-C", str(root / "apps" / "bff"), target, *variables],
            capture_output=True,
            text=True,
            check=False,
        )
        if (result.returncode == 0) != expect_success:
            self.fail(result.stdout + result.stderr)
        return result

    def test_make_cross_build_accepts_matching_tag_and_keeps_parametric_target(self):
        root, sha = self.make_fixture()
        subprocess.run(["git", "tag", "cli/v1.2.3"], cwd=root, check=True)
        cross_output = root.parent / "lwc-sync-linux"
        self.run_make(root, "build-sync-target", "GOOS=linux", "GOARCH=amd64", f"OUTPUT={cross_output}", "CLI_VERSION=1.2.3", f"CLI_COMMIT={sha}", "CLI_CHANNEL=stable", expect_success=True)
        self.assertTrue(cross_output.is_file())
        self.run_make(root, "build-sync", "CLI_VERSION=1.2.3", f"CLI_COMMIT={sha}", "CLI_CHANNEL=stable", expect_success=True)
        output = root / "apps" / "bff" / "lwc-sync"
        self.assertEqual(subprocess.check_output([str(output)], text=True).strip(), f"lwc-sync 1.2.3 {sha}")

    def test_formal_make_builds_reject_invalid_identity_for_both_entrypoints(self):
        for target in ("build-sync", "build-sync-target"):
            with self.subTest(target=target):
                root, sha = self.make_fixture()
                base = ["CLI_VERSION=1.2.3", f"CLI_COMMIT={sha}", "CLI_CHANNEL=stable"]
                target_args = ["GOOS=linux", "GOARCH=amd64", f"OUTPUT={root / 'lwc-sync'}"] if target == "build-sync-target" else []

                result = self.run_make(root, target, *target_args, *base, expect_success=False)
                self.assertIn("matching cli/v", result.stdout + result.stderr)

                subprocess.run(["git", "tag", "cli/v1.2.3"], cwd=root, check=True)
                wrong_commit = self.run_make(root, target, *target_args, *base[:-2], "CLI_COMMIT=0000000000000000000000000000000000000000", "CLI_CHANNEL=stable", expect_success=False)
                self.assertIn("source_revision must exactly match", wrong_commit.stdout + wrong_commit.stderr)

                (root / "dirty.txt").write_text("dirty\n", encoding="utf-8")
                dirty = self.run_make(root, target, *target_args, *base, expect_success=False)
                self.assertIn("not clean", dirty.stdout + dirty.stderr)

                subprocess.run(["git", "tag", "-d", "cli/v1.2.3"], cwd=root, check=True, capture_output=True)
                subprocess.run(["git", "tag", "cli/v1.2.4"], cwd=root, check=True)
                (root / "dirty.txt").unlink()
                mismatch = self.run_make(root, target, *target_args, *base, expect_success=False)
                self.assertIn("matching cli/v", mismatch.stdout + mismatch.stderr)

    def test_formal_make_build_rejects_missing_git_and_dev_build_remains_available(self):
        root, _ = self.make_fixture(with_git=False)
        target_args = ("GOOS=linux", "GOARCH=amd64", f"OUTPUT={root / 'lwc-sync'}")
        for target in ("build-sync", "build-sync-target"):
            target_args_for_make = target_args if target == "build-sync-target" else ()
            result = self.run_make(root, target, *target_args_for_make, "CLI_VERSION=1.2.3", "CLI_COMMIT=0123456789abcdef0123456789abcdef01234567", "CLI_CHANNEL=stable", expect_success=False)
            self.assertNotEqual(result.returncode, 0)

        self.run_make(root, "build-sync-target", *target_args, expect_success=True)
        self.assertTrue((root / "lwc-sync").is_file())


if __name__ == "__main__":
    unittest.main()
