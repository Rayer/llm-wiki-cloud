# LWC-370 implementation report (r1)

## Current r3 integration update (2026-10-08)

Status: the exact committed LWC-374 r3 checkpoint is normally merged into this LWC-370 branch, and the affected offline checks pass. PR #108 remains open; the coordinator owns fresh same-final-SHA TPM/reviewer review and canonical CI before any merge. This worker did not merge to `develop` or perform cloud deployment.

### Execution identity and integration

- Model / effort / mode: GPT-6-Luna (`gpt-6-luna`) / xhigh / YOLO; Codex session and thread `01a11a29-d15c-7fc0-9c76-cbf0c764d43d`.
- Orca runtime / Run / Task / Dispatch / terminal: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `run_ec3a3eca0058` / `task_177a0f044c74` / `ctx_b98dbf4ee68d` / `term_22cddd98-1061-4a48-b360-ab7a5c14a263`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1` / `Rayer/LWC-370-implementation-r1`.
- Starting local and remote PR #108 head: `9c421be6c433022035aa5ed52e0d5cba388351fc`; PR was OPEN against `develop` at base `f62bb530cb968e312b3faad77fe0f1b93b3e447b`. The complete existing PR body was inspected before publication and left unchanged.
- Incoming LWC-374 source commit: `226a154ed1adc6448bbd3facdd0c1b3ee7a7ad85`, parent `4fde834ac4bd6b20d6bc98b4a450a29739ac82fd`; its exact report checkpoint is `3533a6e6bf87b9b41d236197053b92f7c541b416`. The merge base and source parent matched the expected checkpoints. `git merge-tree --write-tree` predicted tree `2c512b7ffb1d2c080fff47853cdfd42b60700c5c`, equal to the normal merge result.
- Normal merge commit: `9879852eaa7ead74dbd2ba5950b6c729877a5c4f`, parents `9c421be6c433022035aa5ed52e0d5cba388351fc` and `3533a6e6bf87b9b41d236197053b92f7c541b416`, tree `2c512b7ffb1d2c080fff47853cdfd42b60700c5c`. The incoming delta is limited to `deploy/components/auth_config.py`, `deploy/components/auth_config.sh`, three Auth contract tests, and `docs/lwc-374/implementation-report-r1.md`; no conflict or 370 frontend hunk change occurred.
- Coordinator checkpoint received 2026-10-08: LWC-374 r3 at `3533a6e6bf87b9b41d236197053b92f7c541b416` has TPM PASS, independent Reviewer PASS receipt `23a05cc4263b9c98fd03def8`, and canonical run `37755548944` with all nine jobs successful. PR #107 was normally merged as `ca078774d1227a4397845864a956e4e302bbed49`; the coordinator reports `develop` has the same tree `532562cb`. Those results are the coordinator's evidence for 374, not a claim that this worker ran those jobs.

### Offline verification

All commands below ran under a macOS `sandbox-exec` profile that denied all inbound and outbound network except loopback. A separate probe confirmed loopback works and an external TCP connection is denied. Test `HOME`, `TMPDIR`, Go cache, Cloud SDK config, and generated config outputs were isolated under `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/lwc-370-r3-offline`; `CLOUDSDK_CONFIG` pointed there, `GOOGLE_APPLICATION_CREDENTIALS=/dev/null`, and an offline `gcloud` shim failed any unexpected provider call. Tests that need a provider used their explicit fake executable or test transport. Go used the existing local module cache with `GOPROXY=off`; no live provider, GSM, IAM, credential, paid Pipeline, or deployment call ran.

| Command | Workdir | Exit / result |
| --- | --- | --- |
| `python3 scripts/test_cd_contract.py` | repository root | 0; 70/70 tests passed. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` | `apps/bff` | 0; 21/21 Auth configuration contract tests passed, zero skips. |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` | `apps/bff` | 0; canonical BFF suite 119/119 passed, zero skips. |
| `python3 scripts/test_bff_explicit_cutover.py` | `apps/bff` | 0; 7/7 retained cutover/release/recovery cases passed. |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` | `apps/bff` | 0 on final attempt; 127/127 passed, zero skips. |
| `go test ./... -v -count=1 -race` | `apps/bff` | 0; 47 packages passed, 8 had no test files, 1,573 test pass events, 91 environment-dependent skip events, zero failures. Skips are not counted as coverage or passes. |
| `go test ./cmd/deploy_config -count=1` | `apps/bff` | 0; deploy_config package passed. |
| `go vet ./...` / `go build ./...` | `apps/bff` | 0 / 0. |
| `python3 ../../scripts/test_frontend_build_config.py` | `apps/frontend` | 0; 4/4 tests passed. |
| `python3 ../../scripts/test_frontend_config_artifacts.py` | `apps/frontend` | 0; 9/9 tests passed, including exact pinned downloader, source-A bytes after executor checkout B, no image rebuild, and wrong-environment/missing-artifact rejection before mutation using synthetic local fixtures. |
| `make config-local CONFIG_TARGET=frontend CAC_OUTPUT_DIR=<owned scratch>/cac`, and corresponding `make config-dev` / `make config-prod` | repository root | 0 / 0 / 0; all three actual Make/CLI calls emitted JSON containing only `api_url`, `auth_url`, and `schema_version`. |
| `make workflow-yaml` | repository root | 0; all five named workflows parsed. |
| `python3 -m unittest scripts.test_exportjob_provision_contract deploy.provision.test_exportjob_dev` | repository root | 0; 40/40 tests passed. |
| `node --test deploy/engine/tests/artifacts.test.cjs` | repository root | 0; 10/10 tests passed. |
| `node --experimental-strip-types --test tests/ci-workflow-contract.test.mjs` | `apps/frontend` | 0; 6/6 tests passed. |
| `bash -n scripts/local-vertical-smoke.sh` | repository root | 0. |

Two initial engine-suite attempts exited 1 at `Acceptance.test_auth_stage1_snapshot_refreshes_for_cross_plan_and_production_image_reuse`: that test's `patch.dict(clear=True)` removed the command-line Go cache settings before spawning `go run`, so Go tried to resolve uncached modules from `proxy.golang.org`, which the required OS network sandbox denied. The production code was unchanged; the isolated scratch `HOME` was then given a Go `GOENV` pointing at the already cached modules with `GOPROXY=off`, after which the exact canonical engine discovery passed 127/127. The two red attempts and this resolved harness cause are retained here rather than presented as passes.

The coordinator reports `make test-flash-execution` passed on exact upstream 374 checkpoint `3533a6e6bf87b9b41d236197053b92f7c541b416`; this worker did not execute that command. Canonical CI still must run on PR #108's final new head.

### Acceptance and remaining limits

| Acceptance area | State | Evidence / remaining work |
| --- | --- | --- |
| LWC-370 exact artifact selection/flattening, selected source publication after a different executor checkout, no rebuild, and rejection before mutation | PASS locally | 9/9 artifact contract tests with the exact pinned downloader source, controlled artifact client, and OS-denied external network. |
| Frontend-only public configuration across local/DEV/Production; no private binding output; required endpoint rejection; strict normal Auth/BFF/Worker validation | PASS locally | 4/4 build-config tests, 21 Auth contract tests, three actual Make/CLI outputs, and the existing normal-validator negative/control cases. |
| Retained BFF cutover, release, recovery, source, executor, eligibility, and permission assertions | PASS locally | BFF canonical 119/119, explicit cutover 7/7, CD contract 70/70, and workflow contract checks; no assertion was deleted or skipped. |
| LWC-374 r3 Auth source and mode/project mapping fixes | Integrated; parent final review/CI PASS | Exact source/report checkpoint is a parent of merge `9879852`; the coordinator reports both reviews and all nine upstream jobs passing at 374 checkpoint `3533a6e`. No Auth-owned file was edited by this worker. |
| PR #108 final exact-head readback, fresh same-SHA TPM/reviewer review, canonical CI, and normal merge | PENDING coordinator | Push the report checkpoint, read back exact remote head/base, then coordinator requests both reviews and CI on that resulting head. No develop merge by this worker. |
| GCS publication/readback, DEV/Production provider deployment, live GSM payload access, IAM/resources/credentials, paid Pipeline, UAT, and cloud verification | NOT RUN | Outside authorized scope; no cloud or deployment acceptance is claimed and status is not Verified. |

## Current r2 bounded repair addendum (2026-10-08)

Status: the three bounded 370 repairs and offline verification are complete at the integrated source checkpoint. PR #108 remains an implementation checkpoint: this worker has not merged to `develop` or run cloud deployment acceptance; final 370 same-SHA review and canonical CI remain with the coordinator.

### Execution identity and integration

- Model/effort/session: GPT-6-Luna (`gpt-6-luna`), xhigh, YOLO; Orca Run `run_ec3a3eca0058`, Task `task_8df9931e6da9`, Dispatch `ctx_8f8cfb7812a8`, terminal `term_22cddd98-1061-4a48-b360-ab7a5c14a263`.
- Worktree/branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1`, `Rayer/LWC-370-implementation-r1`.
- 370 repair commit: `eddc3b6278febacee28cc305f794c550db493ddd`; CLI-level regression test commit: `64e25404118bed9baee90c90c1f935524632947f`.
- Integrated 374 commit: exact `4fde834ac4bd6b20d6bc98b4a450a29739ac82fd`, merged normally after the clean 370 checkpoint. Integration merge commit: `27413f37582c2c254343d8c5d3060a0bb3aa4868`; tree `b2685be12e3d31404297d11d29f1589bb7b73c37`, matching the coordinator's expected tree. The additional CLI test is the only later source-tree commit.
- PR: [#108](https://github.com/Rayer/llm-wiki-cloud/pull/108), title “feat(frontend): deliver versioned public runtime config (LWC-370)”, base `develop`. Full existing PR body was inspected before publication; it was not edited. Pre-publication readback was OPEN, head `84a5ec906762e6f4e64ab34e4c4b34492be39033`, base `f62bb530cb968e312b3faad77fe0f1b93b3e447b`; the updated code/report checkpoint is being pushed and its exact post-push head/base readback is returned to the coordinator.

### r2 changes

- Set `merge-multiple: true` on the pinned exact-ID `actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093` step so the validated `RUNNER_TEMP/frontend-config` directory receives the selected bundle's files at its root. The artifact ID, generation run ID, metadata/hash/source/environment checks, publication target, and no-rebuild flow remain exact.
- Added the unchanged pinned `download-artifact.ts` source fixture (SHA-256 `1c67eb1bb4f77a462522341189f433422c4d35a6999d0dea2a1846e6edfedd80`) and a controlled local artifact client. The test runs the actual workflow executor-check, inspect, validate, and publication shell blocks against synthetic metadata and a local object store. It proves selected source A bytes publish after executor checkout B, and wrong-environment or missing-artifact cases stop before a storage mutation.
- Added a dedicated public Frontend output path to `deploy_config`. It strict-decodes the fixed environment YAML and validates only the public API/Auth endpoints before writing the existing schema. The real CLI and all three Make targets use this path; normal Auth/BFF/Worker `Load` validation remains in place, with a regression asserting the unselected Worker secret reference is still required by the normal validator.
- Updated the retained BFF workflow assertion for the already-present `frontend-config-only` operation/job while retaining the release, recovery, source, executor, eligibility, and permission assertions.

### Verification at the integrated checkpoint

| Command | Exit | Result |
| --- | ---: | --- |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (from `apps/bff`; `TMPDIR` set to the owned LWC-370 scratch directory) | 0 | 119/119 tests passed, zero skips. Baseline was 119 tests with one retained contract failure; two intermediate assertion mismatches were corrected without deleting or skipping the test. |
| `python3 scripts/test_bff_explicit_cutover.py` (from `apps/bff`) | 0 | 7/7 retained release/recovery/workflow safety cases passed. |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (from `apps/bff`) | 0 | 127/127 retained deployment engine tests passed, zero skips. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | 0 | 15/15 Auth configuration contract tests passed. |
| `go test ./... -v -count=1 -race` (from `apps/bff`) | 0 | 47 Go packages passed; 8 packages had no test files. The run recorded 91 environment-dependent skip events (emulator/local prerequisites were not configured); these are not counted as coverage or passes. Full log: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/370-repair-r2/go-race-postmerge.log`. |
| `go test ./cmd/deploy_config -count=1` (from `apps/bff`, after the CLI subprocess test commit) | 0 | Deploy config package passed, including minimal-public-config CLI success, missing-endpoint rejection, and unchanged normal component validation. |
| `go vet ./...` / `go build ./...` (from `apps/bff`) | 0 / 0 | Both passed. |
| `python3 scripts/test_frontend_build_config.py` / `python3 scripts/test_frontend_config_artifacts.py` (from repo root) | 0 / 0 | 4/4 build-config tests and 9/9 artifact handoff tests passed. |
| `make config-local CONFIG_TARGET=frontend` / `make config-dev CONFIG_TARGET=frontend` / `make config-prod CONFIG_TARGET=frontend` | 0 / 0 / 0 | All actual Make/CLI paths generated local, Development, and Production public config using the worktree's selected `8080`/`8081` local ports and existing environment YAML endpoints. |
| `make workflow-yaml` (from repo root) | 0 | CI, CD, DEV, Production, and Frontend generator workflow YAML checks passed. |
| `git diff --check` | 0 | No whitespace errors. |

### Acceptance and limits

| Acceptance area | State | Evidence / remaining work |
| --- | --- | --- |
| Pinned exact artifact flattening, source-A-after-checkout-B publication, no rebuild, and pre-mutation wrong-environment/missing-artifact rejection | PASS for offline local flow | Exact pinned downloader source with a controlled client and the actual workflow validation/publication shell; 9/9 artifact tests. No GitHub artifact or cloud storage client was used by the test. |
| Frontend-only public config output with unrelated private bindings absent; missing required public endpoint rejected | PASS for local CLI/helper | Synthetic minimal-YAML CLI subprocess, missing-endpoint negative, normal validator control, and actual local/dev/prod Make commands. |
| Retained BFF cutover/release/recovery boundaries | PASS for local suite | Canonical BFF discovery 119/119 and explicit cutover suite 7/7; no safety assertions removed or skipped. |
| Integrated LWC-374 Auth/engine source | Integrated; inherited formal HOLD remains | Exact committed 374 checkpoint is present and its offline suites passed locally. Coordinator reported independent 374 Reviewer HOLD on exact `4fde834`: standalone `auth_config.main` version/verify/freeze callers omit trusted `project_number` and reject a legal numeric resource; default-mode precedence also mishandles item mode `0`. This is owned by the separate 374 lane; this worker did not edit Auth-owned files and does not claim those findings are cleared. |
| Final PR #108 same-SHA 370 TPM/reviewer review and canonical CI | PENDING | Coordinator owns same-final-SHA review and canonical CI on the pushed head before any develop merge. |
| Cloud GCS publication/readback, DEV/Production provider deployment, GSM payload access, IAM/resources/credentials, paid Pipeline, UAT, cloud verification | NOT RUN | Outside this worker's authorized scope; deployment acceptance remains NOT RUN and no Verified claim is made. |

The r1 report below records the earlier Frontend slice. Its local Frontend lint/typecheck/build results are historical at the earlier source checkpoint; r2 did not change Frontend application code. The r2 integrated source and current open 374 review status above take precedence for this repair checkpoint.

---

## Historical r1 implementation checkpoint

Status at the earlier r1 source checkpoint: scoped Frontend implementation and shared offline integration were committed and PR #108 was open; the named local acceptance and affected CI suites recorded below passed at that earlier checkpoint.

## Execution identity

- Model/effort: GPT-6-Luna, xhigh (observed terminal footer).
- Orca session: Run `run_ec3a3eca0058`; Task `task_c4cbb43891fe`; Dispatch `ctx_7d4c191aadf5`.
- Terminal/incarnation: `term_22cddd98-1061-4a48-b360-ab7a5c14a263` / `a8f2737e-877a-421e-a69c-badea9b7c78b`.
- Worktree/branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1`, `Rayer/LWC-370-implementation-r1`.
- Own frontend checkpoint commit: `7e69e0b4ef3f8215a4ec091bd8dba6e077e171a5`.
- LWC-374 baseline `c86101d8c999c5c05db9fdf8798073002bae79b0` and final checkpoint `12847cd616e73d7ddcaccbdc612245c0c34d7b13` are integrated by normal merge. The final checkpoint contains repair commit `a5763a361b9e453b2cf0df838340a69154c99f8e` and LWC-374 report-only commits. Integrated source HEAD before this report update is `87f9ab84b00d69c9e22e391b43ec7e1823d2c942`.
- PR: [#108](https://github.com/Rayer/llm-wiki-cloud/pull/108), OPEN. Creation readback: head `ddbe8e0cdb514e6c5ba167d8072577f67dc1a8eb` (`Rayer/LWC-370-implementation-r1`), base `f62bb530cb968e312b3faad77fe0f1b93b3e447b` (`develop`). After pushing that report update, the latest readback was head `347629c6760768c026eec1e90b2ce472bbaf3641`, base `f62bb530cb968e312b3faad77fe0f1b93b3e447b`, state OPEN. This final report-only commit advances the head once more; the post-push head is recorded to the coordinator.
- The PR creation readback above records the exact remote head/base before this report-only update; after pushing the update, the branch head is read back again and reported to the coordinator.

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
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (from `apps/bff`) | 0 | Final LWC-374 Auth contract suite passed 15/15 tests. |
| `python3 -m unittest discover -s deploy/engine/tests` | 0 | Final deployment engine suite passed 124/124 tests after the exact LWC-374 fixture repair was integrated. |
| `go test ./... -count=1 -race` (from `apps/bff`) | 0 | All Go packages passed with the race detector enabled. |
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

Initial frontend-slice runs exposed stale workflow assertions: `npm test` exited 1 with 521/526 Node tests passing and five workflow-contract failures; the first `python3 scripts/test_cd_contract.py` exited 1 with 3/70 workflow-assertion failures. The assertions were updated narrowly, then the complete frontend reruns passed with no skips. Before the LWC-374 fixture repair, Auth config and deployment engine suites exposed the owned fixture failures recorded in the checkpoint; after normal integration of exact final checkpoint `12847cd616e73d7ddcaccbdc612245c0c34d7b13`, the current integrated Auth suite is 15/15 and engine suite is 124/124.

All public JSON, hosts, and artifact metadata used for local tests were synthetic. No deployment workflow was dispatched, and no Vercel, GCS, GSM, IAM, credential, or paid Pipeline action was run.

## Frozen acceptance checkpoint

| Area | State | Evidence / remaining work |
| --- | --- | --- |
| Runtime reader, boundary, no fallback, timeout/retry/cache, and all frontend consumers | PASS for frontend slice | Runtime config tests, full frontend suite, and built client config A/B causal check. |
| Local development route and legal-page independence | PASS for offline integration | Named route cases 2/2 and full component suite. The expected public `.build/cac/local/frontend-config.json` was generated; no `.env.local`, local scope, or key file was read or changed. |
| Frontend config artifact metadata, exact-ID selection, success/source/environment/hash checks, no rebuild publication branch, and Make producer | PASS for offline contract | Artifact tests 7/7; local/dev/prod Make targets emitted the expected public fields. Source A after source B and wrong-environment rejection passed with synthetic artifacts. |
| Static receipt schema and deployment adapter | PASS for offline integration | Receipt contains reader version/config URL; 56/56 adapter cases and 51/51 engine cases pass, including admission, snapshot/restore, and reuse against the new normalized identity. |
| GCS generation/publication, live object generation, CORS/public GET, and cloud readback | NOT RUN | The workflow code is present, but no DEV/Prod provider action or cloud resource was touched. Bucket/IAM/CORS readiness remains unverified. |
| LWC-369/LWC-371 compatibility | PASS for offline/local checks | Retained engine suite 124/124, frontend suite 527/527 plus 307/307 component tests, and current-head CD contract 70/70 pass; no LWC-371 charging/refund paths were changed. |
| LWC-374 producer/normalizer/engine integration | PASS for offline/local integration | Exact final checkpoint `12847cd616e73d7ddcaccbdc612245c0c34d7b13` is integrated. Frontend generation, local ports, normalized target identity, engine reuse, Auth contract, and engine admission/resume fixtures pass their named suites. |
| DEV/Production deployment, live GSM, IAM/resources/credentials, paid Pipeline, UAT, and cloud verification | NOT RUN | Outside this worker’s authorized execution scope. Offline/local results do not claim cloud verification or Verified status. |

Historical r1 conclusion at source HEAD `87f9ab84b00d69c9e22e391b43ec7e1823d2c942`: the earlier Frontend implementation and then-current LWC-374 checkpoint were committed with PR #108 open. That checkpoint predates the r2 repair and the current inherited 374 formal HOLD; the current status is recorded in the r2 addendum above.

---

## LWC-370 registered generation entry repair (2026-10-08)

### Status and execution identity

- This bounded repair adds a canonical, read-only generation entry through the already-registered DEV and Production wrappers. It reuses the existing generator job and exact artifact contract; it does not publish config or deploy an application.
- Model / effort / mode: GPT-6-Luna (`gpt-6-luna`) / xhigh / YOLO. Codex session/thread: `01a11a29-d15c-7fc0-9c76-cbf0c764d43d`.
- Orca runtime / Run / Task / Dispatch / terminal: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `run_ec3a3eca0058` / `task_bc65c32a29c9` / `ctx_6c899e0b8c14` / `term_22cddd98-1061-4a48-b360-ab7a5c14a263`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1` / `Rayer/LWC-370-generation-entry-r1`.
- Starting `origin/develop` and local HEAD: `4b5f331e15b4fd361b74e18a0351799580eb434e`. Code/test commit: `852ca461f73cf4d5c2e5dfcc02ae263ab68ffce9`.
- PR #108 was already merged to `develop` before this repair. New PR [#109](https://github.com/Rayer/llm-wiki-cloud/pull/109) was created against `develop`; its initial head/base readback is recorded below. Follow-up contract-test commits update that head, and the final remote head is reported after the final push.

### Root cause and repair

Owner direction records the original attempt `gh workflow run generate-frontend-config.yml --ref develop environment=dev` returning HTTP 404 because the workflow was absent from the default branch at `8b01fcb385fb43a71f1b82cb0a7dc443c775082a`; a fresh Actions run listing showed no created run. The existing registered wrappers had no read-only generation route, and the artifact inspector accepted only the direct generator workflow path.

- Added `workflow_call` to `.github/workflows/generate-frontend-config.yml`, retaining the existing manual trigger and single generator implementation.
- Added `frontend-config-generate` to `.github/workflows/deploy-dev.yml` and `.github/workflows/promote-production.yml`. Each fixed-environment job runs only on `develop`, has `contents: read`, calls only the existing generator, and passes no secrets. The DEV and Production release jobs exclude this operation; DEV main-fast-forward eligibility excludes it too.
- Updated `scripts/frontend_config_artifacts.py` to accept only the direct generator workflow or the exact matching official wrapper path for `dev` or `prod`. It still validates the selected artifact ID, exact artifact name, run identity, `workflow_dispatch` event, completed/success status, source SHA, and environment. Unsupported environments, cross-environment wrappers, and arbitrary workflow paths reject.
- Expanded generator, metadata, engine-wrapper, and retained BFF workflow tests. No shared CD publication/release/recovery implementation or cloud behavior changed.

### RED-to-GREEN evidence

The new checks were run before implementation to record the original failure shape; none was skipped.

| Pre-implementation command | Exit | Result |
| --- | ---: | --- |
| `python3 -m unittest scripts.test_frontend_config_artifacts.FrontendConfigArtifactTests.test_metadata_binds_exact_artifact_to_successful_generator_run_and_source scripts.test_frontend_config_artifacts.FrontendConfigArtifactTests.test_workflows_keep_generation_and_publication_as_separate_exact_artifact_steps` | 1 | 2 tests; 2 errors. The official wrapper provenance was rejected and the reusable workflow trigger was absent. |
| `python3 -m unittest scripts.test_engine_workflow.EngineWorkflowContract.test_readonly_diagnostic_is_a_separate_fixed_workflow_branch` | 1 | 1 test; 1 failure because the generation operation/guard/job did not exist. |
| `python3 -m unittest discover -s apps/bff/scripts -p 'test_bff_explicit_cutover.py'` | 1 | 7 tests; 1 retained workflow contract failure for the missing generation route. |

Final local verification used a macOS `sandbox-exec` profile that denied external network access and permitted only localhost sockets for disposable loopback fixtures. A synthetic connection probe confirmed loopback was permitted and the external documentation address `192.0.2.1:443` was denied. All final rows below report zero skips.

| Final command | Exit | Result |
| --- | ---: | --- |
| `python3 -m unittest discover -s scripts -p 'test_frontend_config_artifacts.py'` | 0 | 9/9 artifact/generation/metadata cases passed, including exact wrapper provenance, cross-environment/untrusted path rejection, pinned downloader behavior, source-A bytes after checkout-B, and rejection before synthetic mutation. |
| `python3 -m unittest discover -s scripts -p 'test_engine_workflow.py'` | 0 | 8/8 workflow and permission contract cases passed. |
| `python3 -m unittest discover -s apps/bff/scripts -p 'test_*.py'` | 0 | 119/119 retained BFF tests passed, including the full wrapper safety suite. |
| `python3 -m unittest discover -s scripts -p 'test_cd_contract.py'` | 0 | 70/70 retained CD contract tests passed. |
| `python3 -m unittest discover -s apps/bff/scripts -p 'test_local_dev_makefile.py'` | 0 | 20/20 local fixture and loopback tests passed. |
| `python3 -m unittest test_bff_explicit_cutover.SharedCDContractTest.test_shared_bff_path_preserves_cutover_safety_boundaries` (with `PYTHONPATH=apps/bff/scripts`) | 0 | 1/1 retained BFF wrapper safety test passed under network denial. |
| `python3 -m unittest scripts.test_exportjob_provision_contract deploy.provision.test_exportjob_dev` | 0 | 40/40 workflow-source-guard provisioning tests passed. |
| `node --test deploy/engine/tests/artifacts.test.cjs` | 0 | 10/10 offline artifact transport tests passed. |
| `node --experimental-strip-types --test tests/ci-workflow-contract.test.mjs` (from `apps/frontend`) | 0 | 6/6 workflow source guard tests passed, including generation provenance and wrapper isolation. |
| `bash -n scripts/local-vertical-smoke.sh` | 0 | Smoke script syntax passed. |
| `npm test` (from `apps/frontend`) | 0 | 527/527 Node tests and 307/307 Vitest component tests passed; zero skips. |
| `npm run lint` / `npm run typecheck` (from `apps/frontend`) | 0 / 0 | ESLint and TypeScript checks passed. No frontend application source changed, so no local frontend production build was run. |
| `make workflow-yaml` | 0 | CI, CD, DEV, Production, and generator workflow YAML all parsed successfully. |
| `git diff --check` | 0 | No whitespace errors. |

An initial sandbox profile blocked shell temporary-file creation and one test fixture's executable-bit setup; those attempts were treated as harness failures, not passes. After narrowing temporary-file allowances to the owned profile scratch plus the per-user shell temp path needed for spawned Bash heredocs and the exact synthetic fixture, the full BFF/CD suites and affected local suites passed with external network still denied. Test fixture/artifact directories used `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/370-generation-entry-tmp`; repository status contained only the explicitly scoped workflow/helper/test/report files.

### Acceptance matrix and remaining limits

| Acceptance | State | Evidence / limit |
| --- | --- | --- |
| Registered wrapper can select DEV public config generation on `develop` | PASS for local workflow contract | Separate `contents: read` call to the existing generator; release and eligibility routes exclude the operation. No Actions run was dispatched. |
| Registered wrapper can select Production public config generation on `develop` | PASS for local workflow contract | Fixed `prod` input and `contents: read`; the application Production release remains restricted to `main` and excludes generation. No Production application action was run. |
| Exact artifact/source/environment validation | PASS for local synthetic metadata | Only the direct generator or matching official wrapper path is accepted; latest selection, arbitrary workflow paths, cross-environment provenance, failed runs, wrong IDs, and unsupported environments reject. |
| Existing exact artifact publication/release/recovery paths | PASS for local contracts | CD contract and BFF retained safety suites passed; those paths were not changed. |
| GitHub Actions generated artifact ID, public bucket object bytes, public GET/CORS readback, and formal DEV application deployment | NOT RUN | Parent owns review/CI/merge and formal DEV transitions. The repaired workflow was not dispatched. No storage, Google Cloud, GSM, IAM, credential, or paid Pipeline action was used. |
| Application Production deployment or production provider state | NOT AUTHORIZED / NOT RUN | Out of scope for this worker. No Verified/cloud-acceptance claim is made. |

The historical HTTP 404 is the original live blocker; the local tests prove the new checked-in entry and provenance contract only. Until parent-owned same-SHA review/CI/merge and the authorized formal DEV workflow, there is no generated live artifact or cloud readback to report.

### PR publication and canonical CI checkpoint

- PR [#109](https://github.com/Rayer/llm-wiki-cloud/pull/109) is OPEN against `develop`. Initial publication readback: head branch `Rayer/LWC-370-generation-entry-r1`, head `52acfe8e2434955f938ed414038c46ff73186c07`, base branch `develop`, base SHA `4b5f331e15b4fd361b74e18a0351799580eb434e`; `git ls-remote` matched the head. `gh pr view` returned the reviewed body content with one additional trailing newline.
- The initial canonical CI run on that head was `37772406901`. At the readback, `frontend-lint`, `frontend-typecheck`, `actionlint/schema`, and `local-vertical-smoke` were successful; `frontend-test` and `workflow-source-guards` failed on stale operation/guard assertions; `frontend-build` was skipped after the frontend-test failure; `bff` was still in progress. The initial run is not evidence for the corrected head.
- The failure was isolated to retained test expectations in `apps/frontend/tests/ci-workflow-contract.test.mjs`, `lwc-253-vercel-dev-authority.test.mjs`, and `lwc-258-vercel-production-auth-env.test.mjs`. These tests now assert the new operation, exact read-only generation job, and release/eligibility exclusions. The full frontend suite passes locally after the fixes.
- GitHub PR automation also created Vercel and security status checks after PR creation. Those automatic checks are not worker-initiated provider commands or a formal application release. No independent review had arrived at the initial readback; the coordinator owns same-final-SHA TPM/reviewer review and fresh canonical CI after the follow-up commits.
- The test-only correction commit was `d5e297bd9010c00b39a5ed964aa88436bc5cc005`; the final reviewed PR head was `1dfad211e3128bc5b98c6bfd73f841da7a7b5e93` and was merged into `develop` as `057b8fd96189f50d6aba4f1fcace5d001304bf54`. Post-merge canonical CI run `37761956338` passed all nine jobs. The later formal DEV release attempt and its bounded failure analysis are recorded in the following section.

## LWC-370 DEV failure cause preservation (2026-10-08)

### Status and execution identity

- This bounded corrective repair preserves the original failing component and its bounded, typed materialization failure through the existing poll and automatic compensation path. It does not change deployment status, recovery, checkpoint, exit, or rollback policy.
- Model / effort / mode: GPT-6-Luna (`gpt-6-luna`) / xhigh / YOLO, same retained Codex session `01a11a29-d15c-7fc0-9c76-cbf0c764d43d`.
- Orca runtime / Run / Task / Dispatch / terminal: `8bc79eed-2312-4c07-a306-f91ae8d31716` / `run_ec3a3eca0058` / `task_c1e46c152acb` / `ctx_423bcd9500a4` / `term_22cddd98-1061-4a48-b360-ab7a5c14a263`.
- Worktree / branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1` / `Rayer/LWC-370-dev-failure-cause-r1`.
- Starting local and `origin/develop` SHA: `057b8fd96189f50d6aba4f1fcace5d001304bf54`; the worktree was clean before branching. Source/test commit: `f094e302a24c4077d39cad1f8d7cee6eb2758452`.
- PR publication was pending when this report section was written; the final PR number and remote head are reported in the coordinator handoff.

### Source-backed finding and repair

The saved DEV checkpoint records Auth failed at sequence 18 while Export was verified, then both were restored and verified by sequence 24. Its final result instead names `exportjob` and `basic-sanity-mismatch`, with no failure diagnostic or cause; the Auth candidate remained `preparing` / `not_started` with no version or revision. The supplied incident checkpoint correctly classifies the remote root cause as unknown and the missing destination Auth/BFF Secret containers as an unproven prerequisite observation.

Source inspection explains the loss without claiming the historic trigger. `providers.prepare_auth_config_version` invokes the Go `materialize-auth` command before Secret Manager version publication. Go `auth.go` deliberately returns the generic safe message `selected secret version could not be accessed` for secret-read failures, but `auth-config-materialize` was absent from the engine's structured-cause stage allowlist. If materialization fails, `Engine.deploy` reconciles the still-empty candidate; reconciliation returns without an error, then poll replaces the deployment exception with `basic-sanity-mismatch`. Automatic restore iterates in reverse component order and overwrote `engine.component`, which `Engine.result` then used for the top-level result. The original remote exception was not stored, so the specific historical permission/resource/config cause cannot be reconstructed.

- Added `auth-config-materialize` to the existing bounded structured-cause allowlist, retaining only the fixed exception type/code, stage, exit/timeout metadata, and bounded redacted message.
- When a deployment exception is followed by a poll failure, the result now carries both typed deployment and poll phases. After compensation, the engine restores the original failed component as the result identity; final stage/status/checkpoint and rollback actions are unchanged.
- Added a synthetic production-caller regression through actual `Engine.deploy`, Auth materialization invocation, reconcile, poll, restore, and result writing. A second regression invokes the real Node deployment Action entrypoint and checks stdout equals the retained `result.json`. Only the Go subprocess is replaced by a fixed synthetic failure; existing fake gcloud/provider commands handle all other operations.
- No Go source or Auth/BFF publication code changed. The actual Go `materialize-auth` command was not run locally; no SDK, Secret Manager, ADC, live provider, IAM, resource, credential, paid Pipeline, cloud deployment, or rebuild action was performed.

### RED-to-GREEN and local verification

The first test harness invocation exited 1 because the new test referenced a nonexistent test-module attribute; the fixture reference was corrected before recording the source RED. The corrected pre-fix test exited 1 with `result.component == exportjob` where the production caller regression expected the original `auth` component. After the repair, all following commands exited 0; unittest reported zero skips.

| Command (working directory) | Exit | Result |
| --- | ---: | --- |
| `python3 -m unittest test_engine.Acceptance.test_auth_materialization_failure_survives_poll_and_compensation` (`deploy/engine/tests`, before source repair) | 1 | 1 test; expected original Auth component, observed `exportjob`, with final `failed_rolled_back` / `basic-sanity-mismatch` state. |
| `python3 -m unittest test_engine.Acceptance.test_auth_materialization_failure_survives_poll_and_compensation` (`deploy/engine/tests`, after repair) | 0 | 1/1 passed; exact synthetic command error and component survived compensation; no Auth version add occurred. |
| `python3 -m unittest test_engine.Acceptance.test_runtime_action_forwards_auth_materialization_cause` (`deploy/engine/tests`) | 0 | 1/1 passed through the actual Node Action; machine result matched retained result JSON. |
| `python3 -m unittest discover -s ../../deploy/engine/tests -p 'test_*.py'` (`apps/bff`) | 0 | 129/129 engine tests passed, zero skips. |
| `python3 ../../scripts/test_cd_contract.py` (`apps/bff`) | 0 | 70/70 retained CD contract tests passed. |
| `python3 -m unittest discover -s ../../scripts -p 'test_*auth_config_contract.py'` (`apps/bff`) | 0 | 21/21 Auth/BFF configuration contract tests passed. |
| `python3 -m unittest discover -s scripts -p 'test_*.py'` (`apps/bff`) | 0 | 119/119 retained CD safety tests passed. |
| `go test ./cmd/pipeline_config -count=1` (`apps/bff`) | 0 | Go materializer/config package tests passed; test readers/transports are synthetic or local. |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/370-cause-loss-tmp sandbox-exec -f /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/implementation-wave/370-cause-loss-tmp/network-deny.sb python3 -m unittest test_engine.Acceptance.test_auth_materialization_failure_survives_poll_and_compensation test_engine.Acceptance.test_runtime_action_forwards_auth_materialization_cause` (`deploy/engine/tests`) | 0 | 2/2 passed under OS outbound-network denial using the owned profile TMPDIR and synthetic provider/materializer intercepts. A separate sandbox probe confirmed outbound denial. |
| `git diff --check` | 0 | No whitespace errors. |

### Acceptance and remaining limits

| Acceptance | State | Evidence / limit |
| --- | --- | --- |
| Auth remains the reported failed component after automatic restore | PASS offline | Production engine/result regression and actual Action regression; stage remains `failed_rolled_back`, reason remains `basic-sanity-mismatch`, and component status/checkpoint rollback evidence is unchanged. |
| Bounded useful nonsecret materialization cause reaches the result | PASS offline | Result carries `ChildProcessError` / `child-command-failed`, fixed stage `auth-config-materialize`, exit code, and the Go materializer's generic safe message. Raw provider output and payload are not emitted. |
| Specific cause of DEV run `37776060581` | UNKNOWN | Original pre-compensation exception was not retained; no offline test can reconstruct it. The observed missing destination Secret containers are not proof of the original cause. |
| Application DEV deployment, live provider readback, Secret Manager payload or IAM/resource changes | NOT RUN | Parent retains deployment/provisioning authority; no cloud action was taken by this worker. Existing ready/final IDs and receipts were not rebuilt or altered. |
| Production deployment, main promotion, merge, paid Pipeline | NOT AUTHORIZED / NOT RUN | Out of this worker's scope. |

### PR and canonical CI checkpoint

- PR [#110](https://github.com/Rayer/llm-wiki-cloud/pull/110) was opened against `develop`. At initial publication, exact head `23cf06b6798150d9bf71798f6ed11e488f62baab` matched `git ls-remote`; exact base was `057b8fd96189f50d6aba4f1fcace5d001304bf54`. The PR body readback matched the reviewed body.
- Canonical CI run `37781207753` was `in_progress` at readback for that head. `workflow-source-guards` had passed; `bff`, `actionlint/schema`, `frontend-test`, `frontend-typecheck`, `frontend-lint`, and `local-vertical-smoke` were still running or pending. The final report-only push will advance the head; its exact remote readback and current CI state are in the coordinator handoff.

The repair makes the next failure result discriminating and keeps the known Auth component visible; it does not claim that the historical root cause is resolved. PR publication and exact final remote head are owned by the coordinator handoff after this report commit.
