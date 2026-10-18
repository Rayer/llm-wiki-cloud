# LWC-406 r4 narrow localization repair

Date: 2026-10-18
Branch: Rayer/LWC-406-project-home-ux
Scope: narrow R3 repair only; no commit or push.

## Finding and fix

The coordinator's actual browser QA found that the empty upload filename label rendered the literal key Pipeline.selectFiles on both Home and Raw. In the captured R3 source snapshot, RawUploadPanel.tsx used t('Pipeline.selectFiles') for the initial label and when the file selection is cleared, while neither locale catalog defined that key. The immutable review snapshot remains at /Users/rayer/.hermes/workflows/lwc-local/evidence/lwc406/r3-local-checkpoint.

Added the missing Pipeline.selectFiles string to the real English and zh-TW catalogs: “Select files” and “選擇檔案”. No component behavior, markup, upload behavior, or other locale content was changed.

Added tests/lwc-406-raw-upload-locales.test.tsx. It renders the actual RawUploadPanel consumer and actual useT implementation against both real JSON catalogs, then asserts the visible empty file label is localized. The test would fail with the prior missing key because translate returns the key itself for an undefined path.

## Browser QA context

The coordinator's R3 actual-browser matrix exercised English and Traditional Chinese at 375 px and 1280 px, including create → help/name preservation → Profile optional notice → skip → Raw upload → list/readback and preview; it confirmed the uploaded file's 138-byte size and digest, no overflow, and the keyboard Enter/help/focus-back checks. That run surfaced the literal file label recorded above. This worker did not rerun browser automation after the catalog-only fix; the updated locale strings have automated consumer coverage.

## Verification

Observed frontend runtime from the prior r3 checkpoint: Node v22.23.2, npm 10.9.8, Next.js 16.2.7, Vitest 4.1.10.

| Command from apps/frontend | Result | Evidence |
| --- | --- | --- |
| NODE_ENV=test ./node_modules/.bin/vitest run tests/lwc-406-raw-upload-locales.test.tsx tests/lwc-406-raw-upload.test.tsx | exit 0; 6 tests across 2 files | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r4-focused-vitest.log |
| NODE_ENV=test npm test | exit 0; Node 545/545 and component 471/471 across 56 files | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r4-npm-test.log |
| npm run lint | exit 0 | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r4-lint.log |
| npm run typecheck | exit 0; run after lint and before build, not concurrently | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r4-typecheck.log |
| npm run build | exit 0; /, /profile, and /raw generated | /Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc406/r4-build.log |

The standalone Python frontend configuration checks were not rerun for this locale-key-only change; they passed in the r3 checkpoint. No source edits were made after these r4 checks. The prior r2 and r3 reports and immutable source snapshots were left untouched. Parent independent whole-source review remains on the captured r3 snapshot; this report records only the narrow r4 working-tree delta.
