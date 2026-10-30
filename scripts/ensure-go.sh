#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "$0")/.." && pwd)"
version="$(awk '$1 == "go" { print $2; exit }' "$repository_root/apps/bff/go.mod")"
destination="${1:?usage: ensure-go.sh /absolute/path/to/go-root}"

case "$version" in
  1.26.0) ;;
  *) printf 'No pinned Go archive checksum for version %s\n' "$version" >&2; exit 1 ;;
esac

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) asset="linux-amd64"; checksum="aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235" ;;
  Linux/aarch64|Linux/arm64) asset="linux-arm64"; checksum="bd03b743eb6eb4193ea3c3fd3956546bf0e3ca5b7076c8226334afe6b75704cd" ;;
  Darwin/x86_64) asset="darwin-amd64"; checksum="1ca28b7703cbea05a65b2a1d92d6b308610ef92f8824578a0874f2e60c9d5a22" ;;
  Darwin/arm64|Darwin/aarch64) asset="darwin-arm64"; checksum="b1640525dfe68f066d56f200bef7bf4dce955a1a893bd061de6754c211431023" ;;
  *) printf 'Pinned Go %s has no configured archive for %s/%s\n' "$version" "$(uname -s)" "$(uname -m)" >&2; exit 1 ;;
esac

destination_dir="$(dirname "$destination")"
mkdir -p "$destination_dir"
destination="$(cd "$destination_dir" && pwd)/$(basename "$destination")"
if [ -e "$destination" ]; then
  if [ -x "$destination/bin/go" ] && "$destination/bin/go" version | grep -q "go${version} "; then
    exit 0
  fi
  printf 'Go at %s must be version %s\n' "$destination" "$version" >&2
  exit 1
fi

work_dir="$(mktemp -d "$destination_dir/.ensure-go.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
archive="$work_dir/go.tar.gz"
curl --fail --silent --show-error --location \
  "https://go.dev/dl/go${version}.${asset}.tar.gz" --output "$archive"
if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c - >/dev/null
elif command -v shasum >/dev/null 2>&1; then
  printf '%s  %s\n' "$checksum" "$archive" | shasum -a 256 -c - >/dev/null
else
  printf 'A SHA-256 utility is required to verify Go %s\n' "$version" >&2
  exit 1
fi
tar -xzf "$archive" -C "$work_dir"
"$work_dir/go/bin/go" version | grep -q "go${version} "
mv "$work_dir/go" "$destination"
