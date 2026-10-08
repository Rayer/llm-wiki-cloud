#!/usr/bin/env python3
"""Auth/BFF configuration contract: no credential payloads are emitted or persisted."""
import hashlib
import json
from pathlib import Path
import re
import sys

GOOGLE = {
    'GOOGLE_CLIENT_ID': 'client_id', 'GOOGLE_ISSUER': 'issuer',
    'GOOGLE_JWKS_URL': 'jwks_url', 'GOOGLE_TOKEN_URL': 'token_url',
    'GOOGLE_LOGIN_REDIRECT_URL': 'login_redirect_url',
    'GOOGLE_LINK_REDIRECT_URL': 'link_redirect_url',
    'GOOGLE_COMPLETION_URL': 'completion_url',
}
BASE = ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID', 'ALLOWED_HOSTS', 'ALLOWED_ORIGINS',
        'AUTH_SERVICE_URL', 'AUTH_SESSION_ENVIRONMENT', 'AUTH_REFRESH_SESSION_MIGRATION',
        'AUTH_DEMO_USER_ID', 'AUTH_DEMO_USER_EMAIL', 'AUTH_DEMO_USER_ROLE', 'DEV_JWT')
SECRET = ('JWT_SECRET', 'GOOGLE_CLIENT_SECRET')
QUERY_PATH = 'QUERY_STAGE_CONFIG_PATH'
EXPORT_BFF = ('EXPORT_JOB_URL', 'EXPORT_SIGNING_SERVICE_ACCOUNT')
PROFILE_RUNTIME_BFF = ('PROFILE_RUNTIME_AUDIENCE', 'PROFILE_RUNTIME_SERVICE_ACCOUNT')
TYPESAFE_JEV_API_KEY = 'TYPESAFE_JEV_API_KEY'
PIPELINE_DEMO_USER_IDS = 'PIPELINE_DEMO_USER_IDS'
PIPELINE_COOLDOWN_SECONDS = 'PIPELINE_COOLDOWN_SECONDS'
MAX_PIPELINE_COOLDOWN_SECONDS = ((1 << 63) - 1) // 1_000_000_000
BFF_CONFIG_ENV = 'LWC_BFF_CONFIG_PATH'
BFF_CONFIG_DIRECTORY = '/etc/lwc-bff-config'
BFF_CONFIG_FILE = 'bff.json'
BFF_CONFIG_PATH = BFF_CONFIG_DIRECTORY + '/' + BFF_CONFIG_FILE
BFF_SECRET_MODE = 0o444
BFF_LEGACY_ENV = (
    'GCP_PROJECT', 'BUCKET', 'FIRESTORE_DATABASE_ID', 'PIPELINE_JOB_URL', 'AUTH_SERVICE_URL',
    'EXPORT_JOB_URL', 'EXPORT_SIGNING_SERVICE_ACCOUNT', 'ALLOWED_ORIGINS', 'ALLOWED_HOSTS',
    'PIPELINE_DAILY_LIMIT', 'PIPELINE_COOLDOWN_SECONDS', 'PIPELINE_MIN_NEW_RAW',
    'PIPELINE_DEMO_USER_IDS', 'AUTH_SESSION_ENVIRONMENT', 'AUTH_REFRESH_SESSION_MIGRATION',
    'REGISTRATION_ENABLED', 'PROFILE_RUNTIME_AUDIENCE', 'PROFILE_RUNTIME_SERVICE_ACCOUNT',
    'QUERY_STAGE_CONFIG_PATH', 'QUERY_EXPANSION_MODEL', 'QUERY_EXPANSION_REASONING',
    'ANSWER_SYNTHESIS_MODEL', 'ANSWER_SYNTHESIS_REASONING', 'QUERY_SELECTION_LIMIT',
    'QUERY_SELECTION_EXPLORATION_SLOTS', 'QUERY_SELECTION_EVIDENCE_THRESHOLD',
    'QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT', 'QUERY_EXPANSION_ATTEMPTS',
    'QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY', 'DEV_JWT',
)
BFF_LEGACY_SECRET_ENV = ('JWT_SECRET', 'DEEPSEEK_API_KEY', 'TYPESAFE_JEV_API_KEY')
SECRET_RESOURCE = re.compile(r'^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+$')


def require(condition):
    if not condition:
        raise ValueError("contract mismatch")


def query_path(plan):
    # deploy_config already validates sealed canonical bytes. Bind every plan
    # identity to that repository artifact before using its runtime path.
    query = plan['query_config']
    path = query['repository_path']
    require(isinstance(path, str) and re.fullmatch(r'apps/bff/configs/query/[A-Za-z0-9_./-]+\.json', path))
    require(all(part not in ('', '.', '..') for part in path.split('/')))
    root = Path(__file__).resolve().parents[2]
    artifact = root / path
    require(artifact.resolve() == artifact and artifact.is_file())
    with artifact.open() as stream:
        config = json.load(stream)
    require(query == {
        'repository_path': path, 'runtime_path': '/app/' + path.removeprefix('apps/bff/'),
        'schema_version': config['schema_version'], 'revision': config['config_revision'],
        'digest': config['config_digest'],
    })
    require(plan['bff']['query_config'] == path and plan['components']['bff']['query_config'] == query)
    return query['runtime_path']


def desired(plan, component='auth', bff_config_version=None):
    require(plan['environment'] in ('development', 'production'))
    require(component in ('auth', 'bff'))
    if component == 'bff':
        bff = plan['bff']
        inputs = bff.get('runtime_inputs')
        require(isinstance(inputs, dict) and isinstance(inputs.get('config_secret_resource'), str)
                and SECRET_RESOURCE.fullmatch(inputs['config_secret_resource']))
        require(isinstance(bff_config_version, str) and re.fullmatch(r'[1-9][0-9]*', bff_config_version))
        return {
            'env': {BFF_CONFIG_ENV: BFF_CONFIG_PATH},
            'secrets': {},
            'file_secret': {
                'resource': inputs['config_secret_resource'],
                'version': bff_config_version,
                'directory': BFF_CONFIG_DIRECTORY,
                'file': BFF_CONFIG_FILE,
                'mode': BFF_SECRET_MODE,
            },
            'service_account': bff['runtime_service_account'],
        }
    auth = plan['auth']
    google = auth['google']
    demo_user_id = auth.get('demo_user_id', '')
    demo_user_email = auth.get('demo_user_email', '')
    demo_user_role = auth.get('demo_user_role', '')
    require(isinstance(demo_user_id, str) and re.fullmatch(r'[A-Za-z0-9_-]{1,128}', demo_user_id))
    require(isinstance(demo_user_email, str) and re.fullmatch(r'[^@\s]+@[^@\s]+\.[^@\s]+', demo_user_email))
    require(isinstance(demo_user_role, str) and re.fullmatch(r'[a-z][a-z0-9_-]{0,31}', demo_user_role)
            and demo_user_role != 'admin')
    env = dict(zip(BASE, (
        plan['gcp']['project_id'], auth['firestore_database_id'],
        ','.join(auth['allowed_hosts']), ','.join(auth['allowed_origins']),
        'https://' + auth['public_domain'], auth['firestore_database_id'], 'disabled', demo_user_id,
        demo_user_email, demo_user_role, 'false',
    )))
    secrets = {'JWT_SECRET': {'name': auth['secret_references']['jwt'], 'key': 'latest'}}
    require(type(google['enabled']) is bool)
    if google['enabled']:
        env.update({key: google[field] for key, field in GOOGLE.items()})
        secrets['GOOGLE_CLIENT_SECRET'] = {
            'name': google['client_secret_reference'], 'key': google['client_secret_version'],
        }
    return {'env': env, 'secrets': secrets, 'service_account': auth['runtime_service_account']}


def effective(revision, project, component='auth', query_only=False, selective_bff=False,
              include_runtime_bindings=False, manage_demo_user_ids=False, manage_export_bindings=False,
              manage_pipeline_cooldown=False):
    containers = revision['spec']['containers']
    require(len(containers) == 1)
    result = {'env': {}, 'secrets': {}, 'service_account': revision['spec']['serviceAccountName']}
    aliases = {}
    for binding in revision['metadata'].get('annotations', {}).get('run.googleapis.com/secrets', '').split(','):
        if binding:
            alias, target = binding.split(':', 1)
            require(alias not in aliases)
            aliases[alias] = target
    seen = set()
    for entry in containers[0].get('env', []):
        name = entry['name']
        require(name not in seen)
        seen.add(name)
        if component == 'bff' and name in BFF_LEGACY_ENV + BFF_LEGACY_SECRET_ENV:
            raise ValueError('unexpected legacy BFF config binding')
        if ((name in SECRET and not query_only and not selective_bff) or
                (component == 'bff' and include_runtime_bindings and name == TYPESAFE_JEV_API_KEY)):
            # Reject literal credentials without printing or retaining them.
            require(set(entry) == {'name', 'valueFrom'})
            ref = entry['valueFrom']['secretKeyRef']
            require(set(ref) == {'name', 'key'})
            if ref['name'] in aliases:
                parts = aliases[ref['name']].split('/')
                require(len(parts) == 4 and parts[0] == 'projects' and parts[2] == 'secrets')
                # The authenticated, project-scoped revision response supplies its
                # numeric namespace; accept that or the configured project ID.
                require(parts[1] in (project, revision['metadata'].get('namespace')))
                ref = {'name': parts[3], 'key': ref['key']}
            result['secrets'][name] = ref
        elif component == 'bff' and include_runtime_bindings and name in PROFILE_RUNTIME_BFF:
            require(not query_only and set(entry) == {'name', 'value'} and isinstance(entry['value'], str))
            result['env'][name] = entry['value']
        elif component == 'bff' and manage_export_bindings and name in EXPORT_BFF:
            require(not query_only and set(entry) == {'name', 'value'} and isinstance(entry['value'], str))
            result['env'][name] = entry['value']
        elif component == 'bff' and manage_demo_user_ids and name == PIPELINE_DEMO_USER_IDS:
            require(not query_only and set(entry) == {'name', 'value'} and isinstance(entry['value'], str))
            result['env'][name] = entry['value']
        elif component == 'bff' and name == PIPELINE_COOLDOWN_SECONDS and manage_pipeline_cooldown:
            require(not query_only and set(entry) == {'name', 'value'} and
                    isinstance(entry['value'], str) and re.fullmatch(r'[1-9][0-9]*', entry['value']) and
                    int(entry['value']) <= MAX_PIPELINE_COOLDOWN_SECONDS)
            result['env'][name] = entry['value']
        elif component == 'bff' and name == BFF_CONFIG_ENV:
            require(set(entry) == {'name', 'value'} and entry.get('value') == BFF_CONFIG_PATH)
            result['env'][name] = entry['value']
        elif ((name in BASE or name in GOOGLE) and not query_only and not selective_bff) or (component == 'bff' and name == QUERY_PATH):
            omitted_empty_demo = name == 'AUTH_DEMO_USER_ID' and set(entry) == {'name'}
            require(omitted_empty_demo or set(entry) == {'name', 'value'})
            value = entry.get('value', '')
            require(isinstance(value, str))
            result['env'][name] = value
        elif component == 'bff' and (name in PROFILE_RUNTIME_BFF or name == TYPESAFE_JEV_API_KEY):
            raise ValueError('unexpected Profile runtime binding')
        elif name.startswith('GOOGLE_') and not query_only and not selective_bff:
            raise ValueError('unexpected Google variable')
    if component == 'bff':
        result['file_secret'] = native_bff_file_binding(revision, project)
    return result


def native_bff_file_binding(revision, project):
    spec = revision['spec']
    containers = spec.get('containers', [])
    require(len(containers) == 1)
    mounts = [mount for mount in containers[0].get('volumeMounts', [])
              if mount.get('mountPath') == BFF_CONFIG_DIRECTORY]
    require(len(mounts) == 1)
    mount = mounts[0]
    require(isinstance(mount.get('name'), str) and mount.get('name') and mount.get('readOnly') is True)
    volumes = [volume for volume in spec.get('volumes', []) if volume.get('name') == mount['name']]
    require(len(volumes) == 1)
    secret = volumes[0].get('secret')
    require(isinstance(secret, dict))
    secret_name = secret.get('secretName')
    require(isinstance(secret_name, str) and secret_name)
    aliases = {}
    annotations = revision.get('metadata', {}).get('annotations', {})
    for binding in annotations.get('run.googleapis.com/secrets', '').split(','):
        if binding:
            try:
                alias, target = binding.split(':', 1)
            except ValueError:
                continue
            if alias and SECRET_RESOURCE.fullmatch(target) and alias not in aliases:
                aliases[alias] = target
    resource = aliases.get(secret_name, secret_name)
    require(SECRET_RESOURCE.fullmatch(resource) and resource.split('/')[1] in
            (project, revision['metadata'].get('namespace')))
    items = secret.get('items', [])
    require(isinstance(items, list) and len(items) == 1)
    item = items[0]
    require(isinstance(item, dict) and item.get('path') == BFF_CONFIG_FILE)
    version = item.get('key')
    require(isinstance(version, str) and re.fullmatch(r'[1-9][0-9]*', version))
    mode = item.get('mode', BFF_SECRET_MODE)
    require(type(mode) is int and mode == BFF_SECRET_MODE)
    return {
        'resource': resource,
        'version': version,
        'directory': BFF_CONFIG_DIRECTORY,
        'file': BFF_CONFIG_FILE,
        'mode': mode,
    }


def fingerprint(config):
    return 'sha256:' + hashlib.sha256(json.dumps(config, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def main():
    mode, path, component = sys.argv[1:4]
    with open(path) as stream:
        plan = json.load(stream)['normalized']
    if mode == 'version':
        require(component == 'bff')
        revision = json.load(sys.stdin)
        revision_name = revision.get('metadata', {}).get('name')
        require(isinstance(revision_name, str) and
                revision_name.startswith(plan['bff']['service_name'] + '-'))
        conditions = revision.get('status', {}).get('conditions')
        require(isinstance(conditions, list) and
                any(isinstance(c, dict) and c.get('type') == 'Ready' and c.get('status') == 'True'
                    for c in conditions))
        binding = native_bff_file_binding(revision, plan['gcp']['project_id'])
        print(binding['version'])
        return
    if mode == 'args':
        expected = desired(plan, component, sys.argv[4] if component == 'bff' else None)
        values = expected['env']
        require(all('\n' not in v and '|' not in v for v in values.values()))
        args = ['--update-env-vars', '^|^' + '|'.join(k + '=' + v for k, v in values.items())]
        if expected['secrets']:
            args += ['--update-secrets', ','.join(k + '=' + v['name'] + ':' + v['key'] for k, v in expected['secrets'].items())]
        if component == 'auth' and not plan['auth']['google']['enabled']:
            args += ['--remove-env-vars', ','.join(GOOGLE), '--remove-secrets', 'GOOGLE_CLIENT_SECRET']
        if component == 'bff':
            file_secret = expected['file_secret']
            secret_name = file_secret['resource'].split('/')[3]
            args += ['--service-account', expected['service_account'], '--update-secrets',
                     BFF_CONFIG_PATH + '=' + secret_name + ':' + file_secret['version'],
                     '--remove-env-vars', ','.join(BFF_LEGACY_ENV),
                     '--remove-secrets', ','.join(BFF_LEGACY_SECRET_ENV)]
        print('\n'.join(args))
        return
    config_version = sys.argv[7] if component == 'bff' and len(sys.argv) > 7 else None
    expected = desired(plan, component, config_version)
    revision = json.load(sys.stdin)
    require(revision['metadata']['name'] == sys.argv[4])
    if component == 'bff':
        require(sys.argv[4].startswith(plan['bff']['service_name'] + '-'))
    require(revision['status']['imageDigest'] == sys.argv[5])
    require(revision['spec']['containers'][0]['image'] == sys.argv[5])
    require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in revision['status']['conditions']))
    actual = effective(revision, plan['gcp']['project_id'], component,
                       set(expected['env']) == {QUERY_PATH},
                       component == 'bff' and (plan['environment'] == 'development' or plan['auth'].get('google') is None),
                       component == 'bff',
                       component == 'bff' and PIPELINE_DEMO_USER_IDS in expected['env'],
                       component == 'bff' and (plan['environment'] == 'development' or plan['export_job']['enabled']),
                       component == 'bff' and PIPELINE_COOLDOWN_SECONDS in expected['env'])
    digest = fingerprint(actual)
    if component == 'bff':
        # Pin all retained revision settings, including unrelated env/secrets and
        # network annotations, without copying their values into evidence.
        digest = fingerprint({'spec': revision['spec'], 'annotations': revision['metadata'].get('annotations', {})})
    if mode == 'verify':
        require(actual == expected)
    elif mode == 'rollback':
        require(digest == sys.argv[6])
    elif mode != 'freeze':
        raise ValueError('unknown mode')
    print(json.dumps({'image': sys.argv[5], 'revision': sys.argv[4], 'config_fingerprint': digest, 'ready': True}))


if __name__ == '__main__':
    try:
        main()
    except (AssertionError, KeyError, ValueError, TypeError, IndexError, OSError):
        sys.exit('Auth config readback/contract mismatch (values suppressed)')
