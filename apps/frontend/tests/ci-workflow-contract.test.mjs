import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { load as parseYaml } from 'js-yaml';

const workflowPath = join(new URL('../../..', import.meta.url).pathname, '.github/workflows/ci.yml');
const repoRoot = new URL('../../..', import.meta.url).pathname;
const workflowDirectory = join(repoRoot, '.github/workflows');

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

test('exactly one active workflow owns main fast-forward eligibility', async () => {
  const files = (await readdir(workflowDirectory)).filter((file) => file.endsWith('.yml')).sort();
  const producers = [];
  for (const file of files) {
    const source = await readFile(join(workflowDirectory, file), 'utf8');
    if (/context(?:=|:\s*)main-fast-forward-eligible/.test(source)) producers.push(file);
  }
  assert.deepEqual(producers, ['deploy-bff.yml']);
});

test('canonical CI is tests and aggregate only, without status writes', async () => {
  const source = await readFile(workflowPath, 'utf8');
  const workflow = parseYaml(source);
  assert.deepEqual(workflow.permissions, { contents: 'read' });
  assert.equal(Object.values(workflow.jobs).filter((job) => job.permissions?.statuses === 'write').length, 0);
  assert.doesNotMatch(source, /main-fast-forward-eligible|statuses:\s*write|statuses\//);
});

test('eligibility producer is dispatch-only and requires DEV readiness', async () => {
  const source = await readFile(join(workflowDirectory, 'deploy-bff.yml'), 'utf8');
  const workflow = parseYaml(source);
  const job = workflow.jobs['main-fast-forward-eligible'];
  assert.deepEqual(Object.keys(workflow.on), ['workflow_dispatch']);
  assert.equal(job.if, "${{ always() && github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/develop' }}");
  assert.deepEqual(job.needs, ['test-and-deploy', 'production-promotion-ready']);
  assert.deepEqual(job.permissions, { contents: 'read', statuses: 'write' });
  assert.match(source, /-f context=main-fast-forward-eligible/);
});

test('canonical CI keeps component working directories and aggregate gate dependencies explicit', async () => {
  const workflow = parseYaml(await readFile(workflowPath, 'utf8'));
  assert.deepEqual(workflow.jobs.bff.defaults.run, { 'working-directory': 'apps/bff' });
  for (const jobName of ['lint', 'typecheck', 'test', 'frontend-build']) {
    assert.deepEqual(workflow.jobs[jobName].defaults.run, { 'working-directory': 'apps/frontend' });
  }
  const aggregate = workflow.jobs.build;
  assert.equal(aggregate.name, 'canonical-ci');
  assert.equal(aggregate.if, '${{ always() }}');
  assert.deepEqual(aggregate.needs, ['bff', 'frontend-build', 'local-smoke', 'workflow-source']);
  for (const jobName of aggregate.needs) {
    assert.match(aggregate.steps[0].run, new RegExp(`needs\\.${jobName}\\.result`));
  }
});

test('active root deployment workflows are monorepo-rooted, shell-valid, and manual-only', async () => {
  const files = (await readdir(workflowDirectory)).filter((file) => file.endsWith('.yml')).sort();
  assert.ok(files.length >= 11, `expected root CI plus deployment workflows, found ${files.length}`);
  const deploymentFiles = [
    'deploy-auth.yml', 'deploy-bff.yml', 'deploy-worker.yml',
    'release-auth.yml', 'release-bff.yml', 'release-worker.yml', 'rollback-auth.yml',
    'vercel-alias-promotion.yml', 'vercel-dev-authority-reconciliation.yml',
    'vercel-dev-deployment.yml', 'vercel-production-auth-env.yml',
  ];
  const rootSources = new Map();
  let runCount = 0;
  for (const file of files) {
    const source = await readFile(join(workflowDirectory, file), 'utf8');
    const workflow = parseYaml(source);
    assert.ok(workflow && workflow.jobs, `${file} must parse as a workflow`);
    rootSources.set(file, { source, workflow });
    for (const run of collectRunBlocks(workflow)) {
      runCount += 1;
      const checked = run.replace(/\$\{\{[\s\S]*?\}\}/g, 'workflow-expression');
      const result = spawnSync('bash', ['-n'], { input: checked, encoding: 'utf8' });
      assert.equal(result.status, 0, `${file} run block has invalid shell syntax: ${result.stderr}\n${checked}`);
    }
  }
  assert.ok(runCount > 0, 'active workflows must contain executable run blocks');

  for (const file of deploymentFiles) {
    const entry = rootSources.get(file);
    assert.ok(entry, `${file} must be active at the root`);
    assert.deepEqual(Object.keys(entry.workflow.on ?? {}).sort(), ['workflow_dispatch'], `${file} must remain manual-only`);
    assert.doesNotMatch(entry.source, /(?:Rayer\/llm-wiki-(?:bff|frontend)|apps\/(?:bff|frontend)\/\.github\/workflows)/);
  }

  const bff = rootSources.get('deploy-bff.yml').source;
  const auth = rootSources.get('deploy-auth.yml').source;
  const worker = rootSources.get('deploy-worker.yml').source;
  assert.match(bff, /gcloud builds submit apps\/bff[\s\\\n]+--config apps\/bff\/cloudbuild-bff\.yaml/);
  assert.match(auth, /gcloud builds submit apps\/bff[\s\\\n]+--config apps\/bff\/cloudbuild-auth\.yaml/);
  assert.match(worker, /docker build[\s\S]*-f apps\/bff\/cmd\/olw_worker\/Dockerfile[\s\S]* apps\/bff/);
  assert.match(bff, /bff-image-digest-\$\{\{ steps\.image_digest\.outputs\.commit_sha \}\}/);
  assert.match(auth, /auth-image-digest-\$\{\{ steps\.image_digest\.outputs\.commit_sha \}\}/);
  assert.match(worker, /worker-image-digest-\$\{\{ steps\.source\.outputs\.candidate_sha \}\}/);

  for (const file of deploymentFiles.filter((name) => name.startsWith('vercel-'))) {
    const { source, workflow } = rootSources.get(file);
    assert.match(source, /apps\/frontend\/\.github\/scripts\//);
    assert.match(source, /working-directory: apps\/frontend/);
    assert.ok(workflow.jobs, `${file} must retain its job definition`);
  }
});
