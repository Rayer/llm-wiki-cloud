'use client';

import { FormEvent, useCallback, useRef, useState } from 'react';
import {
  scrapeUrl,
  triggerPipeline,
  uploadRawFile,
  type PipelineResult,
} from '@/lib/api';

type Toast = {
  id: number;
  message: string;
  type: 'success' | 'error' | 'info';
};

let toastId = 0;

export function PipelineClient() {
  const [fileLabel, setFileLabel] = useState('Choose .md file');
  const [scrapeUrlText, setScrapeUrlText] = useState('');
  const [pipelineResult, setPipelineResult] = useState<PipelineResult | null>(null);
  const [loading, setLoading] = useState<string | null>(null); // 'upload' | 'scrape' | 'pipeline'
  const [toasts, setToasts] = useState<Toast[]>([]);
  const fileRef = useRef<HTMLInputElement>(null);

  const addToast = useCallback((message: string, type: Toast['type']) => {
    const id = ++toastId;
    setToasts((prev) => [...prev, { id, message, type }]);
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 4000);
  }, []);

  const handleFileChange = useCallback(() => {
    const file = fileRef.current?.files?.[0];
    setFileLabel(file ? file.name : 'Choose .md file');
  }, []);

  const handleUpload = useCallback(async () => {
    const file = fileRef.current?.files?.[0];
    if (!file) {
      addToast('Please select a .md file first.', 'error');
      return;
    }
    setLoading('upload');
    try {
      const result = await uploadRawFile(file);
      addToast(`Uploaded: ${result.filename} (${result.bytes} bytes)`, 'success');
      setFileLabel('Choose .md file');
      if (fileRef.current) fileRef.current.value = '';
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Upload failed', 'error');
    } finally {
      setLoading(null);
    }
  }, [addToast]);

  const handleScrape = useCallback(async (event: FormEvent) => {
    event.preventDefault();
    const url = scrapeUrlText.trim();
    if (!url) {
      addToast('Please enter a URL.', 'error');
      return;
    }
    if (!url.startsWith('http://') && !url.startsWith('https://')) {
      addToast('URL must start with http:// or https://', 'error');
      return;
    }
    setLoading('scrape');
    try {
      const result = await scrapeUrl(url);
      addToast(`Scraped: ${result.title} → ${result.filename}`, 'success');
      setScrapeUrlText('');
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Scrape failed', 'error');
    } finally {
      setLoading(null);
    }
  }, [scrapeUrlText, addToast]);

  const handleRunPipeline = useCallback(async () => {
    setLoading('pipeline');
    try {
      const result = await triggerPipeline();
      setPipelineResult(result);
      addToast(result.message, 'info');
    } catch (err) {
      addToast(err instanceof Error ? err.message : 'Pipeline trigger failed', 'error');
    } finally {
      setLoading(null);
    }
  }, [addToast]);

  return (
    <>
      <section className="rounded-lg border border-white/10 bg-[#1a1a1a] p-5">
        <h2 className="text-lg font-semibold text-white">Add Content</h2>
        <p className="mt-1 text-sm text-zinc-400">
          Upload markdown files or scrape URLs to feed the wiki pipeline.
        </p>

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          {/* File Upload */}
          <div className="rounded-md border border-white/10 bg-[#111] p-4">
            <h3 className="text-sm font-semibold text-zinc-300">📄 Upload File</h3>
            <p className="mt-1 text-xs text-zinc-500">Upload a .md file to the raw/ directory.</p>
            <div className="mt-3 flex gap-2">
              <input
                ref={fileRef}
                type="file"
                accept=".md"
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
              <button
                type="button"
                onClick={handleUpload}
                disabled={loading === 'upload'}
                className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
              >
                {loading === 'upload' ? '⏳' : 'Upload'}
              </button>
            </div>
          </div>

          {/* URL Scrape */}
          <div className="rounded-md border border-white/10 bg-[#111] p-4">
            <h3 className="text-sm font-semibold text-zinc-300">🔗 Scrape URL</h3>
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
                className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-emerald-500 disabled:opacity-50"
              >
                {loading === 'scrape' ? '⏳' : 'Scrape'}
              </button>
            </form>
          </div>
        </div>

        {/* Pipeline Trigger */}
        <div className="mt-4 rounded-md border border-emerald-300/20 bg-emerald-300/[0.04] p-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h3 className="text-sm font-semibold text-emerald-300">⚙️ Pipeline</h3>
              <p className="mt-1 text-xs text-zinc-400">
                Trigger the OLW pipeline to ingest, compile, and publish.
              </p>
            </div>
            <button
              type="button"
              onClick={handleRunPipeline}
              disabled={loading === 'pipeline'}
              className="rounded-md bg-white px-4 py-2 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:opacity-50"
            >
              {loading === 'pipeline' ? 'Running...' : 'Run Pipeline'}
            </button>
          </div>
          {pipelineResult ? (
            <p className="mt-2 text-xs text-zinc-500">{pipelineResult.message}</p>
          ) : null}
        </div>

        {/* Pipeline Info */}
        <div className="mt-3 rounded-md border border-white/5 bg-[#111] p-3">
          <p className="text-xs text-zinc-500">
            The pipeline runs: <strong className="text-zinc-300">ingest</strong> (analyze raw notes) →{' '}
            <strong className="text-zinc-300">compile</strong> (synthesize wiki articles) →{' '}
            <strong className="text-zinc-300">lint</strong> (check quality) →{' '}
            <strong className="text-zinc-300">publish</strong> (auto-approve).
          </p>
        </div>
      </section>

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
    </>
  );
}
