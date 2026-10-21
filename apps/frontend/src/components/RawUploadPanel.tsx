'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { FileUp, Loader2, RotateCcw } from 'lucide-react';
import { uploadRawFile, type RawUploadResult } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';

type UploadItemStatus = 'queued' | 'uploading' | 'created' | 'already_exists' | 'replaced' | 'failed';

type UploadItem = {
  id: string;
  file: File;
  status: UploadItemStatus;
  progress: number;
  error?: string;
  result?: RawUploadResult;
};

export type RawUploadsSettled = { changed: boolean };

type RawUploadPanelProps = {
  title: string;
  hint: string;
  inputId?: string;
  onBlocked?: () => void;
  onQueueSettled?: (result: RawUploadsSettled) => void;
};

const UPLOAD_CONCURRENCY = 3;
const RAW_ACCEPT =
  '.md,.txt,.html,.htm,.csv,.json,.xml,.yaml,.yml,.toml,.ini,.cfg,.log,.rst,.org,.tex';

let uploadItemId = 0;

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
    case 'replaced':
      return 'Replaced';
    case 'failed':
      return 'Failed';
  }
}

function summarizeUploads(items: UploadItem[]): string {
  const created = items.filter((item) => item.status === 'created').length;
  const existing = items.filter((item) => item.status === 'already_exists').length;
  const replaced = items.filter((item) => item.status === 'replaced').length;
  const failed = items.filter((item) => item.status === 'failed').length;
  const pending = items.filter((item) => item.status === 'queued' || item.status === 'uploading').length;
  const parts = [
    `${created} created`,
    `${existing} already exists`,
  ];
  if (replaced > 0) parts.push(`${replaced} replaced`);
  parts.push(`${failed} failed`);
  if (pending > 0) parts.push(`${pending} in progress`);
  return parts.join(', ');
}

export function RawUploadPanel({
  title,
  hint,
  inputId = 'raw-file-upload',
  onBlocked,
  onQueueSettled,
}: RawUploadPanelProps) {
  const { t } = useT();
  const { isDemoSession, currentProject, refreshNavCounts } = useWorkspace();
  const fileRef = useRef<HTMLInputElement>(null);
  const mountedRef = useRef(false);
  const activeUploadsRef = useRef(0);
  const uploadItemsRef = useRef<UploadItem[]>([]);
  const needsCountRefreshRef = useRef(false);
  const needsChangeRefreshRef = useRef(false);
  const needsListRefreshRef = useRef(false);
  const onQueueSettledRef = useRef(onQueueSettled);
  const [fileLabel, setFileLabel] = useState(t('Pipeline.selectFiles'));
  const [uploadItems, setUploadItems] = useState<UploadItem[]>([]);

  onQueueSettledRef.current = onQueueSettled;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const pumpUploadQueue = useCallback(() => {
    const pump = () => {
      if (!mountedRef.current) return;
      while (activeUploadsRef.current < UPLOAD_CONCURRENCY) {
        const next = uploadItemsRef.current.find((item) => item.status === 'queued');
        if (!next) break;

        activeUploadsRef.current += 1;
        const projectId = currentProject?.id;
        uploadItemsRef.current = uploadItemsRef.current.map((item) =>
          item.id === next.id
            ? { ...item, status: 'uploading', progress: 0, error: undefined }
            : item,
        );
        setUploadItems(uploadItemsRef.current);

        void (async () => {
          try {
            const result = await uploadRawFile(next.file, (progress) => {
              if (!mountedRef.current) return;
              const current = uploadItemsRef.current.find((item) => item.id === next.id);
              if (!current || current.status !== 'uploading' || current.progress === progress) return;
              uploadItemsRef.current = uploadItemsRef.current.map((item) =>
                item.id === next.id ? { ...item, progress } : item,
              );
              setUploadItems(uploadItemsRef.current);
            }, projectId);
            if (!mountedRef.current) return;
            if (result.status === 'created') needsCountRefreshRef.current = true;
            if (result.status === 'created' || result.status === 'replaced') needsChangeRefreshRef.current = true;
            needsListRefreshRef.current = true;
            uploadItemsRef.current = uploadItemsRef.current.map((item) =>
              item.id === next.id
                ? { ...item, status: result.status, progress: 100, result, error: undefined }
                : item,
            );
            setUploadItems(uploadItemsRef.current);
          } catch (err) {
            if (!mountedRef.current) return;
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
            if (!mountedRef.current) return;
            const stillBusy = uploadItemsRef.current.some(
              (item) => item.status === 'queued' || item.status === 'uploading',
            );
            if (!stillBusy) {
              const created = needsCountRefreshRef.current;
              const changed = needsChangeRefreshRef.current;
              const succeeded = needsListRefreshRef.current;
              needsCountRefreshRef.current = false;
              needsChangeRefreshRef.current = false;
              needsListRefreshRef.current = false;
              if (created) void refreshNavCounts();
              if (succeeded) onQueueSettledRef.current?.({ changed });
            }
            queueMicrotask(pump);
          }
        })();
      }
    };

    queueMicrotask(pump);
  }, [currentProject?.id, refreshNavCounts]);

  const enqueueFiles = useCallback((fileList: FileList | File[]) => {
    if (isDemoSession) {
      onBlocked?.();
      return;
    }

    const files = Array.from(fileList);
    if (files.length === 0) return;

    const knownNames = new Set(uploadItemsRef.current.map((item) => item.file.name));
    const next = [...uploadItemsRef.current];
    for (const file of files) {
      if (knownNames.has(file.name)) {
        next.push({
          id: `upload-${++uploadItemId}`,
          file,
          status: 'failed',
          progress: 0,
          error: 'duplicate filename in batch (first file wins)',
        });
        continue;
      }
      knownNames.add(file.name);
      next.push({
        id: `upload-${++uploadItemId}`,
        file,
        status: 'queued',
        progress: 0,
      });
    }

    uploadItemsRef.current = next;
    setUploadItems(next);
    setFileLabel(files.length === 1 ? files[0].name : `${files.length} files selected`);
    pumpUploadQueue();
  }, [isDemoSession, onBlocked, pumpUploadQueue]);

  useEffect(() => {
    if (uploadItems.some((item) => item.status === 'queued')) pumpUploadQueue();
  }, [pumpUploadQueue, uploadItems]);

  const handleFileChange = useCallback(() => {
    const files = fileRef.current?.files;
    if (!files?.length) {
      setFileLabel(t('Pipeline.selectFiles'));
      return;
    }
    enqueueFiles(files);
    if (fileRef.current) fileRef.current.value = '';
  }, [enqueueFiles, t]);

  const handleRetry = useCallback((id: string) => {
    if (isDemoSession) {
      onBlocked?.();
      return;
    }
    uploadItemsRef.current = uploadItemsRef.current.map((item) =>
      item.id === id && item.status === 'failed'
        ? { ...item, status: 'queued', progress: 0, error: undefined }
        : item,
    );
    setUploadItems(uploadItemsRef.current);
    pumpUploadQueue();
  }, [isDemoSession, onBlocked, pumpUploadQueue]);

  const uploading = uploadItems.some((item) => item.status === 'queued' || item.status === 'uploading');
  const uploadSummary = useMemo(() => summarizeUploads(uploadItems), [uploadItems]);

  return (
    <section className="rounded-[var(--radius-md)] border border-white/10 bg-zinc-950/50 p-4" aria-labelledby={`${inputId}-title`}>
      <h2 id={`${inputId}-title`} className="flex items-center gap-2 text-sm font-semibold text-zinc-300">
        <FileUp className="size-4 text-emerald-400" /> {title}
      </h2>
      <p id={`${inputId}-hint`} className="mt-1 text-xs text-zinc-500">{hint}</p>
      <div className="mt-3 flex min-w-0 flex-col gap-2 sm:flex-row">
        <input
          ref={fileRef}
          type="file"
          accept={RAW_ACCEPT}
          multiple
          onChange={handleFileChange}
          className="sr-only"
          id={inputId}
          tabIndex={-1}
          aria-hidden="true"
          disabled={isDemoSession || uploading}
        />
        <p className="min-w-0 flex-1 truncate rounded-md border border-white/10 bg-black/30 px-3 py-2 text-sm text-zinc-400" aria-live="polite">
          {isDemoSession ? t('Demo.restricted') : fileLabel}
        </p>
        <button
          type="button"
          onClick={() => {
            if (isDemoSession) {
              onBlocked?.();
              return;
            }
            fileRef.current?.click();
          }}
          disabled={uploading}
          aria-disabled={isDemoSession || undefined}
          title={isDemoSession ? t('Demo.restricted') : undefined}
          aria-describedby={`${inputId}-hint`}
          className={`inline-flex shrink-0 items-center justify-center gap-2 rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 ${(isDemoSession || uploading) ? 'cursor-not-allowed opacity-50' : ''}`}
        >
          {isDemoSession ? t('Demo.restricted') : uploading ? <Loader2 className="size-4 animate-spin" /> : t('Pipeline.select')}
        </button>
      </div>

      {uploadItems.length > 0 ? (
        <div className="mt-3 space-y-2">
          <p className="text-xs text-zinc-500" data-testid="upload-summary">{uploadSummary}</p>
          <ul className="max-h-48 space-y-1.5 overflow-y-auto">
            {uploadItems.map((item) => (
              <li key={item.id} className="flex items-start justify-between gap-2 rounded-md border border-white/5 bg-black/20 px-2.5 py-1.5 text-xs">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium text-zinc-200" title={item.file.name}>{item.file.name}</p>
                  {item.result ? <p className="truncate text-zinc-400" title={item.result.path} data-testid="upload-path">{item.result.path}</p> : null}
                  <div
                    className="mt-1 h-1.5 overflow-hidden rounded-full bg-white/10"
                    role="progressbar"
                    aria-label={`${item.file.name}: ${uploadStatusLabel(item.status)} ${item.progress}%`}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={item.progress}
                  >
                    <div className="h-full rounded-full bg-emerald-400 transition-[width]" style={{ width: `${item.progress}%` }} />
                  </div>
                  <p className="mt-0.5 text-zinc-500">{uploadStatusLabel(item.status)} · {item.progress}%</p>
                  {item.error ? <p className="mt-0.5 text-red-300/90">{item.error}</p> : null}
                </div>
                <div className="flex shrink-0 items-center gap-1.5">
                  <Badge
                    variant={item.status === 'failed' ? 'draft' : item.status === 'created' || item.status === 'already_exists' || item.status === 'replaced' ? 'accent' : 'muted'}
                  >
                    {item.status === 'uploading' ? (
                      <span className="inline-flex items-center gap-1"><Loader2 className="size-3 animate-spin" />{uploadStatusLabel(item.status)}</span>
                    ) : uploadStatusLabel(item.status)}
                  </Badge>
                  {item.status === 'failed' ? (
                    <button
                      type="button"
                      onClick={() => handleRetry(item.id)}
                      disabled={isDemoSession}
                      className="rounded p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white disabled:cursor-not-allowed disabled:opacity-50"
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
    </section>
  );
}
