# LWC-368 PR96 retained workflow contract follow-up (r5)

Recorded 2026-10-06. This report covers the small retained-test correction for the already published `cac-spec-r4` candidate; it does not change product workflow or engine code.

## Source delta

- Base candidate: PR96 head `8a94ab11e3de207e460557e8b7dec594610dd2ab`, tree `5c64b3c54be307028c32da8052f59111dc03cf76`.
- The sole source/test change is `apps/bff/scripts/test_bff_explicit_cutover.py`; SHA256 is `500835a63e2d594fd13fe1821512967de616df34b4b79e4e6a91a4c9911605ee`.
- The retained contract now expects `release_tag` to be optional at the DEV and Production dispatch wrappers, includes `config-only` in both operations lists, keeps the shared reusable workflow's `release_tag` input required, and checks the shared release/config-only job separation and config-only path has no `--image` argument.
- No workflow, engine, product code, or prior r4 evidence was changed. The original `source-manifest-r4.tsv` remains byte-identical at SHA256 `1c1989124da4d049f0a6c4377b9775dccac88874bd192e60e332ed7bf9108488`.

## Failure and contract evidence

- Canonical CI run `37469644335` was independently read by the parent: the retained BFF suite ran 109 tests in 30.726s and failed once at `test_bff_explicit_cutover.py:193`, because `deploy-dev.yml` correctly declares `release_tag.required: false` for the new config-only dispatch path. This is live CI evidence supplied by the parent, not an inference from the local run.
- The pre-fix local focused test reproduced that exact assertion failure; see `ci37469644335-bff-cutover-red.log`.
- `deploy-dev.yml` and `promote-production.yml` both describe the wrapper release tag as unused by `config-only`. The reusable `cd.yml` call still requires a string `release_tag`. `deploy/engine/engine.py:103-104` rejects an empty or option-like tag and runs `git check-ref-format`; `Engine.tag` accepts an existing tag only when it resolves to the plan source commit and rejects a conflicting tag. The existing non-config release admission and immutable tag behavior are present, so no product gate or engine change was needed.
- Shared `cd.yml` keeps the release job excluded for `config-only` and a separate `pipeline-config-only` job. That path remains Worker-only and does not pass an image replacement argument; its runtime/image safety contract is covered by the existing engine workflow suite.

## Local verification

| Check | Result | Evidence |
|---|---|---|
| Pre-fix causal focused test | 1 test failed on the stale `release_tag.required` assertion, exit 1 | `ci37469644335-bff-cutover-red.log` |
| First post-fix focused attempt | blocked before assertions when nested Go used the default `~/Library/Caches/go-build`, exit 1; environment failure only | `ci37469644335-bff-cutover-cache-block.log` |
| Focused corrected contract test | 1 test passed in 8.784s, exit 0; includes the seven embedded engine safety acceptances | `ci37469644335-bff-cutover-focused-green.log` |
| Canonical retained BFF command, sandbox attempt | 109 tests; two local loopback bind errors (`operation not permitted`), exit 1; tests were not changed or skipped | `ci37469644335-retained-bff-suite-sandbox-red.log` |
| Canonical retained BFF command, workspace caches and one-time outside-sandbox execution | 109 tests passed in 39.757s, exit 0 | `ci37469644335-retained-bff-suite-green.log` |
| Parent replay of the exact retained command | 109 tests passed in 39.741s, exit 0; copied byte-for-byte from the parent's raw log (SHA256 `f35831683eee68411ab37655ff708cf7e9a7c6b082cdab78a8b5cdcfe9a5e382`) | `ci37469644335-parent-retained-bff-suite-green.log` |
| `python3 ../../scripts/test_cd_contract.py` | 69 tests passed, exit 0 | `ci37469644335-shared-cd-contract-r5.log` |
| `python3 -m unittest scripts.test_engine_workflow -v` | 7 tests passed, exit 0 | `ci37469644335-engine-workflow-contract-r5.log` |
| `node --test tests/ci-workflow-contract.test.mjs` | 5 tests passed, exit 0 | `ci37469644335-frontend-workflow-contract-r5.log` |
| `git diff --check -- apps/bff/scripts/test_bff_explicit_cutover.py` | exit 0 | Command result; no source whitespace errors |

The first sandbox-only failures are retained as raw diagnostics. The full suite's two loopback-only failures are environmental; the complete same suite passed independently both in the authorized one-time local execution and the parent's replay. No broad unchanged root suite was repeated.

## Publication and stop boundaries

This delta is based on PR96's published r4 candidate and remains limited to the corrected retained assertions plus this report and raw evidence. The successor commit/tree and remote PR head/body readback are reported separately by the active worker; after publication the source, tests, evidence, and manifest are frozen for same-SHA reviews. No merge, Actions dispatch, provider access, DEV deployment, Production operation, IAM, credential, or live runtime action was performed.
