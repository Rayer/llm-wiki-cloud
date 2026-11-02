'use client';

import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import { acceptScrapeCandidate, rejectScrapeCandidate, resolveScrapeCandidate, scrapeUrl, uploadRawFile, type RawUploadResult, type ScrapeResult } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { RawUploadPanel, type RawUploadsSettled } from './RawUploadPanel';
import { UrlPreviewDialog } from './UrlPreviewDialog';
import { NavigationLink } from './NavigationBlocker';

type Tab = 'text' | 'url' | 'file';

type AddMaterialActionProps = {
  className?: string;
  onMaterialPersisted?: () => Promise<boolean>;
};

export function AddMaterialAction({ className = '', onMaterialPersisted }: AddMaterialActionProps) {
  const { t } = useT();
  const { currentProject, isDemoSession, user, sessionIdentityEpoch, refreshNavCounts } = useWorkspace();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<Tab>('text');
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const [url, setUrl] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [refreshError, setRefreshError] = useState(false);
  const [refreshPending, setRefreshPending] = useState(false);
  const [saved, setSaved] = useState<RawUploadResult | null>(null);
  const [urlResult, setUrlResult] = useState<ScrapeResult | null>(null);
  const [urlBusy, setUrlBusy] = useState(false);
  const [urlError, setUrlError] = useState<string | null>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const mountedRef = useRef(false);
  const materialRefreshSequenceRef = useRef(0);
  const scopeRef = useRef({
    projectId: currentProject?.id ?? null,
    ownerId: user?.id ?? null,
    sessionIdentityEpoch,
    generation: 0,
  });

  useLayoutEffect(() => {
    const previous = scopeRef.current;
    const next = {
      projectId: currentProject?.id ?? null,
      ownerId: user?.id ?? null,
      sessionIdentityEpoch,
    };
    if (
      previous.projectId !== next.projectId
      || previous.ownerId !== next.ownerId
      || previous.sessionIdentityEpoch !== next.sessionIdentityEpoch
    ) {
      scopeRef.current = { ...next, generation: previous.generation + 1 };
    }
  }, [currentProject?.id, sessionIdentityEpoch, user?.id]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const isCurrentScope = (projectId: string, generation: number) => (
    mountedRef.current
    && scopeRef.current.projectId === projectId
    && scopeRef.current.generation === generation
  );

  async function refreshAfterMaterialPersisted(projectId: string, generation: number) {
    if (!isCurrentScope(projectId, generation)) return;
    const sequence = ++materialRefreshSequenceRef.current;
    const isCurrentRefresh = () => isCurrentScope(projectId, generation)
      && sequence === materialRefreshSequenceRef.current;
    setRefreshPending(true);
    setRefreshError(false);
    try {
      const refreshed = await (onMaterialPersisted ?? refreshNavCounts)();
      if (isCurrentRefresh()) setRefreshError(!refreshed);
    } catch {
      if (isCurrentRefresh()) setRefreshError(true);
    } finally {
      if (isCurrentRefresh()) setRefreshPending(false);
    }
  }

  function handleFilesSettled({ projectId }: RawUploadsSettled) {
    const scope = scopeRef.current;
    if (scope.projectId !== projectId) return;
    void refreshAfterMaterialPersisted(projectId, scope.generation);
  }

  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy && !urlBusy) {
        setOpen(false);
        queueMicrotask(() => triggerRef.current?.focus());
      }
      if (event.key !== 'Tab') return;
      const items = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"])') ?? []);
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && (document.activeElement === first || !dialogRef.current?.contains(document.activeElement))) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first.focus();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [busy, open, urlBusy]);

  const close = () => {
    setOpen(false);
    queueMicrotask(() => triggerRef.current?.focus());
  };

  async function saveText(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject || !title.trim() || !body.trim() || busy || isDemoSession) return;
    const projectId = currentProject.id;
    const scopeGeneration = scopeRef.current.generation;
    setBusy(true); setError(''); setSaved(null);
    try {
      const filename = `note-${crypto.randomUUID()}.md`;
      const markdown = `# ${title.trim()}\n\n${body}`;
      const result = await uploadRawFile(new File([markdown], filename, { type: 'text/markdown;charset=utf-8' }), undefined, projectId);
      if (!isCurrentScope(projectId, scopeGeneration)) return;
      if ('document_id' in result) throw new Error('The Markdown upload was routed to document import unexpectedly.');
      setSaved(result);
      await refreshAfterMaterialPersisted(projectId, scopeGeneration);
    } catch (cause) {
      if (isCurrentScope(projectId, scopeGeneration)) setError(cause instanceof Error ? cause.message : t('AddMaterial.saveFailed'));
    } finally {
      if (isCurrentScope(projectId, scopeGeneration)) setBusy(false);
    }
  }

  async function previewUrl(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!currentProject || !url.trim() || urlBusy || isDemoSession) return;
    setUrlBusy(true); setUrlError(null); setUrlResult(null);
    try { setUrlResult(await scrapeUrl(url.trim(), currentProject.id)); }
    catch (cause) { setUrlError(cause instanceof Error ? cause.message : t('AddMaterial.urlFailed')); }
    finally { setUrlBusy(false); }
  }

  async function acceptUrl(nextTitle: string) {
    if (!urlResult?.candidate_id || !urlResult.candidate_revision || !currentProject) return;
    const projectId = currentProject.id;
    const scopeGeneration = scopeRef.current.generation;
    setUrlBusy(true); setUrlError(null); setRefreshError(false);
    try {
      const result = await acceptScrapeCandidate(urlResult.candidate_id, urlResult.candidate_revision, nextTitle, projectId);
      if (!isCurrentScope(projectId, scopeGeneration)) return;
      setUrlResult(result);
      if (result.status === 'stored' && result.storage) {
        await refreshAfterMaterialPersisted(projectId, scopeGeneration);
      }
    } catch (cause) {
      if (isCurrentScope(projectId, scopeGeneration)) setUrlError(cause instanceof Error ? cause.message : t('AddMaterial.urlFailed'));
    } finally {
      if (isCurrentScope(projectId, scopeGeneration)) setUrlBusy(false);
    }
  }
  async function declineUrl() {
    if (!urlResult?.candidate_id || !urlResult.candidate_revision || !currentProject) return;
    setUrlBusy(true); setUrlError(null);
    try { setUrlResult(await rejectScrapeCandidate(urlResult.candidate_id, urlResult.candidate_revision, currentProject.id)); }
    catch (cause) { setUrlError(cause instanceof Error ? cause.message : t('AddMaterial.urlFailed')); }
    finally { setUrlBusy(false); }
  }
  async function resolveUrl() {
    if (!urlResult?.candidate_id || !currentProject) return;
    const projectId = currentProject.id;
    const scopeGeneration = scopeRef.current.generation;
    setUrlBusy(true); setUrlError(null); setRefreshError(false);
    try {
      const result = await resolveScrapeCandidate(urlResult.candidate_id, projectId);
      if (!isCurrentScope(projectId, scopeGeneration)) return;
      setUrlResult(result);
      if (result.status === 'stored' && result.storage) {
        await refreshAfterMaterialPersisted(projectId, scopeGeneration);
      }
    } catch (cause) {
      if (isCurrentScope(projectId, scopeGeneration)) setUrlError(cause instanceof Error ? cause.message : t('AddMaterial.urlFailed'));
    } finally {
      if (isCurrentScope(projectId, scopeGeneration)) setUrlBusy(false);
    }
  }

  const urlLabels = {
    heading: t('AddMaterial.urlPreview'), title: t('Pipeline.urlTitleLabel'), save: t('Pipeline.urlConfirmCandidate'),
    decline: t('Pipeline.urlDeclineCandidate'), close: t('Pipeline.urlClosePreview'), checkStatus: t('Pipeline.urlCheckSaveStatus'),
    manualCapture: t('Pipeline.urlManualCapture'), processingSeparate: t('Pipeline.urlProcessingSeparate'), rawArticle: t('Pipeline.urlRawArticle'),
    created: t('Pipeline.urlCreated'), updated: t('Pipeline.urlUpdated'), unchanged: t('Pipeline.urlUnchanged'), digest: t('Pipeline.urlCandidateDigest'),
    loading: t('Pipeline.urlPreviewLoading'), qualityUnavailable: t('Pipeline.urlQualityCheckUnavailable'), qualityUnavailableSaved: t('Pipeline.urlQualityCheckUnavailableSaved'),
  };

  return <>
    <button ref={triggerRef} type="button" onClick={() => setOpen(true)} disabled={!currentProject || isDemoSession} className={`min-h-11 rounded border border-emerald-200/25 px-4 text-sm font-medium text-emerald-100 hover:bg-emerald-200/5 disabled:opacity-50 ${className}`}>
      {t('AddMaterial.open')}
    </button>
    {open ? <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-3 sm:p-5" onMouseDown={(event) => { if (event.target === event.currentTarget && !busy && !urlBusy) close(); }}>
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="add-material-title" className="flex max-h-[calc(100dvh-1.5rem)] w-full max-w-3xl flex-col overflow-hidden rounded-lg border border-white/15 bg-[#151515] shadow-2xl">
        <header className="flex items-start justify-between gap-4 border-b border-white/10 p-4 sm:p-5">
          <div className="min-w-0"><h2 id="add-material-title" className="text-lg font-semibold text-white">{t('AddMaterial.title')}</h2><p className="mt-1 break-words text-sm text-zinc-400">{t('AddMaterial.currentWiki')}: {currentProject?.name}</p><NavigationLink href="/profile" onClick={close} className="mt-2 inline-block text-xs text-emerald-200 underline">{t('AddMaterial.profileLink')}</NavigationLink></div>
          <button ref={closeRef} type="button" disabled={busy || urlBusy} onClick={close} className="min-h-10 rounded border border-white/10 px-3 text-sm text-zinc-300 disabled:opacity-50">{t('AddMaterial.close')}</button>
        </header>
        <div className="min-h-0 flex-1 overflow-auto p-4 sm:p-5">
          {refreshPending ? <p role="status" className="mb-3 text-sm text-zinc-300">{t('AddMaterial.refreshingStatus')}</p> : null}
          {refreshError ? <div className="mb-3 flex flex-wrap items-center gap-3 text-sm text-amber-200" role="alert"><span>{t('AddMaterial.refreshFailed')}</span><button type="button" onClick={() => { const scope = scopeRef.current; if (scope.projectId) void refreshAfterMaterialPersisted(scope.projectId, scope.generation); }} className="min-h-10 rounded border border-amber-200/30 px-3 underline">{t('AddMaterial.retryRefresh')}</button></div> : null}
          <div role="tablist" aria-label={t('AddMaterial.tabs')} className="flex flex-wrap gap-2">
            {(['text', 'url', 'file'] as const).map((value) => <button key={value} role="tab" type="button" aria-selected={tab === value} onClick={() => setTab(value)} className={`min-h-10 rounded px-3 text-sm ${tab === value ? 'bg-emerald-300 text-black' : 'border border-white/10 text-zinc-300'}`}>{t(`AddMaterial.${value}Tab`)}</button>)}
          </div>
          <p className="mt-3 text-xs leading-5 text-zinc-400">{t('AddMaterial.intentNote')}</p>
          {tab === 'text' ? <form onSubmit={(event) => void saveText(event)} className="mt-4 space-y-3">
            <label className="block text-sm text-zinc-300">{t('AddMaterial.textTitle')}<input required maxLength={200} value={title} onChange={(event) => setTitle(event.target.value)} className="mt-1 min-h-11 w-full rounded border border-white/15 bg-black/30 px-3 text-white" /></label>
            <label className="block text-sm text-zinc-300">{t('AddMaterial.textBody')}<textarea required value={body} onChange={(event) => setBody(event.target.value)} rows={8} className="mt-1 w-full rounded border border-white/15 bg-black/30 px-3 py-2 text-white" /></label>
            <p className="text-xs text-zinc-500">{t('AddMaterial.literalMarkdown')}</p>
            <button type="submit" disabled={busy || isDemoSession || !title.trim() || !body.trim()} className="min-h-11 rounded bg-emerald-300 px-4 text-sm font-semibold text-black disabled:opacity-50">{busy ? t('AddMaterial.saving') : t('AddMaterial.saveText')}</button>
            {error ? <p role="alert" className="text-sm text-amber-200">{error}</p> : null}
            {saved ? <p role="status" className="text-sm text-emerald-200">{t(saved.status === 'created' ? 'AddMaterial.savedNew' : 'AddMaterial.savedExisting')} · <NavigationLink href={`/raw?file=${encodeURIComponent(saved.path)}`} onClick={close} className="underline">{saved.path}</NavigationLink> · {t('AddMaterial.processingSeparate')}</p> : null}
          </form> : null}
          {tab === 'url' ? <form onSubmit={(event) => void previewUrl(event)} className="mt-4 space-y-3">
            <label className="block text-sm text-zinc-300">URL<input type="url" required value={url} onChange={(event) => setUrl(event.target.value)} className="mt-1 min-h-11 w-full rounded border border-white/15 bg-black/30 px-3 text-white" /></label>
            <p className="text-xs text-zinc-500">{t('AddMaterial.urlPreviewNote')}</p>
            <button type="submit" disabled={urlBusy || isDemoSession || !url.trim()} className="min-h-11 rounded bg-emerald-300 px-4 text-sm font-semibold text-black disabled:opacity-50">{urlBusy ? t('Pipeline.urlPreviewLoading') : t('AddMaterial.previewUrl')}</button>
            {urlError ? <p role="alert" className="text-sm text-amber-200">{urlError}</p> : null}
            {urlResult && !['preview', 'stored'].includes(urlResult.status) ? <p role="status" className="text-sm text-amber-100">{urlResult.message}</p> : null}
          </form> : null}
          {tab === 'file' ? <div className="mt-4"><p className="mb-3 text-xs text-zinc-400">{t('AddMaterial.fileStatusNote')}</p><RawUploadPanel title={t('AddMaterial.fileTitle')} hint={t('AddMaterial.fileHint')} inputId="add-material-files" onQueueSettled={handleFilesSettled} /></div> : null}
        </div>
      </div>
    </div> : null}
    <UrlPreviewDialog result={urlResult} loading={urlBusy && !urlResult} error={urlError} labels={urlLabels} onAccept={(nextTitle) => void acceptUrl(nextTitle)} onDecline={() => void declineUrl()} onResolve={() => void resolveUrl()} onClose={() => { if (!urlBusy) { setUrlResult(null); setUrlError(null); } }} />
  </>;
}
