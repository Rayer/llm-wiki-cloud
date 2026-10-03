// Actions artifact transport. No CI run/job/head polling; explicit artifact IDs
// are resolved once. Checkpoints are repository-private, retained for 90 days.
const fs = require('node:fs');
const path = require('node:path');
const {DefaultArtifactClient} = require('@actions/artifact');
const client = new DefaultArtifactClient();
const ARTIFACT_PAGE_SIZE = 100;
async function api(endpoint) {
  const response = await fetch(`https://api.github.com/repos/${process.env.GITHUB_REPOSITORY}/${endpoint}`, {
    headers: {Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept:'application/vnd.github+json'},
    signal: AbortSignal.timeout(30000)
  });
  if (!response.ok) throw Error('artifact API unavailable');
  return response.json();
}
function artifactPage(response, totalCount, page) {
  if (!response || !Number.isSafeInteger(response.total_count) ||
      response.total_count !== totalCount || !Array.isArray(response.artifacts)) {
    throw Error('artifact list page is inconsistent');
  }
  const expected = Math.min(ARTIFACT_PAGE_SIZE,
    Math.max(0, totalCount - (page - 1) * ARTIFACT_PAGE_SIZE));
  if (response.artifacts.length !== expected) throw Error('artifact list page coverage mismatch');
  return response.artifacts;
}
async function allArtifacts() {
  const first = await api(`actions/artifacts?per_page=${ARTIFACT_PAGE_SIZE}&page=1`);
  const totalCount = first && first.total_count;
  if (!Number.isSafeInteger(totalCount) || totalCount < 0 || !Array.isArray(first.artifacts)) {
    throw Error('artifact list response is invalid');
  }
  const pageCount = Math.max(1, Math.ceil(totalCount / ARTIFACT_PAGE_SIZE));
  const seen = new Set();
  const artifacts = [];
  for (let page = 1; page <= pageCount; page++) {
    const response = page === 1 ? first : await api(
      `actions/artifacts?per_page=${ARTIFACT_PAGE_SIZE}&page=${page}`);
    for (const artifact of artifactPage(response, totalCount, page)) {
      if (!artifact || !Number.isSafeInteger(artifact.id) || artifact.id < 1 ||
          typeof artifact.name !== 'string') throw Error('artifact list item is invalid');
      if (seen.has(artifact.id)) throw Error('duplicate artifact ID across pages');
      seen.add(artifact.id);
      artifacts.push(artifact);
    }
  }
  if (artifacts.length !== totalCount || seen.size !== totalCount) {
    throw Error('artifact list coverage is incomplete');
  }
  return artifacts;
}
function artifactFailure(error) {
  const name = error && typeof error.name === 'string' ? error.name : typeof error;
  const message = error && typeof error.message === 'string' ? error.message
    : (typeof error === 'string' ? error : '');
  return {
    exception_type: name.slice(0, 80), exception_type_truncated: name.length > 80,
    message: message.slice(0, 513), message_truncated: message.length > 513,
  };
}
function files(dir) {
  return fs.readdirSync(dir, {withFileTypes:true}).flatMap(entry => {
    if (entry.name.startsWith('.')) return [];
    if (entry.isSymbolicLink()) throw Error('artifact symlink rejected');
    const p = path.join(dir, entry.name);
    return entry.isDirectory() ? files(p) : [p];
  });
}
async function download(id, destination) {
  if (!/^[1-9][0-9]*$/.test(id)) throw Error('invalid explicit artifact ID');
  const artifact = await api(`actions/artifacts/${id}`);
  const run = await api(`actions/runs/${artifact.workflow_run.id}`);
  if (run.event !== 'workflow_dispatch' || !['.github/workflows/deploy-dev.yml', '.github/workflows/promote-production.yml', '.github/workflows/recover-deployment.yml'].includes(run.path)) throw Error('untrusted artifact workflow');
  if (artifact.expired || !artifact.name.startsWith('lwc-')) throw Error('artifact unavailable');
  await client.downloadArtifact(Number(id), {path:destination, findBy:{
    token:process.env.GH_TOKEN, repositoryOwner:process.env.GITHUB_REPOSITORY.split('/')[0],
    repositoryName:process.env.GITHUB_REPOSITORY.split('/')[1], workflowRunId:artifact.workflow_run.id
  }});
}
async function main() {
  const [op, arg, name] = process.argv.slice(2);
  if (op === 'upload') {
    await client.uploadArtifact(name, files(arg), arg, {retentionDays:90});
  } else if (op === 'download') {
    await download(arg, name);
  } else if (op === 'latest') {
    if (!['development','production'].includes(arg)) throw Error('invalid target');
    // Consume a complete repository listing before deciding latest or absence.
    const candidates = (await allArtifacts()).filter(a => a.name.startsWith(`lwc-state-${arg}-`));
    if (!candidates.length) return;
    const latest = candidates.sort((a,b)=>b.id-a.id)[0];
    if (latest.expired) throw Error('latest checkpoint expired');
    const temp = fs.mkdtempSync(path.join(require('node:os').tmpdir(), 'lwc-checkpoint-'));
    try {
      // SDK logs must not contaminate the machine-readable result.
      const log = console.log; console.log = () => {};
      await download(String(latest.id), temp);
      console.log = log;
      fs.writeFileSync(name, fs.readFileSync(path.join(temp,'state.json')), {mode:0o600});
    } finally { fs.rmSync(temp, {recursive:true, force:true}); }
  } else throw Error('unknown artifact operation');
}
main().catch(error => {
  if (process.argv[2] === 'latest') {
    console.error('LWC_ARTIFACT_CAUSE ' + JSON.stringify(artifactFailure(error)));
  } else console.error('artifact transport failed (details suppressed)');
  process.exitCode=1;
});
