# LWC-373 implementation report r1

## Execution record

- Frozen contract: `lwc373-spec-r2`, SHA-256 `57d1719a11ea51a9ef54cd7aa84123f2f5c72ef37de3e736cfa220034deea122`.
- Source baseline: `origin/develop` `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`; isolated worktree started clean on `Rayer/LWC-373-implementation-r1`.
- Final implementation and test SHA before this report: `fc55ba8cd8563ff66a3b66ad71d0fda63f649013`.
- Commits: `3aa8ea74c0bbe6fd31a5ae2212c0c7ffacb71c60` (implementation), `a09a78c5d6d2e304e4f03fb0ae06dedc36ec6ff7` (local child-to-consumer test), `fc55ba8cd8563ff66a3b66ad71d0fda63f649013` (quota HTTP acceptance tests).
- Runtime readback: provider/model `codex` / `gpt-6-luna`; requested and effective launch effort were `null` in `worker-show` (no effort value was exposed). Orca runtime/session `8bc79eed-2312-4c07-a306-f91ae8d31716`; dispatch `ctx_1d476c9d7079`; terminal `term_38a9f6bc-26c3-4c02-94e0-e4c60eae9ac6`.
- Toolchain: Pkl 0.32.1; Go 1.26.5 (`darwin/arm64`); Python 3.14.6.
- PR publication/readback: pending; this report will be updated with the exact PR URL, remote head SHA, and base after creation.

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

Frontend npm lint/typecheck/test/build were not run because this change does not modify frontend files. Canonical CI and independent TPM/reviewer checks remain with the coordinator after PR publication.
