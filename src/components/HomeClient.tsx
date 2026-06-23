'use client';

import Link from 'next/link';
import { FormEvent, ReactNode, useCallback, useEffect, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import {
  getConcept,
  getSource,
  getStatus,
  searchWiki,
  type ApiStatus,
  type Citation,
  type SearchResult,
  type WikiEntry,
} from '@/lib/api';
import { useT } from '@/lib/i18n';
import { EmptyState, ErrorState, LoadingState } from './States';

function readSearchParams(): { q: string; mode: 'wiki' | 'full' } {
  if (typeof window === 'undefined') return { q: '', mode: 'wiki' };
  const params = new URLSearchParams(window.location.search);
  const modeParam = params.get('mode');
  return {
    q: params.get('q') ?? '',
    mode: modeParam === 'full' ? 'full' : 'wiki',
  };
}

function syncUrl(q: string, mode: 'wiki' | 'full') {
  if (typeof window === 'undefined') return;
  const params = new URLSearchParams();
  if (q) params.set('q', q);
  if (mode !== 'wiki') params.set('mode', mode);
  const search = params.toString();
  const url = window.location.pathname + (search ? `?${search}` : '');
  window.history.replaceState(null, '', url);
}

type ModalEntry = { title: string; content: string; type: string; slug: string };

export function HomeClient() {
  const { t } = useT();
  const [query, setQuery] = useState('');
  const [mode, setMode] = useState<'wiki' | 'full'>('wiki');
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [statusError, setStatusError] = useState('');
  const [results, setResults] = useState<SearchResult[]>([]);
  const [aiAnswer, setAiAnswer] = useState('');
  const [citations, setCitations] = useState<Citation[]>([]);
  const [searched, setSearched] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [modal, setModal] = useState<ModalEntry | null>(null);
  const [modalLoading, setModalLoading] = useState(false);

  // Restore search from URL on mount (back-button support).
  // React 19 batches all state updates in effects — multiple setStates here
  // is safe and intentional (one render with all new values).
  useEffect(() => {
    const { q, mode: urlMode } = readSearchParams();
    if (q) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- batched in React 19
      setQuery(q);
      setMode(urlMode);
      setLoading(true);
      setSearched(true);
      searchWiki(q, urlMode)
        .then((response) => {
          setResults(response.results);
          setAiAnswer(response.aiAnswer);
          setCitations(response.citations);
        })
        .catch((err: Error) => {
          setError(err instanceof Error ? err.message : 'Search failed');
          setResults([]);
          setAiAnswer('');
          setCitations([]);
        })
        .finally(() => setLoading(false));
    }
  }, []); // run once on mount

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch((err: Error) => setStatusError(err.message));
  }, []);

  const onSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = query.trim();
    if (!trimmed) return;

    syncUrl(trimmed, mode);

    setLoading(true);
    setError('');
    setAiAnswer('');
    setCitations([]);
    setSearched(true);

    try {
      const response = await searchWiki(trimmed, mode);
      setResults(response.results);
      setAiAnswer(response.aiAnswer);
      setCitations(response.citations);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
      setResults([]);
      setAiAnswer('');
      setCitations([]);
    } finally {
      setLoading(false);
    }
  }, [query, mode]);

  const openCitation = useCallback(async (citation: Citation) => {
    setModalLoading(true);
    setModal({ title: citation.text, content: '', type: citation.type, slug: citation.slug });
    try {
      const fetch = citation.type === 'concept' ? getConcept : getSource;
      const entry: WikiEntry = await fetch(citation.slug);
      setModal({
        title: entry.title,
        content: entry.content ?? entry.raw as string ?? '',
        type: citation.type,
        slug: citation.slug,
      });
    } catch {
      setModal(null);
    } finally {
      setModalLoading(false);
    }
  }, []);

  // Close modal on Escape
  useEffect(() => {
    if (!modal) return;
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') setModal(null); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [modal]);

  return (
    <div className="space-y-10">
      <section className="grid gap-8 pt-6 lg:grid-cols-[1fr_320px] lg:items-end">
        <div>
          <div className="text-sm font-semibold uppercase tracking-[0.28em] text-emerald-300">
            LLM Wiki (Demo)
          </div>
          <h1 className="mt-5 max-w-3xl text-3xl font-bold leading-tight text-white sm:text-4xl">
            {t('Demo.heading')}
          </h1>
          <p className="mt-5 max-w-2xl text-lg leading-8 text-zinc-400">
            Browse source documents, distilled concepts, and pipeline state from the
            LLM Wiki backend.
          </p>
          <p className="mt-3 max-w-2xl text-sm leading-6 text-zinc-500">
            Inspired by{" "}
            <a
              href="https://rayer.idv.tw/blog/?p=1351"
              target="_blank"
              rel="noopener noreferrer"
              className="text-emerald-300 underline hover:text-emerald-200"
            >
              Andrej Karpathy&apos;s LLM Wiki concept
            </a>
            , remixed with LLM superpowers.
          </p>
          <form onSubmit={onSubmit} className="mt-8 rounded-lg border border-white/10 bg-[#151515] p-3">
            <div className="flex flex-col gap-3 sm:flex-row">
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t('Demo.searchPlaceholder')}
                className="min-h-12 flex-1 rounded-md border border-white/10 bg-black/40 px-4 text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-300"
              />
              <div className="grid grid-cols-2 rounded-md border border-white/10 bg-black/30 p-1 text-sm">
                {(['wiki', 'full'] as const).map((item) => (
                  <button
                    key={item}
                    type="button"
                    onClick={() => setMode(item)}
                    className={`rounded px-4 py-2 font-medium capitalize transition ${
                      mode === item ? 'bg-emerald-300 text-black' : 'text-zinc-300 hover:text-white'
                    }`}
                  >
                    {t(`Demo.${item}`)}
                  </button>
                ))}
              </div>
              <button
                type="submit"
                className="min-h-12 rounded-md bg-white px-6 font-semibold text-black transition hover:bg-emerald-200"
              >
                {t('Demo.search')}
              </button>
            </div>
          </form>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-1">
          <StatCard label="Sources" value={status?.sourcesCount} error={statusError} />
          <StatCard label="Concepts" value={status?.conceptsCount} error={statusError} />
        </div>
      </section>

      <section className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <h2 className="text-2xl font-semibold text-white">Search results</h2>
          {searched ? <span className="text-sm text-zinc-500">{mode} mode</span> : null}
        </div>
        {loading ? <LoadingState label="Searching" /> : null}
        {error ? <ErrorState message={error} /> : null}
        {!loading && !error && aiAnswer ? (
          <article className="rounded-lg border border-emerald-300/20 bg-emerald-300/[0.06] p-5">
            <h3 className="text-sm font-semibold uppercase tracking-[0.2em] text-emerald-300">
              AI answer
            </h3>
            <div className="mt-3 text-base leading-7 text-zinc-200
              [&_strong]:text-white [&_strong]:font-semibold
              [&_h3]:text-base [&_h3]:font-semibold [&_h3]:text-white [&_h3]:mt-4 [&_h3]:mb-1
              [&_ul]:list-disc [&_ul]:pl-5 [&_ul]:space-y-1 [&_ul]:mb-3
              [&_ol]:list-decimal [&_ol]:pl-5 [&_ol]:space-y-1 [&_ol]:mb-3
              [&_li]:leading-7
              [&_p]:mb-3
            ">
              {renderCitations(aiAnswer, citations, openCitation)}
            </div>
          </article>
        ) : null}
        {!loading && !error && searched && results.length === 0 ? (
          <EmptyState message="No results matched that query." />
        ) : null}
        <div className="grid gap-4 md:grid-cols-2">
          {results.map((result) => (
            <button
              key={`${result.type}-${result.slug}`}
              type="button"
              onClick={() => openCitation({ text: result.title, slug: result.slug, type: (result.type === 'source' ? 'source' as const : 'concept' as const), path: '' })}
              className="rounded-lg border border-white/10 bg-[#1a1a1a] p-5 text-left transition hover:border-emerald-300/50 hover:bg-[#202020]"
            >
                <div className="flex items-center justify-between gap-4">
                  <h3 className="text-lg font-semibold text-white">{result.title}</h3>
                  {result.score !== undefined ? (
                    <span className="text-xs text-zinc-500">{result.score.toFixed(2)}</span>
                  ) : null}
                </div>
                <p className="mt-3 line-clamp-4 text-sm leading-6 text-zinc-400">
                  {result.excerpt ?? result.description ?? 'Open this wiki entry.'}
                </p>
              </button>
          ))}
        </div>
      </section>

      {/* Citation Preview Modal */}
      {modal ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
          onClick={() => setModal(null)}
        >
          <div
            className="relative max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-xl border border-white/10 bg-[#151515] p-6 shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              onClick={() => setModal(null)}
              className="absolute right-4 top-4 rounded-md p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white"
              aria-label="Close"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
              </svg>
            </button>

            {modalLoading ? (
              <LoadingState label="Loading..." />
            ) : (
              <>
                <div className="mb-1 text-xs font-semibold uppercase tracking-[0.2em] text-zinc-500">
                  {modal.type === 'concept' ? 'Concept' : 'Source'}
                </div>
                <h2 className="text-2xl font-semibold text-white">{modal.title}</h2>
                <div className="mt-4 border-t border-white/10 pt-4">
                  <MarkdownBody content={stripLeadingHeading(modal.content)} />
                </div>
                <div className="mt-6 border-t border-white/10 pt-4">
                  <Link
                    href={`/${modal.type === 'concept' ? 'concepts' : 'sources'}/${modal.slug}`}
                    className="text-sm font-medium text-emerald-300 hover:text-emerald-200"
                    onClick={() => setModal(null)}
                  >
                    Open full page →
                  </Link>
                </div>
              </>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

// Strip leading "# Title" or "Title\n===" from markdown to avoid
// double title when the page already shows it as <h1>/<h2>.
function stripLeadingHeading(md: string): string {
  const h1 = /^# .+\n\n?/;
  const h1u = /^.+\n=+\n\n?/;
  return md.replace(h1, '').replace(h1u, '').trimStart();
}

// Convert [[wikilinks]] with section context:
// Under "## Sources" → /sources/ | Under "## Concepts" → /concepts/
function resolveWikilinks(md: string): string {
  const lines = md.split('\n');
  let section = 'concepts'; // default
  const out: string[] = [];
  for (const line of lines) {
    const secMatch = /^## (Sources|Concepts)/i.exec(line);
    if (secMatch) {
      section = secMatch[1].toLowerCase();
    }
    out.push(line.replace(/\[\[([^\]]+)\]\]/g, (_, name: string) => {
      const parts = name.split('|');
      const slug = parts[0].trim();
      const display = (parts[1] || parts[0]).trim();
      return `[${display}](/${section}/${encodeURIComponent(slug)})`;
    }));
  }
  return out.join('\n');
}

function MarkdownBody({ content }: { content: string }) {
  if (!content) return <p className="text-zinc-400 italic">No content available.</p>;
  // Convert [[wikilinks]] with context-aware routing:
  // Under ## Sources → /sources/ | Under ## Concepts → /concepts/ | else → /concepts/
  const withLinks = resolveWikilinks(content);
  return (
    <div className="prose prose-invert prose-sm max-w-none text-zinc-300 leading-7
      [&_h1]:text-xl [&_h1]:font-semibold [&_h1]:text-white [&_h1]:mt-4 [&_h1]:mb-2
      [&_h2]:text-lg [&_h2]:font-semibold [&_h2]:text-white [&_h2]:mt-4 [&_h2]:mb-2
      [&_h3]:text-base [&_h3]:font-semibold [&_h3]:text-white [&_h3]:mt-3 [&_h3]:mb-1
      [&_p]:mb-3 [&_p]:leading-7
      [&_strong]:text-white [&_strong]:font-semibold
      [&_em]:italic [&_em]:text-zinc-200
      [&_ul]:list-disc [&_ul]:pl-5 [&_ul]:space-y-1 [&_ul]:mb-3
      [&_ol]:list-decimal [&_ol]:pl-5 [&_ol]:space-y-1 [&_ol]:mb-3
      [&_li]:leading-7
      [&_code]:bg-white/10 [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-sm
      [&_blockquote]:border-l-2 [&_blockquote]:border-emerald-300/50 [&_blockquote]:pl-4 [&_blockquote]:italic [&_blockquote]:text-zinc-400
      [&_a]:text-emerald-300 [&_a]:underline [&_a]:hover:text-emerald-200
      [&_hr]:border-white/10 [&_hr]:my-4
    ">
      <ReactMarkdown>{withLinks}</ReactMarkdown>
    </div>
  );
}

function renderCitations(
  text: string,
  citations: Citation[],
  onCitationClick: (citation: Citation) => void,
): ReactNode[] {
  const citationMap = new Map(citations.map((citation) => [citation.text, citation]));
  const parts = text.split(/(\[[^\]]+\])/g);

  return parts.map((part, index) => {
    const match = /^\[([^\]]+)\]$/.exec(part);
    if (!match) {
      // Render non-citation text with inline markdown
      return <span key={index}>{renderInlineMarkdown(part)}</span>;
    }

    const citation = citationMap.get(match[1]);
    if (!citation) return <span key={index}>{renderInlineMarkdown(part)}</span>;

    return (
      <button
        key={`${citation.type}-${citation.slug}-${index}`}
        type="button"
        onClick={() => onCitationClick(citation)}
        className="font-medium text-emerald-300 underline decoration-emerald-300/60 underline-offset-4 hover:text-emerald-200 cursor-pointer"
      >
        {match[1]}
      </button>
    );
  });
}

// Lightweight inline markdown: **bold**, *italic*, `code`
function renderInlineMarkdown(text: string): ReactNode[] {
  const tokens = text.split(/(\*\*[^*]+\*\*|\*[^*]+\*|`[^`]+`)/g);
  return tokens.map((token, i) => {
    if (token.startsWith('**') && token.endsWith('**')) {
      return <strong key={i} className="text-white font-semibold">{token.slice(2, -2)}</strong>;
    }
    if (token.startsWith('*') && token.endsWith('*') && !token.startsWith('**')) {
      return <em key={i} className="italic text-zinc-200">{token.slice(1, -1)}</em>;
    }
    if (token.startsWith('`') && token.endsWith('`')) {
      return <code key={i} className="bg-white/10 px-1 py-0.5 rounded text-sm">{token.slice(1, -1)}</code>;
    }
    // Convert double newlines to paragraph breaks
    if (token.includes('\n\n')) {
      return token.split('\n\n').map((para, j) => (
        <span key={`${i}-${j}`}>
          {j > 0 && <span className="block h-3" />}
          {para}
        </span>
      ));
    }
    return token;
  });
}

function StatCard({
  label,
  value,
  error,
}: {
  label: string;
  value?: number;
  error?: string;
}) {
  return (
    <div className="rounded-lg border border-white/10 bg-[#1a1a1a] p-5">
      <div className="text-sm text-zinc-500">{label}</div>
      <div className="mt-2 text-4xl font-semibold text-white">
        {error ? '-' : value ?? '...'}
      </div>
      {error ? <div className="mt-2 text-xs text-red-300">{error}</div> : null}
    </div>
  );
}
