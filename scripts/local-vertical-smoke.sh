#!/usr/bin/env bash
set -euo pipefail

BFF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../apps/bff" && pwd)"
cd "$BFF_DIR"

run_offline_test() {
  env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY \
    -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY \
    -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY "$@"
}

# This smoke stays on loopback. Its native worker subprocess test runs only
# when both explicitly configured emulator endpoints are loopback addresses;
# otherwise Go reports it as skipped. It does not modify .env.local, inspect or
# kill other port owners, or invoke a paid LLM provider.
run_offline_test go test ./cmd/bff -run '^TestLocalCloudLoopbackUsesBearerAndIgnoresUserHeaderIdentity$' -count=1
run_offline_test go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1
run_offline_test go test ./internal/auth -run '^TestLocalRefreshCookiePolicySupportsLoopbackHTTP$' -count=1
run_offline_test go test ./internal/localcloud ./internal/firestore ./internal/gcs ./internal/localpipeline -run 'Scope|WorkerArgs' -count=1
run_offline_test make local-synto-runtime-test
run_offline_test python3 -m unittest scripts.test_local_dev_makefile -v
run_offline_test python3 -m unittest discover -s ../../scripts -p 'test_local_vertical_smoke.py' -v

printf '%s\n' 'loopback/auth-boundary and cloud-scope smoke complete'
