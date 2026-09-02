#!/usr/bin/env bash
set -euo pipefail

BFF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../apps/bff" && pwd)"
FRONTEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../apps/frontend" && pwd)"
BFF_PORT="${BFF_PORT:-18080}"
AUTH_PORT="${AUTH_PORT:-18081}"
FRONTEND_PORT="${FRONTEND_PORT:-13000}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/lwc-306-smoke.XXXXXX")"
declare -a pids=()
env_file="$FRONTEND_DIR/.env.local"
env_backup="$tmp_dir/env.local"
had_env_file=false
cleanup_active=false

listeners() {
  lsof -nP -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null || true
}

stop_tree() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    stop_tree "$child"
  done
  kill -TERM "$pid" 2>/dev/null || true
}

force_tree() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    force_tree "$child"
  done
  kill -KILL "$pid" 2>/dev/null || true
}

cleanup() {
  local pid port remaining attempt alive cleanup_failed=false
  trap - EXIT INT TERM
  if [ "$cleanup_active" != true ]; then
    rm -rf "$tmp_dir"
    return 0
  fi
  for pid in "${pids[@]}"; do
    stop_tree "$pid"
  done
  for attempt in $(seq 1 40); do
    alive=false
    for pid in "${pids[@]}"; do
      if kill -0 "$pid" 2>/dev/null; then
        alive=true
      fi
    done
    [ "$alive" = false ] && break
    sleep 0.1
  done
  for pid in "${pids[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      force_tree "$pid"
    fi
    wait "$pid" 2>/dev/null || true
  done

  for port in "$BFF_PORT" "$AUTH_PORT" "$FRONTEND_PORT"; do
    remaining="$(listeners "$port")"
    if [ -n "$remaining" ]; then
      echo "smoke cleanup failed: port $port still has listener PID(s): $remaining" >&2
      cleanup_failed=true
    fi
  done

  if "$had_env_file"; then
    cp "$env_backup" "$env_file"
  else
    rm -f "$env_file"
  fi
  rm -rf "$tmp_dir"
  [ "$cleanup_failed" = false ]
}
trap cleanup EXIT INT TERM

for port in "$BFF_PORT" "$AUTH_PORT" "$FRONTEND_PORT"; do
  if [ -n "$(listeners "$port")" ]; then
    echo "smoke refused to start: port $port is already occupied" >&2
    exit 1
  fi
done

if [ -e "$env_file" ]; then
  cp "$env_file" "$env_backup"
  had_env_file=true
fi
cleanup_active=true
printf '%s\n' \
  "NEXT_PUBLIC_API_URL=http://127.0.0.1:$BFF_PORT" \
  "NEXT_PUBLIC_AUTH_URL=http://127.0.0.1:$AUTH_PORT" \
  'NEXT_PUBLIC_DEV_USER_ID=local-user' \
  'NEXT_PUBLIC_DEV_PROJECT_ID=demo' > "$env_file"
cp -R "$BFF_DIR/demo" "$tmp_dir/local-data"

unset GOOGLE_APPLICATION_CREDENTIALS GOOGLE_CLOUD_PROJECT GOOGLE_CLOUD_QUOTA_PROJECT GCP_PROJECT \
  DEEPSEEK_API_KEY LLM_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY VERCEL_TOKEN 2>/dev/null || true

(cd "$BFF_DIR" && \
  PORT="$BFF_PORT" LOCAL_DATA_DIR="$tmp_dir/local-data" DEV_JWT=true JWT_SECRET=dev-secret \
  go run ./cmd/bff --local "$tmp_dir/local-data") > "$tmp_dir/bff.log" 2>&1 &
pids+=("$!")
(cd "$BFF_DIR" && \
  PORT="$AUTH_PORT" LOCAL_DATA_DIR="$tmp_dir/local-data" DEV_JWT=true JWT_SECRET=dev-secret \
  go run ./cmd/auth --local "$tmp_dir/local-data") > "$tmp_dir/auth.log" 2>&1 &
pids+=("$!")
(cd "$FRONTEND_DIR" && NODE_ENV=development NEXT_TELEMETRY_DISABLED=1 \
  npm run dev -- --hostname 127.0.0.1 --port "$FRONTEND_PORT") > "$tmp_dir/frontend.log" 2>&1 &
pids+=("$!")

wait_for_http() {
  local name="$1" url="$2" log="$3" pid="$4" body="$tmp_dir/$1.body" attempt
  for attempt in $(seq 1 120); do
    if curl --fail --silent --show-error --max-time 1 "$url" > "$body"; then
      echo "$name ready: $url"
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "$name exited before readiness; log:" >&2
      sed -n '1,120p' "$log" >&2
      return 1
    fi
    sleep 0.25
  done
  echo "$name did not become ready: $url; log:" >&2
  sed -n '1,120p' "$log" >&2
  return 1
}

wait_for_http auth "http://127.0.0.1:$AUTH_PORT/api/v1/public/healthz" "$tmp_dir/auth.log" "${pids[1]}"
wait_for_http bff "http://127.0.0.1:$BFF_PORT/api/v1/public/version" "$tmp_dir/bff.log" "${pids[0]}"
wait_for_http frontend "http://127.0.0.1:$FRONTEND_PORT/" "$tmp_dir/frontend.log" "${pids[2]}"

login="$(curl --fail --silent --show-error --max-time 2 \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@llm-wiki.dev","password":"demo123456"}' \
  "http://127.0.0.1:$AUTH_PORT/api/v1/auth/login")"
token="$(printf '%s' "$login" | python3 -c 'import json, sys; value=json.load(sys.stdin); assert value.get("user", {}).get("id") == "local-user"; token=value.get("access_token"); assert isinstance(token, str) and token; print(token)')"
echo 'auth login useful: user=local-user access_token=present'

projects="$(curl --fail --silent --show-error --max-time 2 \
  -H "Authorization: Bearer $token" \
  "http://127.0.0.1:$BFF_PORT/api/v1/projects")"
printf '%s' "$projects" | python3 -c 'import json, sys; projects=json.load(sys.stdin); assert any(project.get("id") == "demo" for project in projects); print("bff scoped API useful: project=demo")'

python3 -c 'from pathlib import Path; body=Path(__import__("sys").argv[1]).read_text(); assert "LLM Wiki Cloud" in body and "<html" in body; print("frontend HTTP useful: title=LLM Wiki Cloud")' "$tmp_dir/frontend.body"
echo "smoke complete: ports $BFF_PORT $AUTH_PORT $FRONTEND_PORT cleaned up and verified free"
