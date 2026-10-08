#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
git_dir="$(git -C "$repo_root" rev-parse --absolute-git-dir)"
state_dir="$git_dir/lwc361-local-cloud"
mkdir -p "$state_dir" "$state_dir/bin"
chmod 700 "$state_dir"

if [ ! -s "$state_dir/scope" ]; then
  scope="worktree-$(openssl rand -hex 12)"
  temp="$state_dir/.scope.$$"
  (umask 077; printf '%s\n' "$scope" > "$temp")
  ln "$temp" "$state_dir/scope" 2>/dev/null || true
  rm -f "$temp"
fi
scope="$(cat "$state_dir/scope")"
case "$scope" in
  worktree-[a-f0-9][a-f0-9]*) ;;
  *) echo "invalid local cloud scope metadata" >&2; exit 1 ;;
esac

secret_path="$state_dir/jwt-secret"
if [ ! -s "$secret_path" ]; then
  temp="$state_dir/.jwt-secret.$$"
  (umask 077; openssl rand -hex 32 > "$temp")
  chmod 600 "$temp"
  ln "$temp" "$secret_path" 2>/dev/null || true
  rm -f "$temp"
fi
chmod 600 "$secret_path"

for pair in "GCP_PROJECT=llm-wiki-cloud" "GOOGLE_CLOUD_PROJECT=llm-wiki-cloud" "BUCKET=llm-wiki-cloud-local" "FIRESTORE_DATABASE_ID=llm-wiki-cloud-local"; do
  name="${pair%%=*}"
  expected="${pair#*=}"
  current="${!name:-}"
  if [ -n "$current" ] && [ "$current" != "$expected" ]; then
    echo "local cloud target conflict for $name" >&2
    exit 1
  fi
done

BFF_PORT="${BFF_PORT:-8080}"
AUTH_PORT="${AUTH_PORT:-8081}"
FRONTEND_PORT="${FRONTEND_PORT:-3000}"
for port in "$BFF_PORT" "$AUTH_PORT" "$FRONTEND_PORT"; do
  case "$port" in ''|*[!0-9]*) echo "local service ports must be numeric" >&2; exit 1;; esac
  if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then echo "local service port is out of range" >&2; exit 1; fi
done
LOCAL_CLOUD_WORKER_PATH="$state_dir/bin/olw_worker"
LOCAL_CLOUD_STATE_DIR="$state_dir"
LOCAL_CLOUD_REPO_ROOT="$repo_root"
LOCAL_CLOUD_PYTHON="$state_dir/python/bin/python3"
LOCAL_CLOUD_PIPELINE_CONFIG_DIR="${LOCAL_CLOUD_PIPELINE_CONFIG_DIR:-$repo_root/.build/cac/local}"
LOCAL_CLOUD_PIPELINE_CONFIG_PATH="$LOCAL_CLOUD_PIPELINE_CONFIG_DIR/synto.toml"
LOCAL_CLOUD_PIPELINE_BINDINGS_PATH="$LOCAL_CLOUD_PIPELINE_CONFIG_DIR/private-bindings.json"
LOCAL_CLOUD_BFF_CONFIG_PATH="$LOCAL_CLOUD_PIPELINE_CONFIG_DIR/bff.json"
LOCAL_CLOUD_SCOPE="$scope"
LOCAL_CLOUD_JWT_SECRET_FILE="$secret_path"
PATH="$state_dir/python/bin:$PATH"
GCP_PROJECT="llm-wiki-cloud"
GOOGLE_CLOUD_PROJECT="llm-wiki-cloud"
BUCKET="llm-wiki-cloud-local"
FIRESTORE_DATABASE_ID="llm-wiki-cloud-local"
ALLOWED_ORIGINS="http://localhost:$FRONTEND_PORT"
ALLOWED_HOSTS="localhost,127.0.0.1"
AUTH_SERVICE_URL="http://localhost:$AUTH_PORT"
NEXT_PUBLIC_API_URL="http://localhost:$BFF_PORT"
NEXT_PUBLIC_AUTH_URL="http://localhost:$AUTH_PORT"
unset AUTH_DEMO_USER_ID AUTH_DEMO_USER_EMAIL AUTH_DEMO_USER_ROLE PIPELINE_DEMO_USER_IDS
local_demo_config="${LOCAL_DEMO_CONFIG_PATH:-$repo_root/.build/cac/local/local_demo.json}"
if [ -s "$local_demo_config" ]; then
  if demo_values="$(python3 - "$local_demo_config" <<'PY'
import json, re, sys

try:
    demo = json.load(open(sys.argv[1], encoding="utf-8"))
    user_id, email, role = demo["user_id"], demo["email"], demo["role"]
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", user_id):
        raise ValueError()
    if not re.fullmatch(r"[^@\s]+@[^@\s]+\.[^@\s]+", email):
        raise ValueError()
    if not re.fullmatch(r"[a-z][a-z0-9_-]{0,31}", role) or role == "admin":
        raise ValueError()
except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError):
    sys.exit(1)

print("\t".join((user_id, email, role)))
PY
  )"; then
    IFS=$'\t' read -r AUTH_DEMO_USER_ID AUTH_DEMO_USER_EMAIL AUTH_DEMO_USER_ROLE <<< "$demo_values"
    PIPELINE_DEMO_USER_IDS="$AUTH_DEMO_USER_ID"
    export AUTH_DEMO_USER_ID AUTH_DEMO_USER_EMAIL AUTH_DEMO_USER_ROLE PIPELINE_DEMO_USER_IDS
  else
    printf 'local Demo identity config unavailable; Demo login will stay disabled\n' >&2
  fi
fi
unset LOCAL_DEMO_CONFIG_PATH local_demo_config demo_values
export BFF_PORT AUTH_PORT FRONTEND_PORT
export LOCAL_CLOUD_WORKER_PATH LOCAL_CLOUD_STATE_DIR LOCAL_CLOUD_REPO_ROOT LOCAL_CLOUD_PYTHON
export LOCAL_CLOUD_PIPELINE_CONFIG_DIR LOCAL_CLOUD_PIPELINE_CONFIG_PATH LOCAL_CLOUD_PIPELINE_BINDINGS_PATH LOCAL_CLOUD_BFF_CONFIG_PATH PATH
export LOCAL_CLOUD_SCOPE LOCAL_CLOUD_JWT_SECRET_FILE GCP_PROJECT GOOGLE_CLOUD_PROJECT BUCKET FIRESTORE_DATABASE_ID
export ALLOWED_ORIGINS ALLOWED_HOSTS AUTH_SERVICE_URL NEXT_PUBLIC_API_URL NEXT_PUBLIC_AUTH_URL
# Old switches and shared/deployed JWT configuration never flow into local app processes.
unset DEV_JWT LOCAL_DATA_DIR JWT_SECRET

case "${1:-}" in
  --worker-path) printf '%s\n' "$LOCAL_CLOUD_WORKER_PATH"; exit 0 ;;
  --state-dir) printf '%s\n' "$LOCAL_CLOUD_STATE_DIR"; exit 0 ;;
  --summary)
    printf 'target project=%s bucket=%s database=%s scope=%s\n' "$GCP_PROJECT" "$BUCKET" "$FIRESTORE_DATABASE_ID" "$LOCAL_CLOUD_SCOPE"
    printf 'URLs frontend=http://localhost:%s bff=http://localhost:%s auth=http://localhost:%s\n' "$FRONTEND_PORT" "$BFF_PORT" "$AUTH_PORT"
    exit 0
    ;;
  --configure-frontend)
    frontend_dir="${2:?frontend directory required}"
    mkdir -p "$frontend_dir"
    (umask 077; printf 'NEXT_PUBLIC_API_URL=%s\nNEXT_PUBLIC_AUTH_URL=%s\n' "$NEXT_PUBLIC_API_URL" "$NEXT_PUBLIC_AUTH_URL" > "$frontend_dir/.env.local")
    printf 'configured %s/.env.local for the current worktree\n' "$frontend_dir"
    exit 0
    ;;
  --)
    shift
    if [ "$#" -eq 0 ]; then echo "command required after --" >&2; exit 2; fi
    prepare_bff_projection=false
    direct_bff=false
    if [ "$#" -ge 3 ] && [ "$1" = "go" ] && [ "$2" = "run" ] && [ "$3" = "./cmd/bff" ]; then
      prepare_bff_projection=true
      direct_bff=true
    elif [ "$#" -ge 3 ] && [ "$1" = "python3" ] && [ "${2##*/}" = "local-services.py" ] && [ "$3" = "start" ]; then
      for service in "$@"; do
        if [ "$service" = "bff" ]; then prepare_bff_projection=true; break; fi
      done
    fi
    if [ "$prepare_bff_projection" = true ]; then
      (
        cd "$repo_root/apps/bff"
        LWC_REPOSITORY_ROOT="$repo_root" go run ./cmd/pipeline_config prepare --target bff --environment local --output "$LOCAL_CLOUD_PIPELINE_CONFIG_DIR"
      )
      export LWC_BFF_CONFIG_PATH="$LOCAL_CLOUD_BFF_CONFIG_PATH"
      if [ "$direct_bff" = true ]; then
        unset BFF_PORT AUTH_PORT FRONTEND_PORT GCP_PROJECT BUCKET FIRESTORE_DATABASE_ID
        unset LOCAL_CLOUD_SCOPE LOCAL_CLOUD_JWT_SECRET_FILE LOCAL_CLOUD_WORKER_PATH
        unset LOCAL_CLOUD_PIPELINE_CONFIG_PATH LOCAL_CLOUD_PIPELINE_BINDINGS_PATH
        unset LOCAL_CLOUD_BFF_CONFIG_PATH LOCAL_CLOUD_STATE_DIR LOCAL_CLOUD_REPO_ROOT LOCAL_CLOUD_PYTHON
        unset LOCAL_CLOUD_PIPELINE_CONFIG_DIR ALLOWED_ORIGINS ALLOWED_HOSTS AUTH_SERVICE_URL
        unset AUTH_DEMO_USER_ID AUTH_DEMO_USER_EMAIL AUTH_DEMO_USER_ROLE PIPELINE_DEMO_USER_IDS
        unset PIPELINE_DAILY_LIMIT PIPELINE_COOLDOWN_SECONDS PIPELINE_MIN_NEW_RAW PIPELINE_JOB_URL
        unset EXPORT_JOB_URL EXPORT_SIGNING_SERVICE_ACCOUNT AUTH_SESSION_ENVIRONMENT AUTH_REFRESH_SESSION_MIGRATION REGISTRATION_ENABLED
        unset QUERY_STAGE_CONFIG_PATH QUERY_EXPANSION_MODEL QUERY_EXPANSION_REASONING
        unset ANSWER_SYNTHESIS_MODEL ANSWER_SYNTHESIS_REASONING QUERY_SELECTION_LIMIT
        unset QUERY_SELECTION_EXPLORATION_SLOTS QUERY_SELECTION_EVIDENCE_THRESHOLD
        unset QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT QUERY_EXPANSION_ATTEMPTS
        unset QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY
        unset JWT_SECRET DEEPSEEK_API_KEY LLM_API_KEY TYPESAFE_API_KEY TYPESAFE_JEV_API_KEY
        unset PROFILE_RUNTIME_AUDIENCE PROFILE_RUNTIME_SERVICE_ACCOUNT DEV_JWT LOCAL_DATA_DIR
        unset GOOGLE_CLIENT_ID GOOGLE_CLIENT_SECRET GOOGLE_ISSUER GOOGLE_JWKS_URL GOOGLE_TOKEN_URL
        unset GOOGLE_LOGIN_REDIRECT_URL GOOGLE_LINK_REDIRECT_URL GOOGLE_COMPLETION_URL
      fi
    fi
    exec "$@"
    ;;
  *)
    echo "usage: $0 --summary|--worker-path|--state-dir|--configure-frontend DIR|-- COMMAND [ARGS...]" >&2
    exit 2
    ;;
esac
