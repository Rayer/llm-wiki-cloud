#!/usr/bin/env python3
"""r2 workflow contracts, replacing the retired mixed build/mutate/CD polling graph."""
import json
from pathlib import Path
import re
import unittest
import yaml

ROOT=Path(__file__).resolve().parents[1]

class EngineWorkflowContract(unittest.TestCase):
    def test_dev_dispatch_normalizes_typed_numeric_auth_inputs(self):
        dev = yaml.safe_load((ROOT / '.github/workflows/deploy-dev.yml').read_text())
        dispatch_inputs = dev.get('on', dev.get(True, {}))['workflow_dispatch']['inputs']
        called_inputs = yaml.safe_load((ROOT / '.github/workflows/cd.yml').read_text())
        called_inputs = called_inputs.get('on', called_inputs.get(True, {}))['workflow_call']['inputs']
        forwarded = dev['jobs']['release']['with']
        names = ('auth_repository_id', 'auth_artifact_id', 'auth_run_id', 'auth_run_attempt')
        for name in names:
            with self.subTest(input=name):
                self.assertEqual(dispatch_inputs[name]['type'], 'number')
                self.assertEqual(dispatch_inputs[name]['default'], 0)
                self.assertEqual(called_inputs[name]['type'], 'number')
                self.assertEqual(forwarded[name], f'${{{{ fromJSON(inputs.{name}) }}}}')
        self.assertEqual(dispatch_inputs['auth_schema_version']['options'], ['4', '3'])
        self.assertEqual(dispatch_inputs['auth_schema_version']['default'], '4')
        self.assertEqual(called_inputs['auth_schema_version'], {
            'required': False, 'type': 'string', 'default': '4',
            'description': 'Auth runtime input schema; schema 3 is limited to independent Auth-only DEV/Production or BFF-only DEV staging with a retained schema-3 Auth receipt',
        })
        self.assertEqual(forwarded['auth_schema_version'], '${{ inputs.auth_schema_version }}')

    def test_independent_auth_source_tuple_is_typed_verified_and_forwarded(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/cd.yml').read_text())
        trigger = workflow.get('on', workflow.get(True, {}))
        inputs = trigger['workflow_call']['inputs']
        self.assertTrue(inputs['release_source_mode']['required'])
        self.assertEqual(inputs['release_source_mode']['type'], 'string')
        expected_types = {
            'auth_repository_id': 'number', 'auth_revision': 'string',
            'auth_tree_sha256': 'string', 'auth_artifact_id': 'number',
            'auth_run_id': 'number', 'auth_run_attempt': 'number',
        }
        for name, input_type in expected_types.items():
            self.assertEqual(inputs[name]['type'], input_type)
        release = workflow['jobs']['release']
        self.assertEqual(release['env']['GAT4_RELEASE_SOURCE_MODE'], '${{ inputs.release_source_mode }}')
        self.assertEqual(release['env']['GAT4_DEV_RECEIPT_MODE'], '${{ inputs.release_source_mode }}')
        package = next(step for step in release['steps']
                       if step.get('name') == 'Download and verify immutable GateAuth CI package')
        self.assertIn('inputs.release_source_mode == \'independent\'', package['if'])
        preflight = next(step for step in release['steps']
                         if step.get('name') == 'Validate retained Auth receipt before private package fetch')
        prepare = next(step for step in release['steps']
                       if step.get('name') == 'Resolve explicit receipt references and prepare selected artifacts')
        self.assertEqual(preflight['with']['operation'], 'auth-preflight')
        self.assertEqual(preflight['with']['auth-schema-version'], '${{ inputs.auth_schema_version }}')
        self.assertEqual(prepare['with']['auth-schema-version'], '${{ inputs.auth_schema_version }}')
        self.assertNotIn('GATEAUTH_SOURCE_READ_TOKEN', preflight.get('env', {}))
        self.assertIn("steps.gat4-preflight.outputs.needs_auth_ci_package == 'true'", package['if'])
        action_metadata = yaml.safe_load((ROOT / '.github/actions/deployment-engine/action.yml').read_text())
        self.assertIn('needs_auth_ci_package', action_metadata['outputs'])
        self.assertLess(release['steps'].index(preflight), release['steps'].index(package))
        self.assertLess(release['steps'].index(package), release['steps'].index(prepare))
        self.assertEqual(package['env']['AUTH_REPOSITORY_ID'], '${{ inputs.auth_repository_id }}')
        self.assertEqual(package['env']['GATEAUTH_SOURCE_READ_TOKEN'],
                         '${{ secrets.GATEAUTH_SOURCE_READ_TOKEN }}')
        self.assertIn('gat4_ci_artifact.py download', package['run'])
        self.assertIn('GATEAUTH_CI_PACKAGE_PATH=', package['run'])
        self.assertFalse(any(step.get('with', {}).get('repository') ==
                             '${{ steps.gateauth-source.outputs.repository_name }}'
                             for step in release['steps']))
        auth_script = (ROOT / 'deploy/components/auth.sh').read_text()
        auth_mutate = auth_script[auth_script.index('auth_mutate() {'):auth_script.index('\nauth_verify() {')]
        independent = auth_mutate.split('if [[ "${GAT4_RELEASE_SOURCE_MODE:-}" == independent ]]', 1)[1].split('else', 1)[0]
        self.assertIn('image=$(image_for auth)', independent)
        self.assertNotIn('auth_build_image', independent)
        action = (ROOT / '.github/actions/deployment-engine/index.cjs').read_text()
        engine = (ROOT / 'deploy/engine/engine.py').read_text()
        self.assertIn("'auth-preflight'", action)
        self.assertIn('auth_package_preflight', engine)
        self.assertIn('validate_auth_component_receipt(candidate, \'independent\')', engine)
        for flag in ('--auth-repository-id', '--auth-revision', '--auth-tree-sha256',
                     '--auth-artifact-id', '--auth-run-id', '--auth-run-attempt'):
            self.assertIn(flag, action)

    def test_bff_plan_admission_includes_generated_nonsecret_descriptor(self):
        engine=(ROOT/'deploy/engine/engine.py').read_text()
        config=(ROOT/'apps/bff/cmd/deploy_config/main.go').read_text()
        self.assertIn("if 'bff' in selected:",engine)
        self.assertIn("'--descriptor'",engine)
        self.assertIn("'--bff-inputs'",engine)
        self.assertIn('LoadWithBFFInputs',config)
        self.assertIn('generated BFF input descriptor',config)
        self.assertIn('runtime_inputs',config)

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
        self.assertEqual(set(parsed['jobs']),{'release','pipeline-config-only','frontend-config-only','notes-resend'})
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
        notes_resend=parsed['jobs']['notes-resend']
        self.assertEqual(notes_resend['if'],"inputs.operation == 'notes-resend'")
        notes_resend_steps=notes_resend['steps']
        self.assertTrue(any(step.get('name') == 'Resend Notes without product deployment' and
                            '--action resend' in step.get('run','') for step in notes_resend_steps))
        self.assertFalse(any(step.get('uses') == './.github/actions/deployment-engine' for step in notes_resend_steps))
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
        frontend=parsed['jobs']['frontend-config-only']
        self.assertEqual(frontend['if'],"inputs.operation == 'frontend-config-only'")
        self.assertEqual(frontend['permissions'],{'contents':'read','actions':'read','id-token':'write'})
        names=[step.get('name',step.get('uses','')) for step in frontend['steps']]
        inspect=names.index('Inspect exact selected artifact and successful generator run')
        download=next(index for index,step in enumerate(frontend['steps'])
                      if step.get('uses','').startswith('actions/download-artifact@'))
        validate=names.index('Validate selected artifact metadata and public JSON bytes')
        auth=next(index for index,step in enumerate(frontend['steps'])
                  if step.get('uses','').startswith('google-github-actions/auth@'))
        publish=names.index('Publish only the selected public config and verify exact readback')
        self.assertLess(inspect,download)
        self.assertLess(download,validate)
        self.assertLess(validate,auth)
        self.assertLess(auth,publish)
        artifact_step=frontend['steps'][download]
        self.assertIn('artifact-ids',artifact_step['with'])
        self.assertIn('run-id',artifact_step['with'])
        self.assertEqual(frontend['env']['FRONTEND_CONFIG_ARTIFACT_ID'],'${{ inputs.frontend_config_artifact_id }}')
        self.assertFalse(any('make ' in step.get('run','') or 'vercel build' in step.get('run','')
                             for step in frontend['steps']))
        self.assertNotIn('VERCEL_TOKEN',frontend['env'])
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
        recovery_yaml=yaml.safe_load(recovery)
        recovery_trigger=recovery_yaml.get('on',recovery_yaml.get(True,{}))
        self.assertEqual(recovery_trigger['workflow_dispatch']['inputs']['rejection_artifact_id'],
                         {'description':'Optional exact DEV final result artifact proving the original frontend quota rejection',
                          'required':False,'default':'','type':'string'})
        self.assertEqual(recovery_yaml['jobs']['recovery']['with']['rejection_artifact_id'],
                         '${{ inputs.rejection_artifact_id }}')

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
        self.assertEqual(job['env']['REJECTION_ARTIFACT_ID'],
                         '${{ inputs.rejection_artifact_id }}')
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
        selection = next((index, step) for index, step in enumerate(steps)
                         if step.get('id') == 'release-notes-selection')
        self.assertEqual(selection[1]['if'], "inputs.operation == 'release'")
        self.assertLess(next(i for i, step in enumerate(steps) if step.get('id') == 'ready'), selection[0])
        missing_notes_id = next(i for i, step in enumerate(steps)
                                if step.get('name') == 'Record missing Notes artifact ID against exact plan')
        self.assertLess(selection[0], missing_notes_id)
        self.assertEqual(job['environment'],
                         "${{ inputs.environment == 'production' && 'Production' || 'Development' }}")

    def test_release_notes_delivery_seams_use_canonical_plan_and_private_manual_transport(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/cd.yml').read_text())
        release = workflow['jobs']['release']
        steps = release['steps']
        by_name = {step.get('name'): step for step in steps if step.get('name')}
        notes_names = (
            'Record missing Notes artifact ID against exact plan',
            'Inspect exact TPM Release Notes artifact',
            'Download exact TPM Release Notes artifact',
            'Record exact TPM artifact download failure',
            'Record explicit Notes stop',
            'Record explicit Notes skip before release',
            'Validate and store immutable Release Notes content before product release',
            'Retain original product publication time',
            'Publish Notes content and exact current-operation marker',
            'Publish current-operation marker after explicit Notes skip',
        )
        for name in notes_names:
            self.assertIn("steps.release-notes-selection.outputs.bff_selected == 'true'",
                          by_name[name]['if'], name)
            self.assertNotIn('inputs.components', by_name[name]['if'], name)
        runtime = by_name['Execute bounded runtime or explicit recovery']
        self.assertEqual(runtime['if'],
                         "(inputs.operation == 'release' && steps.release-notes-selection.outputs.bff_selected != 'true') || inputs.release_notes_action != 'stop'")
        recovery_selection = next(step for step in steps if step.get('id') == 'recovery-notes-selection')
        self.assertEqual(recovery_selection['if'], "inputs.operation != 'release'")
        marker = by_name['Publish marker for explicit BFF deploy, rollback, or reactivation']
        self.assertIn("steps.recovery-notes-selection.outputs.bff_selected == 'true'", marker['if'])
        self.assertNotIn('inputs.components', marker['if'])
        self.assertNotIn("contains(format(',{0},', inputs.components), ',bff,')", marker['if'])

        dev = yaml.safe_load((ROOT / '.github/workflows/deploy-dev.yml').read_text())
        dev_inputs = dev.get('on', dev.get(True, {}))['workflow_dispatch']['inputs']
        self.assertEqual(dev_inputs['release_notes_action']['default'], 'run')
        self.assertNotIn('default', dev_inputs['components'])
        self.assertEqual(dev['jobs']['release']['with']['components'], '${{ inputs.components }}')

        manual_path = ROOT / '.github/workflows/prepare-release-notes-package.yml'
        manual = yaml.safe_load(manual_path.read_text())
        trigger = manual.get('on', manual.get(True, {}))
        self.assertEqual(set(trigger), {'workflow_dispatch'})
        self.assertEqual(manual['permissions'], {'contents': 'read', 'actions': 'write'})
        job = manual['jobs']['package']
        self.assertEqual(job['if'], "github.ref == 'refs/heads/develop'")
        self.assertEqual(job['permissions'], {'contents': 'read', 'actions': 'write'})
        manual_steps = job['steps']
        self.assertEqual(manual_steps[0]['uses'], 'actions/checkout@11d5960a326750d5838078e36cf38b85af677262')
        downloads = [step for step in manual_steps if step.get('uses', '').startswith('actions/download-artifact@')]
        self.assertEqual(len(downloads), 2)
        download = downloads[0]
        upload = next(step for step in manual_steps if step.get('uses', '').startswith('actions/upload-artifact@'))
        self.assertEqual(download['uses'], 'actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093')
        self.assertIn('artifact-ids', download['with'])
        self.assertEqual(download['with']['merge-multiple'], True)
        self.assertEqual(download['with']['artifact-ids'], '${{ inputs.ready_artifact_id }}')
        self.assertEqual(downloads[1]['with']['artifact-ids'], '${{ steps.ready.outputs.result_artifact_id }}')
        self.assertEqual(downloads[1]['with']['run-id'], '${{ steps.ready.outputs.run_id }}')
        self.assertEqual(upload['uses'], 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02')
        self.assertIn('lwc-release-notes-${{ steps.prepare.outputs.product_version }}-development-${{ github.run_id }}-${{ github.run_attempt }}',
                      upload['with']['name'])
        self.assertEqual(upload['with']['retention-days'], '${{ inputs.retention_days }}')
        prepare = next(step for step in manual_steps if step.get('id') == 'prepare')
        self.assertEqual(prepare['env']['REQUEST_ID'], '${{ inputs.request_id }}')
        self.assertNotIn('LWC_RELEASE_NOTES_PACKAGE_B64', prepare.get('env', {}))
        self.assertNotIn('${{ inputs.package_zip_base64 }}', str(manual))
        self.assertIn('--event-path "$GITHUB_EVENT_PATH"', prepare['run'])
        self.assertNotIn('echo ', prepare['run'])
        self.assertNotIn('eval ', prepare['run'])
        self.assertIn('--request-id "$REQUEST_ID"', prepare['run'])
        locate = next(step for step in manual_steps if step.get('id') == 'ready')
        self.assertIn('find-ready-result', locate['run'])
        ready_plan = next(step for step in manual_steps if step.get('id') == 'ready-plan')
        self.assertIn('validate-ready', ready_plan['run'])
        self.assertIn('"release-notes-evidence.json"',
                      (ROOT / 'deploy/engine/release_notes_workflow.py').read_text())
        verify = next(step for step in manual_steps if step.get('name') == 'Verify the uploaded artifact and emit its exact receipt')
        self.assertIn('verify-upload', verify['run'])
        self.assertIn('artifact-digest', verify['env']['ACTION_DIGEST_RAW'])
        self.assertIn('"$ACTION_DIGEST_RAW" =~ ^[0-9a-f]{64}$', verify['run'])
        helper = (ROOT / 'deploy/engine/release_notes_workflow.py').read_text()
        self.assertIn('normalize_action_digest(action_digest)', helper)
        self.assertIn('"artifact_id": ready_artifact_id', helper)
        self.assertIn('inputs["release_notes_artifact_id"] = notes_artifact_id', helper)
        self.assertIn('"resume_inputs": resume_inputs', helper)
        self.assertNotIn('VERCEL', str(manual))
        self.assertNotIn('WIF_', str(manual))

        shared_job = release
        self.assertEqual(shared_job['env']['REUSE_ID'], '${{ inputs.artifact_id }}')
        deploy_wrapper = yaml.safe_load((ROOT / '.github/workflows/deploy-dev.yml').read_text())
        self.assertEqual(deploy_wrapper['jobs']['release']['with']['artifact_id'], '${{ inputs.artifact_id }}')
        action_source = (ROOT / '.github/actions/deployment-engine/index.cjs').read_text()
        self.assertIn("retainedDirectory(process.env.REUSE_ID,'reuse')", action_source)
        self.assertIn("args.push('--reuse',reuse)", action_source)
        engine_source = (ROOT / 'deploy/engine/engine.py').read_text()
        for invariant in ("compatible_release_identity(retained_plan) == compatible_release_identity(self.plan)",
                          "retained_state.get('status') not in ('prepared', 'ready')",
                          "if retained_state['components']:", "self.plan = copy.deepcopy(retained_plan)"):
            self.assertIn(invariant, engine_source)

    def test_manual_notes_package_operator_command_is_documented_without_content_logging(self):
        doc = (ROOT / 'docs/lwc-478/operator-package-preparation.md').read_text()
        self.assertIn('release_notes_workflow.py dispatch', doc)
        self.assertIn('--ready-artifact-id "$READY_ARTIFACT_ID"', doc)
        self.assertIn('--package-dir "$PRIVATE_TPM_PACKAGE_DIR"', doc)
        self.assertIn('does not print package contents', doc.lower())
        self.assertIn('no real artifact upload was run', doc.lower())

    def test_pinned_recovery_download_has_effective_github_token(self):
        workflow=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        release=workflow['jobs']['release']
        self.assertEqual(release['if'],"inputs.operation != 'diagnose-auth-image' && inputs.operation != 'config-only' && inputs.operation != 'frontend-config-only' && inputs.operation != 'notes-resend'")
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
        self.assertEqual(dev_operation['options'],['release','config-only','frontend-config-only','frontend-config-generate','deploy','rollback','reactivate','tag','readback','notes-resend','diagnose-auth-image'])
        self.assertEqual(dev['jobs']['release']['if'],
                         "github.ref == 'refs/heads/develop' && inputs.operation != 'diagnose-auth-image' && inputs.operation != 'frontend-config-generate'")
        self.assertEqual(dev['jobs']['release']['with'],{
            'environment':'development','source_sha':"${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}",
            'executor_sha':'${{ github.sha }}',
            'components':'${{ inputs.components }}','release_tag':'${{ inputs.release_tag }}',
            'release_source_mode':'${{ inputs.release_source_mode }}',
            'auth_schema_version':'${{ inputs.auth_schema_version }}',
            'auth_repository_id':'${{ fromJSON(inputs.auth_repository_id) }}',
            'auth_revision':'${{ inputs.auth_revision }}',
            'auth_tree_sha256':'${{ inputs.auth_tree_sha256 }}',
            'auth_artifact_id':'${{ fromJSON(inputs.auth_artifact_id) }}',
            'auth_run_id':'${{ fromJSON(inputs.auth_run_id) }}',
            'auth_run_attempt':'${{ fromJSON(inputs.auth_run_attempt) }}',
            'artifact_id':'${{ inputs.artifact_id }}','dev_artifact_id':'${{ inputs.dev_artifact_id }}',
            'release_notes_artifact_id':'${{ inputs.release_notes_artifact_id }}',
            'release_notes_result_artifact_id':'${{ inputs.release_notes_result_artifact_id }}',
            'release_notes_action':'${{ inputs.release_notes_action }}',
            'release_notes_operation':'${{ inputs.release_notes_operation }}',
            'frontend_config_artifact_id':'${{ inputs.frontend_config_artifact_id }}',
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

        generator=dev['jobs']['frontend-config-generate']
        self.assertEqual(generator['if'],"github.ref == 'refs/heads/develop' && inputs.operation == 'frontend-config-generate'")
        self.assertEqual(generator['permissions'],{'contents':'read'})
        self.assertEqual(generator['uses'],'./.github/workflows/generate-frontend-config.yml')
        self.assertEqual(generator['with'],{'environment':'dev'})
        self.assertNotIn('secrets',generator)
        self.assertNotIn('id-token',str(generator))
        self.assertIn("inputs.operation != 'frontend-config-generate'",dev['jobs']['main-fast-forward-eligible']['if'])
        self.assertIn("inputs.operation != 'notes-resend'",dev['jobs']['main-fast-forward-eligible']['if'])

        production=yaml.safe_load((ROOT/'.github/workflows/promote-production.yml').read_text())
        prod_trigger=production.get('on',production.get(True,{}))
        self.assertEqual(prod_trigger['workflow_dispatch']['inputs']['operation']['options'],
                         ['release','config-only','frontend-config-only','frontend-config-generate','notes-resend'])
        self.assertIn("inputs.operation != 'frontend-config-generate'",production['jobs']['release']['if'])
        prod_generator=production['jobs']['frontend-config-generate']
        self.assertEqual(prod_generator['if'],"github.ref == 'refs/heads/develop' && inputs.operation == 'frontend-config-generate'")
        self.assertEqual(prod_generator['permissions'],{'contents':'read'})
        self.assertEqual(prod_generator['uses'],'./.github/workflows/generate-frontend-config.yml')
        self.assertEqual(prod_generator['with'],{'environment':'prod'})
        self.assertNotIn('secrets',prod_generator)
        self.assertNotIn('id-token',str(prod_generator))

        generator_workflow=yaml.safe_load((ROOT/'.github/workflows/generate-frontend-config.yml').read_text())
        self.assertEqual(generator_workflow.get('on',generator_workflow.get(True,{}))['workflow_call']['inputs']['environment'],
                         {'required':True,'type':'string'})

        recovery=yaml.safe_load((ROOT/'.github/workflows/recover-deployment.yml').read_text())
        trigger=recovery.get('on',recovery.get(True,{}))
        options=trigger['workflow_dispatch']['inputs']['operation']['options']
        self.assertIn('diagnose-auth-image',options)
        wrapper=recovery['jobs']['recovery']
        self.assertEqual(wrapper['if'],"inputs.operation != 'diagnose-auth-image'")
        self.assertEqual(wrapper['with'],{
            'environment':'${{ inputs.environment }}','source_sha':'${{ inputs.source_sha }}',
            'executor_sha':'${{ github.sha }}',
            'release_source_mode':'${{ inputs.release_source_mode }}',
            'components':'${{ inputs.components }}','release_tag':'${{ inputs.release_tag }}',
            'operation':'${{ inputs.operation }}','artifact_id':'${{ inputs.artifact_id }}',
            'rejection_artifact_id':'${{ inputs.rejection_artifact_id }}',
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
        self.assertEqual(set(shared['jobs']),{'release','notes-resend','pipeline-config-only','frontend-config-only'})
        job=shared['jobs']['release'];steps=job['steps']
        by_name={step.get('name'): (index,step) for index,step in enumerate(steps) if step.get('name')}
        self.assertEqual(job['if'],"inputs.operation != 'diagnose-auth-image' && inputs.operation != 'config-only' && inputs.operation != 'frontend-config-only' && inputs.operation != 'notes-resend'")
        notes_resend=shared['jobs']['notes-resend']
        self.assertEqual(notes_resend['if'],"inputs.operation == 'notes-resend'")
        self.assertNotIn('needs',notes_resend)
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

    def test_notes_resend_is_a_no_deploy_branch_with_explicit_dispositions(self):
        shared=yaml.safe_load((ROOT/'.github/workflows/cd.yml').read_text())
        release=shared['jobs']['release']
        release_steps={step.get('name'):step for step in release['steps'] if step.get('name')}
        missing=release_steps['Record missing Notes artifact ID against exact plan']['run']
        self.assertIn('--failure-class artifact-id-required',missing)
        self.assertTrue(missing.rstrip().endswith('exit 1'))
        metadata_failure=release_steps['Inspect exact TPM Release Notes artifact']['run']
        self.assertEqual(metadata_failure.count('exit 1'),2)
        self.assertTrue(release_steps['Record exact TPM artifact download failure']['run'].rstrip().endswith('exit 1'))
        self.assertLess(next(i for i,s in enumerate(release['steps']) if s.get('name') == 'Validate and store immutable Release Notes content before product release'),
                        next(i for i,s in enumerate(release['steps']) if s.get('name') == 'Execute bounded runtime or explicit recovery'))
        job=shared['jobs']['notes-resend']
        self.assertEqual(job['if'],"inputs.operation == 'notes-resend'")
        self.assertNotIn('needs',job)
        steps={step.get('name'):step for step in job['steps'] if step.get('name')}
        self.assertIn('(.conclusion == "success" or .conclusion == "failure")',
                      steps['Inspect exact prior release result artifact']['run'])
        verify=steps['Verify the retained product operation succeeded']['run']
        self.assertIn('--action verify-operation',verify)
        self.assertLess(next(i for i,s in enumerate(job['steps']) if s.get('name') == 'Download exact prior release result and retained operation evidence'),
                        next(i for i,s in enumerate(job['steps']) if s.get('name') == 'Verify the retained product operation succeeded'))
        self.assertEqual(steps['Resend Notes without product deployment']['if'],
                         "inputs.release_notes_action == 'run' || inputs.release_notes_action == 'retry'")
        self.assertIn('--action resend',steps['Resend Notes without product deployment']['run'])
        self.assertNotIn('./.github/actions/deployment-engine',
                         '\n'.join(step.get('uses','') for step in job['steps']))
        self.assertEqual(steps['Record explicit Notes skip without product deployment']['if'],
                         "inputs.release_notes_action == 'skip'")
        self.assertEqual(steps['Record explicit Notes stop without product deployment']['if'],
                         "inputs.release_notes_action == 'stop'")
        for name in ('google-github-actions/auth@7c6bc770dae815cd3e89ee6cdf493a5fab2cc093',
                     'google-github-actions/setup-gcloud@e427ad8a34f8676edf47cf7d7925499adf3eb74f'):
            auth=next(step for step in job['steps'] if step.get('uses','').endswith(name))
            self.assertEqual(auth['if'], "inputs.release_notes_action == 'run' || inputs.release_notes_action == 'retry'")

    def test_notes_package_download_and_result_artifact_keep_their_real_roots(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/cd.yml').read_text())
        release = workflow['jobs']['release']
        resend = workflow['jobs']['notes-resend']
        runner_temp = '${{ runner.temp }}'
        package_downloads = [
            next(step for step in release['steps'] if step.get('name') == 'Download exact TPM Release Notes artifact'),
            next(step for step in resend['steps'] if step.get('name') == 'Download exact TPM Release Notes artifact'),
        ]
        for step in package_downloads:
            self.assertEqual(step['uses'], 'actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093')
            self.assertIn('artifact-ids', step['with'])
            self.assertIn('run-id', step['with'])
            self.assertIs(step['with']['merge-multiple'], True)
            self.assertEqual(step['with']['path'], runner_temp + '/release-notes-package')

        pinned_source = (ROOT / 'scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts').read_text()
        self.assertIn('mergeMultiple: core.getBooleanInput(Inputs.MergeMultiple', pinned_source)
        self.assertIn('isSingleArtifactDownload || inputs.mergeMultiple', pinned_source)
        self.assertIn('? resolvedPath\n          : path.join(resolvedPath, artifact.name)', pinned_source)

        result_upload = next(step for step in release['steps'] if step.get('name') == 'Retain final result even after failure')
        resend_upload = next(step for step in resend['steps'] if step.get('name') == 'Retain independent Notes resend evidence')
        self.assertEqual(result_upload['with']['path'], runner_temp + '/release')
        self.assertEqual(resend_upload['with']['path'], runner_temp + '/release')
        result_reader = (ROOT / 'deploy/engine/artifacts.cjs').read_text()
        self.assertIn("client.downloadArtifact(Number(id), {path:destination", result_reader)

        timestamp = next(i for i, step in enumerate(release['steps'])
                         if step.get('name') == 'Retain original product publication time')
        runtime = next(i for i, step in enumerate(release['steps'])
                       if step.get('name') == 'Execute bounded runtime or explicit recovery')
        publish = next(i for i, step in enumerate(release['steps'])
                       if step.get('name') == 'Publish Notes content and exact current-operation marker')
        self.assertLess(runtime, timestamp)
        self.assertLess(timestamp, publish)
        self.assertIn('--action record-publication', release['steps'][timestamp]['run'])
        self.assertNotIn('./.github/actions/deployment-engine', str(resend['steps']))

    def test_profiles_and_real_component_operations(self):
        profiles=json.loads((ROOT/'deploy/engine/profiles.json').read_text())
        self.assertEqual(set(profiles),{'auth','bff','worker','exportjob','documentimport','frontend'})
        for c,p in profiles.items():
            self.assertGreater(p['revision'],0);self.assertTrue(p['inputs'])
        s=(ROOT/'deploy/engine/providers.py').read_text()
        for op in ('prepare','snapshot','deploy','observe','rollback','reconcile_candidate'):
            self.assertIn('def '+op+'(',s)
        self.assertIn("'--skip-domain'",s)
        self.assertIn('range(12)',s);self.assertIn('time.sleep(5)',s)
        creation = s[s.index('    def document_import_create('):s.index('    def service_matches(')]
        self.assertIn("'jobs', 'create'", creation)
        rollback = s[s.index('    def rollback('):]
        self.assertIn("if c == 'documentimport' and prior.get('absent') is True:", rollback)
        self.assertIn("self.cloud(c, 'jobs', 'delete', name, mutation=True)", rollback)
        self.assertNotIn("'jobs', 'execute'", s)

if __name__=='__main__':unittest.main()
