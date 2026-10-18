# LWC-406 local implementation checkpoint r5

**Checkpoint:** source-stable local repair; no commit created. The worktree is on `Rayer/LWC-406-project-home-ux` at base `ce6bf981b32f3fbb6d64383002ab1ed33e9c4e06`, with the accepted r2/r3 work and r4 label fix preserved. Parent capture and whole-source review of this new revision, plus TPM browser recheck, are pending.

## Repair summary

### F1: selected project identity under storage failure

The API helpers and all relevant UI consumers now carry the current in-memory project ID through Raw upload, list, preview, Source list/preview, and navigation-count refresh. `RawUploadPanel` captures the selected project ID when it starts each upload; `RawClient` and `SourceListClient` pass it to their real API helpers. The initial and manual status requests use the current project ID and reject late results when project, token, or session epoch changes.

The pre-repair storage probe reproduced both failure modes. With `currentProject` already set to B and persistence to localStorage failing, list and upload sent `X-Project-ID: project-a`; when reading the remembered-project key threw, list and upload failed with `storage read blocked`. The adapted, original-preserving probe and output are in `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r5-storage-probe.mjs` and `r5-storage-probe-before.log`.

`lwc-406-raw-project-context.test.tsx` uses the real `WorkspaceProvider`, Raw page client, API helpers, and intercepted fetch/XHR transport. It verifies create-once/select-B when storage writes fail, successful upload/list/preview/count refresh headers for B, continued operation when reading the remembered ID throws, and that late Raw-list and nav-count responses from B do not overwrite A after switching back. The test creates no real project or backend mutation.

### F2: Profile dirty-navigation guard

`ProjectProfilePanel` now registers `hasUnsavedChanges` with the real `NavigationBlockerProvider`; the effect clears the block when the draft is saved, project/session identity changes, or the panel unmounts. The reconstructed integration test uses the actual provider and panel and covers unchanged-draft navigation without a prompt, cancel preserving the edited draft on Profile, and confirmed navigation to `/raw`.

The pre-repair integration run is preserved in `r5-dirty-blocker-repro.log`: the unchanged case passed, while both edited-draft cases observed zero calls to the provider’s real `window.confirm` path.

## r5 supporting test updates

- Updated the existing RawClient static test to assert explicit project-ID forwarding and the captured project ID dependency.
- Updated the existing shared-upload test expectations for explicit upload and preview project IDs.
- Updated Profile test-only `useNavigationBlocker` mocks to provide the newly used `setBlocked` API. The real dirty-guard behavior remains covered by the integration test above.
- The first full test attempts and first lint failure remain preserved in scratch logs. The first Node failure was a stale `getRawFiles()` source assertion; the next component run exposed missing `setBlocked` in two old test mocks; the first lint run found React Compiler dependency precision errors. These were corrected without skipping tests or weakening assertions.

## Verification

Runtime: Node `v22.23.2`, npm `10.9.8`, Next.js `16.2.7`, Vitest `4.1.10`.

| Command | Exit | Result and log |
|---|---:|---|
| `NODE_ENV=test ./node_modules/.bin/vitest run tests/lwc-406-raw-project-context.test.tsx tests/lwc-406-profile-dirty-blocker.test.tsx tests/lwc-406-raw-upload.test.tsx tests/lwc-211-profile.test.tsx tests/lwc-406-profile-onboarding.test.tsx` | 0 | 5 files, 56 tests passed; `r5-targeted-final2.log` |
| `node --experimental-strip-types --import ./tests/runtime-config-test-setup.mjs --test tests/lwc-77-raw-files.test.mjs tests/lwc-129-nav-counts-refresh.test.mjs` | 0 | 7 tests passed; `r5-node-focused-final.log` |
| `NODE_ENV=test npm test` | 0 | Node: 545 passed, 0 failed; component: 58 files / 478 tests passed; `r5-full-npm-test-source-final.log` |
| `npm run lint` | 0 | Clean; `r5-lint-source-final.log` |
| `npm run typecheck` | 0 | Clean; `r5-typecheck.log` |
| `npm run build` | 0 | Next production build compiled, generated all 18 static pages, and collected traces; `r5-build.log` |
| `git diff --check` | 0 | Clean |

All full-test, lint, typecheck, and build commands ran sequentially. The full test command was repeated after the source and test updates; its final run has no failures.

## Local QA server ownership and remaining checks

The existing local QA processes were left running and were not restarted or modified by this repair. PID `15427` runs Next dev at `http://127.0.0.1:3000` (child Next server PID `15428`); `/frontend-config.json` points both `api_url` and `auth_url` at `http://127.0.0.1:8787`. PID `41893` runs the synthetic in-memory fixture `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/api-fixture.mjs`; `/health` responds at port 8787. The fixture is synthetic and does not access Owner data or a live backend.

This r5 repair did not perform browser automation or create screenshots. TPM’s browser recheck on the new source and the parent’s immutable full-source review remain pending. Existing r3/r4 reports, screenshots, and source snapshots were not rewritten.
