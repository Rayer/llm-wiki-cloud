import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import {
  ACCESS_TOKEN_STORAGE_KEY,
  AUTH_USER_STORAGE_KEY,
  DEMO_SESSION_STORAGE_KEY,
  clearStoredAccessToken,
  clearStoredAuthUser,
  isAuthFailureStatus,
  normalizeAuthResponse,
  normalizeRegistrationResponse,
  normalizeRefreshResponse,
  persistAuthSession,
  readStoredAccessToken,
  readStoredAuthUser,
  writeStoredAccessToken,
  writeStoredAuthUser,
  writeStoredDemoSession,
} from '../src/lib/auth-core.ts';

const authSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), '../src/lib/auth.tsx'),
  'utf8',
);
const authCoreSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), '../src/lib/auth-core.ts'),
  'utf8',
);
const apiSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), '../src/lib/api.ts'),
  'utf8',
);

test('normalizeAuthResponse accepts access_token and nested user', () => {
  assert.deepEqual(
    normalizeAuthResponse({
      access_token: 'jwt-token',
      user: { id: 'user-1', email: 'person@example.com' },
    }),
    {
      access_token: 'jwt-token',
      user: { id: 'user-1', email: 'person@example.com' },
    },
  );
});

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

test('normalizeAuthResponse accepts user_id without using email as the user id', () => {
  assert.deepEqual(
    normalizeAuthResponse({
      access_token: 'jwt-token',
      user: { user_id: 'uid-123', username: 'person', email: 'person@example.com' },
    }),
    {
      access_token: 'jwt-token',
      user: { id: 'uid-123', email: 'person@example.com' },
    },
  );
});

test('normalizeRegistrationResponse accepts the BFF 201 registration contract', () => {
  assert.deepEqual(
    normalizeRegistrationResponse({
      token: 'jwt-token',
      user_id: 'user-1',
      email: 'person@example.com',
      default_project_id: 'project-1',
    }),
    {
      access_token: 'jwt-token',
      user: { id: 'user-1', email: 'person@example.com' },
    },
  );
});

test('BFF 201 registration persists a non-demo auth session', () => {
  const store = new Map();
  const storage = {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
  };
  writeStoredDemoSession(storage, true);

  persistAuthSession(
    storage,
    normalizeRegistrationResponse({
      token: 'jwt-token',
      user_id: 'user-1',
      email: 'person@example.com',
      default_project_id: 'project-1',
    }),
    false,
  );

  assert.equal(readStoredAccessToken(storage), 'jwt-token');
  assert.deepEqual(readStoredAuthUser(storage), { id: 'user-1', email: 'person@example.com' });
  assert.equal(storage.getItem(DEMO_SESSION_STORAGE_KEY), null);
});

test('normalizeRegistrationResponse fails closed for malformed registration payloads', () => {
  assert.throws(
    () => normalizeRegistrationResponse({ token: 'jwt-token', user_id: 'user-1', email: '' }),
    /valid user/i,
  );
});

test('normalizeAuthResponse rejects a response without an access_token', () => {
  assert.throws(
    () => normalizeAuthResponse({ user: { id: 'user-1', email: 'person@example.com' } }),
    /token/i,
  );
});

test('normalizeRefreshResponse accepts access_token without user', () => {
  assert.deepEqual(
    normalizeRefreshResponse({ access_token: 'fresh-token' }),
    { access_token: 'fresh-token' },
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
    /const refreshAccessToken = useCallback\(async \(options\?: RefreshAccessTokenOptions\) => \{[\s\S]*?\}, \[clearSession\]\);/,
  );
  assert.ok(refreshFn, 'refreshAccessToken callback not found');
  const body = refreshFn[0];
  assert.match(body, /isAuthFailureStatus/);
  assert.match(body, /clearOnAuthFailure/);
  // Bare catch that only clearSession is forbidden
  assert.doesNotMatch(body, /catch\s*\{\s*clearSession\(\);\s*return null;\s*\}/);
});

test('auth provider hydrate restores stored token then soft-rotates without clearing on failure', () => {
  const hydrateFn = authSource.match(
    /async function hydrateFromRefreshCookie\(\) \{[\s\S]*?\n    \}/,
  );
  assert.ok(hydrateFn, 'hydrateFromRefreshCookie not found');
  const body = hydrateFn[0];
  assert.match(body, /if \(stored/);
  assert.match(body, /setHydrated\(true\)/);
  // Stored path: hydrate immediately, soft-refresh must not clear session
  assert.match(body, /refreshAccessToken\(\{\s*clearOnAuthFailure:\s*false\s*\}\)/);
  // Soft-rotate is fire-and-forget (void), not blocking hydrate on cookie success
  assert.match(body, /void refreshAccessToken\(\{\s*clearOnAuthFailure:\s*false\s*\}\)/);
});

test('auth user storage helpers read write and clear', () => {
  const store = new Map();
  const storage = {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
  };

  assert.equal(AUTH_USER_STORAGE_KEY, 'llm-wiki-auth-user');
  assert.equal(readStoredAuthUser(storage), null);

  writeStoredAuthUser(storage, { id: 'u1', email: 'demo@llm-wiki.dev' });
  assert.deepEqual(readStoredAuthUser(storage), { id: 'u1', email: 'demo@llm-wiki.dev' });

  clearStoredAuthUser(storage);
  assert.equal(readStoredAuthUser(storage), null);
});

test('auth provider hydrates stored user with token', () => {
  assert.match(authSource, /readStoredAuthUser/);
  assert.match(authSource, /writeStoredAuthUser/);
  assert.match(authSource, /clearStoredAuthUser/);
  assert.match(authSource, /setUser\(readStoredAuthUser\(storage\)\)/);
});

test('auth provider normalizes registration responses into non-demo sessions', () => {
  const registerFn = authSource.match(
    /const register = useCallback\(async \(email: string, password: string\) => \{[\s\S]*?\}, \[applyAuthResponse\]\);/,
  );
  assert.ok(registerFn, 'register callback not found');
  assert.match(registerFn[0], /normalizeRegistrationResponse\(payload\)/);
  assert.match(registerFn[0], /\{ demo: false \}/);
});

test('auth provider delegates auth persistence to the session helper', () => {
  assert.match(authSource, /persistAuthSession\(/);
});

test('auth endpoints use NEXT_PUBLIC_AUTH_URL while product APIs keep NEXT_PUBLIC_API_URL', () => {
  assert.ok(
    authCoreSource.includes(
      "export const AUTH_URL = process.env.NEXT_PUBLIC_AUTH_URL ?? 'https://auth-dev.rayer.idv.tw';",
    ),
  );
  assert.ok(authSource.includes('${AUTH_URL}${path}'));
  assert.match(authSource, /\$\{AUTH_URL\}\/api\/v1\/auth\/refresh/);
  assert.ok(authSource.includes("postAuth('/api/v1/auth/login', { email, password })"));
  assert.ok(authSource.includes("postAuth('/api/v1/auth/register', { email, password })"));
  assert.ok(authSource.includes("postAuth('/api/v1/auth/logout')"));
  assert.ok(!authSource.includes('${API_URL}/api/v1/auth/'));
  assert.ok(apiSource.includes('const API_URL ='));
  assert.ok(!apiSource.includes('AUTH_URL'));
});
