#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$repo_root/.build/cac/local"
cat > "$repo_root/.build/cac/local/frontend-config.json" <<'JSON'
{"schema_version":1,"api_url":"http://127.0.0.1:8089","auth_url":"http://127.0.0.1:8089"}
JSON

node "$repo_root/docs/lwc-388/profile-fixture.mjs" &
fixture_pid=$!
trap 'kill "$fixture_pid" 2>/dev/null || true' EXIT

cd "$repo_root/apps/frontend"
NEXT_PUBLIC_CONFIG_URL=/frontend-config.json npm run dev -- --hostname 127.0.0.1
