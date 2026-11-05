"""Regression checks for schema-aware, payload-free Auth admission fixtures."""
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
GO = shutil.which('go') or 'go'
sys.path.insert(0, str(ROOT / 'scripts'))
from lwc_auth_test_fixture import write_auth_input_snapshot_fixture

OLD_SCOPES = ['mcp:query', 'mcp:raw:write']
OLD_SCOPE_SETS = [['mcp:query'], ['mcp:query', 'mcp:raw:write']]
NEW_SCOPES = ['mcp:query', 'mcp:content:read', 'mcp:raw:write']
NEW_SCOPE_SETS = [
    ['mcp:query'],
    ['mcp:query', 'mcp:content:read'],
    ['mcp:query', 'mcp:raw:write'],
    ['mcp:query', 'mcp:content:read', 'mcp:raw:write'],
]


def scope_set_membership(scope_sets):
    return {tuple(sorted(scope_set)) for scope_set in scope_sets}


class AuthInputFixtureTests(unittest.TestCase):
    def test_schema_fixtures_normalize_exact_catalogs_without_mutating_source_projection(self):
        env = dict(os.environ, LWC_REPOSITORY_ROOT=str(ROOT))
        with tempfile.TemporaryDirectory(prefix='lwc-schema-aware-fixture-') as temporary:
            root = Path(temporary)
            source_dir = root / 'source'
            source_dir.mkdir()
            subprocess.run(
                [GO, 'run', './cmd/pipeline_config', 'prepare', '--target', 'auth',
                 '--environment', 'dev', '--output', str(source_dir)],
                cwd=ROOT / 'apps/bff', env=env, text=True, capture_output=True, check=True,
            )
            source_path = source_dir / 'auth-source.json'
            source_bytes = source_path.read_bytes()
            source = json.loads(source_bytes)
            self.assertEqual(
                [scope['name'] for scope in source['oauth_resources'][0]['scopes']], NEW_SCOPES)

            for schema_version, want_scopes, want_sets in (
                    (2, OLD_SCOPES, OLD_SCOPE_SETS),
                    (3, NEW_SCOPES, NEW_SCOPE_SETS),
                    (4, NEW_SCOPES, NEW_SCOPE_SETS)):
                with self.subTest(schema_version=schema_version):
                    fixture_dir = root / f'schema-{schema_version}'
                    fixture_dir.mkdir()
                    fixture_source = fixture_dir / 'auth-source.json'
                    fixture_source.write_bytes(source_bytes)
                    auth_inputs_path = write_auth_input_snapshot_fixture(
                        fixture_dir, 'development', source_sha='c' * 40,
                        schema_version=schema_version)

                    self.assertEqual(fixture_source.read_bytes(), source_bytes)
                    inputs = json.loads(auth_inputs_path.read_text())
                    source_project = copy.deepcopy(source['oauth_resources'][0])
                    projected_project = inputs['oauth_resources'][0]
                    self.assertEqual(projected_project['id'], source_project['id'])
                    self.assertEqual([scope['name'] for scope in projected_project['scopes']], want_scopes)
                    self.assertEqual(scope_set_membership(projected_project['allowed_scope_sets']),
                                     scope_set_membership(want_sets))
                    self.assertEqual(projected_project['default_scope_set'], ['mcp:query'])
                    for field in ('scopes', 'allowed_scope_sets', 'default_scope_set'):
                        source_project.pop(field)
                    projected_other = copy.deepcopy(projected_project)
                    for field in ('scopes', 'allowed_scope_sets', 'default_scope_set'):
                        projected_other.pop(field)
                    self.assertEqual(projected_other, source_project)
                    self.assertEqual(inputs['oauth_resources'][1], source['oauth_resources'][1])

                    normalized = subprocess.run(
                        [GO, 'run', './cmd/deploy_config', '--environment', 'development',
                         '--config', str(ROOT / 'deploy/environments/development.yaml'),
                         '--components', 'auth', '--auth-inputs', str(auth_inputs_path)],
                        cwd=ROOT / 'apps/bff', env=env, text=True, capture_output=True,
                    )
                    self.assertEqual(normalized.returncode, 0, normalized.stdout + normalized.stderr)
                    runtime_inputs = json.loads(normalized.stdout)['auth']['runtime_inputs']
                    self.assertEqual(runtime_inputs['schema_version'], schema_version)
                    self.assertEqual(
                        [scope['name'] for scope in runtime_inputs['oauth_resources'][0]['scopes']],
                        want_scopes)
                    self.assertEqual(
                        scope_set_membership(runtime_inputs['oauth_resources'][0]['allowed_scope_sets']),
                        scope_set_membership(want_sets))
                    self.assertEqual(
                        runtime_inputs['oauth_resources'][0]['default_scope_set'], ['mcp:query'])
                    if schema_version < 4:
                        self.assertNotIn('portal', runtime_inputs)
                    else:
                        self.assertIn('portal', runtime_inputs)

    def test_schema_one_keeps_pre_registry_omission_semantics(self):
        env = dict(os.environ, LWC_REPOSITORY_ROOT=str(ROOT))
        with tempfile.TemporaryDirectory(prefix='lwc-schema-one-fixture-') as temporary:
            fixture_dir = Path(temporary)
            subprocess.run(
                [GO, 'run', './cmd/pipeline_config', 'prepare', '--target', 'auth',
                 '--environment', 'dev', '--output', str(fixture_dir)],
                cwd=ROOT / 'apps/bff', env=env, text=True, capture_output=True, check=True,
            )
            source_path = fixture_dir / 'auth-source.json'
            source_bytes = source_path.read_bytes()
            auth_inputs_path = write_auth_input_snapshot_fixture(
                fixture_dir, 'development', source_sha='c' * 40, schema_version=1)
            self.assertEqual(source_path.read_bytes(), source_bytes)
            inputs = json.loads(auth_inputs_path.read_text())
            self.assertNotIn('oauth_resources', inputs)
            self.assertNotIn('management_scopes', inputs)
            self.assertNotIn('consumer_verifiers', inputs)


if __name__ == '__main__':
    unittest.main()
