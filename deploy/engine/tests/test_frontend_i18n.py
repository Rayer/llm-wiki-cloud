"""Offline regression coverage for the frontend catalog translator."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import time
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[3] / "apps" / "frontend" / "scripts" / "prepare-i18n.py"
SPEC = importlib.util.spec_from_file_location("prepare_i18n", SCRIPT)
prepare_i18n = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(prepare_i18n)
ENGINE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ENGINE_DIR))
ENGINE_SPEC = importlib.util.spec_from_file_location(
    "frontend_i18n", ENGINE_DIR / "frontend_i18n.py")
frontend_i18n = importlib.util.module_from_spec(ENGINE_SPEC)
ENGINE_SPEC.loader.exec_module(frontend_i18n)


class FrontendI18nPrepareDiagnostics(unittest.TestCase):
    def test_start_failure_includes_stage_errno_and_public_executable(self):
        failure = FileNotFoundError(2, "No such file or directory", "go")
        with patch.object(frontend_i18n.subprocess, "run", side_effect=failure):
            with self.assertRaises(frontend_i18n.I18nPrepareError) as caught:
                frontend_i18n.run_stage(
                    ["go", "run"], cwd=Path("/tmp"), env={"LLM_API_KEY": "fixture-secret"},
                    timeout=1, stage="i18n source assembly")
        self.assertIn("i18n source assembly could not start", str(caught.exception))
        self.assertIn("FileNotFoundError errno=2 executable=go", str(caught.exception))

    def test_nonzero_child_stderr_is_bounded_and_redacted(self):
        result = subprocess.CompletedProcess(
            ["go", "run"], 7, stdout="", stderr="LLM_API_KEY=fixture-secret " + "x" * 1000)
        with patch.object(frontend_i18n.subprocess, "run", return_value=result):
            with self.assertRaises(frontend_i18n.I18nPrepareError) as caught:
                frontend_i18n.run_stage(
                    ["go", "run"], cwd=Path("/tmp"), env={"LLM_API_KEY": "fixture-secret"},
                    timeout=1, stage="i18n source assembly")
        message = str(caught.exception)
        self.assertIn("exit_code=7", message)
        self.assertIn("[REDACTED]", message)
        self.assertNotIn("fixture-secret", message)
        self.assertLess(len(message), 700)


class FrontendI18nCatalog(unittest.TestCase):
    def test_seed_digest_and_key_set_fail_closed(self):
        seed = {"Login": {"title": "登入"}}
        seed_bytes = json.dumps(seed, ensure_ascii=False).encode("utf-8")
        source = [{"key": "Login.title", "text": "Sign in", "params": [],
                   "protected_literals": [], "source_digest": "a" * 64}]
        provenance = {
            "schema_version": 1,
            "locale": "zh_tw",
            "seed_map_sha256": hashlib.sha256(seed_bytes).hexdigest(),
            "entries": {"Login.title": "a" * 64},
        }

        values, status = prepare_i18n.seed_values(seed, provenance, source, seed_bytes)
        self.assertEqual((values, status), ({"Login.title": "登入"}, "valid"))

        stale = dict(provenance, seed_map_sha256="0" * 64)
        self.assertEqual(prepare_i18n.seed_values(seed, stale, source, seed_bytes), ({}, "seed_map_digest_mismatch"))
        self.assertEqual(prepare_i18n.seed_values({}, provenance, source, seed_bytes), ({}, "seed_key_set_mismatch"))

    def test_placeholders_must_match_exactly(self):
        entry = {"key": "Admin.confirmDeleteProject", "text": "Delete {id} {name}",
                 "params": ["id", "name"], "protected_literals": []}
        self.assertEqual(prepare_i18n.validate_translation(entry, "刪除 {id} {name}", "zh_tw"), "刪除 {id} {name}")
        with self.assertRaisesRegex(prepare_i18n.CatalogError, "translation_parameters_mismatch"):
            prepare_i18n.validate_translation(entry, "刪除 {name}", "zh_tw")

    def test_invalid_fixture_falls_back_to_base(self):
        entry = {"key": "Admin.confirmDeleteProject", "text": "Delete {id} {name}",
                 "source_locale": "en", "params": ["id", "name"], "protected_literals": []}
        result = prepare_i18n.translate_locale(
            [entry], "zh_tw", {entry["key"]: entry["text"]}, {},
            {entry["key"]: "刪除 {name}"}, {}, "fixture-test", "", None,
            "not_configured", time.monotonic() + 10,
        )
        self.assertEqual(result[0], {entry["key"]: entry["text"]})
        self.assertEqual(result[1][entry["key"]], "fallback")
        self.assertEqual(result[4], 1)

    def test_provider_chunk_failure_retries_once_then_uses_base_fallback(self):
        entry = {"key": "Admin.projectsLoadFailed", "text": "Unable to load projects.",
                 "source_locale": "en", "context": "Admin UI", "params": [], "protected_literals": []}
        translation = {"endpoint": "https://provider.invalid/v1/chat/completions", "model": "fixture-model",
                       "max_output_tokens": 128, "request_timeout_seconds": 5}
        with patch.object(prepare_i18n, "safe_provider_call", side_effect=prepare_i18n.CatalogError("invalid fixture response")) as call:
            result = prepare_i18n.translate_locale(
                [entry], "zh_tw", {entry["key"]: entry["text"]}, {}, {}, translation,
                "fixture-test", "fixture-only-not-a-provider-key", None, "ready", time.monotonic() + 10,
            )
        self.assertEqual(call.call_count, 2)
        self.assertEqual(result[0], {entry["key"]: entry["text"]})
        self.assertEqual(result[1][entry["key"]], "fallback")
        self.assertEqual(result[2:], (1, 1, 0))

    def test_http_200_empty_choices_and_null_message_are_catalog_errors(self):
        entry = {"key": "Login.title", "text": "Sign In", "source_locale": "en", "context": "Login title",
                 "params": [], "protected_literals": []}

        class Response:
            def __init__(self, body):
                self.body = body

            def __enter__(self):
                return self

            def __exit__(self, *_):
                return False

            def read(self, _limit):
                return self.body

        for envelope in ({"choices": []}, {"choices": [{"message": None}]}):
            with self.subTest(envelope=envelope):
                response = Response(json.dumps(envelope).encode("utf-8"))
                translation = {"endpoint": "https://provider.invalid/v1/chat/completions", "model": "fixture-model",
                               "max_output_tokens": 128, "request_timeout_seconds": 5}
                with patch.object(prepare_i18n.urllib.request, "build_opener") as opener:
                    opener.return_value.open.return_value = response
                    result = prepare_i18n.translate_locale(
                        [entry], "zh_tw", {entry["key"]: entry["text"]}, {}, {}, translation,
                        "fixture-test", "test-key", None, "ready", time.monotonic() + 10,
                    )
                self.assertEqual(opener.return_value.open.call_count, 2)
                self.assertEqual(result[0], {entry["key"]: entry["text"]})
                self.assertEqual(result[1][entry["key"]], "fallback")
                self.assertEqual(result[2:], (1, 1, 0))

    def test_expired_total_deadline_publishes_a_complete_degraded_catalog(self):
        import tempfile
        with tempfile.TemporaryDirectory() as temporary:
            workspace = Path(temporary)
            source_dir = workspace / "source"
            frontend = workspace / "frontend"
            output = workspace / "catalog"
            source_dir.mkdir()
            frontend.mkdir()
            entry = {
                "key": "Login.title", "text": "Sign In", "source_locale": "en",
                "context": "Visible sign-in title", "params": [], "protected_literals": [],
                "origin": {"kind": "literal", "path": "apps/frontend/src/messages/en.json"},
            }
            entry["source_digest"] = prepare_i18n.entry_digest(entry)
            i18n = {
                "schema_version": 1, "base_locale": "en", "target_locales": ["zh_tw", "en"],
                "default_locale": "zh_tw", "locale_metadata": [
                    {"token": "zh_tw", "ui_locale": "zh-TW", "intl_locale": "zh-TW", "label": "繁體中文"},
                    {"token": "en", "ui_locale": "en", "intl_locale": "en-US", "label": "English"},
                ],
                "translation": {
                    "provider": "custom", "protocol": "openai_chat_completions",
                    "endpoint": "https://provider.invalid/v1/chat/completions", "model": "fixture-model",
                    "request_timeout_seconds": 5, "max_output_tokens": 128,
                    "secret": {"source": "environment", "target": "TEST_LLM_KEY"},
                },
                "prompt_version": "fixture-prompt-v1",
            }
            source_revision, catalog_revision = "a" * 40, "b" * 64
            files = [{"path": "deploy/cac/ssot.pkl#i18n-cac-projection", "sha256": "c" * 64}]
            source_manifest = {
                "schema_version": 1, "environment": "local", "source_revision": source_revision,
                "catalog_revision": catalog_revision, "config_digest": prepare_i18n.configured_public_digest(i18n),
                "cac_projection_digest": "c" * 64, "entry_count": 1,
                "key_set_digest": prepare_i18n.sha256(prepare_i18n.canonical_json([entry["key"]])),
                "files": files,
            }
            snapshot_identity = {key: source_manifest[key] for key in (
                "schema_version", "environment", "source_revision", "catalog_revision", "config_digest",
                "cac_projection_digest", "entry_count", "key_set_digest", "files",
            )}
            snapshot_revision = prepare_i18n.sha256(prepare_i18n.canonical_json(snapshot_identity))
            source_manifest["snapshot_revision"] = snapshot_revision
            config = {
                "schema_version": 1, "environment": "local", "target": "i18n",
                "source_revision": source_revision, "catalog_revision": catalog_revision,
                "snapshot_revision": snapshot_revision, "i18n": i18n,
            }
            source = {
                "schema_version": 1, "source_revision": source_revision,
                "catalog_revision": catalog_revision, "base_locale": "en", "entries": [entry],
            }
            for name, value in (("config.json", config), ("input.json", source),
                                ("source-manifest.json", source_manifest), ("base.json", {"Login.title": "Sign In"})):
                (source_dir / name).write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")
            seed_map = workspace / "seed.json"
            seed_map.write_text('{"Login":{"title":"登入"}}', encoding="utf-8")
            seed_provenance = workspace / "seed-provenance.json"
            seed_provenance.write_text('{"schema_version":1}', encoding="utf-8")
            args = type("Args", (), {
                "config": str(source_dir / "config.json"), "input": str(source_dir / "input.json"),
                "source_manifest": str(source_dir / "source-manifest.json"), "base": str(source_dir / "base.json"),
                "seed_map": str(seed_map), "seed_provenance": str(seed_provenance), "fixture": None,
                "api_key_stdin": True, "provider_status": "ready", "cache": None,
                "output": str(output), "frontend_root": str(frontend), "repository_root": str(workspace),
            })()
            with patch.object(prepare_i18n.sys, "stdin", io.StringIO("test-key")), \
                    patch.object(prepare_i18n.time, "monotonic", side_effect=[0, prepare_i18n.MAX_STAGE_SECONDS + 1]), \
                    patch.object(prepare_i18n, "safe_provider_call") as provider_call:
                prepare_i18n.prepare_catalog(args)

            provider_call.assert_not_called()
            revision = (output / "CURRENT").read_text(encoding="ascii").strip()
            data = json.loads((output / "revisions" / revision / "catalog-data.json").read_text(encoding="utf-8"))
            self.assertEqual(data["base"]["Login.title"], "Sign In")
            self.assertEqual(data["locales"]["zh_tw"]["Login"]["title"], "Sign In")
            self.assertEqual(data["manifest"]["translation"]["status"], "degraded")
            self.assertEqual(data["manifest"]["translation"]["failed_chunks"], 1)
            self.assertEqual(data["manifest"]["translation"]["fallback_counts"]["zh_tw"], 1)
            self.assertTrue((frontend / "src/generated/i18n/catalog.ts").is_file())
            self.assertTrue((frontend / "public/i18n/catalogs" / f"{revision}.manifest.json").is_file())


if __name__ == "__main__":
    unittest.main()
