#!/usr/bin/env bash
set -Eeuo pipefail

readonly ALIASES=("wiki.rayer.idv.tw" "llm-wiki-frontend.vercel.app")
readonly API_BASE_URL="${VERCEL_API_BASE_URL:-https://api.vercel.com}"
readonly GITHUB_BASE_URL="${GITHUB_API_URL:-https://api.github.com}"
readonly EVIDENCE_DIR="${EVIDENCE_DIR:-artifacts/vercel-alias-promotion}"
readonly EVIDENCE_PATH="$EVIDENCE_DIR/vercel-alias-promotion.json"

COMMIT_SHA="${COMMIT_SHA:-}"
DEPLOYMENT_ID="${DEPLOYMENT_ID:-}"
TICKET_REF="${TICKET_REF:-}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-}"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
VERCEL_TOKEN="${VERCEL_TOKEN:-}"
VERCEL_PROJECT_ID="${VERCEL_PROJECT_ID:-}"
VERCEL_TEAM_ID="${VERCEL_TEAM_ID:-}"
VERCEL_SCOPE="${VERCEL_SCOPE:-}"

STATUS="FAILED"
REASON="unexpected failure"
NEXT_ACTION="Inspect the evidence and provider read-back before retrying."
FROZEN_ALIASES="[]"
POST_ALIASES="[]"
HEALTH="[]"
EVIDENCE_WRITTEN=0

write_evidence() {
  if [[ "$EVIDENCE_WRITTEN" -eq 1 ]]; then
    return
  fi
  EVIDENCE_WRITTEN=1
  mkdir -p "$EVIDENCE_DIR"
  local temporary_path="$EVIDENCE_PATH.tmp"
  jq -n \
    --arg schemaVersion "1" \
    --arg ticketRef "$TICKET_REF" \
    --arg commitSha "$COMMIT_SHA" \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg status "$STATUS" \
    --arg reason "$REASON" \
    --arg nextAction "$NEXT_ACTION" \
    --argjson aliases "$FROZEN_ALIASES" \
    --argjson postAliases "$POST_ALIASES" \
    --argjson health "$HEALTH" \
    '{
      schemaVersion: ($schemaVersion | tonumber),
      ticketRef: $ticketRef,
      commitSha: $commitSha,
      deploymentId: $deploymentId,
      projectId: $projectId,
      aliases: $aliases,
      postAliases: $postAliases,
      health: $health,
      status: $status,
      reason: $reason,
      nextAction: $nextAction
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

api_query() {
  local endpoint="$1"
  curl --fail-with-body --silent --show-error --location \
    --header "Accept: application/json" \
    --header "Authorization: Bearer $VERCEL_TOKEN" \
    "$API_BASE_URL$endpoint"
}

github_query() {
  local endpoint="$1"
  curl --fail-with-body --silent --show-error --location \
    --header "Accept: application/vnd.github+json" \
    --header "Authorization: Bearer $GITHUB_TOKEN" \
    --header "X-GitHub-Api-Version: 2022-11-28" \
    "$GITHUB_BASE_URL$endpoint"
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
  POST_ALIASES="[]"
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
      '. + [{alias: $alias, deploymentId: $deploymentId}]' <<< "$POST_ALIASES")"
  done
}

validate_deployment() {
  local response="$1"
  jq -e \
    --arg deploymentId "$DEPLOYMENT_ID" \
    --arg projectId "$VERCEL_PROJECT_ID" \
    --arg commitSha "$COMMIT_SHA" \
    --arg repository "$GITHUB_REPOSITORY" \
    '
      .id == $deploymentId and
      .projectId == $projectId and
      .readyState == "READY" and
      (
        (.gitSource.type // (if .meta.githubDeployment == "1" then "github" else null end)) == "github"
      ) and
      (.gitSource.ref // .meta.githubCommitRef) == "main" and
      (.gitSource.sha // .meta.githubCommitSha) == $commitSha and
      (
        (.meta.githubOrg and .meta.githubRepo) as $hasRepository |
        if $hasRepository then (.meta.githubOrg + "/" + .meta.githubRepo) == $repository else true end
      )
    ' <<< "$response" >/dev/null
}

alias_set() {
  local alias="$1"
  local error_path
  error_path="$(mktemp)"
  local -a command_args=(alias set "$DEPLOYMENT_ID" "$alias" --token "$VERCEL_TOKEN")
  if [[ -n "$VERCEL_SCOPE" ]]; then
    command_args+=(--scope "$VERCEL_SCOPE")
  fi
  if vercel "${command_args[@]}" >/dev/null 2>"$error_path"; then
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
if [[ ! "$VERCEL_PROJECT_ID" =~ ^prj_[A-Za-z0-9]+$ ]]; then
  fail_preflight "VERCEL_PROJECT_ID is not a bounded project ID"
fi
if [[ -n "$VERCEL_TEAM_ID" && ! "$VERCEL_TEAM_ID" =~ ^team_[A-Za-z0-9]+$ ]]; then
  fail_preflight "VERCEL_TEAM_ID is not a bounded team ID"
fi
for command in curl jq vercel; do
  if ! command -v "$command" >/dev/null 2>&1; then
    fail_preflight "required command is unavailable: $command"
  fi
done

github_runs=""
if ! github_runs="$(github_query "/repos/$GITHUB_REPOSITORY/actions/runs?head_sha=$COMMIT_SHA&branch=main&event=push&per_page=100")"; then
  fail_preflight "GitHub CI read failed"
fi
if ! jq -e --arg commitSha "$COMMIT_SHA" '
  any(.workflow_runs[]?;
    .name == "CI" and
    .head_branch == "main" and
    .head_sha == $commitSha and
    .event == "push" and
    .status == "completed" and
    .conclusion == "success"
  )
' <<< "$github_runs" >/dev/null; then
  fail_preflight "exact main CI success was not found for commit_sha"
fi

deployment_response=""
deployment_endpoint="/v13/deployments/$DEPLOYMENT_ID"
if [[ -n "$VERCEL_TEAM_ID" ]]; then
  deployment_endpoint+="?teamId=$VERCEL_TEAM_ID"
fi
if ! deployment_response="$(api_query "$deployment_endpoint")"; then
  fail_preflight "Vercel deployment read failed"
fi
if ! validate_deployment "$deployment_response"; then
  fail_preflight "deployment project, Git source, commit, or READY state did not match"
fi

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
    '. + [{alias: $alias, deploymentId: $deploymentId}]' <<< "$FROZEN_ALIASES")"
done

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
  read_post_aliases || true
  STATUS="PARTIAL_MUTATION"
  REASON="bounded alias command failed or became uncertain for $failed_alias"
  NEXT_ACTION="Read /v4/aliases before retry; do not blindly replay either alias mutation."
  printf '%s: %s\n' "$STATUS" "$REASON" >&2
  exit 1
fi

if ! read_post_aliases; then
  fail_postcheck "authoritative /v4/aliases post-state did not map both aliases to the exact deployment"
fi

if ! deployment_response="$(api_query "$deployment_endpoint")"; then
  fail_postcheck "post-mutation deployment read failed"
fi
if ! validate_deployment "$deployment_response"; then
  fail_postcheck "post-mutation deployment inspect no longer matched the exact READY source"
fi

for alias in "${ALIASES[@]}"; do
  health_path="$(mktemp)"
  http_code=""
  if ! http_code="$(curl --silent --show-error --location --max-time 20 --max-redirs 3 \
    --output "$health_path" --write-out '%{http_code}' "https://$alias/")"; then
    http_code="000"
  fi
  rm -f "$health_path"
  HEALTH="$(jq -c --arg alias "$alias" --arg statusCode "$http_code" \
    '. + [{alias: $alias, statusCode: $statusCode}]' <<< "$HEALTH")"
  if [[ "$http_code" != "200" ]]; then
    fail_postcheck "canonical HTTP health failed for $alias"
  fi
done

STATUS="SUCCESS"
REASON="both canonical aliases mapped to the exact READY deployment and passed read-back and HTTP gates"
NEXT_ACTION="No retry is required."
printf '%s\n' "$STATUS"
exit 0
