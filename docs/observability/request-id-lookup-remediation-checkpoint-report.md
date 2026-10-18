# LWC-401 request ID lookup remediation checkpoint

**Checkpoint:** local remediation only; source remains uncommitted. Baseline: `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`. The previous official review at `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/A-local-review-result.json` remains unchanged with its original **HOLD** and findings; this report records local evidence for Main's independent review, not ticket acceptance, publication, merge, deployment, live DEV UAT, or a `Verified` decision for LWC-393 through LWC-399.

## Remediation evidence

1. **Pinned stdouttrace log discovery:** `scripts/trace_lookup.py` now parses allowlisted `Attributes[].Key/Value`, `SpanContext.TraceID/SpanID`, resource service/revision values, and Link trace IDs from structured `jsonPayload` and JSON `textPayload`. Its Logging filter includes `jsonPayload.Attributes.Value.Value="<request-id>"`; the parser still requires an exact request/origin ID attribute key and value before counting a row. The fixture retains the actual v1.44.0 `SpanStub` serialized field shape in both Cloud Logging forms, with synthetic stable IDs, and replaces the former invented flat `pipeline.worker.terminal` row. `scripts/fixtures/trace_lookup/README.md` records the pinned module and BFF producer methods used to produce it.

2. **Separate trace and truthful async evidence:** matching SDK span logs contribute deduplicated trace IDs, attempt/service/revision hints, and only validated Link trace IDs. A worker trace remains separate from ingress; when its captured stdouttrace Link identifies the ingress trace, the lookup labels the relationship as `separate_trace_with_stdouttrace_link` and keeps native Link verification `manual_console_check_required`. An HTTP terminal status of 202 with no observed related trace evidence adds `async:background_trace_evidence_missing` and returns incomplete.

3. **Bounded deep trees:** construction and text rendering are iterative. Nested tree output stops at depth 128, marks the cutoff with `children_truncated`, preserves the flat span list, adds `span_tree_depth_limit`, and returns an incomplete/truncated result. The JSON and text output tests cover a legal 600-span parent chain without `RecursionError`.

4. **MCP operation safety:** the exact producer MCP operation values, including `tools/call` and `resources/read`, pass through a finite allowlist. Arbitrary URL/path values remain rejected by the generic operation sanitizer.

## Local checks

- From `apps/bff`: `python3.11 -m unittest discover -s ../../scripts -p 'test_trace_lookup.py' -q` — **PASS, 24 tests**. This includes the original lookup cases, the prior `diagnostic_id` regression, and positive/negative SDK structured/text cases, async missing-evidence, MCP allowlist, CLI cap/privacy, and deep-tree output checks.
- From `apps/bff`: `python3.11 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py' -q` — **PASS, 23 tests**.
- From repository root: `python3.11 -m py_compile scripts/trace_lookup.py scripts/test_trace_lookup.py scripts/verify_request_id_trace.py` — **PASS for syntax only**; this does not execute the B probe or verify its imports/shared-core integration.
- From repository root: `git diff --check` — **PASS**.
- From `apps/bff`: `go list -m go.opentelemetry.io/otel/exporters/stdout/stdouttrace` — **PASS**, resolved to v1.44.0. An offline temporary harness called `NewFilteredTraceProvider`, `StartExportAttemptSpan("export.worker")`, `StartWorkerSpan("pipeline.worker.export")`, `FinishExportSpan`, and `FinishPipelineSpan`; `go run ./cmd/lwc401fixture > /tmp/lwc401_stdouttrace_sdk.jsonl` emitted actual SDK JSON with the producer Link and child Parent. The temporary harness was removed; no Cloud or credential path is used.

**Live status: NOT RUN.** No Logging/Trace API, Cloud provider, Console, IAM, credential, secret, product, or Owner corpus call was made. The fixture and producer harness are offline evidence only; native-Link Console verification remains **MANUAL / NOT RUN**. The prior HOLD remains in force until Main completes independent source review.

## Files changed by this remediation

- `scripts/trace_lookup.py`
- `scripts/test_trace_lookup.py`
- `scripts/fixtures/trace_lookup/sanitized.json`
- `scripts/fixtures/trace_lookup/README.md`
- `docs/observability/request-id-runbook.md`
- `docs/observability/request-id-lookup-remediation-checkpoint-report.md`

The existing B probe, B fixtures, B workflow, and B checkpoint report were preserved. The existing A workflow/CI wiring was not changed in this remediation. All source remains uncommitted; Main owns source review and any continuation or publication decision.
