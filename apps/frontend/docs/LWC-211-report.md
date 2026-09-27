# LWC-211 Profile UI report

## Delivered

- Added the Project Profile panel to authenticated Project Home, keyed by user and project. It loads the current Profile, preserves ordered requirement IDs and text, and sends the matching project in both the route and `X-Project-ID` header.
- Added optimistic revision writes for requirement save, candidate confirmation, candidate tagging retry, and failed derivation retry. Conflicts and unconfirmed writes keep the local draft and refresh only the current project. Late responses from a previous project, account, session, or modal lifetime cannot update the visible workspace.
- Kept user requirements separate from dictionary and guidance diffs. Manual candidates require an explicit confirmation; compile-auto candidates render as Tags-only. The active pointer remains visible while a newer candidate waits for confirmation or tagging.
- Displays the server's three-minute schedule and makes clear that saving requirements does not complete derivation. It polls pending derivation and exact job status, retries after transient Profile read failures, and announces a candidate activation once.
- Added optional Profile requirements to project creation. An untouched all-blank form creates the project without scheduling Profile work. If the project is created but Profile save is not confirmed, the draft remains and retry reuses that project after reading current state.
- Wired Recompile all to the server capability endpoint. It stays disabled when capability is denied or unavailable, explains possible BYOK model costs, and posts only to the dedicated recompile route when the server says it is allowed.

## Verification

Commands run from `apps/frontend`:

| Command | Result |
| --- | --- |
| `node --experimental-strip-types --test tests/lwc-211-profile-api.test.mjs tests/project-switching.test.mjs` | Passed, 9 tests. |
| `npm run test:component -- tests/lwc-216-citation-preview-modal.test.tsx tests/lwc-211-profile.test.tsx tests/lwc-211-new-project.test.tsx` | Passed, 30 tests. |
| `npm test` | Passed, 521 Node tests and 245 component tests. |
| `npm run lint` | Passed. |
| `npm run typecheck` | Passed. |
| `npm run build` | Passed. |
| `git diff --check -- apps/frontend` | Passed. |
| `curl -sS -o /dev/null -w 'HTTP %{http_code}\n' http://localhost:3000/` | Returned HTTP 200. |

The local app is running at [http://localhost:3000](http://localhost:3000) from `npm run dev -- --hostname 0.0.0.0` for parent Safari acceptance.

API route/header tests stub `fetch`; component state-transition tests use mocked Profile and job responses. No real BFF-to-Firestore Profile request, provider output, DEV acceptance, or DEV UAT was exercised.

## Follow-on — first-compile bootstrap guidance

- Added typed GET and confirm calls for `/profile/bootstrap-guidance`; both success responses use `{ "bootstrap_guidance": null | object }`. The shared BFF handler and contract test confirm the exact response wrapper in `apps/bff/internal/handler/v1/profile_bootstrap.go` and `apps/bff/internal/handler/v1/profile_test.go`.
- Added a separate first-compile guidance preview for nonempty Profiles without a generation-bound active Profile. It shows the guidance diff and one disposition/explanation row for each requirement, requires explicit confirmation, and labels confirmed guidance as ready for the first compile rather than active.
- Kept the empty Profile path unchanged. A stale Profile revision or unsaved requirements draft cannot be confirmed; a 409 refreshes the server state and guidance while keeping the requirements draft. A successful requirements save clears the old preview and reads the superseding state.
- Pending derivation polls both Profile and bootstrap guidance and reuses the existing derivation retry. Project scopes and the existing account-keyed remount guard late reads and confirmation responses; candidate tagging, the generation-bound active tuple, and Recompile all gating remain unchanged.

### Follow-on verification

Commands run from `apps/frontend`:

| Command | Result |
| --- | --- |
| `node --experimental-strip-types --test tests/lwc-211-profile-api.test.mjs` | Passed, 5 tests. |
| `npm run test:component -- tests/lwc-211-profile.test.tsx` | Passed, 21 tests. |
| `npm test` | Passed, 522 Node tests and 252 component tests. |
| `npm run lint` | Passed. |
| `npm run typecheck` | Passed after the production build generated `.next/types`; the concurrent lint/typecheck/build attempt raced Next's `.next` regeneration. |
| `npm run build` | Passed. |

No authenticated browser session or credentials were available for a profile-panel browser check. Component rendering tests cover the preview, confirmation, pending poll, stale response, and account/project switch states; no live or fabricated E2E was used.

## Follow-on — confirmation/readback race hardening

- Reproduced the stale-read race first: a pending same-revision bootstrap GET resolved after a successful confirmation and replaced the confirmed guidance UI with the preview state. On successful confirmation, the panel now invalidates outstanding bootstrap reads and clears their loading state before displaying the confirmed artifact; the guidance region exposes that loading state with `aria-busy`.
- The 409 path now says the latest Profile was reloaded only after a matching project read succeeds. If readback fails or returns the wrong project, the message says the current server state could not be confirmed and keeps the local requirements draft. Existing project/account switch guards and retry behavior remain covered.

### Follow-on verification

Commands run sequentially from `apps/frontend`:

| Command | Result |
| --- | --- |
| `npm run test:component -- tests/lwc-211-profile.test.tsx -t 'keeps confirmation after a polling GET resolves late with the same-revision preview'` (before the fix) | Expected RED: 1 regression test failed because the late same-revision preview replaced the confirmed state. |
| `npm run test:component -- tests/lwc-211-profile.test.tsx` (after the fix) | Passed, 24 tests, including late poll response/loading, retry after failed confirmation, and existing project/account switch cases. |
| `node --experimental-strip-types --test tests/lwc-211-profile-api.test.mjs` | Passed, 5 tests. |
| `npm run lint` | Passed. |
| `npm run typecheck` | Passed. |

No build was needed for this request-ordering and status-message change after typecheck passed. This is a bounded component/API test slice; no browser E2E or authenticated live Profile request was performed. Submitted to the parent for review.
