"""LWC execution policy for the SHA-pinned Synto 0.7.0; never writes config.

Config.resolve_role is the shared seam after provider/profile/CLI precedence.
OpenAICompatClient ignores think and merges options last into the wire payload.
"""
import hashlib
import json

import synto
from synto.config import Config, ResolvedModel

if synto.__version__ != "0.7.0":
    raise RuntimeError("LWC execution adapter requires pinned Synto 0.7.0")

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
# New compile/checkpoint provenance must name the same effective model as the
# role endpoint; this does not rewrite old rows or force a pipeline execution.
Config.model_name = lambda self, role: self.resolve_role(role).model

if __name__ == "__main__":
    from synto.cli import cli

    cli(prog_name="synto")
