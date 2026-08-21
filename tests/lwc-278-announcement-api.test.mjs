import assert from 'node:assert/strict';
import test from 'node:test';

import { configureApiAuth, getAdminSettings, getPublicConfig, publishAnnouncement } from '../src/lib/api.ts';

const originalFetch = globalThis.fetch;

test.afterEach(() => {
  globalThis.fetch = originalFetch;
});

test('public config preserves announcement markdown and fails safe when unavailable', async () => {
  globalThis.fetch = async () => Response.json({ registration_enabled: true, announcement_markdown: '# Live', announcement_digest: `sha256:${'a'.repeat(64)}` });
  assert.deepEqual(await getPublicConfig({ refresh: true }), {
    registration_enabled: true,
    announcement_markdown: '# Live',
    announcement_digest: `sha256:${'a'.repeat(64)}`,
  });

  globalThis.fetch = async () => Response.json({ registration_enabled: true, announcement_markdown: '# Live', announcement_digest: 'sha256:not-valid' });
  assert.deepEqual(await getPublicConfig({ refresh: true }), {
    registration_enabled: true,
    announcement_markdown: '# Live',
    announcement_digest: null,
  });

  globalThis.fetch = async () => new Response('unavailable', { status: 503 });
  assert.deepEqual(await getPublicConfig({ refresh: true }), {
    registration_enabled: false,
  });
});

test('admin settings and publish use the direct-publish announcement schema', async () => {
  configureApiAuth({
    getAccessToken: () => 'test-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => {},
  });
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });
    if (init.method === 'POST') return Response.json({ registration_enabled: true, announcement_markdown: '# Published' });
    return Response.json({ registration_enabled: true, announcement_markdown: '# Published' });
  };

  assert.deepEqual(await getAdminSettings(), {
    registration_enabled: true,
    announcement_markdown: '# Published',
  });
  await publishAnnouncement('# New announcement');

  assert.match(calls[1].url, /\/api\/v1\/admin\/settings\/announcement\/publish$/);
  assert.equal(calls[1].init.method, 'POST');
  assert.equal(calls[1].init.body, JSON.stringify({ announcement_markdown: '# New announcement' }));
});

test('publish rejects a successful response with missing or invalid fields', async () => {
  configureApiAuth({
    getAccessToken: () => 'test-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => {},
  });
  for (const payload of [
    { announcement_markdown: '# Published' },
    { registration_enabled: 'yes', announcement_markdown: '# Published' },
    { registration_enabled: true },
    { registration_enabled: true, announcement_markdown: 42 },
  ]) {
    globalThis.fetch = async () => Response.json(payload, { status: 200 });
    await assert.rejects(() => publishAnnouncement('# New announcement'), /Invalid announcement publish response/);
  }
});
