#!/usr/bin/env python3
"""BFF readback and cutover safety tests against the shared CD contract."""

import json
import os
import shutil
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
    def normalized(self):
        result = subprocess.run(
            [
                "go",
                "run",
                "./cmd/deploy_config",
                "--environment",
                "development",
                "--config",
                "../../deploy/environments/development.yaml",
                "--components",
                "bff",
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
        (directory / "artifacts" / "journal.json").write_text(json.dumps(journal))
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
            self.assertEqual(readback["result"], "failed")
            self.assertEqual(readback["mutation_count"], 0)
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
        source = (REPO_ROOT / "deploy" / "cd.sh").read_text()
        shared = (REPO_ROOT / ".github" / "workflows" / "cd.yml").read_text()
        for path in ("deploy-dev.yml", "promote-production.yml"):
            workflow = yaml.safe_load((REPO_ROOT / ".github" / "workflows" / path).read_text())
            triggers = workflow.get("on", workflow.get(True, {}))
            self.assertIn("workflow_dispatch", triggers)
            self.assertIn("./.github/workflows/cd.yml", (REPO_ROOT / ".github" / "workflows" / path).read_text())
        self.assertLess(shared.index("Upload durable rollback artifact"), shared.index("Mutate selected components"))
        self.assertIn("if: steps.rollback_upload.outcome == 'success'", shared)
        self.assertIn("event=push", source)
        self.assertIn("event=workflow_dispatch", source)
        self.assertIn("gcloud run services update-traffic", source)
        self.assertNotIn("run jobs execute", source)
        self.assertNotIn("vercel alias set", source)

    def test_production_consumes_immutable_dev_receipt_without_rebuilding_bff(self):
        source = (REPO_ROOT / "deploy" / "cd.sh").read_text()
        consume = source[source.index("consume_dev_images()") : source.index("image_for()")]
        self.assertIn("event=workflow_dispatch", consume)
        self.assertIn("head_sha=$\u007bSOURCE_SHA}", consume)
        self.assertIn("gh run download", consume)
        self.assertNotIn("docker build", consume)
        self.assertNotIn("gcloud builds", consume)
        image_for = source[source.index("image_for()") : source.index("service_env_args()")]
        self.assertNotIn(":latest", image_for)


if __name__ == "__main__":
    unittest.main()
