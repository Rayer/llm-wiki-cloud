# LWC-138 — Pipeline rate limiting (Frontend)

## Goal

Reflect BFF pipeline quota on the Home / Pipeline UI so users see remaining runs, cooldown, and new-raw state **before** clicking Run, and cannot click into known-blocked states.

## Depends on

BFF contract in `llm-wiki-bff/docs/superpowers/specs/2026-07-10-lwc-138-pipeline-rate-limit-design.md`.

## Scope

### In scope

- Types + parsing for `quota` on `GET /api/v1/pipeline/status` and blocked `POST /api/v1/pipeline/run` errors
- `PipelineClient` UX:
  - Always-visible quota status line
  - Run button **disabled** when blocked (including demo)
  - Tooltip / short reason text when disabled (not only toast-on-click)
  - Optional collapsible prerequisites checklist (✓/✗)
  - Toast on trigger failure / race (server blocks after UI thought allow)
- Poll/refresh quota with existing pipeline status polling
- i18n keys (en + zh-TW)
- Tests (node:test pattern like LWC-135)

### Out of scope

- BFF implementation
- Admin panel quota UI
- Redesign of entire Add Content card

## UX behavior (locked)

### Quota status line

Always visible under the Pipeline heading (or above Run):

```text
Runs today: 1/2 · Cooldown: 47m · New files: 3
```

When not enforced (`quota.enforced === false`): show a muted “Quota not enforced (local)” or hide cooldown/daily numbers — prefer short muted note.

When `already_running` or last execution RUNNING: show running state (existing badge may remain).

### Run Pipeline button

Disabled when any of:

1. `isDemoSession` (client demo — keep existing LWC-135)
2. `loading === 'pipeline'`
3. `quota.allowed === false` (from last status fetch)
4. No project selected (existing check — prefer disable + message over only toast)

**Do not rely on click-to-toast for disabled states.** Disabled buttons often do not fire click. Use:

- `disabled={...}`
- `title` / adjacent helper text with `quota.message` or mapped i18n reason
- Optional: wrap control so keyboard users get accessible name + description

Toast remains for:

- Network / 5xx
- Server block when UI was stale (race): parse `error` + prefer `quota.message` if body available

### Prerequisites checklist

Collapsible panel (default **collapsed**):

| Condition | ✓ when |
|-----------|--------|
| Not demo | `!isDemoSession` |
| Under daily limit | `runs_today < daily_limit` |
| Cooldown clear | `!cooldown_until \|\| now >= cooldown_until` |
| Not already running | `!already_running` and execution not RUNNING |
| New raw available | `new_raw_files >= min_new_raw` |

### Data flow

```
mount / project change → getPipelineStatus() → set quota + execution
after successful trigger → poll status (existing 5s) → refresh quota
after upload success → refresh status once (new raw count may change)
```

No separate preflight POST. Status GET is evaluate-only.

### API client

Extend types:

```ts
export type PipelineQuota = {
  enforced: boolean;
  allowed: boolean;
  reason?: string;
  message?: string;
  runs_today: number;
  daily_limit: number;
  cooldown_until?: string | null;
  next_reset?: string | null;
  new_raw_files: number;
  min_new_raw: number;
  already_running: boolean;
};

export type PipelineStatus = {
  last_execution?: PipelineExecution | null;
  project_id?: string;
  quota?: PipelineQuota | null;
};

export type PipelineResult = {
  status?: 'accepted' | string;
  execution_id?: string;
  project_id?: string;
  message?: string;
  quota?: PipelineQuota | null;
  // keep legacy fields if still returned
  rawFiles?: number;
  scheduled?: boolean;
};
```

Improve `triggerPipeline` error path: if response JSON has `error`, throw `Error` with that string (already). Optionally attach quota via a small `PipelineBlockedError` class for richer UI — nice-to-have; string message is enough for v1 if BFF `error` embeds reason.

## Acceptance criteria (FE)

1. Demo session: Run button disabled; helper text uses demo restriction; no network call on click attempt (button disabled).
2. When status returns `quota.allowed=false` with `reason=daily_limit`, button disabled and status line shows `2/2` (or current runs/limit).
3. Cooldown reason shows remaining time in the status line (humanized minutes).
4. `no_new_raw` disables Run and checklist marks new-files ✗.
5. While pipeline RUNNING / `already_running`, Run disabled.
6. After upload creates a new raw file, next status refresh can re-enable Run when other limits allow.
7. Blocked POST (stale UI) still shows toast with server message.
8. Existing multi-upload / demo LWC-135 tests still pass; new LWC-138 tests cover disable + status line strings.

## Testing

- `tests/lwc-138-pipeline-quota.test.mjs` (or extend `pipeline-client.test.mjs`):
  - source assertions for disabled conditions and quota line
  - pure helper tests if cooldown formatting is extracted

## Compatibility

- If BFF omits `quota` (old server): UI behaves as today aside from demo disable improvement (button disabled for demo instead of toast-only).
- Do not break upload queue or scrape WIP modal.
