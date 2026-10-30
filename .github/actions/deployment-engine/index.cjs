// A JavaScript action receives the Actions runtime artifact credentials. Child
// processes inherit them; nothing copies credentials into logs or artifacts.
const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const temp = process.env.RUNNER_TEMP;
const executorSha = process.env.EXECUTOR_SHA;
if (!/^[0-9a-f]{40}$/.test(executorSha || '')) process.exit(1);
function run(command, args) {
  const result = spawnSync(command,args,{stdio:'inherit',env:process.env,timeout:6900000});
  if (result.error || result.status !== 0) process.exit(result.status || 1);
}
const entry = ['deploy/engine/engine.py'];
function retainedDirectory(id, name) {
  if (!id || id === '0') return null;
  const directory=path.join(temp,name);
  const marker=path.join(temp,`gat4-${name}-artifact-id`);
  if (fs.existsSync(marker)) {
    if (fs.readFileSync(marker,'utf8').trim() !== id || !fs.existsSync(directory)) process.exit(1);
    return directory;
  }
  if (fs.existsSync(directory)) process.exit(1);
  run('node',['deploy/engine/artifacts.cjs','download',id,directory]);
  fs.writeFileSync(marker,`${id}\n`,{mode:0o600,flag:'wx'});
  return directory;
}
function admissionArgs(operation, directory, reuse, dev) {
  const args=[...entry,operation,'--directory',directory,
    '--environment',process.env.TARGET,'--source',process.env.SOURCE,
    '--components',process.env.COMPONENTS,'--tag',process.env.RELEASE_TAG,
    '--executor-sha',executorSha];
  if (reuse) args.push('--reuse',reuse);
  if (dev) args.push('--dev',dev);
  if(dev) args.push('--dev-reference',process.env.DEV_ID);
  if (process.env.GAT4_RELEASE_SOURCE_MODE)
    args.push('--release-source-mode',process.env.GAT4_RELEASE_SOURCE_MODE);
  const authSource=[
    ['--auth-repository-id',process.env.AUTH_REPOSITORY_ID],
    ['--auth-revision',process.env.AUTH_REVISION],
    ['--auth-tree-sha256',process.env.AUTH_TREE_SHA256],
    ['--auth-artifact-id',process.env.AUTH_ARTIFACT_ID],
    ['--auth-run-id',process.env.AUTH_RUN_ID],
    ['--auth-run-attempt',process.env.AUTH_RUN_ATTEMPT],
  ];
  if(authSource.some(([,value])=>value && value !== '0')) {
    for(const [flag,value] of authSource) args.push(flag,value || '');
  }
  return args;
}
if (process.env.INPUT_OPERATION === 'auth-preflight' || process.env.INPUT_OPERATION === 'prepare') {
  const reuse=retainedDirectory(process.env.REUSE_ID,'reuse');
  const dev=retainedDirectory(process.env.DEV_ID,'dev');
  const preflight=process.env.INPUT_OPERATION === 'auth-preflight';
  const command=admissionArgs(preflight ? 'auth-preflight' : 'prepare',
    path.join(temp,preflight ? 'gat4-auth-preflight' : 'release'),reuse,dev);
  run('python3',command);
} else if (process.env.INPUT_OPERATION === 'runtime') {
  const op = process.env.OPERATION === 'release' ? 'deploy' : process.env.OPERATION;
  if (!['deploy','rollback','reactivate','tag','readback'].includes(op)) process.exit(1);
  const args=[...entry,op,'--directory',path.join(temp,'release'),
    '--components',process.env.COMPONENTS,'--executor-sha',executorSha];
  if (process.env.INPUT_FORCE === 'true') {
    if (op !== 'deploy' || process.env.TARGET !== 'development') process.exit(1);
    args.push('--force');
  }
  run('python3',args);
} else process.exit(1);
