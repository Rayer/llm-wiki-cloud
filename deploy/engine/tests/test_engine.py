"""TEST ONLY offline acceptance; real production orchestration and adapters."""
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
sys.path.insert(0,str(HERE))
import engine
from support import Breakpoint, ROOT, read, write
import providers

class Acceptance(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.normalized=json.loads(subprocess.check_output(['go','run','./cmd/deploy_config','--environment','development',
            '--config',str(ROOT/'deploy/environments/development.yaml'),'--components',','.join(engine.ORDER)],cwd=ROOT/'apps/bff',text=True))

    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);bin=self.root/'bin';bin.mkdir()
        fake=HERE/'tests/fake_provider.py';fake.chmod(0o755)
        for name in ('gcloud','docker','go','curl','vercel','npm','git','gh'):(bin/name).symlink_to(fake)
        # Keep tool discovery/cache paths, never inherited CI authority or credentials.
        offline_env={k:os.environ[k] for k in ('HOME','TMPDIR','LANG','LC_ALL','SYSTEMROOT') if k in os.environ}
        self.env=patch.dict(os.environ,{**offline_env,'PATH':str(bin)+os.pathsep+os.environ['PATH'],'LWC_TEST_STATE':str(self.root/'provider.json'),
            'VERCEL_PROJECT_ID':'prj_test','VERCEL_TEAM_ID':'team_test','VERCEL_TOKEN':'test-only','GITHUB_REPOSITORY':'test/repo'},clear=True)
        self.env.start();self.addCleanup(self.env.stop)
        self.sleep=patch('providers.time.sleep');self.sleep.start();self.addCleanup(self.sleep.stop)
        self.provider={'resources':{},'revisions':{},'calls':[], 'aliases':{'wiki.dev.rayer.idv.tw':'dpl_prior'},
            'deployments':{'dpl_prior':{'id':'dpl_prior','projectId':'prj_test','teamId':'team_test','readyState':'READY'}}}
        n=self.normalized
        for c in ('auth','bff'):
            name=n[c]['service_name']; rev=name+'-prior'
            self.provider['revisions'][rev]={'metadata':{'name':rev,'annotations':{}},'spec':{
                'serviceAccountName':n[c]['runtime_service_account'], 'containers':[{'image':'prior@sha256:'+'b'*64,'env':[]}]},
                'status':{'imageDigest':'prior@sha256:'+'b'*64,'conditions':[{'type':'Ready','status':'True'}]}}
            self.provider['resources'][name]={'metadata':{'name':name},'spec':{'template':{'spec':copy.deepcopy(self.provider['revisions'][rev]['spec'])}},'status':{
                'latestCreatedRevisionName':rev,'traffic':[{'revisionName':rev,'percent':100}]}}
        for c in ('worker','exportjob'):
            cfg=n['export_job' if c=='exportjob' else c]; env=[]
            if c=='exportjob':
                env=[{'name':k,'value':v} for k,v in {'GCP_PROJECT':n['gcp']['project_id'],'BUCKET':cfg['bucket'],
                   'FIRESTORE_DATABASE_ID':cfg['firestore_database_id'],'EXPORT_SIGNING_SERVICE_ACCOUNT':cfg['signing_service_account']}.items()]
            t={'serviceAccountName':cfg['runtime_service_account'],'containers':[{'image':'prior@sha256:'+'b'*64,'env':env}],
               'timeoutSeconds':'82800','maxRetries':0}
            self.provider['resources'][cfg['job_name']]={'spec':{'template':{'spec':{'parallelism':1,'taskCount':1,'template':{'spec':t}}}}}
        self.flush()

    def flush(self):write(self.root/'provider.json',self.provider)
    def current(self):return read(self.root/'provider.json')
    def configure(self,**kw):
        self.provider=self.current();self.provider.update(kw);self.flush()
    def calls(self,verb):return [x for x in self.current()['calls'] if verb in x]
    def make(self,selected=('worker',),name='release',production=False,tag='test-release'):
        directory=self.root/name;directory.mkdir()
        n=copy.deepcopy(self.normalized);n['environment']='production' if production else 'development'
        p={'schema':2,'id':name,'source':'c'*40,'branch':'main' if production else 'develop','tag':tag,'selected':list(selected),
           'normalized':n,'identities':{c:{'profile':'profile','inputs':'input','files':[]} for c in selected},
           'dev_reference':'explicit-123' if production else None,'engine_content':engine.engine_fingerprint()}
        p['id']=engine.digest({k:v for k,v in p.items() if k != 'id'})
        write(directory/'plan.json',p)
        return engine.Engine(directory)
    def ready(self,e):e.prepare();return e

    def test_offline_fixture_has_no_inherited_authority(self):
        for key in ('GITHUB_ACTIONS','ACTIONS_RUNTIME_TOKEN','GH_TOKEN','GITHUB_TOKEN',
                    'GOOGLE_APPLICATION_CREDENTIALS','NODE_OPTIONS'):
            self.assertNotIn(key,os.environ)

    def test_01_stage_barrier_and_retry_retains_first(self):
        e=self.make(('auth','worker'));self.configure(fail_build='olw-pipeline')
        with self.assertRaises(Breakpoint):e.prepare()
        self.assertTrue((e.directory/'receipts/auth.json').exists());self.assertFalse(self.calls('update'))
        builds=len(self.calls('submit'));self.configure(fail_build=None);e.prepare()
        self.assertEqual(len(self.calls('submit')),builds);self.assertEqual(e.state['status'],'ready')

    def test_02_four_to_five_reuses_four_and_frontend_build_only(self):
        e=self.ready(self.make(engine.ORDER[:-1]));count=len(self.calls('build'))+len(self.calls('submit'))
        # Isolate Vercel local output from the repository.
        f=self.make(engine.ORDER,name='expanded')
        with tempfile.TemporaryDirectory() as temp, patch('providers.ROOT',Path(temp)):
            (Path(temp)/'apps/frontend').mkdir(parents=True)
            f.prepare(reuse=e.directory)
        self.assertEqual(count,len(self.calls('submit'))+len([x for x in self.calls('build') if x[0]=='docker']))
        self.assertFalse(self.calls('deploy'));self.assertEqual(self.current()['aliases']['wiki.dev.rayer.idv.tw'],'dpl_prior')
        self.assertEqual(read(e.directory/'receipts/auth.json'),read(f.directory/'receipts/auth.json'))

    def test_03_production_explicit_dev_no_build_source_and_handle_rejection(self):
        e=self.ready(self.make());e.deploy();count=len(self.calls('build'))
        p=self.make(name='prod',production=True);p.prepare(dev=e.directory)
        self.assertEqual(count,len(self.calls('build')))
        q=self.make(name='wrong',production=True);q.plan['identities']['worker']['inputs']='changed'
        with self.assertRaisesRegex(Breakpoint,'incompatible'):q.prepare(dev=e.directory)
        self.configure(expired=True)
        with self.assertRaises(Breakpoint):p.barrier()

    def test_04_order_barrier_and_real_config(self):
        e=self.ready(self.make(('exportjob','auth','bff','worker')));e.deploy()
        calls=[x for x in self.calls('update') if x[0]=='gcloud']
        self.assertEqual([x[4] for x in calls],[self.normalized['export_job']['job_name'],self.normalized['auth']['service_name'],self.normalized['bff']['service_name'],self.normalized['worker']['job_name']])
        self.assertEqual(e.state['status'],'success')
        self.assertTrue(any('--update-secrets' in x for x in calls));self.assertEqual(len(self.calls('POST')),1)

    def test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback(self):
        e=self.ready(self.make(('auth','bff'),name='auth-bff-ready-deploy'))
        ready_artifacts={c:e.receipt(c)['artifact'] for c in ('auth','bff')}
        self.assertEqual(e.state['status'],'ready')
        self.assertEqual(set(ready_artifacts),set(e.plan['selected']))
        submissions_before=len(self.calls('submit'))

        persisted_states=[]
        write_state=engine.write
        def record_checkpoint(path,value):
            write_state(path,value)
            if Path(path)==e.state_path:
                persisted_states.append(copy.deepcopy(value))
        with patch('engine.write',side_effect=record_checkpoint):
            e.deploy()

        self.assertEqual(e.state['status'],'success')
        self.assertEqual(len(self.calls('submit')),submissions_before,
                         'Stage 2 consumes ready receipts without submitting builds')
        for c in ('auth','bff'):
            with self.subTest(component=c):
                entry=e.state['components'][c]
                self.assertEqual(entry['status'],'verified')
                accepted=[state['components'][c] for state in persisted_states
                          if c in state['components']
                          and state['components'][c]['status']=='pending'
                          and state['components'][c]['candidate'].get('revision')]
                self.assertTrue(accepted,
                                'accepted provider revision must be checkpointed before strict readback')
                self.assertEqual(accepted[-1]['candidate']['revision'],entry['candidate']['revision'])
                artifact=ready_artifacts[c]
                revision_name=entry['candidate']['revision']
                revision=self.current()['revisions'][revision_name]
                service_name=self.normalized[c]['service_name']
                service=self.current()['resources'][service_name]
                self.assertEqual(revision['status']['imageDigest'],artifact['image'])
                self.assertEqual(revision['spec']['containers'][0]['image'],artifact['image'])
                self.assertEqual(service['status']['traffic'],[{'revisionName':revision_name,'percent':100}])
                self.assertTrue(e.provider.service_matches(c,revision,artifact['image']))
                self.assertTrue(e.provider.service_template_matches(c,service,revision,artifact['image']))
                self.assertTrue(e.provider.observe(c,artifact,entry['candidate']))

                all_calls=self.current()['calls']
                updates=[call for call in all_calls
                         if call[:4]==['gcloud','run','services','update'] and service_name in call]
                traffic=[call for call in all_calls
                         if call[:4]==['gcloud','run','services','update-traffic'] and service_name in call]
                self.assertEqual(len(updates),1)
                self.assertEqual(len(traffic),1)
                self.assertIn('--no-traffic',updates[0])
                traffic_index=all_calls.index(traffic[0])
                self.assertEqual(traffic[0][traffic[0].index('--to-revisions')+1],revision_name+'=100')
                revision_reads=[i for i,call in enumerate(all_calls)
                                if call[:4]==['gcloud','run','revisions','describe'] and call[4]==revision_name]
                service_reads=[i for i,call in enumerate(all_calls)
                               if call[:4]==['gcloud','run','services','describe'] and call[4]==service_name]
                self.assertTrue(any(all_calls.index(updates[0]) < i < traffic_index for i in revision_reads))
                self.assertTrue(any(all_calls.index(updates[0]) < i < traffic_index for i in service_reads))
                cutovers=[x for x in self.current().get('traffic_before_cutover',[])
                          if x['service']==service_name]
                self.assertEqual(cutovers,[{
                    'service':service_name,
                    'traffic':[{'revisionName':service_name+'-prior','percent':100}],
                    'target':revision_name+'=100',
                }])
                if c=='bff':
                    desired=providers.auth_config.desired(e.plan['normalized'],'bff')['env']
                    env_arg=updates[0][updates[0].index('--update-env-vars')+1]
                    for key,value in desired.items():
                        self.assertIn(key+'='+value,env_arg)
                    self.assertNotIn('--remove-env-vars',updates[0])

    def test_bff_incompatible_receipt_identity_fails_before_provider_mutation(self):
        prepared=self.ready(self.make(('bff',),name='bff-applicability-source'))
        retained=prepared.receipt('bff')
        for field,value in (('profile','changed-profile'),('inputs','changed-inputs'),
                            ('files',[['apps/bff/cmd/bff/main.go','f'*40]])):
            with self.subTest(identity_field=field):
                candidate=self.make(('bff',),name='bff-applicability-'+field)
                candidate.plan['identities']['bff'][field]=value
                candidate.plan['id']=engine.digest({k:v for k,v in candidate.plan.items() if k!='id'})
                write(candidate.directory/'plan.json',candidate.plan)
                candidate=engine.Engine(candidate.directory)
                write(candidate.directory/'receipts'/'bff.json',retained)
                before=len(self.current()['calls'])
                with self.assertRaisesRegex(Breakpoint,'artifact-source-incompatible'):
                    candidate.deploy()
                runtime_calls=self.current()['calls'][before:]
                self.assertFalse([call for call in runtime_calls
                                  if call[:4] in (['gcloud','run','services','update'],
                                                  ['gcloud','run','services','update-traffic'],
                                                  ['gcloud','run','services','replace'])])
                self.assertEqual(candidate.state['status'],'prepared')

    def test_bff_candidate_readback_failure_compensates_without_unverified_cutover(self):
        e=self.ready(self.make(('bff',),name='bff-candidate-readback-failure'))
        service_name=self.normalized['bff']['service_name']
        prior_traffic=copy.deepcopy(self.current()['resources'][service_name]['status']['traffic'])
        self.configure(partial=service_name)
        with self.assertRaises(Breakpoint):e.deploy()
        calls=self.current()['calls']
        updates=[call for call in calls
                 if call[:4]==['gcloud','run','services','update'] and call[4]==service_name]
        candidate_reads=[call for call in calls
                         if call[:4]==['gcloud','run','revisions','describe'] and
                         call[4]==service_name+'-candidate']
        cutovers=[call for call in calls
                  if call[:4]==['gcloud','run','services','update-traffic'] and call[4]==service_name]
        self.assertEqual(len(updates),1)
        self.assertIn('--no-traffic',updates[0])
        self.assertTrue(candidate_reads)
        self.assertEqual(cutovers,[])
        self.assertEqual(e.state['status'],'failed_rolled_back')
        self.assertEqual(e.state['components']['bff']['status'],'rolled_back')
        self.assertEqual(self.current()['resources'][service_name]['status']['traffic'],prior_traffic)
        self.assertEqual(len(self.calls('replace')),1)
        self.assertTrue(e.provider.observe('bff',e.state['components']['bff']['prior'],{},True))

    def test_05_partial_failure_restores_changed_and_reactivation_no_build(self):
        e=self.ready(self.make(('auth','worker')));self.configure(partial=self.normalized['worker']['job_name'])
        with self.assertRaises(Breakpoint):e.deploy()
        self.assertEqual(e.state['status'],'failed_rolled_back')
        self.assertEqual(e.state['components']['auth']['status'],'rolled_back')
        count=len(self.calls('build'))+len(self.calls('submit'))
        self.configure(partial=None);e.deploy(['auth'],reactivate=True)
        self.assertEqual(count,len(self.calls('build'))+len(self.calls('submit')))
        self.assertEqual(e.state['components']['auth']['status'],'verified')

    def test_06_timeout_after_acceptance_and_unknown_and_rollback_failure(self):
        e=self.ready(self.make());self.configure(accepted_timeout=self.normalized['worker']['job_name']);e.deploy()
        self.assertEqual(len(self.calls('update')),1);self.assertEqual(e.state['status'],'success')
        u=self.ready(self.make(name='unknown'));self.configure(unknown_after=self.normalized['worker']['job_name'])
        with self.assertRaises(Breakpoint) as caught:u.deploy()
        self.assertEqual(caught.exception.status,'unknown');self.assertEqual(u.state['status'],'unknown')
        self.configure(unreadable=False,unknown_after=None,accepted_timeout=None,partial=self.normalized['worker']['job_name'],fail_rollback=True)
        f=self.ready(self.make(name='rollbackfail'))
        with self.assertRaises(Breakpoint):f.deploy()
        self.assertEqual(f.state['status'],'recovery_failed')

    def test_07_severe_recovery_unchanged_not_redeployed(self):
        e=self.ready(self.make(('auth','worker')));e.deploy();self.configure(tag='c'*40)
        count=len(self.calls('update'));e.restore(['auth'])
        self.assertEqual(e.state['components']['worker']['status'],'verified')
        self.assertEqual(count,len(self.calls('update')));self.assertIn('persistent',e.state['job_data_boundary'])
        self.assertEqual(self.current()['tag'],'c'*40)

    def test_08_tag_only_retry_idempotence_conflict(self):
        e=self.ready(self.make());self.configure(tag_fail=True)
        with self.assertRaises(Breakpoint):e.deploy()
        self.assertEqual(e.state['status'],'tag_failed');count=len(self.calls('update'))
        self.configure(tag_fail=False);e.deploy();e.tag()
        self.assertEqual(count,len(self.calls('update')));self.assertEqual(e.state['status'],'success')
        self.configure(tag='d'*40)
        with self.assertRaises(Breakpoint):e.tag()
        self.assertEqual(e.state['status'],'tag_failed')

    def test_09_frontend_alias_restore_and_config_applicability(self):
        e=self.make(('frontend',))
        with tempfile.TemporaryDirectory() as temp,patch('providers.ROOT',Path(temp)):
            (Path(temp)/'apps/frontend').mkdir(parents=True);e.prepare()
        e.deploy();self.assertEqual(self.current()['aliases']['wiki.dev.rayer.idv.tw'],'dpl_candidate')
        count=len([x for x in self.calls('build') if x[0]=='vercel'])
        e.restore(['frontend']);self.assertEqual(self.current()['aliases']['wiki.dev.rayer.idv.tw'],'dpl_prior')
        e.deploy(['frontend'],reactivate=True)
        self.assertEqual(count,len([x for x in self.calls('build') if x[0]=='vercel']))
        e.plan['normalized']['frontend']['api_url']='https://wrong.example'
        with self.assertRaisesRegex(Breakpoint,'config-incompatible'):e.receipt('frontend')

    def test_10_stale_checkpoint_and_absent_resource(self):
        e=self.ready(self.make());e.snapshot()
        def latest(*args,**kwargs):
            write(e.directory/'.latest.json',{'plan':'later','sequence':1})
        with patch.dict(os.environ,{'GITHUB_ACTIONS':'true'}),patch('engine.run',side_effect=latest):
            with self.assertRaisesRegex(Breakpoint,'stale-checkpoint'):e.runtime_guard()
        self.provider=self.current();self.provider['resources']={};self.flush()
        f=self.ready(self.make(name='absent'))
        with self.assertRaises(Breakpoint):f.deploy()
        self.assertFalse(self.calls('update'))

    def test_sanity_rejects_each_managed_service_field(self):
        e=self.ready(self.make(('auth','bff')));e.deploy()
        baseline=self.current()
        for c in ('auth','bff'):
            name=self.normalized[c]['service_name'];revision=e.state['components'][c]['candidate']['revision']
            for fault in ('image','specimage','ready','env','secret','account','traffic','template',
                          'template-env','template-secret','template-account'):
                with self.subTest(component=c,fault=fault):
                    self.provider=copy.deepcopy(baseline)
                    rev=self.provider['revisions'][revision]
                    template=self.provider['resources'][name]['spec']['template']['spec']
                    if fault=='image':rev['status']['imageDigest']='wrong'
                    elif fault=='specimage':rev['spec']['containers'][0]['image']='wrong'
                    elif fault=='ready':rev['status']['conditions'][0]['status']='False'
                    elif fault=='env':rev['spec']['containers'][0]['env']=[v for v in rev['spec']['containers'][0]['env'] if 'value' not in v]
                    elif fault=='secret':rev['spec']['containers'][0]['env']=[v for v in rev['spec']['containers'][0]['env'] if 'valueFrom' not in v]
                    elif fault=='account':rev['spec']['serviceAccountName']='wrong'
                    elif fault=='template':self.provider['resources'][name]['spec']['template']['spec']['containers'][0]['image']='wrong'
                    elif fault=='template-env':template['containers'][0]['env']=[]
                    elif fault=='template-secret':template['containers'][0]['env']=[v for v in template['containers'][0].get('env',[]) if 'valueFrom' not in v]
                    elif fault=='template-account':template['serviceAccountName']='wrong'
                    else:self.provider['resources'][name]['status']['traffic'][0]['percent']=50
                    self.flush()
                    self.assertFalse(e.provider.observe(c,e.receipt(c)['artifact'],e.state['components'][c]['candidate']))
        for c in ('auth','bff'):
            name=self.normalized[c]['service_name'];revision=e.state['components'][c]['candidate']['revision']
            self.provider=copy.deepcopy(baseline)
            self.provider['revisions'][revision]['spec']['containers'][0]['name']='provider-default'
            template=self.provider['resources'][name]['spec']['template']
            template['spec']['containers'][0]['name']='service-default'
            template.setdefault('metadata',{}).setdefault('annotations',{})['autoscaling.knative.dev/maxScale']='7'
            self.flush()
            self.assertTrue(e.provider.observe(c,e.receipt(c)['artifact'],e.state['components'][c]['candidate']))
        self.provider=baseline;self.flush()

    def test_job_template_and_frontend_readback_failures(self):
        e=self.ready(self.make(('exportjob','worker')));e.deploy();baseline=self.current()
        for c in ('exportjob','worker'):
            name=self.normalized['export_job' if c=='exportjob' else c]['job_name']
            for fault in ('image','env') if c=='worker' else ('image','env','account','timeout','retries','tasks','parallelism'):
                with self.subTest(component=c,fault=fault):
                    self.provider=copy.deepcopy(baseline);raw=self.provider['resources'][name]
                    t=providers.Providers.job_template(raw)
                    if fault=='image':t['containers'][0]['image']='wrong'
                    elif fault=='env':t['containers'][0]['env']=[]
                    elif fault=='account':t['serviceAccountName']='wrong'
                    elif fault=='timeout':t['timeoutSeconds']='1'
                    elif fault=='retries':t['maxRetries']=99
                    else:raw['spec']['template']['spec']['taskCount' if fault=='tasks' else 'parallelism']=99
                    self.flush();self.assertFalse(e.provider.observe(c,e.receipt(c)['artifact'],{}))

    def test_unknown_resume_does_not_repeat_accepted_job_update(self):
        e=self.ready(self.make());self.configure(unknown_after=self.normalized['worker']['job_name'])
        with self.assertRaises(Breakpoint):e.deploy()
        count=len(self.calls('update'));self.configure(unreadable=False,unknown_after=None)
        resumed=engine.Engine(e.directory);resumed.deploy()
        self.assertEqual(count,len(self.calls('update')));self.assertEqual(resumed.state['status'],'success')

    def test_plan_tamper_and_runtime_authority_rejected(self):
        e=self.ready(self.make())
        with self.assertRaisesRegex(Breakpoint,'runtime-owned-by-actions'):e.runtime_guard()
        p=read(e.directory/'plan.json');p['tag']='changed';write(e.directory/'plan.json',p)
        with self.assertRaisesRegex(Breakpoint,'plan-content-mismatch'):engine.Engine(e.directory)

    def test_frontend_sanity_checks_ready_target_alias_artifact_and_config(self):
        e=self.make(('frontend',))
        with tempfile.TemporaryDirectory() as temp,patch('providers.ROOT',Path(temp)):
            (Path(temp)/'apps/frontend').mkdir(parents=True);e.prepare()
        e.deploy();baseline=self.current()
        for fault in ('ready','target','alias','artifact','config'):
            with self.subTest(fault=fault):
                self.provider=copy.deepcopy(baseline);d=self.provider['deployments']['dpl_candidate']
                if fault=='ready':d['readyState']='ERROR'
                elif fault=='target':d['target']='production'
                elif fault=='alias':self.provider['aliases']['wiki.dev.rayer.idv.tw']='dpl_prior'
                elif fault=='artifact':d['meta']['lwcArtifact']='wrong'
                else:self.provider['build_config']['api_url']='https://wrong.example'
                self.flush()
                self.assertFalse(e.provider.observe('frontend',e.receipt('frontend')['artifact'],e.state['components']['frontend']['candidate']))

    def test_frontend_runtime_deploy_and_reconcile_causes_reach_formal_result(self):
        e=self.make(('frontend',),name='frontend-runtime-cause')
        with tempfile.TemporaryDirectory() as temp, patch('providers.ROOT',Path(temp)):
            (Path(temp)/'apps/frontend').mkdir(parents=True)
            e.prepare()
        e.snapshot()

        calls=[]
        def fake_subprocess(args, **kwargs):
            argv=[str(arg) for arg in args]
            calls.append(argv)
            if argv[0]=='vercel':
                return subprocess.CompletedProcess(args,17,'','frontend deploy failed with test-only')
            if argv[0]=='curl':
                return subprocess.CompletedProcess(args,0,'{','')
            raise AssertionError('unexpected subprocess boundary')

        stdout=io.StringIO()
        with patch.object(engine.Engine,'runtime_guard'), \
             patch('support.subprocess.run',side_effect=fake_subprocess), \
             patch.object(providers.Providers,'poll',side_effect=AssertionError('poll must not follow failed reconciliation')), \
             patch('sys.argv',['engine.py','deploy','--directory',str(e.directory)]), \
             patch('sys.stdout',stdout):
            exit_code=engine.main()

        result=read(e.directory/'result.json')
        rendered=stdout.getvalue()
        self.assertEqual(exit_code,1)
        self.assertEqual(result['reason'],'provider-result-unreadable',result)
        self.assertEqual(result['status'],'unknown')
        self.assertTrue(result['mutation_may_have_happened'])
        self.assertEqual(result['allowed_next_action'],'reconcile-before-replay')
        self.assertEqual(result['component'],'frontend')
        self.assertEqual(result['observed']['component_status'],'unknown')
        self.assertEqual(result['last_verified_checkpoint'],read(e.state_path)['sequence'])
        self.assertEqual(len([argv for argv in calls if argv[0]=='vercel']),1,
                         'reconciliation failure must not replay deploy')
        self.assertEqual(len([argv for argv in calls if argv[0]=='curl']),1)
        self.assertEqual([cause['phase'] for cause in result['causes']],['deploy','reconcile'])
        self.assertEqual(result['causes'][0]['stage'],'frontend-vercel-deploy')
        self.assertEqual(result['causes'][0]['exit_code'],17)
        self.assertEqual(result['causes'][0]['cause']['message'],
                         'frontend deploy failed with [REDACTED]')
        self.assertEqual(result['causes'][1]['exception_type'],'JSONDecodeError')
        self.assertEqual(result['causes'][1]['stage'],'frontend-deployment-reconcile')
        self.assertIn('line 1 column 2',result['causes'][1]['message'])
        self.assertNotIn('test-only',rendered)
        self.assertNotIn('frontend deploy failed with test-only',rendered)

    def test_unusable_receipt_rebuilds_only_affected_component(self):
        e=self.ready(self.make(('auth','worker')))
        r=read(e.directory/'receipts/worker.json');r['identity']['inputs']='invalid'
        write(e.directory/'receipts/worker.json',r)
        count=len(self.calls('submit'));worker_builds=len(self.calls('build'));e.prepare()
        self.assertEqual(count,len(self.calls('submit')));self.assertEqual(worker_builds+1,len(self.calls('build')))
        self.assertEqual(e.state['status'],'ready')

    def test_service_partial_update_restores_template_even_with_old_traffic(self):
        e=self.ready(self.make(('auth',)))
        self.configure(partial=self.normalized['auth']['service_name'])
        with self.assertRaises(Breakpoint):e.deploy()
        self.assertEqual(e.state['status'],'failed_rolled_back')
        self.assertEqual(len(self.calls('replace')),1)
        self.assertTrue(e.provider.observe('auth',e.state['components']['auth']['prior'],{},True))

    def test_prior_service_snapshot_and_rollback_use_retained_image_identity(self):
        for c in ('auth','bff'):
            with self.subTest(component=c):
                e=self.ready(self.make((c,),name='image-identity-'+c))
                service_name=self.normalized[c]['service_name']
                prior_revision=service_name+'-prior'
                state=self.current()
                state['revisions'][prior_revision]['spec']['containers'][0]['name']='provider-default'
                state['revisions'][prior_revision]['metadata']['annotations']['autoscaling.knative.dev/maxScale']='2'
                self.provider=state;self.flush()

                e.snapshot()
                prior=copy.deepcopy(e.state['components'][c]['prior'])
                old_image=prior['image']
                self.assertEqual(set(prior),{'revision','image','traffic'})
                self.assertEqual(prior['revision'],prior_revision)
                self.assertEqual(prior['traffic'],[{'revisionName':prior_revision,'percent':100}])

                e.deploy([c])
                state=self.current()
                retained=state['revisions'][prior_revision]
                retained['metadata']['annotations']['autoscaling.knative.dev/maxScale']='3'
                self.provider=state;self.flush()
                self.assertEqual(retained['status']['imageDigest'],old_image)
                self.assertFalse(e.provider.observe(c,prior,{},prior=True))
                replace_count=len(self.calls('replace'))
                build_count=len(self.calls('build'))+len(self.calls('submit'))
                e.restore([c])

                self.assertEqual(e.state['components'][c]['status'],'rolled_back')
                self.assertEqual(len(self.calls('replace')),replace_count+1)
                self.assertEqual(len(self.calls('build'))+len(self.calls('submit')),build_count)
                replacement=self.current()['replacements'][-1]
                self.assertEqual(replacement['spec']['template']['spec'],retained['spec'])
                self.assertEqual(replacement['spec']['template']['metadata']['annotations'],
                                 {'autoscaling.knative.dev/maxScale':'3'})
                self.assertEqual(replacement['spec']['traffic'],prior['traffic'])
                self.assertTrue(e.provider.observe(c,prior,{},prior=True))
                state=self.current()
                state['resources'][service_name]['spec']['template']['metadata']['annotations']['autoscaling.knative.dev/maxScale']='service-only'
                self.provider=state;self.flush()
                self.assertTrue(e.provider.observe(c,prior,{},prior=True))
                self.assertEqual(e.provider.snapshot(c)['image'],old_image)

    def test_prior_image_and_route_checks_remain_fail_closed(self):
        e=self.ready(self.make(('auth',),name='prior-image-checks'))
        e.snapshot()
        prior=copy.deepcopy(e.state['components']['auth']['prior'])
        service_name=self.normalized['auth']['service_name']
        revision_name=prior['revision']
        baseline=self.current()
        faults=('saved-image','revision-spec-image','revision-status-digest',
                'service-template-image','route','not-ready')
        for fault in faults:
            with self.subTest(fault=fault):
                self.provider=copy.deepcopy(baseline)
                resource=self.provider['resources'][service_name]
                revision=self.provider['revisions'][revision_name]
                if fault=='saved-image':
                    observed=copy.deepcopy(prior);observed['image']='wrong@sha256:'+'0'*64
                elif fault=='revision-spec-image':
                    revision['spec']['containers'][0]['image']='wrong@sha256:'+'0'*64
                    observed=prior
                elif fault=='revision-status-digest':
                    revision['status']['imageDigest']='wrong@sha256:'+'0'*64
                    observed=prior
                elif fault=='service-template-image':
                    resource['spec']['template']['spec']['containers'][0]['image']='wrong@sha256:'+'0'*64
                    observed=prior
                elif fault=='route':
                    resource['status']['traffic'][0]['percent']=50
                    observed=prior
                else:
                    revision['status']['conditions'][0]['status']='False'
                    observed=prior
                self.flush()
                self.assertFalse(e.provider.observe('auth',observed,{},prior=True))
                if fault=='saved-image':
                    self.assertEqual(e.provider.snapshot('auth')['image'],prior['image'])
                else:
                    with self.assertRaises(Breakpoint):e.provider.snapshot('auth')
                offset=len(self.calls('replace'))
                if fault in ('saved-image','revision-spec-image','revision-status-digest','not-ready'):
                    with self.assertRaisesRegex(Breakpoint,'prior-revision-changed'):
                        e.provider.rollback('auth',observed)
                    self.assertEqual(len(self.calls('replace')),offset)

    def test_durable_pending_failure_prevents_provider_mutation(self):
        e=self.ready(self.make());uploads=[]
        def upload(*args,**kwargs):
            uploads.append(args)
            if len(uploads)==2:raise Breakpoint('artifact-transport-failed')
        with patch.dict(os.environ,{'GITHUB_ACTIONS':'true','GITHUB_RUN_ID':'1','GITHUB_RUN_ATTEMPT':'1'}),patch('engine.run',side_effect=upload):
            with self.assertRaisesRegex(Breakpoint,'artifact-transport-failed'):e.deploy()
        self.assertFalse(self.calls('update'))
        self.assertEqual(e.state['components']['worker']['status'],'pending')

    def test_wrong_container_repository_and_missing_dev_reference_rejected(self):
        e=self.ready(self.make());e.deploy()
        p=self.make(name='prod-wrong-registry',production=True)
        p.plan['normalized']['gcp']['artifact_registry']='wrong.example/repo'
        with self.assertRaisesRegex(Breakpoint,'config-incompatible'):p.prepare(dev=e.directory)
        q=self.make(name='prod-missing-dev',production=True)
        with self.assertRaisesRegex(Breakpoint,'explicit-dev-provenance-required'):q.prepare()

    def test_hold_worker_reactivation_failure_does_not_compensate_healthy_auth(self):
        e=self.ready(self.make(('auth','worker')));e.deploy();e.restore(['worker'])
        auth_entry=copy.deepcopy(e.state['components']['auth'])
        auth_name=self.normalized['auth']['service_name']
        auth_resource=copy.deepcopy(self.current()['resources'][auth_name])
        offset=len(self.current()['calls'])
        self.configure(partial=self.normalized['worker']['job_name'])
        with self.assertRaises(Breakpoint):e.deploy(['worker'],reactivate=True)
        calls=self.current()['calls'][offset:]
        self.assertFalse([a for a in calls if a[:3]==['gcloud','run','services'] and a[3] in ('update','replace','update-traffic')])
        self.assertEqual(e.state['components']['auth'],auth_entry)
        self.assertEqual(self.current()['resources'][auth_name],auth_resource)
        self.assertEqual(e.state['components']['worker']['status'],'rolled_back')

    def test_hold_service_reactivation_preserves_candidate_and_next_snapshot(self):
        for component in ('auth','bff'):
            with self.subTest(component=component):
                e=self.ready(self.make((component,),name='reactivate-'+component));e.deploy()
                receipt=e.receipt(component)
                revision=e.state['components'][component]['candidate']['revision']
                retained=copy.deepcopy(self.current()['revisions'][revision])
                e.restore([component]);offset=len(self.current()['calls'])
                e.deploy([component],reactivate=True)
                self.assertEqual(e.state['status'],'success')
                new=self.make((component,),name='next-'+component,tag='test-next-'+component)
                self.assertNotEqual(new.plan['id'],e.plan['id'])
                new.prepare(reuse=e.directory);new.snapshot()
                snapshot=new.state['components'][component]['prior']
                self.assertEqual(snapshot['revision'],revision)
                self.assertEqual(snapshot['image'],receipt['artifact']['image'])
                self.assertEqual(e.receipt(component),receipt)
                self.assertEqual(self.current()['revisions'][revision],retained)
                replacement=self.current()['replacements'][-1]
                self.assertEqual(replacement['spec']['template']['metadata']['name'],revision)
                self.assertEqual(replacement['spec']['template']['spec'],retained['spec'])
                self.assertEqual(replacement['spec']['traffic'],[{'revisionName':revision,'percent':100}])
                self.assertFalse([a for a in self.current()['calls'][offset:] if 'build' in a or 'submit' in a])

    def test_retained_annotations_full_chain(self):
        for c in ('auth','bff'):
            with self.subTest(component=c):
                state=self.current();name=self.normalized[c]['service_name'];prior=name+'-prior'
                rev=state['revisions'][prior]
                rev['spec']['containers'][0]['env']=[{'name':'JWT_SECRET','valueFrom':{'secretKeyRef':{'name':'prior-alias','key':'latest'}}}]
                annotations={'run.googleapis.com/secrets':'prior-alias:projects/test-only/secrets/prior-test-only',
                             'autoscaling.knative.dev/maxScale':'3'}
                rev['metadata']['annotations']=dict(annotations, **{'run.googleapis.com/operation-id':'prior-controller'})
                state['resources'][name]['spec']['template']={'metadata':{'name':prior,'annotations':annotations},'spec':copy.deepcopy(rev['spec'])}
                state['secret_alias_fixture']=True;write(self.root/'provider.json',state)
                e=self.ready(self.make((c,),name='aliases-'+c));e.deploy()
                receipt=e.receipt(c);candidate=e.state['components'][c]['candidate']
                retained=copy.deepcopy(self.current()['revisions'][candidate['revision']])
                offset=len(self.current()['calls'])
                e.restore([c])
                self.assertEqual(e.state['components'][c]['status'],'rolled_back')
                self.assertTrue(e.provider.observe(c,e.state['components'][c]['prior'],{},prior=True))
                e.deploy([c],reactivate=True)
                self.assertEqual(e.state['status'],'success')
                self.assertEqual(self.current()['revisions'][candidate['revision']],retained)
                self.assertEqual(e.receipt(c),receipt)
                new=self.make((c,),name='aliases-next-'+c,tag='aliases-next-'+c)
                new.prepare(reuse=e.directory);new.snapshot()
                self.assertEqual(new.state['components'][c]['prior']['revision'],candidate['revision'])
                self.assertFalse([a for a in self.current()['calls'][offset:] if 'build' in a or 'submit' in a])
                for payload in self.current()['replacements'][-2:]:
                    self.assertNotIn('run.googleapis.com/operation-id',payload['spec']['template']['metadata']['annotations'])
                good=self.current()
                for prior_mode in (False,True):
                    bad=copy.deepcopy(good)
                    bad['resources'][name]['spec']['template']['metadata']['annotations']={}
                    write(self.root/'provider.json',bad)
                    artifact=new.state['components'][c]['prior'] if prior_mode else receipt['artifact']
                    self.assertEqual(e.provider.observe(c,artifact,candidate,prior=prior_mode),prior_mode)
                    self.assertEqual(e.provider.snapshot(c)['image'],artifact['image'] if prior_mode else receipt['artifact']['image'])
                # Controller-only churn is not effective config drift.
                stable=copy.deepcopy(good)
                stable['revisions'][candidate['revision']]['metadata']['annotations']['run.googleapis.com/operation-id']='new-controller'
                write(self.root/'provider.json',stable)
                self.assertTrue(e.provider.observe(c,new.state['components'][c]['prior'],{},prior=True))
                # Unmanaged effective-config drift does not change prior image identity.
                for obj in (stable['revisions'][candidate['revision']],stable['resources'][name]['spec']['template']):
                    obj['metadata']['annotations']['autoscaling.knative.dev/maxScale']='99'
                write(self.root/'provider.json',stable)
                self.assertTrue(e.provider.observe(c,new.state['components'][c]['prior'],{},prior=True))
                offset=len(self.current()['calls'])
                build_count=len(self.calls('build'))+len(self.calls('submit'))
                e.provider.rollback(c,new.state['components'][c]['prior'])
                replacement=self.current()['replacements'][-1]
                self.assertEqual(replacement['spec']['template']['spec'],stable['revisions'][candidate['revision']]['spec'])
                self.assertEqual(replacement['spec']['template']['metadata']['annotations']['autoscaling.knative.dev/maxScale'],'99')
                self.assertEqual(len(self.calls('build'))+len(self.calls('submit')),build_count)
                self.assertEqual(len([a for a in self.current()['calls'][offset:] if 'replace' in a]),1)
                write(self.root/'provider.json',good)

class SourceApplicability(unittest.TestCase):
    def test_actual_go_inputs_include_embeds_exclude_engine_and_unrelated_packages(self):
        sha=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
        for c in ('auth','bff','worker','exportjob'):
            identity=engine.source_identity(c,sha)
            paths=[p for p,_ in identity['files']]
            self.assertFalse(any(p.startswith('deploy/') or '/cmd/deploy_config/' in p for p in paths))
            if c=='bff':self.assertTrue(any('/prompts/' in p for p in paths))
            if c=='worker':self.assertIn('apps/bff/cmd/olw_worker/synto_execution.py',paths)
            if c=='exportjob':self.assertNotIn('apps/bff/cmd/auth/main.go',paths)

if __name__=='__main__':unittest.main(verbosity=2)
