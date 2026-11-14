# Manual TPM Release Notes package preparation

This is a private transport and validation step for a package the TPM editor has already authored. It does not generate or edit note content. The accepted input is a ZIP containing `release-notes-manifest.json` and the locale JSON files named by its existing manifest; the existing `release_notes.validate_package` contract checks the manifest schema, exact ready-plan SHA, environment, version, locales, file hashes, and optional historical provenance.

Run the preparation workflow from the `develop` ref only. First identify the exact, unexpired `lwc-ready-development-<run-id>-<attempt>` artifact produced by a successful `workflow_dispatch` of `.github/workflows/deploy-dev.yml`. The helper confirms the artifact ID, name, producer run ID and attempt, workflow path, event, completed-success result, `develop` branch, and the artifact digest. It then downloads that exact artifact and accepts only a plan whose canonical `selected` list includes `bff`.

Keep the TPM package directory private. The operator helper reads only the three allowlisted package filenames, rejects symlinks, extra files, invalid manifests, and packages exceeding its bounds, and submits the package as a bounded base64 ZIP through `gh api` stdin. It does not print package contents or construct a shell command from them. The workflow safely extracts only flat manifest/locale files, validates them against the exact ready plan before upload, creates a private Actions artifact with the closed name `lwc-release-notes-<product-version>-development-<run-id>-<attempt>`, and verifies the upload action digest against the artifact API digest before returning the receipt. Retention is explicitly selected as 30 or 90 days.

Use the real successful Deploy Development run ID and attempt from the parent-provided Actions receipt, then query the exact ready artifact ID by its closed name. The current reader artifact is a result artifact, not a ready artifact, and cannot substitute for this input. This does not download or print artifact contents:

```sh
: "${LWC_478_READY_RUN_ID:?Set this from the exact successful Deploy Development run receipt}"
: "${LWC_478_READY_RUN_ATTEMPT:?Set this from the exact run attempt}"
READY_RUN_ID="$LWC_478_READY_RUN_ID"
READY_RUN_ATTEMPT="$LWC_478_READY_RUN_ATTEMPT"
[[ "$READY_RUN_ID" =~ ^[1-9][0-9]*$ && "$READY_RUN_ATTEMPT" =~ ^[1-9][0-9]*$ ]]
READY_ARTIFACTS_JSON="$(mktemp)"
trap 'rm -f "$READY_ARTIFACTS_JSON"' EXIT
gh api "repos/Rayer/llm-wiki-cloud/actions/runs/$READY_RUN_ID/artifacts?per_page=100" > "$READY_ARTIFACTS_JSON"
READY_NAME="lwc-ready-development-${READY_RUN_ID}-${READY_RUN_ATTEMPT}"
READY_ARTIFACT_ID="$(jq -er --arg name "$READY_NAME" \
  '[.artifacts[] | select(.name == $name and .expired == false) | .id] | if length == 1 then .[0] else error("expected one exact ready artifact") end' \
  "$READY_ARTIFACTS_JSON")"
[[ "$READY_ARTIFACT_ID" =~ ^[1-9][0-9]*$ ]]
PRIVATE_TPM_PACKAGE_DIR=/private/path/to/tpm-authored-package
python3 deploy/engine/release_notes_workflow.py dispatch \
  --repository Rayer/llm-wiki-cloud \
  --ready-artifact-id "$READY_ARTIFACT_ID" \
  --package-dir "$PRIVATE_TPM_PACKAGE_DIR" \
  --retention-days 90
```

The parent supplies those exact IDs from a completed successful run and the private directory for the TPM-authored package. The package directory must contain `release-notes-manifest.json` plus exactly the locale bodies listed by its `files` map; the manifest must already bind to the ready plan's SHA. The `gh` CLI must already be authenticated for the repository with permission to dispatch the workflow and read its run/artifact receipts. This command acquires no credentials. Do not put the package body or base64 payload in the terminal command, shell history, logs, or a tracker comment.

On success the helper prints a receipt containing the exact private package artifact ID, closed artifact name, SHA-256 digest, retention expiry, producer workflow run/attempt, product version, and ready-plan artifact ID. Supply that artifact ID to the normal Deploy Development workflow as `release_notes_artifact_id` with `release_notes_action=run` or `retry` and the same canonical BFF-selected plan. The existing deployment engine remains responsible for release-time validation, original publication-time capture, product/runtime operation, and Notes publication; a failed Notes publication continues to use the existing Owner retry, explicit skip, or stop disposition, and resend remains an independent no-redeploy operation.

The existing workflow selection gate reads `plan.json` after release preparation and runs Notes failure disposition, package validation, publication-time capture, content publication, or explicit skip marker only when `plan.selected` contains `bff`. Frontend-only and worker-only plans therefore proceed with the default `release_notes_action=run` without a Notes artifact, a BFF marker, or a false BFF success receipt. Recovery marker publication reads the downloaded canonical plan in the same way. Component selection remains explicit in the deployment engine; this preparation flow invents no empty-component default.

No real artifact upload was run while implementing or testing this path. Test packages are synthetic fixtures only and are not historical DEV notes or product acceptance evidence.
