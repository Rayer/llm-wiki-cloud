import assert from 'node:assert/strict';
import test from 'node:test';

import {
  ApiError,
  apiFetch,
  buildProjectHeaders,
  buildRequestInit,
  citationPathSegment,
  configureApiAuth,
  deleteAdminProject,
  deleteAdminUser,
  getPipelineLog,
  getPipelineStatus,
  getAdminProjects,
  getAdminUsers,
  getBuildInfo,
  getConcept,
  getSource,
  normalizeCitation,
  getRawFilePreview,
  getRawFiles,
  getStatus,
  rebuildAdminProjectIndex,
  renameAdminProject,
  normalizeSearchResponse,
  normalizeStatus,
  RAW_UPLOAD_MAX_BYTES,
  safeWikiRouteSegment,
  triggerAdminProjectPipeline,
  toV1Path,
  triggerPipeline,
  updateAdminUserRole,
  uploadRawFile,
} from '../src/lib/api.ts';

const buildInfoPayload = {
  product_version: '1.2.3',
  commit: 'abc1234',
  branch: 'main',
  tag: '',
  image_tag: '2026.07.15',
  service: 'llm-wiki-bff',
  revision: 'llm-wiki-bff-00042-abc',
};

test('normalizeStatus drops malformed diagnostic fields and accepts only BFF exit codes', () => {
  const status = normalizeStatus({
    last_execution: {
      diagnostic: {
        stage: { invalid: true },
        error_class: ['child_exit'],
        detail_code: 42,
        child_command: null,
        exit_code: 1.5,
      },
    },
  });

  assert.deepEqual(status.lastExecution?.diagnostic, {
    stage: null,
    error_class: null,
    detail_code: null,
    child_command: null,
    exit_code: null,
  });

  assert.equal(normalizeStatus({
    last_execution: {
      diagnostic: { stage: 'future_stage', exit_code: 255 },
    },
  }).lastExecution?.diagnostic?.stage, 'future_stage');
  assert.equal(normalizeStatus({
    last_execution: { diagnostic: { exit_code: 0 } },
  }).lastExecution?.diagnostic?.exit_code, 0);
  assert.equal(normalizeStatus({
    last_execution: { diagnostic: { exit_code: 256 } },
  }).lastExecution?.diagnostic?.exit_code, null);
  assert.equal(normalizeStatus({
    last_execution: { diagnostic: ['not-an-object'] },
  }).lastExecution?.diagnostic, null);
});

test('getBuildInfo reads the unauthenticated no-store public version endpoint', async () => {
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return Response.json(buildInfoPayload);
  };

  try {
    const buildInfo = await getBuildInfo();

    assert.equal(requestedUrl, 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/public/version');
    assert.deepEqual(requestedInit, {
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
    });
    assert.equal(requestedInit.headers?.Authorization, undefined);
    assert.equal(requestedInit.headers?.['X-Project-ID'], undefined);
    assert.deepEqual(buildInfo, buildInfoPayload);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getBuildInfo rejects an unsuccessful public version response', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(null, { status: 503 });

  try {
    await assert.rejects(
      () => getBuildInfo(),
      (error) => error instanceof ApiError && error.status === 503 && /Build info request failed \(503\)/.test(error.message),
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getBuildInfo rejects invalid JSON from the public version endpoint', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response('{not json', {
    headers: { 'Content-Type': 'application/json' },
  });

  try {
    await assert.rejects(() => getBuildInfo(), /Invalid build info response JSON/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getBuildInfo rejects build metadata with a missing field', async () => {
  const originalFetch = globalThis.fetch;
  const missingRevision = { ...buildInfoPayload };
  delete missingRevision.revision;
  globalThis.fetch = async () => Response.json(missingRevision);

  try {
    await assert.rejects(() => getBuildInfo(), /Invalid build info response: revision must be a string/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getBuildInfo rejects build metadata with a non-string field', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ ...buildInfoPayload, tag: null });

  try {
    await assert.rejects(() => getBuildInfo(), /Invalid build info response: tag must be a string/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('getBuildInfo rejects a public version network failure', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    throw new Error('network unavailable');
  };

  try {
    await assert.rejects(() => getBuildInfo(), /Build info request failed: network unavailable/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

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

test('normalizeCitation preserves canonical id and ignores invalid path lookup sources', () => {
  const citation = normalizeCitation({
    text: 'My Concept',
    slug: 'concept-from-title',
    id: 'concept-id',
    type: 'concept',
    path: 'https://evil.example.com/concepts/concept-id-concept-from-title',
    href: '/concepts/concept-id-concept-from-title',
  });

  assert.equal(citation?.text, 'My Concept');
  assert.equal(citation?.slug, 'concept-from-title');
  assert.equal(citation?.type, 'concept');
  assert.equal(citation?.id, 'concept-id');
  assert.equal(citation?.path, undefined);
});

test('normalizeCitation preserves a safe canonical id without requiring a safe slug', () => {
  assert.deepEqual(normalizeCitation({ text: 'ID only', id: 'canonical-id', type: 'concept' }), {
    text: 'ID only',
    slug: undefined,
    type: 'concept',
    id: 'canonical-id',
    path: undefined,
  });
  assert.deepEqual(
    normalizeCitation({ text: 'Unsafe slug', id: 'canonical-id', slug: '..', type: 'source' }),
    {
      text: 'Unsafe slug',
      slug: undefined,
      type: 'source',
      id: 'canonical-id',
      path: undefined,
    },
  );
});

test('normalizeCitation derives slug from a valid same-collection citation path', () => {
  const citation = normalizeCitation({
    text: 'Path source',
    type: 'source',
    path: '/sources/s-id-source-slug',
  });

  assert.deepEqual(citation, {
    text: 'Path source',
    slug: 's-id-source-slug',
    type: 'source',
    id: undefined,
    path: '/sources/s-id-source-slug',
  });
});

test('normalizeCitation rejects malformed citation paths as lookup sources', () => {
  assert.equal(
    normalizeCitation({
      text: 'Bad source',
      type: 'source',
      path: 'https://evil.example.com/sources/source-slug',
      slug: '',
    }),
    null,
  );
});

test('citation paths reject traversal, encoded separators, controls, and extra segments', () => {
  for (const path of [
    '/concepts/../sources/secret',
    '/concepts/%2e%2e',
    '/concepts/%2Fetc',
    '/concepts/%5c..%5csources%5csecret',
    '/concepts/name/extra',
    '/concepts/%00name',
  ]) {
    assert.equal(citationPathSegment('concept', path), null, path);
    assert.equal(
      normalizeCitation({ text: 'Unsafe', type: 'concept', path }),
      null,
      path,
    );
  }
});

test('citation paths accept one decoded same-collection segment with query or hash', () => {
  assert.equal(citationPathSegment('concept', '/concepts/caf%C3%A9?view=preview'), 'café');
  assert.equal(citationPathSegment('source', '/sources/source-name#excerpt'), 'source-name');
  assert.equal(citationPathSegment('concept', '/sources/source-name'), null);
});

test('citation route segments reject explicit traversal and API clients fail before fetch', async () => {
  for (const segment of ['.', '..', '/etc', '\\..\\sources', ' leading', 'trailing ', 'bad\u0000id']) {
    assert.equal(safeWikiRouteSegment(segment), null, segment);
  }
  assert.equal(safeWikiRouteSegment('%2e%2e'), '%2e%2e');

  assert.equal(normalizeCitation({ text: 'Unsafe', type: 'concept', id: '..', slug: '..' }), null);
  assert.deepEqual(
    normalizeCitation({ text: 'Fallback', type: 'source', id: '..', slug: 'safe-source' }),
    { text: 'Fallback', type: 'source', id: undefined, slug: 'safe-source', path: undefined },
  );

  const originalFetch = globalThis.fetch;
  let fetchCount = 0;
  globalThis.fetch = async () => {
    fetchCount += 1;
    throw new Error('unsafe lookup reached fetch');
  };
  try {
    await assert.rejects(() => getConcept('..'), (error) => error instanceof ApiError && error.status === 400);
    await assert.rejects(() => getSource('\\..\\concepts'), (error) => error instanceof ApiError && error.status === 400);
    assert.equal(fetchCount, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
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
      getItem: () => 'project-2',
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
    const log = await getPipelineLog('/api/v1/pipeline/log?execution_id=olw-pipeline-abc123', 'project-1');

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

  const originalXHR = globalThis.XMLHttpRequest;
  const file = new File(['# hi\n'], 'note.md', { type: 'text/markdown' });
  const requests = [];

  class FakeXMLHttpRequest {
    static scenarios = [];

    constructor() {
      this.headers = {};
      this.upload = {};
      requests.push(this);
    }

    open(method, url) {
      this.method = method;
      this.url = url;
    }

    setRequestHeader(name, value) {
      this.headers[name] = value;
    }

    send(body) {
      this.body = body;
      FakeXMLHttpRequest.scenarios.shift()(this);
    }

    respond(status, body) {
      this.status = status;
      this.responseText = JSON.stringify(body);
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = FakeXMLHttpRequest;
    FakeXMLHttpRequest.scenarios.push((xhr) => xhr.respond(201, {
      filename: 'note.md',
      path: 'users/u/projects/p/raw/note.md',
      bytes: 5,
      sha256: 'abc',
      status: 'created',
    }));

    const created = await uploadRawFile(file);
    assert.equal(created.status, 'created');
    assert.equal(created.filename, 'note.md');
    assert.equal(created.sha256, 'abc');

    FakeXMLHttpRequest.scenarios.push((xhr) => xhr.respond(200, {
      filename: 'note.md',
      path: 'users/u/projects/p/raw/note.md',
      bytes: 5,
      sha256: 'abc',
      status: 'already_exists',
    }));

    const existing = await uploadRawFile(file);
    assert.equal(existing.status, 'already_exists');
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile uses credentialed XHR, preserves headers/FormData, and emits bounded progress', async () => {
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

  const originalXHR = globalThis.XMLHttpRequest;
  const file = new File(['# hi\n'], 'note.md', { type: 'text/markdown' });
  const progress = [];

  class FakeXMLHttpRequest {
    static request;

    constructor() {
      this.headers = {};
      this.upload = {};
      FakeXMLHttpRequest.request = this;
    }

    open(method, url) {
      this.method = method;
      this.url = url;
    }

    setRequestHeader(name, value) {
      this.headers[name] = value;
    }

    send(body) {
      this.body = body;
      this.upload.onprogress({ lengthComputable: true, loaded: 1, total: 4 });
      this.upload.onprogress({ lengthComputable: true, loaded: 2, total: 4 });
      this.upload.onprogress({ lengthComputable: true, loaded: 2, total: 4 });
      this.status = 201;
      this.responseText = JSON.stringify({ filename: 'note.md', bytes: 5, status: 'created' });
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = FakeXMLHttpRequest;
    const result = await uploadRawFile(file, (value) => progress.push(value));
    const request = FakeXMLHttpRequest.request;
    assert.equal(request.method, 'POST');
    assert.match(request.url, /\/api\/v1\/raw\/upload$/);
    assert.equal(request.withCredentials, true);
    assert.equal(request.headers.Authorization, 'Bearer jwt-token');
    assert.equal(request.headers['X-Project-ID'], 'project-1');
    assert.equal(request.body instanceof FormData, true);
    assert.equal(request.body.get('file'), file);
    assert.deepEqual(progress, [25, 50, 100]);
    assert.equal(result.status, 'created');
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile normalizes network and error responses as ApiError', async () => {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  const scenarios = [
    (xhr) => xhr.onerror(),
    (xhr) => {
      xhr.status = 409;
      xhr.responseText = JSON.stringify({ error: 'filename already exists with different content' });
      xhr.onload();
    },
  ];

  class FakeXMLHttpRequest {
    constructor() {
      this.headers = {};
      this.upload = {};
    }
    open() {}
    setRequestHeader() {}
    send() { scenarios.shift()(this); }
  }

  try {
    globalThis.XMLHttpRequest = FakeXMLHttpRequest;
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'network.md')),
      (err) => err instanceof ApiError && err.status === 0 && err.message === 'Upload failed',
    );
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'conflict.md')),
      (err) => err instanceof ApiError
        && err.status === 409
        && err.message === 'filename already exists with different content',
    );
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile refreshes once after 401 and never retries indefinitely', async () => {
  let token = 'stale-token';
  let refreshCount = 0;
  let unauthorizedCount = 0;
  configureApiAuth({
    getAccessToken: () => token,
    refreshAccessToken: async () => {
      refreshCount += 1;
      token = 'fresh-token';
      return { accessToken: token, epoch: 0 };
    },
    onUnauthorized: () => { unauthorizedCount += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  const requests = [];
  class FakeXMLHttpRequest {
    constructor() {
      this.headers = {};
      this.upload = {};
      requests.push(this);
    }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(name, value) { this.headers[name] = value; }
    send() {
      if (requests.length === 1) {
        this.status = 401;
        this.responseText = JSON.stringify({ error: 'expired' });
      } else {
        this.status = 201;
        this.responseText = JSON.stringify({ filename: 'retry.md', status: 'created' });
      }
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = FakeXMLHttpRequest;
    const result = await uploadRawFile(new File(['x'], 'retry.md'));
    assert.equal(result.status, 'created');
    assert.equal(refreshCount, 1);
    assert.equal(requests.length, 2);
    assert.equal(requests[0].headers.Authorization, 'Bearer stale-token');
    assert.equal(requests[1].headers.Authorization, 'Bearer fresh-token');
    assert.equal(unauthorizedCount, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }

  let finalUnauthorizedCount = 0;
  let finalUnauthorizedToken = null;
  let finalRefreshCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => {
      finalRefreshCount += 1;
      return { accessToken: 'fresh-token', epoch: 0 };
    },
    onUnauthorized: (failedToken) => {
      finalUnauthorizedCount += 1;
      finalUnauthorizedToken = failedToken;
    },
  });
  const retryRequests = [];
  class AlwaysUnauthorizedXHR extends FakeXMLHttpRequest {
    constructor() {
      super();
      retryRequests.push(this);
    }
    send() {
      this.status = 401;
      this.responseText = JSON.stringify({ error: 'still expired' });
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = AlwaysUnauthorizedXHR;
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'still-retry.md')),
      (err) => err instanceof ApiError && err.status === 401 && err.message === 'still expired',
    );
    assert.equal(retryRequests.length, 2);
    assert.equal(finalRefreshCount, 1);
    assert.equal(finalUnauthorizedCount, 1);
    assert.equal(finalUnauthorizedToken, 'fresh-token');
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }

  let forbiddenUnauthorizedCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => ({ accessToken: 'fresh-token', epoch: 0 }),
    onUnauthorized: () => { forbiddenUnauthorizedCount += 1; },
  });
  const forbiddenRequests = [];
  class ForbiddenAfterRefreshXHR extends FakeXMLHttpRequest {
    constructor() {
      super();
      forbiddenRequests.push(this);
    }
    send() {
      this.status = forbiddenRequests.length === 1 ? 401 : 403;
      this.responseText = JSON.stringify({ error: this.status === 401 ? 'expired' : 'forbidden' });
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = ForbiddenAfterRefreshXHR;
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'forbidden-retry.md')),
      (err) => err instanceof ApiError && err.status === 403 && err.message === 'forbidden',
    );
    assert.equal(forbiddenRequests.length, 2);
    assert.equal(forbiddenUnauthorizedCount, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
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

  const originalXHR = globalThis.XMLHttpRequest;
  class FakeXMLHttpRequest {
    constructor() {
      this.upload = {};
    }
    open() {}
    setRequestHeader() {}
    send() {
      this.status = 409;
      this.responseText = JSON.stringify({ error: 'filename already exists with different content' });
      this.onload();
    }
  }

  try {
    globalThis.XMLHttpRequest = FakeXMLHttpRequest;
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
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile rejects oversized files before transport', async () => {
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
          user_name: 'Demo Owner',
          user_email: 'owner@example.com',
          project_id: 'proj-1',
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
        projectId: 'proj-1',
        userId: 'user-1',
        userName: 'Demo Owner',
        userEmail: 'owner@example.com',
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
          {
            id: 'user-1',
            name: 'Admin Person',
            email: 'admin@example.com',
            role: 'admin',
            project_count: 4,
          },
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
      {
        id: 'user-1',
        name: 'Admin Person',
        email: 'admin@example.com',
        role: 'admin',
        projectCount: 4,
      },
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

test('apiFetch snapshots epoch before token access can schedule a same-token session transition', async () => {
  let epoch = 1;
  let refreshCalls = 0;
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => {
      queueMicrotask(() => { epoch = 2; });
      return 'same-token';
    },
    refreshAccessToken: async () => {
      refreshCalls += 1;
      return null;
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    return new Response('expired', { status: 401 });
  };
  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(epoch, 2);
    assert.equal(refreshCalls, 0);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch never switches auth providers while an initial response is pending', async () => {
  let resolveInitial;
  const initial = new Promise((resolve) => { resolveInitial = resolve; });
  let aRefresh = 0;
  let bRefresh = 0;
  let bUnauthorized = 0;
  configureApiAuth({
    getSessionEpoch: () => 1,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => { aRefresh += 1; return null; },
    onUnauthorized: () => undefined,
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => initial;
  try {
    const request = apiFetch('/api/v1/status');
    configureApiAuth({
      getSessionEpoch: () => 1,
      getAccessToken: () => 'same-token',
      refreshAccessToken: async () => { bRefresh += 1; return { accessToken: 'same-token', epoch: 0 }; },
      onUnauthorized: () => { bUnauthorized += 1; },
    });
    resolveInitial(new Response('expired', { status: 401 }));
    const response = await request;
    assert.equal(response.status, 401);
    assert.equal(aRefresh, 0);
    assert.equal(bRefresh, 0);
    assert.equal(bUnauthorized, 0);
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

test('apiFetch rejects legacy refresh during pre-request when no session epoch getter exists', async () => {
  let unauthorizedCalls = 0;
  let fetchCalls = 0;
  configureApiAuth({
    getAccessToken: () => null,
    refreshAccessToken: async () => 'legacy-token',
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    fetchCalls += 1;
    return new Response('unexpected', { status: 200 });
  };
  try {
    await assert.rejects(
      () => apiFetch('/api/v1/status'),
      /Authentication required|log in/i,
    );
    assert.equal(fetchCalls, 0);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
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
  let unauthorizedToken = null;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => null,
    onUnauthorized: (failedToken) => {
      unauthorizedCalls += 1;
      unauthorizedToken = failedToken;
    },
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
    assert.equal(unauthorizedToken, 'stale-token');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch retries once after 401 when refresh succeeds', async () => {
  let unauthorizedCalls = 0;
  let fetchCount = 0;
  let refreshCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => {
      refreshCount += 1;
      return { accessToken: 'fresh-token', epoch: 0 };
    },
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
    assert.equal(refreshCount, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch performs zero unauthorized callback when epoch changes during refresh', async () => {
  let epoch = 1;
  let resolveRefresh;
  const refresh = new Promise((resolve) => { resolveRefresh = resolve; });
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => refresh,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    return new Response('expired', { status: 401 });
  };
  try {
    const request = apiFetch('/api/v1/status');
    await new Promise((resolve) => setTimeout(resolve, 0));
    epoch = 2;
    resolveRefresh(null);
    assert.equal((await request).status, 401);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch performs zero unauthorized callback when epoch changes during retry', async () => {
  let epoch = 1;
  let resolveRetry;
  const retry = new Promise((resolve) => { resolveRetry = resolve; });
  let fetchCount = 0;
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'old-token',
    refreshAccessToken: async () => { epoch = 2; return { accessToken: 'fresh-token', epoch: 2 }; },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    fetchCount += 1;
    return fetchCount === 1 ? new Response('expired', { status: 401 }) : retry;
  };
  try {
    const request = apiFetch('/api/v1/status');
    await new Promise((resolve) => setTimeout(resolve, 0));
    epoch = 3;
    resolveRetry(new Response('expired again', { status: 401 }));
    assert.equal((await request).status, 401);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile performs zero unauthorized callback when epoch changes during refresh', async () => {
  let epoch = 1;
  let resolveRefresh;
  const refresh = new Promise((resolve) => { resolveRefresh = resolve; });
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => refresh,
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  class EpochXHR {
    constructor() { this.upload = {}; }
    open() {}
    setRequestHeader() {}
    send() { this.status = 401; this.responseText = '{}'; this.onload(); }
  }
  globalThis.XMLHttpRequest = EpochXHR;
  try {
    const upload = uploadRawFile(new File(['x'], 'epoch.md')).catch((error) => error);
    await new Promise((resolve) => setTimeout(resolve, 0));
    epoch = 2;
    resolveRefresh(null);
    const error = await upload;
    assert.equal(error.status, 401);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile performs zero unauthorized callback when epoch changes during retry', async () => {
  let epoch = 1;
  let unauthorizedCalls = 0;
  let requestCount = 0;
  let respondToRetry;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'old-token',
    refreshAccessToken: async () => { epoch = 2; return { accessToken: 'fresh-token', epoch: 2 }; },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  class EpochXHR {
    constructor() { this.upload = {}; }
    open() {}
    setRequestHeader() {}
    send() {
      requestCount += 1;
      if (requestCount === 1) {
        this.status = 401; this.responseText = '{}'; this.onload();
      } else respondToRetry = () => this.respond();
    }
    respond() { this.status = 401; this.responseText = '{}'; this.onload(); }
  }
  globalThis.XMLHttpRequest = EpochXHR;
  try {
    const upload = uploadRawFile(new File(['x'], 'epoch.md')).catch((error) => error);
    await new Promise((resolve) => setTimeout(resolve, 0));
    epoch = 3;
    respondToRetry();
    const error = await upload;
    assert.equal(error.status, 401);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('apiFetch calls unauthorized once when a refreshed retry is still 401', async () => {
  let unauthorizedCalls = 0;
  let unauthorizedToken = null;
  let fetchCount = 0;
  let refreshCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => {
      refreshCount += 1;
      return { accessToken: 'fresh-token', epoch: 0 };
    },
    onUnauthorized: (failedToken) => {
      unauthorizedCalls += 1;
      unauthorizedToken = failedToken;
    },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (_url, init) => {
    fetchCount += 1;
    if (fetchCount === 1) return new Response('expired', { status: 401 });
    const auth = init?.headers?.Authorization ?? init?.headers?.authorization;
    assert.match(String(auth), /Bearer fresh-token/);
    return new Response('still expired', { status: 401 });
  };

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(fetchCount, 2);
    assert.equal(refreshCount, 1);
    assert.equal(unauthorizedCalls, 1);
    assert.equal(unauthorizedToken, 'fresh-token');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch rejects a refresh result owned by an older same-token epoch', async () => {
  let epoch = 1;
  let refreshCalls = 0;
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => {
      refreshCalls += 1;
      epoch = 3;
      return { accessToken: 'same-token', epoch: 2 };
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  let fetchCalls = 0;
  globalThis.fetch = async () => {
    fetchCalls += 1;
    return new Response('expired', { status: 401 });
  };
  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(refreshCalls, 1);
    assert.equal(fetchCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile preserves the business 401 for an older same-token refresh result', async () => {
  let epoch = 1;
  let refreshCalls = 0;
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => {
      refreshCalls += 1;
      epoch = 3;
      return { accessToken: 'same-token', epoch: 2 };
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  class SameTokenXHR {
    status = 401;
    responseText = JSON.stringify({ error: 'expired' });
    upload = {};
    onload = () => undefined;
    open() {}
    setRequestHeader() {}
    send() { this.onload(); }
  }
  globalThis.XMLHttpRequest = SameTokenXHR;
  try {
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'same-token.md')),
      (error) => error instanceof ApiError && error.status === 401 && error.message === 'expired',
    );
    assert.equal(refreshCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('apiFetch does not bind a pre-request refresh to a newer same-token epoch', async () => {
  let epoch = 1;
  let refreshCalls = 0;
  let unauthorizedCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => null,
    refreshAccessToken: async () => {
      refreshCalls += 1;
      epoch = 3;
      return { accessToken: 'same-token', epoch: 2 };
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  let fetchCalls = 0;
  globalThis.fetch = async () => {
    fetchCalls += 1;
    return new Response('expired', { status: 401 });
  };
  try {
    await assert.rejects(() => apiFetch('/api/v1/status'), /Authentication required/);
    assert.equal(refreshCalls, 1);
    assert.equal(fetchCalls, 0);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch does not call unauthorized when a refreshed retry returns 403', async () => {
  let unauthorizedCalls = 0;
  let fetchCount = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => ({ accessToken: 'fresh-token', epoch: 0 }),
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = {
    localStorage: { getItem: () => 'project-1' },
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    fetchCount += 1;
    return new Response(fetchCount === 1 ? 'expired' : 'forbidden', {
      status: fetchCount === 1 ? 401 : 403,
    });
  };

  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 403);
    assert.equal(fetchCount, 2);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch rejects a legacy string refresh after same-token epochs advance twice', async () => {
  let epoch = 1;
  let unauthorizedCalls = 0;
  let fetchCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => {
      epoch = 2;
      queueMicrotask(() => { epoch = 3; });
      return 'same-token';
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    fetchCalls += 1;
    return new Response('expired', { status: 401 });
  };
  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(fetchCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('apiFetch preserves business 401 when malformed refresh result appears after 401 and no epoch getter', async () => {
  let unauthorizedCalls = 0;
  let fetchCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => 'legacy-token',
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => {
    fetchCalls += 1;
    return new Response('expired', { status: 401 });
  };
  try {
    const response = await apiFetch('/api/v1/status');
    assert.equal(response.status, 401);
    assert.equal(fetchCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('uploadRawFile preserves business 401 when a legacy string refresh crosses same-token epochs', async () => {
  let epoch = 1;
  let unauthorizedCalls = 0;
  let requestCalls = 0;
  configureApiAuth({
    getSessionEpoch: () => epoch,
    getAccessToken: () => 'same-token',
    refreshAccessToken: async () => {
      epoch = 2;
      queueMicrotask(() => { epoch = 3; });
      return 'same-token';
    },
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  class SameTokenXHR {
    status = 401;
    responseText = JSON.stringify({ error: 'expired' });
    upload = {};
    onload = () => undefined;
    open() {}
    setRequestHeader() {}
    send() { requestCalls += 1; this.onload(); }
  }
  globalThis.XMLHttpRequest = SameTokenXHR;
  try {
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'same-token-aba.md')),
      (error) => error instanceof ApiError && error.status === 401 && error.message === 'expired',
    );
    assert.equal(requestCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});

test('uploadRawFile preserves business 401 for malformed refresh result after 401 and no epoch getter', async () => {
  let unauthorizedCalls = 0;
  let requestCalls = 0;
  configureApiAuth({
    getAccessToken: () => 'stale-token',
    refreshAccessToken: async () => 'legacy-token',
    onUnauthorized: () => { unauthorizedCalls += 1; },
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
  const originalXHR = globalThis.XMLHttpRequest;
  class LegacyXHR {
    status = 401;
    responseText = JSON.stringify({ error: 'expired' });
    upload = {};
    onload = () => undefined;
    open() {}
    setRequestHeader() {}
    send() { requestCalls += 1; this.onload(); }
  }
  globalThis.XMLHttpRequest = LegacyXHR;
  try {
    await assert.rejects(
      () => uploadRawFile(new File(['x'], 'legacy.md')),
      (error) => error instanceof ApiError && error.status === 401 && error.message === 'expired',
    );
    assert.equal(requestCalls, 1);
    assert.equal(unauthorizedCalls, 0);
  } finally {
    globalThis.XMLHttpRequest = originalXHR;
  }
});
