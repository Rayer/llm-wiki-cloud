import assert from 'node:assert/strict';
import test from 'node:test';

import {
  normalizeProject,
  normalizeProjects,
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
      { id: 'bravo', name: 'Bravo' },
      { id: 'alpha', name: 'Alpha' },
    ],
  );
});

test('normalizeProject accepts a project returned from init-project', () => {
  assert.deepEqual(
    normalizeProject({ project: { id: 'new-project', name: 'New Project' } }),
    { id: 'new-project', name: 'New Project' },
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
