#!/bin/bash
set -e
USER_ID="${USER_ID:-test-user}"
PROJECT_ID="${PROJECT_ID:-demo}"
VAULT="/data/users/${USER_ID}/projects/${PROJECT_ID}"

# Ensure OLW config exists with API key
mkdir -p ~/.config/olw
if [ -n "$DEEPSEEK_API_KEY" ] && [ ! -f ~/.config/olw/config.toml ]; then
    echo "api_key = \"${DEEPSEEK_API_KEY}\"" > ~/.config/olw/config.toml
fi

if [ "$1" = "run" ]; then
    olw run --vault "$VAULT" --auto-approve
    echo "OLW complete — regenerating concepts.jsonl..."
    VAULT="${VAULT}" python3 /regenerate-jsonl.py
else
    exec olw "$1" --vault "$VAULT"
fi
