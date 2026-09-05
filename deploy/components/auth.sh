#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
# shellcheck source=deploy/components/common.sh
source "$ROOT/deploy/components/common.sh"

auth_preflight() {
  local project region account secret
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); account=$(plan_json '.auth.runtime_service_account')
  preflight_service_account "$account" "$project"
  gcloud firestore databases describe --database "$(plan_json '.auth.firestore_database_id')" --project "$project" --format=json --quiet >/dev/null || die "Auth Firestore database is missing or unreadable"
  secret=$(plan_json '.auth.secret_references.jwt')
  preflight_secret "$secret" "$account" "$project"
  preflight_public_service "$(plan_json '.auth.service_name')" "$project" "$region"
}

auth_freeze() {
  local image
  image=$(service_image_handle auth) || die "Auth effective image handle is unavailable or mutable"
  freeze_store auth "$(jq -n --arg image "$image" '{image:$image}')"
}

auth_build_image() {
  local image digest
  image="$(plan_json '.gcp.artifact_registry')/llm-wiki-auth:$SOURCE_SHA"
  timeout --signal=TERM --kill-after=5s 600s gcloud builds submit "$ROOT/apps/bff" --project "$(plan_json '.gcp.project_id')" --config "$ROOT/apps/bff/cloudbuild-auth.yaml" \
    --substitutions="_IMAGE=$image,_APP_VERSION=$(cd "$BFF_DIR" && go run ./cmd/versioncheck VERSION),_GIT_SHA=$SOURCE_SHA,_GIT_BRANCH=$SOURCE_REF,_GIT_TAG=" --quiet --suppress-logs >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "auth image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

auth_mutate() {
  local image service project region deploy_status=0 traffic_status=0
  journal_init
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! image=$(image_for auth); then
      journal_rejected auth
      write_component_result auth failed '{}' immutable_image_receipt_invalid
      return 1
    fi
  fi
  if [[ "$ENVIRONMENT" == development ]]; then
    revalidate_before_provider
    journal_pending auth
    if ! image=$(auth_build_image); then
      journal_transition auth unknown
      write_component_result auth failed '{}' image_build_failed
      return 1
    fi
    validate_image_value auth "$image"
    mutation_accepted auth
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/auth-image-$SOURCE_SHA.txt"
  fi
  revalidate_before_provider
  if ! jq -e '.components.auth? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending auth; fi
  service=$(plan_json '.auth.service_name'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region')
  validate_image_value auth "$image"
  if timeout --signal=TERM --kill-after=5s 600s gcloud run services update "$service" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else deploy_status=$?; fi
  if [[ "$deploy_status" -eq 0 ]]; then
    if timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-latest --project "$project" --region "$region" --quiet >/dev/null; then :; else traffic_status=$?; fi
  fi
  if ! readback_retry auth_verify "$image"; then
    journal_transition auth unknown
    die "auth image/traffic mutation did not converge (image_status=$deploy_status traffic_status=$traffic_status readback=$SERVICE_READBACK_RESULT)"
  fi
  [[ "$ENVIRONMENT" == production ]] && mutation_accepted auth
}

auth_verify() {
  local image="$1" revision="${2:-}" observed readback_status
  SERVICE_READBACK=''; SERVICE_READBACK_RESULT=unknown
  if observed=$(service_image_readback auth "$image" "$revision"); then
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

auth_reconcile() {
  local image revision observed
  image=$(image_for auth)
  revision=$(gcloud run services describe "$(plan_json '.auth.service_name')" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format='value(status.traffic[0].revisionName)' --quiet) || { write_component_result auth unknown '{}' service_revision_readback_unavailable; return 2; }
  if auth_verify "$image" "$revision"; then
    write_component_result auth success "$SERVICE_READBACK"
  else
    observed="${SERVICE_READBACK:-}"
    write_component_result auth "${SERVICE_READBACK_RESULT:-unknown}" "$observed" runtime_readback_mismatch
    return 1
  fi
}

auth_rollback() {
  local project region service image observed update_status=0 readback_status=0
  image=$(jq -er '.handles.auth.image' "$ROLLBACK_PATH") || { write_rollback_result auth failed '{}'; return 1; }
  validate_image_value auth "$image" || { write_rollback_result auth failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.auth.service_name')
  if observed=$(service_image_readback auth "$image"); then
    write_rollback_result auth success "$observed" verified_noop
    return 0
  fi
  if timeout --signal=TERM --kill-after=5s 240s gcloud run services update "$service" --project "$project" --region "$region" --image "$image" --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -eq 0 ]]; then
    timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-latest --project "$project" --region "$region" --quiet >/dev/null || :
  fi
  if observed=$(service_image_readback auth "$image"); then
    write_rollback_result auth success "$observed"
  else
    readback_status=$?
    if [[ "$update_status" -ne 0 || "$readback_status" -eq 2 ]]; then
      write_rollback_result auth unknown '{}'; return 2
    fi
    write_rollback_result auth failed '{}'; return 1
  fi
}

case "${1:-}" in
  help) printf 'auth component: preflight|freeze|mutate|reconcile|rollback\n' ;;
  preflight) auth_preflight ;;
  freeze) auth_freeze ;;
  mutate) auth_mutate ;;
  reconcile) auth_reconcile ;;
  rollback) auth_rollback ;;
  *) die "usage: auth.sh help|preflight|freeze|mutate|reconcile|rollback" ;;
esac
