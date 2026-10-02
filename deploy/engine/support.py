"""Bounded subprocesses and atomic private records; never echo provider output."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]

class Breakpoint(Exception):
    def __init__(self, reason, status='failed', mutation=False, action='correct-input-and-resume',
                 stage=None, exit_code=None, timeout_class=None, build=None):
        super().__init__(reason)
        self.reason, self.status, self.mutation, self.action = reason, status, mutation, action
        self.stage, self.exit_code, self.timeout_class = stage, exit_code, timeout_class
        self.build = build


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
    try:
        result = subprocess.run([str(a) for a in args], cwd=cwd, env=env, input=input,
                                capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        status = 'unknown' if mutation or unknown_on_error else 'failed'
        raise Breakpoint('provider-timeout', status, mutation, 'reconcile-before-replay',
                         stage=stage, timeout_class='subprocess-timeout') from None
    except OSError:
        raise Breakpoint('tool-unavailable', stage=stage, timeout_class='tool-unavailable') from None
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
        raise Breakpoint('permission-denied' if permission else 'command-failed',
                         status, mutation,
                         'restore-existing-principal-permission' if permission else 'reconcile-before-replay',
                         stage=reported_stage, exit_code=exit_code)
    return result.stdout.strip()
