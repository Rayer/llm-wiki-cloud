import assert from 'node:assert/strict';
import test from 'node:test';

import { buildProjectHeaders, toV1Path } from '../src/lib/api.ts';

test('buildProjectHeaders scopes authenticated requests to the selected project', () => {
  assert.deepEqual(buildProjectHeaders('jwt-token', 'project-1', true), {
    Authorization: 'Bearer jwt-token',
    'Content-Type': 'application/json',
    'X-Project-ID': 'project-1',
  });
});

test('toV1Path upgrades existing API paths without changing callers', () => {
  assert.equal(toV1Path('/api/query'), '/api/v1/query');
  assert.equal(toV1Path('/api/v1/projects'), '/api/v1/projects');
});
