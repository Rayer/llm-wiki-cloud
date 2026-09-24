"""Offline checks for first-time DEV Export Job provisioning and policy recovery."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from deploy.provision.exportjob_dev import (
    CONTRACT, STORAGE_EXPORTS, STORAGE_USERS, ProvisionError, Provisioner, load_contract, ready_archive_binding,
)

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff-export-job@sha256:" + "a" * 64


class ExportJobProvisionContractTests(unittest.TestCase):
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
