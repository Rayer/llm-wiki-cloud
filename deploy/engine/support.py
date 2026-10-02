"""Bounded subprocesses and atomic private records with safe failure detail."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]

class Breakpoint(Exception):
    def __init__(self, reason, status='failed', mutation=False, action='correct-input-and-resume',
                 stage=None, exit_code=None, timeout_class=None, build=None, cause=None):
        super().__init__(reason)
        self.reason, self.status, self.mutation, self.action = reason, status, mutation, action
        self.stage, self.exit_code, self.timeout_class = stage, exit_code, timeout_class
        self.build, self.cause = build, cause


class InputShapeError(Exception):
    """A provider response has a known invalid structural shape."""


_CAUSE_TYPES = {
    json.JSONDecodeError: ('JSONDecodeError', 'invalid-json'),
    KeyError: ('KeyError', 'required-field-missing'),
    TypeError: ('TypeError', 'wrong-field-type'),
    AttributeError: ('AttributeError', 'invalid-response-shape'),
    InputShapeError: ('InputShapeError', 'invalid-response-shape'),
    UnicodeDecodeError: ('UnicodeDecodeError', 'invalid-input-encoding'),
    ValueError: ('ValueError', 'invalid-input'),
    FileNotFoundError: ('FileNotFoundError', 'local-input-unreadable'),
    PermissionError: ('PermissionError', 'local-input-unreadable'),
    OSError: ('OSError', 'local-input-unreadable'),
    ChildProcessError: ('ChildProcessError', 'child-command-failed'),
    subprocess.TimeoutExpired: ('TimeoutExpired', 'child-command-timeout'),
}
_CAUSE_STAGES = {
    'frontend-project-readback', 'frontend-npm-ci',
    'frontend-vercel-pull', 'frontend-vercel-build', 'unknown',
}
_SENSITIVE_ENVIRONMENT_KEYS = ('VERCEL_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN', 'ACTIONS_RUNTIME_TOKEN')
_MAX_CAUSE_MESSAGE_LENGTH = 512
_CAUSE_CODES = {value[1] for value in _CAUSE_TYPES.values()} | {
    'tool-unavailable', 'unclassified-input-error'}
_MESSAGE_UNSET = object()


def safe_error_message(value, *, sensitive_values=()):
    """Keep bounded error text while masking known credentials and auth syntax."""
    if isinstance(value, bytes):
        value = value.decode('utf-8', errors='replace')
    if not isinstance(value, str) or not value:
        return None, False
    secrets = {os.environ.get(key, '') for key in _SENSITIVE_ENVIRONMENT_KEYS if os.environ.get(key)}
    secrets.update(value for value in sensitive_values if isinstance(value, str) and value)
    secrets = sorted(secrets, key=len, reverse=True)
    for secret in secrets:
        value = value.replace(secret, '[REDACTED]')
        encoded = urllib.parse.quote(secret, safe='')
        if encoded != secret:
            value = value.replace(encoded, '[REDACTED]')
    value = re.sub(r'(?im)(authorization\s*:\s*)[^\r\n]*',
                   r'\1[REDACTED]', value)
    def redact_token_option(match):
        quote = '"' if match.group(2) is not None else "'" if match.group(3) is not None else ''
        return match.group(1) + quote + '[REDACTED]' + quote

    value = re.sub(r'''(?i)(--token(?:=|\s+))(?:"([^"]*)"|'([^']*)'|([^\s,;]+))''',
                   redact_token_option, value)
    value = re.sub(r'(?i)\b(VERCEL_TOKEN|GH_TOKEN|GITHUB_TOKEN|ACTIONS_RUNTIME_TOKEN)\s*=\s*([^\s,;]+)',
                   r'\1=[REDACTED]', value)
    truncated = len(value) > _MAX_CAUSE_MESSAGE_LENGTH
    return value[:_MAX_CAUSE_MESSAGE_LENGTH], truncated


def structured_cause(exc, stage='unknown', *, code=None, message=_MESSAGE_UNSET,
                     sensitive_values=()):
    """Expose known exception classes and bounded, selectively redacted messages."""
    exception_type, default_code = _CAUSE_TYPES.get(type(exc), ('unknown', 'unclassified-input-error'))
    if exception_type == 'unknown':
        code = 'unclassified-input-error'
    else:
        code = code if isinstance(code, str) and code in _CAUSE_CODES else default_code
    if not isinstance(stage, str) or stage not in _CAUSE_STAGES:
        stage = 'unknown'
    if message is _MESSAGE_UNSET:
        if exception_type == 'unknown':
            raw_message = None
        elif type(exc) is json.JSONDecodeError:
            # JSONDecodeError.__str__ embeds the source document. Keep its useful
            # parser message and location while excluding the possibly sensitive body.
            raw_message = f'{exc.msg} at line {exc.lineno} column {exc.colno}'
        elif type(exc) is UnicodeDecodeError:
            raw_message = f'{exc.reason} at byte {exc.start}'
        else:
            raw_message = str(exc)
    else:
        raw_message = message
    message, truncated = safe_error_message(raw_message, sensitive_values=sensitive_values)
    cause = {
        'exception_type': exception_type,
        'exception_type_omitted': exception_type == 'unknown',
        'stage': stage,
        'code': code,
        'message': message,
        'message_truncated': truncated,
        'message_omitted': message is None,
    }
    return cause


def require(ok, reason):
    if not ok:
        raise Breakpoint(reason)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, sort_keys=True, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


_STAGE_FAILURE = re.compile(
    r'^LWC_ENGINE_FAILURE stage=(build-submit|tag-digest-resolve|digest-validate) '
    r'exit_code=([0-9]{1,3}) permission=([01])$')


def run(args, *, cwd=ROOT, env=None, timeout=30, mutation=False, input=None, stage=None,
        unknown_on_error=False):
    frontend_stage = stage in _CAUSE_STAGES - {'unknown'}
    sensitive_values = tuple((env if isinstance(env, dict) else os.environ).get(key, '')
                             for key in _SENSITIVE_ENVIRONMENT_KEYS)
    try:
        result = subprocess.run([str(a) for a in args], cwd=cwd, env=env, input=input,
                                capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        status = 'unknown' if mutation or unknown_on_error else 'failed'
        cause = (structured_cause(exc, stage, code='child-command-timeout',
                                  message=exc.stderr, sensitive_values=sensitive_values)
                 if frontend_stage else None)
        raise Breakpoint('provider-timeout', status, mutation, 'reconcile-before-replay',
                         stage=stage, timeout_class='subprocess-timeout', cause=cause) from None
    except OSError as exc:
        cause = (structured_cause(exc, stage, code='tool-unavailable',
                                  sensitive_values=sensitive_values)
                 if frontend_stage else None)
        raise Breakpoint('tool-unavailable', stage=stage, timeout_class='tool-unavailable',
                         cause=cause) from None
    if result.returncode:
        details = None
        if stage == 'auth-build':
            for line in result.stderr.splitlines():
                details = _STAGE_FAILURE.fullmatch(line)
                if details:
                    break
        permission = (details and details.group(3) == '1') or any(
            s in result.stderr.lower() for s in ('permission_denied', 'permission denied', 'forbidden',
                'unauthorized', 'returned error: 403', 'returned error: 401'))
        reported_stage = details.group(1) if details else stage
        exit_code = int(details.group(2)) if details else result.returncode
        status = 'unknown' if mutation or unknown_on_error else 'failed'
        cause = (structured_cause(ChildProcessError(), stage, code='child-command-failed',
                                  message=result.stderr, sensitive_values=sensitive_values)
                 if frontend_stage else None)
        raise Breakpoint('permission-denied' if permission else 'command-failed',
                         status, mutation,
                         'restore-existing-principal-permission' if permission else 'reconcile-before-replay',
                         stage=reported_stage, exit_code=exit_code, cause=cause)
    return result.stdout.strip()
