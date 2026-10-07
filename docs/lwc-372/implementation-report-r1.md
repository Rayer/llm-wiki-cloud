# LWC-372 implementation report r1

Updated: 2026-10-07 22:29 UTC (Pkl bootstrap repair evidence appended)

## Run identity

- Model / effort: GPT-6-Luna / xhigh.
- Orca terminal/session handle: `term_a0d953c4-4e6a-42da-ba72-07ea1f78264d`; run `run_ec3a3eca0058`; task `task_97eba6a25f82`; dispatch `ctx_b7dded1bc936`. Orca exposed no separate model-session identifier.
- Worktree: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-372-implementation-r1`.
- Branch: `Rayer/LWC-372-implementation-r1`.
- Tested implementation commit: `4410819e4fdb984f6201e2bec0c883fa856a9b41`.
- PR: [#103](https://github.com/Rayer/llm-wiki-cloud/pull/103), base `develop`. At the implementation checkpoint, GitHub and the remote branch both read back head SHA `4410819e4fdb984f6201e2bec0c883fa856a9b41`; the reviewed PR body matched the published body. This report is a documentation-only follow-up commit.

## Frozen acceptance matrix

| AC | Result | Evidence |
| --- | --- | --- |
| AC1: Development and Production configure Demo UID, email, and non-admin role for Auth. | PASS | `cmd/deploy_config` validation and rendered component inputs; `make test` Auth/BFF config contract suite (32 tests). |
| AC2: The same configured UID reaches BFF pipeline restrictions in both environments. | PASS | Go deploy config tests and offline Auth/BFF provider contract tests under `make test`. |
| AC3: Startup provisions only a missing Demo account with an unreturned random bcrypt password, canonical email reservation, and empty default project; concurrent starts create one identity. | PASS | Local Firestore emulator tests: `TestEnsureDemoAccountCreatesMissingAndPreservesExistingData`, `TestEnsureDemoAccountRejectsLegacyEmailCollisionWithoutMutation`, and `TestEnsureDemoAccountConcurrentStartupCreatesOneIdentity`. |
| AC4: Existing accounts, project data, passwords, inactive accounts, and email conflicts are not reset or transferred; unsafe Demo identities fail closed. | PASS | Emulator preservation/collision tests; `TestEnsureLocalPasswordFixtureCreatesOnlyMissingScopedAccount`; Demo handler tests for inactive, admin, email-mismatch, and unavailable identities. |
| AC5: Passwordless Demo login is mounted for local and deployed Auth, validates the configured identity, and returns 503 when its identity store or account is unavailable. | PASS | `TestLocalAuthDemoRouteIsMountedAndFailsClosedWithoutIdentityStorage`, `TestDeployedDemoRouteIsMountedAndFailsClosedWithoutIdentityStorage`, and Demo handler unit tests. |
| AC6: A Demo ensure error does not abort normal Auth startup; the local admin/test fixture remains separate and reads one stable password from Pkl-generated YAML. | PASS for code/config wiring; live worktree startup NOT RUN | Startup ensure logs and continues; strict YAML reader tests; Pkl local config acceptance verifies exact admin email, stable password across renders, and a separate non-secret Demo JSON. Actual local app startup was not run to preserve this worktree's existing scope, ports, and key. |
| AC7: Docs explain the split identities, no-reset behavior, conflict behavior, and offline/cloud verification boundary. | PASS | Root and BFF local development docs updated; this report records the AC and evidence. |

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

Repair commit `325a0537ce9cb8204e392b83aca5765a0b625607` adds `scripts/ensure-pkl.sh`, pinned to Pkl `0.32.1` and the same public release/version-readback pattern already used by the repository's CD workflows. Root bootstrap, local config generation, smoke, and tests now ensure/use the pinned binary; direct BFF setup, local fixture generation, and BFF tests do the same. The final LWC-372 repair commit does not change `.github/workflows/ci.yml`: the coordinator assigned the separate BFF CI job setup hunk to LWC-373, while LWC-372 owns fixing the `make bootstrap` path used by the local-smoke job. The BFF job's direct Pkl test invocation remains assigned to that separate integration.

### Repair acceptance follow-up

| AC | Repair evidence and current result |
| --- | --- |
| AC1–5 | Prior PASS entries and evidence above remain the source of truth; this setup repair did not change their implementation. |
| AC6 | Pkl fixture generation and local vertical smoke now have executable PASS evidence below. Full native Auth/BFF/Frontend startup and live scoped account/data mutation remain **NOT RUN**; the canonical smoke is loopback/in-memory testing and does not start the three apps or access Firestore/GCS. |
| AC7 | PASS. `apps/bff/docs/LOCAL_DEV.md` now documents pinned Pkl bootstrap and the configured `PKL_BIN` behavior. |

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
- PR: [#103](https://github.com/Rayer/llm-wiki-cloud/pull/103), base `develop`; the repair commit is prepared for normal fast-forward publication. The exact post-push PR/head/base readback is recorded in the worker completion message.
- Repair worker identity: Codex GPT-6-Luna / xhigh; terminal `term_a0d953c4-4e6a-42da-ba72-07ea1f78264d`; Run `run_ec3a3eca0058`; Task `task_fddbc0927dfc`; Dispatch `ctx_a92a65123028`. Orca did not expose a separate model-session identifier. The worker-show process incarnation was `5dd17861-8dc3-4e76-9dcb-2c44461a9e5e::/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-372-implementation-r1@@a6dec75a:4dc0210d-d780-4d41-896d-552106969000`.
- Cloud deployment acceptance remains **NOT RUN**. The repaired head still needs the coordinator's canonical CI rerun and same-final-SHA TPM plus independent `lwc-reviewer` review; merge remains coordinator-owned.
