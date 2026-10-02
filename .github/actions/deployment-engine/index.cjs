// A JavaScript action receives the Actions runtime artifact credentials. Child
// processes inherit them; nothing copies credentials into logs or artifacts.
const {spawnSync} = require('node:child_process');
const path = require('node:path');
const temp = process.env.RUNNER_TEMP;
function run(command, args) {
  const result = spawnSync(command,args,{stdio:'inherit',env:process.env,timeout:6900000});
  if (result.error || result.status !== 0) process.exit(result.status || 1);
}
const entry = ['deploy/engine/engine.py'];
if (process.env.INPUT_OPERATION === 'prepare') {
  const args=[];
  for (const [id,dir,flag] of [[process.env.REUSE_ID,'reuse','--reuse'],[process.env.DEV_ID,'dev','--dev']]) {
    if (!id) continue;
    run('node',['deploy/engine/artifacts.cjs','download',id,path.join(temp,dir)]);
    args.push(flag,path.join(temp,dir));
  }
  if(process.env.DEV_ID) args.push('--dev-reference',process.env.DEV_ID);
  run('python3',[...entry,'prepare','--directory',path.join(temp,'release'),
    '--environment',process.env.TARGET,'--source',process.env.SOURCE,
    '--components',process.env.COMPONENTS,'--tag',process.env.RELEASE_TAG,...args]);
} else if (process.env.INPUT_OPERATION === 'runtime') {
  const op = process.env.OPERATION === 'release' ? 'deploy' : process.env.OPERATION;
  if (!['deploy','rollback','reactivate','tag','readback'].includes(op)) process.exit(1);
  run('python3',[...entry,op,'--directory',path.join(temp,'release'),'--components',process.env.COMPONENTS]);
} else process.exit(1);
