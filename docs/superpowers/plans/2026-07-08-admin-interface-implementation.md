# LWC-72 Admin Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `/admin` frontend MVP with admin-only access, project/user tables, and all required admin actions.

**Architecture:** Extend the existing auth state to carry `role`, add cross-project admin API helpers that bypass project headers, then build a focused `AdminClient` under `src/app/admin/page.tsx`. The UI follows the existing Shell, dark zinc theme, `Surface`, `Badge`, native tables, and modal patterns.

**Tech Stack:** Next 16 App Router, React 19 client components, TypeScript, Tailwind CSS v4 classes, lucide-react, Node test runner with `--experimental-strip-types`.

## Global Constraints

- Admin route is `/admin`.
- Access is based on `user.role === "admin"`.
- Admin API calls must use `apiFetch(..., { requireProject: false })` and must not send `X-Project-ID`.
- UI uses the existing dark zinc Shell, `Surface`, `Badge`, and table pattern.
- Destructive/high-impact actions use confirmation modals; do not use `window.confirm`.
- Do not use optimistic updates; refresh only the affected active table after successful mutations.
- Do not add new runtime dependencies.
- Verify with `npm test` and `npm run lint`.

---

## File Structure

- Modify `src/lib/auth-core.ts`
  - Add `role?: string` to `AuthUser`.
  - Preserve `user.role` in auth/refresh normalization.
- Modify `src/components/Shell.tsx`
  - Add Admin nav item gated by `user?.role === "admin"`.
  - Import a suitable lucide icon, such as `Shield`.
- Modify `src/lib/api.ts`
  - Add admin data types, normalizers, and API helper functions.
- Create `src/components/AdminClient.tsx`
  - Own `/admin` UI state, tabs, table loading, action modals, and mutation flow.
- Create `src/app/admin/page.tsx`
  - Render `AdminClient`.
- Modify tests:
  - `tests/auth.test.mjs`
  - `tests/api.test.mjs`
  - Add `tests/lwc-72-admin-interface.test.mjs`

---

### Task 1: Auth Role Normalization and Admin Navigation Gate

**Files:**
- Modify: `src/lib/auth-core.ts`
- Modify: `src/components/Shell.tsx`
- Test: `tests/auth.test.mjs`
- Test: `tests/lwc-72-admin-interface.test.mjs`

**Interfaces:**
- Produces: `AuthUser = { id: string; email: string; role?: string }`
- Produces: Shell Admin nav item gated by `user?.role === "admin"`
- Consumes: BFF auth payload shape `{ access_token: string, user: { id, email, role? } }`

- [ ] **Step 1: Write failing auth normalization tests**

Add these tests to `tests/auth.test.mjs`:

```js
test('normalizeAuthResponse preserves user role', () => {
  assert.deepEqual(
    normalizeAuthResponse({
      access_token: 'jwt-token',
      user: { id: 'admin-1', email: 'admin@example.com', role: 'admin' },
    }),
    {
      access_token: 'jwt-token',
      user: { id: 'admin-1', email: 'admin@example.com', role: 'admin' },
    },
  );
});

test('normalizeRefreshResponse preserves user role when present', () => {
  assert.deepEqual(
    normalizeRefreshResponse({
      access_token: 'fresh-token',
      user: { id: 'admin-1', email: 'admin@example.com', role: 'admin' },
    }),
    {
      access_token: 'fresh-token',
      user: { id: 'admin-1', email: 'admin@example.com', role: 'admin' },
    },
  );
});
```

- [ ] **Step 2: Write failing Shell admin nav static test**

Create `tests/lwc-72-admin-interface.test.mjs` with:

```js
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell exposes Admin navigation only for admin users', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /Shield/);
  assert.match(shell, /user\?\.role === 'admin'/);
  assert.match(shell, /href: '\/admin'/);
  assert.match(shell, /label: 'Admin'/);
});
```

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
node --experimental-strip-types --test tests/auth.test.mjs tests/lwc-72-admin-interface.test.mjs
```

Expected:

- `normalizeAuthResponse preserves user role` fails because role is not returned.
- `normalizeRefreshResponse preserves user role when present` fails because role is not returned.
- Shell static test fails because Admin nav is not present.

- [ ] **Step 4: Implement auth role normalization**

In `src/lib/auth-core.ts`, update the type and normalizer:

```ts
export type AuthUser = {
  id: string;
  email: string;
  role?: string;
};
```

Update `normalizeUser`:

```ts
function normalizeUser(value: unknown): AuthUser | null {
  if (!isRecord(value)) return null;
  const id = firstString(value, ['id', 'user_id', 'userId']);
  const email = firstString(value, ['email']);
  const role = firstString(value, ['role']);
  if (!id || !email) return null;
  return role ? { id, email, role } : { id, email };
}
```

- [ ] **Step 5: Implement Shell Admin nav gate**

In `src/components/Shell.tsx`:

```ts
import { Search, FileText, Brain, Activity, Menu, X, ChevronUp, Shield } from 'lucide-react';
```

Build nav items as a mutable array after `user` is available:

```ts
const navItems = [
  { href: '/', label: t('Shell.search'), icon: Search, exact: true },
  { href: '/sources', label: t('Shell.sources'), icon: FileText },
  { href: '/concepts', label: t('Shell.concepts'), icon: Brain },
  { href: '/status', label: t('Shell.status'), icon: Activity },
  ...(user?.role === 'admin'
    ? [{ href: '/admin', label: 'Admin', icon: Shield }]
    : []),
];
```

- [ ] **Step 6: Run tests and verify GREEN**

Run:

```bash
node --experimental-strip-types --test tests/auth.test.mjs tests/lwc-72-admin-interface.test.mjs
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/lib/auth-core.ts src/components/Shell.tsx tests/auth.test.mjs tests/lwc-72-admin-interface.test.mjs
git commit -m "Add admin role auth and nav gate"
```

---

### Task 2: Admin API Helpers

**Files:**
- Modify: `src/lib/api.ts`
- Test: `tests/api.test.mjs`

**Interfaces:**
- Produces:
  - `type AdminProject`
  - `type AdminUser`
  - `getAdminProjects(): Promise<AdminProject[]>`
  - `renameAdminProject(id: string, name: string): Promise<void>`
  - `deleteAdminProject(id: string): Promise<void>`
  - `rebuildAdminProjectIndex(id: string): Promise<void>`
  - `triggerAdminProjectPipeline(id: string): Promise<void>`
  - `getAdminUsers(): Promise<AdminUser[]>`
  - `updateAdminUserRole(id: string, role: string): Promise<void>`
  - `deleteAdminUser(id: string): Promise<void>`
- Consumes: `apiFetch(path, options)` from `src/lib/api.ts`

- [ ] **Step 1: Write failing API tests**

Append to `tests/api.test.mjs` imports:

```js
  deleteAdminProject,
  deleteAdminUser,
  getAdminProjects,
  getAdminUsers,
  rebuildAdminProjectIndex,
  renameAdminProject,
  triggerAdminProjectPipeline,
  updateAdminUserRole,
```

Add tests:

```js
test('getAdminProjects reads admin projects without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return Response.json({
      projects: [
        {
          id: 'user-1_proj-1',
          name: 'Demo',
          user_id: 'user-1',
          concept_count: 2,
          source_count: 3,
        },
      ],
    });
  };

  try {
    const projects = await getAdminProjects();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects');
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], undefined);
    assert.deepEqual(projects, [
      {
        id: 'user-1_proj-1',
        name: 'Demo',
        userId: 'user-1',
        conceptCount: 2,
        sourceCount: 3,
      },
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('admin project mutations use admin endpoints without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    return Response.json({ status: 'ok' });
  };

  try {
    await renameAdminProject('project-1', 'New name');
    await rebuildAdminProjectIndex('project-1');
    await triggerAdminProjectPipeline('project-1');
    await deleteAdminProject('project-1');

    assert.deepEqual(
      calls.map((call) => [call.url, call.init.method, call.init.headers['X-Project-ID']]),
      [
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1', 'PATCH', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1/rebuild-index', 'POST', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1/pipeline', 'POST', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1', 'DELETE', undefined],
      ],
    );
    assert.equal(calls[0].init.body, JSON.stringify({ name: 'New name' }));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getAdminUsers and user mutations use admin endpoints without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    if (String(url).endsWith('/api/v1/admin/users')) {
      return Response.json({
        users: [
          { id: 'user-1', email: 'admin@example.com', role: 'admin', project_count: 4 },
        ],
      });
    }
    return Response.json({ status: 'ok' });
  };

  try {
    const users = await getAdminUsers();
    await updateAdminUserRole('user-1', 'user');
    await deleteAdminUser('user-1');

    assert.deepEqual(users, [
      { id: 'user-1', email: 'admin@example.com', role: 'admin', projectCount: 4 },
    ]);
    assert.deepEqual(
      calls.map((call) => [call.url, call.init.method, call.init.headers['X-Project-ID']]),
      [
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users', undefined, undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users/user-1', 'PATCH', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users/user-1', 'DELETE', undefined],
      ],
    );
    assert.equal(calls[1].init.body, JSON.stringify({ role: 'user' }));
  } finally {
    globalThis.fetch = originalFetch;
  }
});
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
node --experimental-strip-types --test tests/api.test.mjs
```

Expected: FAIL because admin helper exports do not exist.

- [ ] **Step 3: Implement admin types and normalizers**

Add to `src/lib/api.ts` after existing top-level types:

```ts
export type AdminProject = {
  id: string;
  name: string;
  userId: string;
  conceptCount: number;
  sourceCount: number;
};

export type AdminUser = {
  id: string;
  email: string;
  role: string;
  projectCount: number;
};
```

Add helper normalizers near existing normalizer functions:

```ts
function extractNamedArray(payload: unknown, keys: string[]): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];
  for (const key of keys) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }
  return [];
}

function normalizeAdminProject(item: unknown): AdminProject | null {
  const record = isRecord(item) ? item : {};
  const id = firstString(record, ['id', 'project_id', 'projectId']);
  const name = firstString(record, ['name', 'project_name', 'projectName']) ?? id;
  const userId = firstString(record, ['user_id', 'userId', 'uid']) ?? '';
  if (!id || !name) return null;
  return {
    id,
    name,
    userId,
    conceptCount: firstNumber(record, ['concept_count', 'conceptCount', 'concepts_count', 'conceptsCount']) ?? 0,
    sourceCount: firstNumber(record, ['source_count', 'sourceCount', 'sources_count', 'sourcesCount']) ?? 0,
  };
}

function normalizeAdminUser(item: unknown): AdminUser | null {
  const record = isRecord(item) ? item : {};
  const id = firstString(record, ['id', 'user_id', 'userId']);
  const email = firstString(record, ['email']) ?? '';
  if (!id) return null;
  return {
    id,
    email,
    role: firstString(record, ['role']) ?? 'user',
    projectCount: firstNumber(record, ['project_count', 'projectCount', 'projects_count', 'projectsCount']) ?? 0,
  };
}
```

- [ ] **Step 4: Implement admin API helpers**

Add to the end of `src/lib/api.ts`:

```ts
async function adminJson(path: string, options: Omit<ApiFetchOptions, 'requireProject'> = {}): Promise<unknown> {
  const response = await apiFetch(path, { ...options, requireProject: false });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: `API request failed (${response.status})` }));
    throw new Error((error as { error?: string }).error ?? `API request failed (${response.status})`);
  }
  return response.json().catch(() => ({}));
}

export async function getAdminProjects(): Promise<AdminProject[]> {
  const payload = await adminJson('/api/v1/admin/projects');
  return extractNamedArray(payload, ['projects', 'items', 'results', 'data'])
    .map(normalizeAdminProject)
    .filter((project): project is AdminProject => project !== null);
}

export async function renameAdminProject(id: string, name: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ name }),
  });
}

export async function deleteAdminProject(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function rebuildAdminProjectIndex(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/rebuild-index`, { method: 'POST' });
}

export async function triggerAdminProjectPipeline(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/pipeline`, { method: 'POST' });
}

export async function getAdminUsers(): Promise<AdminUser[]> {
  const payload = await adminJson('/api/v1/admin/users');
  return extractNamedArray(payload, ['users', 'items', 'results', 'data'])
    .map(normalizeAdminUser)
    .filter((user): user is AdminUser => user !== null);
}

export async function updateAdminUserRole(id: string, role: string): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ role }),
  });
}

export async function deleteAdminUser(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
```

- [ ] **Step 5: Run tests and verify GREEN**

Run:

```bash
node --experimental-strip-types --test tests/api.test.mjs
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/lib/api.ts tests/api.test.mjs
git commit -m "Add admin API helpers"
```

---

### Task 3: Admin Route, Guard, Tabs, and Read Tables

**Files:**
- Create: `src/app/admin/page.tsx`
- Create: `src/components/AdminClient.tsx`
- Test: `tests/lwc-72-admin-interface.test.mjs`

**Interfaces:**
- Consumes:
  - `useAuth()` from `src/lib/auth.tsx`
  - `getAdminProjects(): Promise<AdminProject[]>`
  - `getAdminUsers(): Promise<AdminUser[]>`
  - `Surface`, `Badge`
- Produces:
  - `AdminClient` React component
  - `/admin` route

- [ ] **Step 1: Write failing static test for route and read UI**

Append to `tests/lwc-72-admin-interface.test.mjs`:

```js
test('admin route renders AdminClient', async () => {
  const page = await readFile(
    new URL('../src/app/admin/page.tsx', import.meta.url),
    'utf8',
  );

  assert.match(page, /import \{ AdminClient \} from '@\/components\/AdminClient';/);
  assert.match(page, /return <AdminClient \/>;/);
});

test('AdminClient gates access, renders tabs, and loads admin tables', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /useAuth/);
  assert.match(adminClient, /user\?\.role !== 'admin'/);
  assert.match(adminClient, /Admin access required/);
  assert.match(adminClient, /Projects/);
  assert.match(adminClient, /Users/);
  assert.match(adminClient, /getAdminProjects/);
  assert.match(adminClient, /getAdminUsers/);
  assert.match(adminClient, /<table/);
  assert.match(adminClient, /Project name/);
  assert.match(adminClient, /Concept count/);
  assert.match(adminClient, /Source count/);
  assert.match(adminClient, /Project count/);
});
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
node --experimental-strip-types --test tests/lwc-72-admin-interface.test.mjs
```

Expected: FAIL because `AdminClient` and route do not exist.

- [ ] **Step 3: Create `/admin` route**

Create `src/app/admin/page.tsx`:

```tsx
"use client";

import { AdminClient } from "@/components/AdminClient";

export default function AdminPage() {
  return <AdminClient />;
}
```

- [ ] **Step 4: Implement `AdminClient` read-only shell**

Create `src/components/AdminClient.tsx` with these imports and state:

```tsx
'use client';

import { useCallback, useEffect, useState } from 'react';
import { RefreshCw, ShieldAlert } from 'lucide-react';
import {
  getAdminProjects,
  getAdminUsers,
  type AdminProject,
  type AdminUser,
} from '@/lib/api';
import { useAuth } from '@/lib/auth';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';

type Tab = 'projects' | 'users';
```

Implement:

```tsx
export function AdminClient() {
  const { hydrated, user } = useAuth();
  const [tab, setTab] = useState<Tab>('projects');
  const [projects, setProjects] = useState<AdminProject[]>([]);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [usersLoading, setUsersLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [usersError, setUsersError] = useState('');
  const isAdmin = user?.role === 'admin';

  const loadProjects = useCallback(async () => {
    setProjectsLoading(true);
    setProjectsError('');
    try {
      setProjects(await getAdminProjects());
    } catch (error) {
      setProjectsError(error instanceof Error ? error.message : 'Unable to load projects.');
    } finally {
      setProjectsLoading(false);
    }
  }, []);

  const loadUsers = useCallback(async () => {
    setUsersLoading(true);
    setUsersError('');
    try {
      setUsers(await getAdminUsers());
    } catch (error) {
      setUsersError(error instanceof Error ? error.message : 'Unable to load users.');
    } finally {
      setUsersLoading(false);
    }
  }, []);

  const refreshActive = useCallback(() => {
    return tab === 'projects' ? loadProjects() : loadUsers();
  }, [loadProjects, loadUsers, tab]);

  useEffect(() => {
    if (!hydrated || !isAdmin) return;
    void loadProjects();
    void loadUsers();
  }, [hydrated, isAdmin, loadProjects, loadUsers]);

  if (!hydrated) {
    return <div className="py-20 text-center text-sm text-zinc-500">Loading...</div>;
  }

  if (!isAdmin) {
    return (
      <Surface variant="glass" className="mx-auto max-w-lg p-6 text-center">
        <ShieldAlert className="mx-auto size-8 text-amber-300" aria-hidden="true" />
        <h1 className="mt-4 text-xl font-semibold text-white">Admin access required</h1>
        <p className="mt-2 text-sm text-zinc-400">Your account does not have permission to use this console.</p>
      </Surface>
    );
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">Admin</h1>
          <p className="mt-2 max-w-2xl text-zinc-400">Manage projects, users, and pipeline operations.</p>
        </div>
        <button
          type="button"
          onClick={() => void refreshActive()}
          className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-white/10 bg-white/5 px-3 text-sm font-medium text-zinc-200 transition hover:bg-white/10"
        >
          <RefreshCw className="size-4" aria-hidden="true" />
          Refresh
        </button>
      </header>

      <Surface variant="glass" className="p-2">
        <div className="flex gap-1">
          <TabButton active={tab === 'projects'} onClick={() => setTab('projects')}>Projects</TabButton>
          <TabButton active={tab === 'users'} onClick={() => setTab('users')}>Users</TabButton>
        </div>
      </Surface>

      {tab === 'projects' ? (
        <ProjectsTable projects={projects} loading={projectsLoading} error={projectsError} onRetry={loadProjects} />
      ) : (
        <UsersTable users={users} loading={usersLoading} error={usersError} onRetry={loadUsers} />
      )}
    </div>
  );
}
```

Implement `TabButton`, `ProjectsTable`, and `UsersTable` in the same file:

```tsx
function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`min-h-10 rounded-md px-4 text-sm font-medium transition ${
        active ? 'bg-white/10 text-white' : 'text-zinc-400 hover:bg-white/5 hover:text-zinc-200'
      }`}
    >
      {children}
    </button>
  );
}
```

Use native tables with the required headers and empty-state text:

```tsx
function ProjectsTable({ projects, loading, error, onRetry }: { projects: AdminProject[]; loading: boolean; error: string; onRetry: () => void }) {
  return (
    <Surface variant="glass" className="overflow-hidden">
      <TableStatus loading={loading} error={error} empty={!loading && projects.length === 0} emptyLabel="No projects found." onRetry={onRetry} />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-left text-sm">
          <thead className="border-b border-white/10 text-xs uppercase text-zinc-500">
            <tr>
              <th className="px-4 py-3 font-medium">Project name</th>
              <th className="px-4 py-3 font-medium">User ID</th>
              <th className="px-4 py-3 font-medium">Concept count</th>
              <th className="px-4 py-3 font-medium">Source count</th>
              <th className="px-4 py-3 text-right font-medium">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-white/8">
            {projects.map((project) => (
              <tr key={project.id}>
                <td className="px-4 py-3 font-medium text-white">{project.name}</td>
                <td className="px-4 py-3 font-mono text-xs text-zinc-500">{project.userId || '—'}</td>
                <td className="px-4 py-3 tabular-nums text-zinc-300">{project.conceptCount}</td>
                <td className="px-4 py-3 tabular-nums text-zinc-300">{project.sourceCount}</td>
                <td className="px-4 py-3 text-right text-zinc-500">Actions</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Surface>
  );
}
```

Create `UsersTable` with columns `Email`, `Role`, `Project count`, `Actions` and role rendered through `<Badge variant={user.role === 'admin' ? 'accent' : 'muted'}>{user.role}</Badge>`.

Create `TableStatus` with loading, error + retry button, and empty message.

- [ ] **Step 5: Run tests and verify GREEN**

Run:

```bash
node --experimental-strip-types --test tests/lwc-72-admin-interface.test.mjs
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/app/admin/page.tsx src/components/AdminClient.tsx tests/lwc-72-admin-interface.test.mjs
git commit -m "Add admin console read view"
```

---

### Task 4: Admin Action Modals and Mutations

**Files:**
- Modify: `src/components/AdminClient.tsx`
- Test: `tests/lwc-72-admin-interface.test.mjs`

**Interfaces:**
- Consumes:
  - `renameAdminProject`
  - `deleteAdminProject`
  - `rebuildAdminProjectIndex`
  - `triggerAdminProjectPipeline`
  - `updateAdminUserRole`
  - `deleteAdminUser`
- Produces:
  - Project row actions with modal confirmation.
  - User row actions with modal confirmation.
  - Success/error notices.

- [ ] **Step 1: Write failing static test for action coverage**

Append to `tests/lwc-72-admin-interface.test.mjs`:

```js
test('AdminClient wires project and user actions through confirmation modals', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /renameAdminProject/);
  assert.match(adminClient, /deleteAdminProject/);
  assert.match(adminClient, /rebuildAdminProjectIndex/);
  assert.match(adminClient, /triggerAdminProjectPipeline/);
  assert.match(adminClient, /updateAdminUserRole/);
  assert.match(adminClient, /deleteAdminUser/);
  assert.match(adminClient, /ConfirmActionModal/);
  assert.match(adminClient, /RoleActionModal/);
  assert.match(adminClient, /RenameProjectModal/);
  assert.doesNotMatch(adminClient, /window\.confirm/);
  assert.match(adminClient, /setNotice/);
  assert.match(adminClient, /await loadProjects\(\)/);
  assert.match(adminClient, /await loadUsers\(\)/);
});
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
node --experimental-strip-types --test tests/lwc-72-admin-interface.test.mjs
```

Expected: FAIL because action handlers and modals do not exist.

- [ ] **Step 3: Add action imports and action state**

Update `src/components/AdminClient.tsx` imports:

```tsx
import { Pencil, Play, RefreshCw, RotateCcw, ShieldAlert, Trash2 } from 'lucide-react';
import {
  deleteAdminProject,
  deleteAdminUser,
  getAdminProjects,
  getAdminUsers,
  rebuildAdminProjectIndex,
  renameAdminProject,
  triggerAdminProjectPipeline,
  updateAdminUserRole,
  type AdminProject,
  type AdminUser,
} from '@/lib/api';
```

Add types:

```tsx
type Notice = { tone: 'success' | 'error'; message: string } | null;
type Action =
  | { kind: 'rename-project'; project: AdminProject }
  | { kind: 'delete-project'; project: AdminProject }
  | { kind: 'rebuild-project'; project: AdminProject }
  | { kind: 'trigger-project'; project: AdminProject }
  | { kind: 'change-role'; user: AdminUser }
  | { kind: 'delete-user'; user: AdminUser };
```

Add state in `AdminClient`:

```tsx
const [notice, setNotice] = useState<Notice>(null);
const [action, setAction] = useState<Action | null>(null);
const [actionError, setActionError] = useState('');
const [actionPending, setActionPending] = useState(false);
```

- [ ] **Step 4: Add action submit handlers**

Add in `AdminClient`:

```tsx
const closeAction = () => {
  if (actionPending) return;
  setAction(null);
  setActionError('');
};

const submitRenameProject = async (name: string) => {
  if (!action || action.kind !== 'rename-project') return;
  setActionPending(true);
  setActionError('');
  try {
    await renameAdminProject(action.project.id, name);
    await loadProjects();
    setNotice({ tone: 'success', message: 'Project renamed.' });
    setAction(null);
  } catch (error) {
    setActionError(error instanceof Error ? error.message : 'Rename failed.');
  } finally {
    setActionPending(false);
  }
};

const submitRoleChange = async (role: string) => {
  if (!action || action.kind !== 'change-role') return;
  setActionPending(true);
  setActionError('');
  try {
    await updateAdminUserRole(action.user.id, role);
    await loadUsers();
    setNotice({ tone: 'success', message: 'User role updated.' });
    setAction(null);
  } catch (error) {
    setActionError(error instanceof Error ? error.message : 'Role update failed.');
  } finally {
    setActionPending(false);
  }
};

const submitConfirmAction = async () => {
  if (!action) return;
  setActionPending(true);
  setActionError('');
  try {
    if (action.kind === 'delete-project') {
      await deleteAdminProject(action.project.id);
      await loadProjects();
      setNotice({ tone: 'success', message: 'Project deleted.' });
    } else if (action.kind === 'rebuild-project') {
      await rebuildAdminProjectIndex(action.project.id);
      await loadProjects();
      setNotice({ tone: 'success', message: 'Index rebuild started.' });
    } else if (action.kind === 'trigger-project') {
      await triggerAdminProjectPipeline(action.project.id);
      await loadProjects();
      setNotice({ tone: 'success', message: 'Pipeline triggered.' });
    } else if (action.kind === 'delete-user') {
      await deleteAdminUser(action.user.id);
      await loadUsers();
      setNotice({ tone: 'success', message: 'User deleted.' });
    }
    setAction(null);
  } catch (error) {
    setActionError(error instanceof Error ? error.message : 'Action failed.');
  } finally {
    setActionPending(false);
  }
};
```

- [ ] **Step 5: Add notices and action buttons**

Render notice below header:

```tsx
{notice ? (
  <Surface
    variant="default"
    className={`px-4 py-3 text-sm ${notice.tone === 'error' ? 'text-amber-200' : 'text-emerald-200'}`}
  >
    {notice.message}
  </Surface>
) : null}
```

Pass `onAction={setAction}` to `ProjectsTable` and `UsersTable`.

Replace project Actions cell with:

```tsx
<div className="flex justify-end gap-1">
  <IconAction label="Rename" icon={Pencil} onClick={() => onAction({ kind: 'rename-project', project })} />
  <IconAction label="Rebuild index" icon={RotateCcw} onClick={() => onAction({ kind: 'rebuild-project', project })} />
  <IconAction label="Trigger pipeline" icon={Play} onClick={() => onAction({ kind: 'trigger-project', project })} />
  <IconAction label="Delete" icon={Trash2} danger onClick={() => onAction({ kind: 'delete-project', project })} />
</div>
```

Replace user Actions cell with:

```tsx
<div className="flex justify-end gap-1">
  <IconAction label="Change role" icon={Pencil} onClick={() => onAction({ kind: 'change-role', user })} />
  <IconAction label="Delete user" icon={Trash2} danger onClick={() => onAction({ kind: 'delete-user', user })} />
</div>
```

Implement `IconAction`:

```tsx
function IconAction({
  label,
  icon: Icon,
  danger = false,
  onClick,
}: {
  label: string;
  icon: React.ComponentType<{ className?: string; 'aria-hidden'?: boolean }>;
  danger?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
      className={`inline-flex min-h-10 min-w-10 items-center justify-center rounded-md border transition ${
        danger
          ? 'border-red-400/20 text-red-300 hover:bg-red-400/10'
          : 'border-white/10 text-zinc-300 hover:bg-white/10 hover:text-white'
      }`}
    >
      <Icon className="size-4" aria-hidden />
    </button>
  );
}
```

- [ ] **Step 6: Add modal components**

At the bottom of `AdminClient.tsx`, add `ModalFrame`, `RenameProjectModal`, `RoleActionModal`, and `ConfirmActionModal`.

`ModalFrame`:

```tsx
function ModalFrame({
  title,
  children,
  onClose,
}: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 px-4 backdrop-blur-sm">
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="w-full max-w-md rounded-lg border border-white/10 bg-zinc-950 p-5 shadow-2xl"
      >
        <h2 className="text-lg font-semibold text-white">{title}</h2>
        {children}
        <button type="button" onClick={onClose} className="sr-only">Close</button>
      </div>
    </div>
  );
}
```

`RenameProjectModal`:

```tsx
function RenameProjectModal({
  project,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  project: AdminProject;
  pending: boolean;
  error: string;
  onSubmit: (name: string) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(project.name);
  return (
    <ModalFrame title="Rename project" onClose={onClose}>
      <label className="mt-4 block text-sm text-zinc-400">
        Project name
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          className="mt-2 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus:border-emerald-400"
        />
      </label>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel="Rename"
        pendingLabel="Renaming..."
        disabled={!name.trim()}
        onSubmit={() => onSubmit(name.trim())}
        onClose={onClose}
      />
    </ModalFrame>
  );
}
```

`RoleActionModal`:

```tsx
function RoleActionModal({
  user,
  pending,
  error,
  onSubmit,
  onClose,
}: {
  user: AdminUser;
  pending: boolean;
  error: string;
  onSubmit: (role: string) => void;
  onClose: () => void;
}) {
  const [role, setRole] = useState(user.role === 'admin' ? 'admin' : 'user');
  return (
    <ModalFrame title="Change role" onClose={onClose}>
      <p className="mt-3 text-sm text-zinc-400">{user.email || user.id}</p>
      <select
        value={role}
        onChange={(event) => setRole(event.target.value)}
        className="mt-4 min-h-11 w-full rounded-lg border border-white/10 bg-black/30 px-3 text-sm text-white outline-none focus:border-emerald-400"
      >
        <option value="user">User</option>
        <option value="admin">Admin</option>
      </select>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel="Update role"
        pendingLabel="Updating..."
        onSubmit={() => onSubmit(role)}
        onClose={onClose}
      />
    </ModalFrame>
  );
}
```

`ConfirmActionModal`:

```tsx
function ConfirmActionModal({
  title,
  description,
  submitLabel,
  pendingLabel,
  pending,
  error,
  danger = false,
  onSubmit,
  onClose,
}: {
  title: string;
  description: string;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  error: string;
  danger?: boolean;
  onSubmit: () => void;
  onClose: () => void;
}) {
  return (
    <ModalFrame title={title} onClose={onClose}>
      <p className="mt-3 text-sm leading-6 text-zinc-400">{description}</p>
      {error ? <p className="mt-3 text-sm text-amber-300">{error}</p> : null}
      <ModalActions
        pending={pending}
        submitLabel={submitLabel}
        pendingLabel={pendingLabel}
        danger={danger}
        onSubmit={onSubmit}
        onClose={onClose}
      />
    </ModalFrame>
  );
}
```

`ModalActions`:

```tsx
function ModalActions({
  pending,
  submitLabel,
  pendingLabel,
  disabled = false,
  danger = false,
  onSubmit,
  onClose,
}: {
  pending: boolean;
  submitLabel: string;
  pendingLabel: string;
  disabled?: boolean;
  danger?: boolean;
  onSubmit: () => void;
  onClose: () => void;
}) {
  return (
    <div className="mt-5 flex justify-end gap-2">
      <button
        type="button"
        disabled={pending}
        onClick={onClose}
        className="min-h-11 rounded-lg border border-white/10 px-4 text-sm text-zinc-300 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-60"
      >
        Cancel
      </button>
      <button
        type="button"
        disabled={pending || disabled}
        onClick={onSubmit}
        className={`min-h-11 rounded-lg px-4 text-sm font-semibold transition disabled:cursor-not-allowed disabled:opacity-60 ${
          danger ? 'bg-red-400 text-zinc-950 hover:bg-red-300' : 'bg-emerald-400 text-zinc-950 hover:bg-emerald-300'
        }`}
      >
        {pending ? pendingLabel : submitLabel}
      </button>
    </div>
  );
}
```

- [ ] **Step 7: Render active modal**

Add before closing root `<div>` in `AdminClient`:

```tsx
{action?.kind === 'rename-project' ? (
  <RenameProjectModal
    project={action.project}
    pending={actionPending}
    error={actionError}
    onSubmit={(name) => void submitRenameProject(name)}
    onClose={closeAction}
  />
) : null}
{action?.kind === 'change-role' ? (
  <RoleActionModal
    user={action.user}
    pending={actionPending}
    error={actionError}
    onSubmit={(role) => void submitRoleChange(role)}
    onClose={closeAction}
  />
) : null}
{action?.kind === 'delete-project' ? (
  <ConfirmActionModal
    title="Delete project"
    description={`Delete ${action.project.name} (${action.project.id})? This cannot be undone.`}
    submitLabel="Delete project"
    pendingLabel="Deleting..."
    danger
    pending={actionPending}
    error={actionError}
    onSubmit={() => void submitConfirmAction()}
    onClose={closeAction}
  />
) : null}
{action?.kind === 'rebuild-project' ? (
  <ConfirmActionModal
    title="Rebuild index"
    description={`Rebuild the search index for ${action.project.name} (${action.project.id}).`}
    submitLabel="Rebuild index"
    pendingLabel="Starting..."
    pending={actionPending}
    error={actionError}
    onSubmit={() => void submitConfirmAction()}
    onClose={closeAction}
  />
) : null}
{action?.kind === 'trigger-project' ? (
  <ConfirmActionModal
    title="Trigger pipeline"
    description={`Trigger the pipeline for ${action.project.name} (${action.project.id}).`}
    submitLabel="Trigger pipeline"
    pendingLabel="Triggering..."
    pending={actionPending}
    error={actionError}
    onSubmit={() => void submitConfirmAction()}
    onClose={closeAction}
  />
) : null}
{action?.kind === 'delete-user' ? (
  <ConfirmActionModal
    title="Delete user"
    description={`Delete ${action.user.email || action.user.id}? This deletes their user record and projects.`}
    submitLabel="Delete user"
    pendingLabel="Deleting..."
    danger
    pending={actionPending}
    error={actionError}
    onSubmit={() => void submitConfirmAction()}
    onClose={closeAction}
  />
) : null}
```

- [ ] **Step 8: Run tests and lint**

Run:

```bash
node --experimental-strip-types --test tests/lwc-72-admin-interface.test.mjs
npm test
npm run lint
```

Expected: all commands PASS.

- [ ] **Step 9: Commit**

```bash
git add src/components/AdminClient.tsx tests/lwc-72-admin-interface.test.mjs
git commit -m "Add admin console actions"
```

---

## Final Verification

After all tasks:

- [ ] Run `npm test`
- [ ] Run `npm run lint`
- [ ] Run `git status --short --branch`
- [ ] Confirm the implementation satisfies every item in `docs/superpowers/specs/2026-07-08-admin-interface-design.md`
- [ ] Update `LWC-72` YouTrack with implementation status and verification results
