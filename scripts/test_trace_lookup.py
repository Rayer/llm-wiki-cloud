import contextlib
import copy
import importlib.util
import io
import json
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
FIXTURE = json.loads((ROOT / "scripts/fixtures/trace_lookup/sanitized.json").read_text(encoding="utf-8"))
SPEC = importlib.util.spec_from_file_location("trace_lookup", ROOT / "scripts/trace_lookup.py")
lookup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(lookup)


class FakeProvider:
    def __init__(self, logs=None, traces=None, log_pages=None, trace_errors=None):
        self.logs = copy.deepcopy(FIXTURE["logs"] if logs is None else logs)
        self.traces = copy.deepcopy(FIXTURE["traces"] if traces is None else traces)
        self.log_pages = list(log_pages or [])
        self.trace_errors = dict(trace_errors or {})
        self.log_calls = []
        self.trace_calls = []

    def list_log_entries(self, filter_text, page_token, page_size):
        self.log_calls.append((filter_text, page_token, page_size))
        if self.log_pages:
            response = self.log_pages.pop(0)
            if isinstance(response, Exception):
                raise response
            return copy.deepcopy(response)
        if page_token:
            return {"entries": []}
        return {"entries": copy.deepcopy(self.logs)}

    def get_trace(self, trace_id):
        self.trace_calls.append(trace_id)
        error = self.trace_errors.get(trace_id)
        if error:
            raise error
        return copy.deepcopy(self.traces[trace_id])


class RequestIdTraceLookupTests(unittest.TestCase):
    request_id = FIXTURE["request_id"]
    trace_one = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    trace_two = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    since = "2026-10-18T00:00:00Z"
    until = "2026-10-18T00:05:00Z"

    def test_filter_separates_log_request_id_span_attribute_and_text_logger(self):
        query = lookup.request_log_filter(self.request_id, self.since, self.until)
        self.assertIn('jsonPayload.request_id="' + self.request_id + '"', query)
        self.assertIn('jsonPayload."request.id"="' + self.request_id + '"', query)
        self.assertIn('jsonPayload.origin_request_id=', query)
        self.assertIn('jsonPayload."request.origin_id"=', query)
        self.assertIn('jsonPayload.envelope_request_id=', query)
        self.assertIn('jsonPayload.diagnostic_id=', query)
        self.assertIn('jsonPayload.Attributes.Value.Value="' + self.request_id + '"', query)
        self.assertIn('textPayload:"' + self.request_id + '"', query)
        self.assertIn('timestamp >= "' + self.since + '"', query)

    def test_collects_multiple_traces_and_deduplicates_repeated_json_logs(self):
        result = lookup.collect_trace_ids(FIXTURE["logs"], self.request_id)
        self.assertEqual(set(result["traces"]), {self.trace_one, self.trace_two})
        self.assertEqual(result["matched_log_count"], 4)
        self.assertEqual(result["duplicate_log_count"], 2)
        self.assertEqual(result["origin_trace_ids"], {self.trace_one})
        self.assertEqual(result["traces"][self.trace_one]["evidence"], {"request_id", "envelope_request_id"})
        self.assertEqual(result["traces"][self.trace_two]["attempt_ids"], {"attempt_7"})

    def test_pinned_stdouttrace_v144_structured_and_text_shapes_discover_linked_trace(self):
        structured, text = FIXTURE["logs"][3], FIXTURE["logs"][5]
        result = lookup.collect_trace_ids([structured, text], self.request_id)
        self.assertEqual(set(result["traces"]), {self.trace_two})
        self.assertEqual(result["matched_log_count"], 2)
        self.assertEqual(result["traces"][self.trace_two]["evidence"], {"request.id"})
        self.assertEqual(result["traces"][self.trace_two]["attempt_ids"], {"attempt_7"})
        self.assertEqual(result["traces"][self.trace_two]["services"], {"lwc-bff-dev"})
        self.assertEqual(result["traces"][self.trace_two]["revisions"], {"rev-a"})
        self.assertEqual(result["traces"][self.trace_two]["linked_trace_ids"], {self.trace_one})

    def test_sdk_nested_origin_attribute_is_distinct_from_request_id(self):
        log = copy.deepcopy(FIXTURE["logs"][3])
        for attribute in log["jsonPayload"]["Attributes"]:
            if attribute["Key"] == "request.id":
                attribute["Key"] = "request.origin_id"
        result = lookup.collect_trace_ids([log], self.request_id)
        self.assertEqual(result["traces"][self.trace_two]["evidence"], {"request.origin_id"})

    def test_pinned_stdouttrace_shapes_reject_missing_or_nonmatching_request_attribute(self):
        structured = copy.deepcopy(FIXTURE["logs"][3])
        for attribute in structured["jsonPayload"]["Attributes"]:
            if attribute["Key"] == "request.id":
                attribute["Value"]["Value"] = "22222222-2222-4222-8222-222222222222"
        text = copy.deepcopy(FIXTURE["logs"][5])
        text_payload = json.loads(text["textPayload"])
        text_payload["Attributes"] = [item for item in text_payload["Attributes"] if item["Key"] != "request.id"]
        text["textPayload"] = json.dumps(text_payload)
        result = lookup.collect_trace_ids([structured, text], self.request_id)
        self.assertEqual(result["traces"], {})
        self.assertEqual(result["matched_log_count"], 0)

    def test_json_and_text_loggers_require_a_matching_id_field(self):
        json_entry = {"jsonPayload": {"request.id": self.request_id, "trace_id": self.trace_one}}
        text_entry = {"textPayload": f"msg=request.finished request_id={self.request_id} trace_id={self.trace_two}"}
        unrelated = {"textPayload": f"message mentions {self.request_id} without a request_id field"}
        result = lookup.collect_trace_ids([json_entry, text_entry, unrelated], self.request_id)
        self.assertEqual(set(result["traces"]), {self.trace_one, self.trace_two})
        self.assertEqual(result["matched_log_count"], 2)

        origin_text = {"textPayload": f"request.origin_id={self.request_id} trace_id={self.trace_two}"}
        origin_result = lookup.collect_trace_ids([origin_text], self.request_id)
        self.assertEqual(origin_result["traces"][self.trace_two]["evidence"], {"request.origin_id"})

    def test_lookup_builds_per_trace_tree_and_reports_separate_trace_origin_link(self):
        provider = FakeProvider()
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            sleep_fn=lambda _seconds: None,
        )
        self.assertEqual(result["status"], "found")
        self.assertTrue(result["complete"])
        self.assertEqual(result["trace_count"], 2)
        self.assertEqual(result["log_search"]["matched_entries"], 4)
        self.assertEqual(result["log_search"]["duplicate_entries"], 2)
        root, child = result["traces"][0]["spans"]
        self.assertEqual(root["parent_status"], "root")
        self.assertEqual(child["parent_status"], "connected")
        self.assertEqual(child["parent_span_id"], root["span_id"])
        self.assertEqual(root["service"], "lwc-bff-dev")
        self.assertEqual(child["operation"], "compile")
        self.assertEqual(root["outcome"], "success")
        self.assertEqual(result["async_links"][0]["origin_trace_id"], self.trace_one)
        self.assertEqual(result["async_links"][0]["related_trace_id"], self.trace_two)
        self.assertEqual(result["async_links"][0]["relationship"], "separate_trace_with_stdouttrace_link")
        self.assertEqual(result["async_links"][0]["evidence"], "stdouttrace_span_link")
        self.assertEqual(result["async_links"][0]["native_link_verification"], "manual_console_check_required")

    def test_origin_request_attribute_is_distinct_from_request_id_attribute(self):
        tree = lookup.build_span_tree(
            [
                {
                    "spanId": "3",
                    "name": "pipeline.worker",
                    "labels": {"origin_request_id": self.request_id, "pipeline.outcome": "pending"},
                }
            ],
            self.request_id,
        )
        self.assertEqual(tree["request_id_span_count"], 0)
        self.assertEqual(tree["origin_request_id_span_count"], 1)
        self.assertFalse(tree["spans"][0]["request_id_match"])
        self.assertTrue(tree["spans"][0]["origin_request_id_match"])

        logs = [
            copy.deepcopy(FIXTURE["logs"][0]),
            {
                "insertId": "fixture-origin-request-id",
                "trace": f"projects/synthetic/traces/{self.trace_two}",
                "jsonPayload": {
                    "origin_request.id": self.request_id,
                    "attempt_id": "attempt_8",
                },
            },
        ]
        traces = {
            self.trace_one: copy.deepcopy(FIXTURE["traces"][self.trace_one]),
            self.trace_two: {
                "traceId": self.trace_two,
                "spans": [
                    {
                        "spanId": "3",
                        "name": "pipeline.worker",
                        "labels": {"request.origin_id": self.request_id, "pipeline.outcome": "pending"},
                    }
                ],
            },
        }
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until,
            FakeProvider(logs=logs, traces=traces), retries=1, sleep_fn=lambda _seconds: None,
        )
        related = next(trace for trace in result["traces"] if trace["trace_id"] == self.trace_two)
        self.assertIn("span.origin_request.id", related["correlation_evidence"])
        self.assertEqual(result["async_links"][0]["related_trace_id"], self.trace_two)

    def test_accepted_request_without_observed_background_trace_is_incomplete(self):
        ingress = copy.deepcopy(FIXTURE["logs"][0])
        ingress["jsonPayload"]["status_code"] = 202
        result = lookup.lookup_request(
            self.request_id,
            "llm-wiki-cloud",
            self.since,
            self.until,
            FakeProvider(logs=[ingress], traces={self.trace_one: copy.deepcopy(FIXTURE["traces"][self.trace_one])}),
            retries=1,
            sleep_fn=lambda _seconds: None,
        )
        self.assertEqual(result["async_background"], {"expected": True, "status": "missing", "related_trace_count": 0})
        self.assertIn({"scope": "async", "code": "background_trace_evidence_missing"}, result["issues"])
        self.assertFalse(result["complete"])

    def test_output_omits_raw_log_payload(self):
        provider = FakeProvider()
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            sleep_fn=lambda _seconds: None,
        )
        encoded = json.dumps(result)
        self.assertNotIn("prompt", encoded)
        self.assertNotIn("synthetic payload must never be emitted", encoded)

    def test_cli_json_caps_and_privacy_with_local_provider(self):
        stdout = io.StringIO()
        with patch.object(lookup, "GoogleApiClient", return_value=FakeProvider()):
            with contextlib.redirect_stdout(stdout):
                result = lookup.main([self.request_id, "--json", "--max-traces", "1", "--max-spans", "1", "--attempts", "1"])
        report = json.loads(stdout.getvalue())
        self.assertEqual(result, 3)
        self.assertFalse(report["complete"])
        self.assertTrue(report["traces_truncated"])
        self.assertEqual(report["traces"][0]["status"], "truncated")
        self.assertNotIn("synthetic payload must never be emitted", stdout.getvalue())
        self.assertNotIn("prompt", stdout.getvalue())

    def test_diagnostic_id_terminal_log_is_separate_and_safely_summarized(self):
        call_id = "123e4567-e89b-12d3-a456-426614174000"
        log = {
            "insertId": "fixture-mcp-terminal",
            "trace": f"projects/synthetic/traces/{self.trace_one}",
            "resource": {"labels": {"service_name": "lwc-bff-dev", "revision_name": "bff-r42"}},
            "jsonPayload": {
                "msg": "query.terminal",
                "diagnostic_id": call_id,
                "trace_id": self.trace_one,
                "span_id": "0000000000000003",
                "transport": "mcp",
                "request_status": 200,
                "outcome": "error",
                "mcp_is_error": True,
                "service": "lwc-bff-dev",
                "revision": "bff-r42",
                "prompt": "PRIVATE_PROMPT_CANARY",
            },
        }
        result = lookup.collect_trace_ids([log], call_id)
        self.assertEqual(result["traces"][self.trace_one]["evidence"], {"diagnostic_id"})
        self.assertEqual(
            result["terminal_logs"],
            [
                {
                    "message": "query.terminal",
                    "correlation_fields": ["diagnostic_id"],
                    "correlation_id": call_id,
                    "trace_id": self.trace_one,
                    "span_id": "0000000000000003",
                    "outcome": "error",
                    "transport": "mcp",
                    "service": "lwc-bff-dev",
                    "revision": "bff-r42",
                    "status_code": 200,
                    "mcp_is_error": True,
                }
            ],
        )
        encoded = json.dumps(result["terminal_logs"])
        self.assertNotIn("PRIVATE_PROMPT_CANARY", encoded)

    def test_early_request_id_rejection_does_not_acquire_credentials_or_call_provider(self):
        provider = FakeProvider()
        with self.assertRaisesRegex(ValueError, "request_id_invalid"):
            lookup.lookup_request("not-a-request-id", "llm-wiki-cloud", self.since, self.until, provider)
        self.assertEqual(provider.log_calls, [])
        self.assertEqual(provider.trace_calls, [])
        stdout = io.StringIO()
        with patch.object(lookup.subprocess, "run", side_effect=AssertionError("credential command must not run")):
            with contextlib.redirect_stdout(stdout):
                result = lookup.main(["not-a-request-id", "--json"])
        self.assertEqual(result, 2)
        self.assertEqual(json.loads(stdout.getvalue())["issues"][0]["code"], "request_id_invalid")

    def test_missing_request_attribute_is_explicit(self):
        logs = [copy.deepcopy(FIXTURE["logs"][0])]
        traces = {self.trace_one: {"traceId": self.trace_one, "spans": [{"spanId": "1", "name": "http.request", "labels": {"request.outcome": "success"}}]}}
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until,
            FakeProvider(logs=logs, traces=traces), retries=1, sleep_fn=lambda _seconds: None,
        )
        self.assertIn(
            {"scope": "trace", "trace_id": self.trace_one, "code": "request_id_attribute_missing"},
            result["issues"],
        )
        self.assertFalse(result["complete"])

    def test_missing_span_outcome_is_explicit_and_marks_result_incomplete(self):
        trace = {
            "traceId": self.trace_one,
            "spans": [
                {
                    "spanId": "1",
                    "name": "http.request",
                    "labels": {"request.id": self.request_id},
                }
            ],
        }
        result = lookup.lookup_request(
            self.request_id,
            "llm-wiki-cloud",
            self.since,
            self.until,
            FakeProvider(logs=[copy.deepcopy(FIXTURE["logs"][0])], traces={self.trace_one: trace}),
            retries=1,
            sleep_fn=lambda _seconds: None,
        )
        self.assertIn(
            {"scope": "trace", "trace_id": self.trace_one, "code": "span_outcome_missing"},
            result["issues"],
        )
        self.assertFalse(result["complete"])

    def test_permission_denied_is_classified_without_provider_error_text(self):
        provider = FakeProvider(log_pages=[lookup.ProviderError("permission_denied")])
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            retries=2, sleep_fn=lambda _seconds: None,
        )
        self.assertEqual(result["status"], "incomplete")
        self.assertIn({"scope": "logs", "code": "permission_denied"}, result["issues"])
        self.assertNotIn("secret", json.dumps(result).lower())

    def test_trace_api_rate_limit_is_retried_and_remains_incomplete(self):
        provider = FakeProvider(
            logs=[copy.deepcopy(FIXTURE["logs"][0])],
            trace_errors={self.trace_one: lookup.ProviderError("rate_limited")},
        )
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            retries=2, retry_delay=0, sleep_fn=lambda _seconds: None,
        )
        trace = result["traces"][0]
        self.assertEqual(trace["status"], "rate_limited")
        self.assertEqual(trace["attempts"], ["rate_limited", "rate_limited"])
        self.assertEqual(len(provider.trace_calls), 2)
        self.assertFalse(result["complete"])

    def test_empty_lookup_attempts_are_preserved_before_delayed_log_export(self):
        provider = FakeProvider(
            log_pages=[{"entries": []}, {"entries": []}, {"entries": [copy.deepcopy(FIXTURE["logs"][0])]}],
            traces={self.trace_one: copy.deepcopy(FIXTURE["traces"][self.trace_one])},
        )
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            retries=3, retry_delay=0, sleep_fn=lambda _seconds: None,
        )
        self.assertEqual(result["log_search"]["attempts"], ["no_data", "no_data", "entries_found"])
        self.assertEqual(len(provider.log_calls), 3)
        self.assertEqual(result["status"], "found")

    def test_pagination_entry_bound_is_explicitly_truncated(self):
        provider = FakeProvider(
            log_pages=[
                {"entries": [copy.deepcopy(FIXTURE["logs"][0])], "nextPageToken": "next-page"},
                {"entries": [copy.deepcopy(FIXTURE["logs"][2])]},
            ],
            traces={self.trace_one: copy.deepcopy(FIXTURE["traces"][self.trace_one])},
        )
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            max_log_entries=1, retries=1, sleep_fn=lambda _seconds: None,
        )
        self.assertFalse(result["log_search"]["enumeration_complete"])
        self.assertTrue(result["log_search"]["truncated"])
        self.assertEqual(result["issues"][0]["code"], "entry_limit")
        self.assertEqual(result["status"], "incomplete")

    def test_page_limit_is_explicit(self):
        provider = FakeProvider(
            log_pages=[
                {"entries": [copy.deepcopy(FIXTURE["logs"][0])], "nextPageToken": "next-page"},
            ],
            traces={self.trace_one: copy.deepcopy(FIXTURE["traces"][self.trace_one])},
        )
        result = lookup.lookup_request(
            self.request_id, "llm-wiki-cloud", self.since, self.until, provider,
            max_pages=1, retries=1, sleep_fn=lambda _seconds: None,
        )
        self.assertFalse(result["log_search"]["enumeration_complete"])
        self.assertEqual(result["issues"][0]["code"], "page_limit")

    def test_cycles_missing_parent_and_missing_outcome_have_explicit_states(self):
        tree = lookup.build_span_tree(
            [
                {"spanId": "1", "parentSpanId": "2", "name": "pipeline.one", "labels": {"request.id": self.request_id}},
                {"spanId": "2", "parentSpanId": "1", "name": "pipeline.two", "labels": {"request.id": self.request_id}},
                {"spanId": "3", "parentSpanId": "99", "name": "pipeline.three", "labels": {"request.id": self.request_id, "request.outcome": "not-finite"}},
            ],
            self.request_id,
        )
        statuses = {span["span_id"]: span["parent_status"] for span in tree["spans"]}
        self.assertEqual(statuses["1"], "cycle")
        self.assertEqual(statuses["2"], "cycle")
        self.assertEqual(statuses["3"], "missing_parent")
        self.assertIsNone(tree["spans"][2]["outcome"])
        self.assertEqual(tree["spans"][2]["outcome_status"], "unrecognized")
        self.assertEqual(tree["parent_issues"], 3)
        self.assertEqual(len(tree["tree"]), 3)

    def test_mcp_operation_allowlist_preserves_slashes_without_allowing_paths(self):
        tree = lookup.build_span_tree(
            [
                {"spanId": "1", "name": "mcp.request", "labels": {"mcp.operation": "tools/call", "request.id": self.request_id, "request.outcome": "success"}},
                {"spanId": "2", "name": "mcp.request", "labels": {"mcp.operation": "https://example.test/private", "request.id": self.request_id, "request.outcome": "success"}},
            ],
            self.request_id,
        )
        self.assertEqual(tree["spans"][0]["operation"], "tools/call")
        self.assertIsNone(tree["spans"][1]["operation"])

    def test_deep_legal_trace_is_bounded_for_tree_json_and_text_output(self):
        spans = []
        for index in range(600):
            span = {
                "spanId": f"{index + 1:016x}",
                "name": "pipeline.worker.deep",
                "labels": {"request.id": self.request_id, "pipeline.outcome": "success"},
            }
            if index:
                span["parentSpanId"] = f"{index:016x}"
            spans.append(span)
        result = lookup.lookup_request(
            self.request_id,
            "llm-wiki-cloud",
            self.since,
            self.until,
            FakeProvider(logs=[copy.deepcopy(FIXTURE["logs"][0])], traces={self.trace_one: {"traceId": self.trace_one, "spans": spans}}),
            retries=1,
            sleep_fn=lambda _seconds: None,
        )
        self.assertFalse(result["complete"])
        trace = result["traces"][0]
        self.assertTrue(trace["tree_depth_truncated"])
        self.assertEqual(trace["tree_depth_limit"], lookup.MAX_TREE_DEPTH)
        self.assertEqual(trace["omitted_tree_edges"], 1)
        self.assertEqual(trace["span_count"], 600)
        self.assertIn({"scope": "trace", "trace_id": self.trace_one, "code": "span_tree_depth_limit"}, result["issues"])
        json_stdout = io.StringIO()
        with contextlib.redirect_stdout(json_stdout):
            lookup._emit_report(result, json_stdout=True, output=None)
        self.assertEqual(json.loads(json_stdout.getvalue())["traces"][0]["span_count"], 600)
        text_stdout = io.StringIO()
        with contextlib.redirect_stdout(text_stdout):
            lookup._emit_report(result, json_stdout=False, output=None)
        self.assertIn("children=truncated_at_depth_limit", text_stdout.getvalue())
        self.assertIn("complete=false", text_stdout.getvalue())

    def test_gcloud_http_error_categories_are_finite(self):
        self.assertEqual(lookup.classify_http_status(401), "authentication_required")
        self.assertEqual(lookup.classify_http_status(403), "permission_denied")
        self.assertEqual(lookup.classify_http_status(404), "not_found")
        self.assertEqual(lookup.classify_http_status(429), "rate_limited")
        self.assertEqual(lookup.classify_http_status(504), "timeout")
        self.assertEqual(lookup.classify_http_status(503), "service_unavailable")


if __name__ == "__main__":
    unittest.main()
