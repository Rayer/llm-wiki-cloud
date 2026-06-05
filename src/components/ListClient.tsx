'use client';

import { useEffect, useState } from 'react';
import { EntryCard } from './EntryCard';
import { EmptyState, ErrorState, LoadingState } from './States';
import type { WikiEntry } from '@/lib/api';

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
  const [entries, setEntries] = useState<WikiEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    load()
      .then(setEntries)
      .catch((err: Error) => setError(err.message))
      .finally(() => setLoading(false));
  }, [load]);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-4xl font-semibold text-white">{title}</h1>
        <p className="mt-3 max-w-2xl text-zinc-400">{description}</p>
      </header>

      {loading ? <LoadingState /> : null}
      {error ? <ErrorState message={error} /> : null}
      {!loading && !error && entries.length === 0 ? (
        <EmptyState message={`No ${title.toLowerCase()} were returned by the API.`} />
      ) : null}

      <div className="grid gap-4 md:grid-cols-2">
        {entries.map((entry) => (
          <EntryCard key={entry.slug} entry={entry} href={`${basePath}/${entry.slug}`} />
        ))}
      </div>
    </div>
  );
}
