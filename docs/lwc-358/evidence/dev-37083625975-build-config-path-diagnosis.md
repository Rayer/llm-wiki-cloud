# DEV 37083625975 — Frontend pull-link and build-output roots

Status: offline source verification, local adapter correction, and regressions. The historical live prepare cause remains **UNKNOWN**; that run did not retain the pulled link file or output tree.

## Live result identity

Allowlisted fields were read from `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37083625975-result/result.json` and `state.json`.

- Run `37083625975`, final artifact `11259555588`.
- `result.source` is `fc5be16e85a7832b21ccf63333881e655d44c04f`. Earlier local evidence incorrectly substituted reviewed HEAD `aacd8eb2d3e0fa805ac9ea98a559c3f5e4afc8da`.
- Frontend prepare returned `invalid-or-unreadable-input`, `FileNotFoundError` / `local-input-unreadable`, for the repo-root `.vercel/output/static/build-config.json`; cause stage was `unknown`, with no child exit code or timeout.
- The plan’s configured root was `apps/frontend`. The recorded checkpoint stayed `prepared`, sequence `1`; no component/build entry, candidate, Frontend receipt, ready barrier, or runtime mutation. Parent confirmed the retained Auth receipt remained byte-identical and was not rebuilt.

## Pinned CLI branch verification

I read the exact cached `vercel@59.11.7` tarball without installing or running the CLI; SHA256 `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`.

The project-link path and build-output path differ for this adapter’s actual pull environment:

1. `Providers.prepare` emits `VERCEL_ORG_ID` and `VERCEL_PROJECT_ID`, passes `--scope`, runs `vercel pull` with cwd `ROOT`, and supplies no explicit `--project` selector. In `chunk-2TSBB22D.js:13702-13709,13757-13758`, pinned `getLinkedProject` therefore chooses env context (`orgId`/`projectId`) instead of `getProjectLinkFromRepoLink`. That result has no `repoRoot` or `projectRootDirectory`. Pinned `ensureLink` (`chunk-C762AAVU.js:58-131`) returns the already-linked result.
2. Pinned `pullCommandLogic` (`chunk-XIFATBH5.js:162-193`) uses `currentDirectory=cwd` when `repoRoot` is absent. `writeProjectSettings` (`chunk-EPOLDWRA.js:23-48`) writes `join(currentDirectory, '.vercel', 'project.json')`. Thus this adapter’s pull writes `ROOT/.vercel/project.json`; the earlier nested project-link assumption was wrong.
3. The build code independently resolves the per-directory project root in `commands/build/index.js:4534-4548` and computes its default `OUTPUT_DIR`; `chunk-QHS645AZ.js:10259-10262` defines that output as `.vercel/output`. The already-validated `apps/frontend` output remains `ROOT/apps/frontend/.vercel/output`. The nested build-output path does not imply a nested pull-link path.

This is a static pinned-source contract finding, not proof of the historical live output tree or its cause.

## Source-backed regression and minimal fix

The test fixture `vercel-59.11.7-env-pull-path.js` contains the pinned `ensureLink`, `pullCommandLogic`, and `writeProjectSettings` function bodies. Their source slices were extracted from the exact tarball above (SHA256s `e437c8ad8e254a636ae724144cdf6eaf5592855ddf45f678e3985f8759ad3365`, `c4cf134bd16a85cd02676e2eedb05d96c1ef595387c8be1f013ec7f8aa0efb49`, and `82d3fb6a70a0398c6c5ac679dd57e41ce1c8c193069ec55fd79c5a6a72b9c0cf`). `test_vercel_env_context.js` combines them with the exact pinned env resolver and `getLinkedProject` fixtures. Only API readback and env-file download are stubbed; it supplies the adapter’s selected project/team environment, repository-root cwd, scope context, and executes the real source branches through project-settings file creation. It asserts the link is written only at `ROOT/.vercel/project.json` while the linked project still reports `rootDirectory=apps/frontend`.

The Engine prepare integration invokes that source-backed pull simulation with the production adapter’s actual child env/cwd and CLI argv; the fake build independently creates only nested `.vercel/output`. Before the adapter correction, this test failed because the adapter attempted `apps/frontend/.vercel/project.json`, which the pinned pull did not create. The preserved causal red is [dev-37083625975-env-pull-root-red4.txt](dev-37083625975-env-pull-root-red4.txt), SHA256 `872ac3577ad053d18cee38bf97a5ab827268af0a50abff26561f04b2c4c23b7a`.

The adapter now reads static build config from `ROOT / cfg['root_directory'] / '.vercel/output'` and project-link metadata independently from `ROOT / '.vercel/project.json'`. Strict config equality, canonical archive names `.vercel/output/...` and `.vercel/project.json`, runtime cwd/context/argv, and `--prebuilt` consumption remain unchanged. Missing or wrong configured-root config still fails before Frontend receipt/archive/barrier even with a valid repo-root output decoy. The successful selected Auth+Frontend case checks Auth receipt bytes and proves no Auth build. Runtime tests inspect both canonical archive entries after extraction.

## Verification and retained attempts

Commands used `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`.

- Focused prepare, source-branch, fail-closed, identity, and archive-to-runtime checks: **5 tests / 0.362s / OK**. Output: [dev-37083625975-env-pull-root-focused-green.txt](dev-37083625975-env-pull-root-focused-green.txt), SHA256 `9966cf988ddd00edc9393fc22442d7ac1ca01c7ff2187929ed9c9041c2889785`.
- Full deployment-engine suite: **72 tests / 143.009s / OK**, including the ten acceptance tests and Auth/BFF recovery/transport coverage. Output: [dev-37083625975-env-pull-root-engine-full-green.txt](dev-37083625975-env-pull-root-engine-full-green.txt), SHA256 `40af1fef2f37af2920e31c07016783866bf3a68044a7c58e9831311b2753c451`.
- Earlier `env-pull-root-red.txt` through `red3.txt` are preserved setup/test-harness failures (initial syntax, fixture-hash, and stub-shape corrections); only `red4` is the pre-fix causal source-branch regression. None is counted as a pass.
- The preceding `project-root-engine-full-green.txt` (72 tests) and the parent’s 16-test narrow diagnostic pass are historical results that missed this branch: their fake pull placed `project.json` at the nested root. They do not verify the pinned env-context path. The earlier `project-root-failclosed-red2.txt` remains valid evidence for the separate static-output fallback defect.
- `git diff --check` passed after the correction.

The current implementation manifest has 49 byte/mode rows. Content SHA256: `0cddd7d51f06ba5fc8c1b372018357504d09c4c9cd32110b58e67d7a80cce390`; manifest-file SHA256: `5388605d1112d31933dd57b2bac643642f288534301f7348a679ee804942467f`. Frozen r2 SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

PR79 base/head were `fc5be16e85a7832b21ccf63333881e655d44c04f` / `0fa162fa9df118bde00ba3996e960bb827b1fabd`, tree `80bd5c0589ccd3c48d244acd19ade7035ff6d544`. This correction is local and uncommitted. No live CLI, provider, Vercel API/build, npm network/install, Action dispatch, credential/IAM, runtime, Production, tag, or Git publication occurred. Historical live cause remains UNKNOWN.
