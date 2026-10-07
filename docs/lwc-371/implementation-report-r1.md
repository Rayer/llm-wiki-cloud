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
