#!/usr/bin/env python3
import os
import signal
import socket
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(os.environ.get("LOCAL_VERTICAL_SMOKE_SCRIPT", ROOT / "scripts/local-vertical-smoke.sh"))


def free_ports(count):
    sockets = [socket.socket() for _ in range(count)]
    try:
        for listener in sockets:
            listener.bind(("127.0.0.1", 0))
        return [listener.getsockname()[1] for listener in sockets]
    finally:
        for listener in sockets:
            listener.close()


class LocalVerticalSmokeTests(unittest.TestCase):
    def test_smoke_isolates_google_fonts_and_keeps_auth_bff_frontend_checks(self):
        source = SCRIPT.read_text()
        layout = (ROOT / "apps/frontend/src/app/layout.tsx").read_text()

        self.assertIn('font_mock="$tmp_dir/google-fonts-local-fallback.cjs"', source)
        mock = source.split('cat > "$font_mock" <<\'NODE\'\n', 1)[1].split('\nNODE\n', 1)[0]
        self.assertIn("module.exports = {", mock)
        self.assertIn("family=Geist:wght@100..900&display=swap", mock)
        self.assertIn("family=Geist+Mono:wght@100..900&display=swap", mock)
        self.assertIn("family=Noto+Serif+TC:wght@500;600&display=swap", mock)
        self.assertIn('src: local("Arial")', mock)
        self.assertNotIn("src: url(", mock)
        self.assertIn('NEXT_FONT_GOOGLE_MOCKED_RESPONSES="$font_mock"', source)
        self.assertIn('NODE_ENV=development NEXT_TELEMETRY_DISABLED=1', source)

        # The mock is scoped to the smoke process; product font defaults stay intact.
        self.assertIn('from "next/font/google"', layout)
        self.assertIn('Noto_Serif_TC({', layout)
        self.assertNotIn("NEXT_FONT_GOOGLE_MOCKED_RESPONSES", layout)

        # Retain the complete canonical vertical checks around the isolated font input.
        self.assertIn('wait_for_http auth "http://127.0.0.1:$AUTH_PORT/api/v1/public/healthz"', source)
        self.assertIn('wait_for_http bff "http://127.0.0.1:$BFF_PORT/api/v1/public/version"', source)
        self.assertIn('wait_for_http frontend "http://127.0.0.1:$FRONTEND_PORT/"', source)
        self.assertIn('/api/v1/auth/login', source)
        self.assertIn('/api/v1/projects', source)
        self.assertIn('project.get("id") == "demo"', source)
        self.assertIn('"LLM Wiki Cloud" in body and "<html" in body', source)

    def test_exit_cleanup_statuses(self):
        source = SCRIPT.read_text()
        functions = source[source.index("listeners() {"):source.index("trap cleanup EXIT INT TERM")]
        with tempfile.TemporaryDirectory() as tmp:
            lsof = Path(tmp) / "lsof"
            lsof.write_text('#!/bin/sh\n[ "${OCCUPIED:-}" = 1 ] && echo 999\n')
            lsof.chmod(0o755)
            for original, occupied, expected in [(0, False, 0), (7, False, 7), (0, True, 1), (7, True, 7)]:
                harness = f'''#!/usr/bin/env bash
set -euo pipefail
tmp_dir=$(mktemp -d)
declare -a pids=(999999999)
env_file="$tmp_dir/env.local"
env_backup="$tmp_dir/env.backup"
had_env_file=false
cleanup_active=true
BFF_PORT=18080
AUTH_PORT=18081
FRONTEND_PORT=13000
{functions}
trap cleanup EXIT INT TERM
exit {original}
'''
                result = subprocess.run(
                    ["bash", "-c", harness],
                    env={**os.environ, "PATH": f"{tmp}:{os.environ['PATH']}", "OCCUPIED": "1" if occupied else ""},
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(
                    result.returncode,
                    expected,
                    f"original={original} occupied={occupied}: stderr={result.stderr}",
                )

    def test_failed_parallel_build_reaps_sibling(self):
        ports = free_ports(3)
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            marker = tmp_path / "auth-build.pid"
            fake_bin = tmp_path / "bin"
            fake_bin.mkdir()
            (fake_bin / "lsof").write_text("#!/bin/sh\nexit 0\n")
            (fake_bin / "go").write_text(f'''#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  [ "$1" = -o ] && out="$2" && shift
  shift
done
case "$out" in
  */bff) exit 1 ;;
  */auth) echo $$ > {marker}; sleep 60 ;;
esac
''')
            for tool in (fake_bin / "lsof", fake_bin / "go"):
                tool.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{fake_bin}:{os.environ['PATH']}",
                "BFF_PORT": str(ports[0]),
                "AUTH_PORT": str(ports[1]),
                "FRONTEND_PORT": str(ports[2]),
            }
            result = subprocess.run(["bash", str(SCRIPT)], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            deadline = time.time() + 2
            while time.time() < deadline and not marker.exists():
                time.sleep(0.02)
            self.assertTrue(marker.exists())
            pid = int(marker.read_text())
            try:
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)
            finally:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass


if __name__ == "__main__":
    unittest.main()
