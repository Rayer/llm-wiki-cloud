#!/usr/bin/env python3
import os
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = Path(os.environ.get("LOCAL_VERTICAL_SMOKE_SCRIPT", ROOT / "scripts/local-vertical-smoke.sh"))
PROVIDER_KEYS = (
    "LLM_API_KEY",
    "DEEPSEEK_API_KEY",
    "SYNTO_API_KEY",
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GEMINI_API_KEY",
    "TYPESAFE_API_KEY",
    "TYPESAFE_JEV_API_KEY",
    "LWC331_TEST_API_KEY",
)


class LocalVerticalSmokeTests(unittest.TestCase):
    def test_smoke_keeps_loopback_checks_and_runs_them_without_provider_keys(self):
        source = SCRIPT.read_text()

        self.assertIn("run_offline_test() {", source)
        self.assertIn("run_offline_test make local-synto-runtime-test", source)
        self.assertEqual(source.count("run_offline_test go test"), 4)
        for test_name in (
            "TestLocalCloudLoopbackUsesBearerAndIgnoresUserHeaderIdentity",
            "TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure",
            "TestLocalRefreshCookiePolicySupportsLoopbackHTTP",
        ):
            self.assertIn(test_name, source)
        for key in PROVIDER_KEYS:
            self.assertIn(f"-u {key}", source)

    def test_retired_fake_auth_and_destructive_local_seed_are_not_in_smoke(self):
        source = SCRIPT.read_text()
        for retired in ("--local", "LOCAL_DATA_DIR", "DEV_JWT", "JWT_SECRET=dev-secret", "rm -rf"):
            self.assertNotIn(retired, source)


if __name__ == "__main__":
    unittest.main()
