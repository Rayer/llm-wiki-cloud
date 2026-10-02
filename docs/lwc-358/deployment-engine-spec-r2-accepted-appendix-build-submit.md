# Accepted r2 appendix — Auth/BFF Cloud Build completion

This appendix records Owner acceptance 3584-1660 + SSOT. It supplements the frozen r2 specification; `deployment-engine-spec-r2.md` remains byte-for-byte unchanged. This is an accepted completion-contract change for the existing Auth/BFF Stage 1 build path, not a new runtime or provider authority.

## Submit and completion contract

Auth and BFF submit Cloud Build asynchronously with an explicit project and location, `--async`, and machine-readable output. Preserve the existing build location as `global`: the previous CLI's implicit Cloud Build default is `global`, while an explicit `--region=global` also prevents a runner-level `builds/region` property from silently changing the target. `--quiet` remains prompt-only. No submit or prepare path tails build logs or waits through the log-streaming submit path.

The accepted submission response must identify the exact `(project_id, location, build_id)` tuple. Cloud Build may render the project segment of `name` as the numeric project number rather than the configured project ID. Before submit, resolve the configured project with the read-only `gcloud projects describe PROJECT_ID --format=json --quiet` call; require its `projectId` to exactly match the configured ID and its `projectNumber` to be a canonical decimal number. The build response's `projectId` must still exactly match the configured ID, and its resource name must be `projects/{configured-project-id-or-that-project's-verified-number}/locations/{location}/builds/{build_id}`. A numeric name segment is accepted only when it equals the authoritative number returned for that exact configured ID; arbitrary numeric aliases are rejected. Persist the verified tuple before polling. Every status read uses that exact build ID, project ID, and region; re-resolve the authoritative project mapping for resumed runs and validate the returned project ID, resource name alias, location, and ID again. Never describe a build using an unverified or mismatched tuple. An identity lookup error before submit fails before any build is created; on resume it leaves the existing ID unknown and prevents resubmission. This lookup is read-only and does not change IAM or Cloud Build configuration.

The engine persists a `submitting` checkpoint before the create request. Once it has a verified ID, it durably uploads the `submitted` checkpoint before the first status read. Terminal state, last observed status, and any bounded failure metadata are saved into the current result. The result's `builds[component]` record contains only the project ID, location, build ID (when syntactically safe), identity-verification flag, sanitized reported project/location on a mismatch, status, last observed status, and poll outcome. Stage, numeric exit code, and timeout class are kept in the separate bounded `failure_diagnostic` record. Results never include command arguments, provider stdout/stderr, build log content, environment values, or credentials.

Status polling is status-only: `gcloud builds describe BUILD_ID --project PROJECT --region LOCATION --format=json --quiet`. It has a 600-second monotonic deadline, a five-second interval, and at most a 30-second subprocess timeout capped by the remaining deadline, plus a 120-read ceiling. `PENDING`, `QUEUED`, and `WORKING` continue polling. `SUCCESS` proceeds to the existing source-tag lookup, immutable digest validation, and receipt write. No receipt or ready barrier is created before those checks and every selected component receipt is usable.

`FAILURE`, `INTERNAL_ERROR`, `TIMEOUT`, `CANCELLED`, and `EXPIRED` are terminal build failures. The result retains their exact verified build tuple for on-demand diagnosis. A later explicit prepare invocation may submit a replacement only after the prior ID has been observed terminal; the failed invocation itself never automatically resubmits. `PENDING`/`QUEUED`/`WORKING` at the poll deadline, `STATUS_UNKNOWN`, an unreadable status response, or a status-API timeout produces an unknown result with the existing ID. Resumption re-reads that same ID; it must not submit another build. If submit exits unsuccessfully before a verified ID is returned, keep a durable `SUBMITTING`/unknown marker without an invented ID, block resubmission, and require reconciliation. Do not synthesize an ID or a receipt.

A verified `SUCCESS` followed by digest lookup/validation failure keeps the successful build ID in the state and result. A later explicit prepare invocation reuses that build and retries only the existing tag/digest resolution path; it does not create a second build.

## Stage 1 checkpoint resume boundary

The formal `artifact_id` prepare input may point to a retained Stage 1 artifact downloaded into a new Actions job. An exact same-attempt resume is admitted only when the retained plan and state are both present and valid: the plan digest verifies, `state.plan` names that plan, and the retained plan exactly equals the newly admitted plan. Exact plan equality binds source, target/configuration, release tag, selected components, and engine identity/content. The Stage 1 state must still be `prepared` or `ready` and contain no runtime component checkpoints. Only then may the engine restore that `state.json` (including build IDs, submit-unknown markers, poll state, and sequence) into the new release directory before continuing prepare. A runtime-started or mismatched/corrupt checkpoint fails closed before build submission or runtime mutation.

A different plan is receipt reuse, not checkpoint resume. It must never import the old plan's build state. Existing individually compatible and usable receipts remain eligible for reuse, including selection enlargement and tool-only plan changes. If a prior build record has no compatible receipt and is not known to be terminal (`FAILURE`, `INTERNAL_ERROR`, `TIMEOUT`, `CANCELLED`, `EXPIRED`, or an explicit `SUBMIT_REJECTED` before a build was created), its handle applicability to the new plan is undefined and prepare stops with a typed breakpoint before submitting. This includes `SUBMITTING`/`SUBMIT_UNKNOWN` without an ID, pending or status-unknown IDs, unrecognized statuses, and `SUCCESS` without a completed applicable receipt. A known terminal failure remains eligible for a later explicit prepare attempt under the existing retry contract. The `--dev` provenance path remains separate and does not import DEV runtime state.

## On-demand logs

Logs are not a readiness gate. When a human requests log diagnosis and the build is available in Cloud Logging, use the exact verified build ID and project filter:

```sh
gcloud logging read \
  'resource.type="build" AND resource.labels.build_id="BUILD_ID"' \
  --project="PROJECT_ID" --limit=100 \
  --format='table(timestamp,severity,logName)'
```

The default result shows entry metadata only; it does not print or retain raw log payloads. Cloud Build `LEGACY` logging may not produce Cloud Logging entries. A missing match is not evidence that the build is pending or failed; the verified Cloud Build status remains authoritative. Do not change Cloud Build logging mode, IAM, or log-bucket configuration as part of this contract. No raw logs are copied into Actions artifacts or `result.json`.

## No change to release boundary

The async submission and status check remain in Stage 1. They do not mutate Cloud Run, Vercel, traffic, or aliases; do not change the per-component build inputs or immutable image receipt; do not bypass the all-selected ready barrier; and do not create a release tag. Production continues to consume applicable DEV Auth/BFF artifacts without rebuilding them.
