# Manual TPM Release Notes package preparation

This is a private transport for a package the TPM editor has already authored. It does not generate or edit content. The accepted ZIP contains the existing `release-notes-manifest.json` and the locale JSON files named by that manifest; `release_notes.validate_package` checks the existing schema, exact Ready or retained-result plan hash, DEV environment, version, locale set, file hashes, and historical provenance.

Use the already registered `.github/workflows/deploy-dev.yml` manual entrypoint on `develop`. Its explicit `prepare_release_notes_package=true` mode skips the release, frontend-config, diagnostic, and fast-forward-eligibility jobs, then calls `.github/workflows/prepare-release-notes-package.yml` as a same-commit reusable workflow. The helper dispatches to the registered Deploy Development workflow; do not dispatch the reusable workflow file directly. The caller event contains `operation=release`, `release_notes_action=stop`, the exact `artifact_id`, and the package payload. The reusable workflow receives only the Ready artifact ID, request ID, and retention setting through `with`; it reads the original dispatch event payload from `GITHUB_EVENT_PATH`, so the package bytes are not copied into reusable-workflow inputs, step environment, shell script, argv, or logs. [GitHub documents that a `workflow_call` event receives the caller's event payload, ref, and SHA](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_call).

## Start from the exact pre-runtime Notes gate

On `develop`, start the normal Deploy Development release with the intended canonical BFF-selected component set, `release_notes_action=run`, and an empty `release_notes_artifact_id`. The engine prepares the release and uploads `lwc-ready-development-<run-id>-<attempt>` before the Notes artifact-ID check. That check records `artifact-id-required` with `product_status=not_started` and ends the run before the deployment engine's runtime action; no publication time or product rollback is created.

The reusable package workflow accepts only that specific completed-failure case. It resolves the exact Ready artifact's producer and the same attempt's final result artifact, downloads both by artifact ID, and checks that their `plan.json`, `state.json`, and `result.json` bytes match. It validates the native release plan ID, source/executor binding, canonical BFF selection, ready checkpoint with no runtime component mutations, and the single retained `artifact-id-required` Notes evidence record with the exact plan SHA. A successful aggregate, different failure, missing result artifact, altered plan/checkpoint, or any runtime-started state is rejected. This avoids reusing an unrelated or completed release while still accepting the intentional Notes-only stop.

Find the exact Ready artifact in that failed run without printing its contents:

```sh
: "${LWC_478_READY_RUN_ID:?Set this from the original Deploy Development run receipt}"
: "${LWC_478_READY_RUN_ATTEMPT:?Set this from the same run attempt}"
READY_RUN_ID="$LWC_478_READY_RUN_ID"
READY_RUN_ATTEMPT="$LWC_478_READY_RUN_ATTEMPT"
[[ "$READY_RUN_ID" =~ ^[1-9][0-9]*$ && "$READY_RUN_ATTEMPT" =~ ^[1-9][0-9]*$ ]]
READY_ARTIFACTS_JSON="$(mktemp)"
trap 'rm -f "$READY_ARTIFACTS_JSON"' EXIT
gh api "repos/Rayer/llm-wiki-cloud/actions/runs/$READY_RUN_ID/artifacts?per_page=100" > "$READY_ARTIFACTS_JSON"
READY_NAME="lwc-ready-development-${READY_RUN_ID}-${READY_RUN_ATTEMPT}"
READY_ARTIFACT_ID="$(jq -er --arg name "$READY_NAME" \
  '[.artifacts[] | select(.name == $name and .expired == false) | .id] | if length == 1 then .[0] else error("expected one exact Ready artifact") end' \
  "$READY_ARTIFACTS_JSON")"
[[ "$READY_ARTIFACT_ID" =~ ^[1-9][0-9]*$ ]]
PRIVATE_TPM_PACKAGE_DIR=/private/path/to/tpm-authored-package
python3 deploy/engine/release_notes_workflow.py dispatch \
  --repository Rayer/llm-wiki-cloud \
  --ready-artifact-id "$READY_ARTIFACT_ID" \
  --package-dir "$PRIVATE_TPM_PACKAGE_DIR" \
  --retention-days 90
```

The helper reads only allowlisted package files, rejects symlinks, extra paths, invalid manifests, and oversized archives, then sends the bounded ZIP to `gh api` through stdin. It does not print package contents or place the payload in shell history or command arguments. It dispatches to Deploy Development with the package-only mode, explicit `release` plus `stop`, exact Ready ID, retention, request ID, and `package_zip_base64` in the event body. The reusable package workflow reads the base64 directly from the caller's runner event file; the base64 is not forwarded through reusable-workflow inputs, interpolated into a step environment, run script, argument, or diagnostic. It safely extracts to private staging, validates against the exact Ready plan, uploads a closed-name private Actions artifact with 30 or 90 day retention, and checks the pinned uploader's raw 64-hex output against the API's normalized `sha256:<hex>` digest before returning a receipt.

## Resume the same release plan

After package preparation succeeds, the `Verify the uploaded artifact and emit its exact receipt` step prints a safe `resume_inputs` object alongside the package artifact receipt. It contains the exact Ready artifact ID as `artifact_id`, the original source SHA, release tag, selected components, release-source mode, DEV artifact reference, Auth schema/source tuple when present, and the new Notes package artifact ID. Those values are plan metadata, not Notes content. Use them for the normal Deploy Development release dispatch on `develop`:

```sh
gh workflow run deploy-dev.yml --ref develop \
  -f operation=release \
  -f source_sha="$LWC_478_SOURCE_SHA" \
  -f release_tag="$LWC_478_RELEASE_TAG" \
  -f components="$LWC_478_COMPONENTS" \
  -f artifact_id="$LWC_478_READY_ARTIFACT_ID" \
  -f release_notes_artifact_id="$LWC_478_PACKAGE_ARTIFACT_ID" \
  -f release_notes_action=run \
  -f release_source_mode="$LWC_478_RELEASE_SOURCE_MODE" \
  -f auth_schema_version="$LWC_478_AUTH_SCHEMA_VERSION"
```

Pass the returned `dev_artifact_id` and Auth source fields when they are present in `resume_inputs`. Do not substitute a new source, tag, component selection, Auth tuple, or different Ready artifact. The existing wrapper passes `artifact_id` to the deployment engine as retained Stage 1 input. The engine admits the new invocation, checks the complete compatible release identity against that Ready artifact, requires an untouched `ready` checkpoint with empty runtime components, and restores the original plan bytes and build handles before execution. A mismatch or a checkpoint that has progressed into runtime is rejected by the native engine; it cannot silently create a new Notes-bound plan.

The initial missing-package run does not create the release `published_at` record. On the resumed release, existing native fences run first; the original publication time is captured only after the runtime operation succeeds, and then the Notes package and marker are published. If Notes publication fails after runtime, the existing explicit Owner retry, skip, or stop disposition applies; resend remains an independent no-redeploy action.

## Import a genuine historical DEV version without reusing Ready

Historical import uses a different exact source: the completed successful DEV `lwc-result-development-<run-id>-<attempt>` artifact, never a Ready artifact or Production result. The producer downloads that exact result, verifies its metadata and successful DEV workflow run, validates `plan.json`, `state.json`, `result.json`, and `receipts/bff.json` with the native retained-operation validator, and takes the plan bytes only from that archive. It rejects an expired, failed, wrong-run/attempt/source, non-DEV, marker-sidecar, or operation-mismatched result. The historical package must include all four existing provenance fields (`original_published_at`, `historical_source_record_ref`, `historical_source_record_sha256`, `imported_at`) and its `release_plan_sha256` must match the exact retained plan. The historical upload name embeds the closed operation and exact result artifact ID; the resend consumer checks that name against the same result ID and operation before publication.

The TPM editor authors the package from the canonical historical article and records its original time. For LWC-A-58, the package calendar version is `2026.11.1`, the original time is `2026-11-01T22:58:30+08:00`, and the historical source SHA is `21668758391871a9e4e99cf9319a8aebb1a09051`; the authentic DEV plan tag `2026.11.1-dev.6` identifies runtime source and is not the Notes calendar version. Do not substitute Production artifact `13087949185` for the DEV result artifact `13087604252`, use import time as publication time, or treat synthetic test package content as a real article.

After the TPM-authored package is ready, use the exact retained successful DEV result artifact and operation:

The helper places the historical source in the existing `release_notes_result_artifact_id` field and its operation in `release_notes_operation`, while leaving `artifact_id` empty. Ready preparation uses `artifact_id` and leaves the result field empty; the workflow rejects both-source and no-source requests. These inputs are already part of the registered manual entrypoint, so this path does not add another dispatch field.

```sh
: "${LWC_478_A58_DEV_RESULT_ARTIFACT_ID:?Set this to the verified exact DEV result artifact ID}"
PRIVATE_TPM_PACKAGE_DIR=/private/path/to/tpm-authored-historical-package
python3 deploy/engine/release_notes_workflow.py dispatch \
  --repository Rayer/llm-wiki-cloud \
  --history-result-artifact-id "$LWC_478_A58_DEV_RESULT_ARTIFACT_ID" \
  --history-operation deploy \
  --package-dir "$PRIVATE_TPM_PACKAGE_DIR" \
  --retention-days 90
```

The returned private package receipt contains the uploaded artifact ID, exact history-qualified artifact name, digest, expiry, and source result ID/operation. To import only that history row, select the normal no-deploy Notes resend action with both exact artifact IDs and the same operation:

```sh
gh workflow run deploy-dev.yml --ref develop \
  -f operation=notes-resend \
  -f release_notes_action=run \
  -f release_notes_result_artifact_id="$LWC_478_A58_DEV_RESULT_ARTIFACT_ID" \
  -f release_notes_artifact_id="$LWC_478_HISTORICAL_PACKAGE_ARTIFACT_ID" \
  -f release_notes_operation=deploy
```

The resend validates the same retained operation and package binding, uses the package's original publication time, and merges the historical version into the existing index without capturing or replacing the current marker. It does not deploy or redeploy product services. Owner still chooses the existing retry, explicit skip, or stop disposition if a real package/resend stage fails; package retention expiry or any identity mismatch fails closed.

No real artifact upload was run while implementing or testing this path, and no manual workflow dispatch was performed. GitHub must still confirm that the selected `develop` version of the already registered Deploy Development workflow accepts and routes the added package-only inputs. Unit fixtures are synthetic and do not represent genuine historical DEV notes or product acceptance evidence.
