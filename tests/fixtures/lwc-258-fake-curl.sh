#!/usr/bin/env bash
set -Eeo pipefail

root="$FIXTURE_ROOT"
method=GET
body=
url=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --request|--data|--header|--connect-timeout|--max-time) [[ $# -gt 1 ]] || exit 2; [[ "$1" == --request ]] && method="$2"; [[ "$1" == --data ]] && body="$2"; shift 2 ;;
    --fail-with-body|--silent|--show-error|--location) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\n' "$url" >> "$root/curl-calls"
state="$root/state.json"

if [[ "$url" == https://api.github.com/repos/Rayer/llm-wiki-frontend/actions/workflows/ci.yml/runs* ]]; then
  if [[ "$FIXTURE_SCENARIO" == ci-failure ]]; then
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"develop","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"failure","id":987654321,"html_url":"https://github.com/Rayer/llm-wiki-frontend/actions/runs/987654321"}]}'
  else
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"develop","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"success","id":987654321,"html_url":"https://github.com/Rayer/llm-wiki-frontend/actions/runs/987654321"}]}'
  fi
elif [[ "$url" == https://api.vercel.com/v9/projects/*/env\?* ]]; then
  reads=$(cat "$root/env-reads")
  printf '%s' "$((reads + 1))" > "$root/env-reads"
  response=$(jq '{envs: .envs}' "$state")
  mutations=$(cat "$root/mutations")
  if [[ "$FIXTURE_SCENARIO" == readback-mismatch && "$mutations" -gt 0 && "$reads" -eq 2 ]]; then
    response=$(jq '.envs[0].value = "https://wrong.invalid"' <<< "$response")
  fi
  printf '%s' "$response"
elif [[ "$url" == https://api.vercel.com/v9/projects/*\?* && "$url" != */env/* ]]; then
  jq '.project' "$state"
elif [[ "$method" == POST && "$url" == https://api.vercel.com/v10/projects/*/env\?* ]]; then
  printf '%s\n' POST >> "$root/mutation-log"
  printf '%s' "$(( $(cat "$root/mutations") + 1 ))" > "$root/mutations"
  jq --argjson body "$body" '.envs += [$body + {id: "env_new"}]' "$state" > "$state.tmp"
  mv "$state.tmp" "$state"
  jq '.envs[-1]' "$state"
elif [[ "$method" == PATCH && "$url" == https://api.vercel.com/v9/projects/*/env/*\?* ]]; then
  printf '%s\n' PATCH >> "$root/mutation-log"
  printf '%s' "$(( $(cat "$root/mutations") + 1 ))" > "$root/mutations"
  id=$(printf '%s' "$url" | sed 's/[?].*//' | sed 's#.*/##')
  jq --argjson body "$body" --arg id "$id" '(.envs[] | select(.id == $id)) |= . + $body' "$state" > "$state.tmp"
  mv "$state.tmp" "$state"
  jq --arg id "$id" '.envs[] | select(.id == $id)' "$state"
elif [[ "$method" == DELETE && "$url" == https://api.vercel.com/v9/projects/*/env/*\?* ]]; then
  printf '%s\n' DELETE >> "$root/mutation-log"
  printf '%s' "$(( $(cat "$root/mutations") + 1 ))" > "$root/mutations"
  id=$(printf '%s' "$url" | sed 's/[?].*//' | sed 's#.*/##')
  jq --arg id "$id" 'del(.envs[] | select(.id == $id))' "$state" > "$state.tmp"
  mv "$state.tmp" "$state"
  printf '{}'
else
  exit 22
fi
