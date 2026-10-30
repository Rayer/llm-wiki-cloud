'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Download, Loader2, Trash2 } from 'lucide-react';
import { ApiError, deleteDocumentImport, downloadDocumentImportOriginal, getDocumentImport, getDocumentImports, type DocumentImportList, type DocumentImportRecord } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { Surface } from './ui/Surface';
import { useSearchParams } from 'next/navigation';

type Props = { projectId: string; sourcePaths: string[] };
type ImportFilter = 'all' | 'pending' | 'succeeded' | 'failed';
type LoadError = { message: string; requestId?: string; diagnosticId?: string };

const FILTERS: ImportFilter[] = ['all', 'pending', 'succeeded', 'failed'];
const pendingState = (record: DocumentImportRecord) => ['accepted', 'queued', 'converting'].includes(record.conversion_state)
  || record.source_state === 'saving' || record.source_state === 'unknown';

function recordError(cause: unknown): LoadError {
  return {
    message: cause instanceof Error ? cause.message : 'Document status is unavailable.',
    requestId: cause instanceof ApiError ? cause.requestId : undefined,
    diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
  };
}

export function DocumentImportsPanel({ projectId, sourcePaths }: Props) {
  const { t } = useT();
  const { refreshNavCounts } = useWorkspace();
  const params = useSearchParams();
  const requestedStatus = params.get('conversion_status') ?? 'all';
  const filter: ImportFilter = FILTERS.includes(requestedStatus as ImportFilter) ? requestedStatus as ImportFilter : 'all';
  const requestedDocumentId = params.get('document_id') ?? '';
  const [page, setPage] = useState<DocumentImportList | null>(null);
  const [records, setRecords] = useState<DocumentImportRecord[]>([]);
  const [error, setError] = useState<LoadError | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<Record<string, 'download' | 'delete' | undefined>>({});
  const [feedback, setFeedback] = useState<Record<string, string>>({});
  const targetRowRef = useRef<HTMLLIElement | null>(null);
  const focusedDocumentIdRef = useRef('');

  const load = useCallback(async (cursor?: string, append = false) => {
    try {
      const result = await getDocumentImports(projectId, filter, cursor);
      setPage(result);
      setRecords((current) => append ? [...current, ...result.items.filter((item) => !current.some((known) => known.document_id === item.document_id))] : result.items);
      setError(null);
      return result;
    } catch (cause) {
      setError(recordError(cause));
      return null;
    } finally {
      setLoading(false);
    }
  }, [filter, projectId]);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setPage(null);
    setRecords([]);
    setError(null);
    void getDocumentImports(projectId, filter).then((result) => {
      if (!active) return;
      setPage(result);
      setRecords(result.items);
      setError(null);
    }).catch((cause: unknown) => {
      if (active) setError(recordError(cause));
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [filter, projectId]);

  useEffect(() => {
    if (!requestedDocumentId || requestedDocumentId === focusedDocumentIdRef.current) return;
    let active = true;
    void getDocumentImport(projectId, requestedDocumentId).then((record) => {
      if (!active) return;
      setRecords((current) => [record, ...current.filter((item) => item.document_id !== record.document_id)]);
    }).catch((cause: unknown) => {
      if (active) setError(recordError(cause));
    });
    return () => { active = false; };
  }, [projectId, requestedDocumentId]);

  useEffect(() => {
    if (!requestedDocumentId || !records.some((record) => record.document_id === requestedDocumentId)) return;
    if (focusedDocumentIdRef.current === requestedDocumentId) return;
    focusedDocumentIdRef.current = requestedDocumentId;
    targetRowRef.current?.scrollIntoView({ block: 'center' });
    targetRowRef.current?.focus();
  }, [records, requestedDocumentId]);

  const hasPending = records.some(pendingState);
  useEffect(() => {
    if (!hasPending) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [hasPending, load]);

  const visibleRecords = useMemo(() => records.filter((record) => {
    if (record.deletion_state === 'deleted') return false;
    if (filter === 'all' && record.source_state === 'saved' && record.source_ref && sourcePaths.includes(record.source_ref.raw_path)) return false;
    return true;
  }), [filter, records, sourcePaths]);

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
      anchor.download = record.original_filename;
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
    if (!window.confirm(t('Source.documentImport.confirmDelete', { filename: record.original_filename }))) return;
    setBusy((current) => ({ ...current, [record.document_id]: 'delete' }));
    setFeedback((current) => ({ ...current, [record.document_id]: '' }));
    try {
      await deleteDocumentImport(projectId, record.document_id, record.revision);
      const result = await load();
      if (!result) throw new Error(t('Source.documentImport.refreshFailed'));
      await refreshNavCounts();
      setFeedback((current) => ({ ...current, [record.document_id]: t('Source.documentImport.deleteAccepted') }));
    } catch (cause) {
      setFeedback((current) => ({ ...current, [record.document_id]: recordError(cause).message }));
      void load();
    } finally {
      setBusy((current) => ({ ...current, [record.document_id]: undefined }));
    }
  };

  if (records.length === 0 && !loading && !error) return null;

  return (
    <section className="space-y-3" aria-labelledby="document-imports-title">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 id="document-imports-title" className="text-xl font-semibold text-white">{t('Source.documentImport.listTitle')}</h2>
          {page ? <p className="mt-1 text-sm text-zinc-400">{t('Source.documentImport.failedCount', { count: page.failed_count })}</p> : null}
        </div>
        <label className="text-sm text-zinc-300">
          <span className="mr-2">{t('Source.documentImport.filterLabel')}</span>
          <select value={filter} onChange={(event) => changeFilter(event.target.value as ImportFilter)} className="min-h-10 rounded-md border border-white/10 bg-zinc-900 px-2 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
            {FILTERS.map((value) => <option key={value} value={value}>{t(`Source.documentImport.filter.${value}`)}</option>)}
          </select>
        </label>
      </div>
      {error ? <p role="alert" className="rounded-md border border-red-400/20 bg-red-950/30 p-3 text-sm text-red-100">{t('Source.documentImport.loadFailed')}: {error.message}</p> : null}
      {loading && records.length === 0 ? <p role="status" className="text-sm text-zinc-400">{t('Source.documentImport.loading')}</p> : null}
      <ul className="space-y-3">
        {visibleRecords.map((record) => {
          const conversionFailed = record.conversion_state === 'failed';
          const busyAction = busy[record.document_id];
          const status = record.deletion_state === 'deleting' ? t('Source.documentImport.deleting')
            : record.deletion_state === 'delete_failed' ? t('Source.documentImport.deleteFailed')
              : conversionFailed ? t(`Source.documentImport.failure.${record.failure?.safe_message_key ?? 'conversion_error'}`)
                : record.conversion_state === 'succeeded' && record.source_state === 'saved' ? t('Source.documentImport.sourceSaved')
                  : record.source_state === 'failed' ? t('Source.documentImport.sourceSaveFailed')
                    : record.source_state === 'unknown' ? t('Source.documentImport.statusUnknown')
                      : t(`Source.documentImport.state.${record.conversion_state}`);
          return (
            <li key={record.document_id} ref={record.document_id === requestedDocumentId ? targetRowRef : undefined} tabIndex={record.document_id === requestedDocumentId ? -1 : undefined}>
              <Surface variant="glass" className={`p-4 ${conversionFailed ? 'border-red-400/30' : ''}`}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <h3 className="break-words font-medium text-white">{record.original_filename}</h3>
                    <p className="mt-1 text-xs text-zinc-500">{record.media_type} · {record.bytes.toLocaleString()} B · {record.document_id}</p>
                    <p className={`mt-2 text-sm ${conversionFailed ? 'text-red-200' : 'text-zinc-300'}`}>{status}</p>
                    {record.conversion_state === 'succeeded' && record.pipeline_ref?.status ? <p className="mt-1 text-xs text-zinc-400">{t('Source.documentImport.pipelineState', { state: record.pipeline_ref.status })}</p> : null}
                    {record.failure?.diagnostic_id ? <p className="mt-1 font-mono text-xs text-zinc-500">{t('Source.documentImport.diagnostic', { id: record.failure.diagnostic_id })}</p> : null}
                    {feedback[record.document_id] ? <p className="mt-2 text-sm text-zinc-300" role={feedback[record.document_id]?.includes('failed') ? 'alert' : 'status'}>{feedback[record.document_id]}</p> : null}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <button type="button" onClick={() => void download(record)} disabled={Boolean(busyAction) || record.deletion_state === 'deleted'} className="inline-flex min-h-10 items-center gap-2 rounded-md border border-white/10 px-3 py-2 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400 disabled:opacity-50">
                      {busyAction === 'download' ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : <Download className="size-4" aria-hidden="true" />}{t('Source.documentImport.downloadOriginal')}
                    </button>
                    {(conversionFailed || (record.source_state === 'failed' && !record.source_ref)) ? (
                      <button type="button" onClick={() => void remove(record)} disabled={Boolean(busyAction)} className="inline-flex min-h-10 items-center gap-2 rounded-md border border-red-400/30 px-3 py-2 text-sm text-red-100 hover:bg-red-400/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-red-300 disabled:opacity-50">
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
