"""Fixed, GET-only readback for the unresolved DEV Frontend deployment."""
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from support import InputShapeError, safe_error_message, structured_cause

DIAGNOSTIC = 'dev-frontend-deployment-read-only'
OPERATION = 'diagnose-frontend-deployment'
API = 'https://api.vercel.com'
PAGE_LIMIT = 100
RESPONSE_LIMIT = 256 * 1024
REQUEST_TIMEOUT = 5
SOURCE_SHA = 'b978fe10fb532e87b5afe5dee7e65712622857dd'
RELEASE_TAG = 'dev-lwc-366-b978fe10fb53'
RUN_ID = '37157593306'
ATTEMPT = '142b8a78e39db1d7359d42093e321adab951e1f413f913f4d0df09fbe57a4d92'
ARTIFACT_SHA256 = 'fd4942da5bafb1cf5a16838ad59956537d163a6e5971d114bf550cd0d7640efa'
PROJECT_ID = 'prj_m4r0AIf6l7RgIBsBdpuJvJtUM9th'
TEAM_ID = 'team_Z6PbTGXwFFzZuOgjYegBojxp'
STABLE_ALIAS = 'wiki.dev.rayer.idv.tw'
DEPLOYMENT_ID = re.compile(r'^(?:dpl_[A-Za-z0-9]+|[A-Za-z0-9.-]+\.vercel\.app)$')
CODE_SHA = re.compile(r'^[0-9a-f]{40}$')


def context_allowed(env):
    code_sha = env.get('DIAGNOSTIC_CODE_SHA', '')
    return (env.get('GITHUB_ACTIONS') == 'true' and
            env.get('GITHUB_REF') == 'refs/heads/develop' and
            env.get('DIAGNOSTIC_REF') == env.get('GITHUB_REF') and
            CODE_SHA.fullmatch(code_sha) is not None and
            code_sha == env.get('GITHUB_SHA') and
            env.get('DIAGNOSTIC_OPERATION') == OPERATION)


def rejected_result():
    return {'schema': 1, 'diagnostic': DIAGNOSTIC, 'status': 'rejected',
            'cause': None, 'reason': 'fixed-development-workflow-context-required'}


def _cause(exc, stage, token):
    if isinstance(exc, urllib.error.HTTPError):
        message, truncated = safe_error_message(
            f'HTTP {exc.code}: {exc.reason}', sensitive_values=(token,))
        return {'stage': stage, 'exception_type': 'HTTPError',
                'exception_type_omitted': False, 'code': 'provider-http-error',
                'http_status': exc.code, 'message': message,
                'message_truncated': truncated, 'message_omitted': message is None}
    if isinstance(exc, urllib.error.URLError):
        reason = exc.reason if isinstance(exc.reason, str) else str(exc.reason)
        message, truncated = safe_error_message(reason, sensitive_values=(token,))
        return {'stage': stage, 'exception_type': 'URLError',
                'exception_type_omitted': False, 'code': 'provider-transport-error',
                'message': message, 'message_truncated': truncated,
                'message_omitted': message is None}
    cause = structured_cause(exc, 'unknown', sensitive_values=(token,))
    cause['stage'] = stage
    return cause


def _get_json(path, token, opener=urllib.request.urlopen):
    request = urllib.request.Request(
        API + path,
        headers={'Authorization': 'Bearer ' + token, 'Accept': 'application/json'},
        method='GET')
    with opener(request, timeout=REQUEST_TIMEOUT) as response:
        body = response.read(RESPONSE_LIMIT + 1)
    if len(body) > RESPONSE_LIMIT:
        raise InputShapeError('provider-response-exceeds-limit')
    return json.loads(body)


def _safe_deployment_id(value):
    return value if isinstance(value, str) and DEPLOYMENT_ID.fullmatch(value) else None


def _state(value):
    return value if isinstance(value, str) and re.fullmatch(r'[A-Z_]{1,32}', value) else 'other'


def _deployment_readback(identity, token, opener):
    path = '/v13/deployments/' + urllib.parse.quote(identity, safe='') + '?' + urllib.parse.urlencode({'teamId': TEAM_ID})
    response = _get_json(path, token, opener)
    if not isinstance(response, dict):
        raise InputShapeError('v13-deployment-response-shape-invalid')
    response_id = _safe_deployment_id(response.get('id'))
    response_team = response.get('teamId', response.get('ownerId'))
    return {
        'requested_id': identity,
        'deployment_id': response_id,
        'id_matches_request': response_id == identity,
        'project_matches': response.get('projectId') == PROJECT_ID,
        'team_matches': response_team == TEAM_ID,
        'ready_state': _state(response.get('readyState')),
        'target': response.get('target') if response.get('target') in ('preview', 'production') else 'other',
        'url_present': isinstance(response.get('url'), str) and bool(response.get('url')),
    }


def diagnose(token, opener=urllib.request.urlopen):
    result = {
        'schema': 1, 'diagnostic': DIAGNOSTIC, 'status': 'observed',
        'target': {
            'run_id': RUN_ID, 'source_sha': SOURCE_SHA, 'tag': RELEASE_TAG,
            'attempt': ATTEMPT, 'artifact_sha256': ARTIFACT_SHA256,
            'project_id': PROJECT_ID, 'team_id': TEAM_ID,
            'target': 'preview', 'stable_alias': STABLE_ALIAS,
        },
        'historical_cause_available': False,
        'v6': {'page_limit': PAGE_LIMIT, 'first_page_only': True,
               'absence_is_conclusive': False, 'returned_count': None,
               'reported_total_count': None, 'exact_match_count': None, 'matches': []},
        'v13': [],
        'alias': {'queried': False, 'exists': None, 'deployment_id': None,
                  'project_matches': None, 'points_to_listed_match': None},
        'causes': [],
        'additional_cause_count': 0,
    }
    identities = []
    listed_identities = set()

    def record_cause(stage, exc):
        result['status'] = 'partial'
        result['additional_cause_count'] += 1
        if len(result['causes']) < 8:
            result['causes'].append(_cause(exc, stage, token))

    query = urllib.parse.urlencode({'projectId': PROJECT_ID, 'limit': PAGE_LIMIT,
                                    'teamId': TEAM_ID})
    try:
        response = _get_json('/v6/deployments?' + query, token, opener)
        deployments = response.get('deployments') if isinstance(response, dict) else None
        if not isinstance(deployments, list):
            raise InputShapeError('v6-deployments-list-shape-invalid')
        deployments = deployments[:PAGE_LIMIT]
        result['v6']['returned_count'] = len(deployments)
        pagination = response.get('pagination')
        total_count = pagination.get('count') if isinstance(pagination, dict) else response.get('totalCount')
        if isinstance(total_count, int) and not isinstance(total_count, bool) and total_count >= 0:
            result['v6']['reported_total_count'] = total_count
        for item in deployments:
            if not isinstance(item, dict):
                continue
            meta = item.get('meta') if isinstance(item.get('meta'), dict) else {}
            attempt_match = meta.get('lwcAttempt') == ATTEMPT
            artifact_match = meta.get('lwcArtifact') == ARTIFACT_SHA256
            identity = _safe_deployment_id(item.get('uid') or item.get('id'))
            if attempt_match and artifact_match:
                if identity and identity in listed_identities:
                    continue
                if identity:
                    listed_identities.add(identity)
                result['v6']['matches'].append({
                    'deployment_id': identity,
                    'attempt_matches': True,
                    'artifact_matches': True,
                    'ready_state': _state(item.get('readyState')),
                    'target': item.get('target') if item.get('target') in ('preview', 'production') else 'other',
                })
                if identity and identity not in identities:
                    identities.append(identity)
        result['v6']['exact_match_count'] = sum(
            1 for item in result['v6']['matches']
            if item['attempt_matches'] and item['artifact_matches'])
    except Exception as exc:
        record_cause('v6-list', exc)

    for identity in identities:
        try:
            result['v13'].append(_deployment_readback(identity, token, opener))
        except Exception as exc:
            record_cause('v13-exact-match', exc)

    alias_path = '/v4/aliases/' + urllib.parse.quote(STABLE_ALIAS, safe='') + '?' + urllib.parse.urlencode({'teamId': TEAM_ID})
    result['alias']['queried'] = True
    try:
        alias = _get_json(alias_path, token, opener)
        if not isinstance(alias, dict):
            raise InputShapeError('v4-alias-response-shape-invalid')
        alias_id = _safe_deployment_id(alias.get('deploymentId', alias.get('deployment_id')))
        result['alias'].update({
            'exists': True,
            'deployment_id': alias_id,
            'project_matches': alias.get('projectId') == PROJECT_ID,
            'points_to_listed_match': alias_id in identities if alias_id else False,
        })
        if alias_id and alias_id not in identities:
            try:
                result['v13'].append(_deployment_readback(alias_id, token, opener))
            except Exception as exc:
                record_cause('v13-alias-target', exc)
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            result['alias']['exists'] = False
        else:
            record_cause('v4-alias', exc)
    except Exception as exc:
        record_cause('v4-alias', exc)
    return result


def main(env=None, opener=urllib.request.urlopen):
    env = os.environ if env is None else env
    if not context_allowed(env):
        result = rejected_result()
        print(json.dumps(result, sort_keys=True))
        return 1
    token = env.get('VERCEL_TOKEN', '')
    if not token:
        result = {'schema': 1, 'diagnostic': DIAGNOSTIC, 'status': 'partial',
                  'historical_cause_available': False, 'v6': None, 'v13': [],
                  'alias': None, 'causes': [{
                      'stage': 'authority', 'exception_type': 'MissingAuthority',
                      'exception_type_omitted': False, 'code': 'missing-vercel-token',
                      'message': 'Development Vercel token unavailable',
                      'message_truncated': False, 'message_omitted': False}],
                  'additional_cause_count': 1}
        print(json.dumps(result, sort_keys=True))
        return 1
    result = diagnose(token, opener)
    print(json.dumps(result, sort_keys=True))
    return 0 if result['status'] == 'observed' else 1


if __name__ == '__main__':
    raise SystemExit(main())
