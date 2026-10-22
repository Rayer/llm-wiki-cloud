'use client';

import { useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { ScrapeResult } from '@/lib/api';
import { Surface } from './ui/Surface';

type Labels = {
  heading: string;
  title: string;
  save: string;
  decline: string;
  close: string;
  checkStatus: string;
  manualCapture: string;
  processingSeparate: string;
  rawArticle: string;
  created: string;
  updated: string;
  unchanged: string;
  digest: string;
  loading: string;
  qualityUnavailable: string;
  qualityUnavailableSaved: string;
};

export function UrlPreviewDialog({
  result, loading, error, labels, onAccept, onDecline, onResolve, onClose,
}: {
  result: ScrapeResult | null;
  loading: boolean;
  error: string | null;
  labels: Labels;
  onAccept: (title: string) => void;
  onDecline: () => void;
  onResolve: () => void;
  onClose: () => void;
}) {
  const [editedTitle, setEditedTitle] = useState<string | null>(null);
  const title = editedTitle ?? result?.preview?.title ?? '';
  if (!loading && !result && !error) return null;

  const candidateReady = result?.status === 'preview';
  const savedStorage = result?.status === 'stored' ? result.storage : undefined;
  const saved = Boolean(savedStorage);
  const qualityUnchecked = result?.preview?.quality_status === 'not_checked';
  const needsStatus = result?.status === 'uncertain' && result.candidate_id;
  const rawHref = savedStorage ? `/raw?file=${encodeURIComponent(savedStorage.path)}` : '';
  const savedOutcome = savedStorage?.status === 'created' ? labels.created
    : savedStorage?.status === 'replaced' ? labels.updated
      : savedStorage?.status === 'already_exists' ? labels.unchanged : '';
  const knownMetadata = result?.preview?.metadata;

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/75 p-3 sm:p-5" onClick={onClose}>
      <Surface role="dialog" aria-modal="true" aria-labelledby="url-preview-title" variant="elevated" className="flex max-h-[calc(100dvh-1.5rem)] w-full max-w-5xl flex-col overflow-hidden" onClick={(event) => event.stopPropagation()}>
        <header className="flex items-center justify-between gap-4 border-b border-white/10 p-4">
          <h2 id="url-preview-title" className="text-lg font-semibold text-white">{labels.heading}</h2>
          <button type="button" onClick={onClose} disabled={loading} className="rounded-md border border-white/10 px-3 py-2 text-sm text-zinc-300 disabled:opacity-50">{labels.close}</button>
        </header>
        <div className="min-h-0 flex-1 overflow-auto p-4">
          {loading ? <p role="status" className="text-sm text-zinc-300">{labels.loading}</p> : null}
          {error ? <p role="alert" className="text-sm text-amber-200">{error}</p> : null}
          {result ? (
            <div aria-live="polite" className="space-y-4">
              {qualityUnchecked && !saved ? (
                <p role="status" className="text-sm text-amber-200">{labels.qualityUnavailable}</p>
              ) : (
                <p className={`text-sm ${result.status === 'stored' ? 'text-emerald-200' : result.status === 'uncertain' ? 'text-amber-200' : 'text-zinc-300'}`}>{result.message}</p>
              )}
              {qualityUnchecked && saved ? <p role="status" className="text-sm text-amber-200">{labels.qualityUnavailableSaved}</p> : null}
              {candidateReady && result.preview ? (
                <>
                  <label className="block text-sm text-zinc-300">
                    {labels.title}
                    <input value={title} maxLength={200} onChange={(event) => setEditedTitle(event.target.value)} className="mt-1 block w-full rounded-md border border-white/10 bg-black/40 px-3 py-2 text-white" />
                  </label>
                  <p className="break-all text-xs text-zinc-400">{result.preview.source_url}</p>
                  <p className="break-all font-mono text-[11px] text-zinc-500">{labels.digest}: {result.candidate_revision ?? result.preview.candidate_sha256}</p>
                  {knownMetadata ? (
                    <dl className="grid gap-1 text-xs text-zinc-400 sm:grid-cols-2">
                      {(['created', 'author', 'published', 'description', 'tags'] as const).flatMap((key) => {
                        const value = knownMetadata[key];
                        if (value === undefined || value === null || value === '') return [];
                        return <div key={key} className="flex gap-2"><dt className="font-medium text-zinc-300">{key}</dt><dd>{Array.isArray(value) ? value.join(', ') : String(value)}</dd></div>;
                      })}
                    </dl>
                  ) : null}
                  {result.preview.reason ? <p className="text-sm text-amber-200">{result.preview.reason}</p> : null}
                  {result.preview.warning ? <p className="text-sm text-amber-200">{result.preview.warning}</p> : null}
                  <article className="prose prose-invert max-w-none rounded-md border border-white/10 bg-black/30 p-5 prose-headings:text-white prose-p:text-zinc-200 prose-li:text-zinc-200 prose-a:text-emerald-300">
                    <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ({ children, href }) => <a href={href} target="_blank" rel="noreferrer">{children}</a> }}>
                      {result.preview.markdown}
                    </ReactMarkdown>
                  </article>
                </>
              ) : null}
              {saved ? (
                <div className="space-y-2">
                  <p className="text-sm text-emerald-200">{savedOutcome}</p>
                  <p className="text-sm text-zinc-300">{labels.processingSeparate}</p>
                  <a href={rawHref} className="text-sm font-medium text-emerald-300 underline">{labels.rawArticle}: {savedStorage?.path}</a>
                </div>
              ) : null}
              {result.failure_stage === 'fetch' && (result.status === 'unavailable' || result.status === 'rejected') ? <p className="text-sm text-zinc-400">{labels.manualCapture}</p> : null}
              <div className="flex flex-wrap justify-end gap-2 border-t border-white/10 pt-3">
                {candidateReady ? (
                  <>
                    <button type="button" disabled={loading} onClick={onDecline} className="rounded-md border border-white/15 px-4 py-2 text-sm text-zinc-200 disabled:opacity-50">{labels.decline}</button>
                    <button type="button" disabled={loading || !title.trim()} onClick={() => onAccept(title)} className="rounded-md bg-emerald-500 px-4 py-2 text-sm font-semibold text-zinc-950 disabled:opacity-50">{labels.save}</button>
                  </>
                ) : null}
                {needsStatus ? <button type="button" disabled={loading} onClick={onResolve} className="rounded-md bg-amber-400 px-4 py-2 text-sm font-semibold text-zinc-950 disabled:opacity-50">{labels.checkStatus}</button> : null}
              </div>
            </div>
          ) : null}
        </div>
      </Surface>
    </div>
  );
}
