import assert from 'node:assert/strict';
import test from 'node:test';

import { configureApiAuth, getAdminSettings, getPublicConfig, publishAnnouncement, updateAdminSettings } from '../src/lib/api.ts';

const originalFetch = globalThis.fetch;

test.afterEach(() => {
  globalThis.fetch = originalFetch;
});

test('public config preserves announcement markdown and fails safe when unavailable', async () => {
  globalThis.fetch = async () => Response.json({ registration_enabled: true, announcement_markdown: '# Live' });
  assert.deepEqual(await getPublicConfig({ refresh: true }), {
    registration_enabled: true,
    announcement_markdown: '# Live',
  });

  globalThis.fetch = async () => new Response('unavailable', { status: 503 });
  assert.deepEqual(await getPublicConfig({ refresh: true }), {
    registration_enabled: false,
  });
});

test('admin settings and publish use the frozen announcement schema', async () => {
  configureApiAuth({
    getAccessToken: () => 'test-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => {},
  });
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });
    if (init.method === 'POST') return Response.json({ announcement_published_markdown: '# Published' });
    if (init.method === 'PATCH') return Response.json({ registration_enabled: true, announcement_draft_markdown: '# Draft' });
    return Response.json({ registration_enabled: true, announcement_draft_markdown: '# Draft', announcement_published_markdown: '# Published' });
  };

  assert.deepEqual(await getAdminSettings(), {
    registration_enabled: true,
    announcement_draft_markdown: '# Draft',
    announcement_published_markdown: '# Published',
  });
  assert.deepEqual(await updateAdminSettings({ announcement_draft_markdown: '# New draft' }), {
    registration_enabled: true,
    announcement_draft_markdown: '# Draft',
  });
  await publishAnnouncement();

  assert.equal(calls[1].init.body, JSON.stringify({ announcement_draft_markdown: '# New draft' }));
  assert.match(calls[2].url, /\/api\/v1\/admin\/settings\/announcement\/publish$/);
  assert.equal(calls[2].init.method, 'POST');
});
