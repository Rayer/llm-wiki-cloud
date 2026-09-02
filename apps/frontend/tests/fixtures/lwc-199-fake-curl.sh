#!/usr/bin/env bash
set -eu
root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
vercel_base="${VERCEL_API_BASE_URL:-https://api.vercel.com}"
github_base="${GITHUB_API_URL:-https://api.github.com}"
url=""
auth_header=""
method="GET"
request_body=""
output_path=""
write_out=""
location_option=0
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --location)
      location_option=1
      shift
      ;;
    --header)
      header="${2:-}"
      if [[ "$header" == Authorization:* ]]; then
        auth_header="$header"
      fi
      shift 2
      ;;
    --request)
      method="$2"
      shift 2
      ;;
    --data)
      request_body="$2"
      shift 2
      ;;
    --output)
      output_path="$2"
      shift 2
      ;;
    --write-out)
      write_out="$2"
      shift 2
      ;;
    http://*|https://*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
if [[ -z "$url" ]]; then
  printf 'fake curl did not receive an endpoint\n' >&2
  exit 1
fi
printf '%s\n' "$url" >> "$root/curl-calls"
if [[ "$location_option" -eq 1 && "$method" == POST && "$url" == *"/v2/deployments/"*/aliases* ]]; then
  printf 'LOCATION_OPTION\n' >> "$root/curl-calls"
fi

check_provider_auth() {
  local provider="$1"
  local base_url="$2"
  local expected_header="$3"
  local endpoint="${url#"$base_url"}"
  if [[ "$auth_header" != "$expected_header" ]]; then
    printf 'AUTH_INVALID provider=%s endpoint=%s\n' "$provider" "$endpoint" >> "$root/auth-events"
    printf 'fake curl authentication rejected for provider=%s endpoint=%s\n' "$provider" "$endpoint" >&2
    exit 97
  fi
  printf 'AUTH_VALID provider=%s endpoint=%s\n' "$provider" "$endpoint" >> "$root/auth-events"
}

if [[ "$url" == "$vercel_base"/* ]]; then
  check_provider_auth vercel "$vercel_base" "Authorization: Bearer ${VERCEL_TOKEN:-}"
elif [[ "$url" == "$github_base"/* ]]; then
  check_provider_auth github "$github_base" "Authorization: Bearer ${GITHUB_TOKEN:-}"
fi

if [[ "$url" == *"/v9/projects/"* ]]; then
  cat "$root/project.json"
  exit 0
fi

if [[ "$method" == POST && "$url" == *"/v2/deployments/"*/aliases* ]]; then
  printf '%s\t%s\t%s\t%s\n' "$method" "$url" "${auth_header#Authorization: }" "$request_body" >> "$root/alias-post-calls"
  alias_post_call_number=$(( $(wc -l < "$root/alias-post-calls") ))
  alias_post_alias="$(jq -r '.alias // empty' <<< "$request_body")"
  if [[ "$alias_post_alias" != wiki.rayer.idv.tw && "$alias_post_alias" != llm-wiki-frontend.vercel.app ]]; then
    exit 93
  fi
  case "$scenario:$alias_post_call_number" in
    timeout:1|timeout-old:1)
      exit 28
      ;;
    timeout-target:1)
      jq --arg alias "$alias_post_alias" --arg deploymentId dpl_test123 '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      touch "$root/mutated"
      exit 28
      ;;
    conflict-target:1)
      jq --arg alias "$alias_post_alias" --arg deploymentId dpl_test123 '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      touch "$root/mutated"
      response='{"error":{"code":"conflict"}}'
      status=409
      ;;
    forbidden-wrong-target:1)
      jq --arg alias "$alias_post_alias" '.[$alias] = "dpl_wrong"' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      response='{"error":{"code":"forbidden"}}'
      status=403
      ;;
    drift-before-first-write:1|drift-before-second-write:2)
      if [[ "$scenario" == drift-before-first-write ]]; then drift_id=dpl_drift_before_first; else drift_id=dpl_drift_before_second; fi
      jq --arg alias "$alias_post_alias" --arg deploymentId "$drift_id" '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      response='{"error":{"code":"conflict"}}'
      status=409
      ;;
    drift-after-first-write:1)
      jq --arg alias "$alias_post_alias" --arg deploymentId dpl_test123 '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      jq --arg alias "llm-wiki-frontend.vercel.app" '.[$alias] = "dpl_drift_after_first"' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_$alias_post_call_number\",\"created\":\"2026-08-19T00:00:00.000Z\"}"
      status=200
      ;;
    forbidden:1)
      response='{"error":{"code":"forbidden","message":"raw provider body must stay private"}}'
      status=403
      ;;
    malformed-success:1)
      response='{"alias":"wrong.example","uid":123,"created":false}'
      status=200
      ;;
    empty-uid:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"\",\"created\":\"2026-08-19T00:00:00Z\"}"
      status=200
      ;;
    malformed-uid:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid/unsafe\",\"created\":\"2026-08-19T00:00:00Z\"}"
      status=200
      ;;
    unbounded-uid:1)
      unbounded_uid="$(printf 'u%.0s' $(seq 1 129))"
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"$unbounded_uid\",\"created\":\"2026-08-19T00:00:00Z\"}"
      status=200
      ;;
    empty-created:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid\",\"created\":\"\"}"
      status=200
      ;;
    non-iso-created:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid\",\"created\":\"yesterday\"}"
      status=200
      ;;
    malformed-old-deployment-id:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid\",\"created\":\"2026-08-19T00:00:00Z\",\"oldDeploymentId\":\"old\"}"
      status=200
      ;;
    valid-fractional-created:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid-1\",\"created\":\"2026-08-19T00:00:00.123Z\"}"
      status=200
      ;;
    valid-whole-second-created:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid_1\",\"created\":\"2026-08-19T00:00:00Z\"}"
      status=200
      ;;
    impossible-created-date:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid\",\"created\":\"2026-99-99T00:00:00Z\"}"
      status=200
      ;;
    impossible-created-time:1)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_valid\",\"created\":\"2026-08-19T24:60:60Z\"}"
      status=200
      ;;
    interrupt-in-flight:1)
      touch "$root/mutation-in-flight"
      sleep 30
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_interrupted\",\"created\":\"2026-08-19T00:00:00Z\"}"
      status=200
      ;;
    mutation-redirect:1)
      response=''
      status=307
      ;;
    second-failure:2|partial-mutation:2)
      response='{"error":{"code":"conflict","message":"raw provider body must stay private"}}'
      status=409
      ;;
    *)
      response="{\"alias\":\"$alias_post_alias\",\"uid\":\"uid_$alias_post_call_number\",\"created\":\"2026-08-19T00:00:00.000Z\",\"oldDeploymentId\":\"dpl_old\"}"
      status=200
      ;;
  esac
  if [[ "$scenario" == timeout-target && "$alias_post_call_number" == 1 || "$scenario" == timeout-old && "$alias_post_call_number" == 1 ]]; then
    if [[ "$scenario" == timeout-target ]]; then
      jq --arg alias "$alias_post_alias" --arg deploymentId dpl_test123 '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
      mv "$root/aliases.json.tmp" "$root/aliases.json"
    fi
  elif [[ "$status" == 200 && "$scenario" != malformed-success ]]; then
    jq --arg alias "$alias_post_alias" --arg deploymentId dpl_test123 '.[$alias] = $deploymentId' "$root/aliases.json" > "$root/aliases.json.tmp"
    mv "$root/aliases.json.tmp" "$root/aliases.json"
    touch "$root/mutated"
  fi
  if [[ "$scenario" == wrong-target && "$alias_post_call_number" == 1 ]]; then
    jq --arg alias "$alias_post_alias" '.[$alias] = "dpl_wrong"' "$root/aliases.json" > "$root/aliases.json.tmp"
    mv "$root/aliases.json.tmp" "$root/aliases.json"
  fi
  if [[ -n "$output_path" ]]; then printf '%s' "$response" > "$output_path"; fi
  if [[ "$write_out" == '%{http_code}' ]]; then printf '%s' "$status"; fi
  exit 0
fi

if [[ "$url" == *"/actions/workflows/ci.yml/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"failure","id":987654321,"html_url":"https://github.test/Rayer/llm-wiki-cloud/actions/runs/987654321","run_attempt":2}]}'
  else
    printf '%s' '{"workflow_runs":[{"path":".github/workflows/ci.yml","head_branch":"main","head_sha":"0123456789abcdef0123456789abcdef01234567","event":"push","status":"completed","conclusion":"success","id":987654321,"html_url":"https://github.test/Rayer/llm-wiki-cloud/actions/runs/987654321","run_attempt":2}]}'
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
    post-readback-mismatch)
      if [[ -f "$root/mutated" ]]; then jq '.target = "preview"' "$root/deployment.json"; else cat "$root/deployment.json"; fi
      ;;
    post-malformed)
      if [[ -f "$root/mutated" ]]; then jq '{id, url}' "$root/deployment.json"; else cat "$root/deployment.json"; fi
      ;;
    *) cat "$root/deployment.json" ;;
  esac
  exit 0
fi
if [[ "$url" == *"/v4/aliases/"* ]]; then
  alias_path="${url##*/v4/aliases/}"
  alias_path="${alias_path%%\?*}"

  if [[ "$scenario" == alias-read-failure && "$alias_path" == "wiki.rayer.idv.tw" ]]; then
    exit 7
  elif [[ "$scenario" == partial-readback && -f "$root/mutated" && "$alias_path" == "llm-wiki-frontend.vercel.app" ]]; then
    exit 7
  elif [[ "$scenario" == alias-changed-before-promote && "$alias_path" == "wiki.rayer.idv.tw" ]]; then
    read_count="$(grep -Fc "v4/aliases/$alias_path" "$root/curl-calls" || true)"
    if [[ "$read_count" -ge 3 ]]; then
      printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":"dpl_changed"}'
    else
      deployment="$(jq -r --arg alias "$alias_path" '.[$alias]' "$root/aliases.json")"
      printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":"%s"}' "$deployment"
    fi
  elif [[ "$scenario" == missing-alias && "$alias_path" == "wiki.rayer.idv.tw" ]]; then
    printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":""}'
  elif [[ "$scenario" == divergent-alias && "$alias_path" == "wiki.rayer.idv.tw" ]]; then
    printf '{"alias":"unexpected.example","projectId":"prj_test123","deploymentId":"dpl_wrong"}'
  elif [[ "$scenario" == alias-project-mismatch ]]; then
    deployment="$(jq -r --arg alias "$alias_path" '.[$alias]' "$root/aliases.json")"
    printf '{"alias":"%s","projectId":"prj_other","deploymentId":"%s"}' "$alias_path" "$deployment"
  elif [[ "$scenario" == post-readback-mismatch && -f "$root/mutated" && "$alias_path" == "llm-wiki-frontend.vercel.app" ]]; then
    read_count="$(grep -Fc "v4/aliases/$alias_path" "$root/curl-calls" || true)"
    if [[ "$read_count" -ge 6 ]]; then
      printf '{"alias":"llm-wiki-frontend.vercel.app","projectId":"prj_test123","deploymentId":"dpl_other"}'
    else
      deployment="$(jq -r --arg alias "$alias_path" '.[$alias]' "$root/aliases.json")"
      printf '{"alias":"llm-wiki-frontend.vercel.app","projectId":"prj_test123","deploymentId":"%s"}' "$deployment"
    fi
  else
    deployment="$(jq -r --arg alias "$alias_path" '.[$alias]' "$root/aliases.json")"
    printf '{"alias":"%s","projectId":"prj_test123","deploymentId":"%s"}' "$alias_path" "$deployment"
  fi
  exit 0
fi

if [[ "$url" == *"/v4/aliases?"* ]]; then
  if [[ "$scenario" == "single-alias-only" ]]; then
    exit 7
  fi

  domain="${url##*domain=}"
  domain="${domain%%&*}"
  if [[ "$scenario" == "alias-read-failure" && "$domain" == wiki.rayer.idv.tw ]]; then
    exit 7
  elif [[ "$scenario" == "partial-readback" && -f "$root/mutated" && "$domain" == llm-wiki-frontend.vercel.app ]]; then
    exit 7
  elif [[ "$scenario" == "alias-changed-before-promote" && "$domain" == wiki.rayer.idv.tw ]]; then
    read_count="$(grep -c "domain=$domain" "$root/curl-calls" || true)"
    if [[ "$read_count" -ge 3 ]]; then
      printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":"dpl_changed"}'
    else
      printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":"dpl_oldcustom"}'
    fi
  elif [[ "$scenario" == "missing-alias" && "$domain" == wiki.rayer.idv.tw ]]; then
    printf '{"alias":"wiki.rayer.idv.tw","projectId":"prj_test123","deploymentId":""}'
  elif [[ "$scenario" == "divergent-alias" && "$domain" == wiki.rayer.idv.tw ]]; then
    printf '{"alias":"unexpected.example","projectId":"prj_test123","deploymentId":"dpl_wrong"}'
  elif [[ "$scenario" == "alias-project-mismatch" ]]; then
    printf '{"alias":"%s","projectId":"prj_other","deploymentId":"dpl_oldcustom"}' "$domain"
  elif [[ "$scenario" == "post-readback-mismatch" && -f "$root/mutated" && "$domain" == llm-wiki-frontend.vercel.app ]]; then
    read_count="$(grep -c "domain=$domain" "$root/curl-calls" || true)"
    if [[ "$read_count" -ge 6 ]]; then
      printf '{"alias":"llm-wiki-frontend.vercel.app","projectId":"prj_test123","deploymentId":"dpl_other"}'
    else
      deployment="$(jq -r --arg domain "$domain" '.[$domain]' "$root/aliases.json")"
      printf '{"alias":"%s","projectId":"prj_test123","deploymentId":"%s"}' "$domain" "$deployment"
    fi
  else
    deployment="$(jq -r --arg domain "$domain" '.[$domain]' "$root/aliases.json")"
    printf '{"alias":"%s","projectId":"prj_test123","deploymentId":"%s"}' "$domain" "$deployment"
  fi
  exit 0
fi
if [[ "$url" == https://wiki.rayer.idv.tw/* || "$url" == https://llm-wiki-frontend.vercel.app/* ]]; then
  if [[ "$scenario" == health-transport-failure && "$url" == https://wiki.rayer.idv.tw/* ]]; then
    exit 7
  fi
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
