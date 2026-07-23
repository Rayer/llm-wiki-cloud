#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
printf '%s\n' "$*" >> "$root/vercel-calls"
if [[ "$1" != alias || "$2" != set ]]; then exit 90; fi
if [[ "$3" != dpl_test123 ]]; then exit 91; fi
call_number=$(( $(wc -l < "$root/vercel-calls") ))
scenario="$(<"$root/scenario")"
update_alias() {
  local alias="$1"
  local deployment_id="$2"
  jq --arg alias "$alias" --arg deploymentId "$deployment_id" \
    '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
  mv "$root/aliases.json.tmp" "$root/aliases.json"
}
case "$scenario:$call_number" in
  partial-mutation:2)
    exit 92
    ;;
  drift-before-first-write:1)
    update_alias wiki.rayer.idv.tw dpl_drift_before_first
    exit 92
    ;;
  drift-before-second-write:2)
    update_alias llm-wiki-frontend.vercel.app dpl_drift_before_second
    exit 92
    ;;
  drift-after-first-write:1)
    update_alias wiki.rayer.idv.tw dpl_test123
    update_alias llm-wiki-frontend.vercel.app dpl_drift_after_first
    touch "$root/mutated"
    exit 0
    ;;
esac
case "$4" in
  wiki.rayer.idv.tw|llm-wiki-frontend.vercel.app)
    update_alias "$4" dpl_test123
    ;;
  *)
    exit 93
    ;;
esac
touch "$root/mutated"
