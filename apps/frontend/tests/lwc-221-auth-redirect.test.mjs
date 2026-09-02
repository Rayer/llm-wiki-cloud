import assert from 'node:assert/strict';
import test from 'node:test';

import {
  AUTH_FORCE_HOME_REDIRECT_KEY,
  clearForceHomeRedirect,
  consumeForceHomeRedirect,
  setForceHomeRedirect,
} from '../src/lib/auth-core.ts';

function buildStorage(initial = new Map()) {
  return {
    data: initial,
    getItem: (key) => (initial.has(key) ? initial.get(key) : null),
    setItem: (key, value) => {
      initial.set(key, String(value));
    },
    removeItem: (key) => {
      initial.delete(key);
    },
  };
}

test('LWC-221: force-home marker is one-shot and consumable', () => {
  const storage = buildStorage();

  assert.equal(setForceHomeRedirect(storage), undefined);
  assert.equal(storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY), '1');

  assert.equal(consumeForceHomeRedirect(storage), true);
  assert.equal(storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY), null);
  assert.equal(consumeForceHomeRedirect(storage), false);

  setForceHomeRedirect(storage);
  clearForceHomeRedirect(storage);
  assert.equal(consumeForceHomeRedirect(storage), false);
  assert.equal(storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY), null);
});

test('LWC-221: clearing marker when absent remains a no-op', () => {
  const storage = buildStorage();

  assert.equal(clearForceHomeRedirect(storage), undefined);
  assert.equal(storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY), null);
  assert.equal(consumeForceHomeRedirect(storage), false);
});
