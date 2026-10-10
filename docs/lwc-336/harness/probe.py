#!/usr/bin/env python3
"""Exercise the real BFF MCP route through Hermes with disposable local keys."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import os
import pwd
import re
import subprocess
import sys
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen


OWNED_SCRATCH_ROOT = Path("/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch")
KEY_PATTERN = re.compile(r"^lwc_pk_[0-9a-f]{32}\.[A-Za-z0-9_-]{43}$")
SENSITIVE_VALUES: set[str] = set()


def is_inside(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def validate_isolation(home: Path, hermes_home: Path, scratch: Path) -> None:
    owned = OWNED_SCRATCH_ROOT.resolve(strict=True)
    if scratch.parent != owned or not scratch.name.startswith("lwc336."):
        raise ValueError("scratch must be a direct lwc336 child of the owned scratch root")
    if not is_inside(home, scratch) or not is_inside(hermes_home, scratch):
        raise ValueError("HOME and HERMES_HOME must remain in run-owned scratch")
    native_home = Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()
    if home == native_home or hermes_home == native_home / ".hermes":
        raise ValueError("probe selected a native Hermes profile")


def request(url: str, method: str, *, token: str | None = None, body: dict | None = None,
            protocol: str | None = None, session: bool = False,
            project_id: str | None = None) -> tuple[int, bytes, dict[str, str]]:
    headers = {"Accept": "application/json, text/event-stream"}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body, separators=(",", ":")).encode()
    if token is not None:
        headers["Authorization"] = "Bearer " + token
    if protocol:
        headers["Mcp-Protocol-Version"] = protocol
    if session:
        headers["Mcp-Session-Id"] = "lwc336-transport-probe"
    if project_id:
        headers["X-Project-ID"] = project_id
    req = Request(url, data=data, headers=headers, method=method)
    try:
        with urlopen(req, timeout=8) as response:
            return response.status, response.read(), {key.lower(): value for key, value in response.headers.items()}
    except HTTPError as error:
        return error.code, error.read(), {key.lower(): value for key, value in error.headers.items()}


def rpc(method: str, request_id: int, params: dict | None = None) -> dict:
    message = {"jsonrpc": "2.0", "id": request_id, "method": method}
    if params is not None:
        message["params"] = params
    return message


def decode_dispatch(value):
    return json.loads(value) if isinstance(value, str) else value


def query_response_content(result: dict) -> dict:
    if {"query", "mode", "results"}.issubset(result):
        return result
    value = result.get("structuredContent")
    if isinstance(value, str):
        value = json.loads(value)
    if value is None:
        content = result.get("content", [])
        if isinstance(content, list) and content and isinstance(content[0], dict):
            text = content[0].get("text")
            if isinstance(text, str):
                value = json.loads(text)
    if not isinstance(value, dict):
        nested = result.get("result")
        nested_type = type(nested).__name__ if nested is not None else "missing"
        nested_fields = sorted(nested) if isinstance(nested, dict) else []
        raise AssertionError(f"Hermes returned no QueryResponse object; fields={sorted(result)} nested={nested_type}:{nested_fields}")
    return value


def call_summary(label: str, arguments: dict, result: dict) -> dict:
    content = result.get("content", [])
    text = ""
    if isinstance(content, list) and content and isinstance(content[0], dict):
        text = str(content[0].get("text", ""))
    is_error = result.get("isError") is True or bool(result.get("error"))
    value = result if {"query", "mode", "results"}.issubset(result) else result.get("structuredContent")
    if isinstance(value, str):
        value = json.loads(value)
    if value is None and not is_error and text:
        try:
            value = json.loads(text)
        except json.JSONDecodeError:
            pass
    return {
        "case": label,
        "arguments": arguments,
        "is_error": is_error,
        "structured_fields": sorted(value) if isinstance(value, dict) else [],
        "result_count": len(value.get("results", [])) if isinstance(value, dict) else None,
        "citation_count": len(value.get("citations", [])) if isinstance(value, dict) and isinstance(value.get("citations"), list) else None,
        "disclosure_required": value.get("disclosure_required") if isinstance(value, dict) else None,
        "safe_error_text": str(result.get("error") or text) if is_error else None,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--repo-root", required=True, type=Path)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--scratch-root", required=True, type=Path)
    parser.add_argument("--emulator-host", required=True)
    args = parser.parse_args()

    source = args.source.resolve(strict=True)
    repo_root = args.repo_root.resolve(strict=True)
    binary = args.binary.resolve(strict=True)
    evidence_dir = args.evidence_dir.resolve()
    scratch = args.scratch_root.resolve(strict=True)
    home = Path(os.environ["HOME"]).resolve(strict=True)
    hermes_home = Path(os.environ["HERMES_HOME"]).resolve(strict=True)
    validate_isolation(home, hermes_home, scratch)
    evidence_dir.mkdir(parents=True, exist_ok=True)

    env = os.environ.copy()
    env["LWC336_HERMES_HARNESS"] = "1"
    env["FIRESTORE_EMULATOR_HOST"] = args.emulator_host
    server = subprocess.Popen(
        [str(binary), "-test.run", "^TestLWC336HermesServe$", "-test.v"],
        cwd=repo_root / "apps/bff", env=env, stdin=subprocess.PIPE,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1,
    )
    ready = None
    lifecycle_module = None
    lifecycle_shutdown = False
    try:
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            line = server.stdout.readline()
            if not line:
                if server.poll() is not None:
                    raise RuntimeError(f"product harness exited before readiness ({server.returncode})")
                continue
            if line.startswith("LWC336_READY "):
                ready = json.loads(line[len("LWC336_READY "):])
                break
        if ready is None:
            raise TimeoutError("product router did not publish its loopback endpoint")
        key = ready["key"]
        second_key = ready["second_key"]
        web_token = ready["web_token"]
        SENSITIVE_VALUES.update({key, second_key, web_token})
        if not KEY_PATTERN.fullmatch(key) or not KEY_PATTERN.fullmatch(second_key):
            raise AssertionError("product create API returned a non-canonical project key")

        sys.path.insert(0, str(source))
        import logging
        logging.basicConfig(stream=open(os.devnull, "w", encoding="utf-8"), level=logging.CRITICAL)

        from agent.secret_scope import reset_secret_scope, set_secret_scope
        from tools import mcp_tool_lifecycle as lifecycle_module
        from tools.mcp_tool_common import mcp_field
        from tools.mcp_tool_config import _interpolate_env_vars
        from tools.mcp_tool_discovery import _get_connected_server_for_call, register_mcp_servers
        from tools.registry import registry

        scope_token = set_secret_scope({"LWC336_PROJECT_KEY": key}, profile_home=str(hermes_home))
        try:
            mcp_config = _interpolate_env_vars({
                "url": ready["url"],
                "transport": "streamable-http",
                "headers": {"Authorization": "Bearer ${LWC336_PROJECT_KEY}"},
                "sampling": {"enabled": False},
                "skip_preflight": True,
                "connect_timeout": 8,
                "tool_timeout": 8,
            })
            if mcp_config["headers"].get("Authorization") != "Bearer " + key:
                raise AssertionError("Hermes secret reference did not resolve to the in-memory test key")
            registered = register_mcp_servers({"project-key": mcp_config})
        finally:
            reset_secret_scope(scope_token)

        tool_names = registry.get_tool_names_for_toolset("mcp-project-key")
        if len(tool_names) != 1 or tool_names[0] not in registered:
            raise AssertionError("Hermes did not register exactly one query_project tool")
        tool_name = tool_names[0]
        schema = registry.get_schema(tool_name) or {}
        parameters = schema.get("parameters", {}) if isinstance(schema, dict) else {}
        properties = parameters.get("properties", {}) if isinstance(parameters, dict) else {}
        if (set(properties) != {"q", "mode"} or "q" not in parameters.get("required", [])
                or parameters.get("additionalProperties") is not False):
            raise AssertionError("Hermes schema is not the fixed q/mode Query-only contract")

        native_server = _get_connected_server_for_call("project-key")
        if native_server is None or native_server.session is None:
            raise AssertionError("Hermes did not retain its native MCP client session")
        protocol_version = mcp_field(native_server.initialize_result, "protocol_version", "protocolVersion")
        if not protocol_version:
            raise AssertionError("Hermes did not report its negotiated MCP protocol version")

        lifecycle = []
        for method, body in (
            ("POST", rpc("initialize", 80, {
                "protocolVersion": "2025-11-25", "capabilities": {},
                "clientInfo": {"name": "lwc336-transport-probe", "version": "1"},
            })),
            ("GET", None),
            ("DELETE", None),
        ):
            for auth_name, token in (("missing", None), ("wrong", "invalid-synthetic-token")):
                status, _, _ = request(ready["url"], method, token=token, body=body)
                if status != 401:
                    raise AssertionError(f"{method} did not reject {auth_name} bearer before MCP transport")
                lifecycle.append({"method": method, "auth": auth_name, "status": status})
        for method in ("GET", "DELETE"):
            status, _, _ = request(ready["url"], method, token=key, session=True, protocol=protocol_version)
            if status != 405:
                raise AssertionError(f"stateless SDK {method} status was {status}, expected 405")
            lifecycle.append({"method": method, "auth": "accepted", "status": status})

        init_status, init_body, init_headers = request(
            ready["url"], "POST", token=key,
            body=rpc("initialize", 90, {
                "protocolVersion": "2025-11-25", "capabilities": {},
                "clientInfo": {"name": "lwc336-wire-check", "version": "1"},
            }),
        )
        init_result = json.loads(init_body)
        if init_status != 200 or init_result.get("result", {}).get("protocolVersion") != protocol_version:
            raise AssertionError("BFF initialize negotiation did not match Hermes' negotiated version")
        lifecycle.append({
            "method": "POST", "auth": "accepted", "operation": "initialize",
            "status": init_status,
            "request_content_type": "application/json",
            "response_content_type": init_headers.get("content-type", ""),
            "protocol_version": protocol_version,
            "session_id_issued": bool(init_headers.get("mcp-session-id")),
        })

        list_status, list_body, _ = request(
            ready["url"], "POST", token=key,
            body=rpc("tools/list", 91, {}), protocol=protocol_version, session=True,
        )
        listed = json.loads(list_body)
        wire_tools = listed.get("result", {}).get("tools", [])
        if list_status != 200 or len(wire_tools) != 1 or wire_tools[0].get("name") != "query_project":
            raise AssertionError("BFF wire tools/list was not restricted to query_project")
        if wire_tools[0].get("annotations", {}).get("readOnlyHint") is not True:
            raise AssertionError("query_project did not declare its read-only annotation")

        wire_call_status, wire_call_body, _ = request(
            ready["url"], "POST", token=key,
            body=rpc("tools/call", 92, {"name": "query_project", "arguments": {"q": "wire-contract"}}),
            protocol=protocol_version, session=True,
        )
        wire_result = json.loads(wire_call_body).get("result", {})
        wire_structured = wire_result.get("structuredContent")
        wire_text_items = wire_result.get("content", [])
        wire_text = wire_text_items[0].get("text") if wire_text_items else None
        if (wire_call_status != 200 or not isinstance(wire_structured, dict)
                or not isinstance(wire_text, str) or json.loads(wire_text) != wire_structured):
            raise AssertionError("MCP wire omitted StructuredContent or changed the JSON text projection")

        def dispatch(label: str, arguments: dict) -> tuple[dict, dict]:
            raw = registry.dispatch(tool_name, arguments)
            value = decode_dispatch(raw)
            if isinstance(value, dict) and "result" in value:
                nested = value["result"]
                if hasattr(nested, "model_dump"):
                    nested = nested.model_dump(by_alias=True)
                if isinstance(nested, str):
                    try:
                        nested = json.loads(nested)
                    except json.JSONDecodeError:
                        pass
                if isinstance(nested, dict):
                    value = nested
            if not isinstance(value, dict):
                raise AssertionError(f"Hermes returned an unexpected result type for {label}")
            return value, call_summary(label, arguments, value)

        def mcp_observation() -> dict:
            observation_url = ready["url"].replace("/mcp", "/__lwc336_harness/mcp-observations")
            status, body, _ = request(observation_url, "GET")
            value = json.loads(body)
            if status != 200 or not isinstance(value, dict):
                raise AssertionError("test-only MCP observation endpoint did not return JSON")
            if not {"count", "method", "status"}.issubset(value):
                raise AssertionError("test-only MCP observation omitted sanitized request fields")
            return value

        cases = [
            ("wiki", {"q": "wiki-normal", "mode": "wiki"}),
            ("full", {"q": "full-normal", "mode": "full"}),
            ("default-wiki", {"q": "default-mode"}),
            ("empty-normal", {"q": "empty"}),
            ("insufficient-normal", {"q": "insufficient", "mode": "full"}),
            ("model-prior", {"q": "model-prior", "mode": "full"}),
        ]
        calls = {}
        summaries = []
        for label, arguments in cases:
            result, summary = dispatch(label, arguments)
            calls[label] = result
            summaries.append(summary)

        for label, arguments in (
            ("executor-error", {"q": "executor-failure", "mode": "full"}),
            ("whitespace-query", {"q": "   "}),
            ("invalid-mode", {"q": "wiki-normal", "mode": "invalid"}),
        ):
            result, summary = dispatch(label, arguments)
            calls[label] = result
            summaries.append(summary)
            recovery_label = f"recovery-after-{label}"
            recovery, recovery_summary = dispatch(recovery_label, {"q": recovery_label})
            if recovery_summary["is_error"] or query_response_content(recovery).get("query") != recovery_label:
                raise AssertionError(f"Hermes did not recover after the isolated {label} error")
            summaries.append(recovery_summary)

        parity = []
        for label, arguments in cases[:4] + cases[5:6]:
            mode = arguments.get("mode", "wiki")
            http_body = {"q": arguments["q"], "mode": mode}
            status, body, _ = request(
                ready["url"].replace("/mcp", "/api/v1/query"), "POST", token=key, body=http_body,
            )
            if status != 200:
                raise AssertionError(f"existing HTTP Query returned {status} for {label}")
            http_response = json.loads(body)
            mcp_status, mcp_body, _ = request(
                ready["url"], "POST", token=key,
                body=rpc("tools/call", 100 + len(parity), {
                    "name": "query_project", "arguments": arguments,
                }), protocol=protocol_version, session=True,
            )
            mcp_result = json.loads(mcp_body).get("result", {})
            mcp_structured = mcp_result.get("structuredContent")
            text_items = mcp_result.get("content", [])
            text_response = json.loads(text_items[0].get("text", "{}")) if text_items else None
            if mcp_status != 200 or mcp_structured != http_response or text_response != http_response:
                differing = sorted(key for key in set(http_response) | set(mcp_structured or {})
                                   if http_response.get(key) != (mcp_structured or {}).get(key))
                raise AssertionError(f"HTTP/MCP wire QueryResponse diverged for {label}; differing_fields={differing}")
            parity.append({"case": label, "same_structured_json": True, "same_text_json": True})

        insufficient = query_response_content(calls["insufficient-normal"])
        if insufficient.get("status") != "insufficient_evidence" or calls["insufficient-normal"].get("isError") is True:
            raise AssertionError("domain-level insufficient evidence was marked as a tool error")

        prior = query_response_content(calls["model-prior"])
        if (prior.get("answer_basis") != "model_prior" or prior.get("wiki_evidence_status") != "no_relevant_evidence"
                or prior.get("disclosure_required") is not True or prior.get("citations") != []):
            raise AssertionError("model-prior disclosure or explicit empty citations were lost")
        if calls["model-prior"].get("isError") is True:
            raise AssertionError("domain-level model-prior result was marked as an MCP tool error")
        if query_response_content(calls["default-wiki"]).get("mode") != "wiki":
            raise AssertionError("omitted mode did not default to wiki")

        executor_failure = calls["executor-error"]
        failure_summary = call_summary("executor-error", {"q": "executor-failure", "mode": "full"}, executor_failure)
        safe_failure = failure_summary.get("safe_error_text") or ""
        failure_status, failure_body, _ = request(
            ready["url"], "POST", token=key,
            body=rpc("tools/call", 97, {
                "name": "query_project", "arguments": {"q": "executor-failure", "mode": "full"},
            }), protocol=protocol_version, session=True,
        )
        failure_wire = json.loads(failure_body).get("result", {})
        failure_wire_text = failure_wire.get("content", [{}])[0].get("text", "")
        safe_runtime_failure = "The query could not run with the current configuration. Try again later."
        if (failure_status != 200 or failure_wire.get("isError") is not True
                or failure_wire_text != safe_runtime_failure):
            raise AssertionError("executor failure did not become a safe MCP isError text result")
        if safe_failure != safe_runtime_failure:
            raise AssertionError(f"Hermes did not retain safe tool error text; fields={sorted(executor_failure)}")
        if "credential" in safe_failure.lower() or key in json.dumps(executor_failure):
            raise AssertionError("executor error leaked an internal detail or key")
        http_error_status, http_error_body, _ = request(
            ready["url"].replace("/mcp", "/api/v1/query"), "POST", token=key,
            body={"q": "executor-failure", "mode": "full"},
        )
        if (http_error_status != 500 or
                json.loads(http_error_body).get("error") != safe_failure):
            raise AssertionError("MCP and HTTP executor failures no longer share safe mapping")

        cross_key_status, cross_key_body, _ = request(
            ready["url"], "POST", token=second_key, body=rpc("tools/call", 95, {
                "name": "query_project", "arguments": {"q": "scope-check", "mode": "wiki"},
            }), protocol=protocol_version, session=True,
        )
        cross_key = json.loads(cross_key_body).get("result", {})
        cross_text = json.loads(cross_key.get("content", [{}])[0].get("text", "{}"))
        cross_title = cross_text.get("results", [{}])[0].get("title", "")
        if cross_key_status != 200 or "archive" not in cross_title or "research" in cross_title:
            raise AssertionError("session header reused a prior key's project scope")

        wrong_project_status, _, _ = request(
            ready["url"], "POST", token=key, body=rpc("tools/call", 96, {
                "name": "query_project", "arguments": {"q": "scope-check", "mode": "wiki"},
            }), protocol=protocol_version, session=True, project_id=ready["second_project_id"],
        )
        if wrong_project_status != 403:
            raise AssertionError("mismatched X-Project-ID was not rejected")

        before_pre_revoke = mcp_observation()
        pre_revoke_args = {"q": "pre-revoke-health"}
        pre_revoke_result, pre_revoke_summary = dispatch("pre-revoke-existing-client", pre_revoke_args)
        pre_revoke_response = query_response_content(pre_revoke_result)
        after_pre_revoke = mcp_observation()
        if (pre_revoke_summary["is_error"] or pre_revoke_response.get("query") != pre_revoke_args["q"]
                or after_pre_revoke["count"] <= before_pre_revoke["count"]
                or after_pre_revoke["method"] != "POST" or after_pre_revoke["status"] != 200):
            raise AssertionError("same native Hermes client did not complete a successful pre-revoke BFF request")
        summaries.append(pre_revoke_summary)

        revoke_status, _, _ = request(
            ready["url"].replace("/mcp", f"/api/v1/projects/{ready['project_id']}/keys/{ready['key_id']}/revoke"),
            "POST", token=web_token, body={},
        )
        if revoke_status != 200:
            raise AssertionError("owner could not revoke the synthetic key through the production router")
        before_revoked_call = mcp_observation()
        revoked_result, revoked_summary = dispatch("revoked-existing-client", {"q": "wiki-normal"})
        revoked_text = str(revoked_result.get("error") or "")
        if not revoked_result.get("error") and revoked_result.get("isError") is not True:
            raise AssertionError("Hermes kept using a revoked project key")
        after_revoked_call = mcp_observation()
        if (after_revoked_call["count"] <= before_revoked_call["count"]
                or after_revoked_call["method"] != "POST" or after_revoked_call["status"] != 401):
            raise AssertionError("revoked-key call did not reach the BFF and receive HTTP 401")
        if "last 3 calls" in revoked_text or "Paused for" in revoked_text:
            raise AssertionError("Hermes circuit breaker short-circuited the revoked-key call locally")
        if key in json.dumps(revoked_result) or web_token in json.dumps(revoked_result):
            raise AssertionError("revocation error exposed the test credential")

        lifecycle_module.shutdown_mcp_servers()
        lifecycle_shutdown = True

        evidence = {
            "probe": {
                "status": "passed",
                "source_workspace_head": subprocess.check_output(["git", "-C", str(repo_root), "rev-parse", "HEAD"], text=True).strip(),
                "hermes_source": str(source),
                "hermes_mcp_sdk": importlib.metadata.version("mcp"),
                "go_mcp_sdk": "github.com/modelcontextprotocol/go-sdk/mcp v1.8.0",
                "transport": "Streamable HTTP, JSON responses, stateless server",
                "negotiated_protocol_version": protocol_version,
                "tool_names": [tool_name],
                "tool_schema_properties": sorted(properties),
                "read_only_hint": True,
                "sampling_enabled": False,
                "model_calls": 0,
                "local_firestore_emulator": args.emulator_host,
                "source_manifest_digest": "5cf4f58953c88e6cddc76a70421e9ae1cf65f1ac867368d7154eb2b8208c9364",
                "ac3_cross_key_same_session_header_current_project": True,
                "ac3_mismatched_project_header_status": wrong_project_status,
                "ac3_pre_revoke_same_client": {
                    "result": "success",
                    "mcp_http_method": after_pre_revoke["method"],
                    "mcp_http_status": after_pre_revoke["status"],
                },
                "ac3_existing_client_after_revoke": {
                    "result": "server-rejected",
                    "request_evidence": {
                        "mcp_request_count_before": before_revoked_call["count"],
                        "mcp_request_count_after": after_revoked_call["count"],
                        "method": after_revoked_call["method"],
                        "status": after_revoked_call["status"],
                    },
                },
                "http_mcp_parity": parity,
                "http_lifecycle": lifecycle,
            },
            "calls": summaries + [revoked_summary],
            "commands": [
                {"operation": "actual production-router harness", "exit_code": 0},
                {"operation": "Hermes register_mcp_servers and registry.dispatch", "exit_code": 0},
                {"operation": "Hermes shutdown_mcp_servers", "exit_code": 0},
            ],
        }
        evidence_path = evidence_dir / "native-hermes-results.json"

        if server.stdin:
            server.stdin.close()
            server.stdin = None
        try:
            server.wait(timeout=15)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait(timeout=5)
            raise RuntimeError("product harness process needed forced cleanup")
        if server.returncode != 0:
            raise RuntimeError(f"product harness exited {server.returncode}")
        evidence_text = json.dumps(evidence, indent=2, ensure_ascii=False) + "\n"
        if key in evidence_text or second_key in evidence_text or web_token in evidence_text:
            raise AssertionError("a synthetic credential leaked into the saved evidence")
        evidence_path.write_text(evidence_text, encoding="utf-8")
        print(json.dumps({
            "status": "passed", "protocol_version": protocol_version,
            "hermes_mcp_sdk": evidence["probe"]["hermes_mcp_sdk"],
            "call_cases": [row["case"] for row in evidence["calls"]],
            "http_mcp_parity_cases": [row["case"] for row in parity],
            "evidence_path": str(evidence_path),
        }, ensure_ascii=False))
        return 0
    finally:
        if lifecycle_module is not None and not lifecycle_shutdown:
            try:
                lifecycle_module.shutdown_mcp_servers()
            except Exception:
                pass
        if server.poll() is None:
            if server.stdin:
                server.stdin.close()
                server.stdin = None
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=5)
        if server.stdout:
            server.stdout.close()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        message = str(exc)
        for value in SENSITIVE_VALUES:
            message = message.replace(value, "[redacted]")
        print(f"native Hermes probe failed: {type(exc).__name__}: {message}", file=sys.stderr)
        raise SystemExit(1)
