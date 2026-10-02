# DEV 37061649417 — pinned Vercel environment contract and local repair

This is an offline source-contract result and local candidate only. The underlying Frontend `vercel pull` failure in run 37061649417 remains **UNKNOWN**. The source mismatch is real, but the retained run result does not record the child process's ORG/NOW variable presence, so this evidence does not attribute that incident to the mismatch. No later Actions run has verified the candidate.

## Reproduction against pinned package source

The exact cached `vercel@59.11.7` and its declared `@vercel/build-utils@14.9.1` package tarballs were integrity checked and read directly from the local npm cache. No package installation, CLI execution, network request, or live Vercel operation occurred.

| Material | SHA256 |
| --- | --- |
| `vercel@59.11.7` cached tarball | `34432b6f0ddd6501ab17140dcf6c5baa2e68fa1ce91eabcb2afef4fbf4db44eb` |
| `@vercel/build-utils@14.9.1` cached tarball | `49fb66d28b64bacf2183cfa166e873dbb04459f50d07591bce9e6a7b022f07d6` |
| extracted `getLinkedProject` function | `2e749ec1e3821d3f3ab10d6a1579b8eda9d8676350925981cd58e1cfe269a364` |
| extracted `getPlatformEnv` helper | `ef25d645045be0d0f21a9f362c4450c6e732e4b56aacb021f49046747c8ee0e5` |

The exact source excerpts are retained as test-only fixtures under `deploy/engine/tests/fixtures/`; the offline VM harness verifies their source hashes before evaluation. Fake client lookup functions are the only substituted dependencies. The real extracted guard and platform-variable resolver execute.

- With `VERCEL_PROJECT_ID` and `VERCEL_TEAM_ID` present, ORG absent, and explicit scope context representing `--scope`, the pinned function returns its exact incomplete-pair result before either project lookup. TEAM and scope do not satisfy the ORG check.
- With the validated TEAM mapped to `VERCEL_ORG_ID` and the selected project fixed in `VERCEL_PROJECT_ID`, the same function returns its linked branch and looks up the matching team/project.
- When both VERCEL and NOW aliases are present for ORG or PROJECT, the pinned build-utils helper rejects them with `CONFLICTING_ENV_VAR_NAMES`.

## Local adapter change

After `Providers.project()` completes its existing API readback checks, Frontend prepare now constructs the child environment with `VERCEL_ORG_ID` from that verified `VERCEL_TEAM_ID` and explicitly retains the verified `VERCEL_PROJECT_ID`. It removes only `NOW_ORG_ID` and `NOW_PROJECT_ID` from the child environment so inherited aliases cannot conflict with or override the selected VERCEL identity. The parent environment remains untouched. Pull/build order, argv, cwd, timeout, token source, diagnostic redaction, and Auth receipt/reuse behavior are unchanged.

The causal provider regression drives the production `Providers.prepare('frontend')` path with fake child processes. Its project API fake satisfies the existing project/team/repository/root readback contract; at the actual pull boundary it passes the emitted identity-only environment to the extracted pinned `getLinkedProject` function and requires the linked branch. It also starts with wrong inherited ORG and NOW aliases, verifies pull and build both receive the validated pair with NOW aliases absent, and checks existing argv/cwd/timeouts. The fake-only IDs and token are not live credentials.

## Offline verification

Python 3.14.6; all temp files used the explicitly designated `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` directory.

| Exact command (and cwd) | Actual result |
| --- | --- |
| `node deploy/engine/tests/test_vercel_env_context.js` (repository root) | Exit 0; incomplete pair rejected, validated TEAM-to-ORG mapping linked, conflicting NOW aliases rejected. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics.FrontendPrepareDiagnostics.test_provider_prepare_passes_readback_identity_to_pinned_cli_context -v` (cwd `deploy/engine/tests`) | 1 test, `OK`; output: `evidence/dev-37061649417-vercel-env-focused.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` (repository root) | 64 tests in 136.678s, `OK`; output: `evidence/dev-37061649417-vercel-env-engine-green.txt`. This includes all four Frontend prepare stages × exit/timeout, no receipt/archive/runtime on failure, and Auth receipt retention. |
| `python3.14 -m py_compile deploy/engine/providers.py deploy/engine/tests/test_prepare_diagnostics.py` | Exit 0. |
| `git diff --check` | Exit 0, no output. |

The first full run is retained separately at `evidence/dev-37061649417-vercel-env-engine.txt`: 64 tests, one failed new integration assertion. The child environment correctly omitted NOW aliases; the test's JSON projection represented absent keys as `null`, while the Node harness correctly expected absent properties (`undefined`). The projection was corrected to omit missing keys. The focused test and subsequent complete run then passed; the failed log was not overwritten.

## Identity and limits

Pre-edit repository HEAD/tree: `95c466eb4ccd71302a11ff416de8180d55182d9d` / `3216e0b20e8e0567ae655c82a506ddf46a7c7857`, branch `Rayer/LWC-358-frontend-stage-diagnostics`. The local candidate is uncommitted; its canonical implementation manifest records 46 files and content SHA256 `3dfb6aefcaa9c37a63a8280d64c2f7b499d45b53e53653e1872ff6a1882359bc`. The frozen spec SHA256 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

No Git metadata/publication, Actions dispatch, live Vercel/npm/Cloud Build access, provider write, credential/IAM change, Production action, or tag operation occurred. The candidate needs exact-head review/CI and a separately authorized normal DEV run before anyone can state whether this mapping resolves the live failure.
