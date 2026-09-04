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
    jq -e --arg component "$component" --arg sha "$SOURCE_SHA" --arg fingerprint "$(plan_json '.evidence.config_fingerprint')" --arg image "$image" \
      '.source.sha == $sha and .source.ref == "develop" and .source.workflow_path == ".github/workflows/deploy-dev.yml" and .source.event == "workflow_dispatch" and .config.environment == "development" and .config.path == "deploy/environments/development.yaml" and .config.fingerprint == $fingerprint and (.components | index($component) != null) and .images[$component] == $image' \
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
      .role == $role and (has("condition")|not) and (.members|index($member)) != null and
      ($member == "allUsers" and (.members|sort) == ["allUsers"] or
       $member != "allUsers" and all(.members[]; startswith("serviceAccount:")));
    (.bindings | type) == "array" and
    all(.bindings[]; type == "object" and (.role|type) == "string" and (.members|type) == "array" and all(.members[]; type == "string")) and
    ([.bindings[] | select(requested_binding)] | length) == 1 and
    all(.bindings[];
      ([.members[] | select(broad_member)] | length) as $broad |
      if (.role == $role and (.members | index($member)) != null and has("condition")) then false
      elif ((.members | index($member)) != null and .role != $role and (.role | forbidden_role)) then false
      elif ($broad > 0 and (.role | forbidden_role)) then false
      else true end
    )' <<<"$policy" >/dev/null
}
preflight_service_account() { local account="$1" project="$2"; gcloud iam service-accounts describe "$account" --project "$project" --format=json --quiet >/dev/null || die "runtime service account is missing or unreadable"; }
preflight_secret() { local secret="$1" account="$2" project="$3" policy; gcloud secrets describe "$secret" --project "$project" --format=json --quiet >/dev/null || die "configured secret is missing or unreadable"; policy=$(gcloud secrets get-iam-policy "$secret" --project "$project" --format=json --quiet) || die "configured secret IAM policy is unreadable"; iam_binding_is_exact "$policy" roles/secretmanager.secretAccessor "serviceAccount:$account" || die "configured secret is not granted to its runtime service account"; }
preflight_public_service() { local service="$1" project="$2" region="$3" policy; policy=$(gcloud run services get-iam-policy "$service" --project "$project" --region "$region" --format=json --quiet) || die "service IAM policy is unreadable"; iam_binding_is_exact "$policy" roles/run.invoker allUsers || die "service IAM policy is not the reviewed public invoker binding"; }
preflight_job_binding() { local job="$1" project="$2" region="$3" role="$4" account="$5" policy; policy=$(gcloud run jobs get-iam-policy "$job" --project "$project" --region "$region" --format=json --quiet) || die "Job IAM policy is unreadable"; iam_binding_is_exact "$policy" "$role" "serviceAccount:$account" || die "Job IAM policy is missing its reviewed runtime binding"; }

service_expected() {
  local component="$1" image="$2" revision="$3"
  jq -n --arg component "$component" --arg image "$image" --arg revision "$revision" --arg source_sha "$SOURCE_SHA" --argjson normalized "$(jq '.normalized' "$PLAN_PATH")" '
    $normalized as $n | ($n[$component]) as $cfg |
    (if $component == "auth" then
      {GCP_PROJECT:$n.gcp.project_id,FIRESTORE_DATABASE_ID:$cfg.firestore_database_id,ALLOWED_ORIGINS:($cfg.allowed_origins|join(",")),ALLOWED_HOSTS:($cfg.allowed_hosts|join(",")),DEV_JWT:"false",LWC_SOURCE_COMMIT:$source_sha}
     else
      {GCP_PROJECT:$n.gcp.project_id,BUCKET:$cfg.bucket,FIRESTORE_DATABASE_ID:$cfg.firestore_database_id,PIPELINE_JOB_URL:$cfg.pipeline_job_url,ALLOWED_ORIGINS:($cfg.allowed_origins|join(",")),AUTH_SERVICE_URL:$cfg.auth_service_url,QUERY_STAGE_CONFIG_PATH:$n.query_config.runtime_path,DEV_JWT:"false",LWC_SOURCE_COMMIT:$source_sha}
     end) as $env |
    (if $component == "auth" then {JWT_SECRET:{secret:$cfg.secret_references.jwt,version:"latest",plaintext:false}} else {JWT_SECRET:{secret:$cfg.secret_references.jwt,version:"latest",plaintext:false},DEEPSEEK_API_KEY:{secret:$cfg.secret_references.deepseek_api_key,version:"latest",plaintext:false}} end) as $secrets |
    {component:$component,service_name:$cfg.service_name,revision:$revision,revision_name:$revision,image:$image,service_account:$cfg.runtime_service_account,runtime_service_account:$cfg.runtime_service_account,env:$env,secret_references:$secrets,container:{command:[],args:[],resources:{},volume_mounts:[],working_dir:null,ports:[],execution_controls:{container_concurrency:null,timeout_seconds:null,execution_environment:null,cpu_boost:null}},network:{network:$cfg.network,subnet:$cfg.subnet,vpc_egress:$cfg.vpc_egress,ingress:$cfg.ingress,max_instances:$cfg.max_instances},traffic:[{revision_name:$revision,percent:100,tag:null}],component_config:(if $component == "auth" then {firestore_database_id:$cfg.firestore_database_id,allowed_origins:$cfg.allowed_origins,allowed_hosts:$cfg.allowed_hosts} else {bucket:$cfg.bucket,firestore_database_id:$cfg.firestore_database_id,pipeline_job_url:$cfg.pipeline_job_url,allowed_origins:$cfg.allowed_origins,auth_service_url:$cfg.auth_service_url,query_config:{runtime_path:$n.query_config.runtime_path}} end)}'
}

normalize_service_readback() {
  local component="$1" service_json="$2" revision_json="$3" revision="$4" image="$5" allow_legacy="${6:-0}"
  strict_json <<<"$service_json" || return 1
  strict_json <<<"$revision_json" || return 1
  local allowed secret_names legacy_names='[]'
  case "$component" in
    auth) allowed='["GCP_PROJECT","FIRESTORE_DATABASE_ID","ALLOWED_ORIGINS","ALLOWED_HOSTS","DEV_JWT","LWC_SOURCE_COMMIT"]'; secret_names='["JWT_SECRET"]' ;;
    bff) allowed='["GCP_PROJECT","BUCKET","FIRESTORE_DATABASE_ID","PIPELINE_JOB_URL","ALLOWED_ORIGINS","AUTH_SERVICE_URL","QUERY_STAGE_CONFIG_PATH","DEV_JWT","LWC_SOURCE_COMMIT"]'; secret_names='["JWT_SECRET","DEEPSEEK_API_KEY"]' ;;
    *) return 1 ;;
  esac
  [[ "$allow_legacy" == 1 && "$component" == bff ]] && legacy_names='["USER_ID","PROJECT_ID"]'
  jq -nce --arg component "$component" --arg revision "$revision" --arg image "$image" --argjson service "$service_json" --argjson revision_json "$revision_json" --argjson allowed "$allowed" --argjson secret_names "$secret_names" --argjson legacy_names "$legacy_names" '
    def template: ($service.spec.template // {});
    def template_spec: (template.spec // {});
    def revision_spec: ($revision_json.spec // {});
    def service_containers: (template_spec.containers // []);
    def revision_containers: (revision_spec.containers // revision_spec.template.spec.containers // []);
    def exact_container($containers): if ($containers|type) != "array" or ($containers|length) != 1 or ($containers[0]|type) != "object" then error("service and revision must each contain exactly one container") else $containers[0] end;
    def require_object($value;$allowed;$label): if ($value|type) != "object" then error($label + " must be an object") elif ((($value|keys) - $allowed)|length) != 0 then error($label + " contains an unallowlisted field") else $value end;
    def strings_or_empty($value;$label): if $value == null then [] elif ($value|type) != "array" or any($value[]; type != "string") then error($label + " must be an array of strings") else $value end;
    def secret_ref($entry):
      if ($entry|has("value")) then error("secret environment entry contains a plaintext value")
      elif (($entry|has("valueSource")) and ($entry|has("valueFrom"))) then error("secret environment entry has duplicate sources")
      else (($entry.valueSource.secretKeyRef // $entry.valueFrom.secretKeyRef // null) as $ref |
        if ($ref|type) != "object" or ($ref.secret // $ref.name | type) != "string" or ($ref.version // $ref.key | type) != "string" then error("secret environment reference is malformed") else {secret:($ref.secret // $ref.name),version:($ref.version // $ref.key),plaintext:false} end)
      end;
    def env_shape($entries):
      if ($entries|type) != "array" then error("environment must be an array")
      elif any($entries[]; (type != "object") or (.name|type) != "string") then error("environment entry is malformed")
      elif ([ $entries[].name ] | length) != ([ $entries[].name ] | unique | length) then error("environment contains duplicate names")
      elif ([ $entries[] | .name as $name | select((($allowed + $secret_names + $legacy_names)|index($name)) == null) | $name ] | if length > 0 then error("environment contains unknown name(s): " + join(",")) else false end) then error("environment contains an unknown name")
      else
        ($entries | map(. as $entry | if ($secret_names|index($entry.name)) != null then {name:$entry.name,secret:secret_ref($entry)} elif (($legacy_names|index($entry.name)) != null) then (if (($entry|keys|sort) != ["name","value"] or ($entry.value|type) != "string") then error("legacy environment entry is malformed") else {name:$entry.name,value:$entry.value} end) elif (($entry|keys|sort) != ["name","value"] or ($entry.value|type) != "string") then error("plain environment entry is malformed") else {name:$entry.name,value:$entry.value} end)) as $normalized |
        {values:($normalized|map(select(has("value"))|{key:.name,value:.value})|from_entries),secrets:($normalized|map(select(has("secret"))|{key:.name,value:.secret})|from_entries),legacy:($normalized|map(. as $entry | select(($legacy_names|index($entry.name)) != null)|{key:.name,value:.value})|from_entries)}
      end;
    def container_shape($container):
      require_object($container;["image","env","command","args","resources","volumeMounts","workingDir","ports"];"service container") as $checked |
      (env_shape($checked.env // [])) as $env |
      (strings_or_empty($checked.command // null;"container command")) as $command |
      (strings_or_empty($checked.args // null;"container args")) as $args |
      (if (($checked.resources // {})|type) != "object" then error("container resources are malformed") else ($checked.resources // {}) end) as $resources |
      (if (($checked.volumeMounts // [])|type) != "array" then error("container volume mounts are malformed") else ($checked.volumeMounts // []) end) as $volume_mounts |
      (if (($checked.ports // [])|type) != "array" then error("container ports are malformed") else ($checked.ports // []) end) as $ports |
      {env:$env,command:$command,args:$args,resources:$resources,volume_mounts:$volume_mounts,working_dir:($checked.workingDir // null),ports:$ports};
    def annotations: (($service.metadata.annotations // {}) + (template.metadata.annotations // {}) + (template_spec.metadata.annotations // {}) + ($revision_json.metadata.annotations // {}));
    def interfaces: (annotations["run.googleapis.com/network-interfaces"] // "[]") | fromjson | if type != "array" or length != 1 or ((.[0]|type) != "object") or ((.[0]|keys|sort) != ["network","subnetwork"]) or ((.[0].network|type) != "string") or ((.[0].subnetwork|type) != "string") then error("network interface shape is not exact") else . end;
    def max_instances: (annotations["autoscaling.knative.dev/maxScale"] // annotations["run.googleapis.com/maxScale"] // $service.spec.template.scaling.maxInstanceCount // $service.spec.template.spec.maxInstanceCount // null) | if . == null then null elif ((type == "number") and ((floor) == .)) then . elif (type == "string" and test("^[0-9]+$")) then tonumber else error("max instances is malformed") end;
    def traffic: ($service.status.traffic // error("service traffic is missing")) | if type != "array" or length != 1 then error("service traffic is not exact") else map(if type != "object" or (.revisionName|type) != "string" or (.percent|type) != "number" or ((.percent|floor) != .percent) or .percent < 0 or .percent > 100 or (.tag? != null and (.tag|type) != "string") then error("service traffic entry is malformed") else {revision_name:.revisionName,percent:.percent,tag:(.tag // null)} end) end;
    (exact_container(service_containers)) as $service_container |
    (exact_container(revision_containers)) as $revision_container |
    (container_shape($service_container)) as $service_shape |
    (container_shape($revision_container)) as $revision_shape |
    if $service_shape != $revision_shape then error("service and revision container shapes differ") else . end |
    (revision_spec.serviceAccountName // revision_spec.template.spec.serviceAccountName // template_spec.serviceAccountName // null) as $account |
    ($revision_json.metadata.name // null) as $revision_name |
    ($service.metadata.name // null) as $service_name |
    (if ($service_name|type) != "string" or $service_name == "" or ($revision_name|type) != "string" or $revision_name == "" or $revision_name != $revision then error("service or revision identity is malformed") else . end) |
    (if ($account|type) != "string" or $account == "" then error("runtime service account is missing") else . end) |
    ($service_shape.env.values) as $values | ($service_shape.env.secrets) as $secrets |
    (if ($legacy_names|length) > 0 and (($service_shape.env.legacy|keys|sort) != ($legacy_names|sort)) then error("legacy environment is incomplete") else . end) |
    (if $component == "auth" then {GCP_PROJECT:$values.GCP_PROJECT,FIRESTORE_DATABASE_ID:$values.FIRESTORE_DATABASE_ID,ALLOWED_ORIGINS:$values.ALLOWED_ORIGINS,ALLOWED_HOSTS:$values.ALLOWED_HOSTS,DEV_JWT:$values.DEV_JWT,LWC_SOURCE_COMMIT:$values.LWC_SOURCE_COMMIT} else {GCP_PROJECT:$values.GCP_PROJECT,BUCKET:$values.BUCKET,FIRESTORE_DATABASE_ID:$values.FIRESTORE_DATABASE_ID,PIPELINE_JOB_URL:$values.PIPELINE_JOB_URL,ALLOWED_ORIGINS:$values.ALLOWED_ORIGINS,AUTH_SERVICE_URL:$values.AUTH_SERVICE_URL,QUERY_STAGE_CONFIG_PATH:$values.QUERY_STAGE_CONFIG_PATH,DEV_JWT:$values.DEV_JWT,LWC_SOURCE_COMMIT:$values.LWC_SOURCE_COMMIT} end) as $env |
    (if $component == "auth" then {JWT_SECRET:($secrets.JWT_SECRET // {secret:null,version:null,plaintext:false})} else {JWT_SECRET:($secrets.JWT_SECRET // {secret:null,version:null,plaintext:false}),DEEPSEEK_API_KEY:($secrets.DEEPSEEK_API_KEY // {secret:null,version:null,plaintext:false})} end) as $secret_refs |
    (interfaces[0]) as $interface |
    ({component:$component,service_name:$service_name,revision:$revision,revision_name:$revision_name,image:($revision_json.status.imageDigest // ($revision_container.image // null)),service_account:$account,runtime_service_account:$account,env:$env,secret_references:$secret_refs,container:{command:$revision_shape.command,args:$revision_shape.args,resources:$revision_shape.resources,volume_mounts:$revision_shape.volume_mounts,working_dir:$revision_shape.working_dir,ports:$revision_shape.ports,execution_controls:{container_concurrency:($service.spec.template.containerConcurrency // $service.spec.containerConcurrency // null),timeout_seconds:(revision_spec.timeoutSeconds // revision_spec.template.spec.timeoutSeconds // null),execution_environment:(annotations["run.googleapis.com/execution-environment"] // null),cpu_boost:(annotations["run.googleapis.com/startup-cpu-boost"] // null)}},network:{network:$interface.network,subnet:($interface.subnetwork // $interface.subnet // null),vpc_egress:(annotations["run.googleapis.com/vpc-access-egress"] // $service.spec.template.vpcAccess.egress // null),ingress:($service.metadata.annotations["run.googleapis.com/ingress"] // $service.spec.ingress // "all"),max_instances:max_instances},traffic:traffic,component_config:(if $component == "auth" then {firestore_database_id:$values.FIRESTORE_DATABASE_ID,allowed_origins:($values.ALLOWED_ORIGINS|if . == null then null else split(",") end),allowed_hosts:($values.ALLOWED_HOSTS|if . == null then null else split(",") end)} else {bucket:$values.BUCKET,firestore_database_id:$values.FIRESTORE_DATABASE_ID,pipeline_job_url:$values.PIPELINE_JOB_URL,allowed_origins:($values.ALLOWED_ORIGINS|if . == null then null else split(",") end),auth_service_url:$values.AUTH_SERVICE_URL,query_config:{runtime_path:$values.QUERY_STAGE_CONFIG_PATH}} end)} + (if ($legacy_names|length) > 0 then {legacy_preserved:($service_shape.env.legacy|to_entries|map({name:.key,value:.value})|sort_by(.name))} else {} end))'
}

service_frozen_readback() {
  local component="$1" allow_legacy="${2:-0}" project region service service_json traffic revision revision_json image observed expected
  [[ "$component" == auth || "$component" == bff ]] || return 1
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json ".${component}.service_name")
  service_json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet) || return 1
  traffic=$(jq -ce 'if (.status.traffic|type) != "array" or (.status.traffic|length) != 1 or .status.traffic[0].percent != 100 or (.status.traffic[0].revisionName|type) != "string" or (.status.traffic[0].tag? != null) then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json") || return 1
  revision=$(jq -er '.revisionName' <<<"$traffic") || return 1
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet) || return 1
  image=$(jq -er '.status.imageDigest | select(type == "string" and test("@sha256:"))' <<<"$revision_json") || return 1
  observed=$(normalize_service_readback "$component" "$service_json" "$revision_json" "$revision" "$image" "$allow_legacy") || return 1
  expected=$(jq -ce --arg component "$component" '.handles[$component].readback' "$ROLLBACK_PATH") || return 1
  jq -n -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null || return 1
  printf '%s\n' "$observed"
}

normalize_worker_definition() {
  jq -ce '
    def safe_env: map(if (.valueSource.secretKeyRef? // .valueFrom.secretKeyRef?) then (if has("value") or (has("valueSource") and has("valueFrom")) then error("malformed Job secret environment entry") else ((.valueSource.secretKeyRef // .valueFrom.secretKeyRef) as $ref | {name,valueSource:{secretKeyRef:{secret:($ref.secret // $ref.name),version:($ref.version // $ref.key // "latest")}}}) end) elif ((.name // "")|test("(?i)(secret|token|password|api[_-]?key)")) and has("value") then error("plaintext sensitive Job environment value") else . end);
    # Provider output metadata is excluded from behavior comparison; generation and etag are captured separately.
    del(.status,.metadata.uid,.metadata.resourceVersion,.metadata.generation,.metadata.etag,.metadata.creationTimestamp,.metadata.updateTime,.metadata.selfLink,.metadata.managedFields) |
    .spec.template.spec.template.spec.containers |= map(.env |= safe_env) | {apiVersion,kind,metadata,spec}'
}

worker_provider_state() {
  local job_json="$1"
  strict_json <<<"$job_json" || return 1
  jq -nce --argjson job "$job_json" '
    ($job.metadata // {}) as $metadata |
    ($metadata.generation // $job.generation // null) as $generation |
    ($metadata.etag // $job.etag // null) as $etag |
    if $generation != null and (($generation|type) != "number" or ($generation|floor) != $generation or $generation < 1) then error("Job generation is malformed")
    elif $etag != null and (($etag|type) != "string" or $etag == "") then error("Job etag is malformed")
    else {generation:$generation,etag:$etag} end'
}

worker_expected() {
  local image="$1"
  jq -n --arg image "$image" --argjson normalized "$(jq '.normalized' "$PLAN_PATH")" '$normalized as $n | {image:$image,service_account:$n.worker.runtime_service_account,env:{BUCKET:$n.worker.bucket,PIPELINE_JOB_NAME:$n.worker.job_name,PIPELINE_JOB_LOCATION:$n.worker.location},secret_references:{DEEPSEEK_API_KEY:{secret:$n.worker.secret_references.deepseek_api_key,version:"latest",plaintext:false}},command:[],args:$n.worker.args,resources:{},execution_controls:{task_count:null,parallelism:null,max_retries:null,timeout_seconds:null,execution_environment:null,vpc_access:null,node_selector:null,encryption_key:null},volumes:[],volume_mounts:[],working_dir:null,ports:[]}'
}
normalize_worker_readback() {
  local definition="$1"
  jq -nce --argjson definition "$definition" '
    def require_object($value;$allowed;$label):
      if ($value|type) != "object" then error($label + " must be an object")
      elif ((($value|keys) - $allowed)|length) != 0 then error($label + " contains an unallowlisted field")
      else $value end;
    def strings_or_empty($value;$label):
      if $value == null then [] elif ($value|type) != "array" or any($value[]; type != "string") then error($label + " must be an array of strings") else $value end;
    def env_shape($entries):
      if ($entries|type) != "array" then error("Job environment must be an array")
      elif any($entries[]; type != "object" or (.name|type) != "string") then error("Job environment entry is malformed")
      elif ([ $entries[].name ] | length) != ([ $entries[].name ] | unique | length) then error("Job environment contains duplicate names")
      elif ([ $entries[] | select(. as $entry | (["BUCKET","PIPELINE_JOB_NAME","PIPELINE_JOB_LOCATION","DEEPSEEK_API_KEY"] | index($entry.name)) == null) ] | length) != 0 then error("Job environment contains an unknown name")
      else
        ($entries | map(. as $entry |
          if $entry.name == "DEEPSEEK_API_KEY" then
            if (($entry|keys|sort) != ["name","valueSource"] or ($entry.valueSource|type) != "object" or (($entry.valueSource|keys) != ["secretKeyRef"]) or ($entry.valueSource.secretKeyRef|type) != "object" or (($entry.valueSource.secretKeyRef|keys|sort) != ["secret","version"]) or ($entry.valueSource.secretKeyRef.secret|type) != "string" or ($entry.valueSource.secretKeyRef.version|type) != "string") then error("Job secret environment reference is malformed")
            else {name:$entry.name,secret:{secret:$entry.valueSource.secretKeyRef.secret,version:$entry.valueSource.secretKeyRef.version,plaintext:false}} end
          elif (($entry|keys|sort) != ["name","value"] or ($entry.value|type) != "string") then error("Job plain environment entry is malformed")
          else {name:$entry.name,value:$entry.value} end)) as $normalized |
        {values:($normalized|map(select(has("value"))|{key:.name,value:.value})|from_entries),secrets:($normalized|map(select(has("secret"))|{key:.name,value:.secret})|from_entries)}
      end;
    ($definition.spec // error("Job spec is missing")) as $job_spec |
    ($job_spec.template // error("Job template is missing")) as $job_template |
    ($job_template.spec // error("Job template spec is missing")) as $template_spec |
    ($template_spec.template // error("Job container template is missing")) as $container_template |
    ($container_template.spec // error("Job container spec is missing")) as $spec |
    require_object($job_template;["spec","taskCount","parallelism"];"Job template") as $job_template_checked |
    require_object($container_template;["spec","maxRetries","timeoutSeconds","executionEnvironment","vpcAccess","nodeSelector","encryptionKey"];"Job container template") as $container_template_checked |
    require_object($spec;["serviceAccountName","containers","volumes"];"Job container spec") as $spec_checked |
    ($spec_checked.containers // error("Job containers are missing")) as $containers |
    (if ($containers|type) != "array" or ($containers|length) != 1 then error("Worker container shape is not exact") else $containers[0] end) as $container |
    require_object($container;["image","env","command","args","resources","volumeMounts","workingDir","ports"];"Worker container") as $container_checked |
    (env_shape($container_checked.env // [])) as $env |
    (strings_or_empty($container_checked.command // null;"Job command")) as $command |
    (strings_or_empty($container_checked.args // null;"Job args")) as $args |
    (if (($container_checked.resources // {})|type) != "object" then error("Job resources must be an object") else ($container_checked.resources // {}) end) as $resources |
    (if ($spec_checked.volumes // [])|type != "array" then error("Job volumes must be an array") else ($spec_checked.volumes // []) end) as $volumes |
    (if ($container_checked.volumeMounts // [])|type != "array" then error("Job volume mounts must be an array") else ($container_checked.volumeMounts // []) end) as $volume_mounts |
    (if ($container_checked.ports // [])|type != "array" then error("Job ports must be an array") else ($container_checked.ports // []) end) as $ports |
    {image:($container_checked.image // null),service_account:($spec_checked.serviceAccountName // null),env:{BUCKET:$env.values.BUCKET,PIPELINE_JOB_NAME:$env.values.PIPELINE_JOB_NAME,PIPELINE_JOB_LOCATION:$env.values.PIPELINE_JOB_LOCATION},secret_references:{DEEPSEEK_API_KEY:($env.secrets.DEEPSEEK_API_KEY // {secret:null,version:null,plaintext:false})},command:$command,args:$args,resources:$resources,execution_controls:{task_count:($job_template_checked.taskCount // null),parallelism:($job_template_checked.parallelism // null),max_retries:($container_template_checked.maxRetries // null),timeout_seconds:($container_template_checked.timeoutSeconds // null),execution_environment:($container_template_checked.executionEnvironment // null),vpc_access:($container_template_checked.vpcAccess // null),node_selector:($container_template_checked.nodeSelector // null),encryption_key:($container_template_checked.encryptionKey // null)},volumes:$volumes,volume_mounts:$volume_mounts,working_dir:($container_checked.workingDir // null),ports:$ports}'
}
verify_worker_definition() { local observed="$1" expected="$2"; [[ "$(jq -n --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed')" == true ]]; }
