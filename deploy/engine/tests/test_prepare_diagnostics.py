"""Causal, offline regression for Auth build stage and exit-code metadata."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
import engine
import providers
from support import Breakpoint, ROOT as SUPPORT_ROOT, digest, write


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

        def fake_subprocess(args, *, cwd, env, input, capture_output, text, timeout):
            operation = self.child_operation(args)
            trace.append((operation, str(cwd), timeout))
            if operation == 'auth-digest':
                return subprocess.CompletedProcess(args, 0, stdout='sha256:' + 'a' * 64,
                                                   stderr=self.output_sentinel)
            if operation == failing_stage:
                if failure_kind == 'timeout':
                    raise subprocess.TimeoutExpired(args, timeout, output=self.output_sentinel,
                                                    stderr=self.output_sentinel)
                return subprocess.CompletedProcess(args, 23, stdout=self.output_sentinel,
                                                   stderr=self.output_sentinel)
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
                         (23, None) if failure_kind == 'exit' else (None, 'subprocess-timeout'))
        self.assertEqual((directory / 'receipts/auth.json').read_bytes(), auth_receipt_bytes)
        self.assertFalse((directory / 'receipts/frontend.json').exists())
        self.assertFalse((directory / 'frontend.tgz').exists())
        self.assertEqual(instance.state['status'], 'prepared')
        self.assertEqual(instance.state['components'], {})
        self.assertFalse(any(operation in ('auth-build', 'auth-submit') for operation, _, _ in trace))

        result = json.loads((directory / 'result.json').read_text())
        self.assertEqual(result['failure_diagnostic'], {
            'stage': failing_stage,
            'exit_code': 23 if failure_kind == 'exit' else None,
            'timeout_class': None if failure_kind == 'exit' else 'subprocess-timeout'})
        self.assertEqual(set(result['failure_diagnostic']), {'stage', 'exit_code', 'timeout_class'})
        self.assertEqual((result['component'], result['status'], result['mutation_may_have_happened']),
                         ('frontend', 'failed', False))
        safe_output = stdout.getvalue() + json.dumps(result) + str(error)
        for private_value in (self.token, self.team, self.project, self.output_sentinel,
                              'https://api.test.invalid', 'https://auth.test.invalid',
                              'test-team', 'Rayer/llm-wiki-cloud', '--token', '--scope',
                              'VERCEL_TOKEN', 'VERCEL_TEAM_ID', 'VERCEL_PROJECT_ID',
                              'NEXT_PUBLIC_API_URL', 'NEXT_PUBLIC_AUTH_URL',
                              'Authorization: Bearer'):
            self.assertNotIn(private_value, safe_output)

    def test_each_frontend_subprocess_failure_is_typed_redacted_and_stops_prepare(self):
        for failure_kind in ('exit', 'timeout'):
            for stage, _, _, _ in self.stages:
                with self.subTest(stage=stage, failure=failure_kind), tempfile.TemporaryDirectory() as work:
                    self.assert_failure_case(work, stage, failure_kind)


if __name__ == '__main__':
    unittest.main(verbosity=2)
