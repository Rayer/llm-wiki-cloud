import assert from 'node:assert/strict';
import test from 'node:test';

import {
  ApiError,
  apiFetch,
  buildProjectHeaders,
  buildRequestInit,
  configureApiAuth,
  deleteAdminProject,
  deleteAdminUser,
  getPipelineLog,
  getPipelineStatus,
  getAdminProjects,
  getAdminUsers,
  getRawFilePreview,
  getRawFiles,
  getStatus,
  rebuildAdminProjectIndex,
  renameAdminProject,
  normalizeSearchResponse,
  RAW_UPLOAD_MAX_BYTES,
  triggerAdminProjectPipeline,
  toV1Path,
  triggerPipeline,
  updateAdminUserRole,
  uploadRawFile,
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

test('normalizeSearchResponse preserves query expansion keywords', () => {
  const response = normalizeSearchResponse({
    results: [{ slug: 'park', title: 'Park', type: 'concept' }],
    expand: {
      keywords: ['台北', '親子', '公園'],
      suggestions: ['週末早上去'],
    },
  });

  assert.deepEqual(response.expand?.keywords, ['台北', '親子', '公園']);
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

test('getRawFiles reads project scoped raw metadata and normalizes file fields', async () => {
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
      files: [
        {
          name: 'article.md',
          size: 12345,
          updated: '2026-07-09T10:00:00Z',
          sha256: 'abc123',
          ingested: true,
        },
        {
          name: 'missing-size.md',
          ingested: false,
        },
      ],
    });
  };

  try {
    const files = await getRawFiles();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/raw');
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], 'project-1');
    assert.deepEqual(files.map(({ name, size, updated, sha256, ingested }) => ({
      name,
      size,
      updated,
      sha256,
      ingested,
    })), [
      {
        name: 'article.md',
        size: 12345,
        updated: '2026-07-09T10:00:00Z',
        sha256: 'abc123',
        ingested: true,
      },
      {
        name: 'missing-size.md',
        size: 0,
        updated: '',
        sha256: '',
        ingested: false,
      },
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getRawFilePreview reads project scoped raw text with encoded filename', async () => {
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
    return new Response('# Raw\n', {
      headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
    });
  };

  try {
    const content = await getRawFilePreview('記事 sample.md');

    assert.equal(
      requestedUrl,
      'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/raw/%E8%A8%98%E4%BA%8B%20sample.md?preview=true',
    );
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], 'project-1');
    assert.equal(content, '# Raw\n');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile accepts created and already_exists responses', async () => {
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
  const file = new File(['# hi\n'], 'note.md', { type: 'text/markdown' });

  try {
    globalThis.fetch = async () =>
      new Response(
        JSON.stringify({
          filename: 'note.md',
          path: 'users/u/projects/p/raw/note.md',
          bytes: 5,
          sha256: 'abc',
          status: 'created',
        }),
        { status: 201 },
      );

    const created = await uploadRawFile(file);
    assert.equal(created.status, 'created');
    assert.equal(created.filename, 'note.md');
    assert.equal(created.sha256, 'abc');

    globalThis.fetch = async () =>
      new Response(
        JSON.stringify({
          filename: 'note.md',
          path: 'users/u/projects/p/raw/note.md',
          bytes: 5,
          sha256: 'abc',
          status: 'already_exists',
        }),
        { status: 200 },
      );

    const existing = await uploadRawFile(file);
    assert.equal(existing.status, 'already_exists');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile maps 409 conflict to ApiError', async () => {
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
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify({ error: 'filename already exists with different content' }),
      { status: 409 },
    );

  try {
    await assert.rejects(
      () => uploadRawFile(new File(['# other\n'], 'note.md', { type: 'text/markdown' })),
      (err) => {
        assert.equal(err instanceof ApiError, true);
        assert.equal(err.status, 409);
        assert.match(err.message, /already exists with different content/);
        return true;
      },
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile rejects oversized files before fetch', async () => {
  const big = new File([new Uint8Array(RAW_UPLOAD_MAX_BYTES + 1)], 'big.md');
  await assert.rejects(() => uploadRawFile(big), /file too large/);
});

test('getAdminProjects reads admin projects without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return Response.json({
      projects: [
        {
          id: 'user-1_proj-1',
          name: 'Demo',
          user_id: 'user-1',
          concept_count: 2,
          source_count: 3,
        },
      ],
    });
  };

  try {
    const projects = await getAdminProjects();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects');
    assert.equal(requestedInit.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers['X-Project-ID'], undefined);
    assert.deepEqual(projects, [
      {
        id: 'user-1_proj-1',
        name: 'Demo',
        userId: 'user-1',
        conceptCount: 2,
        sourceCount: 3,
      },
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getAdminProjects preserves backend status on admin API errors', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    Response.json({ error: 'Forbidden' }, { status: 403 });

  try {
    await assert.rejects(
      () => getAdminProjects(),
      (error) => {
        assert.ok(error instanceof ApiError);
        assert.equal(error.status, 403);
        assert.equal(error.message, 'Forbidden');
        return true;
      },
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('admin project mutations use admin endpoints without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    return Response.json({ status: 'ok' });
  };

  try {
    await renameAdminProject('project-1', 'New name');
    await rebuildAdminProjectIndex('project-1');
    await triggerAdminProjectPipeline('project-1');
    await deleteAdminProject('project-1');

    assert.deepEqual(
      calls.map((call) => [call.url, call.init.method, call.init.headers['X-Project-ID']]),
      [
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1', 'PATCH', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1/rebuild-index', 'POST', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1/pipeline', 'POST', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/projects/project-1', 'DELETE', undefined],
      ],
    );
    assert.equal(calls[0].init.body, JSON.stringify({ name: 'New name' }));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getAdminUsers and user mutations use admin endpoints without project header', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    if (String(url).endsWith('/api/v1/admin/users')) {
      return Response.json({
        users: [
          { id: 'user-1', email: 'admin@example.com', role: 'admin', project_count: 4 },
        ],
      });
    }
    return Response.json({ status: 'ok' });
  };

  try {
    const users = await getAdminUsers();
    await updateAdminUserRole('user-1', 'user');
    await deleteAdminUser('user-1');

    assert.deepEqual(users, [
      { id: 'user-1', email: 'admin@example.com', role: 'admin', projectCount: 4 },
    ]);
    assert.deepEqual(
      calls.map((call) => [call.url, call.init.method, call.init.headers['X-Project-ID']]),
      [
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users', undefined, undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users/user-1', 'PATCH', undefined],
        ['https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/users/user-1', 'DELETE', undefined],
      ],
    );
    assert.equal(calls[1].init.body, JSON.stringify({ role: 'user' }));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch does not logout when access token missing and refresh returns null', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => null,
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  await assert.rejects(
    () => apiFetch('/api/v1/status'),
    /Authentication required|log in/i,
  );
  assert.equal(unauthorizedCalls, 0);
});

test('apiFetch does not logout on non-401 responses', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('missing', { status: 404 });

  try {
    const response = await apiFetch('/api/v1/concepts/missing');
    assert.equal(response.status, 404);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch logs out when response is 401 and refresh returns null', async () => {
  let unauthorizedCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('unauthorized', { status: 401 });

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(unauthorizedCalls, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch retries once after 401 when refresh succeeds', async () => {
  let unauthorizedCalls = 0;
  let fetchCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => 'fresh-token',
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (_url, init) => {
    fetchCount += 1;
    if (fetchCount === 1) return new Response('unauthorized', { status: 401 });
    const auth = init?.headers?.Authorization ?? init?.headers?.authorization;
    assert.match(String(auth), /Bearer fresh-token/);
    return Response.json({ ok: true });
  };

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 200);
    assert.equal(fetchCount, 2);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
