# LWC-372 implementation report r1

Updated: 2026-10-08 (r3 startup fail-closed and BFF CI Pkl repair)

## Current r3 repair run identity

- Model / effort: GPT-6-Luna / xhigh; same retained Codex session and worktree.
- Orca terminal/session handle: `term_a0d953c4-4e6a-42da-ba72-07ea1f78264d`; run `run_ec3a3eca0058`; task `task_991a5ca8672b`; dispatch `ctx_cf1aa9d667cc`. Orca exposed no separate model-session identifier.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-372-implementation-r1`, `Rayer/LWC-372-implementation-r1`.
- Reviewed starting SHA: `f68736b77c02db683f0ebc5b780fc596e9254cab`. r3 source/test commit: `6c107b5eea701b1361ff790b6579c37e145802a4`.
- PR: [#103](https://github.com/Rayer/llm-wiki-cloud/pull/103), base `develop`. The source/test commit is the locally verified implementation; a report-only follow-up is part of this publication. Exact remote PR head/base readback will be recorded in the updated PR body and worker completion message.

## Initial implementation run identity

- Model / effort: GPT-6-Luna / xhigh.
- Orca terminal/session handle: `term_a0d953c4-4e6a-42da-ba72-07ea1f78264d`; run `run_ec3a3eca0058`; task `task_97eba6a25f82`; dispatch `ctx_b7dded1bc936`. Orca exposed no separate model-session identifier.
- Worktree: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-372-implementation-r1`.
- Branch: `Rayer/LWC-372-implementation-r1`.
- Tested implementation commit: `4410819e4fdb984f6201e2bec0c883fa856a9b41`.
- PR: [#103](https://github.com/Rayer/llm-wiki-cloud/pull/103), base `develop`. At the implementation checkpoint, GitHub and the remote branch both read back head SHA `4410819e4fdb984f6201e2bec0c883fa856a9b41`; the reviewed PR body matched the published body. This report is a documentation-only follow-up commit.

## Frozen acceptance matrix — authoritative AC1–7

| Frozen AC | Current status | Evidence and boundary |
| --- | --- | --- |
| AC1 — one local/DEV/Prod environment config projects the Demo identity to Auth and BFF; retain the existing Prod Demo identity/data. | **PASS** for local/source wiring; **NOT RUN** for DEV/Prod runtime readback. | Pkl/rendered consumer wiring and the 32 Auth/BFF config contract tests under `make test` verify source delivery. No provider environment was read back in r3. |
| AC2 — create only when missing with a random password hash; existing UID/email/role/status/password/project/content stay unchanged; concurrency is safe; lookup errors are not missing; new Demo gets only an empty default project. | **PASS** for isolated local emulator acceptance. | `TestEnsureDemoAccountCreatesMissingAndPreservesExistingData`, `TestEnsureDemoAccountRejectsLegacyEmailCollisionWithoutMutation`, and `TestEnsureDemoAccountConcurrentStartupCreatesOneIdentity` passed on the r3 candidate. These use disposable loopback Firestore only. |
| AC3 — passwordless Demo endpoint and existing BFF restriction remain; ensure error/conflict returns 503 without a Demo session while general Auth remains available. | **PASS** for local emulator/router/issuer behavior; **NOT RUN** for DEV/Prod/browser behavior. | The worker-authored `TestReviewerStartupConflictMustDisableDemo` passed its four loopback cases (canonical mismatch, other reservation owner, storage error, valid missing account). Failure cases returned Demo 503 without cookie/session; ordinary password login returned 200. Existing BFF restriction/config tests passed under `make test`. This is worker evidence, not an independent reviewer PASS. |
| AC4 — local Pkl config generates `local_fixture.yaml` for `admin-local@llm.wiki.dev`, and the consumer reads that artifact without inline credentials or rewriting an existing account. | **PASS** for local generated-config and emulator consumer tests. | The Pkl fixture test checks the exact email, stable generated password, separate Demo JSON, and repeatable output; `TestEnsureLocalPasswordFixtureCreatesOnlyMissingScopedAccount` verifies scoped creation and preservation. |
| AC5 — the local test password is generated once, stays stable, is stored only as a hash, and supports ordinary login; it never enters DEV/Prod or the frontend bundle. | **PASS** for local fixture/config tests; **NOT RUN** for live environment readback. | Pkl generation and the local fixture emulator test passed; no DEV/Prod credential or bundle readback was performed. |
| AC6 — Demo and admin/test identities stay distinct in the current local scope; Demo is non-admin, the test UID is not Demo-restricted, and other scopes/environments remain untouched. | **PARTIAL PASS** for isolated local role/config separation and unchanged existing worktree scope; **NOT RUN** for native startup, cross-scope, or DEV/Prod readback. | The startup regression uses a fresh synthetic scope and a `member` Demo; the fixture acceptance uses an isolated scope and an `admin` test account. Smoke preserved the existing scope/key metadata. Full Auth/BFF/Frontend startup and other-scope/runtime readback were not run. |
| AC7 — preserve ordinary login, fixture no-reset, project behavior, and document Pkl paths and account differences with explicit evidence boundaries. | **PASS** for local/source checks. | The r2 local docs, r3 report, exact workflow assertion, and local regression suites cover the requested documentation and preserved behavior. Cloud/runtime acceptance remains **NOT RUN**. |

The labels in earlier draft checklists are superseded by the frozen AC1–7 definitions above. The commands and counts in the following sections are retained as historical evidence for their recorded candidate SHAs; the r3 evidence is listed at the end of this report.

## Verification commands

Commands were run in this worktree. `make test` is the final passing run after repairing its synthetic CD fixture; the fixture change preserves the existing config/source checks.

| Command | Exit | Result |
| --- | ---: | --- |
| `make test` | 0 | CD contract: 69 tests; Auth/BFF config contracts: 32; local fixture acceptance: 15; Go race suite: 1,532 passed, 89 skipped, 0 failed; frontend: 32 files, 292 tests passed. |
| `make lint` | 0 | ESLint passed. |
| `make typecheck` | 0 | TypeScript check passed. |
| `make build` | 0 | BFF `go build ./...` and frontend production build passed. |
| `make vet` / `go vet ./...` | 0 | Go vet passed. |
| `make workflow-yaml` | 0 | Four repository workflow YAML files validated. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:18765 go test ./internal/auth -run 'TestEnsureDemoAccount|TestEnsureLocalPasswordFixture' -count=1 -v` | 0 | Four named local Firestore acceptance tests passed, including concurrency, collision preservation, existing-data preservation, and no password reset. Emulator was local and stopped afterward. |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` | 0 | 113 deploy engine tests passed after committing the reviewed source/config candidate. |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` | 0 | 113 BFF script tests passed. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` | 0 | 32 Auth/BFF deployment contract tests passed. |
| `python3 ../../scripts/test_cd_contract.py CDContractTests.test_freeze_extracts_only_immutable_backend_handles_from_live_provider_shapes` | 0 | Targeted CD freeze contract passed. |
| `npm ci --no-audit --no-fund` | 0 | Installed 540 packages from the existing frontend lockfile; no manifest or lockfile changes. |
| `git diff --check` | 0 | No whitespace errors before commit. |

The 89 skips in the canonical Go run are emulator-dependent existing tests; the LWC-372 emulator acceptance tests were run separately and passed. A broader `FIRESTORE_EMULATOR_HOST=127.0.0.1:18765 go test ./... -v -count=1 -race` run exited 1 with 23 failure events in unrelated `internal/handler/v1`, `cmd/demo_password_rotate`, and `cmd/olw_worker` tests; the local emulator logged transaction lock timeouts. The canonical CI command with the emulator variable unset passed.

The first deploy-engine suite attempt, before the implementation commit, exited 1 on its existing `dirty-target-config` and `untracked-build-input` guards. The same suite passed all 113 tests after the explicit scoped commit; neither guard was changed. The first `make test` attempt also caught a stale synthetic CD plan fixture missing the new Auth identity input; that fixture was updated and the final `make test` run passed.

## Remaining limits

- Cloud deployment acceptance: **NOT RUN**. No DEV/Production provider action, GSM payload access, IAM/resource/credential operation, paid Pipeline action, deployment, or merge was performed.
- Live startup against the existing local worktree target: **NOT RUN** to preserve its scope, ports, and key. Local Firestore emulator acceptance and offline generated-config tests passed.
- PR #103 remains open for the coordinator's same-final-SHA TPM and independent `lwc-reviewer` review and normal merge workflow.

## Pkl bootstrap repair r2

The coordinator reported canonical CI run `37694425287`, job `113042441145`, failed on the then-current PR head `4877a7499d74e0d49c9732933b4cb4fb9027e125` in the existing `local-vertical-smoke` job: `make bootstrap` reached `apps/bff setup` → `local-fixture-config`, where `/bin/sh` reported `pkl: not found` (Pkl exit 127; bootstrap exit 2). This historical failure remains recorded as a failure; the local repair below does not claim that GitHub CI reran or passed.

Repair commit `325a0537ce9cb8204e392b83aca5765a0b625607` adds `scripts/ensure-pkl.sh`, pinned to Pkl `0.32.1` and the same public release/version-readback pattern already used by the repository's CD workflows. Root bootstrap, local config generation, smoke, and tests now ensure/use the pinned binary; direct BFF setup, local fixture generation, and BFF tests do the same. At the r2 checkpoint, LWC-372 did not change `.github/workflows/ci.yml`; the coordinator assigned the BFF job setup hunk to LWC-373, while r2 repaired the local-smoke `make bootstrap` path. The BFF job's direct Pkl test invocation remained unresolved at that checkpoint. The scoped r3 follow-up below manually integrates only the pinned Pkl CLI prerequisite from LWC-373; it does not cherry-pick that ticket's product changes.

### r2 repair result (historical)

The r2 local Pkl/bootstrap and loopback-smoke evidence below remains valid for that candidate. Its former AC1–7 labels are reconciled to the authoritative frozen mapping above; local smoke does not prove full native app startup, live environment readback, or cloud acceptance.

### Repair verification

All generated config and npm installation for bootstrap used `/tmp/lwc372-bootstrap.mVdDOW`; the frontend package files were copied there before setup. The target Pkl path started absent, exercising the downloader on this macOS arm64 host. The commands did not alter the checked-in dependency manifests.

| Command | Exit | Result |
| --- | ---: | --- |
| `bash -n scripts/ensure-pkl.sh && bash scripts/ensure-pkl.sh /Users/rayer/.local/bin/pkl` | 0 | Existing pinned Pkl `0.32.1` was accepted. |
| `make bootstrap PKL_BIN="/tmp/lwc372-bootstrap.mVdDOW/tools/pkl" CAC_OUTPUT_DIR="/tmp/lwc372-bootstrap.mVdDOW/cac" FRONTEND_DIR="/tmp/lwc372-bootstrap.mVdDOW/frontend"` | 0 | Downloaded the pinned Pkl CLI to the absent temporary path, rendered both Pkl outputs there, configured the temporary frontend, installed the pinned Synto runtime, downloaded Go modules, and installed 540 npm packages. The downloaded CLI read back as `Pkl 0.32.1 (macOS 26.5, native)`. `npm ci` reported 17 audit advisories (13 high, 3 moderate, 1 critical); no dependency manifests changed and this repair did not investigate those advisories. |
| `env -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-bootstrap.mVdDOW make smoke PKL_BIN="/tmp/lwc372-bootstrap.mVdDOW/tools/pkl"` | 0 | Actual canonical `make smoke` completed. Three named loopback/Auth tests passed (`TestLocalCloudLoopbackUsesBearerAndIgnoresUserHeaderIdentity`, `TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure`, `TestLocalRefreshCookiePolicySupportsLoopbackHTTP`); all four scope-filtered localcloud/firestore/gcs/localpipeline package commands passed; `TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider` passed; `scripts.test_local_dev_makefile` passed 15 tests; `test_local_vertical_smoke` passed 2 tests. Emulator hosts were explicitly unset. |
| `env -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-bootstrap.mVdDOW make test` | 0 | CD contract 69; Auth/BFF config contracts 32; local fixture tests 15; Go race suite 1,532 PASS markers and 89 existing emulator-dependent SKIP markers; frontend 32 files / 292 tests. |
| `PATH="/tmp/lwc372-bootstrap.mVdDOW/tools:$PATH" PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-bootstrap.mVdDOW python3 -m unittest discover -s scripts -p 'test_*.py'` (from `apps/bff`) | 0 | BFF CI legacy script suite: 113 tests. This exercised the Pkl local fixture acceptance through the temporary pinned CLI on `PATH`. |
| `PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-bootstrap.mVdDOW python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (from `apps/bff`) | 0 | Deploy engine suite: 113 tests, after committing the scoped source repair. |
| `make lint`, `make typecheck`, `make vet`, `make build`, `make workflow-yaml` | 0 each | Existing frontend lint/typecheck, Go vet/build, frontend production build, and workflow YAML checks passed. |
| `git diff --check` | 0 | No whitespace errors before the scoped commit. |

The first shell wrapper around `make smoke` exited before printing its captured result because zsh reserves the variable name `status`; the completed log ended with the smoke success line, and a corrected wrapper reran the same command and captured exit `0`. This was a result-capture issue, not a smoke failure. Bootstrap printed only the existing local target summary; the scope/signing-key file modification times and modes matched their pre-run values, the key was never printed, and generated outputs/frontend config were kept under `/tmp`. The apps were not started on their configured ports; the smoke used its existing synthetic loopback tests. No emulator, provider, or live storage access was used.

### Repair publication state

- Code repair commit: `325a0537ce9cb8204e392b83aca5765a0b625607`; based on the prior PR head `4877a7499d74e0d49c9732933b4cb4fb9027e125`.
- Historical r2 checkpoint: PR [#103](https://github.com/Rayer/llm-wiki-cloud/pull/103) targeted `develop`; this earlier report said the r2 commit was prepared for normal fast-forward publication. The later r2 publication checkpoint and r3 repair are recorded below; this sentence does not describe the current branch state.
- Repair worker identity: Codex GPT-6-Luna / xhigh; terminal `term_a0d953c4-4e6a-42da-ba72-07ea1f78264d`; Run `run_ec3a3eca0058`; Task `task_fddbc0927dfc`; Dispatch `ctx_a92a65123028`. Orca did not expose a separate model-session identifier. The worker-show process incarnation was `5dd17861-8dc3-4e76-9dcb-2c44461a9e5e::/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-372-implementation-r1@@a6dec75a:4dc0210d-d780-4d41-896d-552106969000`.
- Cloud deployment acceptance remains **NOT RUN**. The repaired head still needs the coordinator's canonical CI rerun and same-final-SHA TPM plus independent `lwc-reviewer` review; merge remains coordinator-owned.

## r3 startup fail-closed and BFF CI Pkl repair

### Preserved review and CI findings

The exact-f687 supervisor request `b53e327dd89f37a4eb9a9c6a` completed **HOLD**. The independent reviewer reproduced canonical-email mismatch and reservation-other-owner cases in reviewer-owned scratch: startup ensure returned `ErrDemoIdentityConflict`, but `/api/v1/auth/demo` still returned 200 and set a refresh cookie while ordinary Auth login returned 200. The reviewer files were not edited. The coordinator's TPM review is recorded at `/Users/rayer/.hermes/workflows/lwc/lwc372-tpm-pr-review-f68736b7.md`; both reviews are historical f687 findings, not final-SHA acceptance.

The coordinator also reported the f687 BFF CI direct test failing because the test starts bare `pkl` without a runner prerequisite. Earlier local-vertical CI run `37694425287`, job `113042441145`, remains historical red evidence on SHA `4877a7499d74e0d49c9732933b4cb4fb9027e125`: `/bin/sh: pkl: not found` (Pkl exit 127; bootstrap exit 2). The r2 local repair did not clear either remote failure, and this worker did not trigger or claim a canonical CI rerun.

### Code and test changes

- `ensureDemoAccountAtStartup` now returns readiness to `main`; `main` passes it to the production router. A failed/conflicting Demo ensure replaces only the Demo handler with a 503 response before identity lookup or durable-session issuance. The ordinary `/login` route is unchanged.
- `apps/bff/cmd/auth/startup_demo_test.go` uses the reviewer's reproduction name, `TestReviewerStartupConflictMustDisableDemo`, and a disposable loopback Firestore emulator. Its canonical-mismatch, other-reservation-owner, and storage-error cases assert 503, no refresh cookie, and zero Demo sessions; each also asserts normal Auth login still returns 200. The valid missing-account control asserts Demo 200 and one durable session.
- `.github/workflows/ci.yml` manually adds only the pinned Pkl `0.32.1` prerequisite from LWC-373 commit `77377073fa4cf51d27da632875715faa1844097c`. It exports `PKL_BIN` and `PKL_CACHE_DIR` and appends `$RUNNER_TEMP` to `$GITHUB_PATH`, because the existing BFF test invokes the `pkl` command directly in a child process. A local workflow assertion checks that exact path setup; no LWC-373 cooldown product changes were copied and its worktree was not edited.

### r3 local verification

All emulator data used a fresh synthetic local scope on the disposable loopback Firestore emulator at `127.0.0.1:19873`. The emulator was stopped after acceptance. `make smoke` explicitly unset both emulator endpoints and did not start Auth, BFF, or Frontend. The worktree's existing scope, signing-key file, Synto interpreter permissions, and modification times were unchanged; the key was not printed.

| Command | Exit | Result |
| --- | ---: | --- |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:19873 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp go test ./cmd/auth -run '^TestReviewerStartupConflictMustDisableDemo$' -count=1 -v` | 0 | Four named subtests passed: `canonical_mismatch`, `reservation_owned_by_another_user`, `storage_error`, and `valid_missing_identity`. This is worker-authored emulator evidence, not independent review. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:19873 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp go test ./cmd/auth -count=1` | 0 | Complete affected Auth command package passed with the loopback emulator available. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:19873 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp go test ./internal/auth -run 'TestEnsureDemoAccount|TestEnsureLocalPasswordFixture' -count=1 -v` | 0 | Four named emulator acceptance tests passed: missing-only and existing-data preservation, legacy collision, concurrent startup, and local fixture no-reset/ordinary-login behavior. |
| `env -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/tmp/lwc372-r3-emulator.dfTvON/pkl-cache make smoke` | 0 | Canonical loopback smoke passed: three named Auth/BFF tests; four localcloud/firestore/gcs/localpipeline package commands; `TestLocalSyntoRuntimeUsesPinnedVenvWithoutProvider`; 16 local Makefile tests; and 2 smoke-contract tests. |
| `env -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/tmp/lwc372-r3-emulator.dfTvON/pkl-cache make test` | 0 | CD contract 69; Auth/BFF config 32; local Makefile 16; Go race suite 1,531 PASS / 91 SKIP / 0 FAIL; frontend Node tests 524 and component tests 292 passed. Emulator variables were unset, so 91 emulator-dependent skip markers (including the new startup test) are recorded as skips, not passes; the applicable Demo and fixture emulator suites are reported separately above. |
| From `apps/bff`: `PATH="/Users/rayer/.local/bin:$PATH" PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp CAC_OUTPUT_DIR=/tmp/lwc372-r3-emulator.dfTvON/bff-cac PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/tmp/lwc372-r3-emulator.dfTvON/pkl-cache python3 -m unittest discover -s scripts -p 'test_*.py'` | 0 | 114 BFF legacy script tests passed, including the actual child-process Pkl fixture test and the new CI `PATH` assertion. |
| From `apps/bff`: `PYTHONDONTWRITEBYTECODE=1 TMPDIR=/tmp/lwc372-r3-emulator.dfTvON/tmp python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` | 0 | 113 deploy-engine tests passed on the clean source/test commit `6c107b5eea701b1361ff790b6579c37e145802a4`. |
| `go run ./cmd/versioncheck VERSION` (from `apps/bff`) | 0 | Version check printed `1.0.0`. |
| `make test-flash-execution` (from `apps/bff`) | 0 | Pinned Synto Flash wire contract passed with 11 named PASS records using a temporary venv under the private `TMPDIR`; no provider credentials were used. |
| `make lint`; `make typecheck`; `make vet`; `make build`; `make workflow-yaml` | 0 each | ESLint, TypeScript, Go vet/build, frontend production build, and all four workflow YAML validations passed. |
| `git diff --cached --check` | 0 | No whitespace errors before source/test commit `6c107b5eea701b1361ff790b6579c37e145802a4`. |

The first local emulator launch exited 1 because `/usr/bin/java` was the macOS runtime stub; rerunning with the installed OpenJDK directory first on `PATH` started the isolated emulator. Early regression-harness attempts also caught and corrected a missing test CORS origin and an emulator-host override in the synthetic storage-error client. The final exact reviewer reproduction command above passed; these harness corrections did not change application behavior.

### r3 acceptance limits and publication checkpoint

- Frozen AC1 and AC6 DEV/Prod runtime readback, browser Demo-button behavior, full native Auth/BFF/Frontend startup, and cross-worktree/environment verification remain **NOT RUN**. The r3 tests prove isolated local behavior only.
- Cloud deployment acceptance remains **NOT RUN**. No provider, live Firestore/GCS, GSM payload, IAM/resource/credential operation, paid Pipeline, deployment, or merge was performed.
- Source/test commit `6c107b5eea701b1361ff790b6579c37e145802a4` is the locally verified implementation on PR #103. The final remote PR SHA/base, final-body readback, canonical CI, and same-final-SHA TPM plus independent `lwc-reviewer` reviews are coordinator-owned. Merge authority remains with the coordinator.
