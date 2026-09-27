import assert from 'node:assert/strict';
import test from 'node:test';

import {
  ApiError,
  configureApiAuth,
  confirmProfileBootstrapGuidance,
  confirmProfileCandidate,
  getProfile,
  getProfileBootstrapGuidance,
  getProfileJob,
  getRecompileAllCapability,
  retryProfileCandidate,
  retryProfileDerivation,
  recompileAll,
  saveProfile,
} from '../src/lib/api.ts';

const profile = {
  project_id: 'project-a',
  revision: 4,
  requirements: [{ id: 'req-1', text: '  preserve verbatim  ' }],
  derivation_status: 'pending',
  scheduled_for: '2026-09-25T03:00:00Z',
  derivation_error_code: null,
  candidate: null,
  confirmed_candidate_id: null,
  active: null,
  job: null,
};

function installAuth() {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
}

test('Profile API requests bind the URL and X-Project-ID to the explicit project', async () => {
  installAuth();
  globalThis.window = { localStorage: { getItem: () => 'stale-project' } };
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init });
    return Response.json(profile);
  };

  try {
    await getProfile('project-a');
    await getProfileJob('project-a', 'job-17');

    assert.match(requests[0].url, /\/projects\/project-a\/profile$/);
    assert.equal(requests[0].init.method, undefined);
    assert.equal(requests[0].init.headers.Authorization, 'Bearer jwt-token');
    assert.equal(requests[0].init.headers['X-Project-ID'], 'project-a');
    assert.match(requests[1].url, /\/projects\/project-a\/profile\/jobs\/job-17$/);
    assert.equal(requests[1].init.headers['X-Project-ID'], 'project-a');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('Profile writes send expected_revision and keep requirement text unchanged', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init });
    return Response.json(profile);
  };

  try {
    const requirements = [{ id: 'req-1', text: '  preserve verbatim  ' }];
    await saveProfile('project-a', 4, requirements);
    await confirmProfileCandidate('project-a', 'candidate-1', 4);
    await retryProfileCandidate('project-a', 'candidate-1', 4);
    await retryProfileDerivation('project-a', 4);

    assert.equal(requests[0].init.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].init.body), {
      expected_revision: 4,
      requirements,
    });
    assert.equal(requests[0].init.headers['X-Project-ID'], 'project-a');
    assert.equal(requests[1].init.method, 'POST');
    assert.match(requests[1].url, /\/candidates\/candidate-1\/confirm$/);
    assert.deepEqual(JSON.parse(requests[1].init.body), { expected_revision: 4 });
    assert.equal(requests[2].init.method, 'POST');
    assert.match(requests[2].url, /\/candidates\/candidate-1\/retry$/);
    assert.deepEqual(JSON.parse(requests[2].init.body), { expected_revision: 4 });
    assert.match(requests[3].url, /\/derivation\/retry$/);
    assert.deepEqual(JSON.parse(requests[3].init.body), { expected_revision: 4 });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('bootstrap guidance reads and confirmation use the artifact hash and exact revision/digest contract', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  const requests = [];
  const inputDigest = 'b'.repeat(64);
  const preview = {
    revision: 'a'.repeat(64),
    input_digest: inputDigest,
    profile_revision: 4,
    status: 'preview_ready',
    model_version: 'model-1',
    prompt_version: 'prompt-1',
    schema_version: 'profile.bootstrap-guidance.v1',
    confirmed_at: null,
    preview: {
      guidance_diff: 'Use short paragraphs.',
      requirements: [{ id: 'req-1', disposition: 'compile_guidance', explanation: 'Keep the guide concise.' }],
    },
  };
  const confirmed = { ...preview, status: 'confirmed', confirmed_at: '2026-09-25T03:00:00Z' };
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init });
    return Response.json({ bootstrap_guidance: requests.length === 1 ? preview : confirmed });
  };

  try {
    assert.deepEqual(await getProfileBootstrapGuidance('project-a'), { bootstrap_guidance: preview });
    assert.deepEqual(await confirmProfileBootstrapGuidance('project-a', preview.revision, 4, inputDigest), {
      bootstrap_guidance: confirmed,
    });

    assert.match(requests[0].url, /\/projects\/project-a\/profile\/bootstrap-guidance$/);
    assert.equal(requests[0].init.method, undefined);
    assert.equal(requests[0].init.headers['X-Project-ID'], 'project-a');
    assert.match(requests[1].url, new RegExp(`/projects/project-a/profile/bootstrap-guidance/${preview.revision}/confirm$`));
    assert.equal(requests[1].init.method, 'POST');
    assert.equal(requests[1].init.headers['X-Project-ID'], 'project-a');
    assert.deepEqual(JSON.parse(requests[1].init.body), {
      expected_revision: 4,
      input_digest: inputDigest,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('Profile API preserves HTTP conflict status and rejects unsafe project IDs before fetch', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  let fetchCount = 0;
  globalThis.fetch = async () => {
    fetchCount += 1;
    return Response.json({ error: 'profile revision conflict' }, { status: 409 });
  };

  try {
    await assert.rejects(
      () => saveProfile('project-a', 2, []),
      (error) => error instanceof ApiError && error.status === 409,
    );
    await assert.rejects(() => getProfile('../other'), /Invalid project ID/);
    assert.equal(fetchCount, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('Recompile all reads the server capability and posts only to its dedicated endpoint', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = async (url, init) => {
    requests.push({ url: String(url), init });
    return Response.json({ allowed: false, denial_code: 'byok_required' });
  };

  try {
    assert.deepEqual(await getRecompileAllCapability('project-a'), {
      allowed: false,
      denial_code: 'byok_required',
    });
    await recompileAll('project-a');

    assert.match(requests[0].url, /\/projects\/project-a\/recompile-all\/capability$/);
    assert.equal(requests[0].init.method, undefined);
    assert.equal(requests[0].init.headers['X-Project-ID'], 'project-a');
    assert.match(requests[1].url, /\/projects\/project-a\/recompile-all$/);
    assert.equal(requests[1].init.method, 'POST');
    assert.equal(requests[1].init.headers['X-Project-ID'], 'project-a');
    assert.equal(requests[1].init.body, undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
