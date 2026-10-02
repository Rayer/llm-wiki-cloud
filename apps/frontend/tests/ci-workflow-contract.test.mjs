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
  assert.deepEqual(files, ['cd.yml', 'ci.yml', 'deploy-dev.yml', 'promote-production.yml', 'provision-exportjob-dev.yml', 'recover-deployment.yml']);
  for (const [file, environment, branch] of [['deploy-dev.yml', 'development', 'develop'], ['promote-production.yml', 'production', 'main']]) {
    const parsed = parseYaml(await workflow(file));
    assert.deepEqual(Object.keys(parsed.on), ['workflow_dispatch']);
    assert.deepEqual(Object.keys(parsed.on.workflow_dispatch.inputs), ['components', 'release_tag', 'artifact_id', 'dev_artifact_id']);
    assert.equal(parsed.jobs.release.with.environment, environment);
    assert.equal(parsed.jobs.release.if, `github.ref == 'refs/heads/${branch}'`);
    assert.equal(parsed.jobs.release.with.source_sha, '${{ github.sha }}');
    assert.equal(parsed.jobs.release.secrets, 'inherit');
  }
  const recovery = parseYaml(await workflow('recover-deployment.yml'));
  assert.deepEqual(recovery.on.workflow_dispatch.inputs.operation.options, ['rollback', 'reactivate', 'deploy', 'tag', 'readback']);
  assert.equal(recovery.jobs.recovery.uses, './.github/workflows/cd.yml');
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
  assert.equal(job.environment, "${{ inputs.environment == 'production' && 'Production' || 'Development' }}");
  const steps = job.steps;
  const prepare = steps.findIndex(step => step.with?.operation === 'prepare');
  const barrier = steps.findIndex(step => step.id === 'ready');
  const runtime = steps.findIndex(step => step.with?.operation === 'runtime');
  assert.ok(prepare >= 0 && prepare < barrier && barrier < runtime);
  assert.equal(steps[prepare].if, "inputs.operation == 'release'");
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
