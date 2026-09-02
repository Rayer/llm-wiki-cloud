# LWC-125 Raw Multi Upload — Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Multi-file raw upload queue with concurrency 3 and BFF same-name statuses.

**Architecture:** Extend `uploadRawFile` contract; replace single-file Pipeline upload UI with a small queue that reuses `apiFetch`.

**Tech Stack:** Next.js client components, TypeScript, node:test file tests.

## Global Constraints

- No XHR progress (keep `apiFetch`)
- Concurrency = 3
- Max file 5 MiB client pre-check
- Batch duplicate names: first wins

---

### Task 1: API client 200/201/409

**Files:**
- Modify: `src/lib/api.ts`
- Modify: `tests/api.test.mjs`

### Task 2: Multi-file queue UI in PipelineClient

**Files:**
- Modify: `src/components/PipelineClient.tsx`
- Modify/Create: `tests/pipeline-client.test.mjs` or `tests/lwc-125-raw-multi-upload.test.mjs`
