#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
BFF_DIR="$ROOT/apps/bff"

die() { echo "cd contract failed: $*" >&2; exit 1; }
need() { [[ -n "${!1:-}" ]] || die "$1 is required"; }
plan_json() { jq -er ".normalized$1" "$PLAN_PATH"; }
has_component() { jq -e --arg name "$1" '.normalized.selected_components | index($name) != null' "$PLAN_PATH" >/dev/null; }
join_config_list() { plan_json "$1 | join([44] | implode)"; }

strict_json() {
  python3 -c '
import json
import sys

def reject_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result

try:
    json.load(sys.stdin, object_pairs_hook=reject_duplicates)
except (ValueError, UnicodeDecodeError):
    raise SystemExit(1)
'
}

CI_AGGREGATE_JOB_NAME='canonical-ci'

gh_paginated_array() {
  local endpoint="$1" key="$2" page=1 response items count total='' all='[]' separator
  while (( page <= 20 )); do
    separator='?'; [[ "$endpoint" == *\?* ]] && separator='&'
    response=$(gh api "${endpoint}${separator}per_page=100&page=${page}") || return 1
    strict_json <<<"$response" || return 1
    jq -e --arg key "$key" 'type == "object" and has($key) and (.[$key]|type) == "array"' <<<"$response" >/dev/null || return 1
    items=$(jq -c --arg key "$key" '.[$key]' <<<"$response")
    count=$(jq -er 'length' <<<"$items")
    all=$(jq -cn --argjson all "$all" --argjson items "$items" '$all + $items')
    if [[ -z "$total" ]] && jq -e '.total_count? | type == "number" and floor == .' <<<"$response" >/dev/null; then
      total=$(jq -er '.total_count' <<<"$response")
    fi
    (( count < 100 )) && break
    page=$((page + 1))
  done
  (( page <= 20 )) || return 1
  [[ -z "$total" ]] || [[ "$total" == "$(jq -er 'length' <<<"$all")" ]] || return 1
  printf '%s\n' "$all"
}

ci_validate_jobs() {
  local jobs="$1" run_id="$2" attempt="$3"
  jq -e --arg aggregate "$CI_AGGREGATE_JOB_NAME" --argjson run_id "$run_id" --argjson attempt "$attempt" '
    type == "array" and length > 0 and
    all(.[]; type == "object" and (.id|type == "number" and floor == . and . > 0) and
      (.name|type == "string" and length > 0) and (.run_id == $run_id) and (.run_attempt == $attempt) and
      .status == "completed" and .conclusion == "success") and
    (map(.name) as $names |
      map(.id) as $ids |
      ($names|unique|length) == ($names|length) and
      ($ids|unique|length) == ($ids|length) and
      ($names|map(select(. == $aggregate))|length) == 1)
  ' <<<"$jobs" >/dev/null
}

ci_validate_run() {
  local run="$1" expected_sha="$2" expected_ref="$3" expected_attempt="$4"
  jq -e --arg sha "$expected_sha" --arg ref "$expected_ref" --argjson attempt "$expected_attempt" '
    type == "object" and (.id|type == "number" and floor == . and . > 0) and
    .path == ".github/workflows/ci.yml" and .event == "push" and .head_branch == $ref and
    .head_sha == $sha and .run_attempt == $attempt and .status == "completed" and .conclusion == "success"
  ' <<<"$run" >/dev/null
}

revalidate_ci() {
  need PLAN_PATH; need GH_TOKEN; need GITHUB_REPOSITORY; need SOURCE_REF; need SOURCE_SHA
  local run_id attempt_id expected_run current_attempt current_jobs
  jq -e '.ci | type == "object" and (.jobs|type) == "array"' "$PLAN_PATH" >/dev/null || die "pinned canonical CI record is missing"
  run_id=$(jq -er '.ci.run_id | select(type == "number" and floor == . and . > 0)' "$PLAN_PATH") || die "pinned canonical CI run ID is invalid"
  attempt_id=$(jq -er '.ci.run_attempt | select(type == "number" and floor == . and . > 0)' "$PLAN_PATH") || die "pinned canonical CI run attempt is invalid"
  expected_run=$(jq -ce '.ci | {run_id,run_attempt,workflow_path,event,head_branch,head_sha,conclusion}' "$PLAN_PATH") || die "pinned canonical CI record is malformed"
  [[ "$(jq -r '.workflow_path' <<<"$expected_run")" == .github/workflows/ci.yml ]] || die "pinned CI workflow path is not canonical"
  [[ "$(jq -r '.event' <<<"$expected_run")" == push ]] || die "pinned CI event is not canonical"
  [[ "$(jq -r '.head_branch' <<<"$expected_run")" == "$SOURCE_REF" && "$(jq -r '.head_sha' <<<"$expected_run")" == "$SOURCE_SHA" ]] || die "pinned CI source is not exact"
  [[ "$(jq -r '.conclusion' <<<"$expected_run")" == success ]] || die "pinned CI conclusion is not successful"
  git fetch origin "$SOURCE_REF" --force --no-tags || die "canonical source ref could not be refreshed"
  [[ "$(git rev-parse HEAD)" == "$SOURCE_SHA" ]] || die "checked-out source changed before provider work"
  [[ "$(git rev-parse "origin/$SOURCE_REF")" == "$SOURCE_SHA" ]] || die "canonical source ref advanced before provider work"
  local run
  run=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}") || die "pinned canonical CI run is unreadable"
  strict_json <<<"$run" || die "pinned canonical CI run response is malformed"
  jq -e --argjson run_id "$run_id" '.id == $run_id' <<<"$run" >/dev/null || die "pinned canonical CI run identity changed"
  ci_validate_run "$run" "$SOURCE_SHA" "$SOURCE_REF" "$attempt_id" || die "pinned canonical CI run is not the exact successful attempt"
  current_attempt=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/attempts/${attempt_id}") || die "pinned canonical CI attempt is unreadable"
  strict_json <<<"$current_attempt" || die "pinned canonical CI attempt response is malformed"
  jq -e --argjson run_id "$run_id" '.id == $run_id' <<<"$current_attempt" >/dev/null || die "pinned canonical CI attempt identity changed"
  ci_validate_run "$current_attempt" "$SOURCE_SHA" "$SOURCE_REF" "$attempt_id" || die "pinned canonical CI attempt is not successful"
  current_jobs=$(gh_paginated_array "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/attempts/${attempt_id}/jobs" jobs) || die "pinned canonical CI jobs are unreadable or partial"
  ci_validate_jobs "$current_jobs" "$run_id" "$attempt_id" || die "pinned canonical CI job set is incomplete or ambiguous"
  jq -n -e --argjson expected "$(jq -c '.ci.jobs' "$PLAN_PATH")" --argjson actual "$(jq -c 'map({id,name,status,conclusion,run_id,run_attempt})|sort_by(.name)' <<<"$current_jobs")" '$expected == $actual' >/dev/null || die "pinned canonical CI job set changed"
}

revalidate_before_provider() {
  [[ "${ROLLBACK_UPLOADED:-}" == 1 ]] || return 0
  revalidate_ci
}

validate_inputs() {
  # Validate the environment and config/ref tuple before protected environment access.
  need DEPLOYMENT_ENVIRONMENT; need ENVIRONMENT; need SOURCE_REF; need SOURCE_SHA; need COMPONENTS; need CONFIG_PATH; need GITHUB_REPOSITORY
  case "$DEPLOYMENT_ENVIRONMENT:$ENVIRONMENT" in
    Development:development|Production:production) ;;
    *) die "deployment and config environments do not match" ;;
  esac
  [[ "$ENVIRONMENT" == development || "$ENVIRONMENT" == production ]] || die "environment must be development or production"
  [[ "$SOURCE_REF" == develop || "$SOURCE_REF" == main ]] || die "source ref is not an approved branch"
  [[ "$SOURCE_SHA" =~ ^[0-9a-f]{40}$ ]] || die "source SHA is not a full lowercase commit SHA"
  [[ "$GITHUB_REF" == refs/heads/* ]] || die "workflow ref is not a branch ref"
  [[ "$GITHUB_REF_NAME" == "$SOURCE_REF" ]] || die "workflow branch does not match source ref"
  local components_json
  components_json=$(jq -Rn --arg raw "$COMPONENTS" '$raw | split(",") | map(gsub("^\\s+|\\s+$"; ""))')
  jq -e 'type == "array" and length > 0 and all(.[]; . == "auth" or . == "bff" or . == "worker" or . == "frontend") and (unique|length == length)' <<<"$components_json" >/dev/null || die "component selection is not an exact nonempty allowlist"
  [[ "$ENVIRONMENT" == development && "$SOURCE_REF" == develop || "$ENVIRONMENT" == production && "$SOURCE_REF" == main ]] || die "environment and source ref do not match"
  [[ "$CONFIG_PATH" == "deploy/environments/$ENVIRONMENT.yaml" ]] || die "config path is not fixed for the environment"
}

plan() {
  validate_inputs
  git fetch origin "$SOURCE_REF" --force --no-tags
  [[ "$(git rev-parse HEAD)" == "$SOURCE_SHA" ]] || die "checked-out source does not match the selected SHA"
  [[ "$(git rev-parse "origin/$SOURCE_REF")" == "$SOURCE_SHA" ]] || die "canonical source ref advanced"
  local runs candidates run id attempt jobs ci
  runs=$(gh_paginated_array "repos/$GITHUB_REPOSITORY/actions/workflows/ci.yml/runs?head_sha=${SOURCE_SHA}&event=push&branch=${SOURCE_REF}" workflow_runs) || die "canonical CI run list is malformed or partial"
  jq -e 'map(.id) as $ids | ($ids|length) == ($ids|unique|length) and all(.[]; type == "object")' <<<"$runs" >/dev/null || die "canonical CI run list contains duplicate or malformed entries"
  jq -e 'all(.[]; (.id|type == "number" and floor == . and . > 0) and (.run_attempt|type == "number" and floor == . and . > 0) and (.path|type == "string") and (.event|type == "string") and (.head_branch|type == "string") and (.head_sha|type == "string") and (.status|type == "string"))' <<<"$runs" >/dev/null || die "canonical CI run list contains malformed records"
  candidates=$(jq -c --arg sha "$SOURCE_SHA" --arg ref "$SOURCE_REF" '[.[] | select(.path == ".github/workflows/ci.yml" and .event == "push" and .head_branch == $ref and .head_sha == $sha and .status == "completed" and .conclusion == "success")]' <<<"$runs")
  [[ "$(jq -er 'length' <<<"$candidates")" == 1 ]] || die "canonical CI successful run is missing or ambiguous"
  run=$(jq -c '.[0]' <<<"$candidates"); id=$(jq -er '.id' <<<"$run"); attempt=$(jq -er '.run_attempt' <<<"$run")
  jobs=$(gh_paginated_array "repos/$GITHUB_REPOSITORY/actions/runs/$id/attempts/$attempt/jobs" jobs) || die "canonical CI job list is malformed or partial"
  ci_validate_jobs "$jobs" "$id" "$attempt" || die "canonical CI job set is incomplete or ambiguous"
  ci=$(jq -cn --argjson run "$run" --argjson jobs "$jobs" '{run_id:$run.id,run_attempt:$run.run_attempt,workflow_path:$run.path,event:$run.event,head_branch:$run.head_branch,head_sha:$run.head_sha,conclusion:$run.conclusion,jobs:($jobs|map({id,name,status,conclusion,run_id,run_attempt})|sort_by(.name))}')
  mkdir -p "$(dirname "$PLAN_PATH")"
  (cd "$BFF_DIR" && go run ./cmd/deploy_config --environment "$ENVIRONMENT" --config "$ROOT/$CONFIG_PATH" --components "$COMPONENTS") > "$PLAN_PATH.normalized"
  jq -n --slurpfile normalized "$PLAN_PATH.normalized" --arg source_sha "$SOURCE_SHA" --arg source_ref "$SOURCE_REF" --arg config_path "$CONFIG_PATH" --argjson ci "$ci" '{source:{sha:$source_sha,ref:$source_ref},config_path:$config_path,ci:$ci,normalized:$normalized[0]}' > "$PLAN_PATH"
  rm -f "$PLAN_PATH.normalized"
}

mutation_status_path() { printf '%s/mutation-status.json\n' "$ARTIFACT_DIR"; }
set_mutation_status() {
  local component="$1" result="$2"
  mkdir -p "$ARTIFACT_DIR"
  jq -n --arg component "$component" --arg result "$result" '{component:$component,result:$result}' > "$(mutation_status_path)"
}

component_result_path() { printf '%s/components/%s.json\n' "$ARTIFACT_DIR" "$1"; }
write_component_result() {
  local component="$1" result="$2" observed="${3:-}" reason="${4:-}"
  [[ -n "$observed" ]] || observed='{}'
  mkdir -p "$ARTIFACT_DIR/components"
  jq -n --arg component "$component" --arg result "$result" --arg reason "$reason" --argjson observed "$observed" \
    '{component:$component,result:$result,observed:$observed} + (if $reason == "" then {} else {reason:$reason} end)' > "$(component_result_path "$component")"
}

rollback_result_path() { printf '%s/rollback/%s.json\n' "$ARTIFACT_DIR" "$1"; }
write_rollback_result() {
  local component="$1" result="$2" readback="${3:-}" reason="${4:-}"
  [[ -n "$readback" ]] || readback='{}'
  mkdir -p "$ARTIFACT_DIR/rollback"
  jq -n --arg component "$component" --arg result "$result" --arg reason "$reason" --argjson readback "$readback" \
    '{component:$component,result:$result,verified:($result == "success"),readback:$readback} + (if $reason == "" then {} else {reason:$reason} end)' > "$(rollback_result_path "$component")"
}

journal_validate() {
  local selected
  selected=$(plan_json '.selected_components') || return 1
  jq -e --argjson selected "$selected" --argjson known '["auth","bff","worker","frontend"]' '
    def allowed_state:
      . == "pending" or . == "accepted" or . == "unknown" or . == "rejected_or_no_mutation" or
      . == "rollback_pending" or . == "rollback_accepted" or . == "rollback_failed" or . == "rollback_unknown";
    def allowed_transition($from; $to):
      ($from == "pending" and ($to == "accepted" or $to == "unknown" or $to == "rejected_or_no_mutation" or $to == "rollback_pending")) or
      ($from == "accepted" and ($to == "unknown" or $to == "rollback_pending")) or
      ($from == "unknown" and $to == "rollback_pending") or
      ($from == "rollback_pending" and ($to == "rollback_accepted" or $to == "rollback_failed" or $to == "rollback_unknown"));
    type == "object" and .schema == "lwc-306-mutation-journal-v1" and
    (.order | type) == "array" and .order == $selected and
    ((.order | unique | length) == (.order | length)) and ((.order - $known) | length) == 0 and
    (.components | type) == "object" and (((.components | keys) - $selected) | length) == 0 and
    all(.components | to_entries[];
      .value as $entry |
      (($entry | keys | sort) == ["attempt","history","state","timestamp"]) and
      ($entry.state | allowed_state) and
      ($entry.attempt | type == "number" and floor == . and . > 0) and
      ($entry.timestamp | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
      ($entry.history | type == "array" and length > 0 and all(.[]; allowed_state)) and
      ($entry.history[-1] == $entry.state) and
      (($entry.history[0] == "pending") or ($entry.history[0] == "rejected_or_no_mutation")) and
      (all(range(1; ($entry.history | length)); allowed_transition($entry.history[.-1]; $entry.history[.])))
    )
  ' "$JOURNAL_PATH" >/dev/null
}

journal_init() {
  need JOURNAL_PATH; need PLAN_PATH
  mkdir -p "$(dirname "$JOURNAL_PATH")"
  if [[ -s "$JOURNAL_PATH" ]]; then
    journal_validate || die "mutation journal is malformed"
    return 0
  fi
  jq -n --argjson order "$(plan_json '.selected_components')" '{schema:"lwc-306-mutation-journal-v1",order:$order,components:{}}' > "$JOURNAL_PATH"
}

journal_transition() {
  local component="$1" next="$2" current tmp timestamp attempt
  journal_init
  current=$(jq -er --arg component "$component" '.components[$component].state // empty' "$JOURNAL_PATH" 2>/dev/null || true)
  if [[ "$next" == pending ]]; then
    [[ -z "$current" ]] || die "duplicate mutation journal transition for $component"
  elif [[ "$next" == rejected_or_no_mutation ]]; then
    [[ -z "$current" || "$current" == pending ]] || die "duplicate rejection journal transition for $component"
  elif [[ "$next" == accepted ]]; then
    [[ "$current" == pending ]] || die "mutation acceptance for $component is not after pending"
  elif [[ "$next" == unknown ]]; then
    [[ "$current" == pending || "$current" == accepted ]] || die "unknown mutation state for $component is not after pending or accepted"
  elif [[ "$next" == rollback_pending ]]; then
    [[ "$current" == accepted || "$current" == pending || "$current" == unknown ]] || die "rollback transition for $component is not eligible"
  elif [[ "$next" == rollback_accepted || "$next" == rollback_failed || "$next" == rollback_unknown ]]; then
    [[ "$current" == rollback_pending ]] || die "rollback terminal transition for $component is not pending"
  else
    die "unknown mutation journal state $next"
  fi
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  attempt="${GITHUB_RUN_ATTEMPT:-1}"
  [[ "$attempt" =~ ^[1-9][0-9]*$ ]] || die "mutation journal attempt is invalid"
  tmp="$JOURNAL_PATH.tmp"
  jq -e --arg component "$component" '
    ((.order | map(select(. == $component)) | length) == 1) and
    (if .components[$component]? then (.components[$component].history|type) == "array" and (.components[$component].history[-1] == .components[$component].state) else true end)
  ' "$JOURNAL_PATH" >/dev/null || die "mutation journal component is not selected or is malformed"
  jq --arg component "$component" --arg next "$next" --arg timestamp "$timestamp" --argjson attempt "$attempt" '.components[$component] = {state:$next,history:((.components[$component].history // []) + [$next]),timestamp:$timestamp,attempt:$attempt}' "$JOURNAL_PATH" > "$tmp" || die "mutation journal write failed"
  mv "$tmp" "$JOURNAL_PATH"
}

journal_pending() { journal_transition "$1" pending; }
journal_rejected() { journal_transition "$1" rejected_or_no_mutation; }
journal_accepted() { journal_transition "$1" accepted; }

mutation_accepted() {
  local component="$1"
  journal_init
  if ! jq -e --arg component "$component" '.components[$component]? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending "$component"; fi
  journal_accepted "$component"
}

journal_possible_components() {
  jq -r '. as $journal | ($journal.order | reverse[]) as $component | select($journal.components[$component].state == "accepted" or $journal.components[$component].state == "pending" or $journal.components[$component].state == "unknown") | $component' "$JOURNAL_PATH"
}
journal_possible_count() { jq -er '[.order[] as $component | select(.components[$component].state == "accepted" or .components[$component].state == "pending" or .components[$component].state == "unknown")] | length' "$JOURNAL_PATH"; }
journal_mutation_components() { jq -c '[.order[] as $component | select(any(.components[$component].history[]?; . == "accepted" or . == "pending" or . == "unknown")) | $component]' "$JOURNAL_PATH"; }
journal_has_rollback_terminal() { jq -e '[. as $journal | $journal.order[] as $component | select($journal.components[$component].state == "rollback_accepted" or $journal.components[$component].state == "rollback_failed" or $journal.components[$component].state == "rollback_unknown")] | length > 0' "$JOURNAL_PATH" >/dev/null; }

freeze_store() {
  local component="$1" value="$2" current
  mkdir -p "$(dirname "$ROLLBACK_PATH")"
  if [[ -s "$ROLLBACK_PATH" ]]; then current=$(jq -ce . "$ROLLBACK_PATH") || die "rollback artifact is malformed"; else
    current=$(jq -n --arg environment "$ENVIRONMENT" --arg source_sha "$SOURCE_SHA" --argjson selected "$(plan_json '.selected_components')" '{schema:"lwc-306-rollback-v1",environment:$environment,source_sha:$source_sha,selected_components:$selected,handles:{}}')
  fi
  jq -e --arg component "$component" '(.handles[$component]? // null) == null' <<<"$current" >/dev/null || die "rollback handle for $component was already frozen"
  jq --arg component "$component" --argjson value "$value" '.handles[$component] = $value' <<<"$current" > "$ROLLBACK_PATH.tmp"
  mv "$ROLLBACK_PATH.tmp" "$ROLLBACK_PATH"
}

component_image_name() {
  case "$1" in
    auth) printf 'llm-wiki-auth\n' ;;
    bff) printf 'llm-wiki-bff\n' ;;
    worker) printf 'olw-pipeline\n' ;;
    *) die "component $1 has no backend image" ;;
  esac
}
image_for() {
  local component="$1" file image
  file="$ARTIFACT_DIR/images/$component-image-$SOURCE_SHA.txt"; [[ "$ENVIRONMENT" != production ]] || file="$ARTIFACT_DIR/dev-images/$component-image-$SOURCE_SHA.txt"
  [[ -f "$file" ]] || die "immutable $component image receipt is missing"
  image=$(tr -d '[:space:]' < "$file"); validate_image_value "$component" "$image"
  if [[ "$ENVIRONMENT" == production ]]; then
    [[ -s "$ARTIFACT_DIR/dev-images/dev-receipt.json" ]] || die "DEV provenance receipt is missing"
    jq -e --arg component "$component" --arg sha "$SOURCE_SHA" --arg image "$image" \
      '.source.sha == $sha and .source.ref == "develop" and .source.workflow_path == ".github/workflows/deploy-dev.yml" and .source.event == "workflow_dispatch" and .config.environment == "development" and .config.path == "deploy/environments/development.yaml" and (.components | index($component) != null) and .images[$component] == $image' \
      "$ARTIFACT_DIR/dev-images/dev-receipt.json" >/dev/null || die "DEV image provenance receipt does not match $component"
  fi
  printf '%s\n' "$image"
}

validate_image_value() {
  local component="$1" image="$2" registry expected
  registry=$(plan_json '.gcp.artifact_registry'); expected="${registry}/$(component_image_name "$component")"
  [[ "$image" == "$expected@sha256:"???????????????????????????????????????????????????????????????? ]] || die "immutable $component image receipt is not in the exact configured repository"
  [[ "${image##*@sha256:}" =~ ^[0-9a-f]{64}$ ]] || die "immutable $component image receipt digest is invalid"
}

record_dev_receipt() {
  local components images='{}' component image fingerprint run_id run_attempt
  components=$(plan_json '.selected_components'); fingerprint=$(plan_json '.evidence.config_fingerprint')
  need GITHUB_RUN_ID; need GITHUB_RUN_ATTEMPT
  run_id="$GITHUB_RUN_ID"; run_attempt="$GITHUB_RUN_ATTEMPT"
  [[ "$run_id" =~ ^[0-9]+$ && "$run_id" -gt 0 && "$run_attempt" =~ ^[0-9]+$ && "$run_attempt" -gt 0 ]] || die "DEV workflow run identity is invalid"
  while IFS= read -r component; do
    case "$component" in auth|bff|worker)
      image=$(tr -d '[:space:]' < "$ARTIFACT_DIR/images/$component-image-$SOURCE_SHA.txt"); validate_image_value "$component" "$image"
      images=$(jq --arg component "$component" --arg image "$image" '. + {($component):$image}' <<<"$images") ;;
    esac
  done < <(jq -r '.[]' <<<"$components")
  jq -n --arg sha "$SOURCE_SHA" --arg fingerprint "$fingerprint" --argjson run_id "$run_id" --argjson run_attempt "$run_attempt" --argjson components "$components" --argjson images "$images" \
    '{schema:"lwc-306-dev-image-receipt-v1",source:{sha:$sha,ref:"develop",workflow_path:".github/workflows/deploy-dev.yml",event:"workflow_dispatch",run_id:$run_id,run_attempt:$run_attempt},config:{environment:"development",path:"deploy/environments/development.yaml",fingerprint:$fingerprint},components:$components,images:$images}' > "$ARTIFACT_DIR/images/dev-receipt.json"
}

redact_evidence() {
  jq 'walk(if type == "object" then with_entries(if (.key|test("(?i)credential|token|password|secret_value")) then .value = "<redacted>" elif (.key == "value" and (.value|type) == "string") then .value = "<redacted>" else . end) else . end)'
}

iam_binding_is_exact() {
  local policy="$1" role="$2" member="$3"
  jq -e --arg role "$role" --arg member "$member" '
    def broad_member:
      . == "allUsers" or . == "allAuthenticatedUsers" or . == "projectOwners" or . == "projectEditors" or . == "projectViewers" or
      ((type == "string") and (startswith("domain:") or startswith("principalSet://goog/public:")));
    def forbidden_role:
      . == "roles/owner" or . == "roles/editor" or . == "roles/run.admin" or . == "roles/run.developer" or . == "roles/run.jobsAdmin" or
      . == "roles/secretmanager.admin" or . == "roles/secretmanager.secretAccessor" or ((type == "string") and test("^roles/.*(admin|developer)$"));
    def requested_binding:
      .role == $role and (.members|index($member)) != null;
    def exact_requested_binding:
      ($member == "allUsers" and (.members|sort) == ["allUsers"] or
       $member != "allUsers" and all(.members[]; startswith("serviceAccount:")));
    (.bindings | type) == "array" and
    all(.bindings[]; type == "object" and (.role|type) == "string" and (.members|type) == "array" and all(.members[]; type == "string")) and
    ([.bindings[] | select(requested_binding)] | length) == 1 and
    all(.bindings[];
      ([.members[] | select(broad_member)] | length) as $broad |
      if (requested_binding and (has("condition") or (exact_requested_binding|not))) then false
      elif ((.members | index($member)) != null and .role != $role and (.role | forbidden_role)) then false
      elif ($broad > 0 and (.role | forbidden_role)) then false
      else true end
    )' <<<"$policy" >/dev/null
}
preflight_service_account() { local account="$1" project="$2"; gcloud iam service-accounts describe "$account" --project "$project" --format=json --quiet >/dev/null || die "runtime service account is missing or unreadable"; }
preflight_secret() { local secret="$1" account="$2" project="$3" policy; gcloud secrets describe "$secret" --project "$project" --format=json --quiet >/dev/null || die "configured secret is missing or unreadable"; policy=$(gcloud secrets get-iam-policy "$secret" --project "$project" --format=json --quiet) || die "configured secret IAM policy is unreadable"; iam_binding_is_exact "$policy" roles/secretmanager.secretAccessor "serviceAccount:$account" || die "configured secret is not granted to its runtime service account"; }
preflight_public_service() { local service="$1" project="$2" region="$3" policy; policy=$(gcloud run services get-iam-policy "$service" --project "$project" --region "$region" --format=json --quiet) || die "service IAM policy is unreadable"; iam_binding_is_exact "$policy" roles/run.invoker allUsers || die "service IAM policy is not the reviewed public invoker binding"; }
preflight_job_binding() { local job="$1" project="$2" region="$3" role="$4" account="$5" policy; policy=$(gcloud run jobs get-iam-policy "$job" --project "$project" --region "$region" --format=json --quiet) || die "Job IAM policy is unreadable"; iam_binding_is_exact "$policy" "$role" "serviceAccount:$account" || die "Job IAM policy is missing its reviewed runtime binding"; }

service_image_handle() {
  local component="$1" project region service service_json traffic revision revision_json image
  [[ "$component" == auth || "$component" == bff ]] || return 1
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json ".${component}.service_name")
  service_json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet) || return 1
  traffic=$(jq -ce 'if (.status.traffic|type) != "array" or (.status.traffic|length) != 1 or .status.traffic[0].percent != 100 or (.status.traffic[0].revisionName|type) != "string" or (.status.traffic[0].tag? != null) then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json") || return 1
  revision=$(jq -er '.revisionName' <<<"$traffic") || return 1
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet) || return 1
  image=$(jq -er '.status.imageDigest | select(type == "string" and test("@sha256:[0-9a-f]{64}$"))' <<<"$revision_json") || return 1
  validate_image_value "$component" "$image" || return 1
  printf '%s\n' "$image"
}

service_image_readback() {
  local component="$1" expected_image="$2" expected_revision="${3:-}" project region service service_json traffic revision revision_json
  [[ "$component" == auth || "$component" == bff ]] || return 1
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json ".${component}.service_name")
  service_json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet) || return 2
  jq -e 'type == "object" and (.status|type) == "object" and (.status.traffic|type) == "array" and (.status.traffic|length) > 0 and all(.status.traffic[]; type == "object" and (.percent|type) == "number" and (.revisionName|type) == "string" and (.tag? == null or (.tag|type) == "string"))' <<<"$service_json" >/dev/null || return 2
  if ! traffic=$(jq -ce 'if (.status.traffic|length) != 1 or .status.traffic[0].percent != 100 or .status.traffic[0].tag? != null then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json"); then return 1; fi
  revision=$(jq -er '.revisionName' <<<"$traffic") || return 2
  if [[ -n "$expected_revision" && "$revision" != "$expected_revision" ]]; then return 1; fi
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet) || return 2
  jq -e 'def containers: (.spec.containers // .spec.template.spec.containers // null); type == "object" and (.spec|type) == "object" and (.status|type) == "object" and (.status.imageDigest|type) == "string" and (containers|type) == "array" and (containers|length) == 1 and (containers[0].image|type) == "string" and (.status.conditions|type) == "array"' <<<"$revision_json" >/dev/null || return 2
  if ! jq -n -e --arg image "$expected_image" --argjson revision "$revision_json" '
    def containers: ($revision.spec.containers // $revision.spec.template.spec.containers // []);
    ($revision.status.imageDigest == $image) and (containers|type == "array" and length == 1 and .[0].image == $image) and
    any($revision.status.conditions[]?; .type == "Ready" and .status == "True")
  ' >/dev/null; then return 1; fi
  jq -n --arg image "$expected_image" --arg revision "$revision" '{image:$image,revision:$revision,ready:true}'
}

worker_image_handle() {
  local project region job job_json image
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  job_json=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet) || return 1
  image=$(jq -er '(.spec.template.spec.template.spec.containers // .spec.template.spec.containers // []) | if type == "array" and length == 1 and (.[0].image|type) == "string" then .[0].image else error("Worker image is missing") end' <<<"$job_json") || return 1
  validate_image_value worker "$image" || return 1
  printf '%s\n' "$image"
}

worker_image_readback() {
  local expected_image="$1" project region job job_json image
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  job_json=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet) || return 2
  if ! image=$(jq -er '(.spec.template.spec.template.spec.containers // .spec.template.spec.containers // []) | if type == "array" and length == 1 and (.[0].image|type) == "string" then .[0].image else error("Worker image is missing") end' <<<"$job_json"); then return 2; fi
  if [[ "$image" != "$expected_image" ]]; then return 1; fi
  jq -n --arg image "$image" '{image:$image}'
}
