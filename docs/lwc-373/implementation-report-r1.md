# LWC-373 implementation report r1

## Execution record

- Frozen contract: `lwc373-spec-r2`, SHA-256 `57d1719a11ea51a9ef54cd7aa84123f2f5c72ef37de3e736cfa220034deea122`.
- Source baseline: `origin/develop` `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`; isolated worktree started clean on `Rayer/LWC-373-implementation-r1`.
- Final implementation and test SHA before this report: `fc55ba8cd8563ff66a3b66ad71d0fda63f649013`.
- Commits: `3aa8ea74c0bbe6fd31a5ae2212c0c7ffacb71c60` (implementation), `a09a78c5d6d2e304e4f03fb0ae06dedc36ec6ff7` (local child-to-consumer test), `fc55ba8cd8563ff66a3b66ad71d0fda63f649013` (quota HTTP acceptance tests).
- Runtime readback: provider/model `codex` / `gpt-6-luna`; requested and effective launch effort were `null` in `worker-show` (no effort value was exposed). Orca runtime/session `8bc79eed-2312-4c07-a306-f91ae8d31716`; dispatch `ctx_1d476c9d7079`; terminal `term_38a9f6bc-26c3-4c02-94e0-e4c60eae9ac6`.
- Toolchain: Pkl 0.32.1; Go 1.26.5 (`darwin/arm64`); Python 3.14.6.
- PR: [#105](https://github.com/Rayer/llm-wiki-cloud/pull/105), base `develop` at `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`. Creation readback reported head `804bac045dc75305450cc590284aae558a4153cd`, matching the pushed branch ref; this report-only follow-up advances the PR head, whose final readback is included in the worker completion receipt.

## Delivered scope

Added typed local/dev/prod cooldown values of 60/600/3600 seconds to the project SSOT. The existing prepare command now supports `--target pipeline|bff`, with Pipeline remaining the default; BFF-only generation emits the exact small `bff.json` projection without requiring Pipeline timeout or secret resolution. Root config Make targets accept `CAC_TARGET` and preserve their Pipeline default.

Managed local BFF starts regenerate and validate the projection from the current worktree, then set `PIPELINE_COOLDOWN_SECONDS` only in the BFF child. Offline deployment admission generates the selected environment projection and includes the value in normalized BFF/component inputs; the existing Auth config adapter emits and verifies the managed cooldown environment variable. Added tests cover duration boundaries, UTC rollover and changed-duration re-evaluation, local env precedence, admission identity, adapter readback, and HTTP quota behavior. No refund-policy implementation or accepted-worker-failure charging behavior was changed.

## Acceptance matrix

| AC | Result | Evidence |
|---|---|---|
| AC1 — SSOT and real generation | PASS | Real Pkl/prepare generated local 60, dev 600, prod 3600 with exactly the three required projection fields and only `bff.json` in each output directory. The BFF-only commands ran with Pipeline keys, timeout, Secret Manager reference, and Google credentials removed. The default Pipeline target retained its existing three artifacts, timeout field, no cooldown field, and `private-bindings.json` mode `0600` using only a synthetic local key. |
| AC2 — consumer and precedence | PASS | A controlled workerless subprocess generated the real local projection, replaced inherited `PIPELINE_COOLDOWN_SECONDS=777` with `60` for the BFF child, then ran `TestLoadedCooldownOverrideReachesQuotaConsumer`; `config.Load` selected env `60` over TOML `900`, and `pipelineLimits` used 60 seconds. Invalid projection and failed replacement behavior are covered by `TestInvalidBFFTargetDoesNotReplacePriorProjection`; legacy config defaults/fallbacks remain covered by the Go suite. |
| AC3 — clock boundaries | PASS | `TestEnvironmentCooldownValuesRespectExpiryBoundary` covers before/equal/after expiry at 60/600/3600 seconds. `TestDurationChangesReevaluatePersistedTimestampAcrossUTCDay` checks duration changes across UTC rollover without clearing the stored timestamp or daily count. |
| AC4 — HTTP and quota regression | PASS | Controlled fake-store tests cover successful POST reservation/count/timestamp before invocation (`TestPipelineRunReservesQuotaBeforeInvoke`), cooldown HTTP 429 without invocation (`TestPipelineRunBlocksCooldownWithoutInvoke`), GET state read without reservation or mutation (`TestPipelineStatusQuotaEvaluationDoesNotReserve`), daily-limit 429 without invocation, and the existing invoke-failure refund contract. The current 371 accepted/terminal failure settlement remains outside this ticket. |
| AC5 — local BFF path | PASS | Real Pkl projection → managed child environment mapping → actual Go `config.Load`/quota consumer subprocess passed with inherited stale cooldown and Pipeline inputs removed. `test_generated_cooldown_reaches_only_the_bff_child` checks Auth/Frontend isolation; wrapper contract tests cover `local-start`, `support-frontend`, and direct `bff-local` command routing. No local service, emulator, port, scope, or signing-key state was started or changed. |
| AC6 — cloud configuration chain | PASS offline | `engine.admit` ran for development and production with no provider or secret access; normalized BFF and component inputs contained 600/3600 and produced plan identities. The 33-test Auth/BFF config suite covers desired env and exact effective readback through `service_matches`, including wrong-value rejection before traffic. |

Formal cloud deployment/runtime rollout and cloud readback remain **NOT RUN**. No DEV/Prod provider action, GSM payload access, IAM/resource change, paid Pipeline call, or merge was performed; offline results are not represented as cloud verification.

## Verification commands and results

All listed commands exited 0. Python contract suites below were run at `a09a78c5d6d2e304e4f03fb0ae06dedc36ec6ff7`; the only later source change through `fc55ba8` adds Go test cases, with no Python, runtime, SSOT, or deployment implementation changes. Final Go verification and all AC-specific real-generation/admission checks were run at `fc55ba8cd8563ff66a3b66ad71d0fda63f649013`.

| Command | Result |
|---|---|
| `python3 ../../scripts/test_cd_contract.py` (from `apps/bff`) | exit 0; 70 tests |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (from `apps/bff`) | exit 0; 113 tests |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | exit 0; 33 tests |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (from `apps/bff`) | exit 0; 115 tests |
| `python3 scripts/test_local_dev_makefile.py` (from `apps/bff`) | exit 0; 17 tests |
| `python3 scripts/test_engine_workflow.py` (repo root) | exit 0; 8 tests |
| `go test ./cmd/pipeline_config ./cmd/deploy_config ./internal/config ./internal/pipelinequota ./internal/handler/v1 -count=1` | exit 0; 5 packages |
| Focused AC3/AC4 `go test ./internal/pipelinequota ./internal/handler/v1 -run '^(TestEnvironmentCooldownValuesRespectExpiryBoundary|TestDurationChangesReevaluatePersistedTimestampAcrossUTCDay|TestLoadedCooldownOverrideReachesQuotaConsumer|TestPipelineRunBlocksCooldownWithoutInvoke|TestPipelineRunReservesQuotaBeforeInvoke|TestPipelineStatusQuotaEvaluationDoesNotReserve|TestPipelineRunBlocksDailyLimit|TestPipelineRunRefundsOnInvokeFailure)$' -count=1` | exit 0; 2 packages |
| `go vet ./...` | exit 0 |
| `go test ./... -v -count=1 -race` | exit 0; 55 packages (including packages with no test files) |
| `go build ./...` | exit 0 |
| `make test-flash-execution` | exit 0; 12 synthetic wire-contract checks |
| Real Pkl: `make config-local CAC_TARGET=bff`, `make config-dev CAC_TARGET=bff`, `make config-prod CAC_TARGET=bff` with temp output | exit 0 for all three; exact output asserted |
| Pipeline compatibility: `make config-local CAC_TARGET=pipeline` with temp output and synthetic key/timeout `23` | exit 0; legacy artifacts/timeout/private mode asserted |
| Local child-to-consumer acceptance: real Pkl output, `local_services.child_environment`, then `go test ./internal/handler/v1 -run '^TestLoadedCooldownOverrideReachesQuotaConsumer$' -count=1` | exit 0; env `60` observed by the actual quota consumer |
| Offline plan admission for `development` and `production` at SHA `fc55ba8cd8563ff66a3b66ad71d0fda63f649013` | exit 0; normalized/component cooldowns 600/3600 and plan IDs emitted |

At the initial r1 implementation checkpoint, frontend npm lint/typecheck/test/build were not run because no frontend files changed. Repair r2 ran the full canonical frontend commands below; fresh canonical GitHub CI and same-final-SHA reviews remain with the coordinator.

## PR105 supervisor repair r2 — 2026-10-07 UTC

### Receipt and findings

- Frozen scope remains `lwc373-spec-r2`; source baseline and target remain `develop` at `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`. PR [#105](https://github.com/Rayer/llm-wiki-cloud/pull/105) was open at reviewed head `a0fb682a07b0f9345a02bf9eec104ee6af7156c9` when repair began; its base was independently read as the same `develop` SHA.
- Repair task/dispatch: `task_bd867a7eb268` / `ctx_0f3a5caf0969`. Reused terminal `term_38a9f6bc-26c3-4c02-94e0-e4c60eae9ac6`, process incarnation `e7de0083-1ff3-4166-a205-ce8214f14cd1`, generation 8, worktree `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-373-implementation-r1`. The dispatch requested the existing Codex Luna xhigh/YOLO context. The durable launch receipt records provider `codex` but leaves model/effort null for the reused terminal; the prior worker record identifies `gpt-6-luna` and also had no effective effort value, so no more specific runtime effort is claimed.
- Code and regression tests are in commit `77377073fa4cf51d27da632875715faa1844097c`, tested with real Pkl 0.32.1. Report-only changes are recorded separately after the code commit.
- Historical canonical CI run `37694980162`, job `113044365792`, failed at head `a0fb682a` in `test_real_action_terminal_failure_explicit_retry_and_receipt_expansion` (engine test line 536) with admission `command-failed`. The BFF CI job had no Pkl install step, unlike the pinned release jobs. The actual Go producer command, run with a deliberately unavailable `PKL_BIN`, returned inner stderr `evaluate selected Pipeline SSOT: fork/exec /lwc-test/missing-pkl: no such file or directory: exit status 1`; the local installed tool reports Pkl 0.32.1. The repair adds the established pinned 0.32.1 install to the BFF job, and preserves the workflow's `PKL_BIN`/`PKL_CACHE_DIR` through the isolated Action test environment so the installed tool reaches the real admission child. The original CI run did not print a direct `command -v pkl` result; the diagnosis is supported by the absent setup step and reproduced inner error.

### Repair acceptance matrix

| Finding | Result | Evidence |
|---|---|---|
| BFF projection must match the authoritative source while source and executor can differ | PASS | Admission runs real Pkl prepare and the deploy-config normalizer, then checks the working `deploy/cac/ssot.pkl` Git blob against the selected source commit. The fixture uses a config-only source commit at cooldown 601 with a distinct executor; admission succeeds, the normalized projection is 601, and the BFF build identity equals the executor identity. The same chain with uncommitted 602 produces a real Pkl projection and normalized 602, then fails admission as `dirty-bff-ssot`. No hardcoded or shallow-only source SHA is used; config-only changes do not force an image rebuild. |
| Immutable legacy BFF plan with no cooldown remains recoverable | PASS | An offline fake-provider fixture loads a retained plan without the field, calls `Providers.service_matches`, `observe`, and `reconcile_candidate`, then reactivates the candidate without a new build. The legacy plan and receipt bytes are unchanged through recovery. Newly admitted plans remain strict: `LoadWithBFFProjection` rejects a missing generated projection and the Go invalid-input test explicitly rejects a projection missing `pipeline_cooldown_seconds` plus wrong schema/environment/value/shape. |
| Canonical BFF CI has the real Pkl prerequisite | PASS locally; fresh remote CI pending | Pinned public Pkl 0.32.1 setup was added to the BFF job using the existing CD workflow pattern. The real producer/normalizer/action admission regression passes with explicit `PKL_BIN` and `PKL_CACHE_DIR`. The new canonical workflow run will be read by the coordinator after this head is pushed; no CI run is represented as complete here. |
| Frozen LWC-373 AC and cloud boundaries | PASS offline / NOT RUN cloud | The existing AC1–AC6 evidence remains applicable; repair keeps scalar values 60/600/3600, Pipeline default behavior, and source/config-only identity separation. The repair and full canonical local smoke used no provider, GSM, IAM, deploy, or paid Pipeline action. Formal DEV deployment/runtime readback remains **NOT RUN**. |

### Repair verification

| Command | Result |
|---|---|
| `env PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages python3 -m unittest test_build_submission.AsyncBuildSubmission.test_bff_admission_binds_real_projection_to_source_not_executor` (from `deploy/engine/tests`) | exit 0; 1 named integration test, source/executor split and dirty 601→602 control |
| `python3 -m unittest test_engine.Acceptance.test_legacy_bff_retained_plan_without_cooldown_recovers_and_reactivates` (from `deploy/engine/tests`) | exit 0; 1 named retained-provider recovery test |
| `go test ./cmd/deploy_config -run '^TestGeneratedBFFCooldownProjectionRejectsInvalidInputs$' -count=1` (from `apps/bff`) | exit 0; missing and invalid new-plan projection cases rejected |
| `python3 ../../scripts/test_cd_contract.py` (from `apps/bff`) | exit 0; 70 tests |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (from `apps/bff`) | exit 0; 115 tests |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | exit 0; 33 tests |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (from `apps/bff`) | exit 0; 115 tests |
| `go vet ./...` (from `apps/bff`) | exit 0 |
| `go test ./... -v -count=1 -race` (from `apps/bff`) | exit 0; Firestore-emulator-dependent tests reported SKIP: `TestProductionRouterUsesSharedCLIAndWebAuthAuthorities` requires the local emulator, and `TestEmulatorPasswordRotationChangesOnlyPasswordAndKeepsSession` / `TestEmulatorTargetCannotUseProductionOrDefaultDatabase` require unset `LWC366_FIRESTORE_EMULATOR_HOST`; these skips are not claimed as coverage |
| `go build ./...` and `make test-flash-execution` (from `apps/bff`) | both exit 0; the flash command reported 12 synthetic wire-contract checks |
| `make workflow-yaml` and `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` | both exit 0 |
| `node --experimental-strip-types --test tests/ci-workflow-contract.test.mjs` (from `apps/frontend`, after `npm ci`) | exit 0; 5 tests. The first attempt exited 1 because `js-yaml` was absent; standard `npm ci` installed the declared frontend dependencies before rerun. |
| `npm run lint`, `npm run typecheck`, `npm test`, `npm run build` (from `apps/frontend`) | all exit 0; lint/typecheck/build passed, Node suite 524 tests, component suite 292 tests |
| `make smoke` (repo root) | exit 0; canonical offline loopback smoke, including the local cooldown child-isolation tests, 17 local Makefile tests, and 2 smoke-script contract tests |

No local emulator test was counted as covered when skipped, and no result above verifies cloud deployment. The coordinator retains ownership of remote CI, same-final-SHA TPM/lwc-reviewer review, and normal merge.

## PR105 supervisor repair r3 — 2026-10-07 UTC

### Provenance and disposition

- Frozen contract remains `lwc373-spec-r2`, SHA-256 `57d1719a11ea51a9ef54cd7aa84123f2f5c72ef37de3e736cfa220034deea122`; base remains `develop` at `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`. Live LWC-373 comment `4-2038` reconciles the exact `b9dd833e3efac862e058bc68a96933b26465c576` reviews as HOLD only for an old retained BFF plan whose revision already has an unmanaged cooldown env. The comment explicitly keeps the source/executor and Pkl fixes passed, calls this an original compatibility blocker rather than new scope, and authorizes this same-worker repair without merge/deploy.
- Current repair Task/Dispatch: `task_04d5c826eced` / `ctx_7b9283606e2e`; terminal `term_38a9f6bc-26c3-4c02-94e0-e4c60eae9ac6`; Orca runtime `8bc79eed-2312-4c07-a306-f91ae8d31716`; process incarnation `e7de0083-1ff3-4166-a205-ce8214f14cd1`; worktree `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-373-implementation-r1`. The task context specified the retained Codex Luna xhigh/YOLO session; `worker-show` confirms provider `codex` / model `gpt-6-luna`, while its reused-terminal launch receipt leaves requested/effective effort null. No stronger effort readback is claimed.
- Repair implementation and regressions are in code commit `420d27406866a213bfcd1f57ca922fbc118da0b3`. PR #105 remains open against `develop`; the complete pre-update PR body was read before publication. The report commit and final pushed PR head are read back separately in the worker completion receipt.

### Repair result

`auth_config.effective` now accepts the plan's managed-field selection for `PIPELINE_COOLDOWN_SECONDS`. Both the real CLI adapter verification and `Providers.service_matches` pass that selection based on whether the plan's `desired` env includes the field. A legacy plan with no cooldown field therefore ignores a pre-existing revision override during candidate verification, `observe`, reconciliation, and reactivation; a plan that contains the new field still parses and validates the effective value. BFF whole-template fingerprints, image identity, rollback behavior, source/executor separation, and the Pkl child setup were left intact.

The new test matrix drives the actual BFF shell adapter and the offline fake-provider engine through development and production plans, each with and without the pre-existing cooldown. In all four provider cases the immutable plan and receipt bytes remain unchanged across reactivation, and the build/submit count does not increase after the retained artifact is created. Production uses a real `deploy_config` normalized plan and the retained development receipt. The separate new-plan negative test still rejects missing/invalid generated cooldown projections, and the existing shell readback test still rejects a wrong effective value.

### Preserved RED/GREEN evidence

- Before the patch, `python3 -m unittest test_bff_auth_config_contract.BFFQueryConfigTests.test_legacy_plan_leaves_existing_cooldown_unmanaged_in_both_environments` (from `scripts/`) exited 1: the no-existing-env cases passed, while the retained-env cases failed effective readback in both development and production with the adapter's value-suppressed contract mismatch.
- Before the patch, `python3 -m unittest test_engine.Acceptance.test_legacy_bff_retained_plan_without_cooldown_recovers_and_reactivates` (from `deploy/engine/tests/`) exited 1: the no-existing-env cases passed, while the retained-env cases in development and production ended in `basic-sanity-mismatch` through the real candidate poll. An initial fixture attempt placed the production normalizer under the engine's fake `PATH`; it was moved to class setup beside the existing real-Go fixture setup before collecting this causal RED result.
- After the patch, the focused CLI command `python3 -m unittest test_bff_auth_config_contract.BFFQueryConfigTests.test_legacy_plan_leaves_existing_cooldown_unmanaged_in_both_environments test_bff_auth_config_contract.BFFQueryConfigTests.test_cooldown_is_explicit_and_effective_readback_must_match` exited 0 (`Ran 2 tests`, 3.449s); its legacy test covers all four environment/override subcases, while the existing strict wrong-value rejection remains green.
- After the patch, the focused engine command above exited 0 (`Ran 1 test`, 5.145s); it executes all four environment/override combinations through retained artifact observation, `reconcile_candidate`, and reactivation, including unchanged plan/receipt bytes and build/submit counts.

### Current verification

| Command | Result |
|---|---|
| `env PKL_BIN=/Users/rayer/.local/bin/pkl PKL_CACHE_DIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (from `apps/bff`) | exit 0; 115 tests, 130.571s. Includes retained source/executor, dirty-SSOT, Pkl admission/receipt-expansion, provider recovery, and image-based rollback/managed-safety regressions. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | exit 0; 34 tests, 97.969s. |
| `go test ./cmd/deploy_config -run '^TestGeneratedBFFCooldownProjectionRejectsInvalidInputs$' -count=1` (from `apps/bff`) | exit 0; named missing/invalid new-projection contract test. |
| Focused CLI and provider RED/GREEN commands above | RED was reproduced before the code change; both GREEN commands exited 0 after it. |
| `make smoke` (repo root, with a disposable Python `TemporaryDirectory` as `TMPDIR`) | exit 0; canonical loopback smoke, 17 local Makefile tests, 2 smoke-script contract tests, and the pinned local Synto runtime test. No persistent stack was started. |
| `git diff --check`; `python3 -m py_compile deploy/components/auth_config.py deploy/engine/providers.py deploy/engine/tests/test_engine.py scripts/test_bff_auth_config_contract.py` | both exit 0. |

### Acceptance and remaining limits

| Repair criterion | Result |
|---|---|
| Legacy dev/prod plan with existing or absent cooldown recovers through the real CLI and Providers caller | PASS; four subcases each, including retained-artifact observe/reconcile/reactivate and unchanged plan/receipt bytes. |
| No fresh build/submit during legacy candidate reactivation | PASS; build/submit counts remained unchanged after the candidate artifact was retained. |
| New plan missing/invalid/wrong cooldown remains rejected | PASS; Go generated-projection negative test and existing exact CLI readback rejection both pass. |
| Earlier dirty source/executor identity and BFF Pkl child repairs | PASS retained; no source identity, workflow setup, or build logic changed, and their named regressions are included in the passing 115-test engine suite. |
| Formal cloud deployment/runtime readback | **NOT RUN**. No provider/GSM/IAM/resource/credential/paid Pipeline action or merge was performed. |
| Fresh canonical remote CI and same-final-SHA TPM/lwc-reviewer review | Pending coordinator readback after this push. Main retains normal merge ownership. |
