"""Offline tests for exact frontend public-config artifact selection."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import yaml


SCRIPT = Path(__file__).resolve().parent / "frontend_config_artifacts.py"
spec = importlib.util.spec_from_file_location("frontend_config_artifacts", SCRIPT)
artifacts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(artifacts)

SOURCE_SHA = "a" * 40
CONFIG = b'{"schema_version":1,"api_url":"https://api.example.test","auth_url":"https://auth.example.test"}\n'
PINNED_DOWNLOADER = "d3f86a106a0bac45b974a628896c90dbdf5c8093"
PINNED_DOWNLOADER_SOURCE_SHA256 = "1c67eb1bb4f77a462522341189f433422c4d35a6999d0dea2a1846e6edfedd80"
REPO_ROOT = Path(__file__).resolve().parents[1]


def write_executable(path, body):
    path.write_text(body, encoding="utf-8")
    path.chmod(0o755)


def offline_tool_shims(root):
    binary_dir = root / "bin"
    binary_dir.mkdir(parents=True, exist_ok=True)
    write_executable(binary_dir / "gh", """#!/bin/sh
case "$2" in
  */actions/artifacts/*) cat "$LWC_TEST_ARTIFACT_METADATA" ;;
  */actions/runs/*) cat "$LWC_TEST_RUN_METADATA" ;;
  *) exit 2 ;;
esac
""")
    write_executable(binary_dir / "gcloud", """#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
args = sys.argv[1:]
log = pathlib.Path(os.environ["LWC_TEST_GCLOUD_LOG"])
store = pathlib.Path(os.environ["LWC_TEST_OBJECT_STORE"])
def object_path(uri):
    if not uri.startswith("gs://"):
        raise SystemExit("unexpected non-synthetic storage URI")
    return store / uri[5:]
if args[:2] == ["storage", "cp"]:
    paths = [part for part in args[2:] if not part.startswith("-")]
    if len(paths) != 2:
        raise SystemExit("unexpected controlled gcloud cp arguments")
    source, destination = paths
    if destination.startswith("gs://"):
        target = object_path(destination)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        with log.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps({"op": "publish", "uri": destination}) + "\\n")
    elif source.startswith("gs://"):
        shutil.copyfile(object_path(source), destination)
    else:
        raise SystemExit("controlled gcloud cp must have one synthetic URI")
elif args[:3] == ["storage", "objects", "describe"]:
    object_path(args[3])
    print("1")
else:
    raise SystemExit("unexpected controlled gcloud invocation")
""")
    return binary_dir


def workflow_run(step, env):
    return subprocess.run(["bash", "-e", "-c", step["run"]], cwd=REPO_ROOT, env=env,
                          capture_output=True, text=True)


def offline_case(root, selected_artifact=True):
    runner_temp = root / "runner-temp"
    runner_temp.mkdir()
    metadata_path = root / "artifact-metadata.json"
    run_path = root / "run-metadata.json"
    artifacts_path = root / "available-artifacts.json"
    bundles = root / "bundles"
    bundles.mkdir()
    downloads_log = root / "downloads.jsonl"
    gcloud_log = root / "gcloud.jsonl"
    object_store = root / "object-store"
    artifact_id = 701
    run_id = 104
    source_sha = SOURCE_SHA
    metadata_path.write_text(json.dumps(artifact_metadata(artifact_id=artifact_id, source_sha=source_sha)))
    run_path.write_text(json.dumps(run_metadata(source_sha=source_sha)))
    available = [{"id": 702, "name": "frontend-config-dev", "size": 1}]
    if selected_artifact:
        available.insert(0, {"id": artifact_id, "name": "frontend-config-dev", "size": 1})
    artifacts_path.write_text(json.dumps(available))
    tool_path = offline_tool_shims(root)
    env = os.environ.copy()
    env.update({
        "PATH": str(tool_path) + os.pathsep + env.get("PATH", ""),
        "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
        "FRONTEND_CONFIG_ARTIFACT_ID": str(artifact_id),
        "TARGET_ENVIRONMENT": "dev",
        "RUNNER_TEMP": str(runner_temp),
        "GITHUB_OUTPUT": str(root / "inspect-output.txt"),
        "GITHUB_STEP_SUMMARY": str(root / "summary.md"),
        "GH_TOKEN": "synthetic-token",
        "LWC_TEST_ARTIFACT_METADATA": str(metadata_path),
        "LWC_TEST_RUN_METADATA": str(run_path),
        "LWC_TEST_ARTIFACTS_JSON": str(artifacts_path),
        "LWC_TEST_BUNDLES_DIR": str(bundles),
        "LWC_TEST_DOWNLOADS_LOG": str(downloads_log),
        "LWC_TEST_GCLOUD_LOG": str(gcloud_log),
        "LWC_TEST_OBJECT_STORE": str(object_store),
        "LWC_TEST_EXPECTED_RUN_ID": str(run_id),
    })
    return {
        "env": env,
        "root": root,
        "runner_temp": runner_temp,
        "bundles": bundles,
        "downloads_log": downloads_log,
        "gcloud_log": gcloud_log,
        "object_store": object_store,
        "artifact_id": artifact_id,
        "run_id": run_id,
        "source_sha": source_sha,
        "artifacts_path": artifacts_path,
    }


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
        dev_wrapper_identity = artifacts.validate_artifact_metadata(
            artifact_metadata(), run_metadata(path=".github/workflows/deploy-dev.yml@refs/heads/develop"), "72", "dev",
        )
        self.assertEqual(dev_wrapper_identity, identity)
        prod_wrapper_identity = artifacts.validate_artifact_metadata(
            artifact_metadata(environment="prod"),
            run_metadata(path=".github/workflows/promote-production.yml@refs/heads/develop"), "72", "prod",
        )
        self.assertEqual(prod_wrapper_identity, identity)
        cases = [
            (artifact_metadata(environment="prod"), run_metadata(), "72", "dev"),
            (artifact_metadata(expired=True), run_metadata(), "72", "dev"),
            (artifact_metadata(), run_metadata(conclusion="failure"), "72", "dev"),
            (artifact_metadata(), run_metadata(path=".github/workflows/promote-production.yml@refs/heads/develop"), "72", "dev"),
            (artifact_metadata(environment="prod"), run_metadata(path=".github/workflows/deploy-dev.yml@refs/heads/develop"), "72", "prod"),
            (artifact_metadata(), run_metadata(path=".github/workflows/untrusted.yml@refs/heads/develop"), "72", "dev"),
            (artifact_metadata(environment="other"), run_metadata(), "72", "other"),
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
        self.assertEqual(generator["on"]["workflow_call"]["inputs"]["environment"], {"required": "true", "type": "string"})
        self.assertEqual(generator["permissions"], {"contents": "read"})
        generator_steps = generator["jobs"]["generate"]["steps"]
        generate_step = next(step for step in generator_steps if step.get("name") == "Generate selected public Frontend config")
        self.assertIn("CONFIG_TARGET=frontend", generate_step["run"])
        self.assertNotIn("google-github-actions/auth", (root / ".github/workflows/generate-frontend-config.yml").read_text())

        development = workflow("deploy-dev.yml")
        production = workflow("promote-production.yml")
        self.assertIn("frontend-config-generate", development["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend-config-generate", production["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend-config-only", development["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend-config-only", production["on"]["workflow_dispatch"]["inputs"]["operation"]["options"])
        self.assertIn("frontend_config_artifact_id", development["on"]["workflow_dispatch"]["inputs"])
        self.assertIn("frontend_config_artifact_id", production["on"]["workflow_dispatch"]["inputs"])
        for wrapper, environment in ((development, "dev"), (production, "prod")):
            generate_job = wrapper["jobs"]["frontend-config-generate"]
            self.assertEqual(generate_job["if"], "github.ref == 'refs/heads/develop' && inputs.operation == 'frontend-config-generate'")
            self.assertEqual(generate_job["permissions"], {"contents": "read"})
            self.assertEqual(generate_job["uses"], "./.github/workflows/generate-frontend-config.yml")
            self.assertEqual(generate_job["with"], {"environment": environment})
            self.assertNotIn("secrets", generate_job)
            self.assertNotIn("id-token", str(generate_job))
            self.assertIn("inputs.operation != 'frontend-config-generate'", wrapper["jobs"]["release"]["if"])
        self.assertIn("inputs.operation != 'frontend-config-generate'", development["jobs"]["main-fast-forward-eligible"]["if"])

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
        self.assertEqual(download["uses"], f"actions/download-artifact@{PINNED_DOWNLOADER}")
        self.assertEqual(download["with"]["artifact-ids"], "${{ inputs.frontend_config_artifact_id }}")
        self.assertEqual(download["with"]["run-id"], "${{ steps.inspect.outputs.run_id }}")
        self.assertTrue(download["with"]["merge-multiple"])

    def test_actual_pinned_downloader_and_workflow_shell_publish_selected_bytes(self):
        cd = yaml.load((REPO_ROOT / ".github/workflows/cd.yml").read_text(), Loader=yaml.BaseLoader)
        steps = cd["jobs"]["frontend-config-only"]["steps"]
        checkout_validation = next(step for step in steps if step.get("name") == "Validate executor checkout")
        inspect = next(step for step in steps if step.get("name") == "Inspect exact selected artifact and successful generator run")
        download = next(step for step in steps if step.get("uses", "").startswith("actions/download-artifact@"))
        validate = next(step for step in steps if step.get("name") == "Validate selected artifact metadata and public JSON bytes")
        publish = next(step for step in steps if step.get("name") == "Publish only the selected public config and verify exact readback")

        source_fixture = REPO_ROOT / "scripts/fixtures/actions-download-artifact-d3f86a106a0bac45b974a628896c90dbdf5c8093/src/download-artifact.ts"
        self.assertEqual(hashlib.sha256(source_fixture.read_bytes()).hexdigest(), PINNED_DOWNLOADER_SOURCE_SHA256)
        self.assertEqual(download["uses"], f"actions/download-artifact@{PINNED_DOWNLOADER}")
        self.assertTrue(download["with"]["merge-multiple"])
        current_checkout_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, text=True).strip()
        source_a = SOURCE_SHA
        self.assertNotEqual(current_checkout_sha, source_a)
        config_a = b'{"schema_version":1,"api_url":"https://api-a.example.test","auth_url":"https://auth-a.example.test"}\n'
        config_b = b'{"schema_version":1,"api_url":"https://api-b.example.test","auth_url":"https://auth-b.example.test"}\n'

        with tempfile.TemporaryDirectory() as temp:
            case = offline_case(Path(temp))
            checkout_b = case["root"] / "checkout-b-frontend-config.json"
            checkout_b.write_bytes(config_b)
            self.assertNotEqual(source_a, current_checkout_sha)
            source_a_path = case["root"] / "source-a-frontend-config.json"
            source_a_path.write_bytes(config_a)
            generated_bundle = case["root"] / "prepared-artifact-a"
            artifacts.prepare_artifact("dev", source_a, source_a, source_a_path, generated_bundle)
            selected_bundle = case["bundles"] / str(case["artifact_id"])
            selected_bundle.mkdir()
            for entry in generated_bundle.iterdir():
                selected_bundle.joinpath(entry.name).write_bytes(entry.read_bytes())

            env = case["env"].copy()
            Path(env["GITHUB_OUTPUT"]).write_text("")
            env["EXECUTOR_SHA"] = current_checkout_sha
            checkout = workflow_run(checkout_validation, env)
            self.assertEqual(checkout.returncode, 0, checkout.stderr)
            inspected = workflow_run(inspect, env)
            self.assertEqual(inspected.returncode, 0, inspected.stderr)
            outputs = dict(line.split("=", 1) for line in Path(env["GITHUB_OUTPUT"]).read_text().splitlines())
            self.assertEqual(outputs, {"run_id": str(case["run_id"]), "source_sha": source_a})

            env.update({
                "INPUT_ARTIFACT_IDS": str(case["artifact_id"]),
                "INPUT_RUN_ID": outputs["run_id"],
                "INPUT_GITHUB_TOKEN": "synthetic-token",
                "INPUT_REPOSITORY": env["GITHUB_REPOSITORY"],
                "INPUT_PATH": str(case["runner_temp"] / "frontend-config"),
                "INPUT_MERGE_MULTIPLE": str(download["with"]["merge-multiple"]).lower(),
            })
            action = subprocess.run(["node", str(REPO_ROOT / "scripts/fixtures/lwc370_download_artifact_probe.cjs")],
                                    cwd=REPO_ROOT, env=env, capture_output=True, text=True)
            self.assertEqual(action.returncode, 0, action.stderr)
            download_events = [json.loads(line) for line in case["downloads_log"].read_text().splitlines()]
            self.assertEqual(download_events[0]["workflowRunId"], case["run_id"])
            self.assertTrue(download_events[0]["latest"])
            self.assertEqual([event["id"] for event in download_events if event["op"] == "download"], [case["artifact_id"]])
            self.assertEqual(next(event for event in download_events if event["op"] == "download")["path"],
                             str(case["runner_temp"] / "frontend-config"))

            env["SOURCE_SHA"] = outputs["source_sha"]
            validated = workflow_run(validate, env)
            self.assertEqual(validated.returncode, 0, validated.stderr)
            config_sha = Path(env["GITHUB_OUTPUT"]).read_text().split("config_sha256=", 1)[1].splitlines()[0]
            env.update({
                "GENERATION_RUN_ID": outputs["run_id"],
                "CONFIG_SHA256": config_sha,
            })
            published = workflow_run(publish, env)
            self.assertEqual(published.returncode, 0, published.stderr)
            object_path = case["object_store"] / "llm-wiki-frontend-config-dev/frontend-config.json"
            self.assertEqual(object_path.read_bytes(), config_a)
            self.assertNotEqual(object_path.read_bytes(), checkout_b.read_bytes())
            evidence = json.loads((case["runner_temp"] / "frontend-config-publish-evidence.json").read_text())
            self.assertEqual(evidence["artifact_id"], str(case["artifact_id"]))
            self.assertEqual(evidence["generation_run_id"], str(case["run_id"]))
            self.assertEqual(evidence["source_sha"], source_a)
            self.assertEqual(evidence["environment"], "dev")
            self.assertEqual(evidence["config_sha256"], hashlib.sha256(config_a).hexdigest())
            mutations = [json.loads(line) for line in case["gcloud_log"].read_text().splitlines()]
            self.assertEqual(mutations, [{"op": "publish", "uri": "gs://llm-wiki-frontend-config-dev/frontend-config.json"}])

    def test_workflow_rejects_wrong_environment_and_missing_artifact_before_mutation(self):
        cd = yaml.load((REPO_ROOT / ".github/workflows/cd.yml").read_text(), Loader=yaml.BaseLoader)
        steps = cd["jobs"]["frontend-config-only"]["steps"]
        inspect = next(step for step in steps if step.get("name") == "Inspect exact selected artifact and successful generator run")
        download = next(step for step in steps if step.get("uses", "").startswith("actions/download-artifact@"))
        validate = next(step for step in steps if step.get("name") == "Validate selected artifact metadata and public JSON bytes")

        with tempfile.TemporaryDirectory() as temp:
            case = offline_case(Path(temp))
            env = case["env"].copy()
            Path(env["GITHUB_OUTPUT"]).write_text("")
            env["TARGET_ENVIRONMENT"] = "prod"
            wrong_environment = workflow_run(inspect, env)
            self.assertNotEqual(wrong_environment.returncode, 0)
            self.assertNotEqual(env["GITHUB_OUTPUT"], "")
            self.assertFalse(case["downloads_log"].exists())
            self.assertFalse(case["gcloud_log"].exists())

        with tempfile.TemporaryDirectory() as temp:
            case = offline_case(Path(temp), selected_artifact=False)
            env = case["env"].copy()
            Path(env["GITHUB_OUTPUT"]).write_text("")
            inspected = workflow_run(inspect, env)
            self.assertEqual(inspected.returncode, 0, inspected.stderr)
            outputs = dict(line.split("=", 1) for line in Path(env["GITHUB_OUTPUT"]).read_text().splitlines())
            env.update({
                "INPUT_ARTIFACT_IDS": str(case["artifact_id"]),
                "INPUT_RUN_ID": outputs["run_id"],
                "INPUT_GITHUB_TOKEN": "synthetic-token",
                "INPUT_REPOSITORY": env["GITHUB_REPOSITORY"],
                "INPUT_PATH": str(case["runner_temp"] / "frontend-config"),
                "INPUT_MERGE_MULTIPLE": str(download["with"]["merge-multiple"]).lower(),
            })
            missing = subprocess.run(["node", str(REPO_ROOT / "scripts/fixtures/lwc370_download_artifact_probe.cjs")],
                                     cwd=REPO_ROOT, env=env, capture_output=True, text=True)
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("None of the provided artifact IDs were found", missing.stderr)
            env["SOURCE_SHA"] = outputs["source_sha"]
            validation = workflow_run(validate, env)
            self.assertNotEqual(validation.returncode, 0)
            self.assertFalse(case["gcloud_log"].exists())


if __name__ == "__main__":
    unittest.main()
