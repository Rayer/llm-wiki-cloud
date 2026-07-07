'use client';

import Link from 'next/link';
import { useEffect, useState } from 'react';
import { ChevronLeft } from 'lucide-react';
import { MarkdownView } from './MarkdownView';
import { ErrorState, LoadingState } from './States';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { getConcepts, type WikiEntry } from '@/lib/api';

export function DetailClient({
  slug,
  label,
  backHref,
  load,
  entryType,
}: {
  slug: string;
  label: string;
  backHref: string;
  load: (slug: string) => Promise<WikiEntry>;
  entryType?: 'source' | 'concept';
}) {
  const [entry, setEntry] = useState<WikiEntry | null>(null);
  const [existingConceptSlugs, setExistingConceptSlugs] = useState<Set<string> | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;

    const entryPromise = load(slug);
    const conceptsPromise =
      entryType === 'source'
        ? getConcepts().then((concepts) => new Set(concepts.map((concept) => concept.slug)))
        : Promise.resolve(undefined);

    Promise.all([entryPromise, conceptsPromise])
      .then(([loadedEntry, conceptSlugs]) => {
        if (cancelled) return;
        setEntry(loadedEntry);
        setExistingConceptSlugs(conceptSlugs);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [entryType, load, slug]);

  if (loading) return <LoadingState label={`Loading ${label}`} />;
  if (error) return <ErrorState message={error} />;
  if (!entry) return <ErrorState message="Entry not found." />;

  return (
    <div className="space-y-8">
      <Link
        href={backHref}
        className="inline-flex items-center gap-1.5 text-sm font-medium text-zinc-400 transition hover:text-emerald-300"
      >
        <ChevronLeft className="size-4" />
        Back to {label}
      </Link>

      <header className="border-b border-white/10 pb-6">
        <div className="flex flex-wrap items-center gap-2">
          {entryType ? (
            <Badge variant={entryType}>{entryType === 'source' ? 'Source' : 'Concept'}</Badge>
          ) : (
            <span className="text-xs font-medium uppercase tracking-wider text-zinc-500">{label}</span>
          )}
          {entry.status === 'draft' ? <Badge variant="draft">Draft</Badge> : null}
          {entry.status === 'published' ? <Badge variant="published">Published</Badge> : null}
        </div>
        <h1 className="mt-3 text-3xl font-semibold tracking-tight text-white sm:text-4xl">
          {entry.title}
        </h1>
        {entry.description ? (
          <p className="mt-4 max-w-3xl text-lg leading-8 text-zinc-400">{entry.description}</p>
        ) : null}
      </header>

      {entry.frontmatter ? <Frontmatter data={entry.frontmatter} /> : null}
      <MarkdownView content={entry.content} existingConceptSlugs={existingConceptSlugs} />
    </div>
  );
}

function Frontmatter({ data }: { data: Record<string, unknown> }) {
  const entries = Object.entries(data).filter(([, value]) => value !== undefined && value !== null);
  if (entries.length === 0) return null;

  return (
    <Surface variant="glass" className="p-5">
      <h2 className="text-xs font-semibold uppercase tracking-wider text-zinc-500">
        Metadata
      </h2>
      <dl className="mt-4 grid gap-4 sm:grid-cols-2">
        {entries.map(([key, value]) => (
          <div key={key}>
            <dt className="text-xs uppercase tracking-wider text-zinc-600">{key}</dt>
            <dd className="mt-1 break-words text-sm text-zinc-200">
              {typeof value === 'string' && /^https?:\/\//.test(value) ? (
                <a
                  href={value}
                  target="_blank"
                  rel="noreferrer"
                  className="text-emerald-300 underline hover:text-emerald-200"
                >
                  {value}
                </a>
              ) : typeof value === 'string' || typeof value === 'number' ? (
                String(value)
              ) : (
                JSON.stringify(value)
              )}
            </dd>
          </div>
        ))}
      </dl>
    </Surface>
  );
}