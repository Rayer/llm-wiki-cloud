# LWC-139 — Source page prominent raw file link (revised)

## Goal

On source detail pages, show a prominent **原始檔案** link near the title that opens the originating raw file (`/raw?file=…`).

## Why PR #25 was incomplete

Production OLW source frontmatter uses:

```yaml
source_file: raw/some-article.md
```

**not** `sources: [...]`.

PR #25 only resolved `frontmatter.sources[0]`, so the header link almost never appeared on real data. HermesPartner noted empty/missing `sources` in the API response; the underlying contract mismatch is the field name.

BFF `parseFrontmatter` already returns all YAML keys (including `source_file`) via `adrg/frontmatter`. No BFF change required for the common path.

## Acceptance criteria

1. Source with `source_file: raw/foo.md` → header **原始檔案** → `/raw?file=foo.md`
2. Source with `sources: [raw/a.md]` → same, using first entry
3. Source with neither, but with slug → fallback `/raw?file={slug}.md` (ticket)
4. Concept pages → no prominent raw link
5. Metadata `source_file` value is also a clickable raw link (same deep-link as list)
6. Tests cover `source_file` priority and slug fallback

## Resolution order

`primaryRawFileName(frontmatter, { slugFallback })`:

1. `sources[0]` (array)
2. `source_file` (OLW production)
3. `source` (singular string, if present)
4. `{slug}.md` when `slugFallback` provided

## UI

Unchanged from PR #25: header under title, i18n `Detail.rawFile`, filename in parentheses.

## Out of scope

- Changing OLW to emit `sources[]`
- Guaranteeing slug fallback matches GCS object when punctuation differs from `source_file`
