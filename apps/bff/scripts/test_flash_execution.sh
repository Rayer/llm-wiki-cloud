#!/bin/sh
# Same exact wheel URL+hash as the production image; no provider credentials.
set -eu
cd "$(dirname "$0")/.."
if [ -n "${SYNTO_TEST_PYTHON:-}" ]; then
    exec "$SYNTO_TEST_PYTHON" cmd/olw_worker/testdata/flash_execution_smoke.py
fi
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
python3 -m venv "$test_dir/venv"
wheel=$(python3 -c 'import pathlib,re; text = pathlib.Path("cmd/olw_worker/Dockerfile").read_text(); url = re.search(r"^ARG SYNTO_ARTIFACT_URL=(\S+)$", text, re.M).group(1); digest = re.search(r"^ARG SYNTO_SHA256=([0-9a-f]{64})$", text, re.M).group(1); print(url + "#sha256=" + digest)')
"$test_dir/venv/bin/pip" install --disable-pip-version-check "$wheel"
"$test_dir/venv/bin/python" cmd/olw_worker/testdata/flash_execution_smoke.py
