'use client';

import Link from 'next/link';
import { useEffect, useState } from 'react';
import { MarkdownView } from './MarkdownView';
import { ErrorState, LoadingState } from './States';
import type { WikiEntry } from '@/lib/api';

export function DetailClient({
  slug,
  label,
  backHref,
  load,
}: {
  slug: string;
  label: string;
  backHref: string;
  load: (slug: string) => Promise<WikiEntry>;
}) {
  const [entry, setEntry] = useState<WikiEntry | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    load(slug)
      .then(setEntry)
      .catch((err: Error) => setError(err.message))
      .finally(() => setLoading(false));
  }, [load, slug]);

  if (loading) return <LoadingState label={`Loading ${label}`} />;
  if (error) return <ErrorState message={error} />;
  if (!entry) return <ErrorState message="Entry not found." />;

  return (
    <div className="space-y-8">
      <Link href={backHref} className="text-sm font-medium text-emerald-300 hover:text-emerald-200">
        Back to {label}
      </Link>

      <header className="border-b border-white/10 pb-6">
        <div className="text-sm font-semibold uppercase tracking-[0.25em] text-zinc-500">
          {label}
        </div>
        <h1 className="mt-3 text-4xl font-semibold text-white sm:text-5xl">{entry.title}</h1>
        {entry.description ? (
          <p className="mt-4 max-w-3xl text-lg leading-8 text-zinc-400">{entry.description}</p>
        ) : null}
      </header>

      {entry.frontmatter ? <Frontmatter data={entry.frontmatter} /> : null}
      <MarkdownView content={entry.content} />
    </div>
  );
}

function Frontmatter({ data }: { data: Record<string, unknown> }) {
  const entries = Object.entries(data).filter(([, value]) => value !== undefined && value !== null);
  if (entries.length === 0) return null;

  return (
    <section className="rounded-lg border border-white/10 bg-[#151515] p-5">
      <h2 className="text-sm font-semibold uppercase tracking-[0.2em] text-zinc-500">
        Frontmatter
      </h2>
      <dl className="mt-4 grid gap-4 sm:grid-cols-2">
        {entries.map(([key, value]) => (
          <div key={key}>
            <dt className="text-xs uppercase tracking-[0.16em] text-zinc-500">{key}</dt>
            <dd className="mt-1 break-words text-sm text-zinc-200">
              {typeof value === 'string' || typeof value === 'number'
                ? value
                : JSON.stringify(value)}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
