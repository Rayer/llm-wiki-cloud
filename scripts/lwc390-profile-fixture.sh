#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
route_dir="$repo_root/apps/frontend/src/app/lwc390-local-fixture"
route_file="$route_dir/page.tsx"

if [ -e "$route_file" ]; then
  printf '%s\n' "Refusing to replace existing fixture route: $route_file" >&2
  exit 2
fi

mkdir -p "$route_dir"
cat > "$route_file" <<'EOF'
export { default } from '../../../tests/fixtures/lwc-390-profile-fixture';
EOF

cleanup() {
  rm -f "$route_file"
  rmdir "$route_dir" 2>/dev/null || true
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' 'Local mocked Profile fixture: http://127.0.0.1:4173/lwc390-local-fixture'
NEXT_PUBLIC_CONFIG_URL=http://127.0.0.1:4173/frontend-config.json \
  npm --prefix "$repo_root/apps/frontend" run dev -- --hostname 127.0.0.1 --port 4173
