#!/bin/bash
set -e
# Bucket mounted at /data by Cloud Run CSI volume mount
# (configured at deploy time via --add-volume)
exec olw "${1:-run}" --vault /data ${1:+--auto-approve}
