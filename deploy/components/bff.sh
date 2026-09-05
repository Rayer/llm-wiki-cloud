#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
# shellcheck source=deploy/components/common.sh
source "$ROOT/deploy/components/common.sh"

bff_preflight() {
  local project region account
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); account=$(plan_json '.bff.runtime_service_account')
  preflight_service_account "$account" "$project"
  gcloud firestore databases describe --database "$(plan_json '.bff.firestore_database_id')" --project "$project" --format=json --quiet >/dev/null || die "BFF Firestore database is missing or unreadable"
  preflight_secret "$(plan_json '.bff.secret_references.jwt')" "$account" "$project"
  preflight_secret "$(plan_json '.bff.secret_references.deepseek_api_key')" "$account" "$project"
  preflight_public_service "$(plan_json '.bff.service_name')" "$project" "$region"
  preflight_job_binding "$(plan_json '.bff.pipeline_job_name')" "$project" "$region" roles/run.jobsExecutorWithOverrides "$account"
}

bff_freeze() {
  local image
  image=$(service_image_handle bff) || die "BFF effective image handle is unavailable or mutable"
  freeze_store bff "$(jq -n --arg image "$image" '{image:$image}')"
}

bff_build_image() {
  local image digest
  image="$(plan_json '.gcp.artifact_registry')/llm-wiki-bff:$SOURCE_SHA"
  timeout --signal=TERM --kill-after=5s 600s gcloud builds submit "$ROOT/apps/bff" --project "$(plan_json '.gcp.project_id')" --config "$ROOT/apps/bff/cloudbuild-bff.yaml" \
    --substitutions="_IMAGE=$image,_APP_VERSION=$(cd "$BFF_DIR" && go run ./cmd/versioncheck VERSION),_GIT_SHA=$SOURCE_SHA,_GIT_BRANCH=$SOURCE_REF,_GIT_TAG=" --quiet --suppress-logs >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "bff image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

bff_mutate() {
  local image service project region deploy_status=0 traffic_status=0
  journal_init
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! image=$(image_for bff); then
      journal_rejected bff
      write_component_result bff failed '{}' immutable_image_receipt_invalid
      return 1
    fi
  fi
  if [[ "$ENVIRONMENT" == development ]]; then
    revalidate_before_provider
    journal_pending bff
    if ! image=$(bff_build_image); then
      journal_transition bff unknown
      write_component_result bff failed '{}' image_build_failed
      return 1
    fi
    validate_image_value bff "$image"
    mutation_accepted bff
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/bff-image-$SOURCE_SHA.txt"
  fi
  revalidate_before_provider
  if ! jq -e '.components.bff? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending bff; fi
  service=$(plan_json '.bff.service_name'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region')
  validate_image_value bff "$image"
  if timeout --signal=TERM --kill-after=5s 600s gcloud run services update "$service" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else deploy_status=$?; fi
  if [[ "$deploy_status" -eq 0 ]]; then
    if timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-latest --project "$project" --region "$region" --quiet >/dev/null; then :; else traffic_status=$?; fi
  fi
  if ! readback_retry bff_verify "$image"; then
    journal_transition bff unknown
    die "bff image/traffic mutation did not converge (image_status=$deploy_status traffic_status=$traffic_status readback=$SERVICE_READBACK_RESULT)"
  fi
  [[ "$ENVIRONMENT" == production ]] && mutation_accepted bff
}

bff_verify() {
  local image="$1" revision="${2:-}" observed readback_status
  SERVICE_READBACK=''; SERVICE_READBACK_RESULT=unknown
  if observed=$(service_image_readback bff "$image" "$revision"); then
    SERVICE_READBACK="$observed"; SERVICE_READBACK_RESULT=success
    return 0
  else
    readback_status=$?
    case "$readback_status" in
      1) SERVICE_READBACK_RESULT=failed ;;
      *) SERVICE_READBACK_RESULT=unknown ;;
    esac
    return 1
  fi
}

bff_reconcile() {
  local image revision
  image=$(image_for bff)
  revision=$(gcloud run services describe "$(plan_json '.bff.service_name')" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format='value(status.traffic[0].revisionName)' --quiet) || { write_component_result bff unknown '{}' service_revision_readback_unavailable; return 2; }
  if bff_verify "$image" "$revision"; then write_component_result bff success "$SERVICE_READBACK"; else write_component_result bff "${SERVICE_READBACK_RESULT:-unknown}" "${SERVICE_READBACK:-}" runtime_readback_mismatch; return 1; fi
}

bff_rollback() {
  local project region service image observed update_status=0 readback_status=0
  image=$(jq -er '.handles.bff.image' "$ROLLBACK_PATH") || { write_rollback_result bff failed '{}'; return 1; }
  validate_image_value bff "$image" || { write_rollback_result bff failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.bff.service_name')
  if observed=$(service_image_readback bff "$image"); then
    write_rollback_result bff success "$observed" verified_noop
    return 0
  fi
  if timeout --signal=TERM --kill-after=5s 240s gcloud run services update "$service" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -eq 0 ]]; then
    timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-latest --project "$project" --region "$region" --quiet >/dev/null || :
  fi
  if observed=$(service_image_readback bff "$image"); then
    write_rollback_result bff success "$observed"
  else
    readback_status=$?
    if [[ "$update_status" -ne 0 || "$readback_status" -eq 2 ]]; then
      write_rollback_result bff unknown '{}'; return 2
    fi
    write_rollback_result bff failed '{}'; return 1
  fi
}

case "${1:-}" in
  help) printf 'bff component: preflight|freeze|mutate|reconcile|rollback\n' ;;
  preflight) bff_preflight ;;
  freeze) bff_freeze ;;
  mutate) bff_mutate ;;
  reconcile) bff_reconcile ;;
  rollback) bff_rollback ;;
  *) die "usage: bff.sh help|preflight|freeze|mutate|reconcile|rollback" ;;
esac
