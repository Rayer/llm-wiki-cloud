# LWC-406 r3 local implementation checkpoint

Date: 2026-10-18
Branch: Rayer/LWC-406-project-home-ux
Baseline: ce6bf981b32f3fbb6d64383002ab1ed33e9c4e06
Scope: local source, automated checks, and synthetic fixture only. No commit or push was made.

## R3 decision and implementation

Owner selected “保留前往 Raw，將現有上傳操作復用到 Raw 頁”. The r2 base implementation and its historical report remain in implementation-report.md; this r3 checkpoint adds a working Raw upload control while retaining the existing Home uploader.

- Added RawUploadPanel as the shared upload queue. PipelineClient still renders it in the Home Add Content tile with the existing scrape and Pipeline controls. RawClient renders the same component for a selected project and refreshes the Raw list after an upload result, so newly uploaded files can be listed, previewed, and downloaded on Raw.
- Kept the existing uploadRawFile API client and its multipart, project/auth headers, request-ID tracing, and server response handling. Queue progress, three concurrent uploads, multi-file selection, duplicate handling, per-file errors/retry, demo blocking, and Home quota/navigation refresh behavior remain on the shared path. The frontend still does not offer overwrite; a conflicting filename keeps the existing 409 error/retry behavior. Raw does not trigger Pipeline or scraping.
- Keyed the uploader by project and session token so stale callbacks cannot update a newly selected workspace. The Raw list refresh and preview remain scoped by the current project/session.
- Added English and Traditional Chinese upload heading/hint copy. It explains that uploads remain pending until Pipeline is run from Home and does not imply automatic compilation or Profile save.
- Added Raw upload component tests and updated existing static tests to recognize the shared component.

The r2 report records the accepted base behavior: name-only project creation, Profile onboarding presentation fencing and dismissal, help/name preservation, storage-failure recovery, Profile save/dirty-navigation guards, retained Advanced/Beta navigation, and Home navigation distinct from search commands. Those changes remain present in this r3 source snapshot.

## Upload contract and isolated fixture

The existing backend contract was traced in the BFF raw upload/list handlers and the Raw status model. Upload uses POST /api/v1/raw/upload with multipart field file and project/auth headers; list uses GET /api/v1/raw and preview uses GET /api/v1/raw/:filename?preview=true. Upload responses include filename, path, bytes, sha256, and status; list responses include files with name, size, updated, sha256, and ingested. Existing BFF limits include a 5 MiB file maximum and the supported text/document suffix list; the client retains the current no-overwrite semantics.

The disposable fixture is outside the repository at /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/api-fixture.mjs. It binds only to 127.0.0.1:8787, accepts the synthetic qa-owner@example.test account, and keeps project and uploaded filename/bytes/content/digest in memory. It computes SHA-256 from uploaded bytes and serves the actual list and preview response shapes, as well as duplicate, 409 conflict, and explicit overwrite responses. It makes no provider calls and returns no fabricated provider output. The old r2 fixture is preserved as api-fixture-r2-original.mjs with its original request log; the r3 request log is api-fixture-r3-requests.jsonl.

Current QA endpoints:
- App: http://127.0.0.1:3000
- Raw page: http://127.0.0.1:3000/raw
- Served config: http://127.0.0.1:3000/frontend-config.json; api_url and auth_url both point to http://127.0.0.1:8787
- Fixture state: GET http://127.0.0.1:8787/__qa/state
- Reset: POST http://127.0.0.1:8787/__qa/reset (loopback fixture control)

The ignored local config is .build/cac/local/frontend-config.json. At the last health check, /, /raw, /frontend-config.json, and /__qa/state all returned HTTP 200; fixture state was empty (projects: [], rawFiles: []). The Next dev server is Orca exec session 54903, PID 15428, port 3000. The fixture is session 72068, PID 41893, port 8787. Both processes remain running for TPM QA.

Fixture contract verification was run with:
node /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/verify-r3-fixture.mjs
It exited 0 and verified create/login, upload, computed digest, list, preview bytes, idempotent duplicate, 409 conflict, explicit overwrite, and reset. The script is preserved beside the fixture. This is a controlled local mock only; it does not prove compatibility with a deployed backend.

## Automated verification

Observed project runtime: Node v22.23.2, npm 10.9.8, Next.js 16.2.7, Vitest 4.1.10.

| Command from apps/frontend | Result | Evidence |
| --- | --- | --- |
| env -u LLM_API_KEY -u DEEPSEEK_API_KEY -u SYNTO_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u TYPESAFE_API_KEY -u TYPESAFE_JEV_API_KEY -u LWC331_TEST_API_KEY npm test | exit 0; Node 545/545, component 469/469 across 55 files | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-npm-test-final.log |
| npm run lint | exit 0 | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-lint-second-fix.log |
| npm run typecheck | exit 0 when run alone after build | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-typecheck-final-isolated.log |
| npm run build | exit 0; production build includes /, /profile, and /raw | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-build-final.log |
| python3 ../../scripts/test_frontend_build_config.py | exit 0; 4 tests | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-build-config.log |
| python3 ../../scripts/test_frontend_config_artifacts.py | exit 0; 9 tests | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-config-artifacts.log |
| npx vitest run tests/lwc-406-raw-upload.test.tsx tests/lwc-125-raw-multi-upload-progress.test.tsx tests/lwc-396-pr130-original-raw-race.test.tsx tests/lwc-390-391-project-context.test.tsx tests/lwc-391-owner-recovery.test.tsx tests/lwc-391-full-recompile.test.tsx | exit 0; 23 tests across 6 files | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-targeted-vitest.log |
| node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/lwc-142-146-demo-surface.test.mjs tests/lwc-77-raw-files.test.mjs tests/lwc-125-raw-multi-upload.test.mjs tests/lwc-129-nav-counts-refresh.test.mjs tests/lwc-135-demo-session.test.mjs tests/lwc-138-pipeline-quota.test.mjs | exit 0; 31 tests | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-targeted-node-final.log |
| git diff --check | exit 0; no whitespace errors | checked after source changes |

The new upload component tests cover upload → Raw list refresh and content preview without a Pipeline call, 409 conflict and retry, project/session switch fencing for a late upload result, demo blocking, and absence of the upload control without a current project.

### First failures retained

- The first complete npm test run exited 1 on two stale source-shape assertions: one expected Pipeline.select to remain in PipelineClient after extraction, and one expected getRawFiles inline in the effect after it was factored into refreshRawFiles. Those tests were updated to assert the shared uploader and the scoped list-refresh behavior. The full rerun passed without removing or weakening the behavior regressions. First-run log: /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r3-npm-test-full.log.
- Initial lint runs flagged render-time ref updates in PipelineClient and synchronous list state updates in a RawClient effect. The project-generation ref update now runs in a layout effect; the first list request is deferred and cancellable. Final lint passed. Initial logs: r3-lint-initial.log and r3-lint-second.log in the same scratch directory; final log is listed above.
- A typecheck run overlapped with next build and exited 2 while .next/types was being regenerated. This was a generated-output race from parallel checks; the isolated final npm run typecheck completed with exit 0. The overlapping run is preserved as r3-typecheck-final.log.

## Browser work assigned to TPM

This worker did not perform the r3 browser matrix or capture r3 screenshots. TPM owns the actual product UI checks and pixel review. Browser acceptance remains pending for zh-TW/en at 375 px and 1280 px, including one complete synthetic create → Profile → skip → Raw upload → list/content preview flow; the zero Profile-save / zero duplicate-create assertions; and keyboard/focus, overflow, navigation, Advanced/Beta retention, and search-vs-Home checks. The new local fixture is ready for that run. Existing r2 browser evidence remains documented in implementation-report.md, but it does not claim the newly added Raw upload flow.

No source changed after the passing final test/lint/typecheck/build checks; only this report was added after those checks. Parent source capture, independent review, and TPM browser acceptance remain outstanding.
