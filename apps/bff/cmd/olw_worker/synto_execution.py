"""LWC execution policy for the SHA-pinned Synto 0.7.0; never writes config.

Config.resolve_role is the shared seam after provider/profile/CLI precedence.
OpenAICompatClient ignores think and merges options last into the wire payload.
"""
import hashlib
import httpx
import ipaddress
import json
import os
import socket
from pathlib import Path
from urllib.parse import urlsplit

import synto
from synto.config import Config, ResolvedModel
from synto.openai_compat_client import LLMError, OpenAICompatClient
from synto.providers import ProviderInfo, PROVIDER_REGISTRY
from synto.pipeline import compile as compile_pipeline, ingest

if synto.__version__ != "0.7.0":
    raise RuntimeError("LWC execution adapter requires pinned Synto 0.7.0")

# Synto calls this builder only for a newly generated draft. Add identity to
# that compile_run before serialization; publish_drafts keeps the frontmatter
# intact, and carried articles never pass through this seam.
_build_wiki_frontmatter = compile_pipeline.build_wiki_frontmatter


def _build_wiki_frontmatter_with_generator(*args, **kwargs):
    lineage = kwargs.get("lineage")
    if isinstance(lineage, list):
        kwargs = dict(kwargs)
        kwargs["lineage"] = [
            {
                **entry,
                "generator": {
                    "implementation": "synto",
                    "version": synto.__version__,
                },
            }
            if isinstance(entry, dict)
            and isinstance(entry.get("compile_run"), str)
            and entry["compile_run"].strip()
            else entry
            for entry in lineage
        ]
    return _build_wiki_frontmatter(*args, **kwargs)


compile_pipeline.build_wiki_frontmatter = _build_wiki_frontmatter_with_generator

_resolve_role = Config.resolve_role
_MODELS = {
    "deepseek-chat": "disabled",
    "deepseek-reasoner": "enabled",
    "deepseek-v4-flash": "enabled",
    "deepseek-v4-pro": "enabled",
    "deepseek-flash": "enabled",
}


class _FlashResolvedModel(ResolvedModel):
    @property
    def connection_key(self):
        # The pinned router shares clients by connection; each thinking policy
        # needs its own client namespace before OpenAICompatClient's cache lookup.
        return (*super().connection_key, self.options["thinking"]["type"], self.options.get("reasoning_effort"))

    @property
    def cache_namespace(self):
        policy = json.dumps([self.options["thinking"], self.options.get("reasoning_effort")], sort_keys=True)
        return hashlib.sha256((super().cache_namespace + "\0" + policy).encode()).hexdigest()


def _resolve_flash(self, role, *, api_key_env=None):
    resolved = _resolve_role(self, role, api_key_env=api_key_env)
    if resolved.provider_kind != "deepseek":
        return resolved
    # options are merged last by the pinned client, including a model override.
    model = resolved.options.get("model", resolved.model)
    if model not in _MODELS or role == "embed":
        raise ValueError("unsupported DeepSeek execution model/role")
    options = dict(resolved.options)
    thinking = options.get("thinking", {"type": _MODELS[model]})
    if not isinstance(thinking, dict) or thinking not in (
        {"type": "enabled"}, {"type": "disabled"}
    ):
        raise ValueError("unsupported DeepSeek thinking configuration")
    if "reasoning_effort" in options and options["reasoning_effort"] not in ("low", "high", "max"):
        raise ValueError("unsupported DeepSeek reasoning effort")
    options["thinking"] = thinking
    if "model" in options:
        options["model"] = "deepseek-flash"
    return _FlashResolvedModel(**(vars(resolved) | {"model": "deepseek-flash", "options": options}))


Config.resolve_role = _resolve_flash

_profile_raw = os.environ.get("LWC_PROFILE_CONFIG", "")
if _profile_raw:
    _profile = json.loads(_profile_raw)
    _profile_keys_v1 = {"schema_version", "snapshot_digest", "role_map_version", "id", "revision", "provider", "protocol", "endpoint", "model"}
    _profile_keys_v2 = {"schema_version", "snapshot_digest", "role_map_version", "id", "revision", "provider", "protocol", "endpoint", "models"}
    if not isinstance(_profile, dict):
        raise RuntimeError("invalid resolved Project LLM profile")
    if type(_profile.get("schema_version")) is not int:
        raise RuntimeError("invalid resolved Project LLM profile version")
    if _profile.get("schema_version") == 1 and set(_profile) == _profile_keys_v1:
        legacy_model = _profile["model"]
        if not isinstance(legacy_model, str) or not legacy_model.strip():
            raise RuntimeError("invalid legacy Project LLM model")
        _profile.pop("model")
        _profile["models"] = {"fast": legacy_model, "heavy": legacy_model}
        _profile["schema_version"] = 2
    if _profile.get("schema_version") != 2 or set(_profile) != _profile_keys_v2:
        raise RuntimeError("invalid resolved Project LLM profile")
    if (not isinstance(_profile["id"], str) or not _profile["id"]
            or type(_profile["revision"]) is not int or _profile["revision"] < 1
            or not isinstance(_profile["provider"], str) or not isinstance(_profile["endpoint"], str)
            or not isinstance(_profile["models"], dict) or set(_profile["models"]) != {"fast", "heavy"}
            or not all(isinstance(_profile["models"][role], str) and _profile["models"][role] == _profile["models"][role].strip() and 0 < len(_profile["models"][role]) <= 200 for role in ("fast", "heavy"))
            or not isinstance(_profile["snapshot_digest"], str) or len(_profile["snapshot_digest"]) != 64
            or any(ch not in "0123456789abcdef" for ch in _profile["snapshot_digest"])
            or type(_profile["role_map_version"]) is not int or _profile["role_map_version"] != 1):
        raise RuntimeError("invalid resolved Project LLM profile")
    _profile_url = urlsplit(_profile["endpoint"])
    if _profile_url.scheme != "https" or not _profile_url.hostname or _profile_url.username or _profile_url.password or _profile_url.query or _profile_url.fragment:
        raise RuntimeError("invalid resolved Project LLM endpoint")
    _profile_host = _profile_url.hostname.rstrip(".").lower()
    _profile_call_success_marker_path = os.environ.get("LWC_PROFILE_CALL_SUCCESS_MARKER_PATH", "")
    _original_getaddrinfo = socket.getaddrinfo

    def _mark_profile_call_success():
        if not _profile_call_success_marker_path:
            return
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
        try:
            fd = os.open(_profile_call_success_marker_path, flags, 0o600)
        except FileExistsError:
            return
        except OSError as exc:
            raise RuntimeError("Project profile call outcome marker is unavailable") from exc
        os.close(fd)

    def _is_profile_quota_response(response):
        if response.status_code != 429:
            return False
        try:
            envelope = response.json()
        except Exception:
            return False
        error = envelope.get("error") if isinstance(envelope, dict) else None
        code = error.get("code") if isinstance(error, dict) else None
        return isinstance(code, str) and code.strip().lower() in _QUOTA_CODES

    def _clear_profile_attempt_for_quota():
        if not _profile_call_success_marker_path:
            return
        try:
            os.unlink(_profile_call_success_marker_path)
        except FileNotFoundError:
            return
        except OSError as exc:
            raise RuntimeError("Project profile quota fallback marker could not be cleared") from exc

    _profile_httpx_send = httpx.Client.send

    def _send_with_profile_call_success_marker(self, request, *args, **kwargs):
        profile_generation = (
            request.url.host.rstrip(".").lower() == _profile_host
            and request.method.upper() != "GET"
        )
        if profile_generation:
            # Only a provider-confirmed account quota response is safe to
            # replay; interrupted and other failed generations remain pinned.
            _mark_profile_call_success()
        response = _profile_httpx_send(self, request, *args, **kwargs)
        if profile_generation and _is_profile_quota_response(response):
            _clear_profile_attempt_for_quota()
        return response

    httpx.Client.send = _send_with_profile_call_success_marker

    def _public_profile_dns(host, *args, **kwargs):
        if isinstance(host, str) and host.rstrip(".").lower() == _profile_host:
            results = _original_getaddrinfo(host, *args, **kwargs)
            if not results:
                raise OSError("Project LLM endpoint DNS lookup failed")
            for result in results:
                try:
                    address = ipaddress.ip_address(result[4][0].split("%", 1)[0])
                except (ValueError, IndexError, TypeError) as exc:
                    raise OSError("Project LLM endpoint DNS response is invalid") from exc
                if not address.is_global:
                    raise OSError("Project LLM endpoint resolved to a non-public address")
            # Return the exact validated sockaddr records; socket.create_connection
            # uses them directly, so a second DNS lookup cannot rebind the host.
            return results
        return _original_getaddrinfo(host, *args, **kwargs)

    socket.getaddrinfo = _public_profile_dns
    if _profile["protocol"] == "anthropic_messages":
        _profile_kind = "lwc_anthropic_compat"
        PROVIDER_REGISTRY[_profile_kind] = ProviderInfo(
            _profile_kind, "LWC Anthropic-compatible", _profile["endpoint"], True, False, False, False,
            120.0, "LWC_PROFILE_API_KEY", anthropic_compat=True,
        )
    elif _profile["protocol"] == "openai_chat_completions":
        _profile_kind = "custom"
    else:
        raise RuntimeError("unsupported resolved Project LLM protocol")

    _resolve_profile_base = Config.resolve_role

    def _resolve_profile(self, role, *, api_key_env=None):
        resolved = _resolve_profile_base(self, role, api_key_env=api_key_env)
        if role == "embed":
            return resolved
        if role not in ("fast", "heavy"):
            raise ValueError("unsupported Project LLM role")
        api_key = os.environ.get("LWC_PROFILE_API_KEY", "")
        profile_values = vars(resolved).copy()
        profile_values.update({
            "provider_kind": _profile_kind,
            "url": _profile["endpoint"],
            "api_key": api_key or None,
            "api_key_env": "LWC_PROFILE_API_KEY" if api_key else None,
            "model": _profile["models"][role],
            "think": None,
            "supports_json_mode": _profile["protocol"] == "openai_chat_completions",
            "supports_embeddings": False,
            "azure": False,
            "anthropic_compat": _profile["protocol"] == "anthropic_messages",
            "options": {key: value for key, value in resolved.options.items() if key != "model"},
            "headers": {},
        })
        return ResolvedModel(**profile_values)

    Config.resolve_role = _resolve_profile

# Synto 0.7.0 intentionally collapses all HTTP 429 responses to rate limits.
# Preserve its native protocol clients but recognize only provider-confirmed
# account quota codes for a clean shared-pool retry.
_QUOTA_CODES = {"insufficient_quota", "quota_exhausted", "quota_exceeded", "billing_hard_limit_reached"}
_quota_marker_path = os.environ.get("LWC_PROFILE_QUOTA_MARKER_PATH", "")

def _is_explicit_quota(exc):
    response = getattr(exc, "response", None)
    if response is None or response.status_code != 429:
        return False
    try:
        envelope = response.json()
    except Exception:
        return False
    error = envelope.get("error") if isinstance(envelope, dict) else None
    code = error.get("code") if isinstance(error, dict) else None
    return isinstance(code, str) and code.strip().lower() in _QUOTA_CODES

def _mark_explicit_quota():
    if not _quota_marker_path:
        return
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(_quota_marker_path, flags, 0o600)
    os.close(fd)

def _wrap_provider_error(original):
    def wrapped(self, exc, context=""):
        if _is_explicit_quota(exc):
            _mark_explicit_quota()
            return LLMError("shared provider quota exhausted")
        return original(self, exc, context)
    return wrapped

OpenAICompatClient._wrap_error = _wrap_provider_error(OpenAICompatClient._wrap_error)
from synto.anthropic_compat_client import AnthropicCompatClient
AnthropicCompatClient._wrap_error = _wrap_provider_error(AnthropicCompatClient._wrap_error)

# The worker supplies this temporary overlay only when it pinned nonempty
# active Profile guidance. Synto's own CLI still owns the vault's normal schema.
_profile_schema_path = os.environ.get("LWC_PROFILE_SCHEMA_PATH")
if _profile_schema_path:
    Config.schema_path = property(lambda self: Path(_profile_schema_path))
_profile_guidance = os.environ.get("LWC_PROFILE_GUIDANCE", "")
if _profile_guidance:
    _request_structured = compile_pipeline.request_structured

    def _request_structured_with_profile_guidance(*args, **kwargs):
        if kwargs.get("stage") == "compile_article" and isinstance(kwargs.get("prompt"), str):
            kwargs["prompt"] += (
                "\n\nPROJECT PROFILE COMPILE GUIDANCE\n"
                "Follow these project-specific requirements for this article, including over inferred output-language defaults when applicable:\n"
                + _profile_guidance
            )
        return _request_structured(*args, **kwargs)

    compile_pipeline.request_structured = _request_structured_with_profile_guidance
# New compile/checkpoint provenance must name the same effective model as the
# role endpoint; this does not rewrite old rows or force a pipeline execution.
Config.model_name = lambda self, role: self.resolve_role(role).model

# Resumable ingest chunks have a second, independent cache in this exact wheel.
# Preserve its content/prompt/account identity and add only effective policy.
_ingest_checkpoint_hash = ingest._checkpoint_hash


def _flash_checkpoint_hash(content_hash, config, prompt_contexts, source_type="notes"):
    digest = _ingest_checkpoint_hash(content_hash, config, prompt_contexts, source_type)
    fast = config.resolve_role("fast")
    if fast.provider_kind != "deepseek":
        return digest
    policy = json.dumps([fast.options["thinking"], fast.options.get("reasoning_effort")], sort_keys=True)
    return hashlib.sha256((digest + "\0" + policy).encode()).hexdigest()


ingest._checkpoint_hash = _flash_checkpoint_hash

if __name__ == "__main__":
    from synto.cli import cli

    cli(prog_name="synto")
