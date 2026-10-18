# LWC-403 lane C remediation checkpoint

**Status:** local regression remediation PASS; source remains uncommitted at baseline `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`. This is a new local checkpoint for review, not ticket completion, merge, publication, or deployment. **Live DEV verification: NOT RUN** under this task's authority.

The new C-only 20-file source snapshot is `/Users/rayer/.hermes/workflows/lwc-local/evidence/request-id-closeout-r2/lwc403-remediation-r1/c-source-snapshot.tar.gz` (SHA-256 `66132355df2ea74b20f5590df22e8a48c95d344c521e26a019bf86ac8132b06e`), with manifest `/Users/rayer/.hermes/workflows/lwc-local/evidence/request-id-closeout-r2/lwc403-remediation-r1/c-source-manifest.json` (SHA-256 `f803e921dd926a01cba8e726d2111629a875ee07947fe68443e519b38803393a`). All 20 file paths, byte counts, modes, and hashes were verified against the snapshot; `snapshot-delta.txt` confirms its only difference from the prior C source packet is the stable translation fixture change.

## Finding and change

The prior C review found that `tests/lwc-125-raw-multi-upload-progress.test.tsx` supplied a new `t` function on every render, while production `useT` memoizes that function by locale. `PipelineClient` renders `ExportPanel`; its refresh effect depends on `t`, and its new structured error state creates a fresh object after each failed refresh. With the unstable test fixture, that sequence repeatedly refreshes and prevents the upload tests from settling. The exact prior HOLD and evidence remain in `/Users/rayer/.hermes/workflows/lwc-local/discussions/otel-group-r1/request-id-closeout-r2/C-local-review-result.json`.

The test fixture now reuses a stable translator function from its hoisted mocks, matching production identity. Both original upload tests and all assertions remain unchanged; timeout settings are unchanged. No application source, upload concurrency, progress, retry behavior, D UX, dependencies, or lockfiles changed in this remediation.

## Verification

Commands ran from `apps/frontend`, except `git diff --check` from the repository root. Logs and exit-code records are under `/Users/rayer/.hermes/workflows/lwc-local/evidence/request-id-closeout-r2/lwc403-remediation-r1/`.

| Command | Result |
| --- | --- |
| `npm run test:component -- --run tests/lwc-125-raw-multi-upload-progress.test.tsx` | PASS, 1 file / 2 tests |
| `npm run test:component -- --run tests/lwc-403-request-id-errors.test.tsx tests/lwc-403-export-request-id.test.tsx tests/lwc-345-export-api.test.tsx tests/lwc-396-pr130-owned-presenters.test.tsx tests/lwc-396-pr130-diagnostic-consumers.test.tsx tests/lwc-248-home-search-submission-behavior.test.tsx` | PASS, 6 files / 37 tests |
| `env -u NODE_ENV npm test` | PASS, Node 545/545 and component 444/444 across 52 files |
| `npm run lint` | PASS, exit 0 |
| `npm run typecheck` | PASS, exit 0 |
| `npm run build` | PASS, Next.js 16.2.7 webpack build completed |
| `git diff --check` | PASS, exit 0 |

The full component run printed two existing jsdom navigation notices (`Not implemented: navigation to another Document`); the suite still passed. The earlier immutable candidate failure logs and results were preserved unchanged under `/Users/rayer/.hermes/workflows/lwc-local/evidence/request-id-closeout-r2/lwc403-candidate-r1/`; they still record the original 2 timeouts and the exact baseline's 2/2 pass. This passing remediation run does not rewrite or transfer the old immutable packet's HOLD. Main review of the new local source snapshot is pending.

## Separate lane D evidence

Lane D source and screenshots were not edited during this remediation. Its prior local checkpoint is [lane-d-checkpoint.md](../../lwc-404/evidence/lane-d-checkpoint.md): Chrome local synthetic-fixture renders passed at 375px and 1280px for both locales, including expanded mobile navigation. The report separately retains Safari mobile captures where the drawer remained off-canvas despite its expanded state; that browser-surface discrepancy remains unresolved and is not evidence about this C remediation. D results are not reclassified here.

## Changed files

This remediation's source delta is only `apps/frontend/tests/lwc-125-raw-multi-upload-progress.test.tsx`. The C checkpoint's full scoped paths are listed in [lane-c-remediation-r1-changed-files.txt](lane-c-remediation-r1-changed-files.txt); the other pre-existing D files remain in the shared worktree, uncommitted.
