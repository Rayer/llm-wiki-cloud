#!/bin/bash
set -e

# Mount GCS via FUSE
BUCKET="${BUCKET:-llm-wiki-data}"
USER_ID="${USER_ID:-test-user}"
PROJECT_ID="${PROJECT_ID:-demo}"
OLW_CMD="${1:-run}"
PREFIX="users/${USER_ID}/projects/${PROJECT_ID}"

mkdir -p /data
echo "Mounting ${BUCKET}/${PREFIX} → /data"
gcsfuse -o allow_other --implicit-dirs --only-dir "${PREFIX}" "${BUCKET}" /data

# Run OLW
echo "Running: olw ${OLW_CMD} --vault /data --auto-approve"
if [ "$OLW_CMD" = "run" ]; then
    exec olw "$OLW_CMD" --vault /data --auto-approve
else
    exec olw "$OLW_CMD" --vault /data
fi
