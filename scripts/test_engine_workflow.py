#!/usr/bin/env python3
"""r2 workflow contracts, replacing the retired mixed build/mutate/CD polling graph."""
import json
from pathlib import Path
import re
import unittest
import yaml

ROOT=Path(__file__).resolve().parents[1]

class EngineWorkflowContract(unittest.TestCase):
    def test_diagnostic_callers_obey_readonly_reusable_workflow_permission_ceiling(self):
        read_only={'contents':'read','actions':'read','id-token':'write'}
        rank={'none':0,'read':1,'write':2}
        standalone='./.github/workflows/cd-auth-image-diagnostic.yml'
        for file,job_name in [('deploy-dev.yml','auth-image-diagnostic'),
                              ('recover-deployment.yml','auth-image-diagnostic')]:
            caller=yaml.safe_load((ROOT/'.github/workflows'/file).read_text())
            job=caller['jobs'][job_name]
            self.assertEqual(job['permissions'],read_only)
            called_path=job['uses']
            called=yaml.safe_load((ROOT/called_path.removeprefix('./')).read_text())
            scopes=[('workflow',called.get('permissions',{}))]
            scopes.extend((child_name,child['permissions']) for child_name,child in called['jobs'].items()
                          if 'permissions' in child)
            for scope_name,scope in scopes:
                for permission,level in scope.items():
                    ceiling=job['permissions'].get(permission,'none')
                    self.assertLessEqual(rank[level],rank[ceiling],
                                         f'{file} grants {ceiling} but called {called_path} {scope_name} requests {level} for {permission}')
            self.assertEqual(called_path,standalone)
            self.assertEqual(set(called['jobs']),{'auth-image-diagnostic'})

    def test_single_runtime_authority_stays_in_release_workflow(self):
        s=(ROOT/'.github/workflows/cd.yml').read_text()
        parsed=yaml.safe_load(s)
        self.assertEqual(set(parsed['jobs']),{'release','pipeline-config-only'})
        self.assertEqual(parsed['jobs']['release']['environment'],
                         "${{ inputs.environment == 'production' && 'Production' || 'Development' }}")
        prepare=next(step for step in parsed['jobs']['release']['steps']
                     if step.get('with',{}).get('operation') == 'prepare')
        runtime_step=next(step for step in parsed['jobs']['release']['steps']
                          if step.get('with',{}).get('operation') == 'runtime')
        for step in (prepare,runtime_step):
            self.assertEqual(step['env']['VERCEL_TOKEN'],'${{ secrets.VERCEL_TOKEN }}')
            self.assertEqual(step['env']['VERCEL_PROJECT_ID'],'${{ secrets.VERCEL_PROJECT_ID }}')
            self.assertEqual(step['env']['VERCEL_TEAM_ID'],'${{ secrets.VERCEL_TEAM_ID }}')
        self.assertIn('group: lwc-engine-${{ inputs.environment }}',s)
        self.assertIn('cancel-in-progress: false',s)
        self.assertLess(s.index('Publish ready stage barrier'),s.index('Execute bounded runtime'))
        self.assertIn("if: inputs.operation == 'release'",s)
        config_only=parsed['jobs']['pipeline-config-only']
        self.assertEqual(config_only['if'],"inputs.operation == 'config-only'")
        self.assertEqual(config_only['permissions'],{'contents':'read','id-token':'write'})
        delivery=next(step for step in config_only['steps'] if 'pipeline_config_only.py' in step.get('run',''))
        self.assertIn('without replacing image',delivery['name'])
        self.assertNotIn('--image',delivery['run'])
        self.assertNotIn('docker',delivery['run'])
        runtime=s[s.index('      - name: Execute bounded runtime'):]
        self.assertNotIn('build',runtime)
        self.assertIn('operation: runtime',runtime)
        self.assertEqual(sum(step.get('with',{}).get('operation') == 'runtime'
                             for job in parsed['jobs'].values() for step in job.get('steps',[])),1)
        entry=(ROOT/'.github/actions/deployment-engine/index.cjs').read_text()
        self.assertIn("'--components',process.env.COMPONENTS",entry)
        for secret in ('VERCEL_TOKEN','VERCEL_PROJECT_ID','VERCEL_TEAM_ID','WIF_PROVIDER','WIF_SERVICE_ACCOUNT'):
            self.assertIn('secrets.'+secret,s)
        for forbidden in ('revalidate-ci','revalidate-before-provider','preflight-shared','provision-exportjob','gh run list','jobs execute'):
            self.assertNotIn(forbidden,s)

    def test_explicit_wrappers_and_recovery(self):
        for file,env,branch in [('deploy-dev.yml','development','develop'),('promote-production.yml','production','main')]:
            s=(ROOT/'.github/workflows'/file).read_text()
            for text in ('workflow_dispatch:','release_tag:','components:','artifact_id:','secrets: inherit','uses: ./.github/workflows/cd.yml','environment: '+env,"refs/heads/"+branch):
                self.assertIn(text,s)
            self.assertNotIn('workflow_run:',s)
            wrapper = yaml.safe_load(s)
            reusable = yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
            reusable_trigger = reusable.get('on', reusable.get(True, {}))
            self.assertEqual(wrapper['jobs']['release']['with']['executor_sha'], '${{ github.sha }}')
            if file == 'promote-production.yml':
                self.assertNotIn('force', wrapper['jobs']['release']['with'])
                self.assertEqual(wrapper['jobs']['release']['with']['source_sha'], '${{ github.sha }}')
            self.assertEqual(reusable_trigger['workflow_call']['inputs']['executor_sha'],
                             {'required': True, 'type': 'string'})
            self.assertEqual(reusable_trigger['workflow_call']['inputs']['force'],
                             {'type': 'boolean', 'default': False})
            self.assertEqual(next(step for step in reusable['jobs']['release']['steps']
                                  if step.get('uses', '').startswith('actions/checkout@'))['with']['ref'],
                             '${{ inputs.executor_sha }}')
        recovery=(ROOT/'.github/workflows/recover-deployment.yml').read_text()
        self.assertIn('[rollback, reactivate, deploy, tag, readback, diagnose-auth-image]',recovery)
        self.assertIn('uses: ./.github/workflows/cd.yml',recovery)

    def test_registered_dev_release_can_resume_stage1_source_with_current_executor(self):
        dev=yaml.safe_load((ROOT/'.github/workflows/deploy-dev.yml').read_text())
        dev_trigger=dev.get('on',dev.get(True,{}))
        inputs=dev_trigger['workflow_dispatch']['inputs']
        self.assertEqual(inputs['operation']['default'],'release')
        self.assertEqual(inputs['source_sha']['default'],'')
        self.assertEqual(dev['jobs']['release']['with']['source_sha'],
                         "${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}")
        self.assertEqual(dev['jobs']['release']['with']['executor_sha'],'${{ github.sha }}')
        self.assertEqual(dev['jobs']['release']['with']['artifact_id'],'${{ inputs.artifact_id }}')

        cd=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        job=cd['jobs']['release']
        self.assertEqual(job['env']['SOURCE'],'${{ inputs.source_sha }}')
        self.assertEqual(job['env']['EXECUTOR_SHA'],'${{ inputs.executor_sha }}')
        steps=job['steps']
        validate=next((index,step) for index,step in enumerate(steps)
                      if step.get('name')=='Validate retained Stage 1 source input')
        prepare=next((index,step) for index,step in enumerate(steps)
                     if step.get('with',{}).get('operation')=='prepare')
        auth=next(index for index,step in enumerate(steps)
                  if step.get('uses','').startswith('google-github-actions/auth@'))
        self.assertEqual(validate[1]['if'],"inputs.operation == 'release' && inputs.environment == 'development' && inputs.source_sha != github.sha")
        self.assertEqual(validate[1]['env']['ARTIFACT_ID'],'${{ inputs.artifact_id }}')
        self.assertIn('test -n "$ARTIFACT_ID"',validate[1]['run'])
        self.assertLess(validate[0],auth)
        self.assertLess(auth,prepare[0])
        self.assertEqual(prepare[1]['if'],"inputs.operation == 'release'")
        self.assertEqual(next(step for step in steps if step.get('id')=='ready')['if'],
                         "inputs.operation == 'release'")
        self.assertEqual(job['environment'],
                         "${{ inputs.environment == 'production' && 'Production' || 'Development' }}")

    def test_pinned_recovery_download_has_effective_github_token(self):
        workflow=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        release=workflow['jobs']['release']
        self.assertEqual(release['if'],"inputs.operation != 'diagnose-auth-image' && inputs.operation != 'config-only'")
        downloads=[step for step in release['steps']
                   if step.get('name') == 'Download pinned ready artifact or checkpoint']
        self.assertEqual(len(downloads),1)
        download=downloads[0]
        self.assertEqual(download['if'],"inputs.operation != 'release'")
        self.assertIn('node deploy/engine/artifacts.cjs download',download['run'])
        effective_env={**release.get('env',{}),**download.get('env',{})}
        self.assertEqual(effective_env.get('GH_TOKEN'),'${{ github.token }}')
        for credential in ('VERCEL_TOKEN','VERCEL_PROJECT_ID','VERCEL_TEAM_ID'):
            self.assertNotIn(credential,effective_env)

        transport=(ROOT/'deploy/engine/artifacts.cjs').read_text()
        download_impl=transport[transport.index('async function download('):
                                transport.index('async function main()')]
        self.assertIn('process.env.GH_TOKEN',download_impl)
        self.assertIn('await api(`actions/artifacts/${id}`)',download_impl)
        self.assertIn('await client.downloadArtifact',download_impl)

    def test_readonly_diagnostic_is_a_separate_fixed_workflow_branch(self):
        dev=yaml.safe_load((ROOT/'.github/workflows/deploy-dev.yml').read_text())
        dev_trigger=dev.get('on',dev.get(True,{}))
        dev_operation=dev_trigger['workflow_dispatch']['inputs']['operation']
        self.assertEqual(dev_operation['type'],'choice')
        self.assertEqual(dev_operation['default'],'release')
        self.assertEqual(dev_operation['options'],['release','config-only','deploy','rollback','reactivate','tag','readback','diagnose-auth-image'])
        self.assertEqual(dev['jobs']['release']['if'],
                         "github.ref == 'refs/heads/develop' && inputs.operation != 'diagnose-auth-image'")
        self.assertEqual(dev['jobs']['release']['with'],{
            'environment':'development','source_sha':"${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}",
            'executor_sha':'${{ github.sha }}',
            'components':'${{ inputs.components }}','release_tag':'${{ inputs.release_tag }}',
            'artifact_id':'${{ inputs.artifact_id }}','dev_artifact_id':'${{ inputs.dev_artifact_id }}',
            'operation':'${{ inputs.operation }}',
            'force':'${{ inputs.force }}',
            'pipeline_run_timeout_seconds':'${{ inputs.pipeline_run_timeout_seconds }}',
        })
        dev_diagnostic=dev['jobs']['auth-image-diagnostic']
        for fixed in ("inputs.operation == 'diagnose-auth-image'", "github.ref == 'refs/heads/develop'",
                      "inputs.components == 'auth'", "inputs.release_tag == 'diagnostic-36992147920'",
                      "inputs.artifact_id == 'diagnostic-no-receipt'", "inputs.dev_artifact_id == ''"):
            self.assertIn(fixed,dev_diagnostic['if'])
        self.assertEqual(dev_diagnostic['uses'],'./.github/workflows/cd-auth-image-diagnostic.yml')
        self.assertEqual(dev_diagnostic['permissions'],{'contents':'read','actions':'read','id-token':'write'})
        self.assertNotIn('GH_TOKEN',str(dev_diagnostic))
        self.assertNotIn('VERCEL_TOKEN',str(dev_diagnostic))
        self.assertEqual(dev_diagnostic['with'],{'source_sha':'${{ github.sha }}'})

        recovery=yaml.safe_load((ROOT/'.github/workflows/recover-deployment.yml').read_text())
        trigger=recovery.get('on',recovery.get(True,{}))
        options=trigger['workflow_dispatch']['inputs']['operation']['options']
        self.assertIn('diagnose-auth-image',options)
        wrapper=recovery['jobs']['recovery']
        self.assertEqual(wrapper['if'],"inputs.operation != 'diagnose-auth-image'")
        self.assertEqual(wrapper['with'],{
            'environment':'${{ inputs.environment }}','source_sha':'${{ inputs.source_sha }}',
            'executor_sha':'${{ github.sha }}',
            'components':'${{ inputs.components }}','release_tag':'${{ inputs.release_tag }}',
            'operation':'${{ inputs.operation }}','artifact_id':'${{ inputs.artifact_id }}',
        })
        diagnostic_wrapper=recovery['jobs']['auth-image-diagnostic']
        for fixed in ("github.ref == 'refs/heads/develop'", "inputs.environment == 'development'",
                      'inputs.source_sha == github.sha', "inputs.components == 'auth'",
                      "inputs.release_tag == 'diagnostic-36992147920'",
                      "inputs.artifact_id == 'diagnostic-no-receipt'"):
            self.assertIn(fixed,diagnostic_wrapper['if'])
        self.assertEqual(diagnostic_wrapper['permissions'],
                         {'contents':'read','actions':'read','id-token':'write'})
        self.assertEqual(diagnostic_wrapper['uses'],'./.github/workflows/cd-auth-image-diagnostic.yml')
        self.assertEqual(diagnostic_wrapper['with'],{'source_sha':'${{ inputs.source_sha }}'})

        shared=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        self.assertEqual(set(shared['jobs']),{'release','pipeline-config-only'})
        job=shared['jobs']['release'];steps=job['steps']
        by_name={step.get('name'): (index,step) for index,step in enumerate(steps) if step.get('name')}
        self.assertEqual(job['if'],"inputs.operation != 'diagnose-auth-image' && inputs.operation != 'config-only'")
        self.assertEqual(job['env']['SOURCE'],'${{ inputs.source_sha }}')
        self.assertEqual(job['env']['EXECUTOR_SHA'],'${{ inputs.executor_sha }}')
        self.assertEqual(job['env']['FORCE'],'${{ inputs.force }}')
        self.assertNotIn('VERCEL_TOKEN',job['env'])
        self.assertNotIn('GH_TOKEN',job['env'])
        diagnostic_workflow=yaml.safe_load((ROOT/'.github/workflows/cd-auth-image-diagnostic.yml').read_text())
        call_trigger=diagnostic_workflow.get('on',diagnostic_workflow.get(True,{}))
        self.assertEqual(call_trigger['workflow_call']['inputs']['source_sha'],
                         {'required':True,'type':'string'})
        self.assertEqual(set(diagnostic_workflow['jobs']),{'auth-image-diagnostic'})
        diag=diagnostic_workflow['jobs']['auth-image-diagnostic']
        for fixed in ("github.ref == 'refs/heads/develop'", 'inputs.source_sha == github.sha'):
            self.assertIn(fixed,diag['if'])
        self.assertEqual(diag['permissions'],{'contents':'read','actions':'read','id-token':'write'})
        self.assertEqual(diag['environment'],'Development')
        self.assertEqual(diag['env']['SOURCE'],'${{ inputs.source_sha }}')
        self.assertEqual(diag['env']['TARGET'],'development')
        self.assertEqual(diag['env']['COMPONENTS'],'auth')
        self.assertEqual(diag['env']['RELEASE_TAG'],'diagnostic-36992147920')
        self.assertEqual(diag['env']['OPERATION'],'diagnose-auth-image')
        self.assertEqual(diag['env']['REUSE_ID'],'diagnostic-no-receipt')
        self.assertEqual(diag['env']['ARTIFACT_ID'],'diagnostic-no-receipt')
        self.assertEqual(diag['env']['DEV_ID'],'')
        self.assertEqual(diag['env']['WORKFLOW_SHA'],'${{ github.sha }}')
        for forbidden in ('GH_TOKEN','VERCEL_TOKEN','VERCEL_PROJECT_ID','VERCEL_TEAM_ID'):
            self.assertNotIn(forbidden,diag['env'])
        self.assertEqual(set(re.findall(r'\$\{\{\s*secrets\.([A-Z_]+)\s*\}\}',str(diag))),
                         {'WIF_PROVIDER','WIF_SERVICE_ACCOUNT'})
        self.assertNotIn('${{ github.token }}',str(diag))
        diag_steps=diag['steps']
        for forbidden in ('gh_token','vercel_token','vercel_project_id','vercel_team_id',
                          'artifacts.cjs','checkpoint','receipt'):
            self.assertNotIn(forbidden,str(diag_steps).lower())
        diag_by_name={step.get('name'): (index,step) for index,step in enumerate(diag_steps) if step.get('name')}
        auth_index=next(i for i,s in enumerate(diag_steps) if s.get('uses','').startswith('google-github-actions/auth@'))
        gcloud_index=next(i for i,s in enumerate(diag_steps) if s.get('uses','').startswith('google-github-actions/setup-gcloud@'))
        diagnostic_index,diagnostic=diag_by_name['Read-only Auth image diagnostic']
        self.assertLess(auth_index,gcloud_index);self.assertLess(gcloud_index,diagnostic_index)
        self.assertEqual(diag_steps[0]['with']['ref'],'${{ inputs.source_sha }}')
        self.assertIn('deploy/engine/diagnostics.py',diagnostic['run'])
        self.assertNotIn('Download pinned ready artifact',str(diag_steps))
        self.assertNotIn('deployment-engine',str(diag_steps))
        self.assertNotIn('artifacts.cjs',str(diag_steps))
        self.assertEqual([step['with']['name'] for step in diag_steps
                          if step.get('uses','').startswith('actions/upload-artifact@')],
                         ['lwc-auth-diagnostic-${{ github.run_id }}-${{ github.run_attempt }}'])
        self.assertEqual(diag_by_name['Retain redacted diagnostic result'][1]['if'],'always()')
        self.assertIn('lwc-auth-diagnostic-',diag_by_name['Retain redacted diagnostic result'][1]['with']['name'])

    def test_profiles_and_real_component_operations(self):
        profiles=json.loads((ROOT/'deploy/engine/profiles.json').read_text())
        self.assertEqual(set(profiles),{'auth','bff','worker','exportjob','frontend'})
        for c,p in profiles.items():
            self.assertGreater(p['revision'],0);self.assertTrue(p['inputs'])
        s=(ROOT/'deploy/engine/providers.py').read_text()
        for op in ('prepare','snapshot','deploy','observe','rollback','reconcile_candidate'):
            self.assertIn('def '+op+'(',s)
        self.assertIn("'--skip-domain'",s)
        self.assertIn('range(12)',s);self.assertIn('time.sleep(5)',s)
        for op in ("'create'", "'delete'", "'execute'"):
            self.assertNotIn(op,s)

if __name__=='__main__':unittest.main()
