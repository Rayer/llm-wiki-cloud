#!/usr/bin/env bash
# Shared by Auth and BFF; Query selection is managed in both BFF environments.
auth_config_managed() {
  local component="${1:-auth}"
  if [[ "$component" != bff ]]; then
    jq -e '.normalized.auth.google != null' "$PLAN_PATH" >/dev/null || return 1
  fi
  [[ "$ENVIRONMENT" == development || "$ENVIRONMENT" == production ]] &&
    [[ "$(plan_json '.environment')" == "$ENVIRONMENT" ]] || die "managed config environment mismatch"
  [[ "$component" == auth || "$component" == bff ]]
}

auth_config_readback() {
  local mode="$1" revision="$2" image="$3" fingerprint="${4:-}" component="${5:-auth}" raw
  raw=$(gcloud run revisions describe "$revision" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --format=json --quiet) || return 2
  python3 "$ROOT/deploy/components/auth_config.py" "$mode" "$PLAN_PATH" "$component" "$revision" "$image" "$fingerprint" <<<"$raw"
}

auth_config_freeze() {
  local image="$1" component="${2:-auth}" observed revision
  observed=$(service_image_readback "$component" "$image") || return 1
  revision=$(jq -er '.revision' <<<"$observed") || return 1
  auth_config_readback freeze "$revision" "$image" "" "$component"
}

auth_config_mutate() {
  local image="$1" component="${2:-auth}" revision flags arg
  local -a config_args=()
  flags=$(python3 "$ROOT/deploy/components/auth_config.py" args "$PLAN_PATH" "$component") || return 1
  while IFS= read -r arg; do config_args+=("$arg"); done <<<"$flags"
  # Capture the created revision directly, never race a subsequent "latest" lookup.
  if ! revision=$(timeout --signal=TERM --kill-after=5s 600s gcloud run services update "$service" --project "$project" --region "$region" --image "$image" --no-traffic "${config_args[@]}" --format='value(status.latestCreatedRevisionName)' --quiet); then
    journal_transition "$component" unknown
    return 1
  fi
  [[ "$revision" == "$service-"* && "$revision" =~ ^[a-z0-9-]+$ ]] || { journal_transition "$component" unknown; return 1; }
  if ! readback_retry auth_config_readback verify "$revision" "$image" "" "$component" > /dev/null; then
    journal_transition "$component" unknown
    return 1
  fi
  # Preserve the frozen traffic until exact candidate configuration is verified.
  revalidate_before_provider
  if ! timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$service" --to-revisions "$revision=100" --project "$project" --region "$region" --quiet >/dev/null; then
    journal_transition "$component" unknown
    return 1
  fi
  if ! readback_retry "${component}_verify" "$image" "$revision"; then
    journal_transition "$component" unknown
    return 1
  fi
  if [[ "$ENVIRONMENT" == production ]]; then mutation_accepted "$component"; fi
}

auth_config_rollback() {
  local image="$1" component="${2:-auth}" revision fingerprint observed readback_status
  revision=$(jq -er --arg component "$component" '.handles[$component].revision | select(test("^[a-z0-9-]+$"))' "$ROLLBACK_PATH") || return 1
  fingerprint=$(jq -er --arg component "$component" '.handles[$component].config_fingerprint | select(test("^sha256:[0-9a-f]{64}$"))' "$ROLLBACK_PATH") || return 1
  [[ "$revision" == "$(plan_json ".$component.service_name")-"* ]] || return 1
  # Immutable retained revision restores both code and configuration, including
  # absent settings; no credential values need be copied into rollback artifacts.
  if observed=$(auth_config_readback rollback "$revision" "$image" "$fingerprint" "$component"); then :; else
    readback_status=$?
    if [[ "$readback_status" -eq 1 ]]; then write_rollback_result "$component" failed '{}'; else write_rollback_result "$component" unknown '{}'; fi
    return "$readback_status"
  fi
  if ! service_image_readback "$component" "$image" "$revision" >/dev/null; then
    if ! timeout --signal=TERM --kill-after=5s 240s gcloud run services update-traffic "$(plan_json ".$component.service_name")" --to-revisions "$revision=100" --project "$(plan_json '.gcp.project_id')" --region "$(plan_json '.gcp.region')" --quiet >/dev/null; then
      write_rollback_result "$component" unknown '{}'; return 2
    fi
  fi
  if service_image_readback "$component" "$image" "$revision" >/dev/null && observed=$(auth_config_readback rollback "$revision" "$image" "$fingerprint" "$component"); then
    write_rollback_result "$component" success "$observed"
  else
    write_rollback_result "$component" unknown '{}'; return 2
  fi
}
