"""Offline tests for exact frontend public-config artifact selection."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import yaml


SCRIPT = Path(__file__).resolve().parent / "frontend_config_artifacts.py"
spec = importlib.util.spec_from_file_location("frontend_config_artifacts", SCRIPT)
artifacts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(artifacts)

SOURCE_SHA = "a" * 40
CONFIG = b'{"schema_version":1,"api_url":"https://api.example.test","auth_url":"https://auth.example.test"}\n'


def run_metadata(source_sha=SOURCE_SHA, **overrides):
    return {
        "id": 104,
        "path": ".github/workflows/generate-frontend-config.yml@refs/heads/main",
        "event": "workflow_dispatch",
        "status": "completed",
        "conclusion": "success",
        "head_sha": source_sha,
        **overrides,
    }


def artifact_metadata(artifact_id=72, environment="dev", source_sha=SOURCE_SHA, **overrides):
    return {
        "id": artifact_id,
        "name": f"frontend-config-{environment}",
        "expired": False,
        "workflow_run": {"id": 104, "head_sha": source_sha},
        **overrides,
    }


class FrontendConfigArtifactTests(unittest.TestCase):
    def test_prepare_preserves_config_bytes_and_records_exact_checkout(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = root / "source.json"
            config.write_bytes(CONFIG)
            manifest = artifacts.prepare_artifact("dev", SOURCE_SHA, SOURCE_SHA, config, root / "artifact")
            self.assertEqual((root / "artifact/frontend-config.json").read_bytes(), CONFIG)
            self.assertEqual(manifest, {
                "schema_version": 1,
                "environment": "dev",
                "source_sha": SOURCE_SHA,
                "config_file": "frontend-config.json",
                "config_sha256": hashlib.sha256(CONFIG).hexdigest(),
            })

    def test_prepare_rejects_wrong_checkout_or_non_public_payload(self):
        with tempfile.TemporaryDirectory() as temp:
            config = Path(temp) / "source.json"
            config.write_bytes(CONFIG)
            with self.assertRaisesRegex(ValueError, "checkout"):
                artifacts.prepare_artifact("dev", SOURCE_SHA, "b" * 40, config, Path(temp) / "out")
            config.write_bytes(b'{"schema_version":1,"api_url":"https://api.example.test","auth_url":"https://auth.example.test","secret":"x"}')
            with self.assertRaisesRegex(ValueError, "fields"):
                artifacts.prepare_artifact("dev", SOURCE_SHA, SOURCE_SHA, config, Path(temp) / "out")

    def test_validate_bundle_requires_exact_manifest_bytes_environment_and_sha(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            root.joinpath("frontend-config.json").write_bytes(CONFIG)
            root.joinpath("manifest.json").write_text(json.dumps({
                "schema_version": 1, "environment": "dev", "source_sha": SOURCE_SHA,
                "config_file": "frontend-config.json", "config_sha256": hashlib.sha256(CONFIG).hexdigest(),
            }))
            self.assertEqual(artifacts.validate_artifact_directory(root, "dev", SOURCE_SHA)["environment"], "dev")
            with self.assertRaisesRegex(ValueError, "manifest"):
                artifacts.validate_artifact_directory(root, "prod", SOURCE_SHA)
            with self.assertRaisesRegex(ValueError, "manifest"):
                artifacts.validate_artifact_directory(root, "dev", "b" * 40)
            root.joinpath("frontend-config.json").write_bytes(CONFIG + b" ")
            with self.assertRaisesRegex(ValueError, "manifest"):
                artifacts.validate_artifact_directory(root, "dev", SOURCE_SHA)
            root.joinpath("frontend-config.json").write_bytes(CONFIG)
            root.joinpath("extra.txt").write_text("unexpected")
            with self.assertRaisesRegex(ValueError, "only"):
                artifacts.validate_artifact_directory(root, "dev", SOURCE_SHA)

    def test_metadata_binds_exact_artifact_to_successful_generator_run_and_source(self):
        identity = artifacts.validate_artifact_metadata(artifact_metadata(), run_metadata(), "72", "dev")
        self.assertEqual(identity, {"run_id": "104", "source_sha": SOURCE_SHA})
        cases = [
            (artifact_metadata(environment="prod"), run_metadata(), "72", "dev"),
            (artifact_metadata(expired=True), run_metadata(), "72", "dev"),
            (artifact_metadata(), run_metadata(conclusion="failure"), "72", "dev"),
            (artifact_metadata(), run_metadata(path=".github/workflows/deploy-dev.yml@refs/heads/main"), "72", "dev"),
            (artifact_metadata(source_sha="b" * 40), run_metadata(), "72", "dev"),
            (artifact_metadata(), run_metadata(), "73", "dev"),
        ]
        for metadata, run, artifact_id, environment in cases:
            with self.subTest(metadata=metadata, run=run, artifact_id=artifact_id):
                with self.assertRaises(ValueError):
                    artifacts.validate_artifact_metadata(metadata, run, artifact_id, environment)

    def test_selected_source_a_config_publishes_unchanged_after_checkout_advances_to_b(self):
        source_a = "a" * 40
        source_b = "b" * 40
        config_a = b'{"schema_version":1,"api_url":"https://api-a.example.test","auth_url":"https://auth-a.example.test"}\n'
        config_b = b'{"schema_version":1,"api_url":"https://api-b.example.test","auth_url":"https://auth-b.example.test"}\n'
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "source-a.json"
            source.write_bytes(config_a)
            bundle = root / "artifact-a"
            artifacts.prepare_artifact("dev", source_a, source_a, source, bundle)
            identity = artifacts.validate_artifact_metadata(
                artifact_metadata(artifact_id=701, source_sha=source_a),
                run_metadata(source_sha=source_a), "701", "dev",
            )
            artifacts.validate_artifact_directory(bundle, "dev", identity["source_sha"])

            advanced_checkout_config = root / "source-b.json"
            advanced_checkout_config.write_bytes(config_b)
            self.assertEqual(identity["source_sha"], source_a)
            self.assertNotEqual(source_a, source_b)

            object_path = root / "synthetic-dev-object.json"
            object_path.write_bytes((bundle / "frontend-config.json").read_bytes())
            self.assertEqual(object_path.read_bytes(), config_a)
            self.assertNotEqual(object_path.read_bytes(), advanced_checkout_config.read_bytes())

            previous_prod = b'{"schema_version":1,"api_url":"https://old-prod.example.test","auth_url":"https://old-auth.example.test"}'
            prod_object = root / "synthetic-prod-object.json"
            prod_object.write_bytes(previous_prod)
            with self.assertRaises(ValueError):
                artifacts.validate_artifact_metadata(
                    artifact_metadata(artifact_id=701, source_sha=source_a),
                    run_metadata(source_sha=source_a), "701", "prod",
                )
            self.assertEqual(prod_object.read_bytes(), previous_prod)

    def test_rejects_duplicate_json_keys_and_insecure_urls(self):
        with self.assertRaisesRegex(ValueError, "duplicate"):
            artifacts.validate_config(b'{"schema_version":1,"api_url":"https://a","api_url":"https://b","auth_url":"https://c"}')
        for url in ("http://api.example.test", "https://user@api.example.test", "https://api.example.test/?token=x"):
            with self.subTest(url=url):
                payload = json.dumps({"schema_version": 1, "api_url": url, "auth_url": "https://auth.example.test"}).encode()
                with self.assertRaises(ValueError):
                    artifacts.validate_config(payload)

    def test_workflows_keep_generation_and_publication_as_separate_exact_artifact_steps(self):
        root = Path(__file__).resolve().parents[1]

        def workflow(name):
            return yaml.load((root / ".github/workflows" / name).read_text(), Loader=yaml.BaseLoader)

        generator = workflow("generate-frontend-config.yml")
        self.assertEqual(generator["on"]["workflow_dispatch"]["inputs"]["environment"]["options"], ["dev", "prod"])
        self.assertEqual(generator["permissions"], {"contents": "read"})
        generator_steps = generator["jobs"]["generate"]["steps"]
        generate_step = next(step for step in generator_steps if step.get("name") == "Generate selected public Frontend config")
        self.assertIn("CONFIG_TARGET=frontend", generate_step["run"])
        self.assertNotIn("google-github-actions/auth", (root / ".github/workflows/generate-frontend-config.yml").read_text())

        development = workflow("deploy-dev.yml")
        production = workflow("promote-production.yml")
        self.assertIn("frontend-config-only", development["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend-config-only", production["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend_config_artifact_id", development["on"]["workflow_dispatch"]["inputs"])
        self.assertIn("frontend_config_artifact_id", production["on"]["workflow_dispatch"]["inputs"])

        cd = workflow("cd.yml")
        self.assertIn("frontend-config-only", cd["jobs"]["release"]["if"])
        job = cd["jobs"]["frontend-config-only"]
        self.assertEqual(job["if"], "inputs.operation == 'frontend-config-only'")
        steps = job["steps"]
        names = [step.get("name", step.get("uses", "")) for step in steps]
        validate_index = names.index("Validate selected artifact metadata and public JSON bytes")
        auth_index = next(index for index, step in enumerate(steps) if step.get("uses", "").startswith("google-github-actions/auth@"))
        publish_index = names.index("Publish only the selected public config and verify exact readback")
        self.assertLess(validate_index, auth_index)
        self.assertLess(auth_index, publish_index)
        self.assertFalse(any("make " in step.get("run", "") or "vercel build" in step.get("run", "") for step in steps))
        download = next(step for step in steps if step.get("uses", "").startswith("actions/download-artifact@"))
        self.assertIn("artifact-ids", download["with"])
        self.assertIn("run-id", download["with"])


if __name__ == "__main__":
    unittest.main()
