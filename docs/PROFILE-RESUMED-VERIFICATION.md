# LWC-301 resumed local verification

Implementation HOLD superseded by explicit owner resume relayed by main. Source worktree remains Rayer/LWC-209-profile-api at HEAD 36212d7e385f25ad3349c48fbe8258fd7c053bca, with prior dirty/untracked implementation preserved. No commit, PR, merge, deploy, IAM, secret or provider mutation performed in this resumed checkpoint.

## Actual execution (2026-09-26 UTC)
- Prior run run_322b0229fd26 enumerated: 27 dispatch records, none outside completed/failed; no old editor was started twice.
- Existing local Firestore emulator was absent; started installed jar/OpenJDK at 127.0.0.1:8585 and verified HTTP response. Process owned by this session: proc_48029dd8140a (PID79802 at start).
- `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 make test`: EXIT 0. Includes Go race suite and existing Python contract suites, frontend 522 Node tests and 255 component tests (26 files).
- Connected production-path tests `TestProfileRuntimeConnectedBootstrapFirstCompile` and `TestProfileRuntimeConnectedManualThenCompileTagging` explicitly PASS, not skipped. Providers/GCS remain labeled local fakes; Firestore is emulator.
- `make lint` and `make typecheck`: PASS.
- Initial `make vet`: FAIL at cmd/query_experiment/fixture_test.go:411, comparing a query.Request that now contains []string.
- Direct Codex scoped repair changed this assertion to existing reflect.DeepEqual over the entire expected request. Parent inspected actual one-line diff; new fields are not ignored, nil expectations retained. No production change or weakened gate.
- Parent `make vet` after fix: EXIT 0.
- Parent `make build`: EXIT 0 (Go and Next.js).
- `git diff --check`: EXIT 0.
- Child ctx_2e4875b110f3 completed and released; no nested child remains active in this remediation run.
- A second scratch-only browser harness child ctx_4816a8cf6f9e lost its Codex connection after writing the harness (explicit reconnect failure). Parent stopped that exact attempt; no duplicate editor or production change. Parent read and ran the existing harness directly, then operated Safari MCP.
- Real Safari rendered actual ProjectProfilePanel/ProfileRequirementsEditor and styles with real lib/api against explicitly synthetic loopback HTTP endpoints. Parent saved a full requirement, confirmed the resulting bootstrap revision, verified Recompile all has disabled=true, then delayed an A save and switched to B. After A completed, visible B and B saved requirements stayed unchanged; all observed project headers matched their target. Screenshot visually reviewed, no layout breakage observed.
- Browser evidence: `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc301-resumed/browser-profile.png` and `browser-fixture-state.json`. Harness lives at `/Users/rayer/.hermes/profiles/chatgpt/cache/scratch/lwc301-browser`; not committed and not a deployed/full authenticated application E2E.

Logs: /Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc301-resumed/{test.log,static-build.log,vet.log,build.log}. static-build.log intentionally retains initial RED evidence. No credentials recorded.

## Lessons applied: concrete assessment, not added gates
Present:
1. Shared consumer/fixture mismatch actually occurred: Query Request new slice made query_experiment test uncompilable, missed by previous narrow package gates. Fixed minimally and reran whole vet/test.
2. Earlier connected tests found real canonical body/sources parser mismatch and private source receipt expectation. Latest code/report includes corrections; fresh connected test now passed independently.
3. Source executor and fake provider tests cannot prove deployed scheduler/OIDC/IAM/index/model behavior. This boundary remains explicit.
4. Old worktree predates released develop337ac0658cbf890e621c52effc0300612dc15739. Local success is not exact merged-SHA CI or proof of compatibility with that integrated release.
5. Historical preflight recommended a separate Tag Job/runtime SA from older contract/source. Current source instead implements the shared authenticated bounded dispatcher in BFF. Do not carry that recommendation forward as a new Job prerequisite. The old Scheduler API-disabled inventory cannot establish absence; compiler Firestore receipt permission, tick identity/configuration, Jev key mapping and outbox index need current scoped release evidence. No new grants are authorized here.

Absent in this resumed activity:
- No new roles, approval gates, AST/provenance frameworks or deployment-convergence implementation.
- No repeat LWC302 deploy/export; completed release receipts supplied by main are accepted as historical evidence, not freshly reverified here.
- No stale missing-QA-identity blocker: LWC-A-12 sec2 / qa-credential-dev / project4300ccd20e93 are known approved references; no credential fetched.
- Three export cleanup grants are owner-only, not Profile prerequisites.

Unverified/remaining:
- Latest entire application release diff review is not completed by this checkpoint. Bounded real-browser Profile component workflow was exercised independently as above; full authenticated application/DEV acceptance remains separate.
- Profile deployed dispatcher schedule, OIDC identity/audience, effective IAM, index and secret/environment mappings are not proven by local tests. Source runtime requires PROFILE_RUNTIME_AUDIENCE and PROFILE_RUNTIME_SERVICE_ACCOUNT, and report identifies profile_runtime_work(pending,due) index. Missing deployed mechanism must be reported as a genuine runtime need, not solved by copying export grants or adding unapproved gates.
- Exact integrated candidate, exact merged-SHA CI, old-live-to-candidate runtime delta and rollback handles must belong to actual release handoff, not inferred from this dirty base. Main owns final coordination; only dedicated lwc-deployer mutates.
- Hard required tags remain unsupported422 by existing contract; safe rejection is not full directional geography filtering.
- Live model quality, live feature sanity and owner UAT are distinct and not claimed. Retention/GC is not introduced as a new gate.

## Status
Automated local repository test/lint/typecheck/vet/build now pass after one bounded shared-fixture repair. This is verified local progress, not deployment-ready or deployed/live acceptance. Continue only existing approved Profile work; LWC358 remains context-only until after301.

## Parent review follow-up (2026-09-27)

The integrated baseline under review was `183e3c9e3f9e3263142c7de0afc24f5fdb0de7d6`; PR #58 remains open and unmerged.

- **F3, first-character focus loss:** The empty editor row previously changed its React key when the first character created a generated requirement ID, replacing the focused textarea. It now retains that ID through the first edit, and rotates it only when the last requirement is removed. The component regression reproduced the old DOM replacement and passes after the fix.
- **F4, existing generation first activation (initial test was insufficient):** The connected emulator scenario started with a committed *new-format* generation at Profile revision 0. It did not test an archived pre-Profile manifest without `source_snapshot_digest`; the claim that this established legacy first activation is superseded below.
- **F5, immutable guidance visibility (initial implementation incomplete):** The first handler/UI revision served only bootstrap or current candidate guidance, and omitted retained active guidance after a newer save or preview. This is superseded below.

### Follow-up verification

- `PYTHONDONTWRITEBYTECODE=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 make test`: PASS; includes the Go race suite, 70 CD contract tests, 23 auth-config contract tests, 524 frontend Node tests, and 275 component tests.
- `make lint`, `make typecheck`, `make vet`, and `make build`: PASS.
- An earlier full `make test` rerun had one transient LWC-345 export-panel query failure. The isolated test passed, followed by a complete passing `make test`; no unrelated source was changed.
- Final logs are retained at `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc301-resumed/remediation-final-{make-test,lint,typecheck,vet,build}.log`.
- Protected baseline archive SHA-256 remains `58c875034607275717af018141d7ab3d49bc3d3328d1ffc72118188662e5d044`; the unrelated `scripts/__pycache__` tree digest is unchanged and remains untracked.

Hard required tags remain HTTP 422; directional geography filtering is not implemented. Deployed scheduler/OIDC/IAM/index/secret mappings and live provider quality remain unverified. Parent review is still open; no merge or deployment occurred.

## Second parent review follow-up (2026-09-27; review still open)

The parent identified that the prior F3–F5 fixes were incomplete and separately narrowed F2 exhaustion behavior. These findings supersede any broader closure statements above. The follow-up source edits are committed and pushed as `18e5086f4275b576f14a80e0ff40d1cec37f40d9` (`fix(profile): repair follow-up review findings`); PR #58 points at that head and remains open, non-draft, and unmerged. The commit includes the F3/F5 fixes, Swagger regeneration, and F2/F4 diagnostic regressions described below.

- **F3, data-loss correction:** The stable empty-row ID was also used as the placeholder test after the row had become real. Editing or clearing it after adding a sibling could drop the other rows. The branch now treats that ID as a placeholder only when `requirements.length === 0`; component regressions cover first-character focus identity, editing the first of two rows, and clearing it while preserving its sibling. Targeted tests and the full Profile component file passed.
- **F4, legacy evidence reproduced; policy decision pending:** The local tagging fixture was faithfully reduced to a pre-Profile manifest by removing `source_snapshot_digest` and its archived source-snapshot/content-addressed-byte objects while leaving mutable `raw/source.md` available. `readProfileTagInventory` rejects that generation and does not read the mutable raw path, so a confirmed manual candidate on such a generation cannot receive exact source coverage. The prior happy-path test used a new-format fixture and does not close this issue. The proposed safe behavior is bootstrap-first only when no Profile is active and the existing generation lacks an immutable source snapshot: require owner confirmation of generation-free guidance, then promote that exact consumed ref only after a subsequent successful publish supplies new immutable snapshot evidence. This leaves the old corpus unprofiled until a new publish and performs no migration or mutable-source fallback. Parent decision is pending on ask `msg_e4f37b224750`; next check is a reply approving that compatibility behavior or specifying another policy.
- **F5, active and candidate guidance:** The read endpoint now serves a retained Active artifact by loading its immutable historical candidate and matching candidate ID, generation, dictionary revision, and guidance revision to the Active tuple. UI displays Active guidance even after Save clears Candidate, and separately displays a different Candidate guidance ref. Candidate preview types/UI now include per-requirement disposition and explanation. The artifact panel explains the current Synto adapter boundary: materialized schema plus guidance over 1,500 characters is rejected; this is not a Profile input limit. Targeted handler/component/API tests passed.
- **F5 Swagger follow-through:** Regenerated Swagger from handler annotations with `go generate ./cmd/bff`; the guidance operation uses the established `DevUserAuth` and `ProjectHeader` security names, and bootstrap confirmation documents 428 for missing required preconditions.
- **F2 exhaustion precision:** Emulator evidence shows tag exhaustion becomes visible as an incomplete job with `runtime_retry_exhausted`, while preserving Active. Compile exhaustion after a successful compile receipt but before reconcile creation makes the outbox row `exhausted` but leaves Profile `ready` with no error/job/candidate or same-receipt retry route. The compile path is not being claimed fixed; a visible status requires an explicit Profile-state/API/UI contract decision. This compile result still needs parent disposition.

### Rereview local gates

- `PYTHONDONTWRITEBYTECODE=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 make test`: PASS after the F3/F5 changes and diagnostic F2/F4 regressions. It includes 70 CD contract tests, 23 auth-config tests, the Go race suite, 524 frontend Node tests, and 277 component tests.
- `make lint`, `make typecheck`, `make vet`, `make build`, and `git diff --check`: PASS.
- Logs are retained at `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc301-resumed/rereview-{make-test,lint,typecheck,vet,build}.log`.
- After renaming the shared requirement disposition label and making the active-retention test invoke the actual Save transition, the full `make test`, lint, typecheck, vet, build, targeted tests, and `git diff --check` were rerun and passed again. Latest logs are `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc301-resumed/rereview2-{make-test,lint,typecheck,vet,build}.log`.

No deployed cloud, scheduler, IAM, secret, or live-provider work was performed. PR #58 is open, non-draft, unmerged, and undeployed at `18e5086f4275b576f14a80e0ff40d1cec37f40d9`. Full local gates pass; parent review and the F4 compatibility and F2 compile-exhaustion policy dispositions remain open.

## F2/F4 source follow-up (2026-09-28; parent review pending)

On baseline `18e5086f4275b576f14a80e0ff40d1cec37f40d9`, implemented the approved F2/F4 source fixes and focused regressions. An existing no-snapshot generation now enters generation-free bootstrap only when no Active Profile exists; the confirmed bootstrap ref remains pinned to the next successful compile, whose retained generation must pass immutable source-inventory validation before candidate creation. Compile runtime exhaustion now exposes the existing failed/error-code shape, preserves Active, and reuses the existing derivation retry route to requeue the same successful receipt/work identity idempotently; stale receipts and newer Profile revisions do not overwrite current state. The Project Profile panel exposes the exhaustion and retry action.

Focused results: backend profilederive and handler regressions PASS on loopback Firestore (`TestExecutorBootstrapsBeforeLegacyGenerationWithoutSnapshot`, `TestExecutorDerivesManualArtifactsFromOnePinnedEmptyConceptSnapshot`, `TestProfileRuntimeConnectedBootstrapFirstCompile`, `TestProfileRuntimeCompileExhaustionIsVisibleAndRetryable`, `TestProfileRuntimeCompileExhaustionIgnoresStaleReceipt`, `TestProfileRuntimeSupersededCompileRetryClearsOnlyMatchingState`); Project Profile component tests PASS (29); targeted ESLint, frontend typecheck, targeted Go vet, and `git diff --check` PASS. Existing full-suite logs from the baseline were reviewed and not rerun for this follow-up. Detailed commands and results: `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/report.md`.

Before source edits, the pre-existing dirty verification report and `scripts/__pycache__/` were archived to `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/pre-edit-dirty-files.tar.gz` (SHA-256 `01c7e0346fc16a596a3ada56b1e263e987fd45d47d046a141f748e1a1fc0fb90`). The prior report text and unrelated untracked files remain preserved. No commit, push, merge, deploy, or cloud mutation was performed; parent source review remains outstanding.

Exact review diffs: source-only `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/source-changes.diff` (SHA-256 `213825cf4601fc5e6016f2ad51240d474ec04cbe693bbe5db76214dce88282cd`); tracked worktree `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/tracked-worktree.diff`.


## F2 retained activated-candidate retry follow-up (2026-09-28)

The parent review found that exhaustion and retry rejected every nonnil Candidate, including the Candidate retained by an already activated Profile. Both guards now reject only a Candidate whose ID is not the Active Candidate ID. Compile reconciliation recognizes only its matching pending retry marker, while the existing independent manual-candidate activation guard remains in force.

The connected emulator regression uses the real revision-2 retained Candidate and Active plus a valid revision-2 compile receipt. It exhausts the final attempt before generation validation/reconcile creation, checks the visible runtime error and unchanged Active/Candidate, retries the same receipt/work idempotently, restores the local fake generation store, and verifies reconcile status `candidate_ready`, compile-auto Candidate creation, tagging completion, and Active promotion. A separate regression verifies that an unactivated manual Candidate blocks both exhaustion-state mutation and retry; existing stale receipt and newer-revision tests remain in the focused run.

Focused command:

```sh
cd apps/bff
FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 go test ./internal/handler/v1 -run 'TestProfileRuntimeConnectedManualThenCompileTagging|TestProfileRuntimeCompileExhaustionIsVisibleAndRetryable|TestProfileRuntimeCompileExhaustionIgnoresStaleReceipt|TestProfileRuntimeCompileExhaustionPreservesUnactivatedCandidate|TestProfileRuntimeSupersededCompileRetryClearsOnlyMatchingState' -count=1 -v
```

Result: PASS (all five selected tests; loopback Firestore emulator and local fake GCS/provider). The exact output is at `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/f2-focused-handler.log`. The corrected regression fails against the original all-Candidate guard at the exhausted Profile-state assertion; red evidence is at `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/f2-retained-candidate-red-check.log`. `git diff --check` passes. No full suite was rerun.

No commit, push, merge, cloud mutation, or deployment was performed. Parent review remains open.


## Dual F1/F2 focused regression correction (2026-09-28)

The connected test now preserves both reviewed scenarios as separately named subtests sharing the same local fixture. `old-revision-receipt-reconciles-newer-confirmed-intent` restores the original generation-2 receipt at revision 1 with its pinned bootstrap evidence, then verifies the receipt remains unchanged while reconciliation creates a candidate for the newer confirmed revision-2 requirements and tags it active. `retained-candidate-exhaustion-retries-through-activation` uses a new generation-3 revision-2 receipt, exhausts before reconcile creation while its activated Candidate remains retained beside Active, and verifies same-work idempotent retry through candidate-ready reconciliation and tag activation.

The focused handler command in the preceding section passes with both subtests, plus exhausted-retry visibility, stale receipt, independent pending-candidate, and superseded-retry regressions. Updated output: `/Users/rayer/.hermes/profiles/chatgpt/artifacts/lwc209-f2f4-followup/f2-dual-scenarios-focused.log`. `git diff --check` passes. The full suite was not rerun.
