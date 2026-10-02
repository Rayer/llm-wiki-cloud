# DEV 37071113945 Frontend prepare exception diagnosis

## Disposition

The persisted result does not contain enough information to identify the historical Python exception. Its generic `invalid-or-unreadable-input` reason was produced by `engine.main()` for several different exception classes. The incident's exact exception and stage therefore remain **unknown**; this report does not attribute it to a Vercel response, token, network, CLI, or filesystem cause.

This implementation follow-up was offline only. No provider, Actions, Cloud Build, Vercel, IAM, credential, runtime, or Production operation was performed. No retry or receipt was made. Current code and test results are recorded below; the historical incident cause remains unknown.

## Incident artifact and source identity

The result bundle was read from `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/dev-37071113945-result`. Only allowlisted result, state, and plan fields were printed. The Auth receipt body and secret-bearing environment values were not read or displayed.

| Evidence | Readback |
| --- | --- |
| Workflow source from plan | `d7ac3acfcd26dd675d6d83f3036aa0e1e4e30d0b` |
| Local checked-out review commit / tree | `81afcc999665a96183d35b37b25c5826aced8d8d` / `25a9aba0cb8680d0eb0194316b3f8c99f44873bb` |
| Persisted plan digest | Valid; state references that plan |
| Engine-content fingerprint | Matches the current checked-out production engine |
| Selected components | `auth`, `frontend` |
| Result | `failed`, reason `invalid-or-unreadable-input`, stage `prepared`, component `frontend`, checkpoint `1`, candidate absent, mutation false, next action `correct-input-and-resume` |
| Failure diagnostic | Absent: no stage, child exit code, or timeout class |
| State | `prepared`, sequence `1`, no runtime components, no Cloud Build handles |
| Frontend receipt | Absent; no ready barrier or runtime attempt |

The downloaded `auth.json` receipt is present. Per the parent readback recorded with this incident, its bytes match the prior receipt and no new Auth build was submitted. Prepared artifact `11254163503` exists but is not an all-ready artifact. Result/state/plan SHA-256 values at diagnosis time were respectively `ade98264544166dcb755e1450f0ef0b831b2d1ec34206ab85c00e258d563760f9`, `a6594711bef1b1f4fd2d4a678e4dfaab5ce423f4412e6cc825c13f0872c4643a`, and `f93f1f67b8e2d9e946e7c8928935c97d8ccb1cd259b3619ddc44192a71cce2df`.

## Source trace at original diagnosis

The registered `deploy-dev` caller passes `github.sha` into the reusable CD workflow. The normal release job checks out that source, invokes the deployment action for `prepare`, and supplies the normal step-scoped artifact and Vercel environment names. The action downloads a requested retained artifact into its reuse directory, then invokes `engine.py prepare` against a fresh release directory with the selected source, target, tag, and components. These static interfaces match the source and plan reported above; this inspection did not read runtime secret values.

The prepare order is Auth then Frontend. Auth's existing receipt is reused. `Providers.prepare('frontend')` begins with project readback at `providers.py:308`; `Providers.project()` calls the JSON API reader at `providers.py:103-114`. `Providers.api()` runs the stage-labeled curl command and then decodes its response at `providers.py:92-101`. A nonzero child exit or timeout from this call becomes a staged `Breakpoint` in `support.run()` at `support.py:54-81`. However, JSON decoding and response-shape operations after a successful child return can throw ordinary `ValueError`, `KeyError`, or `TypeError` without a stage.

At that source revision, `engine.main()` replaced caught non-`Breakpoint` exceptions with a generic `Breakpoint`, and the result writer had no cause field. The then-current handler also did not catch `AttributeError`; this shape edge escaped without a result.

The incident bundle has no traceback, exception class, or captured child error text. Its result/state/plan files and receipt directory do not contain another persisted cause field; the `failure_diagnostic` key is absent. The source locations where a future cause can be captured are available, but the historical cause itself is not. `auth.sh` and `bff.sh` already emit a bounded `LWC_ENGINE_FAILURE` marker with fixed stage, numeric exit code, and permission bit; `support.run()` consumes this only on the Auth build path and converts it to ordinary `Breakpoint` attributes. That marker does not carry an exception type or cause code. The Node action uses inherited stdio and exits with the child status; when `spawnSync` returns an error object, it currently discards its details. These are possible future forwarding boundaries, not evidence about this Frontend incident.

## Offline causal reproduction

Run:

```sh
PYTHONDONTWRITEBYTECODE=1 python3.14 docs/lwc-358/evidence/dev-37071113945-swallowed-input-repro.py
```

The reproducer calls production `engine.main()`, `Engine.prepare()`, `Providers.prepare()`, `Providers.project()`, `Providers.api()`, and `support.run()`. It substitutes only the subprocess transport with a fake successful curl response and bypasses Auth receipt digest validation so no external executable or provider is reached. It records only the fixed boundary label and Python exception class; it does not retain or print child arguments, environment, stdin, response bodies, or exception messages. The initial execution recorded below used temporary fixtures under `deploy/engine/tests`; they were automatically removed, and that historical execution is not represented as scratch-backed. Following the scratch-path correction, the same command was rerun with temporary fixtures only under `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch`, removing them on context exit. The imported engine remains the checkout's read-only source; its fixture plan is self-contained and does not require source-identity resolution. Both runs emitted the same three lines below; the scratch-backed run is the current reproduction evidence.

Actual output:

```text
case=json-object-missing-id underlying_class=KeyError boundary=frontend-project-readback fake_child=curl/exit0 result=invalid-or-unreadable-input stage=prepared component=frontend checkpoint=1 failure_diagnostic=absent
case=wrong-repository-field-type underlying_class=TypeError boundary=frontend-project-readback fake_child=curl/exit0 result=invalid-or-unreadable-input stage=prepared component=frontend checkpoint=1 failure_diagnostic=absent
case=invalid-json underlying_class=JSONDecodeError boundary=frontend-project-readback fake_child=curl/exit0 result=invalid-or-unreadable-input stage=prepared component=frontend checkpoint=1 failure_diagnostic=absent
```

This reproduces the information-loss class and the observed result shape, not the historical input. The incident result cannot distinguish these project-readback examples from other caught local input/read errors later in prepare. Since the persisted result has no diagnostic, it also cannot establish whether the project response was decoded or whether later Frontend steps ran successfully before a local input read failed. Direct missing Vercel authority is not supported by this reason: the current `api()` path reports it as the typed `missing-vercel-authority` breakpoint instead of converting it to `invalid-or-unreadable-input`.

An exploratory fake JSON `null` response raised `AttributeError` in `raw.get()` and escaped `main()` because that class is outside its catch tuple; it produced no result artifact. This is a separate response-shape edge, not an explanation for this run, which has a persisted generic result.

## Superseded proposal and implemented Owner direction

The earlier proposal below originally favored static message templates and suppressing all stderr. Owner direction 4-1702 superseded that suppression: retain the generic result reason and carry the real, useful underlying message where it can be bounded and selectively sanitized. A static template alone would discard the diagnostic information Owner requested.

The implemented additive result shape is:

```json
{
  "reason": "invalid-or-unreadable-input",
  "cause": {
    "exception_type": "KeyError",
    "exception_type_omitted": false,
    "stage": "frontend-project-readback",
    "code": "required-field-missing",
    "message": "'id'",
    "message_truncated": false,
    "message_omitted": false
  }
}
```

Only exact known exception classes map to exposed names and default codes. Unknown classes are not stringified; their type is marked omitted and code falls back to `unclassified-input-error`. Stage is restricted to the four fixed Frontend stage labels or `unknown`; codes are allowlisted. Existing `failure_diagnostic.exit_code` and `timeout_class`, generic reason, state, and allowed action remain unchanged. Cause messages are capped at 512 characters and carry explicit truncated/omitted booleans.

Python JSON decode errors retain the parser's real message and line/column position without exposing `JSONDecodeError.doc`, which could contain the entire response. Other mapped input errors retain their real message after targeted masking. Frontend child-command failures retain bounded stderr and type as `ChildProcessError`; timeouts retain bounded stderr and type as `TimeoutExpired`. The process stdout, complete argv, stdin, and environment are not serialized. Known credential values from the child environment are masked, along with Authorization header values, `--token` values, and known token-variable assignments. URLs, project/team identifiers, and other provider wording are not blanket-redacted. Successful command output is consumed by its caller and is not copied into cause/result.

`engine.main()` writes the cause into `result.json` and prints the same structured result, including the branch where failure occurs before an Engine object exists. The existing Node Action uses inherited stdio and exits with the child's status; an offline integration invokes that real wrapper around the real Python engine and confirms its printed result exactly matches the retained `result.json`. The CD workflow's always-run result upload retains the release directory. No extra marker protocol was added to Auth/BFF shell wrappers.

The offline regressions cover real `engine.main()` valid Frontend prepare, missing-field and malformed-JSON failures through the Action, all four Frontend subprocess stages with nonzero exit and timeout, message truncation, unknown cause class/code/stage fallback, environment-known token masking, and a different token value appearing only in `--token`. They check that ordinary stderr details, URLs and IDs survive; raw stdout and secrets do not; Auth receipt bytes stay unchanged on failure; no Frontend receipt/archive is produced on failure; and the barrier/runtime path is not reached.

This instrumentation can make a future failure distinguishable. It cannot recover the exception erased from run `37071113945`; the historical root cause remains unknown. The original failure logs and first red test logs are preserved unmodified.

## Content identity and worktree

The original diagnosis was made at `HEAD=81afcc999665a96183d35b37b25c5826aced8d8d`, tree `25a9aba0cb8680d0eb0194316b3f8c99f44873bb`; the local checkout remains at that same HEAD/tree with uncommitted changes. Current canonical manifest has 46 exact byte/mode rows, `content_sha256=20405d5d999aa22667ab270e15eab8a299db1010cd8fa3cd48d7e3e9a6784344`, and manifest-file SHA-256 `0fcef6c19bd80aae7091989021b89ac02fe11af9876d1af8cb308cde7a073a52`. Frozen r2 remains byte-identical at SHA-256 `838817cad154b0ea773c205f671362a6c9c0ad97c38ddf34f2508eb4fcc2488b`. Exact commands, results, preserved red logs and limits are recorded in `implementation-evidence-r2.md`. Evidence reports and execution logs remain outside the canonical manifest to avoid self-reference.

## Offline implementation and regression results

The focused diagnostics suite now exercises production code through the real Node Action. For a missing project `id`, the result carries `KeyError`, `required-field-missing`, and the original `"'id'"` message. For invalid JSON, it carries `JSONDecodeError`, `invalid-json`, and the parser's line/column detail without echoing the source body. Both the Action's inherited stdout and retained `result.json` have equal parsed cause values. A success case through `engine.main()` reaches `ready`, writes the Frontend archive and receipt, and preserves the Auth receipt. Failure cases keep the same Auth receipt and leave no Frontend receipt/archive.

The structured-cause tests verify valid code override, invalid code/type/stage fallback, no stringification of unknown exception classes, 512-character truncation flags, bearer/header/known environment token masking, and a separate non-environment `--token="different-secret"` negative case. All four named Frontend child stages are exercised for both nonzero exit and timeout. Safe stderr detail, ordinary project/URL context and bounded messages survive; child stdout and token values do not. Existing reason, exit code, timeout class, action, status, and mutation flag remain intact. The result upload contract is asserted at the actual Action result directory and the CD workflow's always-run release-directory artifact step.

Actual offline commands used explicit profile scratch and did not call a provider, Vercel, npm network, Cloud Build, Actions, IAM, credentials, runtime, Production, or Git publication:

| Exact command | Result |
| --- | --- |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_prepare_diagnostics.py' -v` | Final post-redaction run: 15 tests in 2.997s, `OK`. Log: `evidence/dev-37071113945-cause-focused-redaction-final.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s deploy/engine/tests -p 'test_*.py' -v` | Final post-redaction run: 71 tests in 137.768s, `OK`, including the real Action and transport regressions. Log: `evidence/dev-37071113945-cause-engine-full-final.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 python3.14 -m unittest discover -s scripts -p 'test_*.py' -v` | Final post-redaction run: 153 tests in 100.500s, `OK`, no filtering. Log: `evidence/dev-37071113945-cause-workflow-final.txt`. |
| `env TMPDIR=/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch PYTHONDONTWRITEBYTECODE=1 node --test deploy/engine/tests/artifacts.test.cjs` | 4 tests passed, 0 failed, 0 skipped. Log: `evidence/dev-37071113945-cause-artifact-transport.txt`. |
| `git diff --check` | exit 0, no output. |

Initial red runs are kept separately and are not counted as passes: `cause-focused.txt` and `cause-focused-rerun.txt` record early implementation/assertion failures; `valid-main-red.txt` records the first fake-root fixture missing `profiles.json`. The subsequent fixes and green logs above cover those specific issues. No raw live failure logs were altered.
