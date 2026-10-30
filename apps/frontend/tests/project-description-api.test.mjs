import assert from 'node:assert/strict';
import test from 'node:test';

import { ApiError, configureApiAuth } from '../src/lib/api.ts';
import { createProject, getProjectDescription, saveProjectDescription } from '../src/lib/projects.ts';

function installAuth() {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: { getItem: () => 'stale-selection' },
  };
}

test('createProject persists the optional description and stable idempotency key', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit = {};
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init ?? {};
    return Response.json({
      project_id: 'project-a',
      name: 'Wiki',
      description: 'Purpose',
      description_revision: 1,
      status: 'ready',
      status_url: '/api/v1/projects/project-a/status',
    }, { status: 202 });
  };

  try {
    const project = await createProject(' Wiki ', 'Purpose', 'same-request-key');
    assert.equal(requestedUrl, 'https://api.runtime.test/api/v1/init-project');
    assert.equal(requestedInit.method, 'POST');
    assert.equal(requestedInit.headers?.Authorization, 'Bearer jwt-token');
    assert.equal(requestedInit.headers?.['Idempotency-Key'], 'same-request-key');
    assert.equal(requestedInit.headers?.['X-Project-ID'], undefined);
    assert.equal(requestedInit.body, '{"name":"Wiki","project":"Wiki","project_name":"Wiki","description":"Purpose"}');
    assert.deepEqual(project, { id: 'project-a', name: 'Wiki', description: 'Purpose', descriptionRevision: 1 });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('description GET and PUT target the path Project and send revision CAS', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init: init ?? {} });
    return Response.json({ project_id: 'project-a', description: 'line one\nline two', revision: 4 });
  };

  try {
    const read = await getProjectDescription('project-a');
    assert.deepEqual(read, { projectId: 'project-a', description: 'line one\nline two', revision: 4 });
    const saved = await saveProjectDescription('project-a', 'line one\r\nline two', 3);
    assert.deepEqual(saved, read);
    assert.equal(requests[0].url, 'https://api.runtime.test/api/v1/projects/project-a/description');
    assert.equal(requests[0].init.method, undefined);
    assert.equal(requests[0].init.headers?.['X-Project-ID'], 'project-a');
    assert.equal(requests[1].url, requests[0].url);
    assert.equal(requests[1].init.method, 'PUT');
    assert.equal(requests[1].init.headers?.['X-Project-ID'], 'project-a');
    assert.equal(requests[1].init.body, '{"description":"line one\\nline two","revision":3}');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('description CAS conflict retains an ApiError status for the editor', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ error: 'description revision conflict' }, { status: 409 });
  try {
    await assert.rejects(
      () => saveProjectDescription('project-a', 'Draft', 0),
      (error) => error instanceof ApiError && error.status === 409,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});
