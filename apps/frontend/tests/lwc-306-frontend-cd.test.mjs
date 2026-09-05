import assert from 'node:assert/strict';
import { chmod, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('../../..', import.meta.url).pathname.replace(/\/$/, '');
const scriptPath = join(repoRoot, 'deploy/cd.sh');
const componentScriptPath = join(repoRoot, 'deploy/components/frontend.sh');
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

async function resetEvents(fixture) {
  await writeFile(join(fixture.root, 'provider-events'), '');
}

async function setup(environment = 'development', scenario = 'success') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-306-frontend-'));
  const artifactDir = join(root, 'artifacts');
  const production = environment === 'production';
  const aliases = production ? ['wiki.rayer.idv.tw', 'llm-wiki-frontend.vercel.app'] : ['wiki.dev.rayer.idv.tw'];
  const projectName = production ? 'llm-wiki-frontend' : 'llm-wiki-frontend-dev';
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'aliases.json'), JSON.stringify(Object.fromEntries(aliases.map((alias, index) => [alias, scenario === 'already-converged' ? 'dpl_frontendnew' : `dpl_old${index}`]))));
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
    EXPECTED_REPO_ROOT: repoRoot,
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
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'accepted');
  const reconcile = await run(fixture, 'reconcile');
  assert.equal(reconcile.code, undefined, reconcile.stderr);
  assert.equal((await json(join(fixture.artifactDir, 'components/frontend.json'))).result, 'success');
  await assertNoToken(fixture);
});

test('reuses a live-shaped existing candidate before any build mutation', async () => {
  const fixture = await setup('development', 'existing-live-candidate');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  await resetEvents(fixture);

  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.deepEqual(await lines(join(fixture.root, 'provider-events')), [
    'project', 'deployment-inventory', 'inspect', 'alias', 'alias-inventory', 'alias-post', 'alias', 'alias-inventory',
  ]);
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.equal(calls.filter((line) => line.startsWith('npm ')).length, 0);
  assert.equal(calls.filter((line) => line.startsWith('vercel pull ')).length, 0);
  assert.equal(calls.filter((line) => line.startsWith('vercel build ')).length, 0);
  assert.equal(calls.filter((line) => line.startsWith('vercel deploy ')).length, 0);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 1);
  assert.equal((await json(join(fixture.artifactDir, 'frontend-deployment.json'))).deployment_id, 'dpl_frontendnew');
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'accepted');
});

test('production already-converged candidate performs no provider write', async () => {
  const fixture = await setup('production', 'already-converged');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  await resetEvents(fixture);

  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.deepEqual(await lines(join(fixture.root, 'provider-events')), [
    'project', 'deployment-inventory', 'inspect', 'alias', 'alias-inventory', 'alias', 'alias-inventory',
  ]);
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.equal(calls.length, 0);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
  assert.equal((await json(join(fixture.artifactDir, 'frontend-deployment.json'))).deployment_id, 'dpl_frontendnew');
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'rejected_or_no_mutation');
});

test('production freeze paginates the complete alias inventory before mutation', async () => {
  const fixture = await setup('production', 'alias-pagination');
  const result = await run(fixture, 'freeze');
  assert.equal(result.code, undefined, result.stderr);
  assert.deepEqual((await json(join(fixture.artifactDir, 'rollback.json'))).handles.frontend.aliases, [
    { alias: 'wiki.rayer.idv.tw', project_id: 'prj_frontendtest', team_id: 'team_frontendtest', deployment_id: 'dpl_old0' },
    { alias: 'llm-wiki-frontend.vercel.app', project_id: 'prj_frontendtest', team_id: 'team_frontendtest', deployment_id: 'dpl_old1' },
  ]);
  const inventoryCalls = (await lines(join(fixture.root, 'curl-calls'))).filter((line) => line.includes('/v4/aliases?'));
  assert.equal(inventoryCalls.length, 4);
  assert.equal(inventoryCalls.filter((line) => line.includes('&until=1700000000000')).length, 2);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
});

test('production mutate paginates a live-shaped 100+48 deployment inventory without argv overflow', async () => {
  const fixture = await setup('production', 'deployment-pagination');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  await resetEvents(fixture);

  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  const inventoryCalls = (await lines(join(fixture.root, 'curl-calls'))).filter((line) => line.includes('/v6/deployments?'));
  assert.equal(inventoryCalls.length, 2);
  assert.equal(inventoryCalls.filter((line) => line.includes('&until=1700000000001')).length, 1);
  assert.equal((await lines(join(fixture.root, 'cli-calls'))).filter((line) => line.startsWith('vercel deploy ')).length, 0);
  assert.equal((await json(join(fixture.artifactDir, 'frontend-deployment.json'))).deployment_id, 'dpl_frontendnew');
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'accepted');
});

for (const scenario of ['duplicate-exact', 'foreign-candidate', 'malformed-candidate', 'missing-uid', 'wrong-authority', 'inventory-unreadable']) {
  test(`${scenario} fails closed before provider mutation`, async () => {
    const fixture = await setup('development', scenario);
    assert.equal((await run(fixture, 'freeze')).code, undefined);
    await resetEvents(fixture);

    await run(fixture, 'mutate');
    assert.equal((await lines(join(fixture.root, 'cli-calls'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
    assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'rejected_or_no_mutation');
  });
}

test('zero candidate preserves the existing build and create path', async () => {
  const fixture = await setup('development', 'zero-candidate');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  await resetEvents(fixture);

  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.deepEqual((await lines(join(fixture.root, 'provider-events'))).slice(0, 10), [
    'project', 'deployment-inventory', 'npm-ci', 'pull', 'build', 'project', 'alias', 'alias-inventory', 'deploy', 'inspect',
  ]);
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.equal(calls.filter((line) => line.startsWith('npm ')).length, 1);
  assert.equal(calls.filter((line) => line.startsWith('vercel pull ')).length, 1);
  assert.equal(calls.filter((line) => line.startsWith('vercel build ')).length, 1);
  assert.equal(calls.filter((line) => line.startsWith('vercel deploy ')).length, 1);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 1);
});

test('production inspect target null is rejected', async () => {
  const fixture = await setup('production', 'production-null-target');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  await resetEvents(fixture);

  await run(fixture, 'mutate');
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.equal(calls.filter((line) => line.startsWith('vercel deploy ')).length, 1);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
});

test('freezes exact project and alias authority before any REST mutation', async () => {
  for (const scenario of ['project-mismatch', 'alias-project-mismatch']) {
    const fixture = await setup('development', scenario);
    const result = await run(fixture, 'freeze');
    assert.notEqual(result.code, undefined);
    assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
  }
});

test('rejects a full alias page without authoritative pagination metadata', async () => {
  const fixture = await setup('development', 'alias-full-missing-pagination');
  const result = await run(fixture, 'freeze');
  assert.notEqual(result.code, undefined);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 0);
});

test('exact wrong-target read-back fails closed without retrying the alias mutation', async () => {
  const fixture = await setup('development', 'wrong-readback');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.notEqual(result.code, undefined);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).length, 1);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'unknown');
  await assertNoToken(fixture);
});

test('accepts an ambiguous timeout only when exact alias and project inventory read-back converges', async () => {
  const fixture = await setup('development', 'timeout-target');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'accepted');
  await assertNoToken(fixture);
});

test('reconciles a deployment timeout through one exact project inventory candidate without retrying create', async () => {
  const fixture = await setup('development', 'deploy-timeout-after-acceptance');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  const calls = await lines(join(fixture.root, 'cli-calls'));
  assert.equal(calls.filter((line) => line.startsWith('vercel deploy ')).length, 1);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'accepted');
  assert.ok((await lines(join(fixture.root, 'curl-calls'))).some((line) => line.includes('/v6/deployments?')));
});

test('rejects a full deployment page without authoritative pagination metadata', async () => {
  const fixture = await setup('development', 'deploy-timeout-full-missing-pagination');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.notEqual(result.code, undefined);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'unknown');
  assert.equal((await lines(join(fixture.root, 'cli-calls'))).filter((line) => line.startsWith('vercel deploy ')).length, 1);
});

test('rejects an arbitrary deployment host after timeout reconciliation', async () => {
  const fixture = await setup('development', 'deploy-timeout-arbitrary-host');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.notEqual(result.code, undefined);
  assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'unknown');
});

test('accepts the canonical deployment host after timeout reconciliation', async () => {
  const fixture = await setup('development', 'deploy-timeout-canonical-host');
  assert.equal((await run(fixture, 'freeze')).code, undefined);
  const result = await run(fixture, 'mutate');
  assert.equal(result.code, undefined, result.stderr);
  assert.equal((await json(join(fixture.artifactDir, 'frontend-deployment.json'))).deployment_url, 'https://frontend-hash.vercel.app');
});

test('accepts a short terminal alias page without pagination metadata', async () => {
  const fixture = await setup('development', 'alias-terminal-short-missing-pagination');
  const result = await run(fixture, 'freeze');
  assert.equal(result.code, undefined, result.stderr);
});

for (const scenario of ['deploy-timeout-absent', 'deploy-timeout-duplicate', 'deploy-timeout-conflicting']) {
  test(`keeps ${scenario} unknown after one deployment inventory lookup`, async () => {
    const fixture = await setup('development', scenario);
    assert.equal((await run(fixture, 'freeze')).code, undefined);
    const result = await run(fixture, 'mutate');
    assert.notEqual(result.code, undefined);
    const calls = await lines(join(fixture.root, 'cli-calls'));
    assert.equal(calls.filter((line) => line.startsWith('vercel deploy ')).length, 1);
    assert.equal((await json(join(fixture.artifactDir, 'journal.json'))).components.frontend.state, 'unknown');
    assert.ok((await lines(join(fixture.root, 'curl-calls'))).some((line) => line.includes('/v6/deployments?')));
  });
}

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
  const result = await json(join(fixture.artifactDir, 'rollback/frontend.json'));
  assert.equal(result.result, 'success');
  assert.equal(result.verified, true);
  assert.deepEqual(result.readback.aliases.map(({ alias, deployment_id, converged }) => ({ alias, deployment_id, converged })), [
    { alias: 'wiki.rayer.idv.tw', deployment_id: 'dpl_old0', converged: true },
    { alias: 'llm-wiki-frontend.vercel.app', deployment_id: 'dpl_old1', converged: true },
  ]);
  assert.equal((await lines(join(fixture.root, 'alias-post-calls'))).every((line) => line.includes('/v2/deployments/')), true);
  await assertNoToken(fixture);
});

test('shared frontend path is REST-only and does not use the invalid npm build shortcut', async () => {
  const source = `${await readFile(scriptPath, 'utf8')}\n${await readFile(componentScriptPath, 'utf8')}`;
  assert.match(source, /vercel pull/);
  assert.match(source, /vercel build/);
  assert.match(source, /vercel deploy --prebuilt/);
  assert.match(source, /\/v2\/deployments\//);
  assert.doesNotMatch(source, /npm run build/);
  assert.doesNotMatch(source, /vercel alias set/);
});
