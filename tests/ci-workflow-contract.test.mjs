import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { load as parseYaml } from 'js-yaml';

const workflowPath = join(new URL('..', import.meta.url).pathname, '.github/workflows/ci.yml');

test('CI main fast-forward eligibility publishes a fail-closed exact-head status bridge', async () => {
  const source = await readFile(workflowPath, 'utf8');
  const workflow = parseYaml(source);
  const job = workflow.jobs['main-fast-forward-eligible'];
  const verify = job.steps.find((step) => step.name === 'Verify develop contains main')?.run ?? '';
  const status = job.steps.find((step) => step.name === 'Publish main fast-forward eligibility status');
  const publish = status?.run ?? '';

  assert.deepEqual(workflow.on.push.branches, ['main', 'develop']);
  assert.equal(job.name, 'main-fast-forward-eligible');
  assert.equal(job.if, "github.event_name == 'push' && github.ref == 'refs/heads/develop'");
  assert.equal(job.needs, 'build');
  assert.deepEqual(job.permissions, { contents: 'read', statuses: 'write' });
  assert.equal(status.if, 'success() && needs.build.result == \'success\'');
  assert.deepEqual(status.env, {
    CANDIDATE_SHA: '${{ steps.verify.outputs.candidate_sha }}',
    GH_TOKEN: '${{ github.token }}',
  });

  assert.match(verify, /git rev-parse HEAD/);
  assert.match(verify, /test "\$candidate_sha" = "\$GITHUB_SHA"/);
  assert.match(verify, /test "\$candidate_sha" = "\$remote_develop_sha"/);
  assert.match(verify, /git merge-base --is-ancestor refs\/remotes\/origin\/main "\$candidate_sha"/);
  assert.match(publish, /\[\[ "\$CANDIDATE_SHA" =~ \^\[0-9a-f\]\{40\}\$ \]\]/);
  assert.match(publish, /git fetch --no-tags origin refs\/heads\/main refs\/heads\/develop/);
  assert.match(publish, /git rev-parse HEAD/);
  assert.match(publish, /git rev-parse refs\/remotes\/origin\/develop/);
  assert.match(publish, /git merge-base --is-ancestor refs\/remotes\/origin\/main "\$CANDIDATE_SHA"/);
  assert.match(publish, /--fail(?:-with-body)?/);
  assert.match(publish, /"context":"main-fast-forward-eligible"/);
  assert.match(publish, /"state":"success"/);
  assert.match(publish, /statuses\/\$\{CANDIDATE_SHA\}/);
  assert.equal(publish.trim().split('\n').at(-1), '  --data \'{"state":"success","context":"main-fast-forward-eligible","description":"Exact develop head is eligible for main fast-forward"}\'');
  assert.match(publish, /curl --fail --silent --show-error --request POST/);

  const statusIndex = job.steps.indexOf(status);
  assert.equal(statusIndex, job.steps.length - 1);
  assert.ok(statusIndex > 0);
  assert.match(source, /persist-credentials: false/);
  assert.doesNotMatch(source, /gh api .*statuses|git (push|update-ref)|x-access-token|secrets\.(?:PAT|TOKEN)|personal access/i);
});
