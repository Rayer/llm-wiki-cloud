import copy
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from contextlib import redirect_stdout
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import trace_lookup  # noqa: E402
import verify_request_id_trace as probe  # noqa: E402


FIXTURE_DIR = ROOT / "scripts/fixtures/request_id_probe"
QUERY_FIXTURE = json.loads((FIXTURE_DIR / "sanitized.json").read_text(encoding="utf-8"))
RESPONSES = json.loads((FIXTURE_DIR / "responses.json").read_text(encoding="utf-8"))
REST_ID = RESPONSES["rest_404"]["headers"]["x-request-id"]
MCP_ENVELOPE_ID = RESPONSES["mcp_tool_error"]["headers"]["x-request-id"]
MCP_CALL_ID = RESPONSES["mcp_tool_error"]["body"]["result"]["_meta"]["request_id"]
QUERY_ID = RESPONSES["query_success"]["headers"]["x-request-id"]
QA_PROJECT = "lwc402-qa"
QUERY_KEY_CANARY = "local-test-key-canary"


def span(span_id, name, request_match, outcome, *, parent=None, parent_status="root", service="llm-wiki-bff", revision="bff-dev-r42", origin_match=False):
    return {
        "span_id": span_id,
        "parent_span_id": parent,
        "span": name,
        "operation": name,
        "service": service,
        "revision": revision,
        "parent_status": parent_status,
        "outcome": outcome,
        "outcome_status": "present" if outcome else "missing",
        "request_id_match": request_match,
        "origin_request_id_match": origin_match,
    }


def terminal(message, request_id, field, status, outcome, *, transport="http", is_error=None, service="llm-wiki-bff", revision="bff-dev-r42"):
    return {
        "message": message,
        "correlation_fields": [field],
        "correlation_id": request_id,
        "trace_id": None,
        "span_id": None,
        "status_code": status,
        "outcome": outcome,
        "transport": transport,
        "operation": "query_project" if transport == "mcp" else "http.request",
        "service": service,
        "revision": revision,
        "mcp_is_error": is_error,
    }


def lookup_result(trace_id, spans, terminal_logs, *, links=None, complete=True, status="found", issues=None):
    for row in terminal_logs:
        row["trace_id"] = trace_id
    return {
        "status": status,
        "complete": complete,
        "log_search": {"attempts": ["page_read", "entries_found" if terminal_logs else "no_data"]},
        "traces": [
            {
                "trace_id": trace_id,
                "status": "available",
                "console_url": f"https://console.cloud.google.com/traces/list?project=llm-wiki-cloud&tid={trace_id}",
                "origin_log": bool(terminal_logs),
                "correlation_evidence": ["request_id"],
                "spans": spans,
            }
        ] if spans else [],
        "terminal_logs": terminal_logs,
        "async_links": links or [],
        "issues": issues or [],
    }


def lookup_map():
    mcp_trace = "b" * 32
    query_trace = "c" * 32
    return {
        REST_ID: lookup_result(
            "a" * 32,
            [span("0000000000000001", "http.request", True, "rejected")],
            [terminal("http.request.terminal", REST_ID, "request_id", 404, "rejected")],
        ),
        MCP_CALL_ID: lookup_result(
            mcp_trace,
            [
                span("0000000000000002", "mcp.request", True, "error"),
                span("0000000000000003", "query.request", True, "error", parent="0000000000000002", parent_status="connected"),
            ],
            [terminal("query.terminal", MCP_CALL_ID, "diagnostic_id", 200, "error", transport="mcp", is_error=True)],
        ),
        MCP_ENVELOPE_ID: lookup_result(
            mcp_trace,
            [span("0000000000000004", "http.request", True, "success")],
            [terminal("http.request.terminal", MCP_ENVELOPE_ID, "request_id", 200, "success")],
        ),
        QUERY_ID: lookup_result(
            query_trace,
            [
                span("0000000000000005", "http.request", True, "success"),
                span("0000000000000006", "query.request", True, "success", parent="0000000000000005", parent_status="connected"),
            ],
            [
                terminal("http.request.terminal", QUERY_ID, "request_id", 200, "success"),
                terminal("query.terminal", QUERY_ID, "diagnostic_id", 200, "success", transport="http", is_error=False),
            ],
        ),
    }


class FakeApp:
    def __init__(self, *, mcp_is_error=True, query_body=None):
        self.calls = []
        self.mcp_is_error = mcp_is_error
        self.query_body = copy.deepcopy(query_body or RESPONSES["query_success"]["body"])

    def request(self, path, method, body=None, bearer_token=None, project_id=None):
        self.calls.append((path, method, copy.deepcopy(body), bearer_token, project_id))
        if path == "/api/v1/__lwc402_request_id_probe_not_found__":
            response = copy.deepcopy(RESPONSES["rest_404"])
        elif path == "/mcp":
            response = copy.deepcopy(RESPONSES["mcp_tool_error"])
            response["body"]["result"]["isError"] = self.mcp_is_error
        elif path == "/api/v1/query":
            response = copy.deepcopy(RESPONSES["query_success"])
            response["body"] = copy.deepcopy(self.query_body)
        else:
            raise AssertionError(f"unexpected product request: {path}")
        encoded = json.dumps(response["body"]).encode() if response["body"] is not None else None
        return probe.HTTPResult(response["status"], response["headers"], encoded)


def ready_prerequisites():
    return {
        "query_project_id": QA_PROJECT,
        "fixture_project_id": QA_PROJECT,
        "fixture_readiness": "verified",
        "fixture_marker": QUERY_FIXTURE["expected_source_marker"],
        "pipeline_project_id": None,
    }


def runner(*, app=None, lookup_fn=None, fixture=None, prerequisites=None, query_key=QUERY_KEY_CANARY, pipeline_project=None, pipeline_token=None, sleep_fn=lambda _seconds: None):
    config = probe.ProbeConfig(QA_PROJECT, query_key, pipeline_project, pipeline_token)
    return probe.ProbeRunner(
        config,
        app or FakeApp(),
        lookup_fn or lookup_map().__getitem__,
        copy.deepcopy(fixture or QUERY_FIXTURE),
        copy.deepcopy(prerequisites or ready_prerequisites()),
        sleep_fn=sleep_fn,
    )


class RequestIDTraceProbeContractTests(unittest.TestCase):
    def test_all_required_response_ids_match_terminal_logs_and_spans(self):
        app = FakeApp()
        probe_runner = runner(app=app)
        report = probe_runner.run()
        checks = {row["name"]: row for row in report["checks"]}

        self.assertEqual({name: checks[name]["status"] for name in ("rest_404", "mcp_tool_error", "query_fixture")}, {
            "rest_404": "pass", "mcp_tool_error": "pass", "query_fixture": "pass"
        })
        self.assertEqual(checks["rest_404"]["ids"]["request_id"], REST_ID)
        self.assertEqual(checks["mcp_tool_error"]["ids"]["call_request_id"], MCP_CALL_ID)
        self.assertEqual(checks["mcp_tool_error"]["ids"]["envelope_request_id"], MCP_ENVELOPE_ID)
        self.assertNotEqual(MCP_CALL_ID, MCP_ENVELOPE_ID)
        mcp_check = checks["mcp_tool_error"]
        self.assertEqual(mcp_check["evidence"]["call"]["mcp_owning_spans"][0]["outcome"], "error")
        self.assertEqual(mcp_check["evidence"]["envelope"]["root"]["outcome"], "success")
        self.assertEqual(mcp_check["evidence"]["envelope"]["matched_terminal_logs"][0]["outcome"], "success")
        self.assertEqual(checks["query_fixture"]["ids"]["request_id"], QUERY_ID)
        self.assertEqual(checks["pipeline"]["status"], "skip")
        self.assertEqual(checks["pipeline_native_link"]["status"], "manual")
        self.assertEqual(report["counts"], {"pass": 3, "fail": 0, "skip": 1, "manual": 1, "checks": 5})
        self.assertNotIn("/api/v1/pipeline/run", [call[0] for call in app.calls])
        rendered = json.dumps(report, ensure_ascii=False)
        self.assertNotIn(QUERY_KEY_CANARY, rendered)
        self.assertNotIn(QUERY_FIXTURE["query"], rendered)
        self.assertNotIn("20:00", rendered)

    def test_mcp_http_200_is_not_pass_without_native_tool_error(self):
        probe_runner = runner(app=FakeApp(mcp_is_error=False))
        report = probe_runner.run()
        check = next(row for row in report["checks"] if row["name"] == "mcp_tool_error")
        self.assertEqual(check["status"], "fail")
        self.assertEqual(check["code"], "mcp_tool_error_missing")

    def test_mcp_tool_error_keeps_http_envelope_lifecycle_outcome_consistent(self):
        for mutation in ("terminal_mismatch", "terminal_missing", "span_mismatch", "span_missing"):
            with self.subTest(mutation=mutation):
                results = lookup_map()
                envelope = results[MCP_ENVELOPE_ID]
                terminal_row = envelope["terminal_logs"][0]
                root_span = envelope["traces"][0]["spans"][0]
                if mutation == "terminal_mismatch":
                    terminal_row["outcome"] = "error"
                elif mutation == "terminal_missing":
                    terminal_row.pop("outcome")
                elif mutation == "span_mismatch":
                    root_span["outcome"] = "rejected"
                else:
                    root_span["outcome"] = None
                    root_span["outcome_status"] = "missing"

                probe_runner = runner(lookup_fn=lambda request_id, data=results: copy.deepcopy(data[request_id]))
                report = probe_runner.run()
                check = next(row for row in report["checks"] if row["name"] == "mcp_tool_error")
                self.assertEqual(check["status"], "fail")
                self.assertEqual(check["code"], "terminal_log_mismatch")
                self.assertEqual(
                    {span_row["outcome"] for span_row in check["evidence"]["call"]["mcp_owning_spans"]},
                    {"error"},
                )

    def test_mcp_call_id_requires_meta_and_matches_diagnostic_id(self):
        response = copy.deepcopy(RESPONSES["mcp_tool_error"])
        http = probe.HTTPResult(response["status"], response["headers"], json.dumps(response["body"]).encode())
        ids, _body, issues = runner()._extract_mcp_ids(http)
        self.assertEqual(ids["envelope_request_id"], MCP_ENVELOPE_ID)
        self.assertEqual(ids["call_request_id"], MCP_CALL_ID)
        self.assertNotIn("trace_correlation_mismatch", issues)

        del response["body"]["result"]["_meta"]
        http = probe.HTTPResult(response["status"], response["headers"], json.dumps(response["body"]).encode())
        ids, _body, issues = runner()._extract_mcp_ids(http)
        self.assertNotIn("call_request_id", ids)
        self.assertIn("request_id_missing", issues)

        response = copy.deepcopy(RESPONSES["mcp_tool_error"])
        response["body"]["result"]["structuredContent"]["diagnostic_id"] = QUERY_ID
        http = probe.HTTPResult(response["status"], response["headers"], json.dumps(response["body"]).encode())
        _ids, _body, issues = runner()._extract_mcp_ids(http)
        self.assertIn("trace_correlation_mismatch", issues)

    def test_query_requires_fixture_answer_and_matching_citation(self):
        probe_runner = runner()
        body = copy.deepcopy(RESPONSES["query_success"]["body"])
        http = probe.HTTPResult(200, RESPONSES["query_success"]["headers"], json.dumps(body).encode())
        self.assertIsNone(probe_runner._query_body_issue(http))

        wrong_answer = copy.deepcopy(body)
        wrong_answer["ai_synth"] = "It closes at 8 PM."
        http = probe.HTTPResult(200, {}, json.dumps(wrong_answer).encode())
        self.assertEqual(probe_runner._query_body_issue(http), "query_answer_mismatch")

        no_citations = copy.deepcopy(body)
        no_citations["citations"] = []
        http = probe.HTTPResult(200, {}, json.dumps(no_citations).encode())
        self.assertEqual(probe_runner._query_body_issue(http), "query_citation_mismatch")

        wrong_source = copy.deepcopy(body)
        wrong_source["citations"][0]["id"] = "01JAZ5N7Y3K8M2Q4R6T9VWXABD"
        http = probe.HTTPResult(200, {}, json.dumps(wrong_source).encode())
        self.assertEqual(probe_runner._query_body_issue(http), "query_citation_mismatch")

    def test_mismatched_terminal_and_missing_outcome_fail_required_check(self):
        results = lookup_map()
        bad_rest = copy.deepcopy(results[REST_ID])
        bad_rest["traces"][0]["spans"][0]["outcome"] = None
        bad_rest["traces"][0]["spans"][0]["outcome_status"] = "missing"
        bad_rest["terminal_logs"][0]["outcome"] = "success"
        results[REST_ID] = bad_rest
        app = FakeApp()
        probe_runner = runner(app=app, lookup_fn=results.__getitem__)
        probe_runner._run_rest_404()
        check = probe_runner.checks[0]
        self.assertEqual(check["status"], "fail")
        self.assertEqual(check["code"], "terminal_log_mismatch")

    def test_lookup_incomplete_and_permission_errors_remain_explicit_and_bounded(self):
        calls = []

        def incomplete(_request_id):
            calls.append(True)
            return {"status": "incomplete", "complete": False, "issues": [{"scope": "logs", "code": "page_limit"}]}

        probe_runner = runner(lookup_fn=incomplete)
        value, attempts = probe_runner._request_lookup(REST_ID, lambda result: bool(result.get("complete")))
        self.assertEqual(value["status"], "incomplete")
        self.assertEqual(len(calls), probe.LOOKUP_ATTEMPTS)
        self.assertEqual(attempts.count("incomplete"), probe.LOOKUP_ATTEMPTS)

        def forbidden(_request_id):
            raise trace_lookup.ProviderError("permission_denied")

        app = FakeApp()
        probe_runner = runner(app=app, lookup_fn=forbidden)
        probe_runner._run_rest_404()
        self.assertEqual(probe_runner.checks[0]["status"], "fail")
        self.assertEqual(probe_runner.checks[0]["code"], "permission_denied")
        self.assertEqual(probe_runner.lookup_attempt_history[0]["issue_codes"], ["permission_denied"])

    def test_missing_project_key_or_fixture_fails_preflight_without_requests(self):
        app = FakeApp()
        probe_runner = runner(app=app, query_key=None, prerequisites={"query_project_id": QA_PROJECT})
        blockers = probe_runner.preflight_blockers()
        self.assertIn("project_key_missing", blockers)
        self.assertIn("fixture_not_verified", blockers)
        report = probe_runner.fail_preflight(blockers)
        self.assertEqual(app.calls, [])
        self.assertEqual(report["live_execution"], "not_run")
        self.assertEqual(report["counts"]["fail"], 4)
        self.assertEqual(report["counts"]["skip"], 1)
        self.assertEqual(report["counts"]["manual"], 1)

    def test_cli_preflight_emits_not_run_report_without_cloud_or_product_calls(self):
        app = FakeApp()
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / "probe.json"
            stdout = io.StringIO()
            empty_values = {
                "LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID": "",
                "LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY": "",
                "LWC_DEV_PIPELINE_QA_PROJECT": "",
                "LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN": "",
            }
            with patch.dict(os.environ, empty_values, clear=False), patch.object(probe, "SafeHTTPClient", return_value=app), patch.object(
                probe, "verify_cloud_read_access", side_effect=AssertionError("preflight must stop before cloud access")
            ), patch.object(probe, "_source_sha", return_value="4e1d6cb3cc89548ca7dc49924d95c64ddc97c723"), redirect_stdout(stdout):
                exit_code = probe.main(["--artifact", str(artifact)])

            report = json.loads(stdout.getvalue())
            stored = json.loads(artifact.read_text(encoding="utf-8"))
        self.assertEqual(exit_code, 2)
        self.assertEqual(report["live_execution"], "not_run")
        self.assertEqual(report["status"], "fail")
        self.assertEqual(report, stored)
        self.assertEqual(app.calls, [])
        self.assertNotIn(QUERY_KEY_CANARY, stdout.getvalue())

    def test_same_project_query_key_cannot_authenticate_opt_in_pipeline(self):
        app = FakeApp()
        prerequisites = ready_prerequisites()
        prerequisites["pipeline_project_id"] = QA_PROJECT
        probe_runner = runner(app=app, prerequisites=prerequisites, pipeline_project=QA_PROJECT)
        blockers = probe_runner.preflight_blockers(enable_pipeline=True)
        self.assertIn("pipeline_identity_missing", blockers)
        self.assertIsNone(probe_runner._pipeline_token())
        report = probe_runner.fail_preflight(blockers, enable_pipeline=True)
        self.assertEqual(report["live_execution"], "not_run")
        pipeline_check = next(row for row in report["checks"] if row["name"] == "pipeline")
        self.assertEqual(pipeline_check["status"], "fail")
        self.assertEqual(pipeline_check["code"], "pipeline_identity_missing")
        self.assertEqual(app.calls, [])

    def test_cli_missing_pipeline_identity_stops_before_cloud_or_product_calls(self):
        app = FakeApp()
        prerequisites = ready_prerequisites()
        prerequisites["pipeline_project_id"] = QA_PROJECT
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prerequisite_path = root / "dev-prerequisites.json"
            prerequisite_path.write_text(json.dumps(prerequisites), encoding="utf-8")
            artifact = root / "probe.json"
            stdout = io.StringIO()
            env = {
                "LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID": QA_PROJECT,
                "LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY": QUERY_KEY_CANARY,
                "LWC_DEV_PIPELINE_QA_PROJECT": QA_PROJECT,
                "LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN": "",
            }
            with patch.dict(os.environ, env, clear=False), patch.object(probe, "DEV_PREREQUISITES_PATH", prerequisite_path), patch.object(
                probe, "SafeHTTPClient", return_value=app
            ), patch.object(probe, "live_lookup", return_value=lambda _request_id: None), patch.object(
                probe, "verify_cloud_read_access", side_effect=AssertionError("missing Pipeline identity must stop before cloud access")
            ), patch.object(probe, "_source_sha", return_value="4e1d6cb3cc89548ca7dc49924d95c64ddc97c723"), redirect_stdout(stdout):
                exit_code = probe.main(["--enable-pipeline", "--artifact", str(artifact)])

            report = json.loads(stdout.getvalue())
            stored = json.loads(artifact.read_text(encoding="utf-8"))

        self.assertEqual(exit_code, 2)
        self.assertEqual(report, stored)
        self.assertEqual(report["status"], "fail")
        self.assertEqual(report["live_execution"], "not_run")
        pipeline_check = next(row for row in report["checks"] if row["name"] == "pipeline")
        self.assertEqual(pipeline_check["code"], "pipeline_identity_missing")
        preflight = next(row for row in report["checks"] if row["name"] == "preflight")
        self.assertIn("pipeline_identity_missing", preflight["evidence"]["blockers"])
        self.assertEqual(app.calls, [])
        self.assertNotIn(QUERY_KEY_CANARY, stdout.getvalue())

    def test_trace_summary_rebuilds_safe_console_urls_and_sanitizes_labels(self):
        malformed = copy.deepcopy(lookup_map()[REST_ID])
        malformed["traces"][0]["console_url"] = "https://console.cloud.google.com/traces/list?object=private/path"
        malformed["traces"][0]["spans"][0].update({
            "span": "prompt answer canary",
            "operation": "../private/object",
            "service": "https://private.example/path",
            "revision": "../../private",
        })
        allowed_mcp_span = span("0000000000000007", "mcp.request", True, "error")
        allowed_mcp_span["operation"] = "tools/call"
        malformed["traces"][0]["spans"].append(allowed_mcp_span)
        malformed["terminal_logs"][0].update({
            "operation": "private/object/path",
            "service": "private service name",
            "revision": "https://private.example/revision",
        })
        summary = probe._trace_summary(malformed)
        encoded = json.dumps(summary, ensure_ascii=False)
        self.assertNotIn("private", encoded)
        self.assertNotIn("prompt answer canary", encoded)
        self.assertIn("https://console.cloud.google.com/traces/list?project=llm-wiki-cloud&tid=" + "a" * 32, encoded)
        self.assertIsNone(summary["traces"][0]["spans"][0]["service"])
        self.assertEqual(summary["traces"][0]["spans"][1]["operation"], "tools/call")

    def test_workflow_is_dispatch_only_development_and_uses_same_probe_contract(self):
        workflow = (ROOT / ".github/workflows/verify-request-id-trace-dev.yml").read_text(encoding="utf-8")
        self.assertIn("workflow_dispatch:", workflow)
        self.assertNotIn("pull_request:", workflow)
        self.assertNotIn("push:", workflow)
        self.assertIn('if [[ "$DISPATCH_REF" != "refs/heads/develop" ]]', workflow)
        self.assertIn("environment: Development", workflow)
        self.assertIn("id-token: write", workflow)
        self.assertIn("contents: read", workflow)
        self.assertIn("LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID", workflow)
        self.assertIn("LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY", workflow)
        self.assertIn("LWC_DEV_PIPELINE_QA_PROJECT", workflow)
        self.assertIn("LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN", workflow)
        self.assertIn("python3 scripts/verify_request_id_trace.py --artifact", workflow)
        self.assertNotIn("LWC_DEV_REQUEST_ID_PROBE_QUERY_PROJECT_ID", workflow)
        self.assertNotIn("LWC_DEV_PIPELINE_BEARER_TOKEN", workflow)
        ci = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("test_request_id_trace.py", ci)

    def test_cloud_permission_preflight_is_read_only_and_fail_closed(self):
        fake_client = Mock()
        with patch.object(probe.trace_lookup, "GoogleApiClient", return_value=fake_client), patch.object(
            probe.trace_lookup,
            "lookup_request",
            return_value={"status": "not_found", "complete": True, "issues": []},
        ):
            fake_client.get_trace.side_effect = trace_lookup.ProviderError("not_found")
            self.assertIsNone(probe.verify_cloud_read_access())
            fake_client.get_trace.side_effect = trace_lookup.ProviderError("permission_denied")
            self.assertEqual(probe.verify_cloud_read_access(), "permission_denied")

    def test_pipeline_is_opt_in_and_missing_allowlist_never_submits(self):
        app = FakeApp()
        probe_runner = runner(app=app, pipeline_project=QA_PROJECT, pipeline_token="pipeline-token-canary")
        probe_runner._run_pipeline(False)
        self.assertEqual(probe_runner.checks[0]["status"], "skip")
        self.assertEqual(probe_runner.checks[1]["status"], "manual")
        probe_runner = runner(app=app, pipeline_project=QA_PROJECT, pipeline_token="pipeline-token-canary")
        probe_runner._run_pipeline(True)
        self.assertEqual(probe_runner.checks[0]["status"], "fail")
        self.assertEqual(probe_runner.checks[0]["code"], "pipeline_project_allowlist_missing")
        self.assertEqual(probe_runner.checks[1]["status"], "manual")
        self.assertEqual(app.calls, [])

    def test_pipeline_running_state_has_a_bounded_poll_and_is_not_a_pass(self):
        class RunningPipelineApp:
            def __init__(self):
                self.status_reads = 0
                self.calls = []

            def request(self, path, method, body=None, bearer_token=None, project_id=None):
                self.calls.append((path, method, bearer_token, project_id))
                if path == "/api/v1/pipeline/run":
                    return probe.HTTPResult(202, {"x-request-id": REST_ID}, json.dumps({"execution_id": "lwc402-execution"}).encode())
                self.status_reads += 1
                return probe.HTTPResult(200, {}, b'{"last_execution":{"status":"RUNNING"}}')

        qa_project = {"query_project_id": QA_PROJECT, "fixture_project_id": QA_PROJECT, "fixture_readiness": "verified", "fixture_marker": QUERY_FIXTURE["expected_source_marker"], "pipeline_project_id": QA_PROJECT}
        app = RunningPipelineApp()
        probe_runner = runner(
            app=app,
            lookup_fn=lambda _request_id: {"status": "not_found", "complete": True, "traces": [], "terminal_logs": [], "issues": []},
            prerequisites=qa_project,
            pipeline_project=QA_PROJECT,
            pipeline_token="pipeline-token-canary",
        )
        probe_runner._run_pipeline(True)
        self.assertEqual(app.status_reads, probe.PIPELINE_POLL_ATTEMPTS)
        self.assertEqual(app.calls[0], ("/api/v1/pipeline/run", "POST", "pipeline-token-canary", QA_PROJECT))
        self.assertTrue(all(call[2] == "pipeline-token-canary" for call in app.calls))
        self.assertTrue(all(call[1] == "GET" for call in app.calls[1:]))
        self.assertEqual(probe_runner.checks[0]["status"], "fail")
        self.assertEqual(probe_runner.checks[0]["code"], "pipeline_not_terminal")
        self.assertEqual(probe_runner.checks[1]["status"], "manual")

    def test_pipeline_success_requires_a_distinct_correlated_worker_trace(self):
        origin_trace = "d" * 32
        worker_trace = "e" * 32
        pipeline_id = "4a123e45-6789-4abc-8def-0123456789ab"
        pipeline_lookup = lookup_result(
            origin_trace,
            [
                span("0000000000000010", "http.request", True, "success"),
                span("0000000000000011", "pipeline.submit", True, "accepted", parent="0000000000000010", parent_status="connected"),
            ],
            [terminal("http.request.terminal", pipeline_id, "request_id", 202, "success")],
            links=[{
                "origin_trace_id": origin_trace,
                "related_trace_id": worker_trace,
                "attempt_ids": ["attempt_42"],
                "relationship": "separate_trace_with_stdouttrace_link",
                "evidence": "stdouttrace_span_link",
                "console_url": f"https://console.cloud.google.com/traces/list?project=llm-wiki-cloud&tid={worker_trace}",
                "native_link_verification": "manual_console_check_required",
            }],
        )
        pipeline_lookup["traces"].append({
            "trace_id": worker_trace,
            "status": "available",
            "console_url": f"https://console.cloud.google.com/traces/list?project=llm-wiki-cloud&tid={worker_trace}",
            "correlation_evidence": ["span.origin_request.id"],
            "spans": [span("0000000000000012", "pipeline.worker", False, "success", service="lwc-worker", revision="worker-dev-r7", origin_match=True)],
        })

        class SuccessfulPipelineApp:
            def request(self, path, method, body=None, bearer_token=None, project_id=None):
                if path == "/api/v1/pipeline/run":
                    return probe.HTTPResult(202, {"x-request-id": pipeline_id}, b'{"execution_id":"lwc402-execution"}')
                return probe.HTTPResult(200, {"x-request-id": QUERY_ID}, b'{"last_execution":{"status":"SUCCEEDED"}}')

        qa_project = {"query_project_id": QA_PROJECT, "fixture_project_id": QA_PROJECT, "fixture_readiness": "verified", "fixture_marker": QUERY_FIXTURE["expected_source_marker"], "pipeline_project_id": QA_PROJECT}
        probe_runner = runner(
            app=SuccessfulPipelineApp(),
            lookup_fn=lambda request_id: copy.deepcopy(pipeline_lookup) if request_id == pipeline_id else {"status": "not_found", "complete": True, "traces": [], "terminal_logs": [], "issues": []},
            prerequisites=qa_project,
            pipeline_project=QA_PROJECT,
            pipeline_token="pipeline-token-canary",
        )
        probe_runner._run_pipeline(True)
        pipeline_check, native_link_check = probe_runner.checks
        self.assertEqual(pipeline_check["status"], "pass")
        self.assertEqual({item["trace_id"] for item in pipeline_check["evidence"]["pipeline_spans"]}, {origin_trace, worker_trace})
        self.assertEqual(native_link_check["status"], "manual")

        without_link = copy.deepcopy(pipeline_lookup)
        without_link["async_links"] = []
        probe_runner = runner(
            app=SuccessfulPipelineApp(),
            lookup_fn=lambda _request_id: copy.deepcopy(without_link),
            prerequisites=qa_project,
            pipeline_project=QA_PROJECT,
            pipeline_token="pipeline-token-canary",
        )
        probe_runner._run_pipeline(True)
        self.assertEqual(probe_runner.checks[0]["status"], "fail")
        self.assertEqual(probe_runner.checks[0]["code"], "trace_correlation_mismatch")


if __name__ == "__main__":
    unittest.main()
