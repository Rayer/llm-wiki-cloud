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
            'annotations': {},
        },
        'spec': {
            'serviceAccountName': plan['bff']['runtime_service_account'],
            'containers': [{
                'image': image,
                'env': [{'name': 'LWC_BFF_CONFIG_PATH', 'value': CONFIG_PATH}],
                'volumeMounts': [{'name': 'bff-volume', 'mountPath': '/etc/lwc-bff-config'}],
            }],
            'volumes': [{'name': 'bff-volume', 'secret': {
                'secretName': alias,
                'items': [{'key': version, 'path': 'bff.json'}],
            }}],
        },
        'status': {
            'imageDigest': image,
            'conditions': [{'type': 'Ready', 'status': 'True'}],
        },
    }


class BFFConfigFileContractTests(unittest.TestCase):
    def run_auth_config(self, mode, plan, value, version=CONFIG_VERSION, fingerprint='',
                        project_number='580854833715', returned_project_id=None,
                        returned_project_number=None):
        script = fixtures.ROOT / 'deploy/components/auth_config.py'
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'plan.json'
            path.write_text(json.dumps({'normalized': plan}))
            args = ['python3', str(script), mode, str(path), 'bff']
            if mode == 'args':
                args.append(version)
            else:
                args.extend([value['metadata']['name'], value['status']['imageDigest'], fingerprint, version])
            env = fixtures.project_mapping_env(directory, plan['gcp']['project_id'], project_number,
                                               returned_project_id, returned_project_number)
            return subprocess.run(args, input=json.dumps(value) if mode != 'args' else None,
                                  text=True, capture_output=True, env=env)

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

    def test_effective_revision_accepts_sdk_file_mount_and_rejects_wrong_identity(self):
        plan = fixtures.bff_plan('development')
        revision = candidate('development', plan)
        verified = self.run_auth_config('verify', plan, revision)
        self.assertEqual(verified.returncode, 0, verified.stderr)

        mutations = {
            'wrong numeric version': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(key='41'),
            'wrong file mode': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(mode=384),
            'wrong file name': lambda value: value['spec']['volumes'][0]['secret']['items'][0].update(path='other.json'),
            'wrong secret': lambda value: value['spec']['volumes'][0]['secret'].update(secretName='wrong-secret'),
            'wrong resource': lambda value: value['spec']['volumes'][0]['secret'].update(
                secretName='projects/another-project/secrets/'+
                fixtures.bff_plan('development')['bff']['runtime_inputs']['config_secret_resource'].split('/')[-1]),
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

        unused_read_only = copy.deepcopy(revision)
        unused_read_only['spec']['containers'][0]['volumeMounts'][0]['readOnly'] = False
        accepted = self.run_auth_config('verify', plan, unused_read_only)
        self.assertEqual(accepted.returncode, 0, accepted.stderr)

    def test_file_mode_precedence_uses_cloud_run_effective_nonroot_mode(self):
        plan = fixtures.bff_plan('development')
        baseline = candidate('development', plan)
        secret = baseline['spec']['volumes'][0]['secret']
        secret['defaultMode'] = 0o400
        rejected_default = self.run_auth_config('verify', plan, baseline)
        self.assertNotEqual(rejected_default.returncode, 0)

        secret['defaultMode'] = 0o444
        secret['items'][0]['mode'] = 0
        accepted_zero_item = self.run_auth_config('verify', plan, baseline)
        self.assertEqual(accepted_zero_item.returncode, 0, accepted_zero_item.stderr)

        secret['defaultMode'] = 0o400
        secret['items'][0]['mode'] = 0o444
        accepted_item_override = self.run_auth_config('verify', plan, baseline)
        self.assertEqual(accepted_item_override.returncode, 0, accepted_item_override.stderr)

        secret['items'][0]['mode'] = 0
        secret['defaultMode'] = 0o644
        accepted_masked_default = self.run_auth_config('verify', plan, baseline)
        self.assertEqual(accepted_masked_default.returncode, 0, accepted_masked_default.stderr)

        secret['items'][0]['mode'] = 0o400
        rejected_unreadable = self.run_auth_config('verify', plan, baseline)
        self.assertNotEqual(rejected_unreadable.returncode, 0)

        for field, value in (('defaultMode', True), ('defaultMode', 292.0), ('defaultMode', '292'),
                             ('mode', True), ('mode', 292.0), ('mode', '292')):
            with self.subTest(field=field, value=repr(value)):
                invalid = candidate('development', plan)
                target = invalid['spec']['volumes'][0]['secret']
                if field == 'mode':
                    target['items'][0]['mode'] = value
                else:
                    target['defaultMode'] = value
                result = self.run_auth_config('verify', plan, invalid)
                self.assertNotEqual(result.returncode, 0)

    def test_numeric_project_resource_uses_trusted_selected_project_mapping(self):
        plan = fixtures.bff_plan('development')
        revision = candidate('development', plan)
        expected_resource = plan['bff']['runtime_inputs']['config_secret_resource']
        secret = expected_resource.split('/')[-1]
        selected_id = plan['gcp']['project_id']
        id_resource = 'projects/' + selected_id + '/secrets/' + secret
        numeric_resource = 'projects/580854833715/secrets/' + secret

        for resource in (id_resource, numeric_resource):
            with self.subTest(resource=resource):
                full_resource = candidate('development', plan)
                full_resource['spec']['volumes'][0]['secret']['secretName'] = resource
                accepted = self.run_auth_config('verify', plan, full_resource)
                self.assertEqual(accepted.returncode, 0, accepted.stderr)

        aliased = candidate('development', plan)
        aliased['metadata']['annotations']['run.googleapis.com/secrets'] = (
            'config-alias:' + numeric_resource)
        aliased['spec']['volumes'][0]['secret']['secretName'] = 'config-alias'
        alias_acceptance = self.run_auth_config('verify', plan, aliased)
        self.assertEqual(alias_acceptance.returncode, 0, alias_acceptance.stderr)

        revision['spec']['volumes'][0]['secret']['secretName'] = (
            numeric_resource)

        version = self.run_auth_config('version', plan, revision)
        self.assertEqual(version.returncode, 0, version.stderr)
        self.assertEqual(version.stdout.strip(), CONFIG_VERSION)
        for mode in ('verify', 'freeze'):
            with self.subTest(mode=mode):
                result = self.run_auth_config(mode, plan, revision)
                self.assertEqual(result.returncode, 0, result.stderr)

        frozen = self.run_auth_config('freeze', plan, revision)
        rollback = self.run_auth_config('rollback', plan, revision,
                                        fingerprint=json.loads(frozen.stdout)['config_fingerprint'])
        self.assertEqual(rollback.returncode, 0, rollback.stderr)

        wrong_project = copy.deepcopy(revision)
        wrong_project['spec']['volumes'][0]['secret']['secretName'] = (
            'projects/999999999999/secrets/' + secret)
        rejected = self.run_auth_config('version', plan, wrong_project)
        self.assertNotEqual(rejected.returncode, 0)

        namespace_only = copy.deepcopy(revision)
        namespace_only['metadata']['namespace'] = '999999999999'
        rejected_mapping = self.run_auth_config('version', plan, namespace_only,
                                                returned_project_id='another-project')
        self.assertNotEqual(rejected_mapping.returncode, 0)

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
