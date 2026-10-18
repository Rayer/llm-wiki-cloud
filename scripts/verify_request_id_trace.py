#!/usr/bin/env python3
"""DEV-only request-ID probe using the shared Cloud Logging/Trace lookup core.

The default run makes three bounded product requests: an unmatched REST GET, a
native MCP tool error, and a Query against the explicitly configured synthetic
fixture. Pipeline is a separate opt-in. No response payload or credential is
written to stdout or the optional JSON artifact.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

import trace_lookup


DEV_BFF_URL = "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app"
DEV_GCP_PROJECT = trace_lookup.DEFAULT_DEV_PROJECT
FIXTURE_PATH = Path(__file__).with_name("fixtures") / "request_id_probe" / "sanitized.json"
DEV_PREREQUISITES_PATH = Path(__file__).with_name("fixtures") / "request_id_probe" / "dev-prerequisites.json"
HTTP_TIMEOUT = 5.0
MAX_RESPONSE_BYTES = 1 << 20
LOOKUP_ATTEMPTS = 6
LOOKUP_DELAY_SECONDS = 5.0
LOOKUP_ATTEMPT_SECONDS = 8.0
PIPELINE_POLL_ATTEMPTS = 18
PIPELINE_POLL_DELAY_SECONDS = 5.0
TERMINAL_PIPELINE_STATES = frozenset({"SUCCEEDED", "FAILED", "CANCELLED", "CANCELED", "TIMED_OUT", "ABORTED"})
SAFE_PIPELINE_STATES = TERMINAL_PIPELINE_STATES | frozenset({"PENDING", "RUNNING", "UNKNOWN"})
SAFE_STATUS_CODES = frozenset({401, 403, 404, 408, 429, 500, 502, 503, 504})
SAFE_ERROR_CODES = trace_lookup.SAFE_ERROR_CATEGORIES | frozenset(
    {
        "response_too_large",
        "invalid_json",
        "request_id_missing",
        "request_id_invalid",
        "query_project_allowlist_missing",
        "query_project_id_mismatch",
        "pipeline_project_allowlist_missing",
        "pipeline_project_id_mismatch",
        "project_key_missing",
        "pipeline_identity_missing",
        "pipeline_not_terminal",
        "pipeline_worker_trace_missing",
        "pipeline_worker_outcome_mismatch",
        "pipeline_failed",
        "query_fixture_mismatch",
        "request_status_unexpected",
        "mcp_tool_error_missing",
        "terminal_log_missing",
        "terminal_log_mismatch",
        "owning_span_missing",
        "root_service_revision_missing",
        "trace_correlation_mismatch",
        "lookup_incomplete",
        "lookup_not_found",
        "pipeline_execution_id_missing",
        "pipeline_response_unexpected",
        "pipeline_status_unrecognized",
        "artifact_write_failed",
        "fixture_unavailable",
        "fixture_not_verified",
        "query_answer_mismatch",
        "query_citation_mismatch",
        "preflight_failed",
        "preflight_not_run",
        "workflow_configuration_invalid",
        "verified",
        "pipeline_opt_in_disabled",
        "pipeline_not_run",
        "native_link_console_check_required",
        "not_started",
        "not_verified",
        "manual_console_verification_required",
        "http_response",
        "submit",
        "status_read",
        "request_failed",
        "submit_failed",
        "entry_limit",
        "page_limit",
        "pagination_cycle",
        "trace_limit",
        "span_limit",
        "span_parent_issue",
        "span_outcome_missing",
        "span_outcome_unrecognized",
        "duplicate_span_ids",
        "invalid_span_ids",
        "request_id_attribute_missing",
        "trace_id_missing_from_matched_log",
    }
)
SAFE_ATTEMPT_CODES = SAFE_ERROR_CODES | frozenset(
    {
        "verified",
        "http_response",
        "submit",
        "status_read",
        "request_failed",
        "submit_failed",
        "not_started",
        "not_verified",
        "manual_console_verification_required",
        "no_data",
        "entries_found",
        "incomplete",
        "page_read",
        "trace_read",
        "empty_trace",
        "found",
        "not_found",
        "available",
        "truncated",
    }
)


@dataclass(frozen=True)
class HTTPResult:
    status: int
    headers: dict[str, str]
    body: bytes | None
    error_code: str | None = None


@dataclass(frozen=True)
class ProbeConfig:
    query_project_id: str | None
    query_project_key: str | None
    pipeline_project_id: str | None
    pipeline_bearer_token: str | None


class ProbeError(Exception):
    def __init__(self, code: str):
        safe_code = code if code in SAFE_ERROR_CODES else "network_error"
        super().__init__(safe_code)
        self.code = safe_code


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


class SafeHTTPClient:
    """HTTP adapter pinned to the DEV BFF host; response bodies stay in memory."""

    def __init__(
        self,
        base_url: str = DEV_BFF_URL,
        timeout: float = HTTP_TIMEOUT,
        opener: Any | None = None,
    ):
        if base_url != DEV_BFF_URL:
            raise ValueError("base_url_invalid")
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.opener = opener or urllib.request.build_opener(NoRedirectHandler()).open

    def request(
        self,
        path: str,
        method: str,
        body: dict[str, Any] | None = None,
        bearer_token: str | None = None,
        project_id: str | None = None,
    ) -> HTTPResult:
        if not path.startswith("/") or path.startswith("//") or "\r" in path or "\n" in path:
            raise ProbeError("invalid_response")
        headers = {"Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if bearer_token:
            headers["Authorization"] = "Bearer " + bearer_token
        if project_id:
            headers["X-Project-ID"] = project_id
        data = json.dumps(body, separators=(",", ":")).encode("utf-8") if body is not None else None
        request = urllib.request.Request(self.base_url + path, data=data, headers=headers, method=method)
        try:
            with self.opener(request, timeout=self.timeout) as response:
                status = int(response.getcode())
                response_headers = {str(key).lower(): str(value) for key, value in response.headers.items()}
                if status < 200 or status >= 300:
                    return HTTPResult(status, response_headers, None, trace_lookup.classify_http_status(status))
                raw = response.read(MAX_RESPONSE_BYTES + 1)
                if len(raw) > MAX_RESPONSE_BYTES:
                    return HTTPResult(status, response_headers, None, "response_too_large")
                return HTTPResult(status, response_headers, raw)
        except urllib.error.HTTPError as exc:
            try:
                response_headers = {str(key).lower(): str(value) for key, value in (exc.headers or {}).items()}
            except (AttributeError, TypeError):
                response_headers = {}
            status = int(exc.code)
            exc.close()
            return HTTPResult(status, response_headers, None, trace_lookup.classify_http_status(status))
        except urllib.error.URLError as exc:
            reason = getattr(exc, "reason", None)
            code = "timeout" if isinstance(reason, (TimeoutError,)) else "network_error"
            return HTTPResult(0, {}, None, code)
        except TimeoutError:
            return HTTPResult(0, {}, None, "timeout")
        except OSError:
            return HTTPResult(0, {}, None, "network_error")


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, ValueError):
        raise ProbeError("fixture_unavailable") from None
    if not isinstance(value, dict):
        raise ProbeError("fixture_unavailable")
    return value


def config_from_environment() -> ProbeConfig:
    return ProbeConfig(
        query_project_id=os.environ.get("LWC_DEV_REQUEST_ID_PROBE_PROJECT_ID") or None,
        query_project_key=os.environ.get("LWC_DEV_REQUEST_ID_PROBE_PROJECT_KEY") or None,
        pipeline_project_id=os.environ.get("LWC_DEV_PIPELINE_QA_PROJECT") or None,
        pipeline_bearer_token=os.environ.get("LWC_DEV_REQUEST_ID_PROBE_PIPELINE_USER_TOKEN") or None,
    )


def _safe_code(value: Any, default: str = "api_error") -> str:
    return value if isinstance(value, str) and value in SAFE_ERROR_CODES else default


def _safe_attempt(value: Any) -> str:
    return value if isinstance(value, str) and value in SAFE_ATTEMPT_CODES else "api_error"


def _safe_label(value: Any, pattern: re.Pattern[str] = trace_lookup.SAFE_LABEL_RE) -> str | None:
    return value if isinstance(value, str) and pattern.fullmatch(value) else None


def _safe_operation(value: Any) -> str | None:
    if isinstance(value, str) and value in trace_lookup.MCP_OPERATIONS:
        return value
    return _safe_label(value, trace_lookup.SAFE_OPERATION_RE)


def _response_json(response: HTTPResult) -> dict[str, Any] | None:
    if response.body is None:
        return None
    try:
        value = json.loads(response.body.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        return None
    return value if isinstance(value, dict) else None


def _response_id(response: HTTPResult) -> str | None:
    raw = response.headers.get("x-request-id")
    if not raw:
        return None
    try:
        return trace_lookup.validate_request_id(raw)
    except ValueError:
        return None


def _configured_project(env_id: str | None, allowlisted_id: Any, prefix: str) -> tuple[str | None, str | None]:
    if not isinstance(allowlisted_id, str) or not allowlisted_id:
        return None, prefix + "_project_allowlist_missing"
    try:
        safe_allowlist = trace_lookup.validate_project(allowlisted_id)
    except ValueError:
        return None, prefix + "_project_allowlist_missing"
    if env_id != safe_allowlist:
        return None, prefix + "_project_id_mismatch"
    return safe_allowlist, None


def _trace_summary(lookup: dict[str, Any] | None) -> dict[str, Any]:
    if not isinstance(lookup, dict):
        return {"status": "incomplete", "complete": False, "attempts": [], "trace_ids": [], "traces": [], "terminal_logs": [], "issues": []}
    traces: list[dict[str, Any]] = []
    trace_ids: list[str] = []
    console_urls: list[str] = []
    for trace in lookup.get("traces", []):
        if not isinstance(trace, dict):
            continue
        trace_id = trace.get("trace_id")
        if not isinstance(trace_id, str) or not trace_lookup.TRACE_ID_RE.fullmatch(trace_id):
            continue
        trace_ids.append(trace_id)
        console_url = f"https://console.cloud.google.com/traces/list?project={urllib.parse.quote(DEV_GCP_PROJECT, safe='')}&tid={trace_id}"
        console_urls.append(console_url)
        spans: list[dict[str, Any]] = []
        for span in trace.get("spans", []):
            if not isinstance(span, dict):
                continue
            row: dict[str, Any] = {
                "span_id": span.get("span_id") if isinstance(span.get("span_id"), str) and trace_lookup.SPAN_ID_RE.fullmatch(span.get("span_id")) else None,
                "parent_span_id": span.get("parent_span_id") if isinstance(span.get("parent_span_id"), str) and trace_lookup.SPAN_ID_RE.fullmatch(span.get("parent_span_id")) else None,
                "span": _safe_label(span.get("span"), trace_lookup.SAFE_OPERATION_RE),
                "operation": _safe_operation(span.get("operation")),
                "service": _safe_label(span.get("service")),
                "revision": _safe_label(span.get("revision")),
                "parent_status": span.get("parent_status") if isinstance(span.get("parent_status"), str) and span.get("parent_status") in {"root", "connected", "missing_parent", "bounded_parent_missing", "invalid_parent_id", "cycle", "unknown"} else "unknown",
                "outcome": span.get("outcome") if isinstance(span.get("outcome"), str) and span.get("outcome") in trace_lookup.FINITE_OUTCOMES else None,
                "outcome_status": span.get("outcome_status") if isinstance(span.get("outcome_status"), str) and span.get("outcome_status") in {"present", "missing", "unrecognized"} else "missing",
                "request_id_match": bool(span.get("request_id_match")),
                "origin_request_id_match": bool(span.get("origin_request_id_match")),
            }
            if isinstance(span.get("attempt_id"), str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", span["attempt_id"]):
                row["attempt_id"] = span["attempt_id"]
            spans.append(row)
        traces.append(
            {
                "trace_id": trace_id,
                "status": trace.get("status") if trace.get("status") in {"available", "truncated", "not_found", "rate_limited", "timeout", "permission_denied", "authentication_required", "service_unavailable", "invalid_response"} else "incomplete",
                "console_url": console_url,
                "origin_log": bool(trace.get("origin_log")),
                "correlation_evidence": [item for item in trace.get("correlation_evidence", []) if isinstance(item, str) and item in {"request_id", "request.id", "origin_request_id", "origin_request.id", "diagnostic_id", "span.request.id", "span.origin_request.id"}],
                "spans": spans,
            }
        )
    terminal_logs = []
    for row in lookup.get("terminal_logs", []):
        if not isinstance(row, dict):
            continue
        message = row.get("message")
        if message not in trace_lookup.TERMINAL_MESSAGES:
            continue
        safe_row = {
            "message": message,
            "correlation_fields": [field for field in row.get("correlation_fields", []) if isinstance(field, str) and field in trace_lookup.LOG_CORRELATION_FIELDS],
            "correlation_id": row.get("correlation_id") if isinstance(row.get("correlation_id"), str) and _is_request_id(row.get("correlation_id")) else None,
            "trace_id": row.get("trace_id") if isinstance(row.get("trace_id"), str) and trace_lookup.TRACE_ID_RE.fullmatch(row.get("trace_id")) else None,
            "span_id": row.get("span_id") if isinstance(row.get("span_id"), str) and trace_lookup.SPAN_ID_RE.fullmatch(row.get("span_id")) else None,
            "outcome": row.get("outcome") if isinstance(row.get("outcome"), str) and row.get("outcome") in trace_lookup.FINITE_OUTCOMES else None,
            "phase": _safe_label(row.get("phase")),
            "status_code": row.get("status_code") if isinstance(row.get("status_code"), int) and 100 <= row.get("status_code") <= 599 else None,
            "transport": row.get("transport") if row.get("transport") in {"http", "mcp", "pipeline", "worker"} else None,
            "operation": _safe_operation(row.get("operation")),
            "service": _safe_label(row.get("service")),
            "revision": _safe_label(row.get("revision")),
            "mcp_is_error": row.get("mcp_is_error") if isinstance(row.get("mcp_is_error"), bool) else None,
            "attempt_id": row.get("attempt_id") if isinstance(row.get("attempt_id"), str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", row.get("attempt_id")) else None,
        }
        terminal_logs.append(safe_row)
    issues = []
    for issue in lookup.get("issues", []):
        if not isinstance(issue, dict):
            continue
        scope = issue.get("scope") if issue.get("scope") in {"logs", "trace", "traces"} else "trace"
        code = _safe_code(issue.get("code"), "api_error")
        trace_id = issue.get("trace_id")
        safe_issue: dict[str, Any] = {"scope": scope, "code": code}
        if isinstance(trace_id, str) and trace_lookup.TRACE_ID_RE.fullmatch(trace_id):
            safe_issue["trace_id"] = trace_id
        issues.append(safe_issue)
    return {
        "status": lookup.get("status") if lookup.get("status") in {"found", "not_found", "incomplete"} else "incomplete",
        "complete": bool(lookup.get("complete")),
        "attempts": [_safe_attempt(value) for value in lookup.get("log_search", {}).get("attempts", [])] if isinstance(lookup.get("log_search"), dict) else [],
        "trace_ids": sorted(set(trace_ids)),
        "console_urls": sorted(set(console_urls)),
        "traces": traces,
        "terminal_logs": terminal_logs,
        "async_links": [
            {
                "origin_trace_id": link.get("origin_trace_id") if isinstance(link.get("origin_trace_id"), str) and trace_lookup.TRACE_ID_RE.fullmatch(link.get("origin_trace_id")) else None,
                "related_trace_id": link.get("related_trace_id") if isinstance(link.get("related_trace_id"), str) and trace_lookup.TRACE_ID_RE.fullmatch(link.get("related_trace_id")) else None,
                "attempt_ids": [value for value in link.get("attempt_ids", []) if isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value)],
                "relationship": link.get("relationship") if link.get("relationship") in {"separate_trace_with_origin_request_id_evidence", "separate_trace_with_stdouttrace_link"} else "unknown",
                "evidence": link.get("evidence") if link.get("evidence") in {"origin_request_id_attribute", "stdouttrace_span_link"} else None,
                "console_url": f"https://console.cloud.google.com/traces/list?project={urllib.parse.quote(DEV_GCP_PROJECT, safe='')}&tid={link.get('related_trace_id')}" if isinstance(link.get("related_trace_id"), str) and trace_lookup.TRACE_ID_RE.fullmatch(link.get("related_trace_id")) else None,
                "native_link_verification": "manual_console_check_required",
            }
            for link in lookup.get("async_links", [])
            if isinstance(link, dict)
        ],
        "issues": issues,
    }


def _all_spans(lookup: dict[str, Any] | None) -> list[tuple[str, dict[str, Any]]]:
    if not isinstance(lookup, dict):
        return []
    result: list[tuple[str, dict[str, Any]]] = []
    for trace in lookup.get("traces", []):
        if not isinstance(trace, dict):
            continue
        trace_id = trace.get("trace_id")
        for span in trace.get("spans", []):
            if isinstance(span, dict):
                result.append((str(trace_id or ""), span))
    return result


def _safe_span_summary(trace_id: str, span: dict[str, Any]) -> dict[str, Any]:
    return {
        "trace_id": trace_id if trace_lookup.TRACE_ID_RE.fullmatch(trace_id) else None,
        "span_id": span.get("span_id") if isinstance(span.get("span_id"), str) and trace_lookup.SPAN_ID_RE.fullmatch(span.get("span_id")) else None,
        "parent_span_id": span.get("parent_span_id") if isinstance(span.get("parent_span_id"), str) and trace_lookup.SPAN_ID_RE.fullmatch(span.get("parent_span_id")) else None,
        "span": _safe_label(span.get("span"), trace_lookup.SAFE_OPERATION_RE),
        "operation": _safe_operation(span.get("operation")),
        "service": _safe_label(span.get("service")),
        "revision": _safe_label(span.get("revision")),
        "outcome": span.get("outcome") if isinstance(span.get("outcome"), str) and span.get("outcome") in trace_lookup.FINITE_OUTCOMES else None,
        "parent_status": span.get("parent_status") if isinstance(span.get("parent_status"), str) and span.get("parent_status") in {"root", "connected", "missing_parent", "bounded_parent_missing", "invalid_parent_id", "cycle", "unknown"} else "unknown",
    }


def _terminal_log(
    lookup: dict[str, Any] | None,
    message: str,
    request_id: str,
    correlation_field: str,
) -> dict[str, Any] | None:
    if not isinstance(lookup, dict):
        return None
    for row in lookup.get("terminal_logs", []):
        if (
            isinstance(row, dict)
            and row.get("message") == message
            and row.get("correlation_id") == request_id
            and correlation_field in row.get("correlation_fields", [])
        ):
            return row
    return None


def _root_span(lookup: dict[str, Any] | None, request_id: str, outcome: str | None = None) -> tuple[str, dict[str, Any]] | None:
    for trace_id, span in _all_spans(lookup):
        if span.get("span") != "http.request" or span.get("parent_status") != "root" or not span.get("request_id_match"):
            continue
        if outcome is not None and span.get("outcome") != outcome:
            continue
        if not span.get("service") or not span.get("revision"):
            continue
        return trace_id, span
    return None


def _query_span(lookup: dict[str, Any] | None, request_id: str, outcome: str) -> tuple[str, dict[str, Any]] | None:
    for trace_id, span in _all_spans(lookup):
        if span.get("span") == "query.request" and span.get("request_id_match") and span.get("outcome") == outcome:
            if span.get("service") and span.get("revision"):
                return trace_id, span
    return None


def _lookup_ready(lookup: dict[str, Any], request_id: str, expected_messages: tuple[tuple[str, str], ...]) -> bool:
    if lookup.get("status") != "found" or not lookup.get("complete"):
        return False
    for message, field in expected_messages:
        if _terminal_log(lookup, message, request_id, field) is None:
            return False
    return True


def _primary_issue(codes: list[str]) -> str:
    priorities = (
        "authentication_required",
        "authentication_timeout",
        "authentication_unavailable",
        "gcloud_unavailable",
        "permission_denied",
        "rate_limited",
        "timeout",
        "service_unavailable",
        "network_error",
        "pipeline_not_terminal",
        "pipeline_failed",
        "pipeline_worker_trace_missing",
        "pipeline_worker_outcome_mismatch",
        "trace_correlation_mismatch",
        "fixture_not_verified",
        "fixture_unavailable",
        "lookup_incomplete",
        "lookup_not_found",
    )
    present = set(codes)
    return next((code for code in priorities if code in present), sorted(present)[0] if present else "verified")


class ProbeRunner:
    def __init__(
        self,
        config: ProbeConfig,
        app: Any,
        lookup_fn: Callable[[str], dict[str, Any]],
        fixture: dict[str, Any],
        prerequisites: dict[str, Any],
        *,
        sleep_fn: Callable[[float], None] = time.sleep,
        now_fn: Callable[[], dt.datetime] = lambda: dt.datetime.now(dt.timezone.utc),
    ):
        self.config = config
        self.app = app
        self.lookup_fn = lookup_fn
        self.fixture = fixture
        self.prerequisites = prerequisites
        self.sleep_fn = sleep_fn
        self.now_fn = now_fn
        self.checks: list[dict[str, Any]] = []
        self.lookup_attempt_history: list[dict[str, Any]] = []

    def _add_check(
        self,
        name: str,
        status: str,
        code: str,
        *,
        ids: dict[str, str] | None = None,
        attempts: list[str] | None = None,
        evidence: dict[str, Any] | None = None,
    ) -> None:
        if status not in {"pass", "fail", "skip", "manual"}:
            status, code = "fail", "invalid_response"
        row: dict[str, Any] = {"name": name, "status": status, "code": _safe_code(code)}
        if ids:
            row["ids"] = {
                key: value
                for key, value in ids.items()
                if key in {"request_id", "envelope_request_id", "call_request_id", "execution_id"}
                and isinstance(value, str)
                and (key == "execution_id" or _is_request_id(value))
            }
        if attempts:
            row["attempts"] = [_safe_attempt(value) for value in attempts]
        if evidence:
            row["evidence"] = evidence
        self.checks.append(row)

    def _query_project(self) -> tuple[str | None, str | None]:
        project_id, error = _configured_project(
            self.config.query_project_id,
            self.prerequisites.get("query_project_id"),
            "query",
        )
        if error:
            return None, error
        if not self.config.query_project_key:
            return None, "project_key_missing"
        return project_id, None

    def _pipeline_project(self) -> tuple[str | None, str | None]:
        return _configured_project(
            self.config.pipeline_project_id,
            self.prerequisites.get("pipeline_project_id"),
            "pipeline",
        )

    def preflight_blockers(self, *, enable_pipeline: bool = False) -> list[str]:
        blockers: list[str] = []
        project_id, project_error = self._query_project()
        if project_error:
            blockers.append(project_error)
        if not self.config.query_project_key:
            blockers.append("project_key_missing")
        if self.prerequisites.get("fixture_readiness") != "verified":
            blockers.append("fixture_not_verified")
        if (
            not project_id
            or self.prerequisites.get("fixture_project_id") != project_id
            or self.prerequisites.get("fixture_marker") != self.fixture.get("expected_source_marker")
        ):
            blockers.append("fixture_not_verified")
        if enable_pipeline:
            pipeline_id, pipeline_error = self._pipeline_project()
            if pipeline_error:
                blockers.append(pipeline_error)
            if not self._pipeline_token():
                blockers.append("pipeline_identity_missing")
        return sorted(set(blockers))

    def _pipeline_token(self) -> str | None:
        return self.config.pipeline_bearer_token

    def _request_lookup(self, request_id: str, ready: Callable[[dict[str, Any]], bool]) -> tuple[dict[str, Any] | None, list[str]]:
        attempts: list[str] = []
        last_lookup: dict[str, Any] | None = None
        for index in range(LOOKUP_ATTEMPTS):
            try:
                lookup = self.lookup_fn(request_id)
            except trace_lookup.ProviderError as exc:
                attempts.append(exc.category)
                self.lookup_attempt_history.append(
                    {"request_id": request_id, "attempt": index + 1, "status": exc.category, "issue_codes": [exc.category], "log_attempts": []}
                )
                if exc.category not in {"rate_limited", "timeout", "service_unavailable"}:
                    break
                lookup = None
            except (OSError, TimeoutError):
                attempts.append("network_error")
                self.lookup_attempt_history.append(
                    {"request_id": request_id, "attempt": index + 1, "status": "network_error", "issue_codes": ["network_error"], "log_attempts": []}
                )
                lookup = None
            if lookup is not None:
                last_lookup = lookup
                status = lookup.get("status") if lookup.get("status") in {"found", "not_found", "incomplete"} else "incomplete"
                attempts.append(status)
                issues = lookup.get("issues", [])
                codes = {issue.get("code") for issue in issues if isinstance(issue, dict)}
                self.lookup_attempt_history.append(
                    {
                        "request_id": request_id,
                        "attempt": index + 1,
                        "status": status,
                        "issue_codes": [_safe_attempt(issue.get("code")) for issue in issues if isinstance(issue, dict)],
                        "log_attempts": [
                            _safe_attempt(value)
                            for value in lookup.get("log_search", {}).get("attempts", [])
                        ] if isinstance(lookup.get("log_search"), dict) else [],
                    }
                )
                if codes & {"authentication_required", "permission_denied", "authentication_timeout", "authentication_unavailable", "gcloud_unavailable"}:
                    break
                if ready(lookup):
                    return lookup, attempts
            if index + 1 < LOOKUP_ATTEMPTS:
                self.sleep_fn(LOOKUP_DELAY_SECONDS)
        return last_lookup, attempts

    def _safe_lookup_evidence(
        self,
        lookup: dict[str, Any] | None,
        request_id: str,
        *,
        root_outcome: str,
        terminal_specs: tuple[tuple[str, str, int | None, str | None, bool | None], ...],
        query_outcome: str | None = None,
    ) -> tuple[dict[str, Any], list[str]]:
        issues: list[str] = []
        if not isinstance(lookup, dict) or lookup.get("status") != "found" or not lookup.get("complete"):
            issues.append("lookup_incomplete" if lookup and lookup.get("status") != "not_found" else "lookup_not_found")
        summary = _trace_summary(lookup)
        terminals: list[dict[str, Any]] = []
        for message, correlation_field, expected_status, expected_outcome, expected_mcp_error in terminal_specs:
            row = _terminal_log(lookup, message, request_id, correlation_field)
            if row is None:
                issues.append("terminal_log_missing")
                continue
            terminals.append(row)
            if expected_status is not None and row.get("status_code") != expected_status:
                issues.append("terminal_log_mismatch")
            if expected_outcome is not None and row.get("outcome") != expected_outcome:
                issues.append("terminal_log_mismatch")
            if expected_mcp_error is not None and row.get("mcp_is_error") is not expected_mcp_error:
                issues.append("terminal_log_mismatch")
        root = _root_span(lookup, request_id, root_outcome)
        if root is None:
            if _root_span(lookup, request_id) is None:
                issues.append("owning_span_missing")
            else:
                issues.append("terminal_log_mismatch")
        query_span = _query_span(lookup, request_id, query_outcome) if query_outcome else None
        if query_outcome and query_span is None:
            issues.append("owning_span_missing")
        if root is not None:
            root_trace_id, root_node = root
            summary["root"] = _safe_span_summary(root_trace_id, root_node)
        if query_span is not None:
            query_trace_id, query_node = query_span
            summary["query_span"] = _safe_span_summary(query_trace_id, query_node)
        summary["matched_terminal_logs"] = terminals
        return summary, sorted(set(issues))

    def _safe_mcp_call_evidence(self, lookup: dict[str, Any] | None, call_id: str) -> tuple[dict[str, Any], list[str]]:
        issues: list[str] = []
        if not isinstance(lookup, dict) or lookup.get("status") != "found" or not lookup.get("complete"):
            issues.append("lookup_incomplete" if lookup and lookup.get("status") != "not_found" else "lookup_not_found")
        terminal = _terminal_log(lookup, "query.terminal", call_id, "diagnostic_id")
        if terminal is None:
            issues.append("terminal_log_missing")
        elif not (
            terminal.get("status_code") == 200
            and terminal.get("outcome") == "error"
            and terminal.get("transport") == "mcp"
            and terminal.get("mcp_is_error") is True
        ):
            issues.append("terminal_log_mismatch")
        spans = _all_spans(lookup)
        owning = [
            (trace_id, span)
            for trace_id, span in spans
            if span.get("span") in {"mcp.request", "query.request"}
            and span.get("request_id_match")
            and span.get("outcome") == "error"
        ]
        mcp_roots = [
            (trace_id, span)
            for trace_id, span in owning
            if span.get("span") == "mcp.request" and span.get("parent_status") == "root"
        ]
        query_spans = [
            (trace_id, span)
            for trace_id, span in owning
            if span.get("span") == "query.request" and span.get("parent_status") == "connected"
        ]
        if not mcp_roots or not query_spans:
            issues.append("owning_span_missing")
        if any(not span.get("service") or not span.get("revision") for _, span in owning):
            issues.append("root_service_revision_missing")
        if mcp_roots and any(trace_id != mcp_roots[0][0] for trace_id, _span in query_spans):
            issues.append("trace_correlation_mismatch")
        summary = _trace_summary(lookup)
        summary["mcp_owning_spans"] = [
            _safe_span_summary(trace_id, span)
            for trace_id, span in owning
        ]
        summary["matched_terminal_logs"] = [terminal] if terminal else []
        return summary, sorted(set(issues))

    def _run_rest_404(self) -> None:
        attempts: list[str] = []
        ids: dict[str, str] = {}
        evidence: dict[str, Any] = {}
        issues: list[str] = []
        try:
            response = self.app.request("/api/v1/__lwc402_request_id_probe_not_found__", "GET")
        except ProbeError as exc:
            self._add_check("rest_404", "fail", exc.code, attempts=["request_failed"])
            return
        attempts.append("http_response")
        request_id = _response_id(response)
        if request_id:
            ids["request_id"] = request_id
        else:
            issues.append("request_id_missing")
        if response.status != 404:
            issues.append(trace_lookup.classify_http_status(response.status) if response.status >= 400 else "request_status_unexpected")
        if request_id:
            lookup, lookup_attempts = self._request_lookup(
                request_id,
                lambda value: _lookup_ready(value, request_id, (("http.request.terminal", "request_id"),)),
            )
            attempts.extend(lookup_attempts)
            evidence, lookup_issues = self._safe_lookup_evidence(
                lookup,
                request_id,
                root_outcome="rejected",
                terminal_specs=(("http.request.terminal", "request_id", 404, "rejected", None),),
            )
            if lookup is None:
                lookup_failure = _primary_issue(lookup_attempts)
                if lookup_failure != "verified":
                    lookup_issues.append(lookup_failure)
            issues.extend(lookup_issues)
        code = _primary_issue(issues)
        self._add_check("rest_404", "pass" if not issues else "fail", code, ids=ids, attempts=attempts, evidence=evidence)

    def _extract_mcp_ids(self, response: HTTPResult) -> tuple[dict[str, str], dict[str, Any] | None, list[str]]:
        ids: dict[str, str] = {}
        issues: list[str] = []
        envelope_id = _response_id(response)
        if envelope_id:
            ids["envelope_request_id"] = envelope_id
        else:
            issues.append("request_id_missing")
        body = _response_json(response)
        if body is None:
            issues.append("invalid_json")
            return ids, None, issues
        result = body.get("result")
        if not isinstance(result, dict):
            issues.append("mcp_tool_error_missing")
            return ids, body, issues
        metadata = result.get("_meta")
        call_id = metadata.get("request_id") if isinstance(metadata, dict) else None
        try:
            ids["call_request_id"] = trace_lookup.validate_request_id(call_id)
        except (ValueError, TypeError):
            issues.append("request_id_missing")
        structured = result.get("structuredContent")
        diagnostic_id = structured.get("diagnostic_id") if isinstance(structured, dict) else None
        try:
            safe_diagnostic_id = trace_lookup.validate_request_id(diagnostic_id)
        except (ValueError, TypeError):
            safe_diagnostic_id = None
        if safe_diagnostic_id and "call_request_id" in ids and safe_diagnostic_id != ids["call_request_id"]:
            issues.append("trace_correlation_mismatch")
        if not safe_diagnostic_id:
            issues.append("request_id_missing")
        return ids, body, issues

    def _run_mcp_error(self, project_id: str | None, config_error: str | None) -> None:
        if config_error or not project_id or not self.config.query_project_key:
            self._add_check("mcp_tool_error", "fail", config_error or "project_key_missing", attempts=["not_started"])
            return
        try:
            response = self.app.request(
                "/mcp",
                "POST",
                {
                    "jsonrpc": "2.0",
                    "id": 402,
                    "method": "tools/call",
                    "params": {"name": "query_project", "arguments": {"q": " "}},
                },
                bearer_token=self.config.query_project_key,
                project_id=project_id,
            )
        except ProbeError as exc:
            self._add_check("mcp_tool_error", "fail", exc.code, attempts=["request_failed"])
            return
        ids, body, issues = self._extract_mcp_ids(response)
        attempts = ["http_response"]
        if response.status != 200:
            issues.append(trace_lookup.classify_http_status(response.status) if response.status >= 400 else "request_status_unexpected")
        result = body.get("result") if isinstance(body, dict) else None
        if not isinstance(result, dict) or result.get("isError") is not True:
            issues.append("mcp_tool_error_missing")
        if ids.get("call_request_id") and ids.get("envelope_request_id") == ids.get("call_request_id"):
            issues.append("trace_correlation_mismatch")
        call_id = ids.get("call_request_id")
        envelope_id = ids.get("envelope_request_id")
        call_lookup = None
        envelope_lookup = None
        if call_id:
            call_lookup, poll_attempts = self._request_lookup(
                call_id,
                lambda value: _lookup_ready(value, call_id, (("query.terminal", "diagnostic_id"),)),
            )
            attempts.extend(poll_attempts)
            call_evidence, call_issues = self._safe_mcp_call_evidence(call_lookup, call_id)
            if call_lookup is None:
                lookup_failure = _primary_issue(poll_attempts)
                if lookup_failure != "verified":
                    call_issues.append(lookup_failure)
            issues.extend(call_issues)
        else:
            call_evidence = {}
        if envelope_id:
            envelope_lookup, poll_attempts = self._request_lookup(
                envelope_id,
                lambda value: _lookup_ready(value, envelope_id, (("http.request.terminal", "request_id"),)),
            )
            attempts.extend(poll_attempts)
            # result.isError belongs to the MCP call; HTTP 200 is a successful
            # envelope under request_lifecycle unless the JSON-RPC envelope itself failed.
            envelope_evidence, envelope_issues = self._safe_lookup_evidence(
                envelope_lookup,
                envelope_id,
                root_outcome="success",
                terminal_specs=(("http.request.terminal", "request_id", 200, "success", None),),
            )
            if envelope_lookup is None:
                lookup_failure = _primary_issue(poll_attempts)
                if lookup_failure != "verified":
                    envelope_issues.append(lookup_failure)
            issues.extend(envelope_issues)
        else:
            envelope_evidence = {}
        call_traces = set(call_evidence.get("trace_ids", []))
        envelope_traces = set(envelope_evidence.get("trace_ids", []))
        if call_traces and envelope_traces and not call_traces.intersection(envelope_traces):
            issues.append("trace_correlation_mismatch")
        evidence = {"call": call_evidence, "envelope": envelope_evidence}
        code = _primary_issue(issues)
        self._add_check("mcp_tool_error", "pass" if not issues else "fail", code, ids=ids, attempts=attempts, evidence=evidence)

    def _query_body_issue(self, response: HTTPResult) -> str | None:
        body = _response_json(response)
        if body is None:
            return "invalid_json"
        expected_query = self.fixture.get("query")
        expected_mode = self.fixture.get("mode")
        if not isinstance(expected_query, str) or body.get("query") != expected_query or body.get("mode") != expected_mode:
            return "query_fixture_mismatch"
        answer = body.get("ai_synth")
        expected_answer = self.fixture.get("expected_answer_contains")
        expected_title = self.fixture.get("expected_citation_title")
        expected_type = self.fixture.get("expected_citation_type")
        if not isinstance(answer, str) or not isinstance(expected_answer, str) or expected_answer not in answer:
            return "query_answer_mismatch"
        if not isinstance(expected_title, str) or expected_type != "source":
            return "fixture_unavailable"
        results = body.get("results")
        citations = body.get("citations")
        if not isinstance(results, list) or not isinstance(citations, list):
            return "query_citation_mismatch"
        result_ids = {
            row.get("id")
            for row in results
            if isinstance(row, dict)
            and row.get("title") == expected_title
            and row.get("type") == expected_type
            and isinstance(row.get("id"), str)
        }
        matches_citation = bool(result_ids) and any(
            isinstance(row, dict)
            and row.get("text") == expected_title
            and row.get("type") == expected_type
            and row.get("id") in result_ids
            for row in citations
        )
        return None if matches_citation else "query_citation_mismatch"

    def _query_body_matches_fixture(self, response: HTTPResult) -> bool:
        return self._query_body_issue(response) is None

    def _run_query(self, project_id: str | None, config_error: str | None) -> None:
        if config_error or not project_id or not self.config.query_project_key:
            self._add_check("query_fixture", "fail", config_error or "project_key_missing", attempts=["not_started"])
            return
        try:
            response = self.app.request(
                "/api/v1/query",
                "POST",
                {"q": self.fixture["query"], "mode": self.fixture["mode"]},
                bearer_token=self.config.query_project_key,
                project_id=project_id,
            )
        except (ProbeError, KeyError, TypeError) as exc:
            code = exc.code if isinstance(exc, ProbeError) else "fixture_unavailable"
            self._add_check("query_fixture", "fail", code, attempts=["request_failed"])
            return
        request_id = _response_id(response)
        ids = {"request_id": request_id} if request_id else {}
        attempts = ["http_response"]
        issues: list[str] = []
        if response.status != 200:
            issues.append(trace_lookup.classify_http_status(response.status) if response.status >= 400 else "request_status_unexpected")
        else:
            issue = self._query_body_issue(response)
            if issue:
                issues.append(issue)
        evidence: dict[str, Any] = {}
        if request_id:
            lookup, poll_attempts = self._request_lookup(
                request_id,
                lambda value: _lookup_ready(
                    value,
                    request_id,
                    (("http.request.terminal", "request_id"), ("query.terminal", "diagnostic_id")),
                ),
            )
            attempts.extend(poll_attempts)
            evidence, lookup_issues = self._safe_lookup_evidence(
                lookup,
                request_id,
                root_outcome="success",
                terminal_specs=(
                    ("http.request.terminal", "request_id", 200, "success", None),
                    ("query.terminal", "diagnostic_id", 200, "success", False),
                ),
                query_outcome="success",
            )
            if lookup is None:
                lookup_failure = _primary_issue(poll_attempts)
                if lookup_failure != "verified":
                    lookup_issues.append(lookup_failure)
            issues.extend(lookup_issues)
        else:
            issues.append("request_id_missing")
        code = _primary_issue(issues)
        self._add_check("query_fixture", "pass" if not issues else "fail", code, ids=ids, attempts=attempts, evidence=evidence)

    def _run_pipeline(self, enabled: bool) -> None:
        if not enabled:
            self._add_check("pipeline", "skip", "pipeline_opt_in_disabled", attempts=["not_started"])
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
            return
        project_id, project_error = self._pipeline_project()
        if project_error:
            self._add_check("pipeline", "fail", project_error, attempts=["not_started"])
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
            return
        pipeline_token = self._pipeline_token()
        if not pipeline_token:
            self._add_check("pipeline", "fail", "pipeline_identity_missing", attempts=["not_started"])
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
            return
        issues: list[str] = []
        attempts = ["submit"]
        ids: dict[str, str] = {}
        try:
            submitted = self.app.request(
                "/api/v1/pipeline/run",
                "POST",
                {},
                bearer_token=pipeline_token,
                project_id=project_id,
            )
        except ProbeError as exc:
            self._add_check("pipeline", "fail", exc.code, attempts=["submit_failed"])
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
            return
        request_id = _response_id(submitted)
        if request_id:
            ids["request_id"] = request_id
        else:
            issues.append("request_id_missing")
        body = _response_json(submitted)
        raw_execution_id = body.get("execution_id") if isinstance(body, dict) else None
        execution_id = _safe_execution_id(raw_execution_id)
        if execution_id:
            ids["execution_id"] = execution_id
        else:
            issues.append("pipeline_execution_id_missing")
        if submitted.status != 202:
            issues.append(trace_lookup.classify_http_status(submitted.status) if submitted.status >= 400 else "request_status_unexpected")
        if not request_id or not execution_id or submitted.status != 202:
            self._add_check("pipeline", "fail", _primary_issue(issues) if issues else "pipeline_response_unexpected", ids=ids, attempts=attempts)
            self._add_check("pipeline_native_link", "manual", "pipeline_not_terminal", ids=ids, attempts=["not_verified"])
            return
        pipeline_state = "UNKNOWN"
        poll_request_ids: list[str] = []
        for poll in range(PIPELINE_POLL_ATTEMPTS):
            attempts.append("status_read")
            query = urllib.parse.urlencode({"execution_id": execution_id})
            try:
                status_response = self.app.request(
                    "/api/v1/pipeline/status?" + query,
                    "GET",
                    bearer_token=pipeline_token,
                    project_id=project_id,
                )
            except ProbeError as exc:
                attempts.append(exc.code)
                if exc.code not in {"rate_limited", "timeout", "service_unavailable", "network_error"}:
                    issues.append(exc.code)
                    break
                status_response = None
            if status_response is not None:
                status_id = _response_id(status_response)
                if status_id:
                    poll_request_ids.append(status_id)
                status_body = _response_json(status_response) if status_response.status == 200 else None
                last_execution = status_body.get("last_execution") if isinstance(status_body, dict) else None
                state = last_execution.get("status") if isinstance(last_execution, dict) else None
                if isinstance(state, str) and state in SAFE_PIPELINE_STATES:
                    pipeline_state = state
                elif status_response.status != 200:
                    issues.append(trace_lookup.classify_http_status(status_response.status))
                else:
                    issues.append("pipeline_status_unrecognized")
                if pipeline_state in TERMINAL_PIPELINE_STATES:
                    break
            if poll + 1 < PIPELINE_POLL_ATTEMPTS:
                self.sleep_fn(PIPELINE_POLL_DELAY_SECONDS)
        evidence: dict[str, Any] = {
            "terminal_state": pipeline_state,
            "status_request_ids": poll_request_ids,
            "native_link_verification": "manual_console_check_required",
        }
        if pipeline_state not in TERMINAL_PIPELINE_STATES:
            issues.append("pipeline_not_terminal")
        if pipeline_state != "SUCCEEDED":
            issues.append("pipeline_failed" if pipeline_state in TERMINAL_PIPELINE_STATES else "pipeline_not_terminal")
        if request_id:
            lookup, lookup_attempts = self._request_lookup(
                request_id,
                lambda value: _lookup_ready(value, request_id, (("http.request.terminal", "request_id"),)),
            )
            attempts.extend(lookup_attempts)
            summary, lookup_issues = self._safe_lookup_evidence(
                lookup,
                request_id,
                root_outcome="success",
                terminal_specs=(("http.request.terminal", "request_id", 202, "success", None),),
            )
            if lookup is None:
                lookup_failure = _primary_issue(lookup_attempts)
                if lookup_failure != "verified":
                    lookup_issues.append(lookup_failure)
            spans = _all_spans(lookup)
            submit_spans = [
                (trace_id, span)
                for trace_id, span in spans
                if span.get("span") == "pipeline.submit"
                and span.get("request_id_match")
                and span.get("outcome") == "accepted"
            ]
            if not submit_spans:
                lookup_issues.append("owning_span_missing")
            worker_spans = [
                (trace_id, span)
                for trace_id, span in spans
                if span.get("span") == "pipeline.worker"
                and span.get("parent_status") == "root"
                and (span.get("request_id_match") or span.get("origin_request_id_match"))
            ]
            origin_trace_ids = {trace_id for trace_id, _span in submit_spans}
            distinct_worker_spans = [
                (trace_id, span)
                for trace_id, span in worker_spans
                if trace_id not in origin_trace_ids and isinstance(span.get("service"), str) and "worker" in span.get("service", "").lower() and span.get("revision")
            ]
            if not distinct_worker_spans:
                lookup_issues.append("pipeline_worker_trace_missing")
            elif not any(span.get("outcome") == "success" for _, span in distinct_worker_spans):
                lookup_issues.append("pipeline_worker_outcome_mismatch")
            if origin_trace_ids and distinct_worker_spans:
                origin = next(iter(origin_trace_ids))
                worker_trace_ids = {trace_id for trace_id, _span in distinct_worker_spans}
                related = [
                    link
                    for link in summary.get("async_links", [])
                    if link.get("origin_trace_id") == origin and link.get("related_trace_id") in worker_trace_ids
                ]
                if not related:
                    lookup_issues.append("trace_correlation_mismatch")
            evidence["lookup"] = summary
            evidence["pipeline_spans"] = [_safe_span_summary(trace_id, span) for trace_id, span in submit_spans + distinct_worker_spans]
            evidence["trace_ids"] = summary.get("trace_ids", [])
            issues.extend(lookup_issues)
        ids["status_request_id"] = poll_request_ids[-1] if poll_request_ids else ""
        ids = {key: value for key, value in ids.items() if value}
        code = _primary_issue(issues)
        self._add_check("pipeline", "pass" if not issues else "fail", code, ids=ids, attempts=attempts, evidence=evidence)
        self._add_check("pipeline_native_link", "manual", "native_link_console_check_required", ids=ids, attempts=["manual_console_verification_required"], evidence={"verification": "manual_console_check_required"})

    def run(self, *, enable_pipeline: bool = False) -> dict[str, Any]:
        started_at = self.now_fn().astimezone(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")
        self._run_rest_404()
        project_id, query_error = self._query_project()
        self._run_mcp_error(project_id, query_error)
        self._run_query(project_id, query_error)
        self._run_pipeline(enable_pipeline)
        counts = {name: sum(1 for row in self.checks if row["status"] == name) for name in ("pass", "fail", "skip", "manual")}
        counts["checks"] = len(self.checks)
        failed = counts["fail"] > 0
        report = {
            "schema": "lwc-request-id-probe-v1",
            "environment": "development",
            "project": DEV_GCP_PROJECT,
            "base_host": urllib.parse.urlsplit(DEV_BFF_URL).hostname,
            "source_sha": _source_sha(),
            "started_at": started_at,
            "finished_at": self.now_fn().astimezone(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z"),
            "status": "fail" if failed else "pass_with_manual_or_skipped_checks" if counts["manual"] or counts["skip"] else "pass",
            "counts": counts,
            "checks": self.checks,
            "lookup_attempt_history": self.lookup_attempt_history,
            "live_execution": "attempted",
            "native_link_verification": "manual_console_check_required",
        }
        return report

    def fail_preflight(self, codes: list[str] | str, *, enable_pipeline: bool = False) -> dict[str, Any]:
        """Return an explicit nonzero result without making product calls."""
        raw_codes = [codes] if isinstance(codes, str) else codes
        safe_codes = sorted({_safe_code(code, "preflight_failed") for code in raw_codes}) or ["preflight_failed"]
        safe = safe_codes[0]
        started_at = self.now_fn().astimezone(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")
        self._add_check("preflight", "fail", safe, attempts=["not_started"], evidence={"blockers": safe_codes})
        for name in ("rest_404", "mcp_tool_error", "query_fixture"):
            self._add_check(name, "fail", safe, attempts=["not_started"], evidence={"live": "not_run", "blockers": safe_codes})
        if enable_pipeline:
            pipeline_code = "pipeline_identity_missing" if "pipeline_identity_missing" in safe_codes else safe
            self._add_check("pipeline", "fail", pipeline_code, attempts=["not_started"], evidence={"live": "not_run", "blockers": safe_codes})
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
        else:
            self._add_check("pipeline", "skip", "pipeline_opt_in_disabled", attempts=["not_started"])
            self._add_check("pipeline_native_link", "manual", "pipeline_not_run", attempts=["not_started"])
        counts = {name: sum(1 for row in self.checks if row["status"] == name) for name in ("pass", "fail", "skip", "manual")}
        counts["checks"] = len(self.checks)
        return {
            "schema": "lwc-request-id-probe-v1",
            "environment": "development",
            "project": DEV_GCP_PROJECT,
            "base_host": urllib.parse.urlsplit(DEV_BFF_URL).hostname,
            "source_sha": _source_sha(),
            "started_at": started_at,
            "finished_at": self.now_fn().astimezone(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z"),
            "status": "fail",
            "counts": counts,
            "checks": self.checks,
            "lookup_attempt_history": [],
            "live_execution": "not_run",
            "native_link_verification": "manual_console_check_required",
        }


def _is_request_id(value: str) -> bool:
    try:
        trace_lookup.validate_request_id(value)
        return True
    except (ValueError, TypeError):
        return False


def _safe_execution_id(value: Any) -> str | None:
    if not isinstance(value, str):
        return None
    short = value.strip().rsplit("/", 1)[-1]
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,127}", short):
        return None
    return short


def _source_sha() -> str | None:
    try:
        completed = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            check=False,
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    value = completed.stdout.strip()
    return value if completed.returncode == 0 and re.fullmatch(r"[0-9a-f]{40}", value) else None


def live_lookup(project: str = DEV_GCP_PROJECT) -> Callable[[str], dict[str, Any]]:
    client = trace_lookup.GoogleApiClient(
        project,
        lambda: trace_lookup.gcloud_access_token(timeout=HTTP_TIMEOUT),
        timeout=HTTP_TIMEOUT,
    )

    def lookup(request_id: str) -> dict[str, Any]:
        now = dt.datetime.now(dt.timezone.utc)
        since = (now - dt.timedelta(minutes=15)).isoformat(timespec="seconds").replace("+00:00", "Z")
        until = (now + dt.timedelta(minutes=1)).isoformat(timespec="seconds").replace("+00:00", "Z")
        return trace_lookup.lookup_request(
            request_id,
            project,
            since,
            until,
            client,
            environment="development",
            max_log_entries=5000,
            max_pages=5,
            max_traces=25,
            max_spans=5000,
            page_size=1000,
            retries=1,
            retry_delay=0,
        )

    return lookup


def verify_cloud_read_access(project: str = DEV_GCP_PROJECT) -> str | None:
    """Check Logs and Trace read access before issuing any product request."""
    client = trace_lookup.GoogleApiClient(
        project,
        lambda: trace_lookup.gcloud_access_token(timeout=HTTP_TIMEOUT),
        timeout=HTTP_TIMEOUT,
    )
    now = dt.datetime.now(dt.timezone.utc)
    since = (now - dt.timedelta(minutes=15)).isoformat(timespec="seconds").replace("+00:00", "Z")
    until = (now + dt.timedelta(minutes=1)).isoformat(timespec="seconds").replace("+00:00", "Z")
    try:
        log_check = trace_lookup.lookup_request(
            str(uuid.uuid4()),
            project,
            since,
            until,
            client,
            environment="development",
            max_log_entries=2,
            max_pages=1,
            max_traces=1,
            max_spans=2,
            page_size=2,
            retries=1,
            retry_delay=0,
        )
        if log_check.get("status") not in {"found", "not_found"} or not log_check.get("complete"):
            codes = [
                issue.get("code")
                for issue in log_check.get("issues", [])
                if isinstance(issue, dict) and issue.get("code") in trace_lookup.SAFE_ERROR_CATEGORIES
            ]
            return codes[0] if codes else "lookup_incomplete"
        try:
            client.get_trace(uuid.uuid4().hex)
        except trace_lookup.ProviderError as exc:
            if exc.category != "not_found":
                return exc.category
    except trace_lookup.ProviderError as exc:
        return exc.category
    except (OSError, TimeoutError):
        return "network_error"
    return None


def write_report(report: dict[str, Any], output: Path | None) -> None:
    encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if output is not None:
        try:
            output.parent.mkdir(parents=True, exist_ok=True)
            temporary = output.with_name(output.name + ".tmp")
            temporary.write_text(encoded, encoding="utf-8")
            temporary.replace(output)
        except OSError:
            report["status"] = "fail"
            report["counts"]["fail"] += 1
            report["counts"]["checks"] += 1
            report["checks"].append({"name": "artifact_write", "status": "fail", "code": "artifact_write_failed"})
            encoded = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    sys.stdout.write(encoded)


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Verify DEV request-ID terminal logs and Cloud Trace with the shared read-only lookup core."
    )
    parser.add_argument("--enable-pipeline", action="store_true", help="Opt in to one run in the statically allowlisted isolated QA project.")
    parser.add_argument("--artifact", type=Path, help="Write the same safe JSON report to this path.")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        fixture = load_json(FIXTURE_PATH)
        prerequisites = load_json(DEV_PREREQUISITES_PATH)
        client = SafeHTTPClient()
        config = config_from_environment()
        runner = ProbeRunner(
            config,
            client,
            live_lookup(),
            fixture,
            prerequisites,
        )
        blockers = runner.preflight_blockers(enable_pipeline=args.enable_pipeline)
        if not blockers:
            cloud_error = verify_cloud_read_access()
            if cloud_error:
                blockers.append(cloud_error)
        report = runner.fail_preflight(blockers, enable_pipeline=args.enable_pipeline) if blockers else runner.run(enable_pipeline=args.enable_pipeline)
    except ProbeError as exc:
        report = {
            "schema": "lwc-request-id-probe-v1",
            "environment": "development",
            "project": DEV_GCP_PROJECT,
            "base_host": urllib.parse.urlsplit(DEV_BFF_URL).hostname,
            "source_sha": _source_sha(),
            "status": "fail",
            "counts": {"checks": 6, "pass": 0, "fail": 4, "skip": 1, "manual": 1},
            "checks": [
                {"name": "preflight", "status": "fail", "code": exc.code, "attempts": ["not_started"]},
                *[
                    {"name": name, "status": "fail", "code": exc.code, "attempts": ["not_started"], "evidence": {"live": "not_run"}}
                    for name in ("rest_404", "mcp_tool_error", "query_fixture")
                ],
                {"name": "pipeline", "status": "skip", "code": "pipeline_opt_in_disabled", "attempts": ["not_started"]},
                {"name": "pipeline_native_link", "status": "manual", "code": "pipeline_not_run", "attempts": ["not_started"]},
            ],
            "lookup_attempt_history": [],
            "live_execution": "not_run",
            "native_link_verification": "manual_console_check_required",
        }
    write_report(report, args.artifact)
    return 2 if report.get("counts", {}).get("fail", 0) else 0


if __name__ == "__main__":
    raise SystemExit(main())
