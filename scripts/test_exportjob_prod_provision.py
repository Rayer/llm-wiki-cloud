"""Offline contract tests for the fixed Production Export IAM helper."""
from __future__ import annotations

import base64
import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit
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
        self.omit_policy_version_on_read = False
        self.return_unconditional_version_one = False
        self.policy_version_on_read = {}
        self.policy_version_absent_at = set()
        self.deny_role_read = False
        self.role_not_found_calls = 0
        self.drop_condition_on_readback = False
        self.drop_target_member_on_readback = False
        self.write_actions = []
        self.writes = []
        self.api_calls = []

    def __call__(self, args):
        self.calls.append(list(args))
        if args[1:4] == ["iam", "roles", "describe"]:
            if self.deny_role_read:
                return response(args, 1, stderr="PERMISSION_DENIED")
            if self.role is None:
                self.role_not_found_calls += 1
                return response(args, 1, stderr="NOT_FOUND")
            return response(args, stdout=json.dumps(self.role))
        raise AssertionError("unexpected provider operation: " + " ".join(args))

    def request(self, method, url, headers, body):
        parsed = urlsplit(url)
        if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or parsed.port != 18764 or
                parsed.path != "/storage/v1/b/llm-wiki-data/iam"):
            raise AssertionError("policy adapter escaped the numeric-loopback fixed-target fixture")
        self.api_calls.append({
            "method": method,
            "url": url,
            "authorization_is_synthetic": headers.get("Authorization") == "Bearer synthetic-lwc486-fake-provider-token",
            "body": body.decode("utf-8") if body is not None else None,
        })
        query = parse_qs(parsed.query)
        if method == "GET":
            if query != {"alt": ["json"], "optionsRequestedPolicyVersion": ["3"],
                         "fields": ["bindings,etag,version"]}:
                raise AssertionError("policy read did not request the exact version-aware projection")
            self.policy_reads += 1
            if self.policy_reads in self.fail_policy_read_at:
                return 403, b'{"error":"PERMISSION_DENIED"}'
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
            if self.policy_reads in self.policy_version_absent_at or self.omit_policy_version_on_read:
                policy.pop("version", None)
            elif self.policy_reads in self.policy_version_on_read:
                policy["version"] = self.policy_version_on_read[self.policy_reads]
            elif (self.return_unconditional_version_one and
                  not any(binding.get("condition") is not None for binding in policy.get("bindings", []))):
                policy["version"] = 1
            return 200, json.dumps(policy).encode("utf-8")
        if method == "PUT":
            if query != {"alt": ["json"]}:
                raise AssertionError("policy write did not request JSON response format")
            submitted = json.loads(body)
            if not isinstance(submitted, dict):
                raise AssertionError("policy write omitted the SDK-compatible policy body")
            self.writes.append(copy.deepcopy(submitted))
            action = self.write_actions.pop(0) if self.write_actions else "success"
            if action == "conflict":
                self.policy["bindings"].append({
                    "role": "roles/logging.logWriter",
                    "members": [f"user:concurrent-{len(self.writes)}@example.test"],
                    "condition": {"title": "concurrent", "expression": "resource.name.startsWith('logs/')"},
                })
                self.policy["etag"] = f"bucket-concurrent-{len(self.writes)}"
                return 412, b'{"error":"conditionNotMet: etag mismatch"}'
            if action == "denied":
                return 403, b'{"error":"PERMISSION_DENIED: storage.buckets.setIamPolicy"}'
            if action == "transport_unknown":
                raise OSError("simulated connection reset after request transmission")
            if action == "apply_then_unknown":
                self.policy = copy.deepcopy(submitted)
                self.policy["etag"] = "bucket-applied-unknown"
                raise OSError("simulated lost response after application")
            if action != "success":
                raise AssertionError(f"unexpected fake write action: {action}")
            if submitted.get("etag") != self.policy.get("etag"):
                return 412, b'{"error":"conditionNotMet: etag mismatch"}'
            self.policy = copy.deepcopy(submitted)
            self.policy["etag"] = f"bucket-write-{len(self.writes)}"
            return 200, json.dumps(self.policy).encode("utf-8")
        raise AssertionError("unexpected fake policy API method: " + method)


class ExportJobProdProvisionTests(unittest.TestCase):
    def provisioner(self, fake, directory):
        adapter = prod.GCSBucketPolicyAdapter(
            token_provider=lambda: "synthetic-lwc486-fake-provider-token",
            transport=fake.request,
            api_base_url=prod.TEST_JSON_API_BASE,
        )
        return prod.Provisioner(CONTRACT, fake, Path(directory) / "evidence.json", policy_adapter=adapter)

    def assert_only_fixed_provider_calls(self, fake):
        for args in fake.calls:
            self.assertEqual(args[0], "gcloud")
            if args[1:4] == ["iam", "roles", "describe"]:
                self.assertEqual(args[4:7], ["lwcExportObjectLister", "--project", "llm-wiki-cloud"])
            else:
                self.fail("only the fixed custom role may be read through gcloud")
        self.assertFalse(any("projects" in args or "service-accounts" in args for args in fake.calls))
        for call in fake.api_calls:
            parsed = urlsplit(call["url"])
            self.assertEqual(parsed.hostname, "127.0.0.1")
            self.assertEqual(parsed.port, 18764)
            self.assertEqual(parsed.path, "/storage/v1/b/llm-wiki-data/iam")
            self.assertTrue(call["authorization_is_synthetic"])

    def test_json_api_adapter_requests_and_writes_complete_versioned_policy(self):
        policy = {"etag": "bucket-e1", "version": 3, "bindings": [
            {"role": "roles/storage.objectAdmin", "members": ["user:admin@example.test", "group:owners@example.test"],
             "condition": {"title": "preserve", "description": "retained", "expression": "resource.name.startsWith('safe/')"}},
        ]}
        fake = FakeProvider(policy=policy)
        adapter = prod.GCSBucketPolicyAdapter(
            token_provider=lambda: "synthetic-lwc486-fake-provider-token",
            transport=fake.request,
            api_base_url=prod.TEST_JSON_API_BASE,
        )
        readback = adapter.read("llm-wiki-data")
        self.assertEqual(readback.wire_policy, policy)
        self.assertEqual(readback.policy, policy)
        self.assertEqual(readback.normalization["requested_fields"], ["bindings", "etag", "version"])
        self.assertEqual(readback.normalization["requested_maximum_policy_version"], 3)
        self.assertEqual(readback.normalization["provider_observed_version"], 3)
        self.assertEqual(readback.normalization["normalized_policy_version"], 3)
        self.assertEqual(readback.normalization["policy_version_normalization"], "none")
        self.assertEqual(fake.api_calls[0]["method"], "GET")
        get_query = parse_qs(urlsplit(fake.api_calls[0]["url"]).query)
        self.assertEqual(get_query, {"alt": ["json"], "optionsRequestedPolicyVersion": ["3"],
                                     "fields": ["bindings,etag,version"]})

        submitted = adapter.serialize_for_write(readback.policy)
        self.assertEqual(submitted, policy)
        result = adapter.write("llm-wiki-data", submitted)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(fake.writes, [policy])
        set_call = fake.api_calls[1]
        self.assertEqual(set_call["method"], "PUT")
        self.assertEqual(json.loads(set_call["body"]), policy)
        with self.assertRaisesRegex(prod.ProvisionError, "fixed bucket"):
            adapter.read("another-bucket")
        with self.assertRaisesRegex(prod.ProvisionError, "approved fixed API"):
            prod.GCSBucketPolicyAdapter(lambda: "synthetic", transport=fake.request,
                                        api_base_url="https://storage.googleapis.com/alternate/v1")

    def test_json_api_read_requires_version_that_was_returned(self):
        adapter_for = lambda fake: prod.GCSBucketPolicyAdapter(
            token_provider=lambda: "synthetic-lwc486-fake-provider-token",
            transport=fake.request,
            api_base_url=prod.TEST_JSON_API_BASE,
        )
        wrong = FakeProvider(policy={"etag": "bucket-e1", "version": 2, "bindings": []})
        with self.assertRaisesRegex(prod.ProvisionError, "unexpected policy version"):
            adapter_for(wrong).read("llm-wiki-data")
        missing = FakeProvider()
        missing.omit_policy_version_on_read = True
        with self.assertRaisesRegex(prod.ProvisionError, "unexpected policy version"):
            adapter_for(missing).read("llm-wiki-data")
        self.assertEqual(missing.writes, [])
        inconsistent = FakeProvider(policy={"etag": "bucket-e1", "version": 1, "bindings": [
            {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"],
             "condition": {"title": "must-not-be-downgraded", "expression": "true"}},
        ]})
        with self.assertRaisesRegex(prod.ProvisionError, "v1 while containing conditions"):
            adapter_for(inconsistent).read("llm-wiki-data")
        with self.assertRaisesRegex(prod.ProvisionError, "requires the verified v3"):
            prod.GCSBucketPolicyAdapter.serialize_for_write({"etag": "bucket-e1", "bindings": []})
        with self.assertRaisesRegex(prod.ProvisionError, "requires the verified v3"):
            prod.GCSBucketPolicyAdapter.serialize_for_write({"etag": "bucket-e1", "version": 2, "bindings": []})
        with self.assertRaisesRegex(prod.ProvisionError, "requires the observed etag"):
            prod.GCSBucketPolicyAdapter.serialize_for_write({"version": 3, "bindings": []})

    def test_unconditional_v1_is_preserved_as_observed_and_normalized_for_noop_and_write(self):
        noop = FakeProvider(policy={"etag": "bucket-v1-noop", "version": 1, "bindings": [
            {"role": ROLE, "members": [MEMBER, "user:other@example.test"]},
        ]})
        noop.return_unconditional_version_one = True
        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(noop, directory).apply()
            evidence = json.loads((Path(directory) / "evidence.json").read_text())
        self.assertEqual(result, "verified_noop")
        self.assertEqual(noop.writes, [])
        attempt = evidence["invocations"][0]["policy_attempts"][0]
        self.assertEqual(attempt["before_wire_policy"]["version"], 1)
        self.assertEqual(attempt["before_policy"]["version"], 3)
        self.assertEqual(attempt["before_policy_normalization"]["provider_observed_version"], 1)
        self.assertEqual(attempt["before_policy_normalization"]["normalized_policy_version"], 3)
        self.assertEqual(attempt["before_policy_normalization"]["policy_version_normalization"],
                         "unconditional_v1_to_v3_internal_policy_view")
        self.assert_only_fixed_provider_calls(noop)

        addition = FakeProvider(policy={"etag": "bucket-v1-add", "version": 1, "bindings": [
            {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]},
        ]})
        addition.return_unconditional_version_one = True
        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(addition, directory).apply()
            evidence = json.loads((Path(directory) / "evidence.json").read_text())
        self.assertEqual(result, "verified_addition")
        self.assertEqual(len(addition.writes), 1)
        self.assertEqual(addition.writes[0]["version"], 3)
        self.assertEqual(addition.writes[0]["etag"], "bucket-v1-add")
        self.assertEqual(addition.policy_reads, 2)
        self.assertEqual(addition.policy["bindings"], [
            {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]},
            {"role": ROLE, "members": [MEMBER]},
        ])
        attempt = evidence["invocations"][0]["policy_attempts"][0]
        self.assertEqual(attempt["before_wire_policy"]["version"], 1)
        self.assertEqual(attempt["before_policy"]["version"], 3)
        self.assertEqual(attempt["after_wire_policy"]["version"], 1)
        self.assertEqual(attempt["after_policy"]["version"], 3)
        self.assertEqual(attempt["after_policy_normalization"]["provider_observed_version"], 1)
        self.assertEqual(attempt["submitted_policy"]["version"], 3)
        self.assertEqual(attempt["set_policy_serialization"]["provider_observed_version"], 1)
        self.assertEqual(attempt["set_policy_serialization"]["version"], 3)
        self.assertEqual(attempt["set_policy_serialization"]["write_policy_version_source"],
                         "explicit_v3_policy_serializer")
        self.assert_only_fixed_provider_calls(addition)

    def test_invalid_or_missing_version_on_write_readback_fails_without_replay(self):
        for case in ("wrong", "missing"):
            fake = FakeProvider()
            if case == "wrong":
                fake.policy_version_on_read[2] = 2
            else:
                fake.policy_version_absent_at.add(2)
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                with self.assertRaisesRegex(prod.ProvisionError, "unexpected policy version"):
                    self.provisioner(fake, directory).apply()
                evidence = json.loads((Path(directory) / "evidence.json").read_text())
            self.assertEqual(evidence["invocations"][0]["status"], "failed")
            self.assertEqual(evidence["invocations"][0]["policy_attempts"][0]["status"], "readback_failed")
            self.assertEqual(fake.policy_reads, 2)
            self.assertEqual(len(fake.writes), 1)
            self.assert_only_fixed_provider_calls(fake)

    def test_installed_sdk_get_policy_and_protojson_serializer_wire_fixture(self):
        gcloud = shutil.which("gcloud")
        python = shutil.which("python3.14")
        if not gcloud or not python:
            self.skipTest("installed Cloud SDK and compatible Python 3.14 are required for the wire fixture")
        sdk_root = Path(gcloud).resolve().parents[1]
        fixture = Path(__file__).with_name("lwc486_gcloud_sdk_wire_fixture.py")
        temp_root = os.environ.get("TMPDIR", "/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch")
        with tempfile.TemporaryDirectory(prefix="lwc486-sdk-wire-test-") as temp_dir:
            scratch = Path(temp_dir)
            home = scratch / "home"
            config = scratch / "cloudsdk-config"
            home.mkdir()
            config.mkdir()
            adc = scratch / "explicit-nonexistent-adc.json"
            env = {
                "PATH": os.environ.get("PATH", ""),
                "HOME": str(home),
                "CLOUDSDK_CONFIG": str(config),
                "GOOGLE_APPLICATION_CREDENTIALS": str(adc),
                "GCE_METADATA_HOST": "127.0.0.1:1",
                "GCE_METADATA_IP": "127.0.0.1",
                "CLOUDSDK_CORE_DISABLE_PROMPTS": "1",
                "CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK": "1",
                "PYTHONPATH": str(sdk_root / "lib") + os.pathsep + str(sdk_root / "lib" / "third_party"),
                "PYTHONNOUSERSITE": "1",
                "PYTHONDONTWRITEBYTECODE": "1",
                "NO_PROXY": "127.0.0.1,localhost",
                "TMPDIR": temp_root,
            }
            self.assertFalse(adc.exists())
            completed = subprocess.run([python, "-S", str(fixture)], cwd=Path(__file__).resolve().parents[1],
                                       env=env, text=True, capture_output=True, timeout=20)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        evidence = json.loads(completed.stdout)
        self.assertEqual(evidence["base_url"], "http://127.0.0.1:18764/storage/v1/")
        self.assertEqual(evidence["captured_call_count"], 12)
        self.assertEqual(evidence["version_controls"], [
            {"response_version": 2, "observed_version": 2},
            {"response_version": None, "observed_version": None},
        ])
        self.assertEqual(evidence["loss_controls"], {"member_loss_observed": True, "condition_loss_observed": True})
        for case in evidence["cases"]:
            expected_response_version = 3 if case["name"] == "conditional" else 1
            self.assertEqual(case["get_version"], expected_response_version)
            self.assertTrue(case["get_wire"]["authorization_is_synthetic"])
            get_query = parse_qs(urlsplit(case["get_wire"]["url"]).query)
            self.assertEqual(get_query, {"alt": ["json"], "optionsRequestedPolicyVersion": ["3"],
                                         "fields": ["bindings,etag,version"]})
            self.assertEqual(case["set_version"], 3)
            self.assertTrue(case["set_wire"]["authorization_is_synthetic"])
            self.assertEqual(urlsplit(case["set_wire"]["url"]).path, "/storage/v1/b/llm-wiki-data/iam")
            submitted = case["set_wire"]["body"]
            self.assertEqual(submitted["version"], 3)
            self.assertEqual(submitted["etag"], base64.b64encode(case["get_etag"].encode("utf-8")).decode("ascii"))
            self.assertEqual(submitted["bindings"], case["get_bindings"]["bindings"])
            self.assertEqual(submitted["bindings"][0].get("condition"), case["get_bindings"]["bindings"][0].get("condition"))
        self.assertIsNotNone(evidence["cases"][0]["get_bindings"]["bindings"][0].get("condition"))
        self.assertNotIn("condition", evidence["cases"][1]["get_bindings"]["bindings"][0])
        self.assertEqual(evidence["product_cases"]["unconditional_v1_noop"]["result"], "verified_noop")
        no_op = evidence["product_cases"]["unconditional_v1_noop"]
        self.assertEqual(no_op["before_wire_version"], 1)
        self.assertEqual(no_op["before_policy_version"], 3)
        self.assertEqual(no_op["normalization"]["provider_observed_version"], 1)
        self.assertEqual(no_op["normalization"]["normalized_policy_version"], 3)
        self.assertEqual(no_op["normalization"]["policy_version_normalization"],
                         "unconditional_v1_to_v3_internal_policy_view")
        self.assertEqual(no_op["write_count"], 0)
        self.assertEqual([call["method"] for call in no_op["calls"]], ["GET"])
        self.assertTrue(no_op["calls"][0]["authorization_is_synthetic"])
        self.assertEqual(parse_qs(urlsplit(no_op["calls"][0]["url"]).query), {
            "alt": ["json"], "optionsRequestedPolicyVersion": ["3"], "fields": ["bindings,etag,version"],
        })
        self.assertEqual(evidence["product_cases"]["unconditional_v1_addition"]["result"], "verified_addition")
        addition = evidence["product_cases"]["unconditional_v1_addition"]
        self.assertEqual(addition["before_wire_version"], 1)
        self.assertEqual(addition["after_wire_version"], 1)
        self.assertEqual(addition["submitted_version"], 3)
        self.assertEqual(addition["after_policy_version"], 3)
        self.assertEqual(addition["before_policy_version"], 3)
        self.assertEqual(addition["before_normalization"]["provider_observed_version"], 1)
        self.assertEqual(addition["before_normalization"]["normalized_policy_version"], 3)
        self.assertEqual(addition["after_normalization"]["provider_observed_version"], 1)
        self.assertEqual(addition["after_normalization"]["normalized_policy_version"], 3)
        self.assertEqual(addition["write_serialization"]["provider_observed_version"], 1)
        self.assertEqual(addition["write_serialization"]["version"], 3)
        self.assertEqual(addition["write_serialization"]["write_policy_version_source"],
                         "explicit_v3_policy_serializer")
        self.assertEqual(addition["methods"], ["GET", "PUT", "GET"])
        self.assertTrue(all(call["authorization_is_synthetic"] for call in addition["calls"]))
        self.assertEqual(addition["calls"][1]["body"], addition["submitted_policy"])
        self.assertEqual(parse_qs(urlsplit(addition["calls"][2]["url"]).query), {
            "alt": ["json"], "optionsRequestedPolicyVersion": ["3"], "fields": ["bindings,etag,version"],
        })
        self.assertEqual(addition["submitted_policy"]["bindings"], [
            {"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]},
            {"role": ROLE, "members": [MEMBER]},
        ])
        self.assertEqual(addition["write_count"], 1)

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
        self.assertEqual(attempt["before_wire_policy"]["version"], 3)
        self.assertEqual(attempt["after_wire_policy"]["version"], 3)
        self.assertEqual(attempt["before_policy_normalization"]["provider_observed_version"], 3)
        self.assertEqual(attempt["after_policy_normalization"]["provider_observed_version"], 3)
        self.assertEqual(attempt["before_policy_normalization"]["policy_version_normalization"], "none")
        self.assertEqual(attempt["after_policy_normalization"]["policy_version_normalization"], "none")
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
        fake.write_actions = ["apply_then_unknown"]

        with tempfile.TemporaryDirectory() as directory:
            result = self.provisioner(fake, directory).apply()
        self.assertEqual(result, "reconciled_ambiguous_write")
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(fake.policy_reads, 2)
        self.assert_only_fixed_provider_calls(fake)


if __name__ == "__main__":
    unittest.main()
