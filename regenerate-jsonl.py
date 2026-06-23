#!/usr/bin/env python3
"""Regenerate cache/concepts.jsonl after OLW pipeline run.

Reads all concept .md files from the vault/wiki/ directory, extracts
slug/title/body/frontmatter/sources, and writes as JSONL to cache/.
"""
import json
import os
import sys

VAULT = os.environ.get("VAULT", "")
if not VAULT:
    print("VAULT env var required", file=sys.stderr)
    sys.exit(1)

WIKI_DIR = os.path.join(VAULT, "wiki")
CACHE_DIR = os.path.join(VAULT, "cache")
JSONL_PATH = os.path.join(CACHE_DIR, "concepts.jsonl")

if not os.path.isdir(WIKI_DIR):
    print(f"wiki dir not found: {WIKI_DIR}", file=sys.stderr)
    sys.exit(1)

entries = []

for filename in os.listdir(WIKI_DIR):
    if not filename.endswith(".md"):
        continue
    path = os.path.join(WIKI_DIR, filename)
    with open(path) as f:
        raw = f.read()

    slug = filename[:-3]
    frontmatter, body = parse_frontmatter(raw)
    title = frontmatter.get("title", slug)
    sources = parse_sources(frontmatter)

    entries.append({
        "slug": slug,
        "title": title,
        "body": body.strip()[:10000],  # cap at 10KB
        "frontmatter": frontmatter,
        "sources": sources,
    })

os.makedirs(CACHE_DIR, exist_ok=True)
with open(JSONL_PATH, "w") as f:
    for e in entries:
        f.write(json.dumps(e, ensure_ascii=False) + "\n")

print(f"Regenerated {len(entries)} concepts → {JSONL_PATH}")


def parse_frontmatter(raw: str) -> tuple[dict, str]:
    fm = {}
    if not raw.startswith("---\n"):
        return fm, raw
    content = raw[4:]
    end = content.find("\n---")
    if end < 0:
        return fm, raw
    for line in content[:end].split("\n"):
        line = line.strip()
        if not line or ":" not in line:
            continue
        key, _, val = line.partition(":")
        key = key.strip()
        val = val.strip().strip("'\"")
        fm[key] = val
    return fm, content[end + 4:]


def parse_sources(fm: dict) -> list[str]:
    for key in ("sources", "source"):
        if key in fm:
            val = fm[key].strip("[] ")
            return [s.strip() for s in val.split(",") if s.strip()]
    return []
