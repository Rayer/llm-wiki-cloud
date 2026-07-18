'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Eye, X } from 'lucide-react';
import { getRawFilePreview, getSources, type SourceListItem } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { EmptyState, ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { NavigationLink } from './NavigationBlocker';
import { rawFileNameFromSource } from '@/lib/raw-file-name';

const lifecycleVariant: Record<SourceListItem['lifecycle'], 'muted' | 'published' | 'draft'> = {
  new: 'muted',
  synced: 'published',
  notes_pending: 'draft',
  content_pending: 'draft',
  error: 'draft',
};

export function SourceListClient() {
  const { t } = useT();
  const { currentProject } = useWorkspace();
  const [sources, setSources] = useState<SourceListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [preview, setPreview] = useState<{ path: string; content: string } | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const previewTriggerRef = useRef<HTMLButtonElement | null>(null);
  const closeButtonRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    void Promise.resolve()
      .then(() => {
        setLoading(true);
        setError('');
        return getSources();
      })
      .then((data) => {
        if (!cancelled) setSources(data);
      })
      .catch((cause: Error) => {
        if (!cancelled) setError(cause.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [currentProject]);

  const openRawPreview = useCallback(async (rawPath: string) => {
    setPreview({ path: rawPath, content: '' });
    setPreviewLoading(true);
    setPreviewError('');
    try {
      const content = await getRawFilePreview(rawFileNameFromSource(rawPath));
      setPreview((current) => current?.path === rawPath ? { ...current, content } : current);
    } catch (cause) {
      setPreviewError(cause instanceof Error ? cause.message : t('Raw.loadPreviewFailed'));
    } finally {
      setPreviewLoading(false);
    }
  }, [t]);

  const closePreview = useCallback(() => {
    setPreview(null);
    queueMicrotask(() => previewTriggerRef.current?.focus());
  }, []);

  useEffect(() => {
    if (!preview) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closePreview();
    };
    window.addEventListener('keydown', closeOnEscape);
    closeButtonRef.current?.focus();
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [closePreview, preview]);

  const filtered = useMemo(() => {
    const query = search.trim().toLowerCase();
    return query
      ? sources.filter((source) => [source.title, source.rawPath].some((value) => value.toLowerCase().includes(query)))
      : sources;
  }, [search, sources]);

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">{t('List.sourcesTitle')}</h1>
        <p className="mt-2 max-w-2xl text-zinc-400">{t('List.sourcesDescription')}</p>
      </header>

      <input
        type="search"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
        placeholder={t('List.searchPlaceholder', { title: t('List.sourcesTitle').toLowerCase() })}
        className="min-h-11 w-full rounded-[var(--radius-lg)] border border-white/10 bg-zinc-900/50 px-4 py-2.5 text-sm text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-400/50 focus:ring-1 focus:ring-emerald-400/20"
      />

      {loading ? <LoadingState label={t('Source.loading')} /> : null}
      {error ? <ErrorState message={error} /> : null}
      {!loading && !error && filtered.length === 0 ? <EmptyState message={t('List.noSources')} /> : null}

      <div className="grid gap-4 md:grid-cols-2">
        {filtered.map((source) => {
          const detailHref = source.id ? `/sources/${source.id}-${encodeURIComponent(source.slug)}` : null;
          const lifecycle = t(`Source.lifecycle.${source.lifecycle}`);
          return (
            <Surface key={source.id ?? source.rawPath} variant="glass" className="h-full p-5 transition hover:border-emerald-400/30">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="source">{t('Source.singular')}</Badge>
                <Badge variant={lifecycleVariant[source.lifecycle]}>{lifecycle}</Badge>
                <Badge variant="muted">
                  {source.annotationPresent ? t('Source.annotationPresent') : t('Source.annotationEmpty')}
                </Badge>
              </div>
              {detailHref ? (
                <NavigationLink href={detailHref} className="mt-3 block break-words text-lg font-semibold text-white hover:text-emerald-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
                  {source.title}
                </NavigationLink>
              ) : <h2 className="mt-3 break-words text-lg font-semibold text-white">{source.title}</h2>}
              <p className="mt-2 break-all font-mono text-xs text-zinc-500">{source.rawPath}</p>
              {source.lifecycle === 'error' ? (
                <p role="alert" className="mt-3 text-sm text-red-200">{source.error ?? t('Source.lifecycleError')}</p>
              ) : null}
              <div className="mt-4 flex items-center gap-2">
                <button
                  type="button"
                  onClick={(event) => {
                    previewTriggerRef.current = event.currentTarget;
                    void openRawPreview(source.rawPath);
                  }}
                  className="inline-flex min-h-11 items-center gap-2 rounded-md border border-white/10 px-3 text-sm text-zinc-200 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
                  aria-label={t('Source.previewRawAria', { path: source.rawPath })}
                >
                  <Eye className="size-4" aria-hidden="true" />
                  {t('Source.previewRaw')}
                </button>
              </div>
            </Surface>
          );
        })}
      </div>

      {preview ? (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-3 sm:p-4" onClick={closePreview}>
          <Surface role="dialog" aria-modal="true" aria-labelledby="source-raw-preview-title" variant="elevated" className="flex max-h-[calc(100dvh-1.5rem)] w-full max-w-3xl flex-col sm:max-h-[calc(100dvh-2rem)]" onClick={(event) => event.stopPropagation()}>
            <div className="flex items-center justify-between gap-4 border-b border-white/10 p-4">
              <h2 id="source-raw-preview-title" className="min-w-0 truncate text-lg font-semibold text-white">{preview.path}</h2>
              <button ref={closeButtonRef} type="button" onClick={closePreview} aria-label={t('Raw.close')} className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-md text-zinc-300 hover:bg-white/10">
                <X className="size-5" aria-hidden="true" />
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto p-4">
              {previewLoading ? <LoadingState label={t('Raw.loadingPreview')} /> : null}
              {previewError ? <ErrorState message={previewError} /> : null}
              {!previewLoading && !previewError ? <pre className="whitespace-pre-wrap break-words text-sm text-zinc-200">{preview.content}</pre> : null}
            </div>
          </Surface>
        </div>
      ) : null}
    </div>
  );
}
