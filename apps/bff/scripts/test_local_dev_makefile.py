#!/usr/bin/env python3
import importlib.util
import io
import json
import multiprocessing
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parents[1]
REPO = ROOT.parents[1]
LOCAL_SERVICES = REPO / "scripts" / "local-services.py"


def run_partial_start_supervisor(state, root, token, auth_port, bff_port):
    spec = importlib.util.spec_from_file_location("local_services_fixture", LOCAL_SERVICES)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)

    def service_command(name, _root, env):
        if name == "auth":
            code = "import os,socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',int(os.environ['AUTH_PORT']))); s.listen(); print('ready',flush=True); time.sleep(30)"
        else:
            code = "raise SystemExit(17)"
        return [sys.executable, "-c", code], root, env

    module.service_command = service_command
    os.environ["LOCAL_CLOUD_REPO_ROOT"] = str(root)
    os.environ["AUTH_PORT"] = str(auth_port)
    os.environ["BFF_PORT"] = str(bff_port)
    raise SystemExit(module.daemon(state, token, ["auth", "bff"], startup_timeout=3))


class LocalDevMakefileTests(unittest.TestCase):
    def make_dry_run(self, target, *variables):
        return subprocess.run(
            ["make", "-n", target, *variables],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        ).stdout

    def test_local_config_writes_only_public_frontend_urls(self):
        with tempfile.TemporaryDirectory() as tmp:
            frontend = Path(tmp) / "frontend"
            frontend.mkdir()
            subprocess.run(
                ["make", "local-config", f"FRONTEND_DIR={frontend}", "BFF_PORT=18080", "AUTH_PORT=18081"],
                cwd=ROOT,
                check=True,
                capture_output=True,
                text=True,
            )
            self.assertEqual(
                (frontend / ".env.local").read_text(),
                "NEXT_PUBLIC_API_URL=http://localhost:18080\n"
                "NEXT_PUBLIC_AUTH_URL=http://localhost:18081\n",
            )

    def test_native_targets_use_worktree_supervisor_and_same_fixture_config(self):
        output = self.make_dry_run("local-start", "BFF_PORT=18080")
        self.assertIn("local-fixture", output)
        self.assertIn("python3 ../../scripts/local-services.py start auth bff frontend", output)
        self.assertIn("python3 -m venv", output)
        self.assertIn('bin/synto" --version', output)
        self.assertIn("LOCAL_LOGIN_EMAIL", output)
        self.assertNotIn("docker compose", output)
        for retired in ("--local", "LOCAL_DATA_DIR", "DEV_JWT", "make seed", "local-token"):
            self.assertNotIn(retired, output)

    def test_local_worker_runtime_matches_pinned_image_and_runs_without_provider(self):
        makefile = (ROOT / "Makefile").read_text()
        dockerfile = (ROOT / "cmd" / "olw_worker" / "Dockerfile").read_text()
        for pin in (
            "SYNTO_VERSION=0.7.0",
            "SYNTO_ARTIFACT_URL=https://files.pythonhosted.org/packages/4a/e9/41c6b61338d98820780a43ed075cd77525674c38242110435330771d771b/synto-0.7.0-py3-none-any.whl",
            "SYNTO_SHA256=4bc8dcf14b53f45fac32ce737ecf878f1a46d6d0b010c7decbe6c3b7b10afa77",
        ):
            key, value = pin.split("=", 1)
            self.assertIn(f"{key} := {value}", makefile)
            self.assertIn(f"ARG {pin}", dockerfile)
        self.assertIn('venv="$$state_dir/python"', makefile)
        self.assertIn("pip install --disable-pip-version-check --no-input --no-cache-dir --force-reinstall", makefile)
        local_env = (ROOT.parents[1] / "scripts" / "local-cloud-env.sh").read_text()
        self.assertIn('PATH="$state_dir/python/bin:$PATH"', local_env)
        self.assertIn("LOCAL_CLOUD_PYTHON PATH", local_env)
        runtime_test = self.make_dry_run("local-synto-runtime-test")
        self.assertIn("go test -tags lwc_local_synto_runtime ./cmd/olw_worker", runtime_test)
        for key in ("LLM_API_KEY", "DEEPSEEK_API_KEY", "SYNTO_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "TYPESAFE_API_KEY", "TYPESAFE_JEV_API_KEY", "LWC331_TEST_API_KEY"):
            self.assertIn(f"-u {key}", runtime_test)

    def test_component_support_targets_start_only_required_native_services(self):
        support_bff = self.make_dry_run("support-bff")
        support_frontend = self.make_dry_run("support-frontend")
        self.assertIn("local-services.py start auth frontend", support_bff)
        self.assertIn("local-services.py start auth bff", support_frontend)

    def test_stop_is_scoped_to_supervisor_metadata(self):
        output = self.make_dry_run("kill-local")
        self.assertIn("local-services.py stop", output)
        self.assertNotIn("lsof", output)
        self.assertNotIn("pkill", output)

    def test_make_help_documents_native_local_path(self):
        root_help = subprocess.run(["make", "help"], cwd=REPO, check=True, capture_output=True, text=True).stdout
        app_help = subprocess.run(["make", "help"], cwd=ROOT, check=True, capture_output=True, text=True).stdout
        self.assertIn("native Auth, BFF, and Frontend", root_help)
        self.assertIn("only processes supervised for this worktree", app_help)
        self.assertIn("pipeline-test", app_help)

    def test_deploy_dev_recipe_is_parseable_and_keeps_production_target(self):
        output = self.make_dry_run("deploy-dev")
        self.assertEqual(output.count("gcloud run deploy"), 1)
        subprocess.run(["sh", "-n"], input=output, cwd=ROOT, check=True, text=True)
        self.assertIn("GCP_PROJECT=llm-wiki-cloud", output)
        self.assertIn("BUCKET=llm-wiki-data-dev", output)
        self.assertIn("FIRESTORE_DATABASE_ID=llm-wiki-cloud-dev", output)
        self.assertIn("DEV_JWT=false", output)

    def test_pipeline_test_is_local_worker_suite_without_cloud_run(self):
        output = self.make_dry_run("pipeline-test")
        self.assertIn("go test ./cmd/olw_worker", output)
        self.assertNotIn("gcloud run jobs execute", output)

    def test_supervisor_reports_partial_start_failure_and_cleans_only_its_child(self):
        with tempfile.TemporaryDirectory() as tmp, socket.socket() as unrelated:
            state = Path(tmp) / "state"
            root = Path(tmp) / "repo"
            root.mkdir()
            unrelated.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            unrelated.bind(("127.0.0.1", 0))
            unrelated.listen()
            unrelated_port = unrelated.getsockname()[1]
            with socket.socket() as auth_probe, socket.socket() as bff_probe:
                auth_probe.bind(("127.0.0.1", 0))
                auth_port = auth_probe.getsockname()[1]
                bff_probe.bind(("127.0.0.1", 0))
                bff_port = bff_probe.getsockname()[1]

            process = multiprocessing.get_context("fork").Process(
                target=run_partial_start_supervisor,
                args=(state, root, "fixture-token", auth_port, bff_port),
            )
            process.start()
            process.join(timeout=8)
            if process.is_alive():
                process.terminate()
                process.join(timeout=3)
                self.fail("native supervisor did not settle after partial startup failure")
            self.assertEqual(process.exitcode, 1)
            startup = json.loads((state / "startup.json").read_text())
            self.assertEqual(startup["state"], "failed")
            self.assertEqual(startup["service"], "bff")
            self.assertIn("code 17", startup["reason"])
            self.assertTrue(Path(startup["log"]).exists())

            with self.assertRaises(OSError):
                socket.create_connection(("127.0.0.1", auth_port), timeout=0.2)
            with socket.create_connection(("127.0.0.1", unrelated_port), timeout=0.2):
                pass

    def test_start_returns_named_failure_when_supervised_service_cannot_start(self):
        spec = importlib.util.spec_from_file_location("local_services_start_fixture", LOCAL_SERVICES)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            state = Path(tmp) / "state"
            root.mkdir()
            module.STARTUP_TIMEOUT_SECONDS = 3
            stderr = io.StringIO()
            with patch.dict(os.environ, {"LOCAL_CLOUD_REPO_ROOT": str(root), "AUTH_PORT": "18081"}), redirect_stderr(stderr):
                result = module.start(state, ["auth"])
            self.assertEqual(result, 1)
            self.assertIn("local service auth failed startup", stderr.getvalue())
            self.assertIn(str(state / "auth.log"), stderr.getvalue())

    def test_frontend_readiness_accepts_ipv6_localhost(self):
        spec = importlib.util.spec_from_file_location("local_services_ipv6_fixture", LOCAL_SERVICES)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        try:
            listener = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("::1", 0))
            listener.listen()
        except OSError as exc:
            self.skipTest(f"IPv6 loopback is unavailable: {exc}")
        with listener:
            port = listener.getsockname()[1]
            child = Mock()
            child.poll.return_value = None
            self.assertIsNone(module.wait_for_service_readiness({"frontend": child}, {"FRONTEND_PORT": str(port)}, 0.1))

    def test_frontend_readiness_rejects_an_unbound_loopback_port(self):
        spec = importlib.util.spec_from_file_location("local_services_closed_port_fixture", LOCAL_SERVICES)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        try:
            with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as probe:
                probe.bind(("::1", 0))
                port = probe.getsockname()[1]
        except OSError as exc:
            self.skipTest(f"IPv6 loopback is unavailable: {exc}")
        child = Mock()
        child.poll.return_value = None
        self.assertEqual(
            module.wait_for_service_readiness({"frontend": child}, {"FRONTEND_PORT": str(port)}, 0.05),
            ("frontend", "did not accept loopback connections within 0.05s"),
        )


if __name__ == "__main__":
    unittest.main()
