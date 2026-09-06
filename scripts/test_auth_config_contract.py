#!/usr/bin/env python3
"""Offline provider fixtures execute the real Auth mutation and rollback shell."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth@sha256:' + 'a' * 64
SERVICE = 'llm-wiki-auth-dev'
REVISION = SERVICE + '-00042-test'
GOOGLE = {
    'enabled': True, 'client_id': '123456-test.apps.googleusercontent.com',
    'client_secret_reference': 'google-oauth-client-dev', 'client_secret_version': '1',
    'issuer': 'https://accounts.google.com', 'jwks_url': 'https://www.googleapis.com/oauth2/v3/certs',
    'token_url': 'https://oauth2.googleapis.com/token',
    'login_redirect_url': 'https://auth.dev.rayer.idv.tw/api/v1/auth/google/callback',
    'link_redirect_url': 'https://auth.dev.rayer.idv.tw/api/v1/auth/google/link/callback',
    'completion_url': 'https://wiki.dev.rayer.idv.tw/login',
}
ENV = {
    'DEV_JWT': 'false', 'GCP_PROJECT': 'llm-wiki-cloud', 'FIRESTORE_DATABASE_ID': 'llm-wiki-cloud-dev',
    'ALLOWED_HOSTS': 'auth.dev.rayer.idv.tw,auth-dev.rayer.idv.tw',
    'ALLOWED_ORIGINS': 'https://wiki.dev.rayer.idv.tw,https://llm-wiki-frontend-dev.vercel.app,http://localhost:3000',
    'AUTH_SERVICE_URL': 'https://auth.dev.rayer.idv.tw', 'AUTH_SESSION_ENVIRONMENT': 'llm-wiki-cloud-dev',
    'AUTH_REFRESH_SESSION_MIGRATION': 'disabled',
    'GOOGLE_CLIENT_ID': GOOGLE['client_id'], 'GOOGLE_ISSUER': GOOGLE['issuer'],
    'GOOGLE_JWKS_URL': GOOGLE['jwks_url'], 'GOOGLE_TOKEN_URL': GOOGLE['token_url'],
    'GOOGLE_LOGIN_REDIRECT_URL': GOOGLE['login_redirect_url'],
    'GOOGLE_LINK_REDIRECT_URL': GOOGLE['link_redirect_url'], 'GOOGLE_COMPLETION_URL': GOOGLE['completion_url'],
}


def revision(enabled=True):
    env = [{'name': k, 'value': v} for k, v in ENV.items() if enabled or not k.startswith('GOOGLE_')]
    for name, secret, version in [('JWT_SECRET', 'jwt-secret-dev', 'latest')] + (
            [('GOOGLE_CLIENT_SECRET', 'google-oauth-client-dev', '1')] if enabled else []):
        env.append({'name': name, 'valueFrom': {'secretKeyRef': {'name': secret, 'key': version}}})
    return {'metadata': {'name': REVISION}, 'spec': {
        'serviceAccountName': 'lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com',
        'containers': [{'image': IMAGE, 'env': env}]},
        'status': {'imageDigest': IMAGE, 'conditions': [{'type': 'Ready', 'status': 'True'}]}}


class AuthConfigContractTests(unittest.TestCase):
    def run_shell(self, candidate, enabled=True, action='auth_mutate', final_bad=False, secret_state='ENABLED'):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            plan = {'normalized': {'environment': 'development', 'gcp': {
                'project_id': 'llm-wiki-cloud', 'region': 'asia-east1',
                'artifact_registry': IMAGE.split('/llm-wiki-auth@')[0]}, 'auth': {
                    'service_name': SERVICE, 'runtime_service_account': 'lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com',
                    'firestore_database_id': 'llm-wiki-cloud-dev', 'public_domain': 'auth.dev.rayer.idv.tw',
                    'allowed_hosts': ENV['ALLOWED_HOSTS'].split(','), 'allowed_origins': ENV['ALLOWED_ORIGINS'].split(','),
                    'secret_references': {'jwt': 'jwt-secret-dev'}, 'google': GOOGLE if enabled else {'enabled': False}}}}
            (path / 'plan.json').write_text(json.dumps(plan))
            (path / 'candidate.json').write_text(json.dumps(candidate))
            (path / 'journal.json').write_text('{"components":{"auth":{}}}')
            provider = path / 'gcloud'
            provider.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
p = Path(os.environ['FIXTURE'])
a = sys.argv[1:]
with (p/'commands').open('a') as f: f.write(json.dumps(a)+'\\n')
if a[:3] == ['secrets','versions','describe']:
 print(os.environ['SECRET_STATE'])
elif a[:2] == ['secrets','get-iam-policy']:
 print(json.dumps({'bindings':[{'role':'roles/secretmanager.secretAccessor','members':['serviceAccount:lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com']}]}))
elif a[:3] == ['run','services','get-iam-policy']:
 print(json.dumps({'bindings':[{'role':'roles/run.invoker','members':['allUsers']}]}))
elif a[:2] == ['secrets','describe'] or a[:3] in (['iam','service-accounts','describe'], ['firestore','databases','describe']):
 print('{}')
elif a[:3] == ['run','services','update']:
 print(os.environ['REVISION'])
elif a[:3] == ['run','services','update-traffic']:
 (p/'traffic').touch()
elif a[:3] == ['run','services','describe']:
 active = os.environ['REVISION']
 if (p/'switch-needed').exists() and not (p/'traffic').exists(): active = 'llm-wiki-auth-dev-00043-other'
 print(json.dumps({'status':{'traffic':[{'revisionName':active,'percent':100}]}}))
elif a[:3] == ['run','revisions','describe']:
 r=json.loads((p/'candidate.json').read_text())
 if os.environ.get('FINAL_BAD') == '1' and (p/'traffic').exists():
  r['spec']['containers'][0]['env'][0]['value']='wrong-project'
 print(json.dumps(r))
else: sys.exit(2)
''')
            provider.chmod(0o755)
            timeout = path / 'timeout'
            timeout.write_text('#!/usr/bin/env bash\nshift 3\nexec "$@"\n')
            timeout.chmod(0o755)
            script = '''source "$AUTH_SOURCE" help
journal_init() { :; }
journal_pending() { :; }
mutation_accepted() { :; }
journal_transition() { printf '%s\\n' "$*" >> "$FIXTURE/journal-events"; }
revalidate_before_provider() { printf 'revalidate\\n' >> "$FIXTURE/events"; }
auth_build_image() { printf '%s\\n' "$IMAGE"; }
sleep() { :; }
'''
            env = {**os.environ, 'ROOT': str(ROOT), 'AUTH_SOURCE': os.environ.get('LWC318_AUTH_SOURCE', str(ROOT / 'deploy/components/auth.sh')),
                   'PATH': str(path) + ':' + os.environ['PATH'], 'PLAN_PATH': str(path / 'plan.json'),
                   'JOURNAL_PATH': str(path / 'journal.json'), 'ARTIFACT_DIR': str(path / 'artifacts'),
                   'ROLLBACK_PATH': str(path / 'rollback.json'), 'ENVIRONMENT': 'development',
                   'SOURCE_SHA': 'b' * 40, 'SOURCE_REF': 'develop', 'IMAGE': IMAGE,
                   'FIXTURE': str(path), 'REVISION': REVISION, 'FINAL_BAD': str(int(final_bad)), 'SECRET_STATE': secret_state}
            result = subprocess.run(['bash', '-c', script + '\n' + action], env=env, text=True, capture_output=True)
            commands = [json.loads(l) for l in (path / 'commands').read_text().splitlines()] if (path / 'commands').exists() else []
            artifacts = {str(p.relative_to(path)): p.read_text() for p in path.rglob('*.json') if p.name not in ('candidate.json', 'plan.json')}
            return result, commands, artifacts

    def test_exact_config_verified_before_explicit_traffic_and_final_readback(self):
        for enabled in (True, False):
            with self.subTest(enabled=enabled):
                result, commands, _ = self.run_shell(revision(enabled), enabled)
                self.assertEqual(result.returncode, 0, result.stderr)
                update = next(c for c in commands if c[:3] == ['run', 'services', 'update'])
                self.assertIn('--no-traffic', update)
                env_arg = update[update.index('--update-env-vars') + 1]
                for k, v in ENV.items():
                    if enabled or not k.startswith('GOOGLE_'):
                        self.assertIn(k + '=' + v, env_arg)
                if enabled:
                    self.assertIn('GOOGLE_CLIENT_SECRET=google-oauth-client-dev:1', update[update.index('--update-secrets') + 1])
                else:
                    self.assertIn('--remove-secrets', update)
                    self.assertIn('GOOGLE_CLIENT_ID', update[update.index('--remove-env-vars') + 1])
                traffic = next(i for i, c in enumerate(commands) if c[:3] == ['run', 'services', 'update-traffic'])
                self.assertIn(REVISION + '=100', commands[traffic])
                self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[:traffic]))
                self.assertTrue(any(c[:3] == ['run', 'revisions', 'describe'] for c in commands[traffic + 1:]))

    def test_every_missing_or_mismatched_effective_binding_blocks_traffic(self):
        for entry in revision()['spec']['containers'][0]['env']:
            for missing in (True, False):
                with self.subTest(name=entry['name'], missing=missing):
                    candidate = revision()
                    entries = candidate['spec']['containers'][0]['env']
                    target = next(e for e in entries if e['name'] == entry['name'])
                    if missing:
                        entries.remove(target)
                    elif 'value' in target:
                        target['value'] += '-wrong'
                    else:
                        target['valueFrom']['secretKeyRef']['key'] = '999'
                    result, commands, _ = self.run_shell(candidate)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_secret_literals_duplicates_identity_and_disabled_residuals_block_traffic(self):
        cases = []
        literal = revision(); literal['spec']['containers'][0]['env'][-1] = {'name': 'GOOGLE_CLIENT_SECRET', 'value': 'CANARY-NEVER-EMIT'}; cases.append((literal, True))
        duplicate = revision(); duplicate['spec']['containers'][0]['env'].append({'name': 'GOOGLE_CLIENT_ID', 'value': 'duplicate'}); cases.append((duplicate, True))
        identity = revision(); identity['spec']['serviceAccountName'] = 'wrong'; cases.append((identity, True))
        cases.append((revision(), False))
        for candidate, enabled in cases:
            result, commands, artifacts = self.run_shell(candidate, enabled)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))
            self.assertNotIn('CANARY-NEVER-EMIT', result.stdout + result.stderr + json.dumps(artifacts))

    def test_preflight_requires_enabled_version_without_reading_secret_payload(self):
        for state in ('ENABLED', 'DISABLED', ''):
            result, commands, _ = self.run_shell(revision(), action='auth_preflight', secret_state=state)
            self.assertEqual(result.returncode == 0, state == 'ENABLED', result.stderr)
            self.assertIn(['secrets', 'versions', 'describe', '1', '--secret', 'google-oauth-client-dev', '--project', 'llm-wiki-cloud', '--format=value(state)', '--quiet'], commands)
            self.assertFalse(any('access' in command for command in commands))

    def test_wrong_secret_name_blocks_traffic(self):
        candidate = revision()
        candidate['spec']['containers'][0]['env'][-1]['valueFrom']['secretKeyRef']['name'] = 'google-oauth-client-prod'
        result, commands, _ = self.run_shell(candidate)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_secret_alias_resolves_to_exact_project_and_name(self):
        for target, valid in (('projects/llm-wiki-cloud/secrets/google-oauth-client-dev', True),
                              ('projects/other-project/secrets/google-oauth-client-dev', False),
                              ('projects/llm-wiki-cloud/secrets/google-oauth-client-prod', False)):
            candidate = revision()
            candidate['metadata']['annotations'] = {'run.googleapis.com/secrets': 'google-alias:' + target}
            candidate['spec']['containers'][0]['env'][-1]['valueFrom']['secretKeyRef']['name'] = 'google-alias'
            result, commands, _ = self.run_shell(candidate)
            self.assertEqual(result.returncode == 0, valid, result.stderr)
            self.assertEqual(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands), valid)

    def test_final_mismatch_is_failure(self):
        result, commands, _ = self.run_shell(revision(), final_bad=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))

    def test_freeze_retains_sanitized_revision_and_rollback_checks_config(self):
        action = 'auth_freeze; touch "$FIXTURE/switch-needed"; auth_rollback'
        result, commands, artifacts = self.run_shell(revision(False), action=action)
        self.assertEqual(result.returncode, 0, result.stderr)
        handle = json.loads(artifacts['rollback.json'])['handles']['auth']
        self.assertEqual(handle['revision'], REVISION)
        self.assertTrue(handle['config_fingerprint'].startswith('sha256:'))
        self.assertNotIn('env', handle)
        traffic = [c for c in commands if c[:3] == ['run', 'services', 'update-traffic']]
        self.assertEqual(len(traffic), 1)
        self.assertIn(REVISION + '=100', traffic[0])
        self.assertFalse(any(c[:3] == ['run', 'services', 'update'] for c in commands))
        result, commands, _ = self.run_shell(revision(False), action='auth_freeze; sed -i.bak "s/sha256:/sha256:0/" "$ROLLBACK_PATH"; auth_rollback')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c[:3] == ['run', 'services', 'update-traffic'] for c in commands))


if __name__ == '__main__':
    unittest.main()
