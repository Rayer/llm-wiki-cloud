'use client';

import Link from 'next/link';
import { FormEvent, useEffect, useState } from 'react';
import { getStatus, searchWiki, type ApiStatus, type SearchResult } from '@/lib/api';
import { EmptyState, ErrorState, LoadingState } from './States';

export function HomeClient() {
  const [query, setQuery] = useState('');
  const [mode, setMode] = useState<'wiki' | 'full'>('wiki');
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [statusError, setStatusError] = useState('');
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searched, setSearched] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch((err: Error) => setStatusError(err.message));
  }, []);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = query.trim();
    if (!trimmed) return;

    setLoading(true);
    setError('');
    setSearched(true);

    try {
      setResults(await searchWiki(trimmed, mode));
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
      setResults([]);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="space-y-10">
      <section className="grid gap-8 pt-6 lg:grid-cols-[1fr_320px] lg:items-end">
        <div>
          <div className="text-sm font-semibold uppercase tracking-[0.28em] text-emerald-300">
            LLM Wiki
          </div>
          <h1 className="mt-5 max-w-3xl text-5xl font-semibold leading-tight text-white sm:text-6xl">
            Search the AI knowledge base.
          </h1>
          <p className="mt-5 max-w-2xl text-lg leading-8 text-zinc-400">
            Browse source documents, distilled concepts, and pipeline state from the
            LLM Wiki backend.
          </p>
          <form onSubmit={onSubmit} className="mt-8 rounded-lg border border-white/10 bg-[#151515] p-3">
            <div className="flex flex-col gap-3 sm:flex-row">
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search topics, model behavior, evaluations..."
                className="min-h-12 flex-1 rounded-md border border-white/10 bg-black/40 px-4 text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-300"
              />
              <div className="grid grid-cols-2 rounded-md border border-white/10 bg-black/30 p-1 text-sm">
                {(['wiki', 'full'] as const).map((item) => (
                  <button
                    key={item}
                    type="button"
                    onClick={() => setMode(item)}
                    className={`rounded px-4 py-2 font-medium capitalize transition ${
                      mode === item ? 'bg-emerald-300 text-black' : 'text-zinc-300 hover:text-white'
                    }`}
                  >
                    {item}
                  </button>
                ))}
              </div>
              <button
                type="submit"
                className="min-h-12 rounded-md bg-white px-6 font-semibold text-black transition hover:bg-emerald-200"
              >
                Search
              </button>
            </div>
          </form>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-1">
          <StatCard label="Sources" value={status?.sourcesCount} error={statusError} />
          <StatCard label="Concepts" value={status?.conceptsCount} error={statusError} />
        </div>
      </section>

      <section className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <h2 className="text-2xl font-semibold text-white">Search results</h2>
          {searched ? <span className="text-sm text-zinc-500">{mode} mode</span> : null}
        </div>
        {loading ? <LoadingState label="Searching" /> : null}
        {error ? <ErrorState message={error} /> : null}
        {!loading && !error && searched && results.length === 0 ? (
          <EmptyState message="No results matched that query." />
        ) : null}
        <div className="grid gap-4 md:grid-cols-2">
          {results.map((result) => {
            const collection = result.type === 'concept' ? 'concepts' : 'sources';
            return (
              <Link
                key={`${collection}-${result.slug}`}
                href={`/${collection}/${result.slug}`}
                className="rounded-lg border border-white/10 bg-[#1a1a1a] p-5 transition hover:border-emerald-300/50 hover:bg-[#202020]"
              >
                <div className="flex items-center justify-between gap-4">
                  <h3 className="text-lg font-semibold text-white">{result.title}</h3>
                  {result.score !== undefined ? (
                    <span className="text-xs text-zinc-500">{result.score.toFixed(2)}</span>
                  ) : null}
                </div>
                <p className="mt-3 line-clamp-4 text-sm leading-6 text-zinc-400">
                  {result.excerpt ?? result.description ?? 'Open this wiki entry.'}
                </p>
                <div className="mt-4 text-xs font-medium text-emerald-300">
                  {collection}/{result.slug}
                </div>
              </Link>
            );
          })}
        </div>
      </section>
    </div>
  );
}

function StatCard({
  label,
  value,
  error,
}: {
  label: string;
  value?: number;
  error?: string;
}) {
  return (
    <div className="rounded-lg border border-white/10 bg-[#1a1a1a] p-5">
      <div className="text-sm text-zinc-500">{label}</div>
      <div className="mt-2 text-4xl font-semibold text-white">
        {error ? '-' : value ?? '...'}
      </div>
      {error ? <div className="mt-2 text-xs text-red-300">{error}</div> : null}
    </div>
  );
}
