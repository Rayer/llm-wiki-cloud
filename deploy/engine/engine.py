#!/usr/bin/env python3
"""LWC-358 two-stage release engine. Runtime entry is owned by Actions."""
import argparse
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import urllib.parse

from support import (ROOT, Breakpoint, digest, read, release_source_mode, require, run,
                     structured_cause, write)
from providers import Providers, CLOUD_BUILD_LOCATION
from receipt_contract import (ReceiptError, validate_auth_component_receipt,
                              validate_dev_receipt, validate_receipt_pair)

ORDER = ('documentimport', 'exportjob', 'auth', 'bff', 'worker', 'frontend')
PROFILES = read(ROOT / 'deploy/engine/profiles.json')
RELEASE_IDENTITY_FIELDS = ('source', 'branch', 'tag', 'normalized', 'identities',
                           'dev_reference', 'selected')
CHECKPOINT_STATUSES = frozenset({
    'prepared', 'ready', 'snapshotted', 'deploying', 'failed', 'unknown',
    'rolling_back', 'recovery_failed', 'failed_rolled_back', 'runtime_success',
    'partially_reactivated', 'rolled_back', 'tag_failed', 'success',
})
RESOLVED_TARGET_STATUSES = frozenset({
    'success', 'rolled_back', 'failed_rolled_back', 'ready', 'prepared',
})
def release_identity(plan):
    identity = {key: plan[key] for key in RELEASE_IDENTITY_FIELDS}
    if 'release_source_mode' in plan:
        identity['release_source_mode'] = plan['release_source_mode']
    if 'auth_source' in plan:
        identity['auth_source'] = plan['auth_source']
    return identity


def compatible_release_identity(plan):
    identity = release_identity(plan)
    if 'release_source_mode' not in identity and release_source_mode(plan) == 'legacy_monorepo':
        identity['release_source_mode'] = 'legacy_monorepo'
    return identity


def plan_id(plan):
    if plan.get('schema') == 2:
        return digest({key: value for key, value in plan.items() if key != 'id'})
    if plan.get('schema') == 3:
        return digest(release_identity(plan))
    raise Breakpoint('unsupported-plan-schema', 'failed', False, 'inspect-retained-artifact')


def _phase_cause(phase, exc, stage):
    if not isinstance(exc, Breakpoint):
        return {'phase': phase, **structured_cause(exc, stage)}
    cause = {'phase': phase, 'reason': exc.reason, 'status': exc.status}
    if exc.stage or stage != 'unknown':
        cause['stage'] = exc.stage or stage
    if exc.exit_code is not None:
        cause['exit_code'] = exc.exit_code
    if exc.timeout_class is not None:
        cause['timeout_class'] = exc.timeout_class
    if exc.cause is not None:
        cause['cause'] = exc.cause
    return cause


def selection(raw):
    values = raw.split(',')
    require(values and len(set(values)) == len(values) and all(c in ORDER for c in values), 'invalid-components')
    return [c for c in ORDER if c in values]


def source_identity(c, sha, auth_source=None):
    """Hash build inputs, not release SHA or engine scripts. Go resolves package dependencies."""
    profile = PROFILES[c]
    if c == 'auth' and auth_source is not None:
        return 'sha256:' + digest({'profile': profile, 'auth_source': auth_source})
    tracked = run(['git', 'ls-tree', '-r', '--name-only', sha]).splitlines()
    roots = list(profile['inputs'])
    package_dirs = []
    if c != 'frontend':
        module_root = ROOT / profile.get('source_root', 'apps/bff')
        module_rel = Path(profile.get('source_root', 'apps/bff'))
        if module_rel == Path('gateauth'):
            roots += ['gateauth/go.mod', 'gateauth/go.sum', 'gateauth/.dockerignore', 'gateauth/.gcloudignore', 'gateauth/.gitignore']
        else:
            roots += ['apps/bff/go.mod', 'apps/bff/go.sum', 'apps/bff/.dockerignore', 'apps/bff/.gcloudignore', 'apps/bff/.gitignore']
        go_env = os.environ.copy()
        go_env.update({'GOWORK': 'off', 'GOFLAGS': '-mod=readonly', 'GOTOOLCHAIN': 'local'})
        dirs = run(['go', 'list', '-deps', '-f', '{{if and (not .Standard) .Module}}{{if .Module.Main}}{{.Dir}}{{range .EmbedFiles}}{{println}}{{$.Dir}}/{{.}}{{end}}{{end}}{{end}}',
                    profile['package']], cwd=module_root, env=go_env, timeout=180).splitlines()
        for d in dirs:
            if d.strip():
                rel = Path(d).relative_to(ROOT).as_posix()
                if Path(d).is_dir():
                    package_dirs.append(rel)
                # A package directory does not recursively own sibling subpackages.
                roots += [rel] if Path(d).is_file() else [p for p in tracked if str(Path(p).parent) == rel and not p.endswith('_test.go')]
    untracked = run(['git', 'ls-files', '--others', '--exclude-standard']).splitlines()
    require(not any((str(Path(p).parent) in package_dirs and p.endswith('.go')) or
                    any(p == r or p.startswith(r.rstrip('/') + '/') for r in roots) for p in untracked), 'untracked-build-input')
    paths = sorted(p for p in tracked if any(p == r or p.startswith(r.rstrip('/') + '/') for r in roots))
    require(paths, 'empty-build-inputs')
    rows = []
    for p in paths:
        if c == 'frontend' and ('/tests/' in p or p.endswith('AGENTS.md')):
            continue
        # Reject dirty build inputs: receipts must describe what builders consume.
        blob = run(['git', 'rev-parse', sha+':'+p])
        require(run(['git', 'hash-object', p]) == blob, 'dirty-build-input')
        rows.append([p, blob])
    return {'profile': digest(profile), 'inputs': digest(rows), 'files': rows}


def auth_source_from_receipt(path):
    path = Path(path)
    if not path.is_file():
        return None
    try:
        receipt = read(path)
    except (OSError, Breakpoint, ValueError, TypeError):
        return None
    source = receipt.get('auth_source') if isinstance(receipt, dict) else None
    if (receipt.get('component') != 'auth' or receipt.get('schema') != 3 or
            not isinstance(source, dict)):
        return None
    return {
        'repository_id': source.get('repository_id'),
        'revision': source.get('revision'),
        'tree_sha256': source.get('tree_sha256'),
        'artifact_id': source.get('artifact_id'),
        'run_id': source.get('build_run_id'),
        'run_attempt': source.get('build_run_attempt'),
    }


def admit(args):
    selected = selection(args.components)
    release_source_mode = getattr(args, 'release_source_mode', None)
    require(release_source_mode in ('independent', 'legacy_monorepo'),
            'release-source-mode-required')
    require(re.fullmatch(r'[0-9a-f]{40}', args.source), 'invalid-source')
    executor_sha = getattr(args, 'executor_sha', None) or run(['git', 'rev-parse', 'HEAD'])
    require(re.fullmatch(r'[0-9a-f]{40}', executor_sha), 'invalid-executor-sha')
    require(run(['git', 'rev-parse', 'HEAD']) == executor_sha, 'executor-checkout-mismatch')
    require(run(['git', 'rev-parse', args.source+'^{commit}']) == args.source, 'invalid-source')
    require(args.tag and not args.tag.startswith('-'), 'invalid-release-tag')
    run(['git', 'check-ref-format', 'refs/tags/'+args.tag])
    branch = 'develop' if args.environment == 'development' else 'main'
    cfg = 'deploy/environments/'+args.environment+'.yaml'
    require(run(['git', 'hash-object', cfg]) == run(['git', 'rev-parse', args.source+':'+cfg]), 'dirty-target-config')
    normalize_args = ['go', 'run', './cmd/deploy_config', '--environment', args.environment,
                      '--config', str(ROOT / cfg), '--components', ','.join(selected)]
    normalize_env = os.environ.copy()
    normalize_env['LWC_REPOSITORY_ROOT'] = str(ROOT)
    if 'bff' in selected or 'auth' in selected or (
            release_source_mode == 'independent' and
            any(c in selected for c in ('worker', 'exportjob'))):
        ssot = 'deploy/cac/ssot.pkl'
        require(run(['git', 'hash-object', ssot]) == run(['git', 'rev-parse', args.source+':'+ssot]),
                'dirty-runtime-ssot')
        target = {'development': 'dev', 'production': 'prod'}[args.environment]
        with tempfile.TemporaryDirectory(prefix='lwc-runtime-inputs-') as projection_dir:
            projection_root = Path(projection_dir)
            if 'auth' in selected or release_source_mode == 'independent':
                auth_dir = projection_root / 'auth'
                auth_dir.mkdir()
                run(['go', 'run', './cmd/pipeline_config', 'prepare', '--target', 'auth', '--descriptor',
                     '--environment', target, '--source-sha', args.source, '--output', str(auth_dir)],
                    cwd=ROOT / 'apps/bff', env=normalize_env, timeout=180)
                if 'auth' in selected:
                    normalize_args.extend(['--auth-inputs', str(auth_dir / 'auth-inputs.json')])
            if 'bff' in selected:
                bff_dir = projection_root / 'bff'
                bff_dir.mkdir()
                run(['go', 'run', './cmd/pipeline_config', 'prepare', '--target', 'bff', '--descriptor',
                     '--environment', target, '--output', str(bff_dir)],
                    cwd=ROOT / 'apps/bff', env=normalize_env, timeout=180)
                normalize_args.extend(['--bff-inputs', str(bff_dir / 'bff-inputs.json')])
            normalized = json.loads(run(normalize_args, cwd=ROOT / 'apps/bff', env=normalize_env, timeout=180))
            if release_source_mode == 'independent' and 'auth' not in selected:
                auth_normalize_args = [
                    'go', 'run', './cmd/deploy_config', '--environment', args.environment,
                    '--config', str(ROOT / cfg), '--components', 'auth',
                    '--auth-inputs', str(auth_dir / 'auth-inputs.json'),
                ]
                auth_normalized = json.loads(run(
                    auth_normalize_args, cwd=ROOT / 'apps/bff', env=normalize_env, timeout=180))
                normalized['auth'] = auth_normalized['auth']
    else:
        normalized = json.loads(run(normalize_args, cwd=ROOT / 'apps/bff', env=normalize_env, timeout=180))
    raw_auth_source = {
        'repository_id': getattr(args, 'auth_repository_id', None),
        'revision': getattr(args, 'auth_revision', None),
        'tree_sha256': getattr(args, 'auth_tree_sha256', None),
        'artifact_id': getattr(args, 'auth_artifact_id', None),
        'run_id': getattr(args, 'auth_run_id', None),
        'run_attempt': getattr(args, 'auth_run_attempt', None),
    }
    provided_auth_source = any(value not in (None, '') for value in raw_auth_source.values())
    auth_source = None
    if release_source_mode == 'legacy_monorepo':
        require(not provided_auth_source, 'legacy-release-cannot-use-independent-auth-source')
    elif provided_auth_source:
        require(all(value not in (None, '') for value in raw_auth_source.values()),
                'independent-auth-source-required')
        repo_id = str(raw_auth_source['repository_id'])
        revision = str(raw_auth_source['revision'])
        tree_sha256 = str(raw_auth_source['tree_sha256'])
        artifact_id = str(raw_auth_source['artifact_id'])
        run_id = str(raw_auth_source['run_id'])
        run_attempt = str(raw_auth_source['run_attempt'])
        require(re.fullmatch(r'[1-9][0-9]*', repo_id) is not None and
                repo_id != os.environ.get('GITHUB_REPOSITORY_ID') and
                re.fullmatch(r'[0-9a-f]{40}', revision) is not None and revision != args.source and
                re.fullmatch(r'sha256:[0-9a-f]{64}', tree_sha256) is not None and
                re.fullmatch(r'[1-9][0-9]*', artifact_id) is not None and
                re.fullmatch(r'[1-9][0-9]*', run_id) is not None and
                re.fullmatch(r'[1-9][0-9]*', run_attempt) is not None,
                'independent-auth-source-invalid')
        auth_source = {'repository_id': 'github:' + repo_id, 'revision': revision,
                       'tree_sha256': tree_sha256, 'artifact_id': artifact_id,
                       'run_id': int(run_id), 'run_attempt': int(run_attempt)}
    elif (release_source_mode == 'independent' and 'auth' in selected and
          args.environment == 'production' and args.dev):
        auth_source = auth_source_from_receipt(Path(args.dev) / 'receipts/auth.json')
        require(auth_source is not None, 'independent-auth-source-required')
    elif (release_source_mode == 'independent' and 'auth' not in selected and
          args.environment == 'development' and args.reuse):
        auth_source = auth_source_from_receipt(Path(args.reuse) / 'receipts/auth.json')
        require(auth_source is not None, 'independent-auth-source-required')
    elif (release_source_mode == 'independent' and 'auth' in selected and
          args.environment == 'development' and args.reuse):
        auth_source = auth_source_from_receipt(Path(args.reuse) / 'receipts/auth.json')
        require(auth_source is not None, 'independent-auth-source-required')
    if (release_source_mode == 'independent' and 'auth' in selected and
            args.environment == 'development'):
        require(auth_source is not None, 'independent-auth-source-required')
    if release_source_mode == 'independent' and 'auth' in selected and args.environment == 'production':
        require(auth_source is not None, 'independent-auth-source-required')
    identities = {c: source_identity(c, args.source,
                                     auth_source if c == 'auth' else None)
                  for c in selected}
    body = {'schema': 3, 'source': args.source, 'branch': branch, 'tag': args.tag,
            'executor_sha': executor_sha, 'normalized': normalized, 'identities': identities,
            'dev_reference': args.dev_reference, 'selected': selected,
            'release_source_mode': release_source_mode}
    if auth_source is not None:
        body['auth_source'] = auth_source
    body['id'] = digest(release_identity(body))
    return body


def engine_fingerprint():
    paths = sorted([* (ROOT / 'deploy/engine').glob('*.py'), ROOT / 'deploy/engine/profiles.json',
                    * (ROOT / 'deploy/components').glob('*.sh'), ROOT / 'deploy/engine/artifacts.cjs', ROOT / '.github/actions/deployment-engine/index.cjs', ROOT / '.github/actions/deployment-engine/action.yml',
                    * (ROOT / '.github/workflows').glob('*deploy*.yml'), ROOT / '.github/workflows/cd.yml', ROOT / '.github/workflows/promote-production.yml', * (ROOT / 'deploy/components').glob('*.py'),
                    * (ROOT / 'apps/bff/cmd/pipeline_config').glob('*.go'),
                    * (ROOT / 'apps/bff/cmd/deploy_config').glob('*.go'),
                    ROOT / 'apps/bff/internal/config/auth_file.go', ROOT / 'apps/bff/internal/config/bff_file.go',
                    ROOT / 'deploy/cac/ssot.pkl'])
    return digest([[str(p.relative_to(ROOT)), hashlib.sha256(p.read_bytes()).hexdigest()] for p in paths])


class Engine:
    def __init__(self, directory, executor_sha=None):
        self.directory = Path(directory).resolve()
        self.plan = read(self.directory / 'plan.json')
        require(self.plan.get('id') == plan_id(self.plan), 'plan-content-mismatch')
        self.executor_sha = (executor_sha or os.environ.get('GITHUB_SHA') or
                             self.plan.get('executor_sha'))
        require(self.executor_sha is None or
                re.fullmatch(r'[0-9a-f]{40}', self.executor_sha) is not None,
                'invalid-executor-sha')
        if self.plan['schema'] == 2:
            require(re.fullmatch(r'[0-9a-f]{64}', self.plan.get('engine_content', '')) is not None and
                    re.fullmatch(r'[0-9a-f]{40}', self.plan.get('engine', '')) is not None,
                    'plan-content-mismatch')
        else:
            require(re.fullmatch(r'[0-9a-f]{40}', self.plan.get('executor_sha', '')) is not None,
                    'plan-content-mismatch')
        require(self.plan['selected'] == selection(','.join(self.plan['selected'])), 'invalid-plan-selection')
        self.provider = Providers(self.plan, self.directory)
        self.state_path = self.directory / 'state.json'
        self.state = read(self.state_path) if self.state_path.exists() else {
            'plan': self.plan['id'], 'status': 'prepared', 'components': {}, 'sequence': 0,
            'checkpoint_schema': 1}
        if self.executor_sha:
            self.state.setdefault('executor_sha', self.executor_sha)
        require(isinstance(self.state, dict) and
                all(key in self.state for key in ('plan', 'status', 'components', 'sequence')) and
                self.state['plan'] == self.plan['id'] and isinstance(self.state['status'], str) and
                isinstance(self.state['components'], dict) and
                isinstance(self.state['sequence'], int) and not isinstance(self.state['sequence'], bool) and
                self.state['sequence'] >= 0 and
                type(self.state.get('checkpoint_schema', 1)) is int and
                self.state.get('checkpoint_schema', 1) == 1,
                'checkpoint-plan-mismatch')
        self.state.setdefault('checkpoint_schema', 1)
        self.state.setdefault('builds', {})
        require(isinstance(self.state['builds'], dict), 'checkpoint-plan-mismatch')
        self.component = None
        self.force_bypass = None

    def save(self):
        self.state['checkpoint_schema'] = 1
        if self.executor_sha:
            self.state['executor_sha'] = self.executor_sha
        self.state['sequence'] += 1
        write(self.state_path, self.state)
        if os.environ.get('GITHUB_ACTIONS') == 'true':
            # Every pending marker is durable before mutation. A failed upload stops work.
            run(['node', ROOT / 'deploy/engine/artifacts.cjs', 'upload', self.directory,
                 ('lwc-prepared-' if self.state['status'] in ('prepared', 'ready') else 'lwc-state-')+self.plan['normalized']['environment']+'-'+self.plan['id'][:16]+'-'+
                 os.environ['GITHUB_RUN_ID']+'-'+os.environ['GITHUB_RUN_ATTEMPT']+'-'+str(self.state['sequence'])], timeout=120)

    def receipt(self, c):
        r = read(self.directory / 'receipts' / (c+'.json'))
        require(r['component'] == c and r['identity'] == self.plan['identities'][c], 'artifact-source-incompatible')
        if c == 'frontend':
            require(r['target_config'] == digest(self.plan['normalized']['frontend']), 'artifact-config-incompatible')
        if c == 'auth' and release_source_mode(self.plan) == 'independent':
            source = self.plan.get('auth_source')
            require(isinstance(source, dict), 'independent-auth-source-required')
            try:
                expected_source = {
                    'repository_id': source['repository_id'],
                    'revision': source['revision'],
                    'tree_sha256': source['tree_sha256'],
                    'artifact_id': source['artifact_id'],
                    'build_run_id': source['run_id'],
                    'build_run_attempt': source['run_attempt'],
                }
                validate_auth_component_receipt(
                    r, 'independent',
                    self.plan.get('source') if self.plan['normalized']['environment'] == 'development' else None,
                    self.auth_input_snapshot() if self.plan['normalized']['environment'] == 'development' else None,
                    expected_source)
            except (ReceiptError, KeyError, TypeError) as exc:
                raise Breakpoint('artifact-auth-receipt-incompatible', 'failed', False,
                                 'inspect-retained-artifact', cause=str(exc)) from None
        self.provider.usable(c, r['artifact'])
        return r

    def validate_dev_receipt_pair(self):
        if (release_source_mode(self.plan) != 'independent' or
                self.plan['normalized']['environment'] != 'development'):
            return
        components = [c for c in self.plan['selected']
                      if c in ('auth', 'bff', 'worker', 'exportjob')]
        if not components:
            return
        auth_path = self.directory / 'receipts/auth.json'
        dev_path = self.directory / 'images/dev-receipt.json'
        metadata_path = self.directory / 'dev-source-metadata.json'
        require(auth_path.is_file() and dev_path.is_file() and metadata_path.is_file(),
                'artifact-auth-dev-pair-missing')
        auth = read(auth_path)
        dev = read(dev_path)
        metadata = read(metadata_path)
        source = self.plan.get('auth_source')
        require(isinstance(auth, dict) and isinstance(dev, dict) and isinstance(metadata, dict) and
                isinstance(metadata.get('lwc'), dict) and isinstance(metadata.get('auth'), dict),
                'artifact-auth-dev-pair-incompatible')
        require(isinstance(source, dict), 'independent-auth-source-required')
        expected_auth = metadata.get('auth', {})
        require(metadata.get('lwc', {}).get('sha') == self.plan.get('source') and
                expected_auth.get('repository_id') == source.get('repository_id') and
                expected_auth.get('revision') == source.get('revision') and
                expected_auth.get('tree_sha256') == source.get('tree_sha256') and
                expected_auth.get('artifact_id') == source.get('artifact_id') and
                expected_auth.get('run_id') == source.get('run_id') and
                expected_auth.get('run_attempt') == source.get('run_attempt'),
                'artifact-auth-dev-source-mismatch')
        try:
            validate_receipt_pair(
                auth, dev, 'independent',
                expected_auth_config_inputs=self.auth_input_snapshot(),
                expected_lwc_source=metadata['lwc'], expected_auth_source=expected_auth,
                expected_components=components)
        except (ReceiptError, KeyError, TypeError) as exc:
            raise Breakpoint('artifact-auth-dev-pair-incompatible', 'failed', False,
                             'inspect-retained-artifact', cause=str(exc)) from None

    def auth_package_preflight(self, reuse=None, dev=None):
        """Require a verified compatible retained Auth receipt before skipping package fetch."""
        if (release_source_mode(self.plan) != 'independent' or
                'auth' not in self.plan['selected']):
            return False
        source_directory = Path(dev).resolve() if dev else (Path(reuse).resolve() if reuse else None)
        if source_directory is None:
            return True
        if dev:
            Engine(dev).validate_dev_receipt_pair()
        receipt_path = source_directory / 'receipts/auth.json'
        if not receipt_path.is_file():
            return True
        try:
            candidate = read(receipt_path)
            validate_auth_component_receipt(candidate, 'independent')
        except (Breakpoint, ReceiptError, KeyError, OSError, TypeError, ValueError) as exc:
            raise Breakpoint('artifact-auth-receipt-incompatible', 'failed', False,
                             'inspect-retained-artifact', stage='auth-reuse-preflight',
                             cause=str(exc)) from None
        source = self.plan.get('auth_source')
        expected_source = {
            'repository_id': source['repository_id'], 'revision': source['revision'],
            'tree_sha256': source['tree_sha256'], 'artifact_id': source['artifact_id'],
            'build_run_id': source['run_id'], 'build_run_attempt': source['run_attempt'],
        }
        if (candidate.get('identity') != self.plan['identities']['auth'] or
                candidate.get('auth_source') != expected_source):
            return True
        try:
            self.provider.usable('auth', candidate['artifact'])
        except Breakpoint as exc:
            if exc.reason == 'artifact-unusable':
                return True
            raise
        return False

    def barrier(self):
        artifacts = {}
        for c in self.plan['selected']:
            receipt = self.receipt(c)
            if c == 'auth' and self.auth_input_snapshot() is not None:
                require(receipt.get('auth_config_inputs') == self.auth_input_snapshot() and
                        receipt.get('auth_materialization_schema') == 1,
                        'artifact-auth-input-snapshot-mismatch')
            artifacts[c] = receipt['artifact']
        self.validate_dev_receipt_pair()
        return artifacts

    def auth_input_snapshot(self):
        inputs = self.plan['normalized'].get('auth', {}).get('runtime_inputs')
        if inputs is None:
            return None
        schema_version = inputs.get('schema_version') if isinstance(inputs, dict) else None
        expected_keys = {
            'schema_version', 'environment', 'target', 'source_sha', 'config_id', 'gcp_project',
            'firestore_database_id', 'local_cloud_scope', 'auth_service_url', 'sync_service_url',
            'allowed_hosts', 'allowed_origins', 'auth_session_environment', 'auth_session_migration',
            'registration_enabled', 'auth_demo_user_id', 'auth_demo_user_email', 'auth_demo_user_role',
            'jwt_secret_version', 'google', 'config_secret_resource',
        }
        if schema_version == 2:
            expected_keys.add('oauth_resources')
        elif schema_version == 3:
            expected_keys.update(('oauth_resources', 'management_scopes', 'consumer_verifiers',
                                  'consumer_public_projection'))
        require(isinstance(inputs, dict) and type(schema_version) is int and schema_version in (1, 2, 3) and
                set(inputs) == expected_keys and
                inputs.get('target') == 'auth' and
                inputs.get('environment') == {'development': 'dev', 'production': 'prod'}.get(
                    self.plan['normalized'].get('environment')) and
                inputs.get('source_sha') == self.plan.get('source') and
                re.fullmatch(r'[0-9a-f]{40}', inputs.get('source_sha', '')) is not None and
                re.fullmatch(r'sha256:[0-9a-f]{64}', inputs.get('config_id', '')) is not None,
                'auth-input-snapshot-invalid')
        if schema_version == 3:
            require(isinstance(inputs['management_scopes'], list) and
                    all(isinstance(value, dict) for value in inputs['management_scopes']) and
                    isinstance(inputs['consumer_verifiers'], list) and
                    all(isinstance(value, dict) for value in inputs['consumer_verifiers']) and
                    isinstance(inputs['consumer_public_projection'], dict),
                    'auth-input-snapshot-invalid')
        return copy.deepcopy(inputs)

    def attach_auth_input_snapshot(self, receipt, *, require_match=False):
        inputs = self.auth_input_snapshot()
        if inputs is None:
            return receipt
        existing = receipt.get('auth_config_inputs')
        if require_match:
            require(existing == inputs, 'artifact-auth-input-snapshot-mismatch')
        receipt['auth_config_inputs'] = inputs
        receipt['auth_materialization_schema'] = 1
        return receipt

    @staticmethod
    def safe_build_record(build):
        fields = ('project_id', 'location', 'build_id', 'identity_verified', 'status',
                  'last_observed_status', 'poll_outcome', 'reported_project_id', 'reported_location')
        return {key: build[key] for key in fields if key in build}

    def prepare_container(self, c):
        builds = self.state['builds']
        build = builds.get(c)
        terminal_failures = {'FAILURE', 'INTERNAL_ERROR', 'TIMEOUT', 'CANCELLED', 'EXPIRED',
                             'SUBMIT_REJECTED'}
        if build and build.get('status') in terminal_failures:
            # A new prepare invocation is the explicit retry; the prior invocation
            # already reconciled that build to a terminal state.
            build = None
        if build and build.get('status') == 'SUCCESS' and build.get('identity_verified') is True:
            pass
        elif build:
            if build.get('identity_verified') is not True or not build.get('build_id'):
                raise Breakpoint('build-identity-unverified', 'unknown', False,
                                 'reconcile-before-replay', stage='build-submit', build=build)
            try:
                self.provider.poll_build(c, build)
            except Breakpoint:
                self.save()
                raise
            self.save()
        else:
            build = {'project_id': self.plan['normalized']['gcp']['project_id'],
                     'location': CLOUD_BUILD_LOCATION,
                     'build_id': None, 'identity_verified': False, 'status': 'SUBMITTING',
                     'last_observed_status': None, 'poll_outcome': 'submitting'}
            builds[c] = build
            self.save()
            try:
                submitted = self.provider.submit_build(c)
            except Breakpoint as exc:
                if exc.build:
                    build = exc.build
                    builds[c] = build
                elif exc.reason == 'permission-denied':
                    build.update(status='SUBMIT_REJECTED', poll_outcome='rejected')
                elif exc.stage == 'build-project-identity':
                    build.update(status='SUBMIT_REJECTED', poll_outcome='project_identity_unverified')
                else:
                    build.update(status='SUBMIT_UNKNOWN', poll_outcome='submit_unknown')
                self.save()
                if exc.reason == 'permission-denied':
                    raise Breakpoint('permission-denied', 'failed', False,
                                     'restore-existing-principal-permission', stage=exc.stage,
                                     exit_code=exc.exit_code, timeout_class=exc.timeout_class,
                                     build=build) from None
                if exc.stage == 'build-project-identity':
                    raise Breakpoint(exc.reason, 'failed', False, 'verify-project-identity',
                                     stage=exc.stage, exit_code=exc.exit_code,
                                     timeout_class=exc.timeout_class, build=build) from None
                if exc.build:
                    raise
                raise Breakpoint('build-submit-outcome-unknown', 'unknown', False,
                                 'reconcile-before-replay', stage=exc.stage,
                                 exit_code=exc.exit_code, timeout_class=exc.timeout_class,
                                 build=build) from None
            build = submitted
            builds[c] = build
            self.save()
            try:
                self.provider.poll_build(c, build)
            except Breakpoint:
                self.save()
                raise
            self.save()

        return self.provider.resolve_build_image(c)

    def resume_or_validate_reuse(self, old):
        """Restore only an exact Stage 1 attempt; never carry build handles across plans."""
        if old is None:
            return
        retained_plan_path = old / 'plan.json'
        retained_state_path = old / 'state.json'
        if not retained_plan_path.exists() and not retained_state_path.exists():
            return  # receipt-only reuse remains supported
        if not retained_plan_path.exists() or not retained_state_path.exists():
            raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                             'inspect-retained-artifact', stage='prepare')
        try:
            retained_plan = read(retained_plan_path)
            retained_state = read(retained_state_path)
        except (OSError, TypeError, ValueError):
            raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                             'inspect-retained-artifact', stage='prepare') from None
        if not isinstance(retained_plan, dict) or not isinstance(retained_state, dict):
            raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                             'inspect-retained-artifact', stage='prepare')
        old_id = retained_plan.get('id')
        builds = retained_state.get('builds', {})
        sequence = retained_state.get('sequence')
        try:
            selected = retained_plan['selected']
            plan_shape_valid = (
                retained_plan.get('schema') in (2, 3) and
                selected == selection(','.join(selected) if isinstance(selected, list) else '') and
                re.fullmatch(r'[0-9a-f]{40}', retained_plan.get('source', '')) is not None and
                ((retained_plan.get('schema') == 2 and
                  re.fullmatch(r'[0-9a-f]{64}', retained_plan.get('engine_content', '')) is not None and
                  re.fullmatch(r'[0-9a-f]{40}', retained_plan.get('engine', '')) is not None) or
                 (retained_plan.get('schema') == 3 and
                  re.fullmatch(r'[0-9a-f]{40}', retained_plan.get('executor_sha', '')) is not None)) and
                retained_plan.get('normalized', {}).get('environment') in ('development', 'production') and
                isinstance(retained_plan.get('identities'), dict) and
                set(selected) <= set(retained_plan['identities']) and
                isinstance(retained_plan['normalized']['gcp']['project_id'], str))
        except (AttributeError, Breakpoint, KeyError, TypeError, ValueError):
            plan_shape_valid = False
        try:
            old_id_valid = isinstance(old_id, str) and old_id == plan_id(retained_plan)
        except (Breakpoint, KeyError, TypeError, ValueError):
            old_id_valid = False
        if (not old_id_valid or not plan_shape_valid or
                retained_state.get('plan') != old_id or
                not isinstance(retained_state.get('components'), dict) or
                type(retained_state.get('checkpoint_schema', 1)) is not int or
                retained_state.get('checkpoint_schema', 1) != 1 or
                not isinstance(builds, dict) or
                not isinstance(sequence, int) or isinstance(sequence, bool) or sequence < 0 or
                not isinstance(retained_state.get('status'), str)):
            raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                             'inspect-retained-artifact', stage='prepare')

        checkpoint_statuses = {'SUBMITTING', 'SUBMIT_UNKNOWN', 'SUBMIT_REJECTED',
                               'IDENTITY_INVALID', 'IDENTITY_MISMATCH', 'SUBMITTED',
                               'PENDING', 'QUEUED', 'WORKING', 'SUCCESS', 'FAILURE',
                               'INTERNAL_ERROR', 'TIMEOUT', 'CANCELLED', 'EXPIRED',
                               'STATUS_UNKNOWN'}
        precreate_statuses = {'SUBMITTING', 'SUBMIT_UNKNOWN', 'SUBMIT_REJECTED',
                              'IDENTITY_INVALID'}
        for component, build in builds.items():
            if (component not in ('auth', 'bff', 'documentimport') or component not in retained_plan['selected'] or
                    not isinstance(build, dict) or build.get('status') not in checkpoint_statuses or
                    not isinstance(build.get('identity_verified'), bool)):
                raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                 'inspect-retained-artifact', stage='prepare')
            build_id = build.get('build_id')
            if build_id is not None and (not isinstance(build_id, str) or not re.fullmatch(
                    r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}',
                    build_id, re.I)):
                raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                 'inspect-retained-artifact', stage='prepare')
            if build.get('identity_verified'):
                if (not build_id or build.get('project_id') != retained_plan['normalized']['gcp']['project_id'] or
                        build.get('location') != CLOUD_BUILD_LOCATION or
                        build.get('status') in precreate_statuses | {'IDENTITY_INVALID', 'IDENTITY_MISMATCH'}):
                    raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                     'inspect-retained-artifact', stage='prepare')
            elif build.get('status') not in precreate_statuses | {'IDENTITY_MISMATCH', 'STATUS_UNKNOWN'}:
                raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                 'inspect-retained-artifact', stage='prepare')
            elif build_id is None and build.get('status') not in precreate_statuses:
                raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                 'inspect-retained-artifact', stage='prepare')

        if compatible_release_identity(retained_plan) == compatible_release_identity(self.plan):
            if retained_state.get('status') not in ('prepared', 'ready'):
                raise Breakpoint('stage1-checkpoint-runtime-started', 'failed', False,
                                 'inspect-latest-checkpoint', stage='prepare')
            if retained_state['components']:
                raise Breakpoint('stage1-checkpoint-runtime-started', 'failed', False,
                                 'inspect-latest-checkpoint', stage='prepare')
            # Equal release identity keeps the immutable legacy attempt ID, sequence,
            # and build handles while allowing a newer executor to resume it.
            self.plan = copy.deepcopy(retained_plan)
            write(self.directory / 'plan.json', self.plan)
            self.provider = Providers(self.plan, self.directory)
            self.state = copy.deepcopy(retained_state)
            self.state.setdefault('builds', {})
            self.state.setdefault('checkpoint_schema', 1)
            if self.executor_sha:
                self.state['executor_sha'] = self.executor_sha
            write(self.state_path, self.state)
            return

        # A changed plan can consume completed compatible receipts, but cannot
        # inherit a Cloud Build handle whose applicability to the new plan is unknown.
        terminal = {'FAILURE', 'INTERNAL_ERROR', 'TIMEOUT', 'CANCELLED', 'EXPIRED',
                    'SUBMIT_REJECTED'}
        uses_old_source = set(self.plan['selected'])
        if self.plan['normalized']['environment'] == 'production' and self.plan.get('dev_reference'):
            uses_old_source = {'frontend'} & uses_old_source
        for component, build in builds.items():
            if component not in uses_old_source:
                continue
            if not isinstance(build, dict):
                raise Breakpoint('stage1-checkpoint-invalid', 'failed', False,
                                 'inspect-retained-artifact', stage='prepare')
            if build.get('status') in terminal:
                continue
            receipt_path = old / 'receipts' / (component + '.json')
            applicable = False
            if receipt_path.exists() and component in self.plan['selected']:
                try:
                    candidate = read(receipt_path)
                    applicable = (isinstance(candidate, dict) and
                                  candidate.get('component') == component and
                                  candidate.get('identity') == self.plan['identities'][component] and
                                  (component != 'frontend' or candidate.get('target_config') ==
                                   digest(self.plan['normalized']['frontend'])))
                    if applicable:
                        self.provider.usable(component, candidate['artifact'])
                except (Breakpoint, KeyError, OSError, TypeError, ValueError):
                    applicable = False
            if not applicable:
                self.component = component
                safe_build = self.safe_build_record(build)
                raise Breakpoint('cross-plan-build-checkpoint-unresolved', 'unknown', False,
                                 'resume-original-plan-checkpoint', stage='build-submit',
                                 build=safe_build)

    def prepare(self, reuse=None, dev=None):
        require(not self.state['components'], 'prepare-after-runtime-forbidden')
        source_mode = release_source_mode(self.plan)
        require(source_mode in ('independent', 'legacy_monorepo'), 'release-source-mode-invalid')
        old = Path(reuse).resolve() if reuse else None
        self.resume_or_validate_reuse(old)
        provenance = Engine(dev) if dev else None
        if provenance:
            provenance.validate_dev_receipt_pair()
        for c in self.plan['selected']:
            self.component = c
            dest = self.directory / 'receipts' / (c+'.json')
            if dest.exists():
                try:
                    if c != 'worker':
                        existing = self.receipt(c)
                        if c == 'auth':
                            existing = self.attach_auth_input_snapshot(existing, require_match=True)
                            write(dest, existing)
                        continue
                    existing = read(dest)
                    require(existing.get('component') == c and
                            existing.get('identity') == self.plan['identities'][c],
                            'artifact-source-incompatible')
                    self.provider.valid_image(c, existing['artifact']['image'])
                    # A retained Worker image stays reusable while target config
                    # is freshly prepared for this release/environment.
                    existing = copy.deepcopy(existing)
                    existing['artifact']['pipeline_config'] = self.provider.prepare_pipeline_config()
                    self.provider.usable(c, existing['artifact'])
                    write(dest, existing)
                    self.save()
                    continue
                except Breakpoint as exc:
                    require(self.plan['normalized']['environment'] == 'development' and exc.reason in ('artifact-source-incompatible', 'artifact-config-incompatible', 'artifact-unusable'), exc.reason)
                    previous = read(dest)
                    write(self.directory / 'receipts/retained' / (digest(previous)+'.json'), previous)
                    dest.unlink()
            source = provenance.directory if provenance and c != 'frontend' else old
            if self.plan['normalized']['environment'] == 'production' and c != 'frontend':
                require(provenance is not None and self.plan['dev_reference'], 'explicit-dev-provenance-required')
                require(provenance.plan['normalized']['environment'] == 'development' and provenance.state['status'] == 'success', 'dev-release-not-successful')
                require(c in provenance.plan['selected'] and provenance.state['components'][c]['status'] == 'verified', 'dev-component-not-successful')
            receipt = None
            if source and (source / 'receipts' / (c+'.json')).exists():
                candidate = read(source / 'receipts' / (c+'.json'))
                applicable = candidate['identity'] == self.plan['identities'][c]
                if c == 'frontend':
                    applicable = applicable and candidate['target_config'] == digest(self.plan['normalized']['frontend'])
                if applicable:
                    if c == 'auth' and source_mode == 'independent':
                        auth_source = self.plan['auth_source']
                        expected_auth_source = {
                            'repository_id': auth_source['repository_id'],
                            'revision': auth_source['revision'],
                            'tree_sha256': auth_source['tree_sha256'],
                            'artifact_id': auth_source['artifact_id'],
                            'build_run_id': auth_source['run_id'],
                            'build_run_attempt': auth_source['run_attempt'],
                        }
                        try:
                            validate_auth_component_receipt(
                                candidate, 'independent', expected_auth_source=expected_auth_source)
                        except (ReceiptError, KeyError, TypeError) as exc:
                            raise Breakpoint('artifact-auth-receipt-incompatible', 'failed', False,
                                             'inspect-retained-artifact', cause=str(exc)) from None
                    receipt = candidate
                    if c == 'frontend':
                        shutil.copy2(source / 'frontend.tgz', self.directory / 'frontend.tgz')
            if receipt is None:
                require(not (self.plan['normalized']['environment'] == 'production' and c != 'frontend'), 'dev-source-config-incompatible')
                if (c == 'auth' and
                        source_mode == 'independent'):
                    require(isinstance(self.plan.get('auth_source'), dict),
                            'independent-auth-source-required')
                if c == 'auth' and source_mode == 'independent':
                    artifact, build = self.provider.prepare_independent_auth()
                    self.state['builds'][c] = build
                    self.save()
                else:
                    artifact = (self.prepare_container(c) if c in ('auth', 'bff', 'documentimport')
                                else self.provider.prepare(c))
                receipt = {'schema': 2, 'component': c, 'identity': self.plan['identities'][c],
                           'build_sha': self.plan['source'], 'artifact': artifact,
                           'target_config': digest(self.plan['normalized']['frontend']) if c == 'frontend' else None}
                if c in ('auth', 'bff', 'documentimport'):
                    receipt['build'] = self.safe_build_record(self.state['builds'][c])
                if (c == 'auth' and source_mode == 'independent' and
                        isinstance(self.plan.get('auth_source'), dict)):
                    source = self.plan['auth_source']
                    receipt['schema'] = 3
                    receipt['auth_source'] = {
                        'repository_id': source['repository_id'],
                        'revision': source['revision'],
                        'tree_sha256': source['tree_sha256'],
                        'artifact_id': source['artifact_id'],
                        'build_run_id': source['run_id'],
                        'build_run_attempt': source['run_attempt'],
                    }
            elif c == 'worker':
                receipt = copy.deepcopy(receipt)
                receipt['artifact']['pipeline_config'] = self.provider.prepare_pipeline_config()
            elif c == 'auth':
                receipt = copy.deepcopy(receipt)
                if source_mode == 'independent':
                    receipt['build_sha'] = self.plan['source']
            if c == 'auth':
                receipt = self.attach_auth_input_snapshot(receipt)
            self.provider.usable(c, receipt['artifact'])
            write(self.directory / 'receipts/retained' / (digest(receipt)+'.json'), receipt)
            write(dest, receipt)
            self.save()
        if (self.plan['normalized']['environment'] == 'development' and
                'auth' not in self.plan['selected'] and
                source_mode == 'independent' and
                isinstance(self.plan.get('auth_source'), dict)):
            source_directory = provenance.directory if provenance else old
            auth_receipt_path = source_directory / 'receipts/auth.json' if source_directory else None
            require(auth_receipt_path is not None and auth_receipt_path.is_file(),
                    'auth-source-artifact-unavailable')
            auth_receipt = read(auth_receipt_path)
            expected_identity = source_identity('auth', self.plan['source'], self.plan['auth_source'])
            require(auth_receipt.get('component') == 'auth' and auth_receipt.get('schema') == 3 and
                    auth_receipt.get('identity') == expected_identity and
                    auth_receipt.get('auth_source') == {
                        'repository_id': self.plan['auth_source']['repository_id'],
                        'revision': self.plan['auth_source']['revision'],
                        'tree_sha256': self.plan['auth_source']['tree_sha256'],
                        'artifact_id': self.plan['auth_source']['artifact_id'],
                        'build_run_id': self.plan['auth_source']['run_id'],
                        'build_run_attempt': self.plan['auth_source']['run_attempt'],
                    }, 'auth-source-artifact-incompatible')
            auth_receipt = copy.deepcopy(auth_receipt)
            auth_receipt['build_sha'] = self.plan['source']
            auth_receipt['auth_config_inputs'] = self.auth_input_snapshot()
            auth_receipt['auth_materialization_schema'] = 1
            self.provider.usable('auth', auth_receipt['artifact'])
            write(self.directory / 'receipts/auth.json', auth_receipt)
        if (self.plan['normalized']['environment'] == 'development' and
                source_mode == 'independent' and
                isinstance(self.plan.get('auth_source'), dict)):
            auth_receipt_path = self.directory / 'receipts/auth.json'
            if auth_receipt_path.exists():
                auth_receipt = read(auth_receipt_path)
                image = auth_receipt.get('artifact', {}).get('image')
                match = re.search(r'@sha256:([0-9a-f]{64})$', image) if isinstance(image, str) else None
                repository_id = os.environ.get('GITHUB_REPOSITORY_ID', '')
                run_id = os.environ.get('GITHUB_RUN_ID', '')
                run_attempt = os.environ.get('GITHUB_RUN_ATTEMPT', '')
                source = self.plan['auth_source']
                require(re.fullmatch(r'[1-9][0-9]*', repository_id) is not None and
                        re.fullmatch(r'[1-9][0-9]*', run_id) is not None and
                        re.fullmatch(r'[1-9][0-9]*', run_attempt) is not None and match is not None,
                        'independent-dev-source-identity-invalid')
                metadata = {
                    'lwc': {'repository_id': 'github:' + repository_id, 'sha': self.plan['source'],
                            'run_id': int(run_id), 'run_attempt': int(run_attempt)},
                    'auth': {'repository_id': source['repository_id'],
                             'revision': source['revision'], 'tree_sha256': source['tree_sha256'],
                             'run_id': source['run_id'], 'run_attempt': source['run_attempt'],
                             'artifact_id': source['artifact_id'],
                             'artifact_digest': 'sha256:' + match.group(1)},
                }
                write(self.directory / 'dev-source-metadata.json', metadata)
        has_dev_backend = any(c in self.plan['selected'] for c in ('auth', 'bff', 'worker', 'exportjob'))
        if (self.plan['normalized']['environment'] == 'development' and has_dev_backend and
                source_mode == 'independent'):
            require(os.environ.get('GAT4_DEV_RECEIPT_MODE') == 'independent' and
                    os.environ.get('GITHUB_RUN_ID') and os.environ.get('GITHUB_RUN_ATTEMPT'),
                    'independent-dev-receipt-producer-identity-required')
        if (self.plan['normalized']['environment'] == 'development' and
                source_mode in ('independent', 'legacy_monorepo') and
                has_dev_backend and
                os.environ.get('GAT4_DEV_RECEIPT_MODE') == source_mode and
                os.environ.get('GITHUB_RUN_ID') and os.environ.get('GITHUB_RUN_ATTEMPT')):
            image_dir = self.directory / 'images'
            image_dir.mkdir(exist_ok=True)
            for component in ('auth', 'bff', 'worker', 'exportjob'):
                if component not in self.plan['selected'] and component != 'auth':
                    continue
                component_receipt_path = self.directory / 'receipts' / (component + '.json')
                if not component_receipt_path.is_file():
                    continue
                component_receipt = read(component_receipt_path)
                image = component_receipt.get('artifact', {}).get('image')
                require(isinstance(image, str), 'artifact-image-missing')
                (image_dir / f'{component}-image-{self.plan["source"]}.txt').write_text(
                    image + '\n', encoding='utf-8')
            env = dict(os.environ, ROOT=str(ROOT), PLAN_PATH=str(self.directory / 'plan.json'),
                       ARTIFACT_DIR=str(self.directory), SOURCE_SHA=self.plan['source'],
                       ENVIRONMENT='development')
            run(['bash', '-c', 'source "$ROOT/deploy/components/common.sh"; record_dev_receipt'],
                cwd=ROOT, env=env, timeout=120)
            dev_receipt = read(image_dir / 'dev-receipt.json')
            if source_mode == 'independent':
                metadata = read(self.directory / 'dev-source-metadata.json')
                validate_dev_receipt(dev_receipt, 'independent', metadata['lwc'], metadata['auth'],
                                     [c for c in self.plan['selected']
                                      if c in ('auth', 'bff', 'worker', 'exportjob')])
            else:
                validate_dev_receipt(dev_receipt, 'legacy_monorepo', {
                    'sha': self.plan['source'], 'run_id': int(os.environ['GITHUB_RUN_ID']),
                    'run_attempt': int(os.environ['GITHUB_RUN_ATTEMPT'])},
                    expected_components=[c for c in self.plan['selected']
                                         if c in ('auth', 'bff', 'worker', 'exportjob')])
        self.barrier()
        self.state['status'] = 'ready'
        self.save()

    def runtime_guard(self, operation=None, force=False):
        require(os.environ.get('GITHUB_ACTIONS') == 'true', 'runtime-owned-by-actions')
        require(not force or (operation == 'deploy' and
                              self.plan['normalized']['environment'] == 'development' and
                              self.state['status'] == 'ready'), 'force-not-allowed')
        if operation == 'readback':
            return
        # Workflow concurrency serializes all entrypoints; latest durable state fences
        # mutations that would replay an older release after a newer pending mutation.
        latest_path = self.directory / '.latest.json'
        latest_path.unlink(missing_ok=True)
        run(['node', ROOT / 'deploy/engine/artifacts.cjs', 'latest',
             self.plan['normalized']['environment'], latest_path], timeout=120,
            stage='latest-checkpoint')
        if latest_path.exists():
            record = read(latest_path)
            require(isinstance(record, dict) and
                    all(key in record for key in ('plan', 'status', 'components', 'sequence')) and
                    isinstance(record.get('plan'), str) and
                    re.fullmatch(r'[0-9a-f]{64}', record['plan']) is not None and
                    isinstance(record.get('status'), str) and
                    record['status'] in CHECKPOINT_STATUSES and
                    isinstance(record.get('components'), dict) and
                    all(component in ORDER for component in record['components']) and
                    isinstance(record.get('sequence'), int) and
                    not isinstance(record.get('sequence'), bool) and record['sequence'] >= 0 and
                    type(record.get('checkpoint_schema', 1)) is int and
                    record.get('checkpoint_schema', 1) == 1 and
                    ('builds' not in record or isinstance(record['builds'], dict)),
                    'latest-checkpoint-invalid')
            if self.state['status'] != 'ready':
                require(record['plan'] == self.plan['id'] and record['sequence'] == self.state['sequence'], 'stale-checkpoint')
            else:
                require(record['plan'] != self.plan['id'] or record['status'] in ('ready', 'prepared'), 'stale-ready-artifact-use-latest-checkpoint')
                resolved = record['status'] in RESOLVED_TARGET_STATUSES
                if not resolved and force and operation == 'deploy' and record['plan'] != self.plan['id']:
                    self.force_bypass = 'target-has-unresolved-attempt'
                else:
                    require(resolved, 'target-has-unresolved-attempt')
        else:
            require(self.state['status'] == 'ready', 'target-checkpoint-unavailable')

    def snapshot(self):
        self.barrier()
        for c in self.plan['selected']:
            self.component = c
            if c not in self.state['components']:
                self.state['components'][c] = {'status': 'unstarted', 'prior': self.provider.snapshot(c), 'candidate': {}}
        self.state['status'] = 'snapshotted'
        self.save()

    def restore(self, components, automatic=False):
        require(all(c in self.state['components'] for c in components), 'retained-snapshot-required')
        self.state['status'] = 'rolling_back'
        self.save()
        errors = []
        error_causes = []
        for c in reversed(self.plan['selected']):
            if c not in components:
                continue
            self.component = c
            entry = self.state['components'][c]
            if entry['status'] in ('unstarted', 'rolled_back'):
                continue
            entry['status'] = 'rollback_pending'
            self.save()
            try:
                if not self.provider.observe(c, entry['prior'], entry.get('candidate', {}), True):
                    try:
                        self.provider.rollback(c, entry['prior'], entry.get('candidate', {}))
                    except Breakpoint:
                        # A timeout can be accepted; verified readback is authoritative.
                        pass
                self.provider.poll(c, entry['prior'], {}, True)
                entry['status'] = 'rolled_back'
            except (Breakpoint, KeyError, ValueError) as exc:
                entry['status'] = 'rollback_unknown' if not isinstance(exc, Breakpoint) or exc.status == 'unknown' else 'rollback_failed'
                errors.append(c)
                stage = exc.stage if isinstance(exc, Breakpoint) and exc.stage else 'rollback'
                cause = _phase_cause('compensation', exc, stage)
                cause['component'] = c
                error_causes.append(cause)
            self.save()
        self.state['status'] = 'recovery_failed' if errors else ('failed_rolled_back' if automatic else 'rolled_back')
        self.state['job_data_boundary'] = 'Running Job executions and persistent writes are not reversed.'
        self.save()
        if errors:
            raise Breakpoint('rollback-not-verified', 'unknown', True, 'inspect-retained-checkpoint',
                             causes=error_causes)

    def reconcile(self, c, artifact, candidate, deploy_error=None):
        try:
            self.provider.reconcile_candidate(c, artifact, candidate, self.save)
        except (Breakpoint, KeyError, ValueError) as exc:
            stage = 'frontend-deployment-reconcile' if c == 'frontend' else 'unknown'
            causes = ([_phase_cause('deploy', deploy_error,
                                    'frontend-vercel-deploy' if c == 'frontend' else 'unknown')]
                      if deploy_error else [])
            causes.append(_phase_cause('reconcile', exc, stage))
            raise Breakpoint('provider-result-unreadable', 'unknown', True, 'reconcile-before-replay',
                             causes=causes) from None

    def deploy(self, components=None, reactivate=False):
        artifacts = self.barrier()
        if self.state['status'] in ('runtime_success', 'tag_failed', 'success') and not reactivate:
            return self.tag()
        if self.state['status'] == 'ready':
            self.snapshot()
        chosen = components or self.plan['selected']
        changed = []  # Compensation belongs to this invocation, not release history.
        for c in self.plan['selected']:
            if c not in chosen:
                continue
            self.component = c
            entry = self.state['components'][c]
            if entry['status'] == 'verified' and not reactivate:
                continue
            require(entry['status'] not in ('rollback_pending', 'rollback_unknown', 'rollback_failed'), 'recovery-must-be-reconciled')
            changed.append(c)
            if c == 'documentimport':
                entry['candidate'].setdefault('create_if_absent', entry['prior'].get('absent') is True)
                self.save()
            was_pending = entry['status'] in ('pending', 'unknown')
            entry['status'] = 'pending'
            self.state['status'] = 'deploying'
            self.save()
            try:
                if was_pending:
                    self.reconcile(c, artifacts[c], entry['candidate'])
                    # A pending operation is never blindly submitted again. Resume
                    # only a discovered service/deployment identity, or verify Job.
                    if self.provider.observe(c, artifacts[c], entry['candidate']):
                        entry['status'] = 'verified'
                        self.save()
                        continue
                    if c in ('worker', 'exportjob', 'documentimport'):
                        raise Breakpoint('pending-job-mismatch', 'failed', True, 'rollback')
                    require(bool(entry['candidate']), 'mutation-result-unknown')
                deploy_error = None
                try:
                    self.provider.deploy(c, artifacts[c], entry['candidate'], self.save)
                except Breakpoint as failure:
                    deploy_error = failure
                    self.reconcile(c, artifacts[c], entry['candidate'], deploy_error=deploy_error)
                    # No second update. Readback determines known failure vs unknown.
                try:
                    self.provider.poll(c, artifacts[c], entry['candidate'])
                except Breakpoint as poll_error:
                    if deploy_error is not None:
                        poll_error.causes = [
                            _phase_cause('deploy', deploy_error, deploy_error.stage or 'unknown'),
                            _phase_cause('poll', poll_error, 'provider-poll'),
                        ]
                    raise
                entry['status'] = 'verified'
                self.save()
            except (Breakpoint, KeyError, ValueError) as exc:
                if not isinstance(exc, Breakpoint):
                    exc = Breakpoint('provider-response-unrepresentable', 'unknown', True, 'reconcile-before-replay')
                entry['status'] = 'unknown' if exc.status == 'unknown' else 'failed'
                self.state['status'] = entry['status']
                self.save()
                if exc.status != 'unknown':
                    try:
                        self.restore(changed, automatic=True)
                    except Breakpoint as rollback_error:
                        rollback_error.causes = [
                            *(exc.causes or [_phase_cause('primary', exc, exc.stage or 'unknown')]),
                            *(rollback_error.causes or [_phase_cause(
                                'compensation', rollback_error, rollback_error.stage or 'rollback')]),
                        ]
                        raise rollback_error from None
                    finally:
                        self.component = c
                else:
                    self.component = c
                raise exc
        if all(e['status'] == 'verified' for e in self.state['components'].values()) and len(self.state['components']) == len(self.plan['selected']):
            self.state['status'] = 'runtime_success'
            self.save()
            self.tag()
        else:
            self.state['status'] = 'partially_reactivated'
            self.save()

    def tag(self):
        require(self.state['status'] in ('runtime_success', 'tag_failed', 'success'), 'tag-before-sanity-forbidden')
        repo = os.environ['GITHUB_REPOSITORY']
        tag = self.plan['tag']
        # Single exact tag lookup. Never force or invent version names.
        try:
            refs = json.loads(run(['gh', 'api', 'repos/'+repo+'/git/matching-refs/tags/'+urllib.parse.quote(tag, safe='')]))
            exact = [r for r in refs if r['ref'] == 'refs/tags/'+tag]
            require(len(exact) <= 1, 'tag-ambiguous')
            if exact:
                obj = exact[0]['object']
                for _ in range(4):
                    if obj['type'] != 'tag':
                        break
                    obj = json.loads(run(['gh', 'api', 'repos/'+repo+'/git/tags/'+obj['sha']]))['object']
                require(obj['type'] == 'commit' and obj['sha'] == self.plan['source'], 'tag-conflict')
            else:
                run(['gh', 'api', '--method', 'POST', 'repos/'+repo+'/git/refs', '-f', 'ref=refs/tags/'+tag,
                     '-f', 'sha='+self.plan['source']], mutation=True)
            self.state['status'] = 'success'
            self.save()
        except Breakpoint:
            self.state['status'] = 'tag_failed'
            self.save()
            raise Breakpoint('tag-incomplete-or-conflicting', 'incomplete_metadata', True, 'retry-tag-only') from None

    def result(self, exc=None):
        entry = self.state['components'].get(self.component, {})
        result = {'release': self.plan['tag'], 'attempt': self.plan['id'], 'stage': self.state['status'],
                  'component': self.component, 'status': exc.status if exc else self.state['status'],
                  'reason': exc.reason if exc else 'completed', 'mutation_may_have_happened': bool(self.state['components']) or bool(exc and exc.mutation),
                  'prior': entry.get('prior'), 'candidate': entry.get('candidate'),
                  'expected': {'source': self.plan['source'], 'identity': digest(self.plan['identities'].get(self.component))},
                  'observed': {'component_status': entry.get('status', 'unstarted')},
                  'last_verified_checkpoint': self.state['sequence'],
                  'allowed_next_action': exc.action if exc else 'inspect-or-explicit-recovery',
                  'limitations': 'Job executions and persistent writes are not reversed by image rollback.'}
        build_records = {c: self.safe_build_record(self.state['builds'][c])
                         for c in self.plan['selected'] if c in self.state.get('builds', {})}
        if exc and exc.build and self.component and self.component not in build_records:
            build_records[self.component] = self.safe_build_record(exc.build)
        if build_records:
            result['builds'] = build_records
        if exc and exc.stage:
            result['failure_diagnostic'] = {
                'stage': exc.stage,
                'exit_code': exc.exit_code,
                'timeout_class': exc.timeout_class,
            }
        if exc and exc.cause:
            result['cause'] = exc.cause
        if exc and exc.causes:
            result['causes'] = exc.causes
        if self.force_bypass:
            result['force_bypass'] = self.force_bypass
        if exc and isinstance(exc.frontend_prepare_diagnostic, dict):
            result['frontend_prepare_diagnostic'] = exc.frontend_prepare_diagnostic
        write(self.directory / 'result.json', result)
        print(json.dumps(result, sort_keys=True))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('operation', choices=('prepare', 'auth-preflight', 'deploy', 'rollback', 'reactivate', 'tag', 'readback'))
    parser.add_argument('--directory', required=True)
    parser.add_argument('--environment', choices=('development', 'production'))
    parser.add_argument('--source')
    parser.add_argument('--tag')
    parser.add_argument('--components')
    parser.add_argument('--reuse')
    parser.add_argument('--dev')
    parser.add_argument('--dev-reference')
    parser.add_argument('--executor-sha')
    parser.add_argument('--release-source-mode', choices=('independent', 'legacy_monorepo'))
    parser.add_argument('--auth-repository-id')
    parser.add_argument('--auth-revision')
    parser.add_argument('--auth-tree-sha256')
    parser.add_argument('--auth-artifact-id')
    parser.add_argument('--auth-run-id')
    parser.add_argument('--auth-run-attempt')
    parser.add_argument('--force', action='store_true')
    args = parser.parse_args()
    directory = Path(args.directory).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    engine = None
    try:
        require(args.executor_sha is None or
                re.fullmatch(r'[0-9a-f]{40}', args.executor_sha) is not None,
                'invalid-executor-sha')
        with (directory / '.lock').open('w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if args.operation in ('prepare', 'auth-preflight') and not (directory / 'plan.json').exists():
                require(all((args.environment, args.source, args.tag, args.components)), 'missing-admission-input')
                write(directory / 'plan.json', admit(args))
            engine = Engine(directory, args.executor_sha)
            if args.operation in ('prepare', 'auth-preflight'):
                require(not args.components or selection(args.components) == engine.plan['selected'], 'selection-change-requires-new-plan')
                require(not args.source or args.source == engine.plan['source'], 'source-change-requires-new-plan')
                require(not args.tag or args.tag == engine.plan['tag'], 'tag-change-requires-new-plan')
                require(not args.environment or args.environment == engine.plan['normalized']['environment'], 'target-change-requires-new-plan')
                if args.operation == 'auth-preflight':
                    needs_package = engine.auth_package_preflight(args.reuse, args.dev)
                    output = os.environ.get('GITHUB_OUTPUT')
                    if output:
                        with open(output, 'a', encoding='utf-8') as stream:
                            stream.write(f"needs_auth_ci_package={'true' if needs_package else 'false'}\n")
                    print(json.dumps({'needs_auth_ci_package': needs_package}, sort_keys=True))
                else:
                    engine.prepare(args.reuse, args.dev)
            else:
                require(not args.force or args.operation == 'deploy', 'force-not-allowed')
                engine.runtime_guard(args.operation, args.force)
                chosen = selection(args.components) if args.components else engine.plan['selected']
                require(set(chosen) <= set(engine.plan['selected']), 'component-outside-plan')
                if args.operation == 'deploy':
                    engine.deploy(chosen)
                elif args.operation == 'tag':
                    engine.tag()
                elif args.operation == 'readback':
                    for c in chosen:
                        engine.provider.poll(c, engine.receipt(c)['artifact'], engine.state['components'][c]['candidate'])
                elif args.operation == 'rollback':
                    engine.restore(chosen)
                else:
                    engine.deploy(chosen, reactivate=True)
            if args.operation != 'auth-preflight':
                engine.result()
    except (Breakpoint, OSError, KeyError, ValueError, TypeError, AttributeError) as exc:
        if not isinstance(exc, Breakpoint):
            exc = Breakpoint('invalid-or-unreadable-input', stage='unknown',
                             cause=structured_cause(exc),
                             frontend_prepare_diagnostic=getattr(
                                 exc, 'frontend_prepare_diagnostic', None))
        if engine:
            engine.result(exc)
        else:
            result = {'release': args.tag, 'attempt': None, 'stage': 'admission', 'component': None,
                      'status': exc.status, 'reason': exc.reason, 'mutation_may_have_happened': False,
                      'prior': None, 'candidate': None, 'expected': 'valid pinned input',
                      'observed': 'input rejected', 'last_verified_checkpoint': None,
                      'allowed_next_action': exc.action}
            if exc.cause:
                result['cause'] = exc.cause
            print(json.dumps(result))
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
