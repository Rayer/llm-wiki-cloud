#!/usr/bin/env bash
set -eu

root="$FIXTURE_ROOT"
printf '%s\n' "$*" >> "$root/mutation-log"
if [[ "${1:-}" == inspect ]]; then
  response="$(jq -c '.url = (.url | sub("^https?://"; "")) | .ownerId = (.ownerId // .teamId) | del(.teamId)' "$root/candidate.json")"
  if [[ "$(<"$root/scenario")" == cli-mismatch ]]; then
    response="$(jq '.projectId = "prj_other"' <<< "$response")"
  fi
  printf '%s' "$response"
  exit 0
fi
[[ "${1:-}" == alias && "${2:-}" == set ]] || exit 90
[[ "${4:-}" == "$STABLE_DOMAIN" ]] || exit 90
jq --arg alias "$4" --arg project "$VERCEL_PROJECT_ID" --arg deployment "$3" \
  '.global = {alias:$alias, projectId:$project, deploymentId:$deployment} | .canonicalAliases = ([.canonicalAliases[] | select(.alias != $alias)] + [{alias:$alias, projectId:$project, deploymentId:$deployment}]) | .legacyAliases = [.legacyAliases[] | select(.alias != $alias)]' "$root/state.json" > "$root/state.tmp"
case "$(<"$root/scenario")" in
  post-inventory-add|post-inventory-mismatch)
    jq '.canonicalAliases += [{alias:"unrelated.example", projectId:"prj_canonical", deploymentId:"dpl_other"}]' "$root/state.tmp" > "$root/state.tmp2" && mv "$root/state.tmp2" "$root/state.tmp"
    ;;
  post-inventory-remove)
    jq '.canonicalAliases = [.canonicalAliases[] | select(.alias != "canonical.example")]' "$root/state.tmp" > "$root/state.tmp2" && mv "$root/state.tmp2" "$root/state.tmp"
    ;;
  post-inventory-change)
    jq '(.canonicalAliases[] | select(.alias == "canonical.example")).deploymentId = "dpl_changed"' "$root/state.tmp" > "$root/state.tmp2" && mv "$root/state.tmp2" "$root/state.tmp"
    ;;
esac
mv "$root/state.tmp" "$root/state.json"
touch "$root/mutated"
if [[ "$(<"$root/scenario")" == alias-failure || "$(<"$root/scenario")" == create-needed-alias-failure ]]; then exit 92; fi
