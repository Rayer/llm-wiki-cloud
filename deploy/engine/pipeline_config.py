"""Validate the nonsecret Pipeline render shared by prepare and deployment."""
import hashlib
import json
from pathlib import Path
import re
import tomllib

SECRET_RESOURCE_RE = re.compile(
    r'^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+/versions/(?:[1-9][0-9]*|latest)$')
MAX_CONFIG_BYTES = 1 << 20
INLINE_SECRET_KEYS = {'api_key', 'api_key_value', 'secret_value', 'token'}


def parse_synto_toml(value):
    parsed = tomllib.loads(value)

    def contains_inline_secret(node):
        if isinstance(node, dict):
            return any(key.lower() in INLINE_SECRET_KEYS or contains_inline_secret(child)
                       for key, child in node.items())
        if isinstance(node, list):
            return any(contains_inline_secret(child) for child in node)
        return False

    if contains_inline_secret(parsed):
        raise ValueError('synto.toml must reference the runtime key through its environment variable')
    return parsed


def rendered_pipeline_config(directory, expected_environment):
    directory = Path(directory)
    public = json.loads((directory / 'pipeline.json').read_text())
    bindings = json.loads((directory / 'private-bindings.json').read_text())
    toml_bytes = (directory / 'synto.toml').read_bytes()
    if len(toml_bytes) == 0 or len(toml_bytes) > MAX_CONFIG_BYTES:
        raise ValueError('rendered Pipeline TOML size is invalid')
    if public.get('environment') != expected_environment or bindings.get('environment') != expected_environment:
        raise ValueError('rendered Pipeline environment does not match the selected target')
    timeout = public.get('runTimeoutSeconds')
    if type(timeout) is not int or timeout <= 0:
        raise ValueError('rendered Pipeline timeout must be a positive number of seconds')
    secret = public.get('secret')
    if not isinstance(secret, dict) or secret.get('target') != 'DEEPSEEK_API_KEY':
        raise ValueError('rendered Pipeline secret binding is invalid')
    if secret.get('source') != 'secret-manager' or not SECRET_RESOURCE_RE.fullmatch(secret.get('resource', '')):
        raise ValueError('deployed Pipeline requires a full Secret Manager version resource')
    if bindings.get('bindings') != [secret]:
        raise ValueError('private Pipeline binding does not match the evaluated SSOT')
    if secret['resource'].encode() in toml_bytes or b'secret-manager' in toml_bytes:
        raise ValueError('rendered Pipeline TOML contains secret reference metadata')
    parse_synto_toml(toml_bytes.decode('utf-8'))
    return {
        'environment': expected_environment,
        'bucket': public.get('bucket'),
        'timeout_seconds': timeout,
        'sha256': hashlib.sha256(toml_bytes).hexdigest(),
        'toml': toml_bytes.decode('utf-8'),
        'secret': secret,
    }


def secret_cli_binding(binding):
    """Convert the full Secret Manager resource to Cloud Run's name:version form."""
    if not isinstance(binding, dict) or not SECRET_RESOURCE_RE.fullmatch(binding.get('resource', '')):
        raise ValueError('Secret Manager resource is invalid')
    parts = binding['resource'].split('/')
    return binding['target'], parts[3] + ':' + parts[5]


def pipeline_config_uri(bucket):
    if not isinstance(bucket, str) or not re.fullmatch(r'[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]', bucket):
        raise ValueError('Pipeline bucket is invalid')
    return 'gs://' + bucket + '/pipeline-config/synto.toml'


def job_secret_binding(container, project_id, name='DEEPSEEK_API_KEY'):
    """Return a normalized secret resource or None; reject plaintext env values."""
    matches = [entry for entry in container.get('env', []) if entry.get('name') == name]
    if len(matches) > 1:
        raise ValueError('Worker has duplicate API key bindings')
    if not matches:
        return None
    entry = matches[0]
    ref = (entry.get('valueSource', {}).get('secretKeyRef') or
           entry.get('valueFrom', {}).get('secretKeyRef'))
    if not isinstance(ref, dict) or 'value' in entry:
        raise ValueError('Worker API key must use a managed secret reference')
    secret = ref.get('secret') or ref.get('name')
    version = ref.get('version') or ref.get('key')
    if not isinstance(secret, str) or not isinstance(version, str):
        raise ValueError('Worker API key secret reference is incomplete')
    if secret.startswith('projects/'):
        resource = secret if '/versions/' in secret else secret + '/versions/' + version
    else:
        resource = 'projects/' + project_id + '/secrets/' + secret + '/versions/' + version
    if not SECRET_RESOURCE_RE.fullmatch(resource):
        raise ValueError('Worker API key secret reference is invalid')
    return resource
