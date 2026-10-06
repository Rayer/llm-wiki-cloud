# LWC-368 DEV prepare failure diagnosis and error preservation (r7)

Status: local source repair and offline verification complete on merged source `37a6b3267fa45b7637f5f0e2046b42597d8ac17b` (tree identical to local starting HEAD `13d05a6211297973d187eeaa62e3b789b4bd79a5`). This report does not claim that the historical prepare failure's root cause was recovered or that a live DEV validation was run.

## Historical attempt evidence

The preserved result artifact for workflow run `37484883149` records `reason=command-failed`, result `stage=prepared`, `failure_diagnostic.exit_code=2`, `failure_diagnostic.stage=unknown`, no timeout class, `component=worker`, `observed.component_status=unstarted`, checkpoint `0`, null prior/candidate, `mutation_may_have_happened=false`, and `allowed_next_action=reconcile-before-replay`. The downloaded result directory contains only `plan.json` and `result.json`; no state or ready receipt is present. The raw GitHub log is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/terminal-output/out-1791299671-68301-1810.log`; it does not retain the failing child's original stderr or a cause record. The parent discussion record is `docs/lwc-362-363/cac-dev-prepare-failure-37484883149.md` in the sibling discussion worktree.

The historical result therefore cannot distinguish a Pkl/render failure from another prepare command that returned exit 2. The local fake failure below proves a current error-transport defect; it does not reconstruct the historical command or cause. Parent's read-only Job observation (timeout 7200 seconds and the pre-attempt digest-pinned image) is consistent with the result's unstarted component and no mutation, but does not identify the failed prepare command.

## Causal reproduction and source change

The fresh-Worker regression invokes the real `.github/actions/deployment-engine/index.cjs`, `engine.py prepare`, `Engine.prepare()`, and `Providers.prepare_pipeline_config()`. It uses a synthetic admitted plan and fake image builder, fake `gcloud` digest lookup, and fake `make`; it makes no provider call. Before the change, fake `make config-dev` returned exit 2 with useful stderr, but the saved and printed result had only `stage=unknown` and `exit_code=2` and no `cause`. This is the same observed loss point in the current adapter: `stage='unknown'` is deliberately excluded from `support.run()`'s cause-capture stages.

`Providers.prepare_pipeline_config()` now reports the fixed stage `pipeline-config-prepare`, and that stage is added to the existing `_CAUSE_STAGES` allowlist. `support.run()` therefore applies the existing bounded, known-value-redacted `structured_cause()` behavior. The top-level reason, status, next action, exit code, timeout class, checkpoint, observed component status, and mutation flag remain the same on the fresh-Worker failure path; only its diagnostic stage and additive cause are populated.

The regression also checks two discriminators through the same real engine/Action route: a successful make with malformed JSON yields `pipeline-config-render-invalid` with no child-process cause, while valid synthetic rendered files reach `ready` and produce the expected receipt timeout/hash.

## Acceptance evidence

| Check | Evidence | Result |
|---|---|---|
| Reproduce stderr loss before repair | `dev-prepare-cause-red-fresh-r7.log` | Expected RED: actual Action result kept `command-failed`, exit 2, `unknown` stage, unstarted component, checkpoint 0, no mutation, and no cause. |
| Preserve useful bounded child error through adapter, engine, saved result, and Action | `dev-prepare-cause-engine-integration-green-r7-bounded.log` | 3/3 focused tests passed. Cause is `ChildProcessError` / `child-command-failed` at `pipeline-config-prepare`; useful text survives, the test token is masked, the 512-character cap is asserted, and stdout JSON equals saved `result.json`. Parser and success paths also pass. |
| Run full affected Python engine suite | `dev-prepare-engine-python-full-r7.log` | Exit 0; 110 tests passed in 1346.465 seconds. This suite includes engine prepare, transport, diagnostics, and build-submission tests. |
| Keep an exploratory retained-receipt probe classified separately | `dev-prepare-cause-red-seeded-receipt-r7.log` | This probe exercised a distinct retained-receipt branch that normalizes errors; it was not used as evidence for the historical fresh attempt because the historical result says `component_status=unstarted`. No change was made to that branch. |

Intermediate test-harness attempts are retained as raw logs and classified by their filenames: `dev-prepare-cause-engine-integration-fixture-lifetime-fail-r7.log` exposed a temporary-directory assertion lifetime mistake; `dev-prepare-cause-engine-integration-hash-assertion-fail-r7.log` exposed an expected-hash newline mistake; and `dev-prepare-cause-engine-integration-pre-bound-pass-r7.log` passed before the explicit truncation assertions were added. The final bounded regression and full engine suite above passed after those corrections.

## Scope and remaining validation

The source delta is limited to the Pipeline prepare stage label and its existing cause-stage allowlist, with one Action-level engine regression. SSOT/Pkl output values, source identity, image/build semantics, timeout defaults, provider configuration, and deployment workflow behavior were not changed. No commit, push, PR, merge, workflow dispatch, live Actions run, GSM/GCS/Cloud Run query or write, IAM operation, credential query, or Production operation was performed.

The historical root cause remains unknown until a future authorized DEV attempt retains its actual cause. The retained-receipt exploratory probe shows that its separate `Engine.prepare()` exception-normalization path does not preserve cause metadata; that path was left unchanged because the observed failure was unstarted and the fresh-Worker route matches it. Parent owns whether that distinct retained-receipt behavior needs a separate task and owns all later live DEV validation.

`docs/lwc-368/evidence/source-manifest-r7.tsv` records the modified source/test files, this report, and the retained local test logs; it excludes itself.
