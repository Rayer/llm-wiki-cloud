import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { GET } from '../src/app/frontend-config.json/route.ts';

const configA = {
  schema_version: 1,
  api_url: 'http://127.0.0.1:18080',
  auth_url: 'http://127.0.0.1:18081',
};
const configB = {
  schema_version: 1,
  api_url: 'http://127.0.0.1:28080',
  auth_url: 'http://127.0.0.1:28081',
};

test('local route reads each worktree file request and disables caching', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'lwc-370-route-'));
  const frontend = join(root, 'apps', 'frontend');
  const configPath = join(root, '.build', 'cac', 'local', 'frontend-config.json');
  mkdirSync(frontend, { recursive: true });
  mkdirSync(join(root, '.build', 'cac', 'local'), { recursive: true });
  const previousCwd = process.cwd();
  const previousNodeEnv = process.env.NODE_ENV;
  process.chdir(frontend);
  process.env.NODE_ENV = 'development';
  t.after(() => {
    process.chdir(previousCwd);
    if (previousNodeEnv === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = previousNodeEnv;
    rmSync(root, { recursive: true, force: true });
  });

  writeFileSync(configPath, JSON.stringify({ ...configA, private_value: 'must not be served' }));
  const first = await GET();
  assert.equal(first.status, 503, 'unexpected private fields are rejected');

  writeFileSync(configPath, JSON.stringify(configA));
  const responseA = await GET();
  assert.equal(responseA.status, 200);
  assert.equal(responseA.headers.get('cache-control'), 'no-store');
  assert.deepEqual(await responseA.json(), configA);

  writeFileSync(configPath, JSON.stringify(configB));
  const responseB = await GET();
  assert.equal(responseB.status, 200);
  assert.deepEqual(await responseB.json(), configB);

  writeFileSync(configPath, JSON.stringify({ schema_version: 1, auth_url: configA.auth_url }));
  const missingEndpoint = await GET();
  assert.equal(missingEndpoint.status, 200);
  assert.deepEqual(await missingEndpoint.json(), { schema_version: 1, auth_url: configA.auth_url });

  writeFileSync(configPath, '{malformed');
  const malformed = await GET();
  assert.equal(malformed.status, 200);
  assert.deepEqual(await malformed.json(), {});
});

test('local route is unavailable outside Next development mode', async (t) => {
  const previousNodeEnv = process.env.NODE_ENV;
  process.env.NODE_ENV = 'production';
  t.after(() => {
    if (previousNodeEnv === undefined) delete process.env.NODE_ENV;
    else process.env.NODE_ENV = previousNodeEnv;
  });

  const response = await GET();
  assert.equal(response.status, 404);
  assert.equal(response.headers.get('cache-control'), 'no-store');
});
