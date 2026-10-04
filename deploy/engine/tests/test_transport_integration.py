"""TEST ONLY: actual Engine argv and subprocess into actual artifacts.cjs parser."""
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

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from engine import Engine, release_identity
from support import digest, read, write


class EngineTransportIntegration(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.directory = self.root / 'release'
        plan = {'schema':3,'selected':['worker'], 'tag':'test-release', 'source':'a' * 40,
                'branch':'develop','dev_reference':None,'identities':{'worker':{}},
                'normalized':{'environment':'development'},'executor_sha':'e' * 40}
        plan['id'] = digest(release_identity(plan))
        write(self.directory / 'plan.json', plan)
        self.engine = Engine(self.directory)
        self.engine.state['status'] = 'snapshotted'
        self.stub_state = self.root / 'sdk.json'
        write(self.stub_state, {'argv':[], 'uploads':[], 'downloads':[], 'requests':[],
                               'checkpoint':self.engine.state})
        preload = Path(__file__).with_name('transport_sdk_stub.cjs')
        env = patch.dict(os.environ, {
            'GITHUB_ACTIONS':'true', 'GITHUB_RUN_ID':'42', 'GITHUB_RUN_ATTEMPT':'1',
            'GITHUB_REPOSITORY':'test/repo', 'GH_TOKEN':'TEST_ONLY',
            'NODE_OPTIONS':f'--require="{preload}"',
            'LWC_TRANSPORT_TEST_STATE':str(self.stub_state),
        })
        env.start()
        self.addCleanup(env.stop)

    def test_save_reaches_sdk_with_actual_engine_upload_argv(self):
        self.engine.save()  # No engine.run or subprocess mock.
        state = read(self.stub_state)
        name = f'lwc-state-development-{self.engine.plan["id"][:16]}-42-1-1'
        self.assertEqual(state['argv'], [['upload', str(self.directory), name]])
        self.assertEqual(len(state['uploads']), 1)
        self.assertEqual(state['uploads'][0]['name'], name)
        self.assertEqual(state['uploads'][0]['checkpoint'], self.engine.state)
        self.assertEqual(state['requests'], [])

    def test_runtime_guard_reads_checkpoint_with_actual_engine_latest_argv(self):
        self.engine.runtime_guard()  # Real subprocess, parser, fetch routing and file readback.
        state = read(self.stub_state)
        self.assertEqual(state['argv'], [['latest', 'development', str(self.directory / '.latest.json')]])
        self.assertEqual(len(state['requests']), 3)
        self.assertEqual(state['downloads'], [12])
        self.assertEqual(read(self.directory / '.latest.json'), self.engine.state)
        self.assertEqual(state['uploads'], [])

    def test_runtime_guard_uses_sequence_within_attempt_when_ids_disagree(self):
        run_id, attempt = 42, 1
        prefix = self.engine.plan['id'][:16]
        sequence9 = {'id': 11285623722,
                     'name': f'lwc-state-development-{prefix}-{run_id}-{attempt}-9',
                     'expired': False, 'workflow_run': {'id': run_id}}
        sequence5 = {'id': 11286159266,
                     'name': f'lwc-state-development-{prefix}-{run_id}-{attempt}-5',
                     'expired': False, 'workflow_run': {'id': run_id}}
        self.engine.state.update(sequence=9, status='deploying')
        state9 = {**self.engine.state, 'status':'unknown'}
        state5 = {**self.engine.state, 'sequence':5, 'status':'deploying'}
        write(self.stub_state, {
            'argv': [], 'uploads': [], 'downloads': [], 'requests': [],
            'checkpoint':self.engine.state,
            'artifact_checkpoints': {str(sequence9['id']):state9, str(sequence5['id']):state5},
            'responses': {
                'actions/artifacts?per_page=100&page=1': {
                    'total_count':2, 'artifacts':[sequence5, sequence9]},
                f"actions/artifacts/{sequence9['id']}":sequence9,
                f"actions/artifacts/{sequence5['id']}":sequence5,
                f'actions/runs/{run_id}': {
                    'event':'workflow_dispatch', 'path':'.github/workflows/deploy-dev.yml'},
            },
        })

        self.engine.runtime_guard('deploy')

        state = read(self.stub_state)
        self.assertEqual(state['downloads'], [sequence9['id']])
        self.assertEqual(read(self.directory / '.latest.json'), state9)
        self.assertEqual(state['uploads'], [])

    def test_latest_page_failure_reaches_engine_result_with_original_cause(self):
        first = [{'id': i, 'name': f'unrelated-{i}'} for i in range(1, 101)]
        write(self.stub_state, {
            'argv': [], 'uploads': [], 'downloads': [], 'requests': [],
            'checkpoint': self.engine.state,
            'responses': {
                'actions/artifacts?per_page=100&page=1': {'total_count': 101, 'artifacts': first},
                'actions/artifacts?per_page=100&page=2': {'total_count': 101, 'artifacts': []},
            },
        })
        stdout = io.StringIO()
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true'}), \
             patch('sys.argv', ['engine.py', 'deploy', '--directory', str(self.directory)]), \
             contextlib.redirect_stdout(stdout):
            self.assertEqual(self._main(), 1)
        state = read(self.stub_state)
        result = read(self.directory / 'result.json')
        self.assertEqual(state['argv'], [['latest', 'development', str(self.directory / '.latest.json')]])
        self.assertEqual(len(state['requests']), 2)
        self.assertEqual(state['downloads'], [])
        self.assertEqual((result['reason'], result['status'], result['mutation_may_have_happened']),
                         ('command-failed', 'failed', False))
        self.assertEqual(result['failure_diagnostic'], {
            'stage': 'latest-checkpoint', 'exit_code': 1, 'timeout_class': None})
        self.assertEqual(result['allowed_next_action'], 'reconcile-before-replay')
        self.assertEqual(result['cause'], {
            'exception_type': 'Error', 'exception_type_omitted': False,
            'stage': 'latest-checkpoint', 'code': 'child-command-failed',
            'message': 'artifact list page coverage mismatch',
            'message_truncated': False, 'message_omitted': False,
        })
        self.assertEqual(json.loads(stdout.getvalue())['cause'], result['cause'])

    def test_latest_subprocess_timeout_keeps_fixed_stage_and_timeout_class(self):
        stdout = io.StringIO()
        timeout = subprocess.TimeoutExpired('node artifacts.cjs latest', 120, stderr=b'')
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true'}), \
             patch('sys.argv', ['engine.py', 'deploy', '--directory', str(self.directory)]), \
             patch('support.subprocess.run', side_effect=timeout), \
             contextlib.redirect_stdout(stdout):
            self.assertEqual(self._main(), 1)
        result = read(self.directory / 'result.json')
        self.assertEqual(result['reason'], 'provider-timeout')
        self.assertEqual(result['failure_diagnostic'], {
            'stage': 'latest-checkpoint', 'exit_code': None,
            'timeout_class': 'subprocess-timeout'})
        self.assertEqual(result['cause']['exception_type'], 'TimeoutExpired')
        self.assertEqual(result['cause']['code'], 'child-command-timeout')
        self.assertEqual(result['cause']['stage'], 'latest-checkpoint')
        self.assertEqual(result['allowed_next_action'], 'reconcile-before-replay')
        self.assertEqual(json.loads(stdout.getvalue())['cause'], result['cause'])

    @staticmethod
    def _main():
        from engine import main
        return main()


if __name__ == '__main__':
    unittest.main(verbosity=2)
