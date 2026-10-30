# LWC-420 r2 implementation report

## Scope and integration

This work implements publication-derived Project content context for local and cloud publishing, read-only Project GET/UI and authenticated MCP metadata. It keeps summary generation outside the knowledge publication result, preserves the legacy local Query identity, and never runs inference from GET.

The exact LWC-445 description commit `c66e6f863cee77c4d2aedce25b6747a4abf8b4c5` and exact final LWC-443 resolver commit `c182182f8ce488599a0d7ba4ffdfdf03f1fe30c6` are integrated. LWC-443 supplies the response-preserving `resolvePipelineLLMProfileResponseWithCredentials` interface and `revalidatePipelineProfileCandidates`; 420 consumes those immutable interfaces without copying resolver code or introducing another profile authority.

The content summary binds the claimed work to the resolver's actual `bootstrap`, `personal`, or `shared` source. Personal and shared summaries use the original pinned Fast profile identity and credentials; shared candidates are eligibility-checked after claim while retaining the pinned model revision. Bootstrap uses the platform Fast client identity and stores no fabricated profile ID or revision. The source is persisted alongside the actual summary identity and exposed to GET, UI, and MCP consumers.

Same-digest carry-forward preserves the original work, lease, retry count, and source generation while advancing the latest publication target. Different-digest work fences old attempts. Summary/provider/storage failures remain nonblocking for knowledge publication and preserve last-good output. The MCP and UI paths read the same authenticated Project context, and the GET path has no LLM call.

## Verification

- Focused emulator-backed worker, content-context, handler, carry-forward, fencing, and shared eligibility tests passed:
  `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./cmd/olw_worker ./internal/contentcontext ./internal/handler/v1 -run 'ContentContext|SummaryWork|PublicationCarryForward|PipelineQuotaFallbackRevalidatesEligibility' -count=1`.
- Broad emulator-backed Go run passed `cmd/olw_worker`, `internal/localfs`, `internal/contentcontext`, `internal/cache`, `internal/llm`, and `internal/queryruntime`. `internal/handler/v1` had one known LWC-443 failure: `TestProfileRuntimeConnectedManualThenCompileTagging/retained-candidate-exhaustion-retries-through-activation` reports `calls=3` at `profile_runtime_e2e_test.go:385`. Project context handler/MCP tests passed in the focused run above.
- Frontend `npm test` passed 570/570 Node tests and 621/621 component tests; `npm run typecheck` passed.
- `git diff --check` passed.

## Qualification limits

No external provider call, production deployment, live-data write, or external Bot/multiconnector client qualification was performed. The last item requires the separately authorized parent DEV/client qualification. The handler retry failure remains assigned to the LWC-443 lane and is not changed by this 420 work.

## Review status

This report records local implementation and QA only. Parent retains merge and DEV authority; the worker remains available for review or CI repair requests.
