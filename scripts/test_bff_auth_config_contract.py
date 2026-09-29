#!/usr/bin/env python3
"""LWC-332: real YAML plans and BFF shell/Python path, with offline providers."""
import copy
import json
import unittest

import test_auth_config_contract as fixtures
from test_production_auth_config_contract import candidate as production_candidate

TEST_PROFILE_AUDIENCE = 'https://profile-dispatch.dev.example.invalid'
TEST_PROFILE_SERVICE_ACCOUNT = 'lwc-profile-dispatcher-dev@llm-wiki-cloud.iam.gserviceaccount.com'
TEST_TYPESAFE_SECRET = {'name': 'typesafe-jev-api-key-dev-test', 'version': '7'}


def candidate(environment, plan_override=None):
    value = production_candidate('bff')
    plan = plan_override or fixtures.bff_plan(environment)
    value['metadata']['name'] = plan['bff']['service_name'] + '-00042-test'
    value['spec']['serviceAccountName'] = plan['bff']['runtime_service_account']
    if environment == 'development':
        # Separately approved DEV settings must survive, even when unlike YAML.
        entries = value['spec']['containers'][0]['env']
        for entry in entries:
            if entry['name'] != 'QUERY_STAGE_CONFIG_PATH' and 'value' in entry:
                entry['value'] = 'preserved-dev-value'
        entries[-1]['valueFrom']['secretKeyRef'] = {'name': 'jwt-secret-dev', 'key': '7'}
        entries.extend([
            {'name': 'EXPORT_JOB_URL', 'value': 'https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/export-job-dev:run'},
            {'name': 'EXPORT_SIGNING_SERVICE_ACCOUNT', 'value': 'lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com'},
        ])
    value['spec']['containers'][0]['env'] += [
        {'name': 'BUCKET', 'value': 'preserved-bucket'},
        {'name': 'DEEPSEEK_API_KEY', 'valueFrom': {'secretKeyRef': {'name': 'deepseek-apikey', 'key': '3'}}},
    ]
    if environment == 'development' and plan['bff'].get('profile_runtime_audience'):
        value['spec']['containers'][0]['env'] += [
            {'name': 'PROFILE_RUNTIME_AUDIENCE', 'value': plan['bff']['profile_runtime_audience']},
            {'name': 'PROFILE_RUNTIME_SERVICE_ACCOUNT', 'value': plan['bff']['profile_runtime_service_account']},
            {'name': 'TYPESAFE_JEV_API_KEY', 'valueFrom': {'secretKeyRef': {
                'name': plan['bff']['secret_references']['typesafe_jev_api_key']['name'],
                'key': plan['bff']['secret_references']['typesafe_jev_api_key']['version'],
            }}},
        ]
    if environment == 'development' and plan['bff'].get('pipeline_demo_user_ids'):
        value['spec']['containers'][0]['env'].append({
            'name': 'PIPELINE_DEMO_USER_IDS', 'value': ','.join(plan['bff']['pipeline_demo_user_ids']),
        })
    value['metadata']['annotations'] = {'run.googleapis.com/vpc-access-egress': 'private-ranges-only'}
    return value


def profile_runtime_plan():
    import yaml

    plan = copy.deepcopy(fixtures.bff_plan('production'))
    dev_config = yaml.safe_load((fixtures.ROOT / 'deploy/environments/development.yaml').read_text())
    plan['environment'] = 'development'
    plan['config_path'] = 'deploy/environments/development.yaml'
    plan['export_job'] = copy.deepcopy(dev_config['export_job'])
    plan['bff'].update({
        'service_name': dev_config['bff']['service_name'],
        'runtime_service_account': dev_config['bff']['runtime_service_account'],
        'firestore_database_id': dev_config['bff']['firestore_database_id'],
        'allowed_origins': dev_config['bff']['allowed_origins'],
        'query_config': dev_config['bff']['query_config'],
        'secret_references': {
            'jwt': dev_config['bff']['secret_references']['jwt'],
            'deepseek_api_key': dev_config['bff']['secret_references']['deepseek_api_key'],
        },
    })
    component = plan['components']['bff']
    component.update({
        'service_name': dev_config['bff']['service_name'],
        'runtime_service_account': dev_config['bff']['runtime_service_account'],
        'secret_references': copy.deepcopy(plan['bff']['secret_references']),
    })
    plan['bff']['profile_runtime_audience'] = TEST_PROFILE_AUDIENCE
    plan['bff']['profile_runtime_service_account'] = TEST_PROFILE_SERVICE_ACCOUNT
    plan['bff']['secret_references']['typesafe_jev_api_key'] = copy.deepcopy(TEST_TYPESAFE_SECRET)
    component['profile_runtime_audience'] = TEST_PROFILE_AUDIENCE
    component['profile_runtime_service_account'] = TEST_PROFILE_SERVICE_ACCOUNT
    component['secret_references']['typesafe_jev_api_key'] = copy.deepcopy(TEST_TYPESAFE_SECRET)
    return plan


class BFFQueryConfigTests(unittest.TestCase):
    def run_shell(self, value, environment, **kwargs):
        return fixtures.AuthConfigContractTests.run_shell(self, value, environment=environment,
                                                        component='bff', **kwargs)

    def test_yaml_selection_delivered_and_exact_revision_verified_before_traffic(self):
        for environment in ('development', 'production'):
            with self.subTest(environment=environment):
                plan = fixtures.bff_plan(environment)
                value = candidate(environment, plan)
                result, commands, artifacts = self.run_shell(value, environment, action='bff_mutate')
                self.assertEqual(result.returncode, 0, result.stderr)
                updates = [c for c in commands if c[:3] == ['run', 'services', 'update']]
                self.assertEqual(len(updates), 1)
                update = updates[0]
                self.assertIn('--no-traffic', update)
                env_arg = update[update.index('--update-env-vars') + 1]
                path = fixtures.bff_plan(environment)['query_config']['runtime_path']
                self.assertIn('QUERY_STAGE_CONFIG_PATH=' + path, env_arg)
                if environment == 'development':
                    self.assertEqual(env_arg, '^|^QUERY_STAGE_CONFIG_PATH=' + path
                                     + '|PROFILE_RUNTIME_AUDIENCE=' + plan['bff']['profile_runtime_audience']
                                     + '|PROFILE_RUNTIME_SERVICE_ACCOUNT=' + plan['bff']['profile_runtime_service_account']
                                     + '|EXPORT_JOB_URL=https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/export-job-dev:run'
                                     + '|EXPORT_SIGNING_SERVICE_ACCOUNT=lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com'
                                     + '|PIPELINE_DEMO_USER_IDS=e492f6bdaf1735e12b2de96d')
                    self.assertEqual(update[update.index('--update-secrets') + 1],
                                     'TYPESAFE_JEV_API_KEY=' + plan['bff']['secret_references']['typesafe_jev_api_key']['name']
                                     + ':' + plan['bff']['secret_references']['typesafe_jev_api_key']['version'])
                else:
                    self.assertIn('AUTH_SERVICE_URL=https://auth.rayer.idv.tw', env_arg)
                    self.assertIn('JWT_SECRET=jwt-secret-prod:latest', update)
                    self.assertNotIn('PIPELINE_DEMO_USER_IDS', env_arg)
                self.assertNotIn('--remove-env-vars', update)
                self.assertFalse(any(arg.startswith(('--clear-', '--set-', '--service-account', '--network', '--subnet')) for arg in update))
                traffic = next(i for i, c in enumerate(commands) if c[:3] == ['run', 'services', 'update-traffic'])
                revision = value['metadata']['name']
                self.assertIn(revision + '=100', commands[traffic])
                for calls in (commands[:traffic], commands[traffic + 1:]):
                    reads = [c[3] for c in calls if c[:3] == ['run', 'revisions', 'describe']]
                    self.assertTrue(reads)
                    self.assertEqual(set(reads), {revision})
                self.assertEqual(json.loads(artifacts['journal.json'])['components']['bff']['history'], ['pending', 'accepted'])

    def test_bad_candidate_query_selection_blocks_traffic_and_reconcile(self):
        for environment in ('development', 'production'):
            for kind in ('missing', 'wrong', 'duplicate', 'secret'):
                value = candidate(environment)
                entries = value['spec']['containers'][0]['env']
                target = entries[0]
                if kind == 'missing': entries.remove(target)
                elif kind == 'wrong': target['value'] = '/app/configs/query/unknown.json'
                elif kind == 'duplicate': entries.append(copy.deepcopy(target))
                else: entries[0] = {'name': 'QUERY_STAGE_CONFIG_PATH', 'valueFrom': {'secretKeyRef': {'name': 'wrong', 'key': 'latest'}}}
                for action in ('bff_mutate', 'bff_reconcile'):
                    with self.subTest(environment=environment, kind=kind, action=action):
                        result, commands, _ = self.run_shell(value, environment, action=action)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_dev_profile_dispatcher_and_typesafe_secret_are_exactly_delivered_and_verified(self):
        plan = profile_runtime_plan()
        value = candidate('development', plan)
        result, commands, _ = self.run_shell(value, 'development', action='bff_mutate', plan_override=plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
        env_arg = update[update.index('--update-env-vars') + 1]
        self.assertIn('PROFILE_RUNTIME_AUDIENCE=' + TEST_PROFILE_AUDIENCE, env_arg)
        self.assertIn('PROFILE_RUNTIME_SERVICE_ACCOUNT=' + TEST_PROFILE_SERVICE_ACCOUNT, env_arg)
        self.assertIn('--update-secrets', update)
        self.assertIn('TYPESAFE_JEV_API_KEY=' + TEST_TYPESAFE_SECRET['name'] + ':' + TEST_TYPESAFE_SECRET['version'],
                      update[update.index('--update-secrets') + 1])
        traffic = next(i for i, c in enumerate(commands) if c[:3] == ['run', 'services', 'update-traffic'])
        self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[:traffic]))
        self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[traffic + 1:]))

    def test_demo_ids_are_dev_only_optional_and_preserve_unmanaged_values(self):
        plan = copy.deepcopy(fixtures.bff_plan('development'))
        self.assertEqual(plan['bff']['pipeline_demo_user_ids'], ['e492f6bdaf1735e12b2de96d'])
        configured = candidate('development', plan)
        result, commands, _ = self.run_shell(configured, 'development', action='bff_mutate', plan_override=plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
        self.assertIn('PIPELINE_DEMO_USER_IDS=e492f6bdaf1735e12b2de96d',
                      update[update.index('--update-env-vars') + 1])

        plan['bff'].pop('pipeline_demo_user_ids')
        unconfigured = candidate('development', plan)
        unconfigured['spec']['containers'][0]['env'].append({
            'name': 'PIPELINE_DEMO_USER_IDS', 'value': 'preexisting-demo-id',
        })
        result, commands, _ = self.run_shell(unconfigured, 'development', action='bff_mutate', plan_override=plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
        self.assertNotIn('PIPELINE_DEMO_USER_IDS', update[update.index('--update-env-vars') + 1])
        if '--remove-env-vars' in update:
            self.assertNotIn('PIPELINE_DEMO_USER_IDS', update[update.index('--remove-env-vars') + 1])

    def test_dev_profile_binding_mismatch_blocks_traffic(self):
        plan = profile_runtime_plan()
        cases = ('audience', 'invoker', 'missing secret', 'wrong secret version', 'duplicate secret')
        for kind in cases:
            value = candidate('development', plan)
            entries = value['spec']['containers'][0]['env']
            if kind in ('audience', 'invoker'):
                name = 'PROFILE_RUNTIME_AUDIENCE' if kind == 'audience' else 'PROFILE_RUNTIME_SERVICE_ACCOUNT'
                next(entry for entry in entries if entry['name'] == name)['value'] = 'wrong-binding'
            elif kind == 'missing secret':
                entries.remove(next(entry for entry in entries if entry['name'] == 'TYPESAFE_JEV_API_KEY'))
            elif kind == 'wrong secret version':
                next(entry for entry in entries if entry['name'] == 'TYPESAFE_JEV_API_KEY')[
                    'valueFrom']['secretKeyRef']['key'] = 'latest'
            else:
                entries.append(copy.deepcopy(next(entry for entry in entries if entry['name'] == 'TYPESAFE_JEV_API_KEY')))
            with self.subTest(kind=kind):
                result, commands, _ = self.run_shell(value, 'development', action='bff_mutate', plan_override=plan)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_dev_typesafe_preflight_checks_exact_secret_and_enabled_numeric_version(self):
        plan = profile_runtime_plan()
        value = candidate('development', plan)
        result, commands, _ = self.run_shell(value, 'development', action='bff_preflight', plan_override=plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        reads = [c for c in commands if c[:3] == ['secrets', 'versions', 'describe']]
        self.assertEqual(reads, [[
            'secrets', 'versions', 'describe', TEST_TYPESAFE_SECRET['version'], '--secret', TEST_TYPESAFE_SECRET['name'],
            '--project', 'llm-wiki-cloud', '--format=value(state)', '--quiet',
        ]])
        disabled, disabled_commands, _ = self.run_shell(
            value, 'development', action='bff_preflight', secret_state='DISABLED', plan_override=plan)
        self.assertNotEqual(disabled.returncode, 0)
        self.assertTrue(any(c[:3] == ['secrets', 'versions', 'describe'] for c in disabled_commands))
        self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in disabled_commands))

    def test_production_rejects_dev_profile_runtime_bindings(self):
        for entry in (
            {'name': 'PROFILE_RUNTIME_AUDIENCE', 'value': TEST_PROFILE_AUDIENCE},
            {'name': 'PROFILE_RUNTIME_SERVICE_ACCOUNT', 'value': TEST_PROFILE_SERVICE_ACCOUNT},
            {'name': 'TYPESAFE_JEV_API_KEY', 'valueFrom': {'secretKeyRef': {
                'name': TEST_TYPESAFE_SECRET['name'], 'key': TEST_TYPESAFE_SECRET['version'],
            }}},
        ):
            with self.subTest(name=entry['name']):
                value = candidate('production')
                value['spec']['containers'][0]['env'].append(entry)
                result, commands, _ = self.run_shell(value, 'production', action='bff_mutate')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_malformed_or_inconsistent_plan_fails_before_service_mutation(self):
        for environment in ('development', 'production'):
            for field, bad in (('runtime_path', '/tmp/query.json'), ('repository_path', '../query.json'),
                               ('repository_path', 'apps/bff/configs/query/unknown.json'),
                               ('revision', 'wrong'), ('digest', 'sha256:' + '0' * 64), ('schema_version', 999)):
                plan = copy.deepcopy(fixtures.bff_plan(environment))
                plan['query_config'][field] = bad
                with self.subTest(environment=environment, field=field):
                    result, commands, _ = self.run_shell(candidate(environment), environment,
                                                       action='bff_mutate', plan_override=plan)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in commands))

    def test_query_delivery_is_independent_of_auth_google_and_final_mismatch_fails(self):
        for environment in ('development', 'production'):
            plan = copy.deepcopy(fixtures.bff_plan(environment))
            plan['auth'].pop('google')
            for final_bad in (False, True):
                with self.subTest(environment=environment, final_bad=final_bad):
                    result, commands, artifacts = self.run_shell(candidate(environment), environment,
                        action='bff_mutate', plan_override=plan, final_bad=final_bad)
                    self.assertEqual(result.returncode == 0, not final_bad, result.stderr)
                    update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
                    expected_env = '^|^QUERY_STAGE_CONFIG_PATH=' + plan['query_config']['runtime_path']
                    if environment == 'development':
                        expected_env += ('|PROFILE_RUNTIME_AUDIENCE=' + plan['bff']['profile_runtime_audience']
                                         + '|PROFILE_RUNTIME_SERVICE_ACCOUNT=' + plan['bff']['profile_runtime_service_account']
                                         + '|EXPORT_JOB_URL=https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/export-job-dev:run'
                                         '|EXPORT_SIGNING_SERVICE_ACCOUNT=lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com'
                                         '|PIPELINE_DEMO_USER_IDS=e492f6bdaf1735e12b2de96d')
                    self.assertEqual(update[update.index('--update-env-vars') + 1], expected_env)
                    self.assertNotIn('--remove-env-vars', update)
                    if environment == 'development':
                        self.assertEqual(update[update.index('--update-secrets') + 1],
                                         'TYPESAFE_JEV_API_KEY=' + plan['bff']['secret_references']['typesafe_jev_api_key']['name']
                                         + ':' + plan['bff']['secret_references']['typesafe_jev_api_key']['version'])
                    else:
                        self.assertNotIn('--update-secrets', update)
                    self.assertTrue(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
                    if final_bad:
                        self.assertEqual(json.loads(artifacts['journal.json'])['components']['bff']['state'], 'unknown')

    def test_rollback_retains_exact_prior_config_without_redeploy_or_values(self):
        for environment in ('development', 'production'):
            tampers = ('', 'query', 'other env', 'secret', 'network')
            if environment == 'development':
                tampers += ('demo IDs',)
            for tamper in tampers:
                value = candidate(environment)
                value['spec']['containers'][0]['env'][0]['value'] = '/app/configs/query/prior-image-only.json'
                value['spec']['containers'][0]['env'].append({'name': 'OPAQUE_SETTING', 'value': 'CANARY-NEVER-EMIT'})
                edit = {
                    '': '', 'query': "sed -i.bak 's/prior-image-only/changed/g' \"$FIXTURE/candidate.json\";",
                    'other env': "sed -i.bak 's/preserved-bucket/changed/g' \"$FIXTURE/candidate.json\";",
                    'secret': "sed -i.bak 's/deepseek-apikey/changed/g' \"$FIXTURE/candidate.json\";",
                    'network': "sed -i.bak 's/private-ranges-only/all-traffic/g' \"$FIXTURE/candidate.json\";",
                    'demo IDs': "sed -i.bak 's/e492f6bdaf1735e12b2de96d/changed/g' \"$FIXTURE/candidate.json\";",
                }[tamper]
                with self.subTest(environment=environment, tamper=tamper):
                    result, commands, artifacts = self.run_shell(value, environment,
                        action='bff_freeze; touch "$FIXTURE/switch-needed"; ' + edit + 'bff_rollback')
                    self.assertEqual(result.returncode == 0, not tamper, result.stderr)
                    handle = json.loads(artifacts['rollback.json'])['handles']['bff']
                    self.assertEqual(handle['revision'], value['metadata']['name'])
                    self.assertRegex(handle['config_fingerprint'], r'^sha256:[0-9a-f]{64}$')
                    self.assertNotIn('CANARY-NEVER-EMIT', result.stdout + result.stderr + json.dumps(artifacts))
                    self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in commands))
                    traffic = [c for c in commands if c[:3] == ['run', 'services', 'update-traffic']]
                    self.assertEqual(len(traffic), 0 if tamper else 1)
                    if traffic: self.assertIn(value['metadata']['name'] + '=100', traffic[0])

    def test_rollback_restores_prior_demo_env_presence_or_value_by_revision(self):
        plan = copy.deepcopy(fixtures.bff_plan('development'))
        for prior_value in (None, 'previous-demo-user'):
            value = candidate('development', plan)
            entries = value['spec']['containers'][0]['env']
            entries[:] = [entry for entry in entries if entry['name'] != 'PIPELINE_DEMO_USER_IDS']
            if prior_value is not None:
                entries.append({'name': 'PIPELINE_DEMO_USER_IDS', 'value': prior_value})
            result, commands, artifacts = self.run_shell(value, 'development', plan_override=plan,
                action='bff_freeze; touch "$FIXTURE/switch-needed"; bff_rollback')
            self.assertEqual(result.returncode, 0, result.stderr)
            restored = [entry['value'] for entry in value['spec']['containers'][0]['env']
                        if entry['name'] == 'PIPELINE_DEMO_USER_IDS']
            self.assertEqual(restored, [] if prior_value is None else [prior_value])
            handle = json.loads(artifacts['rollback.json'])['handles']['bff']
            self.assertEqual(handle['revision'], value['metadata']['name'])
            self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in commands))
            traffic = next(c for c in commands if c[:3] == ['run', 'services', 'update-traffic'])
            self.assertIn(value['metadata']['name'] + '=100', traffic)


if __name__ == '__main__':
    unittest.main()
