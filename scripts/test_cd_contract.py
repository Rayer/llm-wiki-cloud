#!/usr/bin/env python3
"""LWC-306 characterization and regression tests for the CD contract."""

import json
import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from copy import deepcopy
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
CONFIG_DIR = ROOT / "deploy" / "environments"


def source_bundle(*names):
    paths = [ROOT / "deploy" / "cd.sh", ROOT / "deploy" / "components" / "common.sh"]
    paths.extend(ROOT / "deploy" / "components" / f"{name}.sh" for name in names)
    return "\n".join(path.read_text() for path in paths if path.is_file())


def common_source():
    return (ROOT / "deploy/components/common.sh").read_text()


class CDContractTests(unittest.TestCase):
    def test_validate_inputs_accepts_only_fixed_deployment_config_pairs(self):
        source = ROOT / "deploy/components/common.sh"
        cases = [
            ("Development", "development", "develop", True, None),
            ("Production", "production", "main", True, None),
            ("Production", "development", "develop", False, "deployment and config environments do not match"),
            ("Development", "production", "main", False, "deployment and config environments do not match"),
            ("development", "development", "develop", False, "deployment and config environments do not match"),
            ("production", "production", "main", False, "deployment and config environments do not match"),
            (None, "development", "develop", False, "DEPLOYMENT_ENVIRONMENT is required"),
        ]
        with tempfile.TemporaryDirectory() as directory:
            for index, (deployment, config, ref, valid, message) in enumerate(cases):
                with self.subTest(deployment=deployment, config=config):
                    seam = Path(directory) / str(index)
                    env = {
                        **os.environ,
                        "ROOT": str(ROOT),
                        "ENVIRONMENT": config,
                        "SOURCE_REF": ref,
                        "SOURCE_SHA": "0123456789abcdef0123456789abcdef01234567",
                        "COMPONENTS": "auth,bff,worker,frontend",
                        "CONFIG_PATH": f"deploy/environments/{config}.yaml",
                        "GITHUB_REF": f"refs/heads/{ref}",
                        "GITHUB_REF_NAME": ref,
                        "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                        "PROVIDER_SEAM": str(seam),
                    }
                    if deployment is None:
                        env.pop("DEPLOYMENT_ENVIRONMENT", None)
                    else:
                        env["DEPLOYMENT_ENVIRONMENT"] = deployment
                    result = subprocess.run(
                        [
                            "bash",
                            "-c",
                            f"source {str(source)!r}; validate_inputs; touch \"$PROVIDER_SEAM\"",
                        ],
                        env=env,
                        text=True,
                        capture_output=True,
                    )
                    if valid:
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertTrue(seam.is_file())
                    else:
                        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                        self.assertIn(message, result.stderr)
                        self.assertFalse(seam.exists())

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

    def test_reusable_workflow_secret_forwarding_contract(self):
        expected = {
            "deploy-dev.yml": {
                "job": "deploy",
                "branch": "develop",
                "environment": "Development",
                "config_environment": "development",
                "source_ref": "develop",
                "config_path": "deploy/environments/development.yaml",
            },
            "promote-production.yml": {
                "job": "promote",
                "branch": "main",
                "environment": "Production",
                "config_environment": "production",
                "source_ref": "main",
                "config_path": "deploy/environments/production.yaml",
            },
        }
        permissions = {"contents": "read", "actions": "read", "id-token": "write"}
        shared_workflow = "./.github/workflows/cd.yml"
        secret_allowlist = {
            "WIF_PROVIDER",
            "WIF_SERVICE_ACCOUNT",
            "VERCEL_PROJECT_ID",
            "VERCEL_SCOPE",
            "VERCEL_TEAM_ID",
            "VERCEL_TOKEN",
        }

        def workflow_trigger(workflow):
            return workflow.get("on") if "on" in workflow else workflow.get(True)

        def assert_wrappers(workflows):
            callers = {
                (filename, job_id)
                for filename, workflow in all_workflows.items()
                for job_id, job in workflow["jobs"].items()
                if job.get("uses") == shared_workflow
            }
            self.assertEqual(
                callers,
                {(filename, details["job"]) for filename, details in expected.items()},
            )
            for filename, details in expected.items():
                workflow = workflows[filename]
                self.assertEqual(set(workflow_trigger(workflow)), {"workflow_dispatch"})
                self.assertEqual(set(workflow["jobs"]), {details["job"]})
                job = workflow["jobs"][details["job"]]
                self.assertEqual(
                    job.get("if"),
                    f"github.ref == 'refs/heads/{details['branch']}'",
                )
                self.assertEqual(job.get("uses"), shared_workflow)
                self.assertEqual(job.get("secrets"), "inherit")
                self.assertEqual(job.get("permissions"), permissions)
                self.assertEqual(
                    job.get("with"),
                    {
                        "environment": details["environment"],
                        "config_environment": details["config_environment"],
                        "source_ref": details["source_ref"],
                        "source_sha": "${{ github.sha }}",
                        "config_path": details["config_path"],
                        "components": "${{ inputs.components }}",
                    },
                )
                self.assertNotRegex(json.dumps(workflow), r"\$\{\{\s*secrets\.")

        workflows = {
            filename: yaml.safe_load((ROOT / ".github/workflows" / filename).read_text())
            for filename in expected
        }
        all_workflows = {
            path.name: yaml.safe_load(path.read_text())
            for path in (ROOT / ".github/workflows").glob("*.yml")
        }
        assert_wrappers(workflows)

        missing_inherit = deepcopy(workflows)
        del missing_inherit["deploy-dev.yml"]["jobs"]["deploy"]["secrets"]
        with self.assertRaises(AssertionError):
            assert_wrappers(missing_inherit)

        explicit_map = deepcopy(workflows)
        explicit_map["promote-production.yml"]["jobs"]["promote"]["secrets"] = {
            "WIF_PROVIDER": "${{ secrets.WIF_PROVIDER }}"
        }
        with self.assertRaises(AssertionError):
            assert_wrappers(explicit_map)

        cd_path = ROOT / ".github/workflows/cd.yml"
        cd_source = cd_path.read_text()
        called = yaml.safe_load(cd_source)
        self.assertEqual(
            called["jobs"]["mutate"].get("environment"),
            "${{ inputs.environment }}",
        )
        secret_ref_names = set(re.findall(r"secrets\.([A-Za-z_][A-Za-z0-9_]*)", cd_source))
        self.assertTrue(secret_ref_names)
        self.assertTrue(secret_ref_names <= secret_allowlist)
        secret_consumers = [
            value
            for value in called["jobs"]["mutate"]["env"].values()
            if isinstance(value, str) and "secrets." in value
        ]
        secret_consumers.extend(
            step.get("with", {}).get("workload_identity_provider")
            for step in called["jobs"]["mutate"]["steps"]
            if step.get("name") == "Authenticate to Google Cloud"
        )
        secret_consumers.extend(
            step.get("with", {}).get("service_account")
            for step in called["jobs"]["mutate"]["steps"]
            if step.get("name") == "Authenticate to Google Cloud"
        )
        self.assertEqual(
            {
                re.search(r"secrets\.([A-Za-z_][A-Za-z0-9_]*)", value).group(1)
                for value in secret_consumers
            },
            secret_ref_names,
        )
        for value in secret_consumers:
            self.assertRegex(value, r"^\$\{\{\s*secrets\.[A-Z_]+\s*\}\}$")
        self.assertIn("${{ github.token }}", cd_source)
        self.assertNotRegex(
            cd_source,
            r"\b(?:WIF_PROVIDER|WIF_SERVICE_ACCOUNT|VERCEL_PROJECT_ID|VERCEL_SCOPE|VERCEL_TEAM_ID|VERCEL_TOKEN):(?![ \t]*\$\{\{)[ \t]+",
        )

    def test_reusable_callers_grant_mutation_permissions(self):
        called = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        required = called["jobs"]["mutate"]["permissions"]
        self.assertEqual(required["id-token"], "write")

        def assert_contract(workflow):
            callers = [
                (name, job)
                for name, job in workflow["jobs"].items()
                if job.get("uses") == "./.github/workflows/cd.yml"
            ]
            self.assertEqual(len(callers), 1)
            self.assertEqual(callers[0][1].get("permissions"), required)

        for filename in ("deploy-dev.yml", "promote-production.yml"):
            with self.subTest(filename=filename):
                workflow = yaml.safe_load((ROOT / ".github/workflows" / filename).read_text())
                assert_contract(workflow)
                caller_name = next(
                    name
                    for name, job in workflow["jobs"].items()
                    if job.get("uses") == "./.github/workflows/cd.yml"
                )
                for degraded in (
                    {key: value for key, value in required.items() if key != "id-token"},
                    {**required, "id-token": "none"},
                ):
                    fixture = deepcopy(workflow)
                    fixture["jobs"][caller_name]["permissions"] = degraded
                    with self.assertRaises(AssertionError):
                        assert_contract(fixture)

    def test_orchestrator_validates_before_environment_and_gates_mutation_on_rollback_upload(self):
        source = (ROOT / ".github/workflows/cd.yml").read_text()
        plan = source.index("  plan:")
        mutation = source.index("  mutate:")
        self.assertLess(plan, mutation)
        self.assertNotIn("environment:", source[plan:mutation])
        self.assertIn("needs: plan", source[mutation:])
        rollback_upload = source.index("id: rollback_upload")
        first_mutation = source.index("operation: mutate", mutation)
        self.assertLess(rollback_upload, first_mutation)
        self.assertIn("steps.rollback_upload.outcome == 'success'", source)
        self.assertNotIn("run jobs execute", source)

    def test_receipt_boundaries_are_environment_gated_and_causally_ordered(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/cd.yml").read_text())
        steps = workflow["jobs"]["mutate"]["steps"]
        by_id = {step["id"]: step for step in steps if "id" in step}
        positions = {step["id"]: index for index, step in enumerate(steps) if "id" in step}
        backend = ("auth", "bff", "worker")

        consume = by_id["consume_dev_images"]
        self.assertEqual(consume["run"], "bash deploy/cd.sh consume-dev-images")
        self.assertEqual(
            consume["if"],
            "inputs.config_environment == 'production' && "
            "(contains(inputs.components, 'auth') || contains(inputs.components, 'bff') || contains(inputs.components, 'worker')) && "
            "steps.rollback_upload.outcome == 'success' && steps.revalidate_before_mutation.outcome == 'success'",
        )
        self.assertGreater(positions["consume_dev_images"], positions["revalidate_before_mutation"])
        self.assertLess(
            positions["consume_dev_images"],
            min(positions[f"{component}_mutate"] for component in backend),
        )

        for component in (*backend, "frontend"):
            condition = by_id[f"{component}_mutate"]["if"]
            self.assertRegex(
                condition,
                r"\(inputs\.config_environment != 'production' \|\| steps\.consume_dev_images\.outcome == 'success'\)",
            )
        frontend_condition = by_id["frontend_mutate"]["if"]
        for component in backend:
            self.assertIn(f"!contains(inputs.components, '{component}')", frontend_condition)

        record = by_id["record_dev_receipt"]
        self.assertEqual(record["run"], "bash deploy/cd.sh record-dev-receipt")
        self.assertEqual(record["if"].split(" && ")[0], "inputs.config_environment == 'development'")
        self.assertGreater(
            positions["record_dev_receipt"],
            max(positions[f"{component}_mutate"] for component in backend),
        )
        self.assertLess(positions["record_dev_receipt"], positions["dev_receipt_upload"])
        for component in backend:
            self.assertIn(
                f"!contains(inputs.components, '{component}') || steps.{component}_mutate.outcome == 'success'",
                record["if"],
            )

        upload_condition = by_id["dev_receipt_upload"]["if"]
        self.assertIn("steps.record_dev_receipt.outcome == 'success'", upload_condition)
        for component in backend:
            self.assertNotIn(f"steps.{component}_mutate.outcome == 'success'", upload_condition)
        self.assertEqual(
            sum(step.get("id") == "dev_receipt_upload" for step in steps),
            1,
        )
        self.assertNotIn("run: bash deploy/cd.sh mutate", (ROOT / ".github/workflows/cd.yml").read_text())

    def test_record_dev_receipt_cli_rejects_partial_backend_images_and_writes_complete_receipt(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        registry = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"
        images = {
            "auth": f"{registry}/llm-wiki-auth@sha256:{'a' * 64}",
            "worker": f"{registry}/olw-pipeline@sha256:{'b' * 64}",
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            image_dir = artifact_dir / "images"
            image_dir.mkdir(parents=True)
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["auth", "worker"],
                    "gcp": {"artifact_registry": registry},
                    "evidence": {"config_fingerprint": "sha256:" + "c" * 64},
                }
            }))
            (image_dir / f"auth-image-{source_sha}.txt").write_text(images["auth"] + "\n")
            env = {
                **os.environ,
                "ROOT": str(ROOT),
                "ENVIRONMENT": "development",
                "SOURCE_SHA": source_sha,
                "PLAN_PATH": str(plan),
                "ARTIFACT_DIR": str(artifact_dir),
                "GITHUB_RUN_ID": "123",
                "GITHUB_RUN_ATTEMPT": "2",
            }
            partial = subprocess.run(
                ["bash", str(ROOT / "deploy/cd.sh"), "record-dev-receipt"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(partial.returncode, 0, partial.stdout + partial.stderr)
            self.assertFalse((image_dir / "dev-receipt.json").exists())

            (image_dir / f"worker-image-{source_sha}.txt").write_text(images["worker"] + "\n")
            complete = subprocess.run(
                ["bash", str(ROOT / "deploy/cd.sh"), "record-dev-receipt"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(complete.returncode, 0, complete.stdout + complete.stderr)
            receipt = json.loads((image_dir / "dev-receipt.json").read_text())
            self.assertEqual(receipt["components"], ["auth", "worker"])
            self.assertEqual(receipt["images"], images)

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

    def test_default_ci_runs_retained_legacy_python_suites(self):
        source = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn("python3 -m unittest discover -s scripts -p 'test_*.py'", source)

    def test_canonical_ci_aggregates_every_current_leaf_job(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        jobs = workflow["jobs"]
        canonical_jobs = [
            (job_id, job) for job_id, job in jobs.items() if job.get("name") == "canonical-ci"
        ]
        self.assertEqual(len(canonical_jobs), 1)
        aggregate_id, aggregate = canonical_jobs[0]
        self.assertEqual(aggregate.get("if"), "${{ always() }}")
        self.assertIsInstance(aggregate.get("needs"), list)
        aggregate_steps = [
            step for step in aggregate.get("steps", []) if step.get("name") == "Require every canonical job"
        ]
        self.assertEqual(len(aggregate_steps), 1)
        aggregate_script = aggregate_steps[0].get("run", "")
        for job_id in aggregate["needs"]:
            self.assertIn(f'test "${{{{ needs.{job_id}.result }}}}" = success', aggregate_script)
        dependencies = {
            job_id: set(job.get("needs", [])) if isinstance(job.get("needs", []), list) else {job.get("needs")}
            for job_id, job in jobs.items()
        }
        reachable = set()
        pending = list(dependencies[aggregate_id])
        while pending:
            job_id = pending.pop()
            if job_id in reachable:
                continue
            reachable.add(job_id)
            pending.extend(dependencies[job_id])
        self.assertEqual(reachable, set(jobs) - {aggregate_id})

    def test_entry_workflows_are_dispatch_only_and_production_consumes_dispatch_receipt(self):
        development = (ROOT / ".github/workflows/deploy-dev.yml").read_text()
        production = (ROOT / ".github/workflows/promote-production.yml").read_text()
        self.assertNotIn("\n  push:", development)
        self.assertNotIn("\n  push:", production)
        source = source_bundle()
        consume_start = source.index("consume_dev_images()")
        consume = source[consume_start:source.index("\n}\n\npreflight_shared", consume_start)]
        self.assertIn("event=workflow_dispatch", consume)
        self.assertNotIn("event=push", consume)
        self.assertIn("head_sha=${SOURCE_SHA}", consume)
        self.assertIn("branch=develop", consume)
        self.assertIn('gh run download "$id"', consume)
        self.assertIn("cd-images-$SOURCE_SHA", consume)

    def test_protected_mutation_maps_environment_scoped_vercel_credentials(self):
        source = (ROOT / ".github/workflows/cd.yml").read_text()
        mutation = source[source.index("  mutate:"):]
        for mapping in (
            "VERCEL_TOKEN: ${{ secrets.VERCEL_TOKEN }}",
            "VERCEL_PROJECT_ID: ${{ secrets.VERCEL_PROJECT_ID }}",
            "VERCEL_TEAM_ID: ${{ secrets.VERCEL_TEAM_ID }}",
        ):
            self.assertIn(mapping, mutation)
            self.assertNotIn(mapping, source[:source.index("  mutate:")])

    def test_revalidation_rejection_leaves_zero_mutation_and_skips_rollback(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            (bin_dir / "git").write_text(
                "#!/usr/bin/env bash\n"
                "if [[ $1 == fetch ]]; then exit 0; fi\n"
                f"if [[ $1 == rev-parse ]]; then printf '%s\\n' {source_sha}; exit 0; fi\n"
                "exit 2\n"
            )
            (bin_dir / "git").chmod(0o755)
            (bin_dir / "gh").write_text("#!/usr/bin/env bash\nexit 42\n")
            (bin_dir / "gh").chmod(0o755)
            provider_log = root / "provider.log"
            (bin_dir / "gcloud").write_text(
                "#!/usr/bin/env bash\n"
                f"printf '%s\\n' \"$*\" >> {provider_log}\n"
                "exit 99\n"
            )
            (bin_dir / "gcloud").chmod(0o755)
            artifact_dir = root / "artifacts"
            (artifact_dir / "dev-images").mkdir(parents=True)
            image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
            (artifact_dir / "dev-images" / f"worker-image-{source_sha}.txt").write_text(image + "\n")
            (artifact_dir / "dev-images" / "dev-receipt.json").write_text(json.dumps({
                "schema": "lwc-306-dev-image-receipt-v1",
                "source": {"sha": source_sha, "ref": "develop", "workflow_path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch"},
                "config": {"environment": "development", "path": "deploy/environments/development.yaml", "fingerprint": "sha256:fixture"},
                "components": ["worker"], "images": {"worker": image},
            }))
            plan = {
                "ci": {"run_id": 1, "run_attempt": 1, "jobs": []},
                "normalized": {
                    "selected_components": ["worker"],
                    "gcp": {"project_id": "llm-wiki-cloud", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "worker": {"job_name": "olw-pipeline", "location": "asia-east1", "runtime_service_account": "worker@llm-wiki-cloud.iam.gserviceaccount.com", "bucket": "bucket", "args": ["run", "--auto-approve"], "secret_references": {"deepseek_api_key": "deepseek-apikey"}},
                    "evidence": {"config_fingerprint": "sha256:fixture"},
                },
            }
            plan_path = root / "plan.json"
            plan_path.write_text(json.dumps(plan))
            journal_path = artifact_dir / "journal.json"
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "ENVIRONMENT": "production", "SOURCE_REF": "main", "SOURCE_SHA": source_sha,
                "CONFIG_PATH": "deploy/environments/production.yaml", "COMPONENTS": "worker",
                "PLAN_PATH": str(plan_path), "JOURNAL_PATH": str(journal_path), "ARTIFACT_DIR": str(artifact_dir),
                "ROLLBACK_PATH": str(artifact_dir / "rollback.json"), "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"),
                "ROLLBACK_UPLOADED": "1", "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud", "GH_TOKEN": "fixture",
            }
            rejected = subprocess.run(["bash", str(ROOT / "deploy/components/worker.sh"), "mutate"], env=env, text=True, capture_output=True)
            self.assertNotEqual(rejected.returncode, 0, rejected.stdout + rejected.stderr)
            self.assertEqual(json.loads(journal_path.read_text())["components"], {}, rejected.stdout + rejected.stderr)
            rolled_back = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertEqual(rolled_back.returncode, 0, rolled_back.stdout + rolled_back.stderr)
            self.assertFalse(provider_log.exists())
            self.assertEqual(json.loads((artifact_dir / "rollback-result.json").read_text())["result"], "not_needed")

    def test_backend_image_acceptance_is_journaled_before_runtime_revalidation(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
        old_image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "a" * 64
        definition = {
            "apiVersion": "run.googleapis.com/v1",
            "kind": "Job",
            "metadata": {"name": "olw-pipeline", "generation": 9, "etag": "etag-9"},
            "spec": {"template": {"spec": {"template": {"spec": {
                "serviceAccountName": "worker@llm-wiki-cloud.iam.gserviceaccount.com",
                "containers": [{"image": old_image, "env": [
                    {"name": "BUCKET", "value": "bucket"},
                    {"name": "PIPELINE_JOB_NAME", "value": "olw-pipeline"},
                    {"name": "PIPELINE_JOB_LOCATION", "value": "asia-east1"},
                    {"name": "DEEPSEEK_API_KEY", "valueFrom": {"secretKeyRef": {"name": "deepseek-apikey", "key": "latest"}}},
                ], "args": ["run", "--auto-approve"]}],
            }}}}},
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            state = root / "job.json"
            state.write_text(json.dumps(definition))
            revparse_count = root / "revparse-count"
            plan = root / "plan.json"
            ci_jobs = [
                {"id": index + 1, "name": name, "run_id": 1, "run_attempt": 1, "status": "completed", "conclusion": "success"}
                for index, name in enumerate(sorted(["bff", "frontend-build", "local-vertical-smoke", "workflow-source", "canonical-ci"]))
            ]
            plan.write_text(json.dumps({
                "ci": {"run_id": 1, "run_attempt": 1, "workflow_path": ".github/workflows/ci.yml", "event": "push", "head_branch": "develop", "head_sha": source_sha, "conclusion": "success", "jobs": ci_jobs},
                "normalized": {
                    "selected_components": ["worker"],
                    "gcp": {"project_id": "llm-wiki-cloud", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "worker": {"job_name": "olw-pipeline", "location": "asia-east1", "runtime_service_account": "worker@llm-wiki-cloud.iam.gserviceaccount.com", "bucket": "bucket", "args": ["run", "--auto-approve"], "secret_references": {"deepseek_api_key": "deepseek-apikey"}},
                    "evidence": {"config_fingerprint": "sha256:fixture"},
                },
            }))
            provider_log = root / "gcloud.log"
            docker_log = root / "docker.log"
            (bin_dir / "git").write_text(textwrap.dedent(f"""
                #!/usr/bin/env bash
                if [[ $1 == fetch ]]; then exit 0; fi
                if [[ $1 == rev-parse ]]; then
                  count=0; [[ -f {str(revparse_count)!r} ]] && count=$(<{str(revparse_count)!r})
                  count=$((count + 1)); printf '%s' "$count" > {str(revparse_count)!r}
                  (( count >= 3 )) && printf '%s\\n' {'f' * 40} || printf '%s\\n' {source_sha}
                  exit 0
                fi
                exit 2
            """).lstrip())
            (bin_dir / "git").chmod(0o755)
            (bin_dir / "gh").write_text(textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                endpoint = sys.argv[-1]
                run = {{"id": 1, "run_attempt": 1, "path": ".github/workflows/ci.yml", "event": "push", "head_branch": "develop", "head_sha": {source_sha!r}, "status": "completed", "conclusion": "success"}}
                jobs = json.dumps({{"jobs": json.load(open(os.environ["FAKE_PLAN"]))["ci"]["jobs"]}})
                if endpoint.endswith("/runs/1") or endpoint.endswith("/attempts/1"):
                    print(json.dumps(run))
                elif "/attempts/1/jobs" in endpoint:
                    print(jobs)
                else:
                    raise SystemExit(2)
            """).lstrip())
            (bin_dir / "gh").chmod(0o755)
            (bin_dir / "docker").write_text(textwrap.dedent(f"""
                #!/usr/bin/env bash
                printf '%s\\n' "$*" >> {str(docker_log)!r}
                exit 0
            """).lstrip())
            (bin_dir / "docker").chmod(0o755)
            (bin_dir / "gcloud").write_text(textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                from pathlib import Path
                args = sys.argv[1:]
                Path(os.environ["FAKE_GCLOUD_LOG"]).open("a").write(" ".join(args) + "\\n")
                if args[:4] == ["artifacts", "docker", "images", "describe"]:
                    print("sha256:" + "b" * 64)
                elif args[:3] == ["run", "jobs", "describe"]:
                    print(Path(os.environ["FAKE_JOB_STATE"]).read_text())
                elif args[:3] in (["run", "jobs", "update"], ["run", "jobs", "replace"]):
                    raise SystemExit(91)
                else:
                    raise SystemExit(2)
            """).lstrip())
            (bin_dir / "gcloud").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "ENVIRONMENT": "development", "SOURCE_REF": "develop", "SOURCE_SHA": source_sha,
                "CONFIG_PATH": "deploy/environments/development.yaml", "COMPONENTS": "worker", "GITHUB_REF": "refs/heads/develop", "GITHUB_REF_NAME": "develop",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud", "GH_TOKEN": "fixture", "ROLLBACK_UPLOADED": "1", "GITHUB_RUN_ID": "7", "GITHUB_RUN_ATTEMPT": "1",
                "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(root / "rollback.json"), "JOURNAL_PATH": str(root / "journal.json"), "ARTIFACT_DIR": str(root / "artifacts"),
                "EVIDENCE_PATH": str(root / "artifacts" / "readback.json"), "ROLLBACK_RESULT_PATH": str(root / "artifacts" / "rollback-result.json"), "FINAL_EVIDENCE_PATH": str(root / "artifacts" / "evidence.json"),
                "FAKE_GCLOUD_LOG": str(provider_log), "FAKE_JOB_STATE": str(state), "FAKE_PLAN": str(plan),
            }
            frozen = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "freeze"], env=env, text=True, capture_output=True)
            self.assertEqual(frozen.returncode, 0, frozen.stdout + frozen.stderr)
            mutated = subprocess.run(["bash", str(ROOT / "deploy/components/worker.sh"), "mutate"], env=env, text=True, capture_output=True)
            self.assertNotEqual(mutated.returncode, 0, mutated.stdout + mutated.stderr)
            journal = json.loads((root / "journal.json").read_text())
            self.assertIn("worker", journal["components"], mutated.stdout + mutated.stderr)
            self.assertEqual(journal["order"], ["worker"])
            self.assertEqual(journal["components"]["worker"]["state"], "accepted")
            self.assertEqual(journal["components"]["worker"]["history"], ["pending", "accepted"])
            self.assertIn("push", docker_log.read_text())
            provider_calls = provider_log.read_text()
            self.assertNotIn("run jobs update", provider_calls)
            rollback = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertEqual(rollback.returncode, 0, rollback.stdout + rollback.stderr)
            rollback_result = json.loads((root / "artifacts" / "rollback" / "worker.json").read_text())
            self.assertEqual(rollback_result["result"], "success")
            self.assertEqual(rollback_result.get("reason"), "verified_noop", json.dumps(rollback_result) + rollback.stdout + rollback.stderr)
            rollback_journal = json.loads((root / "journal.json").read_text())
            self.assertEqual(rollback_journal["order"], ["worker"])
            self.assertEqual(set(rollback_journal["components"]), {"worker"})
            self.assertEqual(rollback_journal["components"]["worker"]["history"], ["pending", "accepted", "rollback_pending", "rollback_accepted"])
            self.assertNotIn("run jobs replace", provider_log.read_text())
            (root / "artifacts" / "readback.json").write_text(json.dumps({"schema": "lwc-306-readback-v1", "result": "unknown", "verified": False, "components": []}))
            evidence = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "evidence"], env=env, text=True, capture_output=True)
            self.assertEqual(evidence.returncode, 0, evidence.stdout + evidence.stderr)
            rendered = json.loads((root / "artifacts" / "evidence.json").read_text())
            self.assertEqual(rendered["mutation_count"], 1)
            self.assertEqual(rendered["mutation_components"], ["worker"])


    def test_backend_artifact_and_runtime_success_share_one_journal_entry(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            provider_log = root / "provider.log"
            (bin_dir / "docker").write_text(textwrap.dedent(f"""
                #!/usr/bin/env bash
                printf 'docker %s\\n' "$*" >> {str(provider_log)!r}
            """).lstrip())
            (bin_dir / "docker").chmod(0o755)
            (bin_dir / "gcloud").write_text(textwrap.dedent(f"""
                #!/usr/bin/env bash
                printf 'gcloud %s\\n' "$*" >> {str(provider_log)!r}
                if [[ "$1 $2 $3 $4" == "artifacts docker images describe" ]]; then
                  printf '%s\\n' sha256:{'b' * 64}
                fi
            """).lstrip())
            (bin_dir / "gcloud").chmod(0o755)
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["worker"],
                    "gcp": {"project_id": "llm-wiki-cloud", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "worker": {"job_name": "olw-pipeline", "location": "asia-east1", "runtime_service_account": "worker@llm-wiki-cloud.iam.gserviceaccount.com", "bucket": "bucket", "args": ["run", "--auto-approve"], "secret_references": {"deepseek_api_key": "deepseek-apikey"}},
                },
            }))
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "ENVIRONMENT": "development", "SOURCE_REF": "develop", "SOURCE_SHA": source_sha,
                "GITHUB_REF": "refs/heads/develop", "GITHUB_REF_NAME": "develop", "GITHUB_RUN_ID": "7", "GITHUB_RUN_ATTEMPT": "1",
                "PLAN_PATH": str(plan), "JOURNAL_PATH": str(root / "journal.json"), "ARTIFACT_DIR": str(root / "artifacts"),
            }
            script = textwrap.dedent(f"""
                source {str(ROOT / 'deploy/components/worker.sh')!r} help
                revalidate_before_provider() {{ :; }}
                worker_verify() {{ return 0; }}
                worker_mutate
            """)
            result = subprocess.run(["bash", "-c", script], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            journal = json.loads((root / "journal.json").read_text())
            self.assertEqual(journal["order"], ["worker"])
            self.assertEqual(set(journal["components"]), {"worker"})
            self.assertEqual(journal["components"]["worker"]["state"], "accepted")
            self.assertEqual(journal["components"]["worker"]["history"], ["pending", "accepted"])
            commands = provider_log.read_text().splitlines()
            build = next(index for index, line in enumerate(commands) if line.startswith("docker build "))
            push = next(index for index, line in enumerate(commands) if line.startswith("docker push "))
            runtime = next(index for index, line in enumerate(commands) if line.startswith("gcloud run jobs update "))
            self.assertLess(build, push)
            self.assertLess(push, runtime)
            self.assertEqual(sum(line.startswith("docker build ") for line in commands), 1)
            self.assertEqual(sum(line.startswith("docker push ") for line in commands), 1)
            self.assertEqual(sum(line.startswith("gcloud run jobs update ") for line in commands), 1)

    def test_ci_revalidation_rejects_paginated_duplicate_or_omitted_jobs(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        run = {
            "id": 123,
            "run_attempt": 2,
            "path": ".github/workflows/ci.yml",
            "event": "push",
            "head_branch": "develop",
            "head_sha": source_sha,
            "status": "completed",
            "conclusion": "success",
        }
        required = ["bff", "frontend-build", "local-vertical-smoke", "workflow-source", "canonical-ci"]
        for scenario in ("duplicate", "omitted"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                (root / "git-count").write_text("0")
                (root / "run.json").write_text(json.dumps(run))
                jobs = [
                    {"id": index + 1, "name": f"filler-{index}", "run_id": 123, "run_attempt": 2, "status": "completed", "conclusion": "success"}
                    for index in range(100)
                ] + [
                    {"id": index + 101, "name": name, "run_id": 123, "run_attempt": 2, "status": "completed", "conclusion": "success"}
                    for index, name in enumerate(required)
                ]
                (root / "jobs.json").write_text(json.dumps({"total_count": len(jobs), "jobs": jobs}))
                git = textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import sys
                    args = sys.argv[1:]
                    if args and args[0] == "fetch": raise SystemExit(0)
                    if args[:2] == ["rev-parse", "HEAD"] or args[:2] == ["rev-parse", "origin/develop"]:
                        print({source_sha!r}); raise SystemExit(0)
                    raise SystemExit(2)
                """).lstrip()
                gh = textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import json, sys
                    args = sys.argv[1:]
                    endpoint = args[-1] if args else ""
                    if endpoint.endswith("/runs/123") or endpoint.endswith("/attempts/2"):
                        print(json.dumps(json.load(open({str(root / 'run.json')!r}))))
                    elif "/attempts/2/jobs" in endpoint:
                        jobs = json.load(open({str(root / 'jobs.json')!r}))
                        if "page=2" in endpoint:
                            jobs["jobs"] = jobs["jobs"][100:]
                        else:
                            jobs["jobs"] = jobs["jobs"][:100]
                        if {scenario!r} == "duplicate" and "page=2" in endpoint:
                            jobs["jobs"][0]["name"] = jobs["jobs"][1]["name"]
                        if {scenario!r} == "omitted" and "page=2" in endpoint:
                            jobs["jobs"] = jobs["jobs"][1:]
                        print(json.dumps(jobs))
                    else:
                        raise SystemExit(2)
                """).lstrip()
                for name, content in (("git", git), ("gh", gh)):
                    path = bin_dir / name
                    path.write_text(content)
                    path.chmod(0o755)
                plan = root / "plan.json"
                plan.write_text(json.dumps({"ci": run}))
                env = {
                    **os.environ,
                    "PATH": f"{bin_dir}:{os.environ['PATH']}",
                    "PLAN_PATH": str(plan),
                    "GH_TOKEN": "fixture",
                    "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                    "SOURCE_REF": "develop",
                    "SOURCE_SHA": source_sha,
                }
                result = subprocess.run(
                    ["bash", "-c", common_source() + "\nrevalidate_ci"],
                    env=env,
                    text=True,
                    capture_output=True,
                )
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def canonical_ci_inventory(self, run_id=33840184963, run_attempt=1):
        names = sorted([
            "actionlint/schema",
            "bff",
            "frontend-lint",
            "frontend-typecheck",
            "frontend-test",
            "frontend-build",
            "local-vertical-smoke",
            "workflow-source-guards",
            "canonical-ci",
        ])
        return [
            {
                "id": run_id * 10 + index,
                "name": name,
                "status": "completed",
                "conclusion": "success",
                "run_id": run_id,
                "run_attempt": run_attempt,
            }
            for index, name in enumerate(names, 1)
        ]

    def run_ci_validator(self, jobs, run_id=33840184963, run_attempt=1):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "jobs.json"
            path.write_text(json.dumps(jobs))
            return subprocess.run(
                [
                    "bash",
                    "-c",
                    common_source() + f"\nci_validate_jobs \"$(<{str(path)!r})\" {run_id} {run_attempt}",
                ],
                env={**os.environ, "ROOT": str(ROOT)},
                text=True,
                capture_output=True,
            )

    def test_ci_validator_accepts_real_nine_job_inventory_with_canonical_aggregate(self):
        jobs = self.canonical_ci_inventory()
        result = self.run_ci_validator(jobs)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        future_leaf = {**jobs[0], "id": jobs[-1]["id"] + 1, "name": "future-leaf"}
        self.assertEqual(self.run_ci_validator([*jobs, future_leaf]).returncode, 0)

    def test_ci_validator_rejects_invalid_aggregate_inventory(self):
        jobs = self.canonical_ci_inventory()
        aggregate = next(job for job in jobs if job["name"] == "canonical-ci")
        non_aggregate = [job for job in jobs if job["name"] != "canonical-ci"]
        cases = {
            "missing aggregate": non_aggregate,
            "failed aggregate": [*non_aggregate, {**aggregate, "conclusion": "failure"}],
            "skipped aggregate": [*non_aggregate, {**aggregate, "conclusion": "skipped"}],
            "failed leaf": [{**jobs[0], "conclusion": "failure"}, *jobs[1:]],
            "skipped leaf": [{**jobs[0], "conclusion": "skipped"}, *jobs[1:]],
            "duplicate name": [*non_aggregate, {**aggregate, "name": jobs[0]["name"]}],
            "duplicate id": [*non_aggregate, {**aggregate, "id": jobs[0]["id"]}],
            "wrong run": [*non_aggregate, {**aggregate, "run_id": aggregate["run_id"] + 1}],
            "wrong attempt": [*non_aggregate, {**aggregate, "run_attempt": 2}],
            "malformed records": [{"id": aggregate["id"], "name": aggregate["name"]}],
            "malformed shape": {"jobs": jobs},
        }
        for name, value in cases.items():
            with self.subTest(name=name):
                self.assertNotEqual(self.run_ci_validator(value).returncode, 0)

    def test_ci_revalidation_rejects_changed_post_plan_inventory(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        run_id = 33840184963
        run_attempt = 1
        run = {
            "id": run_id,
            "run_attempt": run_attempt,
            "path": ".github/workflows/ci.yml",
            "event": "push",
            "head_branch": "develop",
            "head_sha": source_sha,
            "status": "completed",
            "conclusion": "success",
        }
        planned_jobs = self.canonical_ci_inventory(run_id, run_attempt)
        self.assertEqual([job["name"] for job in planned_jobs], sorted(job["name"] for job in planned_jobs))
        changed_jobs = deepcopy(planned_jobs)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            current_jobs = root / "current-jobs.json"
            current_jobs.write_text(json.dumps(planned_jobs))
            (bin_dir / "git").write_text(textwrap.dedent(f"""
                #!/usr/bin/env bash
                if [[ $1 == fetch ]]; then exit 0; fi
                if [[ $1 == rev-parse ]]; then printf '%s\\n' {source_sha}; exit 0; fi
                exit 2
            """).lstrip())
            (bin_dir / "git").chmod(0o755)
            (bin_dir / "gh").write_text(textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                endpoint = sys.argv[-1]
                run = {json.dumps(run)!r}
                if endpoint.endswith("/runs/{run_id}") or endpoint.endswith("/attempts/{run_attempt}"):
                    print(run)
                elif "/attempts/{run_attempt}/jobs" in endpoint:
                    jobs = json.loads(open(os.environ["CURRENT_JOBS_PATH"]).read())
                    print(json.dumps({{"total_count": len(jobs), "jobs": jobs}}))
                else:
                    raise SystemExit(2)
            """).lstrip())
            (bin_dir / "gh").chmod(0o755)
            plan = root / "plan.json"
            plan.write_text(json.dumps({"ci": {
                "run_id": run_id,
                "run_attempt": run_attempt,
                "workflow_path": run["path"],
                "event": run["event"],
                "head_branch": run["head_branch"],
                "head_sha": run["head_sha"],
                "conclusion": run["conclusion"],
                "jobs": planned_jobs,
            }}))
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "ROOT": str(ROOT),
                "PLAN_PATH": str(plan),
                "GH_TOKEN": "fixture",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                "SOURCE_REF": "develop",
                "SOURCE_SHA": source_sha,
                "CURRENT_JOBS_PATH": str(current_jobs),
            }
            baseline = subprocess.run(
                ["bash", "-c", common_source() + "\nrevalidate_ci"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(baseline.returncode, 0, baseline.stdout + baseline.stderr)
            changed_jobs[-1]["id"] += 1
            current_jobs.write_text(json.dumps(changed_jobs))
            result = subprocess.run(
                ["bash", "-c", common_source() + "\nrevalidate_ci"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(result.stderr.strip(), "cd contract failed: pinned canonical CI job set changed")

    def test_branch_advance_after_rollback_upload_blocks_provider_boundary(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        self.assertIn("revalidate_before_provider", source)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan = root / "plan.json"
            source_sha = "0123456789abcdef0123456789abcdef01234567"
            plan.write_text(json.dumps({"ci": {"run_id": 123, "run_attempt": 1}}))
            bin_dir = root / "bin"
            bin_dir.mkdir()
            (root / "provider-calls").write_text("")
            (root / "git").write_text("#!/usr/bin/env bash\n[[ $1 == fetch ]] && exit 0\nprintf '%s\\n' deadbeef\n")
            (root / "git").chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{root}:{os.environ['PATH']}",
                "PLAN_PATH": str(plan),
                "ROLLBACK_UPLOADED": "1",
                "GH_TOKEN": "fixture",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                "SOURCE_REF": "develop",
                "SOURCE_SHA": source_sha,
            }
            result = subprocess.run(
                ["bash", str(ROOT / "deploy/cd.sh"), "revalidate-before-provider"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((root / "provider-calls").read_text(), "")

    def test_production_rejects_ambiguous_duplicate_dev_receipts(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                args = sys.argv[1:]
                endpoint = args[-1] if args else ""
                if "/actions/workflows/deploy-dev.yml/runs" in endpoint:
                    print(json.dumps({{"total_count": 2, "workflow_runs": [
                        {{"id": 101, "path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch", "head_branch": "develop", "head_sha": {source_sha!r}, "status": "completed", "conclusion": "success"}},
                        {{"id": 102, "path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch", "head_branch": "develop", "head_sha": {source_sha!r}, "status": "completed", "conclusion": "success"}}
                    ]}}))
                else: raise SystemExit(2)
            """).lstrip()
            gh = bin_dir / "gh"
            gh.write_text(fake)
            gh.chmod(0o755)
            plan = root / "plan.json"
            plan.write_text(json.dumps({"source": {"sha": source_sha}, "normalized": {"selected_components": ["bff"], "gcp": {"artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"}, "evidence": {"config_fingerprint": "sha256:fingerprint"}}}))
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PLAN_PATH": str(plan),
                "ARTIFACT_DIR": str(root / "artifacts"),
                "GH_TOKEN": "fixture",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                "ENVIRONMENT": "production",
                "SOURCE_SHA": source_sha,
            }
            result = subprocess.run(
                ["bash", "deploy/cd.sh", "consume-dev-images"],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_production_receipt_does_not_compare_dev_and_production_config_fingerprints(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "a" * 64
        dev_fingerprint = "sha256:" + "d" * 64
        production_fingerprint = "sha256:" + "p" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            receipt = {
                "schema": "lwc-306-dev-image-receipt-v1",
                "source": {
                    "sha": source_sha,
                    "ref": "develop",
                    "workflow_path": ".github/workflows/deploy-dev.yml",
                    "event": "workflow_dispatch",
                    "run_id": 101,
                    "run_attempt": 2,
                },
                "config": {"environment": "development", "path": "deploy/environments/development.yaml", "fingerprint": dev_fingerprint},
                "components": ["worker"],
                "images": {"worker": image},
            }
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, sys
                from pathlib import Path
                args = sys.argv[1:]
                endpoint = args[-1] if args else ""
                if args[:2] == ["run", "download"]:
                    target = Path(args[args.index("--dir") + 1])
                    target.mkdir(parents=True, exist_ok=True)
                    (target / "dev-receipt.json").write_text(json.dumps({receipt!r}))
                elif "/actions/workflows/deploy-dev.yml/runs" in endpoint:
                    print(json.dumps({{"workflow_runs": [{{"id": 101, "run_attempt": 2, "path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch", "head_branch": "develop", "head_sha": {source_sha!r}, "status": "completed", "conclusion": "success"}}]}}))
                elif "/actions/runs/101/artifacts" in endpoint:
                    print(json.dumps({{"artifacts": [{{"id": 202, "name": "cd-images-{source_sha}", "expired": False, "size_in_bytes": 1, "digest": "sha256:{'e' * 64}", "workflow_run": {{"id": 101}}}}]}}))
                else:
                    raise SystemExit(2)
            """).lstrip()
            gh = bin_dir / "gh"
            gh.write_text(fake)
            gh.chmod(0o755)
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["worker"],
                    "gcp": {"artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "evidence": {"config_fingerprint": production_fingerprint},
                }
            }))
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "PLAN_PATH": str(plan),
                "ARTIFACT_DIR": str(root / "artifacts"),
                "GH_TOKEN": "fixture",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                "ENVIRONMENT": "production",
                "SOURCE_SHA": source_sha,
            }
            result = subprocess.run(
                ["bash", "deploy/cd.sh", "consume-dev-images"],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(json.loads((root / "artifacts/dev-images/dev-artifact.json").read_text())["digest"], "sha256:" + "e" * 64)

    def test_auth_and_bff_mutation_and_rollback_converge_explicit_traffic(self):
        registry = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"
        for component, image_name in (("auth", "llm-wiki-auth"), ("bff", "llm-wiki-bff")):
            with self.subTest(component=component), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                old_image = f"{registry}/{image_name}@sha256:" + "a" * 64
                new_image = f"{registry}/{image_name}@sha256:" + "b" * 64
                service_name = f"{component}-service"
                old_revision = f"{component}-old"
                state = {
                    "service": {"status": {"traffic": [{"revisionName": old_revision, "percent": 100}], "latestCreatedRevisionName": old_revision}},
                    "revisions": {old_revision: {"spec": {"containers": [{"image": old_image}]}, "status": {"imageDigest": old_image, "conditions": [{"type": "Ready", "status": "True"}]}}},
                }
                state_path = root / "state.json"
                state_path.write_text(json.dumps(state))
                log_path = root / "provider.log"
                fake = textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import json, os, sys
                    from pathlib import Path
                    args = sys.argv[1:]
                    Path(os.environ["FAKE_LOG"]).open("a").write(" ".join(args) + "\\n")
                    state_path = Path(os.environ["FAKE_STATE"])
                    state = json.loads(state_path.read_text())
                    if args[:3] == ["run", "services", "describe"]:
                        if any(arg.startswith("--format=value(") for arg in args):
                            print(state["service"]["status"]["traffic"][0]["revisionName"])
                        else:
                            print(json.dumps(state["service"]))
                    elif args[:3] == ["run", "revisions", "describe"]:
                        print(json.dumps(state["revisions"][args[3]]))
                    elif args[:3] == ["run", "services", "update"]:
                        image = args[args.index("--image") + 1]
                        revision = "{component}-revision-" + str(len(state["revisions"]))
                        state["service"]["status"]["latestCreatedRevisionName"] = revision
                        state["revisions"][revision] = {{"spec": {{"containers": [{{"image": image}}]}}, "status": {{"imageDigest": image, "conditions": [{{"type": "Ready", "status": "True"}}]}}}}
                        state_path.write_text(json.dumps(state))
                    elif args[:3] == ["run", "services", "update-traffic"]:
                        if "--to-latest" not in args:
                            raise SystemExit(2)
                        revision = state["service"]["status"]["latestCreatedRevisionName"]
                        state["service"]["status"]["traffic"] = [{{"revisionName": revision, "percent": 100}}]
                        state_path.write_text(json.dumps(state))
                    else:
                        raise SystemExit(2)
                """).lstrip()
                provider = bin_dir / "gcloud"
                provider.write_text(fake)
                provider.chmod(0o755)
                plan = root / "plan.json"
                plan.write_text(json.dumps({
                    "normalized": {
                        "selected_components": [component],
                        "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": registry},
                        "evidence": {"config_fingerprint": "sha256:" + "p" * 64},
                        component: {"service_name": service_name},
                    }
                }))
                artifacts = root / "artifacts" / "dev-images"
                artifacts.mkdir(parents=True)
                (artifacts / f"{component}-image-{'a' * 40}.txt").write_text(new_image + "\n")
                (artifacts / "dev-receipt.json").write_text(json.dumps({
                    "schema": "lwc-306-dev-image-receipt-v1",
                    "source": {"sha": "a" * 40, "ref": "develop", "workflow_path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch"},
                    "config": {"environment": "development", "path": "deploy/environments/development.yaml", "fingerprint": "sha256:" + "d" * 64},
                    "components": [component], "images": {component: new_image},
                }))
                env = {
                    **os.environ,
                    "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "ENVIRONMENT": "production", "SOURCE_REF": "main", "SOURCE_SHA": "a" * 40,
                    "CONFIG_PATH": "deploy/environments/production.yaml", "COMPONENTS": component, "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(root / "artifacts" / "rollback.json"),
                    "JOURNAL_PATH": str(root / "artifacts" / "journal.json"), "ARTIFACT_DIR": str(root / "artifacts"), "ROLLBACK_RESULT_PATH": str(root / "artifacts" / "rollback-result.json"),
                    "FAKE_STATE": str(state_path), "FAKE_LOG": str(log_path),
                }
                frozen = subprocess.run(["bash", str(ROOT / "deploy/components" / f"{component}.sh"), "freeze"], env=env, capture_output=True, text=True)
                self.assertEqual(frozen.returncode, 0, frozen.stdout + frozen.stderr)
                mutated = subprocess.run(["bash", str(ROOT / "deploy/components" / f"{component}.sh"), "mutate"], env=env, capture_output=True, text=True)
                self.assertEqual(mutated.returncode, 0, mutated.stdout + mutated.stderr)
                current = json.loads(state_path.read_text())
                current_revision = current["service"]["status"]["traffic"][0]["revisionName"]
                self.assertEqual(current["revisions"][current_revision]["status"]["imageDigest"], new_image)
                self.assertEqual(json.loads((root / "artifacts/journal.json").read_text())["components"][component]["state"], "accepted")
                rolled_back = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, capture_output=True, text=True)
                self.assertEqual(rolled_back.returncode, 0, rolled_back.stdout + rolled_back.stderr)
                current = json.loads(state_path.read_text())
                current_revision = current["service"]["status"]["traffic"][0]["revisionName"]
                self.assertEqual(current["revisions"][current_revision]["status"]["imageDigest"], old_image)
                self.assertEqual(json.loads((root / "artifacts/rollback" / f"{component}.json").read_text())["result"], "success")
                calls = log_path.read_text().splitlines()
                traffic_calls = [call for call in calls if "run services update-traffic" in call]
                self.assertEqual(len(traffic_calls), 2)
                self.assertTrue(all("--to-latest" in call for call in traffic_calls))
                image_updates = [call for call in calls if "run services update " in call]
                self.assertEqual(len(image_updates), 2)
                self.assertIn(f"--image {new_image}", image_updates[0])
                self.assertIn(f"--image {old_image}", image_updates[1])
                for call in image_updates:
                    self.assertNotRegex(call, r"--(update-env-vars|update-secrets|service-account|network|subnet|vpc-egress|ingress|max)")

    def test_readback_classification_preserves_unknown_and_failed(self):
        registry = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"
        for component, image_name in (("auth", "llm-wiki-auth"), ("bff", "llm-wiki-bff"), ("worker", "olw-pipeline")):
            with self.subTest(component=component), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                old_image = f"{registry}/{image_name}@sha256:" + "a" * 64
                expected_image = f"{registry}/{image_name}@sha256:" + "b" * 64
                old_revision = f"{component}-old"
                plan_component = {"service_name": f"{component}-service"} if component != "worker" else {"job_name": "worker-job", "location": "asia-east1"}
                plan = root / "plan.json"
                plan.write_text(json.dumps({"normalized": {"selected_components": [component], "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": registry}, component: plan_component}}))
                service = {"status": {"traffic": [{"revisionName": old_revision, "percent": 100}]}}
                revision = {"spec": {"containers": [{"image": old_image}]}, "status": {"imageDigest": old_image, "conditions": [{"type": "Ready", "status": "True"}]}}
                job = {"spec": {"template": {"spec": {"template": {"spec": {"containers": [{"image": old_image}]}}}}}}
                fake = textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import json, os, sys
                    args = sys.argv[1:]
                    mode = os.environ["FAKE_MODE"]
                    if mode == "transport":
                        raise SystemExit(9)
                    if mode == "unreadable":
                        print("not-json")
                    elif args[:3] == ["run", "services", "describe"]:
                        print(json.dumps({service!r}))
                    elif args[:3] == ["run", "revisions", "describe"]:
                        print(json.dumps({revision!r}))
                    elif args[:3] == ["run", "jobs", "describe"]:
                        print(json.dumps({job!r}))
                    else:
                        raise SystemExit(2)
                """).lstrip()
                provider = bin_dir / "gcloud"
                provider.write_text(fake)
                provider.chmod(0o755)
                verify = f"{component}_verify"
                result_var = "SERVICE_READBACK_RESULT" if component != "worker" else "WORKER_READBACK_RESULT"
                component_script = ROOT / "deploy/components" / f"{component}.sh"
                command = f"source {str(component_script)!r} help >/dev/null; if {verify} \"$IMAGE\"; then printf 'success\\n'; else printf '%s\\n' \"${{{result_var}}}\"; fi"
                for mode, expected in (("transport", "unknown"), ("unreadable", "unknown"), ("mismatch", "failed")):
                    env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "PLAN_PATH": str(plan), "IMAGE": expected_image, "FAKE_MODE": mode}
                    result = subprocess.run(["bash", "-c", command], env=env, capture_output=True, text=True)
                    self.assertEqual(result.stdout.strip(), expected, result.stderr)

    def test_image_receipt_must_use_exact_component_registry_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts/dev-images"
            artifact_dir.mkdir(parents=True)
            sha = "0123456789abcdef0123456789abcdef01234567"
            (artifact_dir / f"auth-image-{sha}.txt").write_text("evil.example/llm-wiki-auth@sha256:" + "a" * 64 + "\n")
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"gcp": {"artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"}}}))
            env = {**os.environ, "ROOT": str(ROOT), "PLAN_PATH": str(plan), "ARTIFACT_DIR": str(root / "artifacts"), "SOURCE_SHA": sha, "ENVIRONMENT": "development"}
            result = subprocess.run(["bash", "-c", common_source() + "\nimage_for auth"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_provider_timeout_after_acceptance_records_structured_accepted_state(self):
        self._run_worker_timeout_fixture(readback="accepted", expect_state="accepted", expect_code=0)

    def test_provider_timeout_with_unknown_readback_records_unknown_state(self):
        self._run_worker_timeout_fixture(readback="unknown", expect_state="unknown", expect_code=None)

    def test_vercel_deployment_readback_requires_each_authoritative_field(self):
        response = {
            "id": "dpl_frontendnew", "url": "frontend-hash.vercel.app", "projectId": "prj_frontendtest",
            "accountId": "team_frontendtest", "readyState": "READY", "target": "preview",
            "meta": {"githubCommitSha": "0123456789abcdef0123456789abcdef01234567", "githubCommitRef": "develop", "githubOrg": "Rayer", "githubRepo": "llm-wiki-cloud"},
        }
        cases = {
            "readyState": lambda value: value.pop("readyState"),
            "target": lambda value: value.pop("target"),
            "source SHA": lambda value: value["meta"].pop("githubCommitSha"),
            "source ref": lambda value: value["meta"].pop("githubCommitRef"),
            "repository": lambda value: (value["meta"].pop("githubOrg"), value["meta"].pop("githubRepo")),
            "project": lambda value: value.pop("projectId"),
            "team": lambda value: value.pop("accountId"),
            "URL": lambda value: value.pop("url"),
        }
        plan = {"normalized": {"frontend": {"repository": "Rayer/llm-wiki-cloud"}}}
        with tempfile.TemporaryDirectory() as directory:
            plan_path = Path(directory) / "plan.json"
            plan_path.write_text(json.dumps(plan))
            for name, remove in cases.items():
                with self.subTest(name=name):
                    value = json.loads(json.dumps(response))
                    remove(value)
                    result = subprocess.run(
                        ["bash", "-c", common_source() + "\nsource \"$ROOT/deploy/components/frontend.sh\"\nvercel_validate_deployment \"$RESPONSE\" dpl_frontendnew https://frontend-hash.vercel.app"],
                        env={**os.environ, "ROOT": str(ROOT), "PLAN_PATH": str(plan_path), "RESPONSE": json.dumps(value), "VERCEL_PROJECT_ID": "prj_frontendtest", "VERCEL_TEAM_ID": "team_frontendtest", "SOURCE_SHA": response["meta"]["githubCommitSha"], "SOURCE_REF": "develop", "ENVIRONMENT": "development"},
                        text=True,
                        capture_output=True,
                    )
                    self.assertNotEqual(result.returncode, 0, name)

    def _run_worker_timeout_fixture(self, readback, expect_state, expect_code):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            sha = "0123456789abcdef0123456789abcdef01234567"
            image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
            artifact_dir = root / "artifacts/dev-images"
            artifact_dir.mkdir(parents=True)
            (artifact_dir / f"worker-image-{sha}.txt").write_text(image + "\n")
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"], "gcp": {"artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images", "project_id": "llm-wiki-cloud"}, "evidence": {"config_fingerprint": "sha256:fingerprint"}, "worker": {"job_name": "olw-pipeline", "location": "asia-east1", "runtime_service_account": "worker@llm-wiki-cloud.iam.gserviceaccount.com", "bucket": "bucket", "args": ["run", "--auto-approve"], "secret_references": {"deepseek_api_key": "deepseek-apikey"}}}}))
            (artifact_dir / "dev-receipt.json").write_text(json.dumps({"schema": "lwc-306-dev-image-receipt-v1", "source": {"sha": sha, "ref": "develop", "workflow_path": ".github/workflows/deploy-dev.yml", "event": "workflow_dispatch", "run_id": 99, "run_attempt": 1}, "config": {"environment": "development", "path": "deploy/environments/development.yaml", "fingerprint": "sha256:fingerprint"}, "components": ["worker"], "images": {"worker": image}}))
            desired = {
                "spec": {"template": {"spec": {"template": {"spec": {
                    "serviceAccountName": "worker@llm-wiki-cloud.iam.gserviceaccount.com",
                    "containers": [{
                        "image": image,
                        "env": [
                            {"name": "BUCKET", "value": "bucket"},
                            {"name": "PIPELINE_JOB_NAME", "value": "olw-pipeline"},
                            {"name": "PIPELINE_JOB_LOCATION", "value": "asia-east1"},
                            {"name": "DEEPSEEK_API_KEY", "valueSource": {"secretKeyRef": {"secret": "deepseek-apikey", "version": "latest"}}},
                        ],
                        "args": ["run", "--auto-approve"],
                    }],
                    "volumes": [],
                }}}}}}
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                args = sys.argv[1:]
                print(" ".join(args), file=open(os.environ["FAKE_LOG"], "a"))
                image = {image!r}
                desired = {desired!r}
                if args[:3] == ["run", "jobs", "update"]:
                    raise SystemExit(28)
                if args[:3] == ["run", "jobs", "describe"]:
                    if {readback!r} == "accepted": print(json.dumps(desired))
                    else: raise SystemExit(1)
                else: raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT": "production", "SOURCE_REF": "main", "SOURCE_SHA": sha, "CONFIG_PATH": "deploy/environments/production.yaml", "COMPONENTS": "worker", "PLAN_PATH": str(plan), "JOURNAL_PATH": str(root / "artifacts/journal.json"), "ARTIFACT_DIR": str(root / "artifacts"), "FAKE_LOG": str(root / "gcloud.log")}
            result = subprocess.run(["bash", str(ROOT / "deploy/components/worker.sh"), "mutate"], env=env, text=True, capture_output=True)
            if expect_code is not None:
                self.assertEqual(result.returncode, expect_code, result.stdout + result.stderr)
            else:
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            journal = json.loads((root / "artifacts/journal.json").read_text())
            self.assertEqual(journal["components"]["worker"]["state"], expect_state)
            self.assertNotIn("run jobs execute", (root / "gcloud.log").read_text())

    def test_local_pre_mutation_rejection_is_journaled_without_provider_call(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            (bin_dir / "gcloud").write_text("#!/usr/bin/env bash\necho called >> \"$PROVIDER_LOG\"\nexit 99\n")
            (bin_dir / "gcloud").chmod(0o755)
            sha = "0123456789abcdef0123456789abcdef01234567"
            artifact_dir = root / "artifacts/dev-images"
            artifact_dir.mkdir(parents=True)
            (artifact_dir / f"worker-image-{sha}.txt").write_text("not-an-immutable-image\n")
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"], "gcp": {"artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images", "project_id": "llm-wiki-cloud"}, "worker": {"job_name": "olw-pipeline", "location": "asia-east1", "runtime_service_account": "worker@llm-wiki-cloud.iam.gserviceaccount.com", "bucket": "bucket", "args": ["run", "--auto-approve"], "secret_references": {"deepseek_api_key": "deepseek-apikey"}}}}))
            provider_log = root / "provider.log"
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT": "production", "SOURCE_REF": "main", "SOURCE_SHA": sha, "PLAN_PATH": str(plan), "JOURNAL_PATH": str(root / "artifacts/journal.json"), "ARTIFACT_DIR": str(root / "artifacts"), "PROVIDER_LOG": str(provider_log)}
            result = subprocess.run(["bash", str(ROOT / "deploy/components/worker.sh"), "mutate"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            journal = json.loads((root / "artifacts/journal.json").read_text())
            self.assertEqual(journal["components"]["worker"]["state"], "rejected_or_no_mutation")
            self.assertFalse(provider_log.exists())

    def test_empty_journal_is_not_needed_and_never_rollback_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            artifact_dir.mkdir()
            (artifact_dir / "journal.json").write_text(json.dumps({"schema": "lwc-306-mutation-journal-v1", "order": [], "components": {}}))
            (artifact_dir / "rollback.json").write_text("{}")
            env = {**os.environ, "PLAN_PATH": str(root / "plan.json"), "ROLLBACK_PATH": str(artifact_dir / "rollback.json"), "JOURNAL_PATH": str(artifact_dir / "journal.json"), "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"), "ARTIFACT_DIR": str(artifact_dir)}
            (root / "plan.json").write_text(json.dumps({"normalized": {"selected_components": []}}))
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            rollback = json.loads((artifact_dir / "rollback-result.json").read_text())
            self.assertEqual(rollback["result"], "not_needed")
            self.assertFalse(rollback["verified"])

    def test_evidence_prefers_verified_rollback_after_journal_is_exhausted(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            artifact_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"]}}))
            env = {
                **os.environ,
                "ROOT": str(ROOT),
                "ENVIRONMENT": "production",
                "SOURCE_SHA": source_sha,
                "PLAN_PATH": str(plan),
                "JOURNAL_PATH": str(artifact_dir / "journal.json"),
                "EVIDENCE_PATH": str(artifact_dir / "readback.json"),
                "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"),
                "FINAL_EVIDENCE_PATH": str(artifact_dir / "evidence.json"),
                "GITHUB_RUN_ATTEMPT": "1",
            }
            journal = subprocess.run(
                [
                    "bash",
                    "-c",
                    f"source {str(ROOT / 'deploy/components/common.sh')!r}; "
                    "journal_init; journal_pending worker; journal_accepted worker; "
                    "journal_transition worker rollback_pending; journal_transition worker rollback_accepted",
                ],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(journal.returncode, 0, journal.stdout + journal.stderr)
            self.assertEqual(
                json.loads((artifact_dir / "journal.json").read_text())["components"]["worker"]["history"],
                ["pending", "accepted", "rollback_pending", "rollback_accepted"],
            )
            rollback = {
                "schema": "lwc-306-rollback-result-v1",
                "result": "success",
                "verified": True,
                "attempted": ["worker"],
                "components": [{"component": "worker", "result": "success", "verified": True}],
            }
            (artifact_dir / "rollback-result.json").write_text(json.dumps(rollback))
            (artifact_dir / "readback.json").write_text(json.dumps({
                "schema": "lwc-306-readback-v1",
                "result": "partial",
                "verified": False,
                "provider_readback": False,
                "mutation_count": 1,
                "mutation_components": ["worker"],
                "components": [{"component": "worker", "result": "failed", "verified": False}],
            }))

            evidence = subprocess.run(
                ["bash", str(ROOT / "deploy/cd.sh"), "evidence"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(evidence.returncode, 0, evidence.stdout + evidence.stderr)
            rendered = json.loads((artifact_dir / "evidence.json").read_text())
            self.assertEqual(rendered["result"], "rolled_back")
            self.assertTrue(rendered["verified"])
            self.assertEqual(rendered["rollback_attempted"], ["worker"])
            self.assertEqual(rendered["rollback_result"], "success")
            self.assertTrue(rendered["rollback_verified"])
            self.assertEqual(rendered["rollback"], rollback)
            self.assertFalse(rendered["partial"])
            self.assertFalse(rendered["unknown"])
            self.assertEqual(rendered["next_action"], "none")

    def test_evidence_preserves_exhausted_rollback_terminal_results(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        for rollback_result, expected in (("failed", "rollback_failed"), ("unknown", "rollback_unknown")):
            with self.subTest(rollback_result=rollback_result), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifact_dir = root / "artifacts"
                artifact_dir.mkdir()
                plan = root / "plan.json"
                plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"]}}))
                (artifact_dir / "journal.json").write_text(json.dumps({
                    "schema": "lwc-306-mutation-journal-v1",
                    "order": ["worker"],
                    "components": {"worker": {
                        "state": "rollback_" + rollback_result,
                        "history": ["pending", "accepted", "rollback_pending", "rollback_" + rollback_result],
                        "timestamp": "2026-09-04T00:00:00Z",
                        "attempt": 1,
                    }},
                }))
                (artifact_dir / "readback.json").write_text(json.dumps({
                    "schema": "lwc-306-readback-v1",
                    "result": "success",
                    "verified": True,
                    "components": [],
                }))
                (artifact_dir / "rollback-result.json").write_text(json.dumps({
                    "schema": "lwc-306-rollback-result-v1",
                    "result": rollback_result,
                    "verified": False,
                    "attempted": ["worker"],
                    "components": [{"component": "worker", "result": rollback_result, "verified": False}],
                }))
                env = {
                    **os.environ,
                    "ENVIRONMENT": "production",
                    "SOURCE_SHA": source_sha,
                    "PLAN_PATH": str(plan),
                    "JOURNAL_PATH": str(artifact_dir / "journal.json"),
                    "EVIDENCE_PATH": str(artifact_dir / "readback.json"),
                    "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"),
                    "FINAL_EVIDENCE_PATH": str(artifact_dir / "evidence.json"),
                }
                evidence = subprocess.run(
                    ["bash", str(ROOT / "deploy/cd.sh"), "evidence"],
                    env=env,
                    text=True,
                    capture_output=True,
                )
                self.assertEqual(evidence.returncode, 0, evidence.stdout + evidence.stderr)
                rendered = json.loads((artifact_dir / "evidence.json").read_text())
                self.assertEqual(rendered["result"], expected)
                self.assertFalse(rendered["verified"])

    def test_nonempty_journal_with_evidence_renderer_failure_is_rollback_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            artifact_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"], "evidence": {"config_fingerprint": "sha256:fingerprint"}}}))
            journal = {"schema": "lwc-306-mutation-journal-v1", "order": ["worker"], "components": {"worker": {"state": "accepted", "history": ["accepted"]}}}
            (artifact_dir / "journal.json").write_text(json.dumps(journal))
            (artifact_dir / "readback.json").write_text("not-json")
            env = {**os.environ, "ENVIRONMENT": "production", "SOURCE_REF": "main", "SOURCE_SHA": "0123456789abcdef0123456789abcdef01234567", "PLAN_PATH": str(plan), "JOURNAL_PATH": str(artifact_dir / "journal.json"), "EVIDENCE_PATH": str(artifact_dir / "readback.json"), "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"), "FINAL_EVIDENCE_PATH": str(artifact_dir / "evidence.json")}
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "evidence"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            evidence = json.loads((artifact_dir / "evidence.json").read_text())
            self.assertIn(evidence["result"], {"rollback_unknown", "rollback_failed"})
            self.assertNotEqual(evidence["rollback"]["result"], "success")

    def test_malformed_nonempty_journal_is_never_treated_as_not_needed(self):
        timestamp = "2026-09-04T00:00:00Z"
        valid = {
            "schema": "lwc-306-mutation-journal-v1", "order": ["worker"],
            "components": {"worker": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": timestamp, "attempt": 1}},
        }
        variants = {
            "unknown key": lambda value: value["components"].update({"rogue": value["components"]["worker"]}),
            "invalid state": lambda value: value["components"]["worker"].update(state="bogus"),
            "invalid history": lambda value: value["components"]["worker"].update(history=["accepted"]),
            "missing timestamp": lambda value: value["components"]["worker"].pop("timestamp"),
            "inconsistent state": lambda value: value["components"]["worker"].update(state="pending"),
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            artifact_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"]}}))
            for name, mutate in variants.items():
                with self.subTest(name=name):
                    journal = json.loads(json.dumps(valid))
                    mutate(journal)
                    journal_path = artifact_dir / "journal.json"
                    journal_path.write_text(json.dumps(journal))
                    rollback_result = artifact_dir / "rollback-result.json"
                    result = subprocess.run(
                        ["bash", str(ROOT / "deploy/cd.sh"), "rollback"],
                        env={**os.environ, "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(artifact_dir / "rollback.json"), "JOURNAL_PATH": str(journal_path), "ROLLBACK_RESULT_PATH": str(rollback_result), "ARTIFACT_DIR": str(artifact_dir)},
                        text=True,
                        capture_output=True,
                    )
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn(json.loads(rollback_result.read_text())["result"], {"unknown", "failed"})

    def test_rollback_runs_accepted_pending_unknown_components_once_in_reverse_order(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifact_dir = root / "artifacts"
            artifact_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker", "frontend"]}}))
            journal = {"schema": "lwc-306-mutation-journal-v1", "order": ["worker", "frontend"], "components": {"worker": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}, "frontend": {"state": "unknown", "history": ["pending", "unknown"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}}}
            (artifact_dir / "journal.json").write_text(json.dumps(journal))
            (artifact_dir / "rollback.json").write_text("{}")
            log = root / "rollback.log"
            env = {**os.environ, "ENVIRONMENT": "production", "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(artifact_dir / "rollback.json"), "JOURNAL_PATH": str(artifact_dir / "journal.json"), "ROLLBACK_RESULT_PATH": str(artifact_dir / "rollback-result.json"), "ARTIFACT_DIR": str(artifact_dir), "ROLLBACK_LOG": str(log)}
            cd_source = (ROOT / "deploy/cd.sh").read_text()
            functions = cd_source[cd_source.index("component_script()"):cd_source.index("\ncase ")]
            override = textwrap.dedent(f"""
                ROOT={str(ROOT)!r}
                source {str(ROOT / 'deploy/components/common.sh')!r}
                {functions}
                run_component() {{
                  printf '%s\\n' "$1" >> "$ROLLBACK_LOG"
                  mkdir -p "$ARTIFACT_DIR/rollback"
                  jq -n --arg component "$1" '{{component:$component,result:"success",verified:true,readback:{{}}}}' > "$(rollback_result_path "$1")"
                }}
                rollback
            """)
            result = subprocess.run(["bash", "-c", override], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(log.read_text().splitlines(), ["frontend", "worker"])
            rollback = json.loads((artifact_dir / "rollback-result.json").read_text())
            self.assertEqual([row["component"] for row in rollback["components"]], ["frontend", "worker"])

    def test_cd_uses_vercel_build_and_bounded_rest_alias_mutation(self):
        source = source_bundle("frontend")
        self.assertIn("vercel build", source)
        self.assertIn("--prebuilt", source)
        self.assertIn("/v2/deployments/", source)
        self.assertNotIn("vercel alias set", source)

    def test_mutation_accepted_is_exact_once_and_deterministic(self):
        helper = common_source()
        with tempfile.TemporaryDirectory() as directory:
            journal = Path(directory) / "journal.json"
            plan = Path(directory) / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["auth", "worker"]}}))
            env = {**os.environ, "JOURNAL_PATH": str(journal), "PLAN_PATH": str(plan)}
            accepted = subprocess.run(
                ["bash", "-c", helper + "\nmutation_accepted auth\nmutation_accepted worker"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertEqual(accepted.returncode, 0, accepted.stderr)
            journal_data = json.loads(journal.read_text())
            self.assertEqual(journal_data["order"], ["auth", "worker"])
            self.assertEqual(journal_data["components"]["auth"]["state"], "accepted")
            self.assertEqual(journal_data["components"]["worker"]["state"], "accepted")
            duplicate = subprocess.run(
                ["bash", "-c", helper + "\nmutation_accepted auth"],
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(duplicate.returncode, 0)
            self.assertEqual(json.loads(journal.read_text())["components"]["auth"]["state"], "accepted")

    def test_freeze_extracts_only_immutable_backend_handles_from_live_provider_shapes(self):
        source_sha = "0123456789abcdef0123456789abcdef01234567"
        registry = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"
        images = {
            "auth": f"{registry}/llm-wiki-auth@sha256:{'a' * 64}",
            "bff": f"{registry}/llm-wiki-bff@sha256:{'b' * 64}",
            "worker": f"{registry}/olw-pipeline@sha256:{'c' * 64}",
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["auth", "bff", "worker"],
                    "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": registry},
                    "auth": {"service_name": "auth-service"},
                    "bff": {"service_name": "bff-service"},
                    "worker": {"job_name": "worker-job", "location": "asia-east1"},
                }
            }))
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                args = sys.argv[1:]
                services = {{
                    "auth-service": {{"metadata": {{"name": "auth-service", "annotations": {{"run.googleapis.com/launch-stage": "GA"}}}}, "status": {{"traffic": [{{"revisionName": "auth-old", "percent": 100}}]}}}},
                    "bff-service": {{"metadata": {{"name": "bff-service", "annotations": {{"run.googleapis.com/launch-stage": "GA"}}}}, "status": {{"traffic": [{{"revisionName": "bff-old", "percent": 100}}]}}}},
                }}
                revisions = {{
                    "auth-old": {{"metadata": {{"name": "auth-old"}}, "spec": {{"containers": [{{"name": "provider-generated", "image": {images['auth']!r}, "startupProbe": {{"tcpSocket": {{"port": 8080}}}}}}]}}, "status": {{"imageDigest": {images['auth']!r}}}}},
                    "bff-old": {{"metadata": {{"name": "bff-old"}}, "spec": {{"containers": [{{"name": "provider-generated", "image": {images['bff']!r}, "startupProbe": {{"tcpSocket": {{"port": 8080}}}}}}]}}, "status": {{"imageDigest": {images['bff']!r}}}}},
                }}
                if args[:3] == ["run", "services", "describe"]:
                    print(json.dumps(services[args[3]]))
                elif args[:3] == ["run", "revisions", "describe"]:
                    print(json.dumps(revisions[args[3]]))
                elif args[:3] == ["run", "jobs", "describe"]:
                    print(json.dumps({{
                        "apiVersion": "run.googleapis.com/v1", "kind": "Job",
                        "metadata": {{"name": "worker-job", "generation": 9, "etag": "live-etag", "uid": "provider-uid"}},
                        "spec": {{"template": {{"spec": {{"template": {{"spec": {{"containers": [{{"name": "provider-generated", "image": {images['worker']!r}, "startupProbe": {{"tcpSocket": {{"port": 8080}}}}}}]}}}}}}}}}}
                    }}))
                else:
                    raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            rollback = root / "rollback.json"
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT),
                "ENVIRONMENT": "development", "SOURCE_SHA": source_sha,
                "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(rollback),
            }
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "freeze"], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            document = json.loads(rollback.read_text())
            self.assertEqual(
                document["handles"],
                {component: {"image": image} for component, image in images.items()},
            )

    def test_backend_mutation_paths_are_image_only_and_reject_mutable_refs(self):
        forbidden = (
            "gcloud run deploy", "--service-account", "--network", "--subnet", "--vpc-egress",
            "--ingress", "--max", "--update-env-vars", "--update-secrets", "--remove-env-vars",
            "--args", "--clear-volume-mounts", "--clear-volumes",
        )
        for component, next_function, command in (
            ("auth", "auth_verify", "gcloud run services update"),
            ("bff", "bff_verify", "gcloud run services update"),
            ("worker", "worker_verify", "gcloud run jobs update"),
        ):
            source = (ROOT / "deploy/components" / f"{component}.sh").read_text()
            body = source[source.index(f"{component}_mutate() {{"):source.index(f"\n{next_function}() {{")]
            self.assertIn(command, body)
            self.assertIn('--image "$image"', body)
            self.assertIn(f"validate_image_value {component} \"$image\"", body)
            if component in ("auth", "bff"):
                self.assertIn("gcloud run services update-traffic", body)
            for marker in forbidden:
                self.assertNotIn(marker, body, f"{component} mutation contains {marker}")

        plan = {"normalized": {"gcp": {"artifact_registry": "registry.example/images"}}}
        with tempfile.TemporaryDirectory() as directory:
            plan_path = Path(directory) / "plan.json"
            plan_path.write_text(json.dumps(plan))
            for component, image in (
                ("auth", "registry.example/images/llm-wiki-auth:latest"),
                ("bff", "registry.example/images/llm-wiki-bff:release"),
                ("worker", "registry.example/images/olw-pipeline"),
            ):
                result = subprocess.run(
                    ["bash", "-c", common_source() + f'\nvalidate_image_value {component} "$IMAGE"'],
                    env={**os.environ, "ROOT": str(ROOT), "PLAN_PATH": str(plan_path), "IMAGE": image},
                    text=True,
                    capture_output=True,
                )
                self.assertNotEqual(result.returncode, 0, f"mutable {component} image was accepted")

    def test_backend_rollbacks_use_only_retained_immutable_image_handles(self):
        forbidden = (".revision", ".readback", ".definition", "normalize_service_readback", "normalize_worker", "gcloud run jobs replace")
        for component, command in (
            ("auth", "gcloud run services update"),
            ("bff", "gcloud run services update"),
            ("worker", "gcloud run jobs update"),
        ):
            source = (ROOT / "deploy/components" / f"{component}.sh").read_text()
            start = source.index(f"{component}_rollback() {{")
            end = source.index("\n}\n", start) + 3
            body = source[start:end]
            self.assertIn(f".handles.{component}.image", body)
            self.assertIn(command, body)
            self.assertIn('--image "$image"', body)
            for marker in forbidden:
                self.assertNotIn(marker, body, f"{component} rollback contains {marker}")

        workflow = (ROOT / ".github/workflows/cd.yml").read_text()
        mutation = workflow[workflow.index("      - name: Mutate Auth"):]
        self.assertIn("steps.rollback_upload.outcome == 'success'", mutation)
        self.assertNotIn("normalize_service_readback", mutation)
        self.assertNotIn("normalize_worker_definition", mutation)


    def test_readback_and_rollback_are_truthful_and_handle_based(self):
        source = source_bundle("auth", "bff", "worker", "frontend")
        for marker in ("service_image_handle", "service_image_readback", "worker_image_handle", "worker_image_readback", "rollback_result"):
            self.assertIn(marker, source)
        for forbidden in ("normalize_service_readback", "normalize_worker_definition", "gcloud run jobs replace", ".handles.worker.definition"):
            self.assertNotIn(forbidden, source)

    def test_service_readback_checks_only_effective_image_and_readiness(self):
        source = source_bundle("auth", "bff")
        self.assertIn("service_image_readback", source)
        self.assertIn("Ready", source)
        for forbidden in ("service_expected", "normalize_service_readback", "component_config"):
            self.assertNotIn(forbidden, source)

    def test_worker_rollback_uses_only_the_retained_image_handle(self):
        worker = (ROOT / "deploy/components/worker.sh").read_text()
        rollback = worker[worker.index("worker_rollback()"):worker.index("\n}\n\ncase", worker.index("worker_rollback()"))]
        self.assertIn(".handles.worker.image", rollback)
        self.assertIn("worker_image_readback", rollback)
        self.assertIn("gcloud run jobs update", rollback)
        for forbidden in (".handles.worker.definition", "normalize_worker_definition", "worker_provider_state", "gcloud run jobs replace", "--update-env-vars", "--update-secrets", "--args"):
            self.assertNotIn(forbidden, rollback)
    def test_evidence_reports_actual_mutation_and_rollback_state(self):
        source = source_bundle()
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


    def test_evidence_contains_no_provider_config_snapshot(self):
        source = source_bundle("auth", "bff", "worker", "frontend")
        self.assertIn("redact_evidence", source)
        self.assertIn("<redacted>", source)
        final = (ROOT / "deploy/cd.sh").read_text()[(ROOT / "deploy/cd.sh").read_text().index("\nevidence() {") :]
        self.assertNotIn(".value", final)

    def test_service_image_readback_rejects_wrong_image_or_unready_revision(self):
        image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth@sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["auth"],
                    "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "auth": {"service_name": "auth-service"},
                }
            }))
            revision = {
                "metadata": {"name": "auth-new", "uid": "provider-uid"},
                "spec": {"containers": [{"name": "provider-generated", "image": image, "startupProbe": {"provider": "default"}}]},
                "status": {"imageDigest": image, "conditions": [{"type": "Ready", "status": "True"}]},
            }
            service = {"metadata": {"name": "auth-service", "annotations": {"provider": "live"}}, "status": {"traffic": [{"revisionName": "auth-new", "percent": 100}]}}
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                args = sys.argv[1:]
                if args[:3] == ["run", "services", "describe"]:
                    print(json.dumps({service!r}))
                elif args[:3] == ["run", "revisions", "describe"]:
                    value = {revision!r}
                    if os.environ.get("FAKE_UNREADY"):
                        value["status"]["conditions"][0]["status"] = "False"
                    print(json.dumps(value))
                else:
                    raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "PLAN_PATH": str(plan), "IMAGE": image}
            command = ["bash", "-c", common_source() + '\nservice_image_readback auth "$IMAGE"']
            valid = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(valid.returncode, 0, valid.stdout + valid.stderr)
            self.assertEqual(json.loads(valid.stdout)["image"], image)

            env["FAKE_UNREADY"] = "1"
            unready = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertNotEqual(unready.returncode, 0, unready.stdout + unready.stderr)




    def test_worker_image_readback_accepts_live_v1_shape_and_rejects_mismatch(self):
        image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            plan = root / "plan.json"
            plan.write_text(json.dumps({
                "normalized": {
                    "selected_components": ["worker"],
                    "gcp": {"project_id": "llm-wiki-cloud", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"},
                    "worker": {"job_name": "worker-job", "location": "asia-east1"},
                }
            }))
            job = {"apiVersion": "run.googleapis.com/v1", "kind": "Job", "metadata": {"name": "worker-job", "generation": 9, "etag": "live-etag"}, "spec": {"template": {"spec": {"template": {"spec": {"containers": [{"name": "provider-generated", "image": image, "startupProbe": {"provider": "default"}}]}}}}}}
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, sys
                if sys.argv[1:4] == ["run", "jobs", "describe"]:
                    print(json.dumps({job!r}))
                else:
                    raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "PLAN_PATH": str(plan), "IMAGE": image}
            valid = subprocess.run(["bash", "-c", common_source() + '\nworker_image_readback "$IMAGE"'], env=env, text=True, capture_output=True)
            self.assertEqual(valid.returncode, 0, valid.stdout + valid.stderr)
            wrong = subprocess.run(["bash", "-c", common_source() + '\nworker_image_readback "$IMAGE-wrong"'], env=env, text=True, capture_output=True)
            self.assertNotEqual(wrong.returncode, 0, wrong.stdout + wrong.stderr)
    def test_worker_iam_preflight_rejects_broad_role_on_runtime_principal(self):
        policy = {"bindings": [
            {"role": "roles/run.viewer", "members": ["serviceAccount:worker@example.com"]},
            {"role": "roles/owner", "members": ["serviceAccount:worker@example.com"]},
            {"role": "roles/logging.logWriter", "members": ["serviceAccount:managed@example.com"]},
        ]}
        result = subprocess.run(
            ["bash", "-c", common_source() + '\niam_binding_is_exact "$POLICY" roles/run.viewer serviceAccount:worker@example.com'],
            env={**os.environ, "ROOT": str(ROOT), "POLICY": json.dumps(policy)},
            text=True,
            capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        public_policy = {"bindings": [
            {"role": "roles/run.invoker", "members": ["allUsers"]},
            {"role": "roles/logging.logWriter", "members": ["serviceAccount:managed@example.com"]},
        ]}
        public = subprocess.run(
            ["bash", "-c", common_source() + '\niam_binding_is_exact "$POLICY" roles/run.invoker allUsers'],
            env={**os.environ, "ROOT": str(ROOT), "POLICY": json.dumps(public_policy)},
            text=True,
            capture_output=True,
        )
        self.assertEqual(public.returncode, 0, public.stdout + public.stderr)

    def test_iam_rejection_is_scoped_to_controlled_principal_and_dangerous_public_grants(self):
        positive_policy = {"bindings": [
            {"role": "roles/run.viewer", "members": ["serviceAccount:worker@example.com"]},
            {"role": "roles/viewer", "members": ["group:domain-auth@example.com", "allAuthenticatedUsers"]},
        ]}
        positive = subprocess.run(
            ["bash", "-c", common_source() + '\niam_binding_is_exact "$POLICY" roles/run.viewer serviceAccount:worker@example.com'],
            env={**os.environ, "ROOT": str(ROOT), "POLICY": json.dumps(positive_policy)}, text=True, capture_output=True,
        )
        self.assertEqual(positive.returncode, 0, positive.stdout + positive.stderr)
        for policy in (
            {"bindings": [{"role": "roles/run.viewer", "members": ["serviceAccount:worker@example.com"]}, {"role": "roles/editor", "members": ["serviceAccount:unrelated@example.com"]}, {"role": "roles/owner", "members": ["allUsers"]}]},
            {"bindings": [{"role": "roles/run.viewer", "members": ["serviceAccount:worker@example.com"]}, {"role": "roles/run.admin", "members": ["serviceAccount:worker@example.com"]}]},
        ):
            negative = subprocess.run(
                ["bash", "-c", common_source() + '\niam_binding_is_exact "$POLICY" roles/run.viewer serviceAccount:worker@example.com'],
                env={**os.environ, "ROOT": str(ROOT), "POLICY": json.dumps(policy)}, text=True, capture_output=True,
            )
            self.assertNotEqual(negative.returncode, 0)

    def test_secret_iam_preflight_accepts_shared_specific_binding_without_relaxing_safety(self):
        cases = [
            (
                "live shared secret binding",
                {"bindings": [{"role": "roles/secretmanager.secretAccessor", "members": [
                    "serviceAccount:lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com",
                    "serviceAccount:lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com",
                ]}]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com",
                True,
            ),
            (
                "worker duplicate requested member across exact bindings",
                {"bindings": [
                    {"role": "roles/run.viewer", "members": [
                        "serviceAccount:worker@example.com",
                    ]},
                    {"role": "roles/run.viewer", "members": [
                        "serviceAccount:worker@example.com", "serviceAccount:another-worker@example.com",
                    ]},
                ]},
                "roles/run.viewer",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "conditioned same-role duplicate requested member",
                {"bindings": [
                    {"role": "roles/run.viewer", "members": [
                        "serviceAccount:worker@example.com",
                    ]},
                    {"role": "roles/run.viewer", "condition": {"title": "temporary", "expression": "true"}, "members": [
                        "serviceAccount:worker@example.com", "user:alice@example.com",
                    ]},
                ]},
                "roles/run.viewer",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "worker shared specific binding",
                {"bindings": [{"role": "roles/run.viewer", "members": [
                    "serviceAccount:worker@example.com",
                    "serviceAccount:another-worker@example.com",
                ]}]},
                "roles/run.viewer",
                "serviceAccount:worker@example.com",
                True,
            ),
            (
                "conditioned requested binding",
                {"bindings": [{"role": "roles/secretmanager.secretAccessor", "condition": {}, "members": [
                    "serviceAccount:worker@example.com",
                ]}]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "missing requested member",
                {"bindings": [{"role": "roles/secretmanager.secretAccessor", "members": [
                    "serviceAccount:another@example.com",
                ]}]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "broad member in sensitive binding",
                {"bindings": [{"role": "roles/secretmanager.secretAccessor", "members": [
                    "serviceAccount:worker@example.com", "allUsers",
                ]}]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "requested member in different sensitive role",
                {"bindings": [
                    {"role": "roles/secretmanager.secretAccessor", "members": ["serviceAccount:worker@example.com"]},
                    {"role": "roles/owner", "members": ["serviceAccount:worker@example.com"]},
                ]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:worker@example.com",
                False,
            ),
            (
                "public invoker remains exact",
                {"bindings": [{"role": "roles/run.invoker", "members": ["allUsers"]}]},
                "roles/run.invoker",
                "allUsers",
                True,
            ),
            (
                "malformed member shape",
                {"bindings": [{"role": "roles/secretmanager.secretAccessor", "members": [
                    {"member": "serviceAccount:worker@example.com"},
                ]}]},
                "roles/secretmanager.secretAccessor",
                "serviceAccount:worker@example.com",
                False,
            ),
        ]
        for name, policy, role, member, valid in cases:
            with self.subTest(case=name):
                result = subprocess.run(
                    ["bash", "-c", common_source() + "\niam_binding_is_exact \"$POLICY\" \"$ROLE\" \"$MEMBER\""],
                    env={
                        **os.environ,
                        "ROOT": str(ROOT),
                        "POLICY": json.dumps(policy),
                        "ROLE": role,
                        "MEMBER": member,
                    },
                    text=True,
                    capture_output=True,
                )
                self.assertEqual(result.returncode == 0, valid, result.stdout + result.stderr)


class ArchitectureAuthorityTests(unittest.TestCase):
    COMPONENTS = ("auth", "bff", "worker", "frontend")

    def test_each_component_has_a_real_independent_action_boundary(self):
        orchestrator = (ROOT / ".github/workflows/cd.yml").read_text()
        self.assertLessEqual(len(orchestrator.splitlines()), 360)
        for component in self.COMPONENTS:
            action_path = ROOT / ".github/actions" / component / "action.yml"
            script_path = ROOT / "deploy/components" / f"{component}.sh"
            self.assertTrue(action_path.is_file(), f"missing {action_path}")
            self.assertTrue(script_path.is_file(), f"missing {script_path}")
            action = yaml.safe_load(action_path.read_text())
            self.assertEqual(action["runs"]["using"], "composite")
            action_source = action_path.read_text()
            self.assertIn(f"deploy/components/{component}.sh", action_source)
            script = script_path.read_text()
            for operation in ("preflight", "freeze", "mutate", "reconcile", "rollback"):
                self.assertRegex(script, rf"\b{operation}\b")
            self.assertIn(f"uses: ./.github/actions/{component}", orchestrator)
        for forbidden in (
            "gcloud run deploy",
            "gcloud run jobs update",
            "vercel deploy --prebuilt",
            'case "$component" in',
        ):
            self.assertNotIn(forbidden, (ROOT / "deploy/cd.sh").read_text())

    def test_component_actions_are_invocable_without_backend_receipts(self):
        for component in self.COMPONENTS:
            script = ROOT / "deploy/components" / f"{component}.sh"
            result = subprocess.run(
                ["bash", str(script), "help"],
                cwd=ROOT,
                text=True,
                capture_output=True,
            )
            self.assertEqual(result.returncode, 0, f"{component}: {result.stderr}")
        frontend = (ROOT / "deploy/components/frontend.sh").read_text()
        self.assertNotIn("consume_dev_images", frontend)
        self.assertNotRegex(frontend, r"image_for (auth|bff|worker)")

    def test_wrappers_require_explicit_components_and_inherit_secrets(self):
        for filename in ("deploy-dev.yml", "promote-production.yml"):
            source = (ROOT / ".github/workflows" / filename).read_text()
            self.assertRegex(source, r"components:\n\s+description:.*\n\s+required: true")
            self.assertNotIn("default:", source)
            self.assertNotIn("inputs.components ||", source)
            self.assertIn("\n    secrets: inherit", source)
            self.assertNotRegex(source, r"\$\{\{\s*secrets\.")

    def test_reusable_workflow_validates_the_complete_fixed_tuple_before_protected_environment(self):
        source = (ROOT / ".github/workflows/cd.yml").read_text()
        plan = source[source.index("  plan:"):source.index("  mutate:")]
        self.assertNotIn("environment:", plan)
        self.assertIn("DEPLOYMENT_ENVIRONMENT:", plan)
        contract = source_bundle()
        self.assertIn("environment and config/ref tuple", contract)
        self.assertIn("DEPLOYMENT_ENVIRONMENT", contract)

    def test_production_is_serialized_without_a_shared_development_lock(self):
        production = (ROOT / ".github/workflows/promote-production.yml").read_text()
        self.assertIn("concurrency:", production)
        self.assertRegex(production, r"cancel-in-progress:\s*false")
        development = (ROOT / ".github/workflows/deploy-dev.yml").read_text()
        self.assertNotIn("group: production", development)

    def test_all_third_party_workflow_actions_are_immutable_sha_pinned(self):
        pattern = re.compile(r"uses:\s+([^\s@]+)@([^\s#]+)")
        for path in (ROOT / ".github/workflows").glob("*.yml"):
            for action, ref in pattern.findall(path.read_text()):
                if action.startswith("./"):
                    continue
                self.assertRegex(ref, r"^[0-9a-f]{40}$", f"mutable action in {path}: {action}@{ref}")

    def test_frontend_aliases_are_read_from_normalized_yaml_plan_only(self):
        production_shell = "\n".join(
            path.read_text()
            for path in [ROOT / "deploy/cd.sh", *sorted((ROOT / "deploy/components").glob("*.sh"))]
        ) if (ROOT / "deploy/components").is_dir() else (ROOT / "deploy/cd.sh").read_text()
        for alias in ("wiki.dev.rayer.idv.tw", "wiki.rayer.idv.tw", "llm-wiki-frontend.vercel.app"):
            self.assertNotIn(alias, production_shell)
        self.assertIn(".normalized.frontend.stable_aliases", production_shell)

    def test_frontend_only_does_not_require_backend_receipt_download_in_the_orchestrator(self):
        source = (ROOT / "deploy/cd.sh").read_text()
        mutate = source[source.index("mutate() {"):source.index("\nreconcile()")]
        self.assertRegex(
            mutate,
            r"if[^\n]*production[\s\S]{0,180}has_component auth[\s\S]{0,120}has_component bff[\s\S]{0,120}has_component worker[\s\S]{0,120}consume_dev_images",
        )
        reconcile = source[source.index("\nreconcile() {"):source.index("\naggregate_reconcile()")]
        self.assertNotIn("consume_dev_images", reconcile)


    def test_fake_worker_rollback_updates_only_the_retained_image(self):
        old_image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "a" * 64
        new_image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            state = root / "job.json"
            container = {"name": "provider-generated", "image": old_image, "startupProbe": {"provider": "default"}}
            state_value = {
                "apiVersion": "run.googleapis.com/v1",
                "kind": "Job",
                "metadata": {"name": "worker-job", "uid": "provider-uid", "generation": 9, "etag": "live-etag"},
                "spec": {"template": {"spec": {"template": {"spec": {"containers": [container]}}}}},
            }
            state.write_text(json.dumps(state_value))
            plan = root / "plan.json"
            plan.write_text(json.dumps({"normalized": {"selected_components": ["worker"], "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"}, "worker": {"job_name": "worker-job", "location": "asia-east1"}}}))
            log = root / "provider.log"
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                from pathlib import Path
                args = sys.argv[1:]
                state = Path({str(state)!r})
                Path({str(log)!r}).open("a").write(" ".join(args) + "\\n")
                if args[:3] == ["run", "jobs", "describe"]:
                    print(state.read_text(), end="")
                elif args[:3] == ["run", "jobs", "update"]:
                    forbidden = {["--update-env-vars", "--update-secrets", "--service-account", "--args", "--clear-volumes", "--clear-volume-mounts"]!r}
                    if any(flag in args for flag in forbidden) or "execute" in args:
                        raise SystemExit(91)
                    value = json.loads(state.read_text())
                    value["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["image"] = args[args.index("--image") + 1]
                    state.write_text(json.dumps(value))
                else:
                    raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            journal = root / "journal.json"
            rollback = root / "rollback.json"
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ROOT": str(ROOT), "ENVIRONMENT": "development", "SOURCE_REF": "develop", "SOURCE_SHA": "0123456789abcdef0123456789abcdef01234567", "CONFIG_PATH": "deploy/environments/development.yaml", "COMPONENTS": "worker", "GITHUB_REF": "refs/heads/develop", "GITHUB_REF_NAME": "develop", "PLAN_PATH": str(plan), "ROLLBACK_PATH": str(rollback), "JOURNAL_PATH": str(journal), "ROLLBACK_RESULT_PATH": str(root / "rollback-result.json"), "ARTIFACT_DIR": str(root / "artifacts")}
            frozen = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "freeze"], env=env, text=True, capture_output=True)
            self.assertEqual(frozen.returncode, 0, frozen.stdout + frozen.stderr)
            self.assertEqual(json.loads(rollback.read_text())["handles"]["worker"], {"image": old_image})
            current = json.loads(state.read_text())
            current["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["image"] = new_image
            state.write_text(json.dumps(current))
            journal.write_text(json.dumps({"schema": "lwc-306-mutation-journal-v1", "order": ["worker"], "components": {"worker": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}}}))
            rolled_back = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertEqual(rolled_back.returncode, 0, rolled_back.stdout + rolled_back.stderr)
            self.assertEqual(json.loads(state.read_text())["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["image"], old_image)
            self.assertEqual(json.loads((root / "rollback-result.json").read_text())["result"], "success")
            calls = log.read_text().splitlines()
            self.assertEqual(sum("run jobs update" in call for call in calls), 1)
            self.assertFalse(any("execute" in call for call in calls))


if __name__ == "__main__":
    unittest.main()
