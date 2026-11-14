'use client';

import { useEffect, useLayoutEffect, useMemo, useState } from 'react';
import { EntryCard } from './EntryCard';
import { EmptyState, ErrorState, LoadingState, useErrorIdCopy } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { ApiError, type WikiEntry } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { selectEntries } from '@/lib/list-controls';
import { ListToolbar } from './ListToolbar';
import { ListPagination } from './ListPagination';
import { LIST_VIEW_PAGE_SIZE, paginate, useListViewState, type ListViewState } from '@/lib/list-view-state';

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
  const { currentProject, token, sessionEpoch, user, listViewSessionId } = useWorkspace();
  const identityKey = JSON.stringify([currentProject?.id ?? 'no-project', user?.id ?? null, listViewSessionId, token, sessionEpoch, props.basePath]);
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
  const {
    currentProject,
    token,
    sessionEpoch,
    hydrated,
    user,
    listViewSessionId,
    listViewGeneration,
    isListViewSessionCurrent,
  } = useWorkspace();
  const cacheKey = JSON.stringify([currentProject?.id ?? 'no-project', user?.id ?? null, listViewSessionId, token, sessionEpoch, basePath]);
  const [entries, setEntries] = useState<WikiEntry[]>(() => clientCache.get(cacheKey) ?? []);
  const [loading, setLoading] = useState(() => !clientCache.has(cacheKey));
  const [error, setError] = useState<ListError | null>(null);
  const defaultOrder = 'title';
  const defaults = useMemo<ListViewState>(() => ({
    search: '',
    filters: { status: 'all', dates: 'all' },
    order: defaultOrder,
    page: 1,
  }), [defaultOrder]);
  const validation = useMemo(() => ({
    filters: { status: ['all', 'published', 'draft', 'missing'], dates: ['all', 'dated', 'undated'] },
    orders: ['updated-desc', 'title', 'title-desc', 'slug', 'date-desc', 'date-asc'],
  }), []);
  const view = useListViewState({
    list: 'concepts',
    projectId: currentProject?.id ?? 'no-project',
    ownerId: user?.id ?? null,
    namespaceId: listViewSessionId,
    generation: listViewGeneration,
    enabled: hydrated && Boolean(user?.id && listViewSessionId),
    defaults,
    validation,
    isSessionCurrent: isListViewSessionCurrent,
  });
  const { search, filters, order, page: requestedPage } = view.state;
  const status = filters.status ?? 'all';
  const dates = filters.dates ?? 'all';
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
  const page = useMemo(() => paginate(filtered, requestedPage, LIST_VIEW_PAGE_SIZE), [filtered, requestedPage]);
  const updateView = view.update;
  const active = Boolean(search.trim() || status !== 'all' || dates !== 'all' || order !== defaultOrder);

  useLayoutEffect(() => {
    if (view.ready && !loading && !error && page.page !== requestedPage) {
      updateView((current) => ({ ...current, page: page.page }));
    }
  }, [error, loading, page.page, requestedPage, updateView, view.ready]);

  const updateControl = (key: 'search' | 'status' | 'dates' | 'order', value: string) => {
    updateView((current) => key === 'search'
      ? { ...current, search: value, page: 1 }
      : key === 'order'
        ? { ...current, order: value, page: 1 }
        : { ...current, filters: { ...current.filters, [key]: value }, page: 1 });
  };
  const resetControls = () => updateView(() => defaults);

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

      <ListToolbar search={search} onSearch={(value) => updateControl('search', value)} searchLabel={t('List.searchPlaceholder', { title: title.toLowerCase() })}
        count={filtered.length} total={entries.length} active={active}
        onClear={resetControls}
        filters={[
          { label: t('List.filterStatus'), value: status, onChange: (value) => updateControl('status', value), options: [
            { value: 'all', label: t('List.allStatuses') },
            ...[...new Set([...entries.map((entry) => entry.status ?? 'missing'), ...(status !== 'all' ? [status] : [])])].sort().map((value) => ({ value, label: value === 'published' ? t('Entry.published') : value === 'draft' ? t('Entry.draft') : value === 'missing' ? t('List.noStatus') : value })),
          ] },
          { label: t('List.filterDate'), value: dates, onChange: (value) => updateControl('dates', value), options: [
            { value: 'all', label: t('List.allDates') }, { value: 'dated', label: t('List.withDate') }, { value: 'undated', label: t('List.withoutDate') },
          ] },
        ]}
        order={{ label: t('List.orderBy'), value: order, onChange: (value) => updateControl('order', value), options: [
          ...(entryType === 'concept' ? [{ value: 'updated-desc', label: t('List.updatedDesc') }] : []),
          { value: 'title', label: t('List.titleAsc') }, { value: 'title-desc', label: t('List.titleDesc') }, { value: 'slug', label: t('List.slugOrder') },
          { value: 'date-desc', label: t('List.dateDesc') }, { value: 'date-asc', label: t('List.dateAsc') },
        ] }} />

      {loading ? <LoadingState label={t('States.loadingWikiData')} /> : null}
      {!view.ready && !loading ? <LoadingState label={t('States.loadingWikiData')} /> : null}
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
      {view.ready && !loading && !error && filtered.length === 0 ? (
        <EmptyState message={active ? t('List.noFilterMatches') : emptyMessage} />
      ) : null}

      <div className="lwc-data-list grid gap-4 md:grid-cols-2">
        {view.ready && !loading && !error ? page.items.map((entry, index) => (
          <EntryCard
            key={entry.slug}
            entry={entry}
            entryType={entryType}
            index={index}
            href={entry.id ? `${basePath}/${entry.id}-${encodeURIComponent(entry.slug)}` : `${basePath}/${encodeURIComponent(entry.slug)}`}
          />
        )) : null}
      </div>
      {view.ready && !loading && !error ? <ListPagination page={page.page} pageCount={page.pageCount} onPageChange={(nextPage) => updateView((current) => ({ ...current, page: nextPage }))} /> : null}
    </div>
  );
}
