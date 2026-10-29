#!/usr/bin/env python3
"""Exercise local GateAuth, LWC consent, and an actual Streamable HTTP MCP client."""

import argparse
import base64
import hashlib
import json
import os
import secrets
import stat
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def local_origin(raw):
    parsed = urllib.parse.urlsplit(raw)
    host = (parsed.hostname or "").lower()
    if parsed.scheme != "http" or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        raise RuntimeError("only a plain HTTP loopback origin is allowed")
    if host not in ("localhost", "127.0.0.1", "::1"):
        raise RuntimeError("only localhost numeric loopback origins are allowed")
    try:
        port = parsed.port
    except ValueError as exc:
        raise RuntimeError("invalid local loopback port") from exc
    if port is not None and not 1 <= port <= 65535:
        raise RuntimeError("invalid local loopback port")
    return raw.rstrip("/")


OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
AUDIT_HEADER = "# GAT4 Firestore RPC audit v1; collection names only"


def validate_auth_audit(raw):
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise RuntimeError("Auth Firestore audit is not UTF-8") from exc
    records = [line for line in text.splitlines() if line and not line.startswith("#")]
    if not records:
        raise RuntimeError("Auth Firestore audit captured no RPCs")
    expected_capture = False
    forbidden = False
    for line in records:
        fields = dict(part.split("=", 1) for part in line.split() if "=" in part)
        if not fields.get("rpc", "").startswith("/google.firestore.v1.Firestore/"):
            raise RuntimeError("Auth Firestore audit contains a malformed RPC record")
        collections = [name for name in fields.get("collections", "").split(",") if name]
        collections += [name for name in fields.get("forbidden", "").split(",") if name]
        if any(name == "projects" or "binding" in name.lower() for name in collections):
            forbidden = True
        if any(name in ("users", "local_scopes", "authority") or name.startswith(("auth_", "mcp_oauth")) for name in collections):
            expected_capture = True
    if not expected_capture:
        raise RuntimeError("Auth Firestore audit has no expected Auth collection capture")
    if forbidden:
        raise RuntimeError("Auth Firestore audit saw a forbidden Project or binding collection")
    return len(records)


def verify_auth_audit_negative_controls():
    for label, raw in (("blank", b""), ("header-only", (AUDIT_HEADER + "\n").encode("utf-8"))):
        try:
            validate_auth_audit(raw)
        except RuntimeError:
            continue
        raise RuntimeError(f"Auth Firestore audit accepted {label} negative control")


def request(method, url, body=None, bearer=None, form=False, timeout=30):
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        if form:
            data = urllib.parse.urlencode(body).encode("utf-8")
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            data = json.dumps(body, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
    if bearer:
        headers["Authorization"] = "Bearer " + bearer
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        response = OPENER.open(req, timeout=timeout)
    except urllib.error.HTTPError as exc:
        return exc.code, exc.headers, exc.read()
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise RuntimeError("local HTTP transport failed") from exc
    with response:
        return response.status, response.headers, response.read()


def json_request(method, url, expected, case, body=None, bearer=None, form=False):
    status, headers, raw = request(method, url, body, bearer, form)
    try:
        data = json.loads(raw) if raw else {}
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"{case}: expected JSON at HTTP {status}") from exc
    if status not in expected:
        raise RuntimeError(f"{case}: unexpected HTTP {status}")
    if not isinstance(data, dict):
        raise RuntimeError(f"{case}: response is not a JSON object")
    return status, headers, data


def firestore_project_document_url(origin, project, database, scope, owner, project_id):
    emulator = local_origin(origin)
    document_id = owner + "_" + project_id
    collection_path = ("local_scopes/" + urllib.parse.quote(scope, safe="") + "/projects/"
                       if scope else "projects/")
    return (emulator + "/v1/projects/" + urllib.parse.quote(project, safe="")
            + "/databases/" + urllib.parse.quote(database, safe="()") + "/documents/"
            + collection_path + urllib.parse.quote(document_id, safe=""))


def restore_project_acl(document_url, fields):
    status, _, _ = request("PATCH", document_url, {"fields": fields})
    if status != 200:
        raise RuntimeError("Firestore emulator could not restore the synthetic Project document")
    status, _, raw = request("GET", document_url)
    if status != 200:
        raise RuntimeError("Firestore emulator did not read back the restored Project document")
    try:
        restored = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("restored Project document is not valid Firestore JSON") from exc
    if not isinstance(restored, dict) or restored.get("fields") != fields:
        raise RuntimeError("restored Project document fields changed")


def remove_project_acl(origin, project, database, scope, owner, project_id):
    document_url = firestore_project_document_url(origin, project, database, scope, owner, project_id)
    status, _, raw = request("GET", document_url)
    if status != 200:
        raise RuntimeError("Firestore emulator could not read the synthetic Project document")
    try:
        document = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("synthetic Project document is not valid Firestore JSON") from exc
    fields = document.get("fields") if isinstance(document, dict) else None
    if (not isinstance(fields, dict)
            or fields.get("user_id", {}).get("stringValue") != owner
            or fields.get("project_id", {}).get("stringValue") != project_id):
        raise RuntimeError("Firestore document is not the expected synthetic owner/Project ACL")
    try:
        status, _, _ = request("DELETE", document_url)
        if status not in (200, 204):
            raise RuntimeError("Firestore emulator refused to remove the synthetic Project ACL")
        status, _, _ = request("GET", document_url)
        if status != 404:
            raise RuntimeError("Firestore emulator did not confirm Project ACL removal")
    except BaseException:
        restore_project_acl(document_url, fields)
        raise
    return document_url, fields


def save_secret(path, value):
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, stat.S_IRUSR | stat.S_IWUSR)
    os.fchmod(fd, stat.S_IRUSR | stat.S_IWUSR)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        stream.write(value + "\n")


def begin_authorization(auth_origin, client_id, redirect_uri, resource, scope):
    verifier = secrets.token_urlsafe(64)[:96]
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode("ascii")).digest()).rstrip(b"=").decode("ascii")
    state = secrets.token_urlsafe(24)
    query = urllib.parse.urlencode({
        "client_id": client_id,
        "redirect_uri": redirect_uri,
        "response_type": "code",
        "code_challenge": challenge,
        "code_challenge_method": "S256",
        "state": state,
        "resource": resource,
        "scope": scope,
    })
    status, headers, _ = request("GET", auth_origin + "/api/v1/auth/mcp/oauth/authorize?" + query)
    if status != 302 or "Location" not in headers:
        raise RuntimeError(f"authorize: expected HTTP 302, got {status}")
    location = urllib.parse.urlsplit(headers["Location"])
    transaction_id = urllib.parse.parse_qs(location.query).get("transaction_id", [""])[0]
    if not transaction_id:
        raise RuntimeError("authorize: redirect did not include a transaction id")
    return transaction_id, verifier, state


def parse_code(redirect_url, redirect_uri, state):
    parsed = urllib.parse.urlsplit(redirect_url)
    if parsed.scheme + "://" + parsed.netloc + parsed.path != redirect_uri:
        raise RuntimeError("consent decision: redirect URI changed")
    values = urllib.parse.parse_qs(parsed.query)
    if values.get("state", [None])[0] != state:
        raise RuntimeError("consent decision: OAuth state did not round-trip")
    if values.get("error"):
        raise RuntimeError("consent decision returned an OAuth error")
    code = values.get("code", [""])[0]
    if not code:
        raise RuntimeError("consent decision did not return an authorization code")
    return code


def exchange_code(auth_origin, client_id, code, redirect_uri, verifier, resource):
    status, _, data = json_request("POST", auth_origin + "/api/v1/auth/mcp/oauth/token", {200}, "token_exchange", {
        "grant_type": "authorization_code", "client_id": client_id, "code": code,
        "redirect_uri": redirect_uri, "code_verifier": verifier, "resource": resource,
    }, form=True)
    access = data.get("access_token", "")
    refresh = data.get("refresh_token", "")
    if not access or not refresh or data.get("scope") != "mcp:query":
        raise RuntimeError("token_exchange: incomplete query-only token response")
    return status, access, refresh


def run(args):
    auth_origin = local_origin(args.auth_origin)
    bff_origin = local_origin(args.bff_origin)
    audit_path = Path(args.auth_audit_log) if args.auth_audit_log else None
    audit_baseline = None
    if audit_path:
        verify_auth_audit_negative_controls()
        try:
            audit_baseline = audit_path.stat().st_size
        except OSError as exc:
            raise RuntimeError("Auth Firestore audit file is unavailable before the journey") from exc
    project_id = args.project_id
    resource = f"{bff_origin}/mcp/projects/{urllib.parse.quote(project_id, safe='')}"
    redirect_uri = "http://127.0.0.1:62390/oauth/callback"
    client_name = "GAT-4 local MCP qualification " + secrets.token_hex(4)
    cases = {}

    status, _, demo = json_request("POST", auth_origin + "/api/v1/auth/demo", {200}, "demo_login", {})
    web_token = demo.get("access_token", "")
    demo_user = demo.get("user")
    subject = demo_user.get("id", "") if isinstance(demo_user, dict) else ""
    if not web_token or not subject:
        raise RuntimeError("demo_login: missing synthetic identity")
    save_secret(args.web_token_output, web_token)
    cases["local_demo_login"] = status

    registration = {
        "client_name": client_name,
        "redirect_uris": [redirect_uri],
        "token_endpoint_auth_method": "none",
        "grant_types": ["authorization_code", "refresh_token"],
        "response_types": ["code"],
        "scope": "mcp:query",
    }
    status, _, client = json_request("POST", auth_origin + "/api/v1/auth/mcp/oauth/register", {201}, "client_registration", registration)
    client_id = client.get("client_id", "")
    if not client_id:
        raise RuntimeError("client_registration: no client ID")
    cases["dynamic_client_registration"] = status

    tx_id, verifier, state = begin_authorization(auth_origin, client_id, redirect_uri, resource, "mcp:query")
    cases["authorize_redirect"] = 302

    status, _, raw_allow = json_request("POST", auth_origin + f"/api/v1/auth/mcp/oauth/consent/{tx_id}/decision", {403}, "browser_allow_negative", {
        "decision": "allow",
    }, bearer=web_token)
    if raw_allow.get("error") != "product_decision_required":
        raise RuntimeError("browser_allow_negative: Auth did not require LWC product approval")
    cases["auth_browser_allow_rejected"] = status

    consent_url = bff_origin + f"/api/v1/mcp/oauth/consent/{tx_id}"
    status, _, details = json_request("GET", consent_url, {200}, "project_consent_details", bearer=web_token)
    if details.get("status") != "pending" or details.get("project_id") != project_id:
        raise RuntimeError("project_consent_details: pending Project consent is unavailable")
    cases["bff_project_consent_details"] = status

    decision_url = bff_origin + f"/api/v1/mcp/oauth/consent/{tx_id}/decision"
    fault_control_origin = None
    if args.require_response_loss:
        if not args.fault_control_origin:
            raise RuntimeError("--require-response-loss requires --fault-control-origin")
        fault_control_origin = local_origin(args.fault_control_origin)
        status, _, proxy_before = json_request("GET", fault_control_origin + "/__control/status", {200}, "read_auth_proxy_baseline")
        dropped_before = proxy_before.get("dropped_decisions")
        if not isinstance(dropped_before, int) or proxy_before.get("drop_armed"):
            raise RuntimeError("auth_fault_proxy is not in a clean decision-drop state")
        cases["auth_fault_proxy_dropped_baseline"] = dropped_before
        status, _, _ = json_request("POST", fault_control_origin + "/__control/arm", {200}, "arm_auth_decision_response_loss", {})
        cases["arm_auth_decision_response_loss"] = status
        lost_status, _, _ = json_request("POST", decision_url, {502, 503, 504}, "first_decision_response_lost", {
            "schema_version": 1, "decision": "allow",
        }, bearer=web_token)
        cases["first_allow_response_lost"] = lost_status
        status, _, proxy_state = json_request("GET", fault_control_origin + "/__control/status", {200}, "confirm_auth_decision_response_loss")
        if proxy_state.get("dropped_decisions") != dropped_before + 1 or proxy_state.get("last_upstream_status") != 200 or proxy_state.get("drop_armed"):
            raise RuntimeError("auth_fault_proxy did not drop exactly one successful decision response")
        cases["auth_decision_upstream_succeeded_response_dropped"] = status
    status, _, allowed = json_request("POST", decision_url, {200}, "allow_retry", {
        "schema_version": 1, "decision": "allow",
    }, bearer=web_token)
    redirect_url = allowed.get("redirect_url", "")
    if not redirect_url or allowed.get("requires_consent"):
        raise RuntimeError("allow_retry: no completed OAuth redirect")
    cases["bff_allow_retry"] = status

    status, _, completed = json_request("GET", consent_url, {200}, "completed_consent_readback", bearer=web_token)
    if completed.get("status") != "completed" or completed.get("redirect_url") != redirect_url:
        raise RuntimeError("completed_consent_readback: idempotent redirect changed")
    cases["completed_consent_readback"] = status
    status, _, repeated = json_request("POST", decision_url, {200}, "repeated_allow", {
        "schema_version": 1, "decision": "allow",
    }, bearer=web_token)
    if repeated.get("redirect_url") != redirect_url:
        raise RuntimeError("repeated_allow: idempotent redirect changed")
    cases["repeated_allow"] = status

    code = parse_code(redirect_url, redirect_uri, state)
    status, access_token, refresh_token = exchange_code(auth_origin, client_id, code, redirect_uri, verifier, resource)
    cases["oauth_code_exchange"] = status

    continued_id, continue_verifier, continue_state = begin_authorization(auth_origin, client_id, redirect_uri, resource, "mcp:query")
    continue_url = bff_origin + f"/api/v1/mcp/oauth/consent/{continued_id}"
    status, _, continue_details = json_request("GET", continue_url, {200}, "continue_consent_details", bearer=web_token)
    if not continue_details.get("can_continue"):
        raise RuntimeError("continue_consent_details: existing grant was not reusable")
    cases["continue_consent_details"] = status
    continue_decision_url = bff_origin + f"/api/v1/mcp/oauth/consent/{continued_id}/decision"
    status, _, continued = json_request("POST", continue_decision_url, {200}, "continue_decision", {
        "schema_version": 1, "decision": "continue",
    }, bearer=web_token)
    continue_redirect = continued.get("redirect_url", "")
    if not continue_redirect or continued.get("requires_consent"):
        raise RuntimeError("continue_decision: reusable grant did not continue")
    cases["continue_decision"] = status
    continue_code = parse_code(continue_redirect, redirect_uri, continue_state)
    status, access_token, refresh_token = exchange_code(auth_origin, client_id, continue_code, redirect_uri, continue_verifier, resource)
    cases["continue_code_exchange"] = status

    unowned_id, _, unowned_state = begin_authorization(auth_origin, client_id, redirect_uri,
        f"{bff_origin}/mcp/projects/gat4-unowned-negative", "mcp:query")
    unowned_consent_url = bff_origin + f"/api/v1/mcp/oauth/consent/{unowned_id}"
    status, _, unowned = json_request("GET", unowned_consent_url, {403}, "unowned_project_consent_denied", bearer=web_token)
    if unowned.get("error") != "project access denied":
        raise RuntimeError("unowned_project_consent_denied: no LWC Project ACL denial")
    cases["unowned_project_consent_denied"] = status
    status, _, raw_unowned_allow = json_request("POST", auth_origin + f"/api/v1/auth/mcp/oauth/consent/{unowned_id}/decision", {403}, "unowned_browser_allow_negative", {
        "decision": "allow",
    }, bearer=web_token)
    if raw_unowned_allow.get("error") != "product_decision_required":
        raise RuntimeError("unowned_browser_allow_negative: Auth made a Project approval decision")
    cases["unowned_browser_allow_rejected"] = status
    deny_url = bff_origin + f"/api/v1/mcp/oauth/consent/{unowned_id}/decision"
    status, _, denied = json_request("POST", deny_url, {200}, "bff_deny", {
        "schema_version": 1, "decision": "deny",
    }, bearer=web_token)
    deny_redirect = denied.get("redirect_url", "")
    deny_values = urllib.parse.parse_qs(urllib.parse.urlsplit(deny_redirect).query)
    if not deny_redirect or deny_values.get("error") != ["access_denied"] or deny_values.get("state") != [unowned_state]:
        raise RuntimeError("bff_deny: denial redirect is incomplete")
    cases["bff_deny"] = status
    status, _, repeated_deny = json_request("POST", deny_url, {200}, "repeated_deny", {
        "schema_version": 1, "decision": "deny",
    }, bearer=web_token)
    if repeated_deny.get("redirect_url") != deny_redirect:
        raise RuntimeError("repeated_deny: idempotent redirect changed")
    cases["repeated_deny"] = status

    status, _, refresh = json_request("POST", auth_origin + "/api/v1/auth/mcp/oauth/token", {200}, "oauth_refresh", {
        "grant_type": "refresh_token", "client_id": client_id, "refresh_token": refresh_token, "resource": resource,
    }, form=True)
    refreshed_access = refresh.get("access_token", "")
    refresh_token = refresh.get("refresh_token", "")
    if not refreshed_access or not refresh_token:
        raise RuntimeError("oauth_refresh: incomplete refresh rotation")
    cases["oauth_refresh_rotation"] = status
    save_secret(args.mcp_token_output, refreshed_access)
    save_secret(args.refresh_token_output, refresh_token)

    if not args.mcp_client:
        raise RuntimeError("--mcp-client is required for an actual MCP protocol client run")
    client_cmd = [args.mcp_client, "--endpoint", resource, "--token-file", args.mcp_token_output]
    client_result = subprocess.run(client_cmd, cwd=args.client_cwd, capture_output=True, text=True, timeout=90, check=False)
    if client_result.returncode != 0:
        raise RuntimeError(f"MCP client failed with exit {client_result.returncode}")
    for line in client_result.stdout.splitlines():
        if line.startswith("case="):
            print(line)
    cases["mcp_client"] = 0

    status, _, grant_list = json_request("GET", bff_origin + "/api/v1/mcp/oauth/grants?" + urllib.parse.urlencode({"project_id": project_id}), {200}, "list_project_grants", bearer=web_token)
    matching = [grant for grant in grant_list.get("grants", []) if grant.get("client_name") == client_name]
    if len(matching) != 2 or len({grant.get("id") for grant in matching}) != 2 or any(
        not grant.get("id") or grant.get("status") != "active" or not grant.get("created_at") for grant in matching
    ):
        raise RuntimeError("list_project_grants: expected the active origin and continuation grants for this synthetic MCP client")
    cases["bff_list_project_grant"] = status
    cases["bff_origin_and_continuation_grants_listed"] = len(matching)
    # Revoking the original grant must invalidate the continuation token family too.
    grant_id = min(matching, key=lambda grant: grant["created_at"])["id"]
    revoke_url = auth_origin + f"/api/v1/auth/mcp/oauth/grants/{urllib.parse.quote(grant_id, safe='')}"
    firestore_document_url_value, project_fields = remove_project_acl(
        args.firestore_emulator_origin, args.firestore_project_id, args.firestore_database_id,
        args.firestore_local_scope, subject, project_id)
    cases["same_project_acl_removed"] = 404
    try:
        acl_result = subprocess.run(client_cmd + ["--expect-project-denied"], cwd=args.client_cwd,
            capture_output=True, text=True, timeout=90, check=False)
        acl_line = next((line for line in acl_result.stdout.splitlines()
                         if line.startswith("case=mcp_project_acl_denied result=")), "")
        if acl_result.returncode != 0 or acl_line != "case=mcp_project_acl_denied result=403_denied":
            raise RuntimeError("same-Project MCP access was not denied after ACL removal")
        print(acl_line)
        cases["same_project_mcp_acl_loss_denied"] = 403

        status, _, acl_denied_grants = json_request(
            "GET", bff_origin + "/api/v1/mcp/oauth/grants?" + urllib.parse.urlencode({"project_id": project_id}),
            {403}, "grant_list_acl_loss_denied", bearer=web_token)
        if acl_denied_grants.get("error") != "project access denied":
            raise RuntimeError("BFF grant listing did not deny the missing Project ACL")
        cases["same_project_bff_grant_list_acl_loss_denied"] = status

        status, _, _ = json_request("DELETE", revoke_url, {204}, "owner_revoke_while_project_acl_missing", bearer=web_token)
        cases["auth_owner_grant_delete_while_project_acl_missing"] = status
    finally:
        restore_project_acl(firestore_document_url_value, project_fields)
    cases["same_project_acl_restored_exact_fields"] = True

    status, _, restored_grants = json_request(
        "GET", bff_origin + "/api/v1/mcp/oauth/grants?" + urllib.parse.urlencode({"project_id": project_id}),
        {200}, "grant_list_after_acl_restore", bearer=web_token)
    if not isinstance(restored_grants.get("grants"), list):
        raise RuntimeError("BFF grant listing did not recover after restoring the Project ACL")
    cases["same_project_bff_grant_list_after_acl_restore"] = status
    cases["oauth_grant_revocation"] = 204

    status, _, invalid_refresh = json_request("POST", auth_origin + "/api/v1/auth/mcp/oauth/token", {400}, "refresh_after_revoke", {
        "grant_type": "refresh_token", "client_id": client_id, "refresh_token": refresh_token, "resource": resource,
    }, form=True)
    if invalid_refresh.get("error") != "invalid_grant":
        raise RuntimeError("refresh_after_revoke: revoked grant refreshed")
    cases["revoked_refresh_rejected"] = status
    revoked_result = subprocess.run(client_cmd + ["--expect-revoked"], cwd=args.client_cwd,
        capture_output=True, text=True, timeout=90, check=False)
    revoked_line = next((line for line in revoked_result.stdout.splitlines() if line.startswith("case=mcp_revoked_access_token result=")), "")
    revoked_status = revoked_line.rsplit("=", 1)[-1].removesuffix("_rejected")
    if revoked_result.returncode != 0 or revoked_status not in ("401", "403"):
        raise RuntimeError("revoked access token was not rejected by the real MCP client")
    print(revoked_line)
    cases["revoked_access_rejected"] = int(revoked_status)

    if audit_path:
        try:
            new_bytes = audit_path.read_bytes()[audit_baseline:]
        except OSError as exc:
            raise RuntimeError("Auth Firestore audit file became unavailable during the journey") from exc
        rpc_count = validate_auth_audit(new_bytes)
        cases["auth_firestore_audit_blank_and_header_only_negative_controls"] = "rejected"
        cases["auth_firestore_forbidden_project_or_binding_access"] = 0
        cases["auth_firestore_audit_rpc_count"] = rpc_count

    report = {"schema_version": 1, "environment": "local-synthetic", "cases": cases,
        "remote_publication": "NOT RUN", "live_google_oidc": "NOT RUN; local fixture validator only"}
    if args.report_output:
        report_path = Path(args.report_output)
        report_path.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(report_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, stat.S_IRUSR | stat.S_IWUSR)
        os.fchmod(fd, stat.S_IRUSR | stat.S_IWUSR)
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            stream.write(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print("case_count=" + str(len(cases)))
    print("case_statuses=" + json.dumps(cases, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--auth-origin", default="http://localhost:62383")
    parser.add_argument("--bff-origin", default="http://localhost:62382")
    parser.add_argument("--project-id", default="default")
    parser.add_argument("--firestore-emulator-origin", default="http://127.0.0.1:62371")
    parser.add_argument("--firestore-project-id", default="llm-wiki-cloud")
    parser.add_argument("--firestore-database-id", default="llm-wiki-cloud-local")
    parser.add_argument("--firestore-local-scope", default="gat4-e2e-62371")
    parser.add_argument("--web-token-output", required=True)
    parser.add_argument("--mcp-token-output", required=True)
    parser.add_argument("--refresh-token-output", required=True)
    parser.add_argument("--mcp-client", required=True, help="prebuilt scripts/gat4-local/e2e/mcp_client.go executable")
    parser.add_argument("--client-cwd", required=True, help="LWC Go module directory containing MCP dependencies")
    parser.add_argument("--auth-audit-log")
    parser.add_argument("--report-output")
    parser.add_argument("--require-response-loss", action="store_true")
    parser.add_argument("--fault-control-origin", help="local Auth fault proxy origin used to arm one response drop")
    args = parser.parse_args()
    try:
        run(args)
    except (RuntimeError, OSError, subprocess.SubprocessError) as exc:
        print(f"qualification failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
