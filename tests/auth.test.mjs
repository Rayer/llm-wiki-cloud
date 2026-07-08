import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeAuthResponse, normalizeRefreshResponse } from '../src/lib/auth-core.ts';

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
