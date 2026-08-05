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
const rollbackArtifactDigestBare = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
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
  await writeFile(join(root, 'auth-events'), '');
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

async function runScript(mode, env, scriptPath = '.github/scripts/vercel-alias-promotion.sh') {
  try {
    return await execFileAsync('bash', [scriptPath, mode], {
      cwd: repoRoot,
      env,
      maxBuffer: 1024 * 1024,
    });
  } catch (error) {
    return error;
  }
}

function buildEnv(fixture, overrides = {}) {
  const fixtureEnv = { ...process.env };
  // Fixture subprocesses simulate the promotion helper outside GitHub Actions.
  // Runner control variables must be opt-in per test, not inherited implicitly.
  delete fixtureEnv.GITHUB_ACTIONS;
  delete fixtureEnv.CI;

  return {
    ...fixtureEnv,
    PATH: fixture.bin + ':' + process.env.PATH,
    FIXTURE_ROOT: fixture.root,
    COMMIT_SHA: commitSha,
    DEPLOYMENT_ID: deploymentId,
    TICKET_REF: 'LWC-199',
    GITHUB_REPOSITORY: 'Rayer/llm-wiki-frontend',
    ORIGINATING_WORKFLOW: 'vercel-alias-promotion.yml',
    ORIGINATING_WORKFLOW_RUN_ID: '123456789',
    ORIGINATING_WORKFLOW_RUN_ATTEMPT: '3',
    LWC199_TEST_MODE: '1',
    GITHUB_API_URL: 'https://github.test/api/v3',
    GITHUB_TOKEN: 'github-sentinel-token-7f31',
    VERCEL_API_BASE_URL: 'https://vercel.test',
    VERCEL_TOKEN: 'vercel-sentinel-token-a2c9',
    ROLLBACK_ARTIFACT_ID: '123456789',
    ROLLBACK_ARTIFACT_URL: 'https://github.com/Rayer/llm-wiki-frontend/actions/runs/123456789/artifacts/123456789',
    ROLLBACK_ARTIFACT_DIGEST: rollbackArtifactDigestBare,
    VERCEL_PROJECT_ID: projectId,
    VERCEL_TEAM_ID: 'team_test123',
    VERCEL_SCOPE: 'rayer-team',
    EVIDENCE_DIR: fixture.evidenceDir,
    PROMOTION_CONTEXT_PATH: join(fixture.evidenceDir, 'vercel-alias-promotion-context.json'),
    ...overrides,
  };
}

async function readOptionalJson(path) {
  try {
    return JSON.parse(await readFile(path, 'utf8'));
  } catch (error) {
    if (error.code === 'ENOENT') return null;
    throw error;
  }
}

function capturedOutput(...results) {
  return results.flatMap((result) => [result?.stdout ?? '', result?.stderr ?? '']).join('\n');
}

async function runCase(scenario = 'success', overrides = {}, mode = 'promote') {
  const fixture = await setupCase(scenario);
  const env = buildEnv(fixture, overrides);
  const preflight = await runScript('preflight', env);
  let result = preflight;
  if (mode === 'promote' && preflight.code === undefined) {
    result = await runScript('promote', env);
  }
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-alias-promotion.json'), 'utf8'));
  const rollbackContract = await readOptionalJson(join(fixture.evidenceDir, 'rollback-contract.json'));
  const resumeContext = await readOptionalJson(join(fixture.evidenceDir, 'vercel-alias-promotion-context.json'));
  const calls = (await readFile(join(fixture.root, 'vercel-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const curlCalls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const authEvents = (await readFile(join(fixture.root, 'auth-events'), 'utf8')).trim().split('\n').filter(Boolean);
  return {
    ...fixture,
    env,
    result,
    preflight,
    evidence,
    rollbackContract,
    resumeContext,
    calls,
    curlCalls,
    authEvents,
    stdout: capturedOutput(preflight, result),
    stderr: [preflight?.stderr ?? '', result?.stderr ?? ''].join('\n'),
  };
}

async function runAuthVariantCase(variant) {
  const fixture = await setupCase();
  const source = await readFile(join(repoRoot, '.github/scripts/vercel-alias-promotion.sh'), 'utf8');
  const vercelAuthLine = '--header "Authorization: Bearer $VERCEL_TOKEN"';
  const githubAuthLine = '--header "Authorization: Bearer $GITHUB_TOKEN"';
  let variantSource = source;
  if (variant === 'wrong') {
    variantSource = source.replace(vercelAuthLine, '--header "Authorization: Bearer wrong-header"');
  } else if (variant === 'swapped') {
    const marker = '--header "Authorization: Bearer __SWAPPED_AUTH__"';
    variantSource = source
      .replace(vercelAuthLine, marker)
      .replace(githubAuthLine, vercelAuthLine)
      .replace(marker, githubAuthLine);
  } else if (variant === 'literal') {
    const placeholderLine = ['--header "Authorization: ', 'Bearer ', '***"'].join('');
    variantSource = source.replace(vercelAuthLine, placeholderLine);
  }
  const scriptPath = join(fixture.root, `vercel-alias-promotion-${variant}.sh`);
  await writeFile(scriptPath, variantSource);
  await execFileAsync('chmod', ['+x', scriptPath]);
  const env = buildEnv(fixture);
  const result = await runScript('preflight', env, scriptPath);
  const evidence = JSON.parse(await readFile(join(fixture.evidenceDir, 'vercel-alias-promotion.json'), 'utf8'));
  const rollbackContract = await readOptionalJson(join(fixture.evidenceDir, 'rollback-contract.json'));
  const resumeContext = await readOptionalJson(join(fixture.evidenceDir, 'vercel-alias-promotion-context.json'));
  const calls = (await readFile(join(fixture.root, 'vercel-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const curlCalls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const authEvents = (await readFile(join(fixture.root, 'auth-events'), 'utf8')).trim().split('\n').filter(Boolean);
  return {
    ...fixture,
    env,
    result,
    evidence,
    rollbackContract,
    resumeContext,
    calls,
    curlCalls,
    authEvents,
    stdout: capturedOutput(result),
    stderr: result?.stderr ?? '',
  };
}

function assertNoCredentialLeak(run) {
  const protectedText = [
    JSON.stringify(run.evidence),
    JSON.stringify(run.rollbackContract),
    JSON.stringify(run.resumeContext),
    run.stdout,
    run.stderr,
    run.curlCalls?.join('\n'),
    run.authEvents?.join('\n'),
    run.calls?.join('\n'),
  ].join('\n');
  for (const token of [run.env.GITHUB_TOKEN, run.env.VERCEL_TOKEN]) {
    assert.equal(protectedText.includes(token), false, `credential leaked: ${token}`);
    assert.equal(protectedText.includes(`Bearer ${token}`), false);
  }
  assert.doesNotMatch(protectedText, /Bearer\s+\S+/);
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
  assert.equal(run.evidence.provider.evidence_artifact_name, `vercel-alias-promotion-evidence-${commitSha}`);
  assert.equal(run.evidence.provider.rollback.artifact_name, `vercel-alias-rollback-${commitSha}`);
  assert.equal(run.evidence.provider.rollback.artifact_id, 123456789);
  assert.equal(run.evidence.provider.rollback.artifact_url, run.env.ROLLBACK_ARTIFACT_URL);
  assert.equal(run.evidence.provider.rollback.artifact_digest, `sha256:${rollbackArtifactDigestBare}`);
  assert.match(run.evidence.provider.rollback.contract_sha256, /^[0-9a-f]{64}$/);
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
  assert.equal(run.curlCalls.filter((url) => url.includes('/v13/deployments/')).length, 2);
  assert.equal(run.curlCalls.filter((url) => url.includes('/actions/workflows/ci.yml/runs?')).length, 1);
  assert.equal(run.curlCalls.some((url) => url.includes('/repos/Rayer/llm-wiki-frontend/actions/runs?')), false);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases/')).length, 14);
  assert.ok(run.curlCalls.includes('https://' + aliases[0] + '/'));
  assert.ok(run.curlCalls.includes('https://' + aliases[1] + '/'));
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases?')).length, 0);
  assert.ok(run.rollbackContract);
  assert.equal(run.resumeContext.phase, 'preflight-complete');
  assert.ok(run.authEvents.every((event) => /^AUTH_VALID provider=(github|vercel) endpoint=/.test(event)));
  assertNoCredentialLeak(run);
});

test('uses exact single-alias endpoint and succeeds on single-endpoint-only provider contract', async () => {
  const run = await runCase('single-alias-only');
  assert.equal(run.result.error, undefined, run.result.stderr);
  assert.equal(run.evidence.status, 'SUCCESS');
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases/')).length, 14);
  assert.equal(run.curlCalls.filter((url) => url.includes('/v4/aliases?')).length, 0);
});

test('provider-specific auth propagation rejects wrong, swapped, and placeholder headers before mutation', async () => {
  const scriptSource = await readFile(join(repoRoot, '.github/scripts/vercel-alias-promotion.sh'), 'utf8');
  assert.match(scriptSource, /--header "Authorization: Bearer \$VERCEL_TOKEN"/);
  assert.match(scriptSource, /--header "Authorization: Bearer \$GITHUB_TOKEN"/);
  assert.doesNotMatch(scriptSource, /Bearer \*\*\*/);
  const correct = await runCase();
  assert.equal(correct.result.error, undefined, correct.result.stderr);
  assert.equal(correct.calls.length, 2);
  assert.ok(correct.authEvents.some((event) => event.startsWith('AUTH_VALID provider=github endpoint=')));
  assert.ok(correct.authEvents.some((event) => event.startsWith('AUTH_VALID provider=vercel endpoint=')));
  assertNoCredentialLeak(correct);

  for (const variant of ['wrong', 'swapped', 'literal']) {
    const run = await runAuthVariantCase(variant);
    assert.notEqual(run.result.code, 0, `${variant} auth unexpectedly passed`);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.equal(run.calls.length, 0, `${variant} auth reached mutation`);
    assert.ok(run.authEvents.some((event) => event.startsWith('AUTH_INVALID provider=')), `${variant} auth was not checked`);
    assertNoCredentialLeak(run);
  }
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

test('preflight freezes both aliases without mutating or fabricating final success', async () => {
  const run = await runCase('success', {}, 'preflight');
  assert.equal(run.result.error, undefined, run.result.stderr);
  assert.equal(run.evidence.status, 'PREFLIGHT_READY');
  assert.equal(run.calls.length, 0);
  const contract = JSON.parse(await readFile(join(run.evidenceDir, 'rollback-contract.json'), 'utf8'));
  assert.deepEqual(contract, {
    schema_version: 1,
    kind: 'vercel-alias-rollback-contract',
    repository: 'Rayer/llm-wiki-frontend',
    commit_sha: commitSha,
    ref: 'refs/heads/main',
    deployment: {
      id: deploymentId,
      project_id: projectId,
      source: 'github',
      repository: 'Rayer/llm-wiki-frontend',
      ref: 'refs/heads/main',
      commit_sha: commitSha,
      ready_state: 'READY',
      target: 'production',
      url: 'https://dpl_test123.vercel.app',
    },
    ci: {
      workflow: 'ci.yml',
      event: 'push',
      run_id: 987654321,
      run_url: 'https://github.test/Rayer/llm-wiki-frontend/actions/runs/987654321',
    },
    rollback_artifact_name: `vercel-alias-rollback-${commitSha}`,
    aliases: [
      { alias: aliases[0], deployment_id: 'dpl_oldcustom' },
      { alias: aliases[1], deployment_id: 'dpl_oldvercel' },
    ],
  });
  assert.deepEqual(contract.aliases, [
    { alias: aliases[0], deployment_id: 'dpl_oldcustom' },
    { alias: aliases[1], deployment_id: 'dpl_oldvercel' },
  ]);
  assert.equal(JSON.parse(await readFile(join(run.evidenceDir, 'vercel-alias-promotion-context.json'), 'utf8')).phase, 'preflight-complete');
  assert.equal(JSON.stringify(run.evidence).includes('SUCCESS'), false);
});

test('promote refuses to run without durable rollback artifact outputs', async () => {
  const run = await runCase('success', {}, 'preflight');
  const result = await runScript('promote', { ...run.env, ROLLBACK_ARTIFACT_ID: '' });
  assert.notEqual(result.code, 0);
  assert.equal(run.calls.length, 0);
  assert.match(JSON.parse(await readFile(join(run.evidenceDir, 'vercel-alias-promotion.json'), 'utf8')).reason, /artifact/);
});

test('promote re-reads both aliases immediately before mutation and aborts on snapshot drift', async () => {
  const run = await runCase('alias-changed-before-promote');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
  assert.match(run.evidence.reason, /rollback snapshot|changed/);
  assert.equal(run.calls.length, 0);
});

test('rejects a tampered resume context before any provider mutation', async () => {
  const run = await runCase('success', {}, 'preflight');
  const contextPath = join(run.evidenceDir, 'vercel-alias-promotion-context.json');
  const context = JSON.parse(await readFile(contextPath, 'utf8'));
  context.deployment_id = 'dpl_tampered';
  await writeFile(contextPath, JSON.stringify(context));
  const result = await runScript('promote', {
    ...run.env,
    PROMOTION_CONTEXT_PATH: contextPath,
  });
  assert.notEqual(result.code, 0);
  assert.equal(run.calls.length, 0);
  assert.match(JSON.parse(await readFile(join(run.evidenceDir, 'vercel-alias-promotion.json'), 'utf8')).reason, /context|identity|deployment/);
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

test('fails closed with a transport reason when health has no effective host', async () => {
  const run = await runCase('health-transport-failure');
  assert.notEqual(run.result.code, 0);
  assert.equal(run.evidence.status, 'POSTCHECK_FAILED');
  assert.match(run.evidence.reason, /transport|effective host/);
  assert.doesNotMatch(run.evidence.reason, /redirected/);
  assert.equal(run.evidence.health[0].status_code, '000');
  assert.equal(run.evidence.health[0].effective_host, null);
});

test('promotion uses Vercel-only credentials after preflight', async () => {
  const run = await runCase('success', {}, 'preflight');
  const result = await runScript('promote', { ...run.env, GITHUB_TOKEN: '' });
  assert.equal(result.error, undefined, result?.stderr);
  assert.equal(JSON.parse(await readFile(join(run.evidenceDir, 'vercel-alias-promotion.json'), 'utf8')).status, 'SUCCESS');
});

for (const [field, value] of [
  ['ROLLBACK_ARTIFACT_ID', '0'],
  ['ROLLBACK_ARTIFACT_URL', 'https://github.com/Rayer/other-repo/actions/runs/123456789/artifacts/123456789'],
  ['ROLLBACK_ARTIFACT_DIGEST', 'sha256:not-a-digest'],
  ['ROLLBACK_ARTIFACT_DIGEST', `md5:${rollbackArtifactDigestBare}`],
  ['ROLLBACK_ARTIFACT_DIGEST', `sha256:${rollbackArtifactDigestBare}01`],
  ['ROLLBACK_ARTIFACT_DIGEST', `sha256:${rollbackArtifactDigestBare.toUpperCase()}`],
  ['ROLLBACK_ARTIFACT_DIGEST', `${rollbackArtifactDigestBare.substring(0, 63)}`],
]) {
  test('rejects invalid durable artifact ' + field + ' before mutation', async () => {
    const run = await runCase('success', {}, 'preflight');
    const result = await runScript('promote', { ...run.env, [field]: value });
    assert.notEqual(result.code, 0);
    const evidence = JSON.parse(await readFile(join(run.evidenceDir, 'vercel-alias-promotion.json'), 'utf8'));
    assert.equal(evidence.status, 'PREFLIGHT_FAILED');
    assert.match(evidence.reason, /artifact/);
    assert.equal(run.calls.length, 0);
  });
}

test('rejects API origin overrides without explicit test mode and in GitHub Actions', async () => {
  for (const overrides of [
    { LWC199_TEST_MODE: '' },
    { GITHUB_ACTIONS: 'true', LWC199_TEST_MODE: '1' },
    { GITHUB_ACTIONS: 'true', LWC199_TEST_MODE: '', VERCEL_API_BASE_URL: 'https://vercel.test', GITHUB_API_URL: 'https://github.test/api/v3' },
  ]) {
    const run = await runCase('success', overrides);
    assert.notEqual(run.result.code, 0);
    assert.equal(run.evidence.status, 'PREFLIGHT_FAILED');
    assert.match(run.evidence.reason, /origin|test mode|Actions/);
    assert.equal(run.curlCalls.length, 0);
    assert.equal(run.calls.length, 0);
    assertNoCredentialLeak(run);
  }
});

for (const [scenario, expected] of [
  ['drift-before-first-write', [ 'dpl_drift_before_first', 'dpl_oldvercel' ]],
  ['drift-before-second-write', [ deploymentId, 'dpl_drift_before_second' ]],
  ['drift-after-first-write', [ deploymentId, 'dpl_drift_after_first' ]],
]) {
  test('fails closed and records causal mixed state for ' + scenario, async () => {
    const run = await runCase(scenario);
    assert.notEqual(run.result.code, 0);
    assert.equal(run.evidence.status, 'PARTIAL_MUTATION');
    assert.deepEqual(run.evidence.observed.alias_routing.map(({ deployment_id }) => deployment_id), expected);
    assert.match(run.evidence.next_action, /Read \/v4\/aliases/);
  });
}

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
  assert.equal(workflow.jobs.promote.env.EVIDENCE_DIR, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_TOKEN, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_PROJECT_ID, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_TEAM_ID, undefined);
  assert.equal(workflow.jobs.promote.env.VERCEL_SCOPE, undefined);
  assert.equal(workflow.jobs.promote.env.GITHUB_TOKEN, undefined);
  const checkout = workflow.jobs.promote.steps.find(({ name }) => name === 'Check out workflow-owned helper');
  assert.equal(checkout.uses, 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262');
  assert.equal(checkout.with.ref, '${{ github.sha }}');
  assert.equal(checkout.with['persist-credentials'], false);
  const preflight = workflow.jobs.promote.steps.find(({ name }) => name === 'Preflight exact deployment and rollback contract');
  const helper = workflow.jobs.promote.steps.find(({ name }) => name === 'Promote exact deployment');
  assert.deepEqual(preflight.env, {
    EVIDENCE_DIR: '${{ runner.temp }}/vercel-alias-promotion',
    GITHUB_TOKEN: '${{ github.token }}',
    VERCEL_TOKEN: '${{ secrets.VERCEL_TOKEN }}',
    VERCEL_PROJECT_ID: '${{ secrets.VERCEL_PROJECT_ID }}',
    VERCEL_TEAM_ID: '${{ secrets.VERCEL_TEAM_ID }}',
    VERCEL_SCOPE: '${{ secrets.VERCEL_SCOPE }}',
  });
  assert.deepEqual(helper.env, {
    EVIDENCE_DIR: '${{ runner.temp }}/vercel-alias-promotion',
    ROLLBACK_ARTIFACT_ID: '${{ steps.rollback_upload.outputs.artifact-id }}',
    ROLLBACK_ARTIFACT_URL: '${{ steps.rollback_upload.outputs.artifact-url }}',
    ROLLBACK_ARTIFACT_DIGEST: '${{ steps.rollback_upload.outputs.artifact-digest }}',
    VERCEL_TOKEN: '${{ secrets.VERCEL_TOKEN }}',
    VERCEL_PROJECT_ID: '${{ secrets.VERCEL_PROJECT_ID }}',
    VERCEL_TEAM_ID: '${{ secrets.VERCEL_TEAM_ID }}',
    VERCEL_SCOPE: '${{ secrets.VERCEL_SCOPE }}',
  });
  const runs = workflow.jobs.promote.steps.filter(({ run }) => typeof run === 'string').map(({ run }) => run);
  assert.ok(runs.some((run) => run.includes('npm install --global vercel@52.0.0 --ignore-scripts')));
  assert.ok(runs.some((run) => run.trim() === 'bash .github/scripts/vercel-alias-promotion.sh preflight'));
  assert.ok(runs.some((run) => run.trim() === 'bash .github/scripts/vercel-alias-promotion.sh promote'));
  assert.equal(runs.some((run) => /vercel\s+(deploy|build)|next\s+build/.test(run)), false);
  const steps = workflow.jobs.promote.steps;
  const rollbackUpload = steps.find(({ name }) => name === 'Upload durable alias rollback contract');
  assert.equal(rollbackUpload.id, 'rollback_upload');
  assert.equal(rollbackUpload.uses, 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02');
  assert.equal(rollbackUpload.with.name, 'vercel-alias-rollback-${{ inputs.commit_sha }}');
  assert.equal(rollbackUpload.with.path, '${{ runner.temp }}/vercel-alias-promotion/rollback-contract.json');
  assert.equal(rollbackUpload.with['if-no-files-found'], 'error');
  assert.equal(rollbackUpload.with['retention-days'], 90);
  assert.equal(rollbackUpload.if, undefined);
  const upload = steps.find(({ name }) => name === 'Upload normalized deployment and rollback evidence');
  assert.equal(upload.uses, 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02');
  assert.equal(upload.with.name, 'vercel-alias-promotion-evidence-${{ inputs.commit_sha }}');
  assert.equal(upload.with.path, '${{ runner.temp }}/vercel-alias-promotion/vercel-alias-promotion.json');
  assert.equal(upload.if, 'always()');
  assert.equal(upload.with['if-no-files-found'], 'error');
  assert.equal(upload.with['retention-days'], 90);
  assert.ok(steps.indexOf(rollbackUpload) < steps.indexOf(helper));
  assert.ok(steps.indexOf(helper) < steps.indexOf(upload));
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
  'alias-project-mismatch',
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
  assert.deepEqual(run.evidence.observed.alias_routing, [
    { alias: aliases[0], deployment_id: deploymentId },
    { alias: aliases[1], deployment_id: 'dpl_oldvercel' },
  ]);
  assert.ok(run.curlCalls.filter((url) => url.includes('/v4/aliases/')).length >= 4);
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
