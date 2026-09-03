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

test('only the canonical CI and two fixed CD entry workflows are active', async () => {
  const files = (await readdir(workflowDirectory)).filter((file) => file.endsWith('.yml')).sort();
  assert.deepEqual(files, ['cd.yml', 'ci.yml', 'deploy-dev.yml', 'promote-production.yml']);
  const dev = parseYaml(await workflow('deploy-dev.yml'));
  const production = parseYaml(await workflow('promote-production.yml'));
  assert.equal(dev.on.push, undefined);
  assert.equal(production.on.push, undefined);
  assert.deepEqual(Object.keys(dev.on.workflow_dispatch.inputs), ['components']);
  assert.deepEqual(Object.keys(production.on.workflow_dispatch.inputs), ['components']);
  assert.equal(dev.jobs.deploy.with.environment, 'Development');
  assert.equal(production.jobs.promote.with.environment, 'Production');
});

test('fixed wrappers cannot accept environment, config, or ref authority', async () => {
  for (const [name, job, branch, config] of [
    ['deploy-dev.yml', 'deploy', 'develop', 'development'],
    ['promote-production.yml', 'promote', 'main', 'production'],
  ]) {
    const text = await workflow(name);
    const parsed = parseYaml(text);
    assert.deepEqual(Object.keys(parsed.on.workflow_dispatch.inputs), ['components']);
    assert.equal(parsed.jobs[job].with.source_ref, branch);
    assert.equal(parsed.jobs[job].with.config_path, `deploy/environments/${config}.yaml`);
    assert.equal(parsed.jobs[job].with.config_environment, config);
    assert.match(text, /uses: \.\/\.github\/workflows\/cd\.yml/);
    assert.doesNotMatch(text, /inputs\.(environment|config|ref)/);
  }
});

test('shared CD validates before protected environment mutation and uploads rollback first', async () => {
  const text = await workflow('cd.yml');
  const parsed = parseYaml(text);
  assert.equal(parsed.jobs.mutate.needs, 'plan');
  assert.equal(parsed.jobs.mutate.environment, '${{ inputs.environment }}');
  assert.equal(parsed.jobs.mutate.if, "needs.plan.result == 'success'");
  const plan = text.indexOf('  plan:');
  const environment = text.indexOf('    environment:', text.indexOf('  mutate:'));
  assert.ok(plan < environment);
  assert.ok(text.indexOf('id: rollback_upload') < text.indexOf('operation: mutate'));
  assert.match(text, /if: steps\.rollback_upload\.outcome == 'success'/);
  assert.doesNotMatch(text, /run jobs execute/);
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
  assert.match(await readFile(join(repoRoot, 'deploy/cd.sh'), 'utf8'), /consume_dev_images/);
  assert.match(await readFile(join(repoRoot, 'deploy/cd.sh'), 'utf8'), /if \[\[ "\$ENVIRONMENT" == production \]\] &&/);
});
