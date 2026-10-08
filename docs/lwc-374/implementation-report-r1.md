# LWC-374 implementation report r1

## Execution identity

- Frozen specification: `lwc374-spec-r1`, including the accepted Demo email/role compatibility addendum.
- Model / effort / mode: GPT-6-Luna / xhigh / YOLO.
- Codex session: `01a11a18-df02-7c80-b905-395dd42ec6f1`.
- Orca runtime / terminal: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `term_c6b8616c-2a70-4667-9bb5-8b80a3bf5783`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-374-implementation-r1` / `Rayer/LWC-374-implementation-r1`.
- Base: `f62bb530cb968e312b3faad77fe0f1b93b3e447b`.
- Tested code and fixture commit: `a5763a361b9e453b2cf0df838340a69154c99f8e` (parent `c86101d8c999c5c05db9fdf8798073002bae79b0`).
- PR: [#107](https://github.com/Rayer/llm-wiki-cloud/pull/107), open against `develop`. Exact readback at publication: head branch `Rayer/LWC-374-implementation-r1`, head SHA `c7460b7cfc77e821cff7e083a5daf0e72f037e36`, base SHA `f62bb530cb968e312b3faad77fe0f1b93b3e447b`.

## Implemented

Auth now loads its strict v1 JSON file before Firestore or the listener, with `LWC_APP_CONFIG_PATH` as the file locator and only `PORT`, `K_SERVICE`, and `K_REVISION` retained as platform inputs. Pkl and the Auth prepare path project local/dev/prod inputs, keep credential payloads out of descriptors, materialize only pinned numeric references into private temporary files, and publish through the existing Auth deployment engine with a native read-only Secret Manager mount and retained known/unknown publication checkpoints.

The local supervisor reuses each worktree's existing scope and JWT key, passes Auth storage scope explicitly, keeps BFF's legacy loader working, and strips migrated Auth settings from stale environment authority. Production Auth contract coverage now validates the native numeric file mount and strict readback; offline Action fixtures synthesize numeric metadata locally without a Secret Manager credential or payload request. Legacy shell rollback coverage remains in `scripts/test_auth_config_contract.py`.

## Frozen acceptance matrix

| AC | Result | Evidence and limits |
| --- | --- | --- |
| AC1: Auth Pkl projection, selected Stage 1 snapshot, pinned numeric inputs, final file materialization, strict loader; Auth-only avoids Pipeline/LLM resolution | PASS locally | Real Pkl Auth prepare plus Go tests below; synthetic metadata/payload fixtures only. Production file output is never committed. |
| AC2: local prepare → real Auth startup → consumer host/origin/config identity; stale same-name app env ignored; platform exceptions retained; malformed/missing config fails before listener; BFF loader remains compatible | PASS locally | Actual Auth binary started against a loopback Firestore emulator on port 8081; `/api/v1/public/version` and health readbacks matched the manifest, host/origin and platform identity checks passed, and missing/malformed files exited before listen. A separate BFF file-loader smoke passed. |
| AC3: per-worktree key/scope reuse and isolation, explicit Firestore scope, Auth/BFF JWT interoperability, forbidden legacy switches | PASS locally | Existing worktree key/scope were reused without printing key material; scoped storage, inherited-scope rejection, worker process-boundary, and local key contract tests passed. |
| AC4: Auth login/register/refresh/logout/session migration/CLI/OAuth/cookie/account regressions and real signing/verification | PASS locally with emulator | The full existing `internal/auth` and `cmd/auth` packages passed against a loopback Firestore emulator: 208 + 22 named test events, zero skips. This uses only synthetic local identities and OAuth fixtures. |
| AC5: offline prepare/normalizer/adapter/engine behavior including image reuse, numeric mount, env preservation/removal, publication checkpoints, no-traffic/Ready/cutover, and legacy rollback | PASS locally | Full engine test discovery: 124 tests passed. Auth config and retained legacy rollback suites passed; no provider adapter was replaced with an example stub. |
| AC6: authorized DEV deployment and provider readback | NOT RUN | No cloud deployment, live GSM payload read/write, IAM/resource action, credentials change, or paid Pipeline action was performed. This report makes no `Verified` claim. |

## Verification log

All commands ran in this worktree. Commands that target `apps/bff` ran from that directory unless stated.

| Command | Exit / result |
| --- | --- |
| `make config-local CAC_TARGET=auth BFF_PORT=8080 AUTH_PORT=8081 FRONTEND_PORT=3000` (repository root) | 0; generated the local Auth file and non-secret manifest with restrictive permissions. |
| Manual local Auth startup harness (compiled Auth binary, loopback Firestore emulator, port 8081) | 0; file identity, platform fields, stale-env rejection, host/origin behavior, health/version, and missing/malformed pre-listener failures checked. |
| `go test ./internal/config ./cmd/pipeline_config ./cmd/deploy_config ./cmd/auth -count=1` | 0. |
| `go test ./internal/config ./internal/firestore ./internal/localcloud ./internal/localpipeline -run '^(TestDecodeAuthFileStrictSchemaAndGoogleModes|TestLoadAuthFileUsesFileAuthorityAndPORTException|TestDecodeAuthFileLocalScopeAndKeyContract|TestNewClientWithDatabaseAndScopeIgnoresInheritedScope|TestCollectionUsesOneRootPerLocalScope|TestScopeValidatesAndBuildsSharedRoots|TestWorkerArgsStayStructuredAtNativeProcessBoundary|TestNativeWorkerCommandCrossesRealProcessBoundaryWithScopedEnvironment)$' -count=1 -v` | 0; all 8 selected test functions passed. |
| `gcloud beta emulators firestore start --host-port=127.0.0.1:8587 --project=auth-contract-test --quiet` with local Java 26 `JAVA_HOME` | Emulator started at `127.0.0.1:8587`; stopped after the local test run. |
| `FIRESTORE_EMULATOR_HOST=127.0.0.1:8587 go test -json ./internal/auth ./cmd/auth -count=1` | 0; 208 `internal/auth` + 22 `cmd/auth` pass events, no skips. |
| `make -C apps/bff test` | 0; CD contract 70 tests, Auth/config discovery 15 tests, local Makefile 20 tests, and `go test ./... -v -count=1 -race` passed. The race suite's Firestore-emulator-gated cases skipped in this invocation; the Auth packages were rerun separately with the emulator above. |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` | 0; 124 tests passed. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | 0; 15 tests passed after the synthetic fixture refactor. |
| `go vet ./...` | 0. |
| `make test-flash-execution` | 0; pinned Synto wire scenarios passed using the local isolated test environment. |
| `go build ./...` | 0. |
| `npm ci` (from `apps/frontend`) | 0; dependencies installed for existing CI checks. |
| `npm run lint` (from `apps/frontend`) | 0. |
| `npm run typecheck` (from `apps/frontend`) | 0. |
| `npm test` (from `apps/frontend`) | 0; 524 Node tests and 295 component tests passed across 32 files. |
| `npm run build` (from `apps/frontend`) | 0; optimized Next.js production build completed. |
| `git diff --check` | 0. |

## Diagnosed test setup failures

The first production Auth contract run exercised the superseded `auth.sh` environment-variable deployment contract and failed 56 assertions with 3 errors across 21 test methods. It was replaced by production native-file adapter/readback checks; the existing legacy shell safety/rollback tests remain and pass in the Auth config suite.

The first full engine discovery run reported 7 failures and 2 errors in 124 tests. Its Action fixture tried to initialize a real Secret Manager metadata client without credentials, and one BFF test expected the previous `dirty-bff-ssot` reason after the stricter early `dirty-runtime-ssot` rejection. The test-only fixture now builds numeric Auth references from the real non-secret Pkl projection, and the test preserves the fail-closed drift assertion; the final full discovery run passes 124 tests. No GSM payload was read or written.

## Remaining work

The PR is published and its exact URL/head/base were read back above. This report-only commit advances the PR head; the post-update remote head is sent to the coordinator with the completion checkpoint for same-SHA TPM/reviewer reviews and canonical CI. Cloud deployment/readback remains `NOT RUN` under the frozen scope.

## Bounded compatibility repair r2

### Execution identity and publication checkpoint

- Model / effort / mode: GPT-6-Luna / xhigh / YOLO, retained Codex session `01a11a18-df02-7c80-b905-395dd42ec6f1`.
- Orca runtime / terminal / task / dispatch: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `term_c6b8616c-2a70-4667-9bb5-8b80a3bf5783` / `task_6e059cf3a36d` / `ctx_7f8552aeac67`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-374-implementation-r1` / `Rayer/LWC-374-implementation-r1`.
- Base: `f62bb530cb968e312b3faad77fe0f1b93b3e447b`. The prior reviewed source SHA was `12847cd616e73d7ddcaccbdc612245c0c34d7b13`; the bounded repair source commit is `174e8612333c9e43f2bb615ecb008b0f110d963a`.
- PR: [#107](https://github.com/Rayer/llm-wiki-cloud/pull/107), targeting `develop`. The PR body was read in full before preparing the r2 update; the source commit above is the code-under-test checkpoint. The post-report publication head is sent to the coordinator after push.
- The independent review HOLD at `12847cd616e73d7ddcaccbdc612245c0c34d7b13` and canonical CI run `37743275262` apply to the earlier SHA only. The new source SHA requires both reviews and canonical CI. No merge or deployment was performed.

### Repair and r2 acceptance

Cloud Run file-binding validation now matches the installed Google Cloud SDK producer: an unaliased same-project short secret name, exact numeric item version/path, and contained file permissions are accepted without relying on the unused `readOnly` mount flag or an alias annotation. Full resource names are accepted only for the selected project ID or its trusted project number, and only the configured secret and numeric version match. The BFF and production contract fixtures use that SDK shape and retain wrong-project, wrong-secret, wrong-version, wrong-path, and wrong-permission controls.

Auth publication obtains the trusted selected-project ID/number mapping before materialization and records both identities in the candidate checkpoint. `addVersion` responses must identify the configured secret and exact selected project by ID or trusted number with a numeric version; malformed or mismatched responses remain unknown and are not replayed. Observe, reconcile, resume, candidate/readback, and image-based rollback exercise the same identity record. The test runner replaces provider process calls with synthetic local responses, including project-number lookup.

`GOOGLE_CLOUD_PROJECT` and `GOOGLE_APPLICATION_CREDENTIALS` remain accepted platform SDK/ADC environment values and are preserved through managed comparison and readback. Removed Google OAuth application settings and legacy bindings still fail the managed comparison. The BFF local auth-file loader rejects `DEV_JWT=true` and nonempty `LOCAL_DATA_DIR` before startup while preserving the current boolean parsing behavior for `DEV_JWT=false`; file authority and the `PORT` exception remain covered.

The installed local gcloud SDK producer was serialized offline with a synthetic `ReachableSecret` at version `17`. Its volume contained `secretName=lwc-auth-app-config-prod` and one item `{key: 17, path: auth.json}`; its mount contained only `mountPath` and `name`. This confirms the producer omits `readOnly`, item mode, and alias annotation. This was local SDK serialization, not a provider request.

| r2 acceptance area | Result | Evidence and limit |
| --- | --- | --- |
| Native Cloud Run file binding and BFF/source callers | PASS locally | Full engine discovery: 127 tests; synthetic SDK-shaped positive and negative controls exercise service matching, candidate/readback, observe, reconcile/resume, and rollback. Auth and production contract suite: 15 tests. |
| Trusted project identity and publication no-replay behavior | PASS locally | Synthetic trusted ID/number mapping, both valid response forms, wrong project/secret/version, malformed response, checkpoint persistence, and single-publication assertions passed. No live mapping lookup was used by these final tests. |
| SDK/ADC environment preservation and legacy OAuth rejection | PASS locally | Managed expected comparison and readback tests passed with both platform values retained and removed OAuth bindings rejected. |
| Local BFF legacy-switch rejection | PASS locally | Two top-level loader tests and three named legacy-switch subtests cover `DEV_JWT=true`, `LOCAL_DATA_DIR`, `DEV_JWT=false`, and `PORT` compatibility. |
| Frozen Demo email/role/EnsureDemoAccount and other scopes | PASS by unchanged source and retained suite | No changes were made to those contracts, frontend hunks, platform ports, worktree key, or 370-owned files. |
| Cloud deployment/provider readback (AC6) | NOT RUN | No deployment, IAM/resource mutation, credential change, or paid Pipeline action was performed. This is not a `Verified` claim. |

### Verification log

Commands below were rerun after source commit `174e8612333c9e43f2bb615ecb008b0f110d963a`; commands are shown with their working directory where it matters.

| Command | Exit / result |
| --- | --- |
| `python3 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` (repository root) | 0; 127 tests passed. The test fixture uses a fake gcloud executable or patched provider runner; no live gcloud or SDK transport was used in this run. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (`apps/bff`) | 0; 15 tests passed. |
| `make -C apps/bff test` | 0; CD contract 70 tests, Auth/config discovery 15 tests, local Makefile 20 tests, and Go race suite passed. Go JSON readback: 47 package passes, 3,039 pass events, 116 skipped events, zero failures. Skip events by package: `cmd/auth` 4, `internal/auth` 72, `cmd/demo_password_rotate` 2, `cmd/bff` 3, `cmd/query_experiment` 1, `internal/exportjob` 4, `internal/handler/v1` 23, `internal/queryconfig` 1, `internal/syssettings` 1, and `cmd/olw_worker` 5. Most require a local Firestore emulator; the remaining gates include local Firestore/Storage emulators, opt-in browser/frozen-local tests, and the exact Synto bridge output paths. |
| `go test -json ./... -count=1 -race` (`apps/bff`) | 0; 47 package passes, 3,039 pass events, 116 skipped events, zero failures; this JSON run was used to preserve exact skip counts. |
| `go test ./internal/config -run '^(TestLoadAuthFileRejectsLocalLegacySwitchesBeforeStartup|TestLoadAuthFileUsesFileAuthorityAndPORTException)$' -count=1 -v` (`apps/bff`) | 0; both test functions and all three named legacy-switch subtests passed. |
| `go vet ./...` (`apps/bff`) | 0. |
| `make test-flash-execution` (`apps/bff`) | 0; pinned Synto scenarios passed using its local test harness. |
| `go build ./...` (`apps/bff`) | 0. |
| `npm run lint` (`apps/frontend`) | 0. |
| `npm run typecheck` (`apps/frontend`) | 0. |
| `npm test` (`apps/frontend`) | 0; 524 Node tests and 295 component tests passed across 32 files, zero failures or skips. |
| `npm run build` (`apps/frontend`) | 0; optimized Next.js production build completed. |
| `python3 -m py_compile deploy/components/auth_config.py deploy/engine/providers.py deploy/engine/tests/fake_provider.py deploy/engine/tests/test_engine.py scripts/test_bff_auth_config_contract.py scripts/test_production_auth_config_contract.py` (repository root) | 0. |
| `git diff --check` | 0. |
| `java -version` | 1; this environment reports that no Java runtime is installed, so the r2 Firestore emulator rerun could not start. |

The Go suite's 76 `cmd/auth` and `internal/auth` emulator-gated skip events remain visible above. The retained r1 emulator run recorded 208 `internal/auth` and 22 `cmd/auth` pass events with zero skips, but I could not repeat that emulator run in r2 because this environment has no Java runtime and no emulator was running; no Java installation or persistent test stack was added.

### Diagnosed failures and external-access disclosure

- An initial Python unittest command ran from the wrong working directory and produced 6 import errors; the corrected engine discovery command passed 127 tests.
- Before the SDK-shape fixtures and clean commit checkpoint, full engine discovery had 2 old `readOnly`/alias-annotation expectation failures and 9 `dirty-build-input` errors. After fixture correction and commit, all 127 tests passed.
- The first auth contract rerun had 3 assertions that still required `readOnly=true` or an alias annotation. The fixture now follows the SDK producer; the final contract run passed all 15 tests.
- One actual local Go `materialize-auth` invocation used synthetic input and exited 1 with the sanitized message “selected secret version could not be accessed.” It can reach Secret Manager, and no transport trace was captured; whether a remote payload read was attempted or completed is unknown. No output payload was logged and the command was not retried. Do not infer from the sanitized error that the operation stayed local.
- Local gcloud logs show four earlier `gcloud projects describe` GETs to Cloud Resource Manager at 15:54:10, 15:54:13, 15:54:37, and 15:54:39 local time; each returned HTTP 200. They were caused by the initial contract path resolving project number for ID-only fixtures. Those were read-only project identity lookups; no GSM payload access or mutation appears in those gcloud logs. The code now resolves trusted mapping only when required, and the final tests inject synthetic mapping responses. A later notification command also had an unescaped-backtick shell expansion and invoked `gcloud projects describe` without an argument; local gcloud rejected it before any HTTP request. These incidents were reported to the coordinator; no further provider calls were made.

### Remaining work

Push the reviewed report/PR-body update to PR #107 and provide the exact remote head/base readback to the coordinator for same-SHA TPM and independent reviewer review plus canonical CI. The task does not claim a merged or deployed release; cloud deployment and provider readback remain `NOT RUN`.
