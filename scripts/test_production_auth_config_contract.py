#!/usr/bin/env python3
"""Production Auth file-mount contract; all inputs are synthetic and local."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import test_auth_config_contract as fixtures

import sys
sys.path.insert(0, str(fixtures.ROOT / 'deploy/engine'))
import providers


IMAGE = fixtures.IMAGE
REVISION = 'llm-wiki-auth-00009-rzw'
VERSION = '17'


def candidate(plan):
    expected = providers.auth_config.desired(plan, 'auth', auth_config_version=VERSION)
    resource = expected['file_secret']['resource']
    alias = resource.split('/')[3]
    volume_name = 'auth-volume'
    return {
        'metadata': {'name': REVISION, 'namespace': 'llm-wiki-cloud', 'annotations': {}},
        'spec': {
            'serviceAccountName': expected['service_account'],
            'containers': [{'image': IMAGE,
                            'env': [{'name': 'LWC_APP_CONFIG_PATH', 'value': '/var/run/lwc-auth-config/auth.json'}],
                            'volumeMounts': [{'name': volume_name, 'mountPath': '/var/run/lwc-auth-config'}]}],
            'volumes': [{'name': volume_name, 'secret': {'secretName': alias,
                                                         'items': [{'key': VERSION, 'path': 'auth.json'}]}}],
        },
        'status': {'imageDigest': IMAGE, 'conditions': [{'type': 'Ready', 'status': 'True'}]},
    }


class ProductionAuthConfigContractTests(unittest.TestCase):
    def setUp(self):
        self.plan = fixtures.normalized_plan('production', 'auth')
        self.runtime = self.plan['auth']['runtime_inputs']
        self.revision = candidate(self.plan)
        self.adapter = providers.Providers({'normalized': self.plan}, fixtures.ROOT)

    def run_auth_config(self, mode, revision, fingerprint='', returned_project_id=None,
                        returned_project_number=None):
        script = fixtures.ROOT / 'deploy/components/auth_config.py'
        with tempfile.TemporaryDirectory(prefix='lwc-auth-cli-contract-') as directory:
            plan_path = Path(directory) / 'plan.json'
            plan_path.write_text(json.dumps({'normalized': self.plan}))
            args = ['python3', str(script), mode, str(plan_path), 'auth', REVISION, IMAGE,
                    fingerprint, VERSION]
            env = fixtures.project_mapping_env(directory, self.plan['gcp']['project_id'],
                                               returned_project_id=returned_project_id,
                                               returned_project_number=returned_project_number)
            return subprocess.run(args, input=json.dumps(revision), text=True,
                                  capture_output=True, env=env)

    def test_production_demo_identity_and_nonsecret_config_id_are_projected(self):
        self.assertEqual(self.runtime['auth_demo_user_id'], 'e492f6bdaf1735e12b2de96d')
        self.assertEqual(self.runtime['auth_demo_user_email'], 'demo@llm-wiki.dev')
        self.assertEqual(self.runtime['auth_demo_user_role'], 'member')
        self.assertRegex(self.runtime['config_id'], r'^sha256:[0-9a-f]{64}$')
        self.assertEqual(self.runtime['target'], 'auth')
        self.assertEqual(self.runtime['environment'], 'prod')

    def test_production_revision_accepts_sdk_file_mount_and_checks_managed_config(self):
        self.assertTrue(self.adapter.service_matches('auth', self.revision, IMAGE,
                                                     auth_config_version=VERSION))
        unrelated = copy.deepcopy(self.revision)
        unrelated['spec']['containers'][0]['env'].append({'name': 'PLATFORM_TRACE', 'value': 'enabled'})
        self.assertTrue(self.adapter.service_matches('auth', unrelated, IMAGE,
                                                     auth_config_version=VERSION))

        tamper = {
            'wrong version': lambda r: r['spec']['volumes'][0]['secret']['items'][0].update(key='18'),
            'wrong file path': lambda r: r['spec']['volumes'][0]['secret']['items'][0].update(path='other.json'),
            'wrong secret': lambda r: r['spec']['volumes'][0]['secret'].update(secretName='wrong-secret'),
            'wrong project': lambda r: r['spec']['volumes'][0]['secret'].update(
                secretName='projects/another-project/secrets/'+self.runtime['config_secret_resource'].split('/')[-1]),
            'wrong account': lambda r: r['spec'].update(serviceAccountName='wrong@llm-wiki-cloud.iam.gserviceaccount.com'),
            'not ready': lambda r: r['status']['conditions'][0].update(status='False'),
            'legacy environment': lambda r: r['spec']['containers'][0]['env'].append(
                {'name': 'JWT_SECRET', 'valueFrom': {'secretKeyRef': {'name': 'jwt-secret-prod', 'key': 'latest'}}}),
        }
        for name, edit in tamper.items():
            with self.subTest(name=name):
                value = copy.deepcopy(self.revision)
                edit(value)
                self.assertFalse(self.adapter.service_matches('auth', value, IMAGE,
                                                              auth_config_version=VERSION))

        unused_read_only = copy.deepcopy(self.revision)
        unused_read_only['spec']['containers'][0]['volumeMounts'][0]['readOnly'] = False
        self.assertTrue(self.adapter.service_matches('auth', unused_read_only, IMAGE,
                                                     auth_config_version=VERSION))

    def test_production_adapter_args_version_and_verify_use_numeric_secret(self):
        script = fixtures.ROOT / 'deploy/components/auth_config.py'
        with tempfile.TemporaryDirectory(prefix='lwc-auth-production-contract-') as directory:
            plan_path = Path(directory) / 'plan.json'
            plan_path.write_text(json.dumps({'normalized': self.plan}))

            args = subprocess.run(['python3', str(script), 'args', str(plan_path), 'auth', VERSION],
                                  text=True, capture_output=True)
            self.assertEqual(args.returncode, 0, args.stderr)
            self.assertIn('/var/run/lwc-auth-config/auth.json=', args.stdout)
            self.assertIn(':' + VERSION, args.stdout)
            self.assertIn('--remove-env-vars', args.stdout)
            self.assertIn('--remove-secrets', args.stdout)
            self.assertNotIn(self.runtime['config_id'], args.stdout)

            version = subprocess.run(['python3', str(script), 'version', str(plan_path), 'auth'],
                                     input=json.dumps(self.revision), text=True, capture_output=True)
            self.assertEqual(version.returncode, 0, version.stderr)
            self.assertEqual(version.stdout.strip(), VERSION)

            verify = subprocess.run(['python3', str(script), 'verify', str(plan_path), 'auth',
                                     REVISION, IMAGE, '', VERSION],
                                    input=json.dumps(self.revision), text=True, capture_output=True)
            self.assertEqual(verify.returncode, 0, verify.stderr)

            wrong = copy.deepcopy(self.revision)
            wrong['spec']['volumes'][0]['secret']['items'][0]['key'] = '18'
            rejected = subprocess.run(['python3', str(script), 'verify', str(plan_path), 'auth',
                                       REVISION, IMAGE, '', VERSION],
                                      input=json.dumps(wrong), text=True, capture_output=True)
            self.assertNotEqual(rejected.returncode, 0)

    def test_direct_cli_uses_trusted_project_mapping_for_version_freeze_verify_and_rollback(self):
        secret = self.runtime['config_secret_resource'].split('/')[-1]
        id_resource = 'projects/' + self.plan['gcp']['project_id'] + '/secrets/' + secret
        numeric_resource = 'projects/580854833715/secrets/' + secret

        full_resource = copy.deepcopy(self.revision)
        full_resource['spec']['volumes'][0]['secret']['secretName'] = id_resource
        accepted = self.run_auth_config('verify', full_resource)
        self.assertEqual(accepted.returncode, 0, accepted.stderr)

        aliased = copy.deepcopy(self.revision)
        aliased['metadata']['annotations']['run.googleapis.com/secrets'] = (
            'auth-config-alias:' + numeric_resource)
        aliased['spec']['volumes'][0]['secret']['secretName'] = 'auth-config-alias'
        alias_acceptance = self.run_auth_config('verify', aliased)
        self.assertEqual(alias_acceptance.returncode, 0, alias_acceptance.stderr)

        revision = copy.deepcopy(self.revision)
        revision['spec']['volumes'][0]['secret']['secretName'] = (
            numeric_resource)

        version = self.run_auth_config('version', revision)
        self.assertEqual(version.returncode, 0, version.stderr)
        self.assertEqual(version.stdout.strip(), VERSION)
        for mode in ('verify', 'freeze'):
            with self.subTest(mode=mode):
                result = self.run_auth_config(mode, revision)
                self.assertEqual(result.returncode, 0, result.stderr)

        frozen = self.run_auth_config('freeze', revision)
        rollback = self.run_auth_config('rollback', revision,
                                        json.loads(frozen.stdout)['config_fingerprint'])
        self.assertEqual(rollback.returncode, 0, rollback.stderr)

        wrong_project = copy.deepcopy(revision)
        wrong_project['spec']['volumes'][0]['secret']['secretName'] = (
            'projects/999999999999/secrets/' + secret)
        rejected = self.run_auth_config('version', wrong_project)
        self.assertNotEqual(rejected.returncode, 0)

        rejected_mapping = self.run_auth_config('verify', revision,
                                               returned_project_id='unrelated-project')
        self.assertNotEqual(rejected_mapping.returncode, 0)
        rejected_number = self.run_auth_config('verify', revision,
                                              returned_project_number='999999999999')
        self.assertNotEqual(rejected_number.returncode, 0)

    def test_file_mode_precedence_uses_cloud_run_effective_nonroot_mode(self):
        revision = copy.deepcopy(self.revision)
        secret = revision['spec']['volumes'][0]['secret']
        secret['defaultMode'] = 0o400
        rejected_default = self.run_auth_config('verify', revision)
        self.assertNotEqual(rejected_default.returncode, 0)

        secret['defaultMode'] = 0o444
        secret['items'][0]['mode'] = 0
        accepted_zero_item = self.run_auth_config('verify', revision)
        self.assertEqual(accepted_zero_item.returncode, 0, accepted_zero_item.stderr)

        secret['defaultMode'] = 0o644
        accepted_masked_default = self.run_auth_config('verify', revision)
        self.assertEqual(accepted_masked_default.returncode, 0, accepted_masked_default.stderr)

        for field, value in (('defaultMode', True), ('defaultMode', 292.0), ('defaultMode', '292'),
                             ('mode', True), ('mode', 292.0), ('mode', '292')):
            with self.subTest(field=field, value=repr(value)):
                invalid = copy.deepcopy(self.revision)
                target = invalid['spec']['volumes'][0]['secret']
                if field == 'mode':
                    target['items'][0]['mode'] = value
                else:
                    target['defaultMode'] = value
                result = self.run_auth_config('verify', invalid)
                self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
