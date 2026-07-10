# LWC-139 — Source page prominent raw file link

## Goal

On source detail pages, show a prominent **原始檔案** link near the title that opens the originating raw file (`/raw?file=…`), without requiring users to scroll into Metadata.

## Acceptance criteria

1. **Given** a source entry with `frontmatter.sources[0]` present  
   **When** the user opens `/sources/{slug}`  
   **Then** a visible **原始檔案** (en: **Raw file**) link appears in the header (near title/metadata), linking to `/raw?file={filename}` where filename is `sources[0]` with optional `raw/` prefix stripped.

2. **Given** a source entry with no usable `sources` list  
   **Then** the prominent link is **hidden** (no slug guessing).

3. **Given** a concept detail page  
   **Then** the prominent raw link is **not** shown.

4. Existing Metadata `sources` list links (LWC-137) remain unchanged.

5. Automated file tests cover presence of the header link path and source-only gating.

## Design

### Approach

Frontend-only. Extend `DetailClient` header when `entryType === 'source'`.

### Resolve raw filename

```ts
function primaryRawFileName(frontmatter?: Record<string, unknown>): string | null {
  const sources = frontmatter?.sources;
  if (!Array.isArray(sources)) return null;
  const first = sources.find((s): s is string => typeof s === 'string' && s.trim().length > 0);
  if (!first) return null;
  return rawFileNameFromSource(first); // strip leading raw/
}
```

### UI

- Placement: under the title (and description if any), still inside `<header>`.
- Style: text link with `FileText` or similar icon; emerald underline consistent with frontmatter source links.
- Label via i18n: `Detail.rawFile` → zh-TW `原始檔案`, en `Raw file`.

### Out of scope

- Multiple primary chips for all sources (Metadata list already shows all)
- Slug-based fallback when `sources` is empty
- BFF changes

## Complexity

~0.5h frontend.
