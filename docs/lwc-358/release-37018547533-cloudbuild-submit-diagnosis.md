# DEV run 37018547533: Cloud Build submit failure diagnosis

Status: offline diagnosis only. The exact cause of this run's `gcloud builds submit` exit code is **unknown**. No deployment source, workflow, manifest, receipt, provider state, or Git history was changed.

## Identity and observed outcome

- Failed run: `37018547533`, Auth, release `dev-lwc-366-6088dc310689`.
- Source: `6088dc3106890c0bff51507eaaa6db84fddc89e2`.
- Local `HEAD`: `8ee9bed045c79dc72ad2d7d9ff551359e15b54c5`; its tree is `fe1afa8d0b262a27402ce0b1cdb8429b36e71719`, identical to the source commit's tree. The local current tree therefore contains the exact deployed source content.
- Parent's retained `result.json` at `/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/release-37018547533/result.json` reports `stage=prepared`, `reason=command-failed`, `failure_diagnostic.stage=build-submit`, `exit_code=1`, `timeout_class=null`, `last_verified_checkpoint=0`, no candidate, `mutation_may_have_happened=false`, and Auth `unstarted`. Ready and runtime were skipped.
- Parent separately reconciled Cloud Build `3e25e3f7-eb57-4d00-85c7-54fdd601a9b9` as `SUCCESS`, with the `6088` image published. Parent's read-only runtime check found Auth revision `00029-rkw` still receiving 100% traffic. These are Parent's supplied read-only findings; no provider was queried in this diagnostic.

The successful remote build and failed local CLI command are compatible: a synchronous `gcloud builds submit` can wait for build completion and then still fail while handling build logs. The retained run result has no child stderr, SDK version, Cloud Build logging mode, or log-read status with which to connect that mechanism to this specific run.

## Source path and captured data

At the exact source tree, `deploy/components/auth.sh:36-45` invokes `timeout ... 600s gcloud builds submit ... --quiet --suppress-logs`. It captures the command's stderr into `detail` and sends stdout to `/dev/null`. On nonzero exit it emits only the typed `build-submit` stage, numeric exit code, and a bounded permission marker. `deploy/engine/support.py:48-77` parses that marker and stores only typed fields; raw stderr is not included in the result. The parent process timeout is 1800 seconds in `deploy/engine/providers.py:95-104`, so the recorded exit 1 with no timeout class is distinct from the engine's subprocess-timeout path.

If the build command returns zero, Auth next resolves the source tag with `gcloud artifacts docker images describe`, validates the digest, and returns an immutable image reference. The engine writes a prepare receipt only after that adapter and its image usability check succeed (`deploy/engine/engine.py:121-164`). Thus this run stopped before an Auth receipt or ready barrier. `deploy/components/bff.sh:36-43` uses the same synchronous `builds submit --quiet --suppress-logs` pattern; that sibling path was audited but did not fail in this run.

The `cd.yml` job uses the pinned `setup-gcloud` action at `.github/workflows/cd.yml:75` without an explicit SDK version input. The local machine has Google Cloud SDK `579.0.0`; that identifies only the SDK used for the offline source inspection and probe, not the SDK version on the failed Actions runner.

## SDK path and offline reproduction

In the locally installed SDK 579.0.0:

- `lib/surface/builds/submit.py:269-276` calls `submit_util.Build` with the `async` and `suppress_logs` options.
- `lib/googlecloudsdk/command_lib/builds/submit_util.py:895-925` returns immediately only for async submission. The normal synchronous path sets `out=None` when `suppress_logs` is true, then still calls `CloudBuildClient.Stream`.
- `lib/googlecloudsdk/api_lib/cloudbuild/logs.py:498-551` starts a log tailer, waits for a terminal Cloud Build status, stops and joins the tailer, then raises any tailer exception. A successful status does not suppress that exception.
- In the GCS tailer path, `logs.py:323-392` raises on a synthetic HTTP 403, and `logs.py:415-435` maps that status to `DefaultLogsBucketIsOutsideSecurityPerimeterException`. The GCS tailer is selected for logging modes other than `NONE`, `STACKDRIVER_ONLY`, and `CLOUD_LOGGING_ONLY`; the actual run's returned mode was not retained.
- `--quiet` is present on the shell command. The SDK's global flag description at `lib/googlecloudsdk/calliope/actions.py:262` says it disables interactive prompts; the reviewed submit helper does not pass or use it. The inspected code provides no evidence that `--quiet` disables waiting or changes the log-read path. `--suppress-logs` suppresses streamed output, not the synchronous wait or tailer.

I exercised the installed SDK's real `CloudBuildClient.Stream` and GCS-tail error-handling classes with a fake local Cloud Build API and a fake log transport. The fake API returned `WORKING`, then `SUCCESS`; the fake transport returned no additional bytes on the initial poll and raised a synthetic 403 on the final log read. `out=None` was passed, and the SDK still raised `DefaultLogsBucketIsOutsideSecurityPerimeterException` after the fake API reached `SUCCESS`.

Exact command:

```text
PYTHONDONTWRITEBYTECODE=1 python3.14 docs/lwc-358/evidence/cloudbuild-sdk-579-offline-log-access-repro.py
```

Recorded output is in [cloudbuild-sdk-579-offline-log-access-repro-final.txt](evidence/cloudbuild-sdk-579-offline-log-access-repro-final.txt); the test-only probe source is [cloudbuild-sdk-579-offline-log-access-repro.py](evidence/cloudbuild-sdk-579-offline-log-access-repro.py). The completed probe exited 0 with two fake status reads, two fake log requests, fake final build status `SUCCESS`, `stream_output_argument_is_none=true`, and the SDK exception above. All network boundaries were replaced with local fakes; no request reached Google or another network service.

Two earlier harness/import attempts are preserved as [repro-v2 output](evidence/cloudbuild-sdk-579-offline-log-access-repro-v2.txt) and [initial probe output](evidence/cloudbuild-sdk-579-offline-log-access-probe.txt). They failed because of probe-counter and SDK import-path setup errors; they are not counted as successful tests and were not removed.

## Causal disposition

**Confirmed:** the job's submit child returned exit 1 without a timeout classification; its result omitted raw child output; the remote Cloud Build completed successfully and published the image; Auth runtime stayed on the prior revision. The local SDK 579.0.0 can produce a CLI failure after a successful build when its final GCS log read receives 403, even with suppressed log output.

**Consistent but unproven:** the failed Actions CLI may have encountered a post-completion log-tail error. The workflow does not pin or record the runner SDK version, and the result artifact does not retain the returned logging mode, stderr, exception class, or log-read response. Other CLI/API/client-side failures also remain possible.

**Not established:** that the failed runner used SDK 579.0.0; that its log bucket returned 403; that VPC Service Controls, IAM, or any particular permission caused the failure; or that log access rather than another submit path caused exit 1. No permission or IAM change is indicated by this evidence.

## Minimal next-step proposal

Do not change build, runtime, or permission behavior based on this correlation. If a later authorized diagnostic change is requested, the smallest useful addition is a closed, safe diagnostic enum derived only from recognized SDK error classes or exact allowlisted markers (for example, `build_log_read_denied`, `build_api_error`, or `unclassified`) plus the SDK version. Preserve only that enum, stage, exit code, and timeout class; discard all raw output and arguments. Unknown text must map to `unclassified`. This would help discriminate a later run but cannot recover the missing cause for run `37018547533`.

Switching to `--async` and separately polling the Cloud Build API could remove the CLI's coupled log-stream failure path, but it changes submission/completion behavior and would require a durable build identifier and explicit reconciliation semantics. It is not a minimal diagnostic and is not proposed for this incident.

## Scope and verification

- Offline SDK reproduction command above: exit 0; output preserved alongside the test-only probe.
- No repository product test suite was rerun because no product code changed.
- No provider or Actions call, rebuild, runtime mutation, credential/IAM change, Production action, receipt fabrication, Git publication, or cleanup was performed.
- Existing unrelated evidence and preexisting untracked files were left untouched.
