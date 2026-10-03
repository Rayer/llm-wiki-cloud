"""Offline reproducer for the safe-result loss in Frontend prepare input handling.

Uses production engine.main/Engine.prepare/Providers.prepare/Providers.project/
Providers.api/support.run. Only support.subprocess.run and Auth receipt usability
are faked; no provider or network request is made.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
SCRATCH = Path('/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch')
sys.path.insert(0, str(ROOT / 'deploy/engine'))
import engine
import providers
from support import digest, write


CASES = (
    ('json-object-missing-id', '{}'),
    ('wrong-repository-field-type', '{"link":{"repo":["fixture"]}}'),
    ('invalid-json', 'not-json'),
)


def run_case(label, payload):
    # The checkout is imported read-only; all generated fixtures live in the
    # designated profile scratch directory and are removed on context exit.
    with tempfile.TemporaryDirectory(dir=SCRATCH) as scratch:
        directory = Path(scratch) / 'release'
        directory.mkdir()
        normalized = {
            'environment': 'development',
            'gcp': {'project_id': 'fixture-project',
                    'artifact_registry': 'registry.invalid/images'},
            'frontend': {'project_name': 'fixture-frontend',
                         'team_slug': 'fixture-team',
                         'repository': 'fixture/repo',
                         'root_directory': 'apps/frontend',
                         'api_url': 'https://api.invalid',
                         'auth_url': 'https://auth.invalid',
                         'stable_aliases': []},
        }
        identities = {
            'auth': {'profile': 'fixture', 'inputs': 'a' * 64, 'files': []},
            'frontend': {'profile': 'fixture', 'inputs': 'b' * 64, 'files': []},
        }
        plan = {
            'schema': 2,
            'source': 'd' * 40,
            'branch': 'develop',
            'tag': 'offline-fixture',
            'engine': 'e' * 40,
            'engine_content': 'f' * 64,
            'normalized': normalized,
            'identities': identities,
            'dev_reference': None,
            'selected': ['auth', 'frontend'],
        }
        plan['id'] = digest(plan)
        write(directory / 'plan.json', plan)
        write(directory / 'state.json', {
            'plan': plan['id'], 'status': 'prepared', 'components': {},
            'sequence': 1, 'builds': {},
        })
        write(directory / 'receipts/auth.json', {
            'schema': 2,
            'component': 'auth',
            'identity': identities['auth'],
            'build_sha': plan['source'],
            'artifact': {'image': 'registry.invalid/images/auth@sha256:' + 'a' * 64},
            'target_config': None,
        })

        observed = {}
        fake_child_labels = []
        original_project = providers.Providers.project

        def observe_project_exception(self, stage=None):
            try:
                return original_project(self, stage=stage)
            except Exception as exc:
                # Record only the Python class and fixed boundary label.
                observed['class'] = type(exc).__name__
                observed['boundary'] = 'frontend-project-readback'
                raise

        def fake_subprocess(args, **_kwargs):
            # Do not retain arguments, environment, input, or fake response data.
            fake_child_labels.append(str(args[0]))
            return subprocess.CompletedProcess(args, 0, stdout=payload, stderr='')

        fake_environment = {
            'PATH': '/usr/bin:/bin',
            'HOME': scratch,
            'TMPDIR': scratch,
            'VERCEL_PROJECT_ID': 'prj_Fixture123',
            'VERCEL_TEAM_ID': 'team_Fixture123',
            'VERCEL_TOKEN': 'TEST_ONLY_FAKE_TOKEN',
        }
        stdout = io.StringIO()
        with patch.dict(os.environ, fake_environment, clear=True), \
                patch('support.subprocess.run', side_effect=fake_subprocess), \
                patch.object(providers.Providers, 'usable', return_value=None), \
                patch.object(providers.Providers, 'project', observe_project_exception), \
                patch.object(sys, 'argv', [
                    'engine.py', 'prepare', '--directory', str(directory),
                    '--environment', 'development', '--source', plan['source'],
                    '--tag', plan['tag'], '--components', 'auth,frontend',
                ]), contextlib.redirect_stdout(stdout):
            exit_code = engine.main()

        result = json.loads(stdout.getvalue())
        assert exit_code == 1
        assert observed['boundary'] == 'frontend-project-readback'
        assert result['status'] == 'failed'
        assert result['reason'] == 'invalid-or-unreadable-input'
        assert result['stage'] == 'prepared'
        assert result['component'] == 'frontend'
        assert result['last_verified_checkpoint'] == 1
        assert result.get('failure_diagnostic') is None
        assert fake_child_labels == ['curl']
        print(
            f"case={label} underlying_class={observed['class']} "
            f"boundary={observed['boundary']} fake_child=curl/exit0 "
            f"result={result['reason']} stage={result['stage']} "
            f"component={result['component']} checkpoint={result['last_verified_checkpoint']} "
            "failure_diagnostic=absent"
        )


if __name__ == '__main__':
    for case in CASES:
        run_case(*case)
