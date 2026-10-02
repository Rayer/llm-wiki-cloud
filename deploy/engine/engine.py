#!/usr/bin/env python3
"""LWC-358 two-stage release engine. Runtime entry is owned by Actions."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sys
import urllib.parse

from support import ROOT, Breakpoint, digest, read, require, run, write
from providers import Providers

ORDER = ('exportjob', 'auth', 'bff', 'worker', 'frontend')
PROFILES = read(ROOT / 'deploy/engine/profiles.json')


def selection(raw):
    values = raw.split(',')
    require(values and len(set(values)) == len(values) and all(c in ORDER for c in values), 'invalid-components')
    return [c for c in ORDER if c in values]


def source_identity(c, sha):
    """Hash build inputs, not release SHA or engine scripts. Go resolves package dependencies."""
    profile = PROFILES[c]
    tracked = run(['git', 'ls-tree', '-r', '--name-only', sha]).splitlines()
    roots = list(profile['inputs'])
    package_dirs = []
    if c != 'frontend':
        roots += ['apps/bff/go.mod', 'apps/bff/go.sum', 'apps/bff/.dockerignore', 'apps/bff/.gcloudignore', 'apps/bff/.gitignore']
        dirs = run(['go', 'list', '-deps', '-f', '{{if and (not .Standard) .Module}}{{if .Module.Main}}{{.Dir}}{{range .EmbedFiles}}{{println}}{{$.Dir}}/{{.}}{{end}}{{end}}{{end}}',
                    profile['package']], cwd=ROOT / 'apps/bff', timeout=180).splitlines()
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


def admit(args):
    selected = selection(args.components)
    require(re.fullmatch(r'[0-9a-f]{40}', args.source), 'invalid-source')
    require(run(['git', 'rev-parse', 'HEAD']) == args.source, 'checkout-source-mismatch')
    require(args.tag and not args.tag.startswith('-'), 'invalid-release-tag')
    run(['git', 'check-ref-format', 'refs/tags/'+args.tag])
    branch = 'develop' if args.environment == 'development' else 'main'
    cfg = 'deploy/environments/'+args.environment+'.yaml'
    require(run(['git', 'hash-object', cfg]) == run(['git', 'rev-parse', args.source+':'+cfg]), 'dirty-target-config')
    normalized = json.loads(run(['go', 'run', './cmd/deploy_config', '--environment', args.environment,
                       '--config', str(ROOT / cfg), '--components', ','.join(selected)], cwd=ROOT / 'apps/bff', timeout=180))
    identities = {c: source_identity(c, args.source) for c in selected}
    body = {'schema': 2, 'source': args.source, 'branch': branch, 'tag': args.tag,
            'engine': run(['git', 'rev-parse', 'HEAD']), 'engine_content': engine_fingerprint(),
            'normalized': normalized, 'identities': identities,
            'dev_reference': args.dev_reference, 'selected': selected}
    body['id'] = digest(body)
    return body


def engine_fingerprint():
    paths = sorted([* (ROOT / 'deploy/engine').glob('*.py'), ROOT / 'deploy/engine/profiles.json',
                    * (ROOT / 'deploy/components').glob('*.sh'), ROOT / 'deploy/engine/artifacts.cjs', ROOT / '.github/actions/deployment-engine/index.cjs', ROOT / '.github/actions/deployment-engine/action.yml',
                    * (ROOT / '.github/workflows').glob('*deploy*.yml'), ROOT / '.github/workflows/cd.yml', ROOT / '.github/workflows/promote-production.yml', * (ROOT / 'deploy/components').glob('*.py')])
    return digest([[str(p.relative_to(ROOT)), hashlib.sha256(p.read_bytes()).hexdigest()] for p in paths])


class Engine:
    def __init__(self, directory):
        self.directory = Path(directory).resolve()
        self.plan = read(self.directory / 'plan.json')
        body = {k:v for k,v in self.plan.items() if k != 'id'}
        require(self.plan['id'] == digest(body), 'plan-content-mismatch')
        require(self.plan['selected'] == selection(','.join(self.plan['selected'])), 'invalid-plan-selection')
        self.provider = Providers(self.plan, self.directory)
        self.state_path = self.directory / 'state.json'
        self.state = read(self.state_path) if self.state_path.exists() else {
            'plan': self.plan['id'], 'status': 'prepared', 'components': {}, 'sequence': 0}
        require(self.state['plan'] == self.plan['id'], 'checkpoint-plan-mismatch')
        self.component = None

    def save(self):
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
        self.provider.usable(c, r['artifact'])
        return r

    def barrier(self):
        return {c: self.receipt(c)['artifact'] for c in self.plan['selected']}

    def prepare(self, reuse=None, dev=None):
        require(not self.state['components'], 'prepare-after-runtime-forbidden')
        old = Path(reuse).resolve() if reuse else None
        provenance = Engine(dev) if dev else None
        for c in self.plan['selected']:
            self.component = c
            dest = self.directory / 'receipts' / (c+'.json')
            if dest.exists():
                try:
                    self.receipt(c)
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
                    receipt = candidate
                    if c == 'frontend':
                        shutil.copy2(source / 'frontend.tgz', self.directory / 'frontend.tgz')
            if receipt is None:
                require(not (self.plan['normalized']['environment'] == 'production' and c != 'frontend'), 'dev-source-config-incompatible')
                artifact = self.provider.prepare(c)
                receipt = {'schema': 2, 'component': c, 'identity': self.plan['identities'][c],
                           'build_sha': self.plan['source'], 'artifact': artifact,
                           'target_config': digest(self.plan['normalized']['frontend']) if c == 'frontend' else None}
            self.provider.usable(c, receipt['artifact'])
            write(self.directory / 'receipts/retained' / (digest(receipt)+'.json'), receipt)
            write(dest, receipt)
            self.save()
        self.barrier()
        self.state['status'] = 'ready'
        self.save()

    def runtime_guard(self):
        require(os.environ.get('GITHUB_ACTIONS') == 'true', 'runtime-owned-by-actions')
        require(self.plan['engine_content'] == engine_fingerprint(), 'pinned-engine-mismatch')
        # Workflow concurrency serializes all entrypoints; latest durable state fences
        # recovery of an older release after any newer pending mutation.
        latest_path = self.directory / '.latest.json'
        latest_path.unlink(missing_ok=True)
        run(['node', ROOT / 'deploy/engine/artifacts.cjs', 'latest',
             self.plan['normalized']['environment'], latest_path], timeout=120)
        if latest_path.exists():
            record = read(latest_path)
            if self.state['status'] != 'ready':
                require(record['plan'] == self.plan['id'] and record['sequence'] == self.state['sequence'], 'stale-checkpoint')
            else:
                require(record['plan'] != self.plan['id'] or record['status'] in ('ready', 'prepared'), 'stale-ready-artifact-use-latest-checkpoint')
                require(record['status'] in ('success', 'rolled_back', 'failed_rolled_back', 'ready', 'prepared'), 'target-has-unresolved-attempt')
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
                if not self.provider.observe(c, entry['prior'], {}, True):
                    try:
                        self.provider.rollback(c, entry['prior'])
                    except Breakpoint:
                        # A timeout can be accepted; verified readback is authoritative.
                        pass
                self.provider.poll(c, entry['prior'], {}, True)
                entry['status'] = 'rolled_back'
            except (Breakpoint, KeyError, ValueError) as exc:
                entry['status'] = 'rollback_unknown' if not isinstance(exc, Breakpoint) or exc.status == 'unknown' else 'rollback_failed'
                errors.append(c)
            self.save()
        self.state['status'] = 'recovery_failed' if errors else ('failed_rolled_back' if automatic else 'rolled_back')
        self.state['job_data_boundary'] = 'Running Job executions and persistent writes are not reversed.'
        self.save()
        if errors:
            raise Breakpoint('rollback-not-verified', 'unknown', True, 'inspect-retained-checkpoint')

    def reconcile(self, c, artifact, candidate):
        try:
            self.provider.reconcile_candidate(c, artifact, candidate, self.save)
        except (Breakpoint, KeyError, ValueError):
            raise Breakpoint('provider-result-unreadable', 'unknown', True, 'reconcile-before-replay') from None

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
                    if c in ('worker', 'exportjob'):
                        raise Breakpoint('pending-job-mismatch', 'failed', True, 'rollback')
                    require(bool(entry['candidate']), 'mutation-result-unknown')
                try:
                    self.provider.deploy(c, artifacts[c], entry['candidate'], self.save)
                except Breakpoint:
                    self.reconcile(c, artifacts[c], entry['candidate'])
                    # No second update. Readback determines known failure vs unknown.
                self.provider.poll(c, artifacts[c], entry['candidate'])
                entry['status'] = 'verified'
                self.save()
            except (Breakpoint, KeyError, ValueError) as exc:
                if not isinstance(exc, Breakpoint):
                    exc = Breakpoint('provider-response-unrepresentable', 'unknown', True, 'reconcile-before-replay')
                entry['status'] = 'unknown' if exc.status == 'unknown' else 'failed'
                self.state['status'] = entry['status']
                self.save()
                if exc.status != 'unknown':
                    self.restore(changed, automatic=True)
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
        write(self.directory / 'result.json', result)
        print(json.dumps(result, sort_keys=True))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('operation', choices=('prepare', 'deploy', 'rollback', 'reactivate', 'tag', 'readback'))
    parser.add_argument('--directory', required=True)
    parser.add_argument('--environment', choices=('development', 'production'))
    parser.add_argument('--source')
    parser.add_argument('--tag')
    parser.add_argument('--components')
    parser.add_argument('--reuse')
    parser.add_argument('--dev')
    parser.add_argument('--dev-reference')
    args = parser.parse_args()
    directory = Path(args.directory).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    engine = None
    try:
        with (directory / '.lock').open('w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if args.operation == 'prepare' and not (directory / 'plan.json').exists():
                require(all((args.environment, args.source, args.tag, args.components)), 'missing-admission-input')
                write(directory / 'plan.json', admit(args))
            engine = Engine(directory)
            if args.operation == 'prepare':
                require(not args.components or selection(args.components) == engine.plan['selected'], 'selection-change-requires-new-plan')
                require(not args.source or args.source == engine.plan['source'], 'source-change-requires-new-plan')
                require(not args.tag or args.tag == engine.plan['tag'], 'tag-change-requires-new-plan')
                require(not args.environment or args.environment == engine.plan['normalized']['environment'], 'target-change-requires-new-plan')
                engine.prepare(args.reuse, args.dev)
            else:
                engine.runtime_guard()
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
            engine.result()
    except (Breakpoint, OSError, KeyError, ValueError, TypeError) as exc:
        if not isinstance(exc, Breakpoint):
            exc = Breakpoint('invalid-or-unreadable-input')
        if engine:
            engine.result(exc)
        else:
            print(json.dumps({'release': args.tag, 'attempt': None, 'stage': 'admission', 'component': None,
                              'status': exc.status, 'reason': exc.reason, 'mutation_may_have_happened': False,
                              'prior': None, 'candidate': None, 'expected': 'valid pinned input',
                              'observed': 'input rejected', 'last_verified_checkpoint': None,
                              'allowed_next_action': exc.action}))
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
