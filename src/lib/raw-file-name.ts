/** Strip leading `raw/` from a frontmatter source path. */
export function rawFileNameFromSource(source: string): string {
  return source.replace(/^raw\//, '').trim();
}

function asNonEmptyString(value: unknown): string | null {
  return typeof value === 'string' && value.trim().length > 0 ? value.trim() : null;
}

/**
 * Resolve the originating raw filename for a source page.
 *
 * Priority (LWC-139 + real OLW frontmatter):
 * 1. `frontmatter.sources[0]` (array form used in some docs / concepts)
 * 2. `frontmatter.source_file` (OLW compiled sources — production)
 * 3. `frontmatter.source` (singular string path, if present)
 * 4. optional `slugFallback` → `{slug}.md` when no frontmatter path exists
 */
export function primaryRawFileName(
  frontmatter?: Record<string, unknown>,
  options?: { slugFallback?: string },
): string | null {
  const sources = frontmatter?.sources;
  if (Array.isArray(sources)) {
    const first = sources.find(
      (item): item is string => typeof item === 'string' && item.trim().length > 0,
    );
    if (first) return rawFileNameFromSource(first);
  }

  for (const key of ['source_file', 'source'] as const) {
    const value = asNonEmptyString(frontmatter?.[key]);
    if (value) return rawFileNameFromSource(value);
  }

  const slug = asNonEmptyString(options?.slugFallback);
  if (slug) {
    const base = slug.endsWith('.md') ? slug : `${slug}.md`;
    return rawFileNameFromSource(base);
  }

  return null;
}
