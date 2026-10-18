# LWC-403 lane C local implementation checkpoint

**Result:** local implementation PASS at baseline `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`; changes remain uncommitted. **Live DEV verification:** NOT RUN; this checkpoint had no cloud/provider/IAM/credential authority.

## Implementation

- Frontend error presenters select a normalized, non-empty response `requestId` first, then fall back to `diagnosticId`; both source fields remain independent on `ApiError` and caller state.
- Query, source/concept list and detail, raw list/preview/download, source annotation, citation dialog, and export error paths now retain, label, and copy the displayed ID consistently. Export errors preserve the actual response header through the custom adapter and panel state.
- English and zh-TW use “Request ID” / “請求 ID”. No backend schema or existing error message text changed; no request panel behavior was changed.

## Verification

All commands ran from `apps/frontend` unless otherwise noted.

| Command | Result |
| --- | --- |
| `npm run test:component -- --run tests/lwc-403-request-id-errors.test.tsx tests/lwc-403-export-request-id.test.tsx tests/lwc-345-export-api.test.tsx tests/lwc-396-pr130-owned-presenters.test.tsx tests/lwc-396-pr130-diagnostic-consumers.test.tsx tests/lwc-248-home-search-submission-behavior.test.tsx` | PASS, 6 files / 37 tests |
| `env -u NODE_ENV npm run test:node` | PASS, 545 passed / 0 failed; full output: `/tmp/lwc403-lane-c-test-node.log` |
| `npm run lint` | PASS, exit 0, no warnings |
| `npm run typecheck` | PASS, exit 0 |
| `npm run build` | PASS, exit 0; Next.js 16.2.7 webpack build completed |
| `git diff --check` | PASS, exit 0 |

`npm ci` installed the locked dependencies without changing the lockfile; npm reported 17 audit advisories (3 moderate, 13 high, 1 critical). No dependency updates were made.

## Changed files

- `apps/frontend/src/components/DetailClient.tsx`
- `apps/frontend/src/components/ExportPanel.tsx`
- `apps/frontend/src/components/HomeClient.tsx`
- `apps/frontend/src/components/ListClient.tsx`
- `apps/frontend/src/components/RawClient.tsx`
- `apps/frontend/src/components/SourceAnnotationEditor.tsx`
- `apps/frontend/src/components/SourceListClient.tsx`
- `apps/frontend/src/components/States.tsx`
- `apps/frontend/src/lib/api.ts`
- `apps/frontend/src/lib/export-api.ts`
- `apps/frontend/src/messages/en.json`
- `apps/frontend/src/messages/zh-TW.json`
- `apps/frontend/tests/api.test.mjs`
- `apps/frontend/tests/lwc-248-home-search-submission-behavior.test.tsx`
- `apps/frontend/tests/lwc-345-export-api.test.tsx`
- `apps/frontend/tests/lwc-396-pr130-diagnostic-consumers.test.tsx`
- `apps/frontend/tests/lwc-396-pr130-owned-presenters.test.tsx`
- `apps/frontend/tests/lwc-403-export-request-id.test.tsx`
- `apps/frontend/tests/lwc-403-request-id-errors.test.tsx`
- `docs/lwc-403/evidence/lane-c-checkpoint.md`

No commit, push, PR, merge, or deployment was performed. Main review and any subsequent lane work remain with the coordinator.
