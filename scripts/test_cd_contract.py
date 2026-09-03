#!/usr/bin/env python3
"""LWC-306 characterization and regression tests for the CD contract."""

import json
import os
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
CONFIG_DIR = ROOT / "deploy" / "environments"


class CDContractTests(unittest.TestCase):
    def load_config(self, environment):
        path = CONFIG_DIR / f"{environment}.yaml"
        self.assertTrue(path.is_file(), f"missing {path}")
        return yaml.safe_load(path.read_text())

    def normalized(self, environment):
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
                "auth,bff,worker,frontend",
            ],
            cwd=ROOT / "apps" / "bff",
            text=True,
            capture_output=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_environment_files_have_equal_complete_shape_and_normalize(self):
        development = self.load_config("development")
        production = self.load_config("production")
        self.assertEqual(set(development), set(production))
        self.assertEqual(
            set(development),
            {"gcp", "auth", "bff", "worker", "frontend"},
        )
        self.assertEqual(
            self.normalized("development")["query_config"],
            self.normalized("production")["query_config"],
        )

    def test_fixed_workflows_delegate_to_one_orchestrator(self):
        development = (ROOT / ".github/workflows/deploy-dev.yml").read_text()
        production = (ROOT / ".github/workflows/promote-production.yml").read_text()
        for source, branch, environment, config in [
            (development, "develop", "Development", "development"),
            (production, "main", "Production", "production"),
        ]:
            self.assertIn("uses: ./.github/workflows/cd.yml", source)
            self.assertIn(f"environment: {environment}", source)
            self.assertIn(f"source_ref: {branch}", source)
            self.assertIn(f"config_path: deploy/environments/{config}.yaml", source)
            self.assertNotIn("inputs.config", source)
            self.assertNotIn("inputs.environment", source)
            self.assertNotIn("inputs.ref", source)
            self.assertNotIn("inputs.source_sha", source)
            self.assertIn(f"if: github.ref == 'refs/heads/{branch}'", source)
            self.assertIn("source_sha: ${{ github.sha }}", source)

    def test_orchestrator_validates_before_environment_and_gates_mutation_on_rollback_upload(self):
        source = (ROOT / ".github/workflows/cd.yml").read_text()
        plan = source.index("  plan:")
        mutation = source.index("  mutate:")
        self.assertLess(plan, mutation)
        self.assertNotIn("environment:", source[plan:mutation])
        self.assertIn("needs: plan", source[mutation:])
        rollback_upload = source.index("id: rollback_upload")
        first_mutation = source.index("run: bash deploy/cd.sh mutate", mutation)
        self.assertLess(rollback_upload, first_mutation)
        self.assertIn("if: steps.rollback_upload.outcome == 'success'", source)
        self.assertNotIn("run jobs execute", source)

    def test_no_legacy_workflow_owns_deployment_literals(self):
        workflows = sorted((ROOT / ".github/workflows").glob("*.yml"))
        self.assertEqual(
            {path.name for path in workflows},
            {"ci.yml", "cd.yml", "deploy-dev.yml", "promote-production.yml"},
        )
        all_source = "\n".join(path.read_text() for path in workflows)
        for literal in (
            "QUERY_STAGE_CONFIG_PATH:",
            "QUERY_STAGE_CONFIG_REVISION:",
            "QUERY_STAGE_CONFIG_DIGEST:",
        ):
            self.assertNotIn(literal, all_source)

    def test_entry_workflows_are_dispatch_only_and_production_consumes_dispatch_receipt(self):
        development = (ROOT / ".github/workflows/deploy-dev.yml").read_text()
        production = (ROOT / ".github/workflows/promote-production.yml").read_text()
        self.assertNotIn("\n  push:", development)
        self.assertNotIn("\n  push:", production)
        source = (ROOT / "deploy/cd.sh").read_text()
        consume = source[source.index("consume_dev_images()"):source.index("image_for()")]
        self.assertIn("event=workflow_dispatch", consume)
        self.assertNotIn("event=push", consume)
        self.assertIn("head_sha=${SOURCE_SHA}", consume)
        self.assertIn("branch=develop", consume)
        self.assertIn('gh run download "$id"', consume)
        self.assertIn("cd-images-$SOURCE_SHA", consume)

    def test_cd_uses_vercel_build_and_bounded_rest_alias_mutation(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        self.assertIn("vercel build", source)
        self.assertIn("--prebuilt", source)
        self.assertIn("/v2/deployments/", source)
        self.assertNotIn("vercel alias set", source)

    def test_mutation_journal_records_only_accepted_provider_mutations(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        mutate = source[source.index("mutate() {"):source.index("reconcile() {")]
        self.assertNotIn("component_mark", source)
        self.assertNotIn("mutation_accepted", mutate)
        self.assertEqual(source.count("mutation_accepted "), 3)
        service = source[source.index("deploy_service()"):source.index("verify_service()")]
        worker = source[source.index("update_worker()"):source.index("verify_worker()")]
        frontend = source[source.index("promote_frontend()"):source.index("mutate()")]
        self.assertEqual(worker.count("mutation_accepted"), 1)
        self.assertLess(service.index("gcloud run deploy"), service.index("mutation_accepted"))
        self.assertLess(worker.index("gcloud run jobs update"), worker.index("mutation_accepted"))
        self.assertLess(frontend.index("vercel build"), frontend.index("mutation_accepted"))
        self.assertLess(frontend.index("vercel deploy --prebuilt"), frontend.index("mutation_accepted"))
        self.assertLess(mutate.index("build_cloud_image"), mutate.index("deploy_service"))
        self.assertLess(mutate.index("build_worker_image"), mutate.index("update_worker"))
        self.assertIn("mutation_accepted", source)

    def test_mutation_accepted_is_exact_once_and_deterministic(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        helper = source[:source.index("\nvalidate_inputs()")]
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "journal.json"
            env = {**os.environ, "JOURNAL_PATH": str(journal)}
            accepted = subprocess.run(
                ["bash", "-c", helper + "\nmutation_accepted auth\nmutation_accepted worker"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(accepted.returncode, 0, accepted.stderr)
            self.assertEqual(json.loads(journal.read_text()), ["auth", "worker"])
            duplicate = subprocess.run(
                ["bash", "-c", helper + "\nmutation_accepted auth"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(duplicate.returncode, 0)
            self.assertEqual(json.loads(journal.read_text()), ["auth", "worker"])

    def test_readback_and_rollback_are_truthful_and_complete(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        self.assertNotIn("provider_readback:true", source)
        self.assertNotIn("verified:true", source)
        self.assertIn("service_account", source)
        self.assertIn("secret_references", source)
        self.assertIn("gcloud run jobs replace", source[source.index("rollback_worker()"):])
        self.assertIn("handles.worker.definition", source[source.index("rollback_worker()"):])
        self.assertIn("rollback_verified", source)

    def test_service_readback_compares_the_allowlisted_runtime_definition(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        verify = source[source.index("service_expected()"):source.index("update_worker()")]
        for marker in (
            "normalize_service_readback",
            "runtime_service_account",
            "secret_references",
            "allowed_origins",
            "allowed_hosts",
            "vpc_egress",
            "network",
            "subnet",
            "max_instances",
            "component_config",
        ):
            self.assertIn(marker, verify, f"service read-back is missing {marker}")

    def test_worker_rollback_uses_the_frozen_definition_and_exact_readback(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        rollback = source[source.index("rollback_worker()") :]
        for marker in (
            "worker.definition",
            "normalize_worker_definition",
            "verify_worker_definition",
            "gcloud run jobs replace",
            "ROLLBACK_COMPONENT_READBACK",
            "secret_references",
        ):
            self.assertIn(marker, rollback, f"Worker rollback is missing {marker}")
        self.assertNotIn("--clear-volume-mounts", rollback)
        self.assertNotIn('update-env-vars \"BUCKET=$(jq', rollback)

    def test_evidence_reports_actual_mutation_and_rollback_state(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        evidence = source[source.index("reconcile()") :]
        for marker in (
            "mutation_count",
            "mutation_components",
            "rollback_attempted",
            "rollback_result",
            "partial",
            "unknown",
            "next_action",
        ):
            self.assertIn(marker, evidence, f"evidence is missing {marker}")
        self.assertNotIn("provider_readback:($result == \"success\")", source)
        self.assertNotIn("rollback_verified:$verified", source[source.index("\nevidence() {") :])

    def test_evidence_redacts_secret_values_and_never_serializes_provider_env(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        self.assertIn("redact", source)
        self.assertIn("secret_references", source)
        self.assertIn("valueSource", source)
        self.assertIn("<redacted>", source)
        final = source[source.index("\nevidence() {") :]
        self.assertNotIn(".value", final)

    def test_mutation_journal_is_written_after_accepted_provider_commands(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        for function, command in (
            ("deploy_service()", "gcloud run deploy"),
            ("update_worker()", "gcloud run jobs update"),
            ("promote_frontend()", "vercel deploy --prebuilt"),
        ):
            body = source[source.index(function) : source.index("\n}", source.index(function))]
            self.assertLess(body.index(command), body.index("mutation_accepted"))

    def test_fake_service_readback_rejects_runtime_mismatch_and_redacts_secret_values(self):
        config = self.load_config("development")
        root = Path(tempfile.mkdtemp(prefix="lwc-306-service-"))
        try:
            bin_dir = root / "bin"
            bin_dir.mkdir()
            image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth@sha256:" + "a" * 64
            plan = {
                "normalized": {
                    "selected_components": ["auth"],
                    "gcp": config["gcp"],
                    "auth": config["auth"],
                    "query_config": {"runtime_path": "/app/configs/query/dev/query-dev-2026-08-31.1.json"},
                    "evidence": {"config_fingerprint": "sha256:fixture"},
                }
            }
            (root / "plan.json").write_text(json.dumps(plan))
            (root / "artifacts/images").mkdir(parents=True)
            (root / "artifacts/images" / "auth-image-0123456789abcdef0123456789abcdef01234567.txt").write_text(image)
            # Keep the fake transport local and deterministic; it never contacts a provider.
            fake = textwrap.dedent(
                f"""
                #!/usr/bin/env python3
                import json, sys
                args = sys.argv[1:]
                image = {image!r}
                env = [
                    {{"name":"GCP_PROJECT","value":"llm-wiki-cloud"}},
                    {{"name":"FIRESTORE_DATABASE_ID","value":"llm-wiki-cloud-dev"}},
                    {{"name":"ALLOWED_ORIGINS","value":",".join({config['auth']['allowed_origins']!r})}},
                    {{"name":"ALLOWED_HOSTS","value":",".join({config['auth']['allowed_hosts']!r})}},
                    {{"name":"DEV_JWT","value":"false"}},
                    {{"name":"LWC_SOURCE_COMMIT","value":"0123456789abcdef0123456789abcdef01234567"}},
                    {{"name":"JWT_SECRET","value":"super-secret-value","valueSource":{{"secretKeyRef":{{"secret":"jwt-secret-dev","version":"latest"}}}}}},
                ]
                revision = {{"metadata":{{"annotations":{{"run.googleapis.com/network-interfaces":json.dumps([{{"network":"default","subnetwork":"default"}}]),"run.googleapis.com/vpc-access-egress":"private-ranges-only"}}}},"spec":{{"serviceAccountName":"wrong@llm-wiki-cloud.iam.gserviceaccount.com","containers":[{{"image":image,"env":env}}]}},"status":{{"imageDigest":image,"conditions":[{{"type":"Ready","status":"True"}}]}}}}
                service = {{"metadata":{{"annotations":{{"run.googleapis.com/ingress":"all"}}}},"spec":{{"template":{{"metadata":{{"annotations":{{"run.googleapis.com/network-interfaces":json.dumps([{{"network":"default","subnetwork":"default"}}]),"run.googleapis.com/vpc-access-egress":"private-ranges-only"}}}},"spec":{{"serviceAccountName":"wrong@llm-wiki-cloud.iam.gserviceaccount.com","containers":[{{"env":env}}]}}}}}},"status":{{"traffic":[{{"revisionName":"auth-new","percent":100}}]}}}}
                if args[:3] == ["run", "services", "describe"]:
                    if any(arg.startswith("value(") for arg in args): print("auth-new")
                    else: print(json.dumps(service))
                elif args[:3] == ["run", "revisions", "describe"]: print(json.dumps(revision))
                else: raise SystemExit(2)
                """
            ).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT":"development", "SOURCE_REF":"develop", "SOURCE_SHA":"0123456789abcdef0123456789abcdef01234567", "CONFIG_PATH":"deploy/environments/development.yaml", "COMPONENTS":"auth", "GITHUB_REF":"refs/heads/develop", "GITHUB_REF_NAME":"develop", "PLAN_PATH":str(root / "plan.json"), "JOURNAL_PATH":str(root / "artifacts/journal.json"), "ARTIFACT_DIR":str(root / "artifacts"), "EVIDENCE_PATH":str(root / "artifacts/readback.json"), "FINAL_EVIDENCE_PATH":str(root / "artifacts/evidence.json")}
            (root / "artifacts/journal.json").write_text("[]\n")
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "reconcile"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            evidence = json.loads((root / "artifacts/readback.json").read_text())
            self.assertEqual(evidence["result"], "failed")
            self.assertFalse(evidence["provider_readback"])
            final = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "evidence"], env=env, text=True, capture_output=True)
            self.assertEqual(final.returncode, 0, final.stderr)
            document = (root / "artifacts/evidence.json").read_text()
            self.assertNotIn("super-secret-value", document)
            self.assertIn("no automatic provider retry", document)
        finally:
            import shutil
            shutil.rmtree(root, ignore_errors=True)

    def test_fake_worker_rollback_restores_full_definition_and_rejects_mismatch(self):
        root = Path(tempfile.mkdtemp(prefix="lwc-306-worker-"))
        try:
            bin_dir = root / "bin"
            bin_dir.mkdir()
            container = {
                "image": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64,
                "env": [
                    {"name": "BUCKET", "value": "llm-wiki-data-dev"},
                    {"name": "PIPELINE_JOB_NAME", "value": "olw-pipeline-dev"},
                    {"name": "PIPELINE_JOB_LOCATION", "value": "asia-east1"},
                    {"name": "DEEPSEEK_API_KEY", "valueFrom": {"secretKeyRef": {"name": "deepseek-apikey", "key": "latest"}}},
                ],
                "args": ["run", "--auto-approve"],
                "volumeMounts": [{"name": "cache", "mountPath": "/cache"}],
            }
            definition = {
                "apiVersion": "run.googleapis.com/v1",
                "kind": "Job",
                "metadata": {"name": "olw-pipeline", "generation": 9},
                "spec": {
                    "template": {
                        "spec": {
                            "template": {
                                "spec": {
                                    "serviceAccountName": "lwc-pipeline-dev@llm-wiki-cloud.iam.gserviceaccount.com",
                                    "containers": [container],
                                    "volumes": [{"name": "cache", "emptyDir": {}}],
                                }
                            }
                        }
                    }
                },
                "status": {},
            }
            state = root / "job.json"
            state.write_text(json.dumps(definition))
            plan = {"normalized": {"selected_components":["worker"], "gcp":{"project_id":"llm-wiki-cloud","region":"asia-east1"}, "worker":{"job_name":"olw-pipeline-dev","location":"asia-east1"}, "evidence":{"config_fingerprint":"sha256:fixture"}}}
            (root / "plan.json").write_text(json.dumps(plan))
            fake = textwrap.dedent(
                """
                #!/usr/bin/env python3
                import json, os, sys
                from pathlib import Path
                args = sys.argv[1:]
                state = Path(os.environ["FAKE_JOB_STATE"])
                if args[:3] == ["run", "jobs", "describe"]:
                    print(state.read_text(), end="")
                elif args[:3] == ["run", "jobs", "replace"]:
                    source = Path(args[3])
                    value = json.loads(source.read_text())
                    if os.environ.get("FAKE_ROLLBACK_MISMATCH") == "1":
                        value["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["args"] = ["wrong"]
                    state.write_text(json.dumps(value))
                else: raise SystemExit(2)
                """
            ).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT":"development", "SOURCE_REF":"develop", "SOURCE_SHA":"0123456789abcdef0123456789abcdef01234567", "CONFIG_PATH":"deploy/environments/development.yaml", "COMPONENTS":"worker", "GITHUB_REF":"refs/heads/develop", "GITHUB_REF_NAME":"develop", "PLAN_PATH":str(root / "plan.json"), "ROLLBACK_PATH":str(root / "rollback.json"), "JOURNAL_PATH":str(root / "journal.json"), "ROLLBACK_RESULT_PATH":str(root / "rollback-result.json"), "ARTIFACT_DIR":str(root / "artifacts"), "FAKE_JOB_STATE":str(state)}
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "freeze"], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            frozen = json.loads((root / "rollback.json").read_text())["handles"]["worker"]["definition"]
            self.assertEqual(frozen["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["args"], definition["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["args"])
            self.assertEqual(len(frozen["spec"]["template"]["spec"]["template"]["spec"]["volumes"]), 1)
            self.assertIn("valueSource", json.dumps(frozen))
            state.write_text(json.dumps({**definition, "spec": {"template": {"spec": {"template": {"spec": {**definition["spec"]["template"]["spec"]["template"]["spec"], "containers": [{**definition["spec"]["template"]["spec"]["template"]["spec"]["containers"][0], "args":["changed"]}]}}}}}}))
            (root / "journal.json").write_text('["worker"]\n')
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            rollback = json.loads((root / "rollback-result.json").read_text())
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue(rollback["rollback_verified"])
            self.assertEqual(json.loads(state.read_text()), frozen)
            env["FAKE_ROLLBACK_MISMATCH"] = "1"
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            rollback = json.loads((root / "rollback-result.json").read_text())
            self.assertEqual(rollback["result"], "failed")
            self.assertFalse(rollback["rollback_verified"])
        finally:
            import shutil
            shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
