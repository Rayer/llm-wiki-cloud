#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
source "$ROOT/deploy/components/common.sh"

exportjob_preflight() {
  local project account job region current
  [[ "$ENVIRONMENT" == development ]] || die "export job deployment is DEV-only"
  project=$(plan_json '.gcp.project_id'); account=$(plan_json '.export_job.runtime_service_account')
  job=$(plan_json '.export_job.job_name'); region=$(plan_json '.export_job.location')
  preflight_service_account "$account" "$project"
  preflight_service_account "$(plan_json '.export_job.signing_service_account')" "$project"
  preflight_job_binding "$job" "$project" "$region" roles/run.jobsExecutorWithOverrides "$(plan_json '.bff.runtime_service_account')"
  current=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet) || die "export job is missing or unreadable; operator provisioning is required"
  exportjob_runtime_matches "$current" || die "export job runtime identity or environment disagrees with the reviewed config"
}

exportjob_freeze() {
  freeze_store exportjob "$(jq -n --arg image "$(exportjob_image_handle)" '{image:$image}')"
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

exportjob_mutate() {
  local image job project region update_status=0
  journal_init; revalidate_before_provider; journal_pending exportjob
  if ! image=$(exportjob_build_image); then journal_transition exportjob unknown; die "export job image build failed"; fi
  validate_image_value exportjob "$image"
  mkdir -p "$ARTIFACT_DIR/images"
  printf '%s\n' "$image" > "$ARTIFACT_DIR/images/exportjob-image-$SOURCE_SHA.txt"
  mutation_accepted exportjob
  job=$(plan_json '.export_job.job_name'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.export_job.location')
  revalidate_before_provider
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -ne 0 ]] && ! exportjob_image_readback "$image"; then journal_transition exportjob unknown; die "export job image mutation did not converge (status=$update_status)"; fi
  readback_retry exportjob_verify "$image" || { journal_transition exportjob unknown; die "export job image/config read-back did not converge"; }
}

exportjob_verify() {
  local image="$1" observed status
  EXPORTJOB_READBACK=''; EXPORTJOB_READBACK_RESULT=unknown
  if observed=$(exportjob_image_readback "$image"); then
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
  local image job project region observed update_status=0 readback_status=0
  image=$(jq -er '.handles.exportjob.image' "$ROLLBACK_PATH") || { write_rollback_result exportjob failed '{}'; return 1; }
  validate_image_value exportjob "$image" || { write_rollback_result exportjob failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.export_job.location'); job=$(plan_json '.export_job.job_name')
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
