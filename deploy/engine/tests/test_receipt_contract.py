from __future__ import annotations

import copy
import json
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "deploy/engine"))
import receipt_contract


FIXTURES = ROOT / "deploy/engine/tests/fixtures"


def fixture(name):
    value = json.loads((FIXTURES / name).read_text(encoding="utf-8"))
    if value.get("_fixture", {}).get("status") != "NOT PUBLISHED":
        raise AssertionError(f"{name} is not marked synthetic")
    return value["receipt"]


class ReceiptContractTests(unittest.TestCase):
    def test_frozen_legacy_receipts_remain_compatible_only_in_legacy_mode(self):
        auth = fixture("auth-receipt-v2-legacy.json")
        dev = fixture("dev-receipt-v1-legacy.json")
        self.assertTrue(receipt_contract.validate_receipt_pair(auth, dev, "legacy_monorepo"))
        with self.assertRaisesRegex(receipt_contract.ReceiptError, "Auth component receipt"):
            receipt_contract.validate_receipt_pair(auth, dev, "independent")

    def test_independent_v3_v2_receipts_pair_two_immutable_sources(self):
        auth = fixture("auth-receipt-v3-independent.json")
        dev = fixture("dev-receipt-v2-independent.json")
        self.assertTrue(receipt_contract.validate_receipt_pair(auth, dev, "independent"))
        self.assertTrue(receipt_contract.validate_receipt_pair(
            auth, dev, "independent", expected_lwc_source=dev["source"]["lwc"],
            expected_auth_source=dev["source"]["auth"], expected_components=["auth"]))

    def test_independent_auth_source_mixing_and_missing_fields_fail_closed(self):
        auth = fixture("auth-receipt-v3-independent.json")
        dev = fixture("dev-receipt-v2-independent.json")
        cases = []
        slug_only = copy.deepcopy(dev)
        slug_only["source"]["auth"]["repository_id"] = "rayer/gateauth"
        cases.append(("immutable", slug_only))
        same_source = copy.deepcopy(dev)
        same_source["source"]["auth"]["revision"] = same_source["source"]["lwc"]["sha"]
        cases.append(("invalid", same_source))
        missing_digest = copy.deepcopy(dev)
        del missing_digest["source"]["auth"]["artifact_digest"]
        cases.append(("unknown or missing fields", missing_digest))
        local_snapshot = copy.deepcopy(dev)
        local_snapshot["source"]["auth"]["repository_id"] = "local-snapshot:gateauth-module"
        cases.append(("immutable", local_snapshot))
        for label, changed in cases:
            with self.subTest(label=label):
                with self.assertRaises(receipt_contract.ReceiptError):
                    receipt_contract.validate_dev_receipt(
                        changed, "independent", changed["source"]["lwc"])

    def test_pair_rejects_mixed_auth_receipt_and_dev_image(self):
        auth = fixture("auth-receipt-v3-independent.json")
        dev = fixture("dev-receipt-v2-independent.json")
        cases = []
        wrong_tree = copy.deepcopy(dev)
        wrong_tree["source"]["auth"]["tree_sha256"] = "sha256:" + "9" * 64
        cases.append(wrong_tree)
        wrong_image = copy.deepcopy(dev)
        wrong_image["images"]["auth"] = wrong_image["images"]["auth"].rsplit("@", 1)[0] + "@sha256:" + "9" * 64
        cases.append(wrong_image)
        wrong_lwc = copy.deepcopy(dev)
        wrong_lwc["source"]["lwc"]["sha"] = "f" * 40
        cases.append(wrong_lwc)
        for changed in cases:
            with self.subTest(changed=changed["source"]):
                with self.assertRaises(receipt_contract.ReceiptError):
                    receipt_contract.validate_receipt_pair(auth, changed, "independent")

    def test_auth_receipt_shape_and_build_identity_are_strict(self):
        dev = fixture("dev-receipt-v2-independent.json")
        auth = fixture("auth-receipt-v3-independent.json")
        cases = []
        unknown = copy.deepcopy(auth)
        unknown["source_slug"] = "rayer/gateauth"
        cases.append(unknown)
        local = copy.deepcopy(auth)
        local["auth_source"]["artifact_id"] = "local-snapshot"
        cases.append(local)
        unverified = copy.deepcopy(auth)
        unverified["build"]["identity_verified"] = False
        cases.append(unverified)
        for changed in cases:
            with self.subTest(changed=changed.get("auth_source", changed.get("build"))):
                with self.assertRaises(receipt_contract.ReceiptError):
                    receipt_contract.validate_receipt_pair(changed, dev, "independent")

    def test_explicit_run_tuple_and_component_selection_are_checked(self):
        auth = fixture("auth-receipt-v3-independent.json")
        dev = fixture("dev-receipt-v2-independent.json")
        with self.assertRaisesRegex(receipt_contract.ReceiptError, "workflow run"):
            receipt_contract.validate_dev_receipt(
                dev, "independent", {**dev["source"]["lwc"], "run_attempt": 2},
                dev["source"]["auth"], ["auth"])
        with self.assertRaisesRegex(receipt_contract.ReceiptError, "selected components"):
            receipt_contract.validate_receipt_pair(
                auth, dev, "independent", expected_lwc_source=dev["source"]["lwc"],
                expected_auth_source=dev["source"]["auth"], expected_components=["bff"])


if __name__ == "__main__":
    unittest.main()
