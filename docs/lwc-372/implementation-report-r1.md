# LWC-372 implementation report r1

Updated: 2026-10-07 22:08 UTC

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
