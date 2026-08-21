import assert from 'node:assert/strict';
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { load as parseYaml } from 'js-yaml';

const workflowPath = join(new URL('..', import.meta.url).pathname, '.github/workflows/ci.yml');
const candidateSha = 'a'.repeat(40);

function commandIndex(source, command) {
  return source.indexOf(command);
}

async function runShell(run, { candidate = candidateSha, head = candidateSha, remoteDevelop = candidateSha, ancestor = 0, ghExit = 0 } = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'lwc-292-ci-'));
  try {
    await writeFile(join(directory, 'gh'), `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_LOG"
exit "$FAKE_GH_EXIT"
`);
    await writeFile(join(directory, 'git'), `#!/bin/sh
case "$*" in
  "rev-parse HEAD") printf '%s\\n' "$FAKE_HEAD_SHA" ;;
  "rev-parse refs/remotes/origin/develop") printf '%s\\n' "$FAKE_REMOTE_DEVELOP_SHA" ;;
  fetch*) exit 0 ;;
  "merge-base --is-ancestor refs/remotes/origin/main "*) exit "$FAKE_ANCESTOR_EXIT" ;;
  *) exit 99 ;;
esac
`);
    await chmod(join(directory, 'gh'), 0o755);
    await chmod(join(directory, 'git'), 0o755);
    const logPath = join(directory, 'gh.log');
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', run], {
      env: {
        ...process.env,
        PATH: `${directory}:${process.env.PATH}`,
        CANDIDATE_SHA: candidate,
        GITHUB_SHA: candidate,
        GITHUB_REPOSITORY: 'example/repo',
        GH_TOKEN: 'workflow-token',
        FAKE_HEAD_SHA: head,
        FAKE_REMOTE_DEVELOP_SHA: remoteDevelop,
        FAKE_ANCESTOR_EXIT: String(ancestor),
        FAKE_GH_EXIT: String(ghExit),
        FAKE_GH_LOG: logPath,
      },
      encoding: 'utf8',
    });
    return { ...result, ghLog: await readFile(logPath, 'utf8').catch(() => '') };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

test('CI main fast-forward eligibility is an ordered fail-closed exact-head status bridge', async () => {
  const source = await readFile(workflowPath, 'utf8');
  const workflow = parseYaml(source);
  const job = workflow.jobs['main-fast-forward-eligible'];
  const [pending, checkout, verify, success] = job.steps;
  const pendingRun = pending.run;
  const verifyRun = verify.run;
  const successRun = success.run;

  assert.deepEqual(workflow.on.push.branches, ['main', 'develop']);
  assert.deepEqual(workflow.permissions, { contents: 'read' });
  assert.equal(job.name, 'main-fast-forward-eligible');
  assert.equal(job.if, "${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/develop' }}");
  assert.equal(job.needs, 'build');
  assert.deepEqual(job.permissions, { contents: 'read', statuses: 'write' });
  assert.equal(Object.values(workflow.jobs).filter((candidate) => candidate.permissions?.statuses === 'write').length, 1);
  assert.deepEqual(job.steps.map((step) => step.name), [
    'Revoke stale main fast-forward eligibility status',
    'Check out exact candidate',
    'Verify develop contains main',
    'Publish main fast-forward eligibility status',
  ]);

  assert.equal(pending.if, 'always()');
  assert.deepEqual(pending.env, {
    CANDIDATE_SHA: '${{ github.sha }}',
    GH_TOKEN: '${{ github.token }}',
  });
  assert.match(pendingRun, /^\s*set -euo pipefail/m);
  assert.match(pendingRun, /\[\[ "\$CANDIDATE_SHA" =~ \^\[0-9a-f\]\{40\}\$ \]\]/);
  assert.match(pendingRun, /^\s*gh api --method POST "repos\/\$\{GITHUB_REPOSITORY\}\/statuses\/\$\{CANDIDATE_SHA\}" --field state=pending --field context=main-fast-forward-eligible --field description='Validating exact develop head for main fast-forward eligibility'$/m);
  assert.doesNotMatch(pendingRun, /needs\.build|steps\.|git |checkout|GITHUB_OUTPUT|curl|jq|\|/);

  assert.equal(checkout.uses, 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262');
  assert.deepEqual(checkout.with, { 'fetch-depth': 0, 'persist-credentials': false });

  assert.equal(verify.if, "needs.build.result == 'success'");
  assert.match(verifyRun, /candidate_sha="\$GITHUB_SHA"/);
  assert.match(verifyRun, /\[\[ "\$candidate_sha" =~ \^\[0-9a-f\]\{40\}\$ \]\]/);
  assert.match(verifyRun, /head_sha="\$\(git rev-parse HEAD\)"/);
  assert.match(verifyRun, /test "\$head_sha" = "\$candidate_sha"/);
  assert.match(verifyRun, /git fetch --no-tags origin refs\/heads\/main refs\/heads\/develop/);
  assert.match(verifyRun, /remote_develop_sha="\$\(git rev-parse refs\/remotes\/origin\/develop\)"/);
  assert.match(verifyRun, /test "\$candidate_sha" = "\$remote_develop_sha"/);
  assert.match(verifyRun, /git merge-base --is-ancestor refs\/remotes\/origin\/main "\$candidate_sha"/);
  assert.doesNotMatch(verifyRun, /gh api|statuses|curl|jq|GITHUB_OUTPUT|\|/);

  assert.equal(success.if, "needs.build.result == 'success'");
  assert.deepEqual(success.env, {
    CANDIDATE_SHA: '${{ github.sha }}',
    GH_TOKEN: '${{ github.token }}',
  });
  assert.match(successRun, /^\s*set -euo pipefail/m);
  assert.match(successRun, /\[\[ "\$CANDIDATE_SHA" =~ \^\[0-9a-f\]\{40\}\$ \]\]/);
  assert.match(successRun, /git fetch --no-tags origin refs\/heads\/main refs\/heads\/develop/);
  assert.match(successRun, /head_sha="\$\(git rev-parse HEAD\)"/);
  assert.match(successRun, /remote_develop_sha="\$\(git rev-parse refs\/remotes\/origin\/develop\)"/);
  assert.match(successRun, /test "\$head_sha" = "\$CANDIDATE_SHA"/);
  assert.match(successRun, /test "\$CANDIDATE_SHA" = "\$remote_develop_sha"/);
  assert.match(successRun, /git merge-base --is-ancestor refs\/remotes\/origin\/main "\$CANDIDATE_SHA"/);
  assert.match(successRun, /^\s*gh api --method POST "repos\/\$\{GITHUB_REPOSITORY\}\/statuses\/\$\{CANDIDATE_SHA\}" --field state=success --field context=main-fast-forward-eligible --field description='Exact develop head is eligible for main fast-forward'$/m);
  assert.doesNotMatch(successRun, /curl|jq|GITHUB_OUTPUT|\|/);

  const finalPost = commandIndex(successRun, 'gh api --method POST');
  assert.ok(finalPost >= 0);
  assert.equal(successRun.trim().slice(finalPost).split('\n').length, 1);
  for (const check of ['git fetch', 'git rev-parse HEAD', 'git rev-parse refs/remotes/origin/develop', 'git merge-base --is-ancestor']) {
    assert.ok(commandIndex(successRun, check) < finalPost, `${check} must precede final POST`);
  }
  assert.equal(job.steps.at(-1), success);
  assert.doesNotMatch(source, /curl|jq|GITHUB_OUTPUT|steps\.verify\.outputs|git (push|update-ref)|x-access-token|secrets\.(?:PAT|TOKEN)|personal access/i);
});

test('CI status bridge shell gates reject stale, malformed, non-ancestral, and failed API cases', async () => {
  const workflow = parseYaml(await readFile(workflowPath, 'utf8'));
  const [pending, , , success] = workflow.jobs['main-fast-forward-eligible'].steps;

  assert.equal((await runShell(pending.run)).status, 0);
  assert.match((await runShell(pending.run)).ghLog, /state=pending/);
  assert.notEqual((await runShell(pending.run, { ghExit: 1 })).status, 0);

  for (const scenario of [
    { candidate: 'A'.repeat(40) },
    { candidate: 'not-a-sha' },
    { head: 'b'.repeat(40) },
    { remoteDevelop: 'c'.repeat(40) },
    { ancestor: 1 },
  ]) {
    const result = await runShell(success.run, scenario);
    assert.notEqual(result.status, 0, `unexpected success for ${JSON.stringify(scenario)}`);
    assert.doesNotMatch(result.ghLog, /state=success/);
  }

  const failedSuccessApi = await runShell(success.run, { ghExit: 1 });
  assert.notEqual(failedSuccessApi.status, 0);
  assert.match(failedSuccessApi.ghLog, /state=success/);

  const successResult = await runShell(success.run);
  assert.equal(successResult.status, 0);
  assert.match(successResult.ghLog, /state=success/);
});
