#!/usr/bin/env python3
"""r2 workflow contracts, replacing the retired mixed build/mutate/CD polling graph."""
import json
from pathlib import Path
import re
import unittest
import yaml

ROOT=Path(__file__).resolve().parents[1]

class EngineWorkflowContract(unittest.TestCase):
    def test_single_runtime_authority_with_separate_dev_diagnostic_job(self):
        s=(ROOT/'.github/workflows/cd.yml').read_text()
        parsed=yaml.safe_load(s)
        self.assertEqual(set(parsed['jobs']),{'release','auth-image-diagnostic'})
        self.assertEqual(parsed['jobs']['release']['environment'],
                         "${{ inputs.environment == 'production' && 'Production' || 'Development' }}")
        self.assertEqual(parsed['jobs']['auth-image-diagnostic']['environment'],'Development')
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
        recovery=(ROOT/'.github/workflows/recover-deployment.yml').read_text()
        self.assertIn('[rollback, reactivate, deploy, tag, readback, diagnose-auth-image]',recovery)
        self.assertIn('uses: ./.github/workflows/cd.yml',recovery)

    def test_pinned_recovery_download_has_effective_github_token(self):
        workflow=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        release=workflow['jobs']['release']
        self.assertEqual(release['if'],"inputs.operation != 'diagnose-auth-image'")
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
        self.assertEqual(dev_operation['options'],['release','diagnose-auth-image'])
        self.assertEqual(dev['jobs']['release']['if'],
                         "github.ref == 'refs/heads/develop' && inputs.operation == 'release'")
        self.assertEqual(dev['jobs']['release']['with']['operation'],'${{ inputs.operation }}')
        dev_diagnostic=dev['jobs']['auth-image-diagnostic']
        for fixed in ("inputs.operation == 'diagnose-auth-image'", "github.ref == 'refs/heads/develop'",
                      "inputs.components == 'auth'", "inputs.release_tag == 'diagnostic-36992147920'",
                      "inputs.artifact_id == 'diagnostic-no-receipt'", "inputs.dev_artifact_id == ''"):
            self.assertIn(fixed,dev_diagnostic['if'])
        self.assertEqual(dev_diagnostic['uses'],'./.github/workflows/cd.yml')
        self.assertEqual(dev_diagnostic['permissions'],{'contents':'read','actions':'read','id-token':'write'})
        self.assertNotIn('GH_TOKEN',str(dev_diagnostic))
        self.assertNotIn('VERCEL_TOKEN',str(dev_diagnostic))
        self.assertEqual(dev_diagnostic['with'],{
            'environment':'development','source_sha':'${{ github.sha }}','components':'auth',
            'release_tag':'diagnostic-36992147920','operation':'diagnose-auth-image',
            'artifact_id':'diagnostic-no-receipt','dev_artifact_id':'',
        })

        recovery=yaml.safe_load((ROOT/'.github/workflows/recover-deployment.yml').read_text())
        trigger=recovery.get('on',recovery.get(True,{}))
        options=trigger['workflow_dispatch']['inputs']['operation']['options']
        self.assertIn('diagnose-auth-image',options)
        wrapper=recovery['jobs']['recovery']
        self.assertEqual(wrapper['if'],"inputs.operation != 'diagnose-auth-image'")
        self.assertEqual(wrapper['with']['source_sha'],'${{ inputs.source_sha }}')
        self.assertEqual(wrapper['with']['artifact_id'],'${{ inputs.artifact_id }}')
        diagnostic_wrapper=recovery['jobs']['auth-image-diagnostic']
        for fixed in ("github.ref == 'refs/heads/develop'", "inputs.environment == 'development'",
                      'inputs.source_sha == github.sha', "inputs.components == 'auth'",
                      "inputs.release_tag == 'diagnostic-36992147920'",
                      "inputs.artifact_id == 'diagnostic-no-receipt'"):
            self.assertIn(fixed,diagnostic_wrapper['if'])
        self.assertEqual(diagnostic_wrapper['permissions'],
                         {'contents':'read','actions':'read','id-token':'write'})
        self.assertEqual(diagnostic_wrapper['with']['source_sha'],'${{ inputs.source_sha }}')
        self.assertEqual(diagnostic_wrapper['with']['artifact_id'],'${{ inputs.artifact_id }}')

        shared=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        job=shared['jobs']['release'];steps=job['steps']
        by_name={step.get('name'): (index,step) for index,step in enumerate(steps) if step.get('name')}
        self.assertEqual(job['if'],"inputs.operation != 'diagnose-auth-image'")
        self.assertEqual(job['env']['SOURCE'],'${{ inputs.source_sha }}')
        self.assertNotIn('VERCEL_TOKEN',job['env'])
        self.assertNotIn('GH_TOKEN',job['env'])
        diag=shared['jobs']['auth-image-diagnostic']
        for fixed in ("inputs.operation == 'diagnose-auth-image'", "github.ref == 'refs/heads/develop'",
                      "inputs.environment == 'development'", 'inputs.source_sha == github.sha',
                      "inputs.components == 'auth'", "inputs.release_tag == 'diagnostic-36992147920'",
                      "inputs.artifact_id == 'diagnostic-no-receipt'"):
            self.assertIn(fixed,diag['if'])
        self.assertEqual(diag['permissions'],{'contents':'read','actions':'read','id-token':'write'})
        self.assertEqual(diag['environment'],'Development')
        self.assertEqual(diag['env']['SOURCE'],'${{ inputs.source_sha }}')
        self.assertEqual(diag['env']['WORKFLOW_SHA'],'${{ github.sha }}')
        self.assertEqual(diag['env']['DEV_ID'],'${{ inputs.dev_artifact_id }}')
        self.assertEqual(diag['env']['ARTIFACT_ID'],'${{ inputs.artifact_id }}')
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
