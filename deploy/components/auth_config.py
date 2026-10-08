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
GOOGLE_PLATFORM_ENV = ('GOOGLE_CLOUD_PROJECT', 'GOOGLE_APPLICATION_CREDENTIALS')
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
AUTH_CONFIG_ENV = 'LWC_APP_CONFIG_PATH'
AUTH_CONFIG_DIRECTORY = '/var/run/lwc-auth-config'
AUTH_CONFIG_FILE = 'auth.json'
AUTH_CONFIG_PATH = AUTH_CONFIG_DIRECTORY + '/' + AUTH_CONFIG_FILE
AUTH_SECRET_MODE = 0o444
AUTH_LEGACY_ENV = BASE + tuple(GOOGLE) + (
    'REGISTRATION_ENABLED', 'LOCAL_CLOUD_SCOPE', 'LOCAL_CLOUD_JWT_SECRET_FILE', 'LOCAL_DATA_DIR',
)
AUTH_LEGACY_SECRET_ENV = SECRET
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


def desired(plan, component='auth', bff_config_version=None, auth_config_version=None):
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
    inputs = auth.get('runtime_inputs')
    if isinstance(inputs, dict):
        resource = inputs.get('config_secret_resource')
        require(isinstance(resource, str) and SECRET_RESOURCE.fullmatch(resource))
        require(inputs.get('target') == 'auth' and
                inputs.get('environment') == {'development': 'dev', 'production': 'prod'}[plan['environment']] and
                isinstance(inputs.get('config_id'), str) and
                re.fullmatch(r'sha256:[0-9a-f]{64}', inputs['config_id']) is not None)
        require(isinstance(auth_config_version, str) and re.fullmatch(r'[1-9][0-9]*', auth_config_version))
        return {
            'env': {AUTH_CONFIG_ENV: AUTH_CONFIG_PATH},
            'secrets': {},
            'file_secret': {
                'resource': resource, 'version': auth_config_version,
                'directory': AUTH_CONFIG_DIRECTORY, 'file': AUTH_CONFIG_FILE,
                'mode': AUTH_SECRET_MODE,
            },
            'service_account': auth['runtime_service_account'],
        }
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
              manage_pipeline_cooldown=False, managed_auth_file=False,
              expected_file_resource=None, project_number=None):
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
        if component == 'auth' and managed_auth_file and name in AUTH_LEGACY_SECRET_ENV:
            raise ValueError('unexpected legacy Auth secret binding')
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
                allowed_projects = {project}
                if isinstance(project_number, str) and re.fullmatch(r'[1-9][0-9]{5,19}', project_number):
                    allowed_projects.add(project_number)
                require(parts[1] in allowed_projects)
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
        elif component == 'auth' and managed_auth_file and name in AUTH_LEGACY_ENV:
            raise ValueError('unexpected legacy Auth config binding')
        elif component == 'auth' and name == AUTH_CONFIG_ENV:
            require(set(entry) == {'name', 'value'} and entry.get('value') == AUTH_CONFIG_PATH)
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
        elif name in GOOGLE_PLATFORM_ENV:
            require(set(entry) == {'name', 'value'} and isinstance(entry['value'], str))
        elif component == 'bff' and (name in PROFILE_RUNTIME_BFF or name == TYPESAFE_JEV_API_KEY):
            raise ValueError('unexpected Profile runtime binding')
        elif name.startswith('GOOGLE_') and not query_only and not selective_bff:
            raise ValueError('unexpected Google variable')
    if component == 'bff':
        result['file_secret'] = native_bff_file_binding(
            revision, project, expected_file_resource, project_number)
    elif component == 'auth' and AUTH_CONFIG_ENV in result['env']:
        result['file_secret'] = native_auth_file_binding(
            revision, project, expected_file_resource, project_number)
    return result


def native_bff_file_binding(revision, project, expected_resource=None, project_number=None):
    return native_file_binding(revision, project, BFF_CONFIG_DIRECTORY, BFF_CONFIG_FILE,
                               BFF_SECRET_MODE, expected_resource, project_number)


def native_auth_file_binding(revision, project, expected_resource=None, project_number=None):
    return native_file_binding(revision, project, AUTH_CONFIG_DIRECTORY, AUTH_CONFIG_FILE,
                               AUTH_SECRET_MODE, expected_resource, project_number)


def native_file_binding(revision, project, directory, filename, expected_mode,
                        expected_resource=None, project_number=None):
    spec = revision['spec']
    containers = spec.get('containers', [])
    require(len(containers) == 1)
    mounts = [mount for mount in containers[0].get('volumeMounts', [])
              if mount.get('mountPath') == directory]
    require(len(mounts) == 1)
    mount = mounts[0]
    # Cloud Run treats secret volumes as read-only; its SDK omits readOnly on
    # the generated VolumeMount, so this unused field is not a binding check.
    require(isinstance(mount.get('name'), str) and mount.get('name'))
    volumes = [volume for volume in spec.get('volumes', []) if volume.get('name') == mount['name']]
    require(len(volumes) == 1)
    secret = volumes[0].get('secret')
    require(isinstance(secret, dict))
    secret_name = secret.get('secretName')
    require(isinstance(secret_name, str) and secret_name)
    require(isinstance(expected_resource, str) and SECRET_RESOURCE.fullmatch(expected_resource))
    expected_parts = expected_resource.split('/')
    require(expected_parts[1] == project)
    aliases = {}
    annotations = revision.get('metadata', {}).get('annotations', {})
    for binding in annotations.get('run.googleapis.com/secrets', '').split(','):
        if binding:
            alias, separator, target = binding.partition(':')
            require(separator and alias and alias not in aliases and SECRET_RESOURCE.fullmatch(target))
            aliases[alias] = target
    resource = aliases.get(secret_name, secret_name)
    if not SECRET_RESOURCE.fullmatch(resource):
        # Cloud Run resolves an unaliased short secret name in the service's
        # project. Only the configured secret can use this shorthand.
        require(resource == expected_parts[3])
        resource = expected_resource
    resource_parts = resource.split('/')
    allowed_projects = {project}
    if isinstance(project_number, str) and re.fullmatch(r'[1-9][0-9]{5,19}', project_number):
        allowed_projects.add(project_number)
    require(resource_parts[1] in allowed_projects and resource_parts[3] == expected_parts[3])
    items = secret.get('items', [])
    require(isinstance(items, list) and len(items) == 1)
    item = items[0]
    require(isinstance(item, dict) and item.get('path') == filename)
    version = item.get('key')
    require(isinstance(version, str) and re.fullmatch(r'[1-9][0-9]*', version))
    mode = item.get('mode', expected_mode)
    require(type(mode) is int and mode == expected_mode)
    return {
        'resource': expected_resource,
        'version': version,
        'directory': directory,
        'file': filename,
        'mode': mode,
    }


def fingerprint(config):
    return 'sha256:' + hashlib.sha256(json.dumps(config, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def main():
    mode, path, component = sys.argv[1:4]
    with open(path) as stream:
        plan = json.load(stream)['normalized']
    if mode == 'version':
        require(component in ('auth', 'bff'))
        revision = json.load(sys.stdin)
        revision_name = revision.get('metadata', {}).get('name')
        require(isinstance(revision_name, str) and
                revision_name.startswith(plan[component]['service_name'] + '-'))
        conditions = revision.get('status', {}).get('conditions')
        require(isinstance(conditions, list) and
                any(isinstance(c, dict) and c.get('type') == 'Ready' and c.get('status') == 'True'
                    for c in conditions))
        if component == 'bff':
            expected_resource = desired(plan, 'bff', '1')['file_secret']['resource']
        elif isinstance(plan['auth'].get('runtime_inputs'), dict):
            expected_resource = desired(plan, 'auth', auth_config_version='1')['file_secret']['resource']
        else:
            raise ValueError('Auth file config is not selected')
        binding = (native_bff_file_binding(revision, plan['gcp']['project_id'], expected_resource)
                   if component == 'bff' else
                   native_auth_file_binding(revision, plan['gcp']['project_id'], expected_resource))
        require(binding['resource'] == expected_resource)
        print(binding['version'])
        return
    if mode == 'args':
        bff_version = sys.argv[4] if component == 'bff' and len(sys.argv) > 4 else None
        auth_version = sys.argv[4] if component == 'auth' and isinstance(plan['auth'].get('runtime_inputs'), dict) and len(sys.argv) > 4 else None
        expected = desired(plan, component, bff_version, auth_version)
        values = expected['env']
        require(all('\n' not in v and '|' not in v for v in values.values()))
        args = ['--update-env-vars', '^|^' + '|'.join(k + '=' + v for k, v in values.items())]
        if expected['secrets']:
            args += ['--update-secrets', ','.join(k + '=' + v['name'] + ':' + v['key'] for k, v in expected['secrets'].items())]
        if component == 'auth' and isinstance(plan['auth'].get('runtime_inputs'), dict):
            file_secret = expected['file_secret']
            secret_name = file_secret['resource'].split('/')[3]
            args += ['--service-account', expected['service_account'], '--update-secrets',
                     AUTH_CONFIG_PATH + '=' + secret_name + ':' + file_secret['version'],
                     '--remove-env-vars', ','.join(AUTH_LEGACY_ENV),
                     '--remove-secrets', ','.join(AUTH_LEGACY_SECRET_ENV)]
        elif component == 'auth' and not plan['auth']['google']['enabled']:
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
    auth_config_version = sys.argv[7] if component == 'auth' and isinstance(plan['auth'].get('runtime_inputs'), dict) and len(sys.argv) > 7 else None
    expected = desired(plan, component, config_version, auth_config_version)
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
                       component == 'bff' and PIPELINE_COOLDOWN_SECONDS in expected['env'],
                       component == 'auth' and isinstance(plan['auth'].get('runtime_inputs'), dict),
                       expected.get('file_secret', {}).get('resource'))
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
