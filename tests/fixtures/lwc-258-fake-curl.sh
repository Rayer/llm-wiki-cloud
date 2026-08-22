#!/usr/bin/env bash
set -Eeo pipefail

root="$FIXTURE_ROOT"; method=GET; body=; url=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --request|--data|--header|--connect-timeout|--max-time) [[ $# -gt 1 ]] || exit 2; [[ "$1" == --request ]] && method="$2"; [[ "$1" == --data ]] && body="$2"; shift 2 ;;
    --fail-with-body|--silent|--show-error|--location) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s %s %s\n' "$method" "$url" "$body" >> "$root/curl-calls"
state="$root/state.json"

if [[ "$url" == https://api.github.com/repos/Rayer/llm-wiki-frontend/actions/workflows/ci.yml/runs* ]]; then
  if [[ "$FIXTURE_SCENARIO" == ci-failure || "$FIXTURE_SCENARIO" == ci-wrong-ref ]]; then
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"develop","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"failure","id":987654321,"html_url":"https://github.com/Rayer/llm-wiki-frontend/actions/runs/987654321"}]}'
  else
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"success","id":987654321,"html_url":"https://github.com/Rayer/llm-wiki-frontend/actions/runs/987654321"}]}'
  fi
elif [[ "$method" == POST && "$url" == https://api.vercel.com/v13/deployments\?* ]]; then
  printf '%s\n' DEPLOY_POST >> "$root/mutation-log"
  printf '%s' "$(( $(cat "$root/deployment-creates") + 1 ))" > "$root/deployment-creates"
  printf '%s' "$body" > "$root/deployment-body.json"
  [[ "$FIXTURE_SCENARIO" != create-failure ]] || exit 22
  if [[ "$FIXTURE_SCENARIO" == invalid-create-response ]]; then printf '%s' '{"url":"new.vercel.app"}';
  elif [[ "$FIXTURE_SCENARIO" == malformed-create-response ]]; then printf '%s' '{';
  elif [[ "$FIXTURE_SCENARIO" == create-invalid-url-scheme ]]; then printf '%s' '{"id":"dpl_new123","url":"http://dpl-new.vercel.app"}';
  elif [[ "$FIXTURE_SCENARIO" == create-invalid-url-path ]]; then printf '%s' '{"id":"dpl_new123","url":"dpl-new.vercel.app/path"}';
  elif [[ "$FIXTURE_SCENARIO" == create-invalid-url-whitespace ]]; then printf '%s' '{"id":"dpl_new123","url":"dpl-new.vercel.app "}';
  elif [[ "$FIXTURE_SCENARIO" == create-alias-assigned ]]; then printf '%s' '{"id":"dpl_new123","url":"dpl-new.vercel.app","aliasAssigned":true}';
  elif [[ "$FIXTURE_SCENARIO" == create-canonical-alias-array ]]; then printf '%s' '{"id":"dpl_new123","url":"dpl-new.vercel.app","userAliases":["wiki.rayer.idv.tw"]}';
  elif [[ "$FIXTURE_SCENARIO" == create-https-url ]]; then printf '%s' '{"id":"dpl_new123","url":"https://dpl-new.vercel.app"}';
  else printf '%s' '{"id":"dpl_new123","url":"dpl-new.vercel.app"}'; fi
elif [[ "$url" == https://api.vercel.com/v9/projects/*/env\?* ]]; then
  reads=$(cat "$root/env-reads"); printf '%s' "$((reads + 1))" > "$root/env-reads"
  response=$(jq '{envs: .envs}' "$state")
  mutations=$(cat "$root/env-mutations")
  if [[ "$FIXTURE_SCENARIO" == readback-mismatch && "$mutations" -gt 0 && "$reads" -eq 2 ]]; then response=$(jq '.envs[0].value = "https://wrong.invalid"' <<< "$response"); fi
  printf '%s' "$response"
elif [[ "$url" == https://api.vercel.com/v9/projects/*\?* && "$url" != */env/* ]]; then
  jq '.project' "$state"
elif [[ "$url" == https://api.vercel.com/v4/aliases/*\?teamId=$VERCEL_TEAM_ID ]]; then
  post_create=$(cat "$root/deployment-creates")
  [[ "$FIXTURE_SCENARIO" != freeze-read-failure ]] || exit 22
  [[ "$FIXTURE_SCENARIO" != post-create-alias-read-failure || "$post_create" -eq 0 ]] || exit 22
  alias="${url#*v4/aliases/}"; alias="${alias%%\?*}"; alias="${alias//%2F//}"
  response=$(jq --arg alias "$alias" '.aliases[$alias]' "$state")
  if [[ "$post_create" -gt 0 && "$FIXTURE_SCENARIO" == post-create-alias-drift && "$alias" == wiki.rayer.idv.tw ]]; then
    response=$(jq '.deploymentId="dpl_new123"' <<< "$response")
  fi
  printf '%s' "$response"
elif [[ "$url" == "https://api.vercel.com/v13/deployments/dpl_existing123?teamId=$VERCEL_TEAM_ID&withGitRepoInfo=true" ]]; then
  jq '.deployments.dpl_existing123' "$state"
elif [[ "$url" == "https://api.vercel.com/v13/deployments/dpl_new123?teamId=$VERCEL_TEAM_ID&withGitRepoInfo=true" ]]; then
  [[ "$FIXTURE_SCENARIO" != deployment-read-failure ]] || exit 22
  if [[ "$FIXTURE_SCENARIO" == deployment-timeout ]]; then jq '.deployments.dpl_new123 | .readyState="BUILDING"' "$state"
  elif [[ "$FIXTURE_SCENARIO" == deployment-analyzing ]]; then reads=$(cat "$root/deployment-reads"); printf '%s' "$((reads + 1))" > "$root/deployment-reads"; if [[ "$reads" -eq 0 ]]; then jq '.deployments.dpl_new123 | .readyState="ANALYZING"' "$state"; else jq '.deployments.dpl_new123' "$state"; fi
  elif [[ "$FIXTURE_SCENARIO" == deployment-deploying ]]; then reads=$(cat "$root/deployment-reads"); printf '%s' "$((reads + 1))" > "$root/deployment-reads"; if [[ "$reads" -eq 0 ]]; then jq '.deployments.dpl_new123 | .readyState="DEPLOYING"' "$state"; else jq '.deployments.dpl_new123' "$state"; fi
  elif [[ "$FIXTURE_SCENARIO" == deployment-failed ]]; then jq '.deployments.dpl_new123 | .readyState="ERROR"' "$state"
  elif [[ "$FIXTURE_SCENARIO" == deployment-source-mismatch ]]; then jq '.deployments.dpl_new123 | .gitSource.sha="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$state"
  elif [[ "$FIXTURE_SCENARIO" == post-create-alias-missing ]]; then jq '.deployments.dpl_new123 | del(.alias)' "$state"
  elif [[ "$FIXTURE_SCENARIO" == post-create-alias-null ]]; then jq '.deployments.dpl_new123 | .alias=null' "$state"
  elif [[ "$FIXTURE_SCENARIO" == post-create-alias-malformed ]]; then jq '.deployments.dpl_new123 | .alias={}' "$state"
  elif [[ "$FIXTURE_SCENARIO" == post-create-canonical-alias-array ]]; then jq '.deployments.dpl_new123 + {automaticAliases:["llm-wiki-frontend.vercel.app"]}' "$state"
  elif [[ "$FIXTURE_SCENARIO" == post-create-alias-assigned ]]; then jq '.deployments.dpl_new123 + {aliasAssigned:true}' "$state"
  else jq '.deployments.dpl_new123' "$state"; fi
elif [[ "$method" == POST && "$url" == https://api.vercel.com/v10/projects/*/env\?* ]]; then
  printf '%s\n' ENV_POST >> "$root/mutation-log"; printf '%s' "$(( $(cat "$root/env-mutations") + 1 ))" > "$root/env-mutations"
  jq --argjson body "$body" '.envs += [$body + {id: "env_new"}]' "$state" > "$state.tmp"; mv "$state.tmp" "$state"; jq '.envs[-1]' "$state"
elif [[ "$method" == PATCH && "$url" == https://api.vercel.com/v9/projects/*/env/*\?* ]]; then
  printf '%s\n' ENV_PATCH >> "$root/mutation-log"; printf '%s' "$(( $(cat "$root/env-mutations") + 1 ))" > "$root/env-mutations"
  id="${url%%\?*}"; id="${id##*/}"
  jq --argjson body "$body" --arg id "$id" '(.envs[] | select(.id == $id)) |= . + $body' "$state" > "$state.tmp"; mv "$state.tmp" "$state"; jq --arg id "$id" '.envs[] | select(.id == $id)' "$state"
elif [[ "$method" == DELETE && "$url" == https://api.vercel.com/v9/projects/*/env/*\?* ]]; then
  printf '%s\n' ENV_DELETE >> "$root/mutation-log"; printf '%s' "$(( $(cat "$root/env-mutations") + 1 ))" > "$root/env-mutations"
  id="${url%%\?*}"; id="${id##*/}"
  jq --arg id "$id" 'del(.envs[] | select(.id == $id))' "$state" > "$state.tmp"; mv "$state.tmp" "$state"; printf '{}'
else
  exit 22
fi
