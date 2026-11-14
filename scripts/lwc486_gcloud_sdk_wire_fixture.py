"""Offline fixture exercising the installed Google Cloud SDK Storage client."""
from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


BUCKET = "llm-wiki-data"
ROLE = "projects/llm-wiki-cloud/roles/lwcExportObjectLister"
MEMBER = "serviceAccount:lwc-export-worker-prod@llm-wiki-cloud.iam.gserviceaccount.com"
SYNTHETIC_AUTH = "Bearer lwc486-sdk-wire-fixture"
SYNTHETIC_TOKEN = "lwc486-sdk-wire-fixture"
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

    @staticmethod
    def summarized_call(call):
        return {
            "method": call["method"],
            "url": call["url"],
            "authorization_is_synthetic": call["authorization_is_synthetic"],
            "body": json.loads(call["body"]) if call["body"] else None,
        }


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
        "response": {"version": 3 if conditioned else 1,
                     "etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": [binding]},
        "input": {"version": 1, "etag": "c2RrLWNvbmRpdGlvbmVkLWV0YWc=", "bindings": [binding]},
    }


class SyntheticRoleCommand:
    """Only satisfy the local custom-role read; it never invokes gcloud."""

    def __call__(self, args):
        if args[1:4] != ["iam", "roles", "describe"]:
            raise AssertionError("the fixture attempted an unexpected command")
        role = {"name": ROLE, "stage": "GA", "includedPermissions": ["storage.objects.list"]}
        return subprocess.CompletedProcess(args, 0, json.dumps(role), "")


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
    project_root = Path(__file__).resolve().parents[1]
    sys.path.insert(0, str(project_root))
    from deploy.provision import exportjob_prod as prod

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

    # Carry the documented provider v1 response through the actual helper adapter
    # and Provisioner while reusing the same captured numeric-loopback transport.
    noop_response = {
        "version": 1,
        "etag": "c2RrLXYxLW5vb3AtZXRhZw==",
        "bindings": [{"role": ROLE, "members": [MEMBER, "user:other@example.test"]}],
    }
    addition_before = {
        "version": 1,
        "etag": "c2RrLXYxLWFkZC1ldGFn",
        "bindings": [{"role": "roles/storage.objectViewer", "members": ["user:reader@example.test"]}],
    }
    addition_after = {
        "version": 1,
        "etag": "c2RrLXYxLWFmdGVyLWV0YWc=",
        "bindings": addition_before["bindings"] + [{"role": ROLE, "members": [MEMBER]}],
    }
    http.get_responses.extend([noop_response, addition_before, addition_after])

    def product_adapter():
        def transport(method, url, headers, body):
            info, content = http.request(url, method=method, body=body, headers=headers)
            return int(info["status"]), content

        return prod.GCSBucketPolicyAdapter(
            token_provider=lambda: SYNTHETIC_TOKEN,
            transport=transport,
            api_base_url=prod.TEST_JSON_API_BASE,
        )

    with tempfile.TemporaryDirectory(prefix="lwc486-product-v1-noop-") as temp_dir:
        before_calls = len(http.calls)
        no_op_provisioner = prod.Provisioner(
            prod.expected_contract(), SyntheticRoleCommand(), Path(temp_dir) / "evidence.json",
            policy_adapter=product_adapter(),
        )
        no_op_result = no_op_provisioner.apply()
        no_op_attempt = json.loads((Path(temp_dir) / "evidence.json").read_text())["invocations"][0]["policy_attempts"][0]
        no_op_calls = http.calls[before_calls:]

    with tempfile.TemporaryDirectory(prefix="lwc486-product-v1-add-") as temp_dir:
        before_calls = len(http.calls)
        add_provisioner = prod.Provisioner(
            prod.expected_contract(), SyntheticRoleCommand(), Path(temp_dir) / "evidence.json",
            policy_adapter=product_adapter(),
        )
        add_result = add_provisioner.apply()
        add_attempt = json.loads((Path(temp_dir) / "evidence.json").read_text())["invocations"][0]["policy_attempts"][0]
        add_calls = http.calls[before_calls:]

    product_cases = {
        "unconditional_v1_noop": {
            "result": no_op_result,
            "before_wire_version": no_op_attempt["before_wire_policy"]["version"],
            "before_policy_version": no_op_attempt["before_policy"]["version"],
            "normalization": no_op_attempt["before_policy_normalization"],
            "write_count": sum(call["method"] == "PUT" for call in no_op_calls),
            "calls": [http.summarized_call(call) for call in no_op_calls],
        },
        "unconditional_v1_addition": {
            "result": add_result,
            "before_wire_version": add_attempt["before_wire_policy"]["version"],
            "before_policy_version": add_attempt["before_policy"]["version"],
            "after_wire_version": add_attempt["after_wire_policy"]["version"],
            "after_policy_version": add_attempt["after_policy"]["version"],
            "before_normalization": add_attempt["before_policy_normalization"],
            "after_normalization": add_attempt["after_policy_normalization"],
            "submitted_version": add_attempt["submitted_policy"]["version"],
            "write_serialization": add_attempt["set_policy_serialization"],
            "submitted_policy": add_attempt["submitted_policy"],
            "write_count": sum(call["method"] == "PUT" for call in add_calls),
            "methods": [call["method"] for call in add_calls],
            "calls": [http.summarized_call(call) for call in add_calls],
        },
    }

    print(json.dumps({"base_url": BASE_URL, "cases": results, "version_controls": controls,
                      "loss_controls": loss_controls,
                      "product_cases": product_cases,
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
