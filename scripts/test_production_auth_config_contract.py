#!/usr/bin/env python3
"""LWC-319 Production config tests; all provider calls are local stubs."""
import json
import unittest

import test_auth_config_contract as fixtures
from test_auth_config_contract import production, revision


def candidate(component='auth', enabled=True):
    value = production(revision(enabled))
    if component == 'bff':
        value = json.loads(json.dumps(value).replace('llm-wiki-auth', 'llm-wiki-bff').replace('lwc-auth-prod@', 'lwc-bff-prod@'))
        value['spec']['containers'][0]['env'] = [entry for entry in value['spec']['containers'][0]['env']
            if entry['name'] in ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID', 'ALLOWED_ORIGINS', 'AUTH_SERVICE_URL', 'DEV_JWT', 'JWT_SECRET')]
        value['spec']['containers'][0]['env'].insert(0, {'name': 'QUERY_STAGE_CONFIG_PATH',
            'value': fixtures.bff_plan('production')['query_config']['runtime_path']})
    return value


class ProductionConfigContractTests(unittest.TestCase):
    def run_shell(self, value, component='auth', **kwargs):
        return fixtures.AuthConfigContractTests.run_shell(self, value, environment='production', component=component, **kwargs)

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
                        if enabled:
                            self.assertIn('GOOGLE_CLIENT_SECRET=google-oauth-client-prod:1', update[update.index('--update-secrets') + 1])
                        else:
                            self.assertIn('--remove-secrets', update)
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
                elif kind == 'literal': entries[-1] = {'name': 'JWT_SECRET', 'value': 'CANARY-NEVER-EMIT'}
                elif kind == 'duplicate': entries.append(entries[0])
                elif kind == 'residual': entries.append({'name': 'GOOGLE_UNREVIEWED', 'value': 'residual'})
                elif kind == 'secret version': entries[-1]['valueFrom']['secretKeyRef']['key'] = 'latest' if component == 'auth' else '999'
                elif kind == 'secret environment': entries[-1]['valueFrom']['secretKeyRef']['name'] = 'google-oauth-client-dev' if component == 'auth' else 'jwt-secret-dev'
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
