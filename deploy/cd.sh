#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
source "$ROOT/deploy/components/common.sh"

component_script() {
  case "$1" in
    auth|bff|worker|frontend) printf '%s/deploy/components/%s.sh\n' "$ROOT" "$1" ;;
    *) die "unexpected component $1" ;;
  esac
}
run_component() { bash "$(component_script "$1")" "$2"; }
selected_components() { plan_json '.selected_components[]'; }

consume_dev_images() {
  local runs candidates run id artifacts artifact
  need GH_TOKEN; need GITHUB_REPOSITORY; need ARTIFACT_DIR; need SOURCE_SHA
  runs=$(gh_paginated_array "repos/$GITHUB_REPOSITORY/actions/workflows/deploy-dev.yml/runs?event=workflow_dispatch&status=completed&head_sha=${SOURCE_SHA}&branch=develop" workflow_runs) || die "DEV receipt run list is malformed or partial"
  jq -e 'map(.id) as $ids | ($ids|length) == ($ids|unique|length) and all(.[]; type == "object")' <<<"$runs" >/dev/null || die "DEV receipt run list contains duplicate or malformed entries"
  jq -e 'all(.[]; (.id|type == "number" and floor == . and . > 0) and (.run_attempt|type == "number" and floor == . and . > 0) and (.path|type == "string") and (.event|type == "string") and (.head_branch|type == "string") and (.head_sha|type == "string") and (.status|type == "string") and (.conclusion|type == "string"))' <<<"$runs" >/dev/null || die "DEV receipt run list contains malformed records"
  candidates=$(jq -c --arg sha "$SOURCE_SHA" '[.[] | select(.path == ".github/workflows/deploy-dev.yml" and .event == "workflow_dispatch" and .head_branch == "develop" and .head_sha == $sha and .status == "completed" and .conclusion == "success" and (.run_attempt|type) == "number" and .run_attempt > 0)]' <<<"$runs")
  [[ "$(jq -er 'length' <<<"$candidates")" == 1 ]] || die "successful DEV receipt run is missing or ambiguous"
  run=$(jq -c '.[0]' <<<"$candidates"); id=$(jq -er '.id' <<<"$run")
  artifacts=$(gh_paginated_array "repos/$GITHUB_REPOSITORY/actions/runs/$id/artifacts" artifacts) || die "DEV receipt artifact list is malformed or partial"
  artifact=$(jq -ce --arg name "cd-images-$SOURCE_SHA" --argjson run_id "$id" '[.[] | select(.name == $name and .expired != true and (.size_in_bytes|type) == "number" and .size_in_bytes > 0 and ((.workflow_run.id // $run_id) == $run_id))] | if length == 1 then .[0] else error("DEV receipt artifact is missing or ambiguous") end' <<<"$artifacts") || die "DEV receipt artifact is missing, expired, or ambiguous"
  jq -e '.id|type == "number" and floor == . and . > 0' <<<"$artifact" >/dev/null || die "DEV receipt artifact identity is invalid"
  jq -e '.digest|type == "string" and test("^sha256:[0-9a-f]{64}$")' <<<"$artifact" >/dev/null || die "DEV receipt artifact digest is missing"
  mkdir -p "$ARTIFACT_DIR/dev-images"
  gh run download "$id" --repo "$GITHUB_REPOSITORY" --name "cd-images-$SOURCE_SHA" --dir "$ARTIFACT_DIR/dev-images" || die "DEV receipt artifact download failed"
  [[ -s "$ARTIFACT_DIR/dev-images/dev-receipt.json" ]] || die "DEV receipt content is missing"
  jq -e --arg sha "$SOURCE_SHA" --arg fingerprint "$(plan_json '.evidence.config_fingerprint')" --argjson run_id "$id" --argjson run_attempt "$(jq -er '.run_attempt' <<<"$run")" --argjson selected "$(plan_json '.selected_components')" '.schema == "lwc-306-dev-image-receipt-v1" and .source.sha == $sha and .source.ref == "develop" and .source.workflow_path == ".github/workflows/deploy-dev.yml" and .source.event == "workflow_dispatch" and .source.run_id == $run_id and .source.run_attempt == $run_attempt and .config.environment == "development" and .config.path == "deploy/environments/development.yaml" and .config.fingerprint == $fingerprint and .components == $selected and (.images|type) == "object"' "$ARTIFACT_DIR/dev-images/dev-receipt.json" >/dev/null || die "DEV receipt provenance does not match the selected production bundle"
  jq -n --argjson id "$(jq -er '.id' <<<"$artifact")" --arg digest "$(jq -er '.digest' <<<"$artifact")" '{schema:"lwc-306-dev-artifact-v1",id:$id,digest:$digest,run_id:'"$id"'}' > "$ARTIFACT_DIR/dev-images/dev-artifact.json"
}

preflight_shared() {
  need PLAN_PATH; [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"; revalidate_ci
  local project region registry repo
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); registry=$(plan_json '.gcp.artifact_registry'); repo=$(basename "$registry")
  gcloud artifacts repositories describe "$repo" --location "$region" --project "$project" --format=json --quiet >/dev/null || die "Artifact Registry repository is missing or unreadable"
}
preflight() { preflight_shared; local component; while IFS= read -r component; do run_component "$component" preflight; done < <(selected_components); }
freeze() { need PLAN_PATH; need ROLLBACK_PATH; [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"; local component; while IFS= read -r component; do run_component "$component" freeze; done < <(selected_components); }
revalidate_before_provider() { [[ "${ROLLBACK_UPLOADED:-}" == 1 ]] || die "durable rollback artifact was not uploaded"; revalidate_ci; }

mutate() {
  need PLAN_PATH; need JOURNAL_PATH; need ARTIFACT_DIR; [[ -s "$PLAN_PATH" ]] || die "validated plan is unavailable"
  journal_init; mkdir -p "$ARTIFACT_DIR/images"; set_mutation_status selected not_started
  if [[ "$ENVIRONMENT" == production ]] && { has_component auth || has_component bff || has_component worker; }; then consume_dev_images; fi
  local component
  while IFS= read -r component; do run_component "$component" mutate; done < <(selected_components)
  if [[ "$ENVIRONMENT" == development ]] && { has_component auth || has_component bff || has_component worker; }; then record_dev_receipt; fi
}

initialize() {
  need PLAN_PATH
  need JOURNAL_PATH
  need ROLLBACK_RESULT_PATH
  journal_init
  mkdir -p "$(dirname "$ROLLBACK_RESULT_PATH")"
  if [[ ! -s "$ROLLBACK_RESULT_PATH" ]]; then
    jq -n '{schema:"lwc-306-rollback-result-v1",result:"not_needed",verified:false,attempted:[],components:[]}' > "$ROLLBACK_RESULT_PATH"
  fi
}

reconcile() {
  need PLAN_PATH
  need JOURNAL_PATH
  need ARTIFACT_DIR
  journal_init
  local component aggregate_status=0
  while IFS= read -r component; do
    run_component "$component" reconcile || :
  done < <(selected_components)
  aggregate_reconcile || aggregate_status=$?
  return "$aggregate_status"
}

aggregate_reconcile() {
  need PLAN_PATH
  need JOURNAL_PATH
  need ARTIFACT_DIR
  journal_init
  local selected component result='success' verified=true rows='[]' file
  selected=$(plan_json '.selected_components')
  while IFS= read -r component; do
    file=$(component_result_path "$component")
    if [[ ! -s "$file" ]] || ! strict_json < "$file" || ! jq -e --arg component "$component" '.component == $component and (.result|type) == "string"' "$file" >/dev/null; then
      result=unknown; verified=false
      rows=$(jq --arg component "$component" '. + [{component:$component,result:"unknown",verified:false,reason:"component evidence missing or malformed"}]' <<<"$rows")
      continue
    fi
    rows=$(jq --argjson item "$(cat "$file")" '. + [$item + {verified:($item.result == "success")}]' <<<"$rows")
    if [[ "$(jq -r '.result' "$file")" != success ]]; then result=unknown; verified=false; fi
  done < <(jq -r '.[]' <<<"$selected")
  jq -n --arg result "$result" --argjson verified "$verified" --argjson provider_readback "$verified" --argjson components "$rows" '{schema:"lwc-306-readback-v1",result:$result,verified:$verified,provider_readback:$provider_readback,components:$components}' > "$EVIDENCE_PATH"
  [[ "$result" == success ]]
}

rollback() {
  need PLAN_PATH
  need ROLLBACK_PATH
  need JOURNAL_PATH
  need ROLLBACK_RESULT_PATH
  if [[ -s "$JOURNAL_PATH" ]] && ! journal_validate; then
    mkdir -p "$(dirname "$ROLLBACK_RESULT_PATH")"
    jq -n '{schema:"lwc-306-rollback-result-v1",result:"unknown",verified:false,attempted:[],components:[],reason:"mutation journal is malformed"}' > "$ROLLBACK_RESULT_PATH"
    return 1
  fi
  journal_init
  local possible component rollback_result='success' verified=true attempted='[]' rows='[]' current rollback_file
  possible=$(journal_possible_components)
  if [[ -z "$possible" ]]; then
    if journal_has_rollback_terminal; then
      jq -n '{schema:"lwc-306-rollback-result-v1",result:"failed",verified:false,attempted:[],components:[],reason:"rollback already processed"}' > "$ROLLBACK_RESULT_PATH"
      return 1
    fi
    jq -n '{schema:"lwc-306-rollback-result-v1",result:"not_needed",verified:false,attempted:[],components:[]}' > "$ROLLBACK_RESULT_PATH"
    return 0
  fi
  while IFS= read -r component; do
    [[ -n "$component" ]] || continue
    journal_transition "$component" rollback_pending
    attempted=$(jq --arg component "$component" '. + [$component]' <<<"$attempted")
    rollback_file=$(rollback_result_path "$component")
    if run_component "$component" rollback; then
      if [[ -s "$rollback_file" ]] && strict_json < "$rollback_file" && jq -e '.result == "success"' "$rollback_file" >/dev/null; then
        current=success
        journal_transition "$component" rollback_accepted
      else
        current=unknown
        rollback_result=unknown; verified=false
        journal_transition "$component" rollback_unknown
      fi
    else
      current=$(jq -r '.result // "unknown"' "$rollback_file" 2>/dev/null || printf 'unknown')
      case "$current" in
        success) journal_transition "$component" rollback_accepted ;;
        failed) journal_transition "$component" rollback_failed; rollback_result=failed; verified=false ;;
        *) journal_transition "$component" rollback_unknown; rollback_result=unknown; verified=false ;;
      esac
    fi
    if [[ ! -s "$rollback_file" ]] || ! strict_json < "$rollback_file"; then
      rollback_result=unknown; verified=false
      current=unknown
    fi
    rows=$(jq --arg component "$component" --arg result "$current" --argjson verified "$([[ "$current" == success ]] && printf true || printf false)" '. + [{component:$component,result:$result,verified:$verified}]' <<<"$rows")
  done <<<"$possible"
  if [[ "$rollback_result" == success && "$verified" == true ]]; then :; elif [[ "$rollback_result" == failed ]]; then :; else rollback_result=unknown; fi
  jq -n --arg result "$rollback_result" --argjson verified "$verified" --argjson attempted "$attempted" --argjson components "$rows" '{schema:"lwc-306-rollback-result-v1",result:$result,verified:$verified,attempted:$attempted,components:$components}' > "$ROLLBACK_RESULT_PATH"
  [[ "$rollback_result" == success ]]
}

evidence() {
  need PLAN_PATH
  need JOURNAL_PATH
  need EVIDENCE_PATH
  need FINAL_EVIDENCE_PATH
  local possible_count journal rollback_result aggregate render_failed=0 final_result final_verified mutation_components mutation_count rollback_attempted rollback_result_value rollback_verified partial unknown next_action provider_readback
  journal=$(cat "$JOURNAL_PATH" 2>/dev/null || printf '{}')
  if ! strict_json <<<"$journal" || ! journal_validate; then
    render_failed=1; possible_count=1; mutation_components='[]'; mutation_count=1
  else
    possible_count=$(journal_possible_count)
    mutation_components=$(jq -c '[.order[] as $component | select(.components[$component].state == "accepted" or .components[$component].state == "pending" or .components[$component].state == "unknown") | $component]' <<<"$journal")
    mutation_count=$(jq -er 'length' <<<"$mutation_components")
  fi
  aggregate=$(cat "$EVIDENCE_PATH" 2>/dev/null || printf '{}')
  rollback_result=$(cat "$ROLLBACK_RESULT_PATH" 2>/dev/null || printf '{}')
  if ! strict_json <<<"$aggregate" || ! jq -e '(.schema == "lwc-306-readback-v1") and (.components|type) == "array"' <<<"$aggregate" >/dev/null; then render_failed=1; aggregate='{"schema":"lwc-306-readback-v1","result":"unknown","verified":false,"components":[]}' ; fi
  if ! strict_json <<<"$rollback_result" || ! jq -e '(.schema == "lwc-306-rollback-result-v1") and (.result|type) == "string" and (.verified|type) == "boolean" and (.components|type) == "array"' <<<"$rollback_result" >/dev/null; then
    [[ "$possible_count" -gt 0 ]] && render_failed=1
    rollback_result='{"schema":"lwc-306-rollback-result-v1","result":"unknown","verified":false,"attempted":[],"components":[]}'
  fi
  final_result=$(jq -r '.result' <<<"$aggregate")
  final_verified=$(jq -r '.verified' <<<"$aggregate")
  rollback_result_value=$(jq -r '.result' <<<"$rollback_result")
  rollback_verified=$(jq -r '(.result == "success" and .verified == true)' <<<"$rollback_result")
  rollback_attempted=$(jq -c '.attempted // []' <<<"$rollback_result")
  if [[ "$possible_count" -eq 0 ]]; then
    if [[ "$final_result" == success && "$final_verified" == true ]]; then
      final_result=success
      final_verified=true
    else
      final_result=failed
      final_verified=false
    fi
  elif [[ "$render_failed" -eq 1 ]]; then
    final_result=rollback_unknown
    final_verified=false
  elif [[ "$rollback_result_value" == success && "$rollback_verified" == true ]]; then
    final_result=rolled_back
    final_verified=true
  elif [[ "$rollback_result_value" == failed ]]; then
    final_result=rollback_failed
    final_verified=false
  elif [[ "$final_result" != success || "$final_verified" != true ]]; then
    final_result=rollback_unknown
    final_verified=false
  fi
  provider_readback=$(jq -r '(.result == "success" and .verified == true)' <<<"$aggregate")
  partial=false; unknown=false
  [[ "$possible_count" -gt 0 && "$final_result" != success ]] && partial=true
  [[ "$final_result" == rollback_unknown || "$rollback_result_value" == unknown ]] && unknown=true
  next_action='none'
  [[ "$final_result" == failed || "$final_result" == rollback_failed || "$final_result" == rollback_unknown ]] && next_action='operator review required; no automatic provider retry'
  jq -n --arg environment "$ENVIRONMENT" --arg source_sha "$SOURCE_SHA" --arg result "$final_result" --argjson verified "$final_verified" --argjson rollback_verified "$rollback_verified" --argjson journal "$journal" --argjson aggregate "$aggregate" --argjson rollback "$rollback_result" \
    --argjson mutation_count "$mutation_count" --argjson mutation_components "$mutation_components" --argjson rollback_attempted "$rollback_attempted" --arg rollback_result "$rollback_result_value" --argjson partial "$partial" --argjson unknown "$unknown" --arg next_action "$next_action" --argjson provider_readback "$provider_readback" \
    '{schema:"lwc-306-evidence-v1",environment:$environment,source_sha:$source_sha,result:$result,verified:$verified,provider_readback:$provider_readback,mutation_count:$mutation_count,mutation_components:$mutation_components,partial:$partial,unknown:$unknown,next_action:$next_action,rollback_attempted:$rollback_attempted,rollback_result:$rollback_result,rollback_verified:$rollback_verified,journal:$journal,readback:$aggregate,rollback:$rollback}' | redact_evidence > "$FINAL_EVIDENCE_PATH" || return 1
  [[ "$render_failed" -eq 0 ]]
}

case "${1:-}" in
  init) initialize ;;
  plan) plan ;;
  preflight-shared) preflight_shared ;;
  preflight) preflight ;;
  freeze) freeze ;;
  revalidate-before-provider) revalidate_before_provider ;;
  mutate) mutate ;;
  reconcile) reconcile ;;
  aggregate-reconcile) aggregate_reconcile ;;
  rollback) rollback ;;
  evidence) evidence ;;
  consume-dev-images) consume_dev_images ;;
  *) die "usage: cd.sh init|plan|preflight-shared|preflight|freeze|revalidate-before-provider|mutate|reconcile|aggregate-reconcile|rollback|evidence|consume-dev-images" ;;
esac
