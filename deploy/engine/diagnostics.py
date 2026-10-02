"""Fixed, read-only DEV Auth image diagnostic. Never emit provider output."""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from support import write

PROJECT = 'llm-wiki-cloud'
IMAGE = 'asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth'
SOURCE_TAG = IMAGE + ':f6e6a5e588088294c8829192b308b0813356f9da'
IMMUTABLE_DIGEST = 'sha256:8f11078a4a379803849a67ca52b9cdcb13b36a310844e72b60581d12a6f0d8a1'
IMMUTABLE_REF = IMAGE + '@' + IMMUTABLE_DIGEST
DIGEST_PATTERN = re.compile(r'sha256:[0-9a-f]{64}')
SOURCE_PATTERN = re.compile(r'[0-9a-f]{40}')
DIAGNOSTIC_OPERATION = 'diagnose-auth-image'
DIAGNOSTIC_TAG = 'diagnostic-36992147920'
NO_RECEIPT = 'diagnostic-no-receipt'


def context_allowed(env):
    source = env.get('SOURCE', '')
    return (env.get('GITHUB_ACTIONS') == 'true' and
            env.get('WORKFLOW_REF') == 'refs/heads/develop' and
            env.get('GITHUB_REF') == env.get('WORKFLOW_REF') and
            SOURCE_PATTERN.fullmatch(source) is not None and
            source == env.get('WORKFLOW_SHA') and
            env.get('GITHUB_SHA') == env.get('WORKFLOW_SHA') and
            env.get('OPERATION') == DIAGNOSTIC_OPERATION and
            env.get('TARGET') == 'development' and
            env.get('COMPONENTS') == 'auth' and
            env.get('RELEASE_TAG') == DIAGNOSTIC_TAG and
            env.get('ARTIFACT_ID') == NO_RECEIPT and
            env.get('REUSE_ID') == NO_RECEIPT and
            env.get('DEV_ID', '') == '')


def rejected_result():
    return {'schema': 1, 'diagnostic': 'dev-auth-image-read-only', 'status': 'rejected',
            'operations': [], 'tag_matches_immutable_digest': None,
            'conclusion': 'fixed-workflow-context-required'}


def inspect(reference, label, runner=subprocess.run):
    try:
        process = runner(
            ['gcloud', 'artifacts', 'docker', 'images', 'describe', reference,
             '--project', PROJECT, '--format=value(image_summary.digest)', '--quiet'],
            capture_output=True, text=True, timeout=30)
        code = process.returncode
        raw = process.stdout.strip() if code == 0 else ''
        timed_out = False
        unavailable = False
    except subprocess.TimeoutExpired:
        code, raw, timed_out, unavailable = None, '', True, False
    except OSError:
        code, raw, timed_out, unavailable = None, '', False, True
    return {'operation': label, 'exit_code': code,
            'timeout_class': 'subprocess-timeout' if timed_out else ('tool-unavailable' if unavailable else 'none'),
            'digest_format_valid': bool(code == 0 and DIGEST_PATTERN.fullmatch(raw))}, raw


def diagnose(runner=subprocess.run):
    tag, tag_value = inspect(SOURCE_TAG, 'source-tag', runner)
    immutable, immutable_value = inspect(IMMUTABLE_REF, 'immutable-digest', runner)
    comparable = tag['digest_format_valid'] and immutable['digest_format_valid']
    equal = tag_value == immutable_value if comparable else None
    if comparable and equal:
        conclusion = 'tag-matches-immutable-digest'
        status = 'verified'
    elif comparable:
        conclusion = 'tag-does-not-match-immutable-digest'
        status = 'mismatch'
    else:
        conclusion = 'read-or-format-incomplete'
        status = 'inconclusive'
    return {'schema': 1, 'diagnostic': 'dev-auth-image-read-only', 'status': status,
            'operations': [tag, immutable], 'tag_matches_immutable_digest': equal,
            'conclusion': conclusion}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    result = diagnose() if context_allowed(os.environ) else rejected_result()
    write(args.output, result)
    print(json.dumps(result, sort_keys=True))
    return 0 if result['status'] == 'verified' else 1


if __name__ == '__main__':
    raise SystemExit(main())
