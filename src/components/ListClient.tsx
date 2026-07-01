'use client';

import { useEffect, useMemo, useState } from 'react';
import { EntryCard } from './EntryCard';
import { EmptyState, ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';
import type { WikiEntry } from '@/lib/api';

// Client-side cache: avoids re-fetching on every navigation.
// Cleared on page refresh; BFF remains stateless.
const clientCache = new Map<string, WikiEntry[]>();

export function ListClient({
  title,
  description,
  load,
  basePath,
}: {
  title: string;
  description: string;
  load: () => Promise<WikiEntry[]>;
  basePath: string;
}) {
  const { currentProject } = useWorkspace();
  const cacheKey = `${currentProject?.id ?? 'no-project'}:${basePath}`;
  const [entries, setEntries] = useState<WikiEntry[]>(clientCache.get(cacheKey) ?? []);
  const [loading, setLoading] = useState(!clientCache.has(cacheKey));
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');

  useEffect(() => {
    let ignore = false;
    const cachedEntries = clientCache.get(cacheKey);
    if (cachedEntries) {
      setEntries(cachedEntries);
      setLoading(false);
      setError('');
      return;
    }

    setEntries([]);
    setLoading(true);
    setError('');
    load()
      .then((data) => {
        if (ignore) return;
        console.log(`[ListClient] loaded ${basePath}: ${data.length} items`, data.slice(0,1));
        if (data.length === 0) clientCache.delete(cacheKey);  // don't cache empty
        else clientCache.set(cacheKey, data);
        setEntries(data);
      })
      .catch((err: Error) => {
        console.error(`[ListClient] ${basePath} failed:`, err);
        clientCache.delete(cacheKey);  // clear on error
        if (!ignore) setError(err.message);
      })
      .finally(() => {
        if (!ignore) setLoading(false);
      });

    return () => {
      ignore = true;
    };
  }, [load, basePath, cacheKey, currentProject]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return entries;
    return entries.filter(
      (e) =>
        e.title.toLowerCase().includes(q) ||
        e.slug.toLowerCase().includes(q)
    );
  }, [entries, search]);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-4xl font-semibold text-white">{title}</h1>
        <p className="mt-3 max-w-2xl text-zinc-400">{description}</p>
      </header>

      <div className="flex items-center gap-3">
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={`Search ${title.toLowerCase()}...`}
          className="flex-1 rounded-lg border border-white/10 bg-[#151515] px-4 py-2.5 text-sm text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-300"
        />
        {search.trim() ? (
          <span className="text-sm text-zinc-500 tabular-nums whitespace-nowrap">
            {filtered.length} of {entries.length}
          </span>
        ) : (
          <span className="text-sm text-zinc-600 tabular-nums whitespace-nowrap">
            {entries.length}
          </span>
        )}
      </div>

      {loading ? <LoadingState /> : null}
      {error ? <ErrorState message={error} /> : null}
      {!loading && !error && filtered.length === 0 ? (
        <EmptyState
          message={
            search.trim()
              ? `No ${title.toLowerCase()} match "${search.trim()}".`
              : `No ${title.toLowerCase()} were returned by the API.`
          }
        />
      ) : null}

      <div className="grid gap-4 md:grid-cols-2">
        {filtered.map((entry) => (
          <EntryCard key={entry.slug} entry={entry} href={entry.id ? `${basePath}/${entry.id}` : `${basePath}/${encodeURIComponent(entry.slug)}`} />
        ))}
      </div>
    </div>
  );
}
