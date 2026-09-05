#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
# shellcheck source=deploy/components/common.sh
source "$ROOT/deploy/components/common.sh"

worker_preflight() {
  local project account
  project=$(plan_json '.gcp.project_id'); account=$(plan_json '.worker.runtime_service_account')
  preflight_service_account "$account" "$project"
  preflight_job_binding "$(plan_json '.worker.job_name')" "$project" "$(plan_json '.worker.location')" roles/run.viewer "$account"
}

worker_freeze() {
  local image
  image=$(worker_image_handle) || die "Worker effective image handle is unavailable or mutable"
  freeze_store worker "$(jq -n --arg image "$image" '{image:$image}')"
}

worker_build_image() {
  local image digest nonce artifact_registry registry_host
  nonce=$(printf '%032x' "$(date -u +%s%N)")
  artifact_registry="$(plan_json '.gcp.artifact_registry')"
  registry_host="${artifact_registry%%/*}"
  gcloud auth configure-docker "$registry_host" --quiet
  image="$artifact_registry/olw-pipeline:$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  docker build --build-arg BUILD_NONCE="$nonce" --target worker -f "$ROOT/apps/bff/cmd/olw_worker/Dockerfile" -t "$image" "$ROOT/apps/bff" >/dev/null
  docker push "$image" >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "worker image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

worker_mutate() {
  local image job project region update_status=0
  journal_init
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! image=$(image_for worker); then
      journal_rejected worker
      write_component_result worker failed '{}' immutable_image_receipt_invalid
      return 1
    fi
  fi
  if [[ "$ENVIRONMENT" == development ]]; then
    revalidate_before_provider
    journal_pending worker
    if ! image=$(worker_build_image); then
      journal_transition worker unknown
      write_component_result worker failed '{}' image_build_failed
      return 1
    fi
    validate_image_value worker "$image"
    mutation_accepted worker
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/worker-image-$SOURCE_SHA.txt"
  fi
  job=$(plan_json '.worker.job_name'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location')
  revalidate_before_provider
  if ! jq -e '.components.worker? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending worker; fi
  validate_image_value worker "$image"
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -ne 0 ]]; then
    if ! worker_verify "$image"; then
      journal_transition worker unknown
      die "Worker image mutation did not converge (status=$update_status)"
    fi
  fi
  [[ "$ENVIRONMENT" == production ]] && mutation_accepted worker
  readback_retry worker_verify "$image" || die "worker post-mutation read-back did not converge"
}

worker_verify() {
  local image="$1" observed readback_status
  WORKER_READBACK=''; WORKER_READBACK_RESULT=unknown
  if observed=$(worker_image_readback "$image"); then
    WORKER_READBACK="$observed"; WORKER_READBACK_RESULT=success
    return 0
  else
    readback_status=$?
    case "$readback_status" in
      1) WORKER_READBACK_RESULT=failed ;;
      *) WORKER_READBACK_RESULT=unknown ;;
    esac
    return 1
  fi
}

worker_reconcile() {
  local image
  image=$(image_for worker)
  if worker_verify "$image"; then write_component_result worker success "$WORKER_READBACK"; else write_component_result worker "${WORKER_READBACK_RESULT:-unknown}" "${WORKER_READBACK:-}" runtime_readback_mismatch; return 1; fi
}

worker_rollback() {
  local image project region job observed update_status=0 readback_status=0
  image=$(jq -er '.handles.worker.image' "$ROLLBACK_PATH") || { write_rollback_result worker failed '{}'; return 1; }
  validate_image_value worker "$image" || { write_rollback_result worker failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  if observed=$(worker_image_readback "$image"); then
    write_rollback_result worker success "$observed" verified_noop
    return 0
  else
    readback_status=$?
  fi
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if observed=$(worker_image_readback "$image"); then
    write_rollback_result worker success "$observed"
  else
    readback_status=$?
    if [[ "$update_status" -ne 0 || "$readback_status" -eq 2 ]]; then
      write_rollback_result worker unknown '{}'; return 2
    fi
    write_rollback_result worker failed '{}'; return 1
  fi
}

case "${1:-}" in
  help) printf 'worker component: preflight|freeze|mutate|reconcile|rollback\n' ;;
  preflight) worker_preflight ;;
  freeze) worker_freeze ;;
  mutate) worker_mutate ;;
  reconcile) worker_reconcile ;;
  rollback) worker_rollback ;;
  *) die "usage: worker.sh help|preflight|freeze|mutate|reconcile|rollback" ;;
esac
