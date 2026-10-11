import hashlib
import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

from package_cli_release import archive_tar_gz, archive_zip, package


class PackageCLIReleaseTests(unittest.TestCase):
    def test_archives_have_fixed_names_and_repeatable_bytes(self):
        self.assertEqual(archive_tar_gz(b"binary", b"readme"), archive_tar_gz(b"binary", b"readme"))
        archive = archive_zip(b"binary", b"readme")
        with zipfile.ZipFile(io.BytesIO(archive)) as packed:
            self.assertEqual(packed.namelist(), ["lwc-sync.exe", "README.md"])
            self.assertEqual(packed.read("lwc-sync.exe"), b"binary")

    def test_packages_all_targets_with_hashes_and_without_unpublished_urls(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary_dir = root / "bin"
            for platform, filename in (("darwin-arm64", "lwc-sync"), ("darwin-amd64", "lwc-sync"), ("windows-amd64", "lwc-sync.exe"), ("linux-amd64", "lwc-sync")):
                directory = binary_dir / platform
                directory.mkdir(parents=True)
                (directory / filename).write_bytes(platform.encode())
            readme = root / "README.md"
            readme.write_text("CLI guide\n", encoding="utf-8")

            manifest = package("1.2.3", "0123456789abcdef0123456789abcdef01234567", "stable", binary_dir, readme, root / "out")

            self.assertEqual(len(manifest["assets"]), 4)
            self.assertTrue(all("download_url" not in asset for asset in manifest["assets"]))
            for asset in manifest["assets"]:
                archive = (root / "out" / "releases" / "1.2.3" / asset["asset_name"]).read_bytes()
                self.assertEqual(asset["size"], len(archive))
                self.assertEqual(asset["sha256"], hashlib.sha256(archive).hexdigest())
            saved = json.loads((root / "out" / "releases" / "1.2.3" / "manifest.json").read_text())
            self.assertEqual(saved, manifest)

    def test_rejects_prerelease_on_stable_channel(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.assertRaises(ValueError, package, "1.0.0-rc.1", "0123456789abcdef0123456789abcdef01234567", "stable", root, root / "README.md", root / "out")

    def test_stable_build_metadata_hyphen_is_not_a_prerelease(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary_dir = root / "bin"
            for platform, filename in (("darwin-arm64", "lwc-sync"), ("darwin-amd64", "lwc-sync"), ("windows-amd64", "lwc-sync.exe"), ("linux-amd64", "lwc-sync")):
                directory = binary_dir / platform
                directory.mkdir(parents=True)
                (directory / filename).write_bytes(platform.encode())
            readme = root / "README.md"
            readme.write_text("CLI guide\n", encoding="utf-8")

            manifest = package("1.2.3+build-test", "0123456789abcdef0123456789abcdef01234567", "stable", binary_dir, readme, root / "out")

            self.assertEqual(manifest["version"], "1.2.3+build-test")


if __name__ == "__main__":
    unittest.main()
