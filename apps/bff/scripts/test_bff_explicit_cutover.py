#!/usr/bin/env python3
"""BFF readback and cutover safety tests against the shared CD contract."""

import json
import os
import shutil
import sys
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[3]
SOURCE_SHA = "a" * 40
IMAGE = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff@sha256:" + "d" * 64


class SharedCDContractTest(unittest.TestCase):
    def normalized(self, environment="development"):
        with tempfile.TemporaryDirectory() as directory:
            projection = Path(directory) / "bff.json"
            projection.write_text(json.dumps({
                "schema_version": 1,
                "environment": "dev" if environment == "development" else "prod",
                "pipeline_cooldown_seconds": 600 if environment == "development" else 3600,
            }))
            result = subprocess.run(
                [
                    "go",
                    "run",
                    "./cmd/deploy_config",
                    "--environment",
                    environment,
                    "--config",
                    f"../../deploy/environments/{environment}.yaml",
                    "--components",
                    "bff",
                    "--bff-config",
                    str(projection),
                ],
                cwd=REPO_ROOT / "apps/bff",
                capture_output=True,
                text=True,
                check=True,
            )
        return json.loads(result.stdout)

    def fake_provider(self, directory, normalized, image, account):
        bff = normalized["bff"]
        env = [
            {"name": "GCP_PROJECT", "value": normalized["gcp"]["project_id"]},
            {"name": "BUCKET", "value": bff["bucket"]},
            {"name": "FIRESTORE_DATABASE_ID", "value": bff["firestore_database_id"]},
            {"name": "PIPELINE_JOB_URL", "value": bff["pipeline_job_url"]},
            {"name": "ALLOWED_ORIGINS", "value": ",".join(bff["allowed_origins"])},
            {"name": "AUTH_SERVICE_URL", "value": bff["auth_service_url"]},
            {"name": "QUERY_STAGE_CONFIG_PATH", "value": normalized["query_config"]["runtime_path"]},
            {"name": "DEV_JWT", "value": "false"},
            {"name": "LWC_SOURCE_COMMIT", "value": SOURCE_SHA},
            {
                "name": "JWT_SECRET",
                "value": "super-secret-value",
                "valueSource": {"secretKeyRef": {"secret": bff["secret_references"]["jwt"], "version": "latest"}},
            },
            {
                "name": "DEEPSEEK_API_KEY",
                "value": "another-secret-value",
                "valueSource": {"secretKeyRef": {"secret": bff["secret_references"]["deepseek_api_key"], "version": "latest"}},
            },
        ]
        annotations = {
            "run.googleapis.com/network-interfaces": json.dumps([{"network": bff["network"], "subnetwork": bff["subnet"]}]),
            "run.googleapis.com/vpc-access-egress": bff["vpc_egress"],
            "autoscaling.knative.dev/maxScale": str(bff["max_instances"]),
        }
        revision = {
            "metadata": {"annotations": annotations},
            "spec": {"serviceAccountName": account, "containers": [{"image": image, "env": env}]},
            "status": {"imageDigest": image, "conditions": [{"type": "Ready", "status": "True"}]},
        }
        service = {
            "metadata": {"annotations": {"run.googleapis.com/ingress": bff["ingress"]}},
            "spec": {"template": {"metadata": {"annotations": annotations}, "spec": {"serviceAccountName": account, "containers": [{"env": env}]}}},
            "status": {"traffic": [{"revisionName": "llm-wiki-bff-new", "percent": 100}]},
        }
        fake = textwrap.dedent(
            """
            #!/usr/bin/env python3
            import os
            import sys
            args = sys.argv[1:]
            if args[:3] == ["run", "services", "describe"]:
                if any(arg.startswith("value(") for arg in args):
                    print("llm-wiki-bff-new")
                else:
                    print(os.environ["FAKE_SERVICE_JSON"])
            elif args[:3] == ["run", "revisions", "describe"]:
                print(os.environ["FAKE_REVISION_JSON"])
            else:
                raise SystemExit(2)
            """
        ).lstrip()
        path = directory / "gcloud"
        path.write_text(fake)
        path.chmod(0o755)
        return service, revision

    def fixture(self, journal):
        directory = Path(tempfile.mkdtemp(prefix="lwc-306-bff-"))
        normalized = self.normalized()
        image_dir = directory / "artifacts" / "images"
        image_dir.mkdir(parents=True)
        (image_dir / f"bff-image-{SOURCE_SHA}.txt").write_text(IMAGE)
        (directory / "plan.json").write_text(json.dumps({"normalized": normalized}))
        journal_data = {
            "schema": "lwc-306-mutation-journal-v1", "order": ["bff"],
            "components": {} if not journal else {"bff": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}},
        }
        (directory / "artifacts" / "journal.json").write_text(json.dumps(journal_data))
        service, revision = self.fake_provider(
            directory,
            normalized,
            IMAGE,
            "wrong@llm-wiki-cloud.iam.gserviceaccount.com",
        )
        env = {
            **os.environ,
            "PATH": f"{directory}:{os.environ['PATH']}",
            "ENVIRONMENT": "development",
            "SOURCE_REF": "develop",
            "SOURCE_SHA": SOURCE_SHA,
            "CONFIG_PATH": "deploy/environments/development.yaml",
            "COMPONENTS": "bff",
            "GITHUB_REF": "refs/heads/develop",
            "GITHUB_REF_NAME": "develop",
            "PLAN_PATH": str(directory / "plan.json"),
            "JOURNAL_PATH": str(directory / "artifacts" / "journal.json"),
            "ARTIFACT_DIR": str(directory / "artifacts"),
            "EVIDENCE_PATH": str(directory / "artifacts" / "readback.json"),
            "FINAL_EVIDENCE_PATH": str(directory / "artifacts" / "evidence.json"),
            "ROLLBACK_RESULT_PATH": str(directory / "artifacts" / "rollback-result.json"),
            "FAKE_SERVICE_JSON": json.dumps(service),
            "FAKE_REVISION_JSON": json.dumps(revision),
        }
        return directory, env

    def test_bff_readback_rejects_runtime_mismatch_and_redacts_secrets(self):
        directory, env = self.fixture([])
        try:
            result = subprocess.run(["bash", str(REPO_ROOT / "deploy/cd.sh"), "reconcile"], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            readback = json.loads((directory / "artifacts" / "readback.json").read_text())
            self.assertEqual(readback["result"], "unknown")
            self.assertEqual(readback["components"][0]["component"], "bff")
            self.assertEqual(readback["components"][0]["result"], "failed")
            self.assertFalse(readback["provider_readback"])
            result = subprocess.run(["bash", str(REPO_ROOT / "deploy/cd.sh"), "evidence"], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            evidence = (directory / "artifacts" / "evidence.json").read_text()
            self.assertNotIn("super-secret-value", evidence)
            self.assertNotIn("another-secret-value", evidence)
            self.assertIn("no automatic provider retry", evidence)
        finally:
            shutil.rmtree(directory, ignore_errors=True)

    def test_bff_partial_result_requires_an_accepted_mutation(self):
        directory, env = self.fixture(["bff"])
        try:
            result = subprocess.run(["bash", str(REPO_ROOT / "deploy/cd.sh"), "reconcile"], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            readback = json.loads((directory / "artifacts" / "readback.json").read_text())
            self.assertEqual(readback["result"], "partial")
            self.assertEqual(readback["mutation_count"], 1)
            self.assertEqual(readback["mutation_components"], ["bff"])
            self.assertFalse(readback["provider_readback"])
        finally:
            shutil.rmtree(directory, ignore_errors=True)

    def test_shared_bff_path_preserves_cutover_safety_boundaries(self):
        workflows = REPO_ROOT / ".github" / "workflows"
        for path, environment, branch in (
            ("deploy-dev.yml", "development", "develop"),
            ("promote-production.yml", "production", "main"),
        ):
            workflow = yaml.safe_load((workflows / path).read_text())
            trigger = workflow.get("on", workflow.get(True, {}))
            self.assertIn("workflow_dispatch", trigger)
            expected_jobs = ["auth-image-diagnostic", "main-fast-forward-eligible", "release"] if path == "deploy-dev.yml" else ["release"]
            self.assertEqual(sorted(workflow["jobs"]), expected_jobs)
            job = workflow["jobs"]["release"]
            expected_release_guard = f"github.ref == 'refs/heads/{branch}'"
            if path == "deploy-dev.yml":
                expected_release_guard += " && inputs.operation != 'diagnose-auth-image'"
            self.assertEqual(job["if"], expected_release_guard)
            self.assertEqual(job["uses"], "./.github/workflows/cd.yml")
            self.assertEqual(job["with"]["environment"], environment)
            self.assertIn("release_tag", trigger["workflow_dispatch"]["inputs"])
            self.assertFalse(trigger["workflow_dispatch"]["inputs"]["release_tag"]["required"])
            if path == "deploy-dev.yml":
                operation = trigger["workflow_dispatch"]["inputs"]["operation"]
                self.assertEqual(operation["default"], "release")
                self.assertEqual(operation["options"], ["release", "config-only", "deploy", "rollback", "reactivate", "tag", "readback", "diagnose-auth-image"])
                self.assertEqual(job["with"]["operation"], "${{ inputs.operation }}")
                self.assertEqual(job["with"]["source_sha"], "${{ inputs.operation == 'release' && (inputs.source_sha || github.sha) || inputs.source_sha }}")
                self.assertEqual(job["with"]["executor_sha"], "${{ github.sha }}")
                self.assertEqual(trigger["workflow_dispatch"]["inputs"]["source_sha"]["default"], "")
                eligibility = workflow["jobs"]["main-fast-forward-eligible"]
                self.assertEqual(eligibility["name"], "main-fast-forward-eligible")
                self.assertEqual(eligibility["needs"], "release")
                self.assertIn("inputs.operation != 'readback'", eligibility["if"])
                self.assertEqual(eligibility["permissions"], {"contents": "read", "actions": "read", "statuses": "write"})
                self.assertIn("${{ github.run_attempt }}", eligibility["steps"][1]["with"]["name"])
                diagnostic = workflow["jobs"]["auth-image-diagnostic"]
                for condition in (
                    "inputs.operation == 'diagnose-auth-image'",
                    "github.ref == 'refs/heads/develop'",
                    "inputs.components == 'auth'",
                    "inputs.release_tag == 'diagnostic-36992147920'",
                    "inputs.artifact_id == 'diagnostic-no-receipt'",
                    "inputs.dev_artifact_id == ''",
                ):
                    self.assertIn(condition, diagnostic["if"])
                self.assertEqual(diagnostic["uses"], "./.github/workflows/cd-auth-image-diagnostic.yml")
                self.assertEqual(diagnostic["permissions"], {"contents": "read", "actions": "read", "id-token": "write"})
                self.assertEqual(diagnostic["with"], {"source_sha": "${{ github.sha }}"})
            else:
                operation = trigger["workflow_dispatch"]["inputs"]["operation"]
                self.assertEqual(operation["default"], "release")
                self.assertEqual(operation["options"], ["release", "config-only"])
                self.assertEqual(job["with"]["source_sha"], "${{ github.sha }}")
                self.assertEqual(job["with"]["executor_sha"], "${{ github.sha }}")

        recovery = yaml.safe_load((workflows / "recover-deployment.yml").read_text())
        trigger = recovery.get("on", recovery.get(True, {}))
        self.assertIn("workflow_dispatch", trigger)
        self.assertEqual(recovery["jobs"]["recovery"]["uses"], "./.github/workflows/cd.yml")
        self.assertEqual(recovery["jobs"]["recovery"]["with"], {
            "environment": "${{ inputs.environment }}", "source_sha": "${{ inputs.source_sha }}",
            "executor_sha": "${{ github.sha }}",
            "components": "${{ inputs.components }}", "release_tag": "${{ inputs.release_tag }}",
            "operation": "${{ inputs.operation }}", "artifact_id": "${{ inputs.artifact_id }}",
        })
        diagnostic_recovery = recovery["jobs"]["auth-image-diagnostic"]
        self.assertEqual(diagnostic_recovery["uses"], "./.github/workflows/cd-auth-image-diagnostic.yml")
        self.assertEqual(diagnostic_recovery["permissions"], {"contents": "read", "actions": "read", "id-token": "write"})
        self.assertEqual(diagnostic_recovery["with"], {"source_sha": "${{ inputs.source_sha }}"})

        diagnostic_workflow = yaml.safe_load((workflows / "cd-auth-image-diagnostic.yml").read_text())
        self.assertEqual(set(diagnostic_workflow["jobs"]), {"auth-image-diagnostic"})
        self.assertEqual(diagnostic_workflow["permissions"], {"contents": "read", "actions": "read", "id-token": "write"})
        self.assertEqual(diagnostic_workflow["jobs"]["auth-image-diagnostic"]["permissions"], diagnostic_recovery["permissions"])
        diagnostic_call = diagnostic_workflow.get("on", diagnostic_workflow.get(True, {}))
        self.assertEqual(diagnostic_call["workflow_call"]["inputs"], {
            "source_sha": {"required": True, "type": "string"},
        })

        shared = yaml.safe_load((workflows / "cd.yml").read_text())
        shared_inputs = shared.get("on", shared.get(True, {}))["workflow_call"]["inputs"]
        self.assertEqual(shared_inputs["executor_sha"], {"required": True, "type": "string"})
        self.assertEqual(shared_inputs["release_tag"], {"required": True, "type": "string"})
        self.assertEqual(shared_inputs["force"], {"type": "boolean", "default": False})
        self.assertEqual(set(shared["jobs"]), {"release", "pipeline-config-only"})
        job = shared["jobs"]["release"]
        self.assertEqual(job["if"], "inputs.operation != 'diagnose-auth-image' && inputs.operation != 'config-only'")
        config_only = shared["jobs"]["pipeline-config-only"]
        self.assertEqual(config_only["if"], "inputs.operation == 'config-only'")
        self.assertEqual(config_only["permissions"], {"contents": "read", "id-token": "write"})
        config_only_delivery = next(step for step in config_only["steps"]
                                    if "pipeline_config_only.py" in step.get("run", ""))
        self.assertNotIn("--image", config_only_delivery["run"])
        self.assertEqual(shared["concurrency"]["cancel-in-progress"], False)
        self.assertEqual(shared["concurrency"]["group"], "lwc-engine-${{ inputs.environment }}")
        steps = job["steps"]
        self.assertEqual(job["env"]["EXECUTOR_SHA"], "${{ inputs.executor_sha }}")
        self.assertEqual(job["env"]["FORCE"], "${{ inputs.force }}")
        self.assertEqual(job["env"]["SOURCE"], "${{ inputs.source_sha }}")
        self.assertEqual(next(step for step in steps if step.get("uses", "").startswith("actions/checkout@"))["with"]["ref"], "${{ inputs.executor_sha }}")
        resume_validation = next(step for step in steps
                                 if step.get("name") == "Validate retained Stage 1 source input")
        self.assertEqual(resume_validation["if"], "inputs.operation == 'release' && inputs.environment == 'development' && inputs.source_sha != github.sha")
        self.assertEqual(resume_validation["env"]["ARTIFACT_ID"], "${{ inputs.artifact_id }}")
        self.assertIn('test -n "$ARTIFACT_ID"', resume_validation["run"])
        prepare = next(i for i, step in enumerate(steps) if step.get("with", {}).get("operation") == "prepare")
        barrier = next(i for i, step in enumerate(steps) if step.get("id") == "ready")
        runtime = next(i for i, step in enumerate(steps) if step.get("with", {}).get("operation") == "runtime")
        self.assertLess(prepare, barrier)
        self.assertLess(barrier, runtime)
        self.assertEqual(steps[barrier]["if"], "inputs.operation == 'release'")
        self.assertEqual(steps[barrier]["with"]["if-no-files-found"], "error")
        self.assertEqual(steps[barrier]["with"]["retention-days"], 90)
        self.assertIn("always()", next(step["if"] for step in steps if step.get("name") == "Retain final result even after failure"))

        engine_tests = subprocess.run(
            [
                sys.executable, "-m", "unittest",
                "test_engine.Acceptance.test_04_order_barrier_and_real_config",
                "test_engine.Acceptance.test_durable_pending_failure_prevents_provider_mutation",
                "test_engine.Acceptance.test_service_partial_update_restores_template_even_with_old_traffic",
                "test_engine.Acceptance.test_hold_service_reactivation_preserves_candidate_and_next_snapshot",
                "test_engine.Acceptance.test_auth_bff_ready_artifact_deploy_updates_traffic_and_strict_readback",
                "test_engine.Acceptance.test_bff_incompatible_receipt_identity_fails_before_provider_mutation",
                "test_engine.Acceptance.test_bff_candidate_readback_failure_compensates_without_unverified_cutover",
            ],
            cwd=REPO_ROOT / "deploy" / "engine" / "tests",
            capture_output=True, text=True, timeout=90,
        )
        self.assertEqual(engine_tests.returncode, 0, engine_tests.stdout + engine_tests.stderr)

        provider = (REPO_ROOT / "deploy" / "engine" / "providers.py").read_text()
        self.assertIn("self.cloud(c, 'services', 'update-traffic'", provider)
        self.assertIn("for alias, deployment in prior['aliases'].items()", provider)
        self.assertNotIn("run jobs execute", provider)

    def test_bff_freezes_and_rolls_back_exact_config_without_auth_google(self):
        directory = Path(tempfile.mkdtemp(prefix="lwc-306-bff-legacy-"))
        try:
            artifacts = directory / "artifacts"
            artifacts.mkdir()
            normalized = self.normalized("production")
            # Query config remains managed independently of the Auth migration.
            normalized["auth"].pop("google", None)
            plan = directory / "plan.json"
            plan.write_text(json.dumps({"normalized": normalized}))
            service_before = REPO_ROOT / "apps/bff/scripts/fixtures/bff-service-before.json"
            revision_before = REPO_ROOT / "apps/bff/scripts/fixtures/bff-revision-before.json"
            revision_data = json.loads(revision_before.read_text())
            revision_data.setdefault("status", {})["conditions"] = [{"type": "Ready", "status": "True"}]
            revision_ready = directory / "revision-ready.json"
            revision_ready.write_text(json.dumps(revision_data))
            fake = textwrap.dedent(
                f"""
                #!/usr/bin/env python3
                import pathlib, sys
                args = sys.argv[1:]
                if args[:3] == ["run", "services", "describe"]:
                    if any(arg.startswith("--format=value(") for arg in args): print("llm-wiki-bff-00001-old")
                    else: print(pathlib.Path({str(service_before)!r}).read_text())
                elif args[:3] == ["run", "revisions", "describe"]:
                    print(pathlib.Path({str(revision_ready)!r}).read_text())
                elif args[:3] == ["run", "services", "update-traffic"]:
                    pass
                else: raise SystemExit(2)
                """
            ).lstrip()
            provider = directory / "gcloud"
            provider.write_text(fake)
            provider.chmod(0o755)
            rollback = artifacts / "rollback.json"
            journal = artifacts / "journal.json"
            env = {
                **os.environ,
                "PATH": f"{directory}:{os.environ['PATH']}", "ENVIRONMENT": "production", "SOURCE_REF": "main",
                "SOURCE_SHA": "a" * 40, "CONFIG_PATH": "deploy/environments/production.yaml", "COMPONENTS": "bff",
                "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(rollback), "JOURNAL_PATH": str(journal),
                "ROLLBACK_RESULT_PATH": str(artifacts / "rollback-result.json"), "ARTIFACT_DIR": str(artifacts),
            }
            frozen = subprocess.run(["bash", str(REPO_ROOT / "deploy/cd.sh"), "freeze"], env=env, text=True, capture_output=True)
            self.assertEqual(frozen.returncode, 0, frozen.stdout + frozen.stderr)
            handle = json.loads(rollback.read_text())["handles"]["bff"]
            self.assertEqual(set(handle.keys()), {"image", "revision", "config_fingerprint", "ready"})
            self.assertEqual(handle['revision'], 'llm-wiki-bff-00001-old')
            self.assertRegex(handle['config_fingerprint'], r'^sha256:[0-9a-f]{64}$')
            self.assertEqual(handle["image"], "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
            journal.write_text(json.dumps({
                "schema": "lwc-306-mutation-journal-v1", "order": ["bff"],
                "components": {"bff": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}},
            }))
            restored = subprocess.run(["bash", str(REPO_ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertEqual(restored.returncode, 0, restored.stdout + restored.stderr)
            rollback_result = json.loads((artifacts / "rollback" / "bff.json").read_text())
            self.assertEqual(rollback_result["result"], "success")
            self.assertEqual(rollback_result["readback"]["image"], handle["image"])
            self.assertEqual(rollback_result["readback"]["revision"], "llm-wiki-bff-00001-old")
        finally:
            shutil.rmtree(directory, ignore_errors=True)

    def test_production_consumes_immutable_dev_receipt_without_rebuilding_bff(self):
        source = (REPO_ROOT / "deploy" / "cd.sh").read_text()
        consume = source[source.index("consume_dev_images()") : source.index("preflight_shared()")]
        self.assertIn("event=workflow_dispatch", consume)
        self.assertIn("head_sha=$\u007bSOURCE_SHA}", consume)
        self.assertIn("gh run download", consume)
        self.assertNotIn("docker build", consume)
        self.assertNotIn("gcloud builds", consume)
        self.assertNotIn(":latest", consume)


    def test_retired_bff_build_entry_fails_closed(self):
        """Legacy direct builds stay rejected; ready artifacts are engine-owned."""
        with tempfile.TemporaryDirectory(prefix="lwc-bff-retired-build-") as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            events = root / "provider-events"
            for tool in ("docker", "gcloud"):
                stub = bin_dir / tool
                stub.write_text(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "with open(os.environ['FAKE_PROVIDER_EVENTS'], 'a') as out:\n"
                    "    out.write(" + repr(tool + " " ) + " + ' '.join(sys.argv[1:]) + '\n')\n"
                )
                stub.chmod(0o755)

            result = subprocess.run(
                ["bash", str(REPO_ROOT / "deploy/components/bff.sh"), "build"],
                env={
                    **os.environ,
                    "ROOT": str(REPO_ROOT),
                    "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_PROVIDER_EVENTS": str(events),
                },
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("BFF builds must be prepared by the deployment engine", result.stderr)
            self.assertFalse(events.exists(), "retired shortcut must fail before provider tools run")


if __name__ == "__main__":
    unittest.main()
