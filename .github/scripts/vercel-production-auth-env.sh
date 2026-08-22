#!/usr/bin/env bash
set -Eeo pipefail

MODE="$1"
readonly EXPECTED_REPOSITORY="Rayer/llm-wiki-frontend"
readonly EXPECTED_PROJECT_NAME="llm-wiki-frontend"
readonly EXPECTED_SCOPE="rayer-tung-s-projects"
readonly ENV_KEY="NEXT_PUBLIC_AUTH_URL"
readonly DESIRED_VALUE="https://auth.rayer.idv.tw"
readonly API_BASE_URL="$VERCEL_API_BASE_URL"
readonly GITHUB_BASE_URL="$GITHUB_API_URL"
readonly EVIDENCE_PATH="$EVIDENCE_DIR/vercel-production-auth-env.json"
readonly ROLLBACK_PATH="$EVIDENCE_DIR/rollback-contract.json"

STATUS="FAILED"; REASON_CODE="UNEXPECTED_FAILURE"; REASON="unexpected failure"
MUTATION_COUNT=0; EVIDENCE_WRITTEN=0
CURRENT_HEAD_SHA=""; CURRENT_REMOTE_DEVELOP_SHA=""; CI_RUN_ID=""; CI_RUN_URL=""
APPLICABLE_JSON="[]"; PRIOR_KIND="unknown"; PRIOR_ENV_JSON="null"; DECISION="unknown"; CONTRACT_SHA256=""
PROVIDER_ENV_ID=""; PROVIDER_TARGETS="[]"; PROVIDER_BRANCH_SCOPE="null"; PROVIDER_TYPE=""
PROVIDER_VALUE_EQUAL=false; PROVIDER_TARGET_EQUAL=false; PROVIDER_BRANCH_EQUAL=false; PROVIDER_SINGLETON=false
ROLLBACK_ATTEMPTED=false; ROLLBACK_RESULT="not_attempted"; PROVIDER_CHECKS="[]"

case "$MODE" in validate|preflight|mutate) ;; *) printf 'usage: %s {validate|preflight|mutate}\n' "$0" >&2; exit 2 ;; esac

add_check() { PROVIDER_CHECKS=$(jq -c --arg check "$1" '. + [$check]' <<< "$PROVIDER_CHECKS"); }

write_evidence() {
  [[ "$EVIDENCE_WRITTEN" == 1 ]] && return
  EVIDENCE_WRITTEN=1
  mkdir -p "$EVIDENCE_DIR"
  jq -n \
    --arg ticket "$TICKET_REF" --arg repository "$GITHUB_REPOSITORY" --arg sha "$COMMIT_SHA" \
    --arg checked "$CURRENT_HEAD_SHA" --arg remote "$CURRENT_REMOTE_DEVELOP_SHA" --arg ci_id "$CI_RUN_ID" --arg ci_url "$CI_RUN_URL" \
    --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg key "$ENV_KEY" --arg desired "$DESIRED_VALUE" \
    --arg prior_kind "$PRIOR_KIND" --arg prior_id "$(jq -r 'if . == null then "" elif type == "array" then (.[0].id // "") else (.id // "") end' <<< "$PRIOR_ENV_JSON")" \
    --arg prior_digest "$CONTRACT_SHA256" --arg artifact_id "$ROLLBACK_ARTIFACT_ID" --arg artifact_url "$ROLLBACK_ARTIFACT_URL" \
    --arg artifact_digest "$ROLLBACK_ARTIFACT_DIGEST" --arg artifact_name "$ROLLBACK_ARTIFACT_NAME" \
    --arg status "$STATUS" --arg reason_code "$REASON_CODE" --arg reason "$REASON" --arg decision "$DECISION" \
    --arg env_id "$PROVIDER_ENV_ID" --arg branch "$PROVIDER_BRANCH_SCOPE" --arg type "$PROVIDER_TYPE" --arg rollback_result "$ROLLBACK_RESULT" \
    --argjson targets "$PROVIDER_TARGETS" --argjson checks "$PROVIDER_CHECKS" --argjson value_equal "$PROVIDER_VALUE_EQUAL" \
    --argjson target_equal "$PROVIDER_TARGET_EQUAL" --argjson branch_equal "$PROVIDER_BRANCH_EQUAL" --argjson singleton "$PROVIDER_SINGLETON" \
    --argjson rollback_attempted "$ROLLBACK_ATTEMPTED" --arg mutation_count "$MUTATION_COUNT" \
    'def n: if . == "" then null else . end;
     {schema_version: 1, ticket_ref: ($ticket|n), environment: "production", action: "configure_project_environment",
      source: {repository: $repository, ref: "refs/heads/develop", commit_sha: ($sha|n), checked_out_sha: ($checked|n),
        current_remote_develop_sha: ($remote|n), canonical_ci: {workflow:"ci.yml", event:"push", head_branch:"develop", head_sha:($sha|n),
          run_id:($ci_id|if test("^[0-9]+$") then tonumber else null end), run_url:($ci_url|n), conclusion:(if $ci_id=="" then null else "success" end)}},
      target: {project_name:"llm-wiki-frontend", project_id:($project|n), team_id:($team|n)},
      variable: {key:$key, desired_value:$desired, type:"plain", targets:["production"], branch_scope:null},
      prior_state: {kind:$prior_kind, env_id:($prior_id|n), artifact_contract_sha256:($prior_digest|n)},
      rollback: {artifact_name:($artifact_name|n), artifact_id:($artifact_id|if test("^[0-9]+$") then tonumber else null end),
        artifact_url:($artifact_url|n), artifact_digest:($artifact_digest|n), attempted:$rollback_attempted, result:$rollback_result,
        independent_readback:($rollback_result=="RESTORED")},
      provider_verification: {result:(if $status=="SUCCESS" then "verified" else "not_verified" end), checks:$checks,
        mutation_count:($mutation_count|tonumber), singleton:$singleton, env_id:($env_id|n), targets:$targets,
        branch_scope:(if $branch=="" or $branch=="null" then null else $branch end), type:($type|n), value_equals_desired:$value_equal,
        targets_equal_production_only:$target_equal, branch_scope_equals_unscoped:$branch_equal},
      decision:$decision, status:$status, reason_code:$reason_code, reason:$reason}' > "$EVIDENCE_PATH.tmp"
  mv "$EVIDENCE_PATH.tmp" "$EVIDENCE_PATH"
}
trap 'exit_code=$?; write_evidence; exit "$exit_code"' EXIT

fail() { STATUS="$1"; REASON_CODE="$2"; REASON="$3"; printf '%s: %s\n' "$REASON_CODE" "$REASON" >&2; exit 1; }
preflight_fail() { fail PREFLIGHT_FAILED "$1" "$2"; }
post_fail() { fail FAILED "$1" "$2"; }

require_tools() {
  for command in curl jq sha256sum git awk; do command -v "$command" >/dev/null 2>&1 || preflight_fail TOOL_MISSING "required command is unavailable"; done
}
validate_inputs() {
  [[ "$COMMIT_SHA" =~ ^[0-9a-f]{40}$ ]] || preflight_fail INPUT_SHA_INVALID "commit_sha must be exactly 40 lowercase hexadecimal characters"
  [[ "$TICKET_REF" =~ ^[A-Za-z0-9._/-]+$ ]] || preflight_fail TICKET_REF_INVALID "ticket_ref is required and contains unsupported characters"
  [[ "$GITHUB_REPOSITORY" == "$EXPECTED_REPOSITORY" ]] || preflight_fail REPOSITORY_NOT_ALLOWLISTED "repository identity is not allowlisted"
  if [[ "$GITHUB_ACTIONS" == true ]]; then
    [[ "$GITHUB_REF" == refs/heads/develop ]] || preflight_fail REF_NOT_ALLOWLISTED "workflow must run from canonical develop"
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
  CURRENT_REMOTE_DEVELOP_SHA=$(git ls-remote origin refs/heads/develop 2>/dev/null | awk 'NR == 1 {print $1}')
  [[ "$COMMIT_SHA" == "$CURRENT_HEAD_SHA" ]] || preflight_fail CHECKED_OUT_SHA_MISMATCH "checked-out SHA differs from input"
  [[ "$COMMIT_SHA" == "$CURRENT_REMOTE_DEVELOP_SHA" ]] || preflight_fail REMOTE_DEVELOP_SHA_MISMATCH "origin/develop differs from input"
  local runs ci_run
  runs=$(github_get "/repos/$GITHUB_REPOSITORY/actions/workflows/ci.yml/runs?head_sha=$COMMIT_SHA&branch=develop&event=push&per_page=100") || preflight_fail CI_READ_FAILED "canonical CI read failed"
  ci_run=$(jq -c --arg sha "$COMMIT_SHA" 'first(.workflow_runs[]? | select(.path==".github/workflows/ci.yml" and .head_branch=="develop" and .head_sha==$sha and .event=="push" and .status=="completed" and .conclusion=="success" and (.id|type)=="number" and (.html_url|type)=="string")) // empty' <<< "$runs")
  [[ -n "$ci_run" ]] || preflight_fail CI_NOT_GREEN "successful canonical CI was not found for exact SHA"
  CI_RUN_ID=$(jq -r '.id' <<< "$ci_run"); CI_RUN_URL=$(jq -r '.html_url' <<< "$ci_run")
}
read_project() {
  local response
  response=$(vercel_get "/v9/projects/$VERCEL_PROJECT_ID?teamId=$VERCEL_TEAM_ID") || return 1
  jq -e --arg id "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg name "$EXPECTED_PROJECT_NAME" 'type=="object" and .id==$id and .name==$name and ((.accountId//.teamId)==$team)' <<< "$response" >/dev/null || return 1
  add_check project_metadata_exact
}
read_env_state() {
  local response
  response=$(vercel_get "/v9/projects/$VERCEL_PROJECT_ID/env?teamId=$VERCEL_TEAM_ID") || return 1
  APPLICABLE_JSON=$(jq -ce --arg key "$ENV_KEY" '(.envs//.) as $envs | if ($envs|type)!="array" then error("env inventory is not an array") else [$envs[] | select(.key==$key and ((.target//[])|index("production"))!=null) | {id,key,value,target,gitBranch:(if (.gitBranch//null)=="" then null else (.gitBranch//null) end),type}] end' <<< "$response") || return 1
  jq -e 'all(.[]; (.id|type)=="string" and (.key|type)=="string" and (.value|type)=="string" and (.target|type)=="array" and (.gitBranch==null or (.gitBranch|type)=="string") and (.type|type)=="string")' <<< "$APPLICABLE_JSON" >/dev/null || return 1
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
  if [[ "$count" == 1 && "$(jq -r '.[0].gitBranch//empty' <<< "$APPLICABLE_JSON")" != "" ]]; then preflight_fail ENV_BRANCH_AMBIGUOUS "branch-scoped production auth entry is ambiguous"; fi
  if [[ "$count" == 0 ]]; then PRIOR_KIND=absent; PRIOR_ENV_JSON=null; DECISION=create
  else
    PRIOR_KIND=present; PRIOR_ENV_JSON=$(jq -c '.[0]' <<< "$APPLICABLE_JSON")
    if jq -e --arg desired "$DESIRED_VALUE" '.[0].value==$desired and .[0].target==["production"] and .[0].gitBranch==null and .[0].type=="plain"' <<< "$APPLICABLE_JSON" >/dev/null; then DECISION=noop; else DECISION=update; fi
  fi
  mkdir -p "$EVIDENCE_DIR"
  jq -n --arg sha "$COMMIT_SHA" --arg ticket "$TICKET_REF" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg key "$ENV_KEY" --arg decision "$DECISION" --argjson previous "$APPLICABLE_JSON" \
    '{schema_version:1,kind:"vercel-production-auth-env-rollback-contract",source:{repository:"Rayer/llm-wiki-frontend",ref:"refs/heads/develop",commit_sha:$sha,ticket_ref:$ticket},target:{project_id:$project,team_id:$team,key:$key,project_name:"llm-wiki-frontend"},decision:$decision,prior_state:(if ($previous|length)==0 then {kind:"absent"} else {kind:"present",env:$previous[0]} end)}' > "$ROLLBACK_PATH.tmp"
  mv "$ROLLBACK_PATH.tmp" "$ROLLBACK_PATH"; CONTRACT_SHA256=$(sha256sum "$ROLLBACK_PATH" | awk '{print $1}')
  add_check filtered_production_env_read; add_check rollback_snapshot_captured
}
load_rollback_contract() {
  [[ -f "$ROLLBACK_PATH" ]] || preflight_fail ROLLBACK_CONTRACT_MISSING "rollback contract is missing"
  [[ "$ROLLBACK_ARTIFACT_ID" =~ ^[0-9]+$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact ID is missing or malformed"
  [[ "$ROLLBACK_ARTIFACT_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact digest is missing or malformed"
  [[ "$ROLLBACK_ARTIFACT_NAME" == vercel-production-auth-env-rollback-$COMMIT_SHA ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact name is not exact"
  [[ "$ROLLBACK_ARTIFACT_URL" == https://github.com/$GITHUB_REPOSITORY/actions/runs/*/artifacts/$ROLLBACK_ARTIFACT_ID ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback artifact URL is not exact"
  local contract; contract=$(cat "$ROLLBACK_PATH") || preflight_fail ROLLBACK_CONTRACT_READ_FAILED "rollback contract could not be read"
  jq -e --arg sha "$COMMIT_SHA" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg key "$ENV_KEY" '.kind=="vercel-production-auth-env-rollback-contract" and .source.commit_sha==$sha and .target.project_id==$project and .target.team_id==$team and .target.key==$key and (.prior_state.kind=="absent" or (.prior_state.kind=="present" and (.prior_state.env.id|type)=="string"))' <<< "$contract" >/dev/null || preflight_fail ROLLBACK_CONTRACT_INVALID "rollback contract identity is invalid"
  CONTRACT_SHA256=$(sha256sum "$ROLLBACK_PATH" | awk '{print $1}'); PRIOR_KIND=$(jq -r '.prior_state.kind' <<< "$contract")
  if [[ "$PRIOR_KIND" == present ]]; then PRIOR_ENV_JSON=$(jq -c '[.prior_state.env]' <<< "$contract"); else PRIOR_ENV_JSON='[]'; fi
  DECISION=$(jq -r '.decision' <<< "$contract")
}
exact_state_matches_previous() { [[ "$(jq -S -c . <<< "$APPLICABLE_JSON")" == "$(jq -S -c . <<< "$PRIOR_ENV_JSON")" ]]; }
restore_previous() {
  ROLLBACK_ATTEMPTED=true
  if [[ "$PRIOR_KIND" == absent ]]; then
    local count id response; read_env_state || { ROLLBACK_RESULT=FAILED; return 1; }; count=$(jq length <<< "$APPLICABLE_JSON")
    if [[ "$count" == 1 ]]; then
      id=$(jq -r '.[0].id' <<< "$APPLICABLE_JSON")
      if ! response=$(vercel_mutate DELETE "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" '{}'); then ROLLBACK_RESULT=FAILED; return 1; fi
      MUTATION_COUNT=$((MUTATION_COUNT + 1))
    elif [[ "$count" -gt 1 ]]; then ROLLBACK_RESULT=FAILED; return 1; fi
  else
    local id body response; id=$(jq -r '.[0].id' <<< "$PRIOR_ENV_JSON")
    body=$(jq -c '.[0] | {key,value,target,type} + (if .gitBranch==null then {} else {gitBranch:.gitBranch} end)' <<< "$PRIOR_ENV_JSON")
    if ! response=$(vercel_mutate PATCH "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" "$body"); then ROLLBACK_RESULT=FAILED; return 1; fi
    MUTATION_COUNT=$((MUTATION_COUNT + 1))
  fi
  read_env_state || { ROLLBACK_RESULT=FAILED; return 1; }
  exact_state_matches_previous || { ROLLBACK_RESULT=FAILED; return 1; }
  ROLLBACK_RESULT=RESTORED; add_check rollback_readback_exact
}
rollback_and_fail() { if restore_previous; then post_fail "$1" "$2; prior provider state restored"; else post_fail ROLLBACK_FAILED "$2; rollback read-back failed"; fi; }
apply_and_verify() {
  if [[ "$DECISION" == noop ]]; then
    set_provider_observation
    if [[ "$PROVIDER_SINGLETON" != true || "$PROVIDER_VALUE_EQUAL" != true || "$PROVIDER_TARGET_EQUAL" != true || "$PROVIDER_BRANCH_EQUAL" != true || "$PROVIDER_TYPE" != plain ]]; then rollback_and_fail POSTCHECK_MISMATCH "idempotent provider state was not exact"; fi
    add_check idempotent_zero_mutation; STATUS=SUCCESS; REASON_CODE=NONE; REASON="exact desired singleton already present; zero mutation performed"; return
  fi
  local body response id; body=$(jq -cn --arg key "$ENV_KEY" --arg value "$DESIRED_VALUE" '{key:$key,value:$value,type:"plain",target:["production"]}')
  if [[ "$DECISION" == create ]]; then
    MUTATION_COUNT=$((MUTATION_COUNT + 1)); if ! response=$(vercel_mutate POST "/v10/projects/$VERCEL_PROJECT_ID/env?teamId=$VERCEL_TEAM_ID" "$body"); then rollback_and_fail MUTATION_FAILED "production env create request failed"; fi
  else
    id=$(jq -r '.[0].id' <<< "$PRIOR_ENV_JSON"); MUTATION_COUNT=$((MUTATION_COUNT + 1))
    if ! response=$(vercel_mutate PATCH "/v9/projects/$VERCEL_PROJECT_ID/env/$id?teamId=$VERCEL_TEAM_ID" "$body"); then rollback_and_fail MUTATION_FAILED "production env update request failed"; fi
  fi
  add_check mutation_attempted
  if ! read_env_state; then rollback_and_fail POSTCHECK_READ_FAILED "authoritative provider env read-back failed"; fi
  set_provider_observation
  if [[ "$PROVIDER_SINGLETON" != true || "$PROVIDER_VALUE_EQUAL" != true || "$PROVIDER_TARGET_EQUAL" != true || "$PROVIDER_BRANCH_EQUAL" != true || "$PROVIDER_TYPE" != plain ]]; then rollback_and_fail POSTCHECK_MISMATCH "authoritative provider env read-back was not the exact desired singleton"; fi
  add_check authoritative_singleton_readback; STATUS=SUCCESS; REASON_CODE=NONE; REASON="exact production auth origin singleton verified"
}
if [[ "$MODE" == validate ]]; then
  require_tools; validate_inputs; validate_exact_source; STATUS=SUCCESS; REASON_CODE=NONE; REASON="exact develop SHA and canonical CI verified"; printf 'VALIDATED\n'
elif [[ "$MODE" == preflight ]]; then
  validate_exact_source; read_project || preflight_fail PROJECT_METADATA_MISMATCH "Vercel project metadata was not exact"; read_env_state || preflight_fail ENV_READ_FAILED "Vercel environment read failed or was malformed"; set_provider_observation; capture_rollback
  STATUS=PREFLIGHT_READY; REASON_CODE=NONE; REASON="filtered production auth state captured; no provider mutation performed"; printf 'PREFLIGHT_READY\n'
else
  validate_exact_source; load_rollback_contract; read_project || preflight_fail PROJECT_METADATA_MISMATCH "Vercel project metadata changed"; read_env_state || preflight_fail ENV_READ_FAILED "Vercel environment read failed or was malformed"
  exact_state_matches_previous || preflight_fail SNAPSHOT_DRIFT "provider auth state changed after rollback snapshot"; apply_and_verify; printf 'APPLIED\n'
fi
