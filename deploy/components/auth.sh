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
  local project region service service_json traffic revision revision_json image readback
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.auth.service_name')
  service_json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet)
  traffic=$(jq -ce 'if (.status.traffic|type) != "array" or (.status.traffic|length) != 1 or .status.traffic[0].percent != 100 or (.status.traffic[0].revisionName|type) != "string" or (.status.traffic[0].tag? != null) then error("service traffic is not one untagged 100-percent revision") else .status.traffic[0] end' <<<"$service_json")
  revision=$(jq -er '.revisionName' <<<"$traffic")
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet)
  image=$(jq -er '.status.imageDigest | select(type == "string" and test("@sha256:"))' <<<"$revision_json")
  readback=$(normalize_service_readback auth "$service_json" "$revision_json" "$revision" "$image")
  freeze_store auth "$(jq -n --arg revision "$revision" --arg image "$image" --argjson traffic "$traffic" --argjson readback "$readback" '{revision:$revision,image:$image,traffic:$traffic,service_account:$readback.service_account,readback:$readback}')"
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
  local image service account project region origins secrets deploy_status=0 revision
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
    if ! image=$(auth_build_image); then
      journal_rejected auth
      write_component_result auth failed '{}' image_build_failed
      return 1
    fi
    mkdir -p "$ARTIFACT_DIR/images"
    printf '%s\n' "$image" > "$ARTIFACT_DIR/images/auth-image-$SOURCE_SHA.txt"
  fi
  revalidate_before_provider
  journal_pending auth
  service=$(plan_json '.auth.service_name'); account=$(plan_json '.auth.runtime_service_account'); project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region')
  origins=$(join_config_list '.auth.allowed_origins')
  secrets="JWT_SECRET=$(plan_json '.auth.secret_references.jwt'):latest"
  local -a flags=(--project "$project" --region "$region" --platform managed --service-account "$account" --image "$image"
    --network "$(plan_json '.auth.network')" --subnet "$(plan_json '.auth.subnet')" --vpc-egress "$(plan_json '.auth.vpc_egress')" --ingress "$(plan_json '.auth.ingress')" --max "$(plan_json '.auth.max_instances')"
    --update-env-vars "^@^GCP_PROJECT=$project@FIRESTORE_DATABASE_ID=$(plan_json '.auth.firestore_database_id')@ALLOWED_ORIGINS=$origins@ALLOWED_HOSTS=$(join_config_list '.auth.allowed_hosts')@DEV_JWT=false@LWC_SOURCE_COMMIT=$SOURCE_SHA"
    --remove-env-vars "QUERY_EXPANSION_MODEL,QUERY_EXPANSION_REASONING,ANSWER_SYNTHESIS_MODEL,ANSWER_SYNTHESIS_REASONING,QUERY_SELECTION_LIMIT,QUERY_SELECTION_EXPLORATION_SLOTS,QUERY_SELECTION_EVIDENCE_THRESHOLD,QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT,QUERY_EXPANSION_ATTEMPTS,QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY"
    --update-secrets "$secrets" --allow-unauthenticated --quiet)
  if timeout --signal=TERM --kill-after=5s 600s gcloud run deploy "$service" "${flags[@]}" >/dev/null; then :; else deploy_status=$?; fi
  if ! revision=$(gcloud run services describe "$service" --project "$project" --region "$region" --format='value(status.latestCreatedRevisionName)' --quiet); then
    journal_transition auth unknown
    die "auth created revision is unavailable after provider command status $deploy_status"
  fi
  if [[ ! "$revision" =~ ^[a-z][a-z0-9-]{0,61}[a-z0-9]$ ]]; then
    journal_transition auth unknown
    die "auth created revision is invalid"
  fi
  if [[ "$deploy_status" -ne 0 ]]; then
    if ! auth_verify "$image" "$revision"; then
      journal_transition auth unknown
      die "auth provider command failed and exact desired definition was not read back"
    fi
  fi
  mutation_accepted auth
  auth_verify "$image" "$revision" || die "auth post-mutation read-back did not converge"
}

auth_verify() {
  local image="$1" revision="$2" project region service json revision_json observed expected
  SERVICE_READBACK_RESULT=unknown
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.auth.service_name')
  if ! json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet); then return 1; fi
  if ! jq -e --arg revision "$revision" '(.status.traffic|type) == "array" and (.status.traffic|length) == 1 and .status.traffic[0].revisionName == $revision and .status.traffic[0].percent == 100 and (.status.traffic[0].tag? == null)' <<<"$json" >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  if ! revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet); then return 1; fi
  if ! jq -e --arg image "$image" '(.status.imageDigest // "") == $image and (.spec.containers[0].image // "") == $image and any(.status.conditions[]?; .type == "Ready" and .status == "True")' <<<"$revision_json" >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  if ! observed=$(normalize_service_readback auth "$json" "$revision_json" "$revision" "$image"); then SERVICE_READBACK_RESULT=failed; return 1; fi
  expected=$(service_expected auth "$image" "$revision")
  if ! jq -n -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then SERVICE_READBACK_RESULT=failed; return 1; fi
  SERVICE_READBACK="$observed"; SERVICE_READBACK_RESULT=success
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
  local project region service revision image expected json revision_json observed
  revision=$(jq -er '.handles.auth.revision' "$ROLLBACK_PATH") || { write_rollback_result auth failed '{}'; return 1; }
  image=$(jq -er '.handles.auth.image' "$ROLLBACK_PATH") || { write_rollback_result auth failed '{}'; return 1; }
  expected=$(jq -cer '.handles.auth.readback' "$ROLLBACK_PATH") || { write_rollback_result auth failed '{}'; return 1; }
  project=$(plan_json '.gcp.project_id'); region=$(plan_json '.gcp.region'); service=$(plan_json '.auth.service_name')
  timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-revisions "$revision=100" --project "$project" --region "$region" --quiet >/dev/null || :
  json=$(gcloud run services describe "$service" --project "$project" --region "$region" --format=json --quiet) || { write_rollback_result auth unknown '{}'; return 2; }
  revision_json=$(gcloud run revisions describe "$revision" --project "$project" --region "$region" --format=json --quiet) || { write_rollback_result auth unknown '{}'; return 2; }
  observed=$(normalize_service_readback auth "$json" "$revision_json" "$revision" "$image") || { write_rollback_result auth failed '{}'; return 1; }
  if jq -n -e --argjson expected "$expected" --argjson observed "$observed" '$expected == $observed' >/dev/null; then write_rollback_result auth success "$observed"; else write_rollback_result auth failed "$observed"; return 1; fi
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
