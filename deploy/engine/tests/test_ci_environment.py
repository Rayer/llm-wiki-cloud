"""TEST ONLY: reproduce an Actions parent environment without network access."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class OfflineCIEnvironment(unittest.TestCase):
    def test_acceptance_does_not_inherit_actions_transport_or_credentials(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            node = root / 'node'
            marker = root / 'transport-called'
            node.write_text('#!/bin/sh\nprintf called > "' + str(marker) + '"\nexit 91\n')
            node.chmod(0o755)
            env = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'],
                       GITHUB_ACTIONS='true', GITHUB_RUN_ID='42', GITHUB_RUN_ATTEMPT='1',
                       ACTIONS_RUNTIME_TOKEN='TEST_ONLY', GH_TOKEN='TEST_ONLY')
            result = subprocess.run(
                [sys.executable, '-m', 'unittest',
                 'test_engine.Acceptance.test_01_stage_barrier_and_retry_retains_first',
                 'test_engine.Acceptance.test_offline_fixture_has_no_inherited_authority'],
                cwd=Path(__file__).parent, env=env, capture_output=True, text=True, timeout=60)
            self.assertFalse(marker.exists(), 'offline acceptance reached artifact transport')
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
