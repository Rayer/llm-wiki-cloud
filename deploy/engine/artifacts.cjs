// Actions artifact transport. No CI run/job/head polling; explicit artifact IDs
// are resolved once. Checkpoints are repository-private, retained for 90 days.
const fs = require('node:fs');
const path = require('node:path');
const {DefaultArtifactClient} = require('@actions/artifact');
const client = new DefaultArtifactClient();
async function api(endpoint) {
  const response = await fetch(`https://api.github.com/repos/${process.env.GITHUB_REPOSITORY}/${endpoint}`, {
    headers: {Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept:'application/vnd.github+json'},
    signal: AbortSignal.timeout(30000)
  });
  if (!response.ok) throw Error('artifact API unavailable');
  return response.json();
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
    // One bounded latest-state lookup, distinct from CI eligibility machinery.
    const response = await api('actions/artifacts?per_page=100');
    const candidates = response.artifacts.filter(a => a.name.startsWith(`lwc-state-${arg}-`));
    if (!candidates.length) {
      if (response.total_count >= 100) throw Error('target checkpoint outside bounded lookup');
      return;
    }
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
main().catch(() => {console.error('artifact transport failed (details suppressed)'); process.exitCode=1;});
