#!/usr/bin/env bash
set -Eeuo pipefail

MODE="${1:-}"
if [[ "$MODE" != validate && "$MODE" != preflight && "$MODE" != promote ]]; then
  printf 'usage: %s {validate|preflight|promote}\n' "$0" >&2
  exit 2
fi

# Reuse the normal lane's bounded provider transport and deployment normalization.
# The normal script is deliberately a library here; its default CLI modes remain unchanged.
VERCEL_DEV_DEPLOYMENT_LIBRARY=1 source "$(dirname "$0")/vercel-dev-deployment.sh"

readonly RECONCILIATION_MODE="authority_reconciliation"
readonly LEGACY_PROJECT_NAME="llm-wiki-frontend"
readonly RECONCILIATION_ACK_PREFIX="I acknowledge LWC-253 authority reconciliation"
readonly MAX_ALIAS_PAGES=10
readonly MAX_DEPLOYMENT_PAGES=10
readonly ALIAS_PAGE_LIMIT=100
readonly RECONCILIATION_EVIDENCE_FILENAME="vercel-authority-reconciliation.json"

EXPECTED_CURRENT_ALIAS_PROJECT_ID="${EXPECTED_CURRENT_ALIAS_PROJECT_ID:-}"
EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID="${EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID:-}"
EXPECTED_CURRENT_ALIAS_SOURCE_SHA="${EXPECTED_CURRENT_ALIAS_SOURCE_SHA:-}"
RECONCILIATION_ACK="${RECONCILIATION_ACK:-}"
RECONCILIATION_ARTIFACT_NAME="${RECONCILIATION_ARTIFACT_NAME:-}"
GITHUB_RUN_ID="${GITHUB_RUN_ID:-}"

readonly RECONCILIATION_EVIDENCE_PATH="$EVIDENCE_DIR/$RECONCILIATION_EVIDENCE_FILENAME"
readonly RECONCILIATION_CONTEXT_PATH="$EVIDENCE_DIR/reconciliation-context.json"
readonly RECONCILIATION_ROLLBACK_PATH="$EVIDENCE_DIR/reconciliation-rollback-contract.json"

STATUS="FAILED"
REASON_CODE="UNEXPECTED_FAILURE"
REASON="unexpected failure"
NEXT_ACTION="Inspect normalized evidence and reconcile provider state before retrying."
MUTATION_COUNT=0
PROVIDER_CHECKS='[]'
EVIDENCE_WRITTEN=0

CANONICAL_PROJECT_JSON='{}'
CANONICAL_DOMAINS_JSON='{}'
LEGACY_PROJECT_JSON='{}'
CURRENT_DEPLOYMENT_JSON='{}'
GLOBAL_ALIAS_JSON='{}'
LEGACY_ALIAS_INVENTORY_JSON='{"aliases":[]}'
CANONICAL_ALIAS_INVENTORY_JSON='{"aliases":[]}'
CANONICAL_DEPLOYMENT_INVENTORY_JSON='{"deployments":[]}'
CANONICAL_CANDIDATE_JSON=""
CANONICAL_CANDIDATE_URL=""
FROZEN_OLD_PROJECT_ID=""
FROZEN_OLD_DEPLOYMENT_ID=""
FROZEN_OLD_SOURCE_SHA=""
FROZEN_OLD_SOURCE_REF=""
FROZEN_OLD_REPOSITORY=""
FROZEN_OLD_READY_STATE=""
FROZEN_OLD_TARGET=""
DEPLOYMENT_ID=""
DEPLOYMENT_URL=""
DEPLOYMENT_DECISION="deployment_needed"
PROJECT_REPOSITORY_ID=""
CI_RUN_ID="${CI_RUN_ID:-}"
CI_RUN_URL="${CI_RUN_URL:-}"
OBSERVED_ALIAS_PROJECT_ID=""
OBSERVED_ALIAS_DEPLOYMENT_ID=""
OBSERVED_DEPLOYMENT_ID=""
OBSERVED_DEPLOYMENT_URL=""
OBSERVED_SOURCE=""
OBSERVED_REPOSITORY=""
OBSERVED_REF=""
OBSERVED_SHA=""
OBSERVED_READY_STATE=""
OBSERVED_TARGET=""
OBSERVED_PROJECT_ID=""
OBSERVED_TEAM_ID=""
ROLLBACK_ARTIFACT_ID="${ROLLBACK_ARTIFACT_ID:-}"
ROLLBACK_ARTIFACT_URL="${ROLLBACK_ARTIFACT_URL:-}"
ROLLBACK_ARTIFACT_DIGEST="${ROLLBACK_ARTIFACT_DIGEST:-}"
ROLLBACK_CONTRACT_SHA256=""

mkdir -p "$EVIDENCE_DIR"

write_evidence() {
  [[ "$EVIDENCE_WRITTEN" -eq 1 ]] && return
  EVIDENCE_WRITTEN=1
  jq -n \
    --arg ticket "$TICKET_REF" \
    --arg sha "$COMMIT_SHA" \
    --arg ref "refs/heads/$EXPECTED_REF" \
    --arg currentHead "$CURRENT_HEAD_SHA" \
    --arg currentRemote "$CURRENT_REMOTE_DEVELOP_SHA" \
    --arg ciId "$CI_RUN_ID" \
    --arg ciUrl "$CI_RUN_URL" \
    --arg project "$VERCEL_PROJECT_ID" \
    --arg team "$VERCEL_TEAM_ID" \
    --arg scope "$VERCEL_SCOPE" \
    --arg domain "$STABLE_DOMAIN" \
    --arg deployment "$DEPLOYMENT_ID" \
    --arg deploymentUrl "$DEPLOYMENT_URL" \
    --arg observedDeployment "$OBSERVED_DEPLOYMENT_ID" \
    --arg observedUrl "$OBSERVED_DEPLOYMENT_URL" \
    --arg observedSource "$OBSERVED_SOURCE" \
    --arg observedRepo "$OBSERVED_REPOSITORY" \
    --arg observedRef "$OBSERVED_REF" \
    --arg observedSha "$OBSERVED_SHA" \
    --arg observedReady "$OBSERVED_READY_STATE" \
    --arg observedTarget "$OBSERVED_TARGET" \
    --arg observedProject "$OBSERVED_PROJECT_ID" \
    --arg observedTeam "$OBSERVED_TEAM_ID" \
    --arg oldProject "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" \
    --arg oldDeployment "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" \
    --arg oldSha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" \
    --arg oldRef "$FROZEN_OLD_SOURCE_REF" \
    --arg oldRepo "$FROZEN_OLD_REPOSITORY" \
    --arg oldReady "$FROZEN_OLD_READY_STATE" \
    --arg oldTarget "$FROZEN_OLD_TARGET" \
    --arg artifactId "$ROLLBACK_ARTIFACT_ID" \
    --arg artifactUrl "$ROLLBACK_ARTIFACT_URL" \
    --arg artifactDigest "$ROLLBACK_ARTIFACT_DIGEST" \
    --arg artifactName "$RECONCILIATION_ARTIFACT_NAME" \
    --arg contractSha "$ROLLBACK_CONTRACT_SHA256" \
    --arg status "$STATUS" \
    --arg reasonCode "$REASON_CODE" \
    --arg reason "$REASON" \
    --arg nextAction "$NEXT_ACTION" \
    --argjson checks "$PROVIDER_CHECKS" \
    --arg count "$MUTATION_COUNT" \
    --arg observedAliasProjectId "$OBSERVED_ALIAS_PROJECT_ID" \
    --arg observedAliasDeploymentId "$OBSERVED_ALIAS_DEPLOYMENT_ID" \
    'def str_or_null: if . == "" then null else . end;
     def num_or_null: if test("^[0-9]+$") then tonumber else null end;
     {
       schema_version: 1,
       mode: "authority_reconciliation",
       ticket_ref: ($ticket | str_or_null),
       environment: "development",
       action: "reconcile_dev_authority",
       source: {repository: "Rayer/llm-wiki-frontend", commit_sha: ($sha | str_or_null), ref: $ref,
         checked_out_sha: ($currentHead | str_or_null), current_remote_develop_sha: ($currentRemote | str_or_null),
         canonical_ci: {workflow: "ci.yml", head_branch: "develop", head_sha: ($sha | str_or_null),
           conclusion: (if $ciId == "" then null else "success" end), run_id: ($ciId | num_or_null), run_url: ($ciUrl | str_or_null)}},
       target: {project_name: "llm-wiki-frontend-dev", project_id: ($project | str_or_null), team_id: ($team | str_or_null), scope: ($scope | str_or_null), stable_domain: $domain},
       deployment: {id: ($deployment | str_or_null), url: ($deploymentUrl | str_or_null), source: ($observedSource | str_or_null), repository: ($observedRepo | str_or_null), ref: ($observedRef | str_or_null), commit_sha: ($observedSha | str_or_null), ready_state: ($observedReady | str_or_null), target: ($observedTarget | str_or_null), project_id: ($observedProject | str_or_null), team_id: ($observedTeam | str_or_null)},
       rollback: {alias: $domain, project_id: ($oldProject | str_or_null), team_id: ($team | str_or_null), deployment_id: ($oldDeployment | str_or_null), source: {repository: ($oldRepo | str_or_null), ref: ($oldRef | str_or_null), commit_sha: ($oldSha | str_or_null), ready_state: ($oldReady | str_or_null), target: ($oldTarget | str_or_null)}, artifact: {name: ($artifactName | str_or_null), id: ($artifactId | num_or_null), url: ($artifactUrl | str_or_null), digest: ($artifactDigest | str_or_null), contract_sha256: ($contractSha | str_or_null)}},
       observed_alias: {alias: $domain, project_id: ($observedAliasProjectId | str_or_null), deployment_id: ($observedAliasDeploymentId | str_or_null)},
       provider_verification: {checks: $checks, mutation_count: ($count | num_or_null)},
       status: $status, reason_code: $reasonCode, reason: $reason, next_action: $nextAction
     }' \
    > "$RECONCILIATION_EVIDENCE_PATH.tmp"
  mv "$RECONCILIATION_EVIDENCE_PATH.tmp" "$RECONCILIATION_EVIDENCE_PATH"
}

trap 'exit_code=$?; write_evidence; exit "$exit_code"' EXIT

fail() {
  STATUS="$1"
  REASON_CODE="$2"
  REASON="$3"
  NEXT_ACTION="$4"
  printf '%s: %s\n' "$REASON_CODE" "$REASON" >&2
  exit 1
}

preflight_fail() { fail "PREFLIGHT_FAILED" "$1" "$2" "Correct the explicit reconciliation request or read-only provider state; zero provider mutation was attempted."; }
partial_fail() { fail "PARTIAL_MUTATION" "$1" "$2" "Reconcile the exact DEV alias and deployment state before any retry or rollback; do not blindly replay the mutation."; }

validate_inputs() {
  if [[ ! "$COMMIT_SHA" =~ ^[0-9a-f]{40}$ ]]; then
    preflight_fail INPUT_SHA_INVALID "commit_sha must be exactly 40 lowercase hexadecimal characters"
  fi
  if [[ "$GITHUB_REPOSITORY" != "$EXPECTED_REPOSITORY" ]]; then
    preflight_fail REPOSITORY_NOT_ALLOWLISTED "GITHUB_REPOSITORY must equal the exact repository identity"
  fi
  if [[ "$TICKET_REF" != LWC-253 ]]; then
    preflight_fail TICKET_REF_INVALID "ticket_ref must be exactly LWC-253 for authority reconciliation"
  fi
  if [[ ! "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" =~ ^prj_[A-Za-z0-9]+$ ]]; then
    preflight_fail INPUT_OLD_PROJECT_INVALID "expected_current_alias_project_id is not a bounded Vercel project ID"
  fi
  if [[ ! "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" =~ ^dpl_[A-Za-z0-9]+$ ]]; then
    preflight_fail INPUT_OLD_DEPLOYMENT_INVALID "expected_current_alias_deployment_id is not an immutable deployment ID"
  fi
  if [[ ! "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
    preflight_fail INPUT_OLD_SHA_INVALID "expected_current_alias_source_sha must be exactly 40 lowercase hexadecimal characters"
  fi
  if [[ "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" == "$VERCEL_PROJECT_ID" ]]; then
    preflight_fail OLD_PROJECT_EQUALS_CANONICAL "old alias owner must differ from canonical DEV project"
  fi
  local expected_ack
  expected_ack="$RECONCILIATION_ACK_PREFIX: alias=$STABLE_DOMAIN old_project=$EXPECTED_CURRENT_ALIAS_PROJECT_ID old_deployment=$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID desired_sha=$COMMIT_SHA"
  [[ "$RECONCILIATION_ACK" == "$expected_ack" ]] || preflight_fail ACKNOWLEDGEMENT_INVALID "acknowledgement must exactly bind LWC-253, the fixed alias, old owner, old deployment, and desired SHA"
  if [[ "${GITHUB_ACTIONS:-}" == true && "${LWC253_TEST_MODE:-}" == 1 ]]; then
    preflight_fail TEST_MODE_FORBIDDEN "LWC253_TEST_MODE is forbidden in GitHub Actions"
  fi
  if [[ "${GITHUB_ACTIONS:-}" == true && ( "$API_BASE_URL" != "https://api.vercel.com" || "$GITHUB_BASE_URL" != "https://api.github.com" ) ]]; then
    preflight_fail API_ORIGIN_NOT_ALLOWLISTED "GitHub Actions requires canonical provider API origins"
  fi
  if [[ "${GITHUB_ACTIONS:-}" != true && "${LWC253_TEST_MODE:-}" != 1 && ( "$API_BASE_URL" != "https://api.vercel.com" || "$GITHUB_BASE_URL" != "https://api.github.com" ) ]]; then
    preflight_fail API_ORIGIN_NOT_ALLOWLISTED "API origin overrides require LWC253_TEST_MODE=1 outside GitHub Actions"
  fi
  if [[ "$MODE" != validate ]]; then
    [[ -n "$VERCEL_TOKEN" && -n "$VERCEL_PROJECT_ID" && -n "$VERCEL_TEAM_ID" && -n "$VERCEL_SCOPE" ]] || preflight_fail CONFIG_MISSING "Development Vercel configuration is missing"
    [[ "$VERCEL_PROJECT_ID" =~ ^prj_[A-Za-z0-9]+$ && "$VERCEL_TEAM_ID" =~ ^team_[A-Za-z0-9]+$ ]] || preflight_fail CONFIG_ID_INVALID "canonical project or team is not a bounded Vercel ID"
    [[ "$VERCEL_SCOPE" == "$EXPECTED_SCOPE" ]] || preflight_fail TEAM_NOT_ALLOWLISTED "Development Vercel scope is not the allowlisted team scope"
    [[ "$POLL_ATTEMPTS" =~ ^[1-9][0-9]*$ && "$POLL_ATTEMPTS" -le 60 && "$POLL_INTERVAL" =~ ^[0-9]+$ && "$POLL_INTERVAL" -le 60 ]] || preflight_fail POLL_BOUNDS_INVALID "deployment polling bounds are invalid"
    [[ "$ALIAS_TIMEOUT" =~ ^[1-9][0-9]*$ && "$ALIAS_TIMEOUT" -le 300 ]] || preflight_fail ALIAS_TIMEOUT_INVALID "alias mutation timeout is not bounded"
    for command in curl jq sha256sum timeout vercel; do
      command -v "$command" >/dev/null 2>&1 || preflight_fail TOOL_MISSING "required command is unavailable: $command"
    done
  else
    for command in curl jq; do
      command -v "$command" >/dev/null 2>&1 || preflight_fail TOOL_MISSING "required command is unavailable: $command"
    done
  fi
}

validate_canonical_project() {
  local project="$1"
  jq -e --arg id "$VERCEL_PROJECT_ID" --arg name "$EXPECTED_PROJECT_NAME" --arg team "$VERCEL_TEAM_ID" '
    type == "object" and .id == $id and .name == $name and ((.accountId // .teamId) == $team)' <<< "$project" >/dev/null ||
    preflight_fail PROJECT_METADATA_MISMATCH "canonical project metadata did not identify the exact Development project and team"
}

validate_canonical_domains() {
  jq -e --arg domain "$STABLE_DOMAIN" 'type == "object" and (.domains | type == "array" and length == 1 and .[0].name == $domain)' <<< "$1" >/dev/null ||
    preflight_fail DOMAIN_NOT_ALLOWLISTED "canonical project must register the fixed stable DEV domain exactly once"
}

validate_legacy_project() {
  jq -e --arg id "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg name "$LEGACY_PROJECT_NAME" --arg team "$VERCEL_TEAM_ID" --arg canonical "$VERCEL_PROJECT_ID" '
    type == "object" and .id == $id and .name == $name and .id != $canonical and ((.accountId // .teamId) == $team)' <<< "$1" >/dev/null ||
    preflight_fail LEGACY_PROJECT_MISMATCH "current alias owner was not the exact allowlisted legacy project in the same team"
}

validate_global_alias() {
  jq -e --arg alias "$STABLE_DOMAIN" --arg project "$1" --arg deployment "$2" '
    type == "object" and .alias == $alias and .projectId == $project and .deploymentId == $deployment and
    (.alias | type == "string") and (.projectId | type == "string") and (.deploymentId | type == "string")' <<< "$3" >/dev/null || return 1
}

validate_legacy_deployment() {
  local response="$1"
  normalize_deployment "$response"
  jq -e --arg id "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" --arg project "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg sha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" --arg repo "$EXPECTED_REPOSITORY" '
    type == "object" and .id == $id and .projectId == $project and ((.teamId // .accountId) == $team) and .readyState == "READY" and (.target == null or .target == "preview") and
    (.gitSource.type // (if .meta.githubDeployment == "1" then "github" else null end)) == "github" and
    (if (.meta.githubOrg and .meta.githubRepo) then (.meta.githubOrg + "/" + .meta.githubRepo) else ((.gitSource.org // "") + "/" + (.gitSource.repo // "")) end) == $repo and
    ((.gitSource.ref // .meta.githubCommitRef // "") == "develop" or (.gitSource.ref // .meta.githubCommitRef // "") == "refs/heads/develop") and
    (.gitSource.sha // .meta.githubCommitSha) == $sha and (.url | type == "string" and test("^https://"))' <<< "$response" >/dev/null ||
    preflight_fail LEGACY_DEPLOYMENT_MISMATCH "current alias deployment did not match exact READY GitHub develop provenance"
  FROZEN_OLD_SOURCE_REF="$OBSERVED_REF"
  FROZEN_OLD_REPOSITORY="$OBSERVED_REPOSITORY"
  FROZEN_OLD_READY_STATE="$OBSERVED_READY_STATE"
  FROZEN_OLD_TARGET="$OBSERVED_TARGET"
}

read_alias_inventory_strict() {
  local project_id="$1" cursor="" next response page inventory='{"aliases":[]}' pages=0 seen=''
  while (( pages < MAX_ALIAS_PAGES )); do
    local query="/v4/aliases?projectId=$project_id&teamId=$VERCEL_TEAM_ID&limit=$ALIAS_PAGE_LIMIT"
    if [[ -n "$cursor" ]]; then query+="&until=$(printf '%s' "$cursor" | jq -Rr @uri)"; fi
    response="$(api_query "$query")" || return 1
    jq -e 'type == "object" and (.aliases | type == "array") and all(.aliases[]; type == "object" and (.alias | type == "string") and (.projectId | type == "string") and (.deploymentId | type == "string")) and ((has("pagination") | not) or (.pagination | type == "object" and ((has("next") | not) or .next == null or (.next | type == "string" and length > 0))))' <<< "$response" >/dev/null || return 1
    page="$(jq -c '.aliases' <<< "$response")"
    inventory="$(jq -cn --argjson current "$(jq -c '.aliases' <<< "$inventory")" --argjson page "$page" '{aliases: ($current + $page)}')"
    next="$(jq -r '.pagination.next // empty' <<< "$response")"
    pages=$((pages + 1))
    [[ -n "$next" ]] || { printf '%s' "$inventory"; return 0; }
    [[ "$next" != "$cursor" && ":$seen:" != *":$next:"* ]] || return 1
    seen="${seen:+$seen:}$next"
    cursor="$next"
  done
  return 1
}

validate_alias_inventory() {
  local inventory="$1" project="$2" expected_count="$3" expected_deployment="${4:-}"
  jq -e --arg alias "$STABLE_DOMAIN" --arg project "$project" --arg deployment "$expected_deployment" --argjson count "$expected_count" '
    ((.aliases | map(select(.alias == $alias)) | length) == $count) and
    ($count == 0 or ((.aliases | map(select(.alias == $alias and .projectId == $project and .deploymentId == $deployment)) | length) == 1))' <<< "$inventory" >/dev/null || return 1
}

read_deployment_inventory_strict() {
  local cursor="" next response page inventory='{"deployments":[]}' pages=0 seen=''
  while (( pages < MAX_DEPLOYMENT_PAGES )); do
    local query="/v6/deployments?projectId=$VERCEL_PROJECT_ID&teamId=$VERCEL_TEAM_ID&limit=$DEPLOYMENT_PAGE_LIMIT"
    if [[ -n "$cursor" ]]; then query+="&until=$(printf '%s' "$cursor" | jq -Rr @uri)"; fi
    response="$(api_query "$query")" || return 1
    jq -e 'type == "object" and (.deployments | type == "array") and all(.deployments[]; type == "object" and (.id | type == "string")) and ((has("pagination") | not) or (.pagination | type == "object" and ((has("next") | not) or .next == null or (.next | type == "string" and length > 0))))' <<< "$response" >/dev/null || return 1
    page="$(jq -c '.deployments' <<< "$response")"
    inventory="$(jq -cn --argjson current "$(jq -c '.deployments' <<< "$inventory")" --argjson page "$page" '{deployments: ($current + $page)}')"
    next="$(jq -r '.pagination.next // empty' <<< "$response")"
    pages=$((pages + 1))
    [[ -n "$next" ]] || { printf '%s' "$inventory"; return 0; }
    [[ "$next" != "$cursor" && ":$seen:" != *":$next:"* ]] || return 1
    seen="${seen:+$seen:}$next"
    cursor="$next"
  done
  return 1
}

find_canonical_candidate() {
  jq -c --arg sha "$COMMIT_SHA" --arg repo "$EXPECTED_REPOSITORY" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" '
    [.deployments[] | select(
      (.id | test("^dpl_[A-Za-z0-9]+$")) and .projectId == $project and ((.teamId // .accountId) == $team) and .readyState == "READY" and .target == "preview" and
      (.gitSource.type // (if .meta.githubDeployment == "1" then "github" else null end)) == "github" and
      ((.gitSource.ref // .meta.githubCommitRef // "") == "develop" or (.gitSource.ref // .meta.githubCommitRef // "") == "refs/heads/develop") and
      (.gitSource.sha // .meta.githubCommitSha // "") == $sha and
      (if (.meta.githubOrg and .meta.githubRepo) then (.meta.githubOrg + "/" + .meta.githubRepo) else ((.gitSource.org // "") + "/" + (.gitSource.repo // "")) end) == $repo and (.url | type == "string" and test("^https://"))
    )] | if length > 1 then error("duplicate exact canonical candidates") elif length == 1 then .[0] else null end' <<< "$1"
}

read_repo_id() {
  local repo
  repo="$(github_query "/repos/$GITHUB_REPOSITORY")" || preflight_fail REPOSITORY_READ_FAILED "GitHub repository provenance read failed"
  PROJECT_REPOSITORY_ID="$(jq -r --arg expected "$GITHUB_REPOSITORY" 'select(type == "object" and .full_name == $expected and (.id | type == "number")) | .id // empty' <<< "$repo")"
  [[ "$PROJECT_REPOSITORY_ID" =~ ^[0-9]+$ ]] || preflight_fail REPOSITORY_READ_FAILED "GitHub repository provenance did not return an immutable numeric repository ID"
}

canonical_json() { jq -S -c . <<< "$1"; }

freeze_context() {
  local candidate_json
  candidate_json="${CANONICAL_CANDIDATE_JSON:-null}"
  jq -n \
    --arg sha "$COMMIT_SHA" --arg ticket "$TICKET_REF" --arg ack "$RECONCILIATION_ACK" --arg repo "$EXPECTED_REPOSITORY" --arg ref "refs/heads/$EXPECTED_REF" --arg oldRef "$FROZEN_OLD_SOURCE_REF" --arg oldRepo "$FROZEN_OLD_REPOSITORY" --arg oldReady "$FROZEN_OLD_READY_STATE" --arg oldTarget "$FROZEN_OLD_TARGET" \
    --arg canonical "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg scope "$VERCEL_SCOPE" --arg domain "$STABLE_DOMAIN" --arg oldProject "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg oldDeployment "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" --arg oldSha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" --arg repoId "$PROJECT_REPOSITORY_ID" --arg decision "$DEPLOYMENT_DECISION" --arg deployment "$DEPLOYMENT_ID" --arg deploymentUrl "$DEPLOYMENT_URL" --arg count "$MUTATION_COUNT" --argjson candidate "$candidate_json" \
    --argjson canonicalProject "$CANONICAL_PROJECT_JSON" --argjson canonicalDomains "$CANONICAL_DOMAINS_JSON" --argjson legacyProject "$LEGACY_PROJECT_JSON" --argjson oldDeploymentResponse "$CURRENT_DEPLOYMENT_JSON" --argjson globalAlias "$GLOBAL_ALIAS_JSON" --argjson legacyAliases "$LEGACY_ALIAS_INVENTORY_JSON" --argjson canonicalAliases "$CANONICAL_ALIAS_INVENTORY_JSON" --argjson checks "$PROVIDER_CHECKS" \
    '{schema_version: 1, mode: "authority_reconciliation", phase: "preflight-complete", status: "ready_for_durable_rollback", ticket_ref: $ticket, acknowledgement: $ack, source: {repository: $repo, commit_sha: $sha, ref: $ref}, target: {project_id: $canonical, project_name: "llm-wiki-frontend-dev", team_id: $team, scope: $scope, stable_domain: $domain, decision: $decision, deployment_id: ($deployment | if . == "" then null else . end), deployment_url: ($deploymentUrl | if . == "" then null else . end), candidate: $candidate}, repository_id: ($repoId | tonumber), frozen_authority: {alias: $domain, canonical_project_id: $canonical, legacy_project_id: $oldProject, legacy_deployment_id: $oldDeployment, legacy_source_sha: $oldSha, legacy_source: {repository: $oldRepo, ref: $oldRef, ready_state: $oldReady, target: ($oldTarget | if . == "" then null else . end)}, canonical_project: $canonicalProject, canonical_domains: $canonicalDomains, legacy_project: $legacyProject, legacy_deployment: $oldDeploymentResponse, global_alias: $globalAlias, legacy_alias_inventory: $legacyAliases, canonical_alias_inventory: $canonicalAliases}, mutation_count: ($count | tonumber), provider_checks: $checks}' > "$RECONCILIATION_CONTEXT_PATH.tmp"
  mv "$RECONCILIATION_CONTEXT_PATH.tmp" "$RECONCILIATION_CONTEXT_PATH"
}

write_rollback_contract() {
  jq -n --arg sha "$COMMIT_SHA" --arg repo "$EXPECTED_REPOSITORY" --arg domain "$STABLE_DOMAIN" --arg oldProject "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg oldDeployment "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" --arg team "$VERCEL_TEAM_ID" --arg oldSha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" --arg oldRef "$FROZEN_OLD_SOURCE_REF" --arg oldRepo "$FROZEN_OLD_REPOSITORY" --arg oldReady "$FROZEN_OLD_READY_STATE" --arg oldTarget "$FROZEN_OLD_TARGET" --arg decision "$DEPLOYMENT_DECISION" --arg target "$DEPLOYMENT_ID" --arg targetUrl "$DEPLOYMENT_URL" \
    '{schema_version: 1, kind: "vercel-authority-reconciliation-rollback-contract", mode: "authority_reconciliation", source: {repository: $repo, commit_sha: $sha, ref: "refs/heads/develop"}, target: {decision: $decision, deployment_id: ($target | if . == "" then null else . end), url: ($targetUrl | if . == "" then null else . end)}, rollback: {alias: $domain, project_id: $oldProject, team_id: $team, deployment_id: $oldDeployment, source: {repository: $oldRepo, ref: $oldRef, commit_sha: $oldSha, ready_state: $oldReady, target: ($oldTarget | if . == "" then null else . end)}}}' > "$RECONCILIATION_ROLLBACK_PATH.tmp"
  mv "$RECONCILIATION_ROLLBACK_PATH.tmp" "$RECONCILIATION_ROLLBACK_PATH"
  ROLLBACK_CONTRACT_SHA256="$(sha256sum "$RECONCILIATION_ROLLBACK_PATH" | awk '{print $1}')"
}

read_authority_state() {
  CANONICAL_PROJECT_JSON="$(api_query "/v9/projects/$VERCEL_PROJECT_ID?teamId=$VERCEL_TEAM_ID")" || return 1
  validate_canonical_project "$CANONICAL_PROJECT_JSON"
  CANONICAL_DOMAINS_JSON="$(api_query "/v9/projects/$VERCEL_PROJECT_ID/domains?teamId=$VERCEL_TEAM_ID")" || return 1
  validate_canonical_domains "$CANONICAL_DOMAINS_JSON"
  LEGACY_PROJECT_JSON="$(api_query "/v9/projects/$EXPECTED_CURRENT_ALIAS_PROJECT_ID?teamId=$VERCEL_TEAM_ID")" || return 1
  validate_legacy_project "$LEGACY_PROJECT_JSON"
  CURRENT_DEPLOYMENT_JSON="$(api_query "/v13/deployments/$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID?teamId=$VERCEL_TEAM_ID")" || return 1
  validate_legacy_deployment "$CURRENT_DEPLOYMENT_JSON"
  local first_alias
  first_alias="$(read_alias)" || return 1
  validate_global_alias "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" "$first_alias" || return 1
  LEGACY_ALIAS_INVENTORY_JSON="$(read_alias_inventory_strict "$EXPECTED_CURRENT_ALIAS_PROJECT_ID")" || return 1
  CANONICAL_ALIAS_INVENTORY_JSON="$(read_alias_inventory_strict "$VERCEL_PROJECT_ID")" || return 1
  local second_alias
  second_alias="$(read_alias)" || return 1
  validate_global_alias "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" "$second_alias" || return 1
  [[ "$(canonical_json "$first_alias")" == "$(canonical_json "$second_alias")" ]] || return 1
  validate_alias_inventory "$LEGACY_ALIAS_INVENTORY_JSON" "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" 1 "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" || return 1
  validate_alias_inventory "$CANONICAL_ALIAS_INVENTORY_JSON" "$VERCEL_PROJECT_ID" 0 || return 1
  GLOBAL_ALIAS_JSON="$second_alias"
  OBSERVED_ALIAS_PROJECT_ID="$EXPECTED_CURRENT_ALIAS_PROJECT_ID"
  OBSERVED_ALIAS_DEPLOYMENT_ID="$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID"
  return 0
}

assert_frozen_authority() {
  local context="$1"
  local key current frozen
  for key in canonical_project canonical_domains legacy_project legacy_deployment global_alias legacy_alias_inventory canonical_alias_inventory; do
    case "$key" in
      canonical_project) current="$(canonical_json "$CANONICAL_PROJECT_JSON" 2>/dev/null || true)" ;;
      canonical_domains) current="$(canonical_json "$CANONICAL_DOMAINS_JSON" 2>/dev/null || true)" ;;
      legacy_project) current="$(canonical_json "$LEGACY_PROJECT_JSON" 2>/dev/null || true)" ;;
      legacy_deployment) current="$(canonical_json "$CURRENT_DEPLOYMENT_JSON" 2>/dev/null || true)" ;;
      global_alias) current="$(canonical_json "$GLOBAL_ALIAS_JSON" 2>/dev/null || true)" ;;
      legacy_alias_inventory) current="$(canonical_json "$LEGACY_ALIAS_INVENTORY_JSON" 2>/dev/null || true)" ;;
      canonical_alias_inventory) current="$(canonical_json "$CANONICAL_ALIAS_INVENTORY_JSON" 2>/dev/null || true)" ;;
    esac
    frozen="$(jq -S -c ".frozen_authority.$key" <<< "$context" 2>/dev/null || true)"
    [[ -n "$current" && "$current" == "$frozen" ]] || return 1
  done
}

load_context() {
  [[ -f "$RECONCILIATION_CONTEXT_PATH" && -f "$RECONCILIATION_ROLLBACK_PATH" ]] || preflight_fail ROLLBACK_ARTIFACT_MISSING "reconciliation context and rollback contract are missing"
  jq -e --arg sha "$COMMIT_SHA" --arg ticket "$TICKET_REF" --arg ack "$RECONCILIATION_ACK" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg scope "$VERCEL_SCOPE" --arg domain "$STABLE_DOMAIN" --arg oldProject "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg oldDeployment "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" --arg oldSha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" --arg expectedAckPrefix "$RECONCILIATION_ACK_PREFIX" '
    .schema_version == 1 and .mode == "authority_reconciliation" and .phase == "preflight-complete" and .status == "ready_for_durable_rollback" and .ticket_ref == $ticket and .acknowledgement == $ack and (.acknowledgement | startswith($expectedAckPrefix)) and .source.commit_sha == $sha and .target.project_id == $project and .target.team_id == $team and .target.scope == $scope and .target.stable_domain == $domain and .frozen_authority.legacy_project_id == $oldProject and .frozen_authority.legacy_deployment_id == $oldDeployment and .frozen_authority.legacy_source_sha == $oldSha and .mutation_count == 0 and (.target.decision == "existing" or .target.decision == "deployment_needed") and (.target.deployment_id == null or (.target.deployment_id | type == "string" and test("^dpl_[A-Za-z0-9]+$")))' "$RECONCILIATION_CONTEXT_PATH" >/dev/null || preflight_fail ROLLBACK_CONTEXT_INVALID "reconciliation context did not match the explicit validated request"
  jq -e --arg sha "$COMMIT_SHA" --arg project "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" --arg deployment "$EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID" --arg team "$VERCEL_TEAM_ID" --arg domain "$STABLE_DOMAIN" --arg oldSha "$EXPECTED_CURRENT_ALIAS_SOURCE_SHA" '
    .schema_version == 1 and .kind == "vercel-authority-reconciliation-rollback-contract" and .mode == "authority_reconciliation" and .source.commit_sha == $sha and .rollback.alias == $domain and .rollback.project_id == $project and .rollback.team_id == $team and .rollback.deployment_id == $deployment and .rollback.source.commit_sha == $oldSha' "$RECONCILIATION_ROLLBACK_PATH" >/dev/null || preflight_fail ROLLBACK_ARTIFACT_INVALID "rollback contract did not match the frozen old authority"
  DEPLOYMENT_DECISION="$(jq -r '.target.decision' "$RECONCILIATION_CONTEXT_PATH")"
  DEPLOYMENT_ID="$(jq -r '.target.deployment_id // empty' "$RECONCILIATION_CONTEXT_PATH")"
  DEPLOYMENT_URL="$(jq -r '.target.deployment_url // empty' "$RECONCILIATION_CONTEXT_PATH")"
  PROJECT_REPOSITORY_ID="$(jq -r '.repository_id' "$RECONCILIATION_CONTEXT_PATH")"
  PROVIDER_CHECKS="$(jq -c '.provider_checks' "$RECONCILIATION_CONTEXT_PATH")"
  FROZEN_OLD_SOURCE_REF="$(jq -r '.frozen_authority.legacy_source.ref' "$RECONCILIATION_CONTEXT_PATH")"
  FROZEN_OLD_REPOSITORY="$(jq -r '.frozen_authority.legacy_source.repository' "$RECONCILIATION_CONTEXT_PATH")"
  FROZEN_OLD_READY_STATE="$(jq -r '.frozen_authority.legacy_source.ready_state' "$RECONCILIATION_CONTEXT_PATH")"
  FROZEN_OLD_TARGET="$(jq -r '.frozen_authority.legacy_source.target // empty' "$RECONCILIATION_CONTEXT_PATH")"
}

validate_artifact_handoff() {
  [[ "$ROLLBACK_ARTIFACT_ID" =~ ^[1-9][0-9]*$ && "$GITHUB_RUN_ID" =~ ^[1-9][0-9]*$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable reconciliation artifact ID or current run ID is missing"
  [[ "$ROLLBACK_ARTIFACT_URL" == "https://github.com/$EXPECTED_REPOSITORY/actions/runs/$GITHUB_RUN_ID/artifacts/$ROLLBACK_ARTIFACT_ID" ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable reconciliation artifact URL is not bound to this repository, run, and artifact ID"
  [[ "$ROLLBACK_ARTIFACT_DIGEST" =~ ^[0-9a-f]{64}$ ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable reconciliation artifact digest must be bare lowercase 64-hex"
  [[ "$RECONCILIATION_ARTIFACT_NAME" == "vercel-authority-reconciliation-rollback-$COMMIT_SHA" ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable reconciliation artifact name is not bound to the requested SHA"
  [[ "$(jq -r '.schema_version' "$RECONCILIATION_ROLLBACK_PATH" 2>/dev/null || true)" == 1 ]] || preflight_fail ROLLBACK_ARTIFACT_INVALID "durable rollback contract is not readable"
}

resolve_canonical_deployment() {
  CANONICAL_DEPLOYMENT_INVENTORY_JSON="$(read_deployment_inventory_strict)" || preflight_fail DEPLOYMENT_LIST_FAILED "canonical DEV deployment inventory read failed"
  CANONICAL_CANDIDATE_JSON="$(find_canonical_candidate "$CANONICAL_DEPLOYMENT_INVENTORY_JSON")" || preflight_fail DEPLOYMENT_CANDIDATE_AMBIGUOUS "canonical DEV deployment inventory contained duplicate exact candidates"
  if [[ "$CANONICAL_CANDIDATE_JSON" != null ]]; then
    DEPLOYMENT_DECISION="existing"
    DEPLOYMENT_ID="$(jq -r '.id' <<< "$CANONICAL_CANDIDATE_JSON")"
    DEPLOYMENT_URL="$(jq -r '.url' <<< "$CANONICAL_CANDIDATE_JSON")"
    normalize_deployment "$CANONICAL_CANDIDATE_JSON"
    PROVIDER_CHECKS="$(jq -c '. + ["canonical_candidate_exact"]' <<< "$PROVIDER_CHECKS")"
  else
    DEPLOYMENT_DECISION="deployment_needed"
    DEPLOYMENT_ID=""
    DEPLOYMENT_URL=""
    OBSERVED_DEPLOYMENT_ID=""
    OBSERVED_DEPLOYMENT_URL=""
    OBSERVED_SOURCE=""
    OBSERVED_REPOSITORY=""
    OBSERVED_REF=""
    OBSERVED_SHA=""
    OBSERVED_READY_STATE=""
    OBSERVED_TARGET=""
    OBSERVED_PROJECT_ID=""
    OBSERVED_TEAM_ID=""
    PROVIDER_CHECKS="$(jq -c '. + ["canonical_candidate_create_needed"]' <<< "$PROVIDER_CHECKS")"
  fi
}

deployment_response_matches() {
  jq -e --arg id "$DEPLOYMENT_ID" --arg project "$VERCEL_PROJECT_ID" --arg team "$VERCEL_TEAM_ID" --arg sha "$COMMIT_SHA" --arg repo "$EXPECTED_REPOSITORY" --arg url "$DEPLOYMENT_URL" '
    type == "object" and .id == $id and .projectId == $project and ((.teamId // .accountId) == $team) and .readyState == "READY" and .target == "preview" and (.gitSource.type // (if .meta.githubDeployment == "1" then "github" else null end)) == "github" and ((.gitSource.ref // .meta.githubCommitRef // "") == "develop" or (.gitSource.ref // .meta.githubCommitRef // "") == "refs/heads/develop") and (.gitSource.sha // .meta.githubCommitSha) == $sha and (if (.meta.githubOrg and .meta.githubRepo) then (.meta.githubOrg + "/" + .meta.githubRepo) else ((.gitSource.org // "") + "/" + (.gitSource.repo // "")) end) == $repo and .url == $url' <<< "$1" >/dev/null
}

poll_canonical_deployment() {
  local attempts=0 response state
  [[ "$DEPLOYMENT_ID" =~ ^dpl_[A-Za-z0-9]+$ ]] || partial_fail DEPLOYMENT_CREATE_UNCERTAIN "canonical deployment ID was not immutable after create"
  while (( attempts < POLL_ATTEMPTS )); do
    response="$(api_query "/v13/deployments/$DEPLOYMENT_ID?teamId=$VERCEL_TEAM_ID" 2>/dev/null)" || partial_fail DEPLOYMENT_INSPECT_FAILED "canonical deployment inspection failed after create"
    normalize_deployment "$response"
    if deployment_response_matches "$response"; then
      PROVIDER_CHECKS="$(jq -c '. + ["canonical_deployment_exact_ready"]' <<< "$PROVIDER_CHECKS")"
      return 0
    fi
    state="$(jq -r '.readyState // empty' <<< "$response" 2>/dev/null || true)"
    [[ "$state" != ERROR && "$state" != FAILED && "$state" != CANCELED ]] || partial_fail DEPLOYMENT_NOT_READY "canonical deployment reached a terminal non-READY state"
    [[ "$state" != READY ]] || partial_fail DEPLOYMENT_SOURCE_MISMATCH "canonical deployment was READY but its source provenance did not match"
    [[ "$state" == BUILDING || "$state" == QUEUED || "$state" == INITIALIZING ]] || partial_fail DEPLOYMENT_SOURCE_MISMATCH "canonical deployment returned unknown or mismatched state after creation"
    attempts=$((attempts + 1))
    sleep "$POLL_INTERVAL"
  done
  partial_fail DEPLOYMENT_POLL_TIMEOUT "canonical deployment did not converge to exact READY provenance"
}

create_canonical_deployment() {
  local payload created
  payload="$(jq -cn --arg project "$VERCEL_PROJECT_ID" --arg repoId "$PROJECT_REPOSITORY_ID" --arg sha "$COMMIT_SHA" '{name: "llm-wiki-frontend-dev", project: $project, target: "preview", gitSource: {type: "github", repoId: ($repoId | tonumber), ref: "develop", sha: $sha}}')"
  MUTATION_COUNT=$((MUTATION_COUNT + 1))
  PROVIDER_CHECKS="$(jq -c '. + ["deployment_create_attempted"]' <<< "$PROVIDER_CHECKS")"
  created="$(api_post "/v13/deployments?teamId=$VERCEL_TEAM_ID" "$payload" 2>/dev/null)" || partial_fail DEPLOYMENT_CREATE_UNCERTAIN "canonical deployment-create POST failed or became uncertain"
  DEPLOYMENT_ID="$(jq -r '.id // empty' <<< "$created" 2>/dev/null || true)"
  DEPLOYMENT_URL="$(jq -r '.url // empty' <<< "$created" 2>/dev/null || true)"
  [[ "$DEPLOYMENT_ID" =~ ^dpl_[A-Za-z0-9]+$ ]] || partial_fail DEPLOYMENT_CREATE_UNCERTAIN "canonical deployment-create response did not return an immutable ID"
  PROVIDER_CHECKS="$(jq -c '. + ["deployment_created"]' <<< "$PROVIDER_CHECKS")"
  poll_canonical_deployment
}

cli_inspect() {
  local output
  output="$(env -u GITHUB_TOKEN VERCEL_TOKEN="$VERCEL_TOKEN" vercel inspect "$DEPLOYMENT_ID" --scope "$VERCEL_SCOPE" --format=json 2>/dev/null)" || return 1
  jq -c 'if (.deployment | type) == "object" then .deployment else . end' <<< "$output"
}

alias_set_once() {
  MUTATION_COUNT=$((MUTATION_COUNT + 1))
  PROVIDER_CHECKS="$(jq -c '. + ["alias_mutation_attempted"]' <<< "$PROVIDER_CHECKS")"
  timeout --signal=TERM --kill-after=5s "${ALIAS_TIMEOUT}s" env -u GITHUB_TOKEN VERCEL_TOKEN="$VERCEL_TOKEN" vercel alias set "$DEPLOYMENT_ID" "$STABLE_DOMAIN" --scope "$VERCEL_SCOPE" >/dev/null 2>"$EVIDENCE_DIR/alias-error.log" || return 1
  PROVIDER_CHECKS="$(jq -c '. + ["alias_mutation_succeeded"]' <<< "$PROVIDER_CHECKS")"
}

postcheck() {
  local alias_response canonical_inventory legacy_inventory deployment_response cli_response
  alias_response="$(read_alias 2>/dev/null)" || partial_fail POSTCHECK_MISMATCH "post-mutation global alias read failed"
  canonical_inventory="$(read_alias_inventory_strict "$VERCEL_PROJECT_ID" 2>/dev/null)" || partial_fail POSTCHECK_MISMATCH "post-mutation canonical alias inventory read failed"
  legacy_inventory="$(read_alias_inventory_strict "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" 2>/dev/null)" || partial_fail POSTCHECK_MISMATCH "post-mutation legacy alias inventory read failed"
  validate_global_alias "$VERCEL_PROJECT_ID" "$DEPLOYMENT_ID" "$alias_response" || partial_fail POSTCHECK_MISMATCH "post-mutation global alias did not identify canonical deployment"
  validate_alias_inventory "$canonical_inventory" "$VERCEL_PROJECT_ID" 1 "$DEPLOYMENT_ID" || partial_fail POSTCHECK_MISMATCH "post-mutation canonical stable alias inventory did not converge"
  validate_alias_inventory "$legacy_inventory" "$EXPECTED_CURRENT_ALIAS_PROJECT_ID" 0 || partial_fail POSTCHECK_MISMATCH "post-mutation legacy inventory retained the stable alias"
  deployment_response="$(api_query "/v13/deployments/$DEPLOYMENT_ID?teamId=$VERCEL_TEAM_ID" 2>/dev/null)" || partial_fail POSTCHECK_MISMATCH "post-mutation canonical deployment read failed"
  normalize_deployment "$deployment_response"
  deployment_response_matches "$deployment_response" || partial_fail POSTCHECK_MISMATCH "post-mutation canonical deployment provenance did not converge"
  cli_response="$(cli_inspect)" || partial_fail POSTCHECK_MISMATCH "Vercel CLI inspect failed after alias mutation"
  deployment_response_matches "$cli_response" || partial_fail POSTCHECK_MISMATCH "Vercel CLI inspect disagreed with canonical deployment provenance"
  OBSERVED_ALIAS_PROJECT_ID="$VERCEL_PROJECT_ID"
  OBSERVED_ALIAS_DEPLOYMENT_ID="$DEPLOYMENT_ID"
  PROVIDER_CHECKS="$(jq -c '. + ["post_global_alias_exact","post_canonical_alias_inventory_exact","post_legacy_alias_inventory_empty","post_api_deployment_exact","post_cli_inspect_exact"]' <<< "$PROVIDER_CHECKS")"
}

run_preflight() {
  validate_exact_sha
  read_authority_state || preflight_fail AUTHORITY_PREFLIGHT_MISMATCH "read-only authority state did not satisfy the exact old/canonical contract"
  read_repo_id
  resolve_canonical_deployment
  write_rollback_contract
  freeze_context
  STATUS="PREFLIGHT_READY"
  REASON_CODE="PREFLIGHT_READY"
  REASON="exact inputs, canonical CI, legacy authority, canonical project/domain, complete inventories, and canonical candidate state were validated read-only"
  NEXT_ACTION="Upload reconciliation-rollback-contract.json before the first mutation-capable step."
  PROVIDER_CHECKS="$(jq -c '. + ["reconciliation_preflight_read_only","rollback_ready"]' <<< "$PROVIDER_CHECKS")"
  printf '%s\n' "$STATUS"
}

run_promote() {
  validate_inputs
  validate_artifact_handoff
  load_context
  read_authority_state || preflight_fail AUTHORITY_RECHECK_FAILED "live authority could not be re-read before mutation"
  local context
  context="$(cat "$RECONCILIATION_CONTEXT_PATH")"
  assert_frozen_authority "$context" || preflight_fail AUTHORITY_DRIFT "live authority differed from the frozen preflight state"
  local inventory candidate
  inventory="$(read_deployment_inventory_strict)" || preflight_fail DEPLOYMENT_LIST_FAILED "canonical deployment inventory read failed before mutation"
  candidate="$(find_canonical_candidate "$inventory")" || preflight_fail DEPLOYMENT_CANDIDATE_AMBIGUOUS "canonical deployment inventory contained duplicate exact candidates"
  if [[ "$candidate" != null ]]; then
    DEPLOYMENT_ID="$(jq -r '.id' <<< "$candidate")"
    DEPLOYMENT_URL="$(jq -r '.url' <<< "$candidate")"
    DEPLOYMENT_DECISION="existing"
    PROVIDER_CHECKS="$(jq -c '. + ["canonical_candidate_reused"]' <<< "$PROVIDER_CHECKS")"
    local response
    response="$(api_query "/v13/deployments/$DEPLOYMENT_ID?teamId=$VERCEL_TEAM_ID" 2>/dev/null)" || preflight_fail DEPLOYMENT_INSPECT_FAILED "canonical existing candidate inspection failed"
    normalize_deployment "$response"
    deployment_response_matches "$response" || preflight_fail DEPLOYMENT_SOURCE_MISMATCH "canonical existing candidate provenance did not match"
  else
    create_canonical_deployment
  fi
  # Candidate creation is allowed to change only the deployment inventory. Re-freeze the
  # authority-bearing project, deployment, global alias, and both alias inventories first.
  read_authority_state || partial_fail AUTHORITY_DRIFT "authority read failed after candidate resolution"
  assert_frozen_authority "$context" || partial_fail AUTHORITY_DRIFT "authority changed after candidate resolution and before alias mutation"
  if ! alias_set_once; then
    partial_fail MUTATION_UNCERTAIN "stable DEV alias mutation failed or became uncertain"
  fi
  postcheck
  STATUS="SUCCESS"
  REASON_CODE="SUCCESS"
  REASON="canonical DEV deployment and fixed stable alias converged with exact post-readback"
  NEXT_ACTION="No retry is required."
  printf '%s\n' "$STATUS"
}

if [[ "$MODE" == validate ]]; then
  validate_exact_sha
  STATUS="VALIDATED"
  REASON_CODE="VALIDATED"
  REASON="requested SHA matched checked-out HEAD, origin/develop, and exact canonical CI success"
  NEXT_ACTION="Proceed to read-only authority reconciliation preflight."
  printf '%s\n' "$STATUS"
elif [[ "$MODE" == preflight ]]; then
  run_preflight
else
  run_promote
fi
