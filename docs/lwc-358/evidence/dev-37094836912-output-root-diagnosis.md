# DEV 37094836912 Frontend output-root diagnosis

This is the bounded offline diagnosis and accepted local repair for the prepare cwd/root mismatch. It does not attribute the historical live failure to a proven CLI branch.

## Run evidence

- Run `37094836912` used plan source `244d0353d019e1af240341631913a515bb00d864`, release tag `dev-lwc-366-244d0353d019`, and configured Frontend root `apps/frontend`. The plan's engine content fingerprint is `cdc34bd8c2a45556cd36d2e63a9381b2088cd7061e68ac318fe5f781ebefa7d3`.
- The final result is `frontend-output-missing` at `prepared`, checkpoint 1. Mutation is false, runtime did not start, and no Frontend build receipt was written. The retained Auth receipt was byte-identical and Auth was not rebuilt (Parent's reconciliation).
- The four recorded prepare commands (`frontend-project-readback`, `frontend-npm-ci`, `frontend-vercel-pull`, `frontend-vercel-build`) each returned exit 0 with no timeout. The selected Vercel org/project/team context was present; legacy `NOW_*` context was absent. The API-linked root and pulled settings root both matched the configured root; metadata readback did not fail.
- After the build, `ROOT/apps/frontend/.vercel/output` was absent. `ROOT/.vercel/output` existed, but its static build config was invalid and did not equal the requested target. These are post-command presence/validation facts; the artifact does not establish that Vercel created the root-level directory during this run or that it contains the selected build.

## Source findings

The local source at reviewed PR80 head `77733ab480295b64fbe5e0130f81321c5df93806` has tree `ff16671166afbf049d71dc7621ba4f451c24d464`, matching the merged tree reported for run source `244d0353d019e1af240341631913a515bb00d864`.

At reviewed PR80 source, `Providers.prepare` installed dependencies from `ROOT/apps/frontend` but ran pull/build from `ROOT`. It accepted only `ROOT/apps/frontend/.vercel/output/static/build-config.json` and archived the project link from `ROOT/.vercel/project.json`. The local repair now derives `project_root = ROOT / cfg['root_directory']`, runs npm/pull/build from that validated directory, reads the pulled settings there, and archives its output/link under the same canonical `.vercel/output/...` and `.vercel/project.json` artifact names. Vercel argv, target, selected org/project/team context, strict config equality, runtime `--prebuilt` behavior, and the no-fallback rule are unchanged.

The earlier bounded audit of the cached `vercel@59.11.7` source (tarball SHA256 `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`) established these branches in `dev-37083625975-build-config-path-diagnosis.md` and `dev-37089775677-output-diagnosis.md`:

- With the adapter's env-linked project context, `getLinkedProject` has no repo root or project-root directory. Pull writes `.vercel/project.json` at its cwd.
- Build output resolution is separate. The pinned build resolver considers cwd, link location, settings root, and ancestor workspace claims; when its link location is empty it returns an empty per-directory root before applying `settings.rootDirectory`. The default output is `.vercel/output` beneath the resulting root. The exact workspace-claim branch selected on this runner is not in the retained run artifact.
- The checked-in app's `package.json` builds with `next build --webpack`. Its `/build-config.json` route is explicitly static and reads the planned public API/Auth URLs at build time. The retained real Next build trace shows this route is prerendered. The Vercel adapter's static config validator still must see the exact target config in the configured output before archiving.

The cwd/root mismatch is a source-backed candidate explanation for the missing configured-root output. The actual resolver branch selected on the historical runner and the provenance of its invalid repo-root directory remain unobserved. Exit 0 alone does not prove a correctly selected app build.

## Accepted local repair and source-backed regression

The regression adds the exact cached `vercel@59.11.7` per-directory workspace/root resolver body to `deploy/engine/tests/fixtures/vercel-59.11.7-build-root.js` (file SHA256 `eac3775d0a261ec4508bfe0e83b6d82c5fe21b0873cf354d95f8ddae9b9283f3`; its test hashes the body without the final newline as `0717a88d30e4a55ac51f0bfa4d12df87d9ded9c7416d9b8ec9b57ee2aee3ddb7`). The source is from the cached tarball SHA256 listed above. Existing exact `getLinkedProject`, `ensureLink`, pull command, and project-settings writer fixtures are exercised together with it by `test_vercel_env_context.js`.

The checked-in checkout is asserted to have no root `package.json` or `pnpm-workspace.yaml`; the fixture copies the actual Frontend package manifest. In this branch, pulling from the project cwd writes `.vercel/project.json` there and the exact pinned resolver places default output at `apps/frontend/.vercel/output`; the same resolver from repo cwd selects repo-root `.vercel/output`. A second fixture adds a claiming ancestor npm workspace entry (`apps/frontend`): the resolver selects the workspace root and resolves `apps/frontend` once, avoiding `apps/frontend/apps/frontend`. It still places output beneath the configured project directory.

The offline matcher only accepts literal workspace pattern equality, and the pnpm YAML stub throws if reached; wildcard matching and pnpm resolution are outside this regression. The provider fake asks the pinned resolver seam to select the output directory before writing its test build output, rather than hardcoding the output root. Real `Engine.main`/`Providers.prepare` tests then verify both workspace cases produce a valid canonical archive and ready result, retain the Auth receipt byte-for-byte without rebuilding it, and consume the archive through the unchanged runtime path. Missing output, missing config, malformed pulled settings, and wrong target remain fail-closed; a valid repo-root decoy cannot satisfy prepare.

## Verification

All commands used Python 3.14.6, explicit `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`, and `PYTHONDONTWRITEBYTECODE=1`; no Vercel CLI, API, provider, npm network/install, or Actions operation was run.

| Exact command (cwd `deploy/engine/tests`) | Result | Evidence |
|---|---|---|
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest -v test_prepare_diagnostics` | 21 tests, 2.438s, OK. Includes pinned-source pull/build root branches, Engine prepare success/failure and diagnostics, retained Auth receipt, and archive-to-runtime coverage. | `dev-37094836912-project-cwd-focused-green.txt`, SHA256 `213a4ac5e24b2258f0eac63f51405fe0f92e91cdba7072d47d53b08a9416a3b7` |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_engine.Acceptance.test_02_four_to_five_reuses_four_and_frontend_build_only test_engine.Acceptance.test_09_frontend_alias_restore_and_config_applicability test_engine.Acceptance.test_frontend_sanity_checks_ready_target_alias_artifact_and_config -v` | 3 tests, 2.488s, OK. | `dev-37094836912-project-cwd-acceptance-focused-green.txt`, SHA256 `af148274c909ad6610738b3634919f58a839c6754c4bc73e9c6f0109e4ecd755` |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s . -p 'test_*.py' -v` | 77 tests, 137.349s, OK. | `dev-37094836912-project-cwd-engine-full-green.txt`, SHA256 `a13b636ccba536ca5f71e8cc305164e65a17e683305cfeda495c30efbc9045a1` |

The first complete run after the adapter edit exposed three old fake-provider outputs still written relative to repo cwd. That exact failed run is preserved as `dev-37094836912-project-cwd-engine-red1.txt` (SHA256 `9e68730a8ad81c02af451a2bf2f76fd371cd03b425a48abb0ec11ae47c4f7fb4`). The shared test-only fake was corrected to write `.vercel/output` relative to the command cwd; all three affected acceptance tests then passed, followed by the complete green suite. `git diff --check` and `node --check deploy/engine/tests/test_vercel_env_context.js` passed.

Historical prepare failures remain causally unknown. Temporary diagnostic-field retention and permanent disposition remain pending Owner direction, outside the deployment success contract. The current source/tests/evidence remain local and uncommitted on branch `Rayer/LWC-358-frontend-prepare-metadata`, worktree HEAD/tree `77733ab480295b64fbe5e0130f81321c5df93806` / `ff16671166afbf049d71dc7621ba4f451c24d464`; this local change has no new reviewed source SHA.
