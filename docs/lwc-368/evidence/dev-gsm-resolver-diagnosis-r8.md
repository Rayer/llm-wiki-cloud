# LWC-368 GSM resolver failure reporting (r8)

Status: bounded local source repair and offline verification complete. No commit, push, PR, live provider call, secret payload access, Actions run, IAM operation, or credential access was performed.

## Source and observed failure

The worktree started at local commit `729d775303ce9b4371e77d396fc1e70206602bb3` with tree `34eb986a5b70508929d73bde2e783bdf50a4e4d6`. The locally available source commit `54a80197e797eafc612acaf43351935a63494d89` resolves to that same tree; no fresh remote checkout of that commit was made.

The preserved result for DEV run `37496572138` records a `pipeline-config-prepare` child failure at exit 2. Its cause message is the resolver's generic `access failed or payload is empty`, so that historical attempt does not distinguish an SDK access failure from an empty payload. Parent readback says Secret Manager metadata showed version 1 enabled and a bounded audit metadata query returned no rows; neither establishes workflow access or payload contents. This local repair does not infer the historical cause.

## Change

`googleSecretReader` now leaves Google API errors available to the resolver and reports an empty decoded payload separately. `resolveBinding` distinguishes client initialization failure, SDK access failure, and empty payload while retaining fail-closed behavior and the selected numeric-version check. For `*googleapi.Error`, the CLI diagnostic carries the HTTP code and typed `Message` field, bounded to 512 UTF-8 bytes with an explicit `message_truncated` flag; it never formats the SDK's raw `Error()`, response `Body`, or `Details`. Other error types are represented by type only. The prepare command's returned error follows the same stderr path used by its CLI entrypoint.

The engine consumer regression uses a synthetic provider-style 403 message and a known `VERCEL_TOKEN` marker. It confirms the existing child-error handler preserves the useful message, applies its existing known-value redaction and length cap, and classifies HTTP 403 as `permission-denied` with the existing `restore-existing-principal-permission` next action. The test change locks down existing behavior; it performs no permission change. Source values, secret resource, identity, IAM, timeout, fallback, and production behavior were not changed.

## Evidence

| Check | Result | Evidence |
|---|---|---|
| Causal RED against the original resolver | Exit 1 as expected: the synthetic SDK access error, empty payload, and initialization error were collapsed to the previous generic messages; SDK success passed. | `dev-gsm-resolver-red-r8.log` |
| Full affected Go package, offline with race detector | Exit 0; 13/13 tests passed. Uses the existing Go build/module caches with `GOPROXY=off`; SDK requests are served by an in-memory HTTP transport. | `dev-gsm-resolver-go-package-offline-r8.log` |
| `go vet ./cmd/pipeline_config`, offline | Exit 0. | `dev-gsm-resolver-go-vet-offline-r8.log` |
| `go build ./cmd/pipeline_config`, offline | Exit 0; binary output stayed under profile scratch. | `dev-gsm-resolver-go-build-offline-r8.log` |
| Action/engine consumer regression | Exit 0; 1/1 selected test passed, including known-token redaction, message preservation, the existing HTTP 403 classification, and fail-closed state. | `dev-gsm-resolver-engine-consumer-green-r8.log` |
| Initial consumer assertion | Exit 1 because the first test expectation assumed generic `command-failed`; the existing consumer correctly classified the synthetic 403 as `permission-denied`. The assertion was corrected to preserve that existing recovery behavior, then the focused test passed. | `dev-gsm-resolver-engine-consumer-classification-red-r8.log` |
| `git diff --check` | Exit 0. | Reported in this report; no whitespace was changed in raw logs. |

## Remaining limits

The historical run's exact GSM failure remains unknown. The synthetic HTTP 403 test proves the diagnostic path for that specific SDK response, not that DEV run `37496572138` was denied; the synthetic empty-response test separately proves empty-payload reporting. The observed metadata project-number difference was not treated as a requirement or changed. Parent/Owner review and any later authorized DEV validation remain outstanding.

No new validation gate was added. No live GSM/GCS/provider query, Actions run, IAM or credential operation, secret payload read, retry, merge, or Production action occurred.

`docs/lwc-368/evidence/source-manifest-r8.tsv` records the source, test, report, and retained raw logs; the manifest excludes itself.
