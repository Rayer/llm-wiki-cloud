import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { load as parseYaml } from 'js-yaml';

const execFileAsync = promisify(execFile);
const repoRoot = new URL('..', import.meta.url).pathname;
const commitSha = '0123456789abcdef0123456789abcdef01234567';
const deploymentId = 'dpl_test123';
const projectId = 'prj_test123';
const aliases = ['wiki.rayer.idv.tw', 'llm-wiki-frontend.vercel.app'];
const workflowPath = join(repoRoot, '.github/workflows/vercel-alias-promotion.yml');

async function setupCase(scenario = 'success') {
  const root = await mkdtemp(join(tmpdir(), 'lwc-199-'));
  const bin = join(root, 'bin');
  await mkdir(bin);
  await writeFile(join(root, 'scenario'), scenario);
  await writeFile(join(root, 'deployment.json'), JSON.stringify({
    id: deploymentId,
    url: 'https://dpl_test123.vercel.app',
    projectId,
    readyState: 'READY',
    target: 'production',
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
    ORIGINATING_WORKFLOW: 'vercel-alias-promotion.yml',
    ORIGINATING_WORKFLOW_RUN_ID: '123456789',
    ORIGINATING_WORKFLOW_RUN_ATTEMPT: '3',
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

function assertCompleteEvidenceSlots(evidence, expected = {}) {
  assert.deepEqual(evidence.provider.rollback.aliases.map(({ alias }) => alias), aliases);
  assert.deepEqual(evidence.provider.rollback.aliases.map((entry) => Object.keys(entry)), [
    ['alias', 'deployment_id'],
    ['alias', 'deployment_id'],
  ]);
  assert.deepEqual(evidence.observed.alias_routing.map(({ alias }) => alias), aliases);
  assert.deepEqual(evidence.observed.alias_routing.map((entry) => Object.keys(entry)), [
    ['alias', 'deployment_id'],
    ['alias', 'deployment_id'],
  ]);
  assert.deepEqual(evidence.health.map(({ alias }) => alias), aliases);
  assert.deepEqual(evidence.health.map((entry) => Object.keys(entry)), [
    ['alias', 'status_code', 'effective_host'],
    ['alias', 'status_code', 'effective_host'],
  ]);
  if (expected.rollback) {
    assert.deepEqual(evidence.provider.rollback.aliases.map(({ deployment_id }) => deployment_id), expected.rollback);
  }
  if (expected.observed) {
    assert.deepEqual(evidence.observed.alias_routing.map(({ deployment_id }) => deployment_id), expected.observed);
  }
  if (expected.health) {
    assert.deepEqual(evidence.health.map(({ status_code, effective_host }) => ({ status_code, effective_host })), expected.health);
  }
}

test('promotes exactly both canonical aliases to one deployment and writes normalized evidence', async () => {
  const run = await runCase();
  assert.equal(run.result.error, undefined, run.result.stderr);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.equal(run.evidence.schema_version, 1);
  assert.equal(run.evidence.project, 'llm-wiki-cloud');
  assert.equal(run.evidence.component, 'frontend');
  assert.equal(run.evidence.environment, 'production');
  assert.equal(run.evidence.action, 'promote');
  assert.deepEqual(run.evidence.source, {
    commit_sha: commitSha,
    ref: 'refs/heads/main',
  });
  assert.deepEqual(run.evidence.dev_provenance, {
    workflow: 'ci.yml',
    event: 'push',
    head_branch: 'main',
    head_sha: commitSha,
    conclusion: 'success',
    run_id: 987654321,
    run_url: 'https://github.test/Rayer/llm-wiki-frontend/actions/runs/987654321',
  });
  assert.deepEqual(run.evidence.provider.current, {
    deployment_id: deploymentId,
    deployment_url: 'https://dpl_test123.vercel.app',
  });
  assert.deepEqual(run.evidence.provider.rollback.aliases, [
    { alias: aliases[0], deployment_id: 'dpl_oldcustom' },
    { alias: aliases[1], deployment_id: 'dpl_oldvercel' },
  ]);
  assert.equal(run.evidence.provider.evidence_artifact_name, 'vercel-alias-promotion-evidence');
  assert.deepEqual(run.evidence.observed, {
    deployment_id: deploymentId,
    deployment_url: 'https://dpl_test123.vercel.app',
    source: 'github',
    ref: 'refs/heads/main',
    ready_state: 'READY',
    target: 'production',
    alias_routing: [
      { alias: aliases[0], deployment_id: deploymentId },
      { alias: aliases[1], deployment_id: deploymentId },
    ],
  });
  assert.equal(run.evidence.provider_verification.result, 'verified');
  assert.deepEqual(run.evidence.provider_verification.checks, [
    'deployment_ready',
    'deployment_target_production',
    'alias_routing_exact',
    'http_health_exact',
  ]);
  assert.match(run.evidence.provider_verification.checked_at, /^20[0-9]{2}-[0-9]{2}-[0-9]{2}T/);
  assert.deepEqual(run.evidence.originating_workflow, {
    repository: 'Rayer/llm-wiki-frontend',
    workflow: 'vercel-alias-promotion.yml',
    run_id: 123456789,
    run_attempt: 3,
  });
  assert.deepEqual(run.evidence.provider.rollback.aliases.map(({ alias }) => alias), aliases);
  assert.deepEqual(run.evidence.provider.rollback.aliases.map(({ deployment_id: id }) => id), ['dpl_oldcustom', 'dpl_oldvercel']);
  assert.deepEqual(run.evidence.observed.alias_routing.map(({ deployment_id: id }) => id), [deploymentId, deploymentId]);
  assert.deepEqual(run.calls.map((call) => call.split(' ').slice(0, 4)), [
    ['alias', 'set', deploymentId, aliases[0]],
    ['alias', 'set', deploymentId, aliases[1]],
  ]);
  assert.deepEqual(run.evidence.health, [
    { alias: aliases[0], status_code: '200', effective_host: aliases[0] },
    { alias: aliases[1], status_code: '200', effective_host: aliases[1] },
  ]);
  assert.equal(JSON.stringify(run.evidence).includes('effective_url'), false);
  assert.doesNotMatch(run.calls.join('\n'), /(^| )(?:--token|vercel-test-token)( |$)/);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v13/deployments/')).length, 2);
  assert.equal(run.curlCalls.filter((url) => url.includes('/actions/workflows/ci.yml/runs?')).length, 1);
  assert.equal(run.curlCalls.some((url) => url.includes('/repos/Rayer/llm-wiki-frontend/actions/runs?')), false);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases?')).length, 6);
  assert.ok(run.curlCalls.includes('https://' + aliases[0] + '/'));
  assert.ok(run.curlCalls.includes('https://' + aliases[1] + '/'));
  assert.doesNotMatch(JSON.stringify(run.evidence), /vercel-test-token|github-test-token/);
});

test('fails closed before mutation when deployment target is not production', async () => {
  const run = await runCase('target-mismatch');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /target/);
  assert.equal(run.calls.length, 0);
});

test('fails closed after mutation when deployment target is no longer production', async () => {
  const run = await runCase('post-target-mismatch');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.equal(run.evidence.provider_verification.result, 'not_verified');
  assert.equal(run.evidence.provider_verification.checked_at, null);
  assert.equal(run.calls.length, 2);
});

test('fails closed before mutation when Vercel repository metadata is missing', async () => {
  const run = await runCase('missing-repository');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /repository/);
  assert.equal(run.calls.length, 0);
});

test('fails closed before mutation when Vercel repository metadata is not exact', async () => {
  const run = await runCase('mismatched-repository');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /repository/);
  assert.equal(run.calls.length, 0);
});

test('fails closed before any provider call when GitHub repository identity is not exact', async () => {
  const run = await runCase('success', { GITHUB_REPOSITORY: 'rayer/llm-wiki-frontend' });
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /GITHUB_REPOSITORY/);
  assert.equal(run.calls.length, 0);
  assert.equal(run.curlCalls.length, 0);
  assertCompleteEvidenceSlots(run.evidence);
});

test('re-reads both aliases immediately before mutation and aborts on snapshot drift', async () => {
  const run = await runCase('alias-changed-before-mutation');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /rollback snapshot|changed/);
  assert.equal(run.calls.length, 0);
});

test('records actual parseable post-readback values instead of stale preflight claims', async () => {
  const run = await runCase('post-malformed');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.deepEqual(run.evidence.observed, {
    deployment_id: deploymentId,
    deployment_url: 'https://dpl_test123.vercel.app',
    source: null,
    ref: null,
    ready_state: null,
    target: null,
    alias_routing: [
      { alias: aliases[0], deployment_id: deploymentId },
      { alias: aliases[1], deployment_id: deploymentId },
    ],
  });
});

test('records unknown observed deployment values when post-readback is unreadable', async () => {
  const run = await runCase('post-unreadable');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.deepEqual(run.evidence.observed, {
    deployment_id: null,
    deployment_url: null,
    source: null,
    ref: null,
    ready_state: null,
    target: null,
    alias_routing: [
      { alias: aliases[0], deployment_id: deploymentId },
      { alias: aliases[1], deployment_id: deploymentId },
    ],
  });
});

test('fails closed when health follows a redirect to a different host', async () => {
  const run = await runCase('redirect-host-mismatch');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.deepEqual(run.evidence.health, [
    { alias: aliases[0], status_code: '200', effective_host: null },
    { alias: aliases[1], status_code: '200', effective_host: aliases[1] },
  ]);
  assert.equal(JSON.stringify(run.evidence).includes('effective_url'), false);
});

test('workflow contract parses as YAML and scopes provider secrets to the helper step', async () => {
  const workflowSource = await readFile(workflowPath, 'utf8');
  const workflow = parseYaml(workflowSource);
  assert.deepEqual(Object.keys(workflow.on.workflow_dispatch.inputs), ['commit_sha', 'deployment_id', 'ticket_ref']);
  assert.equal(workflow.on.workflow_dispatch.inputs.commit_sha.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.commit_sha.type, 'string');
  assert.equal(workflow.on.workflow_dispatch.inputs.deployment_id.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.deployment_id.type, 'string');
  assert.equal(workflow.on.workflow_dispatch.inputs.ticket_ref.required, false);
  assert.equal(workflow.on.workflow_dispatch.inputs.ticket_ref.default, '');
  assert.equal(workflow.permissions.contents, 'read');
  assert.equal(workflow.permissions.actions, 'read');
  assert.equal(workflow.concurrency['cancel-in-progress'], false);
  assert.equal(workflow.jobs.promote.environment.name, 'Production');
  assert.equal(workflow.jobs.promote['timeout-minutes'], 30);
  assert.equal(workflow.jobs.promote.env.VERCEL_TOKEN, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_PROJECT_ID, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_TEAM_ID, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_SCOPE, undefined);
  const checkout = workflow.jobs.promote.steps.find(({ name }) => name === 'Check out workflow-owned helper');
  assert.equal(checkout.uses, 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262');
  assert.equal(checkout.with['persist-credentials'], false);
  const helper = workflow.jobs.promote.steps.find(({ name }) => name === 'Validate and promote exact deployment');
  assert.deepEqual(helper.env, {
    VERCEL_TOKEN: '${{ secrets.VERCEL_TOKEN }}',
    VERCEL_PROJECT_ID: '${{ secrets.VERCEL_PROJECT_ID }}',
    VERCEL_TEAM_ID: '${{ secrets.VERCEL_TEAM_ID }}',
    VERCEL_SCOPE: '${{ secrets.VERCEL_SCOPE }}',
  });
  const runs = workflow.jobs.promote.steps.filter(({ run }) => typeof run === 'string').map(({ run }) => run);
  assert.ok(runs.some((run) => run.includes('npm install --global vercel@52.0.0 --ignore-scripts')));
  assert.ok(runs.some((run) => run.trim() === 'bash .github/scripts/vercel-alias-promotion.sh'));
  assert.equal(runs.some((run) => /vercel\s+(deploy|build)|next\s+build/.test(run)), false);
  const upload = workflow.jobs.promote.steps.find(({ name }) => name === 'Upload normalized deployment and rollback evidence');
  assert.equal(upload.uses, 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02');
  assert.equal(upload.with.name, 'vercel-alias-promotion-evidence');
  assert.equal(upload.with.path, '${{ runner.temp }}/vercel-alias-promotion/vercel-alias-promotion.json');
  await execFileAsync('bash', ['-n', '.github/scripts/vercel-alias-promotion.sh']);
  await execFileAsync('bash', ['-n', '-c', runs.join('\n')]);
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
  assert.match(run.evidence.next_action, /Read \/v4\/aliases before retry/);
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
  assert.equal(run.evidence.provider_verification.checked_at, null);
  assert.ok(run.evidence.health.some(({ status_code }) => status_code === '503'));
  assertCompleteEvidenceSlots(run.evidence);
  assert.ok(run.curlCalls.includes('https://' + aliases[1] + '/'));
});

for (const scenario of [
  ['preflight failure', 'success', { COMMIT_SHA: 'not-a-sha' }, [null, null], [null, null], [
    { status_code: null, effective_host: null },
    { status_code: null, effective_host: null },
  ]],
  ['provider read failure', 'deployment-read-failure', {}, [null, null], [null, null], [
    { status_code: null, effective_host: null },
    { status_code: null, effective_host: null },
  ]],
  ['first alias read failure', 'alias-read-failure', {}, [null, null], [null, null], [
    { status_code: null, effective_host: null },
    { status_code: null, effective_host: null },
  ]],
  ['partial post read-back', 'partial-readback', {}, ['dpl_oldcustom', 'dpl_oldvercel'], [deploymentId, null], [
    { status_code: null, effective_host: null },
    { status_code: null, effective_host: null },
  ]],
  ['first health failure', 'health-failure', {}, ['dpl_oldcustom', 'dpl_oldvercel'], [deploymentId, deploymentId], [
    { status_code: '503', effective_host: aliases[0] },
    { status_code: '200', effective_host: aliases[1] },
  ]],
]) {
  test('normalizes both canonical evidence slots for ' + scenario[0], async () => {
    const run = await runCase(scenario[1], scenario[2]);
    assert.notEqual(run.result.code, 0);
    assertCompleteEvidenceSlots(run.evidence, {
      rollback: scenario[3],
      observed: scenario[4],
      health: scenario[5],
    });
  });
}
