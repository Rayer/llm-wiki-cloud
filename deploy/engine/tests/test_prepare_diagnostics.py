"""Causal, offline regression for Auth build stage and exit-code metadata."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
import engine
import providers
from support import Breakpoint, ROOT as SUPPORT_ROOT, digest, read, structured_cause, write

ORIGINAL_SUBPROCESS_RUN = subprocess.run


class StructuredCauseContract(unittest.TestCase):
    def test_engine_main_admission_exit_keeps_cause_when_engine_was_not_created(self):
        with tempfile.TemporaryDirectory() as work:
            stdout = io.StringIO()
            argv = ['engine.py', 'prepare', '--directory', str(Path(work) / 'release'),
                    '--environment', 'development', '--source', 'a' * 40,
                    '--tag', 'offline-diagnostic', '--components', 'auth']
            with patch.object(sys, 'argv', argv), \
                    patch.object(engine, 'admit', side_effect=KeyError('target-config')), \
                    contextlib.redirect_stdout(stdout):
                self.assertEqual(engine.main(), 1)
            result = json.loads(stdout.getvalue())
            self.assertEqual(result['reason'], 'invalid-or-unreadable-input')
            self.assertEqual(result['cause']['exception_type'], 'KeyError')
            self.assertEqual(result['cause']['stage'], 'unknown')
            self.assertEqual(result['cause']['code'], 'required-field-missing')
            self.assertEqual(result['cause']['message'], "'target-config'")

    def test_code_override_is_allowlisted_and_unknown_cause_is_not_inspected(self):
        missing_tool = FileNotFoundError('vercel')
        default = structured_cause(missing_tool, 'frontend-vercel-pull')
        self.assertEqual((default['exception_type'], default['code']),
                         ('FileNotFoundError', 'local-input-unreadable'))

        explicit = structured_cause(missing_tool, 'frontend-vercel-pull', code='tool-unavailable')
        self.assertEqual(explicit['code'], 'tool-unavailable')
        rejected = structured_cause(missing_tool, ['not', 'a', 'stage'], code=['not', 'a', 'code'])
        self.assertEqual((rejected['stage'], rejected['code']),
                         ('unknown', 'local-input-unreadable'))

        class UnknownCause(Exception):
            def __str__(self):
                raise AssertionError('unknown exception string must not be evaluated')

        unknown = structured_cause(UnknownCause(), stage='not-a-fixed-stage', code='invalid-json')
        self.assertEqual(unknown, {
            'exception_type': 'unknown', 'exception_type_omitted': True,
            'stage': 'unknown', 'code': 'unclassified-input-error',
            'message': None, 'message_truncated': False, 'message_omitted': True})

    def test_python_input_messages_keep_location_and_omit_json_source_text(self):
        secret = 'TEST_ONLY_VERCEL_TOKEN_SENTINEL'
        with self.assertRaises(json.JSONDecodeError) as caught:
            json.loads('{"credential":"' + secret + '",oops')
        cause = structured_cause(caught.exception, 'frontend-project-readback')
        self.assertEqual((cause['exception_type'], cause['code']), ('JSONDecodeError', 'invalid-json'))
        self.assertIn('line 1 column', cause['message'])
        self.assertNotIn(secret, cause['message'])
        self.assertNotIn('credential', cause['message'])

    def test_message_is_bounded_selectively_redacted_and_marks_truncation(self):
        token = 'TEST_ONLY_VERCEL_TOKEN_SENTINEL'
        message = (f'CLI could not link prj_TestProject at https://api.test.invalid; '
                   f'Authorization: Basic {token}\n--token="{token}"; '
                   '--token="different-secret"; VERCEL_TOKEN=' + token + '; ' + 'x' * 600)
        cause = structured_cause(ChildProcessError(), 'frontend-vercel-pull',
                                 code='child-command-failed', message=message,
                                 sensitive_values=(token,))
        self.assertEqual(cause['exception_type'], 'ChildProcessError')
        self.assertEqual(cause['code'], 'child-command-failed')
        self.assertTrue(cause['message_truncated'])
        self.assertEqual(len(cause['message']), 512)
        self.assertIn('prj_TestProject', cause['message'])
        self.assertIn('https://api.test.invalid', cause['message'])
        self.assertNotIn(token, cause['message'])
        self.assertNotIn('different-secret', cause['message'])
        self.assertIn('Authorization: [REDACTED]', cause['message'])
        self.assertEqual(cause['message'].count('--token="[REDACTED]"'), 2)


class AuthPrepareDiagnostics(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.directory = self.root / 'release'
        self.directory.mkdir()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.gcloud = self.bin / 'gcloud'
        self.gcloud.write_text('''#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
if args[:2] == ["projects", "describe"]:
    print(json.dumps({"projectId":args[2],"projectNumber":"580854833715"}))
    raise SystemExit(0)
if args[:2] == ["builds", "submit"]:
    exit_code=int(os.environ.get("FAKE_BUILD_EXIT", "0"))
    if exit_code:
        print(os.environ.get("TEST_ONLY_RAW_PROVIDER_SECRET", ""), file=sys.stderr)
        raise SystemExit(exit_code)
    build_id="12345678-1234-4234-8234-123456789abc"
    print(json.dumps({"id":build_id,"projectId":"llm-wiki-cloud",
        "name":"projects/580854833715/locations/global/builds/"+build_id,
        "status":"QUEUED"}))
    raise SystemExit(0)
if args[:2] == ["builds", "describe"]:
    build_id=args[2]
    print(json.dumps({"id":build_id,"projectId":"llm-wiki-cloud",
        "name":"projects/580854833715/locations/global/builds/"+build_id,
        "location":"global","status":os.environ.get("FAKE_BUILD_STATUS", "SUCCESS")}))
    raise SystemExit(0)
if args[:4] == ["artifacts", "docker", "images", "describe"]:
    reference=args[4]
    key="FAKE_VALIDATE_EXIT" if "@" in reference else "FAKE_LOOKUP_EXIT"
    output_key="FAKE_VALIDATE_DIGEST" if "@" in reference else "FAKE_LOOKUP_DIGEST"
    print(os.environ.get("TEST_ONLY_RAW_PROVIDER_SECRET", ""), file=sys.stderr)
    print(os.environ.get(output_key, "sha256:" + "a" * 64))
    raise SystemExit(int(os.environ.get(key, "0")))
raise SystemExit(91)
''')
        self.gcloud.chmod(0o755)
        fake_go = self.bin / 'go'
        fake_go.write_text('#!/bin/sh\nprintf "1.0.0\\n"\n')
        fake_go.chmod(0o755)
        fake_timeout = self.bin / 'timeout'
        fake_timeout.write_text('''#!/usr/bin/env python3
import os, sys
args=sys.argv[1:]
while args and args[0].startswith("--"):
    args.pop(0)
if args:
    args.pop(0)  # fixed timeout duration
os.execvpe(args[0], args, os.environ)
''')
        fake_timeout.chmod(0o755)
        artifact_registry = 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images'
        self.normalized = {'environment': 'development',
                           'gcp': {'artifact_registry': artifact_registry,
                                   'project_id': 'llm-wiki-cloud'}}
        self.plan = {'source': 'a' * 40, 'branch': 'develop', 'tag': 'diagnostic-fixture',
                     'normalized': self.normalized, 'selected': ['auth'],
                     'identities': {'auth': {'profile': 'test', 'inputs': 'test', 'files': []}}}
        self.plan['engine_content'] = engine.engine_fingerprint()
        self.plan['id'] = digest(self.plan)
        write(self.directory / 'plan.json', self.plan)
        self.provider = providers.Providers(self.plan, self.directory)
        self.instance = engine.Engine(self.directory)
        minimal_env = {
            'PATH': str(self.bin) + os.pathsep + os.environ.get('PATH', '/usr/bin:/bin'),
            'HOME': str(self.root),
            'TMPDIR': str(self.root),
            'TEST_ONLY_RAW_PROVIDER_SECRET': 'TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL',
            'FAKE_LOOKUP_DIGEST': 'sha256:' + 'a' * 64,
            'FAKE_VALIDATE_DIGEST': 'sha256:' + 'a' * 64,
        }
        self.env = patch.dict(os.environ, minimal_env, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)

    def failure(self, **env):
        with patch.dict(os.environ, env):
            with self.assertRaises(Breakpoint) as caught:
                self.instance.prepare()
        return caught.exception

    def render_engine_result(self, error):
        instance = engine.Engine(self.directory)
        instance.component = 'auth'
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            instance.result(error)
        return stdout.getvalue(), json.loads((self.directory / 'result.json').read_text())

    def test_build_submit_stage_keeps_reason_and_redacts_child_output(self):
        exc = self.failure(FAKE_BUILD_EXIT='9')
        self.assertEqual((exc.reason, exc.status, exc.mutation, exc.action),
                         ('build-submit-outcome-unknown', 'unknown', False, 'reconcile-before-replay'))
        self.assertEqual((exc.stage, exc.exit_code, exc.timeout_class), ('build-submit', 9, None))
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', str(exc))
        stdout, result = self.render_engine_result(exc)
        self.assertEqual(result['failure_diagnostic'],
                         {'stage': 'build-submit', 'exit_code': 9, 'timeout_class': None})
        self.assertEqual(result['allowed_next_action'], 'reconcile-before-replay')
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', stdout + json.dumps(result))

    def test_known_permission_classification_and_safe_action_are_preserved(self):
        marker = 'PERMISSION_DENIED TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL'
        exc = self.failure(FAKE_BUILD_EXIT='9', TEST_ONLY_RAW_PROVIDER_SECRET=marker)
        self.assertEqual((exc.reason, exc.action),
                         ('permission-denied', 'restore-existing-principal-permission'))
        self.assertEqual((exc.stage, exc.exit_code), ('build-submit', 9))
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', str(exc))

    def test_digest_lookup_and_digest_validation_are_distinct_stages(self):
        lookup = self.failure(FAKE_LOOKUP_EXIT='7')
        self.assertEqual((lookup.reason, lookup.stage, lookup.exit_code),
                         ('command-failed', 'tag-digest-resolve', 7))
        malformed = self.failure(FAKE_LOOKUP_DIGEST='TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL')
        self.assertEqual((malformed.reason, malformed.stage, malformed.exit_code),
                         ('command-failed', 'digest-validate', 0))
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', str(lookup) + str(malformed))

    def test_immutable_validation_failure_is_typed_without_raw_output(self):
        mismatch = self.failure(FAKE_VALIDATE_DIGEST='sha256:' + 'b' * 64)
        self.assertEqual((mismatch.reason, mismatch.status, mismatch.mutation, mismatch.action),
                         ('artifact-unusable', 'failed', False, 'correct-input-and-resume'))
        self.assertEqual((mismatch.stage, mismatch.exit_code), ('digest-validate', 0))
        # Provider stderr cannot impersonate the trusted auth.sh typed marker.
        spoofed = 'LWC_ENGINE_FAILURE stage=build-submit exit_code=77 permission=1'
        unreadable = self.failure(FAKE_VALIDATE_EXIT='8', TEST_ONLY_RAW_PROVIDER_SECRET=spoofed)
        self.assertEqual((unreadable.reason, unreadable.stage, unreadable.exit_code),
                         ('command-failed', 'digest-validate', 8))
        self.assertNotIn('LWC_ENGINE_FAILURE', str(unreadable))

    def test_engine_result_exposes_only_allowlisted_failure_metadata(self):
        directory = self.root / 'result'
        directory.mkdir()
        result_plan = dict(self.plan)
        result_plan['tag'] = 'local-result-test'
        result_plan['id'] = digest({k: v for k, v in result_plan.items() if k != 'id'})
        write(directory / 'plan.json', result_plan)
        instance = engine.Engine(directory)
        instance.component = 'auth'
        error = Breakpoint('command-failed', stage='build-submit', exit_code=9)
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            instance.result(error)
        result = json.loads((directory / 'result.json').read_text())
        self.assertNotIn('frontend_prepare_diagnostic', result)
        self.assertEqual(result['failure_diagnostic'], {
            'stage': 'build-submit', 'exit_code': 9, 'timeout_class': None})
        self.assertEqual(result['reason'], 'command-failed')
        self.assertEqual(result['allowed_next_action'], 'correct-input-and-resume')
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', stdout.getvalue())
        self.assertNotIn('TEST_ONLY_RAW_PROVIDER_SECRET_SENTINEL', json.dumps(result))


class FrontendPrepareDiagnostics(unittest.TestCase):
    stages = (
        ('frontend-project-readback', 'curl', 'repo', 30),
        ('frontend-npm-ci', 'npm', 'frontend', 600),
        ('frontend-vercel-pull', 'vercel-pull', 'fake-root', 30),
        ('frontend-vercel-build', 'vercel-build', 'fake-root', 900),
    )
    token = 'TEST_ONLY_VERCEL_TOKEN_SENTINEL'
    team = 'team_TestTeam123'
    project = 'prj_TestProject123'
    output_sentinel = 'TEST_ONLY_CHILD_OUTPUT_SENTINEL'

    def make_engine(self, work):
        work = Path(work)
        fake_root = work / 'repo'
        (fake_root / 'apps/frontend').mkdir(parents=True)
        directory = work / 'attempt'
        directory.mkdir()
        identities = {'auth': {'profile': 'fake-auth', 'inputs': 'a' * 64, 'files': []},
                      'frontend': {'profile': 'fake-frontend', 'inputs': 'b' * 64, 'files': []}}
        frontend = {'project_name': 'llm-wiki-frontend-test', 'team_slug': 'test-team',
                    'repository': 'Rayer/llm-wiki-cloud', 'root_directory': 'apps/frontend',
                    'api_url': 'https://api.test.invalid', 'auth_url': 'https://auth.test.invalid'}
        plan = {'schema': 2, 'source': 'c' * 40, 'branch': 'develop', 'tag': 'offline-diagnostic',
                'normalized': {'environment': 'development',
                               'gcp': {'project_id': 'llm-wiki-cloud',
                                       'artifact_registry': 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images'},
                               'frontend': frontend},
                'selected': ['auth', 'frontend'], 'identities': identities,
                'engine_content': 'd' * 64}
        plan['id'] = digest(plan)
        write(directory / 'plan.json', plan)
        auth_receipt = {'schema': 2, 'component': 'auth', 'identity': identities['auth'],
                        'build_sha': plan['source'],
                        'artifact': {'image': 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/'
                                              'llm-wiki-auth@sha256:' + 'a' * 64},
                        'target_config': None}
        write(directory / 'receipts/auth.json', auth_receipt)
        auth_receipt_bytes = (directory / 'receipts/auth.json').read_bytes()
        return engine.Engine(directory), directory, fake_root, auth_receipt_bytes

    @staticmethod
    def child_operation(args):
        if args[:4] == ['gcloud', 'artifacts', 'docker', 'images']:
            return 'auth-digest'
        if args and args[0] == 'curl':
            return 'frontend-project-readback'
        if args[:2] == ['npm', 'ci']:
            return 'frontend-npm-ci'
        if args[:2] == ['vercel', 'pull']:
            return 'frontend-vercel-pull'
        if args[:2] == ['vercel', 'build']:
            return 'frontend-vercel-build'
        raise AssertionError('unexpected subprocess in Frontend prepare fixture')

    def assert_failure_case(self, work, failing_stage, failure_kind):
        instance, directory, fake_root, auth_receipt_bytes = self.make_engine(work)
        trace = []
        operations = [stage for stage, _, _, _ in self.stages]
        raw_failure_message = (
            f'{self.output_sentinel}: project={self.project} team={self.team} '
            f'url=https://api.test.invalid\nAuthorization: Bearer {self.token}\n'
            f'--token="{self.token}"; --token="different-secret"; VERCEL_TOKEN={self.token}')

        def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
            operation = self.child_operation(args)
            trace.append((operation, str(cwd), timeout))
            if operation == 'auth-digest':
                return subprocess.CompletedProcess(args, 0, stdout='sha256:' + 'a' * 64,
                                                   stderr=self.output_sentinel)
            if operation == failing_stage:
                if failure_kind == 'timeout':
                    raise subprocess.TimeoutExpired(args, timeout, output='TEST_ONLY_RAW_STDOUT_SENTINEL',
                                                    stderr=raw_failure_message)
                if failure_kind == 'missing':
                    raise FileNotFoundError('vercel')
                return subprocess.CompletedProcess(args, 23, stdout='TEST_ONLY_RAW_STDOUT_SENTINEL',
                                                   stderr=raw_failure_message)
            if operation == 'frontend-project-readback':
                response = {'id': self.project, 'name': 'llm-wiki-frontend-test',
                            'accountId': self.team, 'rootDirectory': 'apps/frontend',
                            'link': {'org': 'Rayer', 'repo': 'llm-wiki-cloud'}}
                return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response), stderr='')
            return subprocess.CompletedProcess(args, 0, stdout='', stderr='')

        env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(work),
               'TMPDIR': str(work), 'VERCEL_TOKEN': self.token,
               'VERCEL_TEAM_ID': self.team, 'VERCEL_PROJECT_ID': self.project}
        with patch.dict(os.environ, env, clear=True), patch('providers.ROOT', fake_root), \
                patch('support.subprocess.run', side_effect=fake_subprocess), \
                patch.object(instance, 'barrier', side_effect=AssertionError('ready barrier reached')), \
                patch.object(instance, 'runtime_guard', side_effect=AssertionError('runtime path reached')):
            with self.assertRaises(Breakpoint) as caught:
                instance.prepare()
            error = caught.exception
            self.assertEqual(error.stage, failing_stage)
            self.assertEqual(error.status, 'failed')
            self.assertFalse(error.mutation)
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                instance.result(error)

        expected_operations = ['auth-digest', *operations[:operations.index(failing_stage) + 1]]
        self.assertEqual([operation for operation, _, _ in trace], expected_operations)
        for operation, cwd, timeout in trace:
            if operation == 'auth-digest':
                self.assertEqual(cwd, str(SUPPORT_ROOT))
                self.assertEqual(timeout, 30)
            else:
                expected = next(row for row in self.stages if row[0] == operation)
                expected_cwd = {'repo': SUPPORT_ROOT,
                                'frontend': fake_root / 'apps/frontend',
                                'fake-root': fake_root}[expected[2]]
                self.assertEqual(cwd, str(expected_cwd))
                self.assertEqual(timeout, expected[3])
        self.assertEqual((error.exit_code, error.timeout_class),
                         (23, None) if failure_kind == 'exit' else
                         (None, 'subprocess-timeout') if failure_kind == 'timeout' else
                         (None, 'tool-unavailable'))
        self.assertEqual((directory / 'receipts/auth.json').read_bytes(), auth_receipt_bytes)
        self.assertFalse((directory / 'receipts/frontend.json').exists())
        self.assertFalse((directory / 'frontend.tgz').exists())
        self.assertEqual(instance.state['status'], 'prepared')
        self.assertEqual(instance.state['components'], {})
        self.assertFalse(any(operation in ('auth-build', 'auth-submit') for operation, _, _ in trace))

        result = json.loads((directory / 'result.json').read_text())
        diagnostic = result['frontend_prepare_diagnostic']
        self.assertEqual(set(diagnostic['commands']), {stage for stage, _, _, _ in self.stages})
        expected_command_state = {
            stage: ({'status': 'timeout', 'exit_code': None,
                     'timeout_class': 'subprocess-timeout'}
                    if stage == failing_stage and failure_kind == 'timeout' else
                    {'status': 'typed-fail', 'exit_code': 23, 'timeout_class': None}
                    if stage == failing_stage and failure_kind == 'exit' else
                    {'status': 'typed-fail', 'exit_code': None, 'timeout_class': 'tool-unavailable'}
                    if stage == failing_stage else
                    {'status': 'exit0', 'exit_code': 0, 'timeout_class': None}
                    if operations.index(stage) < operations.index(failing_stage) else
                    {'status': 'not-run', 'exit_code': None, 'timeout_class': None})
            for stage, _, _, _ in self.stages
        }
        self.assertEqual(diagnostic['commands'], expected_command_state)
        self.assertIn(diagnostic['linked_root'], ('matches-config', 'empty', 'missing', 'other'))
        self.assertIn(diagnostic['settings_root'], ('matches-config', 'empty', 'missing', 'other'))
        self.assertEqual(set(diagnostic['context_presence']), {
            'VERCEL_ORG_ID', 'VERCEL_PROJECT_ID', 'VERCEL_TEAM_ID', 'NOW_ORG_ID', 'NOW_PROJECT_ID'})
        self.assertEqual(set(diagnostic['outputs']), {'repo_root', 'configured_root'})
        for observation in diagnostic['outputs'].values():
            self.assertEqual(set(observation), {'exists', 'static_config_valid', 'target_equal'})
            self.assertTrue(all(type(value) is bool for value in observation.values()))
        serialized = json.dumps(diagnostic)
        for secret_or_value in (self.token, self.team, self.project, 'TEST_ONLY_RAW_STDOUT_SENTINEL'):
            self.assertNotIn(secret_or_value, serialized)
        self.assertEqual(result['failure_diagnostic'], {
            'stage': failing_stage,
            'exit_code': 23 if failure_kind == 'exit' else None,
            'timeout_class': None if failure_kind == 'exit' else
                             'subprocess-timeout' if failure_kind == 'timeout' else 'tool-unavailable'})
        self.assertEqual(set(result['failure_diagnostic']), {'stage', 'exit_code', 'timeout_class'})
        self.assertEqual((result['component'], result['status'], result['mutation_may_have_happened']),
                         ('frontend', 'failed', False))
        cause = result['cause']
        self.assertEqual(cause['exception_type'],
                         'ChildProcessError' if failure_kind == 'exit' else
                         'TimeoutExpired' if failure_kind == 'timeout' else 'FileNotFoundError')
        self.assertEqual(cause['stage'], failing_stage)
        self.assertEqual(cause['code'],
                         'child-command-failed' if failure_kind == 'exit' else
                         'child-command-timeout' if failure_kind == 'timeout' else 'tool-unavailable')
        if failure_kind == 'missing':
            self.assertEqual(cause['message'], 'vercel')
        else:
            self.assertIn(self.output_sentinel, cause['message'])
            self.assertIn(self.project, cause['message'])
            self.assertIn('https://api.test.invalid', cause['message'])
            self.assertIn('Authorization: [REDACTED]', cause['message'])
            self.assertIn('--token="[REDACTED]"', cause['message'])
            self.assertEqual(cause['message'].count('--token="[REDACTED]"'), 2)
            self.assertIn('VERCEL_TOKEN=[REDACTED]', cause['message'])
        self.assertFalse(cause['message_truncated'])
        safe_output = stdout.getvalue() + json.dumps(result) + str(error)
        if failure_kind != 'missing':
            self.assertIn(self.output_sentinel, safe_output)
        self.assertNotIn(self.token, safe_output)
        self.assertNotIn('different-secret', safe_output)
        self.assertNotIn('TEST_ONLY_RAW_STDOUT_SENTINEL', safe_output)
        self.assertNotIn('VERCEL_TEAM_ID=', safe_output)
        self.assertNotIn('VERCEL_PROJECT_ID=', safe_output)

    def run_engine_main_prepare(self, output_case='valid', root_decoy=False,
                                settings_case='valid', metadata_failure=False):
        with tempfile.TemporaryDirectory() as work:
            instance, directory, fake_root, auth_receipt_bytes = self.make_engine(work)
            fake_profiles = fake_root / 'deploy/engine/profiles.json'
            fake_profiles.parent.mkdir(parents=True)
            shutil.copy2(SUPPORT_ROOT / 'deploy/engine/profiles.json', fake_profiles)
            trace = []
            barrier_calls = []
            probe = HERE / 'tests' / 'test_vercel_env_context.js'

            def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
                operation = self.child_operation(args)
                trace.append(operation)
                if operation == 'auth-digest':
                    return subprocess.CompletedProcess(args, 0, stdout='sha256:' + 'a' * 64, stderr='')
                if operation == 'frontend-project-readback':
                    response = {'id': self.project, 'name': 'llm-wiki-frontend-test',
                                'accountId': self.team, 'rootDirectory': 'apps/frontend',
                                'link': {'org': 'Rayer', 'repo': 'llm-wiki-cloud'}}
                    return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response), stderr='')
                if operation == 'frontend-npm-ci':
                    return subprocess.CompletedProcess(args, 0, stdout='', stderr='')
                if operation == 'frontend-vercel-pull':
                    self.assertEqual(Path(cwd), fake_root)
                    self.assertEqual(args, ['vercel', 'pull', '--yes', '--environment=preview',
                                            '--scope', 'test-team', '--token', self.token])
                    identity_env = {key: env[key] for key in (
                        'VERCEL_ORG_ID', 'VERCEL_PROJECT_ID', 'VERCEL_TEAM_ID')}
                    resolved = ORIGINAL_SUBPROCESS_RUN(
                        ['node', probe, '--simulate-env-pull'],
                        input=json.dumps({'env': identity_env, 'cwd': str(fake_root),
                                          'expected': {'project': self.project, 'team': self.team}}),
                        capture_output=True, text=True, check=False,
                        env={'PATH': os.environ.get('PATH', '/usr/bin:/bin')})
                    self.assertEqual(resolved.returncode, 0, resolved.stderr)
                    self.assertEqual(json.loads(resolved.stdout), {
                        'linkPath': '.vercel/project.json', 'pullDirectory': '.'})
                    project_link = fake_root / '.vercel/project.json'
                    project_link.parent.mkdir(parents=True, exist_ok=True)
                    if settings_case == 'malformed':
                        project_link.write_text('{malformed')
                    else:
                        project_link.write_text(json.dumps({
                            'projectId': self.project, 'orgId': self.team,
                            'projectName': 'test-project',
                            'settings': {'rootDirectory': 'apps/frontend'}}))
                    return subprocess.CompletedProcess(args, 0, stdout='', stderr='')
                if operation == 'frontend-vercel-build':
                    output = fake_root / 'apps/frontend/.vercel/output/static'
                    if output_case != 'none':
                        output.mkdir(parents=True)
                    cfg = instance.plan['normalized']['frontend']
                    if output_case != 'missing':
                        built_config = {'schema_version': 1, 'api_url': cfg['api_url'],
                                        'auth_url': cfg['auth_url']}
                        if output_case == 'wrong':
                            built_config['api_url'] = 'https://wrong.invalid'
                        if output_case != 'none':
                            (output / 'build-config.json').write_text(json.dumps(built_config))
                    if root_decoy:
                        decoy = fake_root / '.vercel/output/static'
                        decoy.mkdir(parents=True)
                        (decoy / 'build-config.json').write_text(json.dumps({
                            'schema_version': 1, 'api_url': cfg['api_url'],
                            'auth_url': cfg['auth_url']}))
                    return subprocess.CompletedProcess(args, 0, stdout='', stderr='')
                raise AssertionError('unexpected operation in Engine.main valid-response fixture')

            env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(work),
                   'TMPDIR': str(work), 'VERCEL_TOKEN': self.token,
                   'VERCEL_TEAM_ID': self.team, 'VERCEL_PROJECT_ID': self.project}
            argv = ['engine.py', 'prepare', '--directory', str(directory),
                    '--environment', 'development', '--source', instance.plan['source'],
                    '--tag', instance.plan['tag'], '--components', 'auth,frontend']
            stdout = io.StringIO()
            original_barrier = engine.Engine.barrier
            def observe_barrier(engine_instance):
                barrier_calls.append(engine_instance.state['status'])
                return original_barrier(engine_instance)
            diagnostic_patch = (patch.object(providers.Providers, '_frontend_prepare_evidence',
                                             side_effect=OSError('diagnostic fixture failure'))
                                if metadata_failure else contextlib.nullcontext())
            with patch.dict(os.environ, env, clear=True), patch('providers.ROOT', fake_root), \
                    patch('support.subprocess.run', side_effect=fake_subprocess), diagnostic_patch, \
                    patch.object(sys, 'argv', argv), \
                    patch.object(engine.Engine, 'barrier', new=observe_barrier), \
                    patch.object(engine.Engine, 'runtime_guard', side_effect=AssertionError('runtime reached')), \
                    contextlib.redirect_stdout(stdout):
                exit_code = engine.main()

            result = json.loads((directory / 'result.json').read_text())
            receipt_bytes = ((directory / 'receipts/frontend.json').read_bytes()
                             if (directory / 'receipts/frontend.json').exists() else None)
            archive_members = []
            archive_payloads = {}
            if (directory / 'frontend.tgz').exists():
                with tarfile.open(directory / 'frontend.tgz', 'r:gz') as archive:
                    archive_members = archive.getnames()
                    for name in ('.vercel/project.json',
                                 '.vercel/output/static/build-config.json'):
                        extracted = archive.extractfile(name) if name in archive_members else None
                        if extracted:
                            archive_payloads[name] = extracted.read()
            return {
                'exit_code': exit_code, 'result': result, 'stdout': stdout.getvalue(),
                'trace': trace, 'auth_receipt': (directory / 'receipts/auth.json').read_bytes(),
                'expected_auth_receipt': auth_receipt_bytes, 'frontend_receipt': receipt_bytes,
                'archive_exists': (directory / 'frontend.tgz').exists(),
                'archive_members': archive_members, 'archive_payloads': archive_payloads,
                'state': read(directory / 'state.json') if (directory / 'state.json').exists() else None,
                'barrier_calls': barrier_calls, 'root_decoy': root_decoy,
            }

    def test_engine_main_valid_project_response_prepares_frontend_and_keeps_auth(self):
        outcome = self.run_engine_main_prepare()
        self.assertEqual(outcome['exit_code'], 0, outcome['stdout'])
        result = outcome['result']
        self.assertEqual(result['reason'], 'completed')
        self.assertEqual(result['stage'], 'ready')
        self.assertNotIn('cause', result)
        self.assertEqual(outcome['auth_receipt'], outcome['expected_auth_receipt'])
        self.assertIsNotNone(outcome['frontend_receipt'])
        self.assertTrue(outcome['archive_exists'])
        self.assertEqual(len(outcome['barrier_calls']), 1)
        self.assertIn('.vercel/project.json', outcome['archive_members'])
        self.assertIn('.vercel/output/static/build-config.json', outcome['archive_members'])
        self.assertEqual(json.loads(outcome['archive_payloads']['.vercel/project.json']),
                         {'projectId': self.project, 'orgId': self.team, 'projectName': 'test-project',
                          'settings': {'rootDirectory': 'apps/frontend'}})
        self.assertEqual(json.loads(outcome['archive_payloads']['.vercel/output/static/build-config.json']), {
            'schema_version': 1, 'api_url': 'https://api.test.invalid',
            'auth_url': 'https://auth.test.invalid'})
        self.assertEqual(outcome['trace'][:4], ['auth-digest', 'frontend-project-readback',
                                                'frontend-npm-ci', 'frontend-vercel-pull'])
        self.assertIn('frontend-vercel-build', outcome['trace'])
        self.assertNotIn('auth-build', outcome['trace'])
        self.assertEqual(json.loads(outcome['stdout']), result)
        self.assertNotIn('frontend_prepare_diagnostic', result)

    def test_failure_reports_fixed_root_and_configured_output_candidates_without_changing_failure(self):
        outcome = self.run_engine_main_prepare(output_case='none', root_decoy=True)
        self.assertEqual(outcome['exit_code'], 1, outcome['stdout'])
        self.assertEqual(outcome['result']['reason'], 'frontend-output-missing')
        self.assertEqual(outcome['result']['status'], 'failed')
        self.assertFalse(outcome['result']['mutation_may_have_happened'])
        self.assertEqual(outcome['result']['observed']['component_status'], 'unstarted')
        diagnostic = outcome['result']['frontend_prepare_diagnostic']
        self.assertEqual(diagnostic['linked_root'], 'matches-config')
        self.assertEqual(diagnostic['settings_root'], 'matches-config')
        self.assertEqual(diagnostic['commands'], {stage: {
            'status': 'exit0', 'exit_code': 0, 'timeout_class': None}
            for stage, _, _, _ in self.stages})
        self.assertEqual(diagnostic['outputs'], {
            'repo_root': {'exists': True, 'static_config_valid': True, 'target_equal': True},
            'configured_root': {'exists': False, 'static_config_valid': False, 'target_equal': False},
        })
        self.assertFalse(diagnostic['metadata_read_failed'])
        self.assertEqual(outcome['auth_receipt'], outcome['expected_auth_receipt'])
        self.assertIsNone(outcome['frontend_receipt'])
        self.assertFalse(outcome['archive_exists'])
        self.assertIsNone(outcome['state'])
        self.assertEqual(outcome['barrier_calls'], [])

    def test_malformed_diagnostic_metadata_keeps_primary_prepare_failure(self):
        outcome = self.run_engine_main_prepare(
            output_case='none', settings_case='malformed', root_decoy=False)
        self.assertEqual(outcome['exit_code'], 1, outcome['stdout'])
        self.assertEqual(outcome['result']['reason'], 'frontend-output-missing')
        self.assertEqual(outcome['result']['status'], 'failed')
        self.assertFalse(outcome['result']['mutation_may_have_happened'])
        self.assertEqual(outcome['result']['observed']['component_status'], 'unstarted')
        diagnostic = outcome['result']['frontend_prepare_diagnostic']
        self.assertEqual(diagnostic['settings_root'], 'other')
        self.assertTrue(diagnostic['metadata_read_failed'])
        self.assertEqual(diagnostic['outputs']['repo_root'], {
            'exists': False, 'static_config_valid': False, 'target_equal': False})
        self.assertEqual(outcome['auth_receipt'], outcome['expected_auth_receipt'])
        self.assertIsNone(outcome['frontend_receipt'])
        self.assertFalse(outcome['archive_exists'])
        self.assertEqual(outcome['barrier_calls'], [])

    def test_diagnostic_collection_error_does_not_replace_primary_prepare_failure(self):
        outcome = self.run_engine_main_prepare(
            output_case='none', metadata_failure=True)
        self.assertEqual(outcome['exit_code'], 1, outcome['stdout'])
        self.assertEqual(outcome['result']['reason'], 'frontend-output-missing')
        self.assertNotIn('frontend_prepare_diagnostic', outcome['result'])
        self.assertIsNone(outcome['frontend_receipt'])
        self.assertFalse(outcome['archive_exists'])
        self.assertEqual(outcome['barrier_calls'], [])

    def test_production_frontend_failure_does_not_add_dev_diagnostic(self):
        with tempfile.TemporaryDirectory() as work:
            instance, directory, fake_root, _ = self.make_engine(work)
            instance.plan['normalized']['environment'] = 'production'
            trace = []

            def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
                operation = self.child_operation(args)
                trace.append(operation)
                if operation == 'frontend-project-readback':
                    response = {'id': self.project, 'name': 'llm-wiki-frontend-test',
                                'accountId': self.team, 'rootDirectory': 'apps/frontend',
                                'link': {'org': 'Rayer', 'repo': 'llm-wiki-cloud'}}
                    return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response), stderr='')
                if operation == 'frontend-vercel-pull':
                    link = fake_root / '.vercel/project.json'
                    link.parent.mkdir(parents=True)
                    link.write_text(json.dumps({'settings': {'rootDirectory': 'apps/frontend'}}))
                return subprocess.CompletedProcess(args, 0, stdout='', stderr='')

            env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(work),
                   'TMPDIR': str(work), 'VERCEL_TOKEN': self.token,
                   'VERCEL_TEAM_ID': self.team, 'VERCEL_PROJECT_ID': self.project}
            with patch.dict(os.environ, env, clear=True), patch('providers.ROOT', fake_root), \
                    patch('support.subprocess.run', side_effect=fake_subprocess):
                with self.assertRaises(Breakpoint) as caught:
                    instance.provider.prepare('frontend')
                error = caught.exception
                self.assertEqual(error.reason, 'frontend-output-missing')
                self.assertIsNone(error.frontend_prepare_diagnostic)
                instance.component = 'frontend'
                instance.result(error)

            result = read(directory / 'result.json')
            self.assertNotIn('frontend_prepare_diagnostic', result)
            self.assertEqual(trace, [stage for stage, _, _, _ in self.stages])

    def test_missing_or_wrong_project_root_config_fails_closed_without_root_fallback(self):
        for output_case in ('missing', 'wrong'):
            with self.subTest(output_case=output_case):
                outcome = self.run_engine_main_prepare(output_case, root_decoy=True)
                self.assertEqual(outcome['exit_code'], 1, outcome['stdout'])
                self.assertEqual(outcome['auth_receipt'], outcome['expected_auth_receipt'])
                self.assertIsNone(outcome['frontend_receipt'])
                self.assertFalse(outcome['archive_exists'])
                self.assertEqual(outcome['result']['stage'], 'prepared')
                self.assertEqual(outcome['result']['observed']['component_status'], 'unstarted')
                self.assertNotEqual(outcome['result']['stage'], 'ready')
                self.assertFalse(outcome['result']['mutation_may_have_happened'])
                self.assertEqual(outcome['barrier_calls'], [])
                self.assertNotIn('auth-build', outcome['trace'])
                if output_case == 'wrong':
                    self.assertEqual(outcome['result']['reason'], 'frontend-build-config-mismatch')
                else:
                    self.assertEqual(outcome['result']['reason'], 'invalid-or-unreadable-input')
                    self.assertEqual(outcome['result']['cause']['exception_type'], 'FileNotFoundError')

    def test_action_forwards_main_cause_to_stdout_and_retained_result_without_raw_input(self):
        action = SUPPORT_ROOT / '.github/actions/deployment-engine/index.cjs'
        workflow = (SUPPORT_ROOT / '.github/workflows/cd.yml').read_text()
        retain = workflow.split('name: Retain final result even after failure', 1)[1]
        self.assertIn('if: always()', retain)
        self.assertIn('path: ${{ runner.temp }}/release', retain)

        for case, response, expected_type, expected_code, message_fragment in (
            ('missing-field', '{"name":"llm-wiki-frontend-test"}',
             'KeyError', 'required-field-missing', "'id'"),
            ('invalid-json', '{"credential":"TEST_ONLY_VERCEL_TOKEN_SENTINEL",oops',
             'JSONDecodeError', 'invalid-json', 'line 1 column'),
        ):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as work:
                work = Path(work)
                _, source_directory, _, auth_receipt_bytes = self.make_engine(work)
                release = work / 'release'
                shutil.copytree(source_directory, release)
                fake_bin = work / 'bin'
                fake_bin.mkdir()
                python = fake_bin / 'python3'
                python.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' "$@"\n')
                python.chmod(0o755)
                gcloud = fake_bin / 'gcloud'
                gcloud.write_text('#!/bin/sh\nprintf "sha256:' + 'a' * 64 + '\\n"\n')
                gcloud.chmod(0o755)
                curl = fake_bin / 'curl'
                curl.write_text('''#!/usr/bin/env python3
import os, sys
sys.stdout.write(os.environ['FAKE_PROJECT_RESPONSE'])
''')
                curl.chmod(0o755)
                env = {
                    'PATH': str(fake_bin) + os.pathsep + os.environ.get('PATH', '/usr/bin:/bin'),
                    'HOME': str(work), 'TMPDIR': os.environ.get('TMPDIR', str(work)),
                    'RUNNER_TEMP': str(work), 'INPUT_OPERATION': 'prepare',
                    'TARGET': 'development', 'SOURCE': 'c' * 40,
                    'COMPONENTS': 'auth,frontend', 'RELEASE_TAG': 'offline-diagnostic',
                    'VERCEL_TOKEN': self.token, 'VERCEL_TEAM_ID': self.team,
                    'VERCEL_PROJECT_ID': self.project, 'FAKE_PROJECT_RESPONSE': response,
                }
                result = ORIGINAL_SUBPROCESS_RUN(
                    ['node', str(action)], cwd=SUPPORT_ROOT, env=env,
                    capture_output=True, text=True, check=False)
                self.assertEqual(result.returncode, 1, result.stderr)
                action_result = json.loads(result.stdout)
                retained_result = json.loads((release / 'result.json').read_text())
                self.assertEqual(action_result, retained_result)
                self.assertEqual(action_result['reason'], 'invalid-or-unreadable-input')
                self.assertEqual(action_result['cause']['exception_type'], expected_type)
                self.assertEqual(action_result['cause']['code'], expected_code)
                self.assertEqual(action_result['cause']['stage'], 'frontend-project-readback')
                self.assertIn(message_fragment, action_result['cause']['message'])
                self.assertEqual(action_result['failure_diagnostic'], {
                    'stage': 'frontend-project-readback', 'exit_code': None, 'timeout_class': None})
                diagnostic = action_result['frontend_prepare_diagnostic']
                self.assertEqual(diagnostic['commands'], {
                    'frontend-project-readback': {
                        'status': 'exit0', 'exit_code': 0, 'timeout_class': None},
                    'frontend-npm-ci': {
                        'status': 'not-run', 'exit_code': None, 'timeout_class': None},
                    'frontend-vercel-pull': {
                        'status': 'not-run', 'exit_code': None, 'timeout_class': None},
                    'frontend-vercel-build': {
                        'status': 'not-run', 'exit_code': None, 'timeout_class': None}})
                self.assertIn(diagnostic['settings_root'], ('matches-config', 'empty', 'missing', 'other'))
                combined = result.stdout + result.stderr + json.dumps(retained_result)
                self.assertNotIn(self.token, combined)
                self.assertNotIn('TEST_ONLY_VERCEL_TOKEN_SENTINEL', combined)
                self.assertEqual((release / 'receipts/auth.json').read_bytes(), auth_receipt_bytes)
                self.assertFalse((release / 'receipts/frontend.json').exists())
                self.assertFalse((release / 'frontend.tgz').exists())

    def test_each_frontend_subprocess_failure_is_typed_redacted_and_stops_prepare(self):
        for failure_kind in ('exit', 'timeout'):
            for stage, _, _, _ in self.stages:
                with self.subTest(stage=stage, failure=failure_kind), tempfile.TemporaryDirectory() as work:
                    self.assert_failure_case(work, stage, failure_kind)

    def test_missing_frontend_cli_is_typed_failure_not_timeout(self):
        with tempfile.TemporaryDirectory() as work:
            self.assert_failure_case(work, 'frontend-vercel-build', 'missing')

    def test_pinned_vercel_and_build_utils_source_env_contract(self):
        result = ORIGINAL_SUBPROCESS_RUN(
            ['node', HERE / 'tests' / 'test_vercel_env_context.js'],
            capture_output=True, text=True, check=False,
            env={'PATH': os.environ.get('PATH', '/usr/bin:/bin')})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('PROJECT present + ORG absent', result.stdout)
        self.assertIn('Validated TEAM mapped to ORG', result.stdout)
        self.assertIn('conflicting inherited NOW aliases', result.stdout)

    def test_provider_prepare_passes_readback_identity_to_pinned_cli_context(self):
        with tempfile.TemporaryDirectory() as work:
            work = Path(work)
            instance, directory, fake_root, _ = self.make_engine(work)
            project_root = fake_root / 'apps/frontend'
            output = project_root / '.vercel/output'
            (output / 'static').mkdir(parents=True)
            cfg = instance.plan['normalized']['frontend']
            (output / 'static/build-config.json').write_text(json.dumps({
                'schema_version': 1, 'api_url': cfg['api_url'], 'auth_url': cfg['auth_url']}))
            (fake_root / '.vercel/project.json').parent.mkdir(parents=True)
            (fake_root / '.vercel/project.json').write_text('{}')
            provider = providers.Providers(instance.plan, directory)
            trace = []
            probe = HERE / 'tests' / 'test_vercel_env_context.js'

            def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
                operation = self.child_operation(args)
                trace.append((operation, list(args), str(cwd), timeout, env))
                if operation == 'frontend-project-readback':
                    response = {'id': self.project, 'name': 'llm-wiki-frontend-test',
                                'accountId': self.team, 'rootDirectory': 'apps/frontend',
                                'link': {'org': 'Rayer', 'repo': 'llm-wiki-cloud'}}
                    return subprocess.CompletedProcess(args, 0, stdout=json.dumps(response), stderr='')
                if operation == 'frontend-npm-ci':
                    return subprocess.CompletedProcess(args, 0, stdout='', stderr='')
                if operation in ('frontend-vercel-pull', 'frontend-vercel-build'):
                    if operation == 'frontend-vercel-pull':
                        self.assertEqual(args, ['vercel', 'pull', '--yes', '--environment=preview',
                                                '--scope', 'test-team', '--token', self.token])
                        safe_env = {key: env[key] for key in (
                            'VERCEL_PROJECT_ID', 'VERCEL_ORG_ID', 'VERCEL_TEAM_ID',
                            'NOW_PROJECT_ID', 'NOW_ORG_ID') if key in env}
                        checked = ORIGINAL_SUBPROCESS_RUN(
                            ['node', probe, '--assert-linked-env'],
                            input=json.dumps({'env': safe_env,
                                              'expected': {'project': self.project, 'team': self.team}}),
                            capture_output=True, text=True, check=False,
                            env={'PATH': os.environ.get('PATH', '/usr/bin:/bin')})
                        self.assertEqual(checked.returncode, 0, checked.stderr)
                    return subprocess.CompletedProcess(args, 0, stdout='', stderr='')
                raise AssertionError('unexpected subprocess in Provider.prepare integration fixture')

            inherited = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(work),
                         'TMPDIR': str(work), 'VERCEL_TOKEN': self.token,
                         'VERCEL_TEAM_ID': self.team, 'VERCEL_PROJECT_ID': self.project,
                         'VERCEL_ORG_ID': 'team_InheritedWrong',
                         'NOW_ORG_ID': 'team_LegacyWrong', 'NOW_PROJECT_ID': 'prj_LegacyWrong'}
            with patch.dict(os.environ, inherited, clear=True), patch('providers.ROOT', fake_root), \
                    patch('support.subprocess.run', side_effect=fake_subprocess):
                artifact = provider.prepare('frontend')
                self.assertEqual(os.environ['VERCEL_ORG_ID'], 'team_InheritedWrong')
                self.assertEqual(os.environ['NOW_ORG_ID'], 'team_LegacyWrong')
                self.assertEqual(os.environ['NOW_PROJECT_ID'], 'prj_LegacyWrong')

            self.assertEqual([row[0] for row in trace], [
                'frontend-project-readback', 'frontend-npm-ci',
                'frontend-vercel-pull', 'frontend-vercel-build'])
            pull, build = trace[2], trace[3]
            for row in (pull, build):
                child_env = row[4]
                self.assertEqual(child_env['VERCEL_ORG_ID'], self.team)
                self.assertEqual(child_env['VERCEL_PROJECT_ID'], self.project)
                self.assertEqual(child_env['VERCEL_TEAM_ID'], self.team)
                self.assertNotIn('NOW_ORG_ID', child_env)
                self.assertNotIn('NOW_PROJECT_ID', child_env)
            self.assertEqual(pull[2], str(fake_root))
            self.assertEqual(pull[3], 30)
            self.assertEqual(build[2], str(fake_root))
            self.assertEqual(build[3], 900)
            self.assertEqual(artifact['project'], self.project)
            self.assertEqual(artifact['team'], self.team)

    def test_runtime_deploy_maps_selected_context_before_pinned_cli(self):
        with tempfile.TemporaryDirectory() as work:
            work = Path(work)
            instance, directory, _, _ = self.make_engine(work)
            provider = providers.Providers(instance.plan, directory)
            provider.p['frontend']['stable_aliases'] = ['site.test.invalid']
            project_file = work / 'archive/.vercel/project.json'
            project_file.parent.mkdir(parents=True)
            project_file.write_text(json.dumps({'projectId': self.project, 'orgId': self.team}))
            config_file = work / 'archive/.vercel/output/static/build-config.json'
            config_file.parent.mkdir(parents=True)
            config_file.write_text(json.dumps({'schema_version': 1, 'api_url': 'https://api.test.invalid',
                                               'auth_url': 'https://auth.test.invalid'}))
            archive = directory / 'frontend.tgz'
            with tarfile.open(archive, 'w:gz') as tar:
                tar.add(project_file, arcname='.vercel/project.json')
                tar.add(work / 'archive/.vercel/output', arcname='.vercel/output')
            artifact = {'archive': 'frontend.tgz', 'sha256': hashlib.sha256(
                            archive.read_bytes()).hexdigest(), 'project': self.project,
                        'team': self.team, 'target': 'preview'}
            candidate = {}
            saved = []
            trace = []
            probe = HERE / 'tests' / 'test_vercel_env_context.js'

            def classify(child_env, cwd):
                safe_env = {key: child_env[key] for key in (
                    'VERCEL_PROJECT_ID', 'VERCEL_ORG_ID', 'VERCEL_TEAM_ID',
                    'NOW_PROJECT_ID', 'NOW_ORG_ID') if key in child_env}
                checked = ORIGINAL_SUBPROCESS_RUN(
                    ['node', probe, '--classify-runtime-env'],
                    input=json.dumps({'env': safe_env, 'cwd': str(cwd),
                                      'expected': {'project': self.project, 'team': self.team}}),
                    capture_output=True, text=True, check=False,
                    env={'PATH': os.environ.get('PATH', '/usr/bin:/bin')})
                self.assertEqual(checked.returncode, 0, checked.stderr)
                return checked.stdout.strip()

            def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
                self.assertEqual(args, ['vercel', 'deploy', '--prebuilt', '--yes', '--scope',
                                        'test-team', '--token', self.token, '--meta',
                                        'lwcArtifact='+artifact['sha256'], '--meta',
                                        'lwcAttempt='+instance.plan['id'], '--target=preview'])
                self.assertEqual(Path(cwd, '.vercel/project.json').read_text(),
                                 json.dumps({'projectId': self.project, 'orgId': self.team}))
                self.assertEqual(json.loads(Path(cwd, '.vercel/output/static/build-config.json').read_text()),
                                 {'schema_version': 1, 'api_url': 'https://api.test.invalid',
                                  'auth_url': 'https://auth.test.invalid'})
                baseline = dict(env)
                for name in ('VERCEL_ORG_ID', 'NOW_ORG_ID', 'NOW_PROJECT_ID'):
                    baseline.pop(name, None)
                self.assertEqual(classify(baseline, cwd), 'pair-incomplete')
                self.assertEqual(classify(env, cwd), 'linked')
                trace.append((list(args), str(cwd), timeout, dict(env)))
                return subprocess.CompletedProcess(args, 0, stdout='https://candidate.vercel.app', stderr='')

            inherited = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(work),
                         'TMPDIR': str(work), 'VERCEL_TOKEN': self.token,
                         'VERCEL_TEAM_ID': self.team, 'VERCEL_PROJECT_ID': self.project,
                         'VERCEL_ORG_ID': 'team_InheritedWrong',
                         'NOW_ORG_ID': 'team_LegacyWrong', 'NOW_PROJECT_ID': 'prj_LegacyWrong',
                         'GITHUB_ACTIONS': 'true', 'GITHUB_SHA': 'a' * 40,
                         'VERCEL_GIT_COMMIT_SHA': 'b' * 40}
            with patch.dict(os.environ, inherited, clear=True), \
                    patch('support.subprocess.run', side_effect=fake_subprocess), \
                    patch.object(provider, 'deployment', return_value={'id': 'dpl_candidate'}) as deployment_readback, \
                    patch.object(provider, 'alias', return_value='dpl_candidate') as alias_readback, \
                    patch.object(provider, 'api') as api:
                provider.deploy('frontend', artifact, candidate, lambda: saved.append(dict(candidate)))
                self.assertEqual(os.environ['VERCEL_PROJECT_ID'], self.project)
                self.assertEqual(os.environ['VERCEL_TEAM_ID'], self.team)
                self.assertEqual(os.environ['VERCEL_ORG_ID'], 'team_InheritedWrong')
                self.assertEqual(os.environ['NOW_ORG_ID'], 'team_LegacyWrong')
                self.assertEqual(os.environ['NOW_PROJECT_ID'], 'prj_LegacyWrong')
                self.assertEqual(os.environ['GITHUB_SHA'], 'a' * 40)
                self.assertEqual(os.environ['VERCEL_GIT_COMMIT_SHA'], 'b' * 40)
                self.assertEqual(candidate, {'deployment': 'dpl_candidate'})
                self.assertEqual(saved, [{'deployment': 'dpl_candidate'}])
                deployment_readback.assert_called_once_with('candidate.vercel.app')
                self.assertEqual([call.args[0] for call in alias_readback.call_args_list],
                                 ['site.test.invalid'])
                api.assert_not_called()

            with patch.dict(os.environ, inherited, clear=True), \
                    patch('support.subprocess.run', side_effect=fake_subprocess), \
                    patch.object(provider, 'deployment', return_value={'id': 'dpl_candidate'}), \
                    patch.object(provider, 'alias', return_value='dpl_candidate') as alias_readback, \
                    patch.object(provider, 'api') as api:
                provider.deploy('frontend', artifact, candidate, lambda: saved.append(dict(candidate)))
                alias_readback.assert_called_once_with('site.test.invalid')
                api.assert_not_called()

            self.assertEqual(len(trace), 1)
            self.assertEqual(trace[0][2], 30)
            self.assertNotIn('GITHUB_SHA', trace[0][3])
            self.assertNotIn('VERCEL_GIT_COMMIT_SHA', trace[0][3])
            self.assertEqual(trace[0][3]['VERCEL_ORG_ID'], self.team)
            self.assertEqual(trace[0][3]['VERCEL_PROJECT_ID'], self.project)
            self.assertEqual(trace[0][3]['VERCEL_TEAM_ID'], self.team)
            self.assertNotIn('NOW_ORG_ID', trace[0][3])
            self.assertNotIn('NOW_PROJECT_ID', trace[0][3])
            self.assertEqual(trace[0][3]['VERCEL_TOKEN'], self.token)
            self.assertEqual(len(saved), 1)
            print('Pinned resolver baseline=pair-incomplete; runtime-child=linked; resume=single-deploy')


if __name__ == '__main__':
    unittest.main(verbosity=2)
