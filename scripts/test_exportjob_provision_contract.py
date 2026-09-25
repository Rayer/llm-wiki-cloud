"""Offline checks for first-time DEV Export Job provisioning and policy recovery."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import yaml

from deploy.provision.exportjob_dev import (
    CONTRACT, DEPLOYER, STORAGE_EXPORTS, STORAGE_USERS, VERIFIER_ROLES, ProvisionError, Provisioner,
    load_contract, ready_archive_binding,
)

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff-export-job@sha256:" + "a" * 64


class ExportJobProvisionContractTests(unittest.TestCase):
    def _prerequisite_runner(self, calls, *, missing_role=None, extra_role_permission=None):
        config = load_contract()
        project = config["project"]
        runtime = "serviceAccount:" + config["runtime_service_account"]
        bff = "serviceAccount:" + config["bff_service_account"]
        role_ids = {"objectLister": "lwcExportObjectLister", "sourceReader": "lwcExportSourceReader",
                    "archiveWriter": "lwcExportArchiveWriter", "blobSigner": "lwcExportBlobSigner",
                    "readyArchiveReader": "lwcExportReadyArchiveReader"}
        role_names = {key: f"projects/{project}/roles/{value}" for key, value in role_ids.items()}
        verifier_names = {key: f"projects/{project}/roles/{value[0]}" for key, value in VERIFIER_ROLES.items()}
        project_policy = {"etag": "project-e1", "version": 3, "bindings": [
            {"role": "roles/datastore.user", "members": [runtime], "condition": {
                "title": "lwc344-export-worker-dev-firestore",
                "description": "Limit Export worker to the DEV Firestore database",
                "expression": "resource.name == 'projects/llm-wiki-cloud/databases/llm-wiki-cloud-dev'"}},
            {"role": verifier_names["roleReadback"], "members": [DEPLOYER]},
            {"role": verifier_names["projectPolicyReadback"], "members": [DEPLOYER]},
        ]}
        bucket_policy = {"etag": "bucket-e1", "bindings": [
            {"role": role_names["objectLister"], "members": [runtime]},
            {"role": role_names["sourceReader"], "members": [runtime], "condition": {
                "title": "lwc344-export-source-objects", "description": "Read Export source objects only",
                "expression": STORAGE_USERS}},
            {"role": role_names["archiveWriter"], "members": [runtime], "condition": {
                "title": "lwc344-export-archive-objects", "description": "Manage Export temporary and ready archives only",
                "expression": STORAGE_EXPORTS}},
            {"role": role_names["readyArchiveReader"],
             "members": ["serviceAccount:" + config["signing_service_account"]],
             "condition": ready_archive_binding(role_names["readyArchiveReader"], config["signing_service_account"])["condition"]},
        ]}
        signer_policy = {"etag": "signer-e1", "bindings": [
            {"role": role_names["blobSigner"], "members": [bff]},
            {"role": verifier_names["signerPolicyReadback"], "members": [DEPLOYER]},
        ]}
        state = {"project": project_policy, "bucket": bucket_policy, "signer": signer_policy,
                 "job": {"etag": "job-e1", "bindings": []}}

        def run(args, **kwargs):
            calls.append(args)
            gcloud_args = args[1:]
            if gcloud_args[:4] == ["config", "get-value", "account", "--quiet"]:
                return subprocess.CompletedProcess(args, 0, DEPLOYER.removeprefix("serviceAccount:") + "\n", "")
            if gcloud_args[:3] == ["iam", "service-accounts", "describe"]:
                email = gcloud_args[3]
                return subprocess.CompletedProcess(args, 0, json.dumps({"email": email,
                    "name": f"projects/{project}/serviceAccounts/{email}"}), "")
            if gcloud_args[:3] == ["iam", "roles", "describe"]:
                role_id = gcloud_args[3]
                if role_id == missing_role:
                    return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                if role_id in role_ids.values():
                    key = next(k for k, value in role_ids.items() if value == role_id)
                    permissions = list(config["storage_roles"][key])
                    name = role_names[key]
                else:
                    key = next(k for k, value in VERIFIER_ROLES.items() if value[0] == role_id)
                    permissions = list(VERIFIER_ROLES[key][2])
                    name = verifier_names[key]
                if role_id == extra_role_permission:
                    permissions.append("resourcemanager.projects.setIamPolicy")
                return subprocess.CompletedProcess(args, 0, json.dumps({"name": name, "stage": "GA",
                    "includedPermissions": permissions}), "")
            if gcloud_args[:3] == ["storage", "buckets", "describe"]:
                return subprocess.CompletedProcess(args, 0, json.dumps({"iam_configuration": {
                    "uniform_bucket_level_access": {"enabled": True}}}), "")
            if gcloud_args[:2] == ["projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(state["project"]), "")
            if gcloud_args[:3] == ["storage", "buckets", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(state["bucket"]), "")
            if gcloud_args[:3] == ["iam", "service-accounts", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(state["signer"]), "")
            if gcloud_args[:3] == ["run", "jobs", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(state["job"]), "")
            if gcloud_args[:3] in (["run", "jobs", "set-iam-policy"],
                                    ["projects", "set-iam-policy"],
                                    ["iam", "service-accounts", "set-iam-policy"]):
                policy_path = Path(gcloud_args[-2])
                policy = json.loads(policy_path.read_text())
                if gcloud_args[0] == "run":
                    state["job"] = policy
                elif gcloud_args[0] == "projects":
                    state["project"] = policy
                else:
                    state["signer"] = policy
                return subprocess.CompletedProcess(args, 0, "", "")
            raise AssertionError("unexpected gcloud command: " + " ".join(args))
        return run, state

    def test_build_submit_substitutions_are_consumed_by_cloudbuild_config(self):
        config = yaml.safe_load((ROOT / "apps/bff/cloudbuild-exportjob.yaml").read_text())
        args = config["steps"][0]["args"]
        self.assertIn("${_IMAGE}", config["images"])
        self.assertIn("--label=org.opencontainers.image.revision=${_SOURCE_SHA}", args)

    def _successful_build_run(self, calls, *, cli_status=0):
        build_id = "7e0c0b33-7b3e-4825-a447-81d3a280df1e"
        digest = "sha256:" + "b" * 64
        tag = load_contract()["artifact_registry"] + "/llm-wiki-bff-export-job:" + "a" * 40

        def run(args, **kwargs):
            calls.append(args)
            if args[:3] == ["gcloud", "builds", "submit"]:
                self.assertIn("--async", args)
                self.assertIn("_SOURCE_SHA=" + "a" * 40, args[args.index("--substitutions") + 1])
                return subprocess.CompletedProcess(args, cli_status, build_id + "\n", "log stream failed")
            if args[:3] == ["gcloud", "builds", "describe"]:
                return subprocess.CompletedProcess(args, 0, json.dumps({
                    "id": build_id, "projectId": "llm-wiki-cloud", "status": "SUCCESS", "createTime": "now",
                    "sourceProvenance": {"resolvedStorageSource": {"bucket": "source", "object": "archive.tgz", "generation": "1"}},
                    "substitutions": {"_IMAGE": tag, "_SOURCE_SHA": "a" * 40},
                    "results": {"images": [{"name": tag, "digest": digest}]},
                }), "")
            if args[:4] == ["gcloud", "artifacts", "docker", "images"]:
                return subprocess.CompletedProcess(args, 0, digest, "")
            self.fail("unexpected command: " + " ".join(args))

        return run, build_id, digest

    def test_build_success_is_reconciled_even_when_submit_cli_returns_nonzero(self):
        calls = []
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            p = Provisioner(load_contract(), self._successful_build_run(calls, cli_status=1)[0], path)
            image = p.build_image("a" * 40)
            saved = json.loads(path.read_text())
        self.assertTrue(image.endswith("@sha256:" + "b" * 64))
        self.assertEqual(saved["build"]["provider_status"], "SUCCESS")
        self.assertEqual(saved["build"]["cli_exit_code"], 1)
        self.assertEqual(saved["build"]["source_sha"], "a" * 40)
        self.assertEqual(saved["build"]["id"], "7e0c0b33-7b3e-4825-a447-81d3a280df1e")

    def test_terminal_build_failure_is_recorded_and_not_rebuilt(self):
        calls = []
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"

            def run(args, **kwargs):
                calls.append(args)
                if args[:3] == ["gcloud", "builds", "submit"]:
                    return subprocess.CompletedProcess(args, 0, "7e0c0b33-7b3e-4825-a447-81d3a280df1e", "")
                return subprocess.CompletedProcess(args, 0, json.dumps({"id": "7e0c0b33-7b3e-4825-a447-81d3a280df1e",
                    "projectId": "llm-wiki-cloud", "status": "FAILURE", "substitutions": {
                        "_IMAGE": load_contract()["artifact_registry"] + "/llm-wiki-bff-export-job:" + "a" * 40,
                        "_SOURCE_SHA": "a" * 40}}), "")

            p = Provisioner(load_contract(), run, path)
            with self.assertRaisesRegex(ProvisionError, "terminal status FAILURE"):
                p.build_image("a" * 40)
            before = len(calls)
            rerun = Provisioner(load_contract(), run, path)
            with self.assertRaisesRegex(ProvisionError, "no proven operation identity"):
                rerun.build_image("a" * 40)
            self.assertEqual(len(calls), before)

    def test_missing_build_identity_fails_closed_and_redacts_secrets(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            def run(args, **kwargs):
                return subprocess.CompletedProcess(args, 1, "", "WIF_SECRET=top-secret Authorization: Bearer ya29.secret")
            p = Provisioner(load_contract(), run, path)
            with self.assertRaisesRegex(ProvisionError, "identity is unknown") as raised:
                p.build_image("a" * 40)
            saved = path.read_text()
            self.assertNotIn("top-secret", saved)
            self.assertNotIn("ya29.secret", saved)
            self.assertNotIn("top-secret", str(raised.exception))
            self.assertEqual(json.loads(saved)["build"]["status"], "unknown")
            calls = []
            rerun = Provisioner(load_contract(), lambda args, **kwargs: calls.append(args), path)
            with self.assertRaises(ProvisionError):
                rerun.build_image("a" * 40)
            self.assertEqual(calls, [])

    def test_provider_build_identity_mismatch_is_unknown_and_never_reuses_image(self):
        calls = []
        build_id = "7e0c0b33-7b3e-4825-a447-81d3a280df1e"
        tag = load_contract()["artifact_registry"] + "/llm-wiki-bff-export-job:" + "a" * 40

        def run(args, **kwargs):
            calls.append(args)
            if args[:3] == ["gcloud", "builds", "submit"]:
                return subprocess.CompletedProcess(args, 0, build_id, "")
            return subprocess.CompletedProcess(args, 0, json.dumps({"id": "different", "projectId": "llm-wiki-cloud",
                "status": "SUCCESS", "substitutions": {"_IMAGE": tag, "_SOURCE_SHA": "a" * 40}}), "")

        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            p = Provisioner(load_contract(), run, path)
            with self.assertRaisesRegex(ProvisionError, "identity or source provenance differs"):
                p.build_image("a" * 40)
            saved = json.loads(path.read_text())
        self.assertEqual(saved["build"]["status"], "unknown")
        self.assertEqual(saved["build"]["reason"], "provider_build_identity_or_source_mismatch")
        self.assertFalse(any(args[:4] == ["gcloud", "artifacts", "docker", "images"] for args in calls))

    def test_known_build_identity_can_be_reconciled_after_temporary_readback_failure(self):
        calls = []
        build_id = "7e0c0b33-7b3e-4825-a447-81d3a280df1e"
        tag = load_contract()["artifact_registry"] + "/llm-wiki-bff-export-job:" + "a" * 40
        digest = "sha256:" + "d" * 64

        def run(args, **kwargs):
            calls.append(args)
            if args[:3] == ["gcloud", "builds", "submit"]:
                return subprocess.CompletedProcess(args, 0, build_id, "")
            if args[:3] == ["gcloud", "builds", "describe"] and sum(c[:3] == args[:3] for c in calls) == 1:
                return subprocess.CompletedProcess(args, 1, "", "temporary provider read error")
            if args[:3] == ["gcloud", "builds", "describe"]:
                return subprocess.CompletedProcess(args, 0, json.dumps({"id": build_id, "projectId": "llm-wiki-cloud",
                    "status": "SUCCESS", "substitutions": {"_IMAGE": tag, "_SOURCE_SHA": "a" * 40},
                    "results": {"images": [{"name": tag, "digest": digest}]}}), "")
            if args[:4] == ["gcloud", "artifacts", "docker", "images"]:
                return subprocess.CompletedProcess(args, 0, digest, "")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            first = Provisioner(load_contract(), run, path)
            with self.assertRaisesRegex(ProvisionError, "status is unknown"):
                first.build_image("a" * 40)
            second = Provisioner(load_contract(), run, path)
            image = second.build_image("a" * 40)
            saved = json.loads(path.read_text())
        self.assertTrue(image.endswith("@" + digest))
        self.assertEqual(saved["build"]["id"], build_id)
        self.assertEqual(sum(c[:3] == ["gcloud", "builds", "submit"] for c in calls), 1)

    def test_source_mismatch_does_not_reuse_or_rebuild(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            calls = []
            p = Provisioner(load_contract(), self._successful_build_run(calls)[0], path)
            p.build_image("a" * 40)
            saved = json.loads(path.read_text())
            saved["build"]["source_sha"] = "c" * 40
            path.write_text(json.dumps(saved))
            new_calls = []
            rerun = Provisioner(load_contract(), lambda args, **kwargs: new_calls.append(args), path)
            with self.assertRaisesRegex(ProvisionError, "provenance differs"):
                rerun.build_image("a" * 40)
            self.assertEqual(new_calls, [])

    def test_legacy_same_sha_tag_without_build_identity_is_not_reused(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            path.write_text(json.dumps({
                "schema": "lwc-344-exportjob-dev-provision-v1",
                "source": {"ref": "refs/heads/develop", "sha": "a" * 40},
                "target": {"environment": "development", "project": "llm-wiki-cloud", "region": "asia-east1"},
                "image": {"tag": load_contract()["artifact_registry"] + "/llm-wiki-bff-export-job:" + "a" * 40,
                          "status": "verified", "reference": IMAGE},
                "resources": {}, "policies": [],
            }))
            calls = []
            p = Provisioner(load_contract(), lambda args, **kwargs: calls.append(args), path)
            with self.assertRaisesRegex(ProvisionError, "no proven operation identity"):
                p.build_image("a" * 40)
            self.assertEqual(calls, [])

    def test_verified_build_rerun_preserves_digest_and_evidence_without_resubmit(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            calls = []
            p = Provisioner(load_contract(), self._successful_build_run(calls)[0], path)
            image = p.build_image("a" * 40)
            before = json.loads(path.read_text())
            rerun_calls = []
            rerun = Provisioner(load_contract(), self._successful_build_run(rerun_calls)[0], path)
            self.assertEqual(rerun.build_image("a" * 40), image)
            after = json.loads(path.read_text())
        self.assertFalse(any(args[:3] == ["gcloud", "builds", "submit"] for args in rerun_calls))
        self.assertEqual(after["build"]["id"], before["build"]["id"])
        self.assertEqual(after["build"]["image_digest"], before["build"]["image_digest"])
        self.assertEqual(after["image"]["reference"], before["image"]["reference"])

    def job_v2(self, expected):
        return json.dumps({"apiVersion": "run.googleapis.com/v2", "template": {
            "parallelism": 1, "taskCount": 1, "template": {
                "containers": [{"image": expected["image"], "env": [{"name": k, "value": v} for k, v in expected["env"].items()]}],
                "serviceAccount": expected["service_account"], "timeout": "82800s", "maxRetries": 0,
            }}})

    def job_v1(self, expected):
        return json.dumps({"apiVersion": "run.googleapis.com/v1", "spec": {"template": {"spec": {
            "parallelism": 1, "taskCount": 1, "template": {"spec": {
                "containers": [{"image": expected["image"], "env": [{"name": k, "value": v} for k, v in expected["env"].items()]}],
                "serviceAccountName": expected["service_account"], "timeoutSeconds": "82800s", "maxRetries": 0,
            }}}}}})

    def test_fixed_dev_contract_and_production_remains_disabled(self):
        config = load_contract()
        self.assertEqual(config["job"], "export-job-dev")
        self.assertEqual(config["runtime_service_account"], "lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com")
        self.assertEqual(config["storage_roles"]["objectLister"], ["storage.objects.list"])
        self.assertEqual(config["storage_roles"]["sourceReader"], ["storage.objects.get"])
        self.assertEqual(config["storage_roles"]["readyArchiveReader"], ["storage.objects.get"])
        self.assertEqual(STORAGE_USERS.split("&&", 1)[1].strip(), "resource.name.startsWith('projects/_/buckets/llm-wiki-data-dev/objects/users/')")
        self.assertIn("objects/exports/tmp/", STORAGE_EXPORTS)
        self.assertIn("objects/exports/ready/", STORAGE_EXPORTS)
        production = (ROOT / "deploy/environments/production.yaml").read_text()
        self.assertIn("export_job:\n  enabled: false", production)
        data = json.loads(CONTRACT.read_text())
        data["environment"] = "production"
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "contract.json"
            path.write_text(json.dumps(data))
            with self.assertRaises(ProvisionError):
                load_contract(path)

    def test_owner_bootstrap_is_separate_from_workflow_build_and_job_deployment(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(load_contract(), evidence_path=Path(temp) / "evidence.json")
            calls = []
            p.ensure_service_account = lambda *args: calls.append(("service-account", args[0]))
            p.ensure_custom_role = lambda key: calls.append(("custom-role", key)) or key
            p.ensure_verifier_roles = lambda: calls.append(("verifier-roles",)) or {"r": "r"}
            p.bucket_ready = lambda: calls.append(("bucket-ready",))
            p.apply_iam = lambda *roles: calls.append(("owner-iam", roles))
            p.apply_verifier_iam = lambda roles: calls.append(("verifier-iam", roles))
            p.run_owner_bootstrap()
        self.assertEqual([call[0] for call in calls], ["service-account", "service-account"] +
            ["custom-role"] * 5 + ["verifier-roles", "bucket-ready", "owner-iam", "verifier-iam"])
        self.assertEqual(p.evidence["result"], "owner_bootstrap_applied_and_read_back")

    def test_verifier_roles_and_bindings_are_exact_and_signer_scoped(self):
        self.assertEqual({key: definition[2] for key, definition in VERIFIER_ROLES.items()}, {
            "roleReadback": ["iam.roles.get"],
            "projectPolicyReadback": ["resourcemanager.projects.getIamPolicy"],
            "signerPolicyReadback": ["iam.serviceAccounts.getIamPolicy"],
        })
        observed = []
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(load_contract(), evidence_path=Path(temp) / "evidence.json")
            p.policy = lambda target, get_args, set_args, desired: observed.append((target, set_args, desired))
            p.apply_verifier_iam({key: f"projects/llm-wiki-cloud/roles/{value[0]}"
                                  for key, value in VERIFIER_ROLES.items()})
        self.assertEqual([item[0] for item in observed], ["project:llm-wiki-cloud"] * 2 +
            ["serviceAccount:lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com"])
        self.assertEqual([item[2] for item in observed], [
            {"role": "projects/llm-wiki-cloud/roles/lwcExportRoleReadback", "member": DEPLOYER},
            {"role": "projects/llm-wiki-cloud/roles/lwcExportProjectPolicyReadback", "member": DEPLOYER},
            {"role": "projects/llm-wiki-cloud/roles/lwcExportSignerPolicyReadback", "member": DEPLOYER},
        ])

    def test_workflow_verifies_owner_prerequisites_before_build_and_job_mutations(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(load_contract(), evidence_path=Path(temp) / "evidence.json")
            calls = []
            p.owner_prerequisites = lambda: calls.append("verify-owner-prerequisites") or ("role",) * 5
            p.build_image = lambda sha: calls.append("build-image") or IMAGE
            p.ensure_job = lambda image: calls.append("create-or-verify-job")
            p.apply_job_iam = lambda: calls.append("job-iam")
            with patch.dict(os.environ, {"WIF_SERVICE_ACCOUNT": DEPLOYER.removeprefix("serviceAccount:")}):
                p.run_workflow("a" * 40)
        self.assertEqual(calls, ["verify-owner-prerequisites", "build-image", "create-or-verify-job", "job-iam"])
        self.assertEqual(p.evidence["result"], "workflow_deployed_and_read_back")

    def test_owner_prerequisites_accept_provider_shaped_exact_readbacks(self):
        calls = []
        run, _state = self._prerequisite_runner(calls)
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(load_contract(), run, Path(temp) / "evidence.json")
            p.owner_prerequisites()
        self.assertEqual(sum(call[1:3] == ["iam", "roles"] for call in calls), 8)
        self.assertEqual(sum(call[1:3] == ["iam", "service-accounts"] and "describe" in call for call in calls), 2)
        self.assertEqual(sum(call[1:3] == ["projects", "get-iam-policy"] for call in calls), 3)
        self.assertEqual(sum(call[1:4] == ["storage", "buckets", "get-iam-policy"] for call in calls), 4)
        self.assertEqual(sum("set-iam-policy" in call for call in calls), 0)
        self.assertEqual(sum("create" in call for call in calls), 0)

    def test_workflow_stops_before_build_when_owner_prerequisite_is_missing_or_mismatched(self):
        for scenario in ("missing", "mismatched"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as temp:
                calls = []
                run, _state = self._prerequisite_runner(calls,
                    missing_role="lwcExportSourceReader" if scenario == "missing" else None,
                    extra_role_permission="lwcExportSourceReader" if scenario == "mismatched" else None)
                p = Provisioner(load_contract(), run, Path(temp) / "evidence.json")
                build_calls = []
                p.build_image = lambda sha: build_calls.append(sha) or IMAGE
                with patch.dict(os.environ, {"WIF_SERVICE_ACCOUNT": DEPLOYER.removeprefix("serviceAccount:")}):
                    with self.assertRaises(ProvisionError):
                        p.run_workflow("a" * 40)
                self.assertEqual(build_calls, [])
                self.assertFalse(any(call[1:3] == ["builds", "submit"] for call in calls))
                self.assertFalse(any("set-iam-policy" in call or "create" in call for call in calls))

    def test_workflow_rejects_a_different_configured_wif_service_account_before_provider_reads(self):
        with tempfile.TemporaryDirectory() as temp:
            calls = []
            run, _state = self._prerequisite_runner(calls)
            p = Provisioner(load_contract(), run, Path(temp) / "evidence.json")
            with patch.dict(os.environ, {"WIF_SERVICE_ACCOUNT": "other@llm-wiki-cloud.iam.gserviceaccount.com"}):
                with self.assertRaisesRegex(ProvisionError, "workflow identity differs"):
                    p.run_workflow("a" * 40)
        self.assertEqual(calls, [])

    def test_workflow_never_writes_owner_managed_iam(self):
        with tempfile.TemporaryDirectory() as temp:
            calls = []
            run, _state = self._prerequisite_runner(calls)
            p = Provisioner(load_contract(), run, Path(temp) / "evidence.json")
            p.build_image = lambda _sha: IMAGE
            p.ensure_job = lambda _image: None
            with patch.dict(os.environ, {"WIF_SERVICE_ACCOUNT": DEPLOYER.removeprefix("serviceAccount:")}):
                p.run_workflow("a" * 40)
        owner_writes = [call for call in calls if "set-iam-policy" in call and
                        call[1:3] != ["run", "jobs"]]
        role_or_account_creates = [call for call in calls if "create" in call and
                                   call[1:3] in (["iam", "roles"], ["iam", "service-accounts"])]
        self.assertEqual(owner_writes, [])
        self.assertEqual(role_or_account_creates, [])
        self.assertTrue(any(call[1:3] == ["run", "jobs"] and "set-iam-policy" in call for call in calls))

    def test_cleanup_removes_only_bootstrap_added_verifier_members_after_matching_workflow(self):
        config = load_contract()
        project = config["project"]
        signer = config["signing_service_account"]
        role_ids = {key: f"projects/{project}/roles/{value[0]}" for key, value in VERIFIER_ROLES.items()}
        principal = DEPLOYER
        state = {
            "project": {"etag": "p1", "bindings": [
                {"role": role_ids["roleReadback"], "members": [principal]},
                {"role": role_ids["projectPolicyReadback"], "members": [principal]},
                {"role": "roles/other", "members": ["user:unrelated@example.com"]}]},
            "signer": {"etag": "s1", "bindings": [
                {"role": role_ids["signerPolicyReadback"], "members": [principal]},
                {"role": "roles/other", "members": ["user:unrelated@example.com"]}]},
        }
        calls = []
        project_conflicted = False
        def run(args, **kwargs):
            nonlocal project_conflicted
            calls.append(args)
            parts = args[1:]
            if parts[:2] == ["projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(state["project"]), "")
            if parts[:2] == ["iam", "service-accounts"] and parts[2] == "get-iam-policy":
                return subprocess.CompletedProcess(args, 0, json.dumps(state["signer"]), "")
            if parts[:2] == ["projects", "set-iam-policy"]:
                if not project_conflicted:
                    project_conflicted = True
                    state["project"]["bindings"].append({"role": "roles/concurrent", "members": ["user:concurrent@example.com"]})
                    state["project"]["etag"] = "p-concurrent"
                    return subprocess.CompletedProcess(args, 1, "", "etag conditionNotMet")
                state["project"] = json.loads(Path(parts[-2]).read_text())
                state["project"]["etag"] = "p2"
                return subprocess.CompletedProcess(args, 0, "", "")
            if parts[:3] == ["iam", "service-accounts", "set-iam-policy"]:
                state["signer"] = json.loads(Path(parts[-2]).read_text())
                state["signer"]["etag"] = "s2"
                return subprocess.CompletedProcess(args, 0, "", "")
            raise AssertionError("unexpected cleanup command: " + " ".join(args))
        with tempfile.TemporaryDirectory() as temp:
            owner_path = Path(temp) / "owner.json"
            workflow_path = Path(temp) / "workflow.json"
            p = Provisioner(config, run, owner_path)
            p.evidence["source"]["sha"] = "a" * 40
            p.evidence["result"] = "owner_bootstrap_applied_and_read_back"
            for key in ("roleReadback", "projectPolicyReadback"):
                p.evidence["policies"].append({"target": "project:" + project,
                    "binding": {"role": role_ids[key], "member": DEPLOYER}, "added_by_this_run": key != "roleReadback"})
            p.evidence["policies"].append({"target": "serviceAccount:" + signer,
                "binding": {"role": role_ids["signerPolicyReadback"], "member": DEPLOYER}, "added_by_this_run": True})
            p.save()
            workflow_roles = [role_ids["roleReadback"], role_ids["projectPolicyReadback"], role_ids["signerPolicyReadback"]]
            workflow_targets = ["project:" + project, "project:" + project, "serviceAccount:" + signer]
            workflow_path.write_text(json.dumps({"schema": "lwc-344-exportjob-dev-provision-v2",
                "result": "workflow_deployed_and_read_back", "source": {"ref": "refs/heads/develop", "sha": "a" * 40},
                "target": p.evidence["target"], "image": {"status": "verified"},
                "resources": {config["job"]: {"creation_status": "verified"}},
                "policies": [{"target": target, "binding": {"role": role, "member": DEPLOYER},
                              "status": "existing_verified", "before_etag": "verified"}
                             for target, role in zip(workflow_targets, workflow_roles)]}))
            p.cleanup_verifier_grants(workflow_path)
            saved = json.loads(owner_path.read_text())
        self.assertEqual(p.evidence["verifier_cleanup"], "complete")
        self.assertEqual(state["project"]["bindings"], [
            {"role": role_ids["roleReadback"], "members": [principal]},
            {"role": "roles/other", "members": ["user:unrelated@example.com"]},
            {"role": "roles/concurrent", "members": ["user:concurrent@example.com"]}])
        self.assertEqual(state["signer"]["bindings"], [{"role": "roles/other", "members": ["user:unrelated@example.com"]}])
        self.assertTrue(any(entry.get("cleanup_status") == "verified_removed" for entry in saved["policies"]))
        self.assertFalse(any("delete" in call for call in calls))

    def test_cleanup_requires_matching_successful_workflow_evidence(self):
        with tempfile.TemporaryDirectory() as temp:
            owner_path = Path(temp) / "owner.json"
            workflow_path = Path(temp) / "workflow.json"
            p = Provisioner(load_contract(), evidence_path=owner_path)
            p.evidence["result"] = "owner_bootstrap_applied_and_read_back"
            p.evidence["source"]["sha"] = "a" * 40
            p.save()
            workflow_path.write_text(json.dumps({"result": "failed", "source": {"sha": "a" * 40},
                "target": p.evidence["target"]}))
            with self.assertRaisesRegex(ProvisionError, "matching owner-bootstrap"):
                p.cleanup_verifier_grants(workflow_path)

    def test_owner_prerequisite_policy_verification_is_read_only_and_exact(self):
        desired = {"role": "roles/datastore.user", "member": "serviceAccount:worker@example.iam.gserviceaccount.com",
                   "condition": {"title": "dev", "expression": "resource.name == 'dev'"}}
        calls = []
        def run(args, **kwargs):
            calls.append(args)
            return subprocess.CompletedProcess(args, 0, json.dumps({"etag": "abc", "bindings": [{
                "role": desired["role"], "members": [desired["member"]], "condition": desired["condition"]}]}), "")
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(load_contract(), run, Path(temp) / "evidence.json")
            p.verify_policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"], desired)
            saved = json.loads(p.evidence_path.read_text())
        self.assertEqual(len(calls), 1)
        self.assertIn("get-iam-policy", calls[0])
        self.assertFalse(any("set-iam-policy" in part for part in calls[0]))
        self.assertEqual(saved["policies"][0]["before_etag"], "abc")
        self.assertFalse(saved["policies"][0]["added_by_this_run"])

    def test_job_is_created_when_absent_and_exact_existing_job_is_idempotent(self):
        config = load_contract()
        expected = Provisioner(config).image_and_job_config(IMAGE)
        raw = self.job_v2(expected)
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:4] == ["gcloud", "run", "jobs", "describe"]:
                return subprocess.CompletedProcess(args, 0, raw, "")
            self.fail("matching existing Job must not be mutated")

        with tempfile.TemporaryDirectory() as temp:
            provisioner = Provisioner(config, run, Path(temp) / "evidence.json")
            provisioner.ensure_job(IMAGE)
        self.assertEqual(len(calls), 1)
        bad = json.loads(raw)
        bad["template"]["template"]["serviceAccount"] = "other@llm-wiki-cloud.iam.gserviceaccount.com"
        provisioner.run = lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(bad), "")
        with self.assertRaises(ProvisionError):
            provisioner.ensure_job(IMAGE)

    def test_absent_job_is_created_from_immutable_image_and_read_back(self):
        config = load_contract()
        expected = Provisioner(config).image_and_job_config(IMAGE)
        raw = self.job_v2(expected)
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:4] == ["gcloud", "run", "jobs", "describe"]:
                if len([c for c in calls if c[:4] == args[:4]]) == 1:
                    return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                return subprocess.CompletedProcess(args, 0, raw, "")
            if args[:4] == ["gcloud", "run", "jobs", "create"]:
                self.assertIn(IMAGE, args)
                self.assertIn("23h", args)
                self.assertIn("--max-retries", args)
                self.assertEqual(args[args.index("--max-retries") + 1], "0")
                return subprocess.CompletedProcess(args, 0, "created", "")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            provisioner = Provisioner(config, run, Path(temp) / "evidence.json")
            provisioner.ensure_job(IMAGE)
            self.assertTrue(provisioner.evidence["resources"]["export-job-dev"]["created_by_this_run"])

    def test_job_v1_and_v2_use_real_outer_execution_limits(self):
        expected = Provisioner(load_contract()).image_and_job_config(IMAGE)
        provisioner = Provisioner(load_contract(), evidence_path=Path("/tmp/nonexistent-exportjob-evidence.json"))
        self.assertTrue(provisioner.job_matches(self.job_v1(expected), expected))
        v2 = json.loads(self.job_v2(expected))
        self.assertTrue(provisioner.job_matches(json.dumps(v2), expected))
        v2["template"]["template"]["parallelism"] = 1
        v2["template"]["template"]["taskCount"] = 1
        v2["template"].pop("parallelism")
        v2["template"].pop("taskCount")
        self.assertFalse(provisioner.job_matches(json.dumps(v2), expected))
        v1 = json.loads(self.job_v1(expected))
        execution = v1["spec"]["template"]["spec"]
        execution["template"]["spec"]["parallelism"] = 1
        execution["template"]["spec"]["taskCount"] = 1
        execution.pop("parallelism")
        execution.pop("taskCount")
        self.assertFalse(provisioner.job_matches(json.dumps(v1), expected))

    def test_mismatched_created_job_keeps_inverse_evidence_before_failure(self):
        config = load_contract()
        expected = Provisioner(config).image_and_job_config(IMAGE)
        mismatch = json.loads(self.job_v2(expected))
        mismatch["template"]["template"]["serviceAccount"] = "wrong@llm-wiki-cloud.iam.gserviceaccount.com"
        with tempfile.TemporaryDirectory() as temp:
            evidence = Path(temp) / "evidence.json"
            describes = 0

            def run(args, **kwargs):
                nonlocal describes
                if args[:4] == ["gcloud", "run", "jobs", "describe"]:
                    describes += 1
                    if describes == 1:
                        return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                    return subprocess.CompletedProcess(args, 0, json.dumps(mismatch), "")
                if args[:4] == ["gcloud", "run", "jobs", "create"]:
                    return subprocess.CompletedProcess(args, 0, "created", "")
                self.fail("unexpected command")

            p = Provisioner(config, run, evidence)
            with self.assertRaises(ProvisionError):
                p.ensure_job(IMAGE)
            saved = json.loads(evidence.read_text())["resources"]["export-job-dev"]
            self.assertTrue(saved["created_by_this_run"])
            self.assertEqual(saved["creation_status"], "accepted")
            self.assertEqual(saved["readback_identity"]["readback"], "mismatched")

    def test_bucket_parser_uses_fresh_gcloud_snake_case_shape(self):
        config = load_contract()
        calls = []
        actual_cli_shape = {"name": config["bucket"], "uniform_bucket_level_access": True}

        def run(args, **kwargs):
            calls.append(args)
            return subprocess.CompletedProcess(args, 0, json.dumps(actual_cli_shape), "")

        p = Provisioner(config, run)
        p.bucket_ready()
        self.assertEqual(len(calls), 1)
        actual_cli_shape.pop("uniform_bucket_level_access")
        actual_cli_shape["iam_configuration"] = {"uniform_bucket_level_access": {"enabled": True}}
        p.bucket_ready()
        actual_cli_shape["iamConfiguration"] = {"uniformBucketLevelAccess": {"enabled": True}}
        actual_cli_shape.pop("iam_configuration")
        with self.assertRaises(ProvisionError):
            p.bucket_ready()

    def test_signer_ready_prefix_permission_negative_matrix(self):
        config = load_contract()
        binding = ready_archive_binding("projects/llm-wiki-cloud/roles/lwcExportReadyArchiveReader",
                                        config["signing_service_account"])
        self.assertEqual(config["storage_roles"]["readyArchiveReader"], ["storage.objects.get"])
        self.assertEqual(binding["member"], "serviceAccount:" + config["signing_service_account"])
        self.assertNotIn(config["runtime_service_account"], binding["member"])
        self.assertNotIn(config["bff_service_account"], binding["member"])
        expression = binding["condition"]["expression"]
        self.assertIn("objects/exports/ready/", expression)
        for forbidden in ("objects/exports/tmp/", "objects/users/", "storage.objects.list",
                          "storage.objects.create", "storage.objects.delete", "storage.objects.update"):
            self.assertNotIn(forbidden, expression)
            self.assertNotIn(forbidden, config["storage_roles"]["readyArchiveReader"])

    def test_ambiguous_service_account_create_retains_unknown_ownership_across_rerun(self):
        config = load_contract()
        email = config["runtime_service_account"]
        actual = {"email": email, "name": "projects/llm-wiki-cloud/serviceAccounts/" + email}
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            describes = 0

            def run(args, **kwargs):
                nonlocal describes
                if args[:4] == ["gcloud", "iam", "service-accounts", "describe"]:
                    describes += 1
                    if describes == 1:
                        return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                    return subprocess.CompletedProcess(args, 0, json.dumps(actual), "")
                if args[:4] == ["gcloud", "iam", "service-accounts", "create"]:
                    return subprocess.CompletedProcess(args, 1, "", "deadline exceeded")
                self.fail("unexpected command")

            p = Provisioner(config, run, path)
            p.ensure_service_account(email, "lwc-export-worker-dev", "LWC DEV Export Worker")
            resource = p.evidence["resources"][email]
            self.assertEqual(resource["creation_status"], "unknown")
            self.assertIsNone(resource["created_by_this_run"])
            self.assertEqual(resource["readback_identity"]["email"], email)

            rerun = Provisioner(config, lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(actual), ""), path)
            rerun.ensure_service_account(email, "lwc-export-worker-dev", "LWC DEV Export Worker")
            self.assertEqual(rerun.evidence["resources"][email]["creation_status"], "unknown")
            self.assertIsNone(rerun.evidence["resources"][email]["created_by_this_run"])

    def test_iam_rejects_missing_etag_and_detects_removed_prior_binding(self):
        config = load_contract()
        desired = {"role": "roles/datastore.user", "member": "serviceAccount:" + config["runtime_service_account"]}
        calls = []
        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, lambda args, **kwargs: (
                calls.append(args) or subprocess.CompletedProcess(args, 0, json.dumps({"bindings": []}), "")
            ), Path(temp) / "evidence.json")
            with self.assertRaises(ProvisionError):
                p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                         ["projects", "set-iam-policy", "llm-wiki-cloud"], desired)
            self.assertEqual(len(calls), 1)
            self.assertEqual(p.evidence["policies"][0]["status"], "missing_etag")

        before = {"etag": "etag-1", "bindings": [{"role": "roles/viewer", "members": ["user:kept@example.com"]}]}
        after = {"etag": "etag-2", "bindings": [{"role": desired["role"], "members": [desired["member"]]}]}
        sets = 0

        def run(args, **kwargs):
            nonlocal sets
            if args[:3] == ["gcloud", "projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(before if sets == 0 else after), "")
            if args[:3] == ["gcloud", "projects", "set-iam-policy"]:
                sets += 1
                return subprocess.CompletedProcess(args, 0, "", "")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, run, Path(temp) / "evidence.json")
            with self.assertRaises(ProvisionError):
                p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                         ["projects", "set-iam-policy", "llm-wiki-cloud"], desired)
            self.assertEqual(sets, 1)
            self.assertEqual(p.evidence["policies"][0]["status"], "readback_missing_binding")

    def test_ambiguous_iam_failure_keeps_the_full_before_policy_on_disk(self):
        config = load_contract()
        before = {"etag": "etag-1", "bindings": [{"role": "roles/viewer", "members": ["user:kept@example.com"]}]}
        desired = {"role": "roles/datastore.user", "member": "serviceAccount:" + config["runtime_service_account"]}

        def run(args, **kwargs):
            if args[:3] == ["gcloud", "projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(before), "")
            if args[:3] == ["gcloud", "projects", "set-iam-policy"]:
                return subprocess.CompletedProcess(args, 1, "", "deadline exceeded")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "evidence.json"
            p = Provisioner(config, run, path)
            with self.assertRaises(ProvisionError):
                p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                         ["projects", "set-iam-policy", "llm-wiki-cloud"], desired)
            saved = json.loads(path.read_text())["policies"][0]
            self.assertEqual(saved["status"], "mutation_failed_or_ambiguous")
            self.assertEqual(saved["attempts"][0]["before_policy"], before)

    def test_service_account_absent_then_existing_and_partial_resource(self):
        config = load_contract()
        email = config["runtime_service_account"]
        created = {"email": email, "name": "projects/llm-wiki-cloud/serviceAccounts/" + email}
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:4] == ["gcloud", "iam", "service-accounts", "describe"]:
                if len([c for c in calls if c[:4] == args[:4]]) == 1:
                    return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                return subprocess.CompletedProcess(args, 0, json.dumps(created), "")
            if args[:4] == ["gcloud", "iam", "service-accounts", "create"]:
                return subprocess.CompletedProcess(args, 0, "created", "")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, run, Path(temp) / "evidence.json")
            p.ensure_service_account(email, "lwc-export-worker-dev", "LWC DEV Export Worker")
            self.assertTrue(p.evidence["resources"][email]["created_by_this_run"])
            p.run = lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(created), "")
            p.ensure_service_account(email, "lwc-export-worker-dev", "LWC DEV Export Worker")
            self.assertTrue(p.evidence["resources"][email]["created_by_this_run"])
            partial = dict(created, email="wrong@llm-wiki-cloud.iam.gserviceaccount.com")
            p.run = lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(partial), "")
            with self.assertRaises(ProvisionError):
                p.ensure_service_account(email, "lwc-export-worker-dev", "LWC DEV Export Worker")

    def test_policy_etag_conflict_rereads_preserves_concurrent_binding_and_adds_only(self):
        config = load_contract()
        binding = {"role": "roles/datastore.user", "member": "serviceAccount:" + config["runtime_service_account"],
                   "condition": {"title": "database", "expression": "resource.name == 'db'"}}
        first = {"version": 3, "etag": "etag-1", "bindings": []}
        concurrent = {"role": "roles/viewer", "members": ["user:operator@example.com"]}
        second = {"version": 3, "etag": "etag-2", "bindings": [concurrent]}
        current = copy.deepcopy(second)
        sets = 0

        def run(args, **kwargs):
            nonlocal sets, current
            if args[:3] == ["gcloud", "projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(first if sets == 0 else current), "")
            if args[:3] == ["gcloud", "projects", "set-iam-policy"]:
                policy = json.loads(Path(args[4]).read_text())
                sets += 1
                if sets == 1:
                    first["etag"] = "etag-2"
                    first["bindings"] = [concurrent]
                    return subprocess.CompletedProcess(args, 1, "", "etag mismatch: conditionNotMet")
                self.assertEqual(policy["etag"], "etag-2")
                self.assertIn(concurrent, policy["bindings"])
                current = dict(policy, etag="etag-3")
                return subprocess.CompletedProcess(args, 0, "", "")
            self.fail("unexpected policy command")

        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, run, Path(temp) / "evidence.json")
            p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                     ["projects", "set-iam-policy", "llm-wiki-cloud"], binding)
            self.assertEqual(sets, 2)
            self.assertTrue(p.policy_has(current, binding))
            self.assertIn(concurrent, current["bindings"])
            p.run = lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(current), "")
            p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                     ["projects", "set-iam-policy", "llm-wiki-cloud"], binding)
            self.assertEqual(len(p.evidence["policies"]), 2)
            self.assertFalse(p.evidence["policies"][-1]["added_by_this_run"])

    def test_existing_same_member_with_different_condition_fails_closed(self):
        config = load_contract()
        member = "serviceAccount:" + config["runtime_service_account"]
        desired = {"role": "roles/datastore.user", "member": member, "condition": {"title": "db", "expression": "db-dev"}}
        existing = {"version": 3, "etag": "etag-1", "bindings": [{"role": desired["role"], "members": [member],
                                                                         "condition": {"title": "other", "expression": "db-prod"}}]}
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:3] == ["gcloud", "projects", "get-iam-policy"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(existing), "")
            self.fail("mismatched existing grants must not be mutated")

        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, run, Path(temp) / "evidence.json")
            with self.assertRaises(ProvisionError):
                p.policy("project:llm-wiki-cloud", ["projects", "get-iam-policy", "llm-wiki-cloud"],
                         ["projects", "set-iam-policy", "llm-wiki-cloud"], desired)
            self.assertEqual(len(calls), 1)
            self.assertEqual(p.evidence["policies"][0]["status"], "existing_member_has_different_condition")

    def test_custom_roles_create_exactly_and_reject_existing_extra_permissions(self):
        config = load_contract()
        role = {"includedPermissions": ["storage.objects.list"], "stage": "GA"}
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:4] == ["gcloud", "iam", "roles", "describe"]:
                if len([c for c in calls if c[:4] == args[:4]]) == 1:
                    return subprocess.CompletedProcess(args, 1, "", "NOT_FOUND")
                return subprocess.CompletedProcess(args, 0, json.dumps(role), "")
            if args[:4] == ["gcloud", "iam", "roles", "create"]:
                self.assertIn("storage.objects.list", args)
                return subprocess.CompletedProcess(args, 0, "created", "")
            self.fail("unexpected command")

        with tempfile.TemporaryDirectory() as temp:
            p = Provisioner(config, run, Path(temp) / "evidence.json")
            self.assertEqual(p.ensure_custom_role("objectLister"), "projects/llm-wiki-cloud/roles/lwcExportObjectLister")
            self.assertTrue(p.evidence["resources"]["projects/llm-wiki-cloud/roles/lwcExportObjectLister"]["created_by_this_run"])
            role["includedPermissions"].append("storage.objects.get")
            p.run = lambda args, **kwargs: subprocess.CompletedProcess(args, 0, json.dumps(role), "")
            with self.assertRaises(ProvisionError):
                p.ensure_custom_role("objectLister")


if __name__ == "__main__":
    unittest.main()
