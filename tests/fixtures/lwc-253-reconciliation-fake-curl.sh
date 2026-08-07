#!/usr/bin/env bash
set -eu

root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
url=""
data=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --header|--connect-timeout|--max-time|--max-redirs|--output|--write-out|--request|--data)
      [[ "$1" == --data ]] && data="${2:-}"
      shift 2
      ;;
    --silent|--show-error|--fail-with-body|--location)
      shift
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *) shift ;;
  esac
done
[[ -n "$url" ]] || exit 1
printf '%s\n' "$url" >> "$root/curl-calls"

if [[ "$url" == *"/actions/workflows/ci.yml/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then jq '.workflow_runs[0].conclusion = "failure"' "$root/ci.json"; else cat "$root/ci.json"; fi
elif [[ "$url" == *"/repos/$GITHUB_REPOSITORY/actions"* ]]; then
  cat "$root/ci.json"
elif [[ "$url" == *"/repos/$GITHUB_REPOSITORY"* ]]; then
  printf '%s' '{"id":98765,"full_name":"Rayer/llm-wiki-frontend"}'
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID/domains"* ]]; then
  if [[ "$scenario" == domain-missing ]]; then printf '%s' '{"domains":[]}'; else cat "$root/domains.json"; fi
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID"* ]]; then
  cat "$root/canonical-project.json"
elif [[ "$url" == *"/v9/projects/$EXPECTED_CURRENT_ALIAS_PROJECT_ID"* ]]; then
  if [[ "$scenario" == legacy-project-mismatch ]]; then jq '.name = "wrong-project"' "$root/legacy-project.json"; else cat "$root/legacy-project.json"; fi
elif [[ "$url" == *"/v13/deployments/dpl_old"* ]]; then
  if [[ "$scenario" == old-source-mismatch ]]; then jq '.meta.githubCommitSha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/old-deployment.json"; else cat "$root/old-deployment.json"; fi
elif [[ "$url" == *"/v13/deployments/dpl_new"* ]]; then
  if [[ "$scenario" == post-api-mismatch && -f "$root/mutated" ]]; then jq '.projectId = "prj_other"' "$root/candidate.json"; else cat "$root/candidate.json"; fi
elif [[ "$url" == *"/v6/deployments?"* ]]; then
  if [[ "$scenario" == deployment-page-2 && "$url" != *"until=deploy-cursor-2"* ]]; then
    jq '{deployments: [{id:"dpl_other", projectId:"prj_other", teamId:"team_test", readyState:"READY", target:"preview", url:"https://other.vercel.app"}], pagination:{next:"deploy-cursor-2"}}' "$root/candidate.json"
  elif [[ "$scenario" == deployment-page-2 && "$url" == *"until=deploy-cursor-2"* ]]; then
    jq '{deployments:[.]}' "$root/candidate.json"
  elif [[ "$scenario" == create-needed && ! -f "$root/created" ]]; then
    printf '%s' '{"deployments":[]}'
  else
    if [[ -f "$root/created" || "$scenario" == existing-candidate || "$scenario" == deployment-page-2 ]]; then jq '{deployments:[.]}' "$root/candidate.json"; else printf '%s' '{"deployments":[]}'; fi
  fi
elif [[ "$url" == *"/v13/deployments?"* ]]; then
  printf '%s\n' "$data" >> "$root/deployment-post-log"
  [[ "$scenario" != create-failure ]] || exit 8
  touch "$root/created"
  printf '%s' '{"id":"dpl_new","url":"https://dpl_new.vercel.app"}'
elif [[ "$url" == *"/v4/aliases/$STABLE_DOMAIN"* ]]; then
  jq '.global' "$root/state.json"
elif [[ "$url" == *"/v4/aliases?"* ]]; then
  project=""
  if [[ "$url" =~ projectId=([^\&]+) ]]; then project="${BASH_REMATCH[1]}"; fi
  if [[ "$scenario" == alias-cursor-loop && "$url" != *"until=alias-loop"* ]]; then
    printf '%s' '{"aliases":[],"pagination":{"next":"alias-loop"}}'
  elif [[ "$scenario" == alias-cursor-loop && "$url" == *"until=alias-loop"* ]]; then
    printf '%s' '{"aliases":[],"pagination":{"next":"alias-loop"}}'
  elif [[ "$scenario" == alias-page-2 && "$url" != *"until=alias-cursor-2"* ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"next":"alias-cursor-2"}}'
  elif [[ "$scenario" == alias-page-2 && "$url" == *"until=alias-cursor-2"* ]]; then
    if [[ "$project" == "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" ]]; then jq '{aliases:.legacyAliases}' "$root/state.json"; else jq '{aliases:.canonicalAliases}' "$root/state.json"; fi
  elif [[ "$scenario" == alias-malformed ]]; then
    printf '%s' '{"aliases":[{"alias":"llm-wiki-frontend-dev.vercel.app"}]}'
  else
    if [[ "$project" == "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" ]]; then jq '{aliases:.legacyAliases}' "$root/state.json"; else jq '{aliases:.canonicalAliases}' "$root/state.json"; fi
  fi
else
  exit 1
fi
