"""LWC execution policy for the SHA-pinned Synto 0.7.0; never writes config.

Config.resolve_role is the shared seam after provider/profile/CLI precedence.
OpenAICompatClient ignores think and merges options last into the wire payload.
"""
from dataclasses import replace

import synto
from synto.config import Config

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
    return replace(resolved, model="deepseek-flash", options=options)


Config.resolve_role = _resolve_flash

if __name__ == "__main__":
    from synto.cli import cli

    cli(prog_name="synto")
