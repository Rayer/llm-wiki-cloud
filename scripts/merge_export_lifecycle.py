#!/usr/bin/env python3
"""Merge the export lifecycle policy into an existing GCS lifecycle config."""

import argparse
import json
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / "deploy/storage/export-lifecycle.json"
EXPECTED_RULES = [
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
EXPORT_PREFIXES = ("exports/ready/", "exports/tmp/")


def _reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def load_json(path):
    try:
        with Path(path).open(encoding="utf-8") as stream:
            return json.load(stream, object_pairs_hook=_reject_duplicate_keys)
    except (OSError, json.JSONDecodeError, ValueError) as error:
        raise ValueError(f"cannot read JSON from {path}: {error}") from error


def validate_policy(policy):
    if not isinstance(policy, dict) or policy.get("rule") != EXPECTED_RULES:
        raise ValueError("export policy must contain exactly the approved ready and tmp rules")
    return policy


def load_policy(path):
    return validate_policy(load_json(path))


def _prefixes_overlap(left, right):
    return left.startswith(right) or right.startswith(left)


def _delete_rule_overlaps_exports(rule):
    condition = rule.get("condition")
    if not isinstance(condition, dict):
        raise ValueError("existing Delete rule condition must be an object")
    prefixes = condition.get("matchesPrefix")
    if prefixes is None:
        return True
    if not isinstance(prefixes, list) or not prefixes or not all(
        isinstance(prefix, str) and prefix for prefix in prefixes
    ):
        raise ValueError("existing Delete rule matchesPrefix must be a nonempty string array")
    return any(
        _prefixes_overlap(existing, export)
        for existing in prefixes
        for export in EXPORT_PREFIXES
    )


def merge_lifecycle(existing, policy):
    validate_policy(policy)
    if not isinstance(existing, dict):
        raise ValueError("existing lifecycle config must be an object")
    rules = existing.get("rule", [])
    if not isinstance(rules, list):
        raise ValueError("existing lifecycle rule must be an array")

    merged = dict(existing)
    merged_rules = list(rules)
    for rule in merged_rules:
        if not isinstance(rule, dict) or not isinstance(rule.get("action"), dict):
            raise ValueError("existing lifecycle rules must have an action object")
        if rule["action"].get("type") == "Delete" and _delete_rule_overlaps_exports(rule):
            matches = sum(rule == approved for approved in policy["rule"])
            if matches != 1:
                raise ValueError("existing Delete rule overlaps export prefixes")

    for approved in policy["rule"]:
        count = merged_rules.count(approved)
        if count > 1:
            raise ValueError("existing lifecycle config contains duplicate export rules")
        if count == 0:
            merged_rules.append(approved)
    merged["rule"] = merged_rules
    return merged


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--existing",
        required=True,
        help="path to the bucket's current lifecycle config JSON ({\"rule\": [...]})",
    )
    parser.add_argument("--policy", default=POLICY_PATH, type=Path)
    args = parser.parse_args()
    try:
        policy = load_policy(args.policy)
        existing = load_json(args.existing)
        merged = merge_lifecycle(existing, policy)
    except ValueError as error:
        parser.error(str(error))
    json.dump(merged, sys.stdout, indent=2, ensure_ascii=False)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
