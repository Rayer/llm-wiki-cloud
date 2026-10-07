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
        'AUTH_DEMO_USER_ID', 'DEV_JWT')
SECRET = ('JWT_SECRET', 'GOOGLE_CLIENT_SECRET')
QUERY_PATH = 'QUERY_STAGE_CONFIG_PATH'
EXPORT_BFF = ('EXPORT_JOB_URL', 'EXPORT_SIGNING_SERVICE_ACCOUNT')
PROFILE_RUNTIME_BFF = ('PROFILE_RUNTIME_AUDIENCE', 'PROFILE_RUNTIME_SERVICE_ACCOUNT')
TYPESAFE_JEV_API_KEY = 'TYPESAFE_JEV_API_KEY'
PIPELINE_DEMO_USER_IDS = 'PIPELINE_DEMO_USER_IDS'
PIPELINE_COOLDOWN_SECONDS = 'PIPELINE_COOLDOWN_SECONDS'
MAX_PIPELINE_COOLDOWN_SECONDS = ((1 << 63) - 1) // 1_000_000_000


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


def desired(plan, component='auth'):
    require(plan['environment'] in ('development', 'production'))
    require(component in ('auth', 'bff'))
    if component == 'bff':
        bff = plan['bff']
        cooldown = bff.get('pipeline_cooldown_seconds')
        env = {QUERY_PATH: query_path(plan)}
        if cooldown is not None:
            require(type(cooldown) is int and 0 < cooldown <= MAX_PIPELINE_COOLDOWN_SECONDS)
            env[PIPELINE_COOLDOWN_SECONDS] = str(cooldown)
        secrets = {}
        audience = bff.get('profile_runtime_audience')
        invoker = bff.get('profile_runtime_service_account')
        ref = bff.get('secret_references', {}).get('typesafe_jev_api_key')
        if audience is not None or invoker is not None or ref is not None:
            require(isinstance(audience, str) and audience.startswith('https://')
                    and not any(c in audience for c in '\n|'))
            require(isinstance(invoker, str) and re.fullmatch(
                r'[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9.-]+\.iam\.gserviceaccount\.com', invoker))
            require(isinstance(ref, dict) and set(ref) == {'name', 'version'}
                    and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', ref['name'])
                    and re.fullmatch(r'[1-9][0-9]*', ref['version']))
            env.update({
                'PROFILE_RUNTIME_AUDIENCE': audience,
                'PROFILE_RUNTIME_SERVICE_ACCOUNT': invoker,
            })
            secrets[TYPESAFE_JEV_API_KEY] = {'name': ref['name'], 'key': ref['version']}
        if plan['export_job']['enabled']:
            env.update({
                'EXPORT_JOB_URL': 'https://run.googleapis.com/v2/projects/{}/locations/{}/jobs/{}:run'.format(
                    plan['gcp']['project_id'], plan['export_job']['location'], plan['export_job']['job_name']),
                'EXPORT_SIGNING_SERVICE_ACCOUNT': plan['export_job']['signing_service_account'],
            })
        if plan['environment'] == 'development':
            demo_ids = bff.get('pipeline_demo_user_ids')
            if demo_ids is not None:
                require(isinstance(demo_ids, list) and demo_ids and
                        all(isinstance(user_id, str) and re.fullmatch(r'[A-Za-z0-9_-]{1,128}', user_id)
                            for user_id in demo_ids) and len(set(demo_ids)) == len(demo_ids))
                env[PIPELINE_DEMO_USER_IDS] = ','.join(demo_ids)
        if plan['environment'] == 'development' or plan['auth'].get('google') is None:
            return {'env': env, 'secrets': secrets, 'service_account': bff['runtime_service_account']}
        secrets['JWT_SECRET'] = {'name': bff['secret_references']['jwt'], 'key': 'latest'}
        return {'env': {
            **env,
            'GCP_PROJECT': plan['gcp']['project_id'], 'FIRESTORE_DATABASE_ID': bff['firestore_database_id'],
            'ALLOWED_ORIGINS': ','.join(bff['allowed_origins']), 'AUTH_SERVICE_URL': bff['auth_service_url'],
            'DEV_JWT': 'false',
        }, 'secrets': secrets,
            'service_account': bff['runtime_service_account']}
    auth = plan['auth']
    google = auth['google']
    demo_user_id = auth.get('demo_user_id', '')
    require(isinstance(demo_user_id, str) and (demo_user_id == '' or re.fullmatch(r'[A-Za-z0-9_-]{1,128}', demo_user_id)))
    env = dict(zip(BASE, (
        plan['gcp']['project_id'], auth['firestore_database_id'],
        ','.join(auth['allowed_hosts']), ','.join(auth['allowed_origins']),
        'https://' + auth['public_domain'], auth['firestore_database_id'], 'disabled', demo_user_id, 'false',
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
    return result


def fingerprint(config):
    return 'sha256:' + hashlib.sha256(json.dumps(config, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def main():
    mode, path, component = sys.argv[1:4]
    with open(path) as stream:
        plan = json.load(stream)['normalized']
    expected = desired(plan, component)
    if mode == 'args':
        values = expected['env']
        require(all('\n' not in v and '|' not in v for v in values.values()))
        args = ['--update-env-vars', '^|^' + '|'.join(k + '=' + v for k, v in values.items())]
        if expected['secrets']:
            args += ['--update-secrets', ','.join(k + '=' + v['name'] + ':' + v['key'] for k, v in expected['secrets'].items())]
        if component == 'auth' and not plan['auth']['google']['enabled']:
            args += ['--remove-env-vars', ','.join(GOOGLE), '--remove-secrets', 'GOOGLE_CLIENT_SECRET']
        if component == 'bff' and plan['environment'] == 'development' and not plan['export_job']['enabled']:
            args += ['--remove-env-vars', ','.join(EXPORT_BFF)]
        print('\n'.join(args))
        return
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
