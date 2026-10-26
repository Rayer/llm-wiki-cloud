"""LWC execution policy for the SHA-pinned Synto 0.7.0; never writes config.

Config.resolve_role is the shared seam after provider/profile/CLI precedence.
OpenAICompatClient ignores think and merges options last into the wire payload.
"""
import hashlib
import json
import os
from pathlib import Path

import synto
from synto.config import Config, ResolvedModel
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
