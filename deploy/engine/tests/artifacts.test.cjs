// TEST ONLY: exercise production artifact transport with offline API/SDK boundaries.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname,'../artifacts.cjs'),'utf8');
async function execute(op,arg,name,responses, directory) {
  const uploads=[];
  const process={argv:['node','artifacts.cjs',op,arg,name],env:{GITHUB_REPOSITORY:'test/repo',GH_TOKEN:'TEST_ONLY'},exitCode:0};
  class Client {
    async uploadArtifact(...args) {uploads.push(args);}
    async downloadArtifact(id, options) {
      assert.equal(options.findBy.token,'TEST_ONLY');
      fs.mkdirSync(options.path,{recursive:true});
      fs.writeFileSync(path.join(options.path,'state.json'),JSON.stringify({plan:'retained',sequence:id,status:'success'}));
    }
  }
  const calls=[];
  await vm.runInNewContext(source,{
    require:n=>n==='@actions/artifact'?{DefaultArtifactClient:Client}:require(n),
    process, console:{log(){},error(){}}, AbortSignal,
    fetch:async(url)=>{
      calls.push(url);
      const key=url.split('/repos/test/repo/')[1];
      assert.ok(key in responses,`unexpected API ${key}`);
      return {ok:true,json:async()=>responses[key]};
    }
  });
  return {code:process.exitCode,uploads,calls,directory};
}
const record={id:12,name:'lwc-state-development-a',expired:false,workflow_run:{id:42}};
const run={event:'workflow_dispatch',path:'.github/workflows/deploy-dev.yml'};
test('upload includes receipts but omits transient lock/latest files',async()=>{
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'lwc-transport-test-'));
  try {
    fs.writeFileSync(path.join(dir,'state.json'),'{}');fs.writeFileSync(path.join(dir,'.latest.json'),'private');
    const result=await execute('upload',dir,'lwc-state-development-a',{});
    assert.equal(result.code,0);assert.deepEqual(result.uploads[0][1],[path.join(dir,'state.json')]);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
test('latest target checkpoint uses artifact identity and writes exact state',async()=>{
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'lwc-transport-test-'));
  try {
    const dest=path.join(dir,'latest.json');
    const result=await execute('latest','development',dest,{
      'actions/artifacts?per_page=100':{total_count:1,artifacts:[record]},
      'actions/artifacts/12':record,'actions/runs/42':run
    });
    assert.equal(result.code,0);assert.equal(JSON.parse(fs.readFileSync(dest)).sequence,12);
    assert.equal(result.calls.length,3);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
test('expired, untrusted and out-of-bound checkpoints fail closed',async()=>{
  for(const responses of [
    {'actions/artifacts?per_page=100':{total_count:100,artifacts:[]}},
    {'actions/artifacts?per_page=100':{total_count:1,artifacts:[{...record,expired:true}]}},
    {'actions/artifacts?per_page=100':{total_count:1,artifacts:[record]},'actions/artifacts/12':record,
      'actions/runs/42':{event:'push',path:'.github/workflows/ci.yml'}}
  ]) assert.equal((await execute('latest','development','/unused-test-only-path',responses)).code,1);
});
test('explicit download rejects nonnumeric references before any network',async()=>{
  const result=await execute('download','../../bad','/unused-test-only-path',{});
  assert.equal(result.code,1);assert.equal(result.calls.length,0);
});
