import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { load as parseYaml } from 'js-yaml';

test('CI main fast-forward eligibility is read-only and exact-head gated', async () => {
  const source = await readFile(join(new URL('..', import.meta.url).pathname, '.github/workflows/ci.yml'), 'utf8');
  const workflow = parseYaml(source);
  const job = workflow.jobs['main-fast-forward-eligible'];
  const normalize = (value) => value.replace(/\r\n/g, '\n').trim();
  const expectedVerifyRun = normalize([
    'set -eu',
    'git fetch --no-tags origin refs/heads/main refs/heads/develop',
    'test "$(git rev-parse refs/remotes/origin/develop)" = "$GITHUB_SHA"',
    'git merge-base --is-ancestor refs/remotes/origin/main "$GITHUB_SHA"',
  ].join('\n'));
  const expectedJob = {
    name: 'main-fast-forward-eligible',
    if: "github.event_name == 'push' && github.ref == 'refs/heads/develop'",
    'runs-on': 'ubuntu-latest',
    needs: 'build',
    steps: [
      {
        uses: 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
        with: { 'fetch-depth': 0, 'persist-credentials': false },
      },
      { name: 'Verify develop contains main', run: expectedVerifyRun },
    ],
  };

  assert.deepEqual(workflow.on.push.branches, ['main', 'develop']);
  assert.deepEqual({ ...job, steps: job.steps.map((step) => step.run ? { ...step, run: normalize(step.run) } : step) }, expectedJob);
  assert.doesNotMatch(source, /git (push|update-ref)|gh api .*statuses|curl .*statuses/);
});
