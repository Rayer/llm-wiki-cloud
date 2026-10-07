#!/usr/bin/env python3
"""Read the small generated BFF runtime projection without exposing other config."""

import argparse
import json
from pathlib import Path

MAX_COOLDOWN_SECONDS = ((1 << 63) - 1) // 1_000_000_000
PROJECTION_KEYS = {"schema_version", "environment", "pipeline_cooldown_seconds"}


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate projection field")
        result[key] = value
    return result


def load_projection(path, expected_environment):
    projection = json.loads(Path(path).read_text(encoding="utf-8"), object_pairs_hook=_unique_object)
    if (not isinstance(projection, dict) or set(projection) != PROJECTION_KEYS or
            type(projection["schema_version"]) is not int or projection["schema_version"] != 1 or
            projection["environment"] != expected_environment or
            type(projection["pipeline_cooldown_seconds"]) is not int or
            projection["pipeline_cooldown_seconds"] <= 0 or
            projection["pipeline_cooldown_seconds"] > MAX_COOLDOWN_SECONDS):
        raise ValueError("BFF cooldown projection is invalid")
    return projection["pipeline_cooldown_seconds"]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("path")
    parser.add_argument("environment", choices=("local", "dev", "prod"))
    args = parser.parse_args()
    print(load_projection(args.path, args.environment))


if __name__ == "__main__":
    main()
