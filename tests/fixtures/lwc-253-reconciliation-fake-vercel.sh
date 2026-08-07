#!/usr/bin/env bash
set -eu

root="$FIXTURE_ROOT"
printf '%s\n' "$*" >> "$root/mutation-log"
if [[ "${1:-}" == inspect ]]; then
  if [[ "$(<"$root/scenario")" == cli-mismatch ]]; then jq '.projectId = "prj_other"' "$root/candidate.json"; else cat "$root/candidate.json"; fi
  exit 0
fi
[[ "${1:-}" == alias && "${2:-}" == set ]] || exit 90
[[ "${4:-}" == "$STABLE_DOMAIN" ]] || exit 90
jq --arg alias "$4" --arg project "$VERCEL_PROJECT_ID" --arg deployment "$3" \
  '.global = {alias:$alias, projectId:$project, deploymentId:$deployment} | .canonicalAliases = [{alias:$alias, projectId:$project, deploymentId:$deployment}] | .legacyAliases = []' "$root/state.json" > "$root/state.tmp"
mv "$root/state.tmp" "$root/state.json"
touch "$root/mutated"
if [[ "$(<"$root/scenario")" == alias-failure ]]; then exit 92; fi
