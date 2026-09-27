import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "merge_export_lifecycle", ROOT / "scripts" / "merge_export_lifecycle.py"
)
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)


def policy_matches(rule, object_name, age_days, days_since_custom_time=None):
    condition = rule["condition"]
    if not any(object_name.startswith(prefix) for prefix in condition["matchesPrefix"]):
        return False
    if "age" in condition and age_days < condition["age"]:
        return False
    if "daysSinceCustomTime" in condition:
        return days_since_custom_time is not None and days_since_custom_time >= condition["daysSinceCustomTime"]
    return True


class ExportLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.policy = module.load_policy(ROOT / "deploy/storage/export-lifecycle.json")

    def test_policy_scopes_ready_and_tmp_with_expected_retention(self):
        self.assertEqual(
            self.policy,
            {
                "rule": [
                    {
                        "action": {"type": "Delete"},
                        "condition": {
                            "matchesPrefix": ["exports/ready/"],
                            "daysSinceCustomTime": 3,
                        },
                    },
                    {
                        "action": {"type": "Delete"},
                        "condition": {"matchesPrefix": ["exports/tmp/"], "age": 1},
                    },
                ]
            },
        )

    def test_merge_preserves_existing_rules_and_adds_export_rules(self):
        existing = {
            "rule": [
                {
                    "action": {"type": "SetStorageClass", "storageClass": "NEARLINE"},
                    "condition": {"age": 90},
                },
                {
                    "action": {"type": "Delete"},
                    "condition": {"matchesPrefix": ["legacy/tmp/"], "age": 30},
                },
            ]
        }
        merged = module.merge_lifecycle(existing, self.policy)
        self.assertEqual(merged["rule"][:2], existing["rule"])
        self.assertEqual(merged["rule"][2:], self.policy["rule"])
        self.assertEqual(module.merge_lifecycle(merged, self.policy), merged)

    def test_merge_rejects_existing_delete_that_can_match_export(self):
        for condition in (
            {"age": 90},
            {"matchesPrefix": ["exports/"], "age": 90},
            {"matchesPrefix": ["exports/ready/"], "daysSinceCustomTime": 30},
        ):
            with self.subTest(condition=condition), self.assertRaisesRegex(
                ValueError, "overlaps export prefixes"
            ):
                module.merge_lifecycle(
                    {"rule": [{"action": {"type": "Delete"}, "condition": condition}]},
                    self.policy,
                )

    def test_merge_allows_delete_rules_with_disjoint_prefixes(self):
        existing = {
            "rule": [
                {
                    "action": {"type": "Delete"},
                    "condition": {"matchesPrefix": ["wiki/", "raw/"], "age": 90},
                }
            ]
        }
        self.assertEqual(
            module.merge_lifecycle(existing, self.policy)["rule"][:1], existing["rule"]
        )

    def test_cli_emits_merged_config_without_applying_it(self):
        existing = {"rule": [{"action": {"type": "Delete"}, "condition": {"matchesPrefix": ["old/"], "age": 30}}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "current.json"
            path.write_text(json.dumps(existing), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(ROOT / "scripts/merge_export_lifecycle.py"), "--existing", str(path)],
                check=False,
                capture_output=True,
                text=True,
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["rule"][:1], existing["rule"])
        self.assertIn("exports/ready/", result.stdout)
        self.assertIn("exports/tmp/", result.stdout)

    def test_policy_with_wrong_prefix_or_retention_is_rejected(self):
        corrupted = json.loads(json.dumps(self.policy))
        corrupted["rule"][0]["condition"]["matchesPrefix"] = ["exports/"]
        with self.assertRaisesRegex(ValueError, "must contain exactly"):
            module.validate_policy(corrupted)

    def test_malformed_existing_rules_fail_closed(self):
        with self.assertRaisesRegex(ValueError, "rule must be an array"):
            module.merge_lifecycle({"rule": {}}, self.policy)

    def test_fixture_selects_only_expired_ready_and_old_finalized_tmp_objects(self):
        ready, tmp = self.policy["rule"]
        self.assertTrue(policy_matches(ready, "exports/ready/u/p/e/archive.zip", 4, 3))
        self.assertFalse(policy_matches(ready, "exports/ready/u/p/e/archive.zip", 4, 2.99))
        self.assertTrue(policy_matches(tmp, "exports/tmp/u/p/e/archive.zip", 1, None))
        self.assertFalse(policy_matches(tmp, "exports/tmp/u/p/e/archive.zip", 23 / 24, None))
        self.assertFalse(policy_matches(ready, "wiki/alice.md", 400, 400))
        self.assertFalse(policy_matches(tmp, "raw/alice.md", 400, None))


if __name__ == "__main__":
    unittest.main()
