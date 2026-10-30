#!/usr/bin/env bash
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
source "$ROOT/deploy/components/common.sh"

documentimport_build_submit() {
  local project image response
  project=$(plan_json '.gcp.project_id')
  image="$(plan_json '.gcp.artifact_registry')/llm-wiki-document-import-worker:$SOURCE_SHA"
  response=$(timeout --signal=TERM --kill-after=5s 600s gcloud builds submit "$ROOT" \
    --project "$project" --region=global \
    --config "$ROOT/apps/bff/cloudbuild-document-import-worker.yaml" \
    --substitutions="_IMAGE=$image,_SOURCE_SHA=$SOURCE_SHA" \
    --async --format=json --quiet)
  printf '%s\n' "$response"
}

documentimport_resolve_build_image() {
  local image digest
  image="$(plan_json '.gcp.artifact_registry')/llm-wiki-document-import-worker:$SOURCE_SHA"
  digest=$(gcloud artifacts docker images describe "$image" \
    --project "$(plan_json '.gcp.project_id')" \
    --format='value(image_summary.digest)' --quiet)
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "document import worker image digest is invalid"
  printf '%s@%s\n' "${image%:*}" "$digest"
}

case "${1:-}" in
  submit) documentimport_build_submit ;;
  resolve) documentimport_resolve_build_image ;;
  help) printf 'documentimport component: submit|resolve\n' ;;
  *) die "usage: documentimport.sh help|submit|resolve" ;;
esac
