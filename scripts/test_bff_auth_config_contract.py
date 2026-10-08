#!/usr/bin/env python3
"""Offline contract tests for the frozen BFF config file mount."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import test_auth_config_contract as fixtures

CONFIG_PATH = '/etc/lwc-bff-config/bff.json'
CONFIG_VERSION = '42'


def production_plan():
    return fixtures.bff_plan('production')


def reviewed_production_plan():
    return production_plan()


def profile_runtime_plan():
    return fixtures.bff_plan('development')


def candidate(environment, plan_override=None, version=CONFIG_VERSION):
    plan = plan_override or fixtures.bff_plan(environment)
    resource = plan['bff']['runtime_inputs']['config_secret_resource']
    secret_name = resource.split('/')[3]
    revision_name = plan['bff']['service_name'] + '-00042-test'
    image_name = 'llm-wiki-bff'
    image = 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/' + image_name + '@sha256:' + 'c' * 64
    alias = secret_name
    return {
        'metadata': {
            'name': revision_name,
            'namespace': 'llm-wiki-cloud',
            'annotations': {'run.googleapis.com/secrets': alias + ':' + resource},
        },
        'spec': {
            'serviceAccountName': plan['bff']['runtime_service_account'],
            'containers': [{
                'image': image,
                'env': [{'name': 'LWC_BFF_CONFIG_PATH', 'value': CONFIG_PATH}],
                'volumeMounts': [{'name': alias, 'mountPath': '/etc/lwc-bff-config', 'readOnly': True}],
            }],
            'volumes': [{'name': alias, 'secret': {
                'secretName': alias,
                'items': [{'key': version, 'path': 'bff.json', 'mode': 292}],
            }}],
        },
        'status': {
            'imageDigest': image,
            'conditions': [{'type': 'Ready', 'status': 'True'}],
        },
    }


class BFFConfigFileContractTests(unittest.TestCase):
    def run_auth_config(self, mode, plan, value, version=CONFIG_VERSION):
        script = fixtures.ROOT / 'deploy/components/auth_config.py'
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'plan.json'
            path.write_text(json.dumps({'normalized': plan}))
            args = ['python3', str(script), mode, str(path), 'bff']
            if mode == 'args':
                args.append(version)
            else:
                args.extend([value['metadata']['name'], value['status']['imageDigest'], '', version])
            return subprocess.run(args, input=json.dumps(value) if mode != 'args' else None,
                                  text=True, capture_output=True)

    def test_deploy_args_pin_numeric_version_and_remove_legacy_bindings(self):
        for environment in ('development', 'production'):
            with self.subTest(environment=environment):
                plan = fixtures.bff_plan(environment)
                result = self.run_auth_config('args', plan, {}, CONFIG_VERSION)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('LWC_BFF_CONFIG_PATH=' + CONFIG_PATH, result.stdout)
                self.assertIn(CONFIG_PATH + '=' + plan['bff']['runtime_inputs']['config_secret_resource'].split('/')[3] + ':' + CONFIG_VERSION,
                              result.stdout)
                self.assertIn('--remove-env-vars', result.stdout)
                self.assertIn('PIPELINE_COOLDOWN_SECONDS', result.stdout)
                self.assertIn('--remove-secrets', result.stdout)
                self.assertNotIn('JWT_SECRET=', result.stdout)
                self.assertNotIn('DEEPSEEK_API_KEY=', result.stdout)
                self.assertNotIn('TYPESAFE_JEV_API_KEY=', result.stdout)

    def test_effective_revision_requires_the_exact_read_only_numeric_file_mount(self):
        plan = fixtures.bff_plan('development')
        revision = candidate('development', plan)
        verified = self.run_auth_config('verify', plan, revision)
        self.assertEqual(verified.returncode, 0, verified.stderr)

        mutations = {
            'wrong numeric version': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(key='41'),
            'wrong file mode': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(mode=384),
            'wrong file name': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(path='other.json'),
            'writable mount': lambda value: value['spec']['containers'][0]['volumeMounts'][0].update(readOnly=False),
            'wrong resource': lambda value: value['metadata']['annotations'].update(
                {'run.googleapis.com/secrets': 'wrong:projects/llm-wiki-cloud/secrets/wrong'}),
            'missing locator': lambda value: value['spec']['containers'][0].update(env=[]),
            'legacy config environment': lambda value: value['spec']['containers'][0]['env'].append(
                {'name': 'PIPELINE_COOLDOWN_SECONDS', 'value': '1'}),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                tampered = copy.deepcopy(revision)
                mutate(tampered)
                rejected = self.run_auth_config('verify', plan, tampered)
                self.assertNotEqual(rejected.returncode, 0)

    def test_provider_matching_binds_candidate_to_published_file_version(self):
        import sys
        sys.path.insert(0, str(fixtures.ROOT / 'deploy/engine'))
        import providers

        plan = fixtures.bff_plan('development')
        revision = candidate('development', plan)
        image = revision['status']['imageDigest']
        adapter = providers.Providers({'normalized': plan}, fixtures.ROOT)
        self.assertTrue(adapter.service_matches('bff', revision, image, CONFIG_VERSION))
        self.assertFalse(adapter.service_matches('bff', revision, image, '43'))
        self.assertFalse(adapter.service_matches('bff', revision, image, None))


if __name__ == '__main__':
    unittest.main()
