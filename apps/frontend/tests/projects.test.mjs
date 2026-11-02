import assert from 'node:assert/strict';
import test from 'node:test';

import {
  isValidProjectDescription,
  normalizeProjectDescription,
  normalizeProject,
  normalizeProjects,
  compareProjectsByLastUse,
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
      { id: 'alpha', name: 'Alpha', createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0 },
      { id: 'bravo', name: 'Bravo', createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0 },
    ],
  );
});

test('normalizeProject accepts a project returned from init-project', () => {
  assert.deepEqual(
    normalizeProject({ project: { id: 'new-project', name: 'New Project' } }),
    { id: 'new-project', name: 'New Project', createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0 },
  );
});

test('normalizeProject does not use the project name as the project id', () => {
  assert.equal(normalizeProject({ name: 'Human Project Name' }), null);
  assert.deepEqual(
    normalizeProject({ project_id: 'project-123', name: 'Human Project Name' }),
    { id: 'project-123', name: 'Human Project Name', createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0 },
  );
});

test('selectDefaultProject restores the last used project when available', () => {
  const projects = [
    { id: 'alpha', name: 'Alpha' },
    { id: 'bravo', name: 'Bravo' },
  ];

  assert.deepEqual(selectDefaultProject(projects, 'bravo'), projects[1]);
});

test('selectDefaultProject preserves the existing alphabetical fallback', () => {
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
    { id: 'project-1', name: 'Notes', createdAt: null, lastUsedAt: null, description: '  line one\nline two  ', descriptionRevision: 4 },
  );
});

test('project timestamps normalize only complete valid RFC3339 timestamps', () => {
  const project = normalizeProject({
    id: 'project-1', name: 'Notes', created_at: '2026-02-28T03:04:05+08:00',
    last_used_at: '2026-03-01T00:00:00Z',
  });
  assert.equal(project?.createdAt, '2026-02-28T03:04:05+08:00');
  assert.equal(project?.lastUsedAt, '2026-03-01T00:00:00Z');

  for (const invalid of [
    null, '', ' ', 123, '2026-02-30T00:00:00Z', '2026-13-01T00:00:00Z',
    '2026-02-01T24:00:00Z', '2026-02-01T00:60:00Z', '2026-02-01T00:00:60Z',
    '2026-02-01T00:00:00', '2026-02-01T00:00:00+24:00',
    '2026-02-01T00:00:00+00:60',
  ]) {
    const normalized = normalizeProject({ id: 'project-1', name: 'Notes', created_at: invalid, last_used_at: invalid });
    assert.equal(normalized?.createdAt, null, `created_at ${String(invalid)}`);
    assert.equal(normalized?.lastUsedAt, null, `last_used_at ${String(invalid)}`);
  }
});

test('project normalization keeps timestamps in direct, nested, fallback, and legacy-string envelopes', () => {
  const timestamps = { created_at: '2026-01-01T00:00:00Z', last_used_at: '2026-01-02T00:00:00Z' };
  assert.deepEqual(normalizeProject({ id: 'direct', name: 'Direct', ...timestamps }), {
    id: 'direct', name: 'Direct', createdAt: timestamps.created_at, lastUsedAt: timestamps.last_used_at,
    description: '', descriptionRevision: 0,
  });
  assert.deepEqual(normalizeProject({ project: { id: 'nested', name: 'Nested', ...timestamps } }), {
    id: 'nested', name: 'Nested', createdAt: timestamps.created_at, lastUsedAt: timestamps.last_used_at,
    description: '', descriptionRevision: 0,
  });
  assert.deepEqual(normalizeProject({ project: 'fallback', ...timestamps }), {
    id: 'fallback', name: 'fallback', createdAt: timestamps.created_at, lastUsedAt: timestamps.last_used_at,
    description: '', descriptionRevision: 0,
  });
  assert.deepEqual(normalizeProject('legacy'), {
    id: 'legacy', name: 'legacy', createdAt: null, lastUsedAt: null, description: '', descriptionRevision: 0,
  });
});

test('last-use comparator groups recent use before created fallback and uses id ordinal ties', () => {
  const projects = normalizeProjects([
    { id: 'last-b', name: 'B', last_used_at: '2026-01-02T00:00:00Z', created_at: '2024-01-01T00:00:00Z' },
    { id: 'created-new', name: 'New', created_at: '2026-01-01T00:00:00Z' },
    { id: 'last-a', name: 'A', last_used_at: '2026-01-02T00:00:00+00:00', created_at: '2020-01-01T00:00:00Z' },
    { id: 'created-old', name: 'Old', created_at: '2020-01-01T00:00:00Z' },
    { id: 'no-date', name: 'No date', created_at: '2026-02-30T00:00:00Z' },
  ]);
  assert.deepEqual(projects.map(({ id }) => id), ['last-a', 'last-b', 'created-new', 'created-old', 'no-date']);
  assert.equal(compareProjectsByLastUse(projects[0], projects[1]) < 0, true);
});

test('last-used timestamp does not fall back to created_at', () => {
  const normalized = normalizeProject({
    id: 'project-1', name: 'Notes', created_at: '2026-01-01T00:00:00Z', last_used_at: '',
  });
  assert.equal(normalized?.createdAt, '2026-01-01T00:00:00Z');
  assert.equal(normalized?.lastUsedAt, null);
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
      generated_at: '2026-10-30T00:00:00Z', prompt_revision: 'project-summary-v2-overview', profile_source: 'bootstrap',
      role: 'fast', requested_model: 'deepseek-flash', returned_model: 'deepseek-v2',
      input_digest: 'b'.repeat(64), selection_version: 'selection-v2-source-spread-15',
      overview_schema_version: 1, short_title: 'Small Wiki', topic_tags: ['Local research'],
      total_concepts: 1, selected_concepts: 1, selected_slugs: ['one-concept'],
      sampled: false, title_truncated_concepts: 0, body_truncated_concepts: 0,
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
    assert.equal(result.summary?.sampled, false);
    assert.equal(result.summary?.title_truncated_concepts, 0);
    assert.equal(result.summary?.body_truncated_concepts, 0);
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
