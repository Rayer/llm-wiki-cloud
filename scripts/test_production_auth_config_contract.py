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


if __name__ == '__main__':
    unittest.main()
