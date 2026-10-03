# DEV 37089775677 — Frontend output path diagnosis

Run `37089775677` used source `79c1da60e599b43396cca09e6c3cc1f37b3e259f` (the merged tree `6e675dda1eb5340ebe1d7a10e26af61c120601e9`). Its result is `frontend-output-missing` for Frontend, checkpoint 1, with no candidate, no component mutation, and runtime unstarted. Final artifact `11262041704` contains result SHA256 `a197af6d1bc02cf3eb58d8a9bd77056898be87c825a8d696c90cf2477cb02e6c`; prepared artifact `11261931923` was not ready. Parent verified the retained Auth receipt is unchanged and there is no new Auth build. The raw result does not record the Vercel link/settings root, candidate output paths, or per-command exits. Historical root cause remains **UNKNOWN**.

## Source trace and bounded conclusion

The cached `vercel@59.11.7` tarball was read without installing or running the CLI; tarball SHA256 is `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`.

| Step | Verified source behavior | Consequence for this adapter |
|---|---|---|
| Provider inputs | `Providers.project()` validates the API `rootDirectory` against the plan. `prepare()` sets `VERCEL_ORG_ID` and `VERCEL_PROJECT_ID`, removes inherited `NOW_*`, uses repo `ROOT` as cwd for pull/build, and passes no `--project` or `--output`. DEV passes no `--prod`; target defaults to preview. | Project selector and target are not supplied through those CLI options. The repo-root cwd and env-context link are the relevant inputs. |
| Pull/link | The prior pinned-source trace showed env-context `getLinkedProject` returns no `repoRoot` or `projectRootDirectory`; `pullCommandLogic` therefore uses cwd, and `writeProjectSettings` writes `ROOT/.vercel/project.json` with project settings. | The pulled link is expected at repo root when this pinned branch is selected. The build reads the link/settings from its cwd. The live artifact does not prove the file existed or show its contents. |
| Build link selection | In `dist/chunks/chunk-2TSBB22D.js:13539-13554`, `getProjectLink` first reads a directory link and returns it when no explicit project name was passed; otherwise it can use a repo link. The adapter passes no `--project`. In `dist/commands/build/index.js:4407-4443`, `hasRepoLevelLink` depends on `link.repoRoot`; a directory env-link has no repo root, so the per-directory resolver is eligible. |
| Workspace/root resolution | `dist/commands/build/index.js:4214-4325` scans up to 64 ancestors for npm or pnpm workspace roots and requires a workspace pattern to claim the linked directory. If `linkLocation` is empty, `resolvePerDirectoryLinkRoot()` immediately returns an empty root **before** applying `settings.rootDirectory`. For a nonempty link location it applies an existing root setting, or falls back to the linked directory when the setting is empty/missing. A repo-level link skips this helper. | If `ROOT` is the selected workspace root (or no ancestor workspace claims it), Vercel’s default output can be `ROOT/.vercel/output` even with settings root `apps/frontend`. If an ancestor workspace claims `ROOT`, the nested configured output branch can be selected. |
| Output option/check | `dist/commands/build/index.js:4548-4549` chooses `join(cwd, projectRootDirectory, OUTPUT_DIR)` unless `--output` is passed; `dist/chunks/chunk-QHS645AZ.js:10261` defines `OUTPUT_DIR` as `.vercel/output`. The adapter checks only `ROOT/apps/frontend/.vercel/output` at `deploy/engine/providers.py:344-347`. | A root-level output, no output, or output under another computed root all produce this adapter breakpoint. No fallback scan occurs. |

The checked-in tree has no root `package.json` or root `pnpm-workspace.yaml`; `apps/frontend/package.json` is a descendant, which the resolver’s ancestor scan does not inspect from `ROOT`. This makes the root-output branch a concrete source-based explanation **if** no parent directory in the runner claims the checkout as a workspace. GitHub runner ancestor contents and the actual pulled link file were not retained, so that condition is not established for this run. The result’s failure point is after the adapter’s sequential `npm ci`, `vercel pull`, and `vercel build` calls. Under current `support.run()` semantics, each nonzero return raises before the output check; the failure is therefore consistent with those calls returning successfully, but the artifact does not store their individual exit codes. It does not show whether Vercel wrote output at the repo root, elsewhere, or nowhere.

## Offline source-backed fake acceptance

An offline harness executed the exact cached `findWorkspaceRootCandidates`, `workspaceTypeOf`, `readWorkspacePatterns`, `workspaceClaims`, `resolvePerDirectoryLinkRoot`, and `normalizeRelative` source bodies. Its source fixture SHA256 is `eac3775d0a261ec4508bfe0e83b6d82c5fe21b0873cf354d95f8ddae9b9283f3`. Synthetic filesystem cases passed for: no ancestor workspace (root output while the adapter’s nested candidate is absent), workspace at the link anchor, a claiming npm ancestor, empty/missing settings fallback, a negative workspace pattern, and a claiming pnpm ancestor. The fixture injects a minimal matcher for the literal patterns used and a minimal parser for the one-line pnpm fixture; it verifies source branches, not full CLI behavior or live runner selection.

Output: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37089775677-build-root-offline-acceptance.txt`, SHA256 `967d21ad339af1f0eb66c75203cd618de56449586bf809b00041c3d98695f533`. Executable: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37089775677-build-root-offline-acceptance.cjs`; extracted source: `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37089775677-build-root-pinned-functions.js`.

The existing `node deploy/engine/tests/test_vercel_env_context.js` also passed its three offline cases (env pair selection, TEAM-to-ORG mapping, and conflicting legacy aliases). Output is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37089775677-env-pull-source-green.txt`, SHA256 `da8a773e5173a4db645dd0d2934851d774b3266579d561932c3f92bffba77044`. The earlier 72-test engine PASS does not establish this build-root branch: its build fake creates the nested output directly and does not run Vercel’s workspace resolver.

## One-run DEV diagnostic proposal for Parent

After exact-head review/CI and Parent approval, add an additive, failure-only Frontend diagnostic record to the existing DEV prepare result. Keep prepare/deploy behavior unchanged. Emit only:

- `linkedroot` and `settingsroot` as bounded labels: `matches-config`, `empty`, `missing`, or `other`; do not emit arbitrary paths or project identifiers.
- Context-presence booleans for `VERCEL_ORG_ID`, `VERCEL_PROJECT_ID`, `VERCEL_TEAM_ID`, `NOW_ORG_ID`, and `NOW_PROJECT_ID`.
- Numeric exit codes for `npm ci`, pull, and build when returned; stage and timeout class when a call fails or times out.
- Fixed candidate-existence booleans for `ROOT/.vercel/output` and `ROOT/apps/frontend/.vercel/output`, plus booleans for configured static config validity and target equality.

Do not emit CLI arguments, environment values, tokens, URLs, config contents, raw stdout/stderr, arbitrary paths, archives, or recursively scanned paths. The diagnostic is evidence only; it does not create a receipt, relax the output gate, or enter runtime/alias work. If both known output candidates are absent, the exact Vercel-selected output directory remains unknown; this bounded record will not pretend to identify an unobserved path.

No provider/API/CLI invocation, Action dispatch, npm install/network, credential/IAM operation, product edit, Git publication, or deployment retry occurred in this diagnostic turn. Prior raw logs and worktree state were preserved.

## Accepted temporary failure-only DEV metadata implementation (3584-1731)

This section supersedes the proposal above. The original run's output-root cause remains **UNKNOWN**; this change only makes a future reviewed DEV prepare failure more discriminating. It does not move the build output, scan for arbitrary paths, make a receipt, relax validation, or change the ready/runtime barrier.

For a failed Development Frontend prepare only, the result now carries `frontend_prepare_diagnostic` alongside the existing primary reason, stage, exit/timeout, cause, allowed action, and mutation/state fields. The record contains:

- `linked_root` from the API project readback and `settings_root` from the single pulled `ROOT/.vercel/project.json`, each reduced to `matches-config`, `empty`, `missing`, or `other`.
- Presence booleans only for `VERCEL_ORG_ID`, `VERCEL_PROJECT_ID`, `VERCEL_TEAM_ID`, `NOW_ORG_ID`, and `NOW_PROJECT_ID` in the child context; when that context was not yet constructed the value is `null`.
- Four fixed command records for project readback, `npm ci`, Vercel pull, and Vercel build. Each has `status` (`exit0`, `typed-fail`, `timeout`, or `not-run`), numeric `exit_code` when returned, and `timeout_class` when set. An unrun or unavailable value is `null`.
- Booleans for existence, static config validity, and target equality at exactly `ROOT/.vercel/output` and `ROOT/<configured-root>/.vercel/output`. No recursive search or path values are emitted.
- `metadata_read_failed`, which records malformed/unreadable metadata observations. A diagnostic collection exception is ignored so it cannot replace the original prepare error.

The original Vercel cause/message path is untouched. Success and Production results omit this Development-only field. The actual deployment Action already forwards engine stdout and retains `result.json`; no Action or workflow code changed. Offline Action integration checks confirm both views contain the additive field. On the missing-output failure fixture, the result remains failed/unstarted with no mutation, Frontend receipt, archive, ready barrier, or runtime call; the retained Auth receipt is byte-identical.

## Verification and current local identity

Using Python 3.14.6 with explicit `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`:

Exact commands, run from `/Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/deploy/engine/tests`:

```sh
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest -v test_prepare_diagnostics > /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/docs/lwc-358/evidence/dev-frontend-prepare-metadata-focused-final5.txt 2>&1
TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s . -p 'test_*.py' -v > /Users/rayer/orca/workspaces/llm-wiki-cloud/LWC-358-engine-r2/docs/lwc-358/evidence/dev-frontend-prepare-metadata-engine-full-final.txt 2>&1
```

The focused command passed **21 tests in 2.158s**, log SHA256 `d99b862b2e547348ac5813330545122b7d5228f02dc53f50f199dd398670b380`. The full deployment-engine suite passed **77 tests in 138.817s**, including Action and artifact transport integrations; log SHA256 `98e19737ec81f32d1bd6a17c42f64cfbc5be3d0d41aebcb7dcd3519157048ce7`. `git diff --check` exited 0.

Earlier focused attempts are retained separately, not counted as green: `dev-frontend-prepare-metadata-focused.txt` records initial test-fixture assertion errors; `dev-frontend-prepare-metadata-focused-final.txt` exposed that project-readback failures needed the already validated plan config available to the best-effort collector. Earlier green logs `dev-frontend-prepare-metadata-focused-final2.txt` and `dev-frontend-prepare-metadata-engine-full.txt` predate the final command classification. The final focused and full engine runs above include the completed command status behavior, including a missing-CLI typed failure with the existing `tool-unavailable` timeout-class value.

The canonical manifest has **49 exact byte/mode rows**, content SHA256 `1b9a0a6028c8653c8e45c4992808ad27efa8bdf55d5b440e596eb7f1ded710d6`; manifest-file SHA256 `2aad1c138ed876e2637803baa113999ba49ecc9352f9168f8c5afb71d4874510`. Its local diff base is `f9814ded505e4fa9b41dbb74709a82943becc8b7` on branch `Rayer/LWC-358-vercel-build-output-root`. HEAD/tree remain `f9814ded505e4fa9b41dbb74709a82943becc8b7` / `6e675dda1eb5340ebe1d7a10e26af61c120601e9`; these edits are local and uncommitted. Frozen r2 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. This temporary metadata is for the next Parent-approved read-only DEV diagnostic; cleanup or permanent disposition remains pending that evidence and Parent review.

No live CLI/API/pull/build, provider, npm network/install, Action dispatch, credentials/IAM, runtime, Production, Git publication, or retry occurred.
