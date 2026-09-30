#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
source "$ROOT/deploy/components/common.sh"

exportjob_describe() {
  local project job region output error_file error
  project=$(plan_json '.gcp.project_id'); job=$(plan_json '.export_job.job_name'); region=$(plan_json '.export_job.location')
  error_file=$(mktemp) || return 1
  if output=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet 2>"$error_file"); then
    rm -f "$error_file"
    printf '%s\n' "$output"
    return 0
  fi
  error=$(<"$error_file"); rm -f "$error_file"
  if grep -Eiq 'NOT_FOUND|not found|cannot find job \[[^]]+\]' <<<"$error"; then return 44; fi
  return 1
}

exportjob_bootstrap_labels() {
  local sha run_id attempt
  sha=${SOURCE_SHA:-}; run_id=${GITHUB_RUN_ID:-}; attempt=${GITHUB_RUN_ATTEMPT:-}
  [[ "$sha" =~ ^[0-9a-f]{40}$ && "$run_id" =~ ^[0-9]+$ && "$run_id" -gt 0 &&
    "$attempt" =~ ^[0-9]+$ && "$attempt" -gt 0 ]] || die "Production Export bootstrap run identity is invalid"
  jq -cn --arg sha "$sha" --arg run "$run_id-$attempt" \
    '{"lwc-created-by-sha":$sha,"lwc-created-by-run":$run}'
}

exportjob_bootstrap_label_args() {
  jq -er '.handles.exportjob as $h |
    if $h.absent == true and
      (($h.created_by["lwc-created-by-sha"] | type) == "string") and
      (($h.created_by["lwc-created-by-sha"] | test("^[0-9a-f]{40}$"))) and
      (($h.created_by["lwc-created-by-run"] | type) == "string") and
      (($h.created_by["lwc-created-by-run"] | test("^[0-9]+-[0-9]+$")))
    then $h.created_by|to_entries|map("\(.key)=\(.value)")|join(",")
    else error("Production Export bootstrap ownership marker is invalid") end' "$ROLLBACK_PATH"
}

exportjob_bootstrap_marker_matches() {
  local job_json="$1" expected
  expected=$(jq -cer '.handles.exportjob.created_by | select(type == "object")' "$ROLLBACK_PATH") || return 1
  jq -e --argjson expected "$expected" '
    .metadata.labels["lwc-created-by-sha"] == $expected["lwc-created-by-sha"] and
    .metadata.labels["lwc-created-by-run"] == $expected["lwc-created-by-run"]
  ' <<<"$job_json" >/dev/null
}

exportjob_json_image() {
  jq -er '(.template.template.containers // .spec.template.spec.template.spec.containers // .spec.template.spec.containers // [])
    | if type == "array" and length == 1 and (.[0].image|type) == "string" then .[0].image else error("Export image missing") end' <<<"$1"
}

exportjob_preflight() {
  local project account job region current status
  project=$(plan_json '.gcp.project_id'); account=$(plan_json '.export_job.runtime_service_account')
  job=$(plan_json '.export_job.job_name'); region=$(plan_json '.export_job.location')
  preflight_service_account "$account" "$project"
  preflight_service_account "$(plan_json '.export_job.signing_service_account')" "$project"
  if current=$(exportjob_describe); then
    exportjob_runtime_matches "$current" || die "export job runtime identity or environment disagrees with the reviewed config"
    preflight_job_binding "$job" "$project" "$region" roles/run.jobsExecutorWithOverrides "$(plan_json '.bff.runtime_service_account')"
  else
    status=$?
    [[ "$ENVIRONMENT" == production && "$status" -eq 44 ]] || die "export job is missing or unreadable"
  fi
}

exportjob_freeze() {
  local current status image labels
  if current=$(exportjob_describe); then
    exportjob_runtime_matches "$current" || die "export job runtime identity or environment disagrees with the reviewed config"
    image=$(exportjob_json_image "$current") || die "export job image handle is unavailable"
    validate_image_value exportjob "$image"
    freeze_store exportjob "$(jq -n --arg image "$image" '{image:$image}')"
  else
    status=$?
    [[ "$ENVIRONMENT" == production && "$status" -eq 44 ]] || die "export job rollback handle is unavailable"
    labels=$(exportjob_bootstrap_labels)
    freeze_store exportjob "$(jq -n --argjson labels "$labels" '{absent:true,created_by:$labels}')"
  fi
}

exportjob_build_image() {
  local image digest registry_host registry
  registry=$(plan_json '.gcp.artifact_registry'); registry_host="${registry%%/*}"
  gcloud auth configure-docker "$registry_host" --quiet
  image="$registry/llm-wiki-bff-export-job:$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  docker build -f "$ROOT/apps/bff/Dockerfile.exportjob" -t "$image" "$ROOT/apps/bff" >/dev/null
  docker push "$image" >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "export job image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

exportjob_create_production() {
  local image="$1" current status project region job account bff_account bucket database signer timeout retries parallelism tasks labels env_args
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.export_job.location'); job=$(plan_json '.export_job.job_name')
  account=$(plan_json '.export_job.runtime_service_account'); bucket=$(plan_json '.export_job.bucket')
  bff_account=$(plan_json '.bff.runtime_service_account')
  database=$(plan_json '.export_job.firestore_database_id'); signer=$(plan_json '.export_job.signing_service_account')
  timeout=$(plan_json '.export_job.job_timeout'); retries=$(plan_json '.export_job.max_retries')
  parallelism=$(plan_json '.export_job.parallelism'); tasks=$(plan_json '.export_job.tasks')
  labels=$(exportjob_bootstrap_label_args) || die "Production Export creation ownership marker is unavailable"
  if current=$(exportjob_describe); then
    journal_rejected exportjob
    write_component_result exportjob failed '{}' job_appeared_after_absent_snapshot
    die "Production Export Job appeared after its absent rollback snapshot; no create was attempted"
  else
    status=$?
    [[ "$status" -eq 44 ]] || die "Production Export Job absence could not be safely confirmed"
  fi
  env_args="^|^GCP_PROJECT=$project|BUCKET=$bucket|FIRESTORE_DATABASE_ID=$database|EXPORT_SIGNING_SERVICE_ACCOUNT=$signer"
  journal_pending exportjob
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs create "$job" --project "$project" --region "$region" \
    --image "$image" --service-account "$account" --task-timeout "$timeout" --max-retries "$retries" \
    --parallelism "$parallelism" --tasks "$tasks" --set-env-vars "$env_args" --labels "$labels" --quiet >/dev/null; then
    mutation_accepted exportjob
  else
    status=$?
    if current=$(exportjob_describe) && exportjob_bootstrap_marker_matches "$current" &&
      [[ "$(exportjob_json_image "$current")" == "$image" ]]; then
      mutation_accepted exportjob
    elif [[ "$status" -ne 0 ]]; then
      if current=$(exportjob_describe); then journal_transition exportjob unknown; else
        status=$?
        if [[ "$status" -eq 44 ]]; then journal_rejected exportjob; else journal_transition exportjob unknown; fi
      fi
      write_component_result exportjob failed '{}' job_create_not_verified
      die "Production Export Job create did not produce the receipt image with this run's ownership marker"
    fi
  fi
  gcloud run jobs add-iam-policy-binding "$job" --project "$project" --region "$region" \
    --member "serviceAccount:$bff_account" --role roles/run.jobsExecutorWithOverrides --quiet >/dev/null ||
    die "Production Export Job could not apply its reviewed BFF invocation binding"
  preflight_job_binding "$job" "$project" "$region" roles/run.jobsExecutorWithOverrides "$bff_account"
  readback_retry exportjob_verify "$image" || { journal_transition exportjob unknown; die "new Production Export Job read-back did not converge"; }
}

exportjob_mutate() {
  local image job project region update_status=0
  journal_init
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! image=$(image_for exportjob); then
      journal_rejected exportjob
      write_component_result exportjob failed '{}' immutable_image_receipt_invalid
      return 1
    fi
    revalidate_before_provider
    if jq -e '.handles.exportjob.absent == true' "$ROLLBACK_PATH" >/dev/null; then
      exportjob_create_production "$image"
      return
    fi
    if current=$(exportjob_image_readback "$image"); then
      journal_rejected exportjob
      write_component_result exportjob success "$current" verified_noop
      return 0
    fi
  else
    revalidate_before_provider; journal_pending exportjob
    if ! image=$(exportjob_build_image); then journal_transition exportjob unknown; die "export job image build failed"; fi
    validate_image_value exportjob "$image"
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/exportjob-image-$SOURCE_SHA.txt"
    mutation_accepted exportjob
  fi
  job=$(plan_json '.export_job.job_name'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.export_job.location')
  revalidate_before_provider
  if ! jq -e '.components.exportjob? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending exportjob; fi
  validate_image_value exportjob "$image"
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -ne 0 ]] && ! exportjob_image_readback "$image"; then journal_transition exportjob unknown; die "export job image mutation did not converge (status=$update_status)"; fi
  [[ "$ENVIRONMENT" == production ]] && mutation_accepted exportjob
  readback_retry exportjob_verify "$image" || { journal_transition exportjob unknown; die "export job image/config read-back did not converge"; }
}

exportjob_verify() {
  local image="$1" observed status current
  EXPORTJOB_READBACK=''; EXPORTJOB_READBACK_RESULT=unknown
  if observed=$(exportjob_image_readback "$image"); then
    if [[ "$ENVIRONMENT" == production ]] && jq -e '.handles.exportjob.absent == true' "$ROLLBACK_PATH" >/dev/null; then
      current=$(exportjob_describe) || { EXPORTJOB_READBACK_RESULT=unknown; return 1; }
      if ! exportjob_bootstrap_marker_matches "$current"; then EXPORTJOB_READBACK_RESULT=failed; return 1; fi
    fi
    EXPORTJOB_READBACK="$observed"; EXPORTJOB_READBACK_RESULT=success; return 0
  else
    status=$?; [[ "$status" -eq 1 ]] && EXPORTJOB_READBACK_RESULT=failed
    return 1
  fi
}

exportjob_reconcile() {
  local image
  image=$(image_for exportjob)
  if exportjob_verify "$image"; then write_component_result exportjob success "$EXPORTJOB_READBACK"; else write_component_result exportjob "${EXPORTJOB_READBACK_RESULT:-unknown}" "${EXPORTJOB_READBACK:-}" runtime_readback_mismatch; return 1; fi
}

exportjob_rollback() {
  local image job project region observed update_status=0 readback_status=0 current status
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.export_job.location'); job=$(plan_json '.export_job.job_name')
  if jq -e '.handles.exportjob.absent == true' "$ROLLBACK_PATH" >/dev/null; then
    image=$(image_for exportjob) || { write_rollback_result exportjob failed '{}'; return 1; }
    if current=$(exportjob_describe); then
      if ! exportjob_bootstrap_marker_matches "$current" || [[ "$(exportjob_json_image "$current")" != "$image" ]]; then
        write_rollback_result exportjob failed '{}' created_job_identity_changed
        return 1
      fi
      if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs delete "$job" --project "$project" --region "$region" --quiet >/dev/null; then :; else update_status=$?; fi
      if exportjob_describe >/dev/null; then
        write_rollback_result exportjob "$([[ "$update_status" -eq 0 ]] && printf failed || printf unknown)" '{}' created_job_remains_after_delete
        return 1
      else
        status=$?
        if [[ "$status" -eq 44 ]]; then write_rollback_result exportjob success '{}' created_job_deleted; return 0; fi
        write_rollback_result exportjob unknown '{}' created_job_delete_readback_unavailable
        return 2
      fi
    else
      status=$?
      if [[ "$status" -eq 44 ]]; then write_rollback_result exportjob success '{}' verified_noop; return 0; fi
      write_rollback_result exportjob unknown '{}' created_job_readback_unavailable
      return 2
    fi
  fi
  image=$(jq -er '.handles.exportjob.image' "$ROLLBACK_PATH") || { write_rollback_result exportjob failed '{}'; return 1; }
  validate_image_value exportjob "$image" || { write_rollback_result exportjob failed '{}'; return 1; }
  if observed=$(exportjob_image_readback "$image"); then write_rollback_result exportjob success "$observed" verified_noop; return 0; else readback_status=$?; fi
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if observed=$(exportjob_image_readback "$image"); then write_rollback_result exportjob success "$observed"; else
    readback_status=$?
    if [[ "$update_status" -ne 0 || "$readback_status" -eq 2 ]]; then write_rollback_result exportjob unknown '{}'; return 2; fi
    write_rollback_result exportjob failed '{}'; return 1
  fi
}

case "${1:-}" in
  help) printf 'exportjob component: preflight|freeze|mutate|reconcile|rollback\n' ;;
  preflight) exportjob_preflight ;;
  freeze) exportjob_freeze ;;
  mutate) exportjob_mutate ;;
  reconcile) exportjob_reconcile ;;
  rollback) exportjob_rollback ;;
  *) die "usage: exportjob.sh help|preflight|freeze|mutate|reconcile|rollback" ;;
esac
