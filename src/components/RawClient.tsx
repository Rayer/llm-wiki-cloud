'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Download, X } from 'lucide-react';
import { useSearchParams } from 'next/navigation';
import { getRawFilePreview, getRawFiles, type RawFile } from '@/lib/api';
import { Badge } from './ui/Badge';
import { MarkdownView } from './MarkdownView';
import { Surface } from './ui/Surface';
import { EmptyState, ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';

type RawPreviewKind = 'markdown' | 'html' | 'download';

type RawPreview = {
  file: RawFile;
  kind: RawPreviewKind;
  content: string;
};

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

function formatUpdated(value: string) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return new Intl.DateTimeFormat(undefined, {
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
  const { currentProject } = useWorkspace();
  const searchParams = useSearchParams();
  const [files, setFiles] = useState<RawFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [preview, setPreview] = useState<RawPreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const openedQueryFileRef = useRef('');

  useEffect(() => {
    let cancelled = false;
    openedQueryFileRef.current = '';

    getRawFiles()
      .then((data) => {
        if (cancelled) return;
        setFiles(data);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [currentProject]);

  const sortedFiles = useMemo(
    () => [...files].sort((a, b) => a.name.localeCompare(b.name)),
    [files],
  );

  const highlightedFile = searchParams.get('file') ?? '';

  const openRawPreview = useCallback(async (file: RawFile) => {
    const kind = rawPreviewKind(file.name);
    setPreview({ file, kind, content: '' });
    setPreviewError('');

    if (kind === 'download') {
      setPreviewLoading(false);
      return;
    }

    setPreviewLoading(true);
    try {
      const content = await getRawFilePreview(file.name);
      setPreview((current) => (
        current?.file.name === file.name ? { ...current, content } : current
      ));
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : 'Failed to load raw preview');
    } finally {
      setPreviewLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!highlightedFile || loading || error || openedQueryFileRef.current === highlightedFile) {
      return;
    }

    const requestedFile = files.find((file) => file.name === highlightedFile);
    if (!requestedFile) return;

    openedQueryFileRef.current = highlightedFile;
    const timer = window.setTimeout(() => {
      void openRawPreview(requestedFile);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [error, files, highlightedFile, loading, openRawPreview]);

  useEffect(() => {
    if (!preview) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setPreview(null);
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [preview]);

  async function downloadRawFile() {
    if (!preview) return;
    setPreviewError('');

    try {
      const content = preview.content || await getRawFilePreview(preview.file.name);
      const url = window.URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = preview.file.name;
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.URL.revokeObjectURL(url);
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : 'Failed to download raw file');
    }
  }

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">
          Raw files
        </h1>
        <p className="mt-2 max-w-2xl text-zinc-400">
          Project source files waiting for or already processed by the pipeline.
        </p>
      </header>

      {loading ? <LoadingState label="Loading raw files" /> : null}
      {error ? <ErrorState message={error} /> : null}
      {!loading && !error && sortedFiles.length === 0 ? (
        <EmptyState message="No raw files yet. Upload content before running the pipeline." />
      ) : null}

      {!loading && !error && sortedFiles.length > 0 ? (
        <Surface variant="glass" className="overflow-hidden">
          <div className="overflow-x-auto">
            <table className="min-w-full text-left text-sm">
              <thead className="border-b border-white/10 bg-white/[0.03] text-xs uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-4 py-3 font-semibold">Name</th>
                  <th className="px-4 py-3 font-semibold">Size</th>
                  <th className="px-4 py-3 font-semibold">Updated</th>
                  <th className="px-4 py-3 font-semibold">SHA256</th>
                  <th className="px-4 py-3 font-semibold">Ingested</th>
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
                          aria-label={kind === 'download' ? `Download ${file.name}` : `Preview ${file.name}`}
                        >
                          {file.name}
                        </button>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 tabular-nums text-zinc-300">
                        {formatBytes(file.size)}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-zinc-400">
                        {formatUpdated(file.updated)}
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
                          {file.ingested ? 'Ingested' : 'Pending'}
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

      {preview ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
          onClick={() => setPreview(null)}
        >
          <Surface
            variant="elevated"
            className="flex max-h-[86vh] w-full max-w-4xl flex-col overflow-hidden"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="flex items-start justify-between gap-4 border-b border-white/10 p-4">
              <div className="min-w-0">
                <h2 className="truncate text-lg font-semibold text-white">{preview.file.name}</h2>
                <p className="mt-1 text-sm text-zinc-400">
                  {preview.kind === 'download' ? 'Preview unavailable for this format.' : 'Raw file preview'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => setPreview(null)}
                className="rounded-md p-2 text-zinc-400 transition hover:bg-white/10 hover:text-white"
                aria-label="Close"
                title="Close"
              >
                <X size={18} aria-hidden="true" />
              </button>
            </div>

            <div className="min-h-0 flex-1 overflow-auto p-4">
              {previewLoading ? <LoadingState label="Loading preview" /> : null}
              {previewError ? <ErrorState message={previewError} /> : null}
              {!previewLoading && !previewError && preview.kind === 'markdown' ? (
                <div className="rounded-md border border-white/10 bg-black/20 p-4">
                  <MarkdownView content={preview.content} />
                </div>
              ) : null}
              {!previewLoading && !previewError && preview.kind === 'html' ? (
                <iframe
                  title={`Preview ${preview.file.name}`}
                  srcDoc={sanitizeRawHtml(preview.content)}
                  sandbox=""
                  className="h-[60vh] w-full rounded-md border border-white/10 bg-white"
                />
              ) : null}
              {!previewLoading && !previewError && preview.kind === 'download' ? (
                <div className="rounded-md border border-white/10 bg-black/20 p-6 text-sm text-zinc-300">
                  This file format is not previewable inline.
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
                Download
              </button>
            </div>
          </Surface>
        </div>
      ) : null}
    </div>
  );
}
