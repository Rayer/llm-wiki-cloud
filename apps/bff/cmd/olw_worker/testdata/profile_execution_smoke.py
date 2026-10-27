"""Pinned Synto Project profile protocol, DNS, and quota marker gate."""
import json
import os
from pathlib import Path
import runpy
import socket
import tempfile

import httpx
import synto
from synto.client_factory import build_router
from synto.config import Config
from synto.openai_compat_client import LLMError

assert synto.__version__ == "0.7.0"
wrapper = Path(__file__).resolve().parents[1] / "synto_execution.py"
base_resolve_role = Config.resolve_role
base_getaddrinfo = socket.getaddrinfo
provider = {"default": {"name": "deepseek", "url": "https://api.deepseek.com", "api_key_env": "LWC_TEST_API_KEY"}}


def run_adapter(profile, api_key="synthetic-profile-secret", marker=None, dns=None):
    env_keys = ("LWC_PROFILE_CONFIG", "LWC_PROFILE_API_KEY", "LWC_PROFILE_QUOTA_MARKER_PATH")
    old_env = {key: os.environ.get(key) for key in env_keys}
    try:
        os.environ["LWC_PROFILE_CONFIG"] = json.dumps(profile)
        os.environ["LWC_PROFILE_API_KEY"] = api_key
        if marker is None:
            os.environ.pop("LWC_PROFILE_QUOTA_MARKER_PATH", None)
        else:
            os.environ["LWC_PROFILE_QUOTA_MARKER_PATH"] = str(marker)
        Config.resolve_role = base_resolve_role
        socket.getaddrinfo = dns or base_getaddrinfo
        runpy.run_path(str(wrapper), run_name="lwc_profile_fixture")
    except Exception:
        Config.resolve_role = base_resolve_role
        socket.getaddrinfo = base_getaddrinfo
        raise
    return old_env


def restore(old_env):
    Config.resolve_role = base_resolve_role
    socket.getaddrinfo = base_getaddrinfo
    for key, value in old_env.items():
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value


def exercise_protocol(protocol):
    anthropic = protocol == "anthropic_messages"
    profile = {
        "id": "fixture-anthropic" if anthropic else "fixture-openai",
        "revision": 3,
        "provider": "anthropic" if anthropic else "openai",
        "protocol": protocol,
        "endpoint": "https://api.anthropic.com" if anthropic else "https://api.openai.com/v1",
        "model": "fixture-model",
    }
    old_env = run_adapter(profile, f"synthetic-{'anthropic' if anthropic else 'openai'}-secret")
    try:
        config = Config(vault="/tmp/lwc-project-profile-smoke", providers=provider,
                       models={"fast": "deepseek-chat", "heavy": "deepseek-reasoner"})
        router = build_router(config)
        endpoint = router.endpoint("heavy")
        assert getattr(endpoint.client, "_api_key", None) == f"synthetic-{'anthropic' if anthropic else 'openai'}-secret"
        seen = []

        def capture(request):
            seen.append(request)
            if anthropic:
                return httpx.Response(200, json={
                    "id": "msg_fixture", "model": profile["model"],
                    "content": [{"type": "text", "text": "fixture answer"}],
                    "stop_reason": "end_turn", "usage": {"input_tokens": 2, "output_tokens": 3},
                })
            return httpx.Response(200, json={
                "id": "chat_fixture", "model": profile["model"],
                "choices": [{"message": {"content": "fixture answer"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 2, "completion_tokens": 3},
            })

        default_headers = dict(endpoint.client._client.headers)
        endpoint.client._client.close()
        endpoint.client._client = httpx.Client(headers=default_headers, transport=httpx.MockTransport(capture))
        answer = endpoint.client.generate("Synthetic profile protocol probe", model=endpoint.model, options=endpoint.options)
        assert answer == "fixture answer", answer
        assert len(seen) == 1, seen
        request = seen[0]
        payload = json.loads(request.content)
        if anthropic:
            assert request.url.path == "/v1/messages", request.url
            assert request.headers["x-api-key"] == "synthetic-anthropic-secret"
            assert request.headers["anthropic-version"] == "2023-06-01"
        else:
            assert request.url.path == "/v1/chat/completions", request.url
            assert request.headers.get("authorization") == "Bearer synthetic-openai-secret", list(request.headers.keys())
        assert payload["model"] == profile["model"]
        assert payload["messages"][0]["content"] == "Synthetic profile protocol probe"
        router.close()
        print(f"PASS pinned Synto Project profile: {protocol} -> {request.url.path}")
    finally:
        restore(old_env)


exercise_protocol("openai_chat_completions")
exercise_protocol("anthropic_messages")


def dns_result(address):
    family = socket.AF_INET6 if ":" in address else socket.AF_INET
    sockaddr = (address, 443, 0, 0) if family == socket.AF_INET6 else (address, 443)
    return [(family, socket.SOCK_STREAM, 6, "", sockaddr)]


for address, accepted in (("8.8.8.8", True), ("127.0.0.1", False), ("169.254.169.254", False), ("::1", False)):
    profile = {
        "id": "fixture-dns", "revision": 1, "provider": "custom",
        "protocol": "openai_chat_completions", "endpoint": "https://provider.example/v1", "model": "fixture-model",
    }
    old_env = run_adapter(profile, dns=lambda host, *args, **kwargs: dns_result(address))
    try:
        try:
            resolved = socket.getaddrinfo("provider.example", 443)
            assert accepted, (address, resolved)
            assert resolved == dns_result(address), resolved
        except OSError as exc:
            assert not accepted and "non-public" in str(exc), (address, exc)
    finally:
        restore(old_env)
print("PASS Project profile DNS rejects private, loopback, and metadata addresses")


def exercise_quota(code, expected_marker):
    profile = {
        "id": "fixture-shared", "revision": 1, "provider": "openai",
        "protocol": "openai_chat_completions", "endpoint": "https://api.openai.com/v1", "model": "fixture-model",
    }
    with tempfile.TemporaryDirectory(prefix="lwc-profile-quota-") as temp:
        marker = Path(temp) / "quota.marker"
        old_env = run_adapter(profile, "synthetic-shared-secret", marker=marker)
        try:
            config = Config(vault=temp, providers=provider, models={"fast": "deepseek-chat", "heavy": "deepseek-reasoner"})
            router = build_router(config)
            endpoint = router.endpoint("heavy")

            def quota_response(_request):
                return httpx.Response(429, headers={"Retry-After": "61"}, json={"error": {"code": code, "message": "fixture"}})

            default_headers = dict(endpoint.client._client.headers)
            endpoint.client._client.close()
            endpoint.client._client = httpx.Client(headers=default_headers, transport=httpx.MockTransport(quota_response))
            try:
                endpoint.client.generate("Synthetic quota probe", model=endpoint.model, options=endpoint.options)
            except LLMError:
                pass
            else:
                raise AssertionError("fixture 429 unexpectedly completed")
            assert marker.exists() is expected_marker, (code, marker.exists(), expected_marker)
            if expected_marker:
                assert marker.is_file() and marker.stat().st_size == 0
            router.close()
            print(f"PASS pinned Synto quota classification: {code}, fallback marker={expected_marker}")
        finally:
            restore(old_env)


exercise_quota("insufficient_quota", True)
exercise_quota("rate_limit_exceeded", False)
