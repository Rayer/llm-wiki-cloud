#!/bin/bash
set -e
# Bucket mounted at /data by Cloud Run CSI volume mount
USER_ID="${USER_ID:-test-user}"
PROJECT_ID="${PROJECT_ID:-demo}"
VAULT="/data/users/${USER_ID}/projects/${PROJECT_ID}"

if [ "$1" = "run" ]; then
    olw run --vault "$VAULT" --auto-approve
    echo "OLW complete — regenerating concepts.jsonl..."
    python3 /regenerate-jsonl.py
else
    exec olw "$1" --vault "$VAULT"
fi
