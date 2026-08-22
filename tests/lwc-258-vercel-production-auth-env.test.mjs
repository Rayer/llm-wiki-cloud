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
  await mkdir(bin);
  await mkdir(evidenceDir);
  await execFileAsync('cp', [fixtureCurl, join(bin, 'curl')]);
  await execFileAsync('cp', [fixtureGit, join(bin, 'git')]);
  await chmod(join(bin, 'curl'), 0o755);
  await chmod(join(bin, 'git'), 0o755);
  const auth = (id, value, target = ['production'], gitBranch = null) => ({ id, key: 'NEXT_PUBLIC_AUTH_URL', value, target, gitBranch, type: 'plain' });
  let envs;
  if (scenario === 'absent') envs = [{ id: 'env_other', key: 'NEXT_PUBLIC_API_URL', value: 'unrelated-secret', target: ['production'], gitBranch: null, type: 'plain' }];
  else if (scenario === 'exact') envs = [auth('env_exact', desiredUrl)];
  else if (scenario === 'duplicate') envs = [auth('env_one', 'https://old-one.invalid'), auth('env_two', 'https://old-two.invalid')];
  else if (scenario === 'branch') envs = [auth('env_branch', 'https://auth-dev.rayer.idv.tw', ['production'], 'develop')];
  else envs = [auth('env_old', 'old-secret-value')];
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'env-reads'), '0');
  await writeFile(join(root, 'mutations'), '0');
  await writeFile(join(root, 'mutation-log'), '');
  await writeFile(join(root, 'curl-calls'), '');
  await writeFile(join(root, 'state.json'), JSON.stringify({ project: { id: projectId, name: 'llm-wiki-cloud', accountId: teamId }, envs }));
  return { root, bin, evidenceDir };
}

function providerEnv(fixture, scenario, overrides = {}) {
  return {
    ...process.env,
    PATH: fixture.bin + ':' + process.env.PATH,
    FIXTURE_ROOT: fixture.root,
    FIXTURE_SCENARIO: scenario,
    FAKE_HEAD_SHA: commitSha,
    FAKE_REMOTE_DEVELOP_SHA: commitSha,
    GITHUB_ACTIONS: 'true',
    GITHUB_REF: 'refs/heads/develop',
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-frontend',
    GITHUB_API_URL: 'https://api.github.com',
    GITHUB_TOKEN: 'github-sentinel-token',
    VERCEL_API_BASE_URL: 'https://api.vercel.com',
    VERCEL_TOKEN: 'vercel-sentinel-token',
    VERCEL_PROJECT_ID: projectId,
    VERCEL_TEAM_ID: teamId,
    VERCEL_SCOPE: 'rayer-tung-s-projects',
    COMMIT_SHA: commitSha,
    TICKET_REF: 'LWC-258',
    EVIDENCE_DIR: fixture.evidenceDir,
    ROLLBACK_ARTIFACT_NAME: 'vercel-production-auth-env-rollback-' + commitSha,
    ROLLBACK_ARTIFACT_ID: '123456789',
    ROLLBACK_ARTIFACT_URL: 'https://github.com/Rayer/llm-wiki-frontend/actions/runs/123/artifacts/123456789',
    ROLLBACK_ARTIFACT_DIGEST: 'sha256:' + 'a'.repeat(64),
    ...overrides,
  };
}

async function runProvider(fixture, scenario, mode, overrides = {}) {
  try {
    return await execFileAsync('bash', [scriptPath, mode], {
      cwd: repoRoot,
      env: providerEnv(fixture, scenario, overrides),
      maxBuffer: 1024 * 1024,
    });
  } catch (error) {
    return error;
  }
}

async function providerCase(scenario, overrides = {}) {
  const fixture = await setupProviderCase(scenario);
  const preflight = await runProvider(fixture, scenario, 'preflight', overrides);
  const mutate = preflight.code === undefined ? await runProvider(fixture, scenario, 'mutate', overrides) : null;
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-production-auth-env.json'), 'utf8'));
  const rollbackPath = join(fixture.evidenceDir, 'rollback-contract.json');
  const rollback = await readFile(rollbackPath, 'utf8').then(JSON.parse).catch(() => null);
  const state = JSON.parse(await readFile(join(fixture.root, 'state.json'), 'utf8'));
  const mutationLog = (await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim().split('\n').filter(Boolean);
  return { fixture, preflight, mutate, evidence, rollback, state, mutationLog };
}

test('workflow is exact-develop gated and rollback upload precedes mutation', async () => {
  const source = await readFile(workflowPath, 'utf8');
  const workflow = parseYaml(source);
  assert.deepEqual(Object.keys(workflow.on.workflow_dispatch.inputs), ['commit_sha', 'ticket_ref']);
  assert.equal(workflow.on.workflow_dispatch.inputs.commit_sha.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.ticket_ref.required, true);
  const job = workflow.jobs.configure;
  assert.equal(job.if, "github.ref == 'refs/heads/develop'");
  assert.equal(job.environment.name, 'Development');
  assert.deepEqual(job.permissions, { contents: 'read', actions: 'read' });
  assert.ok(!Object.values(job.env ?? {}).some((value) => /runner\./.test(value)), 'runner context must not be used in job-level env');
  const steps = job.steps;
  assert.deepEqual(steps.map((step) => step.name), [
    'Check out the exact requested SHA',
    'Validate exact develop SHA and canonical CI',
    'Preflight production auth environment and capture rollback',
    'Upload durable production auth rollback contract',
    'Apply and verify production auth environment',
    'Upload normalized production auth evidence',
  ]);
  assert.equal(steps[3].uses, 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02');
  assert.equal(steps[5].if, 'always()');
  assert.ok(steps.indexOf(steps[3]) < steps.indexOf(steps[4]));
  for (const step of [steps[2], steps[3], steps[4], steps[5]]) {
    assert.equal(step.env.EVIDENCE_DIR, '${{ runner.temp }}/vercel-production-auth-env');
  }
});

test('implemented guard retains the RED contract conditions', async () => {
  const source = await readFile(scriptPath, 'utf8');
  assert.match(source, /COMMIT_SHA.*\[0-9a-f\]\{40\}/s);
  assert.match(source, /ls-remote origin refs\/heads\/develop/);
  assert.match(source, /CI_NOT_GREEN/);
  assert.match(source, /rollback_and_fail/);
});

test('sensitivity guard fixes the production target and auth origin', async () => {
  const workflow = parseYaml(await readFile(workflowPath, 'utf8'));
  const source = await readFile(scriptPath, 'utf8');
  assert.equal(workflow.jobs.configure.environment.name, 'Development');
  assert.match(source, /DESIRED_VALUE="https:\/\/auth\.rayer\.idv\.tw"/);
  assert.match(source, /target:\["production"\]/);
  assert.doesNotMatch(source, /target:\["preview"\]|auth-dev\.rayer\.idv\.tw/);
});

test('production seam forbids deployments, aliases, refs, API URL changes, and alias workflow edits', async () => {
  const source = await readFile(scriptPath, 'utf8').catch(() => '');
  assert.doesNotMatch(source, /vercel\s+(deploy|alias)|git\s+(push|update-ref)|NEXT_PUBLIC_API_URL/i);
  const alias = await readFile(join(repoRoot, '.github/workflows/vercel-alias-promotion.yml'), 'utf8');
  const { stdout } = await execFileAsync('git', ['show', 'HEAD:.github/workflows/vercel-alias-promotion.yml'], { cwd: repoRoot });
  assert.equal(alias, stdout);
});

test('YAML parser and shell guard are part of the causal contract', async () => {
  const source = await readFile(workflowPath, 'utf8');
  assert.equal(parseYaml(source).name, 'Vercel Production Auth Environment Configuration');
  const result = await execFileAsync('bash', ['-n', scriptPath]).catch((error) => error);
  assert.equal(result.code ?? 0, 0, result.stderr);
});

test('provider preflight is read-only and captures the exact prior object', async () => {
  const run = await providerCase('old');
  assert.equal(run.preflight.code, undefined, run.preflight?.stderr);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.equal(run.evidence.prior_state.env_id, 'env_old');
  assert.equal(run.rollback.prior_state.env.value, 'old-secret-value');
  assert.equal(run.mutationLog.length, 1);
  assert.deepEqual(run.mutationLog, ['PATCH']);
});

test('provider preflight rejects stale SHA and non-success canonical CI', async () => {
  const stale = await providerCase('old', { FAKE_HEAD_SHA: 'a'.repeat(40) });
  assert.notEqual(stale.preflight.code, undefined);
  assert.equal(stale.evidence.reason_code, 'CHECKED_OUT_SHA_MISMATCH');
  const failedCi = await providerCase('ci-failure');
  assert.notEqual(failedCi.preflight.code, undefined);
  assert.equal(failedCi.evidence.reason_code, 'CI_NOT_GREEN');
  assert.deepEqual(failedCi.mutationLog, []);
});

test('exact singleton is idempotent with zero provider mutation', async () => {
  const run = await providerCase('exact');
  assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  assert.equal(run.evidence.decision, 'noop');
  assert.equal(run.evidence.provider_verification.mutation_count, 0);
  assert.deepEqual(run.mutationLog, []);
  assert.equal(run.evidence.provider_verification.singleton, true);
  assert.equal(run.evidence.provider_verification.value_equals_desired, true);
  assert.deepEqual(run.evidence.provider_verification.targets, ['production']);
});

test('absent state creates exactly one plaintext production-target entry', async () => {
  const run = await providerCase('absent');
  assert.equal(run.mutate.code, undefined, run.mutate?.stderr);
  assert.equal(run.evidence.decision, 'create');
  assert.equal(run.evidence.provider_verification.mutation_count, 1);
  assert.deepEqual(run.mutationLog, ['POST']);
  assert.deepEqual(run.state.envs.filter(({ key }) => key === 'NEXT_PUBLIC_AUTH_URL'), [{
    id: 'env_new', key: 'NEXT_PUBLIC_AUTH_URL', value: desiredUrl, type: 'plain', target: ['production'],
  }]);
  assert.equal(run.state.envs.find(({ key }) => key === 'NEXT_PUBLIC_API_URL').value, 'unrelated-secret');
});

test('duplicate and branch-scoped entries fail closed before mutation', async () => {
  for (const scenario of ['duplicate', 'branch']) {
    const run = await providerCase(scenario);
    assert.notEqual(run.preflight.code, undefined);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(run.evidence.provider_verification.mutation_count, 0);
    assert.deepEqual(run.mutationLog, []);
  }
});

test('read-back mismatch restores the previous object and independently reads it back', async () => {
  const run = await providerCase('readback-mismatch');
  assert.notEqual(run.mutate.code, undefined);
  assert.equal(run.evidence.status, 'FAILED');
  assert.equal(run.evidence.rollback.result, 'RESTORED');
  assert.deepEqual(run.mutationLog, ['PATCH', 'PATCH']);
  assert.equal(run.state.envs.find(({ id }) => id === 'env_old').value, 'old-secret-value');
  assert.equal(run.evidence.rollback.independent_readback, true);
});

test('mutation requires the durable rollback handoff and rejects invalid artifact evidence', async () => {
  const fixture = await setupProviderCase('old');
  const preflight = await runProvider(fixture, 'old', 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  const missing = await runProvider(fixture, 'old', 'mutate', { ROLLBACK_ARTIFACT_ID: '' });
  assert.notEqual(missing.code, undefined);
  assert.deepEqual((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
});

test('provider logs and normalized evidence do not expose token or unrelated values', async () => {
  const run = await providerCase('old');
  const evidenceText = JSON.stringify(run.evidence);
  assert.doesNotMatch(evidenceText, /old-secret-value|unrelated-secret|vercel-sentinel-token/);
  assert.doesNotMatch((await readFile(join(run.fixture.root, 'curl-calls'), 'utf8')), /vercel-sentinel-token|old-secret-value|unrelated-secret/);
  assert.equal(run.rollback.prior_state.env.value, 'old-secret-value');
});
