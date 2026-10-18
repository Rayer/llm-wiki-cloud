#!/usr/bin/env python3
"""Read-only request ID to Cloud Logging and Cloud Trace lookup.

The functions `request_log_filter`, `collect_trace_ids`, and `lookup_request`
are the shared query core for offline tests and the later DEV probe. They do not
perform product operations; providers are injected at the `lookup_request`
boundary.
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
from pathlib import Path
from typing import Any, Callable


DEFAULT_DEV_PROJECT = "llm-wiki-cloud"
DEFAULT_LOOKBACK = dt.timedelta(minutes=30)
DEFAULT_LOG_LIMIT = 10_000
DEFAULT_PAGE_LIMIT = 1_000
DEFAULT_TRACE_LIMIT = 250
DEFAULT_SPAN_LIMIT = 10_000
MAX_TREE_DEPTH = 128
DEFAULT_RETRIES = 3
DEFAULT_TIMEOUT = 10.0
MAX_WINDOW = dt.timedelta(days=31)

REQUEST_ID_FIELDS = (
    "request_id",
    "request.id",
    "request.origin_id",
    "origin_request_id",
    "origin_request.id",
    "envelope_request_id",
)
LOG_CORRELATION_FIELDS = (*REQUEST_ID_FIELDS, "diagnostic_id")
TERMINAL_MESSAGES = frozenset(
    {
        "http.request.terminal",
        "mcp.request.terminal",
        "query.terminal",
        "pipeline.worker.terminal",
    }
)
TEXT_SUMMARY_FIELDS = (
    "status_code",
    "request_status",
    "phase",
    "outcome",
    "business_outcome",
    "transport",
    "operation",
    "service",
    "revision",
    "mcp_is_error",
)
TRACE_ID_RE = re.compile(r"^[0-9a-fA-F]{32}$")
SPAN_ID_RE = re.compile(r"^(?:[0-9a-fA-F]{16}|[0-9]{1,20})$")
PROJECT_RE = re.compile(r"^[a-z][a-z0-9-]{4,28}[a-z0-9]$")
SAFE_LABEL_RE = re.compile(r"^[A-Za-z0-9_.-]{1,128}$")
SAFE_OPERATION_RE = re.compile(r"^[A-Za-z][A-Za-z0-9_.:-]{0,127}$")
MCP_OPERATIONS = frozenset(
    {
        "initialize",
        "ping",
        "tools/list",
        "tools/call",
        "resources/list",
        "resources/read",
        "prompts/list",
        "prompts/get",
        "notifications/initialized",
        "notifications/cancelled",
        "notifications/progress",
        "notifications/message",
        "other",
    }
)
TEXT_FIELD_RE = re.compile(
    r"(?<![A-Za-z0-9_.-])(?P<key>request_id|request\.id|request\.origin_id|origin_request_id|origin_request\.id|envelope_request_id|diagnostic_id|trace_id|span_id|attempt_id|attempt\.id|status_code|request_status|phase|outcome|business_outcome|transport|operation|service|revision|mcp_is_error)="
    r"(?P<value>\"[^\"]*\"|'[^']*'|[^\s,]+)"
)
FINITE_OUTCOMES = frozenset(
    {
        "success",
        "failure",
        "rejected",
        "protocol_error",
        "error",
        "timeout",
        "canceled",
        "cancelled",
        "accepted",
        "pending",
        "skipped",
        "unknown",
        "degraded",
        "committed",
        "not_committed",
        "not_applicable",
        "no_op",
        "confirmation_required",
        "existing",
        "waiting_manual",
        "superseded",
        "not_active",
        "precondition_failed",
        "transfer_failure",
        "response_failure",
        "cache_hit",
        "cache_miss",
        "placeholder",
        "redirected",
        "no_evidence",
        "partial",
    }
)
SAFE_ERROR_CATEGORIES = frozenset(
    {
        "authentication_required", "authentication_timeout", "authentication_unavailable",
        "gcloud_unavailable", "permission_denied", "not_found", "rate_limited", "timeout",
        "network_error", "service_unavailable", "invalid_response", "api_error",
        "output_write_failed",
    }
)
OUTCOME_LABELS = (
    "request.outcome",
    "auth.outcome",
    "query.outcome",
    "bff.outcome",
    "pipeline.outcome",
    "profile.outcome",
    "export.publication.outcome",
    "export.job.outcome",
    "export.phase.outcome",
    "query.business_outcome",
)
OPERATION_LABELS = (
    "mcp.operation",
    "auth.operation",
    "bff.operation",
    "query.stage",
    "pipeline.operation",
    "profile.operation",
    "export.operation",
)


class ProviderError(Exception):
    """A safe API error category; provider response bodies are never retained."""

    def __init__(self, category: str):
        safe_category = category if category in SAFE_ERROR_CATEGORIES else "api_error"
        super().__init__(safe_category)
        self.category = safe_category


def validate_request_id(value: str) -> str:
    try:
        parsed = uuid.UUID(value)
    except (ValueError, AttributeError, TypeError) as exc:
        raise ValueError("request_id_invalid") from exc
    normalized = str(parsed)
    if normalized == "00000000-0000-0000-0000-000000000000":
        raise ValueError("request_id_invalid")
    if value.lower() != normalized:
        raise ValueError("request_id_invalid")
    return normalized


def validate_project(value: str) -> str:
    if not PROJECT_RE.fullmatch(value):
        raise ValueError("project_invalid")
    return value


def parse_time(value: str) -> dt.datetime:
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("time_invalid") from exc
    if parsed.tzinfo is None:
        raise ValueError("time_timezone_required")
    return parsed.astimezone(dt.timezone.utc)


def format_time(value: dt.datetime) -> str:
    return value.astimezone(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def time_window(since: str | None, until: str | None, now: dt.datetime | None = None) -> tuple[str, str]:
    current = now or dt.datetime.now(dt.timezone.utc)
    current = current.astimezone(dt.timezone.utc)
    if since and until:
        start, end = parse_time(since), parse_time(until)
    elif since:
        start, end = parse_time(since), current
    elif until:
        end = parse_time(until)
        start = end - DEFAULT_LOOKBACK
    else:
        end, start = current, current - DEFAULT_LOOKBACK
    if start >= end or end - start > MAX_WINDOW:
        raise ValueError("time_window_invalid")
    return format_time(start), format_time(end)


def request_log_filter(request_id: str, since: str, until: str) -> str:
    request_id = validate_request_id(request_id)
    timestamp_filter = f"timestamp >= {json.dumps(since)} AND timestamp <= {json.dumps(until)}"
    terms = [
        f"jsonPayload.{field}={json.dumps(request_id)}"
        if "." not in field
        else f"jsonPayload.{json.dumps(field)}={json.dumps(request_id)}"
        for field in LOG_CORRELATION_FIELDS
    ]
    # stdouttrace v1.44.0 serializes SpanStub.Attributes as Key/Value rows.
    # Cloud Logging indexes the typed string at this nested field path.
    terms.append(f"jsonPayload.Attributes.Value.Value={json.dumps(request_id)}")
    terms.append(f"textPayload:{json.dumps(request_id)}")
    return f"{timestamp_filter} AND ({' OR '.join(terms)})"


def _safe_trace_id(value: Any) -> str | None:
    if not isinstance(value, str):
        return None
    value = value.rsplit("/", 1)[-1]
    if not TRACE_ID_RE.fullmatch(value) or set(value) == {"0"}:
        return None
    return value.lower()


def _safe_span_id(value: Any) -> str | None:
    if isinstance(value, int) and not isinstance(value, bool):
        value = str(value)
    if not isinstance(value, str) or not SPAN_ID_RE.fullmatch(value):
        return None
    if value.strip("0") == "":
        return None
    return value.lower() if re.fullmatch(r"[0-9a-fA-F]{16}", value) else value


def _scalar(value: Any) -> str | int | bool | None:
    if isinstance(value, (str, int, bool)):
        return value
    if isinstance(value, dict):
        for key in ("stringValue", "intValue", "boolValue", "value"):
            if key in value:
                child = value[key]
                if isinstance(child, dict) and "value" in child:
                    child = child["value"]
                if isinstance(child, (str, int, bool)):
                    return child
    return None


SDK_LOG_FIELDS = frozenset(
    {
        *LOG_CORRELATION_FIELDS,
        "trace_id",
        "span_id",
        "attempt_id",
        "attempt.id",
        *TEXT_SUMMARY_FIELDS,
        *OUTCOME_LABELS,
        *OPERATION_LABELS,
        "service.name",
        "service.revision",
    }
)


def _sdk_attribute_value(value: Any) -> str | int | bool | None:
    if isinstance(value, dict) and value.get("Type") in ("STRING", "INT64", "BOOL"):
        value = value.get("Value")
    return _scalar(value)


def _sdk_key_values(value: Any) -> dict[str, str]:
    if not isinstance(value, list):
        return {}
    fields: dict[str, str] = {}
    for item in value:
        if not isinstance(item, dict):
            continue
        key, raw_value = item.get("Key"), item.get("Value")
        if not isinstance(key, str) or key not in SDK_LOG_FIELDS:
            continue
        parsed = _sdk_attribute_value(raw_value)
        if isinstance(parsed, (str, int)):
            fields[str(key)] = str(parsed)
    return fields


def _sdk_span_stub_fields(payload: dict[str, Any]) -> tuple[dict[str, str], list[str]]:
    """Read only allowlisted fields from the pinned stdouttrace SpanStub shape."""
    fields = _sdk_key_values(payload.get("Attributes"))
    fields.update(
        {
            key: value
            for key, value in _sdk_key_values(payload.get("Resource")).items()
            if key in ("service.name", "service.revision") and key not in fields
        }
    )
    span_context = payload.get("SpanContext")
    if isinstance(span_context, dict):
        trace_id = _safe_trace_id(span_context.get("TraceID"))
        span_id = _safe_span_id(span_context.get("SpanID"))
        if trace_id:
            fields.setdefault("trace_id", trace_id)
        if span_id:
            fields.setdefault("span_id", span_id)
    linked_trace_ids: list[str] = []
    links = payload.get("Links")
    if isinstance(links, list):
        for link in links:
            if not isinstance(link, dict) or not isinstance(link.get("SpanContext"), dict):
                continue
            trace_id = _safe_trace_id(link["SpanContext"].get("TraceID"))
            if trace_id:
                linked_trace_ids.append(trace_id)
    return fields, linked_trace_ids


def _text_fields(text: str) -> dict[str, str]:
    try:
        parsed = json.loads(text)
    except (ValueError, TypeError):
        parsed = None
    if isinstance(parsed, dict):
        fields: dict[str, str] = {}
        for key in (*LOG_CORRELATION_FIELDS, "trace_id", "span_id", "attempt_id", "attempt.id", *TEXT_SUMMARY_FIELDS, "msg", "message"):
            value = parsed.get(key)
            if isinstance(value, (str, int)):
                fields[key] = str(value)
        for key, value in _sdk_span_stub_fields(parsed)[0].items():
            fields.setdefault(key, value)
        return fields
    fields = {}
    for match in TEXT_FIELD_RE.finditer(text):
        value = match.group("value")
        if value[:1] in ('"', "'") and value[-1:] == value[:1]:
            value = value[1:-1]
        fields[match.group("key")] = value
    if "http.request.terminal" in text:
        fields["msg"] = "http.request.terminal"
    return fields


def _entry_fields(entry: dict[str, Any]) -> tuple[dict[str, str], list[str], str | None, str | None, list[str]]:
    payload = entry.get("jsonPayload")
    if not isinstance(payload, dict):
        payload = {}
    fields: dict[str, str] = {}
    for key in (*LOG_CORRELATION_FIELDS, "trace_id", "span_id", "attempt_id", "attempt.id", *TEXT_SUMMARY_FIELDS, "msg", "message"):
        value = payload.get(key)
        if isinstance(value, (str, int)):
            fields[key] = str(value)
    sdk_fields, linked_trace_ids = _sdk_span_stub_fields(payload)
    for key, value in sdk_fields.items():
        fields.setdefault(key, value)
    text = entry.get("textPayload")
    if isinstance(text, str):
        for key, value in _text_fields(text).items():
            fields.setdefault(key, value)
        try:
            text_payload = json.loads(text)
        except (ValueError, TypeError):
            text_payload = None
        if isinstance(text_payload, dict):
            _text_sdk_fields, text_linked_trace_ids = _sdk_span_stub_fields(text_payload)
            linked_trace_ids.extend(text_linked_trace_ids)
    matched = [key for key in LOG_CORRELATION_FIELDS if fields.get(key) == entry.get("_target_request_id")]
    trace_id = _safe_trace_id(entry.get("trace")) or _safe_trace_id(fields.get("trace_id"))
    span_id = _safe_span_id(entry.get("spanId")) or _safe_span_id(fields.get("span_id"))
    message = fields.get("msg", fields.get("message"))
    return fields, matched, trace_id, span_id, sorted(set(linked_trace_ids))


def _entry_identity(entry: dict[str, Any], trace_id: str | None, span_id: str | None) -> tuple[str, ...]:
    insert_id = entry.get("insertId")
    log_name = entry.get("logName")
    if isinstance(insert_id, str) and insert_id:
        return ("insert", str(log_name or ""), insert_id)
    timestamp = entry.get("timestamp")
    payload = entry.get("jsonPayload") if isinstance(entry.get("jsonPayload"), dict) else {}
    message = payload.get("msg", payload.get("message", ""))
    return ("fallback", str(log_name or ""), str(timestamp or ""), trace_id or "", span_id or "", str(message))


def _terminal_log_summary(entry: dict[str, Any], fields: dict[str, str], matched: list[str], target: str) -> dict[str, Any] | None:
    message = fields.get("msg", fields.get("message"))
    if message not in TERMINAL_MESSAGES:
        return None
    trace_id = _safe_trace_id(entry.get("trace")) or _safe_trace_id(fields.get("trace_id"))
    span_id = _safe_span_id(entry.get("spanId")) or _safe_span_id(fields.get("span_id"))
    summary: dict[str, Any] = {
        "message": message,
        "correlation_fields": sorted(matched),
        "correlation_id": target,
        "trace_id": trace_id,
        "span_id": span_id,
    }
    outcome = fields.get("outcome")
    if outcome in FINITE_OUTCOMES:
        summary["outcome"] = outcome
    for name in ("phase", "transport"):
        value = fields.get(name)
        if value and SAFE_LABEL_RE.fullmatch(value):
            summary[name] = value
    operation = fields.get("operation")
    if operation and (SAFE_OPERATION_RE.fullmatch(operation) or operation in MCP_OPERATIONS) and not re.search(r"[0-9a-fA-F]{8}-[0-9a-fA-F-]{27}", operation):
        summary["operation"] = operation
    for name in ("service", "revision"):
        value = fields.get(name)
        if value and SAFE_LABEL_RE.fullmatch(value):
            summary[name] = value
    status_value = fields.get("status_code") or fields.get("request_status")
    try:
        status_code = int(status_value) if status_value is not None else None
    except ValueError:
        status_code = None
    if status_code is not None and 100 <= status_code <= 599:
        summary["status_code"] = status_code
    tool_error = fields.get("mcp_is_error")
    if tool_error is not None and tool_error.casefold() in ("true", "false"):
        summary["mcp_is_error"] = tool_error.casefold() == "true"
    attempt = fields.get("attempt_id") or fields.get("attempt.id")
    if attempt and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", attempt):
        summary["attempt_id"] = attempt
    resource = entry.get("resource") if isinstance(entry.get("resource"), dict) else {}
    resource_labels = resource.get("labels") if isinstance(resource.get("labels"), dict) else {}
    if "service" not in summary:
        service = resource_labels.get("service_name") or resource_labels.get("service.name")
        if isinstance(service, str) and SAFE_LABEL_RE.fullmatch(service):
            summary["service"] = service
    if "revision" not in summary:
        revision = resource_labels.get("revision_name") or resource_labels.get("service_revision") or resource_labels.get("revision")
        if isinstance(revision, str) and SAFE_LABEL_RE.fullmatch(revision):
            summary["revision"] = revision
    return summary


def collect_trace_ids(entries: list[dict[str, Any]], request_id: str) -> dict[str, Any]:
    request_id = validate_request_id(request_id)
    traces: dict[str, dict[str, Any]] = {}
    terminal_logs: list[dict[str, Any]] = []
    terminal_trace_ids: set[str] = set()
    matched_count = 0
    duplicate_count = 0
    missing_trace_count = 0
    seen: set[tuple[str, ...]] = set()
    for raw in entries:
        if not isinstance(raw, dict):
            continue
        entry = dict(raw)
        entry["_target_request_id"] = request_id
        fields, matched, trace_id, _span_id, linked_trace_ids = _entry_fields(entry)
        if not matched:
            continue
        identity = _entry_identity(entry, trace_id, _span_id)
        if identity in seen:
            duplicate_count += 1
            continue
        seen.add(identity)
        matched_count += 1
        terminal = _terminal_log_summary(entry, fields, matched, request_id)
        if terminal is not None:
            terminal_logs.append(terminal)
        if not trace_id:
            missing_trace_count += 1
            continue
        row = traces.setdefault(
            trace_id,
            {"evidence": set(), "services": set(), "revisions": set(), "origin_log": False, "attempt_ids": set(), "linked_trace_ids": set()},
        )
        row["evidence"].update(matched)
        row["linked_trace_ids"].update(linked_trace_ids)
        resource = entry.get("resource") if isinstance(entry.get("resource"), dict) else {}
        labels = resource.get("labels") if isinstance(resource.get("labels"), dict) else {}
        service = fields.get("service") or fields.get("service.name") or labels.get("service_name") or labels.get("service.name")
        revision = fields.get("revision") or fields.get("service.revision") or labels.get("revision_name") or labels.get("service_revision") or labels.get("revision")
        if isinstance(service, str) and SAFE_LABEL_RE.fullmatch(service):
            row["services"].add(service)
        if isinstance(revision, str) and SAFE_LABEL_RE.fullmatch(revision):
            row["revisions"].add(revision)
        if fields.get("msg") == "http.request.terminal":
            terminal_trace_ids.add(trace_id)
            row["origin_log"] = True
        attempt_id = fields.get("attempt_id") or fields.get("attempt.id")
        if isinstance(attempt_id, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", attempt_id):
            row["attempt_ids"].add(attempt_id)
    return {
        "traces": traces,
        "origin_trace_ids": terminal_trace_ids,
        "matched_log_count": matched_count,
        "duplicate_log_count": duplicate_count,
        "missing_trace_log_count": missing_trace_count,
        "terminal_logs": terminal_logs,
    }


class GoogleApiClient:
    """Small stdlib-only read adapter; no API response body is emitted or saved."""

    def __init__(
        self,
        project: str,
        token_provider: Callable[[], str],
        timeout: float = DEFAULT_TIMEOUT,
        opener: Callable[..., Any] = urllib.request.urlopen,
    ):
        self.project = validate_project(project)
        self.token_provider = token_provider
        self.timeout = timeout
        self.opener = opener
        self._token: str | None = None

    def _access_token(self) -> str:
        if self._token is None:
            self._token = self.token_provider()
        return self._token

    def _request(self, url: str, method: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
        payload = json.dumps(body).encode("utf-8") if body is not None else None
        request = urllib.request.Request(
            url,
            data=payload,
            method=method,
            headers={
                "Authorization": f"Bearer {self._access_token()}",
                "Accept": "application/json",
                "Content-Type": "application/json",
            },
        )
        try:
            with self.opener(request, timeout=self.timeout) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            raise ProviderError(classify_http_status(exc.code)) from None
        except (TimeoutError, urllib.error.URLError) as exc:
            reason = getattr(exc, "reason", exc)
            category = "timeout" if isinstance(reason, (TimeoutError,)) else "network_error"
            raise ProviderError(category) from None
        except OSError:
            raise ProviderError("network_error") from None
        try:
            result = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError):
            raise ProviderError("invalid_response") from None
        if not isinstance(result, dict):
            raise ProviderError("invalid_response")
        return result

    def list_log_entries(self, filter_text: str, page_token: str | None, page_size: int) -> dict[str, Any]:
        body: dict[str, Any] = {
            "resourceNames": [f"projects/{self.project}"],
            "filter": filter_text,
            "orderBy": "timestamp asc",
            "pageSize": page_size,
        }
        if page_token:
            body["pageToken"] = page_token
        return self._request("https://logging.googleapis.com/v2/entries:list", "POST", body)

    def get_trace(self, trace_id: str) -> dict[str, Any]:
        url = "https://cloudtrace.googleapis.com/v1/projects/" + urllib.parse.quote(self.project, safe="") + "/traces/" + trace_id
        return self._request(url, "GET")


def classify_http_status(status: int) -> str:
    if status == 401:
        return "authentication_required"
    if status == 403:
        return "permission_denied"
    if status == 404:
        return "not_found"
    if status == 429:
        return "rate_limited"
    if status in (408, 504):
        return "timeout"
    if status >= 500:
        return "service_unavailable"
    return "api_error"


def gcloud_access_token(gcloud: str = "gcloud", timeout: float = DEFAULT_TIMEOUT) -> str:
    try:
        completed = subprocess.run(
            [gcloud, "auth", "print-access-token", "--quiet"],
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError:
        raise ProviderError("gcloud_unavailable") from None
    except subprocess.TimeoutExpired:
        raise ProviderError("authentication_timeout") from None
    except OSError:
        raise ProviderError("authentication_unavailable") from None
    token = completed.stdout.strip()
    if completed.returncode != 0 or not token or any(char.isspace() for char in token):
        raise ProviderError("authentication_required")
    return token


def _entry_search(
    provider: Any,
    filter_text: str,
    request_id: str,
    max_entries: int,
    max_pages: int,
    page_size: int,
) -> dict[str, Any]:
    entries: list[dict[str, Any]] = []
    page_token: str | None = None
    seen_tokens: set[str] = set()
    attempts: list[str] = []
    pages = 0
    enumeration_complete = False
    truncated = False
    error: str | None = None
    while True:
        if pages >= max_pages:
            truncated, error = True, "page_limit"
            break
        if page_token and page_token in seen_tokens:
            truncated, error = True, "pagination_cycle"
            break
        if page_token:
            seen_tokens.add(page_token)
        try:
            response = provider.list_log_entries(filter_text, page_token, page_size)
        except ProviderError as exc:
            attempts.append(exc.category)
            error = exc.category
            break
        pages += 1
        attempts.append("page_read")
        batch = response.get("entries", [])
        if not isinstance(batch, list):
            error = "invalid_response"
            break
        for entry in batch:
            if not isinstance(entry, dict):
                continue
            if len(entries) >= max_entries:
                truncated, error = True, "entry_limit"
                break
            probe = dict(entry)
            probe["_target_request_id"] = request_id
            if _entry_fields(probe)[1]:
                entries.append(entry)
        if truncated:
            break
        next_token = response.get("nextPageToken")
        if not next_token:
            enumeration_complete = True
            break
        if not isinstance(next_token, str):
            error = "invalid_response"
            break
        page_token = next_token
    return {
        "entries": entries,
        "pages": pages,
        "attempts": attempts,
        "enumeration_complete": enumeration_complete,
        "truncated": truncated,
        "error": error,
    }


def _label_value(labels: Any, name: str) -> str | int | bool | None:
    if not isinstance(labels, dict):
        return None
    variants = (
        name,
        "/" + name,
        name.replace(".", "/"),
        "/" + name.replace(".", "/"),
    )
    for key in variants:
        if key in labels:
            return _scalar(labels[key])
    return None


def _safe_attribute_text(value: Any, pattern: re.Pattern[str]) -> str | None:
    value = _scalar(value)
    if not isinstance(value, str) or not pattern.fullmatch(value):
        return None
    return value


def _span_request_evidence(span: dict[str, Any], request_id: str) -> str | None:
    labels = span.get("labels")
    for key in ("request.id", "request_id", "request.origin_id", "origin_request_id"):
        value = _label_value(labels, key)
        if isinstance(value, str):
            try:
                if validate_request_id(value) == request_id:
                    return key
            except ValueError:
                continue
    return None


def _outcome(span: dict[str, Any]) -> tuple[str | None, str]:
    labels = span.get("labels")
    for key in OUTCOME_LABELS:
        value = _label_value(labels, key)
        if value is None:
            continue
        if isinstance(value, str) and value in FINITE_OUTCOMES:
            return value, "present"
        return None, "unrecognized"
    return None, "missing"


def _operation(span: dict[str, Any]) -> str | None:
    labels = span.get("labels")
    for key in OPERATION_LABELS:
        raw = _label_value(labels, key)
        value = _safe_attribute_text(raw, SAFE_OPERATION_RE)
        if value or (key == "mcp.operation" and isinstance(raw, str) and raw in MCP_OPERATIONS):
            return value or str(raw)
    return None


def _service_revision(span: dict[str, Any], log_hints: dict[str, Any]) -> tuple[str | None, str | None]:
    labels = span.get("labels")
    service = None
    for key in ("service.name", "service_name", "cloud.service"):
        service = _safe_attribute_text(_label_value(labels, key), SAFE_LABEL_RE)
        if service:
            break
    revision = None
    for key in ("service.revision", "revision", "revision_name"):
        revision = _safe_attribute_text(_label_value(labels, key), SAFE_LABEL_RE)
        if revision:
            break
    if not service and len(log_hints.get("services", ())) == 1:
        service = next(iter(log_hints["services"]))
    if not revision and len(log_hints.get("revisions", ())) == 1:
        revision = next(iter(log_hints["revisions"]))
    return service, revision


def _cycle_nodes(parent_ids: dict[str, str | None]) -> set[str]:
    cycles: set[str] = set()
    complete: set[str] = set()
    for start in parent_ids:
        path: list[str] = []
        positions: dict[str, int] = {}
        current: str | None = start
        while current is not None and current in parent_ids and current not in complete:
            if current in positions:
                cycles.update(path[positions[current] :])
                break
            positions[current] = len(path)
            path.append(current)
            current = parent_ids[current]
        complete.update(path)
    return cycles


def build_span_tree(
    raw_spans: list[dict[str, Any]],
    request_id: str,
    log_hints: dict[str, Any] | None = None,
    max_spans: int = DEFAULT_SPAN_LIMIT,
) -> dict[str, Any]:
    hints = log_hints or {}
    source_count = len(raw_spans)
    bounded = source_count > max_spans
    selected = raw_spans[:max_spans]
    nodes: dict[str, dict[str, Any]] = {}
    duplicate_ids = 0
    invalid_span_ids = 0
    for span in selected:
        if not isinstance(span, dict):
            continue
        span_id = _safe_span_id(span.get("spanId"))
        if not span_id:
            invalid_span_ids += 1
            continue
        if span_id in nodes:
            duplicate_ids += 1
            continue
        name = span.get("name")
        safe_name = _safe_attribute_text(name, SAFE_OPERATION_RE)
        if safe_name and re.search(r"[0-9a-fA-F]{8}-[0-9a-fA-F-]{27}", safe_name):
            safe_name = None
        outcome, outcome_status = _outcome(span)
        service, revision = _service_revision(span, hints)
        request_evidence = _span_request_evidence(span, request_id)
        direct_request_match = request_evidence in ("request.id", "request_id")
        origin_request_match = request_evidence in ("request.origin_id", "origin_request_id")
        node = {
            "span_id": span_id,
            "parent_span_id": _safe_span_id(span.get("parentSpanId")),
            "parent_present": span.get("parentSpanId") not in (None, "", 0, "0"),
            "parent_status": "unclassified",
            "span": safe_name,
            "service": service,
            "revision": revision,
            "operation": _operation(span),
            "outcome": outcome,
            "outcome_status": outcome_status,
            "request_id_match": direct_request_match,
            "origin_request_id_match": origin_request_match,
            "attempt_id": None,
            "children": [],
        }
        attempt = _label_value(span.get("labels"), "attempt.id") or _label_value(span.get("labels"), "attempt_id")
        if isinstance(attempt, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", attempt):
            node["attempt_id"] = attempt
        nodes[span_id] = node

    parent_ids = {span_id: node["parent_span_id"] for span_id, node in nodes.items()}
    cycle_edges = _cycle_nodes(parent_ids)
    for span_id, node in nodes.items():
        parent_id = node["parent_span_id"]
        if node["parent_present"] and not parent_id:
            node["parent_status"] = "invalid_parent_id"
        elif not node["parent_present"]:
            node["parent_status"] = "root"
        elif span_id in cycle_edges:
            node["parent_status"] = "cycle"
        elif parent_id not in nodes:
            node["parent_status"] = "bounded_parent_missing" if bounded else "missing_parent"
        else:
            node["parent_status"] = "connected"
            nodes[parent_id]["children"].append(span_id)

    roots = [
        node
        for span_id, node in nodes.items()
        if node["parent_status"] in ("root", "cycle", "missing_parent", "bounded_parent_missing", "invalid_parent_id")
    ]
    for node in nodes.values():
        node["children"].sort()

    def tree_node(span_id: str) -> dict[str, Any]:
        node = nodes[span_id]
        return {key: value for key, value in node.items() if key != "children"} | {"children": []}

    tree: list[dict[str, Any]] = []
    pending: list[tuple[str, dict[str, Any], int]] = []
    for root in roots:
        item = tree_node(root["span_id"])
        tree.append(item)
        pending.append((root["span_id"], item, 1))
    depth_truncated = False
    omitted_tree_edges = 0
    while pending:
        span_id, item, depth = pending.pop()
        children = nodes[span_id]["children"]
        if depth >= MAX_TREE_DEPTH and children:
            item["children_truncated"] = True
            depth_truncated = True
            omitted_tree_edges += len(children)
            continue
        for child_id in children:
            child = tree_node(child_id)
            item["children"].append(child)
            pending.append((child_id, child, depth + 1))
    parent_issues = sum(
        node["parent_status"] in ("missing_parent", "bounded_parent_missing", "invalid_parent_id", "cycle")
        for node in nodes.values()
    )
    missing_outcomes = sum(node["outcome_status"] == "missing" for node in nodes.values())
    unrecognized_outcomes = sum(node["outcome_status"] == "unrecognized" for node in nodes.values())
    return {
        "spans": list(nodes.values()),
        "tree": tree,
        "source_span_count": source_count,
        "span_count": len(nodes),
        "truncated": bounded,
        "tree_depth_truncated": depth_truncated,
        "tree_depth_limit": MAX_TREE_DEPTH,
        "omitted_tree_edges": omitted_tree_edges,
        "duplicate_span_ids": duplicate_ids,
        "invalid_span_ids": invalid_span_ids,
        "parent_issues": parent_issues,
        "missing_outcomes": missing_outcomes,
        "unrecognized_outcomes": unrecognized_outcomes,
        "request_id_span_count": sum(1 for node in nodes.values() if node["request_id_match"]),
        "origin_request_id_span_count": sum(1 for node in nodes.values() if node["origin_request_id_match"]),
        "attempt_ids": sorted({node["attempt_id"] for node in nodes.values() if node["attempt_id"]}),
    }


def _trace_attempt(provider: Any, trace_id: str, retries: int, retry_delay: float, sleep_fn: Callable[[float], None]) -> tuple[dict[str, Any] | None, list[str], str | None]:
    attempts: list[str] = []
    for attempt in range(1, retries + 1):
        try:
            trace = provider.get_trace(trace_id)
            if not isinstance(trace, dict):
                raise ProviderError("invalid_response")
            attempts.append("trace_read")
            spans = trace.get("spans")
            if not isinstance(spans, list):
                raise ProviderError("invalid_response")
            if not spans:
                attempts[-1] = "empty_trace"
                if attempt < retries:
                    sleep_fn(retry_delay * (2 ** (attempt - 1)))
                    continue
            return trace, attempts, None
        except ProviderError as exc:
            attempts.append(exc.category)
            if exc.category not in ("not_found", "rate_limited", "timeout", "service_unavailable") or attempt >= retries:
                return None, attempts, exc.category
            sleep_fn(retry_delay * (2 ** (attempt - 1)))
    return None, attempts, "not_found"


def lookup_request(
    request_id: str,
    project: str,
    since: str,
    until: str,
    provider: Any,
    *,
    environment: str = "development",
    max_log_entries: int = DEFAULT_LOG_LIMIT,
    max_pages: int = DEFAULT_PAGE_LIMIT,
    max_traces: int = DEFAULT_TRACE_LIMIT,
    max_spans: int = DEFAULT_SPAN_LIMIT,
    page_size: int = 1000,
    retries: int = DEFAULT_RETRIES,
    retry_delay: float = 1.0,
    sleep_fn: Callable[[float], None] = time.sleep,
) -> dict[str, Any]:
    request_id = validate_request_id(request_id)
    project = validate_project(project)
    if environment not in ("development", "production"):
        raise ValueError("environment_invalid")
    since, until = time_window(since, until)
    if page_size <= 0 or retries <= 0 or min(max_log_entries, max_pages, max_traces, max_spans) <= 0:
        raise ValueError("limit_invalid")
    if retry_delay < 0:
        raise ValueError("timing_invalid")
    filter_text = request_log_filter(request_id, since, until)
    issues: list[dict[str, str]] = []
    entries_result: dict[str, Any] = {"entries": [], "pages": 0, "attempts": [], "enumeration_complete": False, "truncated": False, "error": None}
    last_error: str | None = None
    empty_attempts: list[str] = []
    best_partial: dict[str, Any] | None = None
    for attempt in range(1, retries + 1):
        entries_result = _entry_search(provider, filter_text, request_id, max_log_entries, max_pages, page_size)
        if entries_result["entries"]:
            best_partial = entries_result
        if entries_result["error"]:
            last_error = entries_result["error"]
            empty_attempts.append(last_error)
            if last_error not in ("rate_limited", "timeout", "service_unavailable") or attempt >= retries:
                break
            sleep_fn(retry_delay * (2 ** (attempt - 1)))
            continue
        last_error = None
        if entries_result["entries"] or entries_result["truncated"] or not entries_result["enumeration_complete"]:
            empty_attempts.append("entries_found" if entries_result["entries"] else "incomplete")
            break
        empty_attempts.append("no_data")
        if attempt < retries:
            sleep_fn(retry_delay * (2 ** (attempt - 1)))
    if best_partial and not entries_result["entries"]:
        entries_result = dict(best_partial)
        entries_result["enumeration_complete"] = False
        entries_result["truncated"] = True
        entries_result["error"] = last_error or "enumeration_incomplete"
    collected = collect_trace_ids(entries_result["entries"], request_id)
    trace_rows = collected["traces"]
    trace_ids = sorted(trace_rows)
    trace_overflow = len(trace_ids) > max_traces
    if trace_overflow:
        trace_ids = trace_ids[:max_traces]
        issues.append({"scope": "traces", "code": "trace_limit"})
    traces_out: list[dict[str, Any]] = []
    origin_trace_ids = collected["origin_trace_ids"]
    for trace_id in trace_ids:
        hints = trace_rows[trace_id]
        trace_data, attempts, trace_error = _trace_attempt(provider, trace_id, retries, retry_delay, sleep_fn)
        console_url = f"https://console.cloud.google.com/traces/list?project={urllib.parse.quote(project, safe='')}&tid={trace_id}"
        if trace_error:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": trace_error})
            traces_out.append(
                {
                    "trace_id": trace_id,
                    "console_url": console_url,
                    "status": trace_error,
                    "attempts": attempts,
                    "correlation_evidence": sorted(hints["evidence"]),
                    "services": sorted(hints["services"]),
                    "revisions": sorted(hints["revisions"]),
                    "origin_log": hints["origin_log"],
                    "spans": [],
                    "tree": [],
                }
            )
            continue
        raw_spans = trace_data["spans"]
        tree_data = build_span_tree(raw_spans, request_id, hints, max_spans)
        if not tree_data["request_id_span_count"] and not tree_data["origin_request_id_span_count"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "request_id_attribute_missing"})
        if tree_data["parent_issues"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "span_parent_issue"})
        if tree_data["missing_outcomes"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "span_outcome_missing"})
        if tree_data["unrecognized_outcomes"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "span_outcome_unrecognized"})
        if tree_data["truncated"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "span_limit"})
        if tree_data["tree_depth_truncated"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "span_tree_depth_limit"})
        if tree_data["duplicate_span_ids"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "duplicate_span_ids"})
        if tree_data["invalid_span_ids"]:
            issues.append({"scope": "trace", "trace_id": trace_id, "code": "invalid_span_ids"})
        traces_out.append(
            {
                "trace_id": trace_id,
                "console_url": console_url,
                "status": "available" if not (tree_data["truncated"] or tree_data["tree_depth_truncated"]) else "truncated",
                "attempts": attempts,
                "correlation_evidence": sorted(hints["evidence"])
                + (["span.request.id"] if tree_data["request_id_span_count"] else [])
                + (["span.origin_request.id"] if tree_data["origin_request_id_span_count"] else []),
                "services": sorted(hints["services"]),
                "revisions": sorted(hints["revisions"]),
                "origin_log": hints["origin_log"],
                "linked_trace_ids": sorted(hints["linked_trace_ids"]),
                "spans": tree_data["spans"],
                "tree": tree_data["tree"],
                "span_count": tree_data["span_count"],
                "source_span_count": tree_data["source_span_count"],
                "duplicate_span_ids": tree_data["duplicate_span_ids"],
                "invalid_span_ids": tree_data["invalid_span_ids"],
                "tree_depth_truncated": tree_data["tree_depth_truncated"],
                "tree_depth_limit": tree_data["tree_depth_limit"],
                "omitted_tree_edges": tree_data["omitted_tree_edges"],
                "parent_issues": tree_data["parent_issues"],
                "missing_outcomes": tree_data["missing_outcomes"],
                "unrecognized_outcomes": tree_data["unrecognized_outcomes"],
                "request_id_span_count": tree_data["request_id_span_count"],
                "origin_request_id_span_count": tree_data["origin_request_id_span_count"],
                "attempt_ids": sorted(set(tree_data["attempt_ids"]) | hints["attempt_ids"]),
            }
        )

    async_links: list[dict[str, Any]] = []
    for origin_trace_id in sorted(origin_trace_ids):
        for related in traces_out:
            if related["trace_id"] == origin_trace_id or related["status"] not in ("available", "truncated"):
                continue
            linked_origin = origin_trace_id in related.get("linked_trace_ids", [])
            origin_attribute = bool(
                {"span.origin_request.id", "request.origin_id", "origin_request_id", "origin_request.id"}
                & set(related.get("correlation_evidence", []))
            )
            if not (linked_origin or origin_attribute):
                continue
            async_links.append(
                {
                    "origin_trace_id": origin_trace_id,
                    "related_trace_id": related["trace_id"],
                    "attempt_ids": related.get("attempt_ids", []),
                    "relationship": "separate_trace_with_stdouttrace_link" if linked_origin else "separate_trace_with_origin_request_id_evidence",
                    "evidence": "stdouttrace_span_link" if linked_origin else "origin_request_id_attribute",
                    "console_url": related["console_url"],
                    "native_link_verification": "manual_console_check_required",
                }
            )

    background_expected = any(
        row.get("message") == "http.request.terminal" and row.get("status_code") == 202
        for row in collected["terminal_logs"]
    )
    background_observed = bool(async_links)
    if background_expected and not background_observed:
        issues.append({"scope": "async", "code": "background_trace_evidence_missing"})

    if entries_result["truncated"] or not entries_result["enumeration_complete"]:
        issues.append({"scope": "logs", "code": entries_result["error"] or "enumeration_incomplete"})
    if last_error and not entries_result["entries"]:
        issues.append({"scope": "logs", "code": last_error})
    if collected["missing_trace_log_count"]:
        issues.append({"scope": "logs", "code": "trace_id_missing_from_matched_log"})
    enumeration_complete = entries_result["enumeration_complete"] and not entries_result["truncated"]
    complete = enumeration_complete and not issues
    status = "found" if traces_out and complete else "incomplete" if traces_out or issues else "not_found"
    return {
        "schema": "lwc-request-id-trace-lookup-v1",
        "status": status,
        "complete": complete,
        "request_id": request_id,
        "environment": environment,
        "project": project,
        "window": {"since": since, "until": until},
        "log_search": {
            "status": "found" if collected["matched_log_count"] else "not_found" if enumeration_complete else "incomplete",
            "matched_entries": collected["matched_log_count"],
            "duplicate_entries": collected["duplicate_log_count"],
            "pages": entries_result["pages"],
            "attempts": empty_attempts or entries_result["attempts"],
            "enumeration_complete": enumeration_complete,
            "truncated": entries_result["truncated"],
            "limit": max_log_entries,
            "page_limit": max_pages,
            "missing_trace_id_entries": collected["missing_trace_log_count"],
        },
        "trace_count": len(traces_out),
        "traces_truncated": trace_overflow,
        "terminal_logs": collected["terminal_logs"],
        "traces": traces_out,
        "async_links": async_links,
        "async_background": {
            "expected": background_expected,
            "status": "observed" if background_observed else "missing" if background_expected else "not_indicated",
            "related_trace_count": len(async_links),
        },
        "issues": issues,
        "native_link_verification": "manual_console_check_required",
    }


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Read-only Cloud Logging and Cloud Trace lookup from a server request ID."
    )
    parser.add_argument("request_id", help="server-issued request ID from a response/log")
    parser.add_argument("--environment", choices=("development", "production"), default="development")
    parser.add_argument("--project", help="Google Cloud project ID (DEV default: llm-wiki-cloud)")
    parser.add_argument("--since", help="RFC3339 lower time bound; defaults to 30 minutes before --until/now")
    parser.add_argument("--until", help="RFC3339 upper time bound; defaults to now")
    parser.add_argument("--json", action="store_true", help="write the safe allowlist report as JSON to stdout")
    parser.add_argument("--output", type=Path, help="write the same safe JSON report to a local file")
    parser.add_argument("--max-log-entries", type=int, default=DEFAULT_LOG_LIMIT)
    parser.add_argument("--max-pages", type=int, default=DEFAULT_PAGE_LIMIT)
    parser.add_argument("--max-traces", type=int, default=DEFAULT_TRACE_LIMIT)
    parser.add_argument("--max-spans", type=int, default=DEFAULT_SPAN_LIMIT)
    parser.add_argument("--attempts", type=int, default=DEFAULT_RETRIES, help="lookup/export attempts, including the first")
    parser.add_argument("--retry-delay-seconds", type=float, default=1.0)
    parser.add_argument("--timeout-seconds", type=float, default=DEFAULT_TIMEOUT)
    parser.add_argument("--gcloud", default="gcloud", help=argparse.SUPPRESS)
    return parser


def _safe_error_report(category: str, request_id: str | None, environment: str, project: str | None) -> dict[str, Any]:
    return {
        "schema": "lwc-request-id-trace-lookup-v1",
        "status": "error",
        "complete": False,
        "request_id": request_id,
        "environment": environment,
        "project": project,
        "issues": [{"scope": "lookup", "code": category}],
        "native_link_verification": "manual_console_check_required",
    }


def _emit_report(report: dict[str, Any], json_stdout: bool, output: Path | None) -> None:
    serialized = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    if output:
        try:
            output.write_text(serialized, encoding="utf-8")
        except OSError:
            raise ProviderError("output_write_failed") from None
        return
    if json_stdout:
        sys.stdout.write(serialized)
        return
    print(f"Request ID: {report.get('request_id') or '(invalid)'}")
    print(f"Environment/project: {report.get('environment')} / {report.get('project') or '(unset)'}")
    if "window" in report:
        print(f"Window: {report['window']['since']} to {report['window']['until']}")
    if "log_search" in report:
        logs = report["log_search"]
        print(
            f"Logs: {logs['status']}; {logs['matched_entries']} unique matched entries; "
            f"{logs['pages']} pages; enumeration {'complete' if logs['enumeration_complete'] else 'incomplete'}; "
            f"attempts={','.join(logs['attempts']) or 'none'}"
        )
    for trace in report.get("traces", []):
        print(f"Trace {trace['trace_id']}: {trace['status']} — {trace['console_url']}")
        _print_tree(trace.get("tree", []), 1)
    if report.get("async_links"):
        print("Separate-trace origin correlations (Console native-Link check is manual):")
        for relation in report["async_links"]:
            print(
                f"  {relation['origin_trace_id']} -> {relation['related_trace_id']} "
                f"[{relation['relationship']}] {relation['console_url']}"
            )
    if report.get("issues"):
        print("Issues: " + ", ".join(f"{item['scope']}:{item['code']}" for item in report["issues"]))
    print("Native Link verification: MANUAL Console check required.")
    print(f"Lookup status: {report['status']}; complete={str(report['complete']).lower()}")


def _print_tree(nodes: list[dict[str, Any]], depth: int) -> None:
    pending = [(node, depth) for node in reversed(nodes)]
    while pending:
        node, node_depth = pending.pop()
        labels = [node.get("span") or "(span name unavailable)"]
        if node.get("service"):
            labels.append(f"service={node['service']}")
        if node.get("revision"):
            labels.append(f"revision={node['revision']}")
        if node.get("operation"):
            labels.append(f"operation={node['operation']}")
        labels.append(f"outcome={node.get('outcome') or node.get('outcome_status')}")
        labels.append(f"parent={node.get('parent_status')}")
        if node.get("children_truncated"):
            labels.append("children=truncated_at_depth_limit")
        print("  " * node_depth + "- " + "; ".join(labels))
        pending.extend((child, node_depth + 1) for child in reversed(node.get("children", [])))


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        request_id = validate_request_id(args.request_id)
        if any(value <= 0 for value in (args.max_log_entries, args.max_pages, args.max_traces, args.max_spans, args.attempts)):
            raise ValueError("limit_invalid")
        if args.retry_delay_seconds < 0 or args.timeout_seconds <= 0:
            raise ValueError("timing_invalid")
        if args.json and args.output:
            raise ValueError("output_mode_conflict")
        if args.environment == "production" and not args.project:
            raise ValueError("project_required_for_environment")
        project = args.project or os.environ.get("LWC_DEV_GCP_PROJECT", DEFAULT_DEV_PROJECT)
        project = validate_project(project)
        since, until = time_window(args.since, args.until)
    except ValueError as exc:
        category = str(exc) if str(exc) in {
            "request_id_invalid", "project_invalid", "time_invalid", "time_timezone_required",
            "time_window_invalid", "limit_invalid", "timing_invalid", "output_mode_conflict",
            "project_required_for_environment",
        } else "input_invalid"
        report = _safe_error_report(category, None, args.environment, args.project)
        _emit_report(report, args.json, args.output)
        return 2

    try:
        client = GoogleApiClient(
            project,
            lambda: gcloud_access_token(args.gcloud, args.timeout_seconds),
            timeout=args.timeout_seconds,
        )
        report = lookup_request(
            request_id,
            project,
            since,
            until,
            client,
            environment=args.environment,
            max_log_entries=args.max_log_entries,
            max_pages=args.max_pages,
            max_traces=args.max_traces,
            max_spans=args.max_spans,
            retries=args.attempts,
            retry_delay=args.retry_delay_seconds,
        )
    except ProviderError as exc:
        report = _safe_error_report(exc.category, request_id, args.environment, project)
        _emit_report(report, args.json, args.output)
        return 3
    try:
        _emit_report(report, args.json, args.output)
    except ProviderError:
        print("error: output_write_failed", file=sys.stderr)
        return 3
    if report["status"] == "found" and report["complete"]:
        return 0
    if report["status"] == "not_found":
        return 1
    return 3


if __name__ == "__main__":
    raise SystemExit(main())
