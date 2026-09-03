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
  local project region job job_json definition readback provider_state account
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  job_json=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet)
  definition=$(normalize_worker_definition <<<"$job_json")
  readback=$(normalize_worker_readback "$definition") || die "frozen Worker definition is malformed or contains unallowlisted behavior"
  provider_state=$(worker_provider_state "$job_json") || die "frozen Worker generation or etag is malformed"
  account=$(jq -er '.service_account // empty' <<<"$readback")
  freeze_store worker "$(jq -n --argjson definition "$definition" --argjson readback "$readback" --argjson provider_state "$provider_state" --arg account "$account" --arg location "$region" '{definition:$definition,readback:$readback,provider_state:$provider_state,location:$location,service_account:$account}')"
}

worker_concurrency_guard() {
  local expected current job project region
  expected=$(jq -ce '.handles.worker.provider_state // {}' "$ROLLBACK_PATH") || die "frozen Worker concurrency state is malformed"
  if ! jq -e '(.generation == null or ((.generation|type) == "number" and (.generation|floor) == .generation and .generation > 0)) and (.etag == null or ((.etag|type) == "string" and .etag != ""))' <<<"$expected" >/dev/null; then
    die "frozen Worker concurrency state is malformed"
  fi
  if jq -e '.generation == null and .etag == null' <<<"$expected" >/dev/null; then return 0; fi
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  current=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet) || die "Worker concurrency readback is unavailable"
  current=$(worker_provider_state "$current") || die "current Worker concurrency state is malformed"
  jq -e --argjson expected "$expected" --argjson current "$current" '($expected.generation == null or $expected.generation == $current.generation) and ($expected.etag == null or $expected.etag == $current.etag)' >/dev/null <<<"{}" || die "Worker changed after freeze; refusing provider mutation"
}

worker_build_image() {
  local image digest nonce
  nonce=$(printf '%032x' "$(date -u +%s%N)")
  image="$(plan_json '.gcp.artifact_registry')/olw-pipeline:$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  docker build --build-arg BUILD_NONCE="$nonce" --target worker -f "$ROOT/apps/bff/cmd/olw_worker/Dockerfile" -t "$image" "$ROOT/apps/bff" >/dev/null
  docker push "$image" >/dev/null
  digest=$(gcloud artifacts docker images describe "$image" --project "$(plan_json '.gcp.project_id')" --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "worker image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

worker_mutate() {
  local image job bucket args secret account update_status=0
  if [[ "$ENVIRONMENT" == production ]]; then
    if ! image=$(image_for worker); then
      journal_rejected worker
      write_component_result worker failed '{}' immutable_image_receipt_invalid
      return 1
    fi
  fi
  journal_pending worker
  revalidate_before_provider
  if [[ "$ENVIRONMENT" == development ]]; then
    if ! image=$(worker_build_image); then
      journal_rejected worker
      write_component_result worker failed '{}' image_build_failed
      return 1
    fi
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/worker-image-$SOURCE_SHA.txt"
  fi
  job=$(plan_json '.worker.job_name'); bucket=$(plan_json '.worker.bucket'); args=$(plan_json '.worker.args | join("@")'); secret=$(plan_json '.worker.secret_references.deepseek_api_key'); account=$(plan_json '.worker.runtime_service_account')
  revalidate_before_provider
  if [[ -n "${ROLLBACK_PATH:-}" && -s "$ROLLBACK_PATH" ]]; then worker_concurrency_guard; fi
  if timeout --signal=TERM --kill-after=5s 600s gcloud run jobs update "$job" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.worker.location')" --service-account "$account" --image "$image" --update-secrets "DEEPSEEK_API_KEY=$secret:latest" --update-env-vars "BUCKET=$bucket,PIPELINE_JOB_NAME=$job,PIPELINE_JOB_LOCATION=$(plan_json '.worker.location')" --remove-env-vars "DATA_DIR,WORKSPACE,VAULT_PATH,WORKSPACE_DIR" --args "^@^$args" --clear-volume-mounts --clear-volumes --quiet >/dev/null; then :; else update_status=$?; fi
  if [[ "$update_status" -ne 0 ]]; then
    if ! worker_verify "$image"; then
      if [[ "${WORKER_READBACK_RESULT:-unknown}" == unknown ]]; then journal_transition worker unknown; else journal_transition worker unknown; fi
      die "Worker provider command failed and exact desired definition was not read back (status=$update_status)"
    fi
  fi
  mutation_accepted worker
  worker_verify "$image" || die "worker post-mutation read-back did not converge"
}

worker_verify() {
  local image="$1" project region job json definition observed expected provider_state
  WORKER_READBACK_RESULT=unknown
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.worker.location'); job=$(plan_json '.worker.job_name')
  if ! json=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet); then return 1; fi
  if ! definition=$(normalize_worker_definition <<<"$json"); then WORKER_READBACK_RESULT=failed; return 1; fi
  if ! observed=$(normalize_worker_readback "$definition"); then WORKER_READBACK_RESULT=failed; return 1; fi
  if ! provider_state=$(worker_provider_state "$json"); then WORKER_READBACK_RESULT=failed; return 1; fi
  expected=$(worker_expected "$image")
  if ! verify_worker_definition "$observed" "$expected"; then WORKER_READBACK_RESULT=failed; return 1; fi
  WORKER_READBACK=$(jq -n --argjson behavior "$observed" --argjson provider_state "$provider_state" '{behavior:$behavior,provider_state:$provider_state}'); WORKER_READBACK_RESULT=success
}

worker_reconcile() {
  local image
  image=$(image_for worker)
  if worker_verify "$image"; then write_component_result worker success "$WORKER_READBACK"; else write_component_result worker "${WORKER_READBACK_RESULT:-unknown}" "${WORKER_READBACK:-}" runtime_readback_mismatch; return 1; fi
}

worker_rollback() {
  local definition expected path project region job job_json observed provider_state
  definition=$(jq -cer '.handles.worker.definition' "$ROLLBACK_PATH") || { write_rollback_result worker failed '{}'; return 1; }
  expected=$(jq -cer '.handles.worker.readback' "$ROLLBACK_PATH") || { write_rollback_result worker failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(jq -er '.handles.worker.location' "$ROLLBACK_PATH"); job=$(plan_json '.worker.job_name')
  mkdir -p "$ARTIFACT_DIR"
  path=$(mktemp "$ARTIFACT_DIR/worker-rollback-definition.XXXXXX")
  printf '%s\n' "$definition" > "$path"
  timeout --signal=TERM --kill-after=5s 600s gcloud run jobs replace "$path" --project "$project" --region "$region" --quiet >/dev/null || :
  job_json=$(gcloud run jobs describe "$job" --project "$project" --region "$region" --format=json --quiet) || { write_rollback_result worker unknown '{}'; return 2; }
  observed=$(normalize_worker_definition <<<"$job_json") || { write_rollback_result worker failed '{}'; return 1; }
  observed=$(normalize_worker_readback "$observed") || { write_rollback_result worker failed '{}'; return 1; }
  provider_state=$(worker_provider_state "$job_json") || { write_rollback_result worker unknown '{}'; return 2; }
  if verify_worker_definition "$observed" "$expected"; then
    write_rollback_result worker success "$(jq -n --argjson behavior "$observed" --argjson provider_state "$provider_state" '{behavior:$behavior,provider_state:$provider_state}')"
  else
    write_rollback_result worker failed "$(jq -n --argjson behavior "$observed" --argjson provider_state "$provider_state" '{behavior:$behavior,provider_state:$provider_state}')"; return 1
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
