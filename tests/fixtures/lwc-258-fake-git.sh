#!/usr/bin/env bash
set -Eeo pipefail
case "$*" in
  "rev-parse HEAD") printf '%s\n' "$FAKE_HEAD_SHA" ;;
  "ls-remote origin refs/heads/develop") printf '%s\trefs/heads/develop\n' "$FAKE_REMOTE_DEVELOP_SHA" ;;
  *) exit 99 ;;
esac
