# LWC-344 Project export API contract

All routes use the existing account JWT and `X-Project-ID` project selection. The BFF must also confirm that the selected project record belongs to the authenticated user before reading or changing export state. A foreign or missing project is reported as `404`.

## Routes

| Method and path | Success | Purpose |
| --- | --- | --- |
| `POST /api/v1/exports` | `202 Accepted` | Atomically admit one export. Body is `CreateExportRequest`. |
| `GET /api/v1/exports` | `200 OK` | Read the latest job, latest valid archive(s), and server-calculated eligibility. No side effects. |
| `GET /api/v1/exports/{export_id}/status` | `200 OK` | Read one owner's job and its archive state. No side effects. |
| `POST /api/v1/exports/{export_id}/download` | `200 OK` | Recheck project access and expiry, then return a short-lived signed URL. |

All response times are RFC 3339 UTC. JSON object fields listed below are always present; nullable values are explicit `null`. `Idempotency-Key` is optional on create. Repeating a key for the same user/project returns the original job; a different body with the same key returns `409 idempotency_conflict`.

## Create

```json
{"scope":"raw-full-metadata"}
```

`scope` is one of `raw`, `raw-full`, `raw-full-metadata`. Response:

```json
{
  "export_id":"01J...",
  "scope":"raw-full-metadata",
  "status":"queued",
  "created_at":"2026-09-24T03:00:00Z",
  "snapshot_at":null,
  "completed_at":null,
  "expires_at":null,
  "next_allowed_at":null,
  "size_bytes":null,
  "download_available":false,
  "error_code":null,
  "error_message":null
}
```

The job becomes `ready` only after the complete ZIP and its GCS metadata are verified. `completed_at` and `expires_at` are read from the archive object's GCS Custom-Time; expiry is `Custom-Time + 72h` and is never persisted as a database field. `snapshot_at` is the data snapshot time from `export-meta.json`, not completion time.

## Read state

`GET /api/v1/exports` returns:

```json
{
  "latest_job":null,
  "current":null,
  "previous":null,
  "eligible":true,
  "rejection_reason":null,
  "next_allowed_at":null
}
```

`latest_job` is either `null` or the same complete Job object used by create and status responses; failed jobs set stable `error_code` and sanitized `error_message`, and inapplicable times/size remain explicit `null`. `current` and `previous` are either `null` or:

```json
{
  "export_id":"01J...",
  "scope":"raw-full-metadata",
  "status":"ready",
  "snapshot_at":"2026-09-24T02:55:00Z",
  "completed_at":"2026-09-24T03:00:00Z",
  "expires_at":"2026-09-27T03:00:00Z",
  "size_bytes":123456,
  "download_available":true
}
```

`current` is the newest unexpired successful archive; `previous` is the next newest unexpired successful archive. Missing GCS objects are never reported as downloadable. `eligible` and `rejection_reason` are calculated by the server, never by the UI. `GET .../{export_id}/status` returns the same complete Job object; terminal failed jobs use `status:"failed"`, and a ready job whose Custom-Time has elapsed uses `status:"expired"`, with `download_available:false`.

## Download and errors

Successful download response:

```json
{"export_id":"01J...","signed_url":"https://storage.googleapis.com/...","expires_at":"2026-09-27T03:00:00Z","filename":"Project_raw-full-metadata_20260924T025500Z_01J....zip"}
```

The signed URL expiry must be no later than `expires_at`. Creation conflicts return `409` with `{"error":"export_rejected","reason":"in_progress|cooldown","existing_export_id":null,"next_allowed_at":null}`. Other errors use `{"error":"<stable_code>"}` with `400 invalid_scope`, `401 unauthenticated`, `404 project_or_export_not_found`, `409 idempotency_conflict`, `410 export_expired`, or `503 export_unavailable`.

## Storage and lifecycle

Project IDs are generated from six random bytes but stored in Firestore under a user-prefixed document ID, with no repository-enforced global uniqueness. Therefore successful archives use `exports/ready/{userID}/{projectID}/{exportID}/archive.zip`; in-progress objects use `exports/tmp/{userID}/{projectID}/{exportID}/...`. Only `ready/` objects may be signed. Configure bucket lifecycle to delete ready objects with `daysSinceCustomTime: 3` and temporary objects by `age: 1 day`; the export job hard timeout is less than 24 hours and tested so an active/recoverable job cannot lose its temporary object to lifecycle. The worker writes and validates a complete temporary ZIP first, sets Custom-Time only after that write is complete, then promotes it while preserving Custom-Time and user metadata. A crash after promotion but before the job becomes ready leaves a ready-prefix object that lifecycle still removes three days after that timestamp.

The ZIP contains `export-meta.json` in every scope with format version, export/project IDs, snapshot time, scope, applicable source-generation/version identities, the archive file manifest and digests, and `redacted_fields` when project config fields were removed. It has no expiry field. It excludes provider credentials and active authorization references, deployment settings, and other users' data.

## Worker source inventory

The worker reads the selected owner's `users/{userID}/projects/{projectID}/` objects and pins each opened GCS object to the generation observed while enumerating. `snapshot_at` is the worker's UTC enumeration start; `source_versions` identifies the exact GCS generations, the current published generation ID when present, and the Firestore project record update time. These identities preserve the actual inputs when different project sources were last written at different times.

- `raw` includes eligible `raw/**` objects.
- `raw-full` adds all eligible `wiki/**` objects, including `.drafts/` and hand-edited content.
- `raw-full-metadata` adds `.synto/INDEX.json`, sanitized `.synto/state.db` and `.olw/state.db` when present, sanitized `wiki.toml` and `synto.toml`, `.lwc/publish/current.json`, the portable annotation/index cache files, vault schema, and an allowlisted `project-metadata/project.json` synthesized from the owner Project record.
- TOML configs are parsed with the repo's TOML parser. The known OLW `provider.api_key` and Synto `providers.<name>.api_key`/`api_key_env` fields are removed; an unrecognized credential-shaped field fails the export. Other config such as provider names, endpoints, models, and pipeline settings remains. `export-meta.json` records the config path and redacted field paths, while each sanitized file digest covers the exact archived bytes.
- StateDB copies are integrity-checked and sanitized before ZIP input: credential/session/token/secret-bearing tables and columns are emptied, sensitive key/value entries are deleted, nested JSON secret fields are removed, and the file is rebuilt with secure deletion before export. The Firestore Project record includes only project identity, name, description, timestamps, profile, and settings, with secret-like fields recursively removed.
- User-owned `raw/**` and `wiki/**` content is retained based on the export scope; path names or body text are not scanned for secret-like substrings. Temporary StateDB copies and archive verification are bounded by the worker's 22-hour timeout, 10,000-file/10-GiB source limits, and SQLite size limit.

The worker is built with `Dockerfile.exportjob`; the configured Cloud Run Job must have a task timeout below 24 hours to match the temporary-prefix lifecycle. Its runtime must provide `GCP_PROJECT`, `BUCKET`, `FIRESTORE_DATABASE_ID`, and `EXPORT_SIGNING_SERVICE_ACCOUNT`. The BFF needs `EXPORT_JOB_URL` and `EXPORT_SIGNING_SERVICE_ACCOUNT`; its identity needs permission to start the configured job, access Firestore export records, and call IAM `signBlob`, while the job identity needs access to its Project sources, export records, and export object prefixes.
