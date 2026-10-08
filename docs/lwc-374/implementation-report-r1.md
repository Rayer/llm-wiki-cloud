# LWC-374 implementation report r1

## Execution identity

- Frozen specification: `lwc374-spec-r1`, including the accepted Demo email/role compatibility addendum.
- Model / effort / mode: GPT-6-Luna / xhigh / YOLO.
- Codex session: `01a11a18-df02-7c80-b905-395dd42ec6f1`.
- Orca runtime / terminal: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `term_c6b8616c-2a70-4667-9bb5-8b80a3bf5783`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-374-implementation-r1` / `Rayer/LWC-374-implementation-r1`.
- Base: `f62bb530cb968e312b3faad77fe0f1b93b3e447b`.
- Tested code and fixture commit: `a5763a361b9e453b2cf0df838340a69154c99f8e` (parent `c86101d8c999c5c05db9fdf8798073002bae79b0`).
- PR: pending creation to `develop`; final remote head/base will be read back after publication.

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

Create the authorized PR to `develop`, read back its exact URL/head/base, and hand the same final PR head to the coordinator for the independent same-SHA TPM/reviewer reviews and canonical CI. Cloud deployment/readback remains `NOT RUN` under the frozen scope.
