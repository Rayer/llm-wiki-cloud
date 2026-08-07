#!/usr/bin/env bash
set -eu

root="$FIXTURE_ROOT"
scenario="$(<"$root/scenario")"
url=""
data=""

increment_counter() {
  local path="$root/$1" count=0
  [[ -f "$path" ]] && count="$(<"$path")"
  count=$((count + 1))
  printf '%s' "$count" > "$path"
  printf '%s' "$count"
}
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

if [[ "$url" == *"/actions/runs/"* ]]; then
  case "$scenario" in
    ci-wrong-path) jq '.workflow_runs[0].path = ".github/workflows/other.yml" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-wrong-event) jq '.workflow_runs[0].event = "pull_request" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-wrong-ref) jq '.workflow_runs[0].head_branch = "feature" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-wrong-sha) jq '.workflow_runs[0].head_sha = "fedcba9876543210fedcba9876543210fedcba98" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-running) jq '.workflow_runs[0].status = "in_progress" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-failure) jq '.workflow_runs[0].conclusion = "failure" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-wrong-url) jq '.workflow_runs[0].html_url = "https://github.com/Rayer/other/actions/runs/123" | .workflow_runs[0]' "$root/ci.json" ;;
    ci-wrong-id) jq '.workflow_runs[0].id = 124 | .workflow_runs[0]' "$root/ci.json" ;;
    *) jq '.workflow_runs[0]' "$root/ci.json" ;;
  esac
elif [[ "$url" == *"/actions/workflows/ci.yml/runs?"* ]]; then
  if [[ "$scenario" == ci-failure ]]; then jq '.workflow_runs[0].conclusion = "failure"' "$root/ci.json"; else cat "$root/ci.json"; fi
elif [[ "$url" == *"/repos/$GITHUB_REPOSITORY/actions"* ]]; then
  cat "$root/ci.json"
elif [[ "$url" == *"/repos/$GITHUB_REPOSITORY"* ]]; then
  printf '%s' '{"id":98765,"full_name":"Rayer/llm-wiki-frontend"}'
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID/domains"* ]]; then
  if [[ "$scenario" == domain-missing ]]; then printf '%s' '{"domains":[]}'; elif [[ "$scenario" == domain-duplicate ]]; then jq '.domains += [{name:"llm-wiki-frontend-dev.vercel.app"}]' "$root/domains.json"; else cat "$root/domains.json"; fi
elif [[ "$url" == *"/v9/projects/$VERCEL_PROJECT_ID"* ]]; then
  canonical_reads="$(increment_counter canonical-project-reads)"
  if [[ "$scenario" == final-reread-authority-failure && "$canonical_reads" -ge 3 ]]; then exit 7; elif [[ "$scenario" == project-mismatch ]]; then jq '.name = "wrong-project"' "$root/canonical-project.json"; elif [[ "$scenario" == team-mismatch ]]; then jq '.accountId = "team_other"' "$root/canonical-project.json"; else cat "$root/canonical-project.json"; fi
elif [[ "$url" == *"/v9/projects/$EXPECTED_CURRENT_ALIAS_PROJECT_ID"* ]]; then
  if [[ "$scenario" == legacy-project-mismatch ]]; then jq '.name = "wrong-project"' "$root/legacy-project.json"; elif [[ "$scenario" == legacy-team-mismatch ]]; then jq '.accountId = "team_other"' "$root/legacy-project.json"; else cat "$root/legacy-project.json"; fi
elif [[ "$url" == *"/v13/deployments/dpl_old"* ]]; then
  case "$scenario" in
    old-source-mismatch) jq '.meta.githubCommitSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$root/old-deployment.json" ;;
    old-ref-mismatch) jq '.meta.githubCommitRef = "feature"' "$root/old-deployment.json" ;;
    old-repo-mismatch) jq '.meta.githubRepo = "other-repo"' "$root/old-deployment.json" ;;
    old-state-mismatch) jq '.readyState = "ERROR"' "$root/old-deployment.json" ;;
    old-target-mismatch) jq '.target = "production"' "$root/old-deployment.json" ;;
    *) cat "$root/old-deployment.json" ;;
  esac
elif [[ "$url" == *"/v13/deployments/dpl_new"* ]]; then
  case "$scenario" in
    create-read-failure) exit 9 ;;
    create-poll-timeout) jq '.readyState = "BUILDING"' "$root/candidate.json" ;;
    create-terminal-failed) jq '.readyState = "ERROR"' "$root/candidate.json" ;;
    create-source-mismatch) jq '.meta.githubCommitSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$root/candidate.json" ;;
    post-api-mismatch) if [[ -f "$root/mutated" ]]; then jq '.projectId = "prj_other"' "$root/candidate.json"; else cat "$root/candidate.json"; fi ;;
    candidate-source-mismatch) jq '.meta.githubCommitSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$root/candidate.json" ;;
    *) cat "$root/candidate.json" ;;
  esac
elif [[ "$url" == *"/v6/deployments?"* ]]; then
  if [[ "$scenario" == deployment-page-2-exact && "$url" != *"until=1700000000101"* ]]; then
    jq '{deployments: [{id:"dpl_other", projectId:"prj_other", teamId:"team_test", readyState:"READY", target:"preview", url:"https://other.vercel.app"}], pagination:{count:1,prev:null,next:1700000000101}}' "$root/candidate.json"
  elif [[ "$scenario" == deployment-page-2-exact && "$url" == *"until=1700000000101"* ]]; then
    jq '{deployments:[.], pagination:{count:1,prev:1700000000101,next:null}}' "$root/candidate.json"
  elif [[ "$scenario" == deployment-cursor-loop ]]; then
    printf '%s' '{"deployments":[],"pagination":{"count":1,"prev":null,"next":1700000000202}}'
  elif [[ "$scenario" == deployment-malformed ]]; then
    printf '%s' '{"deployments":[{"id":7}]}'
  elif [[ "$scenario" == deployment-pagination-string ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null,"next":"1700000000303"}}'
  elif [[ "$scenario" == deployment-pagination-bool ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null,"next":true}}'
  elif [[ "$scenario" == deployment-pagination-object ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null,"next":{}}}'
  elif [[ "$scenario" == deployment-pagination-float ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null,"next":1700000000.5}}'
  elif [[ "$scenario" == deployment-pagination-count-bool ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":true,"prev":null,"next":1700000000304}}'
  elif [[ "$scenario" == deployment-pagination-count-float ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1.5,"prev":null,"next":1700000000305}}'
  elif [[ "$scenario" == deployment-pagination-prev-bool ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":true,"next":1700000000404}}'
  elif [[ "$scenario" == deployment-pagination-prev-object ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":{},"next":1700000000405}}'
  elif [[ "$scenario" == deployment-pagination-negative ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null,"next":-1}}'
  elif [[ "$scenario" == deployment-pagination-missing ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{"count":1,"prev":null}}'
  elif [[ "$scenario" == deployment-pagination-malformed ]]; then
    printf '%s' '{"deployments":[{"id":"dpl_other","projectId":"prj_other","teamId":"team_test","readyState":"READY","target":"preview","url":"https://other.vercel.app"}],"pagination":{}}'
  elif [[ "$scenario" == deployment-page-max ]]; then
    deployment_reads="$(increment_counter deployment-page-reads)"
    printf '{"deployments":[],"pagination":{"count":0,"prev":null,"next":%s}}' "$((1700000000400 + deployment_reads))"
  elif [[ ( "$scenario" == create-needed || "$scenario" == production-before-create-drift || "$scenario" == post-create-authority-drift ) && ! -f "$root/created" ]]; then
    printf '%s' '{"deployments":[],"pagination":{"count":0,"prev":null,"next":null}}'
  else
    if [[ "$scenario" == duplicate-candidates ]]; then jq '{deployments:[.,.], pagination:{count:2,prev:null,next:null}}' "$root/candidate.json"; elif [[ "$scenario" == foreign-project-candidate && ! -f "$root/created" ]]; then jq '{deployments:[. | .projectId = "prj_foreign"], pagination:{count:1,prev:null,next:null}}' "$root/candidate.json"; elif [[ -f "$root/created" || "$scenario" != create-needed && "$scenario" != create-* ]]; then jq '{deployments:[.], pagination:{count:1,prev:null,next:null}}' "$root/candidate.json"; else printf '%s' '{"deployments":[],"pagination":{"count":0,"prev":null,"next":null}}'; fi
  fi
elif [[ "$url" == *"/v13/deployments?"* ]]; then
  printf '%s\n' "$data" >> "$root/deployment-post-log"
  case "$scenario" in
    create-failure) exit 8 ;;
    create-response-missing-id) touch "$root/created"; printf '%s' '{"url":"https://dpl_new.vercel.app"}' ;;
    create-response-invalid-id) touch "$root/created"; printf '%s' '{"id":"not-a-deployment","url":"https://dpl_new.vercel.app"}' ;;
    *) touch "$root/created"; printf '%s' '{"id":"dpl_new","url":"https://dpl_new.vercel.app"}' ;;
  esac
elif [[ "$url" == *"/v4/aliases/wiki.rayer.idv.tw"* ]]; then
  prod_reads=0; [[ -f "$root/production-reads" ]] && prod_reads="$(<"$root/production-reads")"; prod_reads=$((prod_reads + 1)); printf '%s' "$prod_reads" > "$root/production-reads"
  if [[ "$scenario" == production-missing ]]; then printf '%s' '{}'; elif [[ "$scenario" == production-before-create-drift && "$prod_reads" -ge 5 ]]; then jq '.production["wiki.rayer.idv.tw"] | .deploymentId = "dpl_production_drift"' "$root/state.json"; elif [[ "$scenario" == production-drift && -f "$root/mutated" ]]; then jq '.production["wiki.rayer.idv.tw"] | .deploymentId = "dpl_production_drift"' "$root/state.json"; else jq '.production["wiki.rayer.idv.tw"]' "$root/state.json"; fi
elif [[ "$url" == *"/v4/aliases/llm-wiki-frontend.vercel.app"* ]]; then
  prod_reads=0; [[ -f "$root/production-reads" ]] && prod_reads="$(<"$root/production-reads")"; prod_reads=$((prod_reads + 1)); printf '%s' "$prod_reads" > "$root/production-reads"
  if [[ "$scenario" == production-before-create-drift && "$prod_reads" -ge 5 ]]; then jq '.production["llm-wiki-frontend.vercel.app"] | .deploymentId = "dpl_production_drift"' "$root/state.json"; elif [[ "$scenario" == production-drift && -f "$root/mutated" ]]; then jq '.production["llm-wiki-frontend.vercel.app"] | .deploymentId = "dpl_production_drift"' "$root/state.json"; else jq '.production["llm-wiki-frontend.vercel.app"]' "$root/state.json"; fi
elif [[ "$url" == *"/v4/aliases/$STABLE_DOMAIN"* ]]; then
  if [[ "$scenario" == global-absent ]]; then printf '%s' '{}'; elif [[ "$scenario" == global-divergent || ("$scenario" == post-create-authority-drift && -f "$root/created") ]]; then jq '.global.deploymentId = "dpl_other"' "$root/state.json"; elif [[ "$scenario" == third-authority ]]; then jq '.global.projectId = "prj_other"' "$root/state.json"; else jq '.global' "$root/state.json"; fi
elif [[ "$url" == *"/v4/aliases?"* ]]; then
  project=""
  if [[ "$url" =~ projectId=([^\&]+) ]]; then project="${BASH_REMATCH[1]}"; fi
  inventory_reads="$(increment_counter inventory-reads)"
  if [[ "$scenario" == alias-cursor-loop && "$url" != *"until=1700000000111"* ]]; then
    printf '%s' '{"aliases":[],"pagination":{"count":1,"prev":null,"next":1700000000111}}'
  elif [[ "$scenario" == alias-cursor-loop && "$url" == *"until=1700000000111"* ]]; then
    printf '%s' '{"aliases":[],"pagination":{"count":1,"prev":1700000000111,"next":1700000000111}}'
  elif [[ "$scenario" == alias-page-2 && "$url" != *"until=1700000000102"* ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":1700000000102}}'
  elif [[ "$scenario" == alias-page-2 && "$url" == *"until=1700000000102"* ]]; then
    if [[ "$project" == "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" ]]; then jq '{aliases:.legacyAliases, pagination:{count:(.legacyAliases | length), prev:1700000000102, next:null}}' "$root/state.json"; else jq '{aliases:.canonicalAliases, pagination:{count:(.canonicalAliases | length), prev:1700000000102, next:null}}' "$root/state.json"; fi
  elif [[ "$scenario" == alias-malformed ]]; then
    printf '%s' '{"aliases":[{"alias":"llm-wiki-frontend-dev.vercel.app"}]}'
  elif [[ "$scenario" == alias-pagination-string ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":"1700000000303"}}'
  elif [[ "$scenario" == alias-pagination-bool ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":false}}'
  elif [[ "$scenario" == alias-pagination-object ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":{}}}'
  elif [[ "$scenario" == alias-pagination-float ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":1700000000.5}}'
  elif [[ "$scenario" == alias-pagination-count-bool ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":true,"prev":null,"next":1700000000404}}'
  elif [[ "$scenario" == alias-pagination-count-float ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1.5,"prev":null,"next":1700000000405}}'
  elif [[ "$scenario" == alias-pagination-prev-bool ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":true,"next":1700000000504}}'
  elif [[ "$scenario" == alias-pagination-prev-object ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":{},"next":1700000000505}}'
  elif [[ "$scenario" == alias-pagination-negative ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null,"next":-1}}'
  elif [[ "$scenario" == alias-pagination-missing ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{"count":1,"prev":null}}'
  elif [[ "$scenario" == alias-pagination-malformed ]]; then
    printf '%s' '{"aliases":[{"alias":"other.example","projectId":"prj_other","deploymentId":"dpl_other"}],"pagination":{}}'
  elif [[ "$scenario" == inventory-page-max ]]; then
    printf '%s' '{"aliases":[],"pagination":{"count":0,"prev":null,"next":%s}}' "$((1700000000600 + inventory_reads))"
  else
    if [[ "$project" == "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" ]]; then
      if [[ "$scenario" == inventory-order-drift && "$inventory_reads" -ge 5 ]]; then jq '{aliases:(.legacyAliases | reverse), pagination:{count:(.legacyAliases | length), prev:null, next:null}}' "$root/state.json"; else jq '{aliases:.legacyAliases, pagination:{count:(.legacyAliases | length), prev:null, next:null}}' "$root/state.json"; fi
    else
      if [[ "$scenario" == inventory-order-drift && "$inventory_reads" -ge 5 ]]; then jq '{aliases:(.canonicalAliases | reverse), pagination:{count:(.canonicalAliases | length), prev:null, next:null}}' "$root/state.json"; else jq '{aliases:.canonicalAliases, pagination:{count:(.canonicalAliases | length), prev:null, next:null}}' "$root/state.json"; fi
    fi
  fi
else
  exit 1
fi
