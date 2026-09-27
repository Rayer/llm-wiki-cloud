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
