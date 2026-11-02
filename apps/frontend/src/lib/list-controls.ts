import type { SourceListItem, WikiEntry } from './api';

const collator = new Intl.Collator('zh-Hant', { numeric: true, sensitivity: 'base' });
export function entryDate(entry: WikiEntry): number | null {
  const value = entry.date ? Date.parse(entry.date) : NaN;
  return Number.isFinite(value) ? value : null;
}

export function entryObjectUpdatedAt(entry: WikiEntry): number | null {
  const value = entry.objectUpdatedAt ? Date.parse(entry.objectUpdatedAt) : NaN;
  return Number.isFinite(value) ? value : null;
}

export function selectEntries(entries: WikiEntry[], search: string, status: string, dates: string, order: string) {
  const query = search.trim().toLocaleLowerCase();
  return entries.filter((entry) =>
    [entry.title, entry.slug, entry.description ?? ''].some((value) => value.toLocaleLowerCase().includes(query))
    && (status === 'all' || (status === 'missing' ? !entry.status : entry.status === status))
    && (dates === 'all' || (entryDate(entry) !== null) === (dates === 'dated')),
  ).sort((a, b) => {
    if (order === 'updated-desc') {
      const left = entryObjectUpdatedAt(a);
      const right = entryObjectUpdatedAt(b);
      if (left === null || right === null) return left === right ? collator.compare(a.title, b.title) || collator.compare(a.slug, b.slug) : left === null ? 1 : -1;
      return right - left || collator.compare(a.title, b.title) || collator.compare(a.slug, b.slug);
    }
    if (order === 'date-desc' || order === 'date-asc') {
      const left = entryDate(a);
      const right = entryDate(b);
      // Missing dates stay last in both directions. A data date is not an update timestamp.
      if (left === null || right === null) return left === right ? collator.compare(a.title, b.title) : left === null ? 1 : -1;
      return (order === 'date-desc' ? right - left : left - right) || collator.compare(a.title, b.title);
    }
    const left = order === 'slug' ? a.slug : a.title;
    const right = order === 'slug' ? b.slug : b.title;
    return collator.compare(left, right) * (order === 'title-desc' ? -1 : 1);
  });
}

export function selectSources(sources: SourceListItem[], search: string, lifecycle: string, annotation: string, order: string) {
  const query = search.trim().toLocaleLowerCase();
  return sources.filter((source) =>
    [source.title, source.rawPath].some((value) => value.toLocaleLowerCase().includes(query))
    && (lifecycle === 'all' || source.lifecycle === lifecycle)
    && (annotation === 'all' || source.annotationPresent === (annotation === 'present')),
  ).sort((a, b) => collator.compare(order === 'path' ? a.rawPath : a.title, order === 'path' ? b.rawPath : b.title) * (order === 'title-desc' ? -1 : 1));
}
