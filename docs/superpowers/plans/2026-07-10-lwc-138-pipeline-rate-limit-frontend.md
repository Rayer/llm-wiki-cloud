# LWC-138 Pipeline Rate Limiting — Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show pipeline quota on Home and disable Run when BFF (or demo session) blocks, with clear reasons—not toast-only.

**Architecture:** Parse `quota` from `GET /api/v1/pipeline/status`; keep status in `PipelineClient`; disable Run from `isDemoSession || !quota.allowed || running || loading`; collapsible checklist; refresh status after upload and while polling.

**Tech Stack:** Next.js React client components, existing `api.ts`, node:test source tests, i18n en + zh-TW.

**Spec:** `docs/superpowers/specs/2026-07-10-lwc-138-pipeline-rate-limit-frontend-design.md`  
**BFF contract:** sibling BFF design `2026-07-10-lwc-138-pipeline-rate-limit-design.md`

## Global Constraints

- Disabled button + helper text (not click-toast for known blocks)
- Toast only for network/race/server errors
- Checklist default collapsed
- If `quota` missing (old BFF): only demo disable improvement; Run otherwise works as today
- Do not break multi-upload queue

## File map

| File | Responsibility |
|------|----------------|
| Modify `src/lib/api.ts` | `PipelineQuota` type, status/result types, optional normalize |
| Create `src/lib/pipeline-quota.ts` | format status line, humanize cooldown, checklist items pure helpers |
| Create `src/lib/pipeline-quota.test` via `tests/lwc-138-pipeline-quota.test.mjs` | pure helper + source assertions |
| Modify `src/components/PipelineClient.tsx` | UI: bar, disabled, checklist, fetch on mount |
| Modify `src/messages/en.json`, `zh-TW.json` | i18n strings |

---

### Task 1: Types + pure helpers

**Files:**
- Modify: `src/lib/api.ts`
- Create: `src/lib/pipeline-quota.ts`
- Create: `tests/lwc-138-pipeline-quota.test.mjs`

- [ ] **Step 1: Add types to `api.ts`**

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
  command?: string;
  quota?: PipelineQuota | null;
  rawFiles?: number;
  scheduled?: boolean;
};
```

Ensure `getPipelineStatus` / `triggerPipeline` still work; optionally normalize quota if snake_case already matches.

- [ ] **Step 2: Implement pure helpers**

```ts
// src/lib/pipeline-quota.ts
import type { PipelineQuota } from './api';

export function formatQuotaLine(q: PipelineQuota | null | undefined, now = new Date()): string {
  if (!q) return '';
  if (!q.enforced) return 'Quota not enforced';
  const cooldown = formatCooldownRemaining(q.cooldown_until, now);
  const parts = [
    `Runs today: ${q.runs_today}/${q.daily_limit}`,
    cooldown ? `Cooldown: ${cooldown}` : 'Cooldown: clear',
    `New files: ${q.new_raw_files}`,
  ];
  return parts.join(' · ');
}

export function formatCooldownRemaining(until: string | null | undefined, now = new Date()): string | null {
  if (!until) return null;
  const ms = new Date(until).getTime() - now.getTime();
  if (Number.isNaN(ms) || ms <= 0) return null;
  const mins = Math.max(1, Math.ceil(ms / 60000));
  if (mins >= 60) {
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return m ? `${h}h ${m}m` : `${h}h`;
  }
  return `${mins}m`;
}

export function isRunBlocked(opts: {
  isDemoSession: boolean;
  loading: boolean;
  hasProject: boolean;
  executionRunning: boolean;
  quota?: PipelineQuota | null;
}): boolean {
  if (opts.isDemoSession) return true;
  if (opts.loading) return true;
  if (!opts.hasProject) return true;
  if (opts.executionRunning) return true;
  if (opts.quota && opts.quota.enforced && opts.quota.allowed === false) return true;
  return false;
}

export function blockReasonMessage(opts: {
  isDemoSession: boolean;
  hasProject: boolean;
  executionRunning: boolean;
  quota?: PipelineQuota | null;
  demoMessage: string;
  noProjectMessage: string;
}): string {
  if (opts.isDemoSession) return opts.demoMessage;
  if (!opts.hasProject) return opts.noProjectMessage;
  if (opts.executionRunning) return opts.quota?.message || 'Pipeline is running';
  if (opts.quota?.message) return opts.quota.message;
  return '';
}
```

- [ ] **Step 3: Tests**

```js
// tests/lwc-138-pipeline-quota.test.mjs — import helpers if using ts via existing pattern
// Prefer source-read assertions like other LWC tests if imports are awkward:
// assert pipeline-quota.ts exports and PipelineClient matches disabled patterns
```

Follow repo pattern: many tests `readFile` source. Add both:
1. If helpers are plain TS without path aliases issues, import from built path or duplicate pure JS tests.
2. Source match tests for PipelineClient after Task 2.

Minimal Task 1 test: read `pipeline-quota.ts` and assert function names exist; unit-test formatCooldown with dynamic import if supported.

- [ ] **Step 4: Run**

```bash
cd /Users/rayer/Documents/Develop/llm-wiki-frontend
node --test tests/lwc-138-pipeline-quota.test.mjs
```

- [ ] **Step 5: Commit**

```bash
git add src/lib/api.ts src/lib/pipeline-quota.ts tests/lwc-138-pipeline-quota.test.mjs
git commit -m "feat(LWC-138): pipeline quota types and format helpers"
```

---

### Task 2: PipelineClient UI

**Files:**
- Modify: `src/components/PipelineClient.tsx`
- Modify: `src/messages/en.json`, `src/messages/zh-TW.json`

- [ ] **Step 1: i18n keys** (example)

```json
"Pipeline": {
  "quotaLine": "Runs today: {runs}/{limit} · Cooldown: {cooldown} · New files: {newFiles}",
  "quotaNotEnforced": "Quota not enforced",
  "cooldownClear": "clear",
  "runDisabledDemo": "...",
  "prerequisites": "Prerequisites",
  "prereqDemo": "Not a demo session",
  "prereqDaily": "Under daily limit",
  "prereqCooldown": "Cooldown clear",
  "prereqRunning": "No pipeline running",
  "prereqRaw": "New or modified raw files"
}
```

(Adjust to match existing message nesting style in the repo.)

- [ ] **Step 2: State + fetch**

On mount and when workspace project changes:
```ts
const status = await getPipelineStatus();
setPipelineStatus(status);
// quota from status.quota
```

After successful raw upload (`created`), call `getPipelineStatus()` once (in addition to `refreshNavCounts`).

When polling after accept, status already updates — keep `quota` from each poll.

- [ ] **Step 3: Disable Run**

```tsx
const executionRunning =
  pipelineStatus?.last_execution?.status === 'RUNNING' ||
  pipelineStatus?.quota?.already_running === true;
const hasProject = Boolean(
  typeof window !== 'undefined' && window.localStorage.getItem('llm-wiki-last-project')
);
const blocked = isRunBlocked({
  isDemoSession,
  loading: loading === 'pipeline',
  hasProject,
  executionRunning,
  quota: pipelineStatus?.quota,
});
const helper = blockReasonMessage({...});
```

Button:
```tsx
<button
  type="button"
  onClick={handleRunPipeline}
  disabled={blocked}
  title={helper || undefined}
  ...
>
```

Status line above/beside button:
```tsx
<p className="mt-2 text-xs text-zinc-500" data-testid="pipeline-quota-line">
  {formatQuotaLine(pipelineStatus?.quota)}
</p>
{helper ? <p className="mt-1 text-xs text-amber-200/90">{helper}</p> : null}
```

- [ ] **Step 4: Checklist** (collapsed by default)

```tsx
const [showPrereq, setShowPrereq] = useState(false);
// rows with ✓/✗ based on demo, runs, cooldown, running, new raw
```

- [ ] **Step 5: handleRunPipeline**

Keep demo early-return as belt-and-suspenders (button already disabled).  
On error toast, show `err.message`.

- [ ] **Step 6: Tests**

Extend `tests/lwc-138-pipeline-quota.test.mjs`:

```js
assert.match(pipelineClient, /formatQuotaLine|pipeline-quota/);
assert.match(pipelineClient, /disabled=\{blocked\}|disabled=\{[^}]*isDemoSession/);
assert.match(pipelineClient, /data-testid="pipeline-quota-line"/);
assert.match(pipelineClient, /getPipelineStatus/);
// still blocks demo
assert.match(pipelineClient, /isDemoSession/);
```

Also ensure LWC-135 demo test still passes.

- [ ] **Step 7: Run**

```bash
node --test tests/lwc-138-pipeline-quota.test.mjs tests/lwc-135-demo-session.test.mjs tests/pipeline-client.test.mjs
```

- [ ] **Step 8: Commit**

```bash
git commit -am "feat(LWC-138): pipeline quota bar and disabled Run button"
```

---

### Task 3: FE verification

- [ ] Manual: with mock/old BFF without quota — UI does not crash
- [ ] Manual: demo session — Run disabled, helper shown
- [ ] Manual: after BFF deploy — status line updates; blocked reasons match server

---

## Spec coverage

| Spec item | Task |
|-----------|------|
| Types for quota | T1 |
| Status line | T1–T2 |
| Disabled Run + helper | T2 |
| Checklist | T2 |
| Toast only race/error | T2 |
| Refresh after upload | T2 |
| Tests | T1–T2 |
