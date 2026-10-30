# LWC-420 r2 implementation report

## Scope and integration

This work implements publication-derived Project content context for local and cloud publishing, read-only Project GET/UI and authenticated MCP metadata. It keeps summary generation outside the knowledge publication result, preserves the legacy local Query identity, and never runs inference from GET.

The exact LWC-445 description commit `c66e6f863cee77c4d2aedce25b6747a4abf8b4c5` and exact final LWC-443 resolver commit `c182182f8ce488599a0d7ba4ffdfdf03f1fe30c6` are integrated. LWC-443 supplies the response-preserving `resolvePipelineLLMProfileResponseWithCredentials` interface and `revalidatePipelineProfileCandidates`; 420 consumes those immutable interfaces without copying resolver code or introducing another profile authority.

The content summary binds the claimed work to the resolver's actual `bootstrap`, `personal`, or `shared` source. Personal and shared summaries use the original pinned Fast profile identity and credentials; shared candidates are eligibility-checked after claim while retaining the pinned model revision. Bootstrap uses the platform Fast client identity and stores no fabricated profile ID or revision. The source is persisted alongside the actual summary identity and exposed to GET, UI, and MCP consumers.

Same-digest carry-forward preserves the original work, lease, retry count, and source generation while advancing the latest publication target. Different-digest work fences old attempts. Summary/provider/storage failures remain nonblocking for knowledge publication and preserve last-good output. The MCP and UI paths read the same authenticated Project context, and the GET path has no LLM call.

## Verification

- After the exact `68a03c5bebfa003b5c6f72ef096cab1e2a948825` focused review HOLD, the new recovery regression command passed with an owned loopback Firestore emulator at `127.0.0.1:28761`, ADC and metadata disabled: `go test ./cmd/olw_worker -run 'TestLocalWorkerRecoversCommittedSummaryAndContinuesOrdinaryPipeline|TestLocalFirstClaimAfterSameDigestCarryForwardUsesCapturedSource|TestLocalPublicationAuthorityCarriesSameDigestAndFencesLateAttempt' -count=1`. It passed all 3 selected top-level tests; the recovery test covers missing state, expired pending work, and active pending work, and asserts the ordinary pipeline materializes new raw input, reaches compile, publishes changed concepts, records a valid receipt, and fences the interrupted claim.
- A broad `go test ./cmd/olw_worker ./internal/contentcontext -skip 'TestProfileReceiptFirestoreAtomicOutboxAndDuplicate|TestProfileReceiptActualPublisherPersistsOriginRequestID' -count=1` passed both packages using the same fresh owned loopback emulator. The two excluded unrelated profile-receipt cases hard-require `127.0.0.1:8585`; the earlier full package run recorded 472 PASS events, 3 SKIP events, and those 2 port-guard failures. Earlier logs: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc420-r2-review-repair-20261030/bff-focused.log` and `bff-worker-broad.log`.
- Frontend `npm test` passed 570/570 Node tests and 621/621 component tests; `npm run typecheck` passed.
- `git diff --check` passed.

## Review repair and causal comparison

The first review hold is fixed by carrying the committed descriptor's summary-source generation and identity separately from the current publication head; first-time work now binds to captured G1 input even when same-digest G2 is current, while the existing input loader and digest fences remain strict. The second hold is fixed at the local worker recovery entry: if the confirmed descriptor is missing from state, stale, or pending, the worker releases its publication lease before resuming reconciliation and returns without compiling or publishing another generation. Ready or failed current work is not automatically retried.

For the separate retained-candidate handler failure, an exact comparison used the same Go 1.26.5 toolchain and distinct fresh loopback Firestore emulators with ADC and metadata disabled: frozen base `3bacc1f8567d040ef4a8f8fd7fa6df2f56116724` passed; exact LWC-443 handoff `c182182f8ce488599a0d7ba4ffdfdf03f1fe30c6` and PR head `37ade67f687c8365ccceff112e901ea9e1626481` failed the same test. A sanitized diagnostic on c182 recorded bootstrap source, no credentials, zero resolved profiles, and `LLM profile is unavailable`; `calls=3` is only the observed counter outcome. The failure is not present on frozen base and is not an established pre-existing 443 failure; its exact runtime stage and final owning-lane verdict remain with the parent. No 440/443-only code or expected-counter assertion was changed here.

## Qualification limits

No external provider call, production deployment, live-data write, or external Bot/multiconnector client qualification was performed. The last item requires the separately authorized parent DEV/client qualification. Orca dispatch metadata confirms `gpt-6-luna`; this task reused an existing terminal and records both requested and effective effort as null, so xhigh is not verified from this session. The active runtime has unrestricted filesystem/network access and no approval gate. `from -> to | xhigh effort unverified on reused terminal | coordinator can set or re-dispatch with explicit effort and inspect metadata | do not claim xhigh until proven`. GitGuardian remains neutral/skipping due to inherited historical findings recorded as `live4-3985` and `live4-3986`; the dependency history is preserved, no finding values were inspected, and this status is not described as a clean pass.

## Review status

The focused review of `68a03c5bebfa003b5c6f72ef096cab1e2a948825` resolved the source-identity finding and raised a new HOLD: the recovery branch returned from the ordinary worker entry before new input could be processed. The working-tree repair now reacquires and revalidates the publisher state after summary recovery, then continues the ordinary snapshot/compile/publish/receipt path; the replacement candidate still needs commit, CI, and a same-head focused review. Parent retains merge and DEV authority; the worker remains available for review or CI repair requests.
