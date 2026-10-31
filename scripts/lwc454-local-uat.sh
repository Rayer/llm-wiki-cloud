#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
scratch=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch
fixture_port=${LWC454_FIXTURE_PORT:-4174}
frontend_config="$repo_root/.build/cac/local/frontend-config.json"
public_dir="$repo_root/apps/frontend/public"
entry_page="$public_dir/lwc454-local-entry.html"
fixture_pid=
entry_created=0
config_created=0

case "$fixture_port" in
  ''|*[!0-9]*) printf '%s\n' 'LWC454_FIXTURE_PORT must be a numeric port.' >&2; exit 1 ;;
esac
if [ -e "$entry_page" ] || [ -L "$entry_page" ]; then
  printf '%s\n' "Refusing to overwrite existing local entry page: $entry_page" >&2
  exit 1
fi
if [ -e "$frontend_config" ] || [ -L "$frontend_config" ]; then
  printf '%s\n' "Refusing to overwrite existing frontend config: $frontend_config" >&2
  exit 1
fi

mkdir -p "$scratch" "$repo_root/.build/cac/local" "$repo_root/.build/local-uat/fixtures" "$public_dir"
export TMPDIR="$scratch"

cleanup() {
  if [ -n "$fixture_pid" ]; then kill "$fixture_pid" 2>/dev/null || true; fi
  if [ "$config_created" -eq 1 ]; then rm -f "$frontend_config"; fi
  if [ "$entry_created" -eq 1 ] && [ -f "$entry_page" ] && \
    grep -q '^<!-- LWC454_LOCAL_SYNTHETIC_ENTRY_GENERATED -->$' "$entry_page"; then
    rm -f "$entry_page"
  fi
}
trap cleanup EXIT INT TERM

cat > "$frontend_config" <<EOF
{"schema_version":1,"api_url":"http://127.0.0.1:$fixture_port","auth_url":"http://127.0.0.1:$fixture_port"}
EOF
config_created=1

if (set -C; cat > "$entry_page" <<EOF
<!-- LWC454_LOCAL_SYNTHETIC_ENTRY_GENERATED -->
<!doctype html>
<html lang="zh-TW">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>LWC-454 本機合成預覽</title>
  <style>
    :root { color-scheme: dark; font: 16px/1.55 system-ui, -apple-system, sans-serif; background: #09090b; color: #f4f4f5; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px; }
    main { width: min(100%, 720px); border: 1px solid #52525b; border-radius: 18px; background: #18181b; padding: clamp(24px, 5vw, 48px); box-shadow: 0 20px 60px #0008; }
    .lab { margin: 0 0 28px; border: 2px solid #fb7185; border-radius: 10px; background: #4c0519; color: #fff1f2; padding: 14px 16px; font-weight: 800; letter-spacing: .02em; }
    h1 { margin: 0 0 12px; font-size: clamp(28px, 6vw, 40px); }
    p { color: #d4d4d8; }
    code { overflow-wrap: anywhere; border-radius: 4px; background: #27272a; padding: 2px 5px; color: #e4e4e7; }
    button { min-height: 52px; margin-top: 12px; border: 0; border-radius: 10px; padding: 12px 20px; background: #6ee7b7; color: #052e2b; font: inherit; font-weight: 800; cursor: pointer; }
    button:focus-visible { outline: 3px solid #f9a8d4; outline-offset: 4px; }
    button:disabled { cursor: progress; opacity: .7; }
    #status { min-height: 1.6em; color: #fda4af; }
  </style>
</head>
<body>
  <main>
    <p id="lwc454-local-fixture-banner" class="lab" role="note">LOCAL SYNTHETIC LAB ONLY · 僅限本機合成資料 · 不會呼叫雲端或模型服務</p>
    <h1>本機合成預覽</h1>
    <p>此入口只會載入一組固定的本機測試身份與 fixture Project，並進入實際產品首頁。它不會建立帳戶、不會要求密碼或 API key，也不會連到 DEV、雲端或 provider。</p>
    <ul>
      <li>測試身份：<code>fixture-user-1</code> · <code>fixture@example.invalid</code></li>
      <li>測試 Project：<code>fixture-project-1</code></li>
      <li>介面語言：繁體中文（zh-TW）</li>
      <li>本機 fixture API：<code>http://127.0.0.1:$fixture_port</code></li>
    </ul>
    <button id="lwc454-enter-preview" type="button" aria-label="Enter local synthetic preview">載入合成本機資料並開啟產品</button>
    <p id="status" role="status" aria-live="polite"></p>
  </main>
  <script>
    const button = document.getElementById('lwc454-enter-preview');
    const status = document.getElementById('status');
    button.addEventListener('click', async () => {
      if (location.hostname !== '127.0.0.1') {
        status.textContent = '拒絕載入：此入口只允許 127.0.0.1 本機預覽。';
        return;
      }
      button.disabled = true;
      status.textContent = '正在重設本機合成 fixture…';
      try {
        const response = await fetch('http://127.0.0.1:$fixture_port/__fixture/state', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ reset: true, bootstrap: 'ready' }),
        });
        if (!response.ok) throw new Error('本機 fixture API 尚未就緒。');
        localStorage.clear();
        localStorage.setItem('llm-wiki-access-token', 'fixture-token-only');
        localStorage.setItem('llm-wiki-auth-user', JSON.stringify({ id: 'fixture-user-1', email: 'fixture@example.invalid' }));
        localStorage.setItem('llm-wiki-last-project', 'fixture-project-1');
        localStorage.setItem('locale', 'zh-TW');
        status.textContent = '合成本機資料已載入，正在開啟產品首頁…';
        location.assign('/');
      } catch (error) {
        status.textContent = error instanceof Error ? error.message : '無法載入本機 fixture。';
        button.disabled = false;
      }
    });
  </script>
</body>
</html>
EOF
) 2>/dev/null; then
  entry_created=1
else
  printf '%s\n' "Refusing to overwrite existing local entry page: $entry_page" >&2
  exit 1
fi

node "$repo_root/scripts/lwc454-local-api-fixture.mjs" > "$repo_root/.build/local-uat/api.log" 2>&1 &
fixture_pid=$!

ready=0
for attempt in $(seq 1 50); do
  if curl --silent --fail "http://127.0.0.1:$fixture_port/__fixture/state" >/dev/null; then ready=1; break; fi
  sleep 0.1
done
if [ "$ready" -ne 1 ]; then
  printf '%s\n' "Local fixture API did not start on 127.0.0.1:$fixture_port" >&2
  exit 1
fi

printf '%s\n' "LOCAL SYNTHETIC FIXTURE ONLY · entry: http://127.0.0.1:4173/lwc454-local-entry.html · UI: http://127.0.0.1:4173 · API: http://127.0.0.1:$fixture_port"
NEXT_PUBLIC_CONFIG_URL=http://127.0.0.1:4173/frontend-config.json \
  npm --prefix "$repo_root/apps/frontend" run dev -- --hostname 127.0.0.1 --port 4173
