import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { load as parseYaml } from 'js-yaml';

const repoRoot = new URL('../../..', import.meta.url).pathname;
const workflowDirectory = join(repoRoot, '.github/workflows');

function workflow(name) {
  return readFile(join(workflowDirectory, name), 'utf8');
}

function collectRunBlocks(value, blocks = []) {
  if (Array.isArray(value)) value.forEach((item) => collectRunBlocks(item, blocks));
  else if (value && typeof value === 'object') {
    Object.entries(value).forEach(([key, item]) => {
      if (key === 'run' && typeof item === 'string') blocks.push(item);
      else collectRunBlocks(item, blocks);
    });
  }
  return blocks;
}

test('r2 registered release and recovery workflows use explicit artifact inputs', async () => {
  const files = (await readdir(workflowDirectory)).filter((file) => file.endsWith('.yml')).sort();
  assert.deepEqual(files, ['cd-auth-image-diagnostic.yml', 'cd.yml', 'ci.yml', 'deploy-dev.yml', 'promote-production.yml', 'provision-exportjob-dev.yml', 'recover-deployment.yml']);
  for (const [file, environment, branch] of [['deploy-dev.yml', 'development', 'develop'], ['promote-production.yml', 'production', 'main']]) {
    const parsed = parseYaml(await workflow(file));
    assert.deepEqual(Object.keys(parsed.on), ['workflow_dispatch']);
    const expectedInputs = ['components', 'release_tag', 'artifact_id', 'dev_artifact_id'];
    if (file === 'deploy-dev.yml') expectedInputs.push('source_sha', 'force', 'operation');
    assert.deepEqual(Object.keys(parsed.on.workflow_dispatch.inputs), expectedInputs);
    assert.equal(parsed.jobs.release.with.environment, environment);
    assert.equal(parsed.jobs.release.if, file === 'deploy-dev.yml'
      ? `github.ref == 'refs/heads/${branch}' && inputs.operation != 'diagnose-auth-image'`
      : `github.ref == 'refs/heads/${branch}'`);
    assert.equal(parsed.jobs.release.with.source_sha, file === 'deploy-dev.yml'
      ? "${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}"
      : '${{ github.sha }}');
    assert.equal(parsed.jobs.release.with.executor_sha, '${{ github.sha }}');
    assert.equal(parsed.jobs.release.secrets, 'inherit');
    if (file === 'deploy-dev.yml') assert.equal(parsed.jobs.release.with.operation, '${{ inputs.operation }}');
  }
  const deployDev = parseYaml(await workflow('deploy-dev.yml'));
  const operation = deployDev.on.workflow_dispatch.inputs.operation;
  assert.equal(operation.type, 'choice');
  assert.equal(operation.default, 'release');
  assert.deepEqual(operation.options, ['release', 'deploy', 'rollback', 'reactivate', 'tag', 'readback', 'diagnose-auth-image']);
  assert.equal(deployDev.on.workflow_dispatch.inputs.source_sha.default, '');
  assert.deepEqual(deployDev.on.workflow_dispatch.inputs.force, { description: 'Explicitly accept duplicate-mutation risk to deploy a new ready attempt past another attempt\'s unresolved target status', type: 'boolean', default: false });
  assert.deepEqual(Object.keys(deployDev.jobs).sort(), ['auth-image-diagnostic', 'main-fast-forward-eligible', 'release']);
  const eligibility = deployDev.jobs['main-fast-forward-eligible'];
  assert.equal(eligibility.name, 'main-fast-forward-eligible');
  assert.equal(eligibility.needs, 'release');
  assert.ok(eligibility.if.includes("inputs.operation != 'readback'"));
  assert.deepEqual(eligibility.permissions, { contents: 'read', actions: 'read', statuses: 'write' });
  assert.equal(eligibility.steps.find((step) => step.name === 'Download this DEV attempt result').with.name,
    'lwc-result-development-${{ github.run_id }}-${{ github.run_attempt }}');
  const diagnostic = deployDev.jobs['auth-image-diagnostic'];
  for (const condition of [
    "inputs.operation == 'diagnose-auth-image'", "github.ref == 'refs/heads/develop'",
    "inputs.components == 'auth'", "inputs.release_tag == 'diagnostic-36992147920'",
    "inputs.artifact_id == 'diagnostic-no-receipt'", "inputs.dev_artifact_id == ''",
  ]) assert.ok(diagnostic.if.includes(condition), `missing diagnostic constraint: ${condition}`);
  assert.equal(diagnostic.uses, './.github/workflows/cd-auth-image-diagnostic.yml');
  assert.equal(diagnostic.secrets, 'inherit');
  assert.equal(diagnostic.env, undefined);
  assert.doesNotMatch(JSON.stringify(diagnostic), /GH_TOKEN|VERCEL_TOKEN/);
  assert.deepEqual(diagnostic.permissions, { contents: 'read', actions: 'read', 'id-token': 'write' });
  assert.deepEqual(diagnostic.with, { source_sha: '${{ github.sha }}' });
  const recovery = parseYaml(await workflow('recover-deployment.yml'));
  assert.deepEqual(recovery.on.workflow_dispatch.inputs.operation.options, ['rollback', 'reactivate', 'deploy', 'tag', 'readback', 'diagnose-auth-image']);
  assert.equal(recovery.jobs.recovery.uses, './.github/workflows/cd.yml');
  assert.equal(recovery.jobs.recovery.if, "inputs.operation != 'diagnose-auth-image'");
  assert.deepEqual(recovery.jobs.recovery.with, {
    environment: '${{ inputs.environment }}', source_sha: '${{ inputs.source_sha }}',
    executor_sha: '${{ github.sha }}',
    components: '${{ inputs.components }}', release_tag: '${{ inputs.release_tag }}',
    operation: '${{ inputs.operation }}', artifact_id: '${{ inputs.artifact_id }}',
  });
  const diagnosticRecovery = recovery.jobs['auth-image-diagnostic'];
  assert.match(diagnosticRecovery.if, /inputs\.operation == 'diagnose-auth-image'/);
  for (const condition of [
    "github.ref == 'refs/heads/develop'", "inputs.environment == 'development'",
    'inputs.source_sha == github.sha', "inputs.components == 'auth'",
    "inputs.release_tag == 'diagnostic-36992147920'",
    "inputs.artifact_id == 'diagnostic-no-receipt'",
  ]) assert.ok(diagnosticRecovery.if.includes(condition), `missing diagnostic constraint: ${condition}`);
  assert.equal(diagnosticRecovery.uses, './.github/workflows/cd-auth-image-diagnostic.yml');
  assert.deepEqual(diagnosticRecovery.permissions, { contents: 'read', actions: 'read', 'id-token': 'write' });
  assert.deepEqual(diagnosticRecovery.with, { source_sha: '${{ inputs.source_sha }}' });
  const diagnosticWorkflow = parseYaml(await workflow('cd-auth-image-diagnostic.yml'));
  assert.deepEqual(Object.keys(diagnosticWorkflow.on.workflow_call.inputs), ['source_sha']);
  assert.deepEqual(diagnosticWorkflow.on.workflow_call.inputs.source_sha, { required: true, type: 'string' });
  assert.deepEqual(Object.keys(diagnosticWorkflow.jobs), ['auth-image-diagnostic']);
  assert.deepEqual(diagnosticWorkflow.permissions, { contents: 'read', actions: 'read', 'id-token': 'write' });
  const standalone = diagnosticWorkflow.jobs['auth-image-diagnostic'];
  assert.equal(standalone.if, "github.ref == 'refs/heads/develop' && inputs.source_sha == github.sha");
  assert.deepEqual(standalone.permissions, { contents: 'read', actions: 'read', 'id-token': 'write' });
  assert.deepEqual(standalone.with, undefined);
  assert.equal(standalone.env.TARGET, 'development');
  assert.equal(standalone.env.COMPONENTS, 'auth');
  assert.equal(standalone.env.OPERATION, 'diagnose-auth-image');
  assert.equal(standalone.env.RELEASE_TAG, 'diagnostic-36992147920');
  assert.equal(standalone.env.ARTIFACT_ID, 'diagnostic-no-receipt');
  assert.equal(standalone.env.REUSE_ID, 'diagnostic-no-receipt');
  assert.equal(standalone.env.DEV_ID, '');
  assert.doesNotMatch(JSON.stringify(standalone), /GH_TOKEN|VERCEL_TOKEN|VERCEL_PROJECT_ID|VERCEL_TEAM_ID|artifacts\.cjs|checkpoint/i);
  assert.doesNotMatch(JSON.stringify(standalone.steps), /artifacts\.cjs|checkpoint|receipt/i);
  const permissionRank = { none: 0, read: 1, write: 2 };
  for (const call of [diagnostic, diagnosticRecovery]) {
    assert.equal(call.uses, './.github/workflows/cd-auth-image-diagnostic.yml');
    for (const [scopeName, scope] of [
      ['workflow', diagnosticWorkflow.permissions],
      ...Object.entries(diagnosticWorkflow.jobs).map(([name, value]) => [name, value.permissions]),
    ]) {
      for (const [permission, level] of Object.entries(scope)) {
        const ceiling = call.permissions[permission] ?? 'none';
        assert.ok(permissionRank[level] <= permissionRank[ceiling], `${scopeName} requests ${level} ${permission} beyond caller ${ceiling}`);
      }
    }
  }
});

test('DEV provisioning uses the existing auth identity and preserves hidden evidence', async () => {
  const source = await workflow('provision-exportjob-dev.yml');
  const provision = parseYaml(source).jobs.provision;
  const auth = provision.steps.find((step) => step.uses?.startsWith('google-github-actions/auth@'));
  const evidence = provision.steps.find((step) => step.uses?.startsWith('actions/upload-artifact@'));
  assert.equal(auth.with.workload_identity_provider, '${{ secrets.WIF_PROVIDER }}');
  assert.equal(auth.with.service_account, '${{ secrets.WIF_SERVICE_ACCOUNT }}');
  assert.equal(evidence.with['include-hidden-files'], true);
  assert.equal(evidence.with['retention-days'], 90);
  assert.match(source, /if: github\.ref == 'refs\/heads\/develop'/);
  assert.doesNotMatch(source, /concurrency:/);
  assert.doesNotMatch(source, /refs\/heads\/main|production\.yaml|run jobs execute/);
});

test('shared engine has one approval and one serialized runtime authority', async () => {
  const parsed = parseYaml(await workflow('cd.yml'));
  assert.deepEqual(Object.keys(parsed.jobs), ['release']);
  assert.equal(parsed.concurrency.group, 'lwc-engine-${{ inputs.environment }}');
  assert.equal(parsed.concurrency['cancel-in-progress'], false);
  const job = parsed.jobs.release;
  assert.deepEqual(parsed.on.workflow_call.inputs.executor_sha, { required: true, type: 'string' });
  assert.deepEqual(parsed.on.workflow_call.inputs.force, { type: 'boolean', default: false });
  assert.equal(parsed.jobs.release.env.EXECUTOR_SHA, '${{ inputs.executor_sha }}');
  assert.equal(parsed.jobs.release.env.FORCE, '${{ inputs.force }}');
  assert.equal(job.if, "inputs.operation != 'diagnose-auth-image'");
  assert.equal(job.environment, "${{ inputs.environment == 'production' && 'Production' || 'Development' }}");
  const steps = job.steps;
  const prepare = steps.findIndex(step => step.with?.operation === 'prepare');
  const barrier = steps.findIndex(step => step.id === 'ready');
  const runtimeAuthorities = Object.entries(parsed.jobs).flatMap(([jobName, candidate]) =>
    candidate.steps.flatMap((step, index) => step.with?.operation === 'runtime' ? [{ jobName, index, step }] : []));
  assert.equal(runtimeAuthorities.length, 1);
  assert.equal(runtimeAuthorities[0].jobName, 'release');
  assert.equal(runtimeAuthorities[0].step.uses, './.github/actions/deployment-engine');
  const runtime = runtimeAuthorities[0].index;
  assert.ok(prepare >= 0 && prepare < barrier && barrier < runtime);
  const resumeSource = steps.find(step => step.name === 'Validate retained Stage 1 source input');
  assert.equal(resumeSource.if, "inputs.operation == 'release' && inputs.environment == 'development' && inputs.source_sha != github.sha");
  assert.equal(resumeSource.env.ARTIFACT_ID, '${{ inputs.artifact_id }}');
  assert.match(resumeSource.run, /test -n \"\$ARTIFACT_ID\"/);
  assert.equal(job.env.SOURCE, '${{ inputs.source_sha }}');
  assert.equal(job.env.EXECUTOR_SHA, '${{ inputs.executor_sha }}');
  assert.equal(steps[prepare].if, "inputs.operation == 'release'");
  assert.equal(steps.find(step => step.uses?.startsWith('actions/checkout@')).with.ref, '${{ inputs.executor_sha }}');
  assert.equal(steps[runtime].uses, './.github/actions/deployment-engine');
  assert.equal(job.permissions.contents, 'write');
  assert.equal(job.permissions['id-token'], 'write');
});

test('ready and result artifacts have bounded retention and always retain failure evidence', async () => {
  const parsed = parseYaml(await workflow('cd.yml'));
  const uploads = parsed.jobs.release.steps.filter(step => step.uses?.startsWith('actions/upload-artifact@'));
  assert.equal(uploads.length, 2);
  for (const step of uploads) {
    assert.equal(step.with['retention-days'], 90);
    assert.equal(step.with.path, '${{ runner.temp }}/release');
  }
  assert.equal(uploads[1].if, 'always()');
  const adapter = await readFile(join(repoRoot, 'deploy/engine/providers.py'), 'utf8');
  assert.match(adapter, /'--prebuilt'/);
  assert.match(adapter, /'--prod', '--skip-domain'/);
  assert.match(adapter, /'--target=preview'/);
  const prepare = adapter.slice(adapter.indexOf('    def prepare('), adapter.indexOf('    def usable('));
  assert.doesNotMatch(prepare, /'deploy'|'aliases'/);
});

test('all shared run blocks are shell-valid and production consumes, not rebuilds, cloud images', async () => {
  const text = await workflow('cd.yml');
  const parsed = parseYaml(text);
  const runs = collectRunBlocks(parsed);
  assert.ok(runs.length > 0);
  for (const run of runs) {
    const checked = run.replace(/\$\{\{[\s\S]*?\}\}/g, 'workflow-expression');
    const result = spawnSync('bash', ['-n'], { input: checked, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  }
  const engine = await readFile(join(repoRoot, 'deploy/engine/engine.py'), 'utf8');
  assert.match(engine, /explicit-dev-provenance-required/);
  assert.match(engine, /dev-source-config-incompatible/);
});
