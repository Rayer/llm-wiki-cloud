# LWC-77 Raw Files Frontend Design

## Goal

Add a Raw Files page to the frontend so users can inspect raw file metadata for
the selected project.

## Scope

- Add a `Raw` sidebar item between `Concepts` and `Status`.
- Add `/raw` as a project-scoped page.
- Fetch `GET /api/v1/raw` through the existing authenticated API client.
- Render a table with filename, size, updated time, SHA256, and ingested status.
- Keep filenames as plain text. Download and preview are out of scope because
  raw file download will be handled later by the export/package workflow.

## Data Contract

The frontend expects:

```json
{
  "files": [
    {
      "name": "article.md",
      "size": 12345,
      "updated": "2026-07-09T10:00:00Z",
      "sha256": "abc123",
      "ingested": true
    }
  ]
}
```

The page tolerates unknown fields and missing optional values. Missing or invalid
`size` becomes `0`; missing `updated` or `sha256` renders as an em dash.

## UI

`RawClient` uses the existing frontend surface and state components. The table
is dense, scannable, and responsive:

- desktop: normal table
- narrow screens: horizontal overflow, no text overlap
- ingested: badge with `Ingested` or `Pending`
- SHA256: monospace truncated visual width with full value in `title`

## Error Handling

The page uses existing `LoadingState`, `ErrorState`, and `EmptyState`.
Project selection is guarded by `Shell` and `WorkspaceProvider`; the Raw page
does not add a second guard.

## Testing

- API test for `getRawFiles()` path, project headers, and normalization.
- Shell file test for Raw nav item ordering between Concepts and Status.
- Raw client file test for required table columns and no download/preview links.
