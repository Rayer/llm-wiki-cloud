# DEV 37083625975 — Frontend build-config path correction

Status: offline pinned-source verification, local adapter repair, and regression evidence. Historical live prepare cause remains **UNKNOWN**; the run did not retain its link/output tree, and this repair does not retroactively establish that tree.

## Live result readback

Allowlisted fields were read from `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37083625975-result/result.json` and `state.json`.

- Run: `37083625975`; artifact: `11259555588`.
- `result.source`: `fc5be16e85a7832b21ccf63333881e655d44c04f`. This corrects the earlier report’s mistaken use of the reviewed HEAD `aacd8eb2d3e0fa805ac9ea98a559c3f5e4afc8da`.
- Frontend prepare returned `invalid-or-unreadable-input`, with `FileNotFoundError` / `local-input-unreadable` for the repository-root path `/home/runner/work/llm-wiki-cloud/llm-wiki-cloud/.vercel/output/static/build-config.json`. Its structured cause stage was `unknown`; no child exit code or timeout was recorded.
- The plan’s configured Frontend root was `apps/frontend`. The successful project-readback step requires the API `rootDirectory` to equal that plan value. The failure says the adapter’s repository-root static-file lookup was absent; it does not show where the live link or output existed.
- Checkpoint remained `prepared`, sequence `1`, with no component/build entries, no candidate, no possible runtime mutation, and runtime unstarted. Parent confirmed the retained Auth receipt bytes match the earlier receipt; Auth was not rebuilt. No Frontend receipt or ready barrier was produced.

## Pinned Vercel path contract, checked independently

I read the cached `vercel@59.11.7` tarball without installing or executing it. Its SHA256 is `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb`.

The project-link location and build-output location come from separate code paths:

1. `pullCommandLogic` in `package/dist/chunks/chunk-XIFATBH5.js:162-193` takes `repoRoot` and `project.rootDirectory`, then sets `currentDirectory = join(repoRoot, project.rootDirectory || '')`. `writeProjectSettings` in `chunk-EPOLDWRA.js:23-48` writes `join(currentDirectory, '.vercel', 'project.json')`. For the validated `apps/frontend` root, pull therefore writes `ROOT/apps/frontend/.vercel/project.json`.
2. `getProjectLinkFromRepoLink` in `chunk-2TSBB22D.js:13556-13615` exposes the repository link’s project directory as `projectRootDirectory`. `getLinkedProject` resolves the project context from that repo link. `commands/build/index.js:4436-4443` reads Vercel settings from `join(repoRoot, projectRootDirectory, '.vercel')`; separately, `commands/build/index.js:4548-4549` derives the default output as `join(repoRoot, projectRootDirectory, OUTPUT_DIR)`. `chunk-QHS645AZ.js:10259-10262` defines `OUTPUT_DIR` as `.vercel/output`. For the validated `apps/frontend` root, the build output is `ROOT/apps/frontend/.vercel/output`.

The paths coincide here because both contracts consume the validated project root; the project-link formula and build-output formula were checked separately. No fallback search across repository and project roots is part of the pinned contract.

The app’s `build-config.json/route.ts` is statically configured with `dynamic = 'force-static'` and `revalidate = false`. A prior offline integration check ran three real Next `16.2.7` builds; each listed `/build-config.json` as static and the test checked the generated route body and prerender manifest. Its retained output is [dev-37083625975-next-build-config-trace.txt](dev-37083625975-next-build-config-trace.txt), SHA256 `a1a4d4b273360803f9f932493bd6e885c91ee74d4b28e93ca7194d0f2e564b9a`.

## Local correction

`Providers.prepare('frontend')` already calls `project()` before the pull/build commands; that readback rejects a `rootDirectory` which differs from the plan’s `apps/frontend`. The adapter now joins `ROOT` with that validated `cfg['root_directory']` for the static output read and for the separately verified project-link file. It still requires the exact expected `schema_version`, `api_url`, and `auth_url` object. It packages them under the existing canonical archive names `.vercel/output/...` and `.vercel/project.json`.

The runtime `vercel deploy --prebuilt` argv, cwd/context setup, aliases, candidate handling, and archive layout are unchanged. There is no alternate-root fallback, file scan, relaxed validation, new ready path, or Frontend rebuild behavior change.

The offline integration fake now models the pinned pull/build roots. Positive prepare assertions inspect both canonical archive entries. Missing and incorrect configured-root static config cases each place a valid repository-root decoy and assert prepare still fails before Frontend receipt/archive or the ready barrier. The same positive case asserts the Auth receipt is byte-identical and no Auth build ran. The runtime boundary regression reads the canonical project link and static config from the archive consumed by the real `Providers.deploy` path. The full engine acceptance fake also creates output only under `apps/frontend/.vercel/output` and checks the `--prebuilt` child receives that archive content.

## Offline regression evidence

All Python commands explicitly used `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`.

- Before the source fix, the positive Engine/Providers prepare test failed because the adapter reported `frontend-output-missing` when the fake build wrote only to the configured project root. Retained red: [dev-37083625975-project-root-engine-red2.txt](dev-37083625975-project-root-engine-red2.txt), SHA256 `98bdb45e3bebe873aad92e73d72a458d8056432250b4042c516914309c0f9026`.
- Before the fix, with valid repo-root decoys present, both missing and wrong configured-root config cases incorrectly completed as `ready`, demonstrating the fallback bug. Retained red: [dev-37083625975-project-root-failclosed-red2.txt](dev-37083625975-project-root-failclosed-red2.txt), SHA256 `eaaa75f41861ba5e608642b58bf1fc7d1296e709378ac4e8137ac80e302b31b8`.
- The earlier `project-root-engine-red.txt` (SHA256 `8bab0d7e7fcd4de8958d7b9fa05e05760a2275c63ac2cb4cfb139ba6c0ee12ac`) and `project-root-failclosed-red.txt` (SHA256 `8f070ba9eb86f21ab127e1d144630a7d80e537e1c06b5973a6946d0d6ce79301`) are preserved fixture-development failures, not source-causal results: the first expected a state file not yet persisted on prepare failure; the second lacked a repository-root project-link decoy and failed before reaching the assertion.
- The four focused post-fix tests passed: Engine main success, missing/wrong config rejection, adapter readback identity, and archive-to-runtime consumption. Exact output: [dev-37083625975-project-root-engine-focused-green.txt](dev-37083625975-project-root-engine-focused-green.txt), SHA256 `fdaf123bbd9deb45999a2f9094f24577c67802d777786b59a7ac2ef9ba810844`.
- Full deployment-engine Python suite: `python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` passed **72 tests in 139.287s**. This includes all ten acceptance tests, Auth/BFF recovery and readback coverage, Frontend alias/config behavior, workflow/transport integration, and the new nested-root archive flow. Exact output: [dev-37083625975-project-root-engine-full-green.txt](dev-37083625975-project-root-engine-full-green.txt), SHA256 `3a33f05dcba6d6ccfbc6dce6a36e9f8ffcf8f3aeb1bb52363e36a60a2719605c`.
- `git diff --check` exited `0` with no output.

The canonical implementation manifest now records 48 byte/mode rows. Content SHA256: `4edf3e8346989ce301eb5393fcc4cb5a06eaaa6be265066372362e3b8d41af0f`; manifest file SHA256: `9054994bd09a9f6f2cd883873c29e516ae4b4733dbf1bec3d2d29a1909e24212`. Frozen spec SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

Worktree HEAD/tree remain `aacd8eb2d3e0fa805ac9ea98a559c3f5e4afc8da` / `74201d0dc27d95a8bfa7829de12aafee1ab54826`; source edits are local and uncommitted. No Action dispatch, live provider/Vercel/build, npm network/install, credential/IAM, runtime, Production, tag, or Git publication occurred. The exact live `apps/frontend/.vercel/output` presence and historical cause remain unknown.
