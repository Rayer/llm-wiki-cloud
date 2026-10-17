#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf '%s\n' "$1" >&2
  exit 2
}

[[ "${LWC_OTLP_CLOUD_QA:-}" == "1" ]] || fail "Set LWC_OTLP_CLOUD_QA=1 for the Main-owned Cloud Trace run."
[[ "${OTEL_EXPORTER_OTLP_ENDPOINT:-}" == "https://telemetry.googleapis.com" ]] || fail "OTEL_EXPORTER_OTLP_ENDPOINT must be the approved Cloud Trace endpoint."
[[ -n "${LWC_OTLP_QA_PROJECT:-}" ]] || fail "Set LWC_OTLP_QA_PROJECT to the approved Development project."
[[ -n "${LWC_OTLP_QA_REVISION:-}" ]] || fail "Set LWC_OTLP_QA_REVISION to the reviewed source SHA."
[[ -n "${FIRESTORE_EMULATOR_HOST:-}" ]] || fail "UC5 requires an owned loopback Firestore emulator."
[[ -n "${STORAGE_EMULATOR_HOST:-}" ]] || fail "UC5 requires an owned loopback Storage emulator."

python3 - "$FIRESTORE_EMULATOR_HOST" "$STORAGE_EMULATOR_HOST" <<'PY'
import ipaddress
import socket
import sys
from urllib.parse import urlsplit

for endpoint in sys.argv[1:]:
    parsed = urlsplit(endpoint if "://" in endpoint else "//" + endpoint)
    if parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        raise SystemExit("UC5 emulator endpoint must be a bare loopback host and port")
    host, port = parsed.hostname, parsed.port
    try:
        loopback = host == "localhost" or ipaddress.ip_address(host).is_loopback
    except (TypeError, ValueError):
        loopback = False
    if not loopback or not port:
        raise SystemExit("UC5 emulator endpoint must use a loopback IP and explicit port")
    try:
        with socket.create_connection((host, port), timeout=1):
            pass
    except OSError as exc:
        raise SystemExit("UC5 loopback emulator is not accepting local connections") from exc
print("OTLP_QA_EMULATOR_PREFLIGHT=firestore_and_storage_loopback_ready")
PY

export LWC_OTLP_CLOUD_QA=1
export OTEL_EXPORTER_OTLP_ENDPOINT=https://telemetry.googleapis.com

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/lwc398-otlp-cloud-qa.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

run_case() {
  local case_name="$1"
  local package="$2"
  local pattern="$3"
  local log="$tmpdir/$case_name.log"
  printf 'OTLP_QA_COMMAND case=%s package=%s pattern=%s\n' "$case_name" "$package" "$pattern"
  if go test -count=1 -v -run "$pattern" "$package" >"$log" 2>&1; then
    printf 'OTLP_QA_COMMAND case=%s exit=0\n' "$case_name"
  else
    printf 'OTLP_QA_COMMAND case=%s exit=1\n' "$case_name" >&2
    tail -n 40 "$log" >&2
    exit 1
  fi
}

run_case UC1 ./cmd/auth '^TestOTLPCaptureAuthSafeMetadataAndRejectOutcomes$'
run_case UC2 ./internal/observability '^TestOTLPTransportUseCaseRealHTTPAndConcurrentIsolation$'
run_case UC3_UC4 ./internal/handler/v1 '^TestOTLPCaptureActualQuerySuccessAndLogicalFallback$'
run_case UC5_NATIVE ./cmd/bff '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$'
run_case UC5_PROFILE ./internal/handler/v1 '^TestOTLPCaptureProfileDispatcherActualClaimAndWork$'
run_case UC6 ./internal/exportjob '^TestOTLPCaptureExportHTTPToRunnerLocalSyntheticObjectStore$'
run_case UC6_JOB_OVERRIDE ./internal/exportjob '^TestCloudRunStarterSendsOnlyBoundedJobIdentity$'

if [[ -n "${LWC_OTLP_QA_MANIFEST:-}" ]]; then
  [[ ! -e "$LWC_OTLP_QA_MANIFEST" ]] || fail "LWC_OTLP_QA_MANIFEST already exists; refusing to overwrite it."
  python3 scripts/otlp_cloud_trace_manifest.py "$tmpdir" >"$LWC_OTLP_QA_MANIFEST"
else
  python3 scripts/otlp_cloud_trace_manifest.py "$tmpdir"
fi
