'use client';

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { Eye, X } from 'lucide-react';
import { ApiError, getRawFilePreview, getSources, type DocumentImportRecord, type SourceListItem } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { EmptyState, ErrorState, LoadingState, useErrorIdCopy } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { NavigationLink } from './NavigationBlocker';
import { rawFileNameFromSource } from '@/lib/raw-file-name';
import { usePathname, useSearchParams } from 'next/navigation';
import { DocumentImportsPanel, documentImportPipelineStatusKey } from './DocumentImportsPanel';
import { MarkdownView } from './MarkdownView';
import { selectSources } from '@/lib/list-controls';
import { ListToolbar } from './ListToolbar';
import { AddMaterialAction } from './AddMaterialAction';
import { ListPagination } from './ListPagination';
import { LIST_VIEW_PAGE_SIZE, paginate, useListViewState, type ListViewState } from '@/lib/list-view-state';
import {
  consumeDocumentFocus,
  currentListHistoryEntryId,
  documentFocusMatchesLocation,
  ensureListHistoryEntryId,
  hasConsumedDocumentFocus,
  subscribeListHistoryEntry,
  type DocumentFocusIdentity,
} from '@/lib/list-navigation';

const getHistoryEntrySnapshot = () => currentListHistoryEntryId() ?? '';
const getServerHistoryEntrySnapshot = () => '';

const lifecycleVariant: Record<SourceListItem['lifecycle'], 'muted' | 'published' | 'draft'> = {
  new: 'muted',
  synced: 'published',
  notes_pending: 'draft',
  content_pending: 'draft',
  error: 'draft',
};

type SourceListError = { message: string; requestId?: string; diagnosticId?: string };
type PreviewError = SourceListError & { path: string };

export function SourceListClient() {
  const { currentProject, user, sessionIdentityEpoch, listViewSessionId } = useWorkspace();
  const identityKey = JSON.stringify([user?.id ?? null, sessionIdentityEpoch, listViewSessionId, currentProject?.id ?? 'no-project']);
  return <SourceListSession key={identityKey} />;
}

function SourceListSession() {
  const { t } = useT();
  const {
    currentProject,
    token,
    user,
    hydrated,
    listViewSessionId,
    listViewGeneration,
    isListViewSessionCurrent,
    projects,
    projectsLoading,
    selectProject,
  } = useWorkspace();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const currentProjectId = currentProject?.id;
  const requestedProjectId = searchParams.get('project_id');
  const queryString = searchParams.toString();
  const authorizedTarget = requestedProjectId ? projects.find((project) => project.id === requestedProjectId) : null;
  const projectMismatch = Boolean(requestedProjectId && currentProjectId !== requestedProjectId);
  const historyEntryId = useSyncExternalStore(subscribeListHistoryEntry, getHistoryEntrySnapshot, getServerHistoryEntrySnapshot);
  const [navigationGeneration, setNavigationGeneration] = useState(0);
  const [suppressedFocusKey, setSuppressedFocusKey] = useState('');
  const sourceImportTargetRef = useRef<HTMLDivElement | null>(null);
  const deepLinkError = requestedProjectId && !projectsLoading && !authorizedTarget
    ? t('Source.documentImport.projectUnavailable')
    : '';
  useLayoutEffect(() => {
    if (pathname === '/sources') ensureListHistoryEntryId();
  }, [listViewGeneration, listViewSessionId, pathname, queryString, user?.id]);
  const [sources, setSources] = useState<SourceListItem[]>([]);
  const [documentImports, setDocumentImports] = useState<DocumentImportRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<SourceListError | null>(null);
  const defaults = useMemo<ListViewState>(() => ({
    search: '',
    filters: { lifecycle: 'all', annotation: 'all' },
    order: 'title',
    page: 1,
  }), []);
  const validation = useMemo(() => ({
    filters: {
      lifecycle: ['all', 'new', 'synced', 'notes_pending', 'content_pending', 'error'],
      annotation: ['all', 'present', 'empty'],
    },
    orders: ['title', 'title-desc', 'path'],
  }), []);
  const view = useListViewState({
    list: 'sources',
    projectId: currentProjectId ?? 'no-project',
    ownerId: user?.id ?? null,
    namespaceId: listViewSessionId,
    generation: listViewGeneration,
    enabled: hydrated
      && Boolean(user?.id && listViewSessionId)
      && (!requestedProjectId || currentProjectId === requestedProjectId),
    defaults,
    validation,
    isSessionCurrent: isListViewSessionCurrent,
  });
  const { search, filters, order, page: requestedPage } = view.state;
  const lifecycleFilter = filters.lifecycle ?? 'all';
  const annotationFilter = filters.annotation ?? 'all';
  const [preview, setPreview] = useState<{ path: string; content: string } | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<PreviewError | null>(null);
  const previewIsOpen = Boolean(preview);
  const previewTriggerRef = useRef<HTMLButtonElement | null>(null);
  const closeButtonRef = useRef<HTMLButtonElement | null>(null);
  const previewRequestRef = useRef(0);
  const { copyErrorId, errorIdCopied } = useErrorIdCopy(error, error?.requestId, error?.diagnosticId);
  const { copyErrorId: copyPreviewErrorId, errorIdCopied: previewErrorIdCopied } =
    useErrorIdCopy(previewError, previewError?.requestId, previewError?.diagnosticId);
  const mergeDocumentImports = useCallback((records: DocumentImportRecord[]) => {
    setDocumentImports((current) => {
      const merged = new Map(current.map((record) => [record.document_id, record]));
      records.forEach((record) => merged.set(record.document_id, record));
      return [...merged.values()];
    });
  }, []);

  useEffect(() => {
    if (!requestedProjectId || currentProjectId === requestedProjectId || projectsLoading) return;
    if (!authorizedTarget) return;
    const query = searchParams.toString();
    selectProject(requestedProjectId, `${pathname ?? '/sources'}${query ? `?${query}` : ''}`);
  }, [authorizedTarget, currentProjectId, pathname, projectsLoading, requestedProjectId, searchParams, selectProject, t]);

  useEffect(() => {
    if (requestedProjectId && currentProjectId !== requestedProjectId) {
      return;
    }
    let cancelled = false;
    getSources(currentProjectId)
      .then((data) => {
        if (cancelled) return;
        setSources(data);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError({
          message: cause instanceof Error ? cause.message : String(cause),
          requestId: cause instanceof ApiError ? cause.requestId : undefined,
          diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
        });
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
      previewRequestRef.current += 1;
    };
  }, [currentProjectId, requestedProjectId, token]);

  const openRawPreview = useCallback(async (rawPath: string) => {
    const request = ++previewRequestRef.current;
    setPreview({ path: rawPath, content: '' });
    setPreviewLoading(true);
    setPreviewError(null);
    try {
      const content = await getRawFilePreview(rawFileNameFromSource(rawPath), currentProjectId);
      if (previewRequestRef.current === request) {
        setPreview((current) => current?.path === rawPath ? { ...current, content } : current);
      }
    } catch (cause) {
      if (previewRequestRef.current === request) {
        setPreviewError({
          message: cause instanceof Error ? cause.message : t('Raw.loadPreviewFailed'),
          requestId: cause instanceof ApiError ? cause.requestId : undefined,
          diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
          path: rawPath,
        });
      }
    } finally {
      if (previewRequestRef.current === request) setPreviewLoading(false);
    }
  }, [currentProjectId, t]);

  const closePreview = useCallback(() => {
    previewRequestRef.current += 1;
    setPreview(null);
    setPreviewError(null);
    queueMicrotask(() => previewTriggerRef.current?.focus());
  }, []);

  useEffect(() => {
    if (!previewIsOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closePreview();
        return;
      }
      if (event.key !== 'Tab') return;
      const dialog = document.getElementById('source-raw-preview-dialog');
      const focusable = Array.from(dialog?.querySelectorAll<HTMLElement>(
        'button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      ) ?? []).filter((element) => !element.closest('[hidden]'));
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) event.preventDefault();
      else if (event.shiftKey && (document.activeElement === first || !dialog?.contains(document.activeElement))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', closeOnEscape);
    closeButtonRef.current?.focus();
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [closePreview, previewIsOpen]);

  const filtered = useMemo(() => selectSources(sources, search, lifecycleFilter, annotationFilter, order), [search, sources, lifecycleFilter, annotationFilter, order]);
  const page = useMemo(() => paginate(filtered, requestedPage, LIST_VIEW_PAGE_SIZE), [filtered, requestedPage]);
  const updateView = view.update;
  const active = Boolean(search.trim() || lifecycleFilter !== 'all' || annotationFilter !== 'all' || order !== 'title');

  useLayoutEffect(() => {
    if (view.ready && !loading && !error && page.page !== requestedPage) {
      updateView((current) => ({ ...current, page: page.page }));
    }
  }, [error, loading, page.page, requestedPage, updateView, view.ready]);

  const changeControl = (key: 'search' | 'lifecycle' | 'annotation' | 'order', value: string) => {
    setNavigationGeneration((generation) => generation + 1);
    setSuppressedFocusKey('');
    updateView((current) => key === 'search'
      ? { ...current, search: value, page: 1 }
      : key === 'order'
        ? { ...current, order: value, page: 1 }
        : { ...current, filters: { ...current.filters, [key]: value }, page: 1 });
  };
  const clearControls = () => {
    setNavigationGeneration((generation) => generation + 1);
    setSuppressedFocusKey('');
    updateView(() => defaults);
  };
  const setPageFromUser = (nextPage: number, focusKey: string) => {
    setNavigationGeneration((generation) => generation + 1);
    setSuppressedFocusKey(focusKey);
    updateView((current) => ({ ...current, page: nextPage }));
  };

  const documentImportsBySourcePath = useMemo(() => {
    const result = new Map<string, DocumentImportRecord>();
    for (const record of documentImports) {
      if (record.conversion_state === 'succeeded' && record.source_state === 'saved' && record.deletion_state !== 'deleted' && record.source_ref?.raw_path) {
        result.set(record.source_ref.raw_path, record);
      }
    }
    return result;
  }, [documentImports]);

  const requestedDocumentId = searchParams.get('document_id') ?? '';
  const userId = user?.id ?? null;
  const focusIdentity = useMemo<DocumentFocusIdentity | null>(() => (
    requestedDocumentId && historyEntryId && userId && listViewSessionId && currentProjectId
      ? { entryId: historyEntryId, ownerId: userId, namespaceId: listViewSessionId, projectId: currentProjectId, documentId: requestedDocumentId }
      : null
  ), [currentProjectId, historyEntryId, listViewSessionId, requestedDocumentId, userId]);
  const focusKey = focusIdentity ? JSON.stringify(focusIdentity) : '';
  const focusTarget = requestedDocumentId
    ? documentImports.find((item) => item.document_id === requestedDocumentId
      && item.conversion_state === 'succeeded'
      && item.source_state === 'saved'
      && item.deletion_state !== 'deleted'
      && Boolean(item.source_ref?.raw_path))
    : undefined;
  const focusTargetIndex = focusTarget?.source_ref?.raw_path
    ? filtered.findIndex((source) => source.rawPath === focusTarget.source_ref?.raw_path)
    : -1;
  const visibleSources = page.items;
  const focusScopeRef = useRef({
    identity: focusIdentity,
    tupleKey: focusKey,
    filtered,
    visibleSources,
    targetPath: focusTarget?.source_ref?.raw_path ?? null,
    userId: user?.id ?? null,
    namespaceId: listViewSessionId,
    projectId: currentProjectId ?? null,
    authGeneration: listViewGeneration,
    isListViewSessionCurrent,
  });
  useLayoutEffect(() => {
    focusScopeRef.current = {
      identity: focusIdentity,
      tupleKey: focusKey,
      filtered,
      visibleSources,
      targetPath: focusTarget?.source_ref?.raw_path ?? null,
      userId: user?.id ?? null,
      namespaceId: listViewSessionId,
      projectId: currentProjectId ?? null,
      authGeneration: listViewGeneration,
      isListViewSessionCurrent,
    };
  }, [currentProjectId, filtered, focusIdentity, focusKey, focusTarget?.source_ref?.raw_path, isListViewSessionCurrent, listViewGeneration, listViewSessionId, user?.id, visibleSources]);
  const focusIsCurrent = useCallback((identity: DocumentFocusIdentity, generation: number, authGeneration: number, requireVisibleRow: boolean) => {
    const current = focusScopeRef.current;
    if (navigationGeneration !== generation || current.tupleKey !== JSON.stringify(identity)) return false;
    if (current.userId !== identity.ownerId || current.namespaceId !== identity.namespaceId || current.projectId !== identity.projectId) return false;
    if (current.authGeneration !== authGeneration || !current.isListViewSessionCurrent(identity.namespaceId, authGeneration, identity.ownerId)) return false;
    if (!documentFocusMatchesLocation(identity) || current.targetPath === null) return false;
    if (!current.filtered.some((source) => source.rawPath === current.targetPath)) return false;
    return !requireVisibleRow || current.visibleSources.some((source) => source.rawPath === current.targetPath);
  }, [navigationGeneration]);

  useEffect(() => {
    if (!view.ready || loading || error || !focusIdentity || !focusTarget?.source_ref?.raw_path || focusTargetIndex < 0) return;
    if (hasConsumedDocumentFocus(focusIdentity) || suppressedFocusKey === focusKey) return;
    const generation = navigationGeneration;
    const authGeneration = listViewGeneration;
    if (!focusIsCurrent(focusIdentity, generation, authGeneration, false)) return;
    const targetPage = Math.floor(focusTargetIndex / LIST_VIEW_PAGE_SIZE) + 1;
    if (page.page !== targetPage) {
      updateView((current) => ({ ...current, page: targetPage }));
      return;
    }

    let cancelled = false;
    let frame = 0;
    let usingTimeout = false;
    const focusRow = () => {
      if (cancelled || !focusIsCurrent(focusIdentity, generation, authGeneration, true)) return;
      const row = sourceImportTargetRef.current;
      if (!row?.isConnected) return;
      row.scrollIntoView({ block: 'center' });
      row.focus();
      if (!cancelled && document.activeElement === row && focusIsCurrent(focusIdentity, generation, authGeneration, true)) {
        consumeDocumentFocus(focusIdentity);
      }
    };
    if (typeof window.requestAnimationFrame === 'function') {
      frame = window.requestAnimationFrame(focusRow);
    } else {
      usingTimeout = true;
      frame = window.setTimeout(focusRow, 0);
    }
    return () => {
      cancelled = true;
      if (usingTimeout) window.clearTimeout(frame);
      else window.cancelAnimationFrame(frame);
    };
  }, [documentImports, error, focusIdentity, focusIsCurrent, focusKey, focusTarget, focusTargetIndex, listViewGeneration, loading, navigationGeneration, page.page, requestedDocumentId, sources, suppressedFocusKey, updateView, view.ready]);

  if (deepLinkError) return <p role="alert" className="rounded-md border border-red-400/20 bg-red-950/30 p-4 text-red-100">{deepLinkError}</p>;
  if (projectMismatch) return <p role="status" className="rounded-md border border-white/10 p-4 text-zinc-300">{projectsLoading ? t('Source.documentImport.loadingProject') : t('Source.documentImport.switchingProject')}</p>;

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">{t('List.sourcesTitle')}</h1>
        <p className="mt-2 max-w-2xl text-zinc-400">{t('List.sourcesDescription')}</p>
        <AddMaterialAction className="mt-4" />
      </header>

      <ListToolbar search={search} onSearch={(value) => changeControl('search', value)} searchLabel={t('List.searchPlaceholder', { title: t('List.sourcesTitle').toLowerCase() })}
        count={filtered.length} total={sources.length} active={active}
        onClear={clearControls}
        filters={[
          { label: t('List.filterStatus'), value: lifecycleFilter, onChange: (value) => changeControl('lifecycle', value), options: [
            { value: 'all', label: t('List.allStatuses') },
            ...[...new Set([...sources.map((source) => source.lifecycle), ...(lifecycleFilter !== 'all' ? [lifecycleFilter as SourceListItem['lifecycle']] : [])])].sort().map((value) => ({ value, label: t(`Source.lifecycle.${value}`) })),
          ] },
          { label: t('List.filterAnnotation'), value: annotationFilter, onChange: (value) => changeControl('annotation', value), options: [
            { value: 'all', label: t('List.allAnnotations') }, { value: 'present', label: t('Source.annotationPresent') }, { value: 'empty', label: t('Source.annotationEmpty') },
          ] },
        ]}
        order={{ label: t('List.orderBy'), value: order, onChange: (value) => changeControl('order', value), options: [
          { value: 'title', label: t('List.titleAsc') }, { value: 'title-desc', label: t('List.titleDesc') }, { value: 'path', label: t('List.pathOrder') },
        ] }} />


      {loading ? <LoadingState label={t('Source.loading')} /> : null}
      {!view.ready && !loading ? <LoadingState label={t('Source.loading')} /> : null}
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
      {view.ready && !loading && !error && filtered.length === 0 ? <EmptyState message={t(active ? 'List.noFilterMatches' : 'List.noSources')} /> : null}

      <div className="lwc-data-list grid gap-4 md:grid-cols-2">
        {view.ready && !loading && !error ? page.items.map((source) => {
          const detailHref = source.id ? `/sources/${source.id}-${encodeURIComponent(source.slug)}` : null;
          const lifecycle = t(`Source.lifecycle.${source.lifecycle}`);
          const importedRecord = documentImportsBySourcePath.get(source.rawPath);
          const displayName = importedRecord?.original_filename.trim() || source.title;
          const isImportTarget = focusIdentity !== null && focusTarget?.source_ref?.raw_path === source.rawPath;
          return (
            <div key={source.id ?? source.rawPath} ref={isImportTarget ? sourceImportTargetRef : undefined} tabIndex={isImportTarget ? -1 : undefined} className="h-full">
              <Surface variant="glass" className="h-full p-5 transition hover:border-emerald-400/30">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="source">{t('Source.singular')}</Badge>
                  <Badge variant={lifecycleVariant[source.lifecycle]}>{lifecycle}</Badge>
                  <Badge variant="muted">
                    {source.annotationPresent ? t('Source.annotationPresent') : t('Source.annotationEmpty')}
                  </Badge>
                </div>
                {detailHref ? (
                  <NavigationLink href={detailHref} className="mt-3 block break-words text-lg font-semibold text-white hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
                    {displayName}
                  </NavigationLink>
                ) : <h2 className="mt-3 break-words text-lg font-semibold text-white">{displayName}</h2>}
                <details className="mt-2 text-xs text-zinc-500">
                  <summary className="w-fit cursor-pointer">{t('Source.documentImport.rawPath')}</summary>
                  <code className="mt-1 block break-all font-mono">{source.rawPath}</code>
                </details>
                {importedRecord ? (
                  <p role="status" className="mt-2 text-sm text-zinc-300">
                    {t('Source.documentImport.sourceSaved')}
                    {importedRecord.pipeline_ref?.status ? ` · ${t('Source.documentImport.pipelineState', { state: t(documentImportPipelineStatusKey(importedRecord.pipeline_ref.status)) })}` : ''}
                  </p>
                ) : null}
                {source.lifecycle === 'error' ? (
                  <p role="alert" className="mt-3 text-sm text-red-200">{source.error ?? t('Source.lifecycleError')}</p>
                ) : null}
                <div className="mt-4 flex items-center gap-2">
                  <button
                    type="button"
                    onClick={(event) => {
                      previewTriggerRef.current = event.currentTarget;
                      void openRawPreview(source.rawPath);
                    }}
                    className="inline-flex min-h-11 items-center gap-2 rounded-md border border-white/10 px-3 text-sm text-zinc-200 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
                    aria-label={t('Source.previewRawAria', { path: source.rawPath })}
                  >
                    <Eye className="size-4" aria-hidden="true" />
                    {t('Source.previewRaw')}
                  </button>
                </div>
              </Surface>
            </div>
          );
        }) : null}
      </div>
      {view.ready && !loading && !error ? <ListPagination page={page.page} pageCount={page.pageCount} onPageChange={(nextPage) => setPageFromUser(nextPage, focusKey)} /> : null}

      {currentProjectId ? <DocumentImportsPanel projectId={currentProjectId} sourcePaths={sources.map((source) => source.rawPath)} onRecordsChange={mergeDocumentImports} /> : null}

      {preview ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black p-3 sm:p-4" onClick={closePreview}>
          <Surface id="source-raw-preview-dialog" role="dialog" aria-modal="true" aria-labelledby="source-raw-preview-title" variant="elevated" className="flex max-h-[calc(100dvh-1.5rem)] w-full max-w-3xl flex-col overflow-hidden bg-zinc-900 sm:max-h-[calc(100dvh-2rem)]" style={{ backgroundColor: '#191B1C' }} onClick={(event) => event.stopPropagation()}>
            <div className="flex items-center justify-between gap-4 border-b border-white/10 p-4">
              <h2 id="source-raw-preview-title" className="min-w-0 truncate text-lg font-semibold text-white">{documentImportsBySourcePath.get(preview.path)?.original_filename.trim() || preview.path}</h2>
              <button ref={closeButtonRef} type="button" onClick={closePreview} aria-label={t('Raw.close')} className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-md text-zinc-300 hover:bg-white/10">
                <X className="size-5" aria-hidden="true" />
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto p-4">
              {previewLoading ? <LoadingState label={t('Raw.loadingPreview')} /> : null}
              {previewError ? (
                <ErrorState
                  message={previewError.message}
                  requestId={previewError.requestId}
                  diagnosticId={previewError.diagnosticId}
                  requestIdLabel={t('QueryErrors.requestId')}
                  diagnosticIdLabel={t('QueryErrors.diagnosticId')}
                  copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
                  errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
                  errorIdCopied={previewErrorIdCopied}
                  onCopyErrorId={copyPreviewErrorId}
                />
              ) : null}
              {!previewLoading && !previewError ? (
                <div className="min-w-0 max-w-full overflow-x-auto rounded-md border border-white/10 bg-[#101112] p-4">
                  <MarkdownView content={preview.content} preserveLeadingHeading />
                </div>
              ) : null}
            </div>
          </Surface>
        </div>
      ) : null}
    </div>
  );
}
