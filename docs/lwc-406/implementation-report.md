# LWC-406 local implementation checkpoint

Date: 2026-10-18  
Branch: `Rayer/LWC-406-project-home-ux`  
Baseline: `ce6bf981b32f3fbb6d64383002ab1ed33e9c4e06`  
Checkpoint: source-stable and ready for parent review; no commit or push was made.

## Implemented

- `NewProjectModal` now creates from the project name only. Its local help reuses Profile guidance, returns to the form with the typed name intact, and does not create a project. Successful creation navigates to that project's Profile page; a navigation retry reuses the created project instead of creating a duplicate.
- `WorkspaceProvider` treats last-project persistence as best effort so storage errors do not convert a successful create/select into a failed create. The optional onboarding intent is scoped to owner, project, and session, fenced against late responses, and recorded only when the matching valid Profile UI is rendered. Dismissal/presentation survives the supported remount/revisit cases.
- `ProjectProfilePanel` presents the optional nonmodal notice with the approved copy and two explicit actions. “查看本頁設定” opens the existing requirements section without saving; “先加入資料” follows the existing dirty-navigation guard and goes to `/raw`.
- Removed the Profile footer's advanced-settings link while retaining navigation and Beta-marked Advanced/CLI entries. Home navigation is named Home in the shell and command palette; the actual search command is unchanged.
- Added Traditional Chinese and English messages, regressions for creation/storage/navigation/onboarding, and updated existing tests for the changed UI contexts.

## Automated checks

Runtime observed in this worktree: Node `v22.23.2`, npm `10.9.8`, Next.js `16.2.7`, Vitest `4.1.10`.

| Command | Result | Evidence |
| --- | --- | --- |
| `cd apps/frontend && npm run lint` | exit 0 | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/lint-final.log` |
| `cd apps/frontend && npm run typecheck` | exit 0 | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/typecheck-final.log` |
| `cd apps/frontend && npm test` | exit 0; Node 545/545, component 465/465 across 54 files | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/npm-test-full-rerun.log` |
| `cd apps/frontend && npm run build` | exit 0; production build includes `/`, `/profile`, and `/raw` | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/build-final.log` |
| `cd apps/frontend && python3 ../../scripts/test_frontend_build_config.py` | exit 0; 4 tests | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/build-config-final.log` |
| `cd apps/frontend && python3 ../../scripts/test_frontend_config_artifacts.py` | exit 0; 9 tests | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/config-artifacts-final.log` |
| `cd apps/frontend && npx vitest run tests/lwc-174-workspace-provider.test.tsx tests/lwc-211-profile.test.tsx` | exit 0; 54/54 | `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/focused-vitest.log` |
| `git diff --check` | exit 0; no output | checked after the source changes |

These are the frontend CI gates declared in `.github/workflows/ci.yml`; each completed successfully on the final source snapshot. No source changes were made after the final full test/build checks; only this report and copied evidence were added afterward.

### First-run failures retained

- The first full `npm test` exited 1 with 35 component-test failures (430 passed). The workspace storage-failure fixture expected project B although the provider's existing sort/default behavior selected A; a presentation-memory key leaked between tests; and the Profile test harness lacked the Auth, Workspace, Navigation, and Next router providers required by the new render context. Those fixtures/mocks were corrected; the full rerun above passed. The first-run log is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/npm-test-full.log`.
- Initial lint runs exited nonzero on React Compiler diagnostics, unused directives, and `set-state-in-effect`; the implementation was adjusted and the final lint command passed. Logs: `lint-first.log`, `lint-second.log`, and `lint-third.log` in the same scratch directory.
- Dependency setup needed recovery because there is no root lockfile and the frontend's checked-in lockfile was initially out of sync with `package.json`. Root `npm ci` and the first frontend `npm ci` exited nonzero; `npm install --no-package-lock` hit an npm Arborist error. A temporary lock refresh was restored, then `npm ci` against the restored baseline lock completed successfully (540 packages). The checked-in lockfile has no diff. Logs are `npm-ci.log`, `npm-ci-frontend.log`, `npm-install.log`, `npm-lockgen.log`, and `npm-ci-baseline.log` in the scratch directory.

## Browser evidence and limits

The synthetic browser session used the local Next app at `http://127.0.0.1:3000` and a disposable API fixture at `http://127.0.0.1:8787`, configured by the ignored local file `.build/cac/local/frontend-config.json`. The fixture accepted only the synthetic test account, served in-memory project/Profile/Raw data, and did not contact a real backend or use Owner data. The browser flow was controlled in an isolated Chrome profile.

The coordinator's actual browser inspection observed: help/back preserved `LWC406 Parent synthetic flow` and created zero projects; submitting once created `project-lwc406-synthetic` and routed to its Profile; the valid Profile rendered the optional notice; choosing “先加入資料” navigated to `/raw`. The fixture request log records one `POST /api/v1/init-project`, Profile GETs, and Raw GETs, with no Profile mutation or upload request. The help result is recorded in `/Users/rayer/.hermes/workflows/lwc-local/discussions/create-project-home-ux-r1/main-browser/help-create-result.json`; the full synthetic API log is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/api-fixture-requests.jsonl`.

Screenshots in `evidence/screenshots/` include the isolated worker's Traditional Chinese create/help/name-preservation states and the coordinator's 1280×900 Traditional Chinese create/help/Profile-prompt states. The Profile screenshot also shows the retained Beta navigation entries and the exact notice/action copy.

**Not verified in the browser:** English UI, 375 px layout, full keyboard/focus/overflow behavior, and a successful Raw upload. The inspected `/raw` page lists/previews/downloads files but exposes no add/upload control; the existing uploader is in the Home `PipelineClient`. The approved CTA and frozen scope require navigation to `/raw`, so this checkpoint does not invent a new uploader or silently route elsewhere. Although the local fixture has a synthetic `POST /api/v1/raw/upload` handler that returns a placeholder SHA-256, the UI did not call it and no upload success is claimed. Parent inspection also reported native OS focus-control retries as a harness limitation; there is no browser-level focus PASS claim. Automated component tests cover Home navigation versus search and retained navigation behavior, but do not substitute for these uncompleted browser checks.

The Next dev server (exec session `54903`, port 3000) and API fixture (exec session `50874`, port 8787) remain available for the coordinator's follow-up. The fixture currently has one in-memory synthetic project. Its request log and script are outside the repository under `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/`.
