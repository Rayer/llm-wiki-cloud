'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Download, Loader2, Trash2 } from 'lucide-react';
import { ApiError, deleteDocumentImport, downloadDocumentImportOriginal, getDocumentImport, getDocumentImports, type DocumentImportList, type DocumentImportRecord } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { Surface } from './ui/Surface';
import { NavigationLink } from './NavigationBlocker';
import { useSearchParams } from 'next/navigation';

type Props = {
  projectId: string;
  sourcePaths: string[];
  onRecordsChange?: (records: DocumentImportRecord[]) => void;
};
type ImportFilter = 'all' | 'pending' | 'succeeded' | 'failed';
type LoadError = { message: string; requestId?: string; diagnosticId?: string };
type RequestOwner = { projectId: string; filter: ImportFilter; generation: number; listCommit: number; active: boolean };

const FILTERS: ImportFilter[] = ['all', 'pending', 'succeeded', 'failed'];
const pendingState = (record: DocumentImportRecord) => ['accepted', 'queued', 'converting'].includes(record.conversion_state)
  || record.source_state === 'saving' || record.source_state === 'unknown' || record.deletion_state === 'deleting'
  || ['pending', 'preparing', 'running', 'unknown'].includes(record.pipeline_ref?.status ?? '');

const pipelineStatusNames: Record<string, string> = {
  not_started_disabled: 'disabled', not_started: 'idle', idle: 'idle',
  pending: 'pending', preparing: 'pending', ready: 'pending', invoking: 'pending', invocation_unknown: 'pending',
  running: 'running', compiling: 'running', succeeded: 'succeeded', published: 'succeeded',
  failed: 'failed', cancelled: 'cancelled', unknown: 'unknown',
};

export function documentImportPipelineStatusKey(status: string): string {
  return `Source.documentImport.pipelineStatus.${pipelineStatusNames[status.toLowerCase()] ?? 'unknown'}`;
}

function recordError(cause: unknown): LoadError {
  return {
    message: cause instanceof Error ? cause.message : 'Document status is unavailable.',
    requestId: cause instanceof ApiError ? cause.requestId : undefined,
    diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
  };
}

function preserveDeletion(existing: DocumentImportRecord, incoming: DocumentImportRecord) {
  return existing.deletion_state === 'deleted' && incoming.deletion_state !== 'deleted'
    || existing.deletion_state !== 'present' && incoming.deletion_state === 'present';
}

function mergeListRecords(current: DocumentImportRecord[], incoming: DocumentImportRecord[], append: boolean, completePage: boolean, requestedDocumentId: string) {
  const currentById = new Map(current.map((record) => [record.document_id, record]));
  const incomingById = new Map(incoming.map((record) => [record.document_id, record]));
  const incomingIds = new Set(incomingById.keys());
  const useIncoming = (record: DocumentImportRecord) => {
    const existing = currentById.get(record.document_id);
    return existing && preserveDeletion(existing, record) ? existing : record;
  };

  if (append) {
    const merged = current.map((record) => {
      const next = incomingById.get(record.document_id);
      return next ? useIncoming(next) : record;
    });
    for (const record of incoming) {
      if (!currentById.has(record.document_id)) merged.push(record);
    }
    return merged;
  }

  const merged = incoming.map(useIncoming);
  for (const record of current) {
    if (incomingIds.has(record.document_id)) continue;
    if (record.deletion_state === 'deleted') merged.push(record);
    else if (record.deletion_state === 'deleting') {
      merged.push(completePage ? { ...record, deletion_state: 'deleted' } : record);
    } else if (!completePage && record.document_id === requestedDocumentId) merged.push(record);
  }
  return merged;
}

export function DocumentImportsPanel({ projectId, sourcePaths, onRecordsChange }: Props) {
  const { t } = useT();
  const { refreshNavCounts } = useWorkspace();
  const params = useSearchParams();
  const requestedStatus = params.get('conversion_status') ?? 'all';
  const filter: ImportFilter = FILTERS.includes(requestedStatus as ImportFilter) ? requestedStatus as ImportFilter : 'all';
  const requestedDocumentId = params.get('document_id') ?? '';
  const requestedDocumentIdRef = useRef(requestedDocumentId);
  requestedDocumentIdRef.current = requestedDocumentId;
  const [page, setPage] = useState<DocumentImportList | null>(null);
  const [records, setRecords] = useState<DocumentImportRecord[]>([]);
  const [error, setError] = useState<LoadError | null>(null);
  const [loading, setLoading] = useState(true);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [busy, setBusy] = useState<Record<string, 'download' | 'delete' | undefined>>({});
  const [feedback, setFeedback] = useState<Record<string, string>>({});
  const targetRowRef = useRef<HTMLLIElement | null>(null);
  const focusedDocumentIdRef = useRef('');
  const focusedScopeOwnerRef = useRef<RequestOwner | null>(null);
  const mountedRef = useRef(false);
  const observedDeletionsRef = useRef<{ scope: string; states: Map<string, string> }>({ scope: '', states: new Map() });
  const requestOwnerRef = useRef<RequestOwner>({ projectId, filter, generation: 0, listCommit: 0, active: false });
  if (requestOwnerRef.current.projectId !== projectId || requestOwnerRef.current.filter !== filter) {
    requestOwnerRef.current = { projectId, filter, generation: 0, listCommit: 0, active: false };
  }
  const requestOwner = requestOwnerRef.current;
  if (focusedScopeOwnerRef.current !== requestOwner) {
    focusedScopeOwnerRef.current = requestOwner;
    focusedDocumentIdRef.current = '';
  }
  const isRequestOwnerCurrent = useCallback(() => requestOwnerRef.current === requestOwner && requestOwner.active, [requestOwner]);

  const load = useCallback(async (cursor?: string, append = false) => {
    if (!isRequestOwnerCurrent()) return null;
    const requestGeneration = ++requestOwner.generation;
    const isRequestCurrent = () => isRequestOwnerCurrent() && requestOwner.generation === requestGeneration;
    try {
      const result = await getDocumentImports(projectId, filter, cursor);
      if (isRequestCurrent()) {
        requestOwner.listCommit += 1;
        setPage(result);
        setRecords((current) => mergeListRecords(current, result.items, append, !result.next_cursor, requestedDocumentIdRef.current));
        setError(null);
      }
      return isRequestOwnerCurrent() ? result : null;
    } catch (cause) {
      if (isRequestCurrent()) setError(recordError(cause));
      return null;
    } finally {
      if (isRequestCurrent()) setLoading(false);
    }
  }, [filter, isRequestOwnerCurrent, projectId, requestOwner]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  useEffect(() => {
    requestOwner.active = true;
    setLoading(true);
    setPage(null);
    setRecords([]);
    setError(null);
    void load();
    return () => {
      requestOwner.active = false;
      requestOwner.generation += 1;
    };
  }, [filter, load, projectId, requestOwner]);

  useEffect(() => {
    if (!requestedDocumentId || requestedDocumentId === focusedDocumentIdRef.current) return;
    let active = true;
    const listGenerationAtStart = requestOwner.generation;
    const listCommitAtStart = requestOwner.listCommit;
    void getDocumentImport(projectId, requestedDocumentId).then((record) => {
      if (!active || !isRequestOwnerCurrent()) return;
      setRecords((current) => {
        const existing = current.find((item) => item.document_id === record.document_id);
        if (!existing) return [record, ...current];
        if (preserveDeletion(existing, record) || existing.deletion_state === 'deleted') return current;
        if (requestOwner.generation !== listGenerationAtStart || requestOwner.listCommit !== listCommitAtStart) return current;
        if (record.revision < existing.revision) return current;
        return [record, ...current.filter((item) => item.document_id !== record.document_id)];
      });
    }).catch((cause: unknown) => {
      if (active && isRequestOwnerCurrent()) setError(recordError(cause));
    });
    return () => { active = false; };
  }, [isRequestOwnerCurrent, projectId, requestOwner, requestedDocumentId]);

  useEffect(() => {
    if (!requestedDocumentId || !records.some((record) => record.document_id === requestedDocumentId)) return;
    if (focusedDocumentIdRef.current === requestedDocumentId) return;
    const requestedRecord = records.find((record) => record.document_id === requestedDocumentId);
    if (requestedRecord?.source_state === 'saved' && requestedRecord.source_ref && sourcePaths.includes(requestedRecord.source_ref.raw_path)) {
      focusedDocumentIdRef.current = requestedDocumentId;
      return;
    }
    focusedDocumentIdRef.current = requestedDocumentId;
    targetRowRef.current?.scrollIntoView({ block: 'center' });
    targetRowRef.current?.focus();
  }, [records, requestedDocumentId, sourcePaths]);

  useEffect(() => {
    onRecordsChange?.(records);
  }, [onRecordsChange, records]);

  const hasPending = records.some(pendingState);
  useEffect(() => {
    if (!hasPending) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [hasPending, load]);

  useEffect(() => {
    const scope = `${projectId}:${filter}`;
    const states = new Map(records.map((record) => [record.document_id, record.deletion_state]));
    const previous = observedDeletionsRef.current;
    if (previous.scope !== scope) {
      observedDeletionsRef.current = { scope, states };
      return;
    }
    const completedDeletion = [...previous.states].some(([id, state]) => state === 'deleting' && states.get(id) !== 'deleting');
    observedDeletionsRef.current = { scope, states };
    if (completedDeletion) void refreshNavCounts();
  }, [filter, projectId, records, refreshNavCounts]);

  const visibleRecords = useMemo(() => records.filter((record) => {
    if (record.deletion_state === 'deleted') return false;
    if (!historyOpen && record.source_state === 'saved' && record.source_ref && sourcePaths.includes(record.source_ref.raw_path)) return false;
    return true;
  }), [historyOpen, records, sourcePaths]);

  const changeFilter = (value: ImportFilter) => {
    const next = new URLSearchParams(params.toString());
    next.set('conversion_status', value);
    window.history.replaceState(null, '', `/sources?${next.toString()}`);
    focusedDocumentIdRef.current = '';
  };

  const download = async (record: DocumentImportRecord) => {
    setBusy((current) => ({ ...current, [record.document_id]: 'download' }));
    setFeedback((current) => ({ ...current, [record.document_id]: '' }));
    try {
      const blob = await downloadDocumentImportOriginal(projectId, record.document_id);
      const objectURL = URL.createObjectURL(blob);
      const anchor = window.document.createElement('a');
      anchor.href = objectURL;
      anchor.download = record.original_filename.trim() || t('Source.documentImport.unknownFilename');
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
      setFeedback((current) => ({ ...current, [record.document_id]: t('Source.documentImport.downloadPrepared') }));
    } catch (cause) {
      setFeedback((current) => ({ ...current, [record.document_id]: recordError(cause).message }));
    } finally {
      setBusy((current) => ({ ...current, [record.document_id]: undefined }));
    }
  };

  const remove = async (record: DocumentImportRecord) => {
    if (record.deletion_state === 'deleting' || record.deletion_state === 'deleted') return;
    if (!window.confirm(t('Source.documentImport.confirmDelete', { filename: record.original_filename.trim() || t('Source.documentImport.unknownFilename') }))) return;
    setBusy((current) => ({ ...current, [record.document_id]: 'delete' }));
    setFeedback((current) => ({ ...current, [record.document_id]: '' }));
    try {
      await deleteDocumentImport(projectId, record.document_id, record.revision);
      if (!isRequestOwnerCurrent()) {
        await refreshNavCounts();
        return;
      }
      const result = await load();
      if (!result) throw new Error(t('Source.documentImport.refreshFailed'));
      await refreshNavCounts();
      if (isRequestOwnerCurrent()) setFeedback((current) => ({ ...current, [record.document_id]: t('Source.documentImport.deleteAccepted') }));
    } catch (cause) {
      if (isRequestOwnerCurrent()) {
        setFeedback((current) => ({ ...current, [record.document_id]: recordError(cause).message }));
        void load();
      }
    } finally {
      if (mountedRef.current) setBusy((current) => ({ ...current, [record.document_id]: undefined }));
    }
  };

  return (
    <section className="space-y-3" aria-labelledby="document-imports-title">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="document-imports-title" className="text-xl font-semibold text-white">{t('Source.documentImport.listTitle')}</h2>
          {page ? <p className="mt-1 text-sm text-zinc-400">{t('Source.documentImport.failedCount', { count: page.failed_count })}</p> : null}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <label className="text-sm text-zinc-300">
            <span className="mr-2">{t('Source.documentImport.filterLabel')}</span>
            <select value={filter} onChange={(event) => changeFilter(event.target.value as ImportFilter)} className="min-h-10 rounded-md border border-white/10 bg-zinc-900 px-2 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
              {FILTERS.map((value) => <option key={value} value={value}>{t(`Source.documentImport.filter.${value}`)}</option>)}
            </select>
          </label>
          <button type="button" aria-expanded={historyOpen} onClick={() => setHistoryOpen((open) => !open)} className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
            {t(historyOpen ? 'Source.documentImport.hideHistory' : 'Source.documentImport.showHistory')}
          </button>
        </div>
      </div>
      {error ? <p role="alert" className="rounded-md border border-red-400/20 bg-red-950/30 p-3 text-sm text-red-100">{t('Source.documentImport.loadFailed')}: {error.message}</p> : null}
      {loading && records.length === 0 ? <p role="status" className="text-sm text-zinc-400">{t('Source.documentImport.loading')}</p> : null}
      {!loading && !error && visibleRecords.length === 0 ? (
        <p role="status" className="rounded-md border border-white/10 bg-black/15 p-3 text-sm text-zinc-400">
          {records.length > 0 && !historyOpen ? t('Source.documentImport.hiddenHistoryEmpty') : t('Source.documentImport.filterEmpty')}
        </p>
      ) : null}
      <ul className="space-y-3">
        {visibleRecords.map((record) => {
          const conversionFailed = record.conversion_state === 'failed';
          const busyAction = busy[record.document_id];
          const filename = record.original_filename.trim() || t('Source.documentImport.unknownFilename');
          const status = record.deletion_state === 'deleting' ? t('Source.documentImport.deleting')
            : record.deletion_state === 'delete_failed' ? t('Source.documentImport.deleteFailed')
              : conversionFailed ? t(`Source.documentImport.failure.${record.failure?.safe_message_key ?? 'conversion_error'}`)
                : record.conversion_state === 'succeeded' && record.source_state === 'saved' ? t('Source.documentImport.sourceSaved')
                  : record.conversion_state === 'succeeded' && record.source_state === 'saving' ? t('Source.documentImport.sourceState.saving')
                    : record.conversion_state === 'succeeded' && record.source_state === 'failed' ? t('Source.documentImport.sourceSaveFailed')
                      : record.conversion_state === 'succeeded' && record.source_state === 'unknown' ? t('Source.documentImport.statusUnknown')
                  : record.source_state === 'failed' ? t('Source.documentImport.sourceSaveFailed')
                    : record.source_state === 'unknown' ? t('Source.documentImport.statusUnknown')
                      : t(`Source.documentImport.state.${record.conversion_state}`);
          return (
            <li key={record.document_id} ref={record.document_id === requestedDocumentId ? targetRowRef : undefined} tabIndex={record.document_id === requestedDocumentId ? -1 : undefined}>
              <Surface variant="glass" className={`p-4 ${conversionFailed ? 'border-red-400/30' : ''}`}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <h3 className="break-words font-medium text-white">{filename}</h3>
                    <p className="mt-1 text-xs text-zinc-500">{record.media_type} · {record.bytes.toLocaleString()} B</p>
                    <p className={`mt-2 text-sm ${conversionFailed ? 'text-red-200' : 'text-zinc-300'}`}>{status}</p>
                    {record.conversion_state === 'succeeded' && record.pipeline_ref?.status ? <p className="mt-1 text-xs text-zinc-400">{t('Source.documentImport.pipelineState', { state: t(documentImportPipelineStatusKey(record.pipeline_ref.status)) })}</p> : null}
                    {record.source_ref?.raw_path && sourcePaths.includes(record.source_ref.raw_path) ? (
                      <NavigationLink href={`/sources?document_id=${encodeURIComponent(record.document_id)}`} className="mt-2 inline-block text-sm text-emerald-200 underline underline-offset-2 hover:text-emerald-100">
                        {t('Source.documentImport.viewSource')}
                      </NavigationLink>
                    ) : null}
                    {feedback[record.document_id] ? <p className="mt-2 text-sm text-zinc-300" role={feedback[record.document_id]?.includes('failed') ? 'alert' : 'status'}>{feedback[record.document_id]}</p> : null}
                    <details className="mt-2 text-xs text-zinc-500">
                      <summary className="w-fit cursor-pointer">{t('Source.documentImport.diagnostics')}</summary>
                      <div className="mt-1 space-y-1 break-all font-mono">
                        <p>{record.document_id} · revision {record.revision}</p>
                        {record.source_ref?.raw_path ? <p>{record.source_ref.raw_path}</p> : null}
                        {record.failure?.diagnostic_id ? <p>{t('Source.documentImport.diagnostic', { id: record.failure.diagnostic_id })}</p> : null}
                      </div>
                    </details>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <button type="button" onClick={() => void download(record)} disabled={Boolean(busyAction) || record.deletion_state === 'deleted'} className="inline-flex min-h-10 items-center gap-2 rounded-md border border-white/10 px-3 py-2 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400 disabled:opacity-50">
                      {busyAction === 'download' ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : <Download className="size-4" aria-hidden="true" />}{t('Source.documentImport.downloadOriginal')}
                    </button>
                    {(conversionFailed || (record.source_state === 'failed' && !record.source_ref)) ? (
                      <button type="button" onClick={() => void remove(record)} disabled={Boolean(busyAction) || record.deletion_state === 'deleting' || record.deletion_state === 'deleted'} className="inline-flex min-h-10 items-center gap-2 rounded-md border border-red-400/30 px-3 py-2 text-sm text-red-100 hover:bg-red-400/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-red-300 disabled:opacity-50">
                        {busyAction === 'delete' ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : <Trash2 className="size-4" aria-hidden="true" />}{t('Source.documentImport.delete')}
                      </button>
                    ) : null}
                  </div>
                </div>
              </Surface>
            </li>
          );
        })}
      </ul>
      {page?.next_cursor ? <button type="button" onClick={() => void load(page.next_cursor, true)} className="min-h-11 rounded-md border border-white/10 px-4 py-2 text-sm text-zinc-300 hover:bg-white/5">{t('Source.documentImport.loadMore')}</button> : null}
    </section>
  );
}
