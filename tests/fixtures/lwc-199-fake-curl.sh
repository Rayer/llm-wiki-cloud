#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
url="${@: -1}"
printf '%s\n' "$url" >> "$root/curl-calls"
if [[ "$url" == *"/actions/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then
    printf '%s' '{"workflow_runs":[{"name":"CI","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"failure"}]}'
  else
    printf '%s' '{"workflow_runs":[{"name":"CI","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"success"}]}'
  fi
  exit 0
fi
if [[ "$url" == *"/v13/deployments/"* ]]; then
  case "$scenario" in
    deployment-mismatch) jq '.projectId = "prj_other"' "$root/deployment.json" ;;
    source-provider-mismatch) jq '.meta.githubDeployment = "0"' "$root/deployment.json" ;;
    source-ref-mismatch) jq '.meta.githubCommitRef = "release"' "$root/deployment.json" ;;
    source-sha-mismatch) jq '.meta.githubCommitSha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/deployment.json" ;;
    not-ready) jq '.readyState = "BUILDING"' "$root/deployment.json" ;;
    *) cat "$root/deployment.json" ;;
  esac
  exit 0
fi
if [[ "$url" == *"/v4/aliases?"* ]]; then
  domain="${url##*domain=}"
  domain="${domain%%&*}"
  if [[ "$scenario" == missing-alias && "$domain" == wiki.rayer.idv.tw ]]; then
    printf '%s' '{"aliases":[]}'
  elif [[ "$scenario" == divergent-alias && "$domain" == wiki.rayer.idv.tw ]]; then
    printf '%s' '{"aliases":[{"alias":"unexpected.example","deploymentId":"dpl_wrong"}]}'
  elif [[ "$scenario" == post-readback-mismatch && -f "$root/mutated" && "$domain" == llm-wiki-frontend.vercel.app ]]; then
    printf '%s' '{"aliases":[{"alias":"llm-wiki-frontend.vercel.app","deploymentId":"dpl_other"}]}'
  else
    deployment="$(jq -r --arg domain "$domain" '.[$domain]' "$root/aliases.json")"
    if [[ -f "$root/mutated" ]]; then deployment="dpl_test123"; fi
    printf '{"aliases":[{"alias":"%s","deploymentId":"%s"}]}' "$domain" "$deployment"
  fi
  exit 0
fi
if [[ "$url" == https://wiki.rayer.idv.tw/* || "$url" == https://llm-wiki-frontend.vercel.app/* ]]; then
  if [[ "$scenario" == health-failure && "$url" == https://wiki.rayer.idv.tw/* ]]; then
    printf '503'
  else
    printf '200'
  fi
  exit 0
fi
printf 'unexpected curl URL: %s\n' "$url" >&2
exit 1
