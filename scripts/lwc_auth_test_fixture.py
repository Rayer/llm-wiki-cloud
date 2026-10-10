"""Synthetic, payload-free Auth Stage 1 fixtures for offline deployment tests."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess


ROOT = Path(__file__).resolve().parents[1]
GO = shutil.which('go') or 'go'


def write_auth_input_fixture(directory, environment, source_sha='c' * 40):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    target = {'development': 'dev', 'production': 'prod'}[environment]
    env = dict(os.environ, LWC_REPOSITORY_ROOT=str(ROOT))
    subprocess.run(
        [GO, 'run', './cmd/pipeline_config', 'prepare', '--target', 'auth',
         '--environment', target, '--output', str(directory)],
        cwd=ROOT / 'apps/bff', env=env, text=True, capture_output=True, check=True,
    )
    return write_auth_input_snapshot_fixture(directory, environment, source_sha)


def write_auth_input_snapshot_fixture(directory, environment, source_sha='c' * 40, source=None):
    """Build payload-free numeric Auth refs for tests without Secret Manager access."""
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    target = {'development': 'dev', 'production': 'prod'}[environment]
    if source is None:
        source = json.loads((directory / 'auth-source.json').read_text())
    jwt = source['jwt_secret_reference'].rsplit('/versions/', 1)[0] + '/versions/17'
    google_source = source['google']
    google = {
        'enabled': google_source['enabled'],
        'client_id': google_source['client_id'],
        'client_secret_version': (google_source['client_secret_reference'].rsplit('/versions/', 1)[0] + '/versions/7'
                                  if google_source['enabled'] else ''),
        'issuer': google_source['issuer'],
        'jwks_url': google_source['jwks_url'],
        'token_url': google_source['token_url'],
        'login_redirect_url': google_source['login_redirect_url'],
        'link_redirect_url': google_source['link_redirect_url'],
        'completion_url': google_source['completion_url'],
    }
    inputs = {
        'schema_version': 1,
        'environment': target,
        'target': 'auth',
        'source_sha': source_sha,
        'config_id': '',
        'gcp_project': source['gcp_project'],
        'firestore_database_id': source['firestore_database_id'],
        'local_cloud_scope': source['local_cloud_scope'],
        'auth_service_url': source['auth_service_url'],
        'sync_service_url': source['sync_service_url'],
        'allowed_hosts': source['allowed_hosts'],
        'allowed_origins': source['allowed_origins'],
        'auth_session_environment': source['auth_session_environment'],
        'auth_session_migration': source['auth_session_migration'],
        'registration_enabled': source['registration_enabled'],
        'auth_demo_user_id': source['auth_demo_user_id'],
        'auth_demo_user_email': source['auth_demo_user_email'],
        'auth_demo_user_role': source['auth_demo_user_role'],
        'jwt_secret_version': jwt,
        'google': google,
        'config_secret_resource': source['config_secret_resource'],
    }
    canonical = json.dumps(inputs, ensure_ascii=False, separators=(',', ':')).encode()
    inputs['config_id'] = 'sha256:' + hashlib.sha256(canonical).hexdigest()
    path = directory / 'auth-inputs.json'
    path.write_text(json.dumps(inputs, ensure_ascii=False, separators=(',', ':')) + '\n')
    path.chmod(0o600)
    return path
