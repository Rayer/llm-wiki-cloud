import json
import os
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

    def test_source_repair_cleanup_verifies_real_owner_contract_and_journals_removals(self):
        owner_path = Path("/Users/rayer/.hermes/profiles/chatgpt/artifacts/export-auth-orchestration/dev-bootstrap-owner-evidence.json")
        if not owner_path.exists():
            owner_path = Path(__file__).resolve().parent / "testdata/dev-bootstrap-owner-evidence-projection.json"
        original = owner_path.read_bytes()
        owner = json.loads(original)
        current_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
        target = {"environment": "development", "project": "llm-wiki-cloud", "region": "asia-east1"}
        role_names = [f"projects/llm-wiki-cloud/roles/{role[0]}" for role in provision.VERIFIER_ROLES.values()]
        workflow_policies = [{"target": target_name, "binding": {"role": role, "member": provision.DEPLOYER},
                              "status": "existing_verified", "before_etag": "verified"}
                             for target_name, role in (("project:llm-wiki-cloud", role_names[0]),
                                                       ("project:llm-wiki-cloud", role_names[1]),
                                                       ("serviceAccount:" + CONFIG["signing_service_account"], role_names[2]))]
        workflow = {"schema": "lwc-344-exportjob-dev-provision-v2", "result": "workflow_deployed_and_read_back",
                    "source": {"ref": "refs/heads/develop", "sha": current_sha}, "target": target,
                    "image": {"status": "verified"}, "resources": {"export-job-dev": {"creation_status": "verified"}},
                    "policies": workflow_policies}
        with tempfile.TemporaryDirectory() as root:
            workflow_path = Path(root, "workflow.json")
            workflow_path.write_text(json.dumps(workflow))
            state = {
                "project": {"etag": "p1", "bindings": [
                    {"role": role_names[0], "members": [provision.DEPLOYER]},
                    {"role": role_names[1], "members": [provision.DEPLOYER]},
                    {"role": "roles/viewer", "members": ["user:keep@example.com"]}]},
                "signer": {"etag": "s1", "bindings": [
                    {"role": role_names[2], "members": [provision.DEPLOYER]},
                    {"role": "roles/viewer", "members": ["user:keep@example.com"]}]}}

            def run(args):
                parts = args[1:]
                if parts[:2] == ["projects", "get-iam-policy"]:
                    return subprocess.CompletedProcess(args, 0, json.dumps(state["project"]), "")
                if parts[:3] == ["iam", "service-accounts", "get-iam-policy"]:
                    return subprocess.CompletedProcess(args, 0, json.dumps(state["signer"]), "")
                if parts[:2] == ["projects", "set-iam-policy"]:
                    state["project"] = json.loads(Path(parts[-2]).read_text())
                    state["project"]["etag"] = "p2"
                    return subprocess.CompletedProcess(args, 0, "", "")
                if parts[:3] == ["iam", "service-accounts", "set-iam-policy"]:
                    state["signer"] = json.loads(Path(parts[-2]).read_text())
                    state["signer"]["etag"] = "s2"
                    return subprocess.CompletedProcess(args, 0, "", "")
                raise AssertionError("unexpected cleanup command: " + " ".join(args))

            with patch.dict(os.environ, {"SOURCE_SHA": current_sha, "SOURCE_REF": "refs/heads/develop"}):
                p = provision.Provisioner(CONFIG, run=run, evidence_path=Path(root) / "repair-evidence.json")
                p.cleanup_verifier_grants(workflow_path, owner_path)
            self.assertEqual(state["project"]["bindings"], [{"role": "roles/viewer", "members": ["user:keep@example.com"]}])
            self.assertEqual(state["signer"]["bindings"], [{"role": "roles/viewer", "members": ["user:keep@example.com"]}])
            journal = json.loads(p.evidence_path.read_text())
            entries = [item for item in journal["policies"] if item.get("source_repair_owner_sha")]
            self.assertEqual(len(entries), 3)
            for entry in entries:
                self.assertTrue(entry["added_by_this_run"])
                self.assertEqual(entry["source_repair_owner_sha"], owner["source"]["sha"])
                self.assertEqual(entry["source_repair_workflow_sha"], current_sha)
                self.assertEqual(entry["cleanup_status"], "verified_removed")
                self.assertIn(entry["cleanup_attempts"][0]["before_etag"],
                              {"p1", "p2"} if entry["target"] == "project:llm-wiki-cloud" else {"s1"})
                self.assertIn("fresh-etag", entry["inverse"])
            self.assertEqual(owner_path.read_bytes(), original)
            self.assertEqual(journal["source_repair"]["owner_receipt"], "preserved_separate_file")

    def test_source_repair_cleanup_rejects_workflow_from_other_current_sha(self):
        owner = Path(__file__).resolve().parent / "testdata/dev-bootstrap-owner-evidence-projection.json"
        workflow = {"source": {"ref": "refs/heads/develop", "sha": "b" * 40},
                    "result": "workflow_deployed_and_read_back", "schema": "lwc-344-exportjob-dev-provision-v2",
                    "target": {"environment": "development", "project": "llm-wiki-cloud", "region": "asia-east1"},
                    "image": {"status": "verified"}}
        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "workflow.json")
            path.write_text(json.dumps(workflow))
            with patch.dict(os.environ, {"SOURCE_SHA": "c" * 40, "SOURCE_REF": "refs/heads/develop"}):
                p = provision.Provisioner(CONFIG, evidence_path=Path(root, "repair.json"))
            with self.assertRaisesRegex(provision.ProvisionError, "matching owner-bootstrap"):
                p.cleanup_verifier_grants(path, owner)
            self.assertFalse(p.evidence_path.exists())

    def test_source_repair_contract_mismatch_fails_before_provider_calls(self):
        owner_path = Path(__file__).resolve().parent / "testdata/dev-bootstrap-owner-evidence-projection.json"
        owner = json.loads(owner_path.read_text())
        owner["resources"]["projects/llm-wiki-cloud/roles/lwcExportArchiveWriter"]["readback_identity"]["included_permissions"].append("resourcemanager.projects.setIamPolicy")
        current_sha = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
        workflow = {"source": {"ref": "refs/heads/develop", "sha": current_sha},
                    "result": "workflow_deployed_and_read_back", "schema": "lwc-344-exportjob-dev-provision-v2",
                    "target": {"environment": "development", "project": "llm-wiki-cloud", "region": "asia-east1"},
                    "image": {"status": "verified"}, "resources": {"export-job-dev": {"creation_status": "verified"}}}
        with tempfile.TemporaryDirectory() as root:
            owner_path = Path(root, "owner.json")
            owner_path.write_text(json.dumps(owner))
            workflow_path = Path(root, "workflow.json")
            workflow_path.write_text(json.dumps(workflow))
            calls = []
            with patch.dict(os.environ, {"SOURCE_SHA": current_sha, "SOURCE_REF": "refs/heads/develop"}):
                p = provision.Provisioner(CONFIG, run=lambda args: calls.append(args), evidence_path=Path(root, "repair.json"))
            with self.assertRaisesRegex(provision.ProvisionError, "differs from the DEV role contract"):
                p.cleanup_verifier_grants(workflow_path, owner_path)
            self.assertEqual(calls, [])
            self.assertFalse(p.evidence_path.exists())


if __name__ == "__main__":
    unittest.main()
