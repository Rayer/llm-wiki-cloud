'use client';

import { useEffect, useMemo, useState } from 'react';
import { EntryCard } from './EntryCard';
import { EmptyState, ErrorState, LoadingState, useErrorIdCopy } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { ApiError, type WikiEntry } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { selectEntries } from '@/lib/list-controls';
import { ListToolbar } from './ListToolbar';

// Client-side cache: avoids re-fetching on every navigation.
// Cleared on page refresh; BFF remains stateless.
const clientCache = new Map<string, WikiEntry[]>();

type ListError = { message: string; requestId?: string; diagnosticId?: string };

type ListClientProps = {
  title: string;
  description: string;
  load: () => Promise<WikiEntry[]>;
  basePath: string;
  entryType?: 'source' | 'concept';
};

export function ListClient(props: ListClientProps) {
  const { currentProject, token, sessionEpoch } = useWorkspace();
  const identityKey = JSON.stringify([currentProject?.id ?? 'no-project', token, sessionEpoch, props.basePath]);
  return <ListClientSession key={identityKey} {...props} />;
}

function ListClientSession({
  title,
  description,
  load,
  basePath,
  entryType,
}: ListClientProps) {
  const { t } = useT();
  const { currentProject, token, sessionEpoch } = useWorkspace();
  const cacheKey = JSON.stringify([currentProject?.id ?? 'no-project', token, sessionEpoch, basePath]);
  const [entries, setEntries] = useState<WikiEntry[]>(() => clientCache.get(cacheKey) ?? []);
  const [loading, setLoading] = useState(() => !clientCache.has(cacheKey));
  const [error, setError] = useState<ListError | null>(null);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('all');
  const [dates, setDates] = useState('all');
  const [order, setOrder] = useState('title');
  const { copyErrorId, errorIdCopied } = useErrorIdCopy(error, error?.requestId, error?.diagnosticId);

  useEffect(() => {
    let ignore = false;
    const cachedEntries = clientCache.get(cacheKey);
    if (cachedEntries) return () => { ignore = true; };
    load()
      .then((data) => {
        if (ignore) return;
        console.log(`[ListClient] loaded ${basePath}: ${data.length} items`, data.slice(0,1));
        if (data.length === 0) clientCache.delete(cacheKey);  // don't cache empty
        else clientCache.set(cacheKey, data);
        setEntries(data);
        setError(null);
      })
      .catch((cause: unknown) => {
        const err = cause instanceof Error ? cause : new Error(String(cause));
        console.error(`[ListClient] ${basePath} failed:`, err);
        clientCache.delete(cacheKey);  // clear on error
        if (!ignore) {
          setError({
            message: err.message,
            requestId: cause instanceof ApiError ? cause.requestId : undefined,
            diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
          });
        }
      })
      .finally(() => {
        if (!ignore) setLoading(false);
      });

    return () => { ignore = true; };
  }, [load, basePath, cacheKey]);

  const filtered = useMemo(() => selectEntries(entries, search, status, dates, order), [entries, search, status, dates, order]);
  const active = Boolean(search.trim() || status !== 'all' || dates !== 'all' || order !== 'title');

  const emptyMessage = search.trim()
    ? t('List.noSearchMatches', { title: title.toLowerCase(), query: search.trim() })
    : entryType === 'concept'
      ? t('List.noConcepts')
      : entryType === 'source'
        ? t('List.noSources')
        : t('List.noEntries');

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">{title}</h1>
        <p className="mt-2 max-w-2xl text-zinc-400">{description}</p>
      </header>

      <ListToolbar search={search} onSearch={setSearch} searchLabel={t('List.searchPlaceholder', { title: title.toLowerCase() })}
        count={filtered.length} total={entries.length} active={active}
        onClear={() => { setSearch(''); setStatus('all'); setDates('all'); setOrder('title'); }}
        filters={[
          { label: t('List.filterStatus'), value: status, onChange: setStatus, options: [
            { value: 'all', label: t('List.allStatuses') },
            ...[...new Set(entries.map((entry) => entry.status ?? 'missing'))].sort().map((value) => ({ value, label: value === 'published' ? t('Entry.published') : value === 'draft' ? t('Entry.draft') : value === 'missing' ? t('List.noStatus') : value })),
          ] },
          { label: t('List.filterDate'), value: dates, onChange: setDates, options: [
            { value: 'all', label: t('List.allDates') }, { value: 'dated', label: t('List.withDate') }, { value: 'undated', label: t('List.withoutDate') },
          ] },
        ]}
        order={{ label: t('List.orderBy'), value: order, onChange: setOrder, options: [
          { value: 'title', label: t('List.titleAsc') }, { value: 'title-desc', label: t('List.titleDesc') }, { value: 'slug', label: t('List.slugOrder') },
          { value: 'date-desc', label: t('List.dateDesc') }, { value: 'date-asc', label: t('List.dateAsc') },
        ] }} />

      {loading ? <LoadingState label={t('States.loadingWikiData')} /> : null}
      {error ? (
        <ErrorState
          message={error.message}
          requestId={error.requestId}
          diagnosticId={error.diagnosticId}
          requestIdLabel={t('QueryErrors.requestId')}
          diagnosticIdLabel={t('QueryErrors.diagnosticId')}
          copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
          errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
          errorIdCopied={errorIdCopied}
          onCopyErrorId={copyErrorId}
        />
      ) : null}
      {!loading && !error && filtered.length === 0 ? (
        <EmptyState message={active ? t('List.noFilterMatches') : emptyMessage} />
      ) : null}

      <div className="lwc-data-list grid gap-4 md:grid-cols-2">
        {filtered.map((entry, index) => (
          <EntryCard
            key={entry.slug}
            entry={entry}
            entryType={entryType}
            index={index}
            href={entry.id ? `${basePath}/${entry.id}-${encodeURIComponent(entry.slug)}` : `${basePath}/${encodeURIComponent(entry.slug)}`}
          />
        ))}
      </div>
    </div>
  );
}
