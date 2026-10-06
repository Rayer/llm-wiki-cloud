#!/usr/bin/env python3
"""Manual config-only delivery; does not build or replace the Worker image."""
import argparse
import json
import os
from pathlib import Path
import re
import tempfile

from support import ROOT, Breakpoint, require, run
import pipeline_config as pipeline_config_contract


def job_template(raw):
    return raw['spec']['template']['spec']['template']['spec']


def run_config_only(environment, *, root=ROOT, run_command=run, process_env=None):
    target = {'development': 'dev', 'production': 'prod'}.get(environment)
    require(target is not None, 'pipeline-config-environment-invalid')
    source_env = dict(os.environ if process_env is None else process_env)
    timeout = source_env.get('LWC_PIPELINE_RUN_TIMEOUT_SECONDS', '').strip()
    require(re.fullmatch(r'[1-9][0-9]*', timeout) is not None,
            'verified-pipeline-run-timeout-required')
    with tempfile.TemporaryDirectory(prefix='lwc-pipeline-config-only-') as temp:
        run_command(['make', 'config-'+target, 'CAC_OUTPUT_DIR='+temp], cwd=root,
                    env=source_env, timeout=600, stage='unknown')
        try:
            generated = pipeline_config_contract.rendered_pipeline_config(
                Path(temp) / target, target)
        except (OSError, TypeError, ValueError):
            raise Breakpoint('pipeline-config-render-invalid') from None
        require(generated['timeout_seconds'] == int(timeout), 'pipeline-config-timeout-mismatch')
        normalized_text = run_command(
            ['go', 'run', './cmd/deploy_config', '--environment', environment,
             '--config', f'deploy/environments/{environment}.yaml', '--components', 'worker'],
            cwd=root / 'apps/bff', env=source_env, timeout=300, stage='unknown')
        try:
            normalized = json.loads(normalized_text)
            worker = normalized['worker']
            job = worker['job_name']
            project = normalized['gcp']['project_id']
            region = worker['location']
            require(worker['bucket'] == generated['bucket'], 'pipeline-config-bucket-mismatch')
            uri = pipeline_config_contract.pipeline_config_uri(generated['bucket'])
            key, value = pipeline_config_contract.secret_cli_binding(generated['secret'])
        except (KeyError, TypeError, ValueError):
            raise Breakpoint('pipeline-config-target-invalid') from None

        before_text = run_command(
            ['gcloud', 'run', 'jobs', 'describe', job, '--project', project,
             '--region', region, '--format=json', '--quiet'], timeout=60, stage='unknown')
        try:
            before = json.loads(before_text)
            before_template = job_template(before)
            before_containers = before_template['containers']
            require(len(before_containers) == 1, 'unrepresentable-worker')
            before_image = before_containers[0]['image']
            require(re.fullmatch(r'.+@sha256:[0-9a-f]{64}', before_image) is not None,
                    'mutable-prior-image')
        except (KeyError, TypeError, ValueError):
            raise Breakpoint('pipeline-config-target-invalid') from None

        source = Path(temp) / 'synto.toml'
        source.write_text(generated['toml'], encoding='utf-8')
        run_command(['gcloud', 'storage', 'cp', '--quiet', source, uri],
                    timeout=120, mutation=True, stage='unknown')
        observed_toml = run_command(['gcloud', 'storage', 'cat', uri], timeout=60,
                                    stage='unknown', preserve_stdout_bytes=True)
        if isinstance(observed_toml, bytes):
            observed_toml = observed_toml.decode('utf-8')
        require(observed_toml == generated['toml'], 'pipeline-config-object-readback-mismatch')
        run_command(['gcloud', 'run', 'jobs', 'update', job,
                     '--update-secrets', key+'='+value,
                     '--task-timeout', str(generated['timeout_seconds'])+'s',
                     '--project', project, '--region', region, '--format=json', '--quiet'],
                    timeout=120, mutation=True, stage='unknown')
        after_text = run_command(
            ['gcloud', 'run', 'jobs', 'describe', job, '--project', project,
             '--region', region, '--format=json', '--quiet'], timeout=60, stage='unknown')
        try:
            after_template = job_template(json.loads(after_text))
            require(len(after_template['containers']) == 1 and
                    after_template['containers'][0]['image'] == before_image,
                    'pipeline-config-only-replaced-worker-image')
            actual_timeout = after_template.get('timeoutSeconds')
            if isinstance(actual_timeout, str):
                actual_timeout = int(actual_timeout)
            require(actual_timeout == generated['timeout_seconds'],
                    'pipeline-config-timeout-readback-mismatch')
            actual_secret = pipeline_config_contract.job_secret_binding(
                after_template['containers'][0], project)
            require(actual_secret == generated['secret']['resource'],
                    'pipeline-config-secret-readback-mismatch')
        except (KeyError, TypeError, ValueError):
            raise Breakpoint('pipeline-config-job-readback-invalid') from None
    print(f"Pipeline config-only verified environment={target} sha256={generated['sha256']} image_unchanged=true")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--environment', required=True, choices=('development', 'production'))
    args = parser.parse_args()
    try:
        run_config_only(args.environment)
    except Breakpoint as exc:
        print(f'Pipeline config-only failed: {exc.reason}', file=__import__('sys').stderr)
        raise SystemExit(1) from None


if __name__ == '__main__':
    main()
