#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
printf '%s\n' "$*" >> "$root/mutation-log"
if [[ "$1" != alias || "$2" != set || "$3" != dpl_devready || "$4" != llm-wiki-frontend-dev.vercel.app ]]; then
  exit 90
fi
jq --arg domain "$4" '.[$domain] = "dpl_devready"' "$root/aliases.json" > "$root/aliases.json.tmp"
mv "$root/aliases.json.tmp" "$root/aliases.json"
touch "$root/mutated"
if [[ "$(<"$root/scenario")" == partial-mutation ]]; then exit 92; fi
