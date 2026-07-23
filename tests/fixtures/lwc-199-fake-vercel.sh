#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
printf '%s\n' "$*" >> "$root/vercel-calls"
if [[ "$1" != alias || "$2" != set ]]; then exit 90; fi
if [[ "$3" != dpl_test123 ]]; then exit 91; fi
if [[ "$(wc -l < "$root/vercel-calls")" -eq 2 && "$( <"$root/scenario")" == partial-mutation ]]; then exit 92; fi
touch "$root/mutated"
