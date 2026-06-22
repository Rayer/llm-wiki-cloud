#!/bin/bash
set -e
# Bucket mounted at /data by Cloud Run CSI volume mount
# Navigate to the correct project subdirectory based on env vars
USER_ID="${USER_ID:-test-user}"
PROJECT_ID="${PROJECT_ID:-demo}"
VAULT="/data/users/${USER_ID}/projects/${PROJECT_ID}"

if [ "$1" = "run" ]; then
    exec olw run --vault "$VAULT" --auto-approve
else
    exec olw "$1" --vault "$VAULT"
fi
