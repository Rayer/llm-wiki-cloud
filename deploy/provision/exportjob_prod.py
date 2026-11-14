#!/usr/bin/env python3
"""Apply the fixed Production Export worker list-only bucket binding."""
from __future__ import annotations

import argparse
import copy
from dataclasses import dataclass
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Callable
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, quote, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "deploy/provision/exportjob-prod.json"
EVIDENCE = ROOT / ".provision/exportjob-prod-evidence.json"
MAX_ETAG_ATTEMPTS = 5
IAM_POLICY_VERSION = 3
IAM_POLICY_FIELDS = ["bindings", "etag", "version"]
JSON_API_BASE = "https://storage.googleapis.com/storage/v1"
TEST_JSON_API_BASE = "http://127.0.0.1:18764/storage/v1"
FIXED_BUCKET = "llm-wiki-data"
ROLE_ID = "lwcExportObjectLister"
ROLE_NAME = "projects/llm-wiki-cloud/roles/lwcExportObjectLister"
RUNTIME_SERVICE_ACCOUNT = "lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com"
MEMBER = "serviceAccount:" + RUNTIME_SERVICE_ACCOUNT
PERMISSIONS = ["storage.objects.list"]


class ProvisionError(RuntimeError):
    """A fixed-target Production provisioning step failed closed."""


def expected_contract() -> dict:
    return {
        "environment": "production",
        "project": "llm-wiki-cloud",
        "bucket": "llm-wiki-data",
        "runtime_service_account": RUNTIME_SERVICE_ACCOUNT,
        "role": ROLE_NAME,
        "included_permissions": PERMISSIONS.copy(),
    }


def validate_contract(value: object) -> dict:
    if not isinstance(value, dict) or value != expected_contract():
        raise ProvisionError("Production Export IAM contract differs from the fixed bucket, role, permission, or worker")
    return copy.deepcopy(value)


def load_contract(path: Path = CONTRACT) -> dict:
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise ProvisionError("Production Export IAM contract is unavailable or invalid") from error
    return validate_contract(value)


def _run(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, cwd=ROOT, text=True, capture_output=True)


def _is_etag_conflict(result: subprocess.CompletedProcess[str]) -> bool:
    if result.returncode == 0:
        return False
    message = (result.stdout + "\n" + result.stderr).lower()
    return bool(re.search(
        r"conditionnotmet|condition not met|precondition failed|http\s*412|status code:\s*412|"
        r"etag.{0,80}(?:mismatch|does not match|changed|invalid)|(?:mismatch|does not match).{0,80}etag",
        message,
    ))


def _policy_bindings(policy: object) -> list[dict]:
    if not isinstance(policy, dict) or not isinstance(policy.get("bindings"), list):
        raise ProvisionError("Production bucket IAM policy has an invalid shape")
    for binding in policy["bindings"]:
        if (not isinstance(binding, dict) or not isinstance(binding.get("role"), str) or
                not isinstance(binding.get("members"), list) or
                any(not isinstance(member, str) for member in binding["members"]) or
                (binding.get("condition") is not None and not isinstance(binding.get("condition"), dict))):
            raise ProvisionError("Production bucket IAM policy contains an invalid binding")
    return policy["bindings"]


def has_etag(policy: dict) -> bool:
    etag = policy.get("etag")
    return isinstance(etag, str) and bool(etag.strip())


def conflicting_bindings(policy: dict, desired: dict) -> list[dict]:
    return [binding for binding in _policy_bindings(policy)
            if binding.get("role") == desired["role"] and
            desired["member"] in binding["members"] and
            binding.get("condition") is not None]


def exact_bindings(policy: dict, desired: dict) -> list[dict]:
    return [binding for binding in _policy_bindings(policy)
            if binding.get("role") == desired["role"] and binding.get("condition") is None]


def policy_has(policy: dict, desired: dict) -> bool:
    return any(desired["member"] in binding["members"] for binding in exact_bindings(policy, desired))


def policy_preserves(before: dict, after: dict) -> bool:
    def grouped(policy: dict) -> dict[str, set[str]]:
        values: dict[str, set[str]] = {}
        for binding in _policy_bindings(policy):
            key_binding = {key: value for key, value in binding.items() if key != "members"}
            key = json.dumps(key_binding, sort_keys=True, separators=(",", ":"))
            values.setdefault(key, set()).update(binding["members"])
        return values

    old, new = grouped(before), grouped(after)
    return all(members <= new.get(key, set()) for key, members in old.items())


def _canonical_policy(policy: dict) -> dict:
    """Compare full policy semantics while ignoring provider etag and ordering."""
    result = copy.deepcopy(policy)
    result.pop("etag", None)
    bindings = result.get("bindings")
    if isinstance(bindings, list):
        for binding in bindings:
            if isinstance(binding, dict) and isinstance(binding.get("members"), list):
                binding["members"] = sorted(binding["members"])
        result["bindings"] = sorted(bindings, key=lambda item: json.dumps(item, sort_keys=True, separators=(",", ":")))
    return result


@dataclass(frozen=True)
class PolicyReadback:
    policy: dict
    wire_policy: dict
    normalization: dict


def normalize_json_api_policy(raw: object, *, requested_maximum_version: int | None,
                              requested_fields: list[str]) -> PolicyReadback:
    """Validate raw policy version and explicitly normalize a legal unconditional v1."""
    if not isinstance(raw, dict):
        raise ProvisionError("Production bucket IAM policy read returned an invalid shape")
    if requested_maximum_version != IAM_POLICY_VERSION:
        raise ProvisionError("Production bucket IAM policy read requires an explicit maximum v3 request")
    bindings = _policy_bindings(raw)
    wire_policy = copy.deepcopy(raw)
    policy = copy.deepcopy(raw)
    observed_version = policy.get("version")
    if type(observed_version) is not int:
        raise ProvisionError("Production bucket IAM policy readback has an unexpected policy version")
    conditions_present = any(binding.get("condition") is not None for binding in bindings)
    if observed_version == 3:
        version_normalization = "none"
    elif observed_version == 1 and not conditions_present:
        # GetPolicyOptions requests the maximum response format; an unconditioned
        # policy can legally remain v1. Normalize only the working/write copy.
        policy["version"] = IAM_POLICY_VERSION
        version_normalization = "unconditional_v1_to_v3_internal_policy_view"
    elif observed_version == 1 and conditions_present:
        raise ProvisionError("Production bucket IAM policy returned v1 while containing conditions")
    else:
        raise ProvisionError("Production bucket IAM policy readback has an unexpected policy version")
    normalization = {
        "provider_api": "Cloud Storage JSON API buckets.getIamPolicy",
        "requested_maximum_policy_version": requested_maximum_version,
        "requested_fields": requested_fields.copy(),
        "provider_observed_version": observed_version,
        "normalized_policy_version": policy["version"],
        "policy_version_normalization": version_normalization,
    }
    return PolicyReadback(policy=policy, wire_policy=wire_policy, normalization=normalization)


def _urlopen_transport(method: str, url: str, headers: dict[str, str], body: bytes | None):
    request = Request(url, data=body, headers=headers, method=method)
    opener = build_opener(_NoRedirectHandler)
    try:
        with opener.open(request, timeout=30) as response:
            return response.status, response.read()
    except HTTPError as error:
        return error.code, error.read()
    except URLError as error:
        raise OSError("Cloud Storage JSON API transport failed") from error


class _NoRedirectHandler(HTTPRedirectHandler):
    """Keep authenticated policy requests pinned to the configured API host."""

    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


class GCSBucketPolicyAdapter:
    """Read/write the full bucket policy through the version-aware Storage JSON API."""

    def __init__(self, token_provider: Callable[[], str],
                 transport: Callable[[str, str, dict[str, str], bytes | None], tuple[int, bytes]] = _urlopen_transport,
                 api_base_url: str = JSON_API_BASE):
        parsed = urlsplit(api_base_url)
        has_no_url_suffix = not (parsed.username or parsed.password or parsed.query or parsed.fragment)
        is_google_api = (parsed.scheme == "https" and parsed.netloc == "storage.googleapis.com" and
                         parsed.path.rstrip("/") == "/storage/v1")
        is_fixture = (parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and
                      parsed.port == 18764 and parsed.path.rstrip("/") == "/storage/v1")
        if not (has_no_url_suffix and (is_google_api or is_fixture)):
            raise ProvisionError("Production bucket IAM adapter endpoint is not an approved fixed API or loopback fixture")
        self.token_provider = token_provider
        self.transport = transport
        self.api_base_url = api_base_url.rstrip("/")

    def _request(self, method: str, bucket: str, *, query: dict[str, str] | None = None,
                 policy: dict | None = None):
        if bucket != FIXED_BUCKET:
            raise ProvisionError("Production bucket IAM adapter target differs from the fixed bucket")
        token = self.token_provider()
        if not isinstance(token, str) or not token.strip():
            raise ProvisionError("Production Export IAM access token provider returned no token")
        url = f"{self.api_base_url}/b/{quote(bucket, safe='')}/iam"
        query_params = {"alt": "json", **(query or {})}
        url += "?" + urlencode(query_params)
        headers = {"Authorization": "Bearer " + token.strip(), "Accept": "application/json"}
        body = None
        if policy is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(policy, separators=(",", ":")).encode("utf-8")
        try:
            status, content = self.transport(method, url, headers, body)
        except OSError as error:
            raise ProvisionError("Production bucket IAM API transport result is unavailable") from error
        if not isinstance(status, int) or not isinstance(content, bytes):
            raise ProvisionError("Production bucket IAM API returned an invalid transport response")
        return url, status, content

    def read(self, bucket: str) -> PolicyReadback:
        url, status, content = self._request("GET", bucket, query={
            "optionsRequestedPolicyVersion": str(IAM_POLICY_VERSION),
            "fields": ",".join(IAM_POLICY_FIELDS),
        })
        if status < 200 or status >= 300:
            raise ProvisionError("Production bucket IAM policy read failed")
        try:
            raw = json.loads(content)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise ProvisionError("Production bucket IAM policy read returned invalid JSON") from error
        result = normalize_json_api_policy(raw, requested_maximum_version=IAM_POLICY_VERSION,
                                           requested_fields=IAM_POLICY_FIELDS)
        normalization = dict(result.normalization)
        normalization["request_url"] = url
        return PolicyReadback(policy=result.policy, wire_policy=result.wire_policy,
                              normalization=normalization)

    @staticmethod
    def serialize_for_write(policy: dict) -> dict:
        if (not isinstance(policy, dict) or type(policy.get("version")) is not int or
                policy["version"] != IAM_POLICY_VERSION):
            raise ProvisionError("Production bucket IAM policy write requires the verified v3 policy version")
        _policy_bindings(policy)
        if not has_etag(policy):
            raise ProvisionError("Production bucket IAM policy write requires the observed etag")
        serialized = copy.deepcopy(policy)
        serialized["version"] = IAM_POLICY_VERSION
        return serialized

    def write(self, bucket: str, policy: dict) -> subprocess.CompletedProcess[str]:
        serialized = self.serialize_for_write(policy)
        args = ["Cloud Storage JSON API", "buckets.setIamPolicy", bucket]
        try:
            url, status, content = self._request("PUT", bucket, policy=serialized)
        except ProvisionError as error:
            # The caller must perform readback; never replay an ambiguous mutation.
            return subprocess.CompletedProcess(args, 1, "", str(error))
        stdout = content.decode("utf-8", errors="replace")
        if 200 <= status < 300:
            return subprocess.CompletedProcess(args, 0, stdout, "")
        return subprocess.CompletedProcess(args, 1, stdout, f"HTTP {status} from {url}")


class Provisioner:
    """One-role, one-member Production bucket IAM change with injectable transport."""

    def __init__(self, contract: dict, run: Callable[[list[str]], subprocess.CompletedProcess[str]],
                 evidence_path: Path = EVIDENCE, *, policy_adapter: GCSBucketPolicyAdapter | None = None):
        self.c = validate_contract(contract)
        self.run = run
        self.policy_adapter = policy_adapter or GCSBucketPolicyAdapter(self._access_token)
        self.evidence_path = evidence_path
        self.evidence = self._load_evidence()
        self.invocation = {"role_readback": None, "policy_attempts": [], "status": "started"}
        self.evidence["invocations"].append(self.invocation)
        self._save()

    def _load_evidence(self) -> dict:
        if not self.evidence_path.exists():
            return {"schema": "lwc-486-export-prod-list-provision-v1", "target": self.c, "invocations": []}
        try:
            value = json.loads(self.evidence_path.read_text())
        except (OSError, json.JSONDecodeError) as error:
            raise ProvisionError("existing Production Export IAM evidence is unreadable; preserve it for review") from error
        if (not isinstance(value, dict) or value.get("schema") != "lwc-486-export-prod-list-provision-v1" or
                value.get("target") != self.c or not isinstance(value.get("invocations"), list)):
            raise ProvisionError("existing Production Export IAM evidence has a different target or schema")
        return value

    def _save(self) -> None:
        self.evidence_path.parent.mkdir(parents=True, exist_ok=True)
        self.evidence_path.write_text(json.dumps(self.evidence, ensure_ascii=False, indent=2) + "\n")

    def _call(self, args: list[str]) -> subprocess.CompletedProcess[str]:
        try:
            result = self.run(["gcloud", *args])
        except OSError as error:
            raise ProvisionError("Production Export IAM provider command could not start") from error
        if not isinstance(result, subprocess.CompletedProcess) or not isinstance(result.returncode, int):
            raise ProvisionError("Production Export IAM provider returned an invalid result")
        return result

    def _access_token(self) -> str:
        result = self._call(["auth", "print-access-token"])
        if result.returncode != 0 or not result.stdout.strip():
            raise ProvisionError("Production Export IAM access token could not be obtained from the inherited gcloud identity")
        return result.stdout.strip()

    def _read_policy(self) -> PolicyReadback:
        return self.policy_adapter.read(self.c["bucket"])

    def _verify_role(self) -> None:
        args = ["iam", "roles", "describe", ROLE_ID, "--project", self.c["project"], "--format=json", "--quiet"]
        result = self._call(args)
        if result.returncode != 0:
            self.invocation["role_readback"] = {"status": "read_failed"}
            self._save()
            raise ProvisionError("required list-only Export role could not be read")
        try:
            role = json.loads(result.stdout)
        except json.JSONDecodeError as error:
            self.invocation["role_readback"] = {"status": "invalid_response"}
            self._save()
            raise ProvisionError("required Export role readback is invalid") from error
        identity = {
            "name": role.get("name") if isinstance(role, dict) else None,
            "stage": role.get("stage") if isinstance(role, dict) else None,
            "included_permissions": role.get("includedPermissions") if isinstance(role, dict) else None,
        }
        self.invocation["role_readback"] = {**identity, "status": "verified"}
        self._save()
        if (identity["name"] != ROLE_NAME or identity["stage"] != "GA" or
                identity["included_permissions"] != PERMISSIONS):
            self.invocation["role_readback"]["status"] = "mismatched"
            self._save()
            raise ProvisionError("existing Export role is missing or differs from the exact list-only permission set")

    def _record_policy(self, attempt: dict) -> None:
        self.invocation["policy_attempts"].append(attempt)
        self._save()

    def _readback(self, before: dict, desired: dict, attempt: dict, *, ambiguous: bool) -> None:
        attempt["status"] = "readback_pending"
        self._save()
        try:
            readback = self._read_policy()
            after = readback.policy
        except ProvisionError:
            attempt["status"] = "unknown" if ambiguous else "readback_failed"
            self.invocation["status"] = "unknown" if ambiguous else "failed"
            self._save()
            if ambiguous:
                raise ProvisionError("Production Export IAM write remains unknown; no replay was attempted")
            raise
        attempt["after_etag"] = after.get("etag")
        attempt["after_policy"] = after
        attempt["after_wire_policy"] = readback.wire_policy
        attempt["after_policy_normalization"] = readback.normalization
        expected = attempt.get("submitted_policy")
        if not has_etag(after):
            attempt["status"] = "unknown" if ambiguous else "readback_mismatch"
            self.invocation["status"] = "unknown" if ambiguous else "failed"
            self._save()
            if ambiguous:
                raise ProvisionError("Production Export IAM write remains unknown; readback had no etag and no replay was attempted")
            raise ProvisionError("Production bucket IAM readback has no etag; the write could not be verified")
        if (conflicting_bindings(after, desired) or not policy_has(after, desired) or
                not policy_preserves(before, after) or
                not isinstance(expected, dict) or
                _canonical_policy(after) != _canonical_policy(expected)):
            attempt["status"] = "readback_mismatch"
            self.invocation["status"] = "failed"
            self._save()
            raise ProvisionError("Production bucket IAM readback did not verify the exact binding and policy preservation")
        attempt["status"] = "reconciled_ambiguous_write" if ambiguous else "verified_addition"
        self.invocation["status"] = "verified"
        self.invocation["result"] = attempt["status"]
        self._save()

    def apply(self) -> str:
        try:
            self._verify_role()
        except ProvisionError:
            self.invocation["status"] = "failed"
            self._save()
            raise
        desired = {"role": ROLE_NAME, "member": MEMBER}
        target = "bucket:" + self.c["bucket"]

        for index in range(MAX_ETAG_ATTEMPTS):
            try:
                readback = self._read_policy()
                before = readback.policy
            except ProvisionError:
                self.invocation["status"] = "failed"
                self._save()
                raise
            attempt = {"target": target, "binding": desired, "before_etag": before.get("etag"),
                       "before_policy": before, "before_wire_policy": readback.wire_policy,
                       "before_policy_normalization": readback.normalization, "status": "prepared"}
            self._record_policy(attempt)

            # Reject a second conditioned grant before accepting an exact unconditioned member.
            conflicts = conflicting_bindings(before, desired)
            if conflicts:
                attempt["conflicting_bindings"] = conflicts
                attempt["status"] = "member_has_conflicting_condition"
                self.invocation["status"] = "failed"
                self._save()
                raise ProvisionError("Production bucket policy grants this worker the list role with a conflicting condition")

            if not has_etag(before):
                attempt["status"] = "missing_etag"
                self.invocation["status"] = "failed"
                self._save()
                raise ProvisionError("Production bucket IAM policy has no etag; no update was attempted")

            matches = exact_bindings(before, desired)
            if len(matches) > 1:
                attempt["status"] = "duplicate_exact_bindings"
                self.invocation["status"] = "failed"
                self._save()
                raise ProvisionError("Production bucket policy has duplicate unconditioned list-role bindings")
            if matches and MEMBER in matches[0]["members"]:
                attempt.update(status="existing_verified", after_etag=before["etag"], after_policy=before,
                               after_wire_policy=readback.wire_policy,
                               after_policy_normalization=readback.normalization)
                self.invocation.update(status="verified", result="verified_noop")
                self._save()
                return "verified_noop"

            policy = copy.deepcopy(before)
            exact = exact_bindings(policy, desired)
            if exact:
                exact[0]["members"].append(MEMBER)
            else:
                policy.setdefault("bindings", []).append({"role": ROLE_NAME, "members": [MEMBER]})

            attempt["submitted_policy"] = self.policy_adapter.serialize_for_write(policy)
            attempt["set_policy_serialization"] = {
                "version": IAM_POLICY_VERSION,
                "provider_api": "Cloud Storage JSON API buckets.setIamPolicy",
                "provider_observed_version": readback.normalization["provider_observed_version"],
                "write_policy_version_source": "explicit_v3_policy_serializer",
            }
            attempt["status"] = "write_pending"
            self._save()
            result = self.policy_adapter.write(self.c["bucket"], attempt["submitted_policy"])

            if result.returncode != 0 and _is_etag_conflict(result):
                attempt["status"] = "etag_conflict_reread_pending"
                self._save()
                if index + 1 < MAX_ETAG_ATTEMPTS:
                    continue
                attempt["status"] = "etag_conflict_exhausted"
                self.invocation["status"] = "failed"
                self._save()
                raise ProvisionError("Production bucket IAM policy kept changing; the bounded conflict retry limit was reached")

            self._readback(before, desired, attempt, ambiguous=result.returncode != 0)
            return attempt["status"]

        raise ProvisionError("Production bucket IAM policy could not be updated")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="apply the fixed Production bucket binding")
    args = parser.parse_args(argv)
    if not args.apply:
        parser.error("--apply is required; the target is fixed to the Production Export worker and bucket")
    try:
        provisioner = Provisioner(load_contract(), _run)
        result = provisioner.apply()
        print(json.dumps({"result": result, "evidence": str(provisioner.evidence_path)}))
        return 0
    except (ProvisionError, OSError, ValueError, KeyError, json.JSONDecodeError) as error:
        print(f"Production Export IAM provisioning stopped: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
