# DEV run 37055245620 — Frontend prepare diagnostic

Diagnostic-only record for the Stage 1 Frontend failure. Source tree inspected: PR75 candidate `09fc2435e1e16593ab38ec57259a0b9bc229d0ae`, tree `d989e999371a98944b18ff53612c71548c700d18` (Parent reports merged successor `d388` has the same tree). No source or fixture was edited, no test/build/install command was run, and no live action/provider was called.

## Recorded run evidence

The retained result artifact is `11248677083`; its local evidence is `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37055245620-result/`. The masked Action log remains at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37055245620-failed.txt`, SHA256 `d0d90dd8bdd9d860e0e87053fcda5981294372fe95a5be0b7891e5bebcd3af48`. The raw masked log and receipt contents were not copied into this report.

Allowlisted inspection found:

- `result.json`: `status=failed`, `component=frontend`, `reason=command-failed`, top-level `stage=prepared`, `mutation_may_have_happened=false`, checkpoint `4`, observed component status `unstarted`, and next action `reconcile-before-replay`. There is no `failure_diagnostic`, `exit_code`, or `timeout_class` field. Here `stage=prepared` is the persisted engine state, not the failing command stage.
- `state.json`: the Auth build is `SUCCESS`, identity verified, with a verified immutable-image receipt. `receipts/auth.json` exists, and its retained copy has identical bytes. `receipts/frontend.json` and `frontend.tgz` do not exist. The runtime `components` map is empty, so no runtime snapshot was started.
- The masked log contains the generic `command-failed` result and the string `59.11.7`, matching the workflow's Vercel CLI pin; this does not establish that the engine reached either Vercel CLI command. It contains no safe stage, numeric process exit, timeout-class, or visible project-read, `npm ci`, `vercel pull`, or `vercel build` command marker. No secret, token, argument value, environment value, stdout, or stderr is reproduced here.

`command-failed` means one child process returned nonzero to `support.run`; it is distinct from that wrapper's `provider-timeout` path. The possible failing operations include the initial Vercel project metadata `curl` as well as the three build commands below. The exact return code is absent from persisted evidence. No outer subprocess timeout is recorded. This does not exclude a CLI-internal timeout that returned nonzero.

## Source contract inspected

In `deploy/engine/providers.py`, Frontend preparation first verifies the Vercel project identity/configuration, then runs these subprocesses in order:

| Operation | Working directory | Timeout | Environment handling |
| --- | --- | ---: | --- |
| Vercel project metadata read (`curl` through `self.project()` / `self.api()`) | repository `ROOT` (`run()` default) | 30 seconds | Inherits the Action environment; the bearer credential is supplied on stdin to curl's config input. |
| `npm ci --ignore-scripts` | `ROOT/apps/frontend` | 600 seconds | Inherits the Action process environment. |
| `vercel pull --yes --environment=preview ...` | repository `ROOT` | 30 seconds | Inherits the Action environment and adds the target `NEXT_PUBLIC_API_URL` and `NEXT_PUBLIC_AUTH_URL`; the Vercel token is passed as an argument. |
| `vercel build ...` | repository `ROOT` | 900 seconds | Same inherited-plus-target-public-config environment; DEV does not add `--prod`. |

The adapter then requires `ROOT/.vercel/output/static/build-config.json` to match the target public URLs and archives `.vercel/output` plus `.vercel/project.json`. It creates neither a deployment nor an alias during this prepare path.

The workflow selects Node major `22`, installs `@actions/artifact@2.3.2` and global `vercel@59.11.7` with scripts disabled, and provides the Vercel credential variables to the prepare Action. `deploy/engine/profiles.json` also records CLI `59.11.7`. The application uses `apps/frontend/package-lock.json` lockfile version 3; `npm ci --ignore-scripts` consumes that exact application lock. The global Vercel CLI version is fixed, while its transitive dependency graph is not governed by the frontend lockfile.

The project metadata `curl` also omits `stage=`; the three `Provider.prepare()` build calls do as well. `support.run()` captures child stdout/stderr and records a nonzero exit code in the in-memory `Breakpoint`, but `Engine.result()` serializes failure metadata only when `exc.stage` is truthy. Thus this failure loses the command identity, numeric exit code, and (if an outer timeout occurs) timeout class. The result remains redacted, but it is not discriminating. Existing `test_prepare_diagnostics.py` covers Auth build diagnostics and safe result serialization when a stage is present; it does not cover Frontend subprocess failures. Existing Frontend fake acceptance covers successful build-only prepare, not per-command failure metadata.

## Causal disposition

The confirmed cause of missing stage/exit/timeout evidence is the omitted stage metadata on Frontend subprocess calls combined with the result serializer's `if exc.stage` condition. This explains why run 37055245620 cannot identify the failing command; it does **not** explain why that command returned nonzero.

The underlying command is unresolved among the initial project metadata `curl`, `npm ci`, Vercel `pull`, and Vercel `build`. The current plan's expected configuration is `rootDirectory=apps/frontend`, while both Vercel CLI calls use repository-root cwd and local `vercel.json` is under `apps/frontend` (there is no root `vercel.json`). This is a cwd/configuration hypothesis only; local source and the retained result do not establish how CLI 59.11.7 resolves it or that it caused this failure. The 30-second project-read and pull timeouts are also unproven concerns. The application dependency lock is present and used by `npm ci`; no evidence attributes this run to dependency drift or network access. Do not change cwd, timeout, lockfile, or workflow behavior on these hypotheses.

## Minimal safe proposal and next discriminating check

For a reviewed offline remediation, pass explicit stage labels to the existing calls, for example `frontend-project-readback`, `frontend-npm-ci`, `frontend-vercel-pull`, and `frontend-vercel-build`. Thread the project-readback label through `self.project()` / `self.api()` to the curl `run()` call. Do not log arguments, environment values, stdout, or stderr. The existing bounded result format can then report only `{stage, exit_code, timeout_class}`: a nonzero exit identifies the operation and code; a wrapper timeout identifies the operation and `subprocess-timeout`. Keep current command order, cwd, environment, and timeout values unchanged for this diagnostic change.

Add test-only fake-run regressions for each subprocess: simulate one nonzero exit and one `TimeoutExpired`; assert the typed result identifies only the stage/code or stage/timeout class, omits fake token and output sentinels, and stops before subsequent commands, Frontend receipt/archive creation, the ready barrier, or runtime mutation. In a selected Auth+Frontend fixture, assert the existing compatible Auth receipt is retained without another Auth submission. This is an offline observability test, not a fabricated live receipt or live success.

After source review and CI, Parent can decide whether a prepare-only Action entry is needed for a live diagnostic. The current normal `release` workflow proceeds from a successful prepare/barrier into runtime automatically, so dispatching it solely to diagnose Frontend is not read-only if prepare succeeds. Any such dispatch or retry remains Parent-owned. If authorized later, reuse artifact `11248677083` through the established receipt-applicability path so the already verified Auth receipt is not rebuilt; inspect the new bounded result artifact for the stage-only diagnostic before deciding any release action.

No implementation, manifest, Git metadata, publication, provider, credential, IAM, Production, or runtime state was changed by this diagnostic. No live retry or manual receipt was performed. Historical Auth cause remains separate from this Frontend failure.

## Offline stage-metadata repair and verification

After the initial diagnostic, the Owner authorized the bounded offline stage-metadata repair. `deploy/engine/providers.py` now passes four fixed labels through the existing `support.run()` path: `frontend-project-readback`, `frontend-npm-ci`, `frontend-vercel-pull`, and `frontend-vercel-build`. The project label is threaded through optional `Providers.project(stage=None)` and `Providers.api(..., stage=None)` parameters; calls outside Frontend prepare retain the default `None`. The command order, arguments, working directories, environment construction, timeouts (30/600/30/900 seconds), and existing output redaction are unchanged. No stdout, stderr, argv, credential, environment value, provider configuration, or workflow behavior is added to persisted output.

`FrontendPrepareDiagnostics.test_each_frontend_subprocess_failure_is_typed_redacted_and_stops_prepare` drives the real `Engine.prepare()` and production `support.run()` wrapper with a test-only `subprocess.run` fake. It covers nonzero exit and `TimeoutExpired` at each of all four stages. Each case checks the exact `{stage, exit_code, timeout_class}` allowlist, redaction of test-only token/project/team/config/command-output sentinels, preserved command order/cwd/timeout, and immediate stop. It also asserts no Frontend receipt or archive, no ready barrier or runtime guard, empty runtime components, and byte-identical retention of a compatible test-fixture Auth receipt with no Auth build submission. The fake receipt is local test data and is not presented as a live receipt.

Verification used Python 3.14.6 and explicit scratch `TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`:

| Exact command | Actual result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_prepare_diagnostics.py' -v` | 6 tests in 1.625s, `OK`; the new test executes 8 stage/failure combinations. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` | 62 tests in 136.391s, `OK`, including the actual engine/transport integration suite. This full run preceded only additional test assertions that enumerate forbidden serialized argv/environment/config strings; production source did not change afterward, and the updated diagnostics suite was rerun successfully. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 scripts/test_engine_workflow.py -v` | 6 workflow contract tests in 0.018s, `OK`. |
| `git diff --check` | exit 0, no output after all source and test edits. |

Two earlier test attempts failed only in new harness assertions: the first omitted the legitimate Auth image digest read from the expected trace, and the second expected the checked-in Frontend cwd after the test had intentionally patched the provider root to a temporary fake repo. Both expectations were corrected; the final diagnostics rerun passed. These were offline fixture assertion failures, not provider or Frontend command results. No BFF-owned file changed, so the canonical BFF script suite was not rerun; full Engine acceptance and workflow contracts passed. No live release, provider, build, Vercel/npm network, credential, IAM, Production, or Git publication was performed. The actual cause of Frontend command failure in run `37055245620` remains unknown; no cwd/timeout/configuration hypothesis was changed.
