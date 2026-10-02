"""Bounded subprocesses and atomic private records; never echo provider output."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]

class Breakpoint(Exception):
    def __init__(self, reason, status='failed', mutation=False, action='correct-input-and-resume'):
        super().__init__(reason)
        self.reason, self.status, self.mutation, self.action = reason, status, mutation, action


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


def run(args, *, cwd=ROOT, env=None, timeout=30, mutation=False, input=None):
    try:
        result = subprocess.run([str(a) for a in args], cwd=cwd, env=env, input=input,
                                capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise Breakpoint('provider-timeout', 'unknown', mutation, 'reconcile-before-replay') from None
    except OSError:
        raise Breakpoint('tool-unavailable') from None
    if result.returncode:
        permission = any(s in result.stderr.lower() for s in ('permission_denied', 'permission denied', 'forbidden', 'unauthorized', 'returned error: 403', 'returned error: 401'))
        raise Breakpoint('permission-denied' if permission else 'command-failed',
                         'unknown' if mutation else 'failed', mutation,
                         'restore-existing-principal-permission' if permission else 'reconcile-before-replay')
    return result.stdout.strip()
