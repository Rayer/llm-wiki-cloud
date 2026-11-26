"""Exercise the embedded adapter's BYOK classifier without importing Synto or HTTPX."""
import ast
import json
import os
from pathlib import Path
import tempfile


source_path = Path(__file__).resolve().parents[1] / "synto_execution.py"
source = ast.parse(source_path.read_text(encoding="utf-8"), filename=str(source_path))


def load_embedded_function(name):
    node = next(item for item in ast.walk(source) if isinstance(item, ast.FunctionDef) and item.name == name)
    namespace = {"json": json, "os": os, "_byok_failure_marker_path": ""}
    module = ast.Module(body=[node], type_ignores=[])
    exec(compile(module, str(source_path), "exec"), namespace)
    return namespace[name], namespace


class Response:
    def __init__(self, status_code, payload):
        self.status_code = status_code
        self.payload = payload

    def json(self):
        return self.payload


class InvalidJSONResponse(Response):
    def json(self):
        raise ValueError("provider body is not JSON")


classify, _ = load_embedded_function("_actionable_byok_failure")
for status_code, payload, expected in (
    (401, {"error": {"code": "invalid_api_key"}}, "authentication"),
    (403, {"error": {"message": "forbidden"}}, "permission"),
    (429, {"error": {"code": "insufficient_quota"}}, "quota"),
    (429, {"error": {"type": "rate_limit_error"}}, ""),
    (408, {"error": {"message": "timeout"}}, ""),
    (500, {"error": {"message": "server error"}}, ""),
    (400, {"error": {"code": "unsupported_parameter"}}, "model_configuration"),
    (500, {"error": {"type": "billing_error"}}, "billing"),
):
    actual = classify(Response(status_code, payload))
    assert actual == expected, (status_code, payload, actual, expected)

assert classify(InvalidJSONResponse(401, None)) == "authentication"

write_marker, marker_namespace = load_embedded_function("_write_byok_failure_marker")
with tempfile.TemporaryDirectory(prefix="lwc-byok-marker-test-") as directory:
    marker = Path(directory) / "failure"
    marker_namespace["_byok_failure_marker_path"] = str(marker)
    write_marker("authentication")
    assert json.loads(marker.read_text(encoding="utf-8")) == {"reason": "authentication"}
    write_marker("")
    assert not marker.exists()

print("BYOK actionable classifier cases passed")
