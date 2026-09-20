#!/usr/bin/env python3
"""LWC-332: real YAML plans and BFF shell/Python path, with offline providers."""
import copy
import json
import unittest

import test_auth_config_contract as fixtures
from test_production_auth_config_contract import candidate as production_candidate


def candidate(environment):
    value = production_candidate('bff')
    plan = fixtures.bff_plan(environment)
    value['metadata']['name'] = plan['bff']['service_name'] + '-00042-test'
    value['spec']['serviceAccountName'] = plan['bff']['runtime_service_account']
    if environment == 'development':
        # Separately approved DEV settings must survive, even when unlike YAML.
        entries = value['spec']['containers'][0]['env']
        for entry in entries:
            if entry['name'] != 'QUERY_STAGE_CONFIG_PATH' and 'value' in entry:
                entry['value'] = 'preserved-dev-value'
        entries[-1]['valueFrom']['secretKeyRef'] = {'name': 'jwt-secret-dev', 'key': '7'}
    value['spec']['containers'][0]['env'] += [
        {'name': 'BUCKET', 'value': 'preserved-bucket'},
        {'name': 'DEEPSEEK_API_KEY', 'valueFrom': {'secretKeyRef': {'name': 'deepseek-apikey', 'key': '3'}}},
    ]
    value['metadata']['annotations'] = {'run.googleapis.com/vpc-access-egress': 'private-ranges-only'}
    return value


class BFFQueryConfigTests(unittest.TestCase):
    def run_shell(self, value, environment, **kwargs):
        return fixtures.AuthConfigContractTests.run_shell(self, value, environment=environment,
                                                        component='bff', **kwargs)

    def test_yaml_selection_delivered_and_exact_revision_verified_before_traffic(self):
        for environment in ('development', 'production'):
            with self.subTest(environment=environment):
                value = candidate(environment)
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
                    self.assertEqual(env_arg, '^|^QUERY_STAGE_CONFIG_PATH=' + path)
                    self.assertNotIn('--update-secrets', update)
                else:
                    self.assertIn('AUTH_SERVICE_URL=https://auth.rayer.idv.tw', env_arg)
                    self.assertIn('JWT_SECRET=jwt-secret-prod:latest', update)
                self.assertFalse(any(arg.startswith(('--remove-', '--clear-', '--set-', '--service-account', '--network', '--subnet')) for arg in update))
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
                    self.assertEqual(update[update.index('--update-env-vars') + 1],
                                     '^|^QUERY_STAGE_CONFIG_PATH=' + plan['query_config']['runtime_path'])
                    self.assertNotIn('--update-secrets', update)
                    self.assertTrue(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
                    if final_bad:
                        self.assertEqual(json.loads(artifacts['journal.json'])['components']['bff']['state'], 'unknown')

    def test_rollback_retains_exact_prior_config_without_redeploy_or_values(self):
        for environment in ('development', 'production'):
            for tamper in ('', 'query', 'other env', 'secret', 'network'):
                value = candidate(environment)
                value['spec']['containers'][0]['env'][0]['value'] = '/app/configs/query/prior-image-only.json'
                value['spec']['containers'][0]['env'].append({'name': 'OPAQUE_SETTING', 'value': 'CANARY-NEVER-EMIT'})
                edit = {
                    '': '', 'query': "sed -i.bak 's/prior-image-only/changed/g' \"$FIXTURE/candidate.json\";",
                    'other env': "sed -i.bak 's/preserved-bucket/changed/g' \"$FIXTURE/candidate.json\";",
                    'secret': "sed -i.bak 's/deepseek-apikey/changed/g' \"$FIXTURE/candidate.json\";",
                    'network': "sed -i.bak 's/private-ranges-only/all-traffic/g' \"$FIXTURE/candidate.json\";",
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


if __name__ == '__main__':
    unittest.main()
