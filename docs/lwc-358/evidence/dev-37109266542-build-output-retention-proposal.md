# DEV 37109266542 — Frontend build output retention

Owner disposition 3584-1753 authorized this bounded implementation. The historical reason that Vercel omitted `static/build-config.json` remains UNKNOWN; this change improves the next reviewed diagnostic run and does not identify or fix the incident cause.

## Change

Development Frontend prepare now keeps the successful `vercel build` stdout and stderr in memory until prepare returns. If a later prepare step fails, each stream is passed through the existing known-value handling and retained in `frontend_prepare_diagnostic.build_output` as `{text, truncated}`. Each stream is capped at 32 KiB of UTF-8 text; when over cap, the middle is omitted and the output keeps its beginning and end with an explicit truncation marker. Short streams remain complete. There is no new layout metadata or Action/workflow transport: `Engine.result()` already serializes the failure diagnostic to stdout and `result.json`, and the existing Action/result-artifact path retains that file.

The streams are not added to successful prepare results or Production results. Build exit/timeout handling, primary error/cause, command order, cwd, argv, environment, strict config validation, receipts, Auth reuse, barrier, and runtime behavior are unchanged. Diagnostic serialization is best-effort, so an error while retaining streams cannot replace the prepare failure. No argv, environment, config, or file contents are collected.

## Verification

All Python commands used Python 3.14.6 with explicit `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` and `PYTHONDONTWRITEBYTECODE=1`.

| Command, cwd `deploy/engine/tests` unless noted | Result | Evidence SHA256 |
| --- | --- | --- |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics.StructuredCauseContract.test_large_error_message_cap_uses_utf8_bytes_and_keeps_both_ends test_prepare_diagnostics.FrontendPrepareDiagnostics.test_action_retains_bounded_dev_build_streams_only_on_later_prepare_failure` | 2 tests / 1.816s, OK | `dev-37109266542-build-output-retention-focused-rerun1.txt` — `f46a588d91f928a430895d9f0e24f9bfa90015afe53441c707abeb7eae2e520d` |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics` | 25 tests / 4.958s, OK | `dev-37109266542-build-output-retention-diagnostics.txt` — `a0725a4e864041ec4803bfa1e96b09bd2715867138707a920b7e693deb996e17` |
| `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py'` (repository root) | 81 tests / 138.820s, OK | `dev-37109266542-build-output-engine-full.txt` — `a4f9af3d87da5d8c209ad01844417ec0aa528b399bd8e19a5496d065e3ce41dd` |
| `git diff --check` | exit 0 | — |

The end-to-end regression invokes the real Action wrapper against a temporary checkout and fake CLI executables. It proves the missing static file remains the primary `FileNotFoundError`, short stdout/stderr (including a route marker) reach both Action stdout and retained `result.json`, known Vercel token text is redacted by existing handling, long multibyte streams are each within the byte cap with head/tail retained, Auth receipt bytes are unchanged, and no Frontend receipt/archive or runtime is produced. Separate Engine tests verify successful Development and Production outputs omit the streams.

The first focused execution hit `FileExistsError` in the test fixture setup before launching the Action; it is retained as `dev-37109266542-build-output-retention-focused.txt` (SHA256 `dd66d42a56285feb43f4995c1af4a51e88957783ef2245a5d9c109f0c32efed6`). The fixture was corrected to reuse the temporary root from `make_engine`; the separate focused rerun and both broader suites then passed. The failed output was not overwritten.

## Identity and boundaries

Branch `Rayer/LWC-358-vercel-project-cwd`, HEAD `b37d6e4ffcfdfd74fe81dc96ec925a998a1a65c9`, tree `cdb5163bc7ce12e99164e38bcd72e3529d07a0e4`; base `f1480c03fe7aa0c4a10054de741d057210310db1`. The canonical manifest has 51 byte/mode rows, content SHA256 `695c221822be079c89be7cc7179b95320bdb649f33041cdb8818a7604fe784d7`, manifest SHA256 `d8e15d92228ec4b51999d92ffd86912b3e691fea5d4b3bfcdb86c4ac9edfb7f6`; frozen spec SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

Changes and evidence are local and uncommitted. No Action dispatch, Vercel/provider call, build, network install, credential/IAM write, Production operation, tag, commit, push, or PR publication occurred. The next live log inspection remains contingent on exact-content review and CI.
