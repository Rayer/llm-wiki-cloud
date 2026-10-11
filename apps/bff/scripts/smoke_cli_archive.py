import argparse
import hashlib
import json
import subprocess
import tarfile
import tempfile
import zipfile
from pathlib import Path


def smoke(release_dir: Path, version: str, goos: str, goarch: str) -> None:
    manifest = json.loads((release_dir / "manifest.json").read_text(encoding="utf-8"))
    if manifest.get("version") != version or manifest.get("source_commit") is None:
        raise ValueError("release manifest identity does not match the smoke inputs")
    asset = next((entry for entry in manifest.get("assets", []) if entry.get("os") == goos and entry.get("arch") == goarch), None)
    if not asset:
        raise ValueError(f"no archive for {goos}/{goarch}")
    archive_path = release_dir / asset["asset_name"]
    archive = archive_path.read_bytes()
    if len(archive) != asset["size"] or hashlib.sha256(archive).hexdigest() != asset["sha256"]:
        raise ValueError("archive size or SHA-256 does not match the manifest")

    executable = "lwc-sync.exe" if goos == "windows" else "lwc-sync"
    with tempfile.TemporaryDirectory() as temporary:
        binary_path = Path(temporary) / executable
        if goos == "windows":
            with zipfile.ZipFile(archive_path) as package:
                binary_path.write_bytes(package.read(executable))
        else:
            with tarfile.open(archive_path, "r:gz") as package:
                member = package.getmember(executable)
                stream = package.extractfile(member)
                if stream is None or not member.isfile():
                    raise ValueError("archive does not contain a regular CLI binary")
                binary_path.write_bytes(stream.read())
                binary_path.chmod(0o755)
        result = subprocess.run([str(binary_path), "--version"], check=True, capture_output=True, text=True)
        expected = f"lwc-sync {version}\n"
        if result.stdout != expected:
            raise ValueError(f"--version output {result.stdout!r} does not match {expected!r}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--release-dir", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--goos", required=True)
    parser.add_argument("--goarch", required=True)
    args = parser.parse_args()
    smoke(args.release_dir, args.version, args.goos, args.goarch)
    print(f"PASS: extracted {args.goos}/{args.goarch} archive reports lwc-sync {args.version}.")


if __name__ == "__main__":
    main()
