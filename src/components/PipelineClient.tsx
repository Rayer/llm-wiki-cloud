'use client';

import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { FileUp, Link2, Cog, Loader2, Construction, RotateCcw } from 'lucide-react';
import { Surface } from './ui/Surface';
import { Badge } from './ui/Badge';
import {
  getPipelineStatus,
  triggerPipeline,
  uploadRawFile,
  type PipelineQuota,
  type PipelineResult,
  type PipelineStatus,
  type RawUploadResult,
} from '@/lib/api';
import {
  blockReasonMessage,
  formatQuotaLine,
  isRunBlocked,
} from '@/lib/pipeline-quota';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';

type Toast = {
  id: number;
  message: string;
  type: 'success' | 'error' | 'info';
};

type UploadItemStatus = 'queued' | 'uploading' | 'created' | 'already_exists' | 'failed';

type UploadItem = {
  id: string;
  file: File;
  status: UploadItemStatus;
  error?: string;
  result?: RawUploadResult;
};

const UPLOAD_CONCURRENCY = 3;
const RAW_ACCEPT =
  '.md,.txt,.html,.htm,.csv,.json,.xml,.yaml,.yml,.toml,.ini,.cfg,.log,.rst,.org,.tex';

let toastId = 0;
let uploadItemId = 0;

function pipelineStatusBadge(status: PipelineStatus | null): string | null {
  const executionStatus = status?.last_execution?.status;
  const duration = status?.last_execution?.duration ?? 'pending';

  if (executionStatus === 'RUNNING') return 'Pipeline running...';
  if (executionStatus === 'SUCCEEDED') return `Pipeline complete (${duration})`;
  if (executionStatus === 'FAILED') return `Pipeline failed (${duration})`;

  return null;
}

function uploadStatusLabel(status: UploadItemStatus): string {
  switch (status) {
    case 'queued':
      return 'Queued';
    case 'uploading':
      return 'Uploading';
    case 'created':
      return 'Created';
    case 'already_exists':
      return 'Already exists';
    case 'failed':
      return 'Failed';
  }
}

function summarizeUploads(items: UploadItem[]): string {
  const created = items.filter((i) => i.status === 'created').length;
  const existing = items.filter((i) => i.status === 'already_exists').length;
  const failed = items.filter((i) => i.status === 'failed').length;
  const pending = items.filter((i) => i.status === 'queued' || i.status === 'uploading').length;
  const parts = [
    `${created} created`,
    `${existing} already exists`,
    `${failed} failed`,
  ];
  if (pending > 0) parts.push(`${pending} in progress`);
  return parts.join(', ');
}

function isCooldownClear(quota: PipelineQuota | null | undefined, now = new Date()): boolean {
  if (!quota?.enforced) return true;
  if (!quota.cooldown_until) return true;
  const ms = new Date(quota.cooldown_until).getTime() - now.getTime();
  return Number.isNaN(ms) || ms <= 0;
}

function isUnderDailyLimit(quota: PipelineQuota | null | undefined): boolean {
  if (!quota?.enforced) return true;
  return quota.runs_today < quota.daily_limit;
}

function hasNewRaw(quota: PipelineQuota | null | undefined): boolean {
  if (!quota?.enforced) return true;
  return quota.new_raw_files >= quota.min_new_raw;
}

export function PipelineClient() {
  const { t } = useT();
  const { isDemoSession, refreshNavCounts, currentProject } = useWorkspace();
  const [fileLabel, setFileLabel] = useState('Choose files');
  const [uploadItems, setUploadItems] = useState<UploadItem[]>([]);
  const [scrapeUrlText, setScrapeUrlText] = useState('');
  const [pipelineResult, setPipelineResult] = useState<PipelineResult | null>(null);
  const [pipelineStatus, setPipelineStatus] = useState<PipelineStatus | null>(null);
  const [loading, setLoading] = useState<string | null>(null); // 'pipeline'
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [wipModal, setWipModal] = useState(false);
  const [showPrereq, setShowPrereq] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const pipelinePollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const activeUploadsRef = useRef(0);
  // Source of truth for the in-flight queue (state is a mirror for render).
  const uploadItemsRef = useRef<UploadItem[]>([]);
  const needsCountRefreshRef = useRef(false);

  const addToast = useCallback((message: string, type: Toast['type']) => {
    const id = ++toastId;
    setToasts((prev) => [...prev, { id, message, type }]);
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 4000);
  }, []);

  const stopPipelinePolling = useCallback(() => {
    if (pipelinePollRef.current) {
      clearInterval(pipelinePollRef.current);
      pipelinePollRef.current = null;
    }
  }, []);

  const pollPipelineStatus = useCallback(async () => {
    try {
      const status = await getPipelineStatus();
      setPipelineStatus(status);

      const executionStatus = status.last_execution?.status;
      if (executionStatus === 'SUCCEEDED' || executionStatus === 'FAILED') {
        stopPipelinePolling();
      }
    } catch (err) {
      stopPipelinePolling();
      addToast(err instanceof Error ? err.message : 'Pipeline status failed', 'error');
    }
  }, [addToast, stopPipelinePolling]);

  useEffect(() => stopPipelinePolling, [stopPipelinePolling]);

  // Load quota + execution status on mount and when workspace project changes.
  useEffect(() => {
    if (!currentProject) {
      setPipelineStatus(null);
      return;
    }

    let cancelled = false;
    void (async () => {
      try {
        const status = await getPipelineStatus();
        if (!cancelled) setPipelineStatus(status);
      } catch {
        // Initial status is best-effort; Run still surfaces trigger errors via toast.
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [currentProject]);

  const pumpUploadQueue = useCallback(() => {
    const pump = () => {
      while (activeUploadsRef.current < UPLOAD_CONCURRENCY) {
        const next = uploadItemsRef.current.find((item) => item.status === 'queued');
        if (!next) break;

        // Claim immediately on the ref so the same item is not double-started
        // before React state flushes.
        activeUploadsRef.current += 1;
        uploadItemsRef.current = uploadItemsRef.current.map((item) =>
          item.id === next.id ? { ...item, status: 'uploading', error: undefined } : item,
        );
        setUploadItems(uploadItemsRef.current);

        void (async () => {
          try {
            const result = await uploadRawFile(next.file);
            if (result.status === 'created') {
              needsCountRefreshRef.current = true;
            }
            uploadItemsRef.current = uploadItemsRef.current.map((item) =>
              item.id === next.id
                ? { ...item, status: result.status, result, error: undefined }
                : item,
            );
            setUploadItems(uploadItemsRef.current);
          } catch (err) {
            uploadItemsRef.current = uploadItemsRef.current.map((item) =>
              item.id === next.id
                ? {
                    ...item,
                    status: 'failed',
                    error: err instanceof Error ? err.message : 'Upload failed',
                  }
                : item,
            );
            setUploadItems(uploadItemsRef.current);
          } finally {
            activeUploadsRef.current = Math.max(0, activeUploadsRef.current - 1);
            const stillBusy = uploadItemsRef.current.some(
              (item) => item.status === 'queued' || item.status === 'uploading',
            );
            if (!stillBusy && needsCountRefreshRef.current) {
              needsCountRefreshRef.current = false;
              void refreshNavCounts();
              // New raw files may change quota eligibility.
              void getPipelineStatus()
                .then((status) => setPipelineStatus(status))
                .catch(() => {
                  // Keep prior status on refresh failure after upload.
                });
            }
            queueMicrotask(pump);
          }
        })();
      }
    };

    queueMicrotask(pump);
  }, [refreshNavCounts]);

  const enqueueFiles = useCallback(
    (fileList: FileList | File[]) => {
      if (isDemoSession) {
        addToast(t('Demo.restricted'), 'info');
        return;
      }

      const files = Array.from(fileList);
      if (files.length === 0) return;

      const knownNames = new Set(uploadItemsRef.current.map((item) => item.file.name));
      const next: UploadItem[] = [...uploadItemsRef.current];

      for (const file of files) {
        if (knownNames.has(file.name)) {
          next.push({
            id: `upload-${++uploadItemId}`,
            file,
            status: 'failed',
            error: 'duplicate filename in batch (first file wins)',
          });
          continue;
        }
        knownNames.add(file.name);
        next.push({
          id: `upload-${++uploadItemId}`,
          file,
          status: 'queued',
        });
      }

      uploadItemsRef.current = next;
      setUploadItems(next);
      setFileLabel(files.length === 1 ? files[0].name : `${files.length} files selected`);
      pumpUploadQueue();
    },
    [addToast, isDemoSession, pumpUploadQueue, t],
  );

  // When items transition to queued (retry or enqueue), ensure workers run.
  useEffect(() => {
    if (uploadItems.some((item) => item.status === 'queued')) {
      pumpUploadQueue();
    }
  }, [uploadItems, pumpUploadQueue]);

  const handleFileChange = useCallback(() => {
    const files = fileRef.current?.files;
    if (!files?.length) {
      setFileLabel('Choose files');
      return;
    }
    enqueueFiles(files);
    if (fileRef.current) fileRef.current.value = '';
  }, [enqueueFiles]);

  const handleRetry = useCallback(
    (id: string) => {
      uploadItemsRef.current = uploadItemsRef.current.map((item) =>
        item.id === id && item.status === 'failed'
          ? { ...item, status: 'queued', error: undefined }
          : item,
      );
      setUploadItems(uploadItemsRef.current);
      pumpUploadQueue();
    },
    [pumpUploadQueue],
  );

  const handleScrape = useCallback(async (event: FormEvent) => {
    event.preventDefault();
    setWipModal(true);
  }, []);

  const handleRunPipeline = useCallback(async () => {
    // Belt-and-suspenders: button is disabled for known blocks; toast only if click races through.
    if (isDemoSession) {
      addToast(t('Demo.restricted'), 'info');
      return;
    }

    if (!window.localStorage.getItem('llm-wiki-last-project')) {
      addToast(t('Pipeline.noProject'), 'error');
      return;
    }

    setLoading('pipeline');
    try {
      const result = await triggerPipeline();
      setPipelineResult(result);
      if (result.message) addToast(result.message, 'info');
      if (result.status === 'accepted') {
        stopPipelinePolling();
        setPipelineStatus((prev) => ({
          ...prev,
          last_execution: { status: 'RUNNING' },
          quota: prev?.quota
            ? { ...prev.quota, already_running: true, allowed: false }
            : prev?.quota,
        }));
        pipelinePollRef.current = setInterval(pollPipelineStatus, 5000);
      }
    } catch (err) {
      // Network / 5xx / stale UI race: show server message.
      addToast(err instanceof Error ? err.message : 'Pipeline trigger failed', 'error');
    } finally {
      setLoading(null);
    }
  }, [addToast, isDemoSession, pollPipelineStatus, stopPipelinePolling, t]);

  const pipelineStatusText = pipelineStatusBadge(pipelineStatus);
  const uploadSummary = useMemo(() => summarizeUploads(uploadItems), [uploadItems]);
  const uploading = uploadItems.some(
    (item) => item.status === 'queued' || item.status === 'uploading',
  );

  const lastExecutionStatus = pipelineStatus?.last_execution?.status;
  const executionRunning = lastExecutionStatus === 'RUNNING';
  const staleAlreadyRunning =
    pipelineStatus?.quota?.already_running === true && lastExecutionStatus !== 'RUNNING';
  const hasProject = Boolean(currentProject);
  const quota = pipelineStatus?.quota;
  const effectiveQuota = useMemo(
    () => (
      staleAlreadyRunning && quota
        ? { ...quota, allowed: true, already_running: false }
        : quota
    ),
    [quota, staleAlreadyRunning],
  );
  const blocked = isRunBlocked({
    isDemoSession,
    loading: loading === 'pipeline',
    hasProject,
    executionRunning,
    quota: effectiveQuota,
  });
  const helper = blockReasonMessage({
    isDemoSession,
    hasProject,
    executionRunning,
    quota: effectiveQuota,
    demoMessage: t('Demo.restricted'),
    noProjectMessage: t('Pipeline.noProject'),
  });
  const quotaLine = formatQuotaLine(effectiveQuota, new Date(), {
    quotaLine: t('Pipeline.quotaLine'),
    quotaNotEnforced: t('Pipeline.quotaNotEnforced'),
    cooldownClear: t('Pipeline.cooldownClear'),
  });
  const prereqRows = useMemo(
    () => [
      { ok: !isDemoSession, label: t('Pipeline.prereqDemo') },
      { ok: isUnderDailyLimit(effectiveQuota), label: t('Pipeline.prereqDaily') },
      { ok: isCooldownClear(effectiveQuota), label: t('Pipeline.prereqCooldown') },
      { ok: !executionRunning, label: t('Pipeline.prereqRunning') },
      { ok: hasNewRaw(effectiveQuota), label: t('Pipeline.prereqRaw') },
    ],
    [effectiveQuota, executionRunning, isDemoSession, t],
  );

  return (
    <>
      <Surface variant="glass" as="section" className="p-5">
        <h2 className="text-lg font-semibold text-white">{t('Pipeline.addContent')}</h2>
        <p className="mt-1 text-sm text-zinc-400">
          Upload markdown files or scrape URLs to feed the wiki pipeline.
        </p>

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          {/* File Upload */}
          <div className="rounded-[var(--radius-md)] border border-white/10 bg-zinc-950/50 p-4">
            <h3 className="flex items-center gap-2 text-sm font-semibold text-zinc-300">
              <FileUp className="size-4 text-emerald-400" /> {t('Pipeline.uploadFiles')}
            </h3>
            <p className="mt-1 text-xs text-zinc-500">
              Upload one or more files to the raw/ directory (max 3 concurrent).
            </p>
            <div className="mt-3 flex gap-2">
              <input
                ref={fileRef}
                type="file"
                accept={RAW_ACCEPT}
                multiple
                onChange={handleFileChange}
                className="hidden"
                id="raw-file-upload"
              />
              <label
                htmlFor="raw-file-upload"
                className="flex-1 cursor-pointer rounded-md border border-white/10 bg-black/30 px-3 py-2 text-sm text-zinc-400 transition hover:border-zinc-500 hover:text-zinc-200 truncate"
              >
                {fileLabel}
              </label>
              <label
                htmlFor="raw-file-upload"
                className={`inline-flex cursor-pointer items-center justify-center rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 ${
                  uploading ? 'opacity-50' : ''
                }`}
              >
                {uploading ? <Loader2 className="size-4 animate-spin" /> : 'Select'}
              </label>
            </div>

            {uploadItems.length > 0 ? (
              <div className="mt-3 space-y-2">
                <p className="text-xs text-zinc-500" data-testid="upload-summary">
                  {uploadSummary}
                </p>
                <ul className="max-h-48 space-y-1.5 overflow-y-auto">
                  {uploadItems.map((item) => (
                    <li
                      key={item.id}
                      className="flex items-start justify-between gap-2 rounded-md border border-white/5 bg-black/20 px-2.5 py-1.5 text-xs"
                    >
                      <div className="min-w-0 flex-1">
                        <p className="truncate font-medium text-zinc-200" title={item.file.name}>
                          {item.file.name}
                        </p>
                        {item.error ? (
                          <p className="mt-0.5 text-red-300/90">{item.error}</p>
                        ) : null}
                      </div>
                      <div className="flex shrink-0 items-center gap-1.5">
                        <Badge
                          variant={
                            item.status === 'failed'
                              ? 'draft'
                              : item.status === 'created' || item.status === 'already_exists'
                                ? 'accent'
                                : 'muted'
                          }
                        >
                          {item.status === 'uploading' ? (
                            <span className="inline-flex items-center gap-1">
                              <Loader2 className="size-3 animate-spin" />
                              {uploadStatusLabel(item.status)}
                            </span>
                          ) : (
                            uploadStatusLabel(item.status)
                          )}
                        </Badge>
                        {item.status === 'failed' ? (
                          <button
                            type="button"
                            onClick={() => handleRetry(item.id)}
                            className="rounded p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white"
                            aria-label={`Retry ${item.file.name}`}
                            title="Retry"
                          >
                            <RotateCcw className="size-3.5" />
                          </button>
                        ) : null}
                      </div>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
          </div>

          {/* URL Scrape */}
          <div className="rounded-[var(--radius-md)] border border-white/10 bg-zinc-950/50 p-4">
            <h3 className="flex items-center gap-2 text-sm font-semibold text-zinc-300">
              <Link2 className="size-4 text-blue-400" /> {t('Pipeline.scrapeUrl')}
            </h3>
            <p className="mt-1 text-xs text-zinc-500">Fetch a web page and save as raw content.</p>
            <form onSubmit={handleScrape} className="mt-3 flex gap-2">
              <input
                type="url"
                value={scrapeUrlText}
                onChange={(e) => setScrapeUrlText(e.target.value)}
                placeholder="https://example.com/article"
                className="flex-1 rounded-md border border-white/10 bg-black/30 px-3 py-2 text-sm text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-300"
              />
              <button
                type="submit"
                disabled={loading === 'scrape'}
                className="inline-flex items-center justify-center rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
              >
                {loading === 'scrape' ? <Loader2 className="size-4 animate-spin" /> : t('Pipeline.scrape')}
              </button>
            </form>
          </div>
        </div>

        {/* Pipeline Trigger */}
        <div className="mt-4 rounded-[var(--radius-md)] border border-emerald-400/20 bg-emerald-400/[0.04] p-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h3 className="flex items-center gap-2 text-sm font-semibold text-emerald-300">
                <Cog className="size-4" /> {t('Pipeline.pipeline')}
              </h3>
              <p className="mt-1 text-xs text-zinc-400">
                Trigger the OLW pipeline to ingest, compile, and publish.
              </p>
            </div>
            <button
              type="button"
              onClick={handleRunPipeline}
              disabled={blocked}
              title={helper || undefined}
              aria-disabled={blocked}
              className="rounded-md bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {loading === 'pipeline' ? t('Pipeline.running') : t('Pipeline.runPipeline')}
            </button>
          </div>
          <p className="mt-2 text-xs text-zinc-500" data-testid="pipeline-quota-line">
            {quotaLine}
          </p>
          {blocked && helper ? (
            <p className="mt-1 text-xs text-amber-200/90" data-testid="pipeline-block-reason">
              {helper}
            </p>
          ) : null}
          <div className="mt-2">
            <button
              type="button"
              onClick={() => setShowPrereq((open) => !open)}
              className="text-xs font-medium text-zinc-400 transition hover:text-zinc-200"
              aria-expanded={showPrereq}
              data-testid="pipeline-prereq-toggle"
            >
              {showPrereq ? '▾' : '▸'} {t('Pipeline.prerequisites')}
            </button>
            {showPrereq ? (
              <ul className="mt-1.5 space-y-1" data-testid="pipeline-prereq-list">
                {prereqRows.map((row) => (
                  <li
                    key={row.label}
                    className="flex items-center gap-2 text-xs text-zinc-400"
                  >
                    <span aria-hidden="true">{row.ok ? '✓' : '✗'}</span>
                    <span className={row.ok ? 'text-zinc-400' : 'text-amber-200/90'}>
                      {row.label}
                    </span>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
          {pipelineResult?.message ? (
            <p className="mt-2 text-xs text-zinc-500">{pipelineResult.message}</p>
          ) : null}
        </div>

        {/* Pipeline Info */}
        <div className="mt-3 rounded-[var(--radius-md)] border border-white/5 bg-zinc-950/40 p-3">
          <p className="flex flex-wrap items-center gap-2 text-xs text-zinc-500">
            <span>{t('Pipeline.pipelineDesc')}</span>
            {pipelineStatusText ? (
              <Badge variant="accent">{pipelineStatusText}</Badge>
            ) : (
              <>
                <strong className="text-zinc-300">ingest</strong> (analyze raw notes) →{' '}
                <strong className="text-zinc-300">compile</strong> (synthesize wiki articles) →{' '}
                <strong className="text-zinc-300">lint</strong> (check quality) →{' '}
                <strong className="text-zinc-300">publish</strong> (auto-approve).
              </>
            )}
          </p>
        </div>
      </Surface>

      {/* Toast notifications */}
      <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2">
        {toasts.map((toast) => (
          <div
            key={toast.id}
            className={`rounded-lg border px-4 py-2 text-sm shadow-lg backdrop-blur ${
              toast.type === 'success'
                ? 'border-emerald-300/30 bg-emerald-900/80 text-emerald-200'
                : toast.type === 'error'
                  ? 'border-red-300/30 bg-red-900/80 text-red-200'
                  : 'border-zinc-300/30 bg-zinc-800/80 text-zinc-200'
            }`}
          >
            {toast.message}
          </div>
        ))}
      </div>

      {/* WIP Modal */}
      {wipModal ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
          onClick={() => setWipModal(false)}
        >
          <div
            className="relative rounded-xl border border-white/10 bg-[#151515] p-8 shadow-2xl max-w-sm w-full text-center"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              onClick={() => setWipModal(false)}
              className="absolute right-4 top-4 rounded-md p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white"
              aria-label="Close"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"/>
                <line x1="6" y1="6" x2="18" y2="18"/>
              </svg>
            </button>
            <Construction className="mx-auto mb-4 size-10 text-amber-400" />
            <h2 className="text-xl font-semibold text-white mb-2">功能實作中</h2>
            <p className="text-sm text-zinc-400">
              This feature is under development and will be available soon.
            </p>
          </div>
        </div>
      ) : null}
    </>
  );
}
