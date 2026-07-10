/** Strip leading `raw/` from a frontmatter source path. */
export function rawFileNameFromSource(source: string): string {
  return source.replace(/^raw\//, '');
}

/** First frontmatter.sources entry as a raw/ basename, or null. */
export function primaryRawFileName(frontmatter?: Record<string, unknown>): string | null {
  const sources = frontmatter?.sources;
  if (!Array.isArray(sources)) return null;
  const first = sources.find(
    (item): item is string => typeof item === 'string' && item.trim().length > 0,
  );
  if (!first) return null;
  return rawFileNameFromSource(first);
}
