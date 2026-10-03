# Frontend Static and Prerender build-config validation

## Result

Implemented the Owner-accepted 3584-1762/1763 contract locally on the existing LWC-358 engine worktree. The frozen r2 specification is unchanged. The current implementation manifest covers 52 implementation, test, appendix, and frozen-spec files; content SHA-256 is 6c934b7db848398221ff580f40cb42e2b8293d7fb8efba2ae69595b0ba5935b1, manifest-file SHA-256 is 27f786143eff0a8d5cc14b304b275e678bc7d8a49fddc5ea92f383926f907f35, and the frozen specification SHA-256 remains 838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b.

The pre-change causal test is preserved at prerender-layout-causal-red.txt. With the real engine prepare entrypoint and the official same-key Prerender fixture, the old Static-only reader exited 1 with invalid-or-unreadable-input and FileNotFoundError for configured_root/static/build-config.json, despite all four prepare commands returning exit 0. The new validator accepts that exact official Prerender layout, while rejecting malformed descriptors, wrong descriptor or fallback types, a fallback basename escape, a handler escape, a missing fallback or handler, wrong content type, wrong target JSON, and simultaneous Static plus Prerender layouts.

Prepare and the best-effort DEV diagnostic now use one local validator. It checks regular files, output containment, the handler's containment inside its own .func directory, exact fixed descriptor and fallback bindings, strict JSON shape, and exact configured API/Auth target values. It does not scan the output tree, accept an arbitrary body, inspect Vercel environment values in .vc-config.json, or require the outer output config.json. The entire .vercel/output directory and byte/mode-restored .vercel/project.json continue to be archived. Auth receipt reuse and the ready barrier are asserted. Runtime deployment consumes a Prerender-form archive in its existing --prebuilt path; provider-side sanity still compares the remote logical build-config.json response to the immutable target. No runtime args, aliases, diagnostics schema, provider authority, or Production behavior changed.

Consumer audit: Providers.observe and deploy/components/frontend.sh validate the remote logical build-config.json response rather than a local filesystem serialization, so their strict target checks remain unchanged. The test-only fake_provider.py still emits the Static representation for its existing engine workflow cases; the production Providers.deploy adapter regression separately extracts and exercises the official Prerender-form archive.

## Pinned writer evidence

The source and filtered result copied to dev-37111842689-prerender-chain-probe-v3.mjs and dev-37111842689-prerender-chain-output-v3.json are from the previously executed isolated probe, whose full provenance is in dev-37111842689-official-prerender-probe.md. It used real Next 16.2.7 output, the pinned @vercel/next 11.0.2 build API, and the pinned vercel 59.11.7 writeBuildResult implementation (resolved build-utils 14.9.1). The writer returned key build-config.json as Prerender, wrote the same-key .func and prerender descriptor/fallback, and the fallback bytes matched the actual Next route body with SHA-256 7b10a9d8699a93cd4bfe390aafd409bb409ffd145902fd579a4e2cfdd87934bf. The fixture's incidental runtime value is recorded only as output evidence and is not validated or pinned. The writer probe did not produce or validate the outer .vercel/output/config.json route table.

The historical DEV runner layout and cause for prepare run 37111842689 remain unknown. This compatibility repair is supported by the pinned source probe and local production-adapter regression; it does not claim to have identified the historical runner's exact output.

## Test evidence

Commands used explicit task scratch and disabled bytecode writes:

- Red, before the production adapter change: TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-regression-20261003 PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest deploy.engine.tests.test_prepare_diagnostics.FrontendPrepareDiagnostics.test_official_prerender_output_prepares_and_archives_exact_fallback — exit 1, one expected failure recorded in prerender-layout-causal-red.txt.
- Final focused regressions after the .func containment correction: the five named methods in prerender-layout-focused-handlerfix.txt — 5 tests, 1.681 seconds, OK. This includes Static and Prerender prepare, diagnostic consistency, the fail-closed matrix (including a nested symlink to a sibling output function), and actual prebuilt archive consumption.
- Full affected engine suite after the final source change: TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-regression-20261003 PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' — 84 tests, 143.153 seconds, OK; output is preserved in prerender-layout-engine-full-handlerfix.txt.
- Deployment workflow contract: TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-regression-20261003 PYTHONDONTWRITEBYTECODE=1 python3.14 scripts/test_engine_workflow.py -v — 6 tests, 0.022 seconds, OK in prerender-layout-workflow-contract.txt. No workflow source changed in this repair.
- git diff --check — exit 0, no output.

The earlier 84-test run at 144.422 seconds and the two earlier five-test focused runs at 1.370 and 1.580 seconds remain historical pre-.func-boundary evidence in prerender-layout-engine-full.txt, prerender-layout-focused-green.txt, and prerender-layout-focused-final.txt; they are not used as final-source results. No live CLI/API/provider operation, Action dispatch, provider write, npm installation, IAM/credential change, Production action, commit, push, or publication occurred in this implementation step.

## Working identity

The checkout remains on Rayer/LWC-358-frontend-build-logs at HEAD e8989d4abf7072cfc3945812904219e2175c3551, base tree 087fca54193f6556910c4bb920e3a01d46adbd64. The candidate remains local and uncommitted; no new review SHA is claimed.
