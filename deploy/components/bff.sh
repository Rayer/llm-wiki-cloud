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
  local project region service service_json traffic revision revision_json image readback
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.bff.service_name')
  service_json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet)
  traffic=$(jq -ce 'if (.status.traffic|type) != "array" or (.status.traffic|length) != 1 or .status.traffic[0].percent != 100 or (.status.traffic[0].revisionName|type) != "string" or (.status.traffic[0].tag? != null) then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json")
  revision=$(jq -er '.revisionName' <<<"$traffic")
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet)
  image=$(jq -er '.status.imageDigest | select(type == "string" and test("@sha256:"))' <<<"$revision_json")
  readback=$(normalize_service_readback bff "$service_json" "$revision_json" "$revision" "$image" 1)
  freeze_store bff "$(jq -n --arg revision "$revision" --arg image "$image" --argjson traffic "$traffic" --argjson readback "$readback" '{revision:$revision,image:$image,traffic:$traffic,service_account:$readback.service_account,readback:$readback}')"
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
  local image service account project region origins secrets deploy_status=0 revision
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
    mutation_accepted bff
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/bff-image-$SOURCE_SHA.txt"
  fi
  revalidate_before_provider
  if ! jq -e '.components.bff? != null' "$JOURNAL_PATH" >/dev/null; then journal_pending bff; fi
  service=$(plan_json '.bff.service_name'); account=$(plan_json '.bff.runtime_service_account'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region')
  origins=$(join_config_list '.bff.allowed_origins')
  secrets="JWT_SECRET=$(plan_json '.bff.secret_references.jwt'):latest,DEEPSEEK_API_KEY=$(plan_json '.bff.secret_references.deepseek_api_key'):latest"
  local -a flags=(--project "$project" --region "$region" --platform managed --service-account "$account" --image "$image"
    --network "$(plan_json '.bff.network')" --subnet "$(plan_json '.bff.subnet')" --vpc-egress "$(plan_json '.bff.vpc_egress')" --ingress "$(plan_json '.bff.ingress')" --max "$(plan_json '.bff.max_instances')"
    --update-env-vars "^@^GCP_PROJECT=$project@BUCKET=$(plan_json '.bff.bucket')@FIRESTORE_DATABASE_ID=$(plan_json '.bff.firestore_database_id')@PIPELINE_JOB_URL=$(plan_json '.bff.pipeline_job_url')@ALLOWED_ORIGINS=$origins@AUTH_SERVICE_URL=$(plan_json '.bff.auth_service_url')@QUERY_STAGE_CONFIG_PATH=$(plan_json '.query_config.runtime_path')@DEV_JWT=false@LWC_SOURCE_COMMIT=$SOURCE_SHA"
    --remove-env-vars "QUERY_EXPANSION_MODEL,QUERY_EXPANSION_REASONING,ANSWER_SYNTHESIS_MODEL,ANSWER_SYNTHESIS_REASONING,QUERY_SELECTION_LIMIT,QUERY_SELECTION_EXPLORATION_SLOTS,QUERY_SELECTION_EVIDENCE_THRESHOLD,QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT,QUERY_EXPANSION_ATTEMPTS,QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY,USER_ID,PROJECT_ID"
    --update-secrets "$secrets" --allow-unauthenticated --quiet)
  [[ "$ENVIRONMENT" != production ]] || flags+=(--no-traffic)
  if timeout --signal=TERM --kill-after=5s 600s gcloud run deploy "$service" "${flags[@]}" >/dev/null; then :; else deploy_status=$?; fi
  if ! revision=$(gcloud run services describe "$service" --project "$project" --region "$region" --format='value(status.latestCreatedRevisionName)' --quiet); then
    journal_transition bff unknown
    die "bff created revision is unavailable after provider command status $deploy_status"
  fi
  if [[ ! "$revision" =~ ^[a-z][a-z0-9-]{0,61}[a-z0-9]$ ]]; then
    journal_transition bff unknown
    die "bff created revision is invalid"
  fi
  if [[ "$deploy_status" -ne 0 ]]; then
    if ! bff_verify "$image" "$revision"; then
      journal_transition bff unknown
      die "bff provider command failed and exact desired definition was not read back"
    fi
  fi
  [[ "$ENVIRONMENT" == production ]] && mutation_accepted bff
  if [[ "$ENVIRONMENT" == production ]]; then
    revalidate_before_provider
    if ! timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-revisions "$revision=100" --project "$project" --region "$region" --quiet >/dev/null; then
      if ! bff_verify "$image" "$revision"; then
        journal_transition bff unknown
        die "bff traffic mutation failed and authoritative read-back did not converge"
      fi
    fi
  fi
  bff_verify "$image" "$revision" || die "bff post-mutation read-back did not converge"
}

bff_verify() {
  local image="$1" revision="$2" project region service json revision_json observed expected
  SERVICE_READBACK_RESULT=unknown
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.bff.service_name')
  if ! json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet); then return 1; fi
  if ! jq -e --arg revision "$revision" '(.status.traffic|type) == "array" and (.status.traffic|length) == 1 and .status.traffic[0].revisionName == $revision and .status.traffic[0].percent == 100 and (.status.traffic[0].tag? == null)' <<<"$json" >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  if ! revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet); then return 1; fi
  if ! jq -e --arg image "$image" '(.status.imageDigest // "") == $image and (.spec.containers[0].image // "") == $image and any(.status.conditions[]?; .type == "Ready" and .status == "True")' <<<"$revision_json" >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  if ! observed=$(normalize_service_readback bff "$json" "$revision_json" "$revision" "$image"); then SERVICE_READBACK_RESULT=failed; return 1; fi
  expected=$(service_expected bff "$image" "$revision")
  if ! jq -n -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  SERVICE_READBACK="$observed"; SERVICE_READBACK_RESULT=success
}

bff_reconcile() {
  local image revision
  image=$(image_for bff)
  revision=$(gcloud run services describe "$(plan_json '.bff.service_name')" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format='value(status.traffic[0].revisionName)' --quiet) || { write_component_result bff unknown '{}' service_revision_readback_unavailable; return 2; }
  if bff_verify "$image" "$revision"; then write_component_result bff success "$SERVICE_READBACK"; else write_component_result bff "${SERVICE_READBACK_RESULT:-unknown}" "${SERVICE_READBACK:-}" runtime_readback_mismatch; return 1; fi
}

bff_rollback() {
  local project region service revision image expected json revision_json observed
  revision=$(jq -er '.handles.bff.revision' "$ROLLBACK_PATH") || { write_rollback_result bff failed '{}'; return 1; }
  image=$(jq -er '.handles.bff.image' "$ROLLBACK_PATH") || { write_rollback_result bff failed '{}'; return 1; }
  expected=$(jq -cer '.handles.bff.readback' "$ROLLBACK_PATH") || { write_rollback_result bff failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.bff.service_name')
  if observed=$(service_frozen_readback bff 1); then
    write_rollback_result bff success "$observed" verified_noop
    return 0
  fi
  timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-revisions "$revision=100" --project "$project" --region "$region" --quiet >/dev/null || :
  json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet) || { write_rollback_result bff unknown '{}'; return 2; }
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet) || { write_rollback_result bff unknown '{}'; return 2; }
  observed=$(normalize_service_readback bff "$json" "$revision_json" "$revision" "$image" 1) || { write_rollback_result bff failed '{}'; return 1; }
  if jq -n -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then write_rollback_result bff success "$observed"; else write_rollback_result bff failed "$observed"; return 1; fi
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
