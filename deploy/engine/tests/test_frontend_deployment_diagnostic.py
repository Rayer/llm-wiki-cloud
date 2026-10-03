"""Offline tests for the fixed, read-only DEV Frontend deployment diagnostic."""
import contextlib
import io
import json
from urllib.error import HTTPError
import unittest

from deploy.engine import frontend_deployment_diagnostic as diagnostic


class Response:
    def __init__(self, value):
        self.body = json.dumps(value).encode()

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def read(self, size=-1):
        return self.body[:size]


class FrontendDeploymentDiagnosticTests(unittest.TestCase):
    def setUp(self):
        self.token = 'synthetic-vercel-token-must-not-escape'
        self.calls = []

    def opener_for(self, responses):
        def opener(request, timeout):
            self.calls.append((request, timeout))
            self.assertEqual(request.get_method(), 'GET')
            self.assertEqual(request.get_header('Authorization'), 'Bearer ' + self.token)
            self.assertEqual(timeout, diagnostic.REQUEST_TIMEOUT)
            path = request.full_url.removeprefix(diagnostic.API)
            value = responses(path)
            if isinstance(value, BaseException):
                raise value
            return Response(value)
        return opener

    @staticmethod
    def identity(value):
        return {'id': value, 'projectId': diagnostic.PROJECT_ID,
                'teamId': diagnostic.TEAM_ID, 'readyState': 'READY',
                'target': 'preview', 'url': 'private-url-must-not-escape'}

    def valid_env(self):
        return {
            'GITHUB_ACTIONS': 'true', 'GITHUB_REF': 'refs/heads/develop',
            'GITHUB_SHA': diagnostic.SOURCE_SHA,
            'DIAGNOSTIC_REF': 'refs/heads/develop',
            'DIAGNOSTIC_CODE_SHA': diagnostic.SOURCE_SHA,
            'DIAGNOSTIC_OPERATION': diagnostic.OPERATION,
            'VERCEL_TOKEN': self.token,
        }

    def test_actual_main_emits_fixed_tuple_exact_readbacks_and_only_gets(self):
        match_id = 'dpl_exactmatch'
        alias_id = 'dpl_aliascurrent'

        def response(path):
            if path.startswith('/v6/deployments?'):
                query = path.partition('?')[2]
                self.assertIn('projectId=' + diagnostic.PROJECT_ID, query)
                self.assertIn('teamId=' + diagnostic.TEAM_ID, query)
                self.assertIn('limit=100', query)
                return {'deployments': [
                    {'uid': match_id, 'meta': {
                        'lwcAttempt': diagnostic.ATTEMPT,
                        'lwcArtifact': diagnostic.ARTIFACT_SHA256,
                    }, 'readyState': 'READY', 'target': 'preview'},
                    {'uid': 'dpl_other', 'meta': {'lwcAttempt': 'other'}},
                    {'uid': match_id, 'meta': {
                        'lwcAttempt': diagnostic.ATTEMPT,
                        'lwcArtifact': diagnostic.ARTIFACT_SHA256,
                    }, 'readyState': 'READY', 'target': 'preview'},
                ]}
            if path.startswith('/v13/deployments/' + match_id + '?teamId='):
                return self.identity(match_id)
            if path.startswith('/v4/aliases/' + diagnostic.STABLE_ALIAS + '?teamId='):
                return {'deploymentId': alias_id, 'projectId': diagnostic.PROJECT_ID}
            if path.startswith('/v13/deployments/' + alias_id + '?teamId='):
                return self.identity(alias_id)
            self.fail('unexpected endpoint: ' + path)

        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            exit_code = diagnostic.main(self.valid_env(), self.opener_for(response))
        rendered = stdout.getvalue()
        result = json.loads(rendered)

        self.assertEqual(exit_code, 0)
        self.assertEqual(result['status'], 'observed')
        self.assertFalse(result['historical_cause_available'])
        self.assertEqual(result['target']['run_id'], diagnostic.RUN_ID)
        self.assertEqual(result['target']['source_sha'], diagnostic.SOURCE_SHA)
        self.assertEqual(result['target']['tag'], diagnostic.RELEASE_TAG)
        self.assertEqual(result['target']['attempt'], diagnostic.ATTEMPT)
        self.assertEqual(result['target']['artifact_sha256'], diagnostic.ARTIFACT_SHA256)
        self.assertEqual(result['v6']['page_limit'], 100)
        self.assertTrue(result['v6']['first_page_only'])
        self.assertFalse(result['v6']['absence_is_conclusive'])
        self.assertEqual(result['v6']['exact_match_count'], 1)
        self.assertEqual(result['v6']['matches'][0]['deployment_id'], match_id)
        self.assertEqual(len(result['v13']), 2)
        self.assertTrue(all(item['project_matches'] and item['team_matches'] for item in result['v13']))
        self.assertTrue(all(item['id_matches_request'] for item in result['v13']))
        self.assertEqual(result['alias'], {
            'queried': True, 'exists': True, 'deployment_id': alias_id,
            'project_matches': True, 'points_to_listed_match': False,
        })
        self.assertEqual(len(self.calls), 4)
        self.assertNotIn(self.token, rendered)
        self.assertNotIn('private-url-must-not-escape', rendered)

    def test_no_matches_is_observation_not_absence_gate_and_alias_is_still_read(self):
        def response(path):
            if path.startswith('/v6/deployments?'):
                return {'deployments': []}
            if path.startswith('/v4/aliases/' + diagnostic.STABLE_ALIAS + '?teamId='):
                return {'deploymentId': 'dpl_unmatched', 'projectId': diagnostic.PROJECT_ID}
            if path.startswith('/v13/deployments/dpl_unmatched?teamId='):
                return self.identity('dpl_unmatched')
            self.fail('unexpected endpoint: ' + path)

        result = diagnostic.diagnose(self.token, self.opener_for(response))
        self.assertEqual(result['status'], 'observed')
        self.assertEqual(result['v6']['exact_match_count'], 0)
        self.assertFalse(result['v6']['absence_is_conclusive'])
        self.assertTrue(result['alias']['queried'])
        self.assertEqual(result['alias']['deployment_id'], 'dpl_unmatched')
        self.assertEqual([call[0].get_method() for call in self.calls], ['GET', 'GET', 'GET'])

    def test_v6_first_hundred_is_bounded_and_total_count_is_reported_without_absence_claim(self):
        deployments = [
            {'uid': f'dpl_item{index}', 'meta': {
                'lwcAttempt': diagnostic.ATTEMPT,
                'lwcArtifact': diagnostic.ARTIFACT_SHA256,
            }}
            for index in range(120)
        ]

        def response(path):
            if path.startswith('/v6/deployments?'):
                return {'deployments': deployments, 'pagination': {'count': 120}}
            if path.startswith('/v13/deployments/'):
                identity = path.split('/v13/deployments/', 1)[1].split('?', 1)[0]
                return self.identity(identity)
            if path.startswith('/v4/aliases/' + diagnostic.STABLE_ALIAS + '?teamId='):
                error = HTTPError(path, 404, 'not found', {}, None)
                self.addCleanup(error.close)
                raise error
            self.fail('unexpected endpoint: ' + path)

        result = diagnostic.diagnose(self.token, self.opener_for(response))
        self.assertEqual(result['v6']['reported_total_count'], 120)
        self.assertEqual(result['v6']['returned_count'], 100)
        self.assertEqual(result['v6']['exact_match_count'], 100)
        self.assertTrue(result['v6']['first_page_only'])
        self.assertFalse(result['v6']['absence_is_conclusive'])
        self.assertEqual(len(result['v13']), 100)
        self.assertEqual(len(self.calls), 102)

    def test_fixed_context_rejection_makes_no_request(self):
        env = self.valid_env()
        env['GITHUB_SHA'] = 'f' * 40
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            exit_code = diagnostic.main(env, self.opener_for(lambda _path: self.fail('network call')))
        self.assertEqual(exit_code, 1)
        self.assertEqual(json.loads(stdout.getvalue()), diagnostic.rejected_result())
        self.assertEqual(self.calls, [])

    def test_provider_http_failure_keeps_fixed_cause_and_does_not_emit_response_body(self):
        secret_body = b'{"Authorization":"Bearer synthetic-vercel-token-must-not-escape"}'

        def opener(request, timeout):
            self.calls.append(request)
            if request.full_url.startswith(diagnostic.API + '/v6/'):
                error = HTTPError(request.full_url, 503, 'temporary ' + self.token + ' upstream failure', {}, io.BytesIO(secret_body))
                self.addCleanup(error.close)
                raise error
            if request.full_url.startswith(diagnostic.API + '/v4/aliases/' + diagnostic.STABLE_ALIAS + '?teamId='):
                return Response({'deploymentId': None, 'projectId': diagnostic.PROJECT_ID})
            self.fail('unexpected endpoint')

        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            exit_code = diagnostic.main(self.valid_env(), opener)
        rendered = stdout.getvalue()
        result = json.loads(rendered)
        self.assertEqual(exit_code, 1)
        self.assertEqual(result['status'], 'partial')
        self.assertEqual(result['causes'][0]['stage'], 'v6-list')
        self.assertEqual(result['causes'][0]['exception_type'], 'HTTPError')
        self.assertEqual(result['causes'][0]['http_status'], 503)
        self.assertNotIn(self.token, rendered)
        self.assertNotIn(secret_body.decode(), rendered)


if __name__ == '__main__':
    unittest.main(verbosity=2)
