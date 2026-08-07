#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
vercel_base="${VERCEL_API_BASE_URL:-https://api.vercel.com}"
github_base="${GITHUB_API_URL:-https://api.github.com}"
url=""
data=""

normalize_v6_deployment() {
  jq -c '. + {uid: (.uid // .id)} | del(.id,.teamId,.accountId,.ownerId) | .url = (.url | sub("^https?://"; ""))'
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --header|--connect-timeout|--max-time|--max-redirs|--output|--write-out|--request|--data)
      if [[ "$1" == --data ]]; then data="${2:-}"; fi
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
if [[ -z "$url" ]]; then exit 1; fi
printf '%s\n' "$url" >> "$root/curl-calls"

if [[ "$url" == *"/actions/workflows/ci.yml/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then
    jq '.workflow_runs[0].conclusion = "failure"' "$root/ci.json"
  elif [[ "$scenario" == ci-wrong-sha ]]; then
    jq '.workflow_runs[0].head_sha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/ci.json"
  else
    cat "$root/ci.json"
  fi
elif [[ "$url" == *"/repos/$GITHUB_REPOSITORY"* ]]; then
  printf '%s' '{"id":12345}'
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID/domains"* ]]; then
  if [[ "$scenario" == domain-mismatch ]]; then
    printf '%s' '{"domains":[{"name":"llm-wiki-frontend.vercel.app"}]}'
  else
    cat "$root/domains.json"
  fi
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID"* ]]; then
  if [[ "$scenario" == project-mismatch ]]; then
    jq '.name = "llm-wiki-frontend"' "$root/project.json"
  elif [[ "$scenario" == team-mismatch ]]; then
    jq '.accountId = "team_main123"' "$root/project.json"
  else
    cat "$root/project.json"
  fi
elif [[ "$url" == *"/v13/deployments/dpl_"* && "$url" != *"/v13/deployments?"* ]]; then
  if [[ "$scenario" == create-read-failure ]]; then exit 7; fi
  response="$(jq -c --arg team "$VERCEL_TEAM_ID" '.ownerId = $team | del(.teamId) | .url = (.url | sub("^https?://"; ""))' "$root/deployment.json")"
  if [[ "$scenario" == create-poll-timeout ]]; then
    response="$(jq '.readyState = "BUILDING"' <<< "$response")"
  elif [[ "$scenario" == create-source-mismatch ]]; then
    response="$(jq '.meta.githubCommitRef = "release"' <<< "$response")"
  elif [[ "$scenario" == existing-source-mismatch ]]; then
    response="$(jq '.meta.githubCommitRef = "release"' <<< "$response")"
  elif [[ "$scenario" == post-read-mismatch && -f "$root/mutated" ]]; then
    response="$(jq '.projectId = "prj_other"' <<< "$response")"
  fi
  printf '%s' "$response"
elif [[ "$url" == *"/v6/deployments?"* ]]; then
  case "$scenario" in
    deployment-missing|create-failure|create-uncertain|create-poll-timeout|create-source-mismatch|create-read-failure)
      printf '%s' '{"deployments":[]}'
      ;;
    historical-deployment)
      normalize_v6_deployment <<< "$(jq '.meta.githubCommitSha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/deployment.json")" | jq '{deployments: [.] }'
      ;;
    page-2-exact)
      if [[ "$url" == *"until=cursor-2"* ]]; then
        normalize_v6_deployment <<< "$(cat "$root/deployment.json")" | jq '{deployments: [.] }'
      else
        normalize_v6_deployment <<< "$(jq '.meta.githubCommitSha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/deployment.json")" | jq '{deployments: [.], pagination: {next: "cursor-2"}}'
      fi
      ;;
    *)
      normalize_v6_deployment <<< "$(cat "$root/deployment.json")" | jq '{deployments: [.] }'
      ;;
  esac
elif [[ "$url" == *"/v13/deployments?"* ]]; then
  if [[ "$scenario" == create-failure ]]; then exit 8; fi
  if [[ "$scenario" == create-uncertain ]]; then
    printf '%s\n' "$data" >> "$root/deployment-post-log"
    exit 8
  fi
  printf '%s\n' "$data" >> "$root/deployment-post-log"
  printf '%s' '{"id":"dpl_devready"}'
elif [[ "$url" == *"/v4/aliases/$STABLE_DOMAIN"* ]]; then
  if [[ "$scenario" == authority-conflict ]]; then
    printf '%s' '{"alias":"llm-wiki-frontend-dev.vercel.app","projectId":"prj_main123","deploymentId":"dpl_mainready"}'
  elif [[ "$scenario" == alias-absent ]]; then
    printf '{"alias":"%s","projectId":"%s","deploymentId":""}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID"
  elif [[ "$scenario" == alias-divergent ]]; then
    printf '%s' '{"alias":"different.example","projectId":"prj_dev123","deploymentId":"dpl_devold"}'
  elif [[ "$scenario" == alias-project-mismatch ]]; then
    printf '{"alias":"%s","projectId":"prj_main123","deploymentId":"dpl_mainready"}' "$STABLE_DOMAIN"
  elif [[ "$scenario" == rollback-freeze-failure ]]; then
    exit 7
  elif [[ "$scenario" == post-read-mismatch && -f "$root/mutated" ]]; then
    printf '{"alias":"%s","projectId":"%s","deploymentId":"dpl_other"}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID"
  else
    deployment_id="$(jq -r --arg domain "$STABLE_DOMAIN" '.[$domain]' "$root/aliases.json")"
    printf '{"alias":"%s","projectId":"%s","deploymentId":"%s"}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID" "$deployment_id"
  fi
elif [[ "$url" == *"/v4/aliases?"* ]]; then
  if [[ "$url" == *"domain="* ]]; then
    printf '%s' '{"aliases":[]}'
  elif [[ "$scenario" == authority-conflict ]]; then
    printf '{"aliases":[{"alias":"%s","projectId":"%s","deploymentId":"dpl_devold"}]}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID"
  elif [[ "$scenario" == alias-absent ]]; then
    printf '{"aliases":[{"alias":"%s","projectId":"%s","deploymentId":""}]}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID"
  elif [[ "$scenario" == alias-divergent ]]; then
    printf '%s' '{"aliases":[{"alias":"different.example","projectId":"prj_dev123","deploymentId":"dpl_devold"}]}'
  elif [[ "$scenario" == alias-project-mismatch ]]; then
    printf '{"aliases":[{"alias":"%s","projectId":"prj_main123","deploymentId":"dpl_mainready"}]}' "$STABLE_DOMAIN"
  elif [[ "$scenario" == post-read-mismatch && -f "$root/mutated" ]]; then
    printf '{"aliases":[{"alias":"%s","projectId":"%s","deploymentId":"dpl_other"}]}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID"
  else
    deployment_id="$(jq -r --arg domain "$STABLE_DOMAIN" '.[$domain]' "$root/aliases.json")"
    printf '{"aliases":[{"alias":"%s","projectId":"%s","deploymentId":"%s"}]}' "$STABLE_DOMAIN" "$VERCEL_PROJECT_ID" "$deployment_id"
  fi
else
  exit 1
fi
