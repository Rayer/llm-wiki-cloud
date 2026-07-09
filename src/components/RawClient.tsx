'use client';

import { useEffect, useMemo, useState } from 'react';
import { getRawFiles, type RawFile } from '@/lib/api';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { EmptyState, ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';

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

export function RawClient() {
  const { currentProject } = useWorkspace();
  const [files, setFiles] = useState<RawFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;

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
                {sortedFiles.map((file) => (
                  <tr key={file.name} className="transition hover:bg-white/[0.03]">
                    <td className="max-w-xs px-4 py-3 font-medium text-white">
                      <span className="block truncate" title={file.name}>
                        {file.name}
                      </span>
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
                ))}
              </tbody>
            </table>
          </div>
        </Surface>
      ) : null}
    </div>
  );
}
