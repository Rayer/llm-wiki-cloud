"""Offline contract tests for the fixed Production Export IAM helper."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from deploy.provision import exportjob_prod as prod


CONTRACT = prod.expected_contract()
MEMBER = prod.MEMBER
ROLE = prod.ROLE_NAME


def response(args, code=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(args, code, stdout, stderr)


class FakeProvider:
    def __init__(self, policy=None, role=None):
        self.policy = copy.deepcopy(policy or {
            "etag": "bucket-e1",
            "version": 3,
            "bindings": [
                {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]},
                {"role": "roles/storage.objectAdmin", "members": ["user:admin@example.test"],
                 "condition": {"title": "existing", "expression": "resource.name.startsWith('safe/')"}},
            ],
        })
        self.role = copy.deepcopy(role or {
            "name": prod.ROLE_NAME,
            "stage": "GA",
            "includedPermissions": ["storage.objects.list"],
        })
        self.calls = []
        self.policy_reads = 0
        self.fail_policy_read_at = set()
        self.policy_reads_without_etag = set()
        self.deny_role_read = False
        self.write_actions = []
        self.writes = []

    def __call__(self, args):
        self.calls.append(list(args))
        if args[1:4] == ["iam", "roles", "describe"]:
            if self.deny_role_read:
                return response(args, 1, stderr="PERMISSION_DENIED")
            if self.role is None:
                return response(args, 1, stderr="NOT_FOUND")
            return response(args, stdout=json.dumps(self.role))
        if args[1:4] == ["storage", "buckets", "get-iam-policy"]:
            self.policy_reads += 1
            if self.policy_reads in self.fail_policy_read_at:
                return response(args, 1, stderr="PERMISSION_DENIED")
            policy = copy.deepcopy(self.policy)
            if self.policy_reads in self.policy_reads_without_etag:
                policy.pop("etag", None)
            return response(args, stdout=json.dumps(policy))
        if args[1:4] == ["storage", "buckets", "set-iam-policy"]:
            policy_path = Path(args[-2])
            submitted = json.loads(policy_path.read_text())
            self.writes.append(submitted)
            action = self.write_actions.pop(0) if self.write_actions else "success"
            if action == "conflict":
                self.policy["bindings"].append({
                    "role": "roles/logging.logWriter",
                    "members": [f"user:concurrent-{len(self.writes)}@example.test"],
                    "condition": {"title": "concurrent", "expression": "resource.name.startsWith('logs/')"},
                })
                self.policy["etag"] = f"bucket-concurrent-{len(self.writes)}"
                return response(args, 1, stderr="HTTP 412 conditionNotMet: etag mismatch")
            if action == "denied":
                return response(args, 1, stderr="PERMISSION_DENIED: storage.buckets.setIamPolicy")
            if action == "transport_unknown":
                raise OSError("simulated connection reset after request transmission")
            if action != "success":
                raise AssertionError(f"unexpected fake write action: {action}")
            if submitted.get("etag") != self.policy.get("etag"):
                return response(args, 1, stderr="HTTP 412 conditionNotMet: etag mismatch")
            self.policy = copy.deepcopy(submitted)
            self.policy["etag"] = f"bucket-write-{len(self.writes)}"
            return response(args)
        raise AssertionError("unexpected provider operation: " + " ".join(args))


class ExportJobProdProvisionTests(unittest.TestCase):
    def provisioner(self, fake, directory):
        return prod.Provisioner(CONTRACT, fake, Path(directory) / "evidence.json")

    def assert_only_fixed_provider_calls(self, fake):
        for args in fake.calls:
            self.assertEqual(args[0], "gcloud")
            if args[1:4] == ["iam", "roles", "describe"]:
                self.assertEqual(args[4:7], ["lwcExportObjectLister", "--project", "llm-wiki-cloud"])
            else:
                self.assertIn(args[1:4], (
                    ["storage", "buckets", "get-iam-policy"],
                    ["storage", "buckets", "set-iam-policy"],
                ))
                self.assertEqual(args[4], "gs://llm-wiki-data")
        self.assertFalse(any("projects" in args or "service-accounts" in args for args in fake.calls))

    def test_declaration_is_the_exact_fixed_production_binding(self):
        self.assertEqual(prod.load_contract(), {
            "environment": "production", "project": "llm-wiki-cloud", "bucket": "llm-wiki-data",
            "runtime_service_account": "lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com",
            "role": "projects/llm-wiki-cloud/roles/lwcExportObjectLister",
            "included_permissions": ["storage.objects.list"],
        })

    def test_wrong_target_or_added_contract_field_is_rejected_before_provider_work(self):
        for key, value in (("environment", "development"), ("bucket", "another-bucket"),
                           ("runtime_service_account", "someone-else@example.test"),
                           ("included_permissions", ["storage.objects.list", "storage.objects.get"])):
            with self.subTest(key=key):
                contract = copy.deepcopy(CONTRACT)
                contract[key] = value
                fake = FakeProvider()
                with self.assertRaises(prod.ProvisionError):
                    with tempfile.TemporaryDirectory() as directory:
                        prod.Provisioner(contract, fake, Path(directory) / "evidence.json")
                self.assertEqual(fake.calls, [])
        contract = copy.deepcopy(CONTRACT)
        contract["extra_target"] = "unexpected"
        with self.assertRaises(prod.ProvisionError):
            prod.validate_contract(contract)

    def test_role_read_denial_and_missing_role_fail_before_bucket_policy_access(self):
        for fake in (FakeProvider(), FakeProvider(role=None)):
            if fake.role is not None:
                fake.deny_role_read = True
            with self.subTest(role=fake.role, denied=fake.deny_role_read), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(prod.ProvisionError):
                    self.provisioner(fake, directory).apply()
                self.assertEqual(fake.policy_reads, 0)
                self.assertEqual(fake.writes, [])
                self.assertEqual(json.loads((Path(directory) / "evidence.json").read_text())["invocations"][0]["status"], "failed")

    def test_role_missing_or_extra_permission_fails_without_bucket_access(self):
        for permissions in ([], ["storage.objects.list", "storage.objects.get"]):
            fake = FakeProvider(role={"name": ROLE, "stage": "GA", "includedPermissions": permissions})
            with self.subTest(permissions=permissions), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(prod.ProvisionError):
                    self.provisioner(fake, directory).apply()
                self.assertEqual(fake.policy_reads, 0)
                self.assertEqual(fake.writes, [])

    def test_bucket_policy_read_denial_fails_without_write(self):
        fake = FakeProvider()
        fake.fail_policy_read_at.add(1)
        with tempfile.TemporaryDirectory() as directory, self.assertRaises(prod.ProvisionError):
            self.provisioner(fake, directory).apply()
        self.assertEqual(fake.writes, [])
        self.assert_only_fixed_provider_calls(fake)

    def test_existing_exact_binding_is_verified_noop(self):
        fake = FakeProvider(policy={"etag": "bucket-e1", "bindings": [
            {"role": ROLE, "members": [MEMBER, "user:other@example.test"]},
        ]})
        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(fake, directory).apply()
        self.assertEqual(result, "verified_noop")
        self.assertEqual(fake.writes, [])
        self.assert_only_fixed_provider_calls(fake)

    def test_conflict_is_checked_before_exact_noop_when_both_bindings_exist(self):
        fake = FakeProvider(policy={"etag": "bucket-e1", "bindings": [
            {"role": ROLE, "members": [MEMBER]},
            {"role": ROLE, "members": [MEMBER], "condition": {"title": "restricted", "expression": "true"}},
        ]})
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(prod.ProvisionError, "conflicting condition"):
            self.provisioner(fake, directory).apply()
        self.assertEqual(fake.writes, [])

    def test_missing_etag_never_writes(self):
        fake = FakeProvider(policy={"bindings": [{"role": "roles/storage.objectViewer", "members": ["user:x@example.test"]}]})
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(prod.ProvisionError, "no etag"):
            self.provisioner(fake, directory).apply()
        self.assertEqual(fake.writes, [])

    def test_policy_readback_preserves_every_existing_member_and_condition(self):
        policy = {"etag": "bucket-e1", "version": 3, "bindings": [
            {"role": "roles/storage.objectViewer", "members": ["user:a@example.test", "user:b@example.test"]},
            {"role": "roles/storage.objectAdmin", "members": ["user:c@example.test"],
             "condition": {"title": "existing", "description": "kept", "expression": "resource.name.startsWith('safe/')"}},
        ]}
        fake = FakeProvider(policy=policy)
        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(fake, directory).apply()
        self.assertEqual(result, "verified_addition")
        self.assertEqual(fake.policy["bindings"], policy["bindings"] + [{"role": ROLE, "members": [MEMBER]}])
        self.assertEqual(fake.writes[0]["etag"], "bucket-e1")
        self.assert_only_fixed_provider_calls(fake)

    def test_bounded_etag_conflicts_retry_after_full_fresh_policy_read(self):
        fake = FakeProvider()
        fake.write_actions = ["conflict", "success"]
        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(fake, directory).apply()
        self.assertEqual(result, "verified_addition")
        self.assertEqual(len(fake.writes), 2)
        self.assertEqual(fake.writes[0]["etag"], "bucket-e1")
        self.assertEqual(fake.writes[1]["etag"], "bucket-concurrent-1")
        self.assertEqual(fake.policy["bindings"][-1]["members"], [MEMBER])
        self.assertEqual(fake.policy["bindings"][-2]["members"], ["user:concurrent-1@example.test"])
        self.assertEqual(fake.policy_reads, 3)  # initial read, conflict refresh, successful readback
        self.assert_only_fixed_provider_calls(fake)

    def test_conflict_exhaustion_is_bounded_and_never_drops_fresh_policy(self):
        fake = FakeProvider()
        fake.write_actions = ["conflict"] * prod.MAX_ETAG_ATTEMPTS
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(prod.ProvisionError, "bounded conflict"):
            self.provisioner(fake, directory).apply()
        self.assertEqual(len(fake.writes), prod.MAX_ETAG_ATTEMPTS)
        self.assertEqual(fake.policy_reads, prod.MAX_ETAG_ATTEMPTS)
        self.assertEqual(fake.writes[-1]["etag"], f"bucket-concurrent-{prod.MAX_ETAG_ATTEMPTS - 1}")
        self.assert_only_fixed_provider_calls(fake)

    def test_denied_write_is_read_back_and_not_replayed(self):
        fake = FakeProvider()
        fake.write_actions = ["denied"]
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(prod.ProvisionError, "readback"):
            self.provisioner(fake, directory).apply()
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(fake.policy_reads, 2)
        self.assert_only_fixed_provider_calls(fake)

    def test_unknown_write_readback_stays_unknown_without_replay(self):
        fake = FakeProvider()
        fake.write_actions = ["transport_unknown"]
        fake.fail_policy_read_at.add(2)
        with tempfile.TemporaryDirectory() as directory:
            provisioner = self.provisioner(fake, directory)
            with self.assertRaisesRegex(prod.ProvisionError, "remains unknown"):
                provisioner.apply()
            evidence = json.loads((Path(directory) / "evidence.json").read_text())
        self.assertEqual(evidence["invocations"][0]["status"], "unknown")
        self.assertEqual(evidence["invocations"][0]["policy_attempts"][0]["status"], "unknown")
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(fake.policy_reads, 2)
        self.assert_only_fixed_provider_calls(fake)

    def test_ambiguous_write_without_readback_etag_stays_unknown_without_replay(self):
        fake = FakeProvider()
        fake.write_actions = ["transport_unknown"]
        fake.policy_reads_without_etag.add(2)
        with tempfile.TemporaryDirectory() as directory:
            provisioner = self.provisioner(fake, directory)
            with self.assertRaisesRegex(prod.ProvisionError, "remains unknown"):
                provisioner.apply()
            evidence = json.loads((Path(directory) / "evidence.json").read_text())
        self.assertEqual(evidence["invocations"][0]["status"], "unknown")
        self.assertEqual(evidence["invocations"][0]["policy_attempts"][0]["status"], "unknown")
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(fake.policy_reads, 2)
        self.assert_only_fixed_provider_calls(fake)

    def test_ambiguous_write_reconciles_if_exact_binding_is_read_back(self):
        fake = FakeProvider()
        fake.write_actions = ["transport_unknown"]

        def apply_then_lose_response(args):
            policy = json.loads(Path(args[-2]).read_text())
            fake.policy = policy
            fake.policy["etag"] = "bucket-applied-unknown"
            raise OSError("simulated lost response after application")

        fake.write_actions = ["transport_unknown"]
        # Replace the write method for this case to model apply-then-response-loss.
        original = fake.__call__

        def run(args):
            if args[1:4] == ["storage", "buckets", "set-iam-policy"]:
                fake.calls.append(list(args))
                fake.writes.append(json.loads(Path(args[-2]).read_text()))
                apply_then_lose_response(args)
            return original(args)

        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(run, directory).apply()
        self.assertEqual(result, "reconciled_ambiguous_write")
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(fake.policy_reads, 2)
        self.assert_only_fixed_provider_calls(fake)


if __name__ == "__main__":
    unittest.main()
