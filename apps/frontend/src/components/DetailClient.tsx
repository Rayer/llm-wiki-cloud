'use client';

import { useCallback, useEffect, useState } from 'react';
import { ChevronLeft, FileText } from 'lucide-react';
import { MarkdownView } from './MarkdownView';
import { ErrorState, LoadingState, useDiagnosticIdCopy } from './States';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { ApiError, getConcepts, type WikiEntry } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { primaryRawFileName, rawFileNameFromSource } from '@/lib/raw-file-name';
import { NavigationLink } from './NavigationBlocker';
import { SourceAnnotationEditor } from './SourceAnnotationEditor';
import { nextSourceDetailReloadVersion } from '@/lib/source-detail';

type DetailClientProps = {
  slug: string;
  label: string;
  backHref: string;
  load: (slug: string) => Promise<WikiEntry>;
  entryType?: 'source' | 'concept';
};

export function DetailClient(props: DetailClientProps) {
  return <DetailClientSession key={`${props.entryType ?? ''}:${props.slug}`} {...props} />;
}

function DetailClientSession({ slug, label, backHref, load, entryType }: DetailClientProps) {
  const { t } = useT();
  const [entry, setEntry] = useState<WikiEntry | null>(null);
  const [existingConceptSlugs, setExistingConceptSlugs] = useState<Set<string> | undefined>();
  const [reloadVersion, setReloadVersion] = useState(0);
  const [loadedVersion, setLoadedVersion] = useState<number | null>(null);
  const loading = loadedVersion !== reloadVersion;
  const [error, setError] = useState<{ version: number; message: string; diagnosticId?: string } | null>(null);
  const reloadEntry = useCallback(() => setReloadVersion(nextSourceDetailReloadVersion), []);
  const currentError = error?.version === reloadVersion ? error : null;
  const { copyDiagnosticId, diagnosticIdCopied } = useDiagnosticIdCopy(currentError, currentError?.diagnosticId);

  useEffect(() => {
    let cancelled = false;

    const entryPromise = load(slug);
    // LWC-119: concept pages need the slug set too (LWC-101 only wired sources)
    const conceptsPromise =
      entryType === 'source' || entryType === 'concept'
        ? getConcepts().then((concepts) => new Set(concepts.map((concept) => concept.slug)))
        : Promise.resolve(undefined);

    Promise.all([entryPromise, conceptsPromise])
      .then(([loadedEntry, conceptSlugs]) => {
        if (cancelled) return;
        setEntry(loadedEntry);
        setExistingConceptSlugs(conceptSlugs);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError({
            version: reloadVersion,
            message: err instanceof Error ? err.message : String(err),
            diagnosticId: err instanceof ApiError ? err.diagnosticId : undefined,
          });
        }
      })
      .finally(() => {
        if (!cancelled) setLoadedVersion(reloadVersion);
      });

    return () => {
      cancelled = true;
    };
  }, [entryType, load, reloadVersion, slug]);

  if (loading) return <LoadingState label={`Loading ${label}`} />;
  if (currentError) {
    return (
      <ErrorState
        message={currentError.message}
        diagnosticId={currentError.diagnosticId}
        diagnosticIdLabel={t('QueryErrors.diagnosticId')}
        copyDiagnosticIdLabel={t('QueryErrors.copyDiagnosticId')}
        diagnosticIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
        diagnosticIdCopied={diagnosticIdCopied}
        onCopyDiagnosticId={copyDiagnosticId}
      />
    );
  }
  if (!entry) return <ErrorState message="Entry not found." />;

  // LWC-139: prominent raw link on source pages (source_file / sources[0] / slug fallback)
  const primaryRawFile =
    entryType === 'source'
      ? primaryRawFileName(entry.frontmatter, { slugFallback: entry.slug })
      : null;

  return (
    <div className="space-y-8">
      <NavigationLink
        href={backHref}
        className="inline-flex items-center gap-1.5 text-sm font-medium text-zinc-400 transition hover:text-emerald-300"
      >
        <ChevronLeft className="size-4" />
        {t('Detail.backTo', { label })}
      </NavigationLink>

      <header className="border-b border-white/10 pb-6">
        <div className="flex flex-wrap items-center gap-2">
          {entryType ? (
            <Badge variant={entryType}>
              {entryType === 'source' ? t('Source.singular') : t('Entry.singular')}
            </Badge>
          ) : (
            <span className="text-xs font-medium uppercase tracking-wider text-zinc-500">{label}</span>
          )}
          {entry.status === 'draft' ? <Badge variant="draft">{t('Entry.draft')}</Badge> : null}
          {entry.status === 'published' ? <Badge variant="published">{t('Entry.published')}</Badge> : null}
        </div>
        <h1 className="mt-3 text-3xl font-semibold tracking-tight text-white sm:text-4xl">
          {entry.title}
        </h1>
        {entry.description ? (
          <p className="mt-4 max-w-3xl text-lg leading-8 text-zinc-400">{entry.description}</p>
        ) : null}
        {primaryRawFile ? (
          <NavigationLink
            href={`/raw?file=${encodeURIComponent(primaryRawFile)}`}
            className="mt-4 inline-flex items-center gap-1.5 text-sm font-medium text-emerald-300 underline decoration-emerald-300/40 underline-offset-4 transition hover:text-emerald-200 hover:decoration-emerald-200"
            data-testid="source-raw-file-link"
          >
            <FileText className="size-4 shrink-0" aria-hidden="true" />
            {t('Detail.rawFile')}
            <span className="font-normal text-zinc-500 no-underline">({primaryRawFile})</span>
          </NavigationLink>
        ) : null}
      </header>

      {entry.frontmatter ? <Frontmatter data={entry.frontmatter} /> : null}
      <MarkdownView content={entry.content} existingConceptSlugs={existingConceptSlugs} />
      {entryType === 'source' && entry.annotationAllowed && entry.id ? (
        <SourceAnnotationEditor key={entry.id} sourceId={entry.id} onSaved={reloadEntry} />
      ) : null}
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
              {renderFrontmatterValue(key, value)}
            </dd>
          </div>
        ))}
      </dl>
    </Surface>
  );
}

function renderFrontmatterValue(key: string, value: unknown) {
  if (key === 'sources' && Array.isArray(value)) {
    const sources = value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0);
    if (sources.length > 0) {
      return (
        <ul className="space-y-1">
          {sources.map((source) => (
            <li key={source}>
              <NavigationLink
                href={`/raw?file=${encodeURIComponent(rawFileNameFromSource(source))}`}
                className="text-emerald-300 underline hover:text-emerald-200"
              >
                {source}
              </NavigationLink>
            </li>
          ))}
        </ul>
      );
    }
  }

  // OLW sources use source_file: raw/….md — link like the sources list (LWC-139).
  if ((key === 'source_file' || key === 'source') && typeof value === 'string' && value.trim()) {
    const fileName = rawFileNameFromSource(value);
    return (
      <NavigationLink
        href={`/raw?file=${encodeURIComponent(fileName)}`}
        className="text-emerald-300 underline hover:text-emerald-200"
      >
        {value}
      </NavigationLink>
    );
  }

  if (typeof value === 'string' && /^https?:\/\//.test(value)) {
    return (
      <a
        href={value}
        target="_blank"
        rel="noreferrer"
        className="text-emerald-300 underline hover:text-emerald-200"
      >
        {value}
      </a>
    );
  }

  if (typeof value === 'string' || typeof value === 'number') {
    return String(value);
  }

  return JSON.stringify(value);
}
