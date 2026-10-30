import assert from 'node:assert/strict';
import test from 'node:test';

import {
  isValidProjectDescription,
  normalizeProjectDescription,
  normalizeProject,
  normalizeProjects,
  projectDescriptionCodePointCount,
  selectDefaultProject,
} from '../src/lib/project-core.ts';
import { configureApiAuth } from '../src/lib/api.ts';
import { getProjectContentContext } from '../src/lib/projects.ts';

test('normalizeProjects accepts array and wrapped API responses', () => {
  assert.deepEqual(
    normalizeProjects({
      projects: [
        { id: 'bravo', name: 'Bravo' },
        { project_id: 'alpha', project_name: 'Alpha' },
      ],
    }),
    [
      { id: 'bravo', name: 'Bravo', description: '', descriptionRevision: 0 },
      { id: 'alpha', name: 'Alpha', description: '', descriptionRevision: 0 },
    ],
  );
});

test('normalizeProject accepts a project returned from init-project', () => {
  assert.deepEqual(
    normalizeProject({ project: { id: 'new-project', name: 'New Project' } }),
    { id: 'new-project', name: 'New Project', description: '', descriptionRevision: 0 },
  );
});

test('normalizeProject does not use the project name as the project id', () => {
  assert.equal(normalizeProject({ name: 'Human Project Name' }), null);
  assert.deepEqual(
    normalizeProject({ project_id: 'project-123', name: 'Human Project Name' }),
    { id: 'project-123', name: 'Human Project Name', description: '', descriptionRevision: 0 },
  );
});

test('selectDefaultProject restores the last used project when available', () => {
  const projects = [
    { id: 'alpha', name: 'Alpha' },
    { id: 'bravo', name: 'Bravo' },
  ];

  assert.deepEqual(selectDefaultProject(projects, 'bravo'), projects[1]);
});

test('selectDefaultProject falls back to the first project alphabetically', () => {
  const projects = [
    { id: 'zulu', name: 'Zulu' },
    { id: 'alpha', name: 'alpha' },
    { id: 'bravo', name: 'Bravo' },
  ];

  assert.deepEqual(selectDefaultProject(projects, 'missing'), projects[1]);
  assert.equal(selectDefaultProject([], null), null);
});

test('project normalization retains exact saved description and revision', () => {
  assert.deepEqual(
    normalizeProject({ id: 'project-1', name: 'Notes', description: '  line one\nline two  ', description_revision: 4 }),
    { id: 'project-1', name: 'Notes', description: '  line one\nline two  ', descriptionRevision: 4 },
  );
});

test('description length uses normalized Unicode code points and rejects unsupported text', () => {
  assert.equal(normalizeProjectDescription('a\r\nb\rc'), 'a\nb\nc');
  assert.equal(projectDescriptionCodePointCount('😀'.repeat(1000)), 1000);
  assert.equal(projectDescriptionCodePointCount('x\r\ny'), 3);
  assert.equal(isValidProjectDescription('😀'.repeat(1000)), true);
  assert.equal(isValidProjectDescription('😀'.repeat(1001)), false);
  assert.equal(isValidProjectDescription('bad\0text'), false);
  assert.equal(isValidProjectDescription('\ud800'), false);
});

test('Project content context accepts a proven bootstrap summary without a fabricated profile id', async () => {
  const originalFetch = globalThis.fetch;
  const payload = {
    project_id: 'project-1', status: 'ready', current_generation: 'generation-1',
    current_concepts_sha256: 'a'.repeat(64), stale: false,
    summary: {
      text: 'Published topics', source_generation: 'generation-1', concepts_sha256: 'a'.repeat(64),
      generated_at: '2026-10-30T00:00:00Z', prompt_revision: 'project-summary-v1', profile_source: 'bootstrap',
      role: 'fast', requested_model: 'deepseek-flash', returned_model: 'deepseek-v2',
      input_digest: 'b'.repeat(64), selection_version: 'selection-v1',
    },
    error_class: null,
  };
  configureApiAuth({ getAccessToken: () => 'test-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  globalThis.fetch = async () => new Response(JSON.stringify(payload), { status: 200 });
  try {
    const result = await getProjectContentContext('project-1');
    assert.equal(result.summary?.profile_source, 'bootstrap');
    assert.equal(result.summary?.profile_id, undefined);
    assert.equal(result.summary?.profile_revision, undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('Project content context rejects an incomplete or fabricated profile identity', async () => {
  const originalFetch = globalThis.fetch;
  const payload = {
    project_id: 'project-1', status: 'ready', current_generation: 'generation-1',
    current_concepts_sha256: 'a'.repeat(64), stale: false,
    summary: {
      text: 'Published topics', source_generation: 'generation-1', concepts_sha256: 'a'.repeat(64),
      generated_at: '2026-10-30T00:00:00Z', prompt_revision: 'project-summary-v1', profile_source: 'bootstrap',
      profile_id: 'platform', role: 'fast', requested_model: 'deepseek-flash', returned_model: 'deepseek-v2',
      input_digest: 'b'.repeat(64), selection_version: 'selection-v1',
    },
  };
  configureApiAuth({ getAccessToken: () => 'test-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  globalThis.fetch = async () => new Response(JSON.stringify(payload), { status: 200 });
  try {
    await assert.rejects(getProjectContentContext('project-1'), /response is invalid/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
