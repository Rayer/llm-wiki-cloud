'use client';

import { useEffect, useState } from 'react';
import { getStatus, type ApiStatus } from '@/lib/api';
import { ErrorState, LoadingState } from './States';

export function StatusClient() {
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch((err: Error) => setError(err.message))
      .finally(() => setLoading(false));
  }, []);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-4xl font-semibold text-white">Pipeline status</h1>
        <p className="mt-3 max-w-2xl text-zinc-400">
          Current BFF-reported counts and pipeline metadata.
        </p>
      </header>

      {loading ? <LoadingState label="Loading status" /> : null}
      {error ? <ErrorState message={error} /> : null}
      {status ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Metric label="Sources" value={status.sourcesCount} />
            <Metric label="Concepts" value={status.conceptsCount} />
          </div>
          <section className="rounded-lg border border-white/10 bg-[#151515] p-5">
            <h2 className="text-sm font-semibold uppercase tracking-[0.2em] text-zinc-500">
              Raw status
            </h2>
            <pre className="mt-4 overflow-x-auto rounded-md bg-black/50 p-4 text-sm text-zinc-300">
              {JSON.stringify(status.raw, null, 2)}
            </pre>
          </section>
        </>
      ) : null}
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-white/10 bg-[#1a1a1a] p-6">
      <div className="text-sm text-zinc-500">{label}</div>
      <div className="mt-2 text-5xl font-semibold text-white">{value}</div>
    </div>
  );
}
