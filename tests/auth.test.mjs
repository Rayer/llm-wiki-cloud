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
