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

function acknowledgement(project = legacyProject, deployment = oldDeployment, desired = sha, createIfMissing = 'true', runId = '123', newProject = canonicalProject, newTeam = team) {
  return `I acknowledge LWC-253 authority reconciliation: alias=${domain} old_project=${project} old_deployment=${deployment} old_source_sha=${oldSha} new_project=${newProject} new_team=${newTeam} desired_sha=${desired} ci_run_id=${runId} create_if_missing=${createIfMissing}`;
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
  await writeFile(join(root, 'ci.json'), JSON.stringify({ workflow_runs: [{ path: '.github/workflows/ci.yml', head_branch: 'develop', head_sha: sha, event: 'push', status: 'completed', conclusion: 'success', id: 123, html_url: 'https://github.com/Rayer/llm-wiki-frontend/actions/runs/123' }] }));
  await writeFile(join(root, 'old-deployment.json'), JSON.stringify({ id: oldDeployment, url: 'https://old.vercel.app', projectId: legacyProject, teamId: team, ownerId: team, readyState: 'READY', target: null, meta: { githubDeployment: '1', githubOrg: 'Rayer', githubRepo: 'llm-wiki-frontend', githubCommitRef: 'develop', githubCommitSha: oldSha } }));
  await writeFile(join(root, 'candidate.json'), JSON.stringify({ id: newDeployment, url: 'https://new.vercel.app', projectId: canonicalProject, teamId: team, ownerId: team, readyState: 'READY', target: 'preview', meta: { githubDeployment: '1', githubOrg: 'Rayer', githubRepo: 'llm-wiki-frontend', githubCommitRef: 'develop', githubCommitSha: sha } }));
  const initialState = { global: { alias: domain, projectId: legacyProject, deploymentId: oldDeployment }, legacyAliases: [{ alias: domain, projectId: legacyProject, deploymentId: oldDeployment }], canonicalAliases: [], production: {
    'wiki.rayer.idv.tw': { alias: 'wiki.rayer.idv.tw', projectId: legacyProject, deploymentId: oldDeployment, metadata: 'prod-one' },
    'llm-wiki-frontend.vercel.app': { alias: 'llm-wiki-frontend.vercel.app', projectId: legacyProject, deploymentId: oldDeployment, metadata: 'prod-two' },
  } };
  if (scenario === 'canonical-alias-present') initialState.canonicalAliases = [{ alias: domain, projectId: canonicalProject, deploymentId: 'dpl_wrong' }];
  if (scenario === 'legacy-inventory-duplicate') initialState.legacyAliases.push({ alias: domain, projectId: legacyProject, deploymentId: oldDeployment });
  if (scenario === 'already-converged') {
    initialState.global = { alias: domain, projectId: canonicalProject, deploymentId: newDeployment };
    initialState.legacyAliases = [];
    initialState.canonicalAliases = [{ alias: domain, projectId: canonicalProject, deploymentId: newDeployment }];
  }
  if (scenario === 'normal-lane-conflict') {
    initialState.global = { alias: domain, projectId: canonicalProject, deploymentId: 'dpl_other' };
    initialState.legacyAliases = [];
    initialState.canonicalAliases = [{ alias: domain, projectId: canonicalProject, deploymentId: 'dpl_other' }];
  }
  if (scenario === 'legacy-missing') initialState.legacyAliases = [];
  if (scenario === 'canonical-inventory-unexpected') initialState.canonicalAliases = [{ alias: domain, projectId: canonicalProject, deploymentId: 'dpl_other' }];
  if (['inventory-order-drift', 'post-inventory-remove', 'post-inventory-change'].includes(scenario)) {
    initialState.legacyAliases.push({ alias: 'legacy.example', projectId: legacyProject, deploymentId: 'dpl_legacy_extra', metadata: 'legacy' });
    initialState.canonicalAliases.push({ alias: 'canonical.example', projectId: canonicalProject, deploymentId: 'dpl_canonical_extra', metadata: 'canonical' });
  }
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
    CI_RUN_ID: '123',
    EXPECTED_NEW_PROJECT_ID: canonicalProject,
    EXPECTED_TEAM_ID: team,
    CREATE_IF_MISSING: 'true',
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

test('reused candidate final authority reread failure is PREFLIGHT_FAILED with zero writes', async () => {
  const fixture = await setup('final-reread-authority-failure');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PREFLIGHT_FAILED');
  assert.equal(output.reason_code, 'AUTHORITY_DRIFT');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  assert.ok((await lines(join(fixture.root, 'curl-calls'))).filter((url) => url.includes('/v9/projects/prj_canonical?')).length >= 3);
});

test('complete alias inventories compare equal across provider order changes', async () => {
  const fixture = await setup('inventory-order-drift');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, undefined, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'SUCCESS');
  assert.equal(output.provider_verification.mutation_count, 1);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
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

test('legacy inspect rejects ownerId-only legacy payloads in old code and validates exact id in patched code', async () => {
  const fixture = await setup('legacy-ownerid');
  const preflight = await run(fixture, 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PREFLIGHT_READY');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
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

for (const scenario of ['ci-wrong-path', 'ci-wrong-event', 'ci-wrong-ref', 'ci-wrong-sha', 'ci-running', 'ci-failure', 'ci-wrong-url', 'ci-wrong-id']) {
  test(`rejects exact CI run variant ${scenario} before mutation`, async () => {
    const fixture = await setup(scenario);
    const result = await run(fixture, 'preflight');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, 'CI_NOT_GREEN');
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  });
}

for (const [label, value, reason] of [
  ['missing', '', 'CI_RUN_ID_INVALID'],
  ['zero', '0', 'CI_RUN_ID_INVALID'],
  ['decimal', '1.2', 'CI_RUN_ID_INVALID'],
  ['negative', '-1', 'CI_RUN_ID_INVALID'],
]) {
  test(`rejects ${label} ci_run_id before provider mutation`, async () => {
    const fixture = await setup();
    const result = await run(fixture, 'preflight', { CI_RUN_ID: value });
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
  });
}

for (const [scenario, reason] of [
  ['domain-missing', 'DOMAIN_NOT_ALLOWLISTED'],
  ['domain-duplicate', 'DOMAIN_NOT_ALLOWLISTED'],
  ['legacy-project-mismatch', 'LEGACY_PROJECT_MISMATCH'],
  ['legacy-team-mismatch', 'LEGACY_PROJECT_MISMATCH'],
  ['old-source-mismatch', 'LEGACY_DEPLOYMENT_MISMATCH'],
  ['old-ref-mismatch', 'LEGACY_DEPLOYMENT_MISMATCH'],
  ['old-repo-mismatch', 'LEGACY_DEPLOYMENT_MISMATCH'],
  ['old-state-mismatch', 'LEGACY_DEPLOYMENT_MISMATCH'],
  ['old-target-mismatch', 'LEGACY_DEPLOYMENT_MISMATCH'],
  ['global-absent', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['legacy-missing', 'AUTHORITY_DRIFT'],
  ['canonical-inventory-unexpected', 'AUTHORITY_DRIFT'],
  ['third-authority', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['duplicate-candidates', 'DEPLOYMENT_CANDIDATE_AMBIGUOUS'],
]) {
  test(`rejects adversarial authority fixture ${scenario} with zero mutation`, async () => {
    const fixture = await setup(scenario);
    const result = await run(fixture, 'preflight');
    assert.equal(result.code, 1, result.stderr);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  });
}

for (const [label, overrides, reason] of [
  ['expected project secret mismatch', { EXPECTED_NEW_PROJECT_ID: 'prj_other', RECONCILIATION_ACK: acknowledgement(legacyProject, oldDeployment, sha, 'true', '123', 'prj_other') }, 'EXPECTED_NEW_PROJECT_MISMATCH'],
  ['expected team secret mismatch', { EXPECTED_TEAM_ID: 'team_other', RECONCILIATION_ACK: acknowledgement(legacyProject, oldDeployment, sha, 'true', '123', canonicalProject, 'team_other') }, 'EXPECTED_TEAM_MISMATCH'],
  ['live canonical project mismatch', {}, 'PROJECT_METADATA_MISMATCH'],
]) {
  test(`rejects ${label} before provider mutation`, async () => {
    const fixture = await setup(label === 'live canonical project mismatch' ? 'project-mismatch' : 'existing-candidate');
    const result = await run(fixture, 'preflight', overrides);
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
  });
}

test('create_if_missing false blocks a missing candidate without an artifact-dependent mutation', async () => {
  const fixture = await setup('create-needed');
  const result = await run(fixture, 'preflight', { CREATE_IF_MISSING: 'false', RECONCILIATION_ACK: acknowledgement(legacyProject, oldDeployment, sha, 'false') });
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.reason_code, 'CREATE_NOT_ALLOWED');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
});

for (const scenario of ['production-before-create-drift', 'post-create-authority-drift']) {
  test(`${scenario} remains zero alias mutation or partial after provider create`, async () => {
    const fixture = await setup('create-needed');
    await writeFile(join(fixture.root, 'scenario'), scenario);
    const preflight = await run(fixture, 'preflight');
    assert.equal(preflight.code, undefined, preflight.stderr);
    const result = await run(fixture, 'promote');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, scenario === 'production-before-create-drift' ? 'PREFLIGHT_FAILED' : 'PARTIAL_MUTATION');
    assert.equal(output.provider_verification.mutation_count, scenario === 'production-before-create-drift' ? 0 : 1);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, scenario === 'production-before-create-drift' ? 0 : 1);
  });
}

test('post-mutation production and unrelated inventory drift is PARTIAL_MUTATION', async () => {
  const fixture = await setup('existing-candidate');
  await writeFile(join(fixture.root, 'scenario'), 'production-drift');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PARTIAL_MUTATION');
  assert.equal(output.reason_code, 'POSTCHECK_MISMATCH');
  assert.equal(output.provider_verification.mutation_count, 1);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 1);
});

test('post-mutation unrelated inventory drift is not hidden by the DEV alias setter', async () => {
  const fixture = await setup('existing-candidate');
  await writeFile(join(fixture.root, 'scenario'), 'post-inventory-mismatch');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PARTIAL_MUTATION');
  assert.equal(output.reason_code, 'POSTCHECK_MISMATCH');
  assert.equal(output.provider_verification.mutation_count, 1);
});

for (const scenario of ['post-inventory-add', 'post-inventory-remove', 'post-inventory-change']) {
  test(`${scenario} is PARTIAL_MUTATION after the alias attempt`, async () => {
    const fixture = await setup(scenario);
    assert.equal((await run(fixture, 'preflight')).code, undefined);
    const result = await run(fixture, 'promote');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PARTIAL_MUTATION');
    assert.equal(output.reason_code, 'POSTCHECK_MISMATCH');
    assert.equal(output.provider_verification.mutation_count, 1);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
  });
}

test('alias CLI failure after provider acceptance is partial and never retried', async () => {
  const fixture = await setup('alias-failure');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PARTIAL_MUTATION');
  assert.equal(output.reason_code, 'MUTATION_UNCERTAIN');
  assert.equal(output.provider_verification.mutation_count, 1);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
});

for (const scenario of ['post-api-mismatch', 'cli-mismatch']) {
  test(`${scenario} is PARTIAL_MUTATION after alias mutation`, async () => {
    const fixture = await setup(scenario);
    assert.equal((await run(fixture, 'preflight')).code, undefined);
    const result = await run(fixture, 'promote');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PARTIAL_MUTATION');
    assert.equal(output.reason_code, 'POSTCHECK_MISMATCH');
    assert.equal(output.provider_verification.mutation_count, 1);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
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

for (const [label, overrides] of [
  ['invalid ID', { ROLLBACK_ARTIFACT_ID: 'artifact-456' }],
  ['invalid URL', { ROLLBACK_ARTIFACT_URL: 'https://github.com/Rayer/other/actions/runs/123/artifacts/456' }],
  ['invalid name', { RECONCILIATION_ARTIFACT_NAME: `other-${sha}` }],
  ['uppercase digest', { ROLLBACK_ARTIFACT_DIGEST: artifactDigest.toUpperCase() }],
  ['63-character digest', { ROLLBACK_ARTIFACT_DIGEST: artifactDigest.slice(0, 63) }],
  ['65-character digest', { ROLLBACK_ARTIFACT_DIGEST: `${artifactDigest}0` }],
  ['empty digest', { ROLLBACK_ARTIFACT_DIGEST: '' }],
]) {
  test(`reconciliation rejects ${label} artifact handoff before every provider write`, async () => {
    const fixture = await setup('create-needed');
    assert.equal((await run(fixture, 'preflight')).code, undefined);
    const result = await run(fixture, 'promote', overrides);
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PREFLIGHT_FAILED');
    assert.equal(output.reason_code, 'ROLLBACK_ARTIFACT_INVALID');
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  });
}

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
  assert.ok((await readFile(join(fixture.root, 'curl-calls'), 'utf8')).includes('until=1700000000102'));
});

for (const [scenario, reason] of [
  ['deployment-page-2-exact', ''],
  ['deployment-cursor-loop', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-malformed', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-page-max', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-string', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-bool', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-object', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-float', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-count-bool', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-count-float', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-prev-bool', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-prev-object', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-negative', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-missing', 'DEPLOYMENT_LIST_FAILED'],
  ['deployment-pagination-malformed', 'DEPLOYMENT_LIST_FAILED'],
  ['duplicate-candidates', 'DEPLOYMENT_CANDIDATE_AMBIGUOUS'],
]) {
  test(`reconciliation deployment inventory scenario ${scenario} is bounded before writes`, async () => {
    const fixture = await setup(scenario);
    const result = await run(fixture, 'preflight');
    if (scenario === 'deployment-page-2-exact') {
      assert.equal(result.code, undefined, result.stderr);
      const output = await evidence(fixture);
      assert.equal(output.status, 'PREFLIGHT_READY');
      assert.ok((await readFile(join(fixture.root, 'curl-calls'), 'utf8')).includes('until=1700000000101'));
    } else {
      assert.equal(result.code, 1);
      const output = await evidence(fixture);
      assert.equal(output.status, 'PREFLIGHT_FAILED');
      assert.equal(output.reason_code, reason);
      assert.equal(output.provider_verification.mutation_count, 0);
      assert.deepEqual(output.provider_verification.checks, []);
      assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
      assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
      const calls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
      const deploymentCalls = calls.filter((url) => url.includes('/v6/deployments?'));
      if (scenario === 'deployment-cursor-loop') {
        assert.equal(deploymentCalls.length, 2);
        assert.ok(!deploymentCalls[0].includes('until='));
        assert.equal(deploymentCalls[1].includes('until=1700000000202'), true);
      } else if (scenario === 'deployment-page-max') {
        assert.equal(deploymentCalls.length, 10);
        assert.ok(!deploymentCalls[0].includes('until='));
        const cursors = deploymentCalls.slice(1).map((url) => Number(new URL(url).searchParams.get('until')));
        for (let i = 0; i < cursors.length; i += 1) {
          assert.ok(Number.isInteger(cursors[i]) && cursors[i] >= 0);
          if (i > 0) assert.ok(cursors[i] > cursors[i - 1]);
        }
      }
      return;
    }
    if (scenario === 'deployment-page-2-exact') {
      const promote = await run(fixture, 'promote');
      assert.equal(promote.code, undefined, promote.stderr);
      assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
      assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
    }
  });
}

for (const [scenario, reason] of [
  ['alias-pagination-string', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-bool', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-object', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-float', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-count-bool', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-count-float', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-prev-bool', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-prev-object', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-negative', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-missing', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-pagination-malformed', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['inventory-page-max', 'AUTHORITY_PREFLIGHT_MISMATCH'],
]) {
  test(`alias pagination scenario ${scenario} is bounded before writes`, async () => {
    const fixture = await setup(scenario);
    const result = await run(fixture, 'preflight');
    assert.equal(result.code, 1, result.stderr);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PREFLIGHT_FAILED');
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.deepEqual(output.provider_verification.checks, []);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  const calls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
    const aliasCalls = calls.filter((url) => url.includes('/v4/aliases?') && url.includes('projectId='));
    if (scenario === 'inventory-page-max') {
      assert.equal(aliasCalls.length, 20);
      const batches = [aliasCalls.slice(0, 10), aliasCalls.slice(10, 20)];
      for (const aliasBatch of batches) {
        assert.equal(aliasBatch.length, 10);
        assert.ok(!aliasBatch[0].includes('until='));
        const cursors = aliasBatch.slice(1).map((url) => Number(new URL(url).searchParams.get('until')));
        assert.equal(cursors.length, 9);
      for (let i = 0; i < cursors.length; i += 1) {
        assert.ok(Number.isInteger(cursors[i]) && cursors[i] >= 0);
        if (i > 0) assert.ok(cursors[i] > cursors[i - 1]);
      }
      }
    }
  });
}

test('alias pagination next mutated from number to string fails pagination parsing', async () => {
  const fixture = await setup('alias-pagination-string');
  const result = await run(fixture, 'preflight');
  assert.equal(result.code, 1, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'PREFLIGHT_FAILED');
  assert.equal(output.reason_code, 'AUTHORITY_PREFLIGHT_MISMATCH');
  assert.equal(output.provider_verification.mutation_count, 0);
  const calls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
  const inventoryCalls = calls.filter((url) => url.includes('/v4/aliases?'));
  assert.equal(inventoryCalls.length, 2);
  assert.ok(!inventoryCalls[0].includes('until='));
});

  for (const [scenario, reason] of [
  ['alias-cursor-loop', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['alias-malformed', 'AUTHORITY_PREFLIGHT_MISMATCH'],
  ['canonical-alias-present', 'AUTHORITY_DRIFT'],
  ['legacy-inventory-duplicate', 'AUTHORITY_DRIFT'],
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
    const calls = (await readFile(join(fixture.root, 'curl-calls'), 'utf8')).trim().split('\n').filter(Boolean);
    if (scenario === 'alias-cursor-loop') {
      const aliasCalls = calls.filter((url) => url.includes('/v4/aliases?') && url.includes('projectId='));
      assert.equal(aliasCalls.length, 4);
      const batches = [aliasCalls.slice(0, 2), aliasCalls.slice(2, 4)];
      for (const aliasBatch of batches) {
        assert.equal(aliasBatch.length, 2);
        assert.ok(!aliasBatch[0].includes('until='));
        assert.equal(aliasBatch[1].includes('until=1700000000111'), true);
      }
    }
  });
}

test('already-converged is a successful zero-write terminal state', async () => {
  const fixture = await setup('already-converged');
  const preflight = await run(fixture, 'preflight');
  assert.equal(preflight.code, undefined, preflight.stderr);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, undefined, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'ALREADY_CONVERGED');
  assert.equal(output.reason_code, 'ALREADY_CONVERGED');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
});

test('successful promote evidence carries restored checked-out and remote-develop SHA provenance', async () => {
  const fixture = await setup('existing-candidate');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote', {
    CURRENT_HEAD_SHA: '',
    CURRENT_REMOTE_DEVELOP_SHA: '',
  });
  assert.equal(result.code, undefined, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'SUCCESS');
  assert.equal(output.source.checked_out_sha, sha);
  assert.equal(output.source.current_remote_develop_sha, sha);
  assert.equal(output.source.canonical_ci.run_url, 'https://github.com/Rayer/llm-wiki-frontend/actions/runs/123');
  assert.equal(output.provider_verification.mutation_count, 1);
});

test('canonical project with a different deployment is delegated to the normal DEV lane', async () => {
  const fixture = await setup('normal-lane-conflict');
  const result = await run(fixture, 'preflight');
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.reason_code, 'NORMAL_DEV_LANE_REQUIRED');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
});

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

for (const [scenario, reason] of [
  ['create-response-missing-id', 'DEPLOYMENT_CREATE_UNCERTAIN'],
  ['create-response-invalid-id', 'DEPLOYMENT_CREATE_UNCERTAIN'],
  ['create-poll-timeout', 'DEPLOYMENT_POLL_TIMEOUT'],
  ['create-terminal-failed', 'DEPLOYMENT_NOT_READY'],
  ['create-source-mismatch', 'DEPLOYMENT_SOURCE_MISMATCH'],
  ['create-read-failure', 'DEPLOYMENT_INSPECT_FAILED'],
]) {
  test(`${scenario} is partial after one create attempt and before alias`, async () => {
    const fixture = await setup('create-needed');
    await writeFile(join(fixture.root, 'scenario'), scenario);
    assert.equal((await run(fixture, 'preflight')).code, undefined);
    const result = await run(fixture, 'promote');
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.status, 'PARTIAL_MUTATION');
    assert.equal(output.reason_code, reason);
    assert.equal(output.provider_verification.mutation_count, 1);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 1);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 0);
  });
}

for (const createIfMissing of ['false', 'TRUE', '1', 'yes', '', '0']) {
  test(`create_if_missing=${JSON.stringify(createIfMissing)} is rejected by reconciliation before writes`, async () => {
    const fixture = await setup('create-needed');
    const result = await run(fixture, 'preflight', { CREATE_IF_MISSING: createIfMissing, RECONCILIATION_ACK: acknowledgement(legacyProject, oldDeployment, sha, createIfMissing) });
    assert.equal(result.code, 1);
    const output = await evidence(fixture);
    assert.equal(output.reason_code, createIfMissing === 'false' ? 'CREATE_NOT_ALLOWED' : 'CREATE_IF_MISSING_INVALID');
    assert.equal(output.provider_verification.mutation_count, 0);
    assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
    assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
  });
}

test('foreign-project exact-SHA candidate does not suppress canonical create', async () => {
  const fixture = await setup('foreign-project-candidate');
  assert.equal((await run(fixture, 'preflight')).code, undefined);
  const result = await run(fixture, 'promote');
  assert.equal(result.code, undefined, result.stderr);
  const output = await evidence(fixture);
  assert.equal(output.status, 'SUCCESS');
  assert.equal(output.provider_verification.mutation_count, 2);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 1);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).filter((line) => line.startsWith('alias set')).length, 1);
});

test('foreign-project exact-SHA candidate cannot suppress CREATE_NOT_ALLOWED', async () => {
  const fixture = await setup('foreign-project-candidate');
  const result = await run(fixture, 'preflight', { CREATE_IF_MISSING: 'false', RECONCILIATION_ACK: acknowledgement(legacyProject, oldDeployment, sha, 'false') });
  assert.equal(result.code, 1);
  const output = await evidence(fixture);
  assert.equal(output.reason_code, 'CREATE_NOT_ALLOWED');
  assert.equal(output.provider_verification.mutation_count, 0);
  assert.equal((await lines(join(fixture.root, 'deployment-post-log'))).length, 0);
  assert.equal((await lines(join(fixture.root, 'mutation-log'))).length, 0);
});

test('workflow is manual, develop-gated, pinned, and shares normal concurrency', async () => {
  const normal = parseYaml(await readFile(join(repoRoot, '.github/workflows/vercel-dev-deployment.yml'), 'utf8'));
  const reconciliation = parseYaml(await readFile(join(repoRoot, '.github/workflows/vercel-dev-authority-reconciliation.yml'), 'utf8'));
  assert.equal(reconciliation.concurrency.group, normal.concurrency.group);
  assert.deepEqual(Object.keys(reconciliation.on), ['workflow_dispatch']);
  assert.equal(reconciliation.jobs.reconcile.if, "github.ref == 'refs/heads/develop'");
  assert.equal(reconciliation.jobs.reconcile.environment.name, 'Development');
  const inputs = reconciliation.on.workflow_dispatch.inputs;
  for (const name of ['commit_sha', 'ci_run_id', 'expected_new_project_id', 'expected_team_id', 'expected_current_alias_project_id', 'expected_current_alias_deployment_id', 'expected_current_alias_source_sha', 'ticket_ref', 'reconciliation_ack', 'create_if_missing']) {
    assert.equal(inputs[name].required, true);
    assert.equal(inputs[name].type, name === 'ci_run_id' ? 'number' : name === 'create_if_missing' ? 'boolean' : 'string');
  }
  const jobEnv = reconciliation.jobs.reconcile.env;
  for (const name of ['COMMIT_SHA', 'CI_RUN_ID', 'EXPECTED_NEW_PROJECT_ID', 'EXPECTED_TEAM_ID', 'CREATE_IF_MISSING', 'EXPECTED_CURRENT_ALIAS_PROJECT_ID', 'EXPECTED_CURRENT_ALIAS_DEPLOYMENT_ID', 'EXPECTED_CURRENT_ALIAS_SOURCE_SHA', 'TICKET_REF', 'RECONCILIATION_ACK']) {
    assert.ok(name in jobEnv, `missing immutable workflow wiring: ${name}`);
  }
  assert.match(await readFile(join(repoRoot, '.github/scripts/vercel-dev-authority-reconciliation.sh'), 'utf8'), /actions\/runs\/\$CI_RUN_ID/);
  assert.doesNotMatch(await readFile(join(repoRoot, '.github/scripts/vercel-dev-authority-reconciliation.sh'), 'utf8'), /actions\/workflows\/ci\.yml\/runs\?/);
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
  const reconciliationSource = await readFile(join(repoRoot, '.github/scripts/vercel-dev-authority-reconciliation.sh'), 'utf8');
  assert.doesNotMatch(reconciliationSource, /vercel alias set[^\n]*(wiki\.rayer\.idv\.tw|llm-wiki-frontend\.vercel\.app)/);
});

test('sourcing the normal helper is library-only and preserves direct evidence naming', async () => {
  const source = await readFile(join(repoRoot, '.github/scripts/vercel-dev-deployment.sh'), 'utf8');
  assert.match(source, /EVIDENCE_PATH="\$EVIDENCE_DIR\/vercel-dev-deployment\.json"/);
  assert.doesNotMatch(source, /EVIDENCE_FILENAME/);
  const result = await execFileAsync('bash', ['-c', 'VERCEL_DEV_DEPLOYMENT_LIBRARY=1 source .github/scripts/vercel-dev-deployment.sh; printf "%s|%s" "$MODE" "$EVIDENCE_PATH"'], { cwd: repoRoot, env: { ...process.env, EVIDENCE_DIR: '/tmp/lwc-253-library-test' } });
  assert.equal(result.stdout, '|/tmp/lwc-253-library-test/vercel-dev-deployment.json');
});
