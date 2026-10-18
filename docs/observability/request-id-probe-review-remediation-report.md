# LWC-402 B source-review remediation

**Task:** `task_59bb1521e9c0` / **Dispatch:** `ctx_b4c3215c7a13`

**Baseline:** `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`
**Disposition:** Local remediation checkpoint for Main's independent review. The original B source packet and its official `HOLD` are preserved; this report does not claim ticket completion, publication, DEV/UAT verification, merge, or deployment.

## Review findings and changes

The starting review is `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/B-local-review-result.json`; the matching live ticket comment is `LWC-402` comment `4-2666`. Before editing, a local synthetic reproduction showed that a same-project query-key-only configuration produced no Pipeline identity blocker, and envelope terminal mismatch, terminal outcome missing, and HTTP root-span outcome missing each still produced `pass/verified`.

- Pipeline now uses only `pipeline_bearer_token`; the query-only project key is never a fallback. Opt-in preflight returns `pipeline_identity_missing` when the independent bearer token is absent. The CLI's preflight path exits nonzero and records `live_execution=not_run` before Cloud access or product calls. Pipeline remains skipped by default.
- The MCP call's native `result.isError` stays a separate per-call `error`. For the native tool-error fixture, the owning HTTP 200 envelope and its `http.request.terminal` are required to have the `success` outcome emitted by `request_lifecycle`; mismatch, missing terminal outcome, and missing or mismatched root-span outcome fail the check. The positive contract test asserts call `error` alongside envelope terminal/span `success`.
- The Pipeline test captures the configured independent bearer token on both submit and every status request. A CLI-level test supplies only the same-project query key and verifies exit 2, `pipeline_identity_missing`, `NOT RUN`, no product requests, and that Cloud read preflight is never called.
- The fixture README now lists the nonsecret static fields to update only after separately authorized fixture confirmation: query and fixture project IDs, readiness, marker, the sanitized fixture query and its expected answer/citation fields, and the optional Pipeline project allowlist. It documents the shared local command and dispatching `develop` only after the updated prerequisite revision is present. The prior checkpoint README/report line that suggested a same-project query-key fallback has been corrected.

The live `dev-prerequisites.json` remains unchanged: query, pipeline, and fixture project IDs are `null`; fixture readiness is `not_verified`; live readiness is `not_configured`. No fixture readiness was invented. The frozen packet at `/Users/rayer/.hermes/workflows/lwc-local/evidence/request-id-closeout-r2/lwc402-local-r2/source` was not edited. `scripts/trace_lookup.py`, product authentication/API, CI/workflow files, dependencies, IAM, and credentials were not changed in this remediation.

## Producer evidence

`apps/bff/internal/observability/request_lifecycle.go:163-194` assigns HTTP outcome from status and protocol-error state, and writes the same outcome on the HTTP span and `http.request.terminal`. `apps/bff/internal/handler/v1/mcp.go:183-207` detects a protocol error from the JSON-RPC envelope's top-level `error`; lines `210-237` check native tool `result.isError` separately. Therefore the required HTTP 200 native-tool-error response has per-call `error` while its HTTP envelope remains `success`.

The review's Pipeline source proof records `/api/v1/pipeline/run` and `/api/v1/pipeline/status` under `AccountAuth` plus project middleware, while `ProjectKeyQueryAuth` is limited to Query; an application query key cannot authenticate Pipeline, including within the same project.

## Local verification

| Command | Result |
|---|---|
| `python3.11 -m unittest discover -s scripts -p 'test_request_id_trace.py' -q` | Exit 0, 17 tests passed (final run) |
| `python3.11 -m unittest discover -s scripts -p 'test_trace_lookup.py' -q` | Exit 0, 24 tests passed |
| `python3.11 -m unittest discover -s scripts -p 'test_*auth_config_contract.py' -q` | Exit 0, 23 tests passed |
| `python3.11 -m py_compile scripts/verify_request_id_trace.py scripts/test_request_id_trace.py scripts/trace_lookup.py` | Exit 0 |
| `git diff --check` | Exit 0 |
| Untracked-target whitespace check using `git diff --no-index --check /dev/null <path>` for the five modified source/doc files | Clean after removing pre-existing hard-break spaces from the corrected checkpoint report |

No workflow file changed in this remediation, so workflow validation was not rerun. Prior checkpoint actionlint and YAML validation are not presented as new remediation evidence.

The actual unconfigured CLI run was:

```sh
env -u LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID -u LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY -u LWC_DEV_PIPELINE_QA_PROJECT -u LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN -u WIF_PROVIDER -u WIF_SERVICE_ACCOUNT python3.11 scripts/verify_request_id_trace.py --enable-pipeline --artifact /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc402-request-id-probe-review-remediation.json > /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc402-request-id-probe-review-remediation.stdout.json
```

It exited `2`; status is `fail`, `live_execution=not_run`, and the Pipeline check is `fail/pipeline_identity_missing` with attempts `not_started`. The report includes the other missing static prerequisites, stdout equals the saved artifact, and no Cloud or product request was made. The separate synthetic CLI regression isolates same-project query-key-only identity and fails if Cloud preflight is reached.

## Changed files in this remediation

- `scripts/verify_request_id_trace.py`
- `scripts/test_request_id_trace.py`
- `scripts/fixtures/request_id_probe/README.md`
- `docs/observability/request-id-probe-remediation-checkpoint-report.md`
- `docs/observability/request-id-probe-review-remediation-report.md`

All changes remain uncommitted. The retained worktree still contains the earlier A+B draft files; this remediation did not edit the qualified A lookup core or reclassify the original `HOLD`. Main owns the next source review and any later DEV authorization.
