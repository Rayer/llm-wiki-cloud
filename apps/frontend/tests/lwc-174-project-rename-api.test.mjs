import assert from 'node:assert/strict';
import test from 'node:test';

import { configureApiAuth } from '../src/lib/api.ts';
import { renameProject } from '../src/lib/projects.ts';

function installTokenMocks() {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => 'project-a',
    },
  };
}

test('renameProject sends PATCH to the owner project endpoint with strict name JSON', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => 'project-b',
    },
  };
  let requestedUrl = '';
  let requestedInit = {};
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init ?? {};
    return Response.json({ name: 'Renamed project' });
  };

  try {
    const nextName = await renameProject('project-a', '  Renamed project  ');

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/projects/project-a');
    assert.equal(requestedInit.method, 'PATCH');
    assert.equal(requestedInit.credentials, 'include');
    assert.equal(requestedInit.headers?.['Content-Type'], 'application/json');
    assert.equal(requestedInit.headers?.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers?.['X-Project-ID'], 'project-a');
    const requestBody = typeof requestedInit.body === 'string' ? requestedInit.body : '';
    assert.equal(requestBody, '{"name":"Renamed project"}');
    assert.equal(nextName, 'Renamed project');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('renameProject rejects unsafe project route segments before fetch', async () => {
  installTokenMocks();
  const originalFetch = globalThis.fetch;
  let fetchCount = 0;
  globalThis.fetch = async () => {
    fetchCount += 1;
    return Response.json({ name: 'unexpected' });
  };

  try {
    for (const projectId of ['', '.', '..', '/', '\\', '%2f', '%5c', '%', '%zz', '\u0000', '\u001f', '\u007f']) {
      await assert.rejects(
        () => renameProject(projectId, 'Renamed project'),
        Error,
        projectId || '<empty>',
      );
    }
    assert.equal(fetchCount, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('renameProject preserves 400 and 404 contract failures from the API', async () => {
  installTokenMocks();
  const originalFetch = globalThis.fetch;

  for (const status of [400, 404]) {
    globalThis.fetch = async () => Response.json({ error: `error-${status}` }, { status });

    await assert.rejects(
      () => renameProject('project-a', 'Renamed project'),
      (error) => error instanceof Error && error.message.includes(`error-${status}`),
    );
  }

  globalThis.fetch = originalFetch;
});
