import argparse
import re
import sys
from pathlib import Path

from package_cli_release import COMMIT


SAFE_VERSION = re.compile(r"^[A-Za-z0-9.+-]+$")
CHANNELS = {"dev", "stable", "prerelease"}
TAG_PREFIX = "refs/tags/cli/v"


def normalize_event(event_name, ref, event_sha, publish_input="", version_input="", channel_input="", source_revision_input=""):
    if not COMMIT.fullmatch(event_sha):
        raise ValueError("selected event revision is not a full Git SHA")

    if event_name == "push":
        if not ref.startswith(TAG_PREFIX):
            raise ValueError("tag push must use refs/tags/cli/v<version>")
        version = ref[len(TAG_PREFIX):]
        if not SAFE_VERSION.fullmatch(version):
            raise ValueError("tag push contains an unsafe release version")
        publish = "true"
        version_base = version.split("+", 1)[0]
        channel = "prerelease" if "-" in version_base else "stable"
        source_revision = event_sha
    elif event_name == "workflow_dispatch":
        publish = str(publish_input or "false").lower()
        version = version_input or "dev"
        channel = channel_input or "dev"
        source_revision = source_revision_input or event_sha
    else:
        raise ValueError("unsupported CLI release workflow event")

    if publish not in {"true", "false"}:
        raise ValueError("publish input must be true or false")
    if not SAFE_VERSION.fullmatch(version):
        raise ValueError("release version contains unsafe characters")
    if channel not in CHANNELS:
        raise ValueError("release channel is invalid")
    if not COMMIT.fullmatch(source_revision):
        raise ValueError("source revision must be a full Git SHA")

    return {
        "publish": publish,
        "version": version,
        "channel": channel,
        "source_revision": source_revision,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--event", required=True)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--publish", default="")
    parser.add_argument("--version", default="")
    parser.add_argument("--channel", default="")
    parser.add_argument("--source-revision", default="")
    parser.add_argument("--output", required=True)
    arguments = parser.parse_args()

    values = normalize_event(
        arguments.event,
        arguments.ref,
        arguments.sha,
        arguments.publish,
        arguments.version,
        arguments.channel,
        arguments.source_revision,
    )
    with Path(arguments.output).open("a", encoding="utf-8") as output:
        for key, value in values.items():
            output.write(f"{key}={value}\n")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        print(f"CLI release event normalization failed: {error}", file=sys.stderr)
        raise SystemExit(1)
