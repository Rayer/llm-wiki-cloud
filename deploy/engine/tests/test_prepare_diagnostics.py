"""Causal, offline regression for Auth build stage and exit-code metadata."""
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
import engine
import providers
from support import Breakpoint, digest, write


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


if __name__ == '__main__':
    unittest.main(verbosity=2)
