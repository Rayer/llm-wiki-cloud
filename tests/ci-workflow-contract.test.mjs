import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { load as parseYaml } from 'js-yaml';

test('CI main fast-forward eligibility is read-only and exact-head gated', async () => {
  const source = await readFile(join(new URL('..', import.meta.url).pathname, '.github/workflows/ci.yml'), 'utf8');
  const workflow = parseYaml(source);
  const job = workflow.jobs['main-fast-forward-eligible'];
  const checkout = job.steps.find(({ uses }) => uses === 'actions/checkout@v4');
  const verify = job.steps.find(({ name }) => name === 'Verify develop contains main');

  assert.deepEqual(workflow.on.push.branches, ['main', 'develop']);
  assert.equal(job.name, 'main-fast-forward-eligible');
  assert.equal(job.if, "github.event_name == 'push' && github.ref == 'refs/heads/develop'");
  assert.equal(job.needs, 'build');
  assert.deepEqual(checkout.with, { 'fetch-depth': 0, 'persist-credentials': false });
  assert.match(verify.run, /git fetch --no-tags origin refs\/heads\/main refs\/heads\/develop/);
  assert.match(verify.run, /git rev-parse refs\/remotes\/origin\/develop.*\$GITHUB_SHA/);
  assert.match(verify.run, /git merge-base --is-ancestor refs\/remotes\/origin\/main.*\$GITHUB_SHA/);
  assert.doesNotMatch(source, /git (push|update-ref)|gh api .*statuses|curl .*statuses/);
});
