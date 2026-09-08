import assert from 'node:assert/strict';
import test from 'node:test';
import { clearPublicConfigCache, getPublicConfig } from '../src/lib/api.ts';

test('LWC-324 public capabilities preserve all four independent method combinations', async () => {
 const originalFetch = globalThis.fetch;
 try {
  for (const email of [false, true]) for (const google of [false, true]) {
   globalThis.fetch = async () => Response.json({ registration_enabled: false, email_registration_enabled: email, google_registration_enabled: google });
   const config = await getPublicConfig({ refresh: true });
   assert.equal(config.email_registration_enabled, email);
   assert.equal(config.google_registration_enabled, google);
  }
 } finally { globalThis.fetch = originalFetch; clearPublicConfigCache(); }
});

test('LWC-324 missing methods inherit legacy; malformed values and request failures close signup', async () => {
 const originalFetch = globalThis.fetch;
 try {
  for (const [payload, email, google] of [
   [{ registration_enabled: false }, false, false],
   [{ registration_enabled: true }, true, true],
   [{ registration_enabled: true, email_registration_enabled: 'true', google_registration_enabled: null }, false, false],
   [{ email_registration_enabled: true }, true, false],
   [{}, false, false],
  ]) {
   globalThis.fetch = async () => Response.json(payload);
   const config = await getPublicConfig({ refresh: true });
   assert.equal(config.email_registration_enabled, email);
   assert.equal(config.google_registration_enabled, google);
  }
  for (const fetcher of [async () => { throw new Error('offline'); }, async () => new Response('', { status: 500 }), async () => new Response('invalid')]) {
   globalThis.fetch = fetcher;
   const config = await getPublicConfig({ refresh: true });
   assert.equal(config.email_registration_enabled, false);
   assert.equal(config.google_registration_enabled, false);
  }
 } finally { globalThis.fetch = originalFetch; clearPublicConfigCache(); }
});
