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
  const pending = job.steps.find((step) => step.name === 'Publish pending main fast-forward eligibility status');
  const success = job.steps.find((step) => step.name === 'Publish main fast-forward eligibility status');
  const pendingRun = pending?.run ?? '';
  const successRun = success?.run ?? '';

  assert.deepEqual(workflow.on.push.branches, ['main', 'develop']);
  assert.deepEqual(workflow.permissions, { contents: 'read' });
  assert.equal(job.name, 'main-fast-forward-eligible');
  assert.equal(job.if, "github.event_name == 'push' && github.ref == 'refs/heads/develop'");
  assert.equal(job.needs, 'build');
  assert.deepEqual(job.permissions, { contents: 'read', statuses: 'write' });
  assert.deepEqual(
    Object.values(workflow.jobs).filter((candidate) => candidate.permissions?.statuses === 'write').length,
    1,
  );
  assert.equal(pending.if, 'success() && needs.build.result == \'success\'');
  assert.equal(success.if, 'success() && needs.build.result == \'success\'');
  assert.deepEqual(pending.env, {
    CANDIDATE_SHA: '${{ steps.verify.outputs.candidate_sha }}',
    GH_TOKEN: '${{ github.token }}',
  });
  assert.deepEqual(success.env, pending.env);

  assert.match(verify, /set -euo pipefail/);
  assert.match(verify, /candidate_sha="\$\(git rev-parse HEAD\)"/);
  assert.match(verify, /\[\[ "\$candidate_sha" =~ \^\[0-9a-f\]\{40\}\$ \]\]/);
  assert.match(verify, /test "\$candidate_sha" = "\$GITHUB_SHA"/);
  assert.match(verify, /printf 'candidate_sha=%s\\n' "\$candidate_sha" >> "\$GITHUB_OUTPUT"/);
  assert.doesNotMatch(verify, /origin\/(?:main|develop)|merge-base/);

  assert.match(pendingRun, /set -euo pipefail/);
  assert.match(pendingRun, /--data '\{"state":"pending","context":"main-fast-forward-eligible"/);
  assert.match(pendingRun, /--write-out '%\{http_code\}'/);
  assert.match(pendingRun, /test "\$http_code" = '201'/);
  assert.match(pendingRun, /\.state == "pending" and \.context == "main-fast-forward-eligible" and \.sha == \$sha/);
  assert.doesNotMatch(pendingRun, /--location|--fail(?:-with-body)?/);

  assert.match(successRun, /set -euo pipefail/);
  assert.match(successRun, /git fetch --no-tags origin refs\/heads\/main refs\/heads\/develop/);
  assert.match(successRun, /head_sha="\$\(git rev-parse HEAD\)"/);
  assert.match(successRun, /remote_develop_sha="\$\(git rev-parse refs\/remotes\/origin\/develop\)"/);
  assert.match(successRun, /test "\$head_sha" = "\$CANDIDATE_SHA"/);
  assert.match(successRun, /test "\$CANDIDATE_SHA" = "\$remote_develop_sha"/);
  assert.match(successRun, /git merge-base --is-ancestor refs\/remotes\/origin\/main "\$CANDIDATE_SHA"/);
  assert.match(successRun, /--data '\{"state":"success","context":"main-fast-forward-eligible"/);
  assert.match(successRun, /--write-out '%\{http_code\}'/);
  assert.match(successRun, /test "\$http_code" = '201'/);
  assert.match(successRun, /\.state == "success" and \.context == "main-fast-forward-eligible" and \.sha == \$sha/);
  assert.doesNotMatch(successRun, /--location|--fail(?:-with-body)?|2\[0-9\]\{2\}/);

  const pendingIndex = job.steps.indexOf(pending);
  const successIndex = job.steps.indexOf(success);
  assert.ok(pendingIndex > job.steps.findIndex((step) => step.name === 'Verify develop contains main'));
  assert.equal(successIndex, job.steps.length - 1);
  assert.ok(successIndex > pendingIndex);
  assert.ok(pendingRun.indexOf('curl ') < pendingRun.indexOf('git fetch') || !pendingRun.includes('git fetch'));
  assert.ok(successRun.indexOf('git fetch') < successRun.indexOf('curl '));

  assert.match(source, /persist-credentials: false/);
  assert.match(source, /GH_TOKEN: \$\{\{ github\.token \}\}/);
  assert.doesNotMatch(source, /gh api .*statuses|git (push|update-ref)|x-access-token|secrets\.(?:PAT|TOKEN)|personal access/i);
});
