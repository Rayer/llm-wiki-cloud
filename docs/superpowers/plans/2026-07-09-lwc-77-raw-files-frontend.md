# LWC-77 Raw Files Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a project-scoped Raw Files page that lists raw file metadata from `GET /api/v1/raw`.

**Architecture:** Extend the existing API client with a normalized `RawFile` type and `getRawFiles()`. Add a focused `RawClient` table component and route it from `/raw`. Insert the `Raw` nav item between `Concepts` and `Status`.

**Tech Stack:** Next.js App Router, React client components, TypeScript, Tailwind CSS, Node test runner.

## Global Constraints

- No raw download or preview in LWC-77 frontend.
- Reuse existing auth/project scoping through `apiFetch`.
- Reuse existing `Shell`, `Surface`, `Badge`, `LoadingState`, `ErrorState`, and `EmptyState`.
- Follow TDD: each production change must be preceded by a failing test.

---

### Task 1: API Raw Files Client

**Files:**
- Modify: `src/lib/api.ts`
- Test: `tests/api.test.mjs`

**Interfaces:**
- Produces: `type RawFile` with `name`, `size`, `updated`, `sha256`, `ingested`, `raw`
- Produces: `getRawFiles(): Promise<RawFile[]>`

- [ ] **Step 1: Write the failing test**

Add a test that configures auth, stubs `fetch`, calls `getRawFiles()`, and
asserts the URL is `/api/v1/raw`, project auth headers are present, and snake
case payload data is normalized into frontend fields.

- [ ] **Step 2: Run test to verify it fails**

Run: `npm test -- tests/api.test.mjs`

Expected: fail because `getRawFiles` is not exported.

- [ ] **Step 3: Implement minimal API code**

Add `RawFile`, a `normalizeRawFile()` helper, and `getRawFiles()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `npm test -- tests/api.test.mjs`

Expected: pass.

### Task 2: Sidebar Navigation

**Files:**
- Modify: `src/components/Shell.tsx`
- Test: `tests/lwc-77-raw-files.test.mjs`
- Modify i18n: `src/messages/en.json`, `src/messages/zh-TW.json`

**Interfaces:**
- Consumes: `t('Shell.raw')`
- Produces: `/raw` nav item between `/concepts` and `/status`

- [ ] **Step 1: Write the failing test**

Add a file-based test asserting `Shell.tsx` imports `Database`, contains
`href: '/raw'`, uses `t('Shell.raw')`, and the nav item appears after concepts
and before status.

- [ ] **Step 2: Run test to verify it fails**

Run: `npm test -- tests/lwc-77-raw-files.test.mjs`

Expected: fail because Raw nav does not exist.

- [ ] **Step 3: Implement minimal nav code**

Import a lucide icon and add the nav item plus translations.

- [ ] **Step 4: Run test to verify it passes**

Run: `npm test -- tests/lwc-77-raw-files.test.mjs`

Expected: pass.

### Task 3: Raw Files Page

**Files:**
- Create: `src/components/RawClient.tsx`
- Create: `src/app/raw/page.tsx`
- Test: `tests/lwc-77-raw-files.test.mjs`

**Interfaces:**
- Consumes: `getRawFiles(): Promise<RawFile[]>`
- Produces: `/raw` page rendered by `RawClient`

- [ ] **Step 1: Write the failing test**

Extend the file-based test to assert the route imports `RawClient`, the client
calls `getRawFiles`, renders the required table headers, uses `Badge`, and does
not render download or preview actions.

- [ ] **Step 2: Run test to verify it fails**

Run: `npm test -- tests/lwc-77-raw-files.test.mjs`

Expected: fail because files do not exist.

- [ ] **Step 3: Implement minimal page and client**

Create `RawClient` with loading, error, empty, and table states. Create
`src/app/raw/page.tsx` that renders it.

- [ ] **Step 4: Run test to verify it passes**

Run: `npm test -- tests/lwc-77-raw-files.test.mjs`

Expected: pass.

### Task 4: Final Verification

**Files:**
- Verify all touched frontend files.

- [ ] **Step 1: Run targeted tests**

Run: `npm test -- tests/api.test.mjs tests/lwc-77-raw-files.test.mjs`

- [ ] **Step 2: Run full test suite**

Run: `npm test`

- [ ] **Step 3: Run lint**

Run: `npm run lint`
