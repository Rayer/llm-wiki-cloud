import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeLoginResponse } from '../src/lib/auth.ts';

test('normalizeLoginResponse accepts the deployed token and user shape', () => {
  assert.deepEqual(
    normalizeLoginResponse({
      token: 'jwt-token',
      user: { id: 'user-1', email: 'person@example.com' },
    }),
    {
      token: 'jwt-token',
      user: { id: 'user-1', email: 'person@example.com' },
    },
  );
});

test('normalizeLoginResponse rejects a response without a token', () => {
  assert.throws(
    () => normalizeLoginResponse({ user: { id: 'user-1', email: 'person@example.com' } }),
    /token/i,
  );
});
