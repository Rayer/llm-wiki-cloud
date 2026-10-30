"""Offline regression coverage for the frontend catalog translator."""

import hashlib
import importlib.util
import json
from pathlib import Path
import time
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[3] / "apps" / "frontend" / "scripts" / "prepare-i18n.py"
SPEC = importlib.util.spec_from_file_location("prepare_i18n", SCRIPT)
prepare_i18n = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(prepare_i18n)


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


if __name__ == "__main__":
    unittest.main()
