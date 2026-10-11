#!/usr/bin/env python3
import argparse
import gzip
import hashlib
import io
import json
import re
import tarfile
import zipfile
from pathlib import Path


TARGETS = (("darwin", "arm64"), ("darwin", "amd64"), ("windows", "amd64"), ("linux", "amd64"))
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$")
COMMIT = re.compile(r"^[0-9a-f]{40}$")


def archive_tar_gz(binary: bytes, readme: bytes) -> bytes:
    output = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            for name, data, mode in (("lwc-sync", binary, 0o755), ("README.md", readme, 0o644)):
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                entry.mode = mode
                entry.mtime = 0
                entry.uid = 0
                entry.gid = 0
                entry.uname = ""
                entry.gname = ""
                archive.addfile(entry, io.BytesIO(data))
    return output.getvalue()


def archive_zip(binary: bytes, readme: bytes) -> bytes:
    output = io.BytesIO()
    with zipfile.ZipFile(output, mode="w", compression=zipfile.ZIP_STORED) as archive:
        for name, data, mode in (("lwc-sync.exe", binary, 0o755), ("README.md", readme, 0o644)):
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.create_system = 3
            entry.external_attr = (0o100000 | mode) << 16
            archive.writestr(entry, data)
    return output.getvalue()


def package(version: str, commit: str, channel: str, binary_dir: Path, readme_path: Path, output_dir: Path) -> dict:
    if version == "dev":
        if channel != "dev":
            raise ValueError("dev version requires the dev channel")
    elif not SEMVER.fullmatch(version):
        raise ValueError(f"invalid SemVer: {version}")
    elif channel == "stable" and "-" in version:
        raise ValueError("stable channel cannot use a prerelease version")
    elif channel == "prerelease" and "-" not in version:
        raise ValueError("prerelease channel requires a prerelease version")
    elif channel not in {"stable", "prerelease"}:
        raise ValueError(f"unsupported channel: {channel}")
    if not COMMIT.fullmatch(commit):
        raise ValueError("commit must be a full lowercase Git SHA")

    readme = readme_path.read_bytes()
    assets = []
    version_dir = output_dir / "releases" / version
    version_dir.mkdir(parents=True, exist_ok=True)
    checksums = []
    for goos, goarch in TARGETS:
        binary_name = "lwc-sync.exe" if goos == "windows" else "lwc-sync"
        binary_path = binary_dir / f"{goos}-{goarch}" / binary_name
        binary = binary_path.read_bytes()
        suffix = "zip" if goos == "windows" else "tar.gz"
        asset_name = f"lwc-sync_{version}_{goos}_{goarch}.{suffix}"
        archive = archive_zip(binary, readme) if goos == "windows" else archive_tar_gz(binary, readme)
        (version_dir / asset_name).write_bytes(archive)
        digest = hashlib.sha256(archive).hexdigest()
        assets.append({
            "os": goos,
            "arch": goarch,
            "asset_name": asset_name,
            "size": len(archive),
            "sha256": digest,
        })
        checksums.append(f"{digest}  {asset_name}")

    manifest = {
        "schema_version": 1,
        "version": version,
        "source_commit": commit,
        "channel": channel,
        "assets": assets,
    }
    manifest_bytes = (json.dumps(manifest, indent=2, separators=(",", ": ")) + "\n").encode()
    (version_dir / "manifest.json").write_bytes(manifest_bytes)
    (version_dir / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
    return manifest


def main() -> None:
    parser = argparse.ArgumentParser(description="Package deterministic cross-platform lwc-sync archives.")
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--channel", required=True, choices=("stable", "prerelease", "dev"))
    parser.add_argument("--binary-dir", required=True, type=Path)
    parser.add_argument("--readme", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    arguments = parser.parse_args()
    package(arguments.version, arguments.commit, arguments.channel, arguments.binary_dir, arguments.readme, arguments.output_dir)


if __name__ == "__main__":
    main()
