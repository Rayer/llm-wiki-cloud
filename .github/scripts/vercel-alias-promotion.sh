#!/usr/bin/env bash
set -Eeuo pipefail

MODE="${1:-}"
if [[ "$MODE" != "preflight" && "$MODE" != "promote" ]]; then
  printf 'usage: %s {preflight|promote}\n' "$0" >&2
  exit 2
fi

readonly ALIASES=("wiki.rayer.idv.tw" "llm-wiki-frontend.vercel.app")
readonly EXPECTED_REPOSITORY="Rayer/llm-wiki-frontend"
readonly API_BASE_URL="${VERCEL_API_BASE_URL:-https://api.vercel.com}"
readonly GITHUB_BASE_URL="${GITHUB_API_URL:-https://api.github.com}"
readonly EVIDENCE_DIR="${EVIDENCE_DIR:-artifacts/vercel-alias-promotion}"
readonly EVIDENCE_PATH="$EVIDENCE_DIR/vercel-alias-promotion.json"
readonly PROJECT="llm-wiki-cloud"
readonly COMPONENT="frontend"
readonly ENVIRONMENT="production"
readonly ACTION="promote"
readonly CURL_CONNECT_TIMEOUT_SECONDS=10
readonly CURL_MAX_TIME_SECONDS=30
readonly ALIAS_SET_TIMEOUT_SECONDS="${VERCEL_ALIAS_TIMEOUT_SECONDS:-15}"

COMMIT_SHA="${COMMIT_SHA:-}"
DEPLOYMENT_ID="${DEPLOYMENT_ID:-}"
TICKET_REF="${TICKET_REF:-}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-}"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
VERCEL_TOKEN="${VERCEL_TOKEN:-}"
VERCEL_PROJECT_ID="${VERCEL_PROJECT_ID:-}"
VERCEL_TEAM_ID="${VERCEL_TEAM_ID:-}"
VERCEL_SCOPE="${VERCEL_SCOPE:-}"
PROMOTION_CONTEXT_PATH="${PROMOTION_CONTEXT_PATH:-$EVIDENCE_DIR/vercel-alias-promotion-context.json}"
ROLLBACK_CONTRACT_PATH="$EVIDENCE_DIR/rollback-contract.json"
ROLLBACK_ARTIFACT_NAME="vercel-alias-rollback-${COMMIT_SHA}"
EVIDENCE_ARTIFACT_NAME="vercel-alias-promotion-evidence-${COMMIT_SHA}"

STATUS="FAILED"
REASON="unexpected failure"
NEXT_ACTION="Inspect the evidence and provider read-back before retrying."
FROZEN_ALIASES='[{"alias":"wiki.rayer.idv.tw","deploymentId":null},{"alias":"llm-wiki-frontend.vercel.app","deploymentId":null}]'
POST_ALIASES='[{"alias":"wiki.rayer.idv.tw","deploymentId":null},{"alias":"llm-wiki-frontend.vercel.app","deploymentId":null}]'
HEALTH='[{"alias":"wiki.rayer.idv.tw","status_code":null,"effective_host":null},{"alias":"llm-wiki-frontend.vercel.app","status_code":null,"effective_host":null}]'
PROVIDER_CHECKS="[]"
CI_RUN_ID=""
CI_RUN_URL=""
CURRENT_DEPLOYMENT_URL=""
OBSERVED_DEPLOYMENT_ID=""
OBSERVED_DEPLOYMENT_URL=""
OBSERVED_SOURCE=""
OBSERVED_REF=""
OBSERVED_READY_STATE=""
OBSERVED_TARGET=""
CHECKED_AT=""
ROLLBACK_CONTRACT_SHA256=""
EVIDENCE_WRITTEN=0

write_evidence() {
  if [[ "$EVIDENCE_WRITTEN" -eq 1 ]]; then
    return
  fi
  EVIDENCE_WRITTEN=1
  mkdir -p "$EVIDENCE_DIR"
  local temporary_path="$EVIDENCE_PATH.tmp"
  jq -n \
    --arg ticketRef "$TICKET_REF" \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg currentDeploymentUrl "$CURRENT_DEPLOYMENT_URL" \
    --arg ciRunId "$CI_RUN_ID" \
    --arg ciRunUrl "$CI_RUN_URL" \
    --arg originatingRepository "$GITHUB_REPOSITORY" \
    --arg originatingWorkflow "${ORIGINATING_WORKFLOW:-}" \
    --arg originatingRunId "${ORIGINATING_WORKFLOW_RUN_ID:-}" \
    --arg originatingRunAttempt "${ORIGINATING_WORKFLOW_RUN_ATTEMPT:-}" \
    --arg observedDeploymentId "$OBSERVED_DEPLOYMENT_ID" \
    --arg observedDeploymentUrl "$OBSERVED_DEPLOYMENT_URL" \
    --arg observedSource "$OBSERVED_SOURCE" \
    --arg observedRef "$OBSERVED_REF" \
    --arg observedReadyState "$OBSERVED_READY_STATE" \
    --arg observedTarget "$OBSERVED_TARGET" \
    --arg checkedAt "$CHECKED_AT" \
    --arg rollbackArtifactName "$ROLLBACK_ARTIFACT_NAME" \
    --arg rollbackContractSha256 "$ROLLBACK_CONTRACT_SHA256" \
    --arg evidenceArtifactName "$EVIDENCE_ARTIFACT_NAME" \
    --arg status "$STATUS" \
    --arg reason "$REASON" \
    --arg nextAction "$NEXT_ACTION" \
    --argjson rollbackAliases "$(jq -c 'map({alias, deployment_id: .deploymentId})' <<< "$FROZEN_ALIASES")" \
    --argjson aliasRouting "$(jq -c 'map({alias, deployment_id: .deploymentId})' <<< "$POST_ALIASES")" \
    --argjson health "$HEALTH" \
    --argjson providerChecks "$PROVIDER_CHECKS" \
    'def numeric_or_null:
       if test("^[0-9]+$") then tonumber else null end;
     def string_or_null:
       if . == "" then null else . end;
     {
       schema_version: 1,
       project: "llm-wiki-cloud",
       component: "frontend",
       environment: "production",
       action: "promote",
       ticket_ref: $ticketRef,
       source: {
         commit_sha: $commitSha,
         ref: "refs/heads/main"
       },
       dev_provenance: {
         workflow: "ci.yml",
         event: "push",
         head_branch: "main",
         head_sha: $commitSha,
         conclusion: (if $ciRunId == "" then null else "success" end),
         run_id: ($ciRunId | numeric_or_null),
         run_url: ($ciRunUrl | string_or_null)
       },
       provider: {
         current: {
           deployment_id: ($deploymentId | string_or_null),
           deployment_url: ($currentDeploymentUrl | string_or_null)
         },
         rollback: {
           artifact_name: $rollbackArtifactName,
           artifact_sha256: ($rollbackContractSha256 | string_or_null),
           aliases: $rollbackAliases
         },
         rollback_artifact_name: $rollbackArtifactName,
         rollback_artifact_sha256: ($rollbackContractSha256 | string_or_null),
         evidence_artifact_name: $evidenceArtifactName
       },
       observed: {
         deployment_id: ($observedDeploymentId | string_or_null),
         deployment_url: ($observedDeploymentUrl | string_or_null),
         source: ($observedSource | string_or_null),
         ref: ($observedRef | string_or_null),
         ready_state: ($observedReadyState | string_or_null),
         target: ($observedTarget | string_or_null),
         alias_routing: $aliasRouting
       },
       health: $health,
       provider_verification: {
         result: (if $checkedAt == "" then "not_verified" else "verified" end),
         checks: $providerChecks,
         checked_at: ($checkedAt | string_or_null)
       },
       originating_workflow: {
         repository: ($originatingRepository | string_or_null),
         workflow: ($originatingWorkflow | string_or_null),
         run_id: ($originatingRunId | numeric_or_null),
         run_attempt: ($originatingRunAttempt | numeric_or_null)
       },
       status: $status,
       reason: $reason,
       next_action: $nextAction
     }' > "$temporary_path"
  mv "$temporary_path" "$EVIDENCE_PATH"
}

trap 'exit_code=$?; write_evidence; exit "$exit_code"' EXIT

fail_preflight() {
  STATUS="PREFLIGHT_FAILED"
  REASON="$1"
  NEXT_ACTION="Correct the preflight input or read-only state; no alias mutation was attempted."
  printf '%s: %s\n' "$STATUS" "$REASON" >&2
  exit 1
}

fail_postcheck() {
  STATUS="POSTCHECK_FAILED"
  REASON="$1"
  NEXT_ACTION="Inspect authoritative /v4/aliases, deployment read-back, and health evidence before retrying."
  printf '%s: %s\n' "$STATUS" "$REASON" >&2
  exit 1
}

fail_resume() {
  STATUS="PREFLIGHT_FAILED"
  REASON="$1"
  NEXT_ACTION="Regenerate the validated preflight context; no alias mutation was attempted."
  printf '%s: %s\n' "$STATUS" "$REASON" >&2
  exit 1
}

write_rollback_contract() {
  mkdir -p "$EVIDENCE_DIR"
  local temporary_path="$ROLLBACK_CONTRACT_PATH.tmp"
  jq -n \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg deploymentUrl "$CURRENT_DEPLOYMENT_URL" \
    --arg ciRunId "$CI_RUN_ID" \
    --arg ciRunUrl "$CI_RUN_URL" \
    --arg repository "$EXPECTED_REPOSITORY" \
    --arg rollbackArtifactName "$ROLLBACK_ARTIFACT_NAME" \
    --argjson aliases "$(jq -c 'map({alias, deployment_id: .deploymentId})' <<< "$FROZEN_ALIASES")" \
    ' {
       schema_version: 1,
       kind: "vercel-alias-rollback-contract",
       repository: $repository,
       commit_sha: $commitSha,
       ref: "refs/heads/main",
       deployment: {
         id: $deploymentId,
         project_id: $projectId,
         source: "github",
         repository: $repository,
         ref: "refs/heads/main",
         commit_sha: $commitSha,
         ready_state: "READY",
         target: "production",
         url: $deploymentUrl
       },
       ci: {
         workflow: "ci.yml",
         event: "push",
         run_id: ($ciRunId | tonumber),
         run_url: $ciRunUrl
       },
       rollback_artifact_name: $rollbackArtifactName,
       aliases: $aliases
     }' > "$temporary_path"
  mv -f "$temporary_path" "$ROLLBACK_CONTRACT_PATH"
  ROLLBACK_CONTRACT_SHA256="$(sha256sum "$ROLLBACK_CONTRACT_PATH" | awk '{print $1}')"
}

write_promotion_context() {
  mkdir -p "$(dirname "$PROMOTION_CONTEXT_PATH")"
  local temporary_path="$PROMOTION_CONTEXT_PATH.tmp"
  jq -n \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg deploymentUrl "$CURRENT_DEPLOYMENT_URL" \
    --arg ciRunId "$CI_RUN_ID" \
    --arg ciRunUrl "$CI_RUN_URL" \
    --arg repository "$EXPECTED_REPOSITORY" \
    --arg rollbackArtifactName "$ROLLBACK_ARTIFACT_NAME" \
    --arg rollbackContractPath "$ROLLBACK_CONTRACT_PATH" \
    --arg rollbackContractSha256 "$ROLLBACK_CONTRACT_SHA256" \
    --argjson aliases "$(jq -c 'map({alias, deployment_id: .deploymentId})' <<< "$FROZEN_ALIASES")" \
    ' {
       schema_version: 1,
       kind: "vercel-alias-promotion-resume",
       phase: "preflight-complete",
       repository: $repository,
       commit_sha: $commitSha,
       ref: "refs/heads/main",
       deployment_id: $deploymentId,
       project_id: $projectId,
       source: "github",
       target: "production",
       deployment_url: $deploymentUrl,
       ci: { workflow: "ci.yml", event: "push", run_id: ($ciRunId | tonumber), run_url: $ciRunUrl },
       rollback_artifact_name: $rollbackArtifactName,
       rollback_contract_path: $rollbackContractPath,
       rollback_contract_sha256: $rollbackContractSha256,
       aliases: $aliases
     }' > "$temporary_path"
  mv -f "$temporary_path" "$PROMOTION_CONTEXT_PATH"
}

load_and_validate_resume() {
  if [[ "${ROLLBACK_ARTIFACT_UPLOADED:-}" != "true" ]]; then
    fail_resume "durable rollback artifact upload was not confirmed before promote"
  fi
  if [[ ! -f "$PROMOTION_CONTEXT_PATH" || ! -f "$ROLLBACK_CONTRACT_PATH" ]]; then
    fail_resume "validated preflight context or rollback contract is missing"
  fi

  local canonical_aliases='["wiki.rayer.idv.tw","llm-wiki-frontend.vercel.app"]'
  if ! jq -e \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg repository "$EXPECTED_REPOSITORY" \
    --arg rollbackArtifactName "$ROLLBACK_ARTIFACT_NAME" \
    --arg rollbackContractPath "$ROLLBACK_CONTRACT_PATH" \
    --argjson canonicalAliases "$canonical_aliases" \
    'type == "object" and
     .schema_version == 1 and .kind == "vercel-alias-promotion-resume" and
     .phase == "preflight-complete" and .repository == $repository and
     .commit_sha == $commitSha and .ref == "refs/heads/main" and
     .deployment_id == $deploymentId and .project_id == $projectId and
     .source == "github" and .target == "production" and
     (.deployment_url | type) == "string" and
     (.ci.workflow == "ci.yml" and .ci.event == "push" and
       (.ci.run_id | type) == "number" and (.ci.run_url | type) == "string") and
     .rollback_artifact_name == $rollbackArtifactName and
     .rollback_contract_path == $rollbackContractPath and
     (.rollback_contract_sha256 | test("^[0-9a-f]{64}$")) and
     (.aliases | type == "array" and length == 2 and map(.alias) == $canonicalAliases and
       map(.deployment_id | test("^dpl_[A-Za-z0-9]+$")) == [true, true] and
       (map(.alias) | unique | length) == 2)' \
    "$PROMOTION_CONTEXT_PATH" >/dev/null; then
    fail_resume "resume context identity, target deployment, source, repository, or exact aliases did not match"
  fi

  ROLLBACK_CONTRACT_SHA256="$(jq -r '.rollback_contract_sha256' "$PROMOTION_CONTEXT_PATH")"
  local actual_contract_sha256
  actual_contract_sha256="$(sha256sum "$ROLLBACK_CONTRACT_PATH" | awk '{print $1}')"
  if [[ "$actual_contract_sha256" != "$ROLLBACK_CONTRACT_SHA256" ]]; then
    fail_resume "rollback contract identity did not match the validated resume context"
  fi
  if ! jq -e \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg repository "$EXPECTED_REPOSITORY" \
    --arg deploymentUrl "$(jq -r '.deployment_url' "$PROMOTION_CONTEXT_PATH")" \
    --arg ciRunId "$(jq -r '.ci.run_id | tostring' "$PROMOTION_CONTEXT_PATH")" \
    --arg ciRunUrl "$(jq -r '.ci.run_url' "$PROMOTION_CONTEXT_PATH")" \
    --arg rollbackArtifactName "$ROLLBACK_ARTIFACT_NAME" \
    --argjson contextAliases "$(jq -c '.aliases' "$PROMOTION_CONTEXT_PATH")" \
    'type == "object" and .schema_version == 1 and
     .kind == "vercel-alias-rollback-contract" and .repository == $repository and
     .commit_sha == $commitSha and .ref == "refs/heads/main" and
     .rollback_artifact_name == $rollbackArtifactName and
     .deployment.id == $deploymentId and .deployment.project_id == $projectId and
     .deployment.source == "github" and .deployment.repository == $repository and
     .deployment.ref == "refs/heads/main" and .deployment.commit_sha == $commitSha and
     .deployment.ready_state == "READY" and .deployment.target == "production" and
     .deployment.url == $deploymentUrl and
     (.ci.workflow == "ci.yml" and .ci.event == "push" and
       (.ci.run_id | tostring) == $ciRunId and .ci.run_url == $ciRunUrl) and
     .aliases == $contextAliases' \
    "$ROLLBACK_CONTRACT_PATH" >/dev/null; then
    fail_resume "rollback contract target or alias identity did not match the validated resume context"
  fi

  FROZEN_ALIASES="$(jq -c '.aliases | map({alias, deploymentId: .deployment_id})' "$PROMOTION_CONTEXT_PATH")"
  CURRENT_DEPLOYMENT_URL="$(jq -r '.deployment_url' "$PROMOTION_CONTEXT_PATH")"
  CI_RUN_ID="$(jq -r '.ci.run_id' "$PROMOTION_CONTEXT_PATH")"
  CI_RUN_URL="$(jq -r '.ci.run_url' "$PROMOTION_CONTEXT_PATH")"
  PROVIDER_CHECKS='["deployment_ready","deployment_target_production"]'
}

api_query() {
  local endpoint="$1"
  curl --fail-with-body --silent --show-error --location \
    --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" \
    --header "Accept: application/json" \
    --header "Authorization: Bearer $VERCEL_TOKEN" \
    "$API_BASE_URL$endpoint"
}

github_query() {
  local endpoint="$1"
  curl --fail-with-body --silent --show-error --location \
    --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time "$CURL_MAX_TIME_SECONDS" \
    --header "Accept: application/vnd.github+json" \
    --header "Authorization: Bearer $GITHUB_TOKEN" \
    --header "X-GitHub-Api-Version: 2022-11-28" \
    "$GITHUB_BASE_URL$endpoint"
}

normalize_deployment_url() {
  local value="$1"
  if [[ "$value" =~ ^https?://[[:alnum:]_.-]+$ ]]; then
    printf '%s' "$value"
  elif [[ "$value" =~ ^[[:alnum:]_.-]+$ ]]; then
    printf 'https://%s' "$value"
  fi
}

clear_observed_deployment() {
  OBSERVED_DEPLOYMENT_ID=""
  OBSERVED_DEPLOYMENT_URL=""
  OBSERVED_SOURCE=""
  OBSERVED_REF=""
  OBSERVED_READY_STATE=""
  OBSERVED_TARGET=""
}

observe_deployment() {
  local response="$1"
  clear_observed_deployment
  if ! jq -e 'type == "object"' <<< "$response" >/dev/null 2>&1; then
    return 0
  fi

  local value
  value="$(jq -r 'if (.id | type) == "string" then .id else "" end' <<< "$response")"
  if [[ "$value" =~ ^dpl_[A-Za-z0-9]+$ ]]; then
    OBSERVED_DEPLOYMENT_ID="$value"
  fi

  value="$(jq -r 'if (.url | type) == "string" then .url else "" end' <<< "$response")"
  OBSERVED_DEPLOYMENT_URL="$(normalize_deployment_url "$value")"

  value="$(jq -r 'if (.gitSource.type | type) == "string" then .gitSource.type elif .meta.githubDeployment == "1" then "github" else "" end' <<< "$response")"
  case "$value" in
    github|gitlab|bitbucket) OBSERVED_SOURCE="$value" ;;
  esac

  value="$(jq -r 'if (.gitSource.ref | type) == "string" then .gitSource.ref elif (.meta.githubCommitRef | type) == "string" then .meta.githubCommitRef else "" end' <<< "$response")"
  if [[ "$value" == "main" ]]; then
    OBSERVED_REF="refs/heads/main"
  elif [[ "$value" =~ ^refs/heads/[A-Za-z0-9._/-]+$ ]]; then
    OBSERVED_REF="$value"
  fi

  value="$(jq -r 'if (.readyState | type) == "string" then .readyState else "" end' <<< "$response")"
  case "$value" in
    BUILDING|CANCELED|DELETED|ERROR|INITIALIZING|QUEUED|READY|SKIPPED)
      OBSERVED_READY_STATE="$value"
      ;;
  esac

  value="$(jq -r 'if (.target | type) == "string" then .target else "" end' <<< "$response")"
  case "$value" in
    development|preview|production) OBSERVED_TARGET="$value" ;;
  esac
}

validate_alias_response() {
  local response="$1"
  local alias="$2"
  jq -e --arg alias "$alias" '
    (.aliases // []) as $all |
    ($all | map(select(.alias == $alias))) as $matches |
    ($all | length) == 1 and
    ($matches | length) == 1 and
    ($matches[0].deploymentId | type) == "string"
  ' <<< "$response" >/dev/null
}

validate_post_alias_response() {
  local response="$1"
  local alias="$2"
  jq -e --arg alias "$alias" --arg deploymentId "$DEPLOYMENT_ID" '
    (.aliases // []) as $all |
    ($all | map(select(.alias == $alias))) as $matches |
    ($all | length) == 1 and
    ($matches | length) == 1 and
    ($matches[0].deploymentId == $deploymentId)
  ' <<< "$response" >/dev/null
}

read_alias() {
  local alias="$1"
  local team_query=""
  if [[ -n "$VERCEL_TEAM_ID" ]]; then
    team_query="&teamId=$VERCEL_TEAM_ID"
  fi
  api_query "/v4/aliases?projectId=$VERCEL_PROJECT_ID&domain=$alias$team_query"
}

read_post_aliases() {
  POST_ALIASES='[{"alias":"wiki.rayer.idv.tw","deploymentId":null},{"alias":"llm-wiki-frontend.vercel.app","deploymentId":null}]'
  local alias response deployment_id
  for alias in "${ALIASES[@]}"; do
    if ! response="$(read_alias "$alias")"; then
      return 1
    fi
    if ! validate_post_alias_response "$response" "$alias"; then
      return 1
    fi
    deployment_id="$(jq -r --arg alias "$alias" '.aliases[] | select(.alias == $alias) | .deploymentId' <<< "$response")"
    POST_ALIASES="$(jq -c --arg alias "$alias" --arg deploymentId "$deployment_id" \
      'map(if .alias == $alias then .deploymentId = $deploymentId else . end)' <<< "$POST_ALIASES")"
  done
}

read_observed_aliases() {
  POST_ALIASES='[{"alias":"wiki.rayer.idv.tw","deploymentId":null},{"alias":"llm-wiki-frontend.vercel.app","deploymentId":null}]'
  local alias response deployment_id
  for alias in "${ALIASES[@]}"; do
    if ! response="$(read_alias "$alias")"; then
      return 1
    fi
    if ! validate_alias_response "$response" "$alias"; then
      return 1
    fi
    deployment_id="$(jq -r --arg alias "$alias" '.aliases[] | select(.alias == $alias) | .deploymentId' <<< "$response")"
    POST_ALIASES="$(jq -c --arg alias "$alias" --arg deploymentId "$deployment_id" \
      'map(if .alias == $alias then .deploymentId = $deploymentId else . end)' <<< "$POST_ALIASES")"
  done
}

validate_deployment() {
  local response="$1"
  jq -e \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg commitSha "$COMMIT_SHA" \
    --arg repository "$EXPECTED_REPOSITORY" \
    '
      .id == $deploymentId and
      .projectId == $projectId and
      .readyState == "READY" and
      .target == "production" and
      (
        (.gitSource.type // (if .meta.githubDeployment == "1" then "github" else null end)) == "github"
      ) and
      ((.gitSource.ref // .meta.githubCommitRef) == "main" or
       (.gitSource.ref // .meta.githubCommitRef) == "refs/heads/main") and
      (.gitSource.sha // .meta.githubCommitSha) == $commitSha and
      (.meta.githubOrg | type) == "string" and
      (.meta.githubRepo | type) == "string" and
      (.meta.githubOrg + "/" + .meta.githubRepo) == $repository
    ' <<< "$response" >/dev/null
}

verify_frozen_aliases() {
  local alias response current_deployment_id frozen_deployment_id matches=1
  for alias in "${ALIASES[@]}"; do
    if ! response="$(read_alias "$alias")"; then
      matches=0
      continue
    fi
    if ! validate_alias_response "$response" "$alias"; then
      matches=0
      continue
    fi
    current_deployment_id="$(jq -r --arg alias "$alias" '.aliases[] | select(.alias == $alias) | .deploymentId' <<< "$response")"
    frozen_deployment_id="$(jq -r --arg alias "$alias" '.[] | select(.alias == $alias) | .deploymentId' <<< "$FROZEN_ALIASES")"
    if [[ "$current_deployment_id" != "$frozen_deployment_id" ]]; then
      matches=0
    fi
  done
  [[ "$matches" -eq 1 ]]
}

alias_set() {
  local alias="$1"
  local error_path
  error_path="$(mktemp)"
  local -a command_args=(alias set "$DEPLOYMENT_ID" "$alias")
  if [[ -n "$VERCEL_SCOPE" ]]; then
    command_args+=(--scope "$VERCEL_SCOPE")
  fi
  if timeout --signal=TERM --kill-after=5s "${ALIAS_SET_TIMEOUT_SECONDS}s" \
    vercel "${command_args[@]}" >/dev/null 2>"$error_path"; then
    rm -f "$error_path"
    return 0
  fi
  rm -f "$error_path"
  return 1
}

if [[ ! "$COMMIT_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  fail_preflight "commit_sha must be exactly 40 lowercase hexadecimal characters"
fi
if [[ ! "$DEPLOYMENT_ID" =~ ^dpl_[A-Za-z0-9]+$ ]]; then
  fail_preflight "deployment_id must be an immutable Vercel deployment ID"
fi
if [[ -n "$TICKET_REF" && ! "$TICKET_REF" =~ ^[A-Za-z0-9._/-]+$ ]]; then
  fail_preflight "ticket_ref contains unsupported characters"
fi
if [[ -z "$GITHUB_REPOSITORY" || -z "$GITHUB_TOKEN" || -z "$VERCEL_TOKEN" || -z "$VERCEL_PROJECT_ID" ]]; then
  fail_preflight "required read-only provider configuration is missing"
fi
if [[ "$GITHUB_REPOSITORY" != "$EXPECTED_REPOSITORY" ]]; then
  fail_preflight "GITHUB_REPOSITORY must equal the exact repository identity $EXPECTED_REPOSITORY"
fi
if [[ ! "$VERCEL_PROJECT_ID" =~ ^prj_[A-Za-z0-9]+$ ]]; then
  fail_preflight "VERCEL_PROJECT_ID is not a bounded project ID"
fi
if [[ -n "$VERCEL_TEAM_ID" && ! "$VERCEL_TEAM_ID" =~ ^team_[A-Za-z0-9]+$ ]]; then
  fail_preflight "VERCEL_TEAM_ID is not a bounded team ID"
fi
if [[ ! "$ALIAS_SET_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ || "$ALIAS_SET_TIMEOUT_SECONDS" -gt 300 ]]; then
  fail_preflight "VERCEL_ALIAS_TIMEOUT_SECONDS must be a bounded positive number of seconds"
fi
for command in curl jq sha256sum timeout vercel; do
  if ! command -v "$command" >/dev/null 2>&1; then
    fail_preflight "required command is unavailable: $command"
  fi
done

deployment_endpoint="/v13/deployments/$DEPLOYMENT_ID"
if [[ -n "$VERCEL_TEAM_ID" ]]; then
  deployment_endpoint+="?teamId=$VERCEL_TEAM_ID"
fi

if [[ "$MODE" == "preflight" ]]; then
  github_runs=""
  if ! github_runs="$(github_query "/repos/$GITHUB_REPOSITORY/actions/workflows/ci.yml/runs?head_sha=$COMMIT_SHA&branch=main&event=push&per_page=100")"; then
    fail_preflight "GitHub CI read failed"
  fi
  ci_run="$(jq -c --arg commitSha "$COMMIT_SHA" '
    first(.workflow_runs[]? | select(
      .path == ".github/workflows/ci.yml" and
      .head_branch == "main" and
      .head_sha == $commitSha and
      .event == "push" and
      .status == "completed" and
      .conclusion == "success" and
      (.id | type) == "number" and
      (.html_url | type) == "string"
    )) // empty
  ' <<< "$github_runs")"
  if [[ -z "$ci_run" ]]; then
    fail_preflight "exact main CI success was not found for commit_sha"
  fi
  CI_RUN_ID="$(jq -r '.id' <<< "$ci_run")"
  CI_RUN_URL="$(jq -r '.html_url' <<< "$ci_run")"

  deployment_response=""
  if ! deployment_response="$(api_query "$deployment_endpoint")"; then
    fail_preflight "Vercel deployment read failed"
  fi
  if ! validate_deployment "$deployment_response"; then
    fail_preflight "deployment project, repository, Git source, commit, READY state, or production target did not match"
  fi
  observe_deployment "$deployment_response"
  CURRENT_DEPLOYMENT_URL="$OBSERVED_DEPLOYMENT_URL"
  if [[ -z "$CURRENT_DEPLOYMENT_URL" ]]; then
    fail_preflight "deployment URL was not present in the provider read-back"
  fi
  PROVIDER_CHECKS='["deployment_ready","deployment_target_production"]'

  for alias in "${ALIASES[@]}"; do
    alias_response=""
    if ! alias_response="$(read_alias "$alias")"; then
      fail_preflight "canonical alias read failed for $alias"
    fi
    if ! validate_alias_response "$alias_response" "$alias"; then
      fail_preflight "canonical alias is missing or divergent for $alias"
    fi
    prior_deployment_id="$(jq -r --arg alias "$alias" \
      '.aliases[] | select(.alias == $alias) | .deploymentId' <<< "$alias_response")"
    if [[ ! "$prior_deployment_id" =~ ^dpl_[A-Za-z0-9]+$ ]]; then
      fail_preflight "canonical alias rollback handle is not an immutable deployment ID for $alias"
    fi
    FROZEN_ALIASES="$(jq -c --arg alias "$alias" --arg deploymentId "$prior_deployment_id" \
      'map(if .alias == $alias then .deploymentId = $deploymentId else . end)' <<< "$FROZEN_ALIASES")"
  done

  if ! verify_frozen_aliases; then
    fail_preflight "canonical alias changed from the frozen rollback snapshot"
  fi
  write_rollback_contract
  write_promotion_context
  STATUS="PREFLIGHT_READY"
  REASON="exact main CI, READY production deployment, and both canonical alias rollback handles were validated"
  NEXT_ACTION="Upload the immutable rollback contract before starting promote."
  printf '%s\n' "$STATUS"
  exit 0
fi

load_and_validate_resume
if ! verify_frozen_aliases; then
  fail_preflight "canonical alias changed from the frozen rollback snapshot before mutation"
fi

mutation_failed=0
failed_alias=""
if ! alias_set "${ALIASES[0]}"; then
  mutation_failed=1
  failed_alias="${ALIASES[0]}"
else
  if ! alias_set "${ALIASES[1]}"; then
    mutation_failed=1
    failed_alias="${ALIASES[1]}"
  fi
fi

if [[ "$mutation_failed" -eq 1 ]]; then
  read_observed_aliases || true
  STATUS="PARTIAL_MUTATION"
  REASON="bounded alias command failed or became uncertain for $failed_alias"
  NEXT_ACTION="Read /v4/aliases before retry; do not blindly replay either alias mutation."
  printf '%s: %s\n' "$STATUS" "$REASON" >&2
  exit 1
fi

if ! read_post_aliases; then
  fail_postcheck "authoritative /v4/aliases post-state did not map both aliases to the exact deployment"
fi
PROVIDER_CHECKS="$(jq -c '. + ["alias_routing_exact"]' <<< "$PROVIDER_CHECKS")"

clear_observed_deployment
if ! deployment_response="$(api_query "$deployment_endpoint")"; then
  fail_postcheck "post-mutation deployment read failed"
fi
observe_deployment "$deployment_response"
if ! validate_deployment "$deployment_response"; then
  fail_postcheck "post-mutation deployment inspect no longer matched the exact READY production source"
fi
if [[ -z "$OBSERVED_DEPLOYMENT_URL" ]]; then
  fail_postcheck "post-mutation deployment read did not include a deployment URL"
fi
PROVIDER_CHECKS="$(jq -c 'if index("deployment_ready") then . else . + ["deployment_ready"] end | if index("deployment_target_production") then . else . + ["deployment_target_production"] end' <<< "$PROVIDER_CHECKS")"

health_failed=0
health_failure_reason=""
for alias in "${ALIASES[@]}"; do
  health_path="$(mktemp)"
  health_result=""
  if ! health_result="$(curl --silent --show-error --location \
    --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" --max-time 20 --max-redirs 3 \
    --output "$health_path" --write-out '%{http_code}\t%{url_effective}' "https://$alias/")"; then
    health_result=$'000\t'
  fi
  rm -f "$health_path"
  http_code="${health_result%%$'\t'*}"
  effective_url=""
  if [[ "$health_result" == *$'\t'* ]]; then
    effective_url="${health_result#*$'\t'}"
  fi
  effective_host=""
  if [[ "$effective_url" =~ ^https?://([[:alnum:]_.-]+)(:[0-9]+)?(/|$) ]]; then
    if [[ "${BASH_REMATCH[1]}" == "$alias" ]]; then
      effective_host="$alias"
    fi
  fi
  if [[ ! "$http_code" =~ ^[0-9]{3}$ ]]; then
    http_code="000"
  fi
  HEALTH="$(jq -c --arg alias "$alias" --arg statusCode "$http_code" \
    --arg effectiveHost "$effective_host" \
    'map(if .alias == $alias then .status_code = $statusCode | .effective_host = ($effectiveHost | if . == "" then null else . end) else . end)' <<< "$HEALTH")"
  if [[ "$effective_host" != "$alias" ]]; then
    health_failed=1
    health_failure_reason="canonical HTTP health redirected to a different host for $alias"
    continue
  fi
  if [[ "$http_code" != "200" ]]; then
    health_failed=1
    health_failure_reason="canonical HTTP health failed for $alias"
  fi
done

if [[ "$health_failed" -eq 1 ]]; then
  fail_postcheck "$health_failure_reason"
fi

PROVIDER_CHECKS="$(jq -c '. + ["http_health_exact"]' <<< "$PROVIDER_CHECKS")"
CHECKED_AT="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
STATUS="SUCCESS"
REASON="both canonical aliases mapped to the exact READY deployment and passed read-back and HTTP gates"
NEXT_ACTION="No retry is required."
printf '%s\n' "$STATUS"
exit 0
