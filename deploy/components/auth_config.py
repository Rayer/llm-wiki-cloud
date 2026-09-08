#!/usr/bin/env python3
"""Auth and Production BFF configuration contract: no credential payloads are emitted or persisted."""
import hashlib
import json
import sys

GOOGLE = {
    'GOOGLE_CLIENT_ID': 'client_id', 'GOOGLE_ISSUER': 'issuer',
    'GOOGLE_JWKS_URL': 'jwks_url', 'GOOGLE_TOKEN_URL': 'token_url',
    'GOOGLE_LOGIN_REDIRECT_URL': 'login_redirect_url',
    'GOOGLE_LINK_REDIRECT_URL': 'link_redirect_url',
    'GOOGLE_COMPLETION_URL': 'completion_url',
}
BASE = ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID', 'ALLOWED_HOSTS', 'ALLOWED_ORIGINS',
        'AUTH_SERVICE_URL', 'AUTH_SESSION_ENVIRONMENT', 'AUTH_REFRESH_SESSION_MIGRATION', 'DEV_JWT')
SECRET = ('JWT_SECRET', 'GOOGLE_CLIENT_SECRET')


def require(condition):
    if not condition:
        raise ValueError("contract mismatch")


def desired(plan, component='auth'):
    require(plan['environment'] in ('development', 'production'))
    require(component in ('auth', 'bff'))
    if component == 'bff':
        require(plan['environment'] == 'production')
        bff = plan['bff']
        return {'env': {
            'GCP_PROJECT': plan['gcp']['project_id'], 'FIRESTORE_DATABASE_ID': bff['firestore_database_id'],
            'ALLOWED_ORIGINS': ','.join(bff['allowed_origins']), 'AUTH_SERVICE_URL': bff['auth_service_url'],
            'DEV_JWT': 'false',
        }, 'secrets': {'JWT_SECRET': {'name': bff['secret_references']['jwt'], 'key': 'latest'}},
            'service_account': bff['runtime_service_account']}
    auth = plan['auth']
    google = auth['google']
    env = dict(zip(BASE, (
        plan['gcp']['project_id'], auth['firestore_database_id'],
        ','.join(auth['allowed_hosts']), ','.join(auth['allowed_origins']),
        'https://' + auth['public_domain'], auth['firestore_database_id'], 'disabled', 'false',
    )))
    secrets = {'JWT_SECRET': {'name': auth['secret_references']['jwt'], 'key': 'latest'}}
    require(type(google['enabled']) is bool)
    if google['enabled']:
        env.update({key: google[field] for key, field in GOOGLE.items()})
        secrets['GOOGLE_CLIENT_SECRET'] = {
            'name': google['client_secret_reference'], 'key': google['client_secret_version'],
        }
    return {'env': env, 'secrets': secrets, 'service_account': auth['runtime_service_account']}


def effective(revision, project):
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
        if name in SECRET:
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
        elif name in BASE or name in GOOGLE:
            require(set(entry) == {'name', 'value'} and isinstance(entry['value'], str))
            result['env'][name] = entry['value']
        elif name.startswith('GOOGLE_'):
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
        args = ['--update-env-vars', '^|^' + '|'.join(k + '=' + v for k, v in values.items()),
                '--update-secrets', ','.join(k + '=' + v['name'] + ':' + v['key'] for k, v in expected['secrets'].items())]
        if component == 'auth' and not plan['auth']['google']['enabled']:
            args += ['--remove-env-vars', ','.join(GOOGLE), '--remove-secrets', 'GOOGLE_CLIENT_SECRET']
        print('\n'.join(args))
        return
    revision = json.load(sys.stdin)
    require(revision['metadata']['name'] == sys.argv[4])
    require(revision['status']['imageDigest'] == sys.argv[5])
    require(revision['spec']['containers'][0]['image'] == sys.argv[5])
    require(any(c['type'] == 'Ready' and c['status'] == 'True' for c in revision['status']['conditions']))
    actual = effective(revision, plan['gcp']['project_id'])
    digest = fingerprint(actual)
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
    except (AssertionError, KeyError, ValueError, TypeError, IndexError):
        sys.exit('Auth config readback/contract mismatch (values suppressed)')
