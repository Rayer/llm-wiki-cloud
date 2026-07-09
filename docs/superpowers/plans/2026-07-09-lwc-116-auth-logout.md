# LWC-116 Auth Logout Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Force logout only on real authorization failure (refresh 401 / business 401 after failed auth refresh); keep session on network/5xx/404 and reduce reload-driven refresh via access-token persistence.

**Architecture:** Extract pure auth helpers (status classification + localStorage access-token helpers) into `auth-core.ts` for unit tests. Wire `auth.tsx` so refresh clears session only on 401. Change `api.ts` so `accessTokenOrRefresh` never calls `onUnauthorized`; keep `apiFetch` 401-retry logout path.

**Tech Stack:** Next.js client components, TypeScript, Node test runner (`node --experimental-strip-types --test`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-09-lwc-116-auth-logout-design.md`
- Logout only on real auth failure (HTTP 401 on refresh, or business 401 + refresh null after auth failure path).
- Access token storage key must be exactly `llm-wiki-access-token`.
- Refresh token remains cookie-based (`credentials: 'include'`); do not store refresh tokens in localStorage.
- No new session-expired UI/toast.
- TDD: failing test before production change for each task.
- Before implementation: use `using-git-worktrees` to branch out (e.g. `fix/lwc-116-auth-logout`) from current base.

## File map

| File | Responsibility |
|------|----------------|
| `src/lib/auth-core.ts` | Pure helpers: `isAuthFailureStatus`, access-token storage read/write/clear, storage key constant |
| `src/lib/auth.tsx` | Session state, refresh classification, hydrate, login/logout, configureApiAuth, persist token |
| `src/lib/api.ts` | `accessTokenOrRefresh` throw-only; `apiFetch` 401-only logout |
| `tests/auth.test.mjs` | Helpers + storage classification tests |
| `tests/api.test.mjs` | `apiFetch` / token-or-refresh logout behavior tests |

---

### Task 1: Auth-core helpers (status + storage)

**Files:**
- Modify: `src/lib/auth-core.ts`
- Test: `tests/auth.test.mjs`

**Interfaces:**
- Produces: `ACCESS_TOKEN_STORAGE_KEY = 'llm-wiki-access-token'`
- Produces: `isAuthFailureStatus(status: number): boolean` — true only for `401`
- Produces: `readStoredAccessToken(storage: Pick<Storage, 'getItem'> | null | undefined): string | null`
- Produces: `writeStoredAccessToken(storage: Pick<Storage, 'setItem'> | null | undefined, token: string): void`
- Produces: `clearStoredAccessToken(storage: Pick<Storage, 'removeItem'> | null | undefined): void`

- [ ] **Step 1: Write the failing tests**

Append to `tests/auth.test.mjs`:

```js
import {
  ACCESS_TOKEN_STORAGE_KEY,
  clearStoredAccessToken,
  isAuthFailureStatus,
  normalizeAuthResponse,
  normalizeRefreshResponse,
  readStoredAccessToken,
  writeStoredAccessToken,
} from '../src/lib/auth-core.ts';

test('isAuthFailureStatus is true only for 401', () => {
  assert.equal(isAuthFailureStatus(401), true);
  assert.equal(isAuthFailureStatus(403), false);
  assert.equal(isAuthFailureStatus(500), false);
  assert.equal(isAuthFailureStatus(0), false);
});

test('access token storage helpers read write and clear', () => {
  const store = new Map();
  const storage = {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
  };

  assert.equal(ACCESS_TOKEN_STORAGE_KEY, 'llm-wiki-access-token');
  assert.equal(readStoredAccessToken(storage), null);

  writeStoredAccessToken(storage, 'jwt-abc');
  assert.equal(store.get(ACCESS_TOKEN_STORAGE_KEY), 'jwt-abc');
  assert.equal(readStoredAccessToken(storage), 'jwt-abc');

  clearStoredAccessToken(storage);
  assert.equal(readStoredAccessToken(storage), null);
});

test('access token storage helpers tolerate missing storage', () => {
  assert.equal(readStoredAccessToken(null), null);
  assert.equal(readStoredAccessToken(undefined), null);
  writeStoredAccessToken(null, 'x');
  clearStoredAccessToken(undefined);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- tests/auth.test.mjs`

Expected: FAIL — exports missing (`isAuthFailureStatus` / `ACCESS_TOKEN_STORAGE_KEY` / storage helpers not found).

- [ ] **Step 3: Implement helpers in `auth-core.ts`**

Append (keep existing exports):

```ts
export const ACCESS_TOKEN_STORAGE_KEY = 'llm-wiki-access-token';

export function isAuthFailureStatus(status: number): boolean {
  return status === 401;
}

export function readStoredAccessToken(
  storage: Pick<Storage, 'getItem'> | null | undefined,
): string | null {
  if (!storage) return null;
  try {
    const value = storage.getItem(ACCESS_TOKEN_STORAGE_KEY);
    return value && value.trim() ? value.trim() : null;
  } catch {
    return null;
  }
}

export function writeStoredAccessToken(
  storage: Pick<Storage, 'setItem'> | null | undefined,
  token: string,
): void {
  if (!storage || !token.trim()) return;
  try {
    storage.setItem(ACCESS_TOKEN_STORAGE_KEY, token.trim());
  } catch {
    // ignore quota / private mode
  }
}

export function clearStoredAccessToken(
  storage: Pick<Storage, 'removeItem'> | null | undefined,
): void {
  if (!storage) return;
  try {
    storage.removeItem(ACCESS_TOKEN_STORAGE_KEY);
  } catch {
    // ignore
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm test -- tests/auth.test.mjs`

Expected: PASS (all auth tests green).

- [ ] **Step 5: Commit**

```bash
git add src/lib/auth-core.ts tests/auth.test.mjs
git commit -m "feat(LWC-116): auth failure status and access-token storage helpers"
```

---

### Task 2: `apiFetch` / `accessTokenOrRefresh` logout rules

**Files:**
- Modify: `src/lib/api.ts` (`accessTokenOrRefresh` ~L171–180; leave `apiFetch` 401 path)
- Test: `tests/api.test.mjs`

**Interfaces:**
- Consumes: `configureApiAuth({ getAccessToken, refreshAccessToken, onUnauthorized })`
- Behavior change: when no access token and refresh returns null, throw without calling `onUnauthorized`
- Unchanged: non-401 responses never call `onUnauthorized`; 401 + refresh null still calls `onUnauthorized`

- [ ] **Step 1: Write the failing tests**

Append to `tests/api.test.mjs` (import `apiFetch` if not already imported):

```js
import {
  // existing imports...
  apiFetch,
  configureApiAuth,
} from '../src/lib/api.ts';

test('apiFetch does not logout when access token missing and refresh returns null', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => null,
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  await assert.rejects(
    () => apiFetch('/api/v1/status'),
    /Authentication required|log in/i,
  );
  assert.equal(unauthorizedCalls, 0);
});

test('apiFetch does not logout on non-401 responses', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('missing', { status: 404 });

  try {
    const response = await apiFetch('/api/v1/concepts/missing');
    assert.equal(response.status, 404);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch logs out when response is 401 and refresh returns null', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('unauthorized', { status: 401 });

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(unauthorizedCalls, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch retries once after 401 when refresh succeeds', async () => {
  let unauthorizedCalls = 0;
  let fetchCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => 'fresh-token',
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (_url, init) => {
    fetchCount += 1;
    if (fetchCount === 1) return new Response('unauthorized', { status: 401 });
    const auth = init?.headers?.Authorization ?? init?.headers?.authorization;
    assert.match(String(auth), /Bearer fresh-token/);
    return Response.json({ ok: true });
  };

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 200);
    assert.equal(fetchCount, 2);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- tests/api.test.mjs`

Expected: FAIL on first new test — current code calls `onUnauthorized()` when refresh returns null (unauthorizedCalls === 1). Other new tests may already pass.

- [ ] **Step 3: Implement minimal `accessTokenOrRefresh` change**

In `src/lib/api.ts`, replace `accessTokenOrRefresh` with:

```ts
async function accessTokenOrRefresh(): Promise<string> {
  const current = apiAuthConfig.getAccessToken();
  if (current) return current;

  const refreshed = await apiAuthConfig.refreshAccessToken();
  if (refreshed) return refreshed;

  throw new Error('Authentication required');
}
```

Do **not** change the `apiFetch` 401 block:

```ts
  if (response.status !== 401) return response;

  const refreshed = await apiAuthConfig.refreshAccessToken();
  if (!refreshed) {
    apiAuthConfig.onUnauthorized();
    return response;
  }

  return fetch(url, buildRequestInit({ ...options, projectId, accessToken: refreshed }));
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm test -- tests/api.test.mjs`

Expected: PASS (including the four new tests and existing suite).

- [ ] **Step 5: Commit**

```bash
git add src/lib/api.ts tests/api.test.mjs
git commit -m "fix(LWC-116): do not logout when pre-request refresh fails"
```

---

### Task 3: Refresh classification + persistence in AuthProvider

**Files:**
- Modify: `src/lib/auth.tsx`
- Test: `tests/auth.test.mjs` (file-source contract tests for critical lines) **and** pure logic already covered in Task 1

Because `AuthProvider` is a React client component, keep unit coverage on helpers + source-contract tests that assert the implementation uses the helpers and does not clear session on non-401 refresh failures. Optionally extract a small non-React `refreshAccessTokenRequest` if cleaner; prefer wiring inside `auth.tsx` with clear control flow.

**Interfaces:**
- Consumes: `isAuthFailureStatus`, `readStoredAccessToken`, `writeStoredAccessToken`, `clearStoredAccessToken` from `auth-core`
- `clearSession`: clears React state **and** stored access token
- `applyAuthResponse` / successful refresh: write token to storage
- `refreshAccessToken`: 401 → clearSession + null; other failures → null without clearSession; success without user still keeps token
- Hydrate: seed from storage, then soft-refresh; auth failure clears; transient failure keeps storage token

- [ ] **Step 1: Write the failing contract tests**

Append to `tests/auth.test.mjs`:

```js
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const authSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), '../src/lib/auth.tsx'),
  'utf8',
);

test('auth provider uses isAuthFailureStatus before clearing session on refresh', () => {
  assert.match(authSource, /isAuthFailureStatus/);
  assert.match(authSource, /writeStoredAccessToken/);
  assert.match(authSource, /clearStoredAccessToken/);
  assert.match(authSource, /readStoredAccessToken/);
});

test('auth provider refresh catch path does not always clearSession', () => {
  // The refresh implementation must not clearSession() inside a bare catch
  // that handles all errors. Auth failure must be gated by isAuthFailureStatus.
  const refreshFn = authSource.match(
    /const refreshAccessToken = useCallback\(async \(\) => \{[\s\S]*?\}, \[clearSession\]\);/,
  );
  assert.ok(refreshFn, 'refreshAccessToken callback not found');
  const body = refreshFn[0];
  assert.match(body, /isAuthFailureStatus/);
  // Bare catch that only clearSession is forbidden
  assert.doesNotMatch(body, /catch\s*\{\s*clearSession\(\);\s*return null;\s*\}/);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm test -- tests/auth.test.mjs`

Expected: FAIL — current `auth.tsx` has bare `catch { clearSession(); return null; }` and no storage helpers.

- [ ] **Step 3: Implement AuthProvider changes**

Update imports in `src/lib/auth.tsx`:

```ts
import {
  API_URL,
  clearStoredAccessToken,
  isAuthFailureStatus,
  normalizeAuthResponse,
  normalizeRefreshResponse,
  readStoredAccessToken,
  responseError,
  writeStoredAccessToken,
  type AuthResponse,
  type AuthUser,
} from './auth-core';
```

Replace `clearSession`:

```ts
  const clearSession = useCallback(() => {
    setAccessToken(null);
    accessTokenRef.current = null;
    setUser(null);
    clearStoredAccessToken(typeof window !== 'undefined' ? window.localStorage : null);
  }, []);
```

Replace `applyAuthResponse`:

```ts
  const applyAuthResponse = useCallback((result: AuthResponse) => {
    setAccessToken(result.access_token);
    accessTokenRef.current = result.access_token;
    setUser(result.user);
    writeStoredAccessToken(
      typeof window !== 'undefined' ? window.localStorage : null,
      result.access_token,
    );
  }, []);
```

Replace `refreshAccessToken` (no bare clear on all errors):

```ts
  const refreshAccessToken = useCallback(async () => {
    try {
      const response = await fetch(`${API_URL}/api/v1/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
      });
      const payload: unknown = await response.json().catch(() => null);

      if (!response.ok) {
        if (isAuthFailureStatus(response.status)) {
          clearSession();
        }
        return null;
      }

      const result = normalizeRefreshResponse(payload);
      setAccessToken(result.access_token);
      accessTokenRef.current = result.access_token;
      writeStoredAccessToken(
        typeof window !== 'undefined' ? window.localStorage : null,
        result.access_token,
      );
      if (result.user) {
        setUser(result.user);
      }
      return result.access_token;
    } catch {
      // Network / parse failures: keep session
      return null;
    }
  }, [clearSession]);
```

Replace hydrate effect:

```ts
  useEffect(() => {
    let cancelled = false;

    async function hydrateFromRefreshCookie() {
      const stored = readStoredAccessToken(
        typeof window !== 'undefined' ? window.localStorage : null,
      );
      if (stored && !cancelled) {
        setAccessToken(stored);
        accessTokenRef.current = stored;
      }

      const refreshed = await refreshAccessToken();
      if (!cancelled) {
        // refreshAccessToken already cleared on auth failure
        // transient failure keeps stored/in-memory token
        void refreshed;
        setHydrated(true);
      }
    }

    void hydrateFromRefreshCookie();

    return () => {
      cancelled = true;
    };
  }, [refreshAccessToken]);
```

Notes:

- Remove the hydrate branch `if (!refreshed) clearSession()` — that double-cleared on transient failures.
- `logout` already calls `clearSession` in `finally`; storage clears via `clearSession`.
- Do not use `postAuth` for refresh if it cannot expose status; dedicated `fetch` as above is intentional.
- `normalizeRefreshResponse` still throws if access_token missing on OK body — that lands in `catch` and must **not** clear session (already handled).

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
npm test -- tests/auth.test.mjs
npm test -- tests/api.test.mjs
npm test
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add src/lib/auth.tsx tests/auth.test.mjs
git commit -m "fix(LWC-116): clear session only on auth refresh failure; persist access token"
```

---

### Task 4: Regression sweep + handoff

**Files:**
- None expected (fix-only unless failures)

- [ ] **Step 1: Full test suite**

Run: `npm test`

Expected: 0 failures.

- [ ] **Step 2: Manual smoke checklist (document in PR body)**

1. Login → hard reload → still authenticated when BFF cookie valid.
2. While logged in, open a missing wiki slug / force a 404 API → still logged in.
3. DevTools: expire/delete refresh cookie, trigger API → logout once (401 path).
4. Logout → `localStorage` key `llm-wiki-access-token` removed.

- [ ] **Step 3: Final commit only if docs/comments needed; otherwise open PR**

```bash
git status
# If clean after Task 3, create PR from fix/lwc-116-auth-logout
```

PR title suggestion: `fix(LWC-116): logout only on real auth failure`

PR body should link YouTrack LWC-116 and the design spec path.

---

## Spec coverage checklist

| Spec requirement | Task |
|------------------|------|
| `accessTokenOrRefresh` no `onUnauthorized` | Task 2 |
| `apiFetch` 401-only logout | Task 2 (preserve + tests) |
| Refresh 401 → clearSession | Task 3 |
| Refresh network/5xx → no clear | Task 3 |
| Refresh OK without user keeps token | Task 3 |
| Persist access token key `llm-wiki-access-token` | Task 1 + 3 |
| Hydrate from storage + soft refresh | Task 3 |
| No bare clear on hydrate when refresh null | Task 3 |
| Tests for 404 / 401 / refresh null | Task 2 + auth helpers Task 1 |
| No new toast UI | N/A (not implemented) |
| Worktree at implementation time | Pre-step before Task 1 |

## Self-review notes

- No TBD placeholders.
- Types/names consistent: `isAuthFailureStatus`, `ACCESS_TOKEN_STORAGE_KEY`, storage helpers.
- TDD order preserved per task.
- React AuthProvider behavior covered via helpers + source contract tests (matches existing frontend testing style for UI files).
