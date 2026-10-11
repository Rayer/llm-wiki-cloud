import subprocess
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


if __name__ == "__main__":
    unittest.main()
