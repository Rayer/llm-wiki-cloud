'use client';

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Download, X } from 'lucide-react';
import { useSearchParams } from 'next/navigation';
import { ApiError, getRawFilePreview, getRawFiles, type RawFile } from '@/lib/api';
import { Badge } from './ui/Badge';
import { MarkdownView } from './MarkdownView';
import { Surface } from './ui/Surface';
import { EmptyState, ErrorState, LoadingState, useErrorIdCopy } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { useT } from '@/lib/i18n';
import { RawUploadPanel, type RawUploadsSettled } from './RawUploadPanel';

type RawPreviewKind = 'markdown' | 'html' | 'download';

type RawPreview = {
  file: RawFile;
  kind: RawPreviewKind;
  content: string;
  viewKey: string;
};

type RawClientError = { message: string; requestId?: string; diagnosticId?: string };
type RawPreviewError = RawClientError & { fileName: string; viewKey: string };
const EMPTY_RAW_FILES: RawFile[] = [];

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function formatUpdated(value: string, locale: 'zh-TW' | 'zh-CN' | 'en') {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(locale === 'zh-TW' ? 'zh-TW' : locale === 'zh-CN' ? 'zh-CN' : 'en-US', {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

function rawPreviewKind(filename: string): RawPreviewKind {
  const lower = filename.toLowerCase();
  if (lower.endsWith('.md')) return 'markdown';
  if (lower.endsWith('.html') || lower.endsWith('.htm')) return 'html';
  return 'download';
}

function sanitizeRawHtml(html: string) {
  return html.replace(/<script\b[\s\S]*?<\/script>/gi, '');
}

export function RawClient() {
  const { token } = useWorkspace();
  const identityKey = token ?? 'no-session';
  return <RawClientSession key={identityKey} />;
}

function RawClientSession() {
  const { t, locale } = useT();
  const { currentProject, projects, token } = useWorkspace();
  const currentProjectId = currentProject?.id;
  const searchParams = useSearchParams();
  const projectParam = searchParams.get('project');
  const hasProjectTarget = projectParam !== null;
  const requestedProjectId = projectParam?.trim() ?? '';
  const rawProjectId = hasProjectTarget ? requestedProjectId || undefined : currentProjectId;
  const linkedProjectName = rawProjectId
    ? projects?.find((project) => project.id === rawProjectId)?.name ?? rawProjectId
    : '';
  const highlightedFile = searchParams.get('file') ?? '';
  const rawScopeKey = JSON.stringify([hasProjectTarget, rawProjectId ?? '']);
  const rawViewKey = JSON.stringify([rawScopeKey, highlightedFile]);
  const [files, setFiles] = useState<RawFile[]>([]);
  const [filesScopeKey, setFilesScopeKey] = useState(rawScopeKey);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<RawClientError | null>(null);
  const [errorScopeKey, setErrorScopeKey] = useState(rawScopeKey);
  const [preview, setPreview] = useState<RawPreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<RawPreviewError | null>(null);
  const openedQueryFileRef = useRef('');
  const listRequestRef = useRef(0);
  const previewRequestRef = useRef(0);
  const downloadRequestRef = useRef(0);
  const [downloadError, setDownloadError] = useState<RawPreviewError | null>(null);
  const scopedFiles = filesScopeKey === rawScopeKey ? files : EMPTY_RAW_FILES;
  const scopedError = errorScopeKey === rawScopeKey ? error : null;
  const scopedLoading = loading || filesScopeKey !== rawScopeKey;
  const scopedPreview = preview?.viewKey === rawViewKey ? preview : null;
  const scopedPreviewError = previewError?.viewKey === rawViewKey ? previewError : null;
  const scopedDownloadError = downloadError?.viewKey === rawViewKey ? downloadError : null;
  const { copyErrorId, errorIdCopied } = useErrorIdCopy(scopedError, scopedError?.requestId, scopedError?.diagnosticId);
  const { copyErrorId: copyPreviewErrorId, errorIdCopied: previewErrorIdCopied } =
    useErrorIdCopy(scopedPreviewError, scopedPreviewError?.requestId, scopedPreviewError?.diagnosticId);
  const { copyErrorId: copyDownloadErrorId, errorIdCopied: downloadErrorIdCopied } =
    useErrorIdCopy(scopedDownloadError, scopedDownloadError?.requestId, scopedDownloadError?.diagnosticId);

  const previousScopeKeyRef = useRef(rawScopeKey);
  const previousViewKeyRef = useRef(rawViewKey);
  useLayoutEffect(() => {
    if (previousViewKeyRef.current === rawViewKey) return;
    previousViewKeyRef.current = rawViewKey;
    previewRequestRef.current += 1;
    downloadRequestRef.current += 1;
    openedQueryFileRef.current = '';
    setPreview(null);
    setPreviewLoading(false);
    setPreviewError(null);
    setDownloadError(null);
    if (previousScopeKeyRef.current !== rawScopeKey) {
      previousScopeKeyRef.current = rawScopeKey;
      listRequestRef.current += 1;
      setFiles([]);
      setLoading(true);
      setError(null);
    }
  }, [rawScopeKey, rawViewKey]);

  const refreshRawFiles = useCallback(async () => {
    const request = ++listRequestRef.current;
    if (hasProjectTarget && !rawProjectId) {
      setFiles([]);
      setFilesScopeKey(rawScopeKey);
      setErrorScopeKey(rawScopeKey);
      setError({ message: 'This raw link does not identify a Project.' });
      setLoading(false);
      return;
    }

    try {
      const data = await getRawFiles(rawProjectId);
      if (listRequestRef.current !== request) return;
      setFilesScopeKey(rawScopeKey);
      setError(null);
      setErrorScopeKey(rawScopeKey);
      setFiles(data);
    } catch (cause: unknown) {
      if (listRequestRef.current === request) {
        setFiles([]);
        setFilesScopeKey(rawScopeKey);
        setError({
          message: cause instanceof Error ? cause.message : String(cause),
          requestId: cause instanceof ApiError ? cause.requestId : undefined,
          diagnosticId: cause instanceof ApiError ? cause.diagnosticId : undefined,
        });
        setErrorScopeKey(rawScopeKey);
      }
    } finally {
      if (listRequestRef.current === request) setLoading(false);
    }
  }, [hasProjectTarget, rawProjectId, rawScopeKey]);

  const handleUploadsSettled = useCallback(({ projectId }: RawUploadsSettled) => {
    if (projectId !== rawProjectId) return;
    void refreshRawFiles();
  }, [rawProjectId, refreshRawFiles]);

  useEffect(() => {
    openedQueryFileRef.current = '';
    const timer = window.setTimeout(() => void refreshRawFiles(), 0);

    return () => {
      window.clearTimeout(timer);
      listRequestRef.current += 1;
      previewRequestRef.current += 1;
      downloadRequestRef.current += 1;
    };
  }, [rawProjectId, refreshRawFiles, token]);

  const sortedFiles = useMemo(
    () => [...scopedFiles].sort((a, b) => a.name.localeCompare(b.name)),
    [scopedFiles],
  );

  const openRawPreview = useCallback(async (file: RawFile) => {
    if (hasProjectTarget && !rawProjectId) return;
    const request = ++previewRequestRef.current;
    downloadRequestRef.current += 1;
    const kind = rawPreviewKind(file.name);
    setPreview({ file, kind, content: '', viewKey: rawViewKey });
    setPreviewError(null);
    setDownloadError(null);

    if (kind === 'download') {
      setPreviewLoading(false);
      return;
    }

    setPreviewLoading(true);
    try {
      const content = await getRawFilePreview(file.name, rawProjectId);
      if (previewRequestRef.current === request) {
        setPreview((current) => current?.viewKey === rawViewKey && current.file.name === file.name ? { ...current, content } : current);
      }
    } catch (err) {
      if (previewRequestRef.current === request) {
        setPreviewError({
          message: err instanceof Error ? err.message : t('Raw.loadPreviewFailed'),
          requestId: err instanceof ApiError ? err.requestId : undefined,
          diagnosticId: err instanceof ApiError ? err.diagnosticId : undefined,
          fileName: file.name,
          viewKey: rawViewKey,
        });
      }
    } finally {
      if (previewRequestRef.current === request) setPreviewLoading(false);
    }
  }, [hasProjectTarget, rawProjectId, rawViewKey, t]);

  useEffect(() => {
    if (!highlightedFile || hasProjectTarget && !rawProjectId || scopedLoading || scopedError || openedQueryFileRef.current === rawViewKey) {
      return;
    }

    const requestedFile = scopedFiles.find((file) => file.name === highlightedFile);
    if (!requestedFile) return;

    openedQueryFileRef.current = rawViewKey;
    const previewGeneration = previewRequestRef.current;
    window.setTimeout(() => {
      if (previewRequestRef.current === previewGeneration && previousViewKeyRef.current === rawViewKey) {
        void openRawPreview(requestedFile);
      }
    }, 0);
  }, [hasProjectTarget, highlightedFile, openRawPreview, rawProjectId, rawViewKey, scopedError, scopedFiles, scopedLoading]);

  const closePreview = useCallback(() => {
    previewRequestRef.current += 1;
    downloadRequestRef.current += 1;
    setPreview(null);
    setPreviewError(null);
    setDownloadError(null);
    setPreviewLoading(false);
  }, []);

  useEffect(() => {
    if (!scopedPreview) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closePreview();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [closePreview, scopedPreview]);

  async function downloadRawFile() {
    if (!scopedPreview || hasProjectTarget && !rawProjectId) return;
    const selectedPreview = scopedPreview;
    const projectId = rawProjectId;
    const request = ++downloadRequestRef.current;
    setDownloadError(null);

    try {
      const content = selectedPreview.content || await getRawFilePreview(selectedPreview.file.name, projectId);
      if (downloadRequestRef.current !== request) return;
      const url = window.URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = selectedPreview.file.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.URL.revokeObjectURL(url);
    } catch (err) {
      if (downloadRequestRef.current === request) {
        setDownloadError({
          message: err instanceof Error ? err.message : t('Raw.downloadFailed'),
          requestId: err instanceof ApiError ? err.requestId : undefined,
          diagnosticId: err instanceof ApiError ? err.diagnosticId : undefined,
          fileName: selectedPreview.file.name,
          viewKey: rawViewKey,
        });
      }
    }
  }

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">
          {t('Raw.title')}
        </h1>
        <p className="mt-2 max-w-2xl text-zinc-400">
          {t('Raw.description')}
        </p>
        {hasProjectTarget && linkedProjectName ? (
          <p className="mt-2 text-sm text-emerald-200" role="status">
            {t('Raw.linkedProject', { project: linkedProjectName })}
          </p>
        ) : null}
      </header>

      {currentProject && !hasProjectTarget ? (
        <RawUploadPanel
          key={token ?? 'no-session'}
          title={t('Raw.uploadTitle')}
          hint={t('Raw.uploadHint')}
          onQueueSettled={handleUploadsSettled}
        />
      ) : null}

      {scopedLoading ? <LoadingState label={t('Raw.loading')} /> : null}
      {scopedError ? (
        <ErrorState
          message={scopedError.message}
          requestId={scopedError.requestId}
          diagnosticId={scopedError.diagnosticId}
          requestIdLabel={t('QueryErrors.requestId')}
          diagnosticIdLabel={t('QueryErrors.diagnosticId')}
          copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
          errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
          errorIdCopied={errorIdCopied}
          onCopyErrorId={copyErrorId}
        />
      ) : null}
      {!scopedLoading && !scopedError && sortedFiles.length === 0 ? (
        <EmptyState message={t('Raw.empty')} />
      ) : null}

      {!scopedLoading && !scopedError && sortedFiles.length > 0 ? (
        <Surface variant="glass" className="overflow-hidden">
          <div className="overflow-x-auto">
            <table className="min-w-full text-left text-sm">
              <thead className="border-b border-white/10 bg-white/[0.03] text-xs uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-4 py-3 font-semibold">{t('Raw.name')}</th>
                  <th className="px-4 py-3 font-semibold">{t('Raw.size')}</th>
                  <th className="px-4 py-3 font-semibold">{t('Raw.updated')}</th>
                  <th className="px-4 py-3 font-semibold">{t('Raw.sha256')}</th>
                  <th className="px-4 py-3 font-semibold">{t('Raw.ingested')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-white/8">
                {sortedFiles.map((file) => {
                  const kind = rawPreviewKind(file.name);
                  const highlighted = file.name === highlightedFile;
                  return (
                    <tr
                      key={file.name}
                      className={`transition hover:bg-white/[0.03] ${highlighted ? 'bg-emerald-500/10' : ''}`}
                    >
                      <td className="max-w-xs px-4 py-3 font-medium">
                        <button
                          type="button"
                          onClick={() => openRawPreview(file)}
                          className="block max-w-xs truncate text-left text-white underline decoration-white/20 underline-offset-4 transition hover:text-emerald-200 hover:decoration-emerald-300"
                          title={file.name}
                          aria-label={
                            kind === 'download'
                              ? t('Raw.downloadAria', { file: file.name })
                              : t('Raw.previewAria', { file: file.name })
                          }
                        >
                          {file.name}
                        </button>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 tabular-nums text-zinc-300">
                        {formatBytes(file.size)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-zinc-400">
                        {formatUpdated(file.updated, locale)}
                      </td>
                      <td className="px-4 py-3">
                        <code
                          className="block max-w-[18rem] truncate rounded-md border border-white/10 bg-black/30 px-2 py-1 font-mono text-xs text-zinc-300"
                          title={file.sha256 || undefined}
                        >
                          {file.sha256 || '—'}
                        </code>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <Badge variant={file.ingested ? 'published' : 'muted'}>
                          {file.ingested ? t('Raw.ingestedYes') : t('Raw.ingestedNo')}
                        </Badge>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Surface>
      ) : null}

      {scopedPreview ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
          onClick={closePreview}
        >
          <Surface
            variant="elevated"
            className="flex max-h-[86vh] w-full max-w-4xl flex-col overflow-hidden"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="flex items-start justify-between gap-4 border-b border-white/10 p-4">
              <div className="min-w-0">
                <h2 className="truncate text-lg font-semibold text-white">{scopedPreview.file.name}</h2>
                <p className="mt-1 text-sm text-zinc-400">
                  {scopedPreview.kind === 'download' ? t('Raw.previewUnavailable') : t('Raw.previewTitle')}
                </p>
              </div>
              <button
                type="button"
                onClick={closePreview}
                className="rounded-md p-2 text-zinc-400 transition hover:bg-white/10 hover:text-white"
                aria-label={t('Raw.close')}
                title={t('Raw.close')}
              >
                <X size={18} aria-hidden="true" />
              </button>
            </div>

            <div className="min-h-0 flex-1 overflow-auto p-4">
              {previewLoading ? <LoadingState label={t('Raw.loadingPreview')} /> : null}
              {scopedPreviewError ? (
                <ErrorState
                  message={scopedPreviewError.message}
                  requestId={scopedPreviewError.requestId}
                  diagnosticId={scopedPreviewError.diagnosticId}
                  requestIdLabel={t('QueryErrors.requestId')}
                  diagnosticIdLabel={t('QueryErrors.diagnosticId')}
                  copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
                  errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
                  errorIdCopied={previewErrorIdCopied}
                  onCopyErrorId={copyPreviewErrorId}
                />
              ) : null}
              {scopedDownloadError ? (
                <ErrorState
                  message={scopedDownloadError.message}
                  requestId={scopedDownloadError.requestId}
                  diagnosticId={scopedDownloadError.diagnosticId}
                  requestIdLabel={t('QueryErrors.requestId')}
                  diagnosticIdLabel={t('QueryErrors.diagnosticId')}
                  copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
                  errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
                  errorIdCopied={downloadErrorIdCopied}
                  onCopyErrorId={copyDownloadErrorId}
                />
              ) : null}
              {!previewLoading && !scopedPreviewError && scopedPreview.kind === 'markdown' ? (
                <div className="rounded-md border border-white/10 bg-black/20 p-4">
                  <MarkdownView content={scopedPreview.content} />
                </div>
              ) : null}
              {!previewLoading && !scopedPreviewError && scopedPreview.kind === 'html' ? (
                <iframe
                  title={t('Raw.previewAria', { file: scopedPreview.file.name })}
                  srcDoc={sanitizeRawHtml(scopedPreview.content)}
                  sandbox=""
                  className="h-[60vh] w-full rounded-md border border-white/10 bg-white"
                />
              ) : null}
              {!previewLoading && !scopedPreviewError && scopedPreview.kind === 'download' ? (
                <div className="rounded-md border border-white/10 bg-black/20 p-6 text-sm text-zinc-300">
                  {t('Raw.notPreviewable')}
                </div>
              ) : null}
            </div>

            <div className="flex justify-end border-t border-white/10 p-4">
              <button
                type="button"
                onClick={downloadRawFile}
                className="inline-flex items-center gap-2 rounded-md border border-emerald-400/40 bg-emerald-500/10 px-3 py-2 text-sm font-medium text-emerald-100 transition hover:bg-emerald-500/20"
              >
                <Download size={16} aria-hidden="true" />
                {t('Raw.download')}
              </button>
            </div>
          </Surface>
        </div>
      ) : null}
    </div>
  );
}
