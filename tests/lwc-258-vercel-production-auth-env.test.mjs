import assert from 'node:assert/strict';
import { chmod, mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';
import { load as parseYaml } from 'js-yaml';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('..', import.meta.url).pathname;
const workflowPath = join(repoRoot, '.github/workflows/vercel-production-auth-env.yml');
const scriptPath = join(repoRoot, '.github/scripts/vercel-production-auth-env.sh');
const commitSha = '0123456789abcdef0123456789abcdef01234567';
const projectId = 'prj_prod123';
const teamId = 'team_prod123';
const desiredUrl = 'https://auth.rayer.idv.tw';
const fixtureCurl = join(repoRoot, 'tests/fixtures/lwc-258-fake-curl.sh');
const fixtureGit = join(repoRoot, 'tests/fixtures/lwc-258-fake-git.sh');

async function setupProviderCase(scenario) {
  const root = await mkdtemp(join(tmpdir(), 'lwc-258-provider-'));
  const bin = join(root, 'bin');
  const evidenceDir = join(root, 'evidence');
  await mkdir(bin); await mkdir(evidenceDir);
  await execFileAsync('cp', [fixtureCurl, join(bin, 'curl')]);
  await execFileAsync('cp', [fixtureGit, join(bin, 'git')]);
  await chmod(join(bin, 'curl'), 0o755); await chmod(join(bin, 'git'), 0o755);
  const auth = (id, value, target = ['production'], gitBranch = null) => ({ id, key: 'NEXT_PUBLIC_AUTH_URL', value, target, gitBranch, type: 'plain' });
  let envs;
  if (scenario === 'absent') envs = [{ id: 'env_other', key: 'NEXT_PUBLIC_API_URL', value: 'unrelated-secret', target: ['production'], gitBranch: null, type: 'plain' }];
  else if (scenario === 'exact') envs = [auth('env_exact', desiredUrl)];
  else if (scenario === 'duplicate') envs = [auth('env_one', 'https://old-one.invalid'), auth('env_two', 'https://old-two.invalid')];
  else if (scenario === 'branch') envs = [auth('env_branch', 'https://auth-dev.rayer.idv.tw', ['production'], 'develop')];
  else if (scenario === 'production-preview') envs = [auth('env_preview_scope', desiredUrl, ['production', 'preview'])];
  else envs = [auth('env_old', 'old-secret-value')];
  const existingDeployment = { id: 'dpl_existing123', projectId, readyState: 'READY', target: 'production', url: 'https://existing.vercel.app', aliasAssigned: false, alias: [], userAliases: [], automaticAliases: [], gitSource: { type: 'github', org: 'Rayer', repo: 'llm-wiki-frontend', ref: 'main', sha: commitSha } };
  const newDeployment = { id: 'dpl_new123', projectId, readyState: 'READY', target: 'production', url: 'https://dpl-new.vercel.app', aliasAssigned: false, alias: [], userAliases: [], automaticAliases: [], gitSource: { type: 'github', org: 'Rayer', repo: 'llm-wiki-frontend', ref: 'main', sha: commitSha } };
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'env-reads'), '0'); await writeFile(join(root, 'env-mutations'), '0'); await writeFile(join(root, 'deployment-creates'), '0'); await writeFile(join(root, 'deployment-reads'), '0');
  await writeFile(join(root, 'mutation-log'), ''); await writeFile(join(root, 'curl-calls'), ''); await writeFile(join(root, 'deployment-body.json'), '{}');
  await writeFile(join(root, 'state.json'), JSON.stringify({
    project: { id: projectId, name: scenario === 'wrong-project-name' ? 'llm-wiki-cloud' : 'llm-wiki-frontend', accountId: scenario === 'wrong-team' ? 'team_other' : teamId, autoAssignCustomDomains: scenario === 'auto-alias-enabled' ? true : false },
    envs,
    aliases: { 'wiki.rayer.idv.tw': { alias: 'wiki.rayer.idv.tw', projectId: scenario === 'alias-wrong-project' ? 'prj_other' : projectId, deploymentId: 'dpl_existing123' }, 'llm-wiki-frontend.vercel.app': { alias: 'llm-wiki-frontend.vercel.app', projectId, deploymentId: 'dpl_existing123' } },
    deployments: { dpl_existing123: { ...existingDeployment, projectId: scenario === 'deployment-wrong-project' ? 'prj_other' : projectId }, dpl_new123: newDeployment },
  }));
  return { root, bin, evidenceDir };
}

function providerEnv(fixture, scenario, overrides = {}) {
  return {
    ...process.env, PATH: fixture.bin + ':' + process.env.PATH, FIXTURE_ROOT: fixture.root, FIXTURE_SCENARIO: scenario,
    FAKE_HEAD_SHA: commitSha, FAKE_REMOTE_MAIN_SHA: commitSha, GITHUB_ACTIONS: 'true', GITHUB_REF: 'refs/heads/main',
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-frontend', GITHUB_API_URL: 'https://api.github.com', GITHUB_TOKEN: 'github-sentinel-token',
    VERCEL_API_BASE_URL: 'https://api.vercel.com', VERCEL_TOKEN: 'vercel-sentinel-token', VERCEL_PROJECT_ID: projectId, VERCEL_TEAM_ID: teamId,
    VERCEL_SCOPE: 'rayer-tung-s-projects', COMMIT_SHA: commitSha, TICKET_REF: 'LWC-258', EVIDENCE_DIR: fixture.evidenceDir,
    ROLLBACK_ARTIFACT_NAME: 'vercel-production-auth-env-rollback-' + commitSha, ROLLBACK_ARTIFACT_ID: '123456789',
    ROLLBACK_ARTIFACT_URL: 'https://github.com/Rayer/llm-wiki-frontend/actions/runs/123/artifacts/123456789', ROLLBACK_ARTIFACT_DIGEST: 'a'.repeat(64),
    DEPLOYMENT_POLL_ATTEMPTS: '2', DEPLOYMENT_POLL_INTERVAL_SECONDS: '0', ...overrides,
  };
}

async function runProvider(fixture, scenario, mode, overrides = {}) {
  try { return await execFileAsync('bash', [scriptPath, mode], { cwd: repoRoot, env: providerEnv(fixture, scenario, overrides), maxBuffer: 1024 * 1024 }); }
  catch (error) { return error; }
}

async function providerCase(scenario, overrides = {}) {
  const fixture = await setupProviderCase(scenario);
  const preflight = await runProvider(fixture, scenario, 'preflight', overrides);
  const mutate = preflight.code === undefined ? await runProvider(fixture, scenario, 'mutate', overrides) : null;
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-production-auth-env.json'), 'utf8'));
  const rollback = await readFile(join(fixture.evidenceDir, 'rollback-contract.json'), 'utf8').then(JSON.parse).catch(() => null);
  const state = JSON.parse(await readFile(join(fixture.root, 'state.json'), 'utf8'));
  const mutationLog = (await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim().split('\n').filter(Boolean);
  const body = JSON.parse(await readFile(join(fixture.root, 'deployment-body.json'), 'utf8'));
  return { fixture, preflight, mutate, evidence, rollback, state, mutationLog, body };
}

test('workflow is main/Production authority and uploads freeze before the only mutation step', async () => {
  const workflow = parseYaml(await readFile(workflowPath, 'utf8'));
  const job = workflow.jobs.configure; const steps = job.steps;
  assert.equal(job.if, "github.ref == 'refs/heads/main'"); assert.equal(job.environment.name, 'Production');
  assert.equal(workflow.on.workflow_dispatch.inputs.commit_sha.required, true);
  assert.deepEqual(steps.map((step) => step.name), ['Check out the exact requested SHA', 'Validate exact main SHA and canonical CI', 'Preflight production auth environment and capture rollback', 'Upload durable production auth rollback contract', 'Apply and verify production auth environment and create exact deployment', 'Upload normalized production auth evidence']);
  assert.ok(steps.indexOf(steps[3]) < steps.indexOf(steps[4])); assert.equal(steps[4].if, "steps.rollback_upload.outcome == 'success'");
  for (const step of [steps[1], steps[2], steps[3], steps[4], steps[5]]) assert.equal(step.env.EVIDENCE_DIR, '${{ runner.temp }}/vercel-production-auth-env');
});

test('source and safety contract is main-only, production-only, and API-based', async () => {
  const source = await readFile(scriptPath, 'utf8');
  assert.match(source, /ls-remote origin refs\/heads\/main/); assert.match(source, /branch=main/); assert.match(source, /head_branch=="main"/);
  assert.match(source, /target:"production"/); assert.match(source, /auth\.rayer\.idv\.tw/); assert.match(source, /gitSource:\{type:"github"/);
  assert.doesNotMatch(source, /refs\/heads\/develop|auth-dev\.rayer\.idv\.tw|NEXT_PUBLIC_API_URL|vercel\s+(deploy|alias)|git\s+(push|update-ref)/i);
  assert.doesNotMatch(source, /--request\s+(POST|PATCH|DELETE).*v4\/aliases|v4\/aliases.*--request\s+(POST|PATCH|DELETE)/s);
});

test('YAML and shell contracts parse', async () => {
  assert.equal(parseYaml(await readFile(workflowPath, 'utf8')).name, 'Vercel Production Auth Environment Configuration');
  const result = await execFileAsync('bash', ['-n', scriptPath]).catch((error) => error); assert.equal(result.code ?? 0, 0, result.stderr);
});

test('preflight is read-only and freezes aliases plus immutable deployment identities', async () => {
  const fixture = await setupProviderCase('old'); const result = await runProvider(fixture, 'old', 'preflight');
  assert.equal(result.code, undefined, result.stderr); const rollback = JSON.parse(await readFile(join(fixture.evidenceDir, 'rollback-contract.json'), 'utf8'));
  assert.equal((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
  assert.equal(rollback.source.ref, 'refs/heads/main'); assert.equal(rollback.source.canonical_ci.head_branch, 'main');
  assert.deepEqual(rollback.freeze.aliases.map(({ alias }) => alias), ['wiki.rayer.idv.tw', 'llm-wiki-frontend.vercel.app']);
  assert.deepEqual(rollback.freeze.deployments.map(({ id }) => id), ['dpl_existing123']);
});

test('OpenAPI-shaped alias/deployment reads succeed and alias project mismatch fails before writes', async () => {
  const exact = await setupProviderCase('exact'); const preflight = await runProvider(exact, 'exact', 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  const rollback = JSON.parse(await readFile(join(exact.evidenceDir, 'rollback-contract.json'), 'utf8'));
  assert.deepEqual(rollback.freeze.aliases, [
    { alias: 'wiki.rayer.idv.tw', project_id: projectId, deployment_id: 'dpl_existing123' },
    { alias: 'llm-wiki-frontend.vercel.app', project_id: projectId, deployment_id: 'dpl_existing123' },
  ]);
  assert.deepEqual(rollback.freeze.deployments[0].team_id, teamId);

  const mismatch = await providerCase('alias-wrong-project');
  assert.notEqual(mismatch.preflight.code, undefined);
  assert.equal(mismatch.evidence.reason_code, 'FREEZE_READ_FAILED');
  assert.deepEqual(mismatch.mutationLog, []);
  assert.equal(mismatch.evidence.deployment_create_count, 0);
});

test('exact env singleton still creates one exact Git-source production deployment', async () => {
  const run = await providerCase('exact');
  assert.equal(run.preflight.code, undefined, run.preflight?.stderr); assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  assert.deepEqual(run.mutationLog, ['DEPLOY_POST']); assert.equal(run.evidence.env_mutation_count, 0); assert.equal(run.evidence.deployment_create_count, 1);
  assert.equal(run.evidence.provider_verification.deployment.id, 'dpl_new123'); assert.equal(run.evidence.provider_verification.deployment.url, 'https://dpl-new.vercel.app');
  assert.deepEqual(run.body, { name: 'llm-wiki-frontend', project: projectId, target: 'production', gitSource: { type: 'github', org: 'Rayer', repo: 'llm-wiki-frontend', ref: 'main', sha: commitSha } });
  assert.match(await readFile(join(run.fixture.root, 'curl-calls'), 'utf8'), new RegExp(`POST https://api\\.vercel\\.com/v13/deployments\\?forceNew=1&teamId=${teamId}`));
  assert.equal(run.body.autoAssignCustomDomains, undefined);
});

test('teamId scoping is exact on every alias and deployment read', async () => {
  const run = await providerCase('exact'); assert.equal(run.preflight.code, undefined, run.preflight?.stderr); assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  const reads = (await readFile(join(run.fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter((line) => line.startsWith('GET '));
  const aliases = reads.filter((line) => line.includes('/v4/aliases/')); const deployments = reads.filter((line) => line.includes('/v13/deployments/'));
  assert.ok(aliases.length > 0); assert.ok(deployments.length > 0);
  assert.ok(aliases.every((line) => line.split(' ')[1].endsWith(`?teamId=${teamId}`)), aliases.join('\n'));
  assert.ok(deployments.every((line) => line.split(' ')[1].endsWith(`?teamId=${teamId}&withGitRepoInfo=true`)), deployments.join('\n'));
});

test('env mutation converges before deployment and rollback remains exact', async () => {
  const run = await providerCase('old'); assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  assert.deepEqual(run.mutationLog, ['ENV_PATCH', 'DEPLOY_POST']); assert.equal(run.evidence.env_mutation_count, 1); assert.equal(run.evidence.deployment_create_count, 1);
  const mismatch = await providerCase('readback-mismatch'); assert.notEqual(mismatch.mutate.code, undefined); assert.equal(mismatch.evidence.rollback.result, 'RESTORED');
  assert.deepEqual(mismatch.mutationLog, ['ENV_PATCH', 'ENV_PATCH']); assert.equal(mismatch.evidence.deployment_create_count, 0); assert.equal(mismatch.state.envs[0].value, 'old-secret-value');
});

test('wrong project, wrong team, alias/deployment identity, scope, and failed CI are preflight failures with zero writes', async () => {
  for (const scenario of ['wrong-project-name', 'wrong-team', 'alias-wrong-project', 'deployment-wrong-project', 'auto-alias-enabled', 'production-preview', 'branch', 'duplicate', 'ci-failure', 'ci-wrong-ref']) {
    const run = await providerCase(scenario); assert.notEqual(run.preflight.code, undefined, scenario); assert.deepEqual(run.mutationLog, [], scenario); assert.equal(run.evidence.deployment_create_count, 0, scenario);
  }
});

test('deployment create is attempted at most once and all uncertain/failed readbacks are partial', async () => {
  for (const scenario of ['create-failure', 'invalid-create-response', 'malformed-create-response', 'create-invalid-url-scheme', 'create-invalid-url-path', 'create-invalid-url-whitespace', 'deployment-read-failure', 'deployment-timeout', 'deployment-failed', 'deployment-source-mismatch', 'post-create-alias-drift', 'post-create-alias-read-failure', 'post-create-alias-assigned', 'post-create-canonical-alias-array', 'post-create-alias-missing', 'post-create-alias-null', 'post-create-alias-malformed']) {
    const run = await providerCase(scenario); assert.notEqual(run.mutate.code, undefined, scenario); assert.equal(run.evidence.status, 'PARTIAL_MUTATION', scenario); assert.equal(run.evidence.phase.startsWith('deployment_'), true, scenario); assert.equal(run.evidence.partial_uncertainty, true, scenario);
    assert.equal(run.evidence.deployment_create_count, 1, scenario); assert.equal(run.mutationLog.filter((entry) => entry === 'DEPLOY_POST').length, 1, scenario);
  }
});

test('bounded polling accepts ANALYZING and DEPLOYING before READY', async () => {
  for (const scenario of ['deployment-analyzing', 'deployment-deploying']) {
    const run = await providerCase(scenario); assert.equal(run.preflight.code, undefined, scenario); assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
    assert.equal(run.evidence.status, 'SUCCESS', scenario); assert.equal(run.evidence.provider_verification.deployment.ready_state, 'READY', scenario);
  }
});

test('create hostname is normalized to an HTTPS deployment URL', async () => {
  for (const scenario of ['exact', 'create-https-url']) {
    const run = await providerCase(scenario); assert.equal(run.mutate.code, undefined, scenario); assert.equal(run.evidence.provider_verification.deployment.url, 'https://dpl-new.vercel.app', scenario);
  }
});

test('create response routing indicators remain optional', async () => {
  for (const scenario of ['create-alias-assigned', 'create-canonical-alias-array']) {
    const run = await providerCase(scenario); assert.equal(run.mutate.code, undefined, run.mutate?.stderr); assert.equal(run.evidence.status, 'SUCCESS', scenario);
  }
});

test('freeze read failure and invalid artifact block all provider writes', async () => {
  const frozen = await providerCase('freeze-read-failure'); assert.notEqual(frozen.preflight.code, undefined); assert.deepEqual(frozen.mutationLog, []);
  const fixture = await setupProviderCase('old'); const preflight = await runProvider(fixture, 'old', 'preflight'); assert.equal(preflight.code, undefined);
  const rejected = await runProvider(fixture, 'old', 'mutate', { ROLLBACK_ARTIFACT_ID: '' }); assert.notEqual(rejected.code, undefined);
  assert.deepEqual((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
});

test('accepts raw artifact digest and normalizes evidence to sha256 type', async () => {
  const run = await providerCase('exact');
  assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  assert.equal(run.evidence.rollback.artifact_digest, `sha256:${'a'.repeat(64)}`);
});

test('rejects non-raw-lowercase-64-hex artifact digests before provider writes', async () => {
  for (const digest of [`sha256:${'a'.repeat(64)}`, 'A'.repeat(64), 'a'.repeat(63), 'a'.repeat(65), '']) {
    const fixture = await setupProviderCase('old');
    const preflight = await runProvider(fixture, 'old', 'preflight');
    assert.equal(preflight.code, undefined, digest);
    const mutate = await runProvider(fixture, 'old', 'mutate', { ROLLBACK_ARTIFACT_DIGEST: digest });
    assert.notEqual(mutate.code, undefined, digest);
    assert.deepEqual((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '', digest);
  }
});

test('mutate rereads the durable freeze and fails closed on pre-mutation drift', async () => {
  const fixture = await setupProviderCase('old'); const preflight = await runProvider(fixture, 'old', 'preflight'); assert.equal(preflight.code, undefined, preflight.stderr);
  const state = JSON.parse(await readFile(join(fixture.root, 'state.json'), 'utf8')); state.aliases['wiki.rayer.idv.tw'].deploymentId = 'dpl_new123';
  await writeFile(join(fixture.root, 'state.json'), JSON.stringify(state));
  const mutate = await runProvider(fixture, 'old', 'mutate'); assert.notEqual(mutate.code, undefined); assert.equal(JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-production-auth-env.json'), 'utf8')).reason_code, 'FREEZE_DRIFT');
  assert.deepEqual((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
});

test('evidence and provider logs keep tokens and unrelated values out of output', async () => {
  const run = await providerCase('old'); const evidenceText = JSON.stringify(run.evidence); const calls = await readFile(join(run.fixture.root, 'curl-calls'), 'utf8');
  assert.doesNotMatch(evidenceText, /vercel-sentinel-token|old-secret-value|unrelated-secret/); assert.doesNotMatch(calls, /vercel-sentinel-token|old-secret-value|unrelated-secret/);
});
