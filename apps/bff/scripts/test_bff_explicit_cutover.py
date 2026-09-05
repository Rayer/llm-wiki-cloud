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
        source = (REPO_ROOT / "deploy" / "cd.sh").read_text()
        bff_source = (REPO_ROOT / "deploy" / "components" / "bff.sh").read_text()
        shared = (REPO_ROOT / ".github" / "workflows" / "cd.yml").read_text()
        for path in ("deploy-dev.yml", "promote-production.yml"):
            workflow = yaml.safe_load((REPO_ROOT / ".github" / "workflows" / path).read_text())
            triggers = workflow.get("on", workflow.get(True, {}))
            self.assertIn("workflow_dispatch", triggers)
            self.assertIn("./.github/workflows/cd.yml", (REPO_ROOT / ".github" / "workflows" / path).read_text())
        self.assertLess(shared.index("Upload durable rollback artifact"), shared.index("Mutate Auth"))
        self.assertIn("if: steps.rollback_upload.outcome == 'success'", shared)
        self.assertIn("event=workflow_dispatch", source)
        self.assertIn("gcloud run services update-traffic", bff_source)
        self.assertNotIn("run jobs execute", source)
        self.assertNotIn("vercel alias set", source)

    def test_bff_freezes_and_rolls_back_with_immutable_image_only_artifact(self):
        directory = Path(tempfile.mkdtemp(prefix="lwc-306-bff-legacy-"))
        try:
            artifacts = directory / "artifacts"
            artifacts.mkdir()
            normalized = self.normalized()
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
            self.assertEqual(set(handle.keys()), {"image"})
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


    def test_bff_candidate_cutover_revalidates_after_candidate_creation(self):
        """A candidate must stay dark and freshness failures must not cut it over."""
        registry = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images"
        ci_jobs = [{
            "id": 1,
            "name": "canonical-ci",
            "run_id": 7,
            "run_attempt": 1,
            "status": "completed",
            "conclusion": "success",
        }]

        for freshness, expected_error, expected_updates in (
            ("branch-before", "canonical source ref advanced before provider work", 0),
            ("rerun-before", "pinned canonical CI run is not the exact successful attempt", 0),
            ("failure-before", "pinned canonical CI run is not the exact successful attempt", 0),
            ("branch", "canonical source ref advanced before provider work", 1),
            ("rerun", "pinned canonical CI run is not the exact successful attempt", 1),
            ("failure", "pinned canonical CI run is not the exact successful attempt", 1),
            ("success", None, 1),
        ):
            with self.subTest(freshness=freshness), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                bin_dir = root / "bin"
                bin_dir.mkdir()
                phase = root / "phase"
                phase.write_text("before-candidate")
                events = root / "events"
                state_path = root / "state.json"
                old_revision = "bff-old"
                candidate_revision = "bff-candidate"
                old_image = f"{registry}/llm-wiki-bff@sha256:{'a' * 64}"
                state_path.write_text(json.dumps({
                    "service": {"status": {
                        "traffic": [{"revisionName": old_revision, "percent": 100}],
                        "latestCreatedRevisionName": old_revision,
                    }},
                    "revisions": {
                        old_revision: {
                            "spec": {"containers": [{"image": old_image}]},
                            "status": {"imageDigest": old_image, "conditions": [{"type": "Ready", "status": "True"}]},
                        },
                    },
                }))
                (bin_dir / "git").write_text(textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import os, sys
                    from pathlib import Path
                    args = sys.argv[1:]
                    Path(os.environ["FAKE_EVENTS"]).open("a").write("git " + " ".join(args) + "\\n")
                    if args[0] == "fetch":
                        raise SystemExit(0)
                    if args[:2] == ["rev-parse", "HEAD"]:
                        print("{SOURCE_SHA}")
                        raise SystemExit(0)
                    if args[:2] == ["rev-parse", "origin/develop"]:
                        phase = Path(os.environ["FAKE_PHASE"]).read_text()
                        print("{'c' * 40}" if os.environ["FAKE_FRESHNESS"] == "branch-before" or phase == "after-candidate" and os.environ["FAKE_FRESHNESS"] == "branch" else "{SOURCE_SHA}")
                        raise SystemExit(0)
                    raise SystemExit(2)
                """).lstrip())
                (bin_dir / "gh").write_text(textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import json, os, sys
                    from pathlib import Path
                    args = sys.argv[1:]
                    endpoint = args[args.index("api") + 1]
                    Path(os.environ["FAKE_EVENTS"]).open("a").write("gh " + endpoint + "\\n")
                    phase = Path(os.environ["FAKE_PHASE"]).read_text()
                    run = {{
                        "id": 7, "path": ".github/workflows/ci.yml", "event": "push",
                        "head_branch": "develop", "head_sha": "{SOURCE_SHA}",
                        "run_attempt": 1, "status": "completed", "conclusion": "success",
                    }}
                    if os.environ["FAKE_FRESHNESS"] == "rerun-before" or phase == "after-candidate" and os.environ["FAKE_FRESHNESS"] == "rerun":
                        run["run_attempt"] = 2
                    if os.environ["FAKE_FRESHNESS"] == "failure-before" or phase == "after-candidate" and os.environ["FAKE_FRESHNESS"] == "failure":
                        run["conclusion"] = "failure"
                    if "/jobs?" in endpoint:
                        print(json.dumps({{"total_count": 1, "jobs": [{{
                            "id": 1, "name": "canonical-ci", "run_id": 7,
                            "run_attempt": run["run_attempt"], "status": "completed",
                            "conclusion": run["conclusion"],
                        }}]}}))
                    else:
                        print(json.dumps(run))
                """).lstrip())
                (bin_dir / "gcloud").write_text(textwrap.dedent(f"""
                    #!/usr/bin/env python3
                    import json, os, sys
                    from pathlib import Path
                    args = sys.argv[1:]
                    Path(os.environ["FAKE_EVENTS"]).open("a").write("gcloud " + " ".join(args) + "\\n")
                    state_path = Path(os.environ["FAKE_STATE"])
                    state = json.loads(state_path.read_text())
                    if args[:2] == ["builds", "submit"]:
                        pass
                    elif args[:4] == ["artifacts", "docker", "images", "describe"]:
                        print("sha256:{'b' * 64}")
                    elif args[:3] == ["run", "services", "update"]:
                        image = args[args.index("--image") + 1]
                        state["service"]["status"]["latestCreatedRevisionName"] = "{candidate_revision}"
                        state["revisions"]["{candidate_revision}"] = {{
                            "spec": {{"containers": [{{"image": image}}]}},
                            "status": {{"imageDigest": image, "conditions": [{{"type": "Ready", "status": "True"}}]}},
                        }}
                        if "--no-traffic" not in args:
                            state["service"]["status"]["traffic"] = [{{"revisionName": "{candidate_revision}", "percent": 100}}]
                        state_path.write_text(json.dumps(state))
                        Path(os.environ["FAKE_PHASE"]).write_text("after-candidate")
                        print("{candidate_revision}")
                    elif args[:3] == ["run", "services", "update-traffic"]:
                        if "--to-revisions" not in args or args[args.index("--to-revisions") + 1] != "{candidate_revision}=100":
                            raise SystemExit(2)
                        state["service"]["status"]["traffic"] = [{{"revisionName": "{candidate_revision}", "percent": 100}}]
                        state_path.write_text(json.dumps(state))
                    elif args[:3] == ["run", "services", "describe"]:
                        print(json.dumps(state["service"]))
                    elif args[:3] == ["run", "revisions", "describe"]:
                        print(json.dumps(state["revisions"][args[3]]))
                    else:
                        raise SystemExit(2)
                """).lstrip())
                for command in ("git", "gh", "gcloud"):
                    (bin_dir / command).chmod(0o755)

                plan = root / "plan.json"
                plan.write_text(json.dumps({
                    "ci": {
                        "run_id": 7,
                        "run_attempt": 1,
                        "workflow_path": ".github/workflows/ci.yml",
                        "event": "push",
                        "head_branch": "develop",
                        "head_sha": SOURCE_SHA,
                        "conclusion": "success",
                        "jobs": ci_jobs,
                    },
                    "normalized": {
                        "selected_components": ["bff"],
                        "gcp": {"project_id": "llm-wiki-cloud", "region": "asia-east1", "artifact_registry": registry},
                        "bff": {"service_name": "bff-service"},
                    },
                }))
                env = {
                    **os.environ,
                    "PATH": f"{bin_dir}:{os.environ['PATH']}",
                    "ROOT": str(REPO_ROOT),
                    "ENVIRONMENT": "development",
                    "SOURCE_REF": "develop",
                    "SOURCE_SHA": SOURCE_SHA,
                    "PLAN_PATH": str(plan),
                    "JOURNAL_PATH": str(root / "artifacts" / "journal.json"),
                    "ARTIFACT_DIR": str(root / "artifacts"),
                    "ROLLBACK_UPLOADED": "1",
                    "GH_TOKEN": "fixture",
                    "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                    "FAKE_EVENTS": str(events),
                    "FAKE_PHASE": str(phase),
                    "FAKE_STATE": str(state_path),
                    "FAKE_FRESHNESS": freshness,
                }
                result = subprocess.run(
                    ["bash", str(REPO_ROOT / "deploy" / "components" / "bff.sh"), "mutate"],
                    env=env,
                    text=True,
                    capture_output=True,
                )
                calls = events.read_text().splitlines()
                updates = [call for call in calls if call.startswith("gcloud run services update bff-service")]
                cutovers = [call for call in calls if call.startswith("gcloud run services update-traffic bff-service")]
                self.assertEqual(len(updates), expected_updates, result.stdout + result.stderr)
                if updates:
                    self.assertIn("--no-traffic", updates[0])
                    self.assertTrue(calls[calls.index(updates[0]) - 1].startswith("gh repos/Rayer/llm-wiki-cloud/actions/runs/7/attempts/1/jobs?"))
                candidate_reads = [
                    index for index, call in enumerate(calls)
                    if call == f"gcloud run revisions describe {candidate_revision} --project llm-wiki-cloud --region asia-east1 --format=json --quiet"
                ]
                if updates:
                    self.assertTrue(candidate_reads, calls)
                else:
                    self.assertEqual(candidate_reads, [])

                if expected_error:
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn(expected_error, result.stderr)
                    self.assertEqual(cutovers, [])
                    self.assertEqual(bool(candidate_reads), bool(updates))
                    self.assertEqual(json.loads(state_path.read_text())["service"]["status"]["traffic"][0]["revisionName"], old_revision)
                else:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(cutovers), 1)
                    self.assertIn(f"--to-revisions {candidate_revision}=100", cutovers[0])
                    self.assertLess(candidate_reads[0], calls.index(cutovers[0]))
                    self.assertTrue(calls[calls.index(cutovers[0]) - 1].startswith("gh repos/Rayer/llm-wiki-cloud/actions/runs/7/attempts/1/jobs?"))
                    self.assertEqual(json.loads(state_path.read_text())["service"]["status"]["traffic"][0]["revisionName"], candidate_revision)


if __name__ == "__main__":
    unittest.main()
