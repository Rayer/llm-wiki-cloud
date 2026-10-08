#!/usr/bin/env python3
"""LWC-319 Production config tests; all provider calls are local stubs."""
import json
import subprocess
import sys
import unittest

import test_auth_config_contract as fixtures
from test_bff_auth_config_contract import production_plan as synthetic_production_plan
from test_auth_config_contract import production, revision

sys.path.insert(0, str(fixtures.ROOT / 'deploy/engine'))
import providers


def candidate(component='auth', enabled=True, plan_override=None):
    value = production(revision(enabled))
    if component == 'bff':
        plan = plan_override or synthetic_production_plan()
        value = json.loads(json.dumps(value).replace('llm-wiki-auth', 'llm-wiki-bff').replace('lwc-auth-prod@', 'lwc-bff-prod@'))
        value['spec']['containers'][0]['env'] = [entry for entry in value['spec']['containers'][0]['env']
            if entry['name'] in ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID', 'ALLOWED_ORIGINS', 'AUTH_SERVICE_URL', 'DEV_JWT', 'JWT_SECRET', 'PIPELINE_COOLDOWN_SECONDS')]
        value['spec']['containers'][0]['env'].insert(0, {'name': 'QUERY_STAGE_CONFIG_PATH',
            'value': plan['query_config']['runtime_path']})
        value['spec']['containers'][0]['env'].append({
            'name': 'PIPELINE_COOLDOWN_SECONDS',
            'value': str(plan['bff']['pipeline_cooldown_seconds']),
        })
        env = value['spec']['containers'][0]['env']
        if plan['bff'].get('profile_runtime_audience'):
            env.extend([
                {'name': 'PROFILE_RUNTIME_AUDIENCE', 'value': plan['bff']['profile_runtime_audience']},
                {'name': 'PROFILE_RUNTIME_SERVICE_ACCOUNT', 'value': plan['bff']['profile_runtime_service_account']},
                {'name': 'TYPESAFE_JEV_API_KEY', 'valueFrom': {'secretKeyRef': {
                    'name': plan['bff']['secret_references']['typesafe_jev_api_key']['name'],
                    'key': plan['bff']['secret_references']['typesafe_jev_api_key']['version'],
                }}},
            ])
        if plan['export_job']['enabled']:
            env.extend([
                {'name': 'EXPORT_JOB_URL', 'value': 'https://run.googleapis.com/v2/projects/{}/locations/{}/jobs/{}:run'.format(
                    plan['gcp']['project_id'], plan['export_job']['location'], plan['export_job']['job_name'])},
                {'name': 'EXPORT_SIGNING_SERVICE_ACCOUNT', 'value': plan['export_job']['signing_service_account']},
            ])
        if plan['bff'].get('pipeline_demo_user_ids'):
            env.append({'name': 'PIPELINE_DEMO_USER_IDS',
                        'value': ','.join(plan['bff']['pipeline_demo_user_ids'])})
    return value


class ProductionConfigContractTests(unittest.TestCase):
    def test_configured_demo_identity_is_exactly_projected_and_read_back(self):
        config = subprocess.run(
            ['go', 'run', './cmd/deploy_config', '--environment', 'production',
             '--config', '../../deploy/environments/production.yaml', '--components', 'auth'],
            cwd=fixtures.ROOT / 'apps/bff', text=True, capture_output=True, check=True)
        plan = json.loads(config.stdout)
        expected = providers.auth_config.desired(plan, 'auth')
        self.assertEqual(expected['env']['AUTH_DEMO_USER_ID'], 'e492f6bdaf1735e12b2de96d')
        self.assertEqual(expected['env']['AUTH_DEMO_USER_EMAIL'], 'demo@llm-wiki.dev')
        self.assertEqual(expected['env']['AUTH_DEMO_USER_ROLE'], 'member')
        image = fixtures.IMAGE
        revision_name = 'llm-wiki-auth-00009-rzw'
        env = [{'name': name, 'value': value} for name, value in expected['env'].items()]
        env.extend({'name': name, 'valueFrom': {'secretKeyRef': reference}}
                   for name, reference in expected['secrets'].items())
        candidate_revision = {
            'metadata': {'name': revision_name, 'annotations': {}},
            'spec': {'serviceAccountName': expected['service_account'],
                     'containers': [{'image': image, 'env': env}]},
            'status': {'imageDigest': image,
                       'conditions': [{'type': 'Ready', 'status': 'True'}]},
        }
        adapter = providers.Providers({'normalized': plan}, fixtures.ROOT)
        self.assertTrue(adapter.service_matches('auth', candidate_revision, image))

        wrong = json.loads(json.dumps(candidate_revision))
        next(item for item in wrong['spec']['containers'][0]['env']
             if item['name'] == 'AUTH_DEMO_USER_EMAIL')['value'] = 'other@example.test'
        self.assertFalse(adapter.service_matches('auth', wrong, image))

    def run_shell(self, value, component='auth', **kwargs):
        if component == 'bff' and 'plan_override' not in kwargs:
            kwargs['plan_override'] = synthetic_production_plan()
        return fixtures.AuthConfigContractTests.run_shell(self, value, environment='production', component=component, **kwargs)

    def test_disabled_export_does_not_remove_production_bff_settings(self):
        plan = synthetic_production_plan()
        plan['export_job']['enabled'] = False
        value = candidate('bff', plan_override=plan)
        retained = [
            {'name': 'EXPORT_JOB_URL', 'value': 'https://legacy.example/jobs:run'},
            {'name': 'EXPORT_SIGNING_SERVICE_ACCOUNT', 'value': 'legacy-signer@example.iam.gserviceaccount.com'},
        ]
        value['spec']['containers'][0]['env'].extend(retained)
        result, commands, _ = self.run_shell(value, 'bff', action='bff_mutate', plan_override=plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
        self.assertNotIn('--remove-env-vars', update)
        self.assertNotIn('EXPORT_JOB_URL=', update[update.index('--update-env-vars') + 1])
        self.assertNotIn('--remove-secrets', update)

    def test_enabled_and_disabled_exact_readback_before_traffic(self):
        for component in ('auth', 'bff'):
            for enabled in (True, False):
                with self.subTest(component=component, enabled=enabled):
                    value = candidate(component, enabled)
                    result, commands, artifacts = self.run_shell(value, component, enabled=enabled, action=component + '_mutate')
                    self.assertEqual(result.returncode, 0, result.stderr)
                    update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
                    self.assertIn('--no-traffic', update)
                    env_arg = update[update.index('--update-env-vars') + 1]
                    self.assertIn('AUTH_SERVICE_URL=https://auth.rayer.idv.tw', env_arg)
                    self.assertNotIn('dev.rayer', env_arg)
                    if component == 'auth':
                        self.assertIn('AUTH_SESSION_ENVIRONMENT=llm-wiki-cloud-prod', env_arg)
                        self.assertIn('AUTH_REFRESH_SESSION_MIGRATION=disabled', env_arg)
                        self.assertIn('AUTH_DEMO_USER_ID=fixture-demo-user-prod', env_arg)
                        if enabled:
                            self.assertIn('GOOGLE_CLIENT_SECRET=google-oauth-client-prod:1', update[update.index('--update-secrets') + 1])
                        else:
                            self.assertIn('--remove-secrets', update)
                    else:
                        self.assertNotIn('AUTH_DEMO_USER_ID=', env_arg)
                    traffic = next(i for i,c in enumerate(commands) if c[:3] == ['run', 'services', 'update-traffic'])
                    self.assertIn(value['metadata']['name'] + '=100', commands[traffic])
                    self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[:traffic]))
                    self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[traffic+1:]))
                    self.assertEqual(json.loads(artifacts['journal.json'])['components'][component]['history'], ['pending', 'accepted'])

    def test_missing_or_wrong_binding_blocks_traffic(self):
        for component in ('auth', 'bff'):
            for entry in candidate(component)['spec']['containers'][0]['env']:
                for missing in (False, True):
                    with self.subTest(component=component, binding=entry['name'], missing=missing):
                        value = candidate(component)
                        entries = value['spec']['containers'][0]['env']
                        target = next(e for e in entries if e['name'] == entry['name'])
                        if missing:
                            entries.remove(target)
                        elif 'value' in target:
                            target['value'] = 'wrong'
                        else:
                            target['valueFrom']['secretKeyRef']['name'] = 'wrong-secret'
                        result, commands, artifacts = self.run_shell(value, component, action=component + '_mutate')
                        self.assertNotEqual(result.returncode, 0)
                        self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
                        self.assertEqual(json.loads(artifacts['journal.json'])['components'][component]['history'], ['pending', 'unknown'])

    def test_retained_revision_restores_absent_config_and_image(self):
        for component in ('auth', 'bff'):
            value = candidate(component, False)
            # Simulate the live baseline, which has no new session/URL/provider settings.
            value['spec']['containers'][0]['env'] = [e for e in value['spec']['containers'][0]['env'] if not e['name'].startswith(('AUTH_', 'GOOGLE_'))]
            for tamper in ('', 'config', 'image', 'unavailable'):
                with self.subTest(component=component, tamper=tamper):
                    edit = {
                        '': '',
                        'config': "sed -i.bak 's/llm-wiki-cloud-prod/llm-wiki-cloud-dev/g' \"$FIXTURE/candidate.json\";",
                        'image': "sed -i.bak 's/sha256:aaaa/sha256:bbbb/g' \"$FIXTURE/candidate.json\";",
                        'unavailable': 'rm "$FIXTURE/candidate.json";',
                    }[tamper]
                    action = component + '_freeze; touch "$FIXTURE/switch-needed"; ' + edit + component + '_rollback'
                    result, commands, artifacts = self.run_shell(value, component, action=action)
                    self.assertEqual(result.returncode == 0, not tamper, result.stderr)
                    handle = json.loads(artifacts['rollback.json'])['handles'][component]
                    self.assertEqual(handle['revision'], value['metadata']['name'])
                    self.assertEqual(handle['image'], value['status']['imageDigest'])
                    self.assertTrue(handle['config_fingerprint'].startswith('sha256:'))
                    self.assertNotIn('env', handle)
                    traffic = [c for c in commands if c[:3] == ['run', 'services', 'update-traffic']]
                    self.assertEqual(len(traffic), 0 if tamper else 1)
                    self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in commands))

    def test_final_config_mismatch_fails_reconciliation(self):
        for component in ('auth', 'bff'):
            result, commands, artifacts = self.run_shell(candidate(component), component, action=component + '_mutate', final_bad=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
            self.assertEqual(json.loads(artifacts['journal.json'])['components'][component]['history'], ['pending', 'unknown'])

    def test_rollback_final_readback_and_reconcile_fail_closed(self):
        for component in ('auth', 'bff'):
            action = component + '_freeze; touch "$FIXTURE/switch-needed"; ' + component + '_rollback'
            result, commands, artifacts = self.run_shell(candidate(component), component, action=action, final_bad=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(json.loads(artifacts['artifacts/rollback/' + component + '.json'])['result'], 'unknown')
            self.assertTrue(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
            value = candidate(component)
            next(e for e in value['spec']['containers'][0]['env'] if e['name'] == 'AUTH_SERVICE_URL')['value'] = 'https://auth.dev.rayer.idv.tw'
            result, commands, artifacts = self.run_shell(value, component, action=component + '_reconcile')
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_receipt_journal_and_revalidation_guards_precede_provider_work(self):
        for component in ('auth', 'bff'):
            for setup in (
                'rm "$ARTIFACT_DIR/dev-images/dev-receipt.json";',
                "sed -i.bak 's/sha256:aaaa/sha256:bbbb/g' \"$ARTIFACT_DIR/dev-images/dev-receipt.json\";",
                "printf '{}' > \"$JOURNAL_PATH\";",
                'revalidate_before_provider() { return 1; };',
                "sed -i.bak 's/production/development/g' \"$PLAN_PATH\";",
            ):
                with self.subTest(component=component, setup=setup):
                    result, commands, _ = self.run_shell(candidate(component), component, action=setup + component + '_mutate')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(commands, [])
            # Candidate readback cannot bypass the repeated CI/durable-artifact checkpoint.
            action = """revalidate_before_provider() {
 if [[ -f "$FIXTURE/revalidated" ]]; then return 1; fi
 touch "$FIXTURE/revalidated"
}; """ + component + '_mutate'
            result, commands, _ = self.run_shell(candidate(component), component, action=action)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(any(c[:3] == ['run', 'services', 'update'] for c in commands))
            self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_identity_literals_and_disabled_residuals_block_traffic(self):
        for component in ('auth', 'bff'):
            for kind in ('account', 'revision', 'image', 'not ready', 'literal', 'duplicate', 'residual', 'secret version', 'secret environment'):
                value = candidate(component)
                entries = value['spec']['containers'][0]['env']
                if kind == 'account': value['spec']['serviceAccountName'] = 'lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com'
                elif kind == 'revision': value['metadata']['name'] += '-wrong'
                elif kind == 'image': value['spec']['containers'][0]['image'] += 'wrong'
                elif kind == 'not ready': value['status']['conditions'][0]['status'] = 'False'
                elif kind == 'literal':
                    target = 'JWT_SECRET' if component == 'auth' else 'TYPESAFE_JEV_API_KEY'
                    entries[entries.index(next(entry for entry in entries if entry['name'] == target))] = {
                        'name': target, 'value': 'CANARY-NEVER-EMIT'}
                elif kind == 'duplicate': entries.append(entries[0])
                elif kind == 'residual': entries.append({'name': 'GOOGLE_UNREVIEWED', 'value': 'residual'})
                elif kind == 'secret version':
                    target = 'GOOGLE_CLIENT_SECRET' if component == 'auth' else 'TYPESAFE_JEV_API_KEY'
                    next(entry for entry in entries if entry['name'] == target)['valueFrom']['secretKeyRef']['key'] = 'latest' if component == 'auth' else '999'
                elif kind == 'secret environment':
                    target = 'GOOGLE_CLIENT_SECRET' if component == 'auth' else 'TYPESAFE_JEV_API_KEY'
                    next(entry for entry in entries if entry['name'] == target)['valueFrom']['secretKeyRef']['name'] = 'google-oauth-client-dev' if component == 'auth' else 'jwt-secret-dev'
                with self.subTest(component=component, kind=kind):
                    result, commands, artifacts = self.run_shell(value, component, action=component + '_mutate', enabled=kind != 'residual')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
                    self.assertNotIn('CANARY-NEVER-EMIT', result.stdout + result.stderr + json.dumps(artifacts))

    def test_production_preflight_requires_enabled_pinned_secret(self):
        for state in ('ENABLED', 'DISABLED', ''):
            result, commands, _ = self.run_shell(candidate(), action='auth_preflight', secret_state=state)
            self.assertEqual(result.returncode == 0, state == 'ENABLED', result.stderr)
            self.assertIn(['secrets', 'versions', 'describe', '1', '--secret', 'google-oauth-client-prod', '--project', 'llm-wiki-cloud', '--format=value(state)', '--quiet'], commands)
            self.assertFalse(any('access' in command for command in commands))


if __name__ == '__main__':
    unittest.main()
