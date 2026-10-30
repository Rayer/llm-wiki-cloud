# LWC-420 r2 implementation report

## Scope and integration

This work implements publication-derived Project content context for local and cloud publishing, read-only Project GET/UI and authenticated MCP metadata. It keeps summary generation outside the knowledge publication result, preserves the legacy local Query identity, and never runs inference from GET.

The exact LWC-445 description commit `c66e6f863cee77c4d2aedce25b6747a4abf8b4c5` and exact final LWC-443 resolver commit `c182182f8ce488599a0d7ba4ffdfdf03f1fe30c6` are integrated. LWC-443 supplies the response-preserving `resolvePipelineLLMProfileResponseWithCredentials` interface and `revalidatePipelineProfileCandidates`; 420 consumes those immutable interfaces without copying resolver code or introducing another profile authority.

The content summary binds the claimed work to the resolver's actual `bootstrap`, `personal`, or `shared` source. Personal and shared summaries use the original pinned Fast profile identity and credentials; shared candidates are eligibility-checked after claim while retaining the pinned model revision. Bootstrap uses the platform Fast client identity and stores no fabricated profile ID or revision. The source is persisted alongside the actual summary identity and exposed to GET, UI, and MCP consumers.

Same-digest carry-forward preserves the original work, lease, retry count, and source generation while advancing the latest publication target. Different-digest work fences old attempts. Summary/provider/storage failures remain nonblocking for knowledge publication and preserve last-good output. The MCP and UI paths read the same authenticated Project context, and the GET path has no LLM call.

## Verification

- Review repair regression command passed against an owned loopback Firestore emulator at `127.0.0.1:18592`, with ADC and metadata disabled: `go test ./cmd/olw_worker -run 'TestLocal(FirstClaimAfterSameDigestCarryForwardUsesCapturedSource|WorkerRecoversCommittedSummaryWithoutCompilingOrPublishing|PublicationAuthorityCarriesSameDigestAndFencesLateAttempt)$' -count=1 -v`. It passed 3 top-level tests and both recovery subtests (missing first state registration; expired claim before completion). The tests exercise G1 descriptor commit, same-digest G2, actual first Claim/input loader/Firestore Complete/readback, crash recovery without compile or publication, bounded second claim, old-attempt rejection, and changed-digest fencing.
- A broad `go test ./internal/contentcontext ./cmd/olw_worker -skip 'TestProfileReceiptFirestoreAtomicOutboxAndDuplicate|TestProfileReceiptActualPublisherPersistsOriginRequestID' -count=1` passed both packages with a fresh owned loopback emulator at `127.0.0.1:18593`. The two excluded unrelated profile-receipt cases hard-require `127.0.0.1:8585`; the earlier full package run recorded 472 PASS events, 3 SKIP events, and those 2 port-guard failures. Logs: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc420-r2-review-repair-20261030/bff-focused.log` and `bff-worker-broad.log`.
- Frontend `npm test` passed 570/570 Node tests and 621/621 component tests; `npm run typecheck` passed.
- `git diff --check` passed.

## Review repair and causal comparison

The first review hold is fixed by carrying the committed descriptor's summary-source generation and identity separately from the current publication head; first-time work now binds to captured G1 input even when same-digest G2 is current, while the existing input loader and digest fences remain strict. The second hold is fixed at the local worker recovery entry: if the confirmed descriptor is missing from state, stale, or pending, the worker releases its publication lease before resuming reconciliation and returns without compiling or publishing another generation. Ready or failed current work is not automatically retried.

For the separate retained-candidate handler failure, an exact comparison used the same Go 1.26.5 toolchain and distinct fresh loopback Firestore emulators with ADC and metadata disabled: frozen base `3bacc1f8567d040ef4a8f8fd7fa6df2f56116724` passed; exact LWC-443 handoff `c182182f8ce488599a0d7ba4ffdfdf03f1fe30c6` and PR head `37ade67f687c8365ccceff112e901ea9e1626481` failed the same test. A sanitized diagnostic on c182 recorded bootstrap source, no credentials, zero resolved profiles, and `LLM profile is unavailable`; `calls=3` is only the observed counter outcome. The failure is not present on frozen base and is not an established pre-existing 443 failure; its exact runtime stage and final owning-lane verdict remain with the parent. No 440/443-only code or expected-counter assertion was changed here.

## Qualification limits

No external provider call, production deployment, live-data write, or external Bot/multiconnector client qualification was performed. The last item requires the separately authorized parent DEV/client qualification. GitGuardian remains neutral/skipping due to inherited historical findings recorded as `live4-3985` and `live4-3986`; the dependency history is preserved, no finding values were inspected, and this status is not described as a clean pass.

## Review status

This report records local implementation and QA only. Parent retains merge and DEV authority; the worker remains available for review or CI repair requests.
