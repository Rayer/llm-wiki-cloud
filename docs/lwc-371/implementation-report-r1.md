# LWC-371 implementation report r1

## Contract and implementation

Implemented frozen contract `lwc371-failure-refund-spec-r1` from the immutable LWC-371 snapshot and `implementation-wave-369-374.md`. Baseline source SHA: `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`.

Each enforced user full-pipeline debit now has a durable Firestore reservation tied to its execution. A confirmed `FAILED` result refunds once and restores only the reservation's same-day debit and cooldown when they still belong to that run. `SUCCEEDED` stays charged; `CANCELLED` retains the existing charged behavior. `UNKNOWN` and temporary lookup or storage failures stay pending. Cloud Run reservations are reconciled in the BFF background loop, independently of status requests; native executions persist result and settlement transactionally and reconcile interrupted work conservatively. An execution-specific publication receipt remains authoritative over a later worker cleanup failure.

The status API returns quota settlement and safe failure details; the UI shows the failure, settlement, latest quota/cooldown, and truthful unavailable state. Status refresh continues while settlement is pending or temporarily unknown. Admin and unenforced paths do not create reservations. Test fixtures use loopback Firestore/GCS, a local fake Cloud Run endpoint, and a synthetic local worker; no paid provider was called.

## Runtime and Git

- Model: `gpt-6-luna`; reasoning effort: `xhigh`; execution mode: `YOLO`.
- Codex version: `0.160.0`; session: `01a11845-5e60-73b3-bf4d-c1f271c95d0c`.
- Branch: `Rayer/LWC-371-implementation-r1`.
- Baseline SHA: `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`.
- Implementation commit and tested backend source SHA: `2549853e92759bc779162bbb4974d6e11eb3a593` (`feat: refund failed pipeline reservations (LWC-371)`).
- PR #104: [LWC-371: refund confirmed failed pipeline reservations](https://github.com/Rayer/llm-wiki-cloud/pull/104), state `OPEN`.
- Exact create-time remote readback: head branch `Rayer/LWC-371-implementation-r1`, head SHA `a9cac0d33474fad614bb96d047b771c64ed24fdb`; base branch `develop`, base SHA `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`. `git ls-remote` matched the head SHA.
- This report-only follow-up commit advances the PR head after that create-time readback; the final remote head was re-read and is included in the dispatch completion record.

## Commands and results

Provider-key environment variables were unset for the full Go suite and local acceptance commands. The full Go suite also ran without emulator environment variables; emulator-only cases were run separately below against loopback emulators.

| Command | Exit | Result |
| --- | ---: | --- |
| `go test ./... -v -count=1 -race` from `apps/bff`, with provider keys and emulator variables unset | 0 | 1,530 tests passed, 87 conditional tests skipped, 0 failed, 45 packages with test results. Emulator acceptance cases were separately run with emulators. |
| `make -C apps/bff vet` | 0 | `go vet ./...` passed. |
| `make -C apps/bff build` | 0 | `go build ./...` passed. |
| `python3 scripts/test_cd_contract.py` with provider keys unset | 0 | 69 tests passed. |
| `python3 -m unittest discover -s scripts -p 'test_*auth_config_contract.py'` with provider keys unset | 0 | 32 tests passed. |
| `npm test` from `apps/frontend` | 0 | Node: 524 passed, 0 skipped; component: 295 passed across 32 files. |
| `npm run lint` from `apps/frontend` | 0 | ESLint passed. |
| `npm run typecheck` from `apps/frontend` | 0 | TypeScript passed. |
| `npm run build` from `apps/frontend` | 0 | Next.js production build and TypeScript build checks passed; 15 app pages generated. |
| `npm run test:component -- tests/status-client.test.tsx` | 0 | 48 component tests passed. |
| `go test ./internal/handler/v1 -run '^TestPipelineQuotaCloudRunHandlerEmulator$' -count=1 -v` from `apps/bff`, with Firestore at `127.0.0.1:8085` and GCS at `http://127.0.0.1:14443` | 0 | Named Cloud Run handler emulator acceptance passed (1 top-level test; fake Cloud Run HTTP endpoint, real loopback Firestore/GCS emulators). |
| `go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1 -v` from `apps/bff`, with the same loopback emulators | 0 | Named native/local pipeline acceptance passed (1 top-level test; local synthetic worker, Firestore execution and settlement assertions, same-input retry, timeout/config/spawn failures, scope sentinels). |
| `git diff --check` and `git diff --cached --check` | 0 | No whitespace errors. |

The first full frontend run exposed one stale API normalization expectation for the new nullable diagnostic message; the assertion was updated. A later run exposed the test expecting the pending label while the response was correctly `unknown`; the test was updated to assert the truthful unavailable label. The final full frontend run above passed.

## Acceptance matrix

| AC | Result | Evidence |
| --- | --- | --- |
| AC1: user full-pipeline success stays charged in local/native and Cloud Run handler paths | PASS | Both named emulator acceptance tests; Cloud Run is exercised through a local fake endpoint while Firestore/GCS are real loopback emulators. |
| AC2: confirmed config/worker failures refund the debit and cooldown; same input can retry | PASS | Native acceptance covers missing local config, synthetic timeout, spawn failure, failure status/reason, refund, and retry. Cloud Run handler fixture covers terminal failure settlement. |
| AC3: invoke/spawn failure, no-reservation paths, retries, later same-day runs and cross-day quota | PASS | Emulator acceptance covers invoke refund, admin/unenforced no reservation, idempotent settlement, same-day preservation, and cross-day preservation; native acceptance covers spawn failure. |
| AC4: unknown/outage remains pending, later confirmed failure refunds, committed publication wins cleanup failure | PASS | Cloud Run handler emulator acceptance covers unknown/outage recovery, missing execution-link recovery, owner isolation, and publication-receipt precedence. |
| AC5: API/UI shows failure, available reason, settlement, and updated quota/cooldown with safe fallback | PASS | Backend status assertions, full frontend suite, and focused 48-test `StatusClient` suite; pending and unknown settlements keep refreshing until the final settlement is visible. |

## Remaining limits

Cloud deployment acceptance is **NOT RUN**. No DEV/Prod provider action, actual Cloud Run job, live GSM payload, IAM/resource change, paid pipeline call, merge, or deployment was performed. The local and synthetic evidence supports implementation review only; it is not a claim of deployed-cloud verification.

## PR104 repair r2 — frozen contract evidence

This addendum covers the same frozen `lwc371-failure-refund-spec-r1` scope; it supersedes the earlier report's implementation/test SHA and acceptance evidence where the two differ. Runtime: `gpt-6-luna`, reasoning effort `xhigh`, `YOLO`, Codex session `01a11845-5e60-73b3-bf4d-c1f271c95d0c`; repair dispatch stayed in the same worktree and session. The repair source commit is `7a07194d81f17a05380015d5b69429c95d0aa413` on `Rayer/LWC-371-implementation-r1`, based on reviewed PR head `a59330b9deba640721ed222e6a68edb6a70e67d0`.

### Repair coverage

| Reviewed gap | Repair and observed evidence |
| --- | --- |
| Deployed publication evidence had no producer; transient GCS reads could refund a committed execution | The deployed worker now includes its execution ID in the committed manifest and writes the create-only execution receipt with `LOCAL_CLOUD_SCOPE` empty. The Cloud Run emulator acceptance triggered through the real BFF handler, built and ran the deployed-mode worker against a disposable GCS emulator bucket, read back its manifest/receipt, then returned 503 from a loopback GCS proxy: the real reconciler kept the reservation pending and preserved the debit; after recovery it charged the reservation and the status API reported `SUCCEEDED`. |
| Native observed child results could be lost at finish transaction failure/restart | The manager retains a child result for retry, records observed outcome with execution settlement, and runs reconciliation in native mode. The named native acceptance runs a real synthetic child, blocks Firestore at terminal exit, confirms the execution-scoped diagnostic exists without making a status request, closes and restarts the manager, reconciles, verifies one refund and lock release, then retries the same input. |
| A confirmed invoke failure with no execution could lose its first failed refund | A bounded, project-scoped synthetic failure marker preserves only a definitive pre-execution failure; transport-unknown remains pending. The handler/reconciler test injects first-settlement failure, reads the marker, restores settlement writes, and confirms one refund. The Firestore emulator acceptance also covers a definite invoke failure and exercises same-day, repeated-settlement, later-run, and cross-day preservation. |
| Native status ignored the worker's existing failure diagnostic | Native status now reads the execution-scoped bounded diagnostic and returns known stage, class, exit code, and safe message when present, with an explicit unavailable fallback. The actual status API acceptance asserts those fields for both the initial worker failure and the recovered execution. |

No user status poll is used to recover the controlled native Firestore-outage execution; the test queries durable execution/quota records directly and invokes the manager startup reconciler. Native and Cloud Run outcomes continue to settle through their existing backend paths; no queue, platform, provider health check, or credential read was added.

### Commands and results

Commands ran in the existing worktree. Provider-key environment variables were unset by the root/BFF Make targets; emulator endpoints below pointed only to `127.0.0.1` loopback fixtures.

| Command | Exit | Result |
| --- | ---: | --- |
| `env -u FIRESTORE_EMULATOR_HOST -u STORAGE_EMULATOR_HOST -u GOOGLE_CLOUD_PROJECT make test` | 0 | 1,530 Go pass events, 87 conditional skips, 0 failures; 69 CD contract tests and 32 auth-config contract tests passed; frontend Node 524/524 and component 295/295 passed. The emulator-backed acceptance cases were run separately below. |
| `env FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 STORAGE_EMULATOR_HOST=http://127.0.0.1:14443 GOOGLE_CLOUD_PROJECT=lwc371-local go test -json ./cmd/olw_worker ./internal/handler/v1 ./cmd/bff ./internal/localpipeline -count=1 -race` from `apps/bff` | 0 | Four packages passed: 1,371 Go pass events, 4 conditional skips, 0 failures. The skips were `TestLocalCitationBrowserServer`, `TestFrozenSuggestedCorpusAcceptsValidMockProvider`, `TestExactSyntoPackExportBridge`, and `TestExactSyntoMigratedConfigBridge`; both named acceptance tests below ran and passed, without skipping. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585 STORAGE_EMULATOR_HOST=http://127.0.0.1:14443 GOOGLE_CLOUD_PROJECT=lwc371-local go test -json ./internal/handler/v1 -run '^TestPipelineQuotaCloudRunHandlerEmulator$' -count=1 -race` from `apps/bff` | 0 | Named Cloud Run acceptance passed (1 top-level test, 7.67 seconds), including actual worker producer, backend reconciler, temporary GCS 503, pending preservation, and recovered charge. |
| `env -u FIRESTORE_EMULATOR_HOST go test ./cmd/demo_password_rotate -run '^TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry$' -count=1 -race` from `apps/bff` | 0 | The exact unrelated test that rejected an inherited emulator host passed (1 top-level test). |
| `make lint typecheck vet` | 0 | Frontend ESLint and TypeScript checks; `go vet ./...` passed. |
| `make build` | 0 | `go build ./...` passed; Next.js production build compiled and generated all 15 app pages. |
| `git diff --check` and staged diff check | 0 | No whitespace errors. |

The named native acceptance, `TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure`, passed in the four-package emulator-backed race run (1 top-level test, 35.10 seconds). Its controlled Firestore outage/restart path restored the observed terminal result, refunded only that reservation, released the lock, and allowed a same-input retry without status polling.

The first root `make test` attempt inherited `FIRESTORE_EMULATOR_HOST=127.0.0.1:8585` and exited 2 because `cmd/demo_password_rotate/TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry/transaction error after possible commit` rejected that environment before its injected store ran. The isolated test passed with that variable unset, and the complete root suite then passed with emulator variables unset; the changed packages were subsequently run in full with emulator variables enabled as listed above. This was a test-environment issue; no unrelated test or source was changed.

### Acceptance matrix r2

| AC | Result | Evidence |
| --- | --- | --- |
| AC1: native and Cloud Run user-full accepted runs charge once; success stays charged | PASS | Actual native child acceptance plus the real deployed-mode worker→GCS emulator→Cloud Run handler/reconciler fixture. |
| AC2: confirmed child/config failures refund, expose available safe reason, and permit same-input retry | PASS | Native actual subprocess/status API diagnostic assertions, one-refund quota assertions, and same-input retry after reconciliation. |
| AC3: spawn/invoke failures, repeated settlement, ownership, later same-day and cross-day runs | PASS | Native spawn failure; Cloud Run definite invoke failure; failed first refund marker/reconciliation test; actual Firestore emulator owner isolation, duplicate terminal settlement, later-run/cross-day preservation. |
| AC4: unknown/outage stays pending; confirmed failure refunds; committed publication remains charged | PASS | Native Firestore outage, restart, and startup recovery without status polling; Cloud Run actual publication receipt with injected GCS 503 stays pending and charges after recovery; committed publication wins Cloud Run cleanup failure. |
| AC5: API/UI truthful failure diagnostic, settlement, quota/cooldown, and fallback | PASS | Native status API returns the worker's bounded stage/class/message; Cloud Run API reports recovered committed state; full frontend suite passes. Existing bounded readers enforce scope and size; missing/invalid diagnostics keep the unavailable fallback. |

### PR readback and remaining limits

PR #104 remains OPEN and targets `develop`; URL: https://github.com/Rayer/llm-wiki-cloud/pull/104. After pushing source commit `7a07194d81f17a05380015d5b69429c95d0aa413`, `git ls-remote origin refs/heads/Rayer/LWC-371-implementation-r1` returned that exact SHA, and `gh pr view 104` returned head SHA `7a07194d81f17a05380015d5b69429c95d0aa413`, head branch `Rayer/LWC-371-implementation-r1`, base branch `develop`, state `OPEN`. The report-only follow-up commit and final PR head are read back in the dispatch completion record.

Cloud deployment acceptance is **NOT RUN**. No DEV/Prod provider action, actual Cloud Run job, live GSM payload read/write, IAM/resource change, paid pipeline call, merge, or deployment was performed. The evidence above is offline/local acceptance only.
