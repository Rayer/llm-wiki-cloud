#!/usr/bin/env bash
set -euo pipefail

version="0.32.1"
destination="${1:?usage: ensure-pkl.sh /absolute/path/to/pkl}"
destination_dir="$(dirname "$destination")"
mkdir -p "$destination_dir"
destination="$(cd "$destination_dir" && pwd)/$(basename "$destination")"

matches_version() {
  "$1" --version 2>/dev/null | grep -Eq "^Pkl ${version//./\\.}([[:space:]]|$)"
}

if [ -x "$destination" ]; then
  if matches_version "$destination"; then
    exit 0
  fi
  printf 'Pkl at %s must be version %s\n' "$destination" "$version" >&2
  exit 1
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) asset="pkl-linux-amd64" ;;
  Linux/aarch64|Linux/arm64) asset="pkl-linux-aarch64" ;;
  Darwin/x86_64) asset="pkl-macos-amd64" ;;
  Darwin/arm64|Darwin/aarch64) asset="pkl-macos-aarch64" ;;
  *) printf 'Pinned Pkl %s has no configured asset for %s/%s\n' "$version" "$(uname -s)" "$(uname -m)" >&2; exit 1 ;;
esac

download="${destination}.download.$$"
trap 'rm -f "$download"' EXIT
curl --fail --silent --show-error --location \
  "https://github.com/apple/pkl/releases/download/${version}/${asset}" \
  --output "$download"
chmod 0755 "$download"
if ! matches_version "$download"; then
  printf 'Downloaded Pkl does not report the pinned version %s\n' "$version" >&2
  exit 1
fi
mv "$download" "$destination"
