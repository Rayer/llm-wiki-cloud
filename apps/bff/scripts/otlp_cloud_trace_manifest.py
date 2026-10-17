#!/usr/bin/env python3
"""Extract the safe, synthetic trace identity ledger from Cloud QA test output."""

import json
import re
import sys
from pathlib import Path

TRACE_ID = re.compile(r"^[0-9a-f]{32}$")
SPAN_ID = re.compile(r"^[0-9a-f]{16}$")
CASES = {f"UC{i}" for i in range(1, 7)}
CASE_FIELDS = {
    "case", "name", "trace_id", "parent_id", "parent_span_id", "expected_span",
    "outcome", "count", "expected_spans_per_trace", "worker_trace_id",
    "worker_parent_span_id", "profile_dispatch_trace_id", "profile_parent_span_id",
    "expected_http_status", "expected_spans", "http_status",
}
SPAN_FIELDS = {
    "case", "trace_id", "span_id", "parent_trace_id", "parent_span_id",
    "scope", "name", "links",
}


def fields(value):
    parsed = {}
    for item in value.split():
        key, separator, item_value = item.partition("=")
        if separator:
            parsed[key] = item_value
    return parsed


def require_trace_id(value, label):
    if not TRACE_ID.fullmatch(value):
        raise ValueError(f"invalid synthetic {label}")


def require_span_id(value, label):
    if value and not SPAN_ID.fullmatch(value):
        raise ValueError(f"invalid synthetic {label}")


def parse(log_dir):
    cases = []
    spans = []
    for path in sorted(Path(log_dir).glob("*.log")):
        for line in path.read_text(errors="replace").splitlines():
            if "OTLP_QA_CASE=" in line:
                value = line.split("OTLP_QA_CASE=", 1)[1]
                case, _, rest = value.partition(" ")
                if case not in CASES:
                    raise ValueError("unexpected QA case identity")
                item = fields(rest)
                item["case"] = case
                if set(item) - CASE_FIELDS:
                    raise ValueError("unexpected QA case field")
                for name in ("trace_id", "worker_trace_id", "profile_dispatch_trace_id"):
                    if item.get(name):
                        require_trace_id(item[name], name)
                for name in ("parent_id", "parent_span_id", "worker_parent_span_id", "profile_parent_span_id"):
                    require_span_id(item.get(name, ""), name)
                cases.append(item)
            if "OTLP_QA_SPAN " in line:
                item = fields(line.split("OTLP_QA_SPAN ", 1)[1])
                if set(item) != SPAN_FIELDS:
                    raise ValueError("QA span identity is incomplete or has unexpected fields")
                if item["case"] not in CASES:
                    raise ValueError("unexpected QA span case")
                require_trace_id(item["trace_id"], "trace_id")
                require_span_id(item["span_id"], "span_id")
                require_trace_id(item["parent_trace_id"], "parent_trace_id") if item["parent_trace_id"] else None
                require_span_id(item["parent_span_id"], "parent_span_id")
                if not re.fullmatch(r"[a-z0-9._/-]+", item["scope"]) or not re.fullmatch(r"[a-z0-9._-]+", item["name"]):
                    raise ValueError("span name or scope is not a fixed identifier")
                if item["links"]:
                    for link in item["links"].split(","):
                        trace_id, separator, span_id = link.partition("/")
                        if not separator:
                            raise ValueError("invalid causal link identity")
                        require_trace_id(trace_id, "link trace id")
                        require_span_id(span_id, "link span id")
                spans.append(item)
    seen_cases = {item["case"] for item in cases}
    seen_cases.update(item["case"] for item in spans)
    missing = CASES - seen_cases
    if missing:
        raise ValueError("missing QA cases: " + ",".join(sorted(missing)))
    span_counts = {case: sum(item["case"] == case for item in spans) for case in CASES}
    missing_spans = [case for case, count in span_counts.items() if count == 0]
    if missing_spans:
        raise ValueError("cases produced no span identities: " + ",".join(sorted(missing_spans)))
    uc2 = [item for item in spans if item["case"] == "UC2"]
    uc2_traces = {item["trace_id"] for item in uc2}
    if len(uc2_traces) != 24 or any(sum(item["trace_id"] == trace_id for item in uc2) != 3 for trace_id in uc2_traces):
        raise ValueError("UC2 did not produce 24 independent three-span traces")
    names_by_case = {case: {item["name"] for item in spans if item["case"] == case} for case in CASES}
    required_names = {
        "UC1": {"auth.mcp.metadata", "auth.mcp.register", "auth.google.identity"},
        "UC2": {"query.request", "query.http.client", "query.http.server"},
        "UC3": {"query.request"},
        "UC4": {"query.request"},
        "UC5": {"pipeline.worker", "profile.dispatch", "profile.work"},
        "UC6": {"export.request", "export.start", "export.worker"},
    }
    for case, required in required_names.items():
        if not required <= names_by_case[case]:
            raise ValueError(f"{case} is missing expected spans")
    by_case = {case: [item for item in spans if item["case"] == case] for case in CASES}
    for item in [row for row in cases if row["case"] == "UC1"]:
        match = next((span for span in by_case["UC1"] if span["trace_id"] == item.get("trace_id") and span["name"] == item.get("expected_span")), None)
        if match is None or match["parent_span_id"] != item.get("parent_id") or match["span_id"] == item.get("parent_id"):
            raise ValueError("UC1 span parent/outcome correlation does not match its synthetic request")
    for case in ("UC3", "UC4"):
        item = next((row for row in cases if row["case"] == case), None)
        if item is None:
            raise ValueError(f"{case} result record is missing")
        root = next((span for span in by_case[case] if span["trace_id"] == item.get("trace_id") and span["name"] == item.get("expected_span")), None)
        if root is None or root["parent_span_id"] != item.get("parent_span_id"):
            raise ValueError(f"{case} query root is not parented to its synthetic request")
    worker_case = next((row for row in cases if row["case"] == "UC5"), None)
    worker = next((span for span in by_case["UC5"] if span["name"] == "pipeline.worker"), None)
    claim = next((span for span in by_case["UC5"] if span["name"] == "profile.dispatch"), None)
    profile = next((span for span in by_case["UC5"] if span["name"] == "profile.work"), None)
    if worker_case is None or worker is None or claim is None or profile is None:
        raise ValueError("UC5 async span identities are incomplete")
    if worker["parent_span_id"] or f"{worker_case.get('worker_trace_id')}/{worker_case.get('worker_parent_span_id')}" not in worker["links"].split(","):
        raise ValueError("UC5 worker root does not link to the real submission span")
    if claim["trace_id"] != worker_case.get("profile_dispatch_trace_id") or claim["parent_span_id"] != worker_case.get("profile_parent_span_id"):
        raise ValueError("UC5 profile dispatch parent does not match its synthetic request")
    if profile["parent_span_id"] or f"{claim['trace_id']}/{claim['span_id']}" not in profile["links"].split(","):
        raise ValueError("UC5 Profile work root does not link to the actual claim span")
    export_case = next((row for row in cases if row["case"] == "UC6"), None)
    export_request = next((span for span in by_case["UC6"] if span["name"] == "export.request"), None)
    export_start = next((span for span in by_case["UC6"] if span["name"] == "export.start"), None)
    export_worker = next((span for span in by_case["UC6"] if span["name"] == "export.worker"), None)
    if export_case is None or export_request is None or export_start is None or export_worker is None:
        raise ValueError("UC6 export span identities are incomplete")
    if export_request["trace_id"] != export_case.get("trace_id") or export_request["parent_span_id"] != export_case.get("parent_span_id"):
        raise ValueError("UC6 export request parent does not match its synthetic request")
    if export_start["parent_span_id"] != export_request["span_id"] or export_start["trace_id"] != export_request["trace_id"]:
        raise ValueError("UC6 export start is not a child of the request")
    if export_worker["parent_span_id"] or f"{export_start['trace_id']}/{export_start['span_id']}" not in export_worker["links"].split(","):
        raise ValueError("UC6 worker root does not link to its accepted start span")
    grouped_uc2 = {}
    for item in by_case["UC2"]:
        grouped_uc2.setdefault(item["trace_id"], {})[item["name"]] = item
    for trace_id, group in grouped_uc2.items():
        root, client, server = (group.get(name) for name in ("query.request", "query.http.client", "query.http.server"))
        if root is None or client is None or server is None or not root["parent_span_id"] or \
                client["parent_span_id"] != root["span_id"] or server["parent_span_id"] != client["span_id"]:
            raise ValueError(f"UC2 transport span parents are invalid for {trace_id}")
    for line in cases:
        yield {"type": "case", **line, "span_count": span_counts[line["case"]]}
    for line in spans:
        yield {"type": "span", **line}


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: otlp_cloud_trace_manifest.py LOG_DIRECTORY")
    for record in parse(sys.argv[1]):
        print(json.dumps(record, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as exc:
        print(f"OTLP QA manifest error: {exc}", file=sys.stderr)
        raise SystemExit(1)
