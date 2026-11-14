"""Offline contract tests for the fixed Production Export IAM helper."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from deploy.provision import exportjob_prod as prod


CONTRACT = prod.expected_contract()
MEMBER = prod.MEMBER
ROLE = prod.ROLE_NAME
_DEFAULT_ROLE = object()


def response(args, code=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(args, code, stdout, stderr)


class FakeProvider:
    def __init__(self, policy=None, role=_DEFAULT_ROLE):
        self.policy = copy.deepcopy(policy if policy is not None else {
            "etag": "bucket-e1",
            "version": 3,
            "bindings": [
                {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]},
                {"role": "roles/storage.objectAdmin", "members": ["user:admin@example.test"],
                 "condition": {"title": "existing", "expression": "resource.name.startsWith('safe/')"}},
            ],
        })
        if role is _DEFAULT_ROLE:
            role = {
                "name": prod.ROLE_NAME,
                "stage": "GA",
                "includedPermissions": ["storage.objects.list"],
            }
        self.role = copy.deepcopy(role)
        self.policy.setdefault("version", prod.IAM_POLICY_VERSION)
        self.calls = []
        self.policy_reads = 0
        self.fail_policy_read_at = set()
        self.policy_reads_without_etag = set()
        self.deny_role_read = False
        self.role_not_found_calls = 0
        self.drop_condition_on_readback = False
        self.drop_target_member_on_readback = False
        self.write_actions = []
        self.writes = []

    def __call__(self, args):
        self.calls.append(list(args))
        if args[1:4] == ["iam", "roles", "describe"]:
            if self.deny_role_read:
                return response(args, 1, stderr="PERMISSION_DENIED")
            if self.role is None:
                self.role_not_found_calls += 1
                return response(args, 1, stderr="NOT_FOUND")
            return response(args, stdout=json.dumps(self.role))
        if args[1:4] == ["storage", "buckets", "get-iam-policy"]:
            self.policy_reads += 1
            if self.policy_reads in self.fail_policy_read_at:
                return response(args, 1, stderr="PERMISSION_DENIED")
            policy = copy.deepcopy(self.policy)
            if self.policy_reads == 2 and self.drop_condition_on_readback:
                for binding in policy.get("bindings", []):
                    if binding.get("condition") is not None:
                        binding.pop("condition")
                        break
            if self.policy_reads == 2 and self.drop_target_member_on_readback:
                for binding in policy.get("bindings", []):
                    if binding.get("role") == ROLE and binding.get("condition") is None:
                        binding["members"] = [member for member in binding.get("members", []) if member != MEMBER]
            if self.policy_reads in self.policy_reads_without_etag:
                policy.pop("etag", None)
            # Installed gcloud 589 requests v3 but projects only bindings and etag.
            wire_policy = {key: policy[key] for key in prod.IAM_POLICY_FIELDS if key in policy}
            return response(args, stdout=json.dumps(wire_policy))
        if args[1:4] == ["storage", "buckets", "set-iam-policy"]:
            policy_path = Path(args[-2])
            submitted = json.loads(policy_path.read_text())
            self.writes.append(submitted)
            serialized = copy.deepcopy(submitted)
            serialized["version"] = prod.IAM_POLICY_VERSION  # gcloud set-iam-policy serializer behavior
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
            self.policy = serialized
            self.policy["etag"] = f"bucket-write-{len(self.writes)}"
            return response(args)
        raise AssertionError("unexpected provider operation: " + " ".join(args))


class GCloudPolicyWireTransport:
    """Injected subprocess boundary capturing the installed gcloud CLI's JSON wire shape."""

    def __init__(self, get_policy):
        self.get_policy = copy.deepcopy(get_policy)
        self.calls = []
        self.set_payload = None

    def __call__(self, args):
        self.calls.append(list(args))
        if args[1:4] == ["storage", "buckets", "get-iam-policy"]:
            return response(args, stdout=json.dumps(self.get_policy))
        if args[1:4] == ["storage", "buckets", "set-iam-policy"]:
            self.set_payload = json.loads(Path(args[-2]).read_text())
            return response(args)
        raise AssertionError("unexpected gcloud policy adapter command: " + " ".join(args))


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

    def test_gcloud_policy_adapter_normalizes_get_projection_and_serializes_v3_write(self):
        policy = {"etag": "bucket-e1", "version": 3, "bindings": [
            {"role": "roles/storage.objectAdmin", "members": ["user:admin@example.test"],
             "condition": {"title": "preserve", "expression": "resource.name.startsWith('safe/')"}},
        ]}
        wire = {"etag": policy["etag"], "bindings": policy["bindings"]}
        transport = GCloudPolicyWireTransport(wire)

        def call_gcloud(args):
            return transport(["gcloud", *args])

        adapter = prod.GCloudBucketPolicyAdapter(call_gcloud)
        readback = adapter.read("llm-wiki-data")
        self.assertEqual(readback.wire_policy, {"etag": "bucket-e1", "bindings": policy["bindings"]})
        self.assertNotIn("version", readback.wire_policy)
        self.assertEqual(readback.policy["version"], prod.IAM_POLICY_VERSION)
        self.assertEqual(readback.normalization["requested_policy_version"], 3)
        self.assertEqual(readback.normalization["requested_fields"], ["bindings", "etag"])
        self.assertEqual(readback.normalization["version_source"], "inferred_from_explicit_requested_policy_version")
        self.assertEqual(readback.policy["bindings"], policy["bindings"])

        with tempfile.TemporaryDirectory() as directory:
            policy_path = Path(directory) / "policy.json"
            submitted = adapter.write_file(readback.policy, policy_path)
            self.assertEqual(json.loads(policy_path.read_text()), submitted)
            self.assertEqual(submitted["version"], 3)
            self.assertEqual(submitted["etag"], "bucket-e1")
            self.assertEqual(submitted["bindings"], policy["bindings"])
            result = call_gcloud(adapter.set_args("llm-wiki-data", policy_path))
        self.assertEqual(result.returncode, 0)
        self.assertEqual(transport.set_payload, submitted)
        self.assertEqual([call[1:4] for call in transport.calls], [
            ["storage", "buckets", "get-iam-policy"], ["storage", "buckets", "set-iam-policy"],
        ])
        self.assertEqual(transport.calls[0], ["gcloud", "storage", "buckets", "get-iam-policy",
                                               "gs://llm-wiki-data", "--format=json", "--quiet"])
        self.assertEqual(transport.calls[1][4], "gs://llm-wiki-data")
        self.assertEqual(transport.calls[1][-1], "--quiet")

    def test_policy_version_normalization_requires_v3_and_set_serializer_rejects_missing_or_wrong_version(self):
        wire_policy = {"etag": "bucket-e1", "bindings": []}
        with self.assertRaisesRegex(prod.ProvisionError, "explicit v3 request"):
            prod.normalize_gcloud_policy(wire_policy, requested_version=None)
        with self.assertRaisesRegex(prod.ProvisionError, "unexpected policy version"):
            prod.normalize_gcloud_policy({**wire_policy, "version": 1}, requested_version=3)
        with self.assertRaisesRegex(prod.ProvisionError, "requires the verified v3"):
            prod.GCloudBucketPolicyAdapter.serialize_for_write(wire_policy)
        with self.assertRaisesRegex(prod.ProvisionError, "requires the verified v3"):
            prod.GCloudBucketPolicyAdapter.serialize_for_write({**wire_policy, "version": 1})

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
        denied = FakeProvider()
        denied.deny_role_read = True
        missing = FakeProvider(role=None)
        for fake in (denied, missing):
            with self.subTest(role=fake.role, denied=fake.deny_role_read), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(prod.ProvisionError):
                    self.provisioner(fake, directory).apply()
                self.assertEqual(fake.policy_reads, 0)
                self.assertEqual(fake.writes, [])
                self.assertEqual(json.loads((Path(directory) / "evidence.json").read_text())["invocations"][0]["status"], "failed")
        self.assertEqual(denied.role_not_found_calls, 0)
        self.assertEqual(missing.role_not_found_calls, 1)

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
            evidence = json.loads((Path(directory) / "evidence.json").read_text())
        self.assertEqual(result, "verified_addition")
        self.assertEqual(fake.policy["bindings"], policy["bindings"] + [{"role": ROLE, "members": [MEMBER]}])
        self.assertEqual(fake.writes[0]["etag"], "bucket-e1")
        attempt = evidence["invocations"][0]["policy_attempts"][0]
        self.assertEqual(attempt["before_policy"]["version"], 3)
        self.assertEqual(attempt["after_policy"]["version"], 3)
        self.assertNotIn("version", attempt["before_wire_policy"])
        self.assertNotIn("version", attempt["after_wire_policy"])
        self.assertEqual(attempt["before_policy_normalization"]["version_source"], "inferred_from_explicit_requested_policy_version")
        self.assertEqual(attempt["after_policy_normalization"]["version_source"], "inferred_from_explicit_requested_policy_version")
        self.assertEqual(attempt["set_policy_serialization"]["version"], 3)
        self.assert_only_fixed_provider_calls(fake)

    def test_condition_or_target_member_loss_on_readback_fails_closed(self):
        for lost_field in ("condition", "member"):
            fake = FakeProvider()
            if lost_field == "condition":
                fake.drop_condition_on_readback = True
            else:
                fake.drop_target_member_on_readback = True
            with self.subTest(lost_field=lost_field), tempfile.TemporaryDirectory() as directory:
                with self.assertRaisesRegex(prod.ProvisionError, "did not verify"):
                    self.provisioner(fake, directory).apply()
                evidence = json.loads((Path(directory) / "evidence.json").read_text())
            self.assertEqual(evidence["invocations"][0]["status"], "failed")
            self.assertEqual(len(fake.writes), 1)
            self.assertEqual(fake.policy_reads, 2)

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
