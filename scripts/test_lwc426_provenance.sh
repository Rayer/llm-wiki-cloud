#!/usr/bin/env bash
set -euo pipefail

: "${TMPDIR:?TMPDIR must point at the supplied task scratch directory}"
: "${LOCAL_CLOUD_PYTHON:?Set LOCAL_CLOUD_PYTHON to the pinned Synto Python interpreter}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
scratch="$(mktemp -d "$TMPDIR/lwc426-provenance.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
python_dir="$(dirname "$LOCAL_CLOUD_PYTHON")"
api_response="$scratch/concept-detail.json"

(
  cd "$repo_root/apps/bff"
  env \
    -u LLM_API_KEY \
    -u DEEPSEEK_API_KEY \
    -u SYNTO_API_KEY \
    -u OPENAI_API_KEY \
    -u ANTHROPIC_API_KEY \
    -u GEMINI_API_KEY \
    -u TYPESAFE_API_KEY \
    -u TYPESAFE_JEV_API_KEY \
    -u GOOGLE_APPLICATION_CREDENTIALS \
    TMPDIR="$scratch" \
    PYTHONDONTWRITEBYTECODE=1 \
    PATH="$python_dir:$PATH" \
    LOCAL_CLOUD_PYTHON="$LOCAL_CLOUD_PYTHON" \
    LWC426_API_RESPONSE_OUTPUT="$api_response" \
    go test -tags lwc_local_synto_runtime ./cmd/olw_worker -run '^TestLocalSyntoProvenanceRoundTripsThroughPublicationAndBFF$' -count=1 -v
)

(
  cd "$repo_root/apps/frontend"
  TMPDIR="$scratch" PYTHONDONTWRITEBYTECODE=1 LWC426_API_RESPONSE_PATH="$api_response" ./node_modules/.bin/vitest run tests/lwc-426-provenance.test.tsx
)
