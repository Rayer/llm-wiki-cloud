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
