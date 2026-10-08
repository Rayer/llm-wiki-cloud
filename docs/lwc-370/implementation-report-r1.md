# LWC-370 implementation report (r1)

Status: frontend reader, receipt adapter, and artifact publication workflow checkpoint; shared producer/normalizer integration remains to be completed against the committed LWC-374 checkpoint. No commit or PR exists yet.

## Execution identity

- Model/effort: GPT-6-Luna, xhigh (observed terminal footer).
- Orca session: Run `run_ec3a3eca0058`; Task `task_c4cbb43891fe`; Dispatch `ctx_7d4c191aadf5`.
- Terminal/incarnation: `term_22cddd98-1061-4a48-b360-ab7a5c14a263` / `a8f2737e-877a-421e-a69c-badea9b7c78b`.
- Worktree/branch: `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-370-implementation-r1`, `Rayer/LWC-370-implementation-r1`.
- Current HEAD/base: `f62bb530cb968e312b3faad77fe0f1b93b3e447b` (all LWC-370 work is uncommitted at this checkpoint).
- PR: none. Exact PR URL, head/base readback, and final commit SHA will be recorded after the shared integration checkpoint and publication.
- Coordinator released the exact committed LWC-374 baseline `c86101d8c999c5c05db9fdf8798073002bae79b0` (parent `f62bb530cb968e312b3faad77fe0f1b93b3e447b`) for normal integration after this scoped checkpoint is committed. That commit is not yet merged into this worktree.

## Implemented checkpoint

- Added runtime config schema/URL validation, deferred no-credential/no-cache loading, 10-second timeout, lifecycle caching, retry/error states, and a workspace boundary that mounts AuthProvider/Shell only after config loads. API, upload, Auth, Google, CLI, public-config, and version callers now use the loaded runtime URLs.
- Added a development-only `/frontend-config.json` route that reads the current worktree file on every request, sanitizes the public fields, sends no-store, and returns 404 outside development. Legal pages remain independent.
- Replaced the static build receipt’s baked API/Auth endpoints with `{schema_version: 1, config_url}`. `deploy/components/frontend.sh` now builds with `NEXT_PUBLIC_CONFIG_URL` and validates the exact reader-schema/bootstrap URL receipt while retaining its deployment identity, archive, alias snapshot, rollback, and reuse checks.
- Added the manual `generate-frontend-config.yml` artifact workflow and isolated `frontend-config-only` routing for the existing DEV/Production entries. The publisher identifies one artifact ID and successful generator run, checks environment/source/hash/public JSON before cloud authentication, then targets only the fixed environment object and records object generation plus byte-equal readback evidence.
- Added the receipt parser and exact artifact handoff scripts to the existing frontend CI test job; no separate status check or deployment gate was added.
- Added synthetic metadata, manifest, wrong-environment, source-A-after-source-B, route, built-bundle, and deployment adapter controls. Updated direct workflow contract assertions to preserve existing release/recovery behavior while covering the new branch.
- The artifact workflow expects `make config-dev/config-prod CONFIG_TARGET=frontend` to emit `.build/cac/<env>/frontend-config.json`; the normalized deployment plan must also expose `.frontend.config_url`. Those producer/normalizer paths are pending integration from the exact LWC-374 commit. No uncommitted LWC-374 bytes were copied.

## Local verification

`npm ci` in `apps/frontend`: exit 0; 540 packages added, 541 audited. npm reported 17 audit findings (3 moderate, 13 high, 1 critical); no lockfile change was made. The frontend test script change is intentional for synthetic reserved-host setup.

| Command | Exit | Result |
| --- | ---: | --- |
| `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/lwc-370-local-config-route.test.mjs` (from `apps/frontend`) | 0 | 2/2 route cases passed: per-request worktree read/no-store/sanitization and development-only availability. |
| `node --test apps/frontend/tests/lwc-306-frontend-cd.test.mjs` (from repo root) | 0 | 56/56 deployment adapter cases passed, including new receipt acceptance, wrong-URL rejection, and old receipt rejection, with synthetic provider responses only. |
| `python3 scripts/test_frontend_build_config.py` | 0 | 4 receipt parser/transport tests passed. |
| `python3 scripts/test_frontend_config_artifacts.py` | 0 | 7 artifact/workflow tests passed, including exact artifact identity, source A bytes after checkout B, and cross-environment rejection before synthetic object mutation. |
| `node tests/lwc-318-built-config.mjs` (from `apps/frontend`) | 0 | One real Next webpack client build; two fresh app lifecycles read synthetic config A/B and exercised API/public/version/Google consumers. Receipt matching control exited 0 and wrong URL exited 1 as expected. |
| `python3 scripts/test_cd_contract.py` | 0 | Full retained suite: 70/70 tests passed, including the updated shared workflow assertions. |
| `npm test` | 0 | Node suite 527/527; Vitest 33/33 files and 307/307 tests passed, including 6 runtime-config cases. |
| `npm run lint` | 0 | ESLint completed without findings. |
| `npm run typecheck` | 0 | TypeScript completed without errors. |
| `make workflow-yaml` | 0 | Existing CI/CD workflow YAML checks passed; the new generator workflow is parsed by the artifact workflow tests. |
| `NEXT_PUBLIC_CONFIG_URL=https://config.example.test/frontend-config.json npm run build` (from `apps/frontend`) | 0 | Next production build compiled/typechecked and emitted `/frontend-config.json` as a dynamic route. The static receipt body was checked for exactly `schema_version` and the synthetic `config_url`. |
| `bash -n deploy/components/frontend.sh apps/frontend/tests/fixtures/lwc-306-fake-curl apps/frontend/tests/fixtures/lwc-306-fake-vercel` | 0 | Shell syntax passed. |
| `node --check apps/frontend/tests/lwc-318-built-config.mjs` | 0 | Node syntax passed. |
| `git diff --check` | 0 | No whitespace errors. |

Initial full-suite runs exposed stale exact workflow assertions: `npm test` exited 1 with 521/526 Node tests passing and five workflow-contract failures; the first `python3 scripts/test_cd_contract.py` exited 1 with 3/70 workflow-assertion failures. The assertions were updated narrowly to reflect the new operation/job/input and add branch-isolation checks; the complete reruns above passed with no skips.

All public JSON, hosts, and artifact metadata used for local tests were synthetic. No deployment workflow was dispatched, and no Vercel, GCS, GSM, IAM, credential, or paid Pipeline action was run.

## Frozen acceptance checkpoint

| Area | State | Evidence / remaining work |
| --- | --- | --- |
| Runtime reader, boundary, no fallback, timeout/retry/cache, and all frontend consumers | PASS for frontend slice | Runtime config tests, full frontend suite, and built client config A/B causal check. |
| Local development route and legal-page independence | PASS for frontend slice | Named route cases 2/2; full component suite. No worktree config file was read or created during verification. |
| Frontend config artifact metadata, exact-ID selection, success/source/environment/hash checks, and no rebuild publication branch | PASS for offline contract | Artifact tests 7/7 and reusable workflow contract suite. Actual Make producer and normalized plan integration remain pending LWC-374 merge. |
| Static receipt schema and deployment adapter | PASS for frontend slice | Built static receipt contains only reader version/config URL; adapter positive and negative controls pass. End-to-end target admission, snapshot/restore/reuse across the new normalized contract remains pending shared integration. |
| GCS generation/publication, live object generation, CORS/public GET, and cloud readback | NOT RUN | The workflow code is present, but no DEV/Prod provider action or cloud resource was touched. Bucket/IAM/CORS readiness remains unverified. |
| LWC-369/LWC-371 compatibility | PASS for current checkpoint; recheck after shared integration | Retained CD and frontend suites pass; no LWC-371 charging/refund paths were changed. |
| LWC-374 producer/normalizer/engine integration | PENDING | Integrate only the exact committed `c86101d8c999c5c05db9fdf8798073002bae79b0` checkpoint after committing this scoped work; then complete frontend target generation, local ports, normalized admission/reuse equality, and retained tests. |
| DEV/Production deployment, live GSM, IAM/resources/credentials, paid Pipeline, UAT, and cloud verification | NOT RUN | Outside this worker’s authorized execution scope. Offline/local results do not claim cloud verification or Verified status. |

No commit, push, PR, TPM review, independent review, canonical CI, merge-to-develop, or deployment has occurred at this checkpoint. This report will be updated with post-integration commands/AC, final SHA, PR URL, and exact remote PR head/base readback before publication.
