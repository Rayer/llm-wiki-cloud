import assert from 'node:assert/strict';
import { chmod, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('../../..', import.meta.url).pathname;
const scriptPath = join(repoRoot, 'deploy/cd.sh');
const fixtureDir = join(new URL('.', import.meta.url).pathname, 'fixtures');
const sourceSha = '0123456789abcdef0123456789abcdef01234567';

async function lines(path) {
  try {
    return (await readFile(path, 'utf8')).trim().split('\n').filter(Boolean);
  } catch (error) {
    if (error.code === 'ENOENT') return [];
    throw error;
  }
}

async function setup(environment = 'development', scenario = 'success') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-306-frontend-'));
  const artifactDir = join(root, 'artifacts');
  const production = environment === 'production';
  const aliases = production ? ['wiki.rayer.idv.tw', 'llm-wiki-frontend.vercel.app'] : ['wiki.dev.rayer.idv.tw'];
  const projectName = production ? 'llm-wiki-frontend' : 'llm-wiki-frontend-dev';
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'aliases.json'), JSON.stringify(Object.fromEntries(aliases.map((alias, index) => [alias, `dpl_old${index}`]))));
  await writeFile(join(root, 'project.json'), JSON.stringify({
    id: 'prj_frontendtest', name: projectName, accountId: 'team_frontendtest',
    rootDirectory: 'apps/frontend', link: { type: 'github', org: 'Rayer', repo: 'llm-wiki-cloud' },
  }));
  await writeFile(join(root, 'deployment.json'), JSON.stringify({
    id: 'dpl_frontendnew', url: 'frontend-hash.vercel.app', projectId: 'prj_frontendtest',
    accountId: 'team_frontendtest', readyState: 'READY', target: production ? 'production' : 'preview',
    meta: { githubCommitSha: sourceSha, githubCommitRef: production ? 'main' : 'develop', githubOrg: 'Rayer', githubRepo: 'llm-wiki-cloud' },
  }));
  await writeFile(join(root, 'plan.json'), JSON.stringify({
    normalized: {
      selected_components: ['frontend'],
      frontend: {
        project_name: projectName, team_slug: 'rayer-tung-s-projects', repository: 'Rayer/llm-wiki-cloud',
        root_directory: 'apps/frontend', stable_aliases: aliases,
        api_url: production ? 'https://bff.example' : 'https://bff.dev.example',
        auth_url: production ? 'https://auth.example' : 'https://auth.dev.example',
      },
      evidence: { config_fingerprint: 'sha256:fixture' },
    },
  }));
  const bin = join(root, 'bin');
  await execFileAsync('mkdir', ['-p', bin]);
  for (const name of ['npm', 'vercel', 'curl']) {
    const source = join(fixtureDir, `lwc-306-fake-${name}`);
    const target = join(bin, name);
    await execFileAsync('cp', [source, target]);
    await chmod(target, 0o755);
  }
  const env = {
    ...process.env,
    PATH: `${bin}:${process.env.PATH}`,
    FIXTURE_ROOT: root,
    ENVIRONMENT: environment,
    SOURCE_REF: production ? 'main' : 'develop',
    SOURCE_SHA: sourceSha,
    PLAN_PATH: join(root, 'plan.json'),
    JOURNAL_PATH: join(artifactDir, 'journal.json'),
    ROLLBACK_PATH: join(artifactDir, 'rollback.json'),
    ARTIFACT_DIR: artifactDir,
    EVIDENCE_PATH: join(artifactDir, 'readback.json'),
    ROLLBACK_RESULT_PATH: join(artifactDir, 'rollback-result.json'),
    FINAL_EVIDENCE_PATH: join(artifactDir, 'evidence.json'),
    VERCEL_TOKEN: 'vercel-fixture-token',
    VERCEL_PROJECT_ID: 'prj_frontendtest',
    VERCEL_TEAM_ID: 'team_frontendtest',
    VERCEL_API_BASE_URL: 'https://vercel.test',
  };
  return { root, artifactDir, aliases, env };
}

async function run(fixture, mode) {
  return execFileAsync('bash', [scriptPath, mode], { cwd: repoRoot, env: fixture.env }).catch((error) => error);
}

async function json(path) {
  return JSON.parse(await readFile(path, 'utf8'));
}

async function assertNoToken(fixture) {
  const files = await Promise.all([
    lines(join(fixture.root, 'cli-calls')),
    lines(join(fixture.root, 'curl-calls')),
    lines(join(fixture.root, 'auth-events')),
  ]);
  assert.equal(files.flat().some((line) => line.includes(fixture.env.VERCEL_TOKEN)), false);
}

test('builds once with Vercel prebuilt ordering and keeps immutable ID separate from URL', async () => {
  const fixture = await setup();
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.deepEqual(calls.map((line) => line.split(' ')[0]), ['npm', 'vercel', 'vercel', 'vercel']);
  assert.match(calls[1], /pull .*--environment=preview/);
  assert.match(calls[2], /build .*--scope/);
  assert.match(calls[3], /deploy .*--prebuilt.*--json/);
  assert.equal(calls.some((line) => line.includes('npm run build')), false);
  const deployment = await json(join(fixture.artifactDir, 'frontend-deployment.json'));
  assert.equal(deployment.deployment_id, 'dpl_frontendnew');
  assert.equal(deployment.deployment_url, 'https://frontend-hash.vercel.app');
  assert.notEqual(deployment.deployment_id, deployment.deployment_url.split('/').pop());
  assert.deepEqual(await json(join(fixture.artifactDir, 'journal.json')), ['frontend']);
  const reconcile = await run(fixture, 'reconcile');
  assert.equal(reconcile.code, undefined, reconcile.stderr);
  assert.equal((await json(join(fixture.artifactDir, 'readback.json'))).provider_readback, true);
  await assertNoToken(fixture);
});

test('freezes exact project and alias authority before any REST mutation', async () => {
  for (const scenario of ['project-mismatch', 'alias-project-mismatch']) {
    const fixture = await setup('development', scenario);
    const result = await run(fixture, 'freeze');
    assert.notEqual(result.code, undefined);
    assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
  }
});

test('exact wrong-target read-back fails closed without retrying the alias mutation', async () => {
  const fixture = await setup('development', 'wrong-readback');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.notEqual(result.code, undefined);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 1);
  assert.deepEqual(await json(join(fixture.artifactDir, 'journal.json')), ['frontend']);
  await assertNoToken(fixture);
});

test('accepts an ambiguous timeout only when exact alias and project inventory read-back converges', async () => {
  const fixture = await setup('development', 'timeout-target');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json')))[0], 'frontend');
  await assertNoToken(fixture);
});

test('production rollback restores every frozen alias through REST and verifies exact read-back', async () => {
  const fixture = await setup('production', 'second-timeout-old');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const mutation = await run(fixture, 'mutate');
  assert.notEqual(mutation.code, undefined);
  const rollback = await run(fixture, 'rollback');
  assert.equal(rollback.code, undefined, rollback.stderr);
  assert.deepEqual(await json(join(fixture.root, 'aliases.json')), {
    'wiki.rayer.idv.tw': 'dpl_old0', 'llm-wiki-frontend.vercel.app': 'dpl_old1',
  });
  const result = await json(join(fixture.artifactDir, 'rollback-result.json'));
  assert.equal(result.result, 'success');
  assert.equal(result.rollback_verified, true);
  assert.deepEqual(result.frontend.aliases.map(({ alias, deployment_id, converged }) => ({ alias, deployment_id, converged })), [
    { alias: 'wiki.rayer.idv.tw', deployment_id: 'dpl_old0', converged: true },
    { alias: 'llm-wiki-frontend.vercel.app', deployment_id: 'dpl_old1', converged: true },
  ]);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).every((line) => line.includes('/v2/deployments/')), true);
  await assertNoToken(fixture);
});

test('shared frontend path is REST-only and does not use the invalid npm build shortcut', async () => {
  const source = await readFile(scriptPath, 'utf8');
  assert.match(source, /vercel pull/);
  assert.match(source, /vercel build/);
  assert.match(source, /vercel deploy --prebuilt/);
  assert.match(source, /\/v2\/deployments\//);
  assert.doesNotMatch(source, /npm run build/);
  assert.doesNotMatch(source, /vercel alias set/);
});
