/** Strip leading `raw/` from a frontmatter source path while keeping subdirectories. */
export function rawFileNameFromSource(source: string): string {
  const path = source;
  return path.startsWith('raw/') ? path.slice('raw/'.length) : path;
}

export function isValidRawRelativePath(value: string): boolean {
  if (!value || new TextEncoder().encode(value).byteLength > 1024 || value.startsWith('/') || value.includes('\\')) {
    return false;
  }
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(value) || (value.length >= 2 && value[1] === ':')) {
    return false;
  }
  if (value.split('/').some((segment) => segment === '' || segment === '.' || segment === '..')) {
    return false;
  }
  try {
    encodeURIComponent(value);
    return true;
  } catch {
    return false;
  }
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
