# PR77 runtime Vercel context repair — offline evidence

## Review finding and call path

I independently read the keyed Supervisor result `0fa70a4e163e1a4046a381df` at `/Users/rayer/.hermes/profiles/lwc-tpm/logs/process-results/proc_42d1752f7363.json` and its full source findings. It reproduces a sibling deployment-context defect at the exact PR77 head `c8e32bef33107bd87d7bed44369ee717c7074308`. The contemporaneous [TPM review](</Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/pr77-tpm-c8e32b-final-review.md>) passed the narrower prepare/source review and did not identify this runtime defect; Parent reconciled the Supervisor finding as overall HOLD in live LWC-358 comment `4-1690`. The successful PR77 CI run does not cover this runtime child-environment boundary.

The ordinary CD runtime step supplies the existing Vercel token, project ID, and team ID. `Engine.deploy()` first loads a ready receipt through `barrier()`, whose `receipt()` calls `Providers.usable()`; for Frontend that validates the receipt archive, target config, and stored project/team against the selected runtime inputs. `Providers.deploy()` then extracts the archive and constructs a separate child environment by filtering `GITHUB_*` and `VERCEL_GIT_*` names. Before this change it did not provide `VERCEL_ORG_ID` or remove legacy NOW aliases. Pinned Vercel 59.11.7's `deploy --prebuilt` still enters `ensureLink`/`getLinkedProject`; the real extracted resolver rejects an incomplete ORG/PROJECT pair before consulting a valid local `.vercel/project.json`.

The other production Vercel CLI paths were inspected. `Providers.prepare()` already maps the API-readback-validated TEAM/PROJECT for `pull` and `build`. The retained shell adapter `deploy/components/frontend.sh` exports `VERCEL_ORG_ID="$VERCEL_TEAM_ID"` before its pull/build/deploy commands. No API-only aliases were treated as CLI call sites. The new production change is confined to the `Providers.deploy('frontend')` child environment.

## Causal regression and repair

The first test version ran the unmodified `Providers.deploy()` method against a fake archive containing a valid `.vercel/project.json` with the selected IDs. Its fake subprocess boundary passed the actual emitted identity environment to the exact source-extracted `getLinkedProject` and `getPlatformEnv` functions from the integrity-checked Vercel 59.11.7/build-utils 14.9.1 fixtures. With selected PROJECT and TEAM but no ORG, the resolver returned the exact pair-incomplete result (exit 1, zero lookups); `Providers.deploy()` stopped before provider readback, alias handling, or candidate save. The full raw test output is `evidence/dev-37061649417-runtime-deploy-context-before-fix.txt`.

The minimal fix now adds `VERCEL_ORG_ID=artifact['team']` and `VERCEL_PROJECT_ID=artifact['project']` to that already-filtered child environment, then removes `NOW_ORG_ID` and `NOW_PROJECT_ID`. The values come from the selected Frontend ready artifact, which has passed the existing receipt/barrier validation. It does not mutate the parent environment or add a validation gate. Existing CLI argv, token, environment filters, cwd, timeout, candidate persistence, deployment readback, and alias sequence are unchanged.

The green runtime regression starts the parent with a wrong inherited VERCEL ORG and wrong legacy NOW aliases, plus GITHUB/VERCEL_GIT values. At the actual `Providers.deploy()` subprocess boundary it verifies the unchanged deploy argv and valid extracted archive; the source probe first confirms the baseline TEAM+PROJECT/no-ORG context yields `pair-incomplete`, then confirms the emitted mapped child context reaches the linked branch. It checks the parent environment is unchanged, child aliases are sanitized, the token remains in the child environment, one candidate is saved, aliases are read as before, and a second invocation resumes the saved deployment without another deploy subprocess. Provider readback/write methods are fakes; no provider call occurs.

## Verification

All commands used Python 3.14.6 and explicit `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch` for Python tests.

| Exact command (and cwd) | Result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics.FrontendPrepareDiagnostics.test_runtime_deploy_boundary_reproduces_missing_pair_before_fix -v` (`deploy/engine/tests`, pre-fix source) | 1 test, `OK`; causal assertion proves the actual old deploy environment fails before provider operations. Raw: `evidence/dev-37061649417-runtime-deploy-context-before-fix.txt`. |
| `node deploy/engine/tests/test_vercel_env_context.js` (repository root) | Exit 0; missing-pair, TEAM-to-ORG mapping, and NOW-conflict source cases pass. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest test_prepare_diagnostics.FrontendPrepareDiagnostics.test_runtime_deploy_maps_selected_context_before_pinned_cli -v` (`deploy/engine/tests`) | 1 test, `OK`; output explicitly records `baseline=pair-incomplete; runtime-child=linked; resume=single-deploy`. Raw: `evidence/dev-37061649417-runtime-deploy-context-green.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` (repository root) | 65 tests in 136.637s, `OK`, exit 0. Raw: `evidence/dev-37061649417-runtime-deploy-engine-green.txt`. This includes the prior 64 green prepare/source/resume cases and the new runtime deploy boundary. |
| `python3.14 -m py_compile deploy/engine/providers.py deploy/engine/tests/test_prepare_diagnostics.py` and `git diff --check` | Both exit 0; no diff-check output. |

The earlier prepare-only full-suite green and its single corrected harness-projection failure remain preserved at their original filenames. This runtime fix does not overwrite or relabel either result.

Runtime-evidence SHA256: pre-fix focused `29e502aa97bda62c272d12ac4af6952b6e36522741b1a55349cba753fda3ecc7`; post-fix focused `114c624b3d78d4cffa95d62b693bd2101da82524d259b48158af405f7524836b`; full-engine green `0b46855b0f84c14f0fd6a553a85d25df5ea145251746d4bd0e9c922866e2e8d5`.

## Identity and limits

Pre-edit HEAD/tree: `c8e32bef33107bd87d7bed44369ee717c7074308` / `ec3ed43371d194f0a55841c0488e16cff6c64b04`, branch `Rayer/LWC-358-vercel-project-context`. The local-only canonical manifest now records 46 file byte/mode rows with content SHA256 `b90aeb7c06e4eb11b71516a3c21292546d965a736522173e8ff8258fb06a445d`; frozen r2 remains `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`.

This proves the runtime source-context contract and offline adapter behavior. It does not establish whether live DEV run 37061649417 had the same inherited environment or whether this fixes a live Vercel result; that historical cause remains UNKNOWN. This candidate is uncommitted and requires new exact-head review/CI. No Git publication, workflow dispatch, Vercel/npm network, provider operation, Cloud Build, credentials/IAM change, Production action, or tag operation occurred.
