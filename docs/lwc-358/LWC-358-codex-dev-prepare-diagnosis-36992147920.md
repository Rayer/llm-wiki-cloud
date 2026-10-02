# LWC-358 DEV prepare failure diagnosis — run 36992147920

Status: offline diagnostic complete; no production root cause proven and no production code changed. This report is for Parent review before any further DEV attempt.

## Exact source and worktree identity

- Failed DEV run: 36992147920
- Source commit: f6e6a5e588088294c8829192b308b0813356f9da
- Selected components: auth,frontend
- Target: development; explicit release tag: dev-lwc-366-f6e6a5e58808
- Local implementer branch/HEAD: Rayer/LWC-358-engine-r2 / a90590974722248893a4676084093cf29951e1a4
- Current reviewed implementation content: 8c92173b50e3b55b9a27f4b9f288d430934a7fd6305d1e10ae0e31ca305bff39
- Compared the exact f6 source blobs against local HEAD for deploy/engine/providers.py, deploy/engine/support.py, deploy/engine/engine.py, deploy/engine/profiles.json, deploy/components/auth.sh, and deploy/components/common.sh; all match byte-for-byte. No alternate or unmerged implementation was used.

## Facts from the retained run artifacts

The downloaded result artifact 11220235845 contains only plan.json and result.json. The result says Auth / stage prepared / status failed / reason command-failed; last verified checkpoint is zero, Auth remains unstarted, prior and candidate are null, and mutation_may_have_happened is false. The safe next action is reconcile-before-replay. There is no ready-stage artifact or Auth receipt to reuse. Frontend prepare did not run after Auth failed. Runtime was skipped; no service update or rollback was needed.

The independently collected Cloud Build record for 4cac01f9-d2a5-41ea-95fe-3f47390482e8 says SUCCESS; Parent also confirmed the corresponding image was published and separately readable from Artifact Registry. Parent's read-only Auth status still showed the pre-run revision and the single 100% traffic route. These facts establish that a build and image publication happened, but do not establish which later command failed or that the Actions WIF principal could make the same registry read.

The retained Actions log contains no child-command stderr or stdout explaining the failure. It does show successful WIF authentication and then the structured command-failed result. It also contains a Node migration warning; there is no evidence tying that warning to the prepare failure. No credential file was opened. No secret values, raw command output, environment dump, image digest, or provider stderr are copied into this report.

## Source path and offline diagnosis

The exact f6 path is:

1. Providers.prepare('auth') in deploy/engine/providers.py invokes the real deploy/components/auth.sh build adapter.
2. auth_build_image in deploy/components/auth.sh runs gcloud builds submit, then resolves the source tag to a digest with gcloud artifacts docker images describe, validates the digest shape, and prints the immutable image reference.
3. Providers.prepare then calls valid_image, which independently describes that digest and checks that the returned digest matches.
4. support.run in deploy/engine/support.py captures stdout and stderr but discards both on failure. It maps recognized permission strings to permission-denied; otherwise several unrelated child failures become the same command-failed breakpoint.

I ran a TEST ONLY harness against the real Providers.prepare, real Auth shell adapter, and real support subprocess handling. Only gcloud, go, and timeout executables were faked; no network or provider was called. The fake records operation labels only and never prints image references or digests. It covers a valid receipt path, simulated submit failure after acceptance, an outer timeout after simulated acceptance, failed tag-to-digest lookup, malformed digest output, digest mismatch at valid_image, and valid_image read failure.

Exact command:

    python3.14 docs/lwc-358/evidence/prepare-diagnostic-harness.py

Observed typed outcomes:

- success -> receipt returned;
- simulated submit failure after acceptance -> command-failed / failed / reconcile-before-replay;
- simulated outer timeout after acceptance -> provider-timeout / unknown / reconcile-before-replay;
- digest lookup failure and malformed digest -> command-failed / failed / reconcile-before-replay;
- valid_image digest mismatch -> artifact-unusable / failed / correct-input-and-resume;
- valid_image read failure -> command-failed / failed / reconcile-before-replay.

The safe output is preserved in evidence/prepare-diagnostic-fake-scenarios.txt; the reproducible TEST ONLY harness is evidence/prepare-diagnostic-harness.py. These cases demonstrate that distinct post-build faults can yield the retained generic command-failed result. They do not identify which case occurred in run 36992147920. The simulated timeout also demonstrates that a remote build may have been accepted even when the caller gets an unknown result; it does not prove this happened in the real run.

## Conclusion and smallest discriminating check

No source defect is causally established. The observed successful build followed by a generic prepare failure is consistent with multiple later failures, including the tag digest lookup and immutable digest validation. Parent's separate registry read used a different principal/context and cannot discriminate the Actions-side path. The current result and log cannot distinguish these branches because the adapter boundary intentionally drops child output and no stage label is retained.

I made no code repair: adding retry/rebuild or changing permissions based on these facts could duplicate a published image or mask the actual failing command. No receipt was synthesized, and the successful image alone is not treated as a ready receipt.

Before any later authorized prepare/recovery attempt, the smallest useful read-only diagnostic is to run the two Artifact Registry describe operations under the same Actions WIF principal that ran the failed job: once for the source tag and once for the immutable digest already confirmed by Parent. Record only operation label, exit code, whether output matches the digest format, and whether the two values compare equal. Discard command stdout/stderr and do not log image/digest strings, arguments, environment, principal credentials, or secrets. Separately, a future narrowly reviewed adapter change could preserve a bounded stage label (build-submit, tag-digest-resolve, digest-validate) and exit/timeout class in the typed result, without preserving raw output. Parent should review that diagnostic approach before another DEV run or implementation change.

## Scope and verification

Ran the offline harness above and python3.14 -m py_compile deploy/engine/providers.py deploy/engine/support.py (exit 0). No production source, workflow, manifest, or receipt was modified. No Git publication, workflow dispatch, provider, credential, IAM, or tag operation was performed. No LWC-366 worktree or shared package worktree was touched.
