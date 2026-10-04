// TEST ONLY: exercise production artifact transport with offline API/SDK boundaries.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname,'../artifacts.cjs'),'utf8');
async function execute(op,arg,name,responses, directory, downloadedStates={}) {
  const uploads=[];
  const errors=[];
  const process={argv:['node','artifacts.cjs',op,arg,name],env:{GITHUB_REPOSITORY:'test/repo',GH_TOKEN:'TEST_ONLY'},exitCode:0};
  class Client {
    async uploadArtifact(...args) {uploads.push(args);}
    async downloadArtifact(id, options) {
      assert.equal(options.findBy.token,'TEST_ONLY');
      fs.mkdirSync(options.path,{recursive:true});
      fs.writeFileSync(path.join(options.path,'state.json'),JSON.stringify(
        downloadedStates[id] || {plan:'retained',sequence:id,status:'success'}));
    }
  }
  const calls=[];
  await vm.runInNewContext(source,{
    require:n=>n==='@actions/artifact'?{DefaultArtifactClient:Client}:require(n),
    process, AbortSignal,
    fetch:async(url)=>{
      calls.push(url);
      const key=url.split('/repos/test/repo/')[1];
      assert.ok(key in responses,`unexpected API ${key}`);
      if (responses[key] instanceof Error) throw responses[key];
      return {ok:true,json:async()=>responses[key]};
    },
    console:{log(){},error(...parts){errors.push(parts.join(' '))}}
  });
  return {code:process.exitCode,uploads,calls,errors,directory};
}
const record={id:12,name:'lwc-state-development-a',expired:false,workflow_run:{id:42}};
const run={event:'workflow_dispatch',path:'.github/workflows/deploy-dev.yml'};
const listKey=page=>`actions/artifacts?per_page=100&page=${page}`;
const page=(artifacts,total_count=artifacts.length)=>({total_count,artifacts});
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
      [listKey(1)]:page([record]),
      'actions/artifacts/12':record,'actions/runs/42':run
    });
    assert.equal(result.code,0);assert.equal(JSON.parse(fs.readFileSync(dest)).sequence,12);
    assert.equal(result.calls.length,3);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
test('latest uses checkpoint sequence within the same workflow attempt, not artifact ID',async()=>{
  const plan='a'.repeat(64), prefix=plan.slice(0,16), runId=42, attempt=1;
  const sequence9={id:11285623722,name:`lwc-state-development-${prefix}-${runId}-${attempt}-9`,
    expired:false,workflow_run:{id:runId}};
  const sequence5={id:11286159266,name:`lwc-state-development-${prefix}-${runId}-${attempt}-5`,
    expired:false,workflow_run:{id:runId}};
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'lwc-transport-test-'));
  try {
    const dest=path.join(dir,'latest.json');
    const result=await execute('latest','development',dest,{
      [listKey(1)]:page([sequence5,sequence9]),
      [`actions/artifacts/${sequence9.id}`]:sequence9,
      [`actions/artifacts/${sequence5.id}`]:sequence5,
      'actions/runs/42':run,
    },undefined,{
      [sequence9.id]:{plan,sequence:9,status:'unknown'},
      [sequence5.id]:{plan,sequence:5,status:'deploying'},
    });
    assert.equal(result.code,0);
    assert.equal(JSON.parse(fs.readFileSync(dest)).sequence,9);
    assert.equal(result.calls.includes(`https://api.github.com/repos/test/repo/actions/artifacts/${sequence9.id}`),true);
    assert.equal(result.calls.includes(`https://api.github.com/repos/test/repo/actions/artifacts/${sequence5.id}`),false);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
test('latest consumes every page beyond 400 before selecting the highest matching ID',async()=>{
  const records=Array.from({length:501},(_,i)=>({id:i+1,name:`other-${i}`,workflow_run:{id:42}}));
  records[7]={...record,id:7000,name:'lwc-state-development-older'};
  records[500]={...record,id:9000,name:'lwc-state-development-newest'};
  const responses={};
  for(let i=0;i<6;i++)responses[listKey(i+1)]=page(records.slice(i*100,(i+1)*100),501);
  responses['actions/artifacts/9000']={...records[500],expired:false};
  responses['actions/runs/42']=run;
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'lwc-transport-test-'));
  try {
    const dest=path.join(dir,'latest.json');
    const result=await execute('latest','development',dest,responses);
    assert.equal(result.code,0);
    assert.equal(JSON.parse(fs.readFileSync(dest)).sequence,9000);
    assert.deepEqual(result.calls.slice(0,6),Array.from({length:6},(_,i)=>
      `https://api.github.com/repos/test/repo/${listKey(i+1)}`));
    assert.ok(result.calls.includes('https://api.github.com/repos/test/repo/actions/artifacts/9000'));
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
test('complete listing with no matching checkpoint is absence only after all pages',async()=>{
  const records=Array.from({length:401},(_,i)=>({id:i+1,name:`unrelated-${i}`}));
  const responses={};
  for(let i=0;i<5;i++)responses[listKey(i+1)]=page(records.slice(i*100,(i+1)*100),401);
  const result=await execute('latest','development','/unused-test-only-path',responses);
  assert.equal(result.code,0);
  assert.equal(result.calls.length,5);
  assert.equal(result.calls.some(url=>url.includes('/actions/artifacts/')),false);
});
test('incomplete, inconsistent and duplicate pages fail closed with a cause',async()=>{
  const first=Array.from({length:100},(_,i)=>({id:i+1,name:`other-${i}`}));
  const cases=[
    {[listKey(1)]:page(first,101),[listKey(2)]:page([],101)},
    {[listKey(1)]:page(first,101),[listKey(2)]:page([{id:101,name:'other'}],102)},
    {[listKey(1)]:page(first,101),[listKey(2)]:page([{id:1,name:'duplicate'}],101)},
  ];
  for(const responses of cases){
    const result=await execute('latest','development','/unused-test-only-path',responses);
    assert.equal(result.code,1);
    assert.equal(result.calls.filter(url=>url.includes('/actions/artifacts/')).length,0);
    const cause=JSON.parse(result.errors[0].slice('LWC_ARTIFACT_CAUSE '.length));
    assert.equal(cause.exception_type,'Error');
    assert.match(cause.message,/page coverage mismatch|page is inconsistent|duplicate artifact ID/);
  }
});
test('latest API source failure preserves fixed cause metadata',async()=>{
  const timeout=new Error('test-only API timeout');
  timeout.name='TimeoutError';
  const result=await execute('latest','development','/unused-test-only-path',{[listKey(1)]:timeout});
  assert.equal(result.code,1);
  const cause=JSON.parse(result.errors[0].slice('LWC_ARTIFACT_CAUSE '.length));
  assert.deepEqual(cause,{exception_type:'TimeoutError',exception_type_truncated:false,
    message:'test-only API timeout',message_truncated:false});
});
test('newest expired checkpoint does not fall back to an older usable one',async()=>{
  const older={...record,id:11};
  const newest={...record,id:13,expired:true};
  const result=await execute('latest','development','/unused-test-only-path',{
    [listKey(1)]:page([older,newest]),
  });
  assert.equal(result.code,1);
  assert.equal(result.calls.some(url=>url.includes('/actions/artifacts/11'))||
    result.calls.some(url=>url.includes('/actions/artifacts/13')),false);
});
test('untrusted checkpoint workflow fails closed',async()=>{
  for(const responses of [
    {[listKey(1)]:page([record]),'actions/artifacts/12':record,
      'actions/runs/42':{event:'push',path:'.github/workflows/ci.yml'}}
  ]) assert.equal((await execute('latest','development','/unused-test-only-path',responses)).code,1);
});
test('explicit download rejects nonnumeric references before any network',async()=>{
  const result=await execute('download','../../bad','/unused-test-only-path',{});
  assert.equal(result.code,1);assert.equal(result.calls.length,0);
});
