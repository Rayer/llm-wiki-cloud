"""Offline fixture exercising the installed Google Cloud SDK Storage client."""
from __future__ import annotations

import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit


BUCKET = "llm-wiki-data"
ROLE = "projects/llm-wiki-cloud/roles/lwcExportObjectLister"
MEMBER = "serviceAccount:lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com"
SYNTHETIC_AUTH = "Bearer lwc486-sdk-wire-fixture"
BASE_URL = "http://127.0.0.1:18764/storage/v1/"


class CapturedHttp:
    """A bottom httplib2-compatible transport; it never opens a socket."""

    def __init__(self, get_responses):
        self.get_responses = list(get_responses)
        self.calls = []

    def request(self, uri, method="GET", body=None, headers=None, **kwargs):
        parsed = urlsplit(uri)
        if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or
                parsed.port != 18764 or not parsed.path.startswith("/storage/v1/")):
            raise AssertionError("SDK attempted to leave the numeric-loopback fixture")
        call = {
            "method": method,
            "url": uri,
            "authorization_is_synthetic": (headers or {}).get("Authorization") == SYNTHETIC_AUTH,
            "body": body.decode("utf-8") if isinstance(body, bytes) else body,
        }
        self.calls.append(call)
        if method == "GET":
            payload = self.get_responses.pop(0)
        elif method == "PUT":
            payload = json.loads(call["body"])
        else:
            raise AssertionError("unexpected SDK HTTP method: " + method)
        content = json.dumps(payload).encode("utf-8")
        return {"status": "200", "content-type": "application/json", "content-length": str(len(content))}, content


def policy_case(name, *, conditioned):
    binding = {"role": ROLE, "members": [MEMBER, "user:fixture@example.test"]}
    if conditioned:
        binding["condition"] = {
            "title": name,
            "description": "synthetic condition retained by the offline SDK fixture",
            "expression": "resource.name.startsWith('projects/llm-wiki-cloud/')",
        }
    return {
        "name": name,
        "response": {"version": 3, "etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": [binding]},
        "input": {"version": 1, "etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": [binding]},
    }


def main():
    adc = os.environ.get("GOOGLE_APPLICATION_CREDENTIALS", "")
    if Path(adc).exists():
        raise AssertionError("fixture ADC path must be explicitly nonexistent")
    if os.environ.get("GCE_METADATA_HOST") != "127.0.0.1:1":
        raise AssertionError("metadata must be blocked to an owned loopback refusal endpoint")
    if "CLOUDSDK_CONFIG" not in os.environ or "HOME" not in os.environ:
        raise AssertionError("fixture requires an isolated Cloud SDK config and home")

    # Import only after the parent test launcher has supplied the isolated environment.
    # These are the exact installed Cloud SDK modules used by `gcloud storage`.
    from googlecloudsdk.api_lib.storage.gcs_json.metadata_field_converters import process_iam_file
    from googlecloudsdk.generated_clients.apis.storage.v1 import storage_v1_client
    from googlecloudsdk.generated_clients.apis.storage.v1 import storage_v1_messages

    cases = [policy_case("conditional", conditioned=True), policy_case("unconditioned", conditioned=False)]
    wrong_version = {"version": 2, "etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": cases[0]["response"]["bindings"]}
    missing_version = {"etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": cases[1]["response"]["bindings"]}
    member_loss = copy_json(cases[0]["response"])
    member_loss["bindings"][0]["members"] = ["user:fixture@example.test"]
    condition_loss = copy_json(cases[0]["response"])
    condition_loss["bindings"][0].pop("condition", None)
    http = CapturedHttp(
        [case["response"] for case in cases] +
        [wrong_version, missing_version, member_loss, condition_loss]
    )
    messages = storage_v1_messages
    client = storage_v1_client.StorageV1(
        url=BASE_URL,
        get_credentials=False,
        http=http,
        additional_http_headers={"Authorization": SYNTHETIC_AUTH},
    )
    results = []
    for case in cases:
        before = len(http.calls)
        request = messages.StorageBucketsGetIamPolicyRequest(
            bucket=BUCKET, optionsRequestedPolicyVersion=3)
        policy = client.buckets.GetIamPolicy(
            request,
            global_params=messages.StandardQueryParameters(fields="bindings,etag,version"),
        )
        get_call = http.calls[before]
        with tempfile.TemporaryDirectory(prefix="lwc486-sdk-wire-") as temp_dir:
            policy_file = Path(temp_dir) / "policy.json"
            policy_file.write_text(json.dumps(case["input"]))
            serialized_policy = process_iam_file(str(policy_file))
        set_request = messages.StorageBucketsSetIamPolicyRequest(bucket=BUCKET, policy=serialized_policy)
        set_response = client.buckets.SetIamPolicy(set_request)
        set_call = http.calls[before + 1]
        results.append({
            "name": case["name"],
            "get_version": policy.version,
            "get_etag": policy.etag.decode("utf-8"),
            "get_bindings": policy_dict(policy),
            "get_wire": {"method": get_call["method"], "url": get_call["url"],
                         "authorization_is_synthetic": get_call["authorization_is_synthetic"]},
            "set_version": set_response.version,
            "set_wire": {"method": set_call["method"], "url": set_call["url"],
                         "authorization_is_synthetic": set_call["authorization_is_synthetic"],
                         "body": json.loads(set_call["body"])},
        })

    controls = []
    for response_version in (2, None):
        request = messages.StorageBucketsGetIamPolicyRequest(bucket=BUCKET, optionsRequestedPolicyVersion=3)
        policy = client.buckets.GetIamPolicy(
            request,
            global_params=messages.StandardQueryParameters(fields="bindings,etag,version"),
        )
        controls.append({"response_version": response_version,
                         "observed_version": policy.version})

    member_policy = client.buckets.GetIamPolicy(
        messages.StorageBucketsGetIamPolicyRequest(bucket=BUCKET, optionsRequestedPolicyVersion=3),
        global_params=messages.StandardQueryParameters(fields="bindings,etag,version"),
    )
    condition_policy = client.buckets.GetIamPolicy(
        messages.StorageBucketsGetIamPolicyRequest(bucket=BUCKET, optionsRequestedPolicyVersion=3),
        global_params=messages.StandardQueryParameters(fields="bindings,etag,version"),
    )
    loss_controls = {
        "member_loss_observed": MEMBER not in member_policy.bindings[0].members,
        "condition_loss_observed": condition_policy.bindings[0].condition is None,
    }

    print(json.dumps({"base_url": BASE_URL, "cases": results, "version_controls": controls,
                      "loss_controls": loss_controls,
                      "captured_call_count": len(http.calls)}, sort_keys=True))


def copy_json(value):
    return json.loads(json.dumps(value))


def policy_dict(policy):
    bindings = []
    for item in policy.bindings:
        binding = {"role": item.role, "members": list(item.members)}
        condition = item.condition
        if condition is not None:
            binding["condition"] = {
                "title": condition.title,
                "description": condition.description,
                "expression": condition.expression,
            }
        bindings.append(binding)
    return {"etag": policy.etag.decode("utf-8"), "version": policy.version, "bindings": bindings}


if __name__ == "__main__":
    main()
