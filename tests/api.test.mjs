import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildProjectHeaders,
  buildRequestInit,
  configureApiAuth,
  getPipelineLog,
  getPipelineStatus,
  getStatus,
  toV1Path,
  triggerPipeline,
} from '../src/lib/api.ts';

test('buildProjectHeaders scopes authenticated requests to the selected project', () => {
  assert.deepEqual(buildProjectHeaders('project-1', 'jwt-token', true), {
    Authorization: 'Bearer jwt-token',
    'Content-Type': 'application/json',
    'X-Project-ID': 'project-1',
  });
});

test('getStatus reads latest pipeline execution from the status endpoint', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => 'project-1',
    },
  };

  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return Response.json({
      sources_count: 2,
      concepts_count: 3,
      last_execution: {
        status: 'SUCCEEDED',
        duration: '12s',
        log_url: '/api/v1/pipeline/log?execution_id=exec-1',
      },
    });
  };

  try {
    const status = await getStatus();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/status');
    assert.equal(status.sourcesCount, 2);
    assert.equal(status.conceptsCount, 3);
    assert.equal(status.lastExecution?.status, 'SUCCEEDED');
    assert.equal(status.lastExecution?.log_url, '/api/v1/pipeline/log?execution_id=exec-1');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('buildRequestInit includes cookies for refresh-token auth', () => {
  assert.deepEqual(
    buildRequestInit({
      method: 'POST',
      projectId: 'project-1',
      accessToken: 'jwt-token',
      json: true,
    }),
    {
      method: 'POST',
      credentials: 'include',
      headers: {
        Authorization: 'Bearer jwt-token',
        'Content-Type': 'application/json',
        'X-Project-ID': 'project-1',
      },
    },
  );
});

test('toV1Path upgrades existing API paths without changing callers', () => {
  assert.equal(toV1Path('/api/query'), '/api/v1/query');
  assert.equal(toV1Path('/api/v1/projects'), '/api/v1/projects');
});

test('triggerPipeline requires a selected project before calling the API', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => null,
    },
  };

  await assert.rejects(
    () => triggerPipeline(),
    /Please select a project first/,
  );
});

test('getPipelineStatus reads the project scoped pipeline status endpoint', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => 'project-1',
    },
  };

  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return Response.json({
      last_execution: {
        status: 'SUCCEEDED',
        duration: '12s',
      },
    });
  };

  try {
    const status = await getPipelineStatus();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/pipeline/status');
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], 'project-1');
    assert.equal(status.last_execution.status, 'SUCCEEDED');
    assert.equal(status.last_execution.duration, '12s');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getPipelineLog reads text from the project scoped log URL', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => 'project-1',
    },
  };

  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return new Response('line 1\nline 2\n', {
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  };

  try {
    const log = await getPipelineLog('/api/v1/pipeline/log?execution_id=olw-pipeline-abc123');

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/pipeline/log?execution_id=olw-pipeline-abc123');
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], 'project-1');
    assert.equal(log, 'line 1\nline 2\n');
  } finally {
    globalThis.fetch = originalFetch;
  }
});
