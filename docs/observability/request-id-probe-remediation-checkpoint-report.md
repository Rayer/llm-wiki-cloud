# LWC-402 B local remediation checkpoint

**Task:** `task_ef39b1e77ff2` / **Dispatch:** `ctx_e5cdd720765b`
**Baseline:** `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`
**Disposition:** Local offline implementation is ready for Main's independent source review. **Live DEV is NOT RUN**; this is not LWC-402 ticket completion, a LWC-400/UAT verification, publication, merge, or deployment.

The official result in `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/A-repair-review-result.json` clears the repaired A source only. The earlier A HOLD remains part of the historical record, and this report does not qualify B.

## Implemented

- `scripts/verify_request_id_trace.py` uses the shared `trace_lookup` core. Before any product request, it fails closed unless the query project matches the static allowlist, the application key is present, the Harbor fixture readiness and project marker are verified, and a read-only Logs/Trace access preflight succeeds. A missing fixture or configuration emits a nonzero `NOT RUN` report.
- The three required cases capture server IDs from their real response locations: REST `X-Request-ID`; MCP envelope `X-Request-ID` separately from `result._meta.request_id` and matching `structuredContent.diagnostic_id`; and Query `X-Request-ID`. The MCP case requires HTTP 200 plus `isError: true`, terminal log evidence, owning spans, service/revision, and finite outcomes. Query uses `mode=full` and requires `ai_synth` to contain `20:00`, a result for the Harbor Library source title, and a citation with the same source ID.
- Pipeline stays disabled by default. Explicit use requires a statically allowlisted project, an independent supported bearer token, bounded polling to a terminal provider state, and distinct worker trace evidence correlated through the shared core's async link. The query-only application key cannot authenticate Pipeline, even for the same project. Native stored-Link verification remains `MANUAL`.
- The JSON summary filters IDs, span/operation labels, service/revision, finite outcomes and status, rebuilds Console URLs from validated trace IDs, and omits response bodies, query answer text, credentials, and object paths. The DEV-only `workflow_dispatch` is branch-guarded to `develop`, uses the `Development` environment, explicit read/id-token permissions and the existing WIF secret names, and uploads the sanitized JSON artifact. Offline CI now discovers the B contract suite.
- `scripts/fixtures/request_id_probe/sanitized.json` derives the answer and source title from `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/B-harbor-library-fixture-source.md`. The static query/pipeline project allowlists remain unset, fixture readiness is `not_verified`, and historical project `75ce0901c17b` is recorded for reference only. The exact new project key/ID and optional pipeline token names are unconfigured; the existing WIF secret names were checked, but their IAM permissions were not.

## Source proof inspected

- `apps/bff/internal/observability/request_lifecycle.go:22,123,144-152,155-195` sets the server-owned HTTP request ID header and emits the terminal status/outcome.
- `apps/bff/internal/handler/v1/mcp.go:212-251,254-256` keeps the HTTP envelope ID distinct from the tool-call ID in `_meta.request_id`, and sets the native error outcome. `apps/bff/internal/handler/v1/query_diagnostics_test.go:633-697` exercises distinct per-call IDs under an HTTP 200 envelope.
- `apps/bff/internal/handler/v1/endpoints.go:450-498`, `apps/bff/internal/handler/types.go:89-101`, `apps/bff/internal/handler/v1/query_adapter.go:35-51`, and `apps/bff/internal/search/citation_authority.go:182-188` establish the Query response fields and citation identity used by the probe.

## Local checks

- `python3.11 -m unittest discover -s ../../scripts -p 'test_request_id_trace.py' -q` from `apps/bff` — **PASS, 14 tests**.
- `python3.11 -m unittest discover -s ../../scripts -p 'test_trace_lookup.py' -q` from `apps/bff` — **PASS, 24 tests**.
- `python3.11 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py' -q` from `apps/bff` — **PASS, 23 tests**.
- `python3.11 -m py_compile scripts/verify_request_id_trace.py scripts/test_request_id_trace.py scripts/trace_lookup.py` — **PASS**.
- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/verify-request-id-trace-dev.yml` — **PASS**; actionlint made no repository dependency changes.
- `ruby -e 'require "yaml"; YAML.load_file(".github/workflows/verify-request-id-trace-dev.yml"); puts "DEV workflow YAML: valid"'` — **PASS**.
- `make workflow-yaml` — **PASS** for the repository's existing workflow set; the new DEV workflow was separately parsed and checked by actionlint above.
- `git diff --check` — **PASS, exit 0** on the final source and report bytes after report creation.
- Actual local CLI preflight, with credential/config environment variables unset:

  ```sh
  env -u LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID -u LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY -u LWC_DEV_PIPELINE_QA_PROJECT -u LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN -u WIF_PROVIDER -u WIF_SERVICE_ACCOUNT python3.11 scripts/verify_request_id_trace.py --artifact /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc402-request-id-probe.json > /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc402-request-id-probe.stdout.json
  ```

  Exit **2**, as expected: status `fail`, `live_execution=not_run`, `pass=0`, `fail=4`, `skip=1`, `manual=1`; blockers are `fixture_not_verified`, `project_key_missing`, and `query_project_allowlist_missing`. The three required cases are `fail/not_started`; Pipeline is `skip`, native Link is `manual`; stdout and the saved JSON artifact match. Static preflight stopped before any Cloud or product request was issued.

## Live status and remaining work

No Cloud/provider, product, Console, IAM, credential, secret inventory, fixture setup, or Pipeline call was made. The read-access preflight behavior is covered with mocks only. A real DEV run remains blocked until Main separately authorizes it and establishes the approved isolated-project allowlist, current published Harbor fixture readiness, application key custody, and read-only Logs/Trace permissions; no credentials or IAM were created or changed. Pipeline and native-Link Console verification remain **NOT RUN / MANUAL**, and LWC-393 through LWC-399 were not marked Verified.

## Changed files for this checkpoint

- `.github/workflows/verify-request-id-trace-dev.yml`
- `.github/workflows/ci.yml` (B test entrypoint added; this file also contains earlier A CI changes)
- `scripts/verify_request_id_trace.py`
- `scripts/test_request_id_trace.py`
- `scripts/fixtures/request_id_probe/README.md`
- `scripts/fixtures/request_id_probe/dev-prerequisites.json`
- `scripts/fixtures/request_id_probe/responses.json`
- `scripts/fixtures/request_id_probe/sanitized.json`
- `docs/observability/request-id-lookup-remediation-checkpoint-report.md` (syntax-only wording correction to the preserved A report)
- `docs/observability/request-id-probe-remediation-checkpoint-report.md`

All source remains uncommitted. The previously uncommitted A source, tests, fixtures, and runbook remain present for Main's combined source review and were not semantically changed by this B continuation.
