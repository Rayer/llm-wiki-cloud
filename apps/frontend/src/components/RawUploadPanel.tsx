'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { FileUp, Loader2, RotateCcw } from 'lucide-react';
import { RAW_UPLOAD_MAX_BYTES, uploadRawFile, type RawUploadResult } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';

type UploadItemStatus = 'queued' | 'uploading' | 'created' | 'already_exists' | 'replaced' | 'failed';

type UploadItem = {
  id: string;
  file: File;
  projectId: string;
  projectName: string;
  status: UploadItemStatus;
  progress: number;
  error?: string;
  result?: RawUploadResult;
};

export type RawUploadsSettled = { changed: boolean; projectId: string };

type RawUploadPanelProps = {
  title: string;
  hint: string;
  inputId?: string;
  onBlocked?: () => void;
  onQueueSettled?: (result: RawUploadsSettled) => void;
};

type FolderCandidate = {
  id: string;
  file: File;
  relativePath: string;
  selected: boolean;
  error?: string;
};

type UploadProject = { id: string; name: string };

const UPLOAD_CONCURRENCY = 3;
const RAW_ACCEPT =
  '.md,.txt,.html,.htm,.csv,.json,.xml,.yaml,.yml,.toml,.ini,.cfg,.log,.rst,.org,.tex';
const RAW_UPLOAD_FILENAME = /^[^/\x00]+\.(md|txt|html?|csv|json|xml|ya?ml|toml|ini|cfg|log|rst|org|tex)$/;

let uploadItemId = 0;

function folderCandidateError(file: File): string | undefined {
  if (file.size === 0) return 'RawUpload.emptyFile';
  if (file.size > RAW_UPLOAD_MAX_BYTES) return 'RawUpload.fileTooLarge';
  if (file.name === '.md' || !RAW_UPLOAD_FILENAME.test(file.name)) return 'RawUpload.unsupportedFile';
  if (file.name.includes('\\') || (file.name.length > 1 && file.name[1] === ':') || [...file.name].some((character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint < 32 || (codePoint >= 127 && codePoint <= 159);
  })) return 'RawUpload.unsupportedFile';
  if (new TextEncoder().encode(file.name).length > 512) return 'RawUpload.filenameTooLong';
  return undefined;
}

function formatBytes(bytes: number): string {
  return `${bytes.toLocaleString()} B`;
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
  const folderRef = useRef<HTMLInputElement>(null);
  const folderConfirmationRef = useRef(false);
  const mountedRef = useRef(false);
  const activeUploadsRef = useRef(0);
  const uploadItemsRef = useRef<UploadItem[]>([]);
  const settledProjectIdsRef = useRef(new Set<string>());
  const createdProjectIdsRef = useRef(new Set<string>());
  const changedProjectIdsRef = useRef(new Set<string>());
  const onQueueSettledRef = useRef(onQueueSettled);
  const [fileLabel, setFileLabel] = useState(t('Pipeline.selectFiles'));
  const [uploadItems, setUploadItems] = useState<UploadItem[]>([]);
  const [folderCandidates, setFolderCandidates] = useState<FolderCandidate[]>([]);
  const [folderCandidateProjectId, setFolderCandidateProjectId] = useState<string | null>(null);
  const [folderSelectionMade, setFolderSelectionMade] = useState(false);
  const [folderPickerSupported, setFolderPickerSupported] = useState(false);
  const currentProjectRef = useRef(currentProject);
  currentProjectRef.current = currentProject;

  onQueueSettledRef.current = onQueueSettled;

  useEffect(() => {
    mountedRef.current = true;
    setFolderPickerSupported('webkitdirectory' in document.createElement('input'));
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const clearFolderCandidates = useCallback(() => {
    setFolderCandidates([]);
    setFolderCandidateProjectId(null);
    setFolderSelectionMade(false);
    folderConfirmationRef.current = false;
    if (folderRef.current) folderRef.current.value = '';
  }, []);

  const previousProjectIdRef = useRef(currentProject?.id);
  useEffect(() => {
    if (previousProjectIdRef.current === currentProject?.id) return;
    previousProjectIdRef.current = currentProject?.id;
    clearFolderCandidates();
  }, [clearFolderCandidates, currentProject?.id]);

  const pumpUploadQueue = useCallback(() => {
    const pump = () => {
      if (!mountedRef.current) return;
      while (activeUploadsRef.current < UPLOAD_CONCURRENCY) {
        const next = uploadItemsRef.current.find((item) => item.status === 'queued');
        if (!next) break;

        activeUploadsRef.current += 1;
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
            }, next.projectId);
            if (!mountedRef.current) return;
            settledProjectIdsRef.current.add(next.projectId);
            if (result.status === 'created') createdProjectIdsRef.current.add(next.projectId);
            if (result.status === 'created' || result.status === 'replaced') {
              changedProjectIdsRef.current.add(next.projectId);
            }
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
              const settledProjectIds = [...settledProjectIdsRef.current];
              const createdProjectIds = createdProjectIdsRef.current;
              const changedProjectIds = changedProjectIdsRef.current;
              if (currentProjectRef.current && createdProjectIds.has(currentProjectRef.current.id)) {
                void refreshNavCounts();
              }
              for (const projectId of settledProjectIds) {
                onQueueSettledRef.current?.({ changed: changedProjectIds.has(projectId), projectId });
              }
              settledProjectIdsRef.current = new Set();
              createdProjectIdsRef.current = new Set();
              changedProjectIdsRef.current = new Set();
            }
            queueMicrotask(pump);
          }
        })();
      }
    };

    queueMicrotask(pump);
  }, [refreshNavCounts]);

  const enqueueFiles = useCallback((fileList: FileList | File[], project: UploadProject | null) => {
    if (isDemoSession) {
      onBlocked?.();
      return;
    }

    const files = Array.from(fileList);
    if (files.length === 0 || !project) return;

    const projectNameKey = (projectId: string, filename: string) => `${projectId}\0${filename}`;
    const knownNames = new Set(uploadItemsRef.current
      .filter((item) => item.status === 'queued' || item.status === 'uploading')
      .map((item) => projectNameKey(item.projectId, item.file.name)));
    const next = [...uploadItemsRef.current];
    for (const file of files) {
      const nameKey = projectNameKey(project.id, file.name);
      if (knownNames.has(nameKey)) {
        next.push({
          id: `upload-${++uploadItemId}`,
          file,
          projectId: project.id,
          projectName: project.name,
          status: 'failed',
          progress: 0,
          error: 'duplicate filename in batch (first file wins)',
        });
        continue;
      }
      knownNames.add(nameKey);
      next.push({
        id: `upload-${++uploadItemId}`,
        file,
        projectId: project.id,
        projectName: project.name,
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
    enqueueFiles(files, currentProject);
    if (fileRef.current) fileRef.current.value = '';
  }, [currentProject, enqueueFiles, t]);

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

  const visibleFolderCandidates = folderCandidateProjectId === currentProject?.id ? folderCandidates : [];
  const eligibleCandidates = visibleFolderCandidates.filter((candidate) => !candidate.error);
  const selectedCandidates = eligibleCandidates.filter((candidate) => candidate.selected);
  const selectedBytes = selectedCandidates.reduce((total, candidate) => total + candidate.file.size, 0);
  const selectedBasenameCounts = new Map<string, number>();
  for (const candidate of selectedCandidates) {
    selectedBasenameCounts.set(candidate.file.name, (selectedBasenameCounts.get(candidate.file.name) ?? 0) + 1);
  }
  const selectedByBasename = new Set(
    [...selectedBasenameCounts].filter(([, count]) => count > 1).map(([name]) => name),
  );
  const hasSelectedCollisions = selectedByBasename.size > 0;
  const canConfirmFolder = selectedCandidates.length > 0
    && !hasSelectedCollisions
    && !folderConfirmationRef.current
    && !isDemoSession
    && Boolean(currentProject)
    && folderCandidateProjectId === currentProject?.id;

  const handleFolderChange = useCallback(() => {
    const files = Array.from(folderRef.current?.files ?? []);
    folderConfirmationRef.current = false;
    setFolderCandidateProjectId(currentProject?.id ?? null);
    setFolderSelectionMade(true);
    setFolderCandidates(files.map((file, index) => {
      const error = folderCandidateError(file);
      return {
        id: `folder-${index}-${file.webkitRelativePath || file.name}`,
        file,
        relativePath: file.webkitRelativePath || file.name,
        selected: !error,
        error,
      };
    }));
    if (folderRef.current) folderRef.current.value = '';
  }, [currentProject?.id]);

  const confirmFolder = useCallback(() => {
    if (!canConfirmFolder || !currentProject || folderConfirmationRef.current) return;
    folderConfirmationRef.current = true;
    const selected = selectedCandidates.map((candidate) => candidate.file);
    enqueueFiles(selected, currentProject);
    setFolderCandidates([]);
    setFolderCandidateProjectId(null);
    setFolderSelectionMade(false);
    if (folderRef.current) folderRef.current.value = '';
  }, [canConfirmFolder, currentProject, enqueueFiles, selectedCandidates]);

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
        <input
          ref={(input) => {
            folderRef.current = input;
            input?.setAttribute('webkitdirectory', '');
          }}
          type="file"
          multiple
          onChange={handleFolderChange}
          className="sr-only"
          id={`${inputId}-folder`}
          tabIndex={-1}
          aria-hidden="true"
          disabled={isDemoSession || uploading || !currentProject || !folderPickerSupported}
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
        <button
          type="button"
          onClick={() => {
            if (isDemoSession) {
              onBlocked?.();
              return;
            }
            folderRef.current?.click();
          }}
          disabled={uploading || !currentProject || !folderPickerSupported}
          aria-disabled={isDemoSession || undefined}
          title={isDemoSession ? t('Demo.restricted') : undefined}
          aria-describedby={`${inputId}-folder-hint`}
          className="inline-flex shrink-0 items-center justify-center gap-2 rounded-md border border-emerald-500/40 px-4 py-2 text-sm font-semibold text-emerald-200 transition hover:bg-emerald-500/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {t('RawUpload.chooseFolder')}
        </button>
      </div>

      <p id={`${inputId}-folder-hint`} className="mt-2 text-xs text-zinc-500">
        {folderPickerSupported ? t('RawUpload.folderHint') : t('RawUpload.folderUnsupportedBrowser')}
      </p>

      {folderCandidateProjectId === currentProject?.id && folderSelectionMade ? (
        <div className="mt-3 space-y-3 rounded-md border border-white/10 bg-black/20 p-3" aria-live="polite">
          {visibleFolderCandidates.length === 0 ? (
            <p className="text-sm text-zinc-400">{t('RawUpload.emptyFolder')}</p>
          ) : (
            <>
              <p className="text-xs text-zinc-400">
                {t('RawUpload.importIntoProject', { project: currentProject?.name ?? '', id: currentProject?.id ?? '' })}
              </p>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-xs text-zinc-300">
                  {t('RawUpload.selectionSummary', { count: selectedCandidates.length, bytes: formatBytes(selectedBytes) })}
                </p>
                <div className="flex flex-wrap gap-2">
                  <button
                    type="button"
                    onClick={() => setFolderCandidates((candidates) => candidates.map((candidate) => (
                      candidate.error ? candidate : { ...candidate, selected: true }
                    )))}
                    disabled={eligibleCandidates.length === 0}
                    className="rounded px-2 py-1 text-xs text-emerald-200 hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {t('RawUpload.selectAllEligible')}
                  </button>
                  <button
                    type="button"
                    onClick={() => setFolderCandidates((candidates) => candidates.map((candidate) => ({ ...candidate, selected: false })))}
                    disabled={selectedCandidates.length === 0}
                    className="rounded px-2 py-1 text-xs text-zinc-300 hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {t('RawUpload.clearSelection')}
                  </button>
                </div>
              </div>

              {hasSelectedCollisions ? (
                <p className="text-sm text-amber-200" role="alert">{t('RawUpload.basenameCollision')}</p>
              ) : null}

              <ul className="max-h-72 space-y-2 overflow-y-auto" aria-label={t('RawUpload.candidateList')}>
                {visibleFolderCandidates.map((candidate) => {
                  const collision = selectedByBasename.has(candidate.file.name);
                  return (
                    <li key={candidate.id} className="flex items-start gap-2 rounded border border-white/5 bg-black/20 p-2 text-xs">
                      <input
                        type="checkbox"
                        checked={candidate.selected}
                        disabled={Boolean(candidate.error)}
                        onChange={(event) => setFolderCandidates((candidates) => candidates.map((item) => (
                          item.id === candidate.id ? { ...item, selected: event.target.checked } : item
                        )))}
                        aria-label={t('RawUpload.selectCandidate', { path: candidate.relativePath })}
                        className="mt-0.5 size-4 shrink-0 accent-emerald-500 disabled:opacity-50"
                      />
                      <div className="min-w-0 flex-1">
                        <p className="truncate font-medium text-zinc-200" title={candidate.file.name}>{candidate.file.name}</p>
                        <p className="break-all text-zinc-400" title={candidate.relativePath}>{candidate.relativePath}</p>
                        <p className="mt-0.5 text-zinc-500">
                          {formatBytes(candidate.file.size)} · {t('RawUpload.destination', { filename: candidate.file.name })}
                        </p>
                        {candidate.error ? <p className="mt-0.5 text-amber-200">{t(candidate.error)}</p> : null}
                        {collision ? <p className="mt-0.5 text-amber-200">{t('RawUpload.collisionForFile')}</p> : null}
                      </div>
                    </li>
                  );
                })}
              </ul>

              <div className="flex flex-wrap justify-end gap-2">
                <button
                  type="button"
                  onClick={clearFolderCandidates}
                  className="rounded-md border border-white/10 px-3 py-2 text-sm text-zinc-300 hover:bg-white/5"
                >
                  {t('RawUpload.cancel')}
                </button>
                <button
                  type="button"
                  onClick={confirmFolder}
                  disabled={!canConfirmFolder || uploading}
                  className="rounded-md bg-emerald-600 px-3 py-2 text-sm font-semibold text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {t('RawUpload.confirmImport')}
                </button>
              </div>
            </>
          )}
        </div>
      ) : null}

      {uploadItems.length > 0 ? (
        <div className="mt-3 space-y-2">
          <p className="text-xs text-zinc-500" data-testid="upload-summary">{uploadSummary}</p>
          <ul className="max-h-48 space-y-1.5 overflow-y-auto">
            {uploadItems.map((item) => (
              <li key={item.id} className="flex items-start justify-between gap-2 rounded-md border border-white/5 bg-black/20 px-2.5 py-1.5 text-xs">
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium text-zinc-200" title={item.file.name}>{item.file.name}</p>
                  <p className="text-zinc-500">{t('RawUpload.projectLabel', { project: item.projectName, id: item.projectId })}</p>
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
