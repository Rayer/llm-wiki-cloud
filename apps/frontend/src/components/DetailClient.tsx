'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ChevronLeft, FileText } from 'lucide-react';
import { MarkdownView } from './MarkdownView';
import { ErrorState, LoadingState, useErrorIdCopy } from './States';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { ApiError, getConcepts, getRawFilePreview, safeWikiRouteSegment, type WikiEntry, type WikiReference } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { primaryRawFileName, rawFileNameFromSource } from '@/lib/raw-file-name';
import { NavigationLink } from './NavigationBlocker';
import { SourceAnnotationEditor } from './SourceAnnotationEditor';
import { nextSourceDetailReloadVersion } from '@/lib/source-detail';
import { latestConceptProvenance } from '@/lib/concept-provenance';

type DetailClientProps = {
  slug: string;
  labelKey?: 'Source.plural' | 'Entry.plural';
  label?: string;
  backHref: string;
  load: (slug: string) => Promise<WikiEntry>;
  entryType?: 'source' | 'concept';
};

export function DetailClient(props: DetailClientProps) {
  return <DetailClientSession key={`${props.entryType ?? ''}:${props.slug}`} {...props} />;
}

function DetailClientSession({ slug, labelKey, label: labelOverride, backHref, load, entryType }: DetailClientProps) {
  const { t } = useT();
  const label = labelOverride ?? t(labelKey ?? (entryType === 'source' ? 'Source.plural' : 'Entry.plural'));
  const [entry, setEntry] = useState<WikiEntry | null>(null);
  const [existingConceptSlugs, setExistingConceptSlugs] = useState<Set<string> | undefined>();
  const [reloadVersion, setReloadVersion] = useState(0);
  const [loadedVersion, setLoadedVersion] = useState<number | null>(null);
  const loading = loadedVersion !== reloadVersion;
  const [error, setError] = useState<{ version: number; message: string; requestId?: string; diagnosticId?: string } | null>(null);
  const reloadEntry = useCallback(() => setReloadVersion(nextSourceDetailReloadVersion), []);
  const currentError = error?.version === reloadVersion ? error : null;
  const { copyErrorId, errorIdCopied } = useErrorIdCopy(currentError, currentError?.requestId, currentError?.diagnosticId);

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
            requestId: err instanceof ApiError ? err.requestId : undefined,
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

  if (loading) return <LoadingState label={t('States.loadingDetail', { label })} />;
  if (currentError) {
    return (
      <ErrorState
        message={currentError.message}
          requestId={currentError.requestId}
        diagnosticId={currentError.diagnosticId}
          requestIdLabel={t('QueryErrors.requestId')}
        diagnosticIdLabel={t('QueryErrors.diagnosticId')}
          copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
          errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
          errorIdCopied={errorIdCopied}
          onCopyErrorId={copyErrorId}
      />
    );
  }
  if (!entry) return <ErrorState message={t('Detail.entryNotFound')} />;

  // LWC-139: prominent raw link on source pages (source_file / sources[0] / slug fallback)
  const primaryRawFile =
    entryType === 'source'
      ? entry.rawPath
        ? rawFileNameFromSource(entry.rawPath)
        : primaryRawFileName(entry.frontmatter, { slugFallback: entry.slug })
      : null;
  const rawMappingVerified = Boolean(entry.rawPath);

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
            <span className="text-xs font-medium text-zinc-500">{label}</span>
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

      {entryType === 'concept' ? <ConceptProvenance data={entry.frontmatter} /> : null}
      {entry.relatedConcepts !== undefined ? (
        <RelatedReferences
          titleKey="Detail.relatedConcepts"
          reasonKey="Detail.relatedConceptsReason"
          references={entry.relatedConcepts}
        />
      ) : null}
      {entry.relatedSources !== undefined ? (
        <RelatedReferences
          titleKey="Detail.relatedSources"
          reasonKey="Detail.relatedSourcesReason"
          references={entry.relatedSources}
        />
      ) : null}
      {entryType === 'source' ? (
        <RawFileSection rawPath={primaryRawFile} verified={rawMappingVerified} />
      ) : null}
      {entry.frontmatter ? <Frontmatter data={entry.frontmatter} /> : null}
      <MarkdownView content={entry.content} existingConceptSlugs={existingConceptSlugs} />
      {entryType === 'source' && entry.annotationAllowed && entry.id ? (
        <SourceAnnotationEditor key={entry.id} sourceId={entry.id} onSaved={reloadEntry} />
      ) : null}
    </div>
  );
}

function RelatedReferences({ titleKey, reasonKey, references }: {
  titleKey: 'Detail.relatedConcepts' | 'Detail.relatedSources';
  reasonKey: 'Detail.relatedConceptsReason' | 'Detail.relatedSourcesReason';
  references: WikiReference[];
}) {
  const { t } = useT();
  return (
    <Surface variant="glass" className="p-5">
      <h2 className="text-base font-semibold text-white">{t(titleKey)}</h2>
      <p className="mt-1 text-sm leading-6 text-zinc-400">{t(reasonKey)}</p>
      {references.length === 0 ? <p role="status" className="mt-3 text-sm text-zinc-400">{t('Detail.noRelatedEntries')}</p> : (
        <ul className="mt-4 grid gap-3 sm:grid-cols-2">
          {references.map((reference) => {
            const id = safeWikiRouteSegment(reference.id);
            const slug = safeWikiRouteSegment(reference.slug);
            const collection = reference.type === 'concept' ? 'concepts' : 'sources';
            const href = id
              ? `/${collection}/${encodeURIComponent(id)}${slug ? `-${encodeURIComponent(slug)}` : ''}`
              : reference.type === 'concept' && slug
                ? `/${collection}/${encodeURIComponent(slug)}`
                : null;
            return (
              <li key={`${reference.type}:${reference.id ?? reference.slug}`} className="min-w-0 rounded border border-white/10 bg-black/15 p-3">
                {href ? (
                  <NavigationLink href={href} className="break-words text-sm font-medium text-emerald-200 underline decoration-emerald-200/30 underline-offset-4 hover:text-emerald-100">
                    {reference.title}
                  </NavigationLink>
                ) : <span className="break-words text-sm text-zinc-300">{reference.title}</span>}
                <p className="mt-1 break-all font-mono text-xs text-zinc-500">{reference.slug}</p>
              </li>
            );
          })}
        </ul>
      )}
    </Surface>
  );
}

function RawFileSection({ rawPath, verified }: { rawPath: string | null; verified: boolean }) {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const mountedRef = useRef(false);
  const requestRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
    };
  }, []);

  const loadRaw = async () => {
    if (!rawPath) return;
    const request = ++requestRef.current;
    setLoading(true);
    setError('');
    try {
      const value = await getRawFilePreview(rawPath);
      if (mountedRef.current && requestRef.current === request) setContent(value);
    } catch (cause) {
      if (mountedRef.current && requestRef.current === request) {
        setError(cause instanceof Error ? cause.message : t('Source.rawPreviewFailed'));
      }
    } finally {
      if (mountedRef.current && requestRef.current === request) setLoading(false);
    }
  };

  const togglePreview = () => {
    if (!open) {
      setOpen(true);
      if (content === null && !loading) void loadRaw();
      return;
    }
    requestRef.current += 1;
    setLoading(false);
    setOpen(false);
  };

  return (
    <Surface variant="glass" className="border border-emerald-200/15 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-white">{t('Detail.rawSectionTitle')}</h2>
          {rawPath ? <code className="mt-1 block break-all font-mono text-xs text-zinc-400">{rawPath}</code> : null}
          {verified ? <p className="mt-2 text-xs text-emerald-100">{t('Detail.rawMappingVerified')}</p> : rawPath ? <p className="mt-2 text-xs text-amber-100">{t('Detail.rawMappingUnverified')}</p> : <p className="mt-2 text-sm text-zinc-400">{t('Detail.rawMappingUnavailable')}</p>}
        </div>
        {rawPath ? (
          <div className="flex flex-wrap gap-2">
            <button type="button" aria-expanded={open} onClick={togglePreview} className="min-h-11 rounded border border-emerald-200/25 px-3 text-sm font-medium text-emerald-100 hover:bg-emerald-200/5">
              {open ? t('Detail.closeRawPreview') : t('Source.previewRaw')}
            </button>
            <NavigationLink href={`/raw?file=${encodeURIComponent(rawPath)}`} className="inline-flex min-h-11 items-center rounded border border-white/15 px-3 text-sm text-zinc-200 hover:bg-white/5">
              {t('Detail.openRawLibrary')}
            </NavigationLink>
          </div>
        ) : null}
      </div>
      {open && loading ? <p role="status" className="mt-4 text-sm text-zinc-400">{t('States.loadingDetail', { label: t('Detail.rawSectionTitle') })}</p> : null}
      {open && error ? <div className="mt-4 flex flex-wrap items-center gap-3"><p role="alert" className="text-sm text-amber-100">{error}</p><button type="button" onClick={() => void loadRaw()} className="min-h-11 rounded border border-white/15 px-3 text-sm text-zinc-100">{t('Detail.retryRawPreview')}</button></div> : null}
      {open && content !== null ? <pre className="mt-4 max-h-[70dvh] overflow-auto whitespace-pre-wrap break-words rounded border border-white/10 bg-black/30 p-4 font-mono text-xs leading-5 text-zinc-200">{content}</pre> : null}
    </Surface>
  );
}

function ConceptProvenance({ data }: { data?: Record<string, unknown> }) {
  const { t } = useT();
  const provenance = latestConceptProvenance(data);
  const value = (current: string | null) => current ?? t('ConceptProvenance.unknown');

  return (
    <Surface variant="glass" className="p-5" data-testid="concept-generation-provenance">
      <h2 className="text-xs font-semibold text-zinc-500">
        {t('ConceptProvenance.title')}
      </h2>
      <dl className="mt-4 grid gap-4 sm:grid-cols-2">
        <div>
          <dt className="text-xs text-zinc-600">{t('ConceptProvenance.implementation')}</dt>
          <dd className="mt-1 break-words text-sm text-zinc-200">{value(provenance.implementation)}</dd>
        </div>
        <div>
          <dt className="text-xs text-zinc-600">{t('ConceptProvenance.version')}</dt>
          <dd className="mt-1 break-words text-sm text-zinc-200">{value(provenance.version)}</dd>
        </div>
        <div>
          <dt className="text-xs text-zinc-600">{t('ConceptProvenance.revision')}</dt>
          <dd className="mt-1 break-words text-sm text-zinc-200">{value(provenance.revision)}</dd>
        </div>
        <div>
          <dt className="text-xs text-zinc-600">{t('ConceptProvenance.compileRun')}</dt>
          <dd className="mt-1 break-words text-sm text-zinc-200">{value(provenance.compileRun)}</dd>
        </div>
      </dl>
    </Surface>
  );
}

function Frontmatter({ data }: { data: Record<string, unknown> }) {
  const entries = Object.entries(data).filter(([, value]) => value !== undefined && value !== null);
  if (entries.length === 0) return null;

  return (
    <Surface variant="glass" className="p-5">
      <h2 className="text-xs font-semibold text-zinc-500">
        Metadata
      </h2>
      <dl className="mt-4 grid gap-4 sm:grid-cols-2">
        {entries.map(([key, value]) => (
          <div key={key}>
            <dt className="text-xs text-zinc-600">{key}</dt>
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
          {sources.map((source) => <li key={source} className="break-all font-mono text-xs">{source}</li>)}
        </ul>
      );
    }
  }

  // OLW sources use source_file: raw/….md，link like the sources list (LWC-139).
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
