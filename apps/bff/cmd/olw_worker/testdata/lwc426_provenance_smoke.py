#!/usr/bin/env python3
"""Exercise the pinned Synto writer and publisher without a provider call."""
from __future__ import annotations

import importlib.util
import os
import tempfile
from pathlib import Path

import synto
from synto.config import Config
from synto.models import PipelineVersion, SingleArticle
from synto.pipeline import compile as compile_pipeline
from synto.state import StateDB
from synto.vault import parse_note


RUN_1 = "01JAZ5N7Y3K8M2Q4R6T9VWXAC1"
RUN_2 = "01JAZ5N7Y3K8M2Q4R6T9VWXAC2"
OLD_RUN = "01JAZ5N7Y3K8M2Q4R6T9VWXAC3"
OLD_GENERATOR = {
    "compile_run": OLD_RUN,
    "generator": {"implementation": "olw", "version": "0.8.5", "revision": "fixture-r1"},
}
PROVIDER_KEYS = (
    "LLM_API_KEY",
    "DEEPSEEK_API_KEY",
    "SYNTO_API_KEY",
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GEMINI_API_KEY",
    "TYPESAFE_API_KEY",
    "TYPESAFE_JEV_API_KEY",
)


def load_worker_adapter() -> None:
    adapter_path = Path(__file__).parent.parent / "synto_execution.py"
    spec = importlib.util.spec_from_file_location("lwc_synto_execution", adapter_path)
    if spec is None or spec.loader is None:
        raise AssertionError("could not load the embedded worker adapter source")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)


def generator_entry(entry: dict, run_id: str) -> None:
    if entry.get("compile_run") != run_id:
        raise AssertionError(f"compile_run={entry.get('compile_run')!r}, want {run_id}")
    generator = entry.get("generator")
    if generator != {"implementation": "synto", "version": "0.7.0"}:
        raise AssertionError(f"generator={generator!r}, want pinned Synto 0.7.0")


def main() -> None:
    if synto.__version__ != "0.7.0":
        raise AssertionError(f"loaded Synto {synto.__version__}, want pinned 0.7.0")
    if not os.environ.get("TMPDIR"):
        raise AssertionError("TMPDIR must point at the supplied task scratch directory")
    for key in PROVIDER_KEYS:
        if os.environ.get(key):
            raise AssertionError(f"{key} must be unset for this offline producer regression")
    output = os.environ.get("LWC426_ARTIFACT_OUTPUT")
    if not output:
        raise AssertionError("LWC426_ARTIFACT_OUTPUT is required")

    load_worker_adapter()
    content = "Same generated body for both compile runs."
    article = SingleArticle(title="Alpha", content=content, tags=["fixture"])
    pipeline = PipelineVersion(fast_model="controlled-local", heavy_model="controlled-local")
    with tempfile.TemporaryDirectory(prefix="lwc426-synto-producer-", dir=os.environ["TMPDIR"]) as temp:
        vault = Path(temp)
        (vault / ".synto").mkdir()
        config = Config(vault=vault)
        db = StateDB(vault / ".synto" / "state.db")
        try:
            draft = compile_pipeline._write_draft(
                article,
                config,
                [],
                db,
                confidence=0.95,
                existing_meta={"lineage": [OLD_GENERATOR]},
                run_ulid=RUN_1,
                pipeline=pipeline,
            )
            meta_1, body_1 = parse_note(draft)
            lineage_1 = meta_1.get("lineage")
            if not isinstance(lineage_1, list) or len(lineage_1) != 2:
                raise AssertionError(f"first lineage={lineage_1!r}")
            if lineage_1[0] != OLD_GENERATOR:
                raise AssertionError(f"carried generator changed: {lineage_1[0]!r}")
            generator_entry(lineage_1[-1], RUN_1)

            compile_pipeline._write_draft(
                article,
                config,
                [],
                db,
                confidence=0.95,
                existing_meta=meta_1,
                run_ulid=RUN_2,
                pipeline=pipeline,
            )
            meta_2, body_2 = parse_note(draft)
            lineage_2 = meta_2.get("lineage")
            if body_1 != body_2:
                raise AssertionError("identical-body regeneration changed the article body")
            if not isinstance(lineage_2, list) or len(lineage_2) != 3:
                raise AssertionError(f"second lineage={lineage_2!r}")
            if lineage_2[0] != OLD_GENERATOR:
                raise AssertionError("regeneration changed carried historical identity")
            generator_entry(lineage_2[-2], RUN_1)
            generator_entry(lineage_2[-1], RUN_2)

            published = compile_pipeline.publish_drafts(config, db, paths=[draft])
            if len(published) != 1:
                raise AssertionError(f"published {published!r}, expected one article")
            final_meta, final_body = parse_note(published[0])
            if final_meta.get("lineage") != lineage_2:
                raise AssertionError("Synto publication changed provenance")
            if final_body != compile_pipeline._strip_draft_annotations(body_2):
                raise AssertionError("Synto publication changed article body beyond draft annotation removal")
            output_path = Path(output)
            output_path.parent.mkdir(parents=True, exist_ok=True)
            output_path.write_bytes(published[0].read_bytes())
        finally:
            db.close()

    print("LWC426_PINNED_SYNTO_WRITER=PASS")
    print("LWC426_IDENTICAL_BODY_REGENERATION=PASS")
    print("LWC426_SYNTO_PUBLICATION_PRESERVES_LINEAGE=PASS")


if __name__ == "__main__":
    main()
