"""Production adapters. Tests replace subprocess executables, never these adapters."""
import copy
import json
import os
from pathlib import Path
import re
import sys
import tarfile
import tempfile
import time
import urllib.parse

from support import ROOT, Breakpoint, digest, read, require, run, write
sys.path.insert(0, str(ROOT / 'deploy/components'))
import auth_config
import frontend_build_config

CLOUD_BUILD_LOCATION = 'global'
BUILD_POLL_INTERVAL_SECONDS = 5
BUILD_POLL_TIMEOUT_SECONDS = 600
BUILD_POLL_MAX_READS = 120
BUILD_READ_TIMEOUT_SECONDS = 30
BUILD_ID_RE = re.compile(r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$', re.I)
BUILD_NAME_RE = re.compile(r'^projects/([^/]+)/locations/([^/]+)/builds/([^/]+)$')
GCP_PROJECT_NUMBER_RE = re.compile(r'^[1-9][0-9]{5,19}$')
BUILD_STATUSES = {'PENDING', 'QUEUED', 'WORKING', 'SUCCESS', 'FAILURE',
                  'INTERNAL_ERROR', 'TIMEOUT', 'CANCELLED', 'EXPIRED', 'STATUS_UNKNOWN'}
BUILD_TERMINAL_FAILURES = {'FAILURE', 'INTERNAL_ERROR', 'TIMEOUT', 'CANCELLED', 'EXPIRED'}


class Providers:
    def __init__(self, plan, directory):
        self.plan, self.p, self.directory = plan, plan['normalized'], Path(directory)
        self.profiles = read(ROOT / 'deploy/engine/profiles.json')
        self._gcp_project_number = None

    def gcp_project_number(self):
        """Resolve the authoritative number for this configured project ID."""
        if self._gcp_project_number is not None:
            return self._gcp_project_number
        project_id = self.p['gcp']['project_id']
        try:
            output = run(['gcloud', 'projects', 'describe', project_id,
                          '--format=json', '--quiet'], timeout=30,
                         stage='build-project-identity')
            identity = json.loads(output)
        except Breakpoint as exc:
            reason = 'permission-denied' if exc.reason == 'permission-denied' else 'gcp-project-identity-unavailable'
            action = 'restore-existing-principal-permission' if reason == 'permission-denied' else 'verify-project-identity'
            raise Breakpoint(reason, exc.status, False, action, stage='build-project-identity',
                             exit_code=exc.exit_code, timeout_class=exc.timeout_class) from None
        except (TypeError, ValueError):
            raise Breakpoint('gcp-project-identity-unreadable', 'failed', False,
                             'verify-project-identity', stage='build-project-identity') from None
        project_number = identity.get('projectNumber') if isinstance(identity, dict) else None
        if isinstance(project_number, int):
            project_number = str(project_number)
        if (not isinstance(identity, dict) or identity.get('projectId') != project_id or
                not isinstance(project_number, str) or
                not GCP_PROJECT_NUMBER_RE.fullmatch(project_number)):
            raise Breakpoint('gcp-project-identity-mismatch', 'failed', False,
                             'verify-project-identity', stage='build-project-identity', exit_code=0)
        self._gcp_project_number = project_number
        return project_number

    def cloud(self, c, *args, mutation=False):
        key = 'export_job' if c == 'exportjob' else c
        cfg = self.p[key]
        region = cfg.get('location', self.p['gcp']['region'])
        return run(['gcloud', 'run', *args, '--project', self.p['gcp']['project_id'],
                    '--region', region, '--format=json', '--quiet'], mutation=mutation)

    def resource(self, c):
        cfg = self.p['export_job' if c == 'exportjob' else c]
        kind = 'services' if c in ('auth', 'bff') else 'jobs'
        return kind, cfg['service_name' if kind == 'services' else 'job_name']

    def describe(self, c):
        kind, name = self.resource(c)
        return json.loads(self.cloud(c, kind, 'describe', name))

    def revision(self, c, revision):
        require(re.fullmatch(r'[a-z][a-z0-9-]+', revision), 'invalid-revision')
        return json.loads(self.cloud(c, 'revisions', 'describe', revision))

    def api(self, endpoint, body=None, output=False, stage=None):
        token = os.environ.get('VERCEL_TOKEN')
        team = os.environ.get('VERCEL_TEAM_ID', '')
        require(token and re.fullmatch(r'team_[A-Za-z0-9]+', team), 'missing-vercel-authority')
        # Credentials travel on stdin, not command output or an argument list.
        require('\n' not in token and '"' not in token, 'invalid-token')
        args = ['curl', '--disable', '--fail', '--silent', '--show-error', '--max-time', '30',
                '--config', '-', 'https://api.vercel.com' + endpoint +
                ('&' if '?' in endpoint else '?') + 'teamId=' + team]
        if body is not None:
            args += ['--request', 'POST', '--header', 'Content-Type: application/json', '--data', json.dumps(body)]
        if output:
            args += ['--max-filesize', '8192']
        raw = run(args, input='header = "Authorization: Bearer '+token+'"\n',
                  mutation=body is not None, stage=stage)
        return json.loads(frontend_build_config.document(raw.encode()) if output else raw)

    def project(self, stage=None):
        identity = os.environ.get('VERCEL_PROJECT_ID', '')
        require(re.fullmatch(r'prj_[A-Za-z0-9]+', identity), 'invalid-vercel-project')
        raw = self.api('/v9/projects/'+identity, stage=stage)
        cfg = self.p['frontend']
        link = raw.get('link', {})
        repository = link.get('repo')
        if repository and '/' not in repository:
            repository = link.get('org', '')+'/'+repository
        require(raw['id'] == identity and raw['name'] == cfg['project_name'] and
                raw.get('accountId', raw.get('team', {}).get('id')) == os.environ['VERCEL_TEAM_ID'] and
                raw.get('rootDirectory') == cfg['root_directory'] and repository == cfg['repository'], 'vercel-project-config-incompatible')

    def deployment(self, identity):
        require(re.fullmatch(r'(dpl_[A-Za-z0-9]+|[A-Za-z0-9.-]+\.vercel\.app)', identity), 'invalid-deployment')
        result = self.api('/v13/deployments/' + identity)
        require(result['projectId'] == os.environ['VERCEL_PROJECT_ID'], 'wrong-vercel-project')
        require(result.get('teamId', result.get('ownerId')) == os.environ['VERCEL_TEAM_ID'], 'wrong-vercel-team')
        return result

    def alias(self, alias):
        require(re.fullmatch(r'[a-zA-Z0-9.-]+', alias), 'invalid-alias')
        result = self.api('/v4/aliases/' + alias)
        require(result['projectId'] == os.environ['VERCEL_PROJECT_ID'], 'wrong-alias-project')
        return result.get('deploymentId', result.get('deployment_id'))

    def valid_image(self, c, image):
        prefix = self.p['gcp']['artifact_registry'] + '/' + self.profiles[c]['image']
        require(re.fullmatch(re.escape(prefix) + r'@sha256:[0-9a-f]{64}', image), 'artifact-config-incompatible')
        actual = run(['gcloud', 'artifacts', 'docker', 'images', 'describe', image,
                      '--project', self.p['gcp']['project_id'], '--format=value(image_summary.digest)', '--quiet'],
                     stage='digest-validate')
        if actual != image.split('@')[1]:
            raise Breakpoint('artifact-unusable', stage='digest-validate', exit_code=0)

    def _build_record(self, c, raw=None, *, status='SUBMITTED', poll_outcome='accepted',
                      expected_id=None, require_name=True):
        project = self.p['gcp']['project_id']
        record = {'project_id': project, 'location': CLOUD_BUILD_LOCATION,
                  'build_id': None, 'identity_verified': False, 'status': status,
                  'last_observed_status': None, 'poll_outcome': poll_outcome}
        if not isinstance(raw, dict):
            return record, 'build-identity-invalid'

        build_id = raw.get('id')
        safe_id = build_id.lower() if isinstance(build_id, str) and BUILD_ID_RE.fullmatch(build_id) else None
        record['build_id'] = safe_id
        response_project = raw.get('projectId')
        if isinstance(response_project, str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{0,99}', response_project):
            if response_project != project:
                record['reported_project_id'] = response_project

        resource_name = raw.get('name')
        name_parts = BUILD_NAME_RE.fullmatch(resource_name) if isinstance(resource_name, str) else None
        name_project, name_location, name_id = name_parts.groups() if name_parts else (None, None, None)
        name_project_matches = False
        if name_project == project:
            name_project_matches = True
        elif name_project and GCP_PROJECT_NUMBER_RE.fullmatch(name_project):
            name_project_matches = name_project == self.gcp_project_number()
        if (name_project and not name_project_matches and
                re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{0,99}', name_project)):
            record['reported_project_id'] = name_project
        location = raw.get('location')
        observed_location = location if isinstance(location, str) else name_location
        if observed_location and re.fullmatch(r'[a-z][a-z0-9-]{0,62}', observed_location) and observed_location != CLOUD_BUILD_LOCATION:
            record['reported_location'] = observed_location
        if safe_id is None and name_id and BUILD_ID_RE.fullmatch(name_id):
            record['build_id'] = name_id.lower()

        observed_status = raw.get('status')
        if isinstance(observed_status, str) and observed_status in BUILD_STATUSES:
            record['last_observed_status'] = observed_status

        valid = (safe_id is not None and response_project == project and
                 (not require_name or name_parts is not None) and
                 (name_parts is None or (name_project_matches and name_location == CLOUD_BUILD_LOCATION and
                                         BUILD_ID_RE.fullmatch(name_id) and name_id.lower() == safe_id)) and
                 (location is None or location == CLOUD_BUILD_LOCATION) and
                 (expected_id is None or safe_id == expected_id))
        if valid:
            record['identity_verified'] = True
            return record, None
        if safe_id is None and record['build_id'] is None:
            reason = 'build-identity-invalid'
            record['status'] = 'IDENTITY_INVALID'
            record['poll_outcome'] = 'identity_invalid'
        else:
            reason = 'build-identity-mismatch'
            record['status'] = 'IDENTITY_MISMATCH'
            record['poll_outcome'] = 'identity_mismatch'
        return record, reason

    def submit_build(self, c):
        # Resolve project ID -> number before creating a build. Cloud Build's
        # resource name may use either identifier; the numeric alias is accepted
        # only after this explicit, read-only identity check.
        self.gcp_project_number()
        env = dict(os.environ, PLAN_PATH=str(self.directory / 'plan.json'), ROOT=str(ROOT),
                   SOURCE_SHA=self.plan['source'], SOURCE_REF=self.plan['branch'],
                   GITHUB_RUN_ID=os.environ.get('GITHUB_RUN_ID', '1'),
                   GITHUB_RUN_ATTEMPT=os.environ.get('GITHUB_RUN_ATTEMPT', '1'))
        output = run(['bash', ROOT / 'deploy/components' / (c + '.sh'), 'submit'], env=env,
                     timeout=630, mutation=True, unknown_on_error=True, stage='auth-build')
        try:
            raw = json.loads(output)
        except (TypeError, ValueError):
            record, _ = self._build_record(c)
            record.update(status='SUBMIT_UNKNOWN', poll_outcome='submit_unknown')
            raise Breakpoint('build-response-invalid', 'unknown', False, 'reconcile-before-replay',
                             stage='build-submit', build=record) from None
        record, reason = self._build_record(c, raw, status='SUBMITTED', poll_outcome='accepted')
        if reason:
            raise Breakpoint(reason, 'unknown', False, 'reconcile-before-replay',
                             stage='build-submit', build=record)
        return record

    def poll_build(self, c, build):
        require(build.get('identity_verified') is True and
                isinstance(build.get('build_id'), str) and BUILD_ID_RE.fullmatch(build['build_id']) and
                build.get('project_id') == self.p['gcp']['project_id'] and
                build.get('location') == CLOUD_BUILD_LOCATION, 'build-identity-unverified')
        try:
            self.gcp_project_number()
        except Breakpoint as exc:
            build.update(status='STATUS_UNKNOWN', poll_outcome='project_identity_unavailable')
            action = ('restore-existing-principal-permission' if exc.reason == 'permission-denied'
                      else 'reconcile-before-replay')
            raise Breakpoint(exc.reason, 'unknown', False, action,
                             stage='build-project-identity', exit_code=exc.exit_code,
                             timeout_class=exc.timeout_class, build=build) from None
        deadline = time.monotonic() + BUILD_POLL_TIMEOUT_SECONDS
        for _ in range(BUILD_POLL_MAX_READS):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            try:
                output = run(['gcloud', 'builds', 'describe', build['build_id'],
                              '--project', build['project_id'], '--region', build['location'],
                              '--format=json', '--quiet'], timeout=min(BUILD_READ_TIMEOUT_SECONDS, remaining),
                             stage='build-status', unknown_on_error=True)
            except Breakpoint as exc:
                build.update(status='STATUS_UNKNOWN', poll_outcome='status_unavailable')
                raise Breakpoint(exc.reason, 'unknown', False, 'reconcile-before-replay',
                                 stage='build-status', exit_code=exc.exit_code,
                                 timeout_class=exc.timeout_class, build=build) from None
            try:
                raw = json.loads(output)
            except (TypeError, ValueError):
                build.update(status='STATUS_UNKNOWN', poll_outcome='malformed_status')
                raise Breakpoint('build-status-unreadable', 'unknown', False,
                                 'reconcile-before-replay', stage='build-status', build=build) from None
            observed, reason = self._build_record(c, raw, expected_id=build['build_id'], require_name=True)
            if reason:
                for key in ('reported_project_id', 'reported_location'):
                    if key in observed:
                        build[key] = observed[key]
                build.update(identity_verified=False, status='STATUS_UNKNOWN', poll_outcome='identity_mismatch')
                raise Breakpoint(reason, 'unknown', False, 'reconcile-before-replay',
                                 stage='build-status', build=build)
            status = raw.get('status')
            if status not in BUILD_STATUSES:
                status = 'STATUS_UNKNOWN'
            build['status'] = status
            build['last_observed_status'] = status
            if status == 'SUCCESS':
                build['poll_outcome'] = 'succeeded'
                return status
            if status in BUILD_TERMINAL_FAILURES:
                build['poll_outcome'] = 'terminal_failure'
                raise Breakpoint('build-failed', 'failed', False, 'inspect-build-logs-by-id',
                                 stage='build-status', build=build)
            if status == 'STATUS_UNKNOWN':
                build['poll_outcome'] = 'status_unknown'
                raise Breakpoint('build-status-unknown', 'unknown', False,
                                 'reconcile-before-replay', stage='build-status', build=build)
            build['poll_outcome'] = 'pending'
            remaining = deadline - time.monotonic()
            if remaining > 0:
                time.sleep(min(BUILD_POLL_INTERVAL_SECONDS, remaining))
        build.update(status='STATUS_UNKNOWN', poll_outcome='deadline')
        raise Breakpoint('build-poll-deadline', 'unknown', False, 'reconcile-before-replay',
                         stage='build-status', timeout_class='build-poll-deadline', build=build)

    def resolve_build_image(self, c):
        env = dict(os.environ, PLAN_PATH=str(self.directory / 'plan.json'), ROOT=str(ROOT),
                   SOURCE_SHA=self.plan['source'], SOURCE_REF=self.plan['branch'],
                   GITHUB_RUN_ID=os.environ.get('GITHUB_RUN_ID', '1'),
                   GITHUB_RUN_ATTEMPT=os.environ.get('GITHUB_RUN_ATTEMPT', '1'))
        image = run(['bash', ROOT / 'deploy/components' / (c + '.sh'), 'resolve'], env=env,
                    timeout=60, stage='auth-build')
        self.valid_image(c, image)
        return {'image': image}

    def prepare(self, c):
        if c in ('worker', 'exportjob'):
            env = dict(os.environ, PLAN_PATH=str(self.directory / 'plan.json'), ROOT=str(ROOT),
                       SOURCE_SHA=self.plan['source'], SOURCE_REF=self.plan['branch'],
                       GITHUB_RUN_ID=os.environ.get('GITHUB_RUN_ID', '1'),
                       GITHUB_RUN_ATTEMPT=os.environ.get('GITHUB_RUN_ATTEMPT', '1'))
            image = run(['bash', ROOT / 'deploy/components' / (c + '.sh'), 'build'], env=env,
                        timeout=1800).splitlines()[-1]
            self.valid_image(c, image)
            return {'image': image}
        require(c == 'frontend', 'container-prepare-requires-build-checkpoint')
        self.project(stage='frontend-project-readback')
        cfg = self.p['frontend']
        env = dict(os.environ, NEXT_PUBLIC_API_URL=cfg['api_url'], NEXT_PUBLIC_AUTH_URL=cfg['auth_url'])
        target = 'production' if self.p['environment'] == 'production' else 'preview'
        run(['npm', 'ci', '--ignore-scripts'], cwd=ROOT / 'apps/frontend', timeout=600,
            stage='frontend-npm-ci')
        run(['vercel', 'pull', '--yes', '--environment='+target, '--scope', cfg['team_slug'],
             '--token', os.environ['VERCEL_TOKEN']], env=env, cwd=ROOT, timeout=30,
            stage='frontend-vercel-pull')
        run(['vercel', 'build', '--scope', cfg['team_slug'], '--token', os.environ['VERCEL_TOKEN'],
             *(['--prod'] if target == 'production' else [])], env=env, cwd=ROOT, timeout=900,
            stage='frontend-vercel-build')
        output = ROOT / '.vercel/output'
        require(output.is_dir(), 'frontend-output-missing')
        expected = {'schema_version': 1, 'api_url': cfg['api_url'], 'auth_url': cfg['auth_url']}
        require(read(output / 'static/build-config.json') == expected, 'frontend-build-config-mismatch')
        archive = self.directory / 'frontend.tgz'
        with tarfile.open(archive, 'w:gz') as tar:
            tar.add(output, arcname='.vercel/output')
            tar.add(ROOT / '.vercel/project.json', arcname='.vercel/project.json')
        import hashlib
        return {'archive': 'frontend.tgz', 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest(),
                'config': expected, 'target': target, 'project': os.environ['VERCEL_PROJECT_ID'],
                'team': os.environ['VERCEL_TEAM_ID']}

    def usable(self, c, artifact):
        if c != 'frontend':
            self.valid_image(c, artifact['image'])
        else:
            import hashlib
            require(artifact['archive'] == 'frontend.tgz', 'invalid-archive-path')
            require(hashlib.sha256((self.directory / 'frontend.tgz').read_bytes()).hexdigest() == artifact['sha256'], 'artifact-unusable')
            expected = {'schema_version': 1, 'api_url': self.p['frontend']['api_url'], 'auth_url': self.p['frontend']['auth_url']}
            require(artifact['config'] == expected and artifact['target'] == ('production' if self.p['environment'] == 'production' else 'preview'), 'artifact-config-incompatible')
            require(artifact['project'] == os.environ['VERCEL_PROJECT_ID'] and artifact['team'] == os.environ['VERCEL_TEAM_ID'], 'artifact-config-incompatible')

    @staticmethod
    def job_template(raw):
        return raw['spec']['template']['spec']['template']['spec']

    @staticmethod
    def service_template(raw):
        return raw['spec']['template']['spec']

    @staticmethod
    def revision_config(revision):
        # Preserve all effective annotations, including secret aliases and future
        # config keys. Exclude only controller/audit output, never a whole prefix.
        controller = {'run.googleapis.com/operation-id', 'run.googleapis.com/ingress-status',
                      'run.googleapis.com/urls', 'serving.knative.dev/creator',
                      'serving.knative.dev/lastModifier', 'serving.knative.dev/routes',
                      'client.knative.dev/user-image', 'run.googleapis.com/client-name',
                      'run.googleapis.com/client-version'}
        return {'spec': revision['spec'], 'annotations': {
            k: v for k, v in revision.get('metadata', {}).get('annotations', {}).items()
            if k not in controller}}

    def template_matches(self, raw, revision):
        template = raw['spec']['template']
        return (self.revision_config(template) == self.revision_config(revision) and
                template.get('metadata', {}).get('name', revision['metadata']['name']) == revision['metadata']['name'])

    def snapshot(self, c):
        if c == 'frontend':
            self.project()
            aliases = {a: self.alias(a) for a in self.p['frontend']['stable_aliases']}
            for d in aliases.values():
                require(self.deployment(d)['readyState'] == 'READY', 'unusable-prior-deployment')
            return {'aliases': aliases}
        raw = self.describe(c)
        if c in ('auth', 'bff'):
            traffic = raw['status']['traffic']
            require(len(traffic) == 1 and traffic[0]['percent'] == 100 and not traffic[0].get('tag'), 'unrepresentable-prior-traffic')
            revision = self.revision(c, traffic[0]['revisionName'])
            require(any(x['type'] == 'Ready' and x['status'] == 'True' for x in revision['status']['conditions']), 'prior-not-ready')
            image = revision['status']['imageDigest']
            require('@sha256:' in image, 'mutable-prior-image')
            template = self.service_template(raw)
            require(self.template_matches(raw, revision) and len(template['containers']) == 1 and template['containers'][0]['image'] == image, 'unrepresentable-prior-service-template')
            return {'revision': revision['metadata']['name'], 'image': image,
                    'template_fingerprint': digest(self.revision_config(raw['spec']['template'])),
                    'fingerprint': digest(self.revision_config(revision)),
                    'traffic': [{'revisionName': revision['metadata']['name'], 'percent': 100}]}
        template = self.job_template(raw)
        require(len(template['containers']) == 1, 'unrepresentable-job')
        container = template['containers'][0]
        require(re.fullmatch(r'.+@sha256:[0-9a-f]{64}', container['image']), 'mutable-prior-image')
        # Only fields this profile changes are retained. No plaintext credentials.
        prior = {'image': container['image']}
        if c == 'worker':
            prior['env'] = [x for x in container.get('env', []) if x['name'] in ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID')]
            require(all(set(x) == {'name', 'value'} and isinstance(x['value'], str) for x in prior['env']), 'unrepresentable-job-env')
        else:
            require(self.export_matches(raw), 'prior-export-config-incompatible')
        return prior

    def export_matches(self, raw):
        t = self.job_template(raw)
        cfg = self.p['export_job']
        env = {x['name']: x.get('value') for x in t['containers'][0].get('env', [])}
        execution = raw['spec']['template']['spec']
        return (t.get('serviceAccountName') == cfg['runtime_service_account'] and
                all(env.get(k) == v for k, v in {'GCP_PROJECT': self.p['gcp']['project_id'], 'BUCKET': cfg['bucket'],
                    'FIRESTORE_DATABASE_ID': cfg['firestore_database_id'], 'EXPORT_SIGNING_SERVICE_ACCOUNT': cfg['signing_service_account']}.items()) and
                str(t.get('timeoutSeconds')) == '82800' and t.get('maxRetries') == cfg['max_retries'] and
                execution.get('parallelism') == cfg['parallelism'] and execution.get('taskCount') == cfg['tasks'])

    def service_matches(self, c, revision, image):
        expected = auth_config.desired(self.p, c)
        actual = auth_config.effective(revision, self.p['gcp']['project_id'], c,
            set(expected['env']) == {auth_config.QUERY_PATH},
            c == 'bff' and (self.p['environment'] == 'development' or self.p['auth'].get('google') is None),
            c == 'bff', c == 'bff' and auth_config.PIPELINE_DEMO_USER_IDS in expected['env'],
            c == 'bff' and (self.p['environment'] == 'development' or self.p['export_job']['enabled']))
        return (actual == expected and revision['status']['imageDigest'] == image and
                revision['spec']['containers'][0]['image'] == image and
                any(x['type'] == 'Ready' and x['status'] == 'True' for x in revision['status']['conditions']))

    def observe(self, c, artifact, candidate, prior=False):
        if c == 'frontend':
            aliases = artifact['aliases'] if prior else {a: candidate.get('deployment') for a in self.p['frontend']['stable_aliases']}
            if not all(aliases.values()):
                raise Breakpoint('deployment-identity-unknown', 'unknown', True, 'reconcile-before-replay')
            for alias, deployment in aliases.items():
                d = self.deployment(deployment)
                if d['readyState'] != 'READY' or self.alias(alias) != deployment:
                    return False
                if not prior:
                    if d.get('meta', {}).get('lwcArtifact') != artifact['sha256'] or (d.get('target') or 'preview') != artifact['target']:
                        return False
                    output = self.api('/v6/deployments/'+deployment+'/files/outputs?file=build-config.json', output=True)
                    if output != artifact['config']:
                        return False
            return True
        raw = self.describe(c)
        if c in ('auth', 'bff'):
            traffic = raw['status']['traffic']
            if len(traffic) != 1 or traffic[0]['percent'] != 100 or traffic[0].get('tag'):
                return False
            revision = self.revision(c, traffic[0]['revisionName'])
            if prior:
                return (revision['metadata']['name'] == artifact['revision'] and
                        self.template_matches(raw, revision) and
                        digest(self.revision_config(raw['spec']['template'])) == artifact['template_fingerprint'] and
                        digest(self.revision_config(revision)) == artifact['fingerprint'])
            return (self.service_matches(c, revision, artifact['image']) and
                    self.template_matches(raw, revision) and
                    raw['spec']['template']['metadata']['name'] == revision['metadata']['name'] and
                    (not candidate.get('revision') or candidate['revision'] == revision['metadata']['name']))
        t = self.job_template(raw)
        if len(t['containers']) != 1 or t['containers'][0]['image'] != artifact['image']:
            return False
        if c == 'exportjob':
            return self.export_matches(raw)
        env = [x for x in t['containers'][0].get('env', []) if x['name'] in ('GCP_PROJECT', 'FIRESTORE_DATABASE_ID')]
        expected = artifact['env'] if prior else [{'name': 'GCP_PROJECT', 'value': self.p['gcp']['project_id']},
                           {'name': 'FIRESTORE_DATABASE_ID', 'value': self.p['bff']['firestore_database_id']}]
        return sorted(env, key=lambda x:x['name']) == sorted(expected, key=lambda x:x['name'])

    def poll(self, c, artifact, candidate, prior=False):
        for n in range(12):
            try:
                if self.observe(c, artifact, candidate, prior):
                    return
            except Breakpoint as exc:
                raise Breakpoint(exc.reason, 'unknown', True, 'reconcile-before-replay') from None
            if n < 11:
                time.sleep(5)
        raise Breakpoint('basic-sanity-mismatch', 'failed', True, 'rollback')

    def deploy(self, c, artifact, candidate, save):
        if c == 'frontend':
            if not candidate.get('deployment'):
                with tempfile.TemporaryDirectory() as temp:
                    with tarfile.open(self.directory / artifact['archive']) as tar:
                        tar.extractall(temp, filter='data')
                    args = ['vercel', 'deploy', '--prebuilt', '--yes', '--scope', self.p['frontend']['team_slug'],
                            '--token', os.environ['VERCEL_TOKEN'], '--meta', 'lwcArtifact='+artifact['sha256'],
                            '--meta', 'lwcAttempt='+self.plan['id']]
                    if artifact['target'] == 'production':
                        args += ['--prod', '--skip-domain']
                    else:
                        args += ['--target=preview']
                    # Detached archive deployment: no branch-domain auto-assignment.
                    env = {k:v for k,v in os.environ.items() if not k.startswith(('GITHUB_', 'VERCEL_GIT_'))}
                    url = run(args, cwd=temp, env=env, mutation=True).splitlines()[-1].removeprefix('https://')
                    d = self.deployment(url)
                    candidate['deployment'] = d['id']
                    save()
            for alias in self.p['frontend']['stable_aliases']:
                if self.alias(alias) != candidate['deployment']:
                    self.api('/v2/deployments/'+candidate['deployment']+'/aliases', {'alias': alias})
            return
        kind, name = self.resource(c)
        args = [kind, 'update', name, '--image', artifact['image']]
        if c in ('auth', 'bff'):
            if not candidate.get('revision'):
                flags = run(['python3', ROOT / 'deploy/components/auth_config.py', 'args', self.directory / 'plan.json', c]).splitlines()
                result = json.loads(self.cloud(c, *args, '--no-traffic', *flags, mutation=True))
                candidate['revision'] = result['status']['latestCreatedRevisionName']
                save()
            revision = candidate['revision']
            retained = self.revision(c, revision)
            require(self.service_matches(c, retained, artifact['image']), 'candidate-config-not-ready')
            raw = self.describe(c)
            if not self.template_matches(raw, retained) or raw['spec']['template'].get('metadata', {}).get('name') != revision:
                self.restore_service(c, retained, [{'revisionName': revision, 'percent': 100}])
            else:
                self.cloud(c, 'services', 'update-traffic', name, '--to-revisions', revision+'=100', mutation=True)
        else:
            if c == 'worker':
                args += ['--update-env-vars', '^|^GCP_PROJECT='+self.p['gcp']['project_id']+'|FIRESTORE_DATABASE_ID='+self.p['bff']['firestore_database_id']]
            self.cloud(c, *args, mutation=True)

    def reconcile_candidate(self, c, artifact, candidate, save):
        """Discover accepted creation before allowing continuation; never replay creation."""
        if c in ('auth', 'bff') and not candidate.get('revision'):
            raw = self.describe(c)
            name = raw['status']['latestCreatedRevisionName']
            if self.service_matches(c, self.revision(c, name), artifact['image']):
                candidate['revision'] = name
                save()
        if c == 'frontend' and not candidate.get('deployment'):
            result = self.api('/v6/deployments?projectId='+os.environ['VERCEL_PROJECT_ID']+'&limit=100')
            matches = [d for d in result['deployments'] if d.get('meta', {}).get('lwcAttempt') == self.plan['id']
                       and d.get('meta', {}).get('lwcArtifact') == artifact['sha256']]
            require(len(matches) == 1, 'deployment-identity-unknown')
            candidate['deployment'] = matches[0].get('uid', matches[0].get('id'))
            save()

    def restore_service(self, c, revision, traffic):
        # Use the retained revision name and exact spec: never mint a substitute
        # revision or rebuild an artifact if the provider rejects this restore.
        _, name = self.resource(c)
        raw = self.describe(c)
        template = {'metadata': {'name': revision['metadata']['name'],
                    'annotations': self.revision_config(revision)['annotations']},
                    'spec': revision['spec']}
        body = {'apiVersion': 'serving.knative.dev/v1', 'kind': 'Service',
                'metadata': {'name': name, 'annotations': raw['metadata'].get('annotations', {})},
                'spec': {'template': template, 'traffic': traffic}}
        # Raw configuration is private and ephemeral, not uploaded evidence.
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'restore.json'
            write(path, body)
            self.cloud(c, 'services', 'replace', path, mutation=True)

    def rollback(self, c, prior):
        if c == 'frontend':
            for alias, deployment in prior['aliases'].items():
                require(self.deployment(deployment)['readyState'] == 'READY', 'prior-deployment-unusable')
                if self.alias(alias) != deployment:
                    self.api('/v2/deployments/'+deployment+'/aliases', {'alias': alias})
            return
        kind, name = self.resource(c)
        if c in ('auth', 'bff'):
            revision = self.revision(c, prior['revision'])
            require(digest(self.revision_config(revision)) == prior['fingerprint'], 'prior-revision-changed')
            self.restore_service(c, revision, prior['traffic'])
        else:
            args = [kind, 'update', name, '--image', prior['image']]
            if c == 'worker':
                env = {x['name']: x['value'] for x in prior['env']}
                require(all('|' not in v and '\n' not in v for v in env.values()), 'unrepresentable-prior-env')
                if env:
                    args += ['--update-env-vars', '^|^'+'|'.join(k+'='+v for k,v in env.items())]
                missing = set(('GCP_PROJECT', 'FIRESTORE_DATABASE_ID')) - env.keys()
                if missing:
                    args += ['--remove-env-vars', ','.join(sorted(missing))]
            self.cloud(c, *args, mutation=True)
