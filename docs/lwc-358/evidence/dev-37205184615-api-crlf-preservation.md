# DEV 37205184615: preserve Vercel output response bytes

The retained runtime result reports `invalid-or-unreadable-input` after a verified Frontend candidate and alias operation. The parent-observed readback failure is reproduced locally at the response parser boundary: Python `subprocess.run(text=True)` translated multipart CRLF to LF; `Providers.api(output=True)` then re-encoded the already-normalized text, and `frontend_build_config.document()` raised `ValueError("boundary")`.

The regression uses the real `Providers.api` call, the real `support.run` subprocess path, and the real `frontend_build_config.document` parser. Only the `curl` executable is the TEST ONLY fake, which writes a CRLF multipart response as bytes; no HTTP or provider request occurs. The pre-fix RED is preserved in [the causal test output](dev-37205184615-api-crlf-causal-red.txt).

`support.run()` now has an opt-in `preserve_stdout_bytes` mode. `Providers.api()` enables it only for `output=True`, and passes those response bytes directly to `document()`. In that mode existing string stdin is UTF-8 encoded for `subprocess.run(text=False)`; nonzero stderr is decoded back to text before the existing stage, permission, and cause handling. All default callers still use `text=True` and `stdout.strip()`.

The first binary-mode GREEN attempt exposed a test-harness issue: `subprocess.run(text=False)` requires bytes stdin. That output is preserved in [the intermediate log](dev-37205184615-api-crlf-causal-green.txt). The fix encodes the existing curl config input without changing its contents. The final actual-path regression then passes in [the final focused log](dev-37205184615-api-crlf-causal-green2.txt).

## Verification

Environment: Python 3.14.6, `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`, `PYTHONDONTWRITEBYTECODE=1`, and offline Go environment (`GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`).

| Command | Result | Evidence |
|---|---|---|
| `python3.14 -m unittest test_engine.Acceptance.test_frontend_multipart_output_crlf_survives_api_run_document -v` (cwd `deploy/engine/tests`) | Final test passed through `Providers.api → support.run → document`; one fake `curl` subprocess; parsed config equals expected | [focused GREEN](dev-37205184615-api-crlf-causal-green2.txt) |
| `python3.14 -m unittest discover -s deploy/engine/tests -v` | 98 tests, 188.383s, OK | [full engine suite](dev-37205184615-api-crlf-engine-full.txt) |
| `git diff --check` | exit 0 | no output |

Changed source/test paths: `deploy/engine/support.py`, `deploy/engine/providers.py`, `deploy/engine/tests/fake_provider.py`, `deploy/engine/tests/test_engine.py`. The canonical manifest has 60 byte/mode rows; `content_sha256=1cfb21e36a87b511bb4d0dff0caeabac36257b76520040ce7603bb7200e58e6f`, manifest-file SHA256 `d60d1b7c123aa70e157193846c46bd07b68f013316dc33650d67965215a281c3`. Frozen r2 remains unchanged at SHA256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

The worktree remains uncommitted at HEAD/tree `dcca1bf833d6943bb7b3c4b177b363dc464422c5` / `f2290c76068ed125b06261ab2f91ed5e1336937d`. No rebuild, Vercel CLI/API request, provider write, alias change, Actions dispatch, Git publication, credential/IAM, Production, or tag operation occurred. This local result does not establish post-fix live readback; the candidate and alias remain as observed, and Parent owns the next readback/recovery action.
