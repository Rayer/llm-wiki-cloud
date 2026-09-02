#!/usr/bin/env bash
set -Eeo pipefail

MODE="${1:-}"
case "$MODE" in validate|preflight|mutate) ;; *) printf 'usage: %s {validate|preflight|mutate}\n' "$0" >&2; exit 2 ;; esac

readonly EXPECTED_REPOSITORY="Rayer/llm-wiki-cloud"
readonly EXPECTED_PROJECT_NAME="llm-wiki-frontend"
readonly EXPECTED_ROOT_DIRECTORY="apps/frontend"
readonly EXPECTED_SCOPE="rayer-tung-s-projects"
readonly ENV_KEY="NEXT_PUBLIC_AUTH_URL"
readonly DESIRED_VALUE="https://auth.rayer.idv.tw"
readonly ALIASES=("wiki.rayer.idv.tw" "llm-wiki-frontend.vercel.app")
readonly API_BASE_URL="${VERCEL_API_BASE_URL:-https://api.vercel.com}"
readonly GITHUB_BASE_URL="${GITHUB_API_URL:-https://api.github.com}"
readonly EVIDENCE_PATH="${EVIDENCE_DIR:?}/vercel-production-auth-env.json"
readonly ROLLBACK_PATH="$EVIDENCE_DIR/rollback-contract.json"
readonly DEPLOYMENT_POLL_ATTEMPTS="${DEPLOYMENT_POLL_ATTEMPTS:-30}"
readonly DEPLOYMENT_POLL_INTERVAL_SECONDS="${DEPLOYMENT_POLL_INTERVAL_SECONDS:-2}"

STATUS="FAILED"; REASON_CODE="UNEXPECTED_FAILURE"; REASON="unexpected failure"; PHASE="$MODE"
EVIDENCE_WRITTEN=0; ENV_MUTATION_COUNT=0; DEPLOYMENT_CREATE_COUNT=0
CURRENT_HEAD_SHA=""; CURRENT_REMOTE_MAIN_SHA=""; CI_RUN_ID=""; CI_RUN_URL=""
APPLICABLE_JSON="[]"; PRIOR_KIND="unknown"; PRIOR_ENV_JSON="null"; DECISION="unknown"; CONTRACT_SHA256=""
PROVIDER_ENV_ID=""; PROVIDER_TARGETS="[]"; PROVIDER_BRANCH_SCOPE="null"; PROVIDER_TYPE=""
PROVIDER_VALUE_EQUAL=false; PROVIDER_TARGET_EQUAL=false; PROVIDER_BRANCH_EQUAL=false; PROVIDER_SINGLETON=false
ROLLBACK_ATTEMPTED=false; ROLLBACK_RESULT="not_attempted"; PROVIDER_CHECKS="[]"
FREEZE_ALIASES="[]"; FREEZE_DEPLOYMENTS="[]"
DEPLOYMENT_ID=""; DEPLOYMENT_URL=""; DEPLOYMENT_READY_STATE=""; DEPLOYMENT_TARGET=""
DEPLOYMENT_SOURCE=""; DEPLOYMENT_REF=""; DEPLOYMENT_SHA=""; DEPLOYMENT_REPOSITORY=""
DEPLOYMENT_CHECKS="[]"; PARTIAL_UNCERTAINTY=false

add_check() { PROVIDER_CHECKS=$(jq -c --arg check "$1" '. + [$check]' <<< "$PROVIDER_CHECKS"); }
add_deployment_check() { DEPLOYMENT_CHECKS=$(jq -c --arg check "$1" '. + [$check]' <<< "$DEPLOYMENT_CHECKS"); }

write_evidence() {
  [[ "$EVIDENCE_WRITTEN" == 1 ]] && return
  EVIDENCE_WRITTEN=1
  mkdir -p "$EVIDENCE_DIR"
  jq -n \
    --arg ticket "$TICKET_REF" --arg repository "$GITHUB_REPOSITORY" --arg sha "$COMMIT_SHA" \
    --arg checked "$CURRENT_HEAD_SHA" --arg remote "$CURRENT_REMOTE_MAIN_SHA" --arg ci_id "$CI_RUN_ID" --arg ci_url "$CI_RUN_URL" \
    --arg project "${VERCEL_PROJECT_ID:-}" --arg team "${VERCEL_TEAM_ID:-}" --arg key "$ENV_KEY" --arg desired "$DESIRED_VALUE" \
    --arg prior_kind "$PRIOR_KIND" --arg prior_id "$(jq -r 'if . == null then "" elif type == "array" then (.[0].id // "") else (.id // "") end' <<< "$PRIOR_ENV_JSON")" \
    --arg prior_digest "$CONTRACT_SHA256" --arg artifact_id "${ROLLBACK_ARTIFACT_ID:-}" --arg artifact_url "${ROLLBACK_ARTIFACT_URL:-}" \
    --arg artifact_digest "${ROLLBACK_ARTIFACT_DIGEST:-}" --arg artifact_name "${ROLLBACK_ARTIFACT_NAME:-}" \
    --arg status "$STATUS" --arg phase "$PHASE" --arg reason_code "$REASON_CODE" --arg reason "$REASON" --arg decision "$DECISION" \
    --arg env_id "$PROVIDER_ENV_ID" --arg branch "$PROVIDER_BRANCH_SCOPE" --arg type "$PROVIDER_TYPE" --arg rollback_result "$ROLLBACK_RESULT" \
    --arg deployment_id "$DEPLOYMENT_ID" --arg deployment_url "$DEPLOYMENT_URL" --arg ready_state "$DEPLOYMENT_READY_STATE" \
    --arg deployment_target "$DEPLOYMENT_TARGET" --arg deployment_source "$DEPLOYMENT_SOURCE" --arg deployment_ref "$DEPLOYMENT_REF" \
    --arg deployment_sha "$DEPLOYMENT_SHA" --arg deployment_repository "$DEPLOYMENT_REPOSITORY" \
    --argjson aliases "$FREEZE_ALIASES" --argjson deployments "$FREEZE_DEPLOYMENTS" --argjson checks "$PROVIDER_CHECKS" --argjson targets "$PROVIDER_TARGETS" \
    --argjson deployment_checks "$DEPLOYMENT_CHECKS" --argjson value_equal "$PROVIDER_VALUE_EQUAL" \
    --argjson target_equal "$PROVIDER_TARGET_EQUAL" --argjson branch_equal "$PROVIDER_BRANCH_EQUAL" --argjson singleton "$PROVIDER_SINGLETON" \
    --argjson rollback_attempted "$ROLLBACK_ATTEMPTED" --argjson partial_uncertainty "$PARTIAL_UNCERTAINTY" \
    --arg env_count "$ENV_MUTATION_COUNT" --arg create_count "$DEPLOYMENT_CREATE_COUNT" \
    'def n: if . == "" then null else . end;
     def number_or_null: if test("^[0-9]+$") then tonumber else null end;
     {schema_version:2, ticket_ref:($ticket|n), environment:"production", action:"configure_and_deploy",
      source:{repository:$repository, ref:"refs/heads/main", commit_sha:($sha|n), checked_out_sha:($checked|n),
        current_remote_main_sha:($remote|n), canonical_ci:{workflow:"ci.yml", event:"push", head_branch:"main", head_sha:($sha|n),
          run_id:($ci_id|number_or_null), run_url:($ci_url|n), conclusion:(if $ci_id=="" then null else "success" end)}},
      target:{project_name:"llm-wiki-frontend", project_id:($project|n), team_id:($team|n)},
      variable:{key:$key, desired_value:$desired, type:"plain", targets:["production"], branch_scope:null},
      prior_state:{kind:$prior_kind, env_id:(($prior_id)|n), artifact_contract_sha256:($prior_digest|n)},
      rollback:{artifact_name:($artifact_name|n), artifact_id:($artifact_id|number_or_null), artifact_url:($artifact_url|n),
        artifact_digest:($artifact_digest|n), attempted:$rollback_attempted, result:$rollback_result,
        independent_readback:($rollback_result=="RESTORED")},
      freeze:{aliases:$aliases, deployments:$deployments},
      env_mutation_count:($env_count|tonumber), deployment_create_count:($create_count|tonumber), partial_uncertainty:$partial_uncertainty,
      provider_verification:{environment:{result:(if $status=="SUCCESS" then "verified" else "not_verified" end), checks:$checks,
        mutation_count:($env_count|tonumber), singleton:$singleton, env_id:($env_id|n), targets:$targets,
        branch_scope:(if $branch=="" or $branch=="null" then null else $branch end), type:($type|n), value_equals_desired:$value_equal,
        targets_equal_production_only:$target_equal, branch_scope_equals_unscoped:$branch_equal},
        deployment:{result:(if $deployment_id=="" then "not_verified" elif $status=="SUCCESS" then "verified" else "partial" end),
          checks:$deployment_checks, id:($deployment_id|n), url:($deployment_url|n), ready_state:($ready_state|n), target:($deployment_target|n),
          source:($deployment_source|n), ref:($deployment_ref|n), sha:($deployment_sha|n), repository:($deployment_repository|n)}},
      decision:$decision, phase:$phase, status:$status, reason_code:$reason_code, reason:$reason}' > "$EVIDENCE_PATH.tmp"
  mv "$EVIDENCE_PATH.tmp" "$EVIDENCE_PATH"
}
trap 'exit_code=$?; write_evidence; exit "$exit_code"' EXIT

fail() { STATUS="$1"; REASON_CODE="$2"; REASON="$3"; printf '%s: %s\n' "$REASON_CODE" "$REASON" >&2; exit 1; }
preflight_fail() { fail PREFLIGHT_FAILED "$1" "$2"; }
post_fail() { fail FAILED "$1" "$2"; }
partial_fail() { PARTIAL_UNCERTAINTY=true; fail PARTIAL_MUTATION "$1" "$2"; }

require_tools() {
  for command in curl jq sha256sum git awk; do command -v "$command" >/dev/null 2>&1 || preflight_fail TOOL_MISSING "required command is unavailable"; done
}
validate_inputs() {
  [[ "$COMMIT_SHA" =~ ^[0-9a-f]{40}$ ]] || preflight_fail INPUT_SHA_INVALID "commit_sha must be exactly 40 lowercase hexadecimal characters"
  [[ "$TICKET_REF" =~ ^[A-Za-z0-9._/-]+$ ]] || preflight_fail TICKET_REF_INVALID "ticket_ref is required and contains unsupported characters"
  [[ "$GITHUB_REPOSITORY" == "$EXPECTED_REPOSITORY" ]] || preflight_fail REPOSITORY_NOT_ALLOWLISTED "repository identity is not allowlisted"
  [[ "$DEPLOYMENT_POLL_ATTEMPTS" =~ ^[1-9][0-9]*$ && "$DEPLOYMENT_POLL_ATTEMPTS" -le 60 ]] || preflight_fail POLL_CONFIG_INVALID "deployment polling attempts are not bounded"
  [[ "$DEPLOYMENT_POLL_INTERVAL_SECONDS" =~ ^[0-9]+$ && "$DEPLOYMENT_POLL_INTERVAL_SECONDS" -le 30 ]] || preflight_fail POLL_CONFIG_INVALID "deployment polling interval is not bounded"
  if [[ "$GITHUB_ACTIONS" == true ]]; then
    [[ "$GITHUB_REF" == refs/heads/main ]] || preflight_fail REF_NOT_ALLOWLISTED "workflow must run from canonical main"
    [[ "$API_BASE_URL" == https://api.vercel.com && "$GITHUB_BASE_URL" == https://api.github.com ]] || preflight_fail API_ORIGIN_NOT_ALLOWLISTED "canonical API origins are required"
  fi
  if [[ "$MODE" != validate ]]; then
    [[ -n "$VERCEL_TOKEN" && -n "$VERCEL_PROJECT_ID" && -n "$VERCEL_TEAM_ID" && -n "$VERCEL_SCOPE" ]] || preflight_fail CONFIG_MISSING "required Vercel configuration is missing"
    [[ "$VERCEL_PROJECT_ID" =~ ^prj_[A-Za-z0-9]+$ && "$VERCEL_TEAM_ID" =~ ^team_[A-Za-z0-9]+$ ]] || preflight_fail CONFIG_ID_INVALID "Vercel project or team ID is not bounded"
    [[ "$VERCEL_SCOPE" == "$EXPECTED_SCOPE" ]] || preflight_fail SCOPE_NOT_ALLOWLISTED "Vercel scope is not allowlisted"
  fi
}
github_get() {
  curl --fail-with-body --silent --show-error --location --connect-timeout 10 --max-time 30 \
    --header "Authorization: Bearer $GITHUB_TOKEN" --header 'Accept: application/vnd.github+json' "$GITHUB_BASE_URL$1" 2>/dev/null
}
vercel_get() {
  curl --fail-with-body --silent --show-error --location --connect-timeout 10 --max-time 30 \
    --header "Authorization: Bearer $VERCEL_TOKEN" --header 'Accept: application/json' "$API_BASE_URL$1" 2>/dev/null
}
vercel_mutate() {
  curl --fail-with-body --silent --show-error --location --connect-timeout 10 --max-time 30 --request "$1" \
    --header "Authorization: Bearer $VERCEL_TOKEN" --header 'Accept: application/json' --header 'Content-Type: application/json' \
    --data "$3" "$API_BASE_URL$2" 2>/dev/null
}
validate_exact_source() {
  require_tools; validate_inputs
  [[ -n "$GITHUB_TOKEN" ]] || preflight_fail GITHUB_TOKEN_MISSING "GitHub token is required for canonical CI read-back"
  CURRENT_HEAD_SHA=$(git rev-parse HEAD 2>/dev/null) || preflight_fail CHECKED_OUT_SHA_UNAVAILABLE "checked-out SHA could not be read"
  CURRENT_REMOTE_MAIN_SHA=$(git ls-remote origin refs/heads/main 2>/dev/null | awk 'NR == 1 {print $1}')
  [[ "$COMMIT_SHA" == "$CURRENT_HEAD_SHA" ]] || preflight_fail CHECKED_OUT_SHA_MISMATCH "checked-out SHA differs from input"
  [[ "$COMMIT_SHA" == "$CURRENT_REMOTE_MAIN_SHA" ]] || preflight_fail REMOTE_MAIN_SHA_MISMATCH "origin/main differs from input"
  local runs ci_run
  runs=$(github_get "/repos/$GITHUB_REPOSITORY/actions/workflows/ci.yml/runs?head_sha=$COMMIT_SHA&branch=main&event=push&per_page=100") || preflight_fail CI_READ_FAILED "canonical CI read failed"
  ci_run=$(jq -c --arg sha "$COMMIT_SHA" 'first(.workflow_runs[]? | select(.path==".github/workflows/ci.yml" and .head_branch=="main" and .head_sha==$sha and .event=="push" and .status=="completed" and .conclusion=="success" and (.id|type)=="number" and (.html_url|type)=="string")) // empty' <<< "$runs")
  [[ -n "$ci_run" ]] || preflight_fail CI_NOT_GREEN "successful canonical CI was not found for exact SHA"
  CI_RUN_ID=$(jq -r '.id' <<< "$ci_run"); CI_RUN_URL=$(jq -r '.html_url' <<< "$ci_run"); add_check exact_main_ci
}
read_project() {
  local response
  response=$(vercel_get "/v9/projects/$VERCEL_PROJECT_ID?teamId=$VERCEL_TEAM_ID") || return 1
  jq -e --arg id "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg name "$EXPECTED_PROJECT_NAME" --arg root "$EXPECTED_ROOT_DIRECTORY" 'type=="object" and .id==$id and .name==$name and .accountId==$team and .rootDirectory==$root and .link.type=="github" and .link.org=="Rayer" and .link.repo=="llm-wiki-cloud" and .link.productionBranch=="main"' <<< "$response" >/dev/null || return 1
  # The public /v13/deployments schema has no autoAssignCustomDomains request field.
  # Require the authoritative project setting to prove production creation is staged.
  jq -e 'has("autoAssignCustomDomains") and .autoAssignCustomDomains==false' <<< "$response" >/dev/null || return 2
  add_check project_id_team_name_exact
  add_check auto_assign_custom_domains_disabled
}
require_project() {
  if read_project; then return 0; fi
  local project_status=$?
  if [[ "$project_status" == 2 ]]; then
    preflight_fail AUTO_ALIAS_CONTRACT_UNAVAILABLE "Vercel cannot prove no production alias/domain routing for this deployment"
  fi
  preflight_fail PROJECT_METADATA_MISMATCH "Vercel project metadata was not exact"
}
read_env_state() {
  local response
  response=$(vercel_get "/v9/projects/$VERCEL_PROJECT_ID/env?teamId=$VERCEL_TEAM_ID") || return 1
  APPLICABLE_JSON=$(jq -ce --arg key "$ENV_KEY" '(.envs//.) as $envs | if ($envs|type)!="array" then error("env inventory is not an array") else [$envs[] | select(.key==$key and ((.target//[])|index("production"))!=null) | {id,key,value,target,gitBranch:(if (.gitBranch//null)=="" then null else (.gitBranch//null) end),type}] end' <<< "$response") || return 1
  jq -e 'all(.[]; (.id|type)=="string" and (.key|type)=="string" and (.value|type)=="string" and (.target|type)=="array" and (.gitBranch==null or (.gitBranch|type)=="string") and (.type|type)=="string")' <<< "$APPLICABLE_JSON" >/dev/null || return 1
}
read_freeze_state() {
  local aliases='[]' deployments='[]' alias response id encoded deployment
  for alias in "${ALIASES[@]}"; do
    encoded=$(jq -rn --arg value "$alias" '$value|@uri')
    response=$(vercel_get "/v4/aliases/$encoded?teamId=$VERCEL_TEAM_ID") || return 1
    # GET /v4/aliases/{idOrAlias} is already teamId-scoped. Its OpenAPI shape
    # identifies the exact alias, project, and deployment, but has no teamId or
    # accountId field; the Production token and request scope provide authority.
    jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" 'type=="object" and .alias==$alias and .projectId==$project and (.deploymentId|type)=="string" and (.deploymentId|test("^dpl_[A-Za-z0-9]+$"))' <<< "$response" >/dev/null || return 1
    id=$(jq -r '.deploymentId' <<< "$response")
    aliases=$(jq -c --arg alias "$alias" --arg id "$id" --arg project "$(jq -r '.projectId // ""' <<< "$response")" '. + [{alias:$alias,project_id:($project|if .=="" then null else . end),deployment_id:$id}]' <<< "$aliases")
    if ! jq -e --arg id "$id" 'any(.[]; .id==$id)' <<< "$deployments" >/dev/null; then
      deployment=$(vercel_get "/v13/deployments/$id?teamId=$VERCEL_TEAM_ID&withGitRepoInfo=true") || return 1
      jq -e --arg id "$id" --arg project "$VERCEL_PROJECT_ID" 'type=="object" and ((.id==$id) or (.uid==$id)) and .projectId==$project and ((.id // .uid)|test("^dpl_[A-Za-z0-9]+$"))' <<< "$deployment" >/dev/null || return 1
      deployments=$(jq -c --arg id "$id" --arg url "$(jq -r '.url // ""' <<< "$deployment")" --arg project "$(jq -r '.projectId // ""' <<< "$deployment")" --arg team "$VERCEL_TEAM_ID" --arg ready "$(jq -r '.readyState // .ready_state // ""' <<< "$deployment")" --arg target "$(jq -r '.target // ""' <<< "$deployment")" '. + [{id:$id,url:($url|if .=="" then null else . end),project_id:($project|if .=="" then null else . end),team_id:$team,ready_state:($ready|if .=="" then null else . end),target:($target|if .=="" then null else . end)}]' <<< "$deployments")
    fi
  done
  FREEZE_ALIASES="$aliases"; FREEZE_DEPLOYMENTS="$deployments"; add_check canonical_aliases_and_deployments_frozen
}
set_provider_observation() {
  local count; count=$(jq length <<< "$APPLICABLE_JSON")
  PROVIDER_ENV_ID=""; PROVIDER_TARGETS="[]"; PROVIDER_BRANCH_SCOPE="null"; PROVIDER_TYPE=""; PROVIDER_SINGLETON=false
  PROVIDER_VALUE_EQUAL=false; PROVIDER_TARGET_EQUAL=false; PROVIDER_BRANCH_EQUAL=false
  if [[ "$count" == 1 ]]; then
    PROVIDER_SINGLETON=true; PROVIDER_ENV_ID=$(jq -r '.[0].id' <<< "$APPLICABLE_JSON"); PROVIDER_TARGETS=$(jq -c '.[0].target' <<< "$APPLICABLE_JSON")
    PROVIDER_BRANCH_SCOPE=$(jq -c '.[0].gitBranch' <<< "$APPLICABLE_JSON"); PROVIDER_TYPE=$(jq -r '.[0].type' <<< "$APPLICABLE_JSON")
    PROVIDER_VALUE_EQUAL=$(jq -c --arg desired "$DESIRED_VALUE" '.[0].value==$desired' <<< "$APPLICABLE_JSON")
    PROVIDER_TARGET_EQUAL=$(jq -c '.[0].target==["production"]' <<< "$APPLICABLE_JSON"); PROVIDER_BRANCH_EQUAL=$(jq -c '.[0].gitBranch==null' <<< "$APPLICABLE_JSON")
  fi
}
capture_rollback() {
  local count; count=$(jq length <<< "$APPLICABLE_JSON")
  [[ "$count" -le 1 ]] || preflight_fail ENV_DUPLICATE "multiple applicable production auth entries were found"
  if [[ "$count" == 1 ]]; then
    jq -e '.[0].target==["production"] and .[0].gitBranch==null and .[0].type=="plain"' <<< "$APPLICABLE_JSON" >/dev/null || preflight_fail ENV_SCOPE_INVALID "existing production auth entry must be exactly plain, unscoped production"
  fi
  if [[ "$count" == 0 ]]; then PRIOR_KIND=absent; PRIOR_ENV_JSON=null; DECISION=create
  else
    PRIOR_KIND=present; PRIOR_ENV_JSON=$(jq -c '.[0]' <<< "$APPLICABLE_JSON")
    if jq -e --arg desired "$DESIRED_VALUE" '.[0].value==$desired and .[0].target==["production"] and .[0].gitBranch==null and .[0].type=="plain"' <<< "$APPLICABLE_JSON" >/dev/null; then DECISION=noop; else DECISION=update; fi
  fi
  mkdir -p "$EVIDENCE_DIR"
  jq -n --arg sha "$COMMIT_SHA" --arg ticket "$TICKET_REF" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg key "$ENV_KEY" --arg decision "$DECISION" --argjson previous "$APPLICABLE_JSON" --argjson aliases "$FREEZE_ALIASES" --argjson deployments "$FREEZE_DEPLOYMENTS" \
    '{schema_version:2,kind:"vercel-production-auth-env-rollback-contract",source:{repository:"Rayer/llm-wiki-cloud",ref:"refs/heads/main",commit_sha:$sha,ticket_ref:$ticket,canonical_ci:{workflow:"ci.yml",head_branch:"main",head_sha:$sha}},target:{project_id:$project,team_id:$team,key:$key,project_name:"llm-wiki-frontend"},decision:$decision,prior_state:(if ($previous|length)==0 then {kind:"absent"} else {kind:"present",env:$previous[0]} end),freeze:{aliases:$aliases,deployments:$deployments}}' > "$ROLLBACK_PATH.tmp"
  mv "$ROLLBACK_PATH.tmp" "$ROLLBACK_PATH"; CONTRACT_SHA256=$(sha256sum "$ROLLBACK_PATH" | awk '{print $1}')
  add_check filtered_production_env_read; add_check rollback_snapshot_captured
}
load_rollback_contract() {
  [[ -f "$ROLLBACK_PATH" ]] || preflight_fail ROLLBACK_CONTRACT_MISSING "rollback contract is missing"
  [[ "${ROLLBACK_ARTIFACT_ID:-}" =~ ^[0-9]+$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact ID is missing or malformed"
  [[ "${ROLLBACK_ARTIFACT_DIGEST:-}" =~ ^[0-9a-f]{64}$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact digest is missing or malformed"
  ROLLBACK_ARTIFACT_DIGEST="sha256:$ROLLBACK_ARTIFACT_DIGEST"
  [[ "${ROLLBACK_ARTIFACT_NAME:-}" == vercel-production-auth-env-rollback-$COMMIT_SHA ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact name is not exact"
  [[ "${ROLLBACK_ARTIFACT_URL:-}" == https://github.com/$GITHUB_REPOSITORY/actions/runs/*/artifacts/${ROLLBACK_ARTIFACT_ID:-} ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact URL is not exact"
  local contract; contract=$(cat "$ROLLBACK_PATH") || preflight_fail ROLLBACK_CONTRACT_READ_FAILED "rollback contract could not be read"
  jq -e --arg sha "$COMMIT_SHA" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg key "$ENV_KEY" \
    '.kind=="vercel-production-auth-env-rollback-contract" and .source.commit_sha==$sha and .source.ref=="refs/heads/main" and .source.canonical_ci.head_branch=="main" and .target.project_id==$project and .target.team_id==$team and .target.key==$key and (.prior_state.kind=="absent" or (.prior_state.kind=="present" and (.prior_state.env.id|type)=="string")) and (.freeze.aliases|type)=="array" and (.freeze.deployments|type)=="array" and all(.freeze.aliases[]; .project_id==$project and (.deployment_id|type)=="string" and (.deployment_id|test("^dpl_[A-Za-z0-9]+$"))) and all(.freeze.deployments[]; .project_id==$project and .team_id==$team and (.id|type)=="string" and (.id|test("^dpl_[A-Za-z0-9]+$")))' <<< "$contract" >/dev/null || preflight_fail ROLLBACK_CONTRACT_INVALID "rollback contract identity or freeze state is invalid"
  CONTRACT_SHA256=$(sha256sum "$ROLLBACK_PATH" | awk '{print $1}'); PRIOR_KIND=$(jq -r '.prior_state.kind' <<< "$contract")
  if [[ "$PRIOR_KIND" == present ]]; then PRIOR_ENV_JSON=$(jq -c '[.prior_state.env]' <<< "$contract"); else PRIOR_ENV_JSON='[]'; fi
  DECISION=$(jq -r '.decision' <<< "$contract"); FREEZE_ALIASES=$(jq -c '.freeze.aliases' <<< "$contract"); FREEZE_DEPLOYMENTS=$(jq -c '.freeze.deployments' <<< "$contract")
}
exact_state_matches_previous() { [[ "$(jq -S -c . <<< "$APPLICABLE_JSON")" == "$(jq -S -c . <<< "$PRIOR_ENV_JSON")" ]]; }
# jq's sorted compact form gives the durable freeze comparison a byte-normalized identity.
freeze_state_matches_contract() { [[ "$(jq -S -c . <<< "$FREEZE_ALIASES")" == "$(jq -S -c . <<< "$(jq -c '.freeze.aliases' "$ROLLBACK_PATH")")" && "$(jq -S -c . <<< "$FREEZE_DEPLOYMENTS")" == "$(jq -S -c . <<< "$(jq -c '.freeze.deployments' "$ROLLBACK_PATH")")" ]]; }
restore_previous() {
  ROLLBACK_ATTEMPTED=true
  if [[ "$PRIOR_KIND" == absent ]]; then
    local count id response; read_env_state || { ROLLBACK_RESULT=FAILED; return 1; }; count=$(jq length <<< "$APPLICABLE_JSON")
    if [[ "$count" == 1 ]]; then id=$(jq -r '.[0].id' <<< "$APPLICABLE_JSON"); if ! response=$(vercel_mutate DELETE "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" '{}'); then ROLLBACK_RESULT=FAILED; return 1; fi; ENV_MUTATION_COUNT=$((ENV_MUTATION_COUNT + 1)); elif [[ "$count" -gt 1 ]]; then ROLLBACK_RESULT=FAILED; return 1; fi
  else
    local id body response; id=$(jq -r '.[0].id' <<< "$PRIOR_ENV_JSON"); body=$(jq -c '.[0] | {key,value,target,type} + (if .gitBranch==null then {} else {gitBranch:.gitBranch} end)' <<< "$PRIOR_ENV_JSON")
    ENV_MUTATION_COUNT=$((ENV_MUTATION_COUNT + 1)); if ! response=$(vercel_mutate PATCH "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" "$body"); then ROLLBACK_RESULT=FAILED; return 1; fi
  fi
  read_env_state || { ROLLBACK_RESULT=FAILED; return 1; }; exact_state_matches_previous || { ROLLBACK_RESULT=FAILED; return 1; }
  ROLLBACK_RESULT=RESTORED; add_check rollback_readback_exact
}
rollback_and_fail() { if restore_previous; then post_fail "$1" "$2; prior provider state restored"; else post_fail ROLLBACK_FAILED "$2; rollback read-back failed"; fi; }
apply_and_verify_env() {
  if [[ "$DECISION" == noop ]]; then
    set_provider_observation
    if [[ "$PROVIDER_SINGLETON" != true || "$PROVIDER_VALUE_EQUAL" != true || "$PROVIDER_TARGET_EQUAL" != true || "$PROVIDER_BRANCH_EQUAL" != true || "$PROVIDER_TYPE" != plain ]]; then rollback_and_fail POSTCHECK_MISMATCH "idempotent provider state was not exact"; fi
    add_check idempotent_zero_env_mutation; PHASE="env_verified"; return
  fi
  local body response id; body=$(jq -cn --arg key "$ENV_KEY" --arg value "$DESIRED_VALUE" '{key:$key,value:$value,type:"plain",target:["production"]}')
  if [[ "$DECISION" == create ]]; then ENV_MUTATION_COUNT=$((ENV_MUTATION_COUNT + 1)); if ! response=$(vercel_mutate POST "/v10/projects/$VERCEL_PROJECT_ID/env?teamId=$VERCEL_TEAM_ID" "$body"); then rollback_and_fail MUTATION_FAILED "production env create request failed"; fi
  else id=$(jq -r '.[0].id' <<< "$PRIOR_ENV_JSON"); ENV_MUTATION_COUNT=$((ENV_MUTATION_COUNT + 1)); if ! response=$(vercel_mutate PATCH "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" "$body"); then rollback_and_fail MUTATION_FAILED "production env update request failed"; fi; fi
  add_check env_mutation_attempted
  if ! read_env_state; then rollback_and_fail POSTCHECK_READ_FAILED "authoritative provider env read-back failed"; fi
  set_provider_observation
  if [[ "$PROVIDER_SINGLETON" != true || "$PROVIDER_VALUE_EQUAL" != true || "$PROVIDER_TARGET_EQUAL" != true || "$PROVIDER_BRANCH_EQUAL" != true || "$PROVIDER_TYPE" != plain ]]; then rollback_and_fail POSTCHECK_MISMATCH "authoritative provider env read-back was not the exact desired singleton"; fi
  add_check authoritative_singleton_readback; PHASE="env_verified"
}
capture_deployment_observation() {
  local response="$1"
  DEPLOYMENT_READY_STATE=$(jq -r '.readyState // .ready_state // ""' <<< "$response")
  DEPLOYMENT_TARGET=$(jq -r '.target // ""' <<< "$response")
  DEPLOYMENT_SOURCE=$(jq -r '.gitSource.type // (if .meta.githubDeployment=="1" then "github" else "" end)' <<< "$response")
  DEPLOYMENT_REF=$(jq -r '.gitSource.ref // .meta.githubCommitRef // ""' <<< "$response")
  DEPLOYMENT_SHA=$(jq -r '.gitSource.sha // .meta.githubCommitSha // ""' <<< "$response")
  DEPLOYMENT_REPOSITORY=$(jq -r 'if (.gitSource.org and .gitSource.repo) then (.gitSource.org+"/"+.gitSource.repo) elif (.meta.githubOrg and .meta.githubRepo) then (.meta.githubOrg+"/"+.meta.githubRepo) else "" end' <<< "$response")
}
deployment_is_exact() {
  jq -e --arg id "$DEPLOYMENT_ID" --arg project "$VERCEL_PROJECT_ID" --arg sha "$COMMIT_SHA" --arg repository "$EXPECTED_REPOSITORY" \
    'type=="object" and ((.id==$id) or (.uid==$id)) and .projectId==$project and (.target=="production") and
      ((.gitSource.type // (if .meta.githubDeployment=="1" then "github" else null end))=="github") and
      ((.gitSource.ref // .meta.githubCommitRef)=="main" or (.gitSource.ref // .meta.githubCommitRef)=="refs/heads/main") and
      ((.gitSource.sha // .meta.githubCommitSha)==$sha) and
      (((.gitSource.org // .meta.githubOrg)+"/"+(.gitSource.repo // .meta.githubRepo))==$repository)' <<< "$1" >/dev/null
}
deployment_ready_routing_is_exact() {
  jq -e --arg custom "${ALIASES[0]}" --arg vercel "${ALIASES[1]}" \
    'def valid_host:
       if type == "string" then test("^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$") else false end;
     def clean_aliases:
       if type != "array" then false
       elif any(.[]; (valid_host|not)) then false
       else map(ascii_downcase) as $hosts |
         ($hosts|length) == ($hosts|unique|length) and
         all($hosts[]; . != ($custom|ascii_downcase) and . != ($vercel|ascii_downcase))
       end;
     type=="object" and
     (if has("alias") then .alias|clean_aliases else true end) and
     (if has("userAliases") then .userAliases|clean_aliases else true end) and
     (if has("automaticAliases") then .automaticAliases|clean_aliases else true end) and
     (if has("aliasAssigned") then .aliasAssigned|type=="boolean" else true end) and
     (if has("aliasError") then .aliasError==null else true end) and
     (if has("aliasWarning") then .aliasWarning==null else true end) and
     (if has("aliasFinal") then
       if .aliasFinal==null then true
       elif (.aliasFinal|valid_host) then
         (.aliasFinal|ascii_downcase) as $final |
         $final != ($custom|ascii_downcase) and $final != ($vercel|ascii_downcase)
       else false end
      else true end)' <<< "$1" >/dev/null
}
create_and_verify_deployment() {
  local body response parsed attempt curl_exit
  body=$(jq -cn --arg name "$EXPECTED_PROJECT_NAME" --arg project "$VERCEL_PROJECT_ID" --arg sha "$COMMIT_SHA" \
    '{name:$name,project:$project,target:"production",gitSource:{type:"github",org:"Rayer",repo:"llm-wiki-cloud",ref:"main",sha:$sha}}')
  DEPLOYMENT_CREATE_COUNT=1; PHASE="deployment_create_attempted"
  response=""; curl_exit=0; response=$(vercel_mutate POST "/v13/deployments?forceNew=1&teamId=$VERCEL_TEAM_ID" "$body") || curl_exit=$?
  ((curl_exit == 0)) || partial_fail DEPLOYMENT_CREATE_UNCERTAIN "deployment create response was uncertain; no retry attempted"
  if ! parsed=$(jq -ce '
    def host: "[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+";
    def normalized_url: if test("^" + host + "$") then "https://" + . elif test("^https://" + host + "$") then . else empty end;
    if (type=="object" and ((.id // .uid // "")|test("^dpl_[A-Za-z0-9]+$")) and (.url|type)=="string") then
      (.url|normalized_url) as $url | if $url then {id:(.id // .uid),url:$url} else empty end
    else empty end' <<< "$response"); then
    partial_fail DEPLOYMENT_RESPONSE_INVALID "deployment create returned empty, malformed, or invalid JSON"
  fi
  DEPLOYMENT_ID=$(jq -r '.id' <<< "$parsed"); DEPLOYMENT_URL=$(jq -r '.url' <<< "$parsed")
  for ((attempt=1; attempt<=DEPLOYMENT_POLL_ATTEMPTS; attempt++)); do
    PHASE="deployment_readback"
    response=""; curl_exit=0; response=$(vercel_get "/v13/deployments/$DEPLOYMENT_ID?teamId=$VERCEL_TEAM_ID&withGitRepoInfo=true") || curl_exit=$?
    ((curl_exit == 0)) || partial_fail DEPLOYMENT_READ_FAILED "deployment read-back failed after create"
    jq -e --arg id "$DEPLOYMENT_ID" 'type=="object" and ((.id // .uid)==$id) and ((.readyState // .ready_state)|type)=="string" and ((.readyState // .ready_state)|test("^(READY|BUILDING|QUEUED|INITIALIZING|ANALYZING|DEPLOYING|ERROR|CANCELED)$"))' <<< "$response" >/dev/null || partial_fail DEPLOYMENT_READ_INVALID "deployment read-back returned empty, malformed, or invalid JSON"
    capture_deployment_observation "$response"
    if [[ "$DEPLOYMENT_READY_STATE" == READY ]]; then
      deployment_ready_routing_is_exact "$response" || partial_fail DEPLOYMENT_ALIAS_ROUTING "READY deployment routing inventory was missing, malformed, or indicated canonical alias routing"
      deployment_is_exact "$response" || partial_fail DEPLOYMENT_SOURCE_MISMATCH "READY deployment provenance did not match the exact production Git source"
      read_freeze_state || partial_fail POST_CREATE_FREEZE_READ_FAILED "canonical alias or deployment re-read failed after deployment creation"
      freeze_state_matches_contract || partial_fail POST_CREATE_FREEZE_DRIFT "canonical alias or deployment identity drifted after deployment creation"
      add_deployment_check ready; add_deployment_check target_production; add_deployment_check project_exact; add_deployment_check team_authority_from_scoped_request; add_deployment_check source_github_main_sha_repo_exact
      add_deployment_check canonical_aliases_and_deployments_unchanged
      PHASE="deployment_verified"; STATUS=SUCCESS; REASON_CODE=NONE; REASON="exact production auth singleton verified and exact Git-source deployment READY"; printf 'DEPLOYED\n'; return
    fi
    [[ "$DEPLOYMENT_READY_STATE" != ERROR && "$DEPLOYMENT_READY_STATE" != CANCELED ]] || partial_fail DEPLOYMENT_FAILED "deployment reached $DEPLOYMENT_READY_STATE; it remains unaliased"
    if (( attempt < DEPLOYMENT_POLL_ATTEMPTS )); then sleep "$DEPLOYMENT_POLL_INTERVAL_SECONDS"; fi
  done
  partial_fail DEPLOYMENT_TIMEOUT "deployment did not reach READY within the bounded read-back window"
}

if [[ "$MODE" == validate ]]; then
  validate_exact_source; STATUS=SUCCESS; REASON_CODE=NONE; REASON="exact main SHA and canonical CI verified"; PHASE="validated"; printf 'VALIDATED\n'
elif [[ "$MODE" == preflight ]]; then
  validate_exact_source; require_project; read_env_state || preflight_fail ENV_READ_FAILED "Vercel environment read failed or was malformed"; read_freeze_state || preflight_fail FREEZE_READ_FAILED "canonical alias or deployment freeze read failed"; set_provider_observation; capture_rollback
  STATUS=PREFLIGHT_READY; REASON_CODE=NONE; REASON="filtered production auth state and deployment freeze captured; no provider mutation performed"; PHASE="preflight"; printf 'PREFLIGHT_READY\n'
else
  validate_exact_source; load_rollback_contract
  if read_project; then :; else project_status=$?; [[ "$project_status" == 2 ]] && preflight_fail AUTO_ALIAS_CONTRACT_UNAVAILABLE "Vercel cannot prove no production alias/domain routing for this deployment"; preflight_fail PROJECT_METADATA_MISMATCH "Vercel project metadata changed"; fi
  read_env_state || preflight_fail ENV_READ_FAILED "Vercel environment read failed or was malformed"
  read_freeze_state || preflight_fail FREEZE_READ_FAILED "canonical alias or deployment freeze read failed before mutation"
  freeze_state_matches_contract || preflight_fail FREEZE_DRIFT "canonical alias or deployment identity changed after rollback snapshot"
  exact_state_matches_previous || preflight_fail SNAPSHOT_DRIFT "provider auth state changed after rollback snapshot"; apply_and_verify_env; create_and_verify_deployment
fi
