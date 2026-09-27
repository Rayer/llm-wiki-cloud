import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('..', import.meta.url).pathname;
const monorepoRoot = new URL('../../../', import.meta.url).pathname;
const commitSha = '0123456789abcdef0123456789abcdef01234567';
const deploymentId = 'dpl_devready';
const projectId = 'prj_dev123';
const teamId = 'team_dev123';
const rollbackArtifactDigestBare = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
const stableDomain = 'wiki.dev.rayer.idv.tw';

async function setupCase(scenario = 'authority-conflict') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-253-'));
  const bin = join(root, 'bin');
  const evidenceDir = join(root, 'evidence');
  await mkdir(bin);
  await mkdir(evidenceDir);
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'project-reads'), '0');
  await writeFile(join(root, 'mutation-log'), '');
  await writeFile(join(root, 'deployment-post-log'), '');
  await writeFile(join(root, 'curl-calls'), '');
  await writeFile(join(root, 'deployment.json'), JSON.stringify({
    id: deploymentId,
    url: 'https://dpl_dev_ready.vercel.app',
    projectId,
    teamId,
    ownerId: teamId,
    readyState: 'READY',
    target: null,
    meta: {
      githubDeployment: '1',
      githubOrg: 'Rayer',
      githubRepo: 'llm-wiki-cloud',
      githubCommitRef: 'develop',
      githubCommitSha: commitSha,
    },
  }));
  await writeFile(join(root, 'aliases.json'), JSON.stringify({
    [stableDomain]: 'dpl_devold',
  }));
  await writeFile(join(root, 'project.json'), JSON.stringify({
    id: projectId,
    name: 'llm-wiki-frontend-dev',
    accountId: teamId,
    rootDirectory: ['root-null', 'root-dot'].includes(scenario) ? (scenario === 'root-null' ? null : '.') : (scenario === 'root-wrong' ? 'apps/bff' : 'apps/frontend'),
    ...(scenario === 'linked-deployment-missing' ? { link: { repoId: 12345 } } : {}),
  }));
  await writeFile(join(root, 'ci.json'), JSON.stringify({
    workflow_runs: [{
      path: '.github/workflows/ci.yml',
      head_branch: 'develop',
      head_sha: commitSha,
      event: 'push',
      status: 'completed',
      conclusion: 'success',
      id: 987654321,
      html_url: 'https://github.test/Rayer/llm-wiki-cloud/actions/runs/987654321',
    }],
  }));
  await execFileAsync('cp', [
    join(repoRoot, 'tests/fixtures/lwc-253-fake-curl.sh'),
    join(bin, 'curl'),
  ]);
  await execFileAsync('cp', [
    join(repoRoot, 'tests/fixtures/lwc-253-fake-vercel.sh'),
    join(bin, 'vercel'),
  ]);
  await execFileAsync('chmod', ['+x', join(bin, 'curl'), join(bin, 'vercel')]);
  return { root, bin, evidenceDir };
}

async function runScript(mode, env) {
  try {
    return await execFileAsync('bash', ['.github/scripts/vercel-dev-deployment.sh', mode], {
      cwd: repoRoot,
      env,
      maxBuffer: 1024 * 1024,
    });
  } catch (error) {
    return error;
  }
}

function buildEnv(fixture, overrides = {}) {
  const env = { ...process.env };
  delete env.GITHUB_ACTIONS;
  delete env.CI;
  return {
    ...env,
    PATH: fixture.bin + ':' + process.env.PATH,
    FIXTURE_ROOT: fixture.root,
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-cloud',
    GITHUB_TOKEN: 'github-sentinel-token',
    VERCEL_TOKEN: 'vercel-sentinel-token',
    VERCEL_API_BASE_URL: 'https://vercel.test',
    GITHUB_API_URL: 'https://github.test',
    VERCEL_PROJECT_ID: projectId,
    VERCEL_TEAM_ID: teamId,
    VERCEL_SCOPE: 'rayer-tung-s-projects',
    COMMIT_SHA: commitSha,
    DEPLOYMENT_ID: deploymentId,
    CURRENT_HEAD_SHA: commitSha,
    CURRENT_REMOTE_DEVELOP_SHA: commitSha,
    EVIDENCE_DIR: fixture.evidenceDir,
    STABLE_DOMAIN: stableDomain,
    LWC253_TEST_MODE: '1',
    VERCEL_POLL_ATTEMPTS: '2',
    VERCEL_POLL_INTERVAL_SECONDS: '0',
    ROLLBACK_ARTIFACT_ID: '123456789',
    ROLLBACK_ARTIFACT_URL: 'https://github.com/Rayer/llm-wiki-cloud/actions/runs/123456789/artifacts/123456789',
    ROLLBACK_ARTIFACT_DIGEST: rollbackArtifactDigestBare,
    ...overrides,
  };
}

async function runCase(scenario = 'success', overrides = {}) {
  const fixture = await setupCase(scenario);
  const env = buildEnv(fixture, overrides);
  const preflight = await runScript('preflight', env);
  const preflightMutationLog = (await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim().split('\n').filter(Boolean);
  const preflightDeploymentPostLog = (await readFile(join(fixture.root, 'deployment-post-log'), 'utf8')).trim().split('\n').filter(Boolean);
  const preflightEvidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
  let result = preflight;
  if (preflight.code === undefined) result = await runScript('promote', env);
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
  const mutationLog = (await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim().split('\n').filter(Boolean);
  const deploymentPostLog = (await readFile(join(fixture.root, 'deployment-post-log'), 'utf8')).trim().split('\n').filter(Boolean);
  const curlCalls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  return { fixture, env, preflight, result, evidence, preflightEvidence, mutationLog, deploymentPostLog, curlCalls, preflightMutationLog, preflightDeploymentPostLog };
}

async function readContext(fixture) {
  return JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment-context.json'), 'utf8'));
}

async function readRollbackContract(fixture) {
  return JSON.parse(await readFile(join(fixture.evidenceDir, 'rollback-contract.json'), 'utf8'));
}

test('fails closed on contradictory DEV alias authority before mutation', async () => {
  const fixture = await setupCase();
  const env = buildEnv(fixture);
  const result = await runScript('preflight', env);

  assert.equal(result.code, 1, result.stderr);
  assert.equal((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
  assert.equal(evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(evidence.reason_code, 'ALIAS_AUTHORITY_CONFLICT');
});

test('root directory contract fails closed before provider mutation', async () => {
  for (const scenario of ['root-null', 'root-dot', 'root-wrong']) {
    const fixture = await setupCase(scenario);
    const result = await runScript('preflight', buildEnv(fixture));
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /rootDirectory|root directory/i);
    assert.equal((await readFile(join(fixture.root, 'deployment-post-log'), 'utf8')).trim(), '');
  }
});

test('rejects the retired Vercel scope slug', async () => {
  const fixture = await setupCase('success');
  const result = await runScript('preflight', buildEnv(fixture, { VERCEL_SCOPE: 'rayer-team' }));

  assert.equal(result.code, 1, result.stderr);
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
  assert.equal(evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(evidence.reason_code, 'TEAM_NOT_ALLOWLISTED');
  assert.equal((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
});

test('uses bounded project-scoped alias inventory without the unsupported domain filter', async () => {
  const run = await runCase('success');
  assert.equal(run.result.code, undefined, run.result?.stderr);
  assert.equal(run.curlCalls.filter((url) => url.includes('/domains')).length, 0);
  const aliasRead = run.curlCalls.find((url) => url.includes('/v4/aliases/'));
  const inventoryReads = run.curlCalls.filter((url) => url.includes('/v4/aliases?'));
  assert.match(aliasRead, /\/v4\/aliases\/wiki\.dev\.rayer\.idv\.tw\?teamId=team_dev123$/);
  assert.equal(inventoryReads.length, 3);
  for (const url of inventoryReads) {
    assert.match(url, /\/v4\/aliases\?projectId=prj_dev123&teamId=team_dev123&limit=100$/);
    assert.doesNotMatch(url, /(?:^|[?&])domain=/);
  }
});

for (const [scenario, overrides, reasonCode] of [
  ['ci-failure', {}, 'CI_NOT_GREEN'],
  ['ci-wrong-sha', {}, 'CI_NOT_GREEN'],
  ['success', { COMMIT_SHA: 'not-a-sha' }, 'INPUT_SHA_INVALID'],
  ['success', { CURRENT_HEAD_SHA: 'fedcba9876543210fedcba9876543210fedcba98' }, 'CHECKED_OUT_SHA_MISMATCH'],
  ['success', { CURRENT_REMOTE_DEVELOP_SHA: 'fedcba9876543210fedcba9876543210fedcba98' }, 'REMOTE_DEVELOP_SHA_MISMATCH'],
  ['project-mismatch', {}, 'PROJECT_METADATA_MISMATCH'],
  ['team-mismatch', {}, 'PROJECT_METADATA_MISMATCH'],
  ['alias-absent', {}, 'ALIAS_AUTHORITY_CONFLICT'],
  ['alias-divergent', {}, 'ALIAS_AUTHORITY_CONFLICT'],
  ['alias-project-mismatch', {}, 'ALIAS_AUTHORITY_CONFLICT'],
  ['rollback-freeze-failure', {}, 'ALIAS_READ_FAILED'],
]) {
  test(`fails closed before mutation for ${scenario}`, async () => {
    const run = await runCase(scenario, overrides);
    assert.equal(run.result.code, 1, run.result.stderr);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(run.evidence.reason_code, reasonCode);
    assert.equal(run.mutationLog.length, 0);
  });
}

test('read-only preflight records a typed create-needed decision without provider mutation', async () => {
  const run = await runCase('deployment-missing');
  assert.equal(run.result.code, undefined, run.result?.stderr);
  const context = await readContext(run.fixture);
  const contract = await readRollbackContract(run.fixture);
  assert.equal(context.target.decision, 'deployment_needed');
  assert.equal(context.target.deployment_id, null);
  assert.equal(context.source.commit_sha, commitSha);
  assert.equal(context.source.ref, 'refs/heads/develop');
  assert.equal(context.source.repository, 'Rayer/llm-wiki-cloud');
  assert.equal(context.frozen_authority.repository_id, 12345);
  assert.equal(context.frozen_authority.deployment_id, 'dpl_devold');
  assert.deepEqual(context.frozen_authority.project, {
    id: projectId,
    name: 'llm-wiki-frontend-dev',
    team_id: teamId,
    root_directory: 'apps/frontend',
    repository_id: 12345,
    git_link: { state: 'unlinked', repo_id: null },
  });
  assert.equal(context.mutation_count, 0);
  assert.equal(contract.rollback.project_id, projectId);
  assert.equal(contract.rollback.team_id, teamId);
  assert.equal(contract.rollback.alias, stableDomain);
  assert.equal(contract.rollback.deployment_id, 'dpl_devold');
  assert.equal(run.preflight.code, undefined);
  assert.equal(run.preflight.stdout.trim(), 'PREFLIGHT_READY');
  assert.equal(run.preflightMutationLog.length, 0);
  assert.equal(run.preflightDeploymentPostLog.length, 0);
  assert.equal(run.preflightEvidence.status, 'PREFLIGHT_READY');
  assert.equal(run.preflightEvidence.provider_verification.mutation_count, 0);
  assert.equal(run.curlCalls.filter((url) => url === 'https://github.test/repos/Rayer/llm-wiki-cloud').length, 1);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.equal(run.evidence.provider_verification.mutation_count, 2);
  assert.equal(run.deploymentPostLog.length, 1);
  assert.equal(run.mutationLog.length, 1);
  const deploymentPayload = JSON.parse(run.deploymentPostLog.find((entry) => entry.startsWith('{')));
  assert.equal(deploymentPayload.gitSource.repoId, 12345);
  assert.equal(deploymentPayload.target, undefined);
  assert.equal(run.evidence.deployment.target, 'preview');
  assert.ok(run.evidence.provider_verification.checks.includes('deployment_create_attempted'));
  assert.ok(run.evidence.provider_verification.checks.includes('alias_mutation_attempted'));
  assert.match(run.mutationLog[0], /^alias set dpl_devready wiki\.dev\.rayer\.idv\.tw/);
});

test('rejects a wrong-linked repository before any mutation', async () => {
  const run = await runCase('wrong-linked-repo');
  assert.equal(run.result.code, 1, run.result.stderr);
  assert.equal(run.evidence.reason_code, 'PROJECT_METADATA_MISMATCH');
  assert.equal(run.mutationLog.length, 0);
  assert.equal(run.deploymentPostLog.length, 0);
});

test('uses the frozen exact repository ID for a linked project deployment', async () => {
  const run = await runCase('linked-deployment-missing');
  assert.equal(run.result.code, undefined, run.result?.stderr);
  const context = await readContext(run.fixture);
  assert.deepEqual(context.frozen_authority.project.git_link, { state: 'linked', repo_id: '12345' });
  assert.equal(context.frozen_authority.project.repository_id, 12345);
  assert.equal(JSON.parse(run.deploymentPostLog[0]).gitSource.repoId, 12345);
});

for (const scenario of ['root-drift', 'link-drift']) {
  test(`rechecks project authority before mutation for ${scenario}`, async () => {
    const run = await runCase(scenario);
    assert.equal(run.preflight.code, undefined, run.preflight.stderr);
    assert.equal(run.result.code, 1);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(run.evidence.reason_code, 'PROJECT_AUTHORITY_DRIFT');
    assert.equal(run.deploymentPostLog.length, 0);
    assert.equal(run.mutationLog.length, 0);
  });
}

test('creates through historical deployments and counts deployment plus alias mutations', async () => {
  const run = await runCase('historical-deployment');
  assert.equal(run.preflightDeploymentPostLog.length, 0);
  assert.equal(run.preflightEvidence.provider_verification.mutation_count, 0);
  assert.equal(run.result.code, undefined, run.result?.stderr);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.equal(run.deploymentPostLog.length, 1);
  assert.equal(run.curlCalls.filter((url) => url === 'https://vercel.test/v13/deployments?teamId=team_dev123').length, 1);
  assert.equal(run.evidence.provider_verification.mutation_count, 2);
  assert.ok(run.evidence.provider_verification.checks.includes('deployment_created'));
  assert.equal(run.mutationLog.length, 1);
});

test('promote without durable artifact handoff performs no provider mutation', async () => {
  const fixture = await setupCase('deployment-missing');
  const env = buildEnv(fixture, { ROLLBACK_ARTIFACT_ID: '' });
  const preflight = await runScript('preflight', env);
  const result = await runScript('promote', env);
  assert.equal(preflight.code, undefined, preflight.stderr);
  assert.equal(result.code, 1, result.stderr);
  assert.equal((await readFile(join(fixture.root, 'deployment-post-log'), 'utf8')).trim(), '');
  assert.equal((await readFile(join(fixture.root, 'mutation-log'), 'utf8')).trim(), '');
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
  assert.equal(evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(evidence.reason_code, 'ROLLBACK_ARTIFACT_INVALID');
  assert.equal(evidence.provider_verification.mutation_count, 0);
});

for (const [label, digest] of [
  ['prefixed-sha256', `sha256:${rollbackArtifactDigestBare}`],
  ['uppercase', rollbackArtifactDigestBare.toUpperCase()],
  ['too-short-63', rollbackArtifactDigestBare.substring(0, 63)],
  ['too-long-65', `${rollbackArtifactDigestBare}0`],
  ['empty', ''],
]) {
  test(`rejects invalid durable artifact digest ${label} before mutation`, async () => {
    const run = await runCase('success', { ROLLBACK_ARTIFACT_DIGEST: digest });
    const evidence = JSON.parse(await readFile(join(run.fixture.evidenceDir, 'vercel-dev-deployment.json'), 'utf8'));
    assert.equal(run.result.code, 1, run.result?.stderr);
    assert.equal(evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(evidence.reason_code, 'ROLLBACK_ARTIFACT_INVALID');
    assert.equal(run.mutationLog.length, 0);
    assert.equal(run.deploymentPostLog.length, 0);
  });
}

test('page-one and page-two uid deployments are normalized and reused without deployment creation', async () => {
  const run = await runCase('page-2-exact');
  assert.equal(run.result.code, undefined, run.result?.stderr);
  assert.equal(run.deploymentPostLog.length, 0);
  assert.equal(run.mutationLog.length, 1);
  assert.equal(run.evidence.deployment.id, deploymentId);
  assert.equal(run.evidence.provider_verification.mutation_count, 1);
  assert.ok(run.curlCalls.some((url) => url.includes('/v6/deployments?') && url.includes('until=cursor-2')));
  const deploymentCalls = run.curlCalls.filter((url) => url.includes('/v6/deployments?'));
  assert.equal(deploymentCalls.length, 2);
  assert.ok(!deploymentCalls[0].includes('until='));
  assert.ok(deploymentCalls[1].includes('until=cursor-2'));
});

test('existing candidate source failure remains zero-mutation blocked', async () => {
  const run = await runCase('existing-source-mismatch');
  assert.equal(run.result.code, 1);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(run.evidence.reason_code, 'DEPLOYMENT_SOURCE_MISMATCH');
  assert.equal(run.evidence.provider_verification.mutation_count, 0);
  assert.equal(run.deploymentPostLog.length, 0);
  assert.equal(run.mutationLog.length, 0);
});

test('reconciles a partial mutation and records the uncertain state without retrying', async () => {
  const run = await runCase('partial-mutation');
  assert.equal(run.result.code, 1);
  assert.equal(run.evidence.status, 'PARTIAL_MUTATION');
  assert.equal(run.evidence.reason_code, 'MUTATION_UNCERTAIN');
  assert.equal(run.evidence.observed_alias.deployment_id, deploymentId);
  assert.equal(run.mutationLog.length, 1);
});

test('refuses to mutate without a durable rollback artifact handoff', async () => {
  const run = await runCase('success', { ROLLBACK_ARTIFACT_ID: '' });
  assert.equal(run.result.code, 1);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.equal(run.evidence.reason_code, 'ROLLBACK_ARTIFACT_INVALID');
  assert.equal(run.mutationLog.length, 0);
});

test('fails closed when post-mutation alias or deployment read-back diverges', async () => {
  const run = await runCase('post-read-mismatch');
  assert.equal(run.result.code, 1);
  assert.equal(run.evidence.status, 'PARTIAL_MUTATION');
  assert.equal(run.evidence.reason_code, 'POSTCHECK_MISMATCH');
  assert.match(run.evidence.next_action, /reconcile/i);
  assert.equal(run.mutationLog.length, 1);
});

for (const [scenario, reasonCode] of [
  ['create-uncertain', 'DEPLOYMENT_CREATE_UNCERTAIN'],
  ['create-poll-timeout', 'DEPLOYMENT_POLL_TIMEOUT'],
  ['create-source-mismatch', 'DEPLOYMENT_SOURCE_MISMATCH'],
  ['create-read-failure', 'DEPLOYMENT_INSPECT_FAILED'],
]) {
  test(`classifies ${scenario} as partial mutation and preserves rollback identity`, async () => {
    const run = await runCase(scenario);
    assert.equal(run.result.code, 1, run.result.stderr);
    assert.equal(run.evidence.status, 'PARTIAL_MUTATION');
    assert.equal(run.evidence.reason_code, reasonCode);
    assert.equal(run.evidence.provider_verification.mutation_count, 1);
    assert.equal(run.evidence.rollback.deployment_id, 'dpl_devold');
    assert.equal(run.deploymentPostLog.length, 1);
    assert.equal(run.mutationLog.length, 0);
  });
}

test('DEV workflow invokes the shared CD with fixed authority', async () => {
  const workflow = (await readFile(join(monorepoRoot, '.github/workflows/deploy-dev.yml'), 'utf8'));
  const source = workflow.replace(/\$\{\{[\s\S]*?\}\}/g, 'VALUE');
  assert.doesNotMatch(source, /\n  push:/);
  assert.match(source, /workflow_dispatch:/);
  assert.match(source, /uses: \.\/\.github\/workflows\/cd\.yml/);
  assert.match(source, /environment: Development/);
  assert.match(source, /config_environment: development/);
  assert.match(source, /source_ref: develop/);
  assert.match(source, /config_path: deploy\/environments\/development\.yaml/);
  assert.deepEqual(Object.keys((await import('js-yaml')).load(workflow).on.workflow_dispatch.inputs), ['components']);
});
