# LWC-404 lane D local implementation checkpoint

Baseline: `4e1d6cb3cc89548ca7dc49924d95c64ddc97c723`  
Status: local implementation and evidence ready for Main review; source remains uncommitted. This is not ticket verification, merge, PR publication, or deployment.

## Implementation

Added localized `Beta` badges to the shared Agent & MCP and CLI navigation entries, the Agent & MCP page heading, the Project API keys subsection heading, and the CLI page heading. The Project parent heading is unchanged. The CLI intro now explains raw/ sync, returning to Web to compile before searching, and that the CLI does not read content from other folders. No route, authentication, key lifecycle, CLI binary, or drawer behavior was changed.

The locale values are in `apps/frontend/src/messages/en.json` and `apps/frontend/src/messages/zh-TW.json`. Other lane C edits also exist in those shared locale files; they remain separate and uncommitted.

## Local render evidence

Rendered the actual local Next app at 375x812 and 1280x812 in English and Traditional Chinese using a synthetic non-Demo owner/project and local-only auth/data fixtures. The fixture used `127.0.0.1:3101`; the screenshot receiver used `127.0.0.1:3102`. The eight full-page Chrome captures below show the headings, badges, CLI intro and steps, and Project keys subsection with its details open. For all eight page renders, `document.documentElement.scrollWidth` equaled the viewport width; the intro followed the page heading in DOM/render order. At 1280px the shared sidebar was sticky at x=0; at 375px it was closed off-canvas at x=-256.

| Locale | Width | Agent & MCP / Project keys | CLI |
|---|---:|---|---|
| English | 375 | [screenshot](screenshots/agent-mcp-project-keys-en-chrome-375.png) | [screenshot](screenshots/cli-en-chrome-375.png) |
| English | 1280 | [screenshot](screenshots/agent-mcp-project-keys-en-chrome-1280.png) | [screenshot](screenshots/cli-en-chrome-1280.png) |
| zh-TW | 375 | [screenshot](screenshots/agent-mcp-project-keys-zh-tw-chrome-375.png) | [screenshot](screenshots/cli-zh-tw-chrome-375.png) |
| zh-TW | 1280 | [screenshot](screenshots/agent-mcp-project-keys-zh-tw-chrome-1280.png) | [screenshot](screenshots/cli-zh-tw-chrome-1280.png) |

Expanded mobile navigation screenshots: [English at 375px](screenshots/mobile-nav-en-open-chrome-375.png), [zh-TW at 375px](screenshots/mobile-nav-zh-tw-open-chrome-375.png). On both, the expanded drawer had `aria-expanded=true`, `translate: 0px`, x=0, width 256px, and document width 375px. The Beta labels are visible beside Agent & MCP and CLI.

Interaction checks on the same local synthetic fixture passed: header toggle closes after settling (aria-expanded false, drawer x=-256); backdrop click closes; Escape closes; selecting CLI setup navigates to `/cli` and closes the drawer. Desktop sidebar remains visible at 1280px. The visible links were not obstructed by the badges.

The earlier Safari MCP captures remain as failure evidence: [English](screenshots/mobile-nav-en-safari-missing-drawer-375.png) and [zh-TW](screenshots/mobile-nav-zh-tw-safari-missing-drawer-375.png). In that surface, the click reported `aria-expanded=true` and class `translate-x-0`, while computed `translate` remained `-100%` and the aside x-coordinate remained -256. Chrome reproduced the same app state correctly on the same fixture, with `translate: 0px` and x=0. This browser-surface discrepancy is unresolved at its root; the required expanded mobile render is verified using the Chrome captures above. No drawer source fix was needed or made.

## Checks

Commands run from `apps/frontend`:

- `npm run test:component -- tests/lwc-174-shell.test.tsx tests/cli-lane.test.tsx tests/lwc-377-project-keys.test.tsx tests/lwc-404-beta-heading.test.tsx` — passed, 4 files / 31 tests.
- `npm run lint` — passed.
- `npm run typecheck` — passed.
- `npm run build` — passed with Next.js 16.2.7; routes compiled and generated.
- `git diff --check` — passed.

The temporary seed page was removed and the local fixture/screenshot server processes were stopped after capture; their scratch scripts remain outside the repository. All observed app API traffic was to the local fixture. Live DEV validation: **NOT RUN**. No production, cloud, provider, IAM, credential, or Owner corpus calls were made.

## Changed-file scope

Lane D source/test paths:

- `apps/frontend/src/app/(workspace)/advanced/page.tsx`
- `apps/frontend/src/components/CLILaneClient.tsx`
- `apps/frontend/src/components/ProjectKeysSection.tsx`
- `apps/frontend/src/components/Shell.tsx`
- `apps/frontend/src/messages/en.json` and `apps/frontend/src/messages/zh-TW.json` (shared with lane C)
- `apps/frontend/tests/cli-lane.test.tsx`
- `apps/frontend/tests/lwc-174-shell.test.tsx`
- `apps/frontend/tests/lwc-377-project-keys.test.tsx`
- `apps/frontend/tests/lwc-404-beta-heading.test.tsx`
- `docs/lwc-404/evidence/screenshots/` (render and interaction captures linked above, plus retained initial screenshots and Safari failure captures)

The same working tree also contains separate lane C changes and evidence, including `docs/lwc-403/evidence/lane-c-checkpoint.md`; they were preserved and not repaired or reclassified during D. Main reported lane C review is on HOLD for an ExportPanel translation-function identity regression and owns that source review and follow-up. The focused D results do not constitute a lane C pass. The complete combined working-tree path list is saved in `docs/lwc-404/evidence/changed-files.txt`.
