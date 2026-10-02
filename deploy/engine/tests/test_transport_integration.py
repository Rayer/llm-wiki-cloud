"""TEST ONLY: actual Engine argv and subprocess into actual artifacts.cjs parser."""
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from engine import Engine, engine_fingerprint
from support import digest, read, write


class EngineTransportIntegration(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.directory = self.root / 'release'
        plan = {'selected':['worker'], 'normalized':{'environment':'development'},
                'engine_content':engine_fingerprint()}
        plan['id'] = digest(plan)
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


if __name__ == '__main__':
    unittest.main(verbosity=2)
