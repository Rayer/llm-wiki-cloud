"""Offline tests for selected Pipeline config delivery contracts."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(HERE))
import pipeline_config
import pipeline_config_only
import providers
from support import Breakpoint


RESOURCE = 'projects/llm-wiki-cloud/secrets/deepseek-apikey/versions/latest'


def synthetic_config(root, target):
    directory = Path(root) / target
    directory.mkdir(parents=True)
    secret = {'source': 'secret-manager', 'target': 'DEEPSEEK_API_KEY',
              'envName': '', 'resource': RESOURCE}
    (directory / 'pipeline.json').write_text(json.dumps({
        'environment': target, 'bucket': 'llm-wiki-data-dev',
        'runTimeoutSeconds': 321, 'secret': secret,
    }))
    (directory / 'private-bindings.json').write_text(json.dumps({
        'environment': target, 'bindings': [secret],
    }))
    (directory / 'synto.toml').write_text('[pipeline]\nrun_timeout_seconds = 321\n')


class PipelineConfigContract(unittest.TestCase):
    def test_full_secret_version_maps_to_cloud_run_reference_without_payload(self):
        secret = {'target': 'DEEPSEEK_API_KEY', 'resource': RESOURCE}
        self.assertEqual(pipeline_config.secret_cli_binding(secret),
                         ('DEEPSEEK_API_KEY', 'deepseek-apikey:latest'))
        self.assertEqual(pipeline_config.pipeline_config_uri('llm-wiki-data-dev'),
                         'gs://llm-wiki-data-dev/pipeline-config/synto.toml')

    def test_config_only_renders_uploads_and_verifies_without_changing_image(self):
        state = {
            'image': 'image@sha256:' + 'b' * 64,
            'timeout': '322',
            'env': [],
            'toml': None,
            'calls': [],
        }
        normalized = json.dumps({
            'gcp': {'project_id': 'llm-wiki-cloud'},
            'worker': {'job_name': 'olw-pipeline-dev', 'location': 'asia-east1',
                       'bucket': 'llm-wiki-data-dev'},
        })

        def command(args, **kwargs):
            args = [str(value) for value in args]
            state['calls'].append(args)
            if args[0] == 'make':
                output = Path(args[2].split('=', 1)[1])
                synthetic_config(output, 'dev')
                return ''
            if args[:3] == ['go', 'run', './cmd/deploy_config']:
                return normalized
            if args[:4] == ['gcloud', 'run', 'jobs', 'describe']:
                return json.dumps({'spec': {'template': {'spec': {'template': {'spec': {
                    'timeoutSeconds': state['timeout'],
                    'containers': [{'image': state['image'], 'env': state['env']}],
                }}}}}})
            if args[:3] == ['gcloud', 'storage', 'cp']:
                source, uri = Path(args[4]), args[5]
                self.assertEqual(uri, 'gs://llm-wiki-data-dev/pipeline-config/synto.toml')
                state['toml'] = source.read_bytes()
                return ''
            if args[:3] == ['gcloud', 'storage', 'cat']:
                return state['toml']
            if args[:4] == ['gcloud', 'run', 'jobs', 'update']:
                self.assertNotIn('--image', args)
                self.assertNotIn('docker', args)
                self.assertIn('--task-timeout', args)
                self.assertEqual(args[args.index('--update-secrets') + 1],
                                 'DEEPSEEK_API_KEY=deepseek-apikey:latest')
                state['timeout'] = '321'
                state['env'] = [{'name': 'DEEPSEEK_API_KEY', 'valueSource': {
                    'secretKeyRef': {'secret': 'projects/llm-wiki-cloud/secrets/deepseek-apikey',
                                     'version': 'latest'}}}]
                return '{}'
            self.fail('unexpected command: ' + ' '.join(args))

        output = io.StringIO()
        with tempfile.TemporaryDirectory() as root, contextlib.redirect_stdout(output):
            pipeline_config_only.run_config_only(
                'development', root=Path(root), run_command=command,
                process_env={'LWC_PIPELINE_RUN_TIMEOUT_SECONDS': '321'})
        self.assertTrue(state['toml'].endswith(b'\n'))
        self.assertIn('image_unchanged=true', output.getvalue())
        self.assertEqual(state['image'], 'image@sha256:' + 'b' * 64)
        self.assertEqual(state['timeout'], '321')
        self.assertFalse(any('docker' in args or 'build' in args or 'push' in args
                             for args in state['calls']))
        update = next(args for args in state['calls'] if args[:4] == ['gcloud', 'run', 'jobs', 'update'])
        self.assertNotIn('--image', update)
        self.assertEqual(hashlib.sha256(state['toml']).hexdigest(),
                         hashlib.sha256(b'[pipeline]\nrun_timeout_seconds = 321\n').hexdigest())

    def test_normal_prepare_does_not_gate_on_current_worker_timeout(self):
        provider = providers.Providers.__new__(providers.Providers)
        provider.p = {'environment': 'development',
                      'worker': {'bucket': 'llm-wiki-data-dev'}}
        provider.describe = lambda component: self.fail('config prepare must not add a live timeout gate')

        def command(args, **kwargs):
            self.assertEqual(args[:2], ['make', 'config-dev'])
            synthetic_config(Path(args[2].split('=', 1)[1]), 'dev')
            return ''

        with patch.object(providers, 'run', command):
            with patch.dict(os.environ, {'LWC_PIPELINE_RUN_TIMEOUT_SECONDS': '321'}):
                config = provider.prepare_pipeline_config()
        self.assertEqual(config['timeout_seconds'], 321)

    def test_config_only_updates_timeout_that_differs_from_current_job(self):
        state = {
            'image': 'image@sha256:' + 'b' * 64,
            'timeout': '322',
            'env': [],
            'toml': None,
        }
        normalized = json.dumps({
            'gcp': {'project_id': 'llm-wiki-cloud'},
            'worker': {'job_name': 'olw-pipeline-dev', 'location': 'asia-east1',
                       'bucket': 'llm-wiki-data-dev'},
        })

        def command(args, **kwargs):
            args = [str(value) for value in args]
            if args[0] == 'make':
                synthetic_config(Path(args[2].split('=', 1)[1]), 'dev')
                return ''
            if args[:3] == ['go', 'run', './cmd/deploy_config']:
                return normalized
            if args[:4] == ['gcloud', 'run', 'jobs', 'describe']:
                return json.dumps({'spec': {'template': {'spec': {'template': {'spec': {
                    'timeoutSeconds': state['timeout'],
                    'containers': [{'image': state['image'], 'env': state['env']}],
                }}}}}})
            if args[:3] == ['gcloud', 'storage', 'cp']:
                state['toml'] = Path(args[4]).read_bytes()
                return ''
            if args[:3] == ['gcloud', 'storage', 'cat']:
                return state['toml']
            if args[:4] == ['gcloud', 'run', 'jobs', 'update']:
                self.assertEqual(args[args.index('--task-timeout') + 1], '321s')
                state['timeout'] = '321'
                state['env'] = [{'name': 'DEEPSEEK_API_KEY', 'valueSource': {
                    'secretKeyRef': {'secret': 'projects/llm-wiki-cloud/secrets/deepseek-apikey',
                                     'version': 'latest'}}}]
                return '{}'
            self.fail('unexpected command: ' + ' '.join(args))

        with tempfile.TemporaryDirectory() as root:
            pipeline_config_only.run_config_only(
                'development', root=Path(root), run_command=command,
                process_env={'LWC_PIPELINE_RUN_TIMEOUT_SECONDS': '321'})
        self.assertEqual(state['timeout'], '321')
        self.assertEqual(state['image'], 'image@sha256:'+'b'*64)

    def test_missing_verified_timeout_stops_before_prepare_or_mutation(self):
        calls = []
        with self.assertRaisesRegex(Breakpoint, 'verified-pipeline-run-timeout-required'):
            pipeline_config_only.run_config_only(
                'production', run_command=lambda *args, **kwargs: calls.append(args),
                process_env={})
        self.assertEqual(calls, [])


if __name__ == '__main__':
    unittest.main()
