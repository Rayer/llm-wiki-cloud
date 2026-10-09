#!/bin/sh
set -eu

harness_dir=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
repo_root=$(CDPATH= cd "$harness_dir/../../.." && pwd -P)
bff_dir="$repo_root/apps/bff"
evidence_dir="$repo_root/docs/lwc-336/evidence/native"
hermes_source=/Users/rayer/.hermes/hermes-agent
hermes_python=/Users/rayer/.hermes/hermes-agent/venv/bin/python
owned_scratch_root=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch
emulator_host=${LWC336_EMULATOR_HOST:-127.0.0.1:8596}

case "$emulator_host" in
  127.0.0.1:*|localhost:*) ;;
  *) echo "LWC336_EMULATOR_HOST must be loopback" >&2; exit 2 ;;
esac
test -x "$hermes_python"
test -d "$hermes_source"
mkdir -p "$owned_scratch_root" "$evidence_dir"
owned_scratch_root=$(CDPATH= cd "$owned_scratch_root" && pwd -P)
scratch=$(mktemp -d "$owned_scratch_root/lwc336.XXXXXX")
chmod 700 "$scratch"
mkdir -m 700 "$scratch/home" "$scratch/hermes" "$scratch/tmp"

cleanup() {
  scratch_real=$(CDPATH= cd "$scratch" && pwd -P)
  case "$scratch_real" in
    "$owned_scratch_root"/lwc336.*) rm -rf "$scratch_real" ;;
    *) echo "refusing to remove scratch outside the owned lwc336 root" >&2; return 1 ;;
  esac
}
trap cleanup EXIT HUP INT TERM

go_cache=$(go env GOCACHE)
module_cache=$(go env GOMODCACHE)
set +e
(cd "$bff_dir" && env -u FIRESTORE_EMULATOR_HOST \
  -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY \
  -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY \
  -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY \
  GOCACHE="$go_cache" GOMODCACHE="$module_cache" \
  go test -c -o "$scratch/bff-harness.test" ./cmd/bff)
build_exit=$?
set -e
if [ "$build_exit" -ne 0 ]; then
  exit "$build_exit"
fi

set +e
env -i \
  HOME="$scratch/home" \
  HERMES_HOME="$scratch/hermes" \
  TMPDIR="$scratch/tmp" \
  PATH=/opt/homebrew/bin:/usr/bin:/bin \
  LANG=en_US.UTF-8 \
  FIRESTORE_EMULATOR_HOST="$emulator_host" \
  "$hermes_python" -I -B "$harness_dir/probe.py" \
    --source "$hermes_source" \
    --repo-root "$repo_root" \
    --binary "$scratch/bff-harness.test" \
    --evidence-dir "$evidence_dir" \
    --scratch-root "$scratch" \
    --emulator-host "$emulator_host" \
    >"$scratch/probe.stdout" 2>"$scratch/probe.stderr"
probe_exit=$?
set -e

cat >> "$evidence_dir/commands.log" <<EOF
Build command: go test -c -o $scratch/bff-harness.test ./cmd/bff; exit=$build_exit
Probe command: $hermes_python -I -B $harness_dir/probe.py --source $hermes_source --repo-root $repo_root --binary $scratch/bff-harness.test --evidence-dir $evidence_dir --scratch-root $scratch --emulator-host $emulator_host; exit=$probe_exit
EOF
if [ -s "$scratch/probe.stdout" ]; then cat "$scratch/probe.stdout"; fi
if [ "$probe_exit" -ne 0 ]; then
  if [ -s "$scratch/probe.stderr" ]; then cat "$scratch/probe.stderr" >&2; fi
  exit "$probe_exit"
fi
