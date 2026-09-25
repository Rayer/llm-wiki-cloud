import json
import subprocess
import tempfile
import unittest
from pathlib import Path
import sys
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import exportjob_dev as provision


CONFIG = provision.load_contract()


class ExportJobProvisionTests(unittest.TestCase):
    def provisioner(self, root, run=lambda _: subprocess.CompletedProcess([], 0, "", "")):
        return provision.Provisioner(CONFIG, run=run, evidence_path=Path(root) / "evidence.json")

    def test_expected_absence_for_supported_describes(self):
        with tempfile.TemporaryDirectory() as root:
            p = self.provisioner(root)
            cases = [
                (("run", "jobs", "describe", "export-job-dev"), "ERROR: (gcloud.run.jobs.describe) Cannot find job [export-job-dev]."),
                (("iam", "service-accounts", "describe", "missing@example"), "NOT_FOUND: Service account does not exist"),
                (("iam", "roles", "describe", "missing-role"), "ERROR: role not found"),
            ]
            for args, message in cases:
                with self.subTest(args=args):
                    p.run = lambda _: subprocess.CompletedProcess([], 1, "", message)
                    self.assertIsNone(p.gcloud(*args, allow_missing=True))

    def test_permission_auth_and_transport_failures_are_not_absence(self):
        with tempfile.TemporaryDirectory() as root:
            p = self.provisioner(root)
            args = ("run", "jobs", "describe", "export-job-dev")
            for message in ("PERMISSION_DENIED: permission denied", "PERMISSION_DENIED: resource not found",
                            "Unauthenticated request", "connection timed out"):
                with self.subTest(message=message):
                    p.run = lambda _, error=message: subprocess.CompletedProcess([], 1, "", error)
                    with self.assertRaises(provision.ProvisionError):
                        p.gcloud(*args, allow_missing=True)
                    record = json.loads(Path(root, "evidence.json").read_text())["provider_failure"]
                    self.assertIn(message, record["diagnostic"])

    def test_source_repair_cleanup_keeps_old_owner_receipt_and_only_removes_its_grants(self):
        old_sha = "466b54a358c5d6a9274d9c085078fc7dd2a1b938"
        new_sha = "1111111111111111111111111111111111111111"
        target = {"environment": "development", "project": "llm-wiki-cloud", "region": "asia-east1"}
        role_names = [f"projects/llm-wiki-cloud/roles/{role[0]}" for role in provision.VERIFIER_ROLES.values()]
        owner = {"schema": "lwc-344-exportjob-dev-provision-v2", "result": "owner_bootstrap_applied_and_read_back",
                 "source": {"ref": "refs/heads/develop", "sha": old_sha}, "target": target, "resources": {}, "policies": []}
        for email in (CONFIG["runtime_service_account"], CONFIG["signing_service_account"]):
            owner["resources"][email] = {"creation_status": "verified", "readback_identity": {"email": email}}
        workflow_policies = []
        for role in role_names:
            role_id = role.rsplit("/", 1)[-1]
            perms = {
                "lwcExportRoleReadback": ["iam.roles.get"],
                "lwcExportProjectPolicyReadback": ["resourcemanager.projects.getIamPolicy"],
                "lwcExportSignerPolicyReadback": ["iam.serviceAccounts.getIamPolicy"],
            }[role_id]
            owner["resources"][role] = {"creation_status": "verified", "readback_identity": {"stage": "GA", "included_permissions": perms}}
        grants = [
            ("project:llm-wiki-cloud", role_names[0], ["projects", "get-iam-policy", "llm-wiki-cloud", "--format=json", "--quiet"], ["projects", "set-iam-policy", "llm-wiki-cloud"], True),
            ("project:llm-wiki-cloud", role_names[1], ["projects", "get-iam-policy", "llm-wiki-cloud", "--format=json", "--quiet"], ["projects", "set-iam-policy", "llm-wiki-cloud"], True),
            ("serviceAccount:" + CONFIG["signing_service_account"], role_names[2], ["iam", "service-accounts", "get-iam-policy"], ["iam", "service-accounts", "set-iam-policy"], True),
        ]
        for target_name, role, _, _, added in grants:
            binding = {"role": role, "member": provision.DEPLOYER}
            owner["policies"].append({"target": target_name, "binding": binding, "status": "verified_addition", "added_by_this_run": added})
            workflow_policies.append({"target": target_name, "binding": binding, "status": "existing_verified", "before_etag": "etag"})
        workflow = {"schema": "lwc-344-exportjob-dev-provision-v2", "result": "workflow_deployed_and_read_back",
                    "source": {"ref": "refs/heads/develop", "sha": new_sha}, "target": target,
                    "image": {"status": "verified"}, "resources": {"export-job-dev": {"creation_status": "verified"}},
                    "policies": workflow_policies}
        with tempfile.TemporaryDirectory() as root:
            owner_path, workflow_path = Path(root, "owner.json"), Path(root, "workflow.json")
            owner_path.write_text(json.dumps(owner))
            workflow_path.write_text(json.dumps(workflow))
            original = owner_path.read_bytes()
            p = self.provisioner(root)
            removals = []
            p.remove_policy_member = lambda target, get_args, set_args, desired, entry: removals.append(desired)
            with patch.object(p, "_verify_owner_contract"):
                p.cleanup_verifier_grants(workflow_path, owner_path)
            self.assertEqual(len(removals), 3)
            self.assertEqual(owner_path.read_bytes(), original)
            self.assertEqual(json.loads(Path(root, "evidence.json").read_text())["verifier_cleanup"], "complete")


if __name__ == "__main__":
    unittest.main()
