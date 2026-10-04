"""TEST ONLY: fake the Cloud Build CLI while exercising the real engine/adapters."""
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest
from types import SimpleNamespace
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
import engine
import providers
from support import Breakpoint, digest, read, write

PROJECT = 'llm-wiki-cloud'
PROJECT_NUMBER = '580854833715'
LOCATION = 'global'
BUILD_IDS = ('12345678-1234-4234-8234-123456789abc',
             'abcdefab-cdef-4abc-8def-abcdefabcdef')
DIGEST = 'sha256:' + 'a' * 64
RAW_SENTINEL = 'TEST_ONLY_RAW_PROVIDER_OUTPUT_SENTINEL'


class AsyncBuildSubmission(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.fake_state = self.root / 'fake-provider.json'
        self.fake_uploads = self.root / 'fake-uploads.json'
        self.fake_state.write_text(json.dumps({'calls': [], 'submit_count': 0,
                                               'status_count': {}, 'uploads': []}))
        self.action_runs = 0
        (self.bin / 'gcloud').write_text(textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, os, sys
            from pathlib import Path
            state_path=Path(os.environ['LWC_TEST_FAKE_STATE'])
            state=json.loads(state_path.read_text())
            args=sys.argv[1:]
            state['calls'].append(args)
            def save(): state_path.write_text(json.dumps(state))
            def flag(name, default=None):
                if name in args: return args[args.index(name)+1]
                return next((x.split('=',1)[1] for x in args if x.startswith(name+'=')), default)
            def component(): return 'bff' if 'cloudbuild-bff.yaml' in str(args) else 'auth'
            if args[:2] == ['projects','describe']:
                save()
                if os.environ.get('FAKE_PROJECT_LOOKUP_EXIT'):
                    print(os.environ.get('FAKE_PROJECT_LOOKUP_STDERR','project lookup unavailable'),file=sys.stderr)
                    raise SystemExit(int(os.environ['FAKE_PROJECT_LOOKUP_EXIT']))
                identity={'projectId':os.environ.get('FAKE_PROJECT_IDENTITY_PROJECT',args[2]),
                    'projectNumber':os.environ.get('FAKE_PROJECT_NUMBER','580854833715')}
                print(json.dumps(identity))
                raise SystemExit(0)
            if args[:2] == ['builds','submit']:
                state['submit_count'] += 1
                save()
                if os.environ.get('FAKE_SUBMIT_EXIT'):
                    print(os.environ.get('FAKE_SUBMIT_STDERR', 'provider rejected request'), file=sys.stderr)
                    raise SystemExit(int(os.environ['FAKE_SUBMIT_EXIT']))
                ids=json.loads(os.environ.get('FAKE_BUILD_IDS', '[]')) or [
                    '12345678-1234-4234-8234-123456789abc']
                build_id=ids[min(state['submit_count']-1, len(ids)-1)]
                state.setdefault('build_component', {})[build_id]=component()
                save()
                project=os.environ.get('FAKE_SUBMIT_PROJECT', 'llm-wiki-cloud')
                location=os.environ.get('FAKE_SUBMIT_LOCATION', 'global')
                resource_project=os.environ.get('FAKE_SUBMIT_RESOURCE_PROJECT',
                    os.environ.get('FAKE_PROJECT_NUMBER','580854833715'))
                resource_id=os.environ.get('FAKE_SUBMIT_RESOURCE_ID',build_id)
                submitted={'id':build_id, 'projectId':project, 'status':'QUEUED',
                    'statusDetail':os.environ.get('FAKE_RAW_FIELD',''),
                    'logsBucket':os.environ.get('FAKE_RAW_FIELD','')}
                if os.environ.get('FAKE_SUBMIT_OMIT_NAME') != '1':
                    submitted['name']=f'projects/{resource_project}/locations/{location}/builds/{resource_id}'
                print(json.dumps(submitted))
                raise SystemExit(0)
            if args[:2] == ['builds','describe']:
                c=state.get('build_component', {}).get(args[2], 'auth')
                state['status_count'][c]=state['status_count'].get(c,0)+1
                save()
                build_id=args[2]
                if os.environ.get('FAKE_ASSERT_DURABLE_UPLOAD') == '1':
                    snapshots=json.loads(Path(os.environ['LWC_TEST_FAKE_UPLOADS']).read_text())
                    if not any(x.get('builds',{}).get(c,{}).get('build_id') == build_id and
                               x.get('builds',{}).get(c,{}).get('identity_verified') is True
                               for x in snapshots):
                        print('durable build checkpoint missing before poll', file=sys.stderr)
                        raise SystemExit(93)
                if os.environ.get('FAKE_STATUS_API_ERROR') == '1':
                    print(os.environ.get('FAKE_STATUS_STDERR', 'status API unavailable'), file=sys.stderr)
                    raise SystemExit(17)
                statuses=json.loads(os.environ.get('FAKE_STATUSES_'+c.upper(),
                    os.environ.get('FAKE_STATUSES','["SUCCESS"]')))
                count=state['status_count'][c]
                status=statuses[min(count-1,len(statuses)-1)]
                project=os.environ.get('FAKE_STATUS_PROJECT','llm-wiki-cloud')
                location=os.environ.get('FAKE_STATUS_LOCATION','global')
                resource_project=os.environ.get('FAKE_STATUS_RESOURCE_PROJECT',
                    os.environ.get('FAKE_PROJECT_NUMBER','580854833715'))
                resource_id=os.environ.get('FAKE_STATUS_RESOURCE_ID',build_id)
                described={'id':build_id,'projectId':project,'location':location,'status':status,
                    'statusDetail':os.environ.get('FAKE_RAW_FIELD','')}
                if os.environ.get('FAKE_STATUS_OMIT_NAME') != '1':
                    described['name']=f'projects/{resource_project}/locations/{location}/builds/{resource_id}'
                print(json.dumps(described))
                raise SystemExit(0)
            if args[:4] == ['artifacts','docker','images','describe']:
                image=args[4]
                if os.environ.get('FAKE_TAG_LOOKUP_EXIT') and '@' not in image:
                    print(os.environ.get('FAKE_STATUS_STDERR','lookup unavailable'),file=sys.stderr)
                    raise SystemExit(int(os.environ['FAKE_TAG_LOOKUP_EXIT']))
                print(image.split('@',1)[1] if '@' in image else 'sha256:'+'a'*64)
                raise SystemExit(0)
            print('unexpected fake gcloud command', file=sys.stderr)
            raise SystemExit(90)
        '''))
        (self.bin / 'gcloud').chmod(0o755)
        real_go = shutil.which('go')
        self.assertIsNotNone(real_go, 'real Go is required for the actual Action admission path')
        (self.bin / 'go').write_text(textwrap.dedent(f'''\
            #!/usr/bin/env python3
            import os, sys
            args=sys.argv[1:]
            if args[:3] == ['run','./cmd/versioncheck','VERSION']:
                print('1.0.0')
                raise SystemExit(0)
            os.execv({real_go!r}, [{real_go!r}, *args])
        '''))
        (self.bin / 'go').chmod(0o755)
        (self.bin / 'python3').write_text(
            '#!/bin/sh\nexec ' + repr(sys.executable) + ' "$@"\n')
        (self.bin / 'python3').chmod(0o755)
        (self.bin / 'timeout').write_text(textwrap.dedent('''\
            #!/usr/bin/env python3
            import os, sys
            args=sys.argv[1:]
            while args and args[0].startswith('--'): args.pop(0)
            if not args: raise SystemExit(90)
            args.pop(0)  # duration
            if os.environ.get('FAKE_SUBMIT_TIMEOUT') == '1' and args[:3] == ['gcloud','builds','submit']:
                print(os.environ.get('FAKE_SUBMIT_STDERR','submission timed out'),file=sys.stderr)
                raise SystemExit(124)
            os.execvpe(args[0],args,os.environ)
        '''))
        (self.bin / 'timeout').chmod(0o755)
        (self.bin / 'node').write_text(textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, os, shutil, sys
            from pathlib import Path
            args=sys.argv[1:]
            if len(args) >= 4 and args[1] == 'download':
                if args[2] != os.environ.get('LWC_TEST_ARTIFACT_ID'): raise SystemExit(92)
                source=Path(os.environ['LWC_TEST_ARTIFACT_SOURCE'])
                destination=Path(args[3])
                shutil.copytree(source,destination,ignore=shutil.ignore_patterns('.*'))
                raise SystemExit(0)
            if len(args) < 4 or args[1] != 'upload': raise SystemExit(91)
            state=json.loads((Path(args[2])/'state.json').read_text())
            path=Path(os.environ['LWC_TEST_FAKE_UPLOADS'])
            uploads=json.loads(path.read_text()) if path.exists() else []
            uploads.append(state)
            path.write_text(json.dumps(uploads))
        '''))
        (self.bin / 'node').chmod(0o755)
        (self.bin / 'sitecustomize.py').write_text(
            'import time\ntime.sleep = lambda _seconds: None\n')
        self.environment = {
            'PATH': str(self.bin) + os.pathsep + os.environ.get('PATH', '/usr/bin:/bin'),
            'PYTHONPATH': str(self.bin),
            'HOME': str(self.root), 'TMPDIR': str(self.root),
            'LWC_TEST_FAKE_STATE': str(self.fake_state),
            'LWC_TEST_FAKE_UPLOADS': str(self.fake_uploads),
            'FAKE_STATUSES': json.dumps(['SUCCESS']),
            'FAKE_BUILD_IDS': json.dumps(list(BUILD_IDS)),
        }

    def make_plan(self, selected=('auth',), name='release'):
        directory = self.root / name
        directory.mkdir()
        normalized = {
            'environment': 'development',
            'gcp': {'project_id': PROJECT, 'region': 'asia-east1',
                    'artifact_registry': 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images'},
            'auth': {'service_name': 'auth-test'},
            'bff': {'service_name': 'bff-test'},
        }
        plan = {'schema': 3, 'source': 'a' * 40, 'branch': 'develop',
                'tag': 'test-lwc358-' + name,
                'executor_sha': 'e' * 40,
                'normalized': normalized,
                'identities': {c: {'profile': 'test', 'inputs': 'test', 'files': []}
                               for c in selected},
                'dev_reference': None, 'selected': list(selected)}
        plan['id'] = digest(engine.release_identity(plan))
        write(directory / 'plan.json', plan)
        return directory

    def fake_data(self):
        return json.loads(self.fake_state.read_text())

    def reset_fake(self):
        self.fake_state.write_text(json.dumps({'calls': [], 'submit_count': 0,
                                               'status_count': {}, 'uploads': [],
                                               'build_component': {}}))
        self.fake_uploads.unlink(missing_ok=True)

    def invoke(self, directory, extra=None):
        env = {**self.environment, **(extra or {})}
        return subprocess.run([sys.executable, str(ROOT / 'deploy/engine/engine.py'),
                               'prepare', '--directory', str(directory)],
                              cwd=ROOT, env=env, text=True, capture_output=True, timeout=60)

    def result_from(self, directory):
        return json.loads((directory / 'result.json').read_text())

    def admitted_plan(self, *, name, selected=('auth',), tag=None, source=None):
        source = source or subprocess.check_output(['git','rev-parse','HEAD'], cwd=ROOT, text=True).strip()
        args = SimpleNamespace(environment='development', source=source,
                               components=','.join(selected), tag=tag or 'test-lwc358-'+name,
                               dev_reference=None)
        plan = engine.admit(args)
        directory = self.root / name
        directory.mkdir()
        write(directory/'plan.json', plan)
        return directory, plan

    def make_action_checkpoint(self, name, *, statuses=None, extra=None, source=None):
        directory, plan = self.admitted_plan(name=name, source=source)
        with patch.dict(os.environ,{**self.environment,**(extra or {}),
                                    'FAKE_STATUSES':json.dumps(statuses or ['WORKING'])},clear=True), \
             patch.object(providers,'BUILD_POLL_MAX_READS',1), \
             patch.object(providers,'BUILD_POLL_INTERVAL_SECONDS',0), \
             patch.object(providers.time,'sleep',lambda _seconds:None):
            instance=engine.Engine(directory)
            try:
                instance.prepare()
                error=None
            except Breakpoint as exc:
                error=exc
            output=io.StringIO()
            with contextlib.redirect_stdout(output):
                instance.result(error)
        self.assertIsNotNone(error, 'checkpoint setup must stop before a ready receipt')
        return directory, plan, self.result_from(directory)

    def run_action_prepare(self, *, source, tag, components, artifact_source, executor_sha=None,
                           target='development', extra=None):
        node = shutil.which('node')
        self.assertIsNotNone(node, 'Node is required to execute the real JavaScript Action')
        self.action_runs += 1
        runner_temp=self.root/f'runner-temp-{self.action_runs}'
        runner_temp.mkdir(exist_ok=True)
        env={**self.environment,**(extra or {}),'RUNNER_TEMP':str(runner_temp),
             'EXECUTOR_SHA':executor_sha or source,
             'INPUT_OPERATION':'prepare','TARGET':target,'SOURCE':source,
             'COMPONENTS':components,'RELEASE_TAG':tag,'REUSE_ID':'9001','DEV_ID':'',
             'LWC_TEST_ARTIFACT_ID':'9001','LWC_TEST_ARTIFACT_SOURCE':str(artifact_source)}
        return subprocess.run([node,str(ROOT/'.github/actions/deployment-engine/index.cjs')],
                              cwd=ROOT,env=env,text=True,capture_output=True,timeout=300)

    def make_ready_action_artifact(self, name):
        directory, plan = self.admitted_plan(name=name)
        with patch.dict(os.environ,{**self.environment,'FAKE_STATUSES':json.dumps(['SUCCESS'])},clear=True):
            instance=engine.Engine(directory)
            instance.prepare()
            with contextlib.redirect_stdout(io.StringIO()):
                instance.result()
        return directory, plan

    def test_real_action_download_fresh_release_resumes_same_build_id(self):
        for first_status in ('WORKING','STATUS_UNKNOWN'):
            with self.subTest(first_status=first_status):
                self.reset_fake()
                retained, plan, first = self.make_action_checkpoint(
                    'action-resume-'+first_status.lower(),statuses=[first_status])
                self.assertEqual(first['builds']['auth']['build_id'],BUILD_IDS[0])
                resumed=self.run_action_prepare(source=plan['source'],tag=plan['tag'],
                                                components='auth',artifact_source=retained,
                                                extra={'FAKE_STATUSES':json.dumps(['SUCCESS'])})
                self.assertEqual(resumed.returncode,0,resumed.stdout+resumed.stderr)
                result=json.loads(resumed.stdout)
                self.assertEqual(result['stage'],'ready')
                self.assertEqual(result['builds']['auth']['build_id'],BUILD_IDS[0])
                self.assertEqual(self.fake_data()['submit_count'],1)
                describe=[call for call in self.fake_data()['calls'] if call[:2]==['builds','describe']]
                self.assertGreaterEqual(len(describe),2)
                self.assertTrue(all(call[2]==BUILD_IDS[0] for call in describe))
                self.assertTrue((self.root/f'runner-temp-{self.action_runs}'/'reuse'/'state.json').exists())
                self.assertTrue((self.root/f'runner-temp-{self.action_runs}'/'release'/'receipts'/'auth.json').exists())

    def test_real_action_download_v2_checkpoint_keeps_legacy_id_sequence_and_handle(self):
        retained, plan, first = self.make_action_checkpoint(
            'action-v2-resume',statuses=['WORKING'])
        legacy = {'schema':2,'source':plan['source'],'branch':plan['branch'],'tag':plan['tag'],
                  'engine':'1'*40,'engine_content':'2'*64,'normalized':plan['normalized'],
                  'identities':plan['identities'],'dev_reference':plan['dev_reference'],
                  'selected':plan['selected']}
        legacy['id'] = digest(legacy)
        state = read(retained/'state.json')
        state['plan'] = legacy['id']
        state.pop('checkpoint_schema',None)
        state.pop('executor_sha',None)
        write(retained/'plan.json',legacy)
        write(retained/'state.json',state)
        old_sequence = state['sequence']
        resumed = self.run_action_prepare(source=plan['source'],tag=plan['tag'],
                                          components='auth',artifact_source=retained,
                                          extra={'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode,0,resumed.stdout+resumed.stderr)
        directory=self.root/f'runner-temp-{self.action_runs}'/'release'
        kept_plan=read(directory/'plan.json')
        kept_state=read(directory/'state.json')
        self.assertEqual(kept_plan,legacy)
        self.assertEqual(kept_state['plan'],legacy['id'])
        self.assertGreater(kept_state['sequence'],old_sequence)
        self.assertEqual(kept_state['checkpoint_schema'],1)
        self.assertEqual(kept_state['executor_sha'],plan['executor_sha'])
        self.assertEqual(read(directory/'receipts/auth.json')['build']['build_id'],BUILD_IDS[0])
        self.assertEqual(self.fake_data()['submit_count'],1)

    def test_registered_dev_release_resumes_legacy_source_with_current_executor(self):
        source = '60178904b62f7702a236689576ffb5862b8addcc'
        executor = subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
        wrapper=(ROOT/'.github/workflows/deploy-dev.yml').read_text()
        shared=(ROOT/'.github/workflows/cd.yml').read_text()
        self.assertIn("source_sha: ${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}",wrapper)
        self.assertIn('executor_sha: ${{ github.sha }}',wrapper)
        self.assertIn('SOURCE: ${{ inputs.source_sha }}',shared)
        self.assertIn('EXECUTOR_SHA: ${{ inputs.executor_sha }}',shared)

        retained, plan, first = self.make_action_checkpoint(
            'registered-cross-executor-resume',statuses=['WORKING'],source=source)
        self.assertEqual(plan['source'],source)
        self.assertEqual(first['builds']['auth']['build_id'],BUILD_IDS[0])
        legacy={'schema':2,**{key:value for key,value in plan.items()
                             if key not in ('id','schema','executor_sha')},
                'engine':'1'*40,'engine_content':'2'*64}
        legacy['id']=digest(legacy)
        state=read(retained/'state.json')
        state['plan']=legacy['id']
        state.pop('checkpoint_schema',None)
        state.pop('executor_sha',None)
        write(retained/'plan.json',legacy)
        write(retained/'state.json',state)
        original_sequence=state['sequence']

        resumed=self.run_action_prepare(source=source,executor_sha=executor,tag=plan['tag'],
                                        components='auth',artifact_source=retained,
                                        extra={'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode,0,resumed.stdout+resumed.stderr)
        runner=self.root/f'runner-temp-{self.action_runs}'
        release=runner/'release'
        kept_plan=read(release/'plan.json')
        kept_state=read(release/'state.json')
        result=json.loads(resumed.stdout)
        self.assertEqual(result['stage'],'ready')
        self.assertEqual(kept_plan,legacy)
        self.assertEqual(kept_state['plan'],legacy['id'])
        self.assertGreater(kept_state['sequence'],original_sequence)
        self.assertEqual(kept_state['executor_sha'],executor)
        self.assertEqual(result['builds']['auth']['build_id'],BUILD_IDS[0])
        self.assertEqual(read(release/'receipts/auth.json')['build']['build_id'],BUILD_IDS[0])
        self.assertEqual(self.fake_data()['submit_count'],1)

    def test_real_action_download_blocks_idless_unknown_without_submit(self):
        retained, plan, first = self.make_action_checkpoint(
            'action-resume-idless',extra={'FAKE_SUBMIT_EXIT':'17'})
        self.assertEqual(first['builds']['auth']['status'],'SUBMIT_UNKNOWN')
        self.assertIsNone(first['builds']['auth']['build_id'])
        resumed=self.run_action_prepare(source=plan['source'],tag=plan['tag'],
                                        components='auth',artifact_source=retained)
        self.assertEqual(resumed.returncode,1)
        result=self.result_from(self.root/f'runner-temp-{self.action_runs}'/'release')
        self.assertEqual((result['status'],result['reason']),
                         ('unknown','build-identity-unverified'))
        self.assertIsNone(result['builds']['auth']['build_id'])
        self.assertEqual(self.fake_data()['submit_count'],1)

    def test_real_action_download_success_digest_failure_only_retries_digest(self):
        retained, plan, first = self.make_action_checkpoint(
            'action-resume-digest',statuses=['SUCCESS'],extra={'FAKE_TAG_LOOKUP_EXIT':'9'})
        self.assertEqual(first['builds']['auth']['status'],'SUCCESS')
        self.assertFalse((retained/'receipts'/'auth.json').exists())
        resumed=self.run_action_prepare(source=plan['source'],tag=plan['tag'],
                                        components='auth',artifact_source=retained)
        self.assertEqual(resumed.returncode,0,resumed.stdout+resumed.stderr)
        result=json.loads(resumed.stdout)
        self.assertEqual(result['builds']['auth']['build_id'],BUILD_IDS[0])
        self.assertEqual(self.fake_data()['submit_count'],1)
        self.assertEqual(len([call for call in self.fake_data()['calls']
                              if call[:2]==['builds','describe']]),1)

    def test_real_action_cross_plan_unresolved_handle_fails_closed(self):
        retained, plan, first = self.make_action_checkpoint('action-cross-plan')
        self.assertEqual(first['builds']['auth']['status'],'STATUS_UNKNOWN')
        changed_tag='test-lwc358-action-cross-plan-next'
        result=self.run_action_prepare(source=plan['source'],tag=changed_tag,
                                       components='auth',artifact_source=retained)
        self.assertEqual(result.returncode,1)
        body=self.result_from(self.root/f'runner-temp-{self.action_runs}'/'release')
        self.assertEqual((body['status'],body['reason']),
                         ('unknown','cross-plan-build-checkpoint-unresolved'))
        self.assertEqual(body['builds']['auth']['build_id'],BUILD_IDS[0])
        self.assertEqual(self.fake_data()['submit_count'],1)

    def test_cross_plan_source_target_tag_selection_input_and_provenance_changes_fail_closed(self):
        for changed in ('source','target','tag','selected','input-identity','dev-reference'):
            with self.subTest(changed=changed):
                self.reset_fake()
                current=self.make_plan(('auth',),'identity-current-'+changed)
                current_plan=read(current/'plan.json')
                retained=self.root/('identity-retained-'+changed)
                retained.mkdir()
                old_plan=json.loads(json.dumps(current_plan))
                if changed == 'source':
                    old_plan['source']='b'*40
                elif changed == 'target':
                    old_plan['normalized']['environment']='production'
                    old_plan['branch']='main'
                elif changed == 'tag':
                    old_plan['tag']='test-lwc358-different-tag'
                elif changed == 'selected':
                    old_plan['selected']=['auth','bff']
                    old_plan['identities']['bff']={'profile':'test','inputs':'other','files':[]}
                elif changed == 'input-identity':
                    old_plan['identities']['auth']['inputs']='changed'
                else:
                    old_plan['dev_reference']='different-dev-reference'
                old_plan['id']=engine.plan_id(old_plan)
                write(retained/'plan.json',old_plan)
                write(retained/'state.json',{'plan':old_plan['id'],'status':'prepared',
                    'components':{},'sequence':1,'builds':{'auth':{
                        'project_id':PROJECT,'location':LOCATION,'build_id':BUILD_IDS[0],
                        'identity_verified':True,'status':'STATUS_UNKNOWN',
                        'last_observed_status':'STATUS_UNKNOWN','poll_outcome':'status_unknown'}}})
                with patch.dict(os.environ,{**self.environment},clear=True):
                    instance=engine.Engine(current)
                    with self.assertRaises(Breakpoint) as caught:
                        instance.prepare(reuse=retained)
                self.assertEqual(caught.exception.reason,
                                 'cross-plan-build-checkpoint-unresolved')
                self.assertEqual(self.fake_data()['submit_count'],0)

    def test_same_plan_runtime_started_and_corrupt_checkpoint_fail_closed(self):
        for mode in ('runtime-started','plan-digest','state-plan'):
            with self.subTest(mode=mode):
                current=self.make_plan(('auth',),'checkpoint-invalid-'+mode)
                plan=read(current/'plan.json')
                retained=self.root/('checkpoint-retained-'+mode)
                retained.mkdir()
                state={'plan':plan['id'],'status':'prepared','components':{},'sequence':1,'builds':{}}
                if mode == 'runtime-started':
                    state['status']='deploying'
                    state['components']={'auth':{'status':'pending'}}
                elif mode == 'state-plan':
                    state['plan']='0'*64
                else:
                    plan['tag']='test-lwc358-corrupt-plan'
                write(retained/'plan.json',plan)
                write(retained/'state.json',state)
                with patch.dict(os.environ,{**self.environment},clear=True):
                    instance=engine.Engine(current)
                    with self.assertRaises(Breakpoint) as caught:
                        instance.prepare(reuse=retained)
                expected=('stage1-checkpoint-runtime-started' if mode=='runtime-started'
                          else 'stage1-checkpoint-invalid')
                self.assertEqual(caught.exception.reason,expected)
                self.assertEqual(self.fake_data()['submit_count'],0)

    def test_real_action_terminal_failure_explicit_retry_and_receipt_expansion(self):
        terminal, plan, first = self.make_action_checkpoint(
            'action-terminal-retry',statuses=['FAILURE'])
        self.assertEqual(first['builds']['auth']['status'],'FAILURE')
        retried=self.run_action_prepare(source=plan['source'],tag=plan['tag'],
                                        components='auth',artifact_source=terminal,
                                        extra={'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(retried.returncode,0,retried.stdout+retried.stderr)
        self.assertEqual(json.loads(retried.stdout)['builds']['auth']['build_id'],BUILD_IDS[1])
        self.assertEqual(self.fake_data()['submit_count'],2)

        self.reset_fake()
        ready, old_plan = self.make_ready_action_artifact('action-receipt-expansion')
        before=self.fake_data()['submit_count']
        expanded=self.run_action_prepare(source=old_plan['source'],tag=old_plan['tag'],
                                         components='auth,bff',artifact_source=ready)
        self.assertEqual(expanded.returncode,0,expanded.stdout+expanded.stderr)
        result=json.loads(expanded.stdout)
        self.assertEqual(result['stage'],'ready')
        auth_receipt=json.loads((self.root/f'runner-temp-{self.action_runs}'/'release'/'receipts'/'auth.json').read_text())
        self.assertEqual(auth_receipt['build']['build_id'],BUILD_IDS[0])
        self.assertEqual(result['builds']['bff']['build_id'],BUILD_IDS[1])
        self.assertEqual(self.fake_data()['submit_count']-before,1)

    def run_direct(self, directory, extra=None, max_reads=1):
        with patch.dict(os.environ, {**self.environment, **(extra or {})}, clear=True), \
             patch.object(providers, 'BUILD_POLL_MAX_READS', max_reads), \
             patch.object(providers.time, 'sleep', lambda _seconds: None):
            instance = engine.Engine(directory)
            try:
                instance.prepare()
                error = None
            except Breakpoint as exc:
                error = exc
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                instance.result(error)
            return error, output.getvalue(), self.result_from(directory)

    def test_auth_and_bff_async_submit_exact_tuple_status_only_then_digest(self):
        for component in ('auth', 'bff'):
            with self.subTest(component=component):
                self.reset_fake()
                directory = self.make_plan((component,), component)
                result = self.invoke(directory, {'FAKE_STATUSES': json.dumps(['WORKING','SUCCESS']),
                                                'FAKE_RAW_FIELD': RAW_SENTINEL})
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                body = json.loads(result.stdout)
                self.assertEqual(body['stage'], 'ready')
                record = body['builds'][component]
                expected_id = BUILD_IDS[0]
                self.assertEqual((record['project_id'], record['location'], record['build_id']),
                                 (PROJECT, LOCATION, expected_id))
                self.assertTrue(record['identity_verified'])
                self.assertEqual(record['status'], 'SUCCESS')
                calls = self.fake_data()['calls']
                identity_reads = [c for c in calls if c[:2] == ['projects','describe']]
                submits = [c for c in calls if c[:2] == ['builds','submit']]
                describes = [c for c in calls if c[:2] == ['builds','describe']]
                self.assertEqual(len(identity_reads), 1)
                self.assertEqual(identity_reads[0][2], PROJECT)
                self.assertIn('--format=json', identity_reads[0])
                self.assertEqual(len(submits), 1)
                self.assertIn('--async', submits[0])
                self.assertIn('--format=json', submits[0])
                self.assertIn('--quiet', submits[0])
                self.assertEqual(next(x.split('=',1)[1] for x in submits[0] if x.startswith('--region=')), LOCATION)
                self.assertEqual(submits[0][submits[0].index('--project')+1], PROJECT)
                self.assertNotIn('--suppress-logs', submits[0])
                self.assertEqual(len(describes), 2)
                for call in describes:
                    self.assertEqual(call[2], expected_id)
                    self.assertEqual(call[call.index('--project')+1], PROJECT)
                    self.assertEqual(call[call.index('--region')+1], LOCATION)
                    self.assertIn('--format=json', call)
                    self.assertIn('--quiet', call)
                self.assertFalse(any('logging' in c or 'log' == c[:1] for c in calls))
                receipt = json.loads((directory / 'receipts' / (component + '.json')).read_text())
                self.assertEqual(receipt['build']['build_id'], expected_id)
                self.assertNotIn(RAW_SENTINEL, result.stdout + result.stderr + json.dumps(body) + json.dumps(receipt))

                submits_before_repeat = self.fake_data()['submit_count']
                repeated = self.invoke(directory, {'FAKE_STATUSES': json.dumps(['SUCCESS'])})
                self.assertEqual(repeated.returncode, 0, repeated.stdout + repeated.stderr)
                self.assertEqual(self.fake_data()['submit_count'], submits_before_repeat,
                                 'ready receipt must prevent duplicate build')

    def test_project_identity_lookup_must_match_config_before_submit(self):
        cases = (
            ('wrong-project-id', {'FAKE_PROJECT_IDENTITY_PROJECT':'another-project',
                                  'FAKE_PROJECT_NUMBER':PROJECT_NUMBER}),
            ('malformed-number', {'FAKE_PROJECT_IDENTITY_PROJECT':PROJECT,
                                  'FAKE_PROJECT_NUMBER':'58085'}),
            ('lookup-api-error', {'FAKE_PROJECT_LOOKUP_EXIT':'17',
                                  'FAKE_PROJECT_LOOKUP_STDERR':RAW_SENTINEL}),
        )
        for name, extra in cases:
            with self.subTest(name=name):
                self.reset_fake()
                directory = self.make_plan(('auth',), 'project-identity-' + name)
                result = self.invoke(directory, extra)
                self.assertEqual(result.returncode, 1)
                body = self.result_from(directory)
                self.assertEqual(body['status'],'failed')
                if name == 'lookup-api-error':
                    self.assertEqual(body['reason'],'gcp-project-identity-unavailable')
                    self.assertEqual(body['failure_diagnostic']['exit_code'],17)
                    self.assertNotIn(RAW_SENTINEL,result.stdout+result.stderr+json.dumps(body))
                else:
                    self.assertEqual((body['reason'], body['allowed_next_action']),
                                     ('gcp-project-identity-mismatch','verify-project-identity'))
                self.assertEqual(body['builds']['auth']['status'],'SUBMIT_REJECTED')
                calls = self.fake_data()['calls']
                self.assertEqual(len([c for c in calls if c[:2] == ['projects','describe']]),1)
                self.assertEqual(len([c for c in calls if c[:2] == ['builds','submit']]),0)

    def test_resume_rechecks_project_number_mapping_before_status_read(self):
        directory = self.make_plan(('auth',), 'resume-project-identity')
        first_error, _, first_body = self.run_direct(
            directory, {'FAKE_STATUSES':json.dumps(['WORKING'])}, max_reads=1)
        self.assertIsNotNone(first_error)
        self.assertEqual(first_body['builds']['auth']['build_id'],BUILD_IDS[0])
        submits_before = self.fake_data()['submit_count']
        status_reads_before = self.fake_data()['status_count']['auth']

        denied = self.invoke(directory, {'FAKE_PROJECT_LOOKUP_EXIT':'17',
                                         'FAKE_PROJECT_LOOKUP_STDERR':RAW_SENTINEL})
        self.assertEqual(denied.returncode,1)
        denied_body = self.result_from(directory)
        self.assertEqual(denied_body['status'],'unknown')
        self.assertEqual(denied_body['builds']['auth']['build_id'],BUILD_IDS[0])
        self.assertEqual(denied_body['builds']['auth']['poll_outcome'],
                         'project_identity_unavailable')
        self.assertNotIn(RAW_SENTINEL,denied.stdout+denied.stderr+json.dumps(denied_body))
        self.assertEqual(self.fake_data()['submit_count'],submits_before)
        self.assertEqual(self.fake_data()['status_count']['auth'],status_reads_before)

        resumed = self.invoke(directory, {'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode,0,resumed.stdout+resumed.stderr)
        self.assertEqual(json.loads(resumed.stdout)['builds']['auth']['build_id'],BUILD_IDS[0])
        self.assertEqual(self.fake_data()['submit_count'],submits_before)

    def test_numeric_resource_name_matches_only_authoritative_project_number(self):
        directory = self.make_plan(('auth',), 'non-default-project-number')
        other_valid_number = '987654321098'
        result = self.invoke(directory, {'FAKE_PROJECT_NUMBER':other_valid_number})
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        record=self.result_from(directory)['builds']['auth']
        self.assertTrue(record['identity_verified'])
        calls=self.fake_data()['calls']
        identity=next(c for c in calls if c[:2] == ['projects','describe'])
        self.assertEqual(identity[2],PROJECT)
        self.assertTrue(any(c[:2] == ['builds','submit'] for c in calls))
        self.assertTrue(any(c[:2] == ['builds','describe'] for c in calls))
        self.assertEqual(self.fake_data()['build_component'][BUILD_IDS[0]],'auth')
        self.assertEqual(record['build_id'],BUILD_IDS[0])

    def test_configured_project_id_resource_name_remains_accepted(self):
        directory = self.make_plan(('bff',), 'project-id-resource-name')
        result = self.invoke(directory, {'FAKE_SUBMIT_RESOURCE_PROJECT':PROJECT,
                                         'FAKE_STATUS_RESOURCE_PROJECT':PROJECT})
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        record=self.result_from(directory)['builds']['bff']
        self.assertTrue(record['identity_verified'])
        self.assertEqual(record['status'],'SUCCESS')

    def test_verified_id_is_uploaded_before_poll_and_pending_resume_does_not_resubmit(self):
        directory = self.make_plan(('auth',), 'durable')
        extra = {'GITHUB_ACTIONS':'true', 'GITHUB_RUN_ID':'100', 'GITHUB_RUN_ATTEMPT':'1',
                 'FAKE_ASSERT_DURABLE_UPLOAD':'1', 'FAKE_STATUSES':json.dumps(['WORKING'])}
        with patch.dict(os.environ, {**self.environment, **extra}, clear=True), \
             patch.object(providers, 'BUILD_POLL_MAX_READS', 1), \
             patch.object(providers, 'BUILD_POLL_INTERVAL_SECONDS', 0):
            instance = engine.Engine(directory)
            with self.assertRaises(Breakpoint) as caught:
                instance.prepare()
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                instance.result(caught.exception)
        result = self.result_from(directory)
        self.assertEqual(result['status'], 'unknown')
        self.assertEqual(result['builds']['auth']['build_id'], BUILD_IDS[0])
        self.assertEqual(result['builds']['auth']['poll_outcome'], 'deadline')
        uploads = json.loads(self.fake_uploads.read_text())
        self.assertTrue(any(x.get('builds',{}).get('auth',{}).get('build_id') == BUILD_IDS[0]
                            and x['builds']['auth']['identity_verified'] for x in uploads))
        before = self.fake_data()['submit_count']
        resumed = self.invoke(directory, {'GITHUB_ACTIONS':'true','GITHUB_RUN_ID':'101',
                                          'GITHUB_RUN_ATTEMPT':'1','FAKE_ASSERT_DURABLE_UPLOAD':'1',
                                          'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode, 0, resumed.stdout + resumed.stderr)
        self.assertEqual(self.fake_data()['submit_count'], before)
        self.assertEqual(json.loads(resumed.stdout)['builds']['auth']['build_id'], BUILD_IDS[0])

    def test_terminal_failure_and_cancel_are_recorded_and_not_retried_in_same_invocation(self):
        for status in ('FAILURE', 'CANCELLED'):
            with self.subTest(status=status):
                self.reset_fake()
                directory = self.make_plan(('auth',), 'terminal-' + status.lower())
                result = self.invoke(directory, {'FAKE_STATUSES':json.dumps([status])})
                self.assertEqual(result.returncode, 1)
                body = self.result_from(directory)
                record = body['builds']['auth']
                self.assertEqual(body['status'], 'failed')
                self.assertEqual(body['allowed_next_action'], 'inspect-build-logs-by-id')
                self.assertEqual(record['build_id'], BUILD_IDS[0])
                self.assertEqual(record['status'], status)
                self.assertEqual(record['poll_outcome'], 'terminal_failure')
                self.assertEqual(self.fake_data()['submit_count'], 1)
                self.assertFalse(any(c[:2] == ['artifacts','docker'] for c in self.fake_data()['calls']))

    def test_poll_deadline_and_status_unknown_resume_same_id_without_duplicate_build(self):
        directory = self.make_plan(('auth',), 'poll-resume')
        error, _, body = self.run_direct(directory, {'FAKE_STATUSES':json.dumps(['WORKING']),
                                                     'FAKE_BUILD_IDS':json.dumps([BUILD_IDS[0]])}, max_reads=1)
        self.assertIsNotNone(error)
        with patch.dict(os.environ, {**self.environment,'FAKE_STATUSES':json.dumps(['WORKING'])}, clear=True), \
             patch.object(providers, 'BUILD_POLL_MAX_READS', 2), \
             patch.object(providers, 'BUILD_POLL_INTERVAL_SECONDS', 0):
            instance = engine.Engine(directory)
            with self.assertRaises(Breakpoint) as caught:
                instance.prepare()
            instance.result(caught.exception)
        body = self.result_from(directory)
        self.assertEqual(body['status'], 'unknown')
        self.assertEqual(body['builds']['auth']['build_id'], BUILD_IDS[0])
        self.assertEqual(self.fake_data()['status_count']['auth'], 3)
        resumed = self.invoke(directory, {'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode, 0, resumed.stdout + resumed.stderr)
        self.assertEqual(self.fake_data()['submit_count'], 1)
        self.assertEqual(json.loads(resumed.stdout)['builds']['auth']['build_id'], BUILD_IDS[0])

        unknown_dir = self.make_plan(('auth',), 'status-unknown')
        self.reset_fake()
        unknown = self.invoke(unknown_dir, {'FAKE_STATUSES':json.dumps(['STATUS_UNKNOWN'])})
        self.assertEqual(unknown.returncode, 1)
        unknown_body = self.result_from(unknown_dir)
        self.assertEqual(unknown_body['status'], 'unknown')
        self.assertEqual(unknown_body['builds']['auth']['build_id'], BUILD_IDS[0])

    def test_status_api_error_retains_id_redacts_output_and_reconciles_same_build(self):
        directory = self.make_plan(('auth',), 'status-api-error')
        failed = self.invoke(directory, {'FAKE_STATUS_API_ERROR':'1',
                                         'FAKE_STATUS_STDERR':RAW_SENTINEL})
        self.assertEqual(failed.returncode, 1)
        body = self.result_from(directory)
        self.assertEqual(body['status'], 'unknown')
        self.assertEqual(body['builds']['auth']['build_id'], BUILD_IDS[0])
        self.assertEqual(body['builds']['auth']['poll_outcome'], 'status_unavailable')
        self.assertNotIn(RAW_SENTINEL, failed.stdout + failed.stderr + json.dumps(body))
        resumed = self.invoke(directory, {'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(resumed.returncode, 0, resumed.stdout + resumed.stderr)
        self.assertEqual(self.fake_data()['submit_count'], 1)

    def test_submit_api_error_or_timeout_has_no_id_and_blocks_blind_replay(self):
        for mode, extra in (
            ('api-error', {'FAKE_SUBMIT_EXIT':'17','FAKE_SUBMIT_STDERR':RAW_SENTINEL}),
            ('timeout', {'FAKE_SUBMIT_TIMEOUT':'1','FAKE_SUBMIT_STDERR':RAW_SENTINEL}),
        ):
            with self.subTest(mode=mode):
                directory = self.make_plan(('auth',), 'submit-' + mode)
                failed = self.invoke(directory, extra)
                self.assertEqual(failed.returncode, 1)
                body = self.result_from(directory)
                self.assertEqual(body['status'], 'unknown')
                record = body['builds']['auth']
                self.assertIsNone(record['build_id'])
                self.assertEqual(record['poll_outcome'], 'submit_unknown')
                self.assertNotIn(RAW_SENTINEL, failed.stdout + failed.stderr + json.dumps(body))
                before = self.fake_data()['submit_count']
                resumed = self.invoke(directory)
                self.assertEqual(resumed.returncode, 1)
                self.assertEqual(self.fake_data()['submit_count'], before)

    def test_permission_rejection_is_typed_and_terminal_build_failure_can_be_explicitly_retried(self):
        directory = self.make_plan(('auth',), 'explicit-retry')
        denied = self.invoke(directory, {'FAKE_SUBMIT_EXIT':'7',
                                         'FAKE_SUBMIT_STDERR':'PERMISSION_DENIED ' + RAW_SENTINEL})
        self.assertEqual(denied.returncode, 1)
        body = self.result_from(directory)
        self.assertEqual((body['reason'], body['status'], body['allowed_next_action']),
                         ('permission-denied','failed','restore-existing-principal-permission'))
        self.assertEqual(body['builds']['auth']['status'], 'SUBMIT_REJECTED')
        self.assertNotIn(RAW_SENTINEL, denied.stdout + denied.stderr + json.dumps(body))

        # A separate invocation may retry only after a verified terminal result.
        self.reset_fake()
        directory = self.make_plan(('auth',), 'explicit-terminal-retry')
        first = self.invoke(directory, {'FAKE_STATUSES':json.dumps(['FAILURE'])})
        self.assertEqual(first.returncode, 1)
        second = self.invoke(directory, {'FAKE_BUILD_IDS':json.dumps(list(BUILD_IDS)),
                                         'FAKE_STATUSES':json.dumps(['SUCCESS'])})
        self.assertEqual(second.returncode, 0, second.stdout + second.stderr)
        self.assertEqual(self.fake_data()['submit_count'], 2)
        self.assertEqual(json.loads(second.stdout)['builds']['auth']['build_id'], BUILD_IDS[1])

    def test_malformed_id_and_submit_project_or_location_mismatch_never_poll(self):
        cases = (
            ('bad-id', {'FAKE_BUILD_IDS':json.dumps([RAW_SENTINEL])}, None, 'IDENTITY_INVALID'),
            ('wrong-project', {'FAKE_SUBMIT_PROJECT':'safe-other-project'}, BUILD_IDS[0], 'IDENTITY_MISMATCH'),
            ('wrong-location', {'FAKE_SUBMIT_LOCATION':'us-central1'}, BUILD_IDS[0], 'IDENTITY_MISMATCH'),
            ('wrong-project-number', {'FAKE_SUBMIT_RESOURCE_PROJECT':'987654321098'}, BUILD_IDS[0], 'IDENTITY_MISMATCH'),
            ('wrong-resource-id', {'FAKE_SUBMIT_RESOURCE_ID':BUILD_IDS[1]}, BUILD_IDS[0], 'IDENTITY_MISMATCH'),
            ('missing-resource-name', {'FAKE_SUBMIT_OMIT_NAME':'1'}, BUILD_IDS[0], 'IDENTITY_MISMATCH'),
        )
        for name, extra, expected_id, expected_status in cases:
            with self.subTest(name=name):
                self.reset_fake()
                directory = self.make_plan(('auth',), 'identity-' + name)
                result = self.invoke(directory, extra)
                self.assertEqual(result.returncode, 1)
                body = self.result_from(directory)
                record = body['builds']['auth']
                self.assertEqual(body['status'], 'unknown')
                self.assertEqual(record['status'], expected_status)
                self.assertEqual(record['build_id'], expected_id)
                self.assertFalse(record['identity_verified'])
                self.assertEqual(self.fake_data()['status_count'].get('auth', 0), 0)
                self.assertNotIn(RAW_SENTINEL, result.stdout + result.stderr + json.dumps(body))

    def test_status_project_or_location_mismatch_keeps_id_unverified_and_stops(self):
        for name, override in (('project', {'FAKE_STATUS_PROJECT':'safe-other-project'}),
                               ('location', {'FAKE_STATUS_LOCATION':'us-central1'}),
                               ('project-number', {'FAKE_STATUS_RESOURCE_PROJECT':'987654321098'}),
                               ('resource-id', {'FAKE_STATUS_RESOURCE_ID':BUILD_IDS[1]}),
                               ('missing-resource-name', {'FAKE_STATUS_OMIT_NAME':'1'})):
            with self.subTest(name=name):
                self.reset_fake()
                directory = self.make_plan(('auth',), 'status-identity-' + name)
                result = self.invoke(directory, override)
                self.assertEqual(result.returncode, 1)
                body = self.result_from(directory)
                record = body['builds']['auth']
                self.assertEqual(body['status'], 'unknown')
                self.assertEqual(record['build_id'], BUILD_IDS[0])
                self.assertFalse(record['identity_verified'])
                self.assertEqual(record['poll_outcome'], 'identity_mismatch')
                self.assertFalse(any(c[:4] == ['artifacts','docker','images','describe']
                                     for c in self.fake_data()['calls']))

    def test_digest_failure_keeps_successful_build_id_and_retry_does_not_resubmit(self):
        directory = self.make_plan(('bff',), 'digest-retry')
        failed = self.invoke(directory, {'FAKE_TAG_LOOKUP_EXIT':'9',
                                         'FAKE_STATUS_STDERR':RAW_SENTINEL})
        self.assertEqual(failed.returncode, 1)
        body = self.result_from(directory)
        self.assertEqual(body['builds']['bff']['status'], 'SUCCESS')
        self.assertEqual(body['builds']['bff']['build_id'], BUILD_IDS[0])
        self.assertEqual(body['failure_diagnostic']['stage'], 'tag-digest-resolve')
        self.assertNotIn(RAW_SENTINEL, failed.stdout + failed.stderr + json.dumps(body))
        resumed = self.invoke(directory)
        self.assertEqual(resumed.returncode, 0, resumed.stdout + resumed.stderr)
        self.assertEqual(self.fake_data()['submit_count'], 1)

    def test_multi_component_prepare_fails_barrier_without_runtime_and_retains_both_build_ids(self):
        directory = self.make_plan(('auth','bff'), 'two-components')
        result = self.invoke(directory, {
            'FAKE_STATUSES_AUTH':json.dumps(['SUCCESS']),
            'FAKE_STATUSES_BFF':json.dumps(['FAILURE']),
        })
        self.assertEqual(result.returncode, 1)
        body = self.result_from(directory)
        self.assertEqual(body['component'], 'bff')
        self.assertEqual(body['builds']['auth']['status'], 'SUCCESS')
        self.assertEqual(body['builds']['bff']['status'], 'FAILURE')
        self.assertTrue((directory/'receipts/auth.json').exists())
        self.assertFalse((directory/'receipts/bff.json').exists())
        calls = self.fake_data()['calls']
        self.assertFalse(any(c and c[0] == 'run' for c in calls))
        self.assertEqual(self.fake_data()['submit_count'], 2)


if __name__ == '__main__':
    unittest.main(verbosity=2)
