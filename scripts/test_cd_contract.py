#!/usr/bin/env python3
"""LWC-306 characterization and regression tests for the CD contract."""

import json
import os
import re
import subprocess
import tempfile
import textwrap
import unittest
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
        first_mutation = source.index("operation: mutate", mutation)
        self.assertLess(rollback_upload, first_mutation)
        self.assertIn("steps.rollback_upload.outcome == 'success'", source)
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

    def test_default_ci_runs_retained_legacy_python_suites(self):
        source = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn("python3 -m unittest discover -s scripts -p 'test_*.py'", source)

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
                import json, sys
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
                ["bash", "-c", "source deploy/cd.sh; consume_dev_images"],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
            )
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

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

    def test_readback_and_rollback_are_truthful_and_complete(self):
        source = source_bundle("auth", "bff", "worker", "frontend")
        self.assertNotIn("provider_readback:true", source)
        self.assertNotIn("verified:true", source)
        self.assertIn("service_account", source)
        self.assertIn("secret_references", source)
        worker = (ROOT / "deploy/components/worker.sh").read_text()
        self.assertIn("gcloud run jobs replace", worker)
        self.assertIn("handles.worker.definition", worker)
        self.assertIn("rollback_result", source)

    def test_service_readback_compares_the_allowlisted_runtime_definition(self):
        verify = source_bundle("auth", "bff")
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
        worker = (ROOT / "deploy/components/worker.sh").read_text()
        rollback = worker[worker.index("worker_rollback()"):worker.index("\n}\n\ncase", worker.index("worker_rollback()"))]
        source = common_source() + worker
        for marker in (
            "worker.definition",
            "normalize_worker_definition",
            "verify_worker_definition",
            "gcloud run jobs replace",
            "write_rollback_result",
            "secret_references",
        ):
            self.assertIn(marker, source, f"Worker rollback is missing {marker}")
        self.assertNotIn("--clear-volume-mounts", rollback)
        self.assertNotIn('update-env-vars \"BUCKET=$(jq', rollback)

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

    def test_evidence_redacts_secret_values_and_never_serializes_provider_env(self):
        source = source_bundle("auth", "bff", "worker", "frontend")
        self.assertIn("redact", source)
        self.assertIn("secret_references", source)
        self.assertIn("valueSource", source)
        self.assertIn("<redacted>", source)
        final = (ROOT / "deploy/cd.sh").read_text()[(ROOT / "deploy/cd.sh").read_text().index("\nevidence() {") :]
        self.assertNotIn(".value", final)

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
                    if any(arg.startswith("--format=value(") for arg in args): print("auth-new")
                    else: print(json.dumps(service))
                elif args[:3] == ["run", "revisions", "describe"]: print(json.dumps(revision))
                else: raise SystemExit(2)
                """
            ).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT":"development", "SOURCE_REF":"develop", "SOURCE_SHA":"0123456789abcdef0123456789abcdef01234567", "CONFIG_PATH":"deploy/environments/development.yaml", "COMPONENTS":"auth", "GITHUB_REF":"refs/heads/develop", "GITHUB_REF_NAME":"develop", "PLAN_PATH":str(root / "plan.json"), "JOURNAL_PATH":str(root / "artifacts/journal.json"), "ARTIFACT_DIR":str(root / "artifacts"), "EVIDENCE_PATH":str(root / "artifacts/readback.json"), "ROLLBACK_RESULT_PATH":str(root / "artifacts/rollback-result.json"), "FINAL_EVIDENCE_PATH":str(root / "artifacts/evidence.json")}
            (root / "artifacts/journal.json").write_text(json.dumps({"schema": "lwc-306-mutation-journal-v1", "order": ["auth"], "components": {}}))
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "reconcile"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            evidence = json.loads((root / "artifacts/readback.json").read_text())
            self.assertEqual(evidence["result"], "unknown")
            self.assertFalse(evidence["provider_readback"])
            final = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "evidence"], env=env, text=True, capture_output=True)
            self.assertEqual(final.returncode, 0, final.stderr)
            document = (root / "artifacts/evidence.json").read_text()
            self.assertNotIn("super-secret-value", document)
            self.assertIn("no automatic provider retry", document)
        finally:
            import shutil
            shutil.rmtree(root, ignore_errors=True)

    def test_fake_service_readback_accepts_valid_shape_and_rejects_extra_behavior_env_entry(self):
        config = self.load_config("development")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            sha = "0123456789abcdef0123456789abcdef01234567"
            image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-auth@sha256:" + "a" * 64
            artifact_dir = root / "artifacts"
            (artifact_dir / "images").mkdir(parents=True)
            (artifact_dir / "images" / f"auth-image-{sha}.txt").write_text(image + "\n")
            expected_env = [
                {"name": "GCP_PROJECT", "value": "llm-wiki-cloud"},
                {"name": "FIRESTORE_DATABASE_ID", "value": "llm-wiki-cloud-dev"},
                {"name": "ALLOWED_ORIGINS", "value": ",".join(config["auth"]["allowed_origins"])},
                {"name": "ALLOWED_HOSTS", "value": ",".join(config["auth"]["allowed_hosts"])},
                {"name": "DEV_JWT", "value": "false"},
                {"name": "LWC_SOURCE_COMMIT", "value": sha},
                {"name": "JWT_SECRET", "valueSource": {"secretKeyRef": {"secret": "jwt-secret-dev", "version": "latest"}}},
            ]
            service = {
                "metadata": {"name": "llm-wiki-auth-dev", "annotations": {"run.googleapis.com/ingress": "all"}},
                "spec": {"template": {"metadata": {"annotations": {
                    "run.googleapis.com/network-interfaces": '[{"network":"default","subnetwork":"default"}]',
                    "run.googleapis.com/vpc-access-egress": "private-ranges-only",
                    "autoscaling.knative.dev/maxScale": "1",
                }}, "spec": {"serviceAccountName": config["auth"]["runtime_service_account"], "containers": [{"env": expected_env}]}}},
                "status": {"traffic": [{"revisionName": "auth-new", "percent": 100}]},
            }
            revision = {
                "metadata": {"name": "auth-new"},
                "spec": {"serviceAccountName": config["auth"]["runtime_service_account"], "containers": [{"image": image, "env": expected_env}]},
                "status": {"imageDigest": image, "conditions": [{"type": "Ready", "status": "True"}]},
            }
            fake = textwrap.dedent(f"""
                #!/usr/bin/env python3
                import json, os, sys
                args = sys.argv[1:]
                print(" ".join(args), file=open(os.environ["FAKE_LOG"], "a"))
                service = {service!r}
                revision = {revision!r}
                if os.environ.get("FAKE_EXTRA") == "1":
                    extra = {{"name": "UNEXPECTED_BEHAVIOR", "value": "must-be-rejected"}}
                    service["spec"]["template"]["spec"]["containers"][0]["env"].append(extra)
                    revision["spec"]["containers"][0]["env"].append(extra)
                if args[:3] == ["run", "services", "describe"]:
                    if any(arg.startswith("--format=value(") for arg in args): print("auth-new")
                    else: print(json.dumps(service))
                elif args[:3] == ["run", "revisions", "describe"]:
                    print(json.dumps(revision))
                else:
                    raise SystemExit(2)
            """).lstrip()
            fake_path = bin_dir / "gcloud"
            fake_path.write_text(fake)
            fake_path.chmod(0o755)
            plan = {
                "normalized": {
                    "selected_components": ["auth"], "gcp": config["gcp"], "auth": config["auth"],
                    "query_config": {"runtime_path": "/app/configs/query/dev/query-dev-2026-08-31.1.json"},
                    "evidence": {"config_fingerprint": "sha256:fixture"},
                }
            }
            plan_path = root / "plan.json"
            plan_path.write_text(json.dumps(plan))
            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}", "ENVIRONMENT": "development", "SOURCE_REF": "develop",
                "SOURCE_SHA": sha, "CONFIG_PATH": "deploy/environments/development.yaml", "COMPONENTS": "auth",
                "PLAN_PATH": str(plan_path), "ARTIFACT_DIR": str(artifact_dir), "JOURNAL_PATH": str(artifact_dir / "journal.json"),
                "FAKE_LOG": str(root / "gcloud.log"),
            }
            valid = subprocess.run(["bash", str(ROOT / "deploy/components/auth.sh"), "reconcile"], env=env, text=True, capture_output=True)
            self.assertEqual(valid.returncode, 0, valid.stdout + valid.stderr)
            extra = subprocess.run(["bash", str(ROOT / "deploy/components/auth.sh"), "reconcile"], env={**env, "FAKE_EXTRA": "1"}, text=True, capture_output=True)
            self.assertNotEqual(extra.returncode, 0, extra.stdout + extra.stderr)
            component = json.loads((artifact_dir / "components/auth.json").read_text())
            self.assertNotEqual(component["result"], "success")

    def test_worker_readback_rejects_extra_behavior_env_entry(self):
        image = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/olw-pipeline@sha256:" + "b" * 64
        definition = {
            "apiVersion": "run.googleapis.com/v1",
            "kind": "Job",
            "metadata": {"name": "olw-pipeline"},
            "spec": {"template": {"spec": {"template": {"spec": {
                "serviceAccountName": "worker@llm-wiki-cloud.iam.gserviceaccount.com",
                "containers": [{
                    "image": image,
                    "env": [
                        {"name": "BUCKET", "value": "bucket"},
                        {"name": "PIPELINE_JOB_NAME", "value": "olw-pipeline"},
                        {"name": "PIPELINE_JOB_LOCATION", "value": "asia-east1"},
                        {"name": "DEEPSEEK_API_KEY", "valueSource": {"secretKeyRef": {"secret": "deepseek", "version": "latest"}}},
                        {"name": "UNEXPECTED_BEHAVIOR", "value": "must-be-rejected"},
                    ],
                    "args": ["run", "--auto-approve"],
                }],
            }}}}},
        }
        result = subprocess.run(
            ["bash", "-c", common_source() + '\nnormalize_worker_readback "$DEFINITION"'],
            env={**os.environ, "ROOT": str(ROOT), "DEFINITION": json.dumps(definition)},
            text=True,
            capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

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

    def test_wrappers_require_explicit_components_and_do_not_forward_environment_secrets(self):
        for filename in ("deploy-dev.yml", "promote-production.yml"):
            source = (ROOT / ".github/workflows" / filename).read_text()
            self.assertRegex(source, r"components:\n\s+description:.*\n\s+required: true")
            self.assertNotIn("default:", source)
            self.assertNotIn("inputs.components ||", source)
            self.assertNotIn("\n    secrets:", source)

    def test_reusable_workflow_validates_the_complete_fixed_tuple_before_protected_environment(self):
        source = (ROOT / ".github/workflows/cd.yml").read_text()
        plan = source[source.index("  plan:"):source.index("  mutate:")]
        self.assertNotIn("environment:", plan)
        self.assertIn("DEPLOYMENT_ENVIRONMENT:", plan)
        contract = source_bundle()
        self.assertIn("environment and config/ref tuple", contract)
        self.assertIn("DEPLOYMENT_ENVIRONMENT", contract)
        self.assertNotIn("secrets:", (ROOT / ".github/workflows/deploy-dev.yml").read_text())
        self.assertNotIn("secrets:", (ROOT / ".github/workflows/promote-production.yml").read_text())

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
                "metadata": {"name": "olw-pipeline", "generation": 9, "etag": "etag-9"},
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
            frozen_handle = json.loads((root / "rollback.json").read_text())["handles"]["worker"]
            self.assertEqual(frozen_handle["provider_state"]["generation"], 9)
            self.assertEqual(frozen_handle["provider_state"]["etag"], "etag-9")
            self.assertEqual(frozen["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["args"], definition["spec"]["template"]["spec"]["template"]["spec"]["containers"][0]["args"])
            self.assertEqual(len(frozen["spec"]["template"]["spec"]["template"]["spec"]["volumes"]), 1)
            self.assertIn("valueSource", json.dumps(frozen))
            state.write_text(json.dumps({**definition, "spec": {"template": {"spec": {"template": {"spec": {**definition["spec"]["template"]["spec"]["template"]["spec"], "containers": [{**definition["spec"]["template"]["spec"]["template"]["spec"]["containers"][0], "args":["changed"]}]}}}}}}))
            (root / "journal.json").write_text(json.dumps({"schema": "lwc-306-mutation-journal-v1", "order": ["worker"], "components": {"worker": {"state": "accepted", "history": ["pending", "accepted"], "timestamp": "2026-09-04T00:00:00Z", "attempt": 1}}}))
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            rollback = json.loads((root / "rollback-result.json").read_text())
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue(rollback["verified"])
            self.assertEqual(json.loads(state.read_text()), frozen)
            env["FAKE_ROLLBACK_MISMATCH"] = "1"
            result = subprocess.run(["bash", str(ROOT / "deploy/cd.sh"), "rollback"], env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            rollback = json.loads((root / "rollback-result.json").read_text())
            self.assertEqual(rollback["result"], "failed")
            self.assertFalse(rollback["verified"])
        finally:
            import shutil
            shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
