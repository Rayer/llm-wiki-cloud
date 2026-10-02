"""Offline tests for the fixed DEV diagnostic action branch."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'deploy/engine'))
import diagnostics


class ReadOnlyDiagnostic(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.calls = self.root / 'calls.jsonl'
        self.gcloud = self.bin / 'gcloud'
        self.gcloud.write_text('''#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
with open(os.environ["FAKE_CALLS"], "a") as f: f.write(json.dumps(args)+"\\n")
sys.stderr.write(os.environ.get("FAKE_STDERR", ""))
reference=args[4]
if "@" in reference:
    output=os.environ["FAKE_IMMUTABLE_OUTPUT"]
    code=int(os.environ.get("FAKE_IMMUTABLE_EXIT", "0"))
else:
    output=os.environ["FAKE_TAG_OUTPUT"]
    code=int(os.environ.get("FAKE_TAG_EXIT", "0"))
sys.stdout.write(output+"\\n")
raise SystemExit(code)
''')
        self.gcloud.chmod(0o755)
        self.sha = 'a' * 40  # represents the reviewed diagnostic code, not target image source
        self.env = {
            **os.environ,
            'PATH': str(self.bin) + os.pathsep + os.environ['PATH'],
            'FAKE_CALLS': str(self.calls),
            'FAKE_TAG_OUTPUT': diagnostics.IMMUTABLE_DIGEST,
            'FAKE_IMMUTABLE_OUTPUT': diagnostics.IMMUTABLE_DIGEST,
            'FAKE_STDERR': 'TEST_ONLY_PROVIDER_SECRET_SENTINEL',
            'GITHUB_ACTIONS': 'true',
            'GITHUB_REF': 'refs/heads/develop',
            'GITHUB_SHA': self.sha,
            'WORKFLOW_REF': 'refs/heads/develop',
            'WORKFLOW_SHA': self.sha,
            'SOURCE': self.sha,
            'TARGET': 'development',
            'COMPONENTS': 'auth',
            'OPERATION': diagnostics.DIAGNOSTIC_OPERATION,
            'RELEASE_TAG': diagnostics.DIAGNOSTIC_TAG,
            'ARTIFACT_ID': diagnostics.NO_RECEIPT,
            'REUSE_ID': diagnostics.NO_RECEIPT,
            'DEV_ID': '',
            'ACTIONS_RUNTIME_TOKEN': 'TEST_ONLY_ACTIONS_TOKEN_SENTINEL',
            'GH_TOKEN': 'TEST_ONLY_GH_TOKEN_SENTINEL',
            'VERCEL_TOKEN': 'TEST_ONLY_VERCEL_TOKEN_SENTINEL',
        }

    def invoke(self, **overrides):
        output = self.root / 'result.json'
        env = dict(self.env, **overrides)
        proc = subprocess.run([sys.executable, str(ROOT / 'deploy/engine/diagnostics.py'),
                               '--output', str(output)], env=env, capture_output=True,
                              text=True, timeout=20)
        result = json.loads(output.read_text())
        return proc, result

    def test_actual_action_helper_runs_only_two_fixed_read_operations_and_redacts(self):
        proc, result = self.invoke()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(result['status'], 'verified')
        self.assertEqual(result['operations'], [
            {'operation': 'source-tag', 'exit_code': 0, 'timeout_class': 'none', 'digest_format_valid': True},
            {'operation': 'immutable-digest', 'exit_code': 0, 'timeout_class': 'none', 'digest_format_valid': True},
        ])
        self.assertTrue(result['tag_matches_immutable_digest'])
        self.assertEqual(result['conclusion'], 'tag-matches-immutable-digest')
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertEqual([call[:4] for call in calls], [
            ['artifacts', 'docker', 'images', 'describe'],
            ['artifacts', 'docker', 'images', 'describe'],
        ])
        self.assertEqual([call[4] for call in calls],
                         [diagnostics.SOURCE_TAG, diagnostics.IMMUTABLE_REF])
        self.assertTrue(all('--project' in call and '--quiet' in call for call in calls))
        rendered = proc.stdout + proc.stderr + json.dumps(result) + self.calls.read_text()
        for sentinel in ('TEST_ONLY_PROVIDER_SECRET_SENTINEL', 'TEST_ONLY_ACTIONS_TOKEN_SENTINEL',
                         'TEST_ONLY_GH_TOKEN_SENTINEL', 'TEST_ONLY_VERCEL_TOKEN_SENTINEL'):
            self.assertNotIn(sentinel, rendered)
        self.assertNotIn('gcloud', proc.stdout)
        self.assertNotIn(diagnostics.SOURCE_TAG, proc.stdout)
        self.assertNotIn(diagnostics.IMMUTABLE_REF, proc.stdout)

    def test_mismatch_invalid_digest_and_failed_read_are_bounded(self):
        proc, mismatch = self.invoke(FAKE_TAG_OUTPUT='sha256:' + '1' * 64)
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(mismatch['status'], 'mismatch')
        self.assertFalse(mismatch['tag_matches_immutable_digest'])
        proc, invalid = self.invoke(FAKE_TAG_OUTPUT='TEST_ONLY_PROVIDER_SECRET_SENTINEL')
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(invalid['status'], 'inconclusive')
        self.assertFalse(invalid['operations'][0]['digest_format_valid'])
        self.assertIsNone(invalid['tag_matches_immutable_digest'])
        proc, failed = self.invoke(FAKE_TAG_EXIT='17')
        self.assertEqual(proc.returncode, 1)
        self.assertEqual(failed['operations'][0]['exit_code'], 17)
        self.assertEqual(failed['operations'][1]['exit_code'], 0)
        for result in (mismatch, invalid, failed):
            self.assertNotIn('TEST_ONLY_PROVIDER_SECRET_SENTINEL', json.dumps(result))

    def test_actual_helper_rejects_old_source_and_other_dispatch_tuples_before_gcloud(self):
        invalid = [
            {'SOURCE': 'f6e6a5e588088294c8829192b308b0813356f9da'},
            {'TARGET': 'production'},
            {'WORKFLOW_REF': 'refs/heads/feature'},
            {'GITHUB_SHA': 'b' * 40},
            {'COMPONENTS': 'auth,bff'},
            {'ARTIFACT_ID': '11220235845'},
            {'RELEASE_TAG': 'some-release-tag'},
        ]
        for override in invalid:
            with self.subTest(override=override):
                self.calls.unlink(missing_ok=True)
                proc, result = self.invoke(**override)
                self.assertEqual(proc.returncode, 1)
                self.assertEqual(result, diagnostics.rejected_result())
                self.assertFalse(self.calls.exists(), 'rejected action branch invoked gcloud')
                self.assertNotIn('TEST_ONLY', proc.stdout + proc.stderr + json.dumps(result))

    def test_timeouts_and_missing_tool_have_only_fixed_timeout_classes(self):
        calls = []

        def timed_out(*args, **kwargs):
            calls.append(args[0])
            raise subprocess.TimeoutExpired(args[0], kwargs['timeout'])

        result = diagnostics.diagnose(timed_out)
        self.assertEqual(len(calls), 2)
        self.assertTrue(all(op['timeout_class'] == 'subprocess-timeout' and op['exit_code'] is None
                            for op in result['operations']))
        missing = diagnostics.diagnose(lambda *args, **kwargs: (_ for _ in ()).throw(OSError()))
        self.assertTrue(all(op['timeout_class'] == 'tool-unavailable' and op['exit_code'] is None
                            for op in missing['operations']))


if __name__ == '__main__':
    unittest.main(verbosity=2)
