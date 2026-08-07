import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chmod, mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { load as parseYaml } from 'js-yaml';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('..', import.meta.url).pathname;
const sha = '0123456789abcdef0123456789abcdef01234567';
const oldSha = 'fedcba9876543210fedcba9876543210fedcba98';
const canonicalProject = 'prj_canonical';
const legacyProject = 'prj_legacy';
const team = 'team_test';
const oldDeployment = 'dpl_old';
const newDeployment = 'dpl_new';
const domain = 'llm-wiki-frontend-dev.vercel.app';
const artifactDigest = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';

function acknowledgement(project = legacyProject, deployment = oldDeployment, desired = sha) {
  return `I acknowledge LWC-253 authority reconciliation: alias=${domain} old_project=${project} old_deployment=${deployment} desired_sha=${desired}`;
}

async function setup(scenario = 'existing-candidate') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-253-reconcile-'));
  const bin = join(root, 'bin');
  const evidenceDir = join(root, 'evidence');
  await mkdir(bin);
  await mkdir(evidenceDir);
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'mutation-log'), '');
  await writeFile(join(root, 'deployment-post-log'), '');
  await writeFile(join(root, 'curl-calls'), '');
  await writeFile(join(root, 'canonical-project.json'), JSON.stringify({ id: canonicalProject, name: 'llm-wiki-frontend-dev', accountId: team }));
  await writeFile(join(root, 'legacy-project.json'), JSON.stringify({ id: legacyProject, name: 'llm-wiki-frontend', accountId: team }));
  await writeFile(join(root, 'domains.json'), JSON.stringify({ domains: [{ name: domain }] }));
  await writeFile(join(root, 'ci.json'), JSON.stringify({ workflow_runs: [{ path: '.github/workflows/ci.yml', head_branch: 'develop', head_sha: sha, event: 'push', status: 'completed', conclusion: 'success', id: 123, html_url: 'https://github.test/Rayer/llm-wiki-frontend/actions/runs/123' }] }));
  await writeFile(join(root, 'old-deployment.json'), JSON.stringify({ id: oldDeployment, url: 'https://old.vercel.app', projectId: legacyProject, teamId: team, readyState: 'READY', target: null, meta: { githubDeployment: '1', githubOrg: 'Rayer', githubRepo: 'llm-wiki-frontend', githubCommitRef: 'develop', githubCommitSha: oldSha } }));
  await writeFile(join(root, 'candidate.json'), JSON.stringify({ id: newDeployment, url: 'https://new.vercel.app', projectId: canonicalProject, teamId: team, readyState: 'READY', target: 'preview', meta: { githubDeployment: '1', githubOrg: 'Rayer', githubRepo: 'llm-wiki-frontend', githubCommitRef: 'develop', githubCommitSha: sha } }));
  const initialState = { global: { alias: domain, projectId: legacyProject, deploymentId: oldDeployment }, legacyAliases: [{ alias: domain, projectId: legacyProject, deploymentId: oldDeployment }], canonicalAliases: [] };
  if (scenario === 'canonical-alias-present') initialState.canonicalAliases = [{ alias: domain, projectId: canonicalProject, deploymentId: 'dpl_wrong' }];
  if (scenario === 'legacy-inventory-duplicate') initialState.legacyAliases.push({ alias: domain, projectId: legacyProject, deploymentId: oldDeployment });
  await writeFile(join(root, 'state.json'), JSON.stringify(initialState));
  for (const fixture of ['lwc-253-reconciliation-fake-curl.sh', 'lwc-253-reconciliation-fake-vercel.sh']) {
    const destination = join(bin, fixture === 'lwc-253-reconciliation-fake-curl.sh' ? 'curl' : 'vercel');
    await execFileAsync('cp', [join(repoRoot, 'tests/fixtures', fixture), destination]);
    await chmod(destination, 0o755);
  }
  return { root, bin, evidenceDir };
}

function envFor(fixture, overrides = {}) {
  const environment = { ...process.env };
  delete environment.GITHUB_ACTIONS;
  delete environment.CI;
  return {
    ...environment,
    PATH: `${fixture.bin}:${process.env.PATH}`,
    FIXTURE_ROOT: fixture.root,
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-frontend',
    GITHUB_TOKEN: 'github-sentinel-token',
    VERCEL_TOKEN: 'vercel-sentinel-token',
    VERCEL_API_BASE_URL: 'https://vercel.test',
    GITHUB_API_URL: 'https://github.test',
    VERCEL_PROJECT_ID: canonicalProject,
    VERCEL_TEAM_ID: team,
    VERCEL_SCOPE: 'rayer-tung-s-projects',
    COMMIT_SHA: sha,
    CURRENT_HEAD_SHA: sha,
    CURRENT_REMOTE_DEVELOP_SHA: sha,
    EXPECTED_CURRENT_ALIAS_PROJECT_ID: legacyProject,
    EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID: oldDeployment,
    EXPECTED_CURRENT_ALIAS_SOURCE_SHA: oldSha,
    TICKET_REF: 'LWC-253',
    RECONCILIATION_ACK: acknowledgement(),
    GITHUB_RUN_ID: '123',
    RECONCILIATION_ARTIFACT_NAME: `vercel-authority-reconciliation-rollback-${sha}`,
    ROLLBACK_ARTIFACT_ID: '456',
    ROLLBACK_ARTIFACT_URL: `https://github.com/Rayer/llm-wiki-frontend/actions/runs/123/artifacts/456`,
    ROLLBACK_ARTIFACT_DIGEST: artifactDigest,
    EVIDENCE_DIR: fixture.evidenceDir,
    STABLE_DOMAIN: domain,
    LWC253_TEST_MODE: '1',
    VERCEL_POLL_ATTEMPTS: '2',
    VERCEL_POLL_INTERVAL_SECONDS: '0',
    VERCEL_ALIAS_TIMEOUT_SECONDS: '5',
    ...overrides,
  };
}

async function run(fixture, mode, overrides = {}) {
  try {
    return await execFileAsync('bash', ['.github/scripts/vercel-dev-authority-reconciliation.sh', mode], { cwd: repoRoot, env: envFor(fixture, overrides), maxBuffer: 1024 * 1024 });
  } catch (error) {
    return error;
  }
}

async function evidence(fixture) {
  return JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-authority-reconciliation.json'), 'utf8'));
}

async function lines(path) {
  return (await readFile(path, 'utf8')).trim().split('\n').filter(Boolean);
}

test('GREEN reuses exact canonical candidate and performs exactly one alias mutation', async () => {
  const fixture = await setup('existing-candidate');
  const preflight = await run(fixture, 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, undefined, result.stderr);
  const finalEvidence = await evidence(fixture);
  assert.equal(finalEvidence.mode, 'authority_reconciliation');
  assert.equal(finalEvidence.status, 'SUCCESS');
  assert.equal(finalEvidence.provider_verification.mutation_count, 1);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
  assert.equal(finalEvidence.rollback.project_id, legacyProject);
  assert.equal(finalEvidence.rollback.deployment_id, oldDeployment);
});

test('GREEN create-needed performs one create after handoff and one alias mutation', async () => {
  const fixture = await setup('create-needed');
  const preflight = await run(fixture, 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  const before = await evidence(fixture);
  assert.equal(before.status, 'PREFLIGHT_READY');
  assert.equal(before.provider_verification.mutation_count, 0);
  assert.equal(before.deployment.id, null);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, undefined, result.stderr);
  const after = await evidence(fixture);
  assert.equal(after.status, 'SUCCESS');
  assert.equal(after.provider_verification.mutation_count, 2);
  assert.equal(after.deployment.id, newDeployment);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 1);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
});

for (const [label, overrides, reason] of [
  ['wrong ticket', { TICKET_REF: 'LWC-252' }, 'TICKET_REF_INVALID'],
  ['wrong acknowledgement', { RECONCILIATION_ACK: 'approve' }, 'ACKNOWLEDGEMENT_INVALID'],
  ['old project equals canonical', { EXPECTED_CURRENT_ALIAS_PROJECT_ID: canonicalProject }, 'OLD_PROJECT_EQUALS_CANONICAL'],
  ['old source SHA format', { EXPECTED_CURRENT_ALIAS_SOURCE_SHA: 'ABC' }, 'INPUT_OLD_SHA_INVALID'],
]) {
  test(`rejects ${label} before any provider mutation`, async () => {
    const fixture = await setup();
    const result = await run(fixture, 'preflight', overrides);
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PREFLIGHT_FAILED');
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  });
}

test('bad artifact handoff blocks create and alias writes', async () => {
  const fixture = await setup('create-needed');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote', { ROLLBACK_ARTIFACT_DIGEST: `sha256:${artifactDigest}` });
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.reason_code, 'ROLLBACK_ARTIFACT_INVALID');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
});

test('authority drift after preflight is zero-mutation fail-closed', async () => {
  const fixture = await setup('existing-candidate');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  await writeFile(join(fixture.root, 'state.json'), JSON.stringify({ global: { alias: domain, projectId: 'prj_other', deploymentId: 'dpl_other' }, legacyAliases: [], canonicalAliases: [] }));
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.reason_code, 'AUTHORITY_RECHECK_FAILED');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
});

test('complete paginated inventories include required page-two authority records', async () => {
  const fixture = await setup('alias-page-2');
  const result = await run(fixture, 'preflight');
  assert.equal(result.code, undefined, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PREFLIGHT_READY');
  assert.ok((await readFile(join(fixture.root, 'curl-calls'), 'utf8')).includes('until=alias-cursor-2'));
});

for (const [scenario, reason] of [
  ['alias-cursor-loop', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-malformed', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['canonical-alias-present', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['legacy-inventory-duplicate', 'AUTHORITY_PREFLIGHT_MISMATCH'],
]) {
  test(`${scenario} is rejected without provider mutation`, async () => {
    const fixture = await setup(scenario);
    const result = await run(fixture, 'preflight');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  });
}

test('create uncertainty is PARTIAL_MUTATION and never retries alias', async () => {
  const fixture = await setup('create-failure');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PARTIAL_MUTATION');
  assert.equal(output.reason_code, 'DEPLOYMENT_CREATE_UNCERTAIN');
  assert.equal(output.provider_verification.mutation_count, 1);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 1);
});

test('workflow is manual, develop-gated, pinned, and shares normal concurrency', async () => {
  const normal = parseYaml(await readFile(join(repoRoot, '.github/workflows/vercel-dev-deployment.yml'), 'utf8'));
  const reconciliation = parseYaml(await readFile(join(repoRoot, '.github/workflows/vercel-dev-authority-reconciliation.yml'), 'utf8'));
  assert.equal(reconciliation.concurrency.group, normal.concurrency.group);
  assert.deepEqual(Object.keys(reconciliation.on), ['workflow_dispatch']);
  assert.equal(reconciliation.jobs.reconcile.if, "github.ref == 'refs/heads/develop'");
  assert.equal(reconciliation.jobs.reconcile.environment.name, 'Development');
  const inputs = reconciliation.on.workflow_dispatch.inputs;
  for (const name of ['commit_sha', 'expected_current_alias_project_id', 'expected_current_alias_deployment_id', 'expected_current_alias_source_sha', 'ticket_ref', 'reconciliation_ack']) {
    assert.equal(inputs[name].required, true);
    assert.equal(inputs[name].type, 'string');
  }
  const steps = reconciliation.jobs.reconcile.steps;
  const names = steps.map(({ name }) => name);
  assert.ok(names.indexOf('Validate requested SHA, remote develop, and canonical CI') < names.indexOf('Install pinned Vercel CLI'));
  assert.ok(names.indexOf('Install pinned Vercel CLI') < names.indexOf('Read-only reconciliation preflight'));
  assert.ok(names.indexOf('Read-only reconciliation preflight') < names.indexOf('Upload durable reconciliation rollback contract'));
  assert.ok(names.indexOf('Upload durable reconciliation rollback contract') < names.indexOf('Reconciliation promote (first mutation-capable step)'));
  assert.ok(steps.every(({ uses }) => !uses || /@[0-9a-f]{40}/.test(uses)));
  assert.ok(normal.jobs.promote.steps.filter(({ uses }) => uses).every(({ uses }) => /@[0-9a-f]{40}/.test(uses)));
  assert.match(normal.jobs.promote.steps.find(({ name }) => name === 'Install pinned Vercel CLI').run, /vercel@52\.0\.0/);
  const runBlocks = steps.filter(({ run }) => typeof run === 'string').map(({ run }) => run.replace(/\$\{\{[\s\S]*?\}\}/g, 'VALUE'));
  await execFileAsync('bash', ['-n', '.github/scripts/vercel-dev-authority-reconciliation.sh']);
  await execFileAsync('bash', ['-n', '-c', runBlocks.join('\n')]);
  assert.match(await readFile(join(repoRoot, '.github/scripts/vercel-dev-authority-reconciliation.sh'), 'utf8'), /api_post/);
});
