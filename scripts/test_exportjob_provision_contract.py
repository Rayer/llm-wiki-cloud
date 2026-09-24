"""Offline checks for first-time DEV Export Job provisioning and policy recovery."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from deploy.provision.exportjob_dev import (
    CONTRACT, STORAGE_EXPORTS, STORAGE_USERS, ProvisionError, Provisioner, load_contract,
)

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images/llm-wiki-bff-export-job@sha256:" + "a" * 64


class ExportJobProvisionContractTests(unittest.TestCase):
    def test_fixed_dev_contract_and_production_remains_disabled(self):
        config = load_contract()
        self.assertEqual(config["job"], "export-job-dev")
        self.assertEqual(config["runtime_service_account"], "lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com")
        self.assertEqual(config["storage_roles"]["objectLister"], ["storage.objects.list"])
        self.assertEqual(config["storage_roles"]["sourceReader"], ["storage.objects.get"])
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
        raw = json.dumps({"template": {"template": {
            "containers": [{"image": IMAGE, "env": [{"name": k, "value": v} for k, v in expected["env"].items()]}],
            "serviceAccount": expected["service_account"], "timeout": "82800s", "maxRetries": 0,
            "parallelism": 1, "taskCount": 1,
        }}})
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[:4] == ["gcloud", "run", "jobs", "describe"]:
                return subprocess.CompletedProcess(args, 0, raw, "")
            self.fail("matching existing Job must not be mutated")

        provisioner = Provisioner(config, run, Path(tempfile.gettempdir()) / "provision-test-evidence.json")
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
        raw = json.dumps({"template": {"template": {
            "containers": [{"image": IMAGE, "env": [{"name": k, "value": v} for k, v in expected["env"].items()]}],
            "serviceAccount": expected["service_account"], "timeout": "82800s", "maxRetries": 0,
            "parallelism": 1, "taskCount": 1,
        }}})
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
            self.assertFalse(p.evidence["resources"][email]["created_by_this_run"])
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
