#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
# shellcheck source=deploy/components/common.sh
source "$ROOT/deploy/components/common.sh"

FRONTEND_RESPONSE_PATH=""
FRONTEND_MUTATION_FAILURE=""
FRONTEND_READBACK='{}'
FRONTEND_READBACK_RESULT=unknown
VERCEL_READBACK_RESULT=unknown
FRONTEND_ROLLBACK_READBACK='{}'
FRONTEND_DEPLOYMENT_JSON=''
FRONTEND_DEPLOYMENT_URL=''

cleanup_frontend_response() {
  if [[ -n "$FRONTEND_RESPONSE_PATH" ]]; then rm -f "$FRONTEND_RESPONSE_PATH"; FRONTEND_RESPONSE_PATH=""; fi
}
trap cleanup_frontend_response EXIT

frontend_expected_aliases() {
  plan_json '.frontend.stable_aliases[]'
}

validate_frontend_alias_config() {
  jq -e '.normalized.frontend.stable_aliases | type == "array" and length > 0 and all(.[]; type == "string" and length > 0)' "$PLAN_PATH" >/dev/null || die "frontend aliases are not a valid normalized allowlist"
}

vercel_api_get() {
  local endpoint="$1"
  [[ "${VERCEL_PROJECT_ID:-}" =~ ^prj_[A-Za-z0-9]+$ && "${VERCEL_TEAM_ID:-}" =~ ^team_[A-Za-z0-9]+$ ]] || return 1
  curl --fail --silent --show-error --max-time 30 --connect-timeout 10 -H 'Accept: application/json' -H "Authorization: Bearer ${VERCEL_TOKEN:?}" "${VERCEL_API_BASE_URL:-https://api.vercel.com}${endpoint}"
}
vercel_get_alias() { local encoded; encoded=$(jq -rn --arg alias "$1" '$alias|@uri'); vercel_api_get "/v4/aliases/${encoded}?teamId=${VERCEL_TEAM_ID:?}"; }
vercel_get_project() { local encoded; encoded=$(jq -rn --arg project "$1" '$project|@uri'); vercel_api_get "/v9/projects/${encoded}?teamId=${VERCEL_TEAM_ID:?}"; }
vercel_get_deployment() { local encoded; encoded=$(jq -rn --arg deployment "$1" '$deployment|@uri'); vercel_api_get "/v13/deployments/${encoded}?teamId=${VERCEL_TEAM_ID:?}"; }
vercel_get_deployment_inventory() {
  local cursor='' pages=0 endpoint response page next inventory='[]' seen=''
  while (( pages < 10 )); do
    endpoint="/v6/deployments?projectId=${VERCEL_PROJECT_ID:?}&teamId=${VERCEL_TEAM_ID:?}&limit=100"
    [[ -z "$cursor" ]] || endpoint+="&until=$(jq -rn --arg cursor "$cursor" '$cursor|@uri')"
    response=$(vercel_api_get "$endpoint") || return 1
    strict_json <<<"$response" || return 1
    page=$(jq -ce 'if type != "object" or (.deployments|type) != "array" then error("deployment inventory is not an array") else .deployments end' <<<"$response") || return 1
    inventory=$(jq -cn --argjson current "$inventory" --argjson page "$page" '$current + $page')
    if [[ "$(jq -er 'length' <<<"$page")" == 100 ]]; then jq -e '.pagination|type == "object" and has("next")' <<<"$response" >/dev/null || return 1; fi
    next=$(jq -r 'if (.pagination|type) != "object" or .pagination.next == null then "" elif (.pagination.next|type) == "string" and length > 0 then .pagination.next elif (.pagination.next|type) == "number" and isfinite and floor == . and . >= 0 then (.pagination.next|tostring) else "__invalid__" end' <<<"$response")
    [[ "$next" != __invalid__ ]] || return 1
    if [[ -z "$next" ]]; then jq -cn --argjson deployments "$inventory" '{deployments:$deployments}'; return 0; fi
    [[ "$next" != "$cursor" && ":$seen:" != *":$next:"* ]] || return 1
    seen="${seen:+$seen:}$next"; cursor="$next"; pages=$((pages + 1))
  done
  return 1
}

vercel_get_alias_inventory() {
  local cursor='' pages=0 endpoint response page next inventory='[]' seen=''
  while (( pages < 10 )); do
    endpoint="/v4/aliases?projectId=${VERCEL_PROJECT_ID:?}&teamId=${VERCEL_TEAM_ID:?}&limit=100"
    [[ -z "$cursor" ]] || endpoint+="&until=$(jq -rn --arg cursor "$cursor" '$cursor|@uri')"
    response=$(vercel_api_get "$endpoint") || return 1
    page=$(jq -ce 'if type == "array" then . elif (.aliases|type) == "array" then .aliases else error("alias inventory is not an array") end' <<<"$response") || return 1
    jq -e 'all(.[]; type == "object" and (.alias|type) == "string" and (.projectId|type) == "string" and ((.deploymentId // .deployment_id)|type) == "string" and ((.deploymentId // .deployment_id)|startswith("dpl_")))' <<<"$page" >/dev/null || return 1
    page=$(jq -c 'map({alias,project_id:.projectId,team_id:(.teamId // .accountId // .ownerId // null),deployment_id:(.deploymentId // .deployment_id)})' <<<"$page")
    inventory=$(jq -cn --argjson current "$inventory" --argjson page "$page" '$current + $page')
    if [[ "$(jq -er 'length' <<<"$page")" == 100 ]]; then jq -e '.pagination|type == "object" and has("next")' <<<"$response" >/dev/null || return 1; fi
    next=$(jq -r 'if (has("pagination")|not) or .pagination.next == null then "" elif (.pagination.next|type) == "number" and isfinite and floor == . and . >= 0 then (.pagination.next|tostring) else "__invalid__" end' <<<"$response")
    [[ "$next" != __invalid__ ]] || return 1
    if [[ -z "$next" ]]; then jq -cn --argjson aliases "$inventory" '{aliases:$aliases}'; return 0; fi
    [[ "$next" != "$cursor" && ":$seen:" != *":$next:"* ]] || return 1
    seen="${seen:+$seen:}$next"; cursor="$next"; pages=$((pages + 1))
  done
  return 1
}

vercel_alias_inventory_contains() {
  local inventory="$1" alias="$2" deployment="$3"
  jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg deployment "$deployment" '.aliases | map(select(.alias == $alias and .project_id == $project and (.team_id == null or .team_id == $team) and .deployment_id == $deployment)) | length == 1' <<<"$inventory" >/dev/null
}
vercel_read_alias_authority() {
  local alias="$1" response deployment inventory
  response=$(vercel_get_alias "$alias") || return 1
  deployment=$(jq -er '.deploymentId // .deployment_id | select(type == "string" and startswith("dpl_"))' <<<"$response") || return 1
  jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" 'type == "object" and .alias == $alias and .projectId == $project and ((.teamId // .accountId // .ownerId // $team) == $team)' <<<"$response" >/dev/null || return 1
  inventory=$(vercel_get_alias_inventory) || return 1
  vercel_alias_inventory_contains "$inventory" "$alias" "$deployment" || return 1
  printf '%s\n' "$deployment"
}
vercel_verify_alias_target() {
  local alias="$1" deployment="$2" response inventory
  VERCEL_READBACK_RESULT=unknown
  response=$(vercel_get_alias "$alias") || return 1
  if ! jq -e --arg alias "$alias" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg deployment "$deployment" 'type == "object" and .alias == $alias and .projectId == $project and ((.teamId // .accountId // .ownerId // $team) == $team) and ((.deploymentId // .deployment_id) == $deployment)' <<<"$response" >/dev/null; then VERCEL_READBACK_RESULT=failed; return 1; fi
  inventory=$(vercel_get_alias_inventory) || return 1
  vercel_alias_inventory_contains "$inventory" "$alias" "$deployment" || { VERCEL_READBACK_RESULT=failed; return 1; }
  VERCEL_READBACK_RESULT=success
}
vercel_project_authority() {
  local project_json
  project_json=$(vercel_get_project "${VERCEL_PROJECT_ID:?}") || return 1
  jq -e --arg project_id "$VERCEL_PROJECT_ID" --arg team_id "$VERCEL_TEAM_ID" --arg project_name "$(plan_json '.frontend.project_name')" --arg repository "$(plan_json '.frontend.repository')" --arg root "$(plan_json '.frontend.root_directory')" 'type == "object" and .id == $project_id and (.name // "") == $project_name and ((.team.id // .team_id // .accountId // $team_id) == $team_id) and ((.link.type // "github") == "github") and (((.link.repo // "") == $repository) or (((.link.org // "") + "/" + (.link.repo // "")) == $repository)) and ((.rootDirectory // "") == $root)' <<<"$project_json" >/dev/null
}
frontend_vercel_environment() { [[ "$ENVIRONMENT" == production ]] && printf 'production\n' || printf 'preview\n'; }
frontend_deploy_target() { [[ "$ENVIRONMENT" == production ]] && printf 'production\n' || printf 'preview\n'; }
frontend_deployment_path() { printf '%s/frontend-deployment.json\n' "$ARTIFACT_DIR"; }

vercel_canonical_deployment_url() {
  local value="$1" host
  case "$value" in
    https://*) host="${value#https://}" ;;
    http://*) return 1 ;;
    *) host="$value" ;;
  esac
  [[ "$host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*\.vercel\.app$ ]] || return 1
  printf 'https://%s\n' "$host"
}

vercel_validate_deployment() {
  local response="$1" deployment_id="$2" deployment_url="$3" response_url canonical_url
  response_url=$(jq -er '.url // .deployment_url | select(type == "string" and length > 0)' <<<"$response") || return 1
  canonical_url=$(vercel_canonical_deployment_url "$response_url") || return 1
  [[ "$canonical_url" == "$deployment_url" ]] || return 1
  jq -e --arg id "$deployment_id" --arg url "$deployment_url" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg repository "$(plan_json '.frontend.repository')" --arg source_sha "$SOURCE_SHA" --arg source_ref "$SOURCE_REF" --arg target "$(frontend_deploy_target)" '
    def nonempty_string($value): ($value|type) == "string" and ($value|length) > 0;
    def ref_ok($value): nonempty_string($value) and ($value == $source_ref or $value == ("refs/heads/" + $source_ref));
    def sha_ok($value): nonempty_string($value) and $value == $source_sha;
    def repo_ok($org; $repo): nonempty_string($org) and nonempty_string($repo) and ($org + "/" + $repo) == $repository;
    (.teamId // .accountId // .ownerId) as $actual_team |
    .meta as $meta | .gitSource as $git |
    type == "object" and .id == $id and .projectId == $project and nonempty_string($actual_team) and $actual_team == $team and
    .readyState == "READY" and .target == $target and nonempty_string(.url) and
    nonempty_string(.url) and
    ((($git.sha == null) or sha_ok($git.sha)) and (($meta.githubCommitSha == null) or sha_ok($meta.githubCommitSha)) and (sha_ok($git.sha) or sha_ok($meta.githubCommitSha))) and
    ((($git.ref == null) or ref_ok($git.ref)) and (($meta.githubCommitRef == null) or ref_ok($meta.githubCommitRef)) and (ref_ok($git.ref) or ref_ok($meta.githubCommitRef))) and
    ((($meta.githubOrg == null and $meta.githubRepo == null) or repo_ok($meta.githubOrg; $meta.githubRepo)) and (($git.org == null and $git.repo == null) or repo_ok($git.org; $git.repo)) and (repo_ok($meta.githubOrg; $meta.githubRepo) or repo_ok($git.org; $git.repo)))
  ' <<<"$response" >/dev/null
}
vercel_select_deployment_after_uncertain() {
  local inventory identities
  inventory=$(vercel_get_deployment_inventory) || return 1
  identities=$(jq -ce --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg source_sha "$SOURCE_SHA" --arg source_ref "$SOURCE_REF" --arg repository "$(plan_json '.frontend.repository')" '
    def nonempty_string($value): ($value|type) == "string" and ($value|length) > 0;
    def ref_ok($value): nonempty_string($value) and ($value == $source_ref or $value == ("refs/heads/" + $source_ref));
    def sha_ok($value): nonempty_string($value) and $value == $source_sha;
    def repo_ok($org; $repo): nonempty_string($org) and nonempty_string($repo) and ($org + "/" + $repo) == $repository;
    [.deployments[] | select(type == "object") | select(
      (.id|nonempty_string(.)) and (.id|startswith("dpl_")) and .projectId == $project and
      ((.teamId // .accountId // .ownerId)|nonempty_string(.)) and ((.teamId // .accountId // .ownerId) == $team) and
      ((.gitSource.sha == null) or sha_ok(.gitSource.sha)) and ((.meta.githubCommitSha == null) or sha_ok(.meta.githubCommitSha)) and (sha_ok(.gitSource.sha) or sha_ok(.meta.githubCommitSha)) and
      ((.gitSource.ref == null) or ref_ok(.gitSource.ref)) and ((.meta.githubCommitRef == null) or ref_ok(.meta.githubCommitRef)) and (ref_ok(.gitSource.ref) or ref_ok(.meta.githubCommitRef)) and
      ((.meta.githubOrg == null and .meta.githubRepo == null) or repo_ok(.meta.githubOrg; .meta.githubRepo)) and ((.gitSource.org == null and .gitSource.repo == null) or repo_ok(.gitSource.org; .gitSource.repo)) and (repo_ok(.meta.githubOrg; .meta.githubRepo) or repo_ok(.gitSource.org; .gitSource.repo))
    )]
  ' <<<"$inventory") || return 1
  [[ "$(jq -er 'length' <<<"$identities")" == 1 ]] || return 1
  jq -er --arg target "$(frontend_deploy_target)" '.[0] | select(.target == $target) | .id' <<<"$identities"
}
vercel_poll_deployment() {
  local deployment_id="$1" attempt response url
  for attempt in 1 2 3; do
    response=$(vercel_get_deployment "$deployment_id") || return 1
    url=$(jq -er '.url | select(type == "string" and length > 0)' <<<"$response") || url=''
    if [[ -n "$url" ]] && url=$(vercel_canonical_deployment_url "$url") && vercel_validate_deployment "$response" "$deployment_id" "$url"; then
      FRONTEND_DEPLOYMENT_JSON="$response"; FRONTEND_DEPLOYMENT_URL="$url"; return 0
    fi
    (( attempt < 3 )) && sleep 1
  done
  return 1
}
vercel_validate_alias_mutation_response() {
  local response="$1" alias="$2"
  jq -e --arg alias "$alias" 'type == "object" and .alias == $alias and (.uid|type == "string" and test("^[A-Za-z0-9_-]{1,128}$")) and (.created|type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?Z$")) and (if has("oldDeploymentId") then (.oldDeploymentId|type == "string" and test("^dpl_[A-Za-z0-9]+$")) else true end)' <<<"$response" >/dev/null
}
vercel_alias_apply() {
  local deployment_id="$1" alias="$2" encoded body status='' curl_status=0 response_valid=0
  FRONTEND_MUTATION_FAILURE=""
  [[ "$deployment_id" =~ ^dpl_[A-Za-z0-9]+$ ]] || { FRONTEND_MUTATION_FAILURE="frontend alias target deployment ID is invalid"; return 1; }
  mkdir -p "$ARTIFACT_DIR"; FRONTEND_RESPONSE_PATH=$(mktemp "$ARTIFACT_DIR/vercel-alias-response.XXXXXX")
  encoded=$(jq -rn --arg id "$deployment_id" '$id|@uri'); body=$(jq -cn --arg alias "$alias" '{alias:$alias}')
  if status=$(curl --silent --show-error --max-time 120 --connect-timeout 10 -X POST -H 'Accept: application/json' -H 'Content-Type: application/json' -H "Authorization: Bearer ${VERCEL_TOKEN:?}" --data "$body" --output "$FRONTEND_RESPONSE_PATH" --write-out '%{http_code}' "${VERCEL_API_BASE_URL:-https://api.vercel.com}/v2/deployments/${encoded}/aliases?teamId=${VERCEL_TEAM_ID:?}" 2>/dev/null); then :; else curl_status=$?; fi
  if [[ "$curl_status" -eq 0 && "$status" =~ ^2[0-9]{2}$ ]] && vercel_validate_alias_mutation_response "$(cat "$FRONTEND_RESPONSE_PATH")" "$alias"; then response_valid=1; fi
  # A timeout or non-2xx response may have been accepted. One exact read-back decides acceptance; never blindly retry.
  if vercel_verify_alias_target "$alias" "$deployment_id"; then cleanup_frontend_response; return 0; fi
  [[ "$status" =~ ^[0-9]{3}$ ]] || status=transport
  [[ "$response_valid" -eq 1 || "$curl_status" -ne 0 || ! "$status" =~ ^2[0-9]{2}$ ]] || status=malformed_response
  FRONTEND_MUTATION_FAILURE="frontend alias mutation failed for $alias (status=$status)"; cleanup_frontend_response; return 1
}

vercel_verify_frozen_frontend() {
  [[ -s "$ROLLBACK_PATH" ]] || return 1
  local alias deployment
  while IFS=$'\t' read -r alias deployment; do vercel_verify_alias_target "$alias" "$deployment" || return 1; done < <(jq -r '.handles.frontend.aliases[] | [.alias,.deployment_id] | @tsv' "$ROLLBACK_PATH")
}
frontend_preflight() { validate_frontend_alias_config; vercel_project_authority; }
frontend_freeze() {
  local aliases='[]' alias deployment
  validate_frontend_alias_config; vercel_project_authority || die "Vercel project authority does not match the reviewed target"
  while IFS= read -r alias; do deployment=$(vercel_read_alias_authority "$alias") || die "Vercel alias authority does not match the frozen project target"; aliases=$(jq --arg alias "$alias" --arg deployment "$deployment" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" '. + [{alias:$alias,project_id:$project,team_id:$team,deployment_id:$deployment}]' <<<"$aliases"); done < <(frontend_expected_aliases)
  freeze_store frontend "$(jq -n --arg project_id "$VERCEL_PROJECT_ID" --arg team_id "$VERCEL_TEAM_ID" --argjson aliases "$aliases" '{project_id:$project_id,team_id:$team_id,aliases:$aliases}')"
}

frontend_mutate() {
  local api_url auth_url project team vercel_environment target deployment_json deployment_id deployment_url alias
  journal_init
  set_mutation_status frontend unknown
  project=$(plan_json '.frontend.project_name'); team=$(plan_json '.frontend.team_slug'); api_url=$(plan_json '.frontend.api_url'); auth_url=$(plan_json '.frontend.auth_url')
  export VERCEL_ORG_ID="$VERCEL_TEAM_ID"
  if ! validate_frontend_alias_config || ! vercel_project_authority; then
    journal_rejected frontend
    write_component_result frontend failed '{}' frontend_preflight_rejected
    return 1
  fi
  revalidate_before_provider
  vercel_environment=$(frontend_vercel_environment); target=$(frontend_deploy_target)
  (cd "$ROOT/apps/frontend" && npm ci --ignore-scripts >/dev/null && NEXT_PUBLIC_API_URL="$api_url" NEXT_PUBLIC_AUTH_URL="$auth_url" timeout --signal=TERM --kill-after=5s 120s vercel pull "$project" --yes --environment="$vercel_environment" --scope "$team" --token "${VERCEL_TOKEN:?}" >/dev/null && NEXT_PUBLIC_API_URL="$api_url" NEXT_PUBLIC_AUTH_URL="$auth_url" timeout --signal=TERM --kill-after=5s 300s vercel build --scope "$team" --token "${VERCEL_TOKEN:?}" $([[ "$target" == production ]] && printf '%s' '--prod') >/dev/null)
  vercel_project_authority || { journal_rejected frontend; die "Vercel project authority changed before frontend deployment"; }
  vercel_verify_frozen_frontend || { journal_rejected frontend; die "frontend alias authority changed from the frozen rollback snapshot"; }
  revalidate_before_provider
  journal_pending frontend
  if ! deployment_json=$(cd "$ROOT/apps/frontend" && timeout --signal=TERM --kill-after=5s 300s vercel deploy --prebuilt --yes --json --scope "$team" --token "${VERCEL_TOKEN:?}" --meta "githubCommitSha=$SOURCE_SHA" --meta "githubCommitRef=$SOURCE_REF" --meta "githubOrg=Rayer" --meta "githubRepo=llm-wiki-cloud" $([[ "$target" == production ]] && printf '%s' '--prod')); then
    if ! deployment_id=$(vercel_select_deployment_after_uncertain); then
      journal_transition frontend unknown
      set_mutation_status frontend unknown; die "frontend deployment result is unknown after the provider command"
    fi
    if ! vercel_poll_deployment "$deployment_id"; then
      journal_transition frontend unknown
      set_mutation_status frontend unknown; die "frontend deployment read-back is still unknown after the provider command"
    fi
    deployment_json="$FRONTEND_DEPLOYMENT_JSON"
    deployment_url="$FRONTEND_DEPLOYMENT_URL"
  else
    deployment_id=$(jq -er '.id // .deploymentId | select(type == "string" and startswith("dpl_"))' <<<"$deployment_json") || { journal_transition frontend unknown; die "frontend deployment ID is not immutable"; }
    deployment_url=$(jq -er '.url // .deployment_url | select(type == "string")' <<<"$deployment_json") || { journal_transition frontend unknown; die "frontend deployment URL is missing"; }
    deployment_url=$(vercel_canonical_deployment_url "$deployment_url") || { journal_transition frontend unknown; die "frontend deployment URL is not an immutable Vercel URL"; }
    vercel_validate_deployment "$(vercel_get_deployment "$deployment_id")" "$deployment_id" "$deployment_url" || { journal_transition frontend unknown; die "frontend deployment read-back did not match exact source and authority"; }
  fi
  mutation_accepted frontend
  mkdir -p "$ARTIFACT_DIR"; jq -n --arg deployment_id "$deployment_id" --arg deployment_url "$deployment_url" --arg source_sha "$SOURCE_SHA" --arg target "$target" '{deployment_id:$deployment_id,deployment_url:$deployment_url,source_sha:$source_sha,target:$target}' > "$(frontend_deployment_path)"
  while IFS= read -r alias; do
    revalidate_before_provider
    if ! vercel_alias_apply "$deployment_id" "$alias"; then
      journal_transition frontend unknown
      die "${FRONTEND_MUTATION_FAILURE:-frontend alias mutation failed}"
    fi
  done < <(frontend_expected_aliases)
  set_mutation_status frontend success
}

frontend_reconcile() {
  local receipt deployment_id deployment_url observed aliases='[]' alias
  FRONTEND_READBACK='{}'; FRONTEND_READBACK_RESULT=unknown; receipt=$(frontend_deployment_path)
  [[ -s "$receipt" ]] || { FRONTEND_READBACK_RESULT=failed; write_component_result frontend failed '{}' deployment_receipt_missing; return 1; }
  deployment_id=$(jq -er '.deployment_id | select(type == "string" and startswith("dpl_"))' "$receipt") || { FRONTEND_READBACK_RESULT=failed; write_component_result frontend failed '{}' deployment_id_invalid; return 1; }
  deployment_url=$(jq -er '.deployment_url | select(type == "string" and startswith("https://"))' "$receipt") || { FRONTEND_READBACK_RESULT=failed; write_component_result frontend failed '{}' deployment_url_invalid; return 1; }
  observed=$(vercel_get_deployment "$deployment_id") || { write_component_result frontend unknown '{}' deployment_readback_unavailable; return 2; }
  vercel_validate_deployment "$observed" "$deployment_id" "$deployment_url" || { FRONTEND_READBACK_RESULT=failed; write_component_result frontend failed '{}' deployment_readback_mismatch; return 1; }
  while IFS= read -r alias; do vercel_verify_alias_target "$alias" "$deployment_id" || { FRONTEND_READBACK_RESULT="$VERCEL_READBACK_RESULT"; write_component_result frontend "$FRONTEND_READBACK_RESULT" '{}' alias_readback_mismatch; return 1; }; aliases=$(jq --arg alias "$alias" '. + [{alias:$alias,converged:true}]' <<<"$aliases"); done < <(frontend_expected_aliases)
  FRONTEND_READBACK=$(jq -n --arg deployment_id "$deployment_id" --arg deployment_url "$deployment_url" --argjson aliases "$aliases" '{deployment_id:$deployment_id,deployment_url:$deployment_url,aliases:$aliases}')
  FRONTEND_READBACK_RESULT=success; write_component_result frontend success "$FRONTEND_READBACK"
}

frontend_rollback() {
  local failed=0 unknown=0 alias deployment converged rows='[]'
  while IFS=$'\t' read -r alias deployment; do
    vercel_alias_apply "$deployment" "$alias" || :; converged=false
    if vercel_verify_alias_target "$alias" "$deployment"; then converged=true; elif [[ "$VERCEL_READBACK_RESULT" == unknown ]]; then unknown=1; else failed=1; fi
    rows=$(jq --arg alias "$alias" --arg deployment "$deployment" --argjson converged "$converged" '. + [{alias:$alias,deployment_id:$deployment,converged:$converged}]' <<<"$rows")
  done < <(jq -r '.handles.frontend.aliases[] | [.alias,.deployment_id] | @tsv' "$ROLLBACK_PATH")
  FRONTEND_ROLLBACK_READBACK=$(jq -n --argjson aliases "$rows" '{aliases:$aliases}')
  if (( failed )); then write_rollback_result frontend failed "$FRONTEND_ROLLBACK_READBACK"; return 1; elif (( unknown )); then write_rollback_result frontend unknown "$FRONTEND_ROLLBACK_READBACK"; return 2; else write_rollback_result frontend success "$FRONTEND_ROLLBACK_READBACK"; fi
}

case "${1:-}" in
  help) printf 'frontend component: preflight|freeze|mutate|reconcile|rollback\n' ;;
  preflight) frontend_preflight ;;
  freeze) frontend_freeze ;;
  mutate) frontend_mutate ;;
  reconcile) frontend_reconcile ;;
  rollback) frontend_rollback ;;
  *) die "usage: frontend.sh help|preflight|freeze|mutate|reconcile|rollback" ;;
esac
