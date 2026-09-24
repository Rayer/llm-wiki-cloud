'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { Download, Package, X } from 'lucide-react';
import { useWorkspace } from './WorkspaceProvider';
import { Surface } from './ui/Surface';
import { useT } from '@/lib/i18n';
import {
  createExport, getExportState, requestExportDownload,
  type ExportArchive, type ExportJob, type ExportScope, type ExportState,
} from '@/lib/export-api';

const initialState: ExportState = {
  latest_job: null, current: null, previous: null, eligible: false, rejection_reason: null, next_allowed_at: null,
};
const scopes: ExportScope[] = ['raw', 'raw-full', 'raw-full-metadata'];
const inProgress = (job?: ExportJob | null) => job?.status === 'queued' || job?.status === 'running';
const MAX_TIMER_DELAY = 2_147_483_647;

function formatBytes(bytes: number | null, locale: string): string {
  if (bytes === null || !Number.isFinite(bytes) || bytes < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value)} ${units[unit]}`;
}

function formatTime(value: string | null, locale: string): string {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

export function ExportPanel() {
  const { t, locale } = useT();
  const { currentProject } = useWorkspace();
  const projectId = currentProject?.id ?? null;
  const [data, setData] = useState<ExportState>(initialState);
  const [dataProjectId, setDataProjectId] = useState<string | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [scope, setScope] = useState<ExportScope>('raw-full-metadata');
  const [submitting, setSubmitting] = useState(false);
  const [actionError, setActionError] = useState('');
  const generationRef = useRef(0);
  const dataProjectIdRef = useRef<string | null>(null);
  const creatingRef = useRef(false);
  const previousProjectIdRef = useRef(projectId);
  const dialogRef = useRef<HTMLElement>(null);
  const modalOpenerRef = useRef<HTMLElement | null>(null);
  const modalWasOpenRef = useRef(false);

  const refresh = useCallback(async (targetProjectId: string, generation: number, signal?: AbortSignal) => {
    try {
      const next = await getExportState(targetProjectId, signal);
      if (generationRef.current === generation) {
        dataProjectIdRef.current = targetProjectId;
        setData(next);
        setDataProjectId(targetProjectId);
      }
    } catch (error) {
      if (!signal?.aborted && generationRef.current === generation) {
        if (dataProjectIdRef.current !== targetProjectId) setData(initialState);
        dataProjectIdRef.current = targetProjectId;
        setDataProjectId(targetProjectId);
        setActionError(error instanceof Error ? error.message : t('Export.loadFailed'));
      }
    }
  }, [t]);

  useEffect(() => {
    const generation = ++generationRef.current;
    if (!projectId) return;
    const controller = new AbortController();
    void refresh(projectId, generation, controller.signal);
    return () => {
      controller.abort();
      generationRef.current += 1;
    };
  }, [projectId, refresh]);

  const loading = Boolean(projectId && dataProjectId !== projectId);
  const currentData = dataProjectId === projectId ? data : initialState;

  useEffect(() => {
    if (previousProjectIdRef.current === projectId) return;
    previousProjectIdRef.current = projectId;
    setModalOpen(false);
    setScope('raw-full-metadata');
    setActionError('');
  }, [projectId]);

  useEffect(() => {
    if (!projectId || dataProjectId !== projectId || !inProgress(data.latest_job)) return;
    const generation = generationRef.current;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const controller = new AbortController();
    const poll = async () => {
      await refresh(projectId, generation, controller.signal);
      if (!controller.signal.aborted && generationRef.current === generation) timer = setTimeout(poll, 2500);
    };
    timer = setTimeout(poll, 2500);
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [data.latest_job, dataProjectId, projectId, refresh]);

  useEffect(() => {
    const nextAllowedAt = currentData.next_allowed_at;
    if (!projectId || !nextAllowedAt) return;
    const timestamp = Date.parse(nextAllowedAt);
    if (!Number.isFinite(timestamp)) return;
    const generation = generationRef.current;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      void refresh(projectId, generation, controller.signal);
    }, Math.min(Math.max(0, timestamp - Date.now()), MAX_TIMER_DELAY));
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [currentData.next_allowed_at, projectId, refresh]);

  useEffect(() => {
    if (!modalOpen) {
      if (modalWasOpenRef.current) {
        modalWasOpenRef.current = false;
        modalOpenerRef.current?.focus();
        modalOpenerRef.current = null;
      }
      return;
    }

    modalWasOpenRef.current = true;
    (dialogRef.current?.querySelector<HTMLInputElement>('input:checked:not([disabled])')
      ?? dialogRef.current?.querySelector<HTMLInputElement>('input:not([disabled])'))?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setModalOpen(false);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ) ?? []);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [modalOpen]);

  const openModal = (opener: HTMLElement) => {
    modalOpenerRef.current = opener;
    setActionError('');
    setModalOpen(true);
  };

  const startExport = async () => {
    if (!projectId || !currentData.eligible || creatingRef.current) return;
    creatingRef.current = true;
    setSubmitting(true);
    const generation = generationRef.current;
    setActionError('');
    try {
      const job = await createExport(projectId, scope);
      if (generationRef.current !== generation) return;
      setModalOpen(false);
      setData((current) => ({ ...current, latest_job: job }));
      await refresh(projectId, generation);
    } catch (error) {
      if (generationRef.current === generation) {
        setActionError(error instanceof Error ? error.message : t('Export.createFailed'));
        await refresh(projectId, generation);
      }
    } finally {
      creatingRef.current = false;
      setSubmitting(false);
    }
  };

  const download = async (archive: ExportArchive) => {
    if (!projectId || !archive.download_available) return;
    const generation = generationRef.current;
    setActionError('');
    try {
      const result = await requestExportDownload(projectId, archive.export_id);
      if (generationRef.current === generation) window.location.assign(result.signed_url);
    } catch (error) {
      if (generationRef.current === generation) {
        setActionError(error instanceof Error ? error.message : t('Export.downloadFailed'));
        await refresh(projectId, generation);
      }
    }
  };

  const latest = currentData.latest_job;
  const active = inProgress(latest);
  const archives = [currentData.current, currentData.previous].filter((archive): archive is ExportArchive => Boolean(archive));
  const blockReason = currentData.rejection_reason === 'in_progress'
    ? t('Export.blockedRunning')
    : currentData.rejection_reason === 'cooldown'
      ? t('Export.blockedCooldown', { time: formatTime(currentData.next_allowed_at, locale) })
      : '';
  const disableReason = !projectId ? t('Export.selectProject') : blockReason;

  return (
    <Surface as="section" className="space-y-4 p-5" aria-labelledby="export-heading">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 id="export-heading" className="flex items-center gap-2 text-lg font-semibold text-zinc-100"><Package size={18} />{t('Export.title')}</h2>
          <p className="mt-1 text-sm text-zinc-400">{t('Export.description')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={(event) => openModal(event.currentTarget)} disabled={!projectId || !currentData.eligible || loading} className="rounded-lg bg-emerald-400 px-4 py-2 text-sm font-semibold text-zinc-950 transition hover:bg-emerald-300 disabled:cursor-not-allowed disabled:opacity-40">
            {active ? t('Export.preparing') : t('Export.package')}
          </button>
          <button type="button" onClick={() => archives[0] && void download(archives[0])} disabled={!archives[0]?.download_available} className="inline-flex items-center gap-2 rounded-lg border border-white/15 px-4 py-2 text-sm text-zinc-100 transition hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40">
            <Download size={15} />{active && archives[0] ? t('Export.downloadPrevious') : t('Export.download')}
          </button>
        </div>
      </div>
      {disableReason && <p className="text-sm text-amber-300" role="status">{disableReason}</p>}
      {!archives[0] && <p className="text-sm text-zinc-500">{t('Export.noDownloadReason')}</p>}
      {loading && <p className="text-sm text-zinc-400" role="status">{t('Export.loading')}</p>}
      {active && <div className="rounded-lg border border-sky-400/20 bg-sky-400/5 p-3 text-sm text-sky-200" role="status">
        <p className="font-medium">{t('Export.preparing')}</p><p className="mt-1 text-sky-200/70">{t('Export.leaveAndReturn')}</p>
        {latest?.next_allowed_at && <p className="mt-1 text-sky-200/70">{t('Export.nextAllowed', { time: formatTime(latest.next_allowed_at, locale) })}</p>}
      </div>}
      {latest?.status === 'ready' && <p className="text-sm text-emerald-300">{t('Export.ready')}</p>}
      {latest?.status === 'failed' && <div className="rounded-lg border border-red-400/20 bg-red-400/5 p-3 text-sm text-red-200" role="alert">
        <p>{t('Export.failed')}: {latest.error_message || latest.error_code || t('Export.unknownError')}</p>
        <button type="button" disabled={!currentData.eligible} onClick={(event) => openModal(event.currentTarget)} className="mt-2 underline disabled:opacity-50">{t('Export.retry')}</button>
      </div>}
      {latest?.status === 'expired' && <p className="text-sm text-amber-300" role="status">{t('Export.expired')}</p>}
      {archives.map((archive, index) => <dl key={archive.export_id} className="grid gap-2 rounded-lg border border-white/10 bg-black/10 p-3 text-sm sm:grid-cols-2">
        <div><dt className="text-zinc-500">{t('Export.scope')}</dt><dd className="text-zinc-200">{t(`Export.scope_${archive.scope}`)}</dd></div>
        <div><dt className="text-zinc-500">{t('Export.size')}</dt><dd className="text-zinc-200">{formatBytes(archive.size_bytes, locale)}</dd></div>
        <div><dt className="text-zinc-500">{t('Export.snapshot')}</dt><dd className="text-zinc-200">{formatTime(archive.snapshot_at, locale)}</dd></div>
        <div><dt className="text-zinc-500">{t('Export.expires')}</dt><dd className="text-zinc-200">{formatTime(archive.expires_at, locale)}</dd></div>
        {index === 1 && <button type="button" disabled={!archive.download_available} onClick={() => void download(archive)} className="justify-self-start text-sm text-emerald-300 underline disabled:opacity-40">{t('Export.downloadPrevious')}</button>}
      </dl>)}
      {currentData.next_allowed_at && !active && <p className="text-sm text-zinc-400">{t('Export.nextAllowed', { time: formatTime(currentData.next_allowed_at, locale) })}</p>}
      {actionError && <p className="text-sm text-red-300" role="alert">{actionError}</p>}
      {modalOpen && <div className="fixed inset-0 z-50 grid place-items-center bg-black/70 p-4" onMouseDown={(event) => { if (event.target === event.currentTarget) setModalOpen(false); }}>
        <section ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="export-dialog-title" className="max-h-[90vh] w-full max-w-xl overflow-y-auto rounded-2xl border border-white/10 bg-zinc-900 p-6 shadow-2xl">
          <div className="flex items-start justify-between gap-4"><div><h3 id="export-dialog-title" className="text-lg font-semibold text-white">{t('Export.modalTitle')}</h3><p className="mt-1 text-sm text-zinc-400">{t('Export.modalDescription')}</p></div><button type="button" aria-label={t('Export.close')} onClick={() => setModalOpen(false)} className="rounded p-1 text-zinc-400 hover:bg-white/10"><X size={18} /></button></div>
          <fieldset className="mt-5 space-y-2"><legend className="mb-2 text-sm font-medium text-zinc-200">{t('Export.scope')}</legend>{scopes.map((option) => <label key={option} className="flex cursor-pointer gap-3 rounded-lg border border-white/10 p-3 text-sm hover:bg-white/5"><input type="radio" name="export-scope" value={option} checked={scope === option} onChange={() => setScope(option)} /><span><span className="block font-medium text-zinc-100">{t(`Export.scope_${option}`)}</span><span className="mt-1 block text-zinc-400">{t(`Export.scopeDescription_${option}`)}</span></span></label>)}</fieldset>
          <ul className="mt-4 list-disc space-y-1 pl-5 text-sm text-zinc-400"><li>{t('Export.excludesSecrets')}</li><li>{t('Export.noCompile')}</li><li>{t('Export.noRestore')}</li></ul>
          {blockReason && <p className="mt-4 text-sm text-amber-300" role="status">{blockReason}</p>}
          {actionError && <p className="mt-3 text-sm text-red-300" role="alert">{actionError}</p>}
          <div className="mt-6 flex justify-end gap-2"><button type="button" onClick={() => setModalOpen(false)} className="rounded-lg border border-white/15 px-4 py-2 text-sm text-zinc-200">{t('Export.cancel')}</button><button type="button" disabled={!currentData.eligible || submitting} onClick={() => void startExport()} className="rounded-lg bg-emerald-400 px-4 py-2 text-sm font-semibold text-zinc-950 disabled:cursor-not-allowed disabled:opacity-40">{submitting ? t('Export.preparing') : t('Export.start')}</button></div>
        </section>
      </div>}
    </Surface>
  );
}
