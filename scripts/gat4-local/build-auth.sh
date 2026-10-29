#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/gat4-local/build-auth.sh [--help]

Builds and tests the versioned gateauth/ module in a temporary clean context.
Outputs are written to "$TMPDIR/gat4-local/out/".
The manifest hashes gateauth/contracts/gateauth-artifact/v1/local-auth-config.test.json,
a synthetic nonsecret fixture; no runtime credentials are loaded.
EOF
}

if [[ "$#" -eq 1 && "$1" == --help ]]; then
  usage
  exit 0
fi
if [[ "$#" -ne 0 ]]; then
  usage >&2
  exit 2
fi
tmpdir="$(printenv TMPDIR || true)"
if [[ -z "$tmpdir" ]]; then
  echo "TMPDIR must name the local scratch directory" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
source_root="$repo_root/gateauth"
helper="$repo_root/scripts/gat4-local/artifact.py"
work_root="$tmpdir/gat4-local"
out="$work_root/out"
mkdir -p "$out"
work="$(mktemp -d "$work_root/work.XXXXXX")"
trap 'rm -rf "$work"' EXIT

origin_lwc_revision="$(git -C "$repo_root" rev-parse HEAD)"
if [[ ! "$origin_lwc_revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "origin LWC revision is not a full lowercase commit SHA" >&2
  exit 1
fi
source_tree_before="$(python3 -B "$helper" tree-digest --root "$source_root")"
cp -R "$source_root" "$work/gateauth"
source_tree_staged="$(python3 -B "$helper" tree-digest --root "$work/gateauth")"
if [[ "$source_tree_before" != "$source_tree_staged" ]]; then
  echo "GateAuth source changed while creating the isolated build context" >&2
  exit 1
fi

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
go_version="$(go version | awk '{print $3}')"
version="$(tr -d '[:space:]' < "$work/gateauth/VERSION")"
config_input="$source_root/contracts/gateauth-artifact/v1/local-auth-config.test.json"

rm -f "$out/auth" "$out/auth-linux-amd64" "$out/go-test.txt" \
  "$out/gateauth-image.oci.tar" "$out/gateauth-artifact-v1.json"
(
  cd "$work/gateauth"
  export GOWORK=off
  export GOFLAGS=-mod=readonly
  export GOTOOLCHAIN=local
  if ! go test -count=1 ./... > "$out/go-test.txt" 2>&1; then
    cat "$out/go-test.txt" >&2
    exit 1
  fi
  CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$out/auth" ./cmd/auth
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
    -o "$out/auth-linux-amd64" ./cmd/auth
)

source_tree_after="$(python3 -B "$helper" tree-digest --root "$source_root")"
if [[ "$source_tree_before" != "$source_tree_after" ]]; then
  echo "GateAuth source changed during the isolated build" >&2
  exit 1
fi

python3 -B "$helper" create \
  --source-root "$source_root" \
  --config-input "$config_input" \
  --artifacts-dir "$out" \
  --origin-lwc-revision "$origin_lwc_revision" \
  --go-version "$go_version" \
  --native-goos "$goos" \
  --native-goarch "$goarch" \
  --version "$version"
echo "GAT4_LOCAL_BUILD=PASS"
echo "GAT4_ARTIFACT_DIR=$out"
