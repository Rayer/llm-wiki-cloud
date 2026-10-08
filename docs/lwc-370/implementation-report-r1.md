# LWC-370 implementation report (r1)

Status: scoped implementation and offline integration are committed locally; a PR checkpoint can be published while final shared acceptance awaits the coordinator's LWC-374 fixture repair.

## Execution identity

- Model/effort: GPT-6-Luna, xhigh (observed terminal footer).
- Orca session: Run `run_ec3a3eca0058`; Task `task_c4cbb43891fe`; Dispatch `ctx_7d4c191aadf5`.
- Terminal/incarnation: `term_22cddd98-1061-4a48-b360-ab7a5c14a263` / `a8f2737e-877a-421e-a69c-badea9b7c78b`.
- Worktree/branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1`, `Rayer/LWC-370-implementation-r1`.
- Own frontend checkpoint commit: `7e69e0b4ef3f8215a4ec091bd8dba6e077e171a5`.
- Exact LWC-374 checkpoint integrated by normal merge: `c86101d8c999c5c05db9fdf8798073002bae79b0` (parent `f62bb530cb968e312b3faad77fe0f1b93b3e447b`). Current HEAD is `cb96ee89a35a5270ba33ce20b5dbedd0d1f929f6`, containing the committed LWC-370 implementation and that integration checkpoint. This report has pending documentation updates; source changes are committed.
- PR: none. Exact PR URL, remote head/base readback, and final SHA will be recorded after final integration and publication.

## Implemented checkpoint

- Added runtime config schema/URL validation, deferred no-credential/no-cache loading, 10-second timeout, lifecycle caching, retry/error states, and a workspace boundary that mounts AuthProvider/Shell only after config loads. API, upload, Auth, Google, CLI, public-config, and version callers now use the loaded runtime URLs.
- Added a development-only `/frontend-config.json` route that reads the current worktree file on every request, sanitizes the public fields, sends no-store, and returns 404 outside development. Legal pages remain independent.
- Replaced the static build receipt’s baked API/Auth endpoints with `{schema_version: 1, config_url}`. `deploy/components/frontend.sh` now builds with `NEXT_PUBLIC_CONFIG_URL` and validates the exact reader-schema/bootstrap URL receipt while retaining its deployment identity, archive, alias snapshot, rollback, and reuse checks.
- Added the manual `generate-frontend-config.yml` artifact workflow and isolated `frontend-config-only` routing for the existing DEV/Production entries. The publisher identifies one artifact ID and successful generator run, checks environment/source/hash/public JSON before cloud authentication, then targets only the fixed environment object and records object generation plus byte-equal readback evidence.
- Added the receipt parser and exact artifact handoff scripts to the existing frontend CI test job; no separate status check or deployment gate was added.
- Added synthetic metadata, manifest, wrong-environment, source-A-after-source-B, route, built-bundle, and deployment adapter controls. Updated direct workflow contract assertions to preserve existing release/recovery behavior while covering the new branch.
- Added a secretless Frontend-only `deploy_config` output path and `CONFIG_TARGET=frontend` Make target. DEV/Prod endpoints remain sourced from the existing environment YAML; local endpoints use the selected numeric BFF/Auth ports. Config files are written atomically to `.build/cac/<env>/frontend-config.json` with exactly `schema_version`, `api_url`, and `auth_url`; generation does not call Pkl or resolve Pipeline/BFF secrets.
- Normalized deployment metadata carries `config_schema_version`, the fixed environment `config_url`, and provider identity, while API/Auth runtime endpoint values stay outside the Frontend build/reuse identity. The Vercel receipt and engine admission/reuse path now compare the reader version and bootstrap URL.
- The local BFF `local-config` path generates the local JSON before writing only `NEXT_PUBLIC_CONFIG_URL=/frontend-config.json` to `.env.local`; the local route reads the generated file. Its test uses a synthetic gitdir and ports, preserving this worktree's local scope/key state.

## Local verification

`npm ci` in `apps/frontend`: exit 0; 540 packages added, 541 audited. npm reported 17 audit findings (3 moderate, 13 high, 1 critical); no lockfile change was made. The frontend test script change is intentional for synthetic reserved-host setup.

| Command | Exit | Result |
| --- | ---: | --- |
| `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/lwc-370-local-config-route.test.mjs` (from `apps/frontend`) | 0 | 2/2 route cases passed: per-request read/no-store, private-field rejection, missing-field propagation, malformed-JSON format handling, and development-only availability. |
| `node --test apps/frontend/tests/lwc-306-frontend-cd.test.mjs` (from repo root) | 0 | 56/56 deployment adapter cases passed, including new receipt acceptance, wrong-URL rejection, and old receipt rejection, with synthetic provider responses only. |
| `python3 scripts/test_frontend_build_config.py` | 0 | 4 receipt parser/transport tests passed. |
| `python3 scripts/test_frontend_config_artifacts.py` | 0 | 7 artifact/workflow tests passed, including exact artifact identity, source A bytes after checkout B, and cross-environment rejection before synthetic object mutation. |
| `go test ./cmd/deploy_config -count=1` (from `apps/bff`) | 0 | Package tests passed, including DEV/Prod endpoint generation, normalized build identity, and local loopback-only ports. |
| `python3 apps/bff/scripts/test_local_dev_makefile.py` | 0 | 20/20 local config and launcher tests passed; local config used a synthetic gitdir and ports. |
| `python3 scripts/test_engine_workflow.py` | 0 | 8/8 workflow routing and permission contract cases passed. |
| `python3 -m unittest discover -s deploy/engine/tests -p test_engine.py` | 0 | 51/51 retained engine tests passed with synthetic providers. |
| `python3 -m unittest discover -s deploy/engine/tests -p test_prepare_diagnostics.py` | 0 | 32/32 prepare and frontend diagnostic tests passed with synthetic providers. |
| `python3 scripts/test_cd_contract.py` | 0 | Current-head CD workflow contract suite passed 70/70 tests. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | 1 | 21 tests ran; the LWC-374-owned production Auth contract fixture currently has 56 failing subcases and 3 errors. Coordinator is repairing the fixture/scripts; no fixture changes were made here. |
| `python3 -m unittest discover -s deploy/engine/tests` | 1 | 124 tests ran; 7 failures and 2 errors are in LWC-374-owned `test_build_submission.py` admission/resume fixtures. Coordinator owns that repair; no fixture changes were made here. |
| `go test ./... -v -count=1 -race` (from `apps/bff`) | 0 | All Go packages passed with the race detector enabled. |
| `make config-local CONFIG_TARGET=frontend` | 0 | Generated `.build/cac/local/frontend-config.json` with the selected worktree default ports; the command did not invoke local scope/key setup. |
| `make config-dev CONFIG_TARGET=frontend` / `make config-prod CONFIG_TARGET=frontend` | 0 / 0 | Generated `.build/cac/dev/frontend-config.json` and `.build/cac/prod/frontend-config.json`; both have exactly the public schema and existing endpoints. |
| `make config-local CONFIG_TARGET=frontend CAC_OUTPUT_DIR=/tmp/lwc370-cac-validation BFF_PORT=19080 AUTH_PORT=19081` | 0 | Generated exactly the three public fields and used the selected loopback ports. |
| `make config-dev CONFIG_TARGET=frontend CAC_OUTPUT_DIR=/tmp/lwc370-cac-validation` / `make config-prod CONFIG_TARGET=frontend CAC_OUTPUT_DIR=/tmp/lwc370-cac-validation` | 0 / 0 | Both generated exactly the three public fields with the current DEV/Prod endpoints; no cloud or private binding access. |
| `node tests/lwc-318-built-config.mjs` (from `apps/frontend`) | 0 | One real Next webpack client build; two fresh app lifecycles read synthetic config A/B and exercised API/public/version/Google consumers. Receipt matching control exited 0 and wrong URL exited 1 as expected. |
| `npm test` | 0 | After the route correction: Node suite 527/527; Vitest 33/33 files and 307/307 tests passed, including runtime-config cases. |
| `npm run lint` | 0 | Re-run after the route correction; ESLint completed without findings. |
| `npm run typecheck` | 0 | Re-run after the route correction; TypeScript completed without errors. |
| `make workflow-yaml` | 0 | YAML checks passed for CI, CD, DEV, Production, and the generator workflow. |
| `NEXT_PUBLIC_CONFIG_URL=https://config.example.test/frontend-config.json npm run build` (from `apps/frontend`) | 0 | Re-run after the route correction; Next production build emitted `/frontend-config.json` as a dynamic route. The static receipt body was checked for exactly `schema_version` and the synthetic `config_url`. |
| `bash -n deploy/components/frontend.sh apps/frontend/tests/fixtures/lwc-306-fake-curl apps/frontend/tests/fixtures/lwc-306-fake-vercel` | 0 | Shell syntax passed. |
| `node --check apps/frontend/tests/lwc-318-built-config.mjs` | 0 | Node syntax passed. |
| `git diff --check` | 0 | No whitespace errors. |

Initial frontend-slice runs exposed stale workflow assertions: `npm test` exited 1 with 521/526 Node tests passing and five workflow-contract failures; the first `python3 scripts/test_cd_contract.py` exited 1 with 3/70 workflow-assertion failures. The assertions were updated narrowly, then the complete frontend reruns passed with no skips. Current-head `test_cd_contract.py` is 70/70. The Auth config and engine broad suites still fail in LWC-374-owned fixtures (21 tests with 56 failing subcases/3 errors, and 124 tests with 7 failures/2 errors respectively); coordinator repair is pending, and these results are not reported as passes.

All public JSON, hosts, and artifact metadata used for local tests were synthetic. No deployment workflow was dispatched, and no Vercel, GCS, GSM, IAM, credential, or paid Pipeline action was run.

## Frozen acceptance checkpoint

| Area | State | Evidence / remaining work |
| --- | --- | --- |
| Runtime reader, boundary, no fallback, timeout/retry/cache, and all frontend consumers | PASS for frontend slice | Runtime config tests, full frontend suite, and built client config A/B causal check. |
| Local development route and legal-page independence | PASS for offline integration | Named route cases 2/2 and full component suite. The expected public `.build/cac/local/frontend-config.json` was generated; no `.env.local`, local scope, or key file was read or changed. |
| Frontend config artifact metadata, exact-ID selection, success/source/environment/hash checks, no rebuild publication branch, and Make producer | PASS for offline contract | Artifact tests 7/7; local/dev/prod Make targets emitted the expected public fields. Source A after source B and wrong-environment rejection passed with synthetic artifacts. |
| Static receipt schema and deployment adapter | PASS for offline integration | Receipt contains reader version/config URL; 56/56 adapter cases and 51/51 engine cases pass, including admission, snapshot/restore, and reuse against the new normalized identity. |
| GCS generation/publication, live object generation, CORS/public GET, and cloud readback | NOT RUN | The workflow code is present, but no DEV/Prod provider action or cloud resource was touched. Bucket/IAM/CORS readiness remains unverified. |
| LWC-369/LWC-371 compatibility | PASS for scoped offline checks; broad suite pending | Retained engine/frontend and current-head CD contract checks pass; no LWC-371 charging/refund paths were changed. Auth config and full engine suites remain blocked by LWC-374-owned fixtures. |
| LWC-374 producer/normalizer/engine integration | PASS for scoped offline integration | Exact committed `c86101d8c999c5c05db9fdf8798073002bae79b0` is merged. Frontend generation, local ports, normalized target identity, and engine reuse are covered. LWC-374 owns the still-pending Auth/BFF admission fixture repair and will provide its exact commit for normal integration before final QA. |
| DEV/Production deployment, live GSM, IAM/resources/credentials, paid Pipeline, UAT, and cloud verification | NOT RUN | Outside this worker’s authorized execution scope. Offline/local results do not claim cloud verification or Verified status. |

The LWC-370 source implementation and exact LWC-374 integration checkpoint are committed locally at HEAD `cb96ee89a35a5270ba33ce20b5dbedd0d1f929f6`; only this report has pending updates. The standalone PR checkpoint is ready; final integrated PR head, TPM review, independent review, canonical CI, merge-to-develop, and deployment have not occurred. The coordinator will provide the exact LWC-374 fixture repair commit for normal integration and final verification.
