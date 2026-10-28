#!/usr/bin/env bash
set -euo pipefail

tmpdir="$(printenv TMPDIR || true)"
if [[ -z "$tmpdir" ]]; then
  echo "TMPDIR must name the local scratch directory" >&2
  exit 2
fi
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
evidence="$tmpdir/gat4-local/build-helper"
mkdir -p "$evidence"
work="$(mktemp -d "$tmpdir/gat4-local/clean-checkout.XXXXXX")"
trap 'rm -rf "$work"' EXIT
clean_repo="$work/repo"
mkdir -p "$clean_repo"
cp -R "$repo_root/gateauth" "$clean_repo/gateauth"
mkdir -p "$clean_repo/scripts"
cp -R "$repo_root/scripts/gat4-local" "$clean_repo/scripts/gat4-local"
git -C "$clean_repo" init -q
git -C "$clean_repo" config user.name "GAT-4 local fixture"
git -C "$clean_repo" config user.email "gat4-local@example.test"
git -C "$clean_repo" add gateauth scripts/gat4-local
git -C "$clean_repo" commit -qm "isolated GateAuth local build fixture"
clean_tmp="$work/tmp"
mkdir -p "$clean_tmp"
log="$evidence/clean-checkout.log"
(
  cd "$clean_repo"
  TMPDIR="$clean_tmp" "$clean_repo/scripts/gat4-local/build-auth.sh"
  TMPDIR="$clean_tmp" python3 -B -m unittest discover \
    -s scripts/gat4-local/tests -p 'test_*.py'
) > "$log" 2>&1 || {
  cat "$log" >&2
  exit 1
}
cat "$log"
echo "GAT4_CLEAN_CHECKOUT_NO_LWC_TREE=PASS"
