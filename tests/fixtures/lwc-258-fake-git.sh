#!/usr/bin/env bash
set -Eeo pipefail
case "$*" in
  "rev-parse HEAD") printf '%s\n' "$FAKE_HEAD_SHA" ;;
  "ls-remote origin refs/heads/main") printf '%s\trefs/heads/main\n' "${FAKE_REMOTE_MAIN_SHA:-$FAKE_HEAD_SHA}" ;;
  *) exit 99 ;;
esac
