import argparse
import os
import re
import subprocess
import sys

from package_cli_release import COMMIT, SEMVER


def git_value(root, *args):
    return subprocess.check_output(["git", *args], cwd=root, text=True).strip()


def validate(root, publish, version, channel, source_revision, git_ref, github_sha):
    if not COMMIT.fullmatch(github_sha):
        raise ValueError("selected workflow revision is not a full Git SHA")
    source = source_revision or github_sha
    if not COMMIT.fullmatch(source) or source != github_sha or git_value(root, "rev-parse", "HEAD") != github_sha:
        raise ValueError("source_revision must exactly match the selected workflow commit")
    if git_value(root, "status", "--porcelain"):
        raise ValueError("release source checkout is not clean")

    if not publish:
        if version != "dev" or channel != "dev":
            raise ValueError("build-only runs must use version dev and channel dev")
        return source

    if not SEMVER.fullmatch(version):
        raise ValueError("publish requires a valid full SemVer version")
    if git_ref != f"refs/tags/cli/v{version}":
        raise ValueError("publish must run from the matching cli/v<version> tag")
    if git_value(root, "rev-parse", f"{git_ref}^{{commit}}") != source:
        raise ValueError("release tag does not resolve to the selected source revision")
    prerelease = re.search(r"-", version.split("+", 1)[0]) is not None
    expected_channel = "prerelease" if prerelease else "stable"
    if channel != expected_channel:
        raise ValueError(f"version requires the {expected_channel} channel")
    return source


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    publish = os.environ.get("PUBLISH", "false").lower() == "true"
    version = os.environ.get("RELEASE_VERSION", "dev")
    channel = os.environ.get("RELEASE_CHANNEL", "dev")
    revision = os.environ.get("SOURCE_REVISION", "")
    git_ref = os.environ.get("GIT_REF", "")
    github_sha = os.environ.get("GITHUB_SHA", "")
    source = validate(args.root, publish, version, channel, revision, git_ref, github_sha)
    with open(args.output, "a", encoding="utf-8") as output:
        output.write(f"version={version}\nchannel={channel}\nsource_sha={source}\n")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as error:
        print(f"CLI source validation failed: {error}", file=sys.stderr)
        raise SystemExit(1)
