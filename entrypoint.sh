#!/bin/bash
set -e
USER_ID="${USER_ID:-test-user}"
PROJECT_ID="${PROJECT_ID:-demo}"
VAULT="/data/users/${USER_ID}/projects/${PROJECT_ID}"

# Ensure OLW config exists with API key
mkdir -p ~/.config/olw
if [ -n "$DEEPSEEK_API_KEY" ] && [ ! -f ~/.config/olw/config.toml ]; then
    echo "api_key = \"${DEEPSEEK_API_KEY}\"" > ~/.config/olw/config.toml
    echo "OLW config written"
fi

# Monkeypatch OLW healthcheck timeout: 5s → 30s (Cloud Run DNS/SSL is slow)
python3 -c "
from obsidian_llm_wiki.openai_compat_client import OpenAICompatClient
_orig = OpenAICompatClient.healthcheck
def patched(self):
    try:
        resp = self._client.get(self._models_url(), timeout=30)
        return resp.status_code in (200, 401)
    except Exception:
        return False
OpenAICompatClient.healthcheck = patched
print('Healthcheck timeout: 5s → 30s')
"

if [ "$1" = "run" ]; then
    olw run --vault "$VAULT" --auto-approve
    echo "OLW complete — regenerating concepts.jsonl..."
    VAULT="${VAULT}" python3 /regenerate-jsonl.py
else
    exec olw "$1" --vault "$VAULT"
fi
