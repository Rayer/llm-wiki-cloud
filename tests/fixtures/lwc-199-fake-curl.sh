#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
url="${@: -1}"
printf '%s\n' "$url" >> "$root/curl-calls"
if [[ "$url" == *"/actions/workflows/ci.yml/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"failure","id":987654321,"html_url":"https://github.test/Rayer/llm-wiki-frontend/actions/runs/987654321","run_attempt":2}]}'
  else
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"success","id":987654321,"html_url":"https://github.test/Rayer/llm-wiki-frontend/actions/runs/987654321","run_attempt":2}]}'
  fi
  exit 0
fi
if [[ "$url" == *"/v13/deployments/"* ]]; then
  if [[ "$scenario" == deployment-read-failure ]]; then
    exit 7
  fi
  if [[ -f "$root/mutated" && "$scenario" == post-unreadable ]]; then
    exit 7
  fi
  case "$scenario" in
    deployment-mismatch) jq '.projectId = "prj_other"' "$root/deployment.json" ;;
    missing-repository) jq 'del(.meta.githubOrg, .meta.githubRepo)' "$root/deployment.json" ;;
    mismatched-repository) jq '.meta.githubOrg = "Other" | .meta.githubRepo = "other-repo"' "$root/deployment.json" ;;
    source-provider-mismatch) jq '.meta.githubDeployment = "0"' "$root/deployment.json" ;;
    source-ref-mismatch) jq '.meta.githubCommitRef = "release"' "$root/deployment.json" ;;
    source-sha-mismatch) jq '.meta.githubCommitSha = "fedcba9876543210fedcba9876543210fedcba98"' "$root/deployment.json" ;;
    not-ready) jq '.readyState = "BUILDING"' "$root/deployment.json" ;;
    target-mismatch) jq '.target = "preview"' "$root/deployment.json" ;;
    post-target-mismatch)
      if [[ -f "$root/mutated" ]]; then jq '.target = "preview"' "$root/deployment.json"; else cat "$root/deployment.json"; fi
      ;;
    post-malformed)
      if [[ -f "$root/mutated" ]]; then jq '{id, url}' "$root/deployment.json"; else cat "$root/deployment.json"; fi
      ;;
    *) cat "$root/deployment.json" ;;
  esac
  exit 0
fi
if [[ "$url" == *"/v4/aliases?"* ]]; then
  domain="${url##*domain=}"
  domain="${domain%%&*}"
  if [[ "$scenario" == alias-read-failure && "$domain" == wiki.rayer.idv.tw ]]; then
    exit 7
  elif [[ "$scenario" == partial-readback && -f "$root/mutated" && "$domain" == llm-wiki-frontend.vercel.app ]]; then
    exit 7
  elif [[ "$scenario" == alias-changed-before-promote && "$domain" == wiki.rayer.idv.tw ]]; then
    read_count="$(grep -c "domain=$domain" "$root/curl-calls" || true)"
    if [[ "$read_count" -ge 3 ]]; then
      printf '%s' '{"aliases":[{"alias":"wiki.rayer.idv.tw","deploymentId":"dpl_changed"}]}'
    else
      printf '%s' '{"aliases":[{"alias":"wiki.rayer.idv.tw","deploymentId":"dpl_oldcustom"}]}'
    fi
  elif [[ "$scenario" == missing-alias && "$domain" == wiki.rayer.idv.tw ]]; then
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
  if [[ "$scenario" == redirect-host-mismatch && "$url" == https://wiki.rayer.idv.tw/* ]]; then
    printf '200\thttps://attacker.example/'
  elif [[ "$scenario" == health-failure && "$url" == https://wiki.rayer.idv.tw/* ]]; then
    printf '503\thttps://%s/' "${url#https://}"
  else
    printf '200\thttps://%s/' "${url#https://}"
  fi
  exit 0
fi
printf 'unexpected curl URL: %s\n' "$url" >&2
exit 1
