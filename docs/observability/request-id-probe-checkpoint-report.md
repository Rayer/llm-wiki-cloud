# LWC-402 local checkpoint — blocked by shared LWC-401 core HOLD

Task: `task_ef39b1e77ff2` / dispatch `ctx_f56a60527e44`
Baseline: `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`
Disposition: **B partial draft; not accepted or complete. Live suite NOT RUN.**

## Handoff and stop point

The coordinator delivered an official LWC-401 lookup-core review HOLD (`msg_7709f525fe73`) and directed this worker to stop B at the next safe source checkpoint, preserve all uncommitted A/B work, and avoid relying on the current A core. I acknowledged that direction and did not make further source edits after the notice. The authoritative UAT handoff `uat-source-tickets/authoritative-uat-contracts.md` was read in full by sections; it contains seven separate source contracts, LWC-393 through LWC-399, and no scenarios were inferred from LWC-401 alone.

The concrete A blocker is recorded in `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/A-local-review-result.json`: the current parser/query do not handle actual SDK-shaped structured stdout attributes/trace context, an actual SDK-shaped background log is missed, a counterexample returns `complete=true` with only one trace, a 600-span chain can raise `RecursionError`, and finite MCP operation names with `/` are rejected by the generic operation filter. A must be repaired and reviewed before B can depend on it; this worker did not attempt those repairs under the current B dispatch.

## Partial B draft present in the worktree

- `scripts/verify_request_id_trace.py`: draft DEV-only REST 404, MCP envelope/tool ID, Query fixture, bounded lookup and optional pipeline paths using the imported A core.
- `scripts/fixtures/request_id_probe/sanitized.json` and `dev-prerequisites.json`: synthetic evidence contract; query and pipeline allowlist project IDs remain `null` and live readiness is `not_configured`.
- `scripts/fixtures/request_id_probe/README.md`: local command and required fixture/identity names.
- `.github/workflows/verify-request-id-trace-dev.yml`: draft `workflow_dispatch` on `develop`, `Development` environment, existing WIF secret path, safe artifact upload; not YAML/actionlint checked.
- `scripts/trace_lookup.py` and `scripts/test_trace_lookup.py` also contain uncommitted LWC-401 implementation plus this worker’s attempted `diagnostic_id` search and safe terminal-log summary extension. Their current behavior is not qualified because of the A HOLD.
- Existing A work already present when B started remains uncommitted: `.github/workflows/ci.yml`, `docs/observability/`, `scripts/fixtures/trace_lookup/`, `scripts/test_trace_lookup.py`, and `scripts/trace_lookup.py`. No B test file or B CI test wiring was added before the stop instruction.
- This checkpoint report: `docs/observability/request-id-probe-checkpoint-report.md`.

## Checks actually run

- `python3 scripts/test_trace_lookup.py` — final run: 17 tests passed, exit 0. Two earlier iterations failed while adding the new terminal summary fixture (boolean normalization, then set serialization); both test issues were corrected before the passing run. This is local evidence for the current A unit-test subset only; it does not override the official HOLD.
- `python3 -m py_compile scripts/verify_request_id_trace.py scripts/trace_lookup.py` — exit 0 on an intermediate B draft. The probe source changed afterward, so the current B draft has not been syntax-checked again.
- No B contract tests exist or were run. No new workflow validation was run. No live DEV, product, provider, IAM, credential, provisioning, publication, PR, or deployment action was run.

## Readiness limits and next step

The query project allowlist, pipeline project allowlist, query-only project key, synthetic Query fixture evidence in DEV, and pipeline user identity are not available in source and were not inspected in external secret inventory. The existing GitHub `WIF_PROVIDER`/`WIF_SERVICE_ACCOUNT` workflow references were read from repository source; actual IAM roles/read-only scope were not verified, and no cloud/IAM call was made. The draft therefore must not be treated as a green live probe or a review-ready B implementation.

Main should first dispatch the same editor to repair and qualify A per the review result, then resume this B task on that repaired core. B still needs offline sanitized tests, CI wiring, workflow validation, current-source syntax/test runs, and Main-owned fixture/identity readiness review before any authorized DEV run. No ticket state or UAT mapping was marked Verified.
