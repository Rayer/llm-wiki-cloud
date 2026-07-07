import assert from 'node:assert/strict';
import test from 'node:test';

import { resolveWikilink } from '../src/lib/wikilinks.ts';

const API = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8080';

async function login() {
  const response = await fetch(`${API}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: 'demo@llm-wiki.dev', password: 'demo123456' }),
  });
  assert.equal(response.status, 200);
  const payload = await response.json();
  return payload.access_token;
}

async function fetchJson(path, token) {
  const response = await fetch(`${API}${path}`, {
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Project-ID': 'demo',
    },
  });
  assert.equal(response.status, 200, `${path} status ${response.status}`);
  return response.json();
}

function extractWikilinks(markdown) {
  return [...markdown.matchAll(/\[\[([^\]]+)\]\]/g)].map((match) => match[1]);
}

test('LWC-101 integration: local BFF source wikilinks resolve dead vs live concepts', async () => {
  const token = await login();
  const conceptsPayload = await fetchJson('/api/v1/concepts', token);
  const concepts = Array.isArray(conceptsPayload)
    ? conceptsPayload
    : conceptsPayload.concepts ?? [];
  const existing = new Set(concepts.map((concept) => concept.slug));

  const source = await fetchJson('/api/v1/sources/local-dev-source', token);
  const markdown = source.body ?? source.content ?? source.raw ?? '';
  const wikilinks = extractWikilinks(String(markdown));
  assert.ok(wikilinks.length >= 3, `expected wikilinks in source markdown, got ${wikilinks.length}`);

  const resolved = wikilinks.map((raw) => resolveWikilink(raw, 'concepts', existing));
  const dead = resolved.filter((item) => item.dead);
  const live = resolved.filter((item) => !item.dead);

  assert.ok(live.length >= 1, 'expected at least one live concept wikilink');
  assert.ok(dead.length >= 2, 'expected at least two dead concept wikilinks');
  assert.ok(dead.every((item) => item.href === null));
  assert.ok(live.every((item) => typeof item.href === 'string' && item.href.startsWith('/concepts/')));
});