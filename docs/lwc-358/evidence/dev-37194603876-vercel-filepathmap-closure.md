# DEV 37194603876: Vercel prebuilt filePathMap closure

This local candidate fixes the confirmed Stage 1 archive omission. It does not repair or alter the retained artifact from the failed DEV attempt, write a receipt, or claim a live deployment was retried.

## Evidence and change

The retained `frontend.tgz` is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37194603876-result/frontend.tgz`, SHA256 `d8e5818d148dfb14bfe2a6ca5ad7ab4dd6c5b885f162c0a17a6cbad17f03a487`. Its three `.vc-config.json` files contain 521 `filePathMap` entries and 197 unique project-root-relative paths; none of those 197 paths is a member of the archive.

The cached official package is Vercel CLI 59.11.7. Its chunk `chunk-G3PXSXIB.js` is SHA256 `a82fc9872d484d67ff49e2267f154dc20ea1948ac13b136128a979467c5727df`. The offline call uses the package's exported `continueDeployment` implementation, which invokes its own `buildFileTree` and `hashes` and yields at `hashes-calculated`, before its first API request. Against the retained archive it returned `ENOENT` at `node_modules/@swc/helpers/_/_interop_require_default/package.json`. The captured output is [the preserved RED](pr90-filepathmap-oldarchive-pinned-hash-red.txt); the original scratch output remains at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-pr90-oldarchive-collector-red-20261004.txt`.

`Providers.prepare()` now reads each generated `.vc-config.json`, gathers its `filePathMap` values that resolve inside the configured project root, and adds those source paths to `frontend.tgz` under the same project-root-relative names. The `.vercel/output` tree and pulled `.vercel/project.json` retain their existing archive paths and contents. It skips paths already represented inside `.vercel/output`, deduplicates references, and stops prepare if an in-root referenced path is absent. Out-of-root references are skipped, matching the pinned collector behavior. No source dependency reinstall or receipt editing was added.

The regression fixture stores the exact three real `filePathMap` objects extracted from the retained archive (`9c8caba360889cde948af62467b6d94cfb5406f5f785c11b43bd8fbe2ce17cc5`). Its test-only build creates non-empty placeholder payloads at those exact 197 paths because the retained archive lacks the source files. This verifies archive closure and collector hashing, not the original bytes or file types of the missing Next/vendor files. Those original source files were unavailable locally; their live file types remain unknown. No batch symlink workaround was added. The production code passes each path to Python `tarfile.add()` with the default symlink behavior.

The new formal test runs the actual provider prepare path, checks every one of the 197 references in the produced tar, extracts it, runs a portable source-derived file-map/hash check unconditionally, and lets the fake deploy consume the extracted prebuilt archive. The portable check runs in CI without a locally cached CLI chunk. With `LWC_TEST_VERCEL_CHUNK` set, as in this candidate's named and full-suite runs, the same test additionally invokes the real cached 59.11.7 `continueDeployment` collector; its first yielded event proves hashing completed before any network/API request. That result is `event=hashes-calculated`, `unique_hashes=201`. The explicit extracted-archive RED/GREEN log is [here](pr90-filepathmap-archive-pinned-hash-green.txt).

## Verification

Environment: Python 3.14.6, Node from the existing workspace, `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`, `PYTHONDONTWRITEBYTECODE=1`, and `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`. No dependency install was run.

| Command | Result | Raw evidence |
|---|---|---|
| `python3.14 -m unittest test_engine.Acceptance.test_frontend_prepare_archives_pinned_file_path_map_closure -v` with `LWC_TEST_VERCEL_CHUNK=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-vercel59-inspect/package/dist/chunks/chunk-G3PXSXIB.js` | 1 test passed; the test exercised prepare → archive → extraction → portable hash check → actual pinned CLI `hashes-calculated` → fake prebuilt deploy | [named case](pr90-file-path-map-closure-final-green2.txt), SHA256 `d6dc796c79dfa5f2caa8e4ffe156fd9d7ac621277b6f6908ffa5a091ba49746d` |
| `python3.14 -m unittest discover -s deploy/engine/tests -v` with the same environment | 97 tests, 184.878s, OK | [full engine suite](pr90-file-path-map-closure-engine-full.txt), SHA256 `eb0ebb2d3254e052c188c7e23a5b5ff70377bc60fafe272189542a5f4254de8c` |
| `git diff --check` | exit 0 | no output |

The failed first fixture run (`NameError: tarfile is not defined`) is retained as `pr90-file-path-map-closure-focused.txt`. Three succeeding interim runs passed the portable assertions before the test retained the caller's optional CLI path across its environment-isolation setup; those raw logs remain under `pr90-file-path-map-closure-focused2.txt`, `...focused3.txt`, and `...final-green.txt`. Only `...final-green2.txt` and the full suite include the real pinned collector invocation. The preserved RED is the actual archive failure; the first fixture RED is only a test-import error.

The implementation paths are `deploy/engine/providers.py`, `deploy/engine/tests/fake_provider.py`, `deploy/engine/tests/test_engine.py`, and the three `deploy/engine/tests/fixtures/vercel-59.11.7-{file-path-map-hash.cjs,file-path-map-refs.json,prebuilt-collector.cjs}` paths. Canonical `implementation-content.json` lists 60 byte/mode rows; aggregate `content_sha256=6e740d73e716a409d86e10a7c76e1c51aece19d3daf1b80e2e8943d46a73a8df`; manifest-file SHA256 `5f68454a5f97aaa2a5a6e712554605579a756d724070cd6358b53e7a9050f961`. The frozen r2 spec remains SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

The worktree remains local and uncommitted at HEAD/tree `34afb9b8391f0e771e6d902281e4c6a990755991` / `9c90dac1d75dfe94ea0b584e58e7b97ad14443bf`. There was no live CLI/API call, Vercel pull/build/deploy, provider write, Actions dispatch, credential/IAM or Production operation, Git publication, or tag operation.
