# LWC-125 Raw Multi Upload — Frontend Design

## Goal

Allow selecting and uploading N raw files at once against the existing
per-file BFF endpoint, with a concurrency-limited queue and clear per-file
outcomes including BFF same-name semantics.

## Scope

In scope:

- Multi-file file input (and optional drag-drop if trivial)
- Upload queue with concurrency = 3
- Per-file status: queued → uploading → created | already_exists | failed
- Summary counts
- Retry failed items
- Client pre-check for > 5 MiB
- Batch-internal duplicate filenames: first wins; later → failed
- `uploadRawFile` handles 200 / 201 / 409

Out of scope:

- XHR upload progress percent (use spinner / status only; keep `apiFetch`)
- Cross-file content dedupe on client
- URL scrape multi

## BFF Contract (consumed)

`POST /api/v1/raw/upload`

| HTTP | Meaning |
|------|---------|
| 201 | `status: "created"` |
| 200 | `status: "already_exists"` |
| 409 | same name, different content → failed with error message |
| 4xx/5xx | failed |

## API Client

```ts
export type RawUploadResult = {
  filename: string;
  path: string;
  bytes: number;
  sha256: string;
  status: 'created' | 'already_exists';
};
```

`uploadRawFile(file)`:

- 200/201 → return JSON
- 409 / other errors → throw `Error` (or `ApiError`) with server `error` text

## Queue Model

```ts
type UploadItem = {
  id: string;
  file: File;
  status: 'queued' | 'uploading' | 'created' | 'already_exists' | 'failed';
  error?: string;
  result?: RawUploadResult;
};
```

- Select files → append to queue (auto-start workers)
- Worker pool concurrency = 3
- Summary: `N created, M already exists, K failed`
- Retry: failed → queued again
- Max size client check: 5 MiB (`5 << 20`)

## UI

Primary surface: Pipeline **Upload File** card in `PipelineClient` (or small
extracted `RawUploadPanel` if the file grows too large).

- Hidden `<input type="file" multiple>` with accept aligned to common BFF types
  (at least `.md`; prefer BFF-supported text extensions)
- List of file rows with status badge + error line
- One summary line; avoid N toasts — single toast on batch idle optional

## Testing

- `uploadRawFile` 200/201/409 handling in `tests/api.test.mjs`
- Pipeline/upload UI file tests for `multiple`, concurrency constant, summary strings
