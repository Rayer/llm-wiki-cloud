# LWC-129 — Sidebar nav counts refresh after raw upload (Frontend)

## Goal

After multi-file raw upload creates new files, the sidebar **Raw** badge updates on the same page without navigation or project switch.

## Depends on

BFF LWC-129: `GET /api/v1/status` `raw_count` must be the **live** `raw/` file count (not stale `raw_status.json`). See BFF spec  
`llm-wiki-bff/docs/superpowers/specs/2026-07-10-lwc-129-status-raw-count-live-design.md`.

Without that change, FE re-fetch alone does not fix the bug once a pipeline artifact exists.

## Acceptance criteria

### Must

1. **Given** a selected project with sidebar Raw badge visible  
   **When** multi-upload finishes with at least one `created` result  
   **Then** Raw badge equals current live raw file count (same as `GET /api/v1/status` `raw_count` / `GET /api/v1/raw` `files.length`) without page navigation or project switch.

2. **When** the batch has only `already_exists` and/or `failed` (no `created`)  
   **Then** badge may stay unchanged (no mandatory refresh).

3. **When** the user switches project  
   **Then** badges reflect the new project (no stale counts from the previous project).

4. Sources / concepts badges still load from status when token + project are present; behavior not regressed for project switch.

### Should

5. At most one status re-fetch per upload batch when the queue goes idle (not one per file).
6. If refresh fails, keep previous badge values (no flash to empty).

### Out of scope

- Unifying HomeClient / StatusClient metrics with workspace counts
- Refreshing sources/concepts after pipeline run
- Optimistic +1 without server round-trip
- LWC-133 lint fix is not required, but avoid introducing new `set-state-in-effect` guard patterns when moving count state

## Design

### Approach

**A (locked):** Nav counts live in `WorkspaceContext` with `refreshNavCounts()`; `PipelineClient` calls it after a successful create batch. Shell only renders `navCounts`.

### WorkspaceContext

```ts
type NavCounts = {
  sources: number | null;
  concepts: number | null;
  raw: number | null;
};

// exposed:
navCounts: NavCounts;
refreshNavCounts: () => Promise<void>;
```

- Load counts via `getStatus()` when `token` and `currentProject` are set (same triggers as today).
- Display: if `!token || !currentProject`, Shell treats counts as hidden/`null` (prefer **derive at render** over sync setState in effect guard — aligns with LWC-133 guidance).
- `refreshNavCounts`: re-call `getStatus()`, update all three counts; on error leave previous values.

### Shell

- Remove local `navCounts` state and `getStatus` effect.
- `const { navCounts, ... } = useWorkspace()`.

### PipelineClient

- `const { refreshNavCounts } = useWorkspace()`.
- Track `needsCountRefresh` (ref): set true when any item becomes `created`.
- When upload queue is idle (no `queued`/`uploading`) and flag is true: `void refreshNavCounts()`, clear flag.

### Testing

- Source/file tests: WorkspaceProvider exposes `refreshNavCounts`; PipelineClient invokes it after `created`.
- Shell reads counts from workspace (no direct `getStatus` for nav).
- Keep existing project-switch related assertions green (update if effect ownership moved).

## Merge / deploy

1. Merge **BFF** live `raw_count` first (or same release train).
2. Merge **FE** refresh second.
3. Deploy BFF before FE for correct production behavior.

## Complexity

~0.5–1 day total (BFF small + FE context move + tests).
