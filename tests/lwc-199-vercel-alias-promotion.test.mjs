import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('..', import.meta.url).pathname;
const commitSha = '0123456789abcdef0123456789abcdef01234567';
const deploymentId = 'dpl_test123';
const projectId = 'prj_test123';
const aliases = ['wiki.rayer.idv.tw', 'llm-wiki-frontend.vercel.app'];

async function setupCase(scenario = 'success') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-199-'));
  const bin = join(root, 'bin');
  await mkdir(bin);
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'deployment.json'), JSON.stringify({
    id: deploymentId,
    projectId,
    readyState: 'READY',
    meta: {
      githubDeployment: '1',
      githubOrg: 'Rayer',
      githubRepo: 'llm-wiki-frontend',
      githubCommitRef: 'main',
      githubCommitSha: commitSha,
    },
  }));
  await writeFile(join(root, 'aliases.json'), JSON.stringify({
    [aliases[0]]: 'dpl_oldcustom',
    [aliases[1]]: 'dpl_oldvercel',
  }));
  await writeFile(join(root, 'vercel-calls'), '');
  await writeFile(join(root, 'curl-calls'), '');
  await execFileAsync('cp', [
    join(repoRoot, 'tests/fixtures/lwc-199-fake-curl.sh'),
    join(bin, 'curl'),
  ]);
  await execFileAsync('cp', [
    join(repoRoot, 'tests/fixtures/lwc-199-fake-vercel.sh'),
    join(bin, 'vercel'),
  ]);
  await execFileAsync('chmod', ['+x', join(bin, 'curl'), join(bin, 'vercel')]);
  const evidenceDir = join(root, 'evidence');
  await mkdir(evidenceDir);
  return { root, bin, evidenceDir };
}

async function runCase(scenario = 'success', overrides = {}) {
  const fixture = await setupCase(scenario);
  const env = {
    ...process.env,
    PATH: fixture.bin + ':' + process.env.PATH,
    FIXTURE_ROOT: fixture.root,
    COMMIT_SHA: commitSha,
    DEPLOYMENT_ID: deploymentId,
    TICKET_REF: 'LWC-199',
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-frontend',
    GITHUB_API_URL: 'https://github.test/api/v3',
    GITHUB_TOKEN: 'github-test-token',
    VERCEL_API_BASE_URL: 'https://vercel.test',
    VERCEL_TOKEN: 'vercel-test-token',
    VERCEL_PROJECT_ID: projectId,
    VERCEL_TEAM_ID: 'team_test123',
    VERCEL_SCOPE: 'rayer-team',
    EVIDENCE_DIR: fixture.evidenceDir,
    ...overrides,
  };
  let result;
  try {
    result = await execFileAsync('bash', ['.github/scripts/vercel-alias-promotion.sh'], {
      cwd: repoRoot,
      env,
      maxBuffer: 1024 * 1024,
    });
  } catch (error) {
    result = error;
  }
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-alias-promotion.json'), 'utf8'));
  const calls = (await readFile(join(fixture.root, 'vercel-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const curlCalls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  return { ...fixture, result, evidence, calls, curlCalls };
}

test('promotes exactly both canonical aliases to one deployment and writes normalized evidence', async () => {
  const run = await runCase();
  assert.equal(run.result.error, undefined, run.result.stderr);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.deepEqual(run.evidence.aliases.map(({ alias }) => alias), aliases);
  assert.deepEqual(run.evidence.aliases.map(({ deploymentId: id }) => id), ['dpl_oldcustom', 'dpl_oldvercel']);
  assert.deepEqual(run.evidence.postAliases.map(({ deploymentId: id }) => id), [deploymentId, deploymentId]);
  assert.deepEqual(run.calls.map((call) => call.split(' ').slice(0, 4)), [
    ['alias', 'set', deploymentId, aliases[0]],
    ['alias', 'set', deploymentId, aliases[1]],
  ]);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v13/deployments/')).length, 2);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases?')).length, 4);
  assert.ok(run.curlCalls.includes('https://' + aliases[0] + '/'));
  assert.ok(run.curlCalls.includes('https://' + aliases[1] + '/'));
  assert.doesNotMatch(JSON.stringify(run.evidence), /vercel-test-token|github-test-token/);
});

for (const scenario of [
  'ci-failure',
  'deployment-mismatch',
  'source-provider-mismatch',
  'source-ref-mismatch',
  'source-sha-mismatch',
  'not-ready',
  'missing-alias',
  'divergent-alias',
]) {
  test('fails closed before mutation for ' + scenario, async () => {
    const run = await runCase(scenario);
    assert.notEqual(run.result.code, 0);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(run.calls.length, 0);
  });
}

test('fails closed for an invalid SHA before any provider call', async () => {
  const run = await runCase('success', { COMMIT_SHA: 'not-a-sha' });
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(run.calls.length, 0);
  assert.equal(run.curlCalls.length, 0);
});

test('fails closed for an invalid immutable deployment ID before any provider call', async () => {
  const run = await runCase('success', { DEPLOYMENT_ID: 'https://evil.example/deployment' });
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(run.calls.length, 0);
  assert.equal(run.curlCalls.length, 0);
});

test('marks a partial alias mutation and requires read-back before retry', async () => {
  const run = await runCase('partial-mutation');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PARTIAL_MUTATION');
  assert.match(run.evidence.nextAction, /Read \/v4\/aliases before retry/);
  assert.equal(run.calls.length, 2);
  assert.ok(run.curlCalls.filter((url) => url.includes('/v4/aliases?')).length >= 4);
  assert.equal(run.curlCalls.some((url) => url.startsWith('https://wiki.rayer.idv.tw/')), false);
});

test('fails closed when authoritative post-state diverges', async () => {
  const run = await runCase('post-readback-mismatch');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.equal(run.calls.length, 2);
});

test('fails closed when either canonical health check is not HTTP 200', async () => {
  const run = await runCase('health-failure');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.equal(run.calls.length, 2);
  assert.ok(run.evidence.health.some(({ statusCode }) => statusCode === '503'));
});
