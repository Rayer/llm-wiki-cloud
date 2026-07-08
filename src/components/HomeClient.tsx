'use client';

import Link from 'next/link';
import { FormEvent, ReactNode, useCallback, useEffect, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import {
  getConcept,
  getConcepts,
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
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';

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

type ModalEntry = { title: string; content: string; type: string; slug: string; id?: string };
type SearchMode = 'wiki' | 'full';

function conceptHref(concept: WikiEntry): string {
  const target = concept.id
    ? `${concept.id}-${encodeURIComponent(concept.slug)}`
    : encodeURIComponent(concept.slug);
  return `/concepts/${target}`;
}

export function HomeClient() {
  const { t } = useT();
  const { currentProject } = useWorkspace();
  const [query, setQuery] = useState('');
  const [mode, setMode] = useState<SearchMode>('wiki');
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [statusError, setStatusError] = useState('');
  const [results, setResults] = useState<SearchResult[]>([]);
  const [aiAnswer, setAiAnswer] = useState('');
  const [citations, setCitations] = useState<Citation[]>([]);
  const [expandKeywords, setExpandKeywords] = useState<string[]>([]);
  const [searched, setSearched] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [modal, setModal] = useState<ModalEntry | null>(null);
  const [modalLoading, setModalLoading] = useState(false);
  const [latestConcepts, setLatestConcepts] = useState<WikiEntry[]>([]);

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
          setExpandKeywords(response.expand?.keywords ?? []);
        })
        .catch((err: Error) => {
          setError(err instanceof Error ? err.message : 'Search failed');
          setResults([]);
          setAiAnswer('');
          setCitations([]);
          setExpandKeywords([]);
        })
        .finally(() => setLoading(false));
    }
  }, []); // run once on mount

  useEffect(() => {
    getStatus()
      .then(setStatus)
      .catch((err: Error) => setStatusError(err.message));
  }, [currentProject]);

  useEffect(() => {
    let ignore = false;
    getConcepts()
      .then((data) => {
        if (!ignore) setLatestConcepts(data.slice(0, 4));
      })
      .catch(() => {
        if (!ignore) setLatestConcepts([]);
      });

    return () => {
      ignore = true;
    };
  }, [currentProject]);

  const handleSearch = useCallback(async (searchMode: SearchMode) => {
    const trimmed = query.trim();
    if (!trimmed) return;

    syncUrl(trimmed, searchMode);

    setLoading(true);
    setError('');
    setAiAnswer('');
    setCitations([]);
    setExpandKeywords([]);
    setSearched(true);

    try {
      const response = await searchWiki(trimmed, searchMode);
      setResults(response.results);
      setAiAnswer(response.aiAnswer);
      setCitations(response.citations);
      setExpandKeywords(response.expand?.keywords ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
      setResults([]);
      setAiAnswer('');
      setCitations([]);
      setExpandKeywords([]);
    } finally {
      setLoading(false);
    }
  }, [query]);

  const onSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    await handleSearch(mode);
  }, [handleSearch, mode]);

  const openCitation = useCallback(async (citation: Citation) => {
    setModalLoading(true);
    setModal({ title: citation.text, content: '', type: citation.type, slug: citation.slug, id: citation.id });
    try {
      const fetch = citation.type === 'concept' ? getConcept : getSource;
      const entry: WikiEntry = await fetch(citation.slug);
      setModal({
        title: entry.title,
        content: entry.content ?? entry.raw as string ?? '',
        type: citation.type,
        slug: citation.slug,
        id: entry.id,
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

  const resultType = (type?: string): 'source' | 'concept' =>
    type === 'source' ? 'source' : 'concept';

  return (
    <div className="space-y-10">
      <section className="flex flex-col items-center pt-8 text-center">
        <p className="text-sm text-zinc-500">{t('Demo.heroSubtitle')}</p>
        <h1 className="mt-2 max-w-2xl text-2xl font-semibold tracking-tight text-white sm:text-3xl">
          {t('Demo.heading')}
        </h1>

        <form onSubmit={onSubmit} className="mt-8 w-full max-w-2xl">
          <Surface variant="glass" className="p-2">
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t('Demo.searchPlaceholder')}
                className="min-h-12 flex-1 rounded-[var(--radius-md)] bg-transparent px-4 text-white outline-none transition placeholder:text-zinc-600 focus:ring-1 focus:ring-emerald-400/30"
              />
              <div className="flex items-center gap-2 px-1 sm:pr-1">
                <div className="grid grid-cols-2 rounded-[var(--radius-md)] border border-white/10 bg-black/30 p-0.5 text-sm">
                  {(['wiki', 'full'] as const).map((item) => (
                    <button
                      key={item}
                      type="button"
                      onClick={() => { setMode(item); if (query.trim()) handleSearch(item); }}
                      className={`min-h-11 rounded-md px-3 py-2 font-medium capitalize transition ${
                        mode === item ? 'bg-emerald-400/20 text-emerald-200' : 'text-zinc-400 hover:text-white'
                      }`}
                    >
                      {t(`Demo.${item}`)}
                    </button>
                  ))}
                </div>
                <button
                  type="submit"
                  className="min-h-12 rounded-[var(--radius-md)] bg-emerald-400 px-5 text-sm font-semibold text-zinc-900 transition hover:bg-emerald-300"
                >
                  {t('Demo.search')}
                </button>
              </div>
            </div>
          </Surface>
          <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
            <StatPill label="Sources" value={status?.sourcesCount} error={statusError} />
            <StatPill label="Concepts" value={status?.conceptsCount} error={statusError} />
            <Badge variant="muted" className="hidden sm:inline-flex">⌘K</Badge>
          </div>
        </form>
      </section>

      {!searched && latestConcepts.length > 0 ? (
        <section className="space-y-4" aria-labelledby="latest-concepts-heading">
          <div className="flex items-center justify-between gap-4">
            <h2 id="latest-concepts-heading" className="text-lg font-semibold text-white">
              {t('Demo.latestConcepts')}
            </h2>
            <Badge variant="muted">{latestConcepts.length}</Badge>
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            {latestConcepts.map((concept, index) => (
              <Link key={concept.slug} href={conceptHref(concept)} className="group block">
                <Surface
                  variant="glass"
                  className="animate-fade-in border-l-[3px] border-l-emerald-400 p-5 transition duration-200 [animation-fill-mode:backwards] hover:-translate-y-0.5 hover:border-emerald-400/30 hover:shadow-lg hover:shadow-emerald-500/5"
                  style={{ animationDelay: `${index * 50}ms` }}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="concept">Concept</Badge>
                    <h3 className="text-base font-semibold text-white group-hover:text-emerald-50">
                      {concept.title}
                    </h3>
                  </div>
                  <p className="mt-3 line-clamp-3 text-sm leading-6 text-zinc-400">
                    {concept.description ?? 'Open this concept to start exploring the knowledge base.'}
                  </p>
                </Surface>
              </Link>
            ))}
          </div>
        </section>
      ) : null}

      <section className="space-y-4">
        {searched ? (
          <div className="flex items-center justify-between gap-4">
            <h2 className="text-lg font-semibold text-white">{t('Demo.results')}</h2>
            <Badge variant="muted">{mode} mode</Badge>
          </div>
        ) : null}
        {loading ? <LoadingState label="Searching" /> : null}
        {error ? <ErrorState message={error} /> : null}
        {!loading && !error && aiAnswer ? (
          <article className="relative overflow-hidden rounded-[var(--radius-lg)] border border-emerald-400/20 bg-emerald-400/[0.06] p-5 backdrop-blur-sm">
            <div className="absolute inset-y-0 left-0 w-1 bg-gradient-to-b from-emerald-300 to-teal-500" />
            <div className="pl-3">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-semibold text-emerald-200">{t('Demo.answer')}</h3>
                {citations.length > 0 ? (
                  <Badge variant="accent">{citations.length} sources</Badge>
                ) : null}
              </div>
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
            </div>
          </article>
        ) : null}
        {!loading && !error && searched && results.length === 0 ? (
          <EmptyState message="No results matched that query." />
        ) : null}
        <div className="grid gap-4 md:grid-cols-2">
          {results.map((result, index) => {
            const type = resultType(result.type);
            const typeBorderClass = type === 'source'
              ? 'border-l-blue-400'
              : type === 'concept'
                ? 'border-l-emerald-400'
                : '';
            return (
              <button
                key={`${result.type}-${result.slug}`}
                type="button"
                onClick={() => openCitation({
                  text: result.title,
                  slug: result.slug,
                  type: type,
                  path: '',
                })}
                className={`animate-fade-in rounded-[var(--radius-lg)] border border-l-[3px] border-white/10 bg-zinc-900/40 p-5 text-left backdrop-blur-sm transition duration-200 [animation-fill-mode:backwards] hover:-translate-y-0.5 hover:border-emerald-400/30 hover:shadow-lg hover:shadow-emerald-500/5 ${typeBorderClass}`}
                style={{ animationDelay: `${index * 50}ms` }}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={type}>{type === 'source' ? 'Source' : 'Concept'}</Badge>
                    <h3 className="text-lg font-semibold text-white">{result.title}</h3>
                  </div>
                  {result.score !== undefined ? (
                    <Badge variant="muted">{result.score.toFixed(2)}</Badge>
                  ) : null}
                </div>
                <p className="mt-3 line-clamp-4 text-sm leading-6 text-zinc-400">
                  {result.excerpt ?? result.description ?? 'Open this wiki entry.'}
                </p>
              </button>
            );
          })}
        </div>
        {!loading && !error && searched && expandKeywords.length > 0 ? (
          <div className="mt-1 text-xs text-zinc-500">
            搜尋關鍵字：{expandKeywords.join('、')}
          </div>
        ) : null}
      </section>

      {/* Citation Preview Modal */}
      {modal ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm animate-fade-in"
          onClick={() => setModal(null)}
        >
          <Surface
            variant="elevated"
            className="relative max-h-[85vh] w-full max-w-2xl overflow-y-auto p-6 animate-scale-in"
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
                <Badge variant={modal.type === 'concept' ? 'concept' : 'source'}>
                  {modal.type === 'concept' ? 'Concept' : 'Source'}
                </Badge>
                <h2 className="text-2xl font-semibold text-white">{modal.title}</h2>
                <div className="mt-4 border-t border-white/10 pt-4">
                  <MarkdownBody content={stripLeadingHeading(modal.content)} />
                </div>
                <div className="mt-6 border-t border-white/10 pt-4">
                  <Link
                    href={`/${modal.type === 'concept' ? 'concepts' : 'sources'}/${modal.id || modal.slug}`}
                    className="text-sm font-medium text-emerald-300 hover:text-emerald-200"
                    onClick={() => setModal(null)}
                  >
                    Open full page →
                  </Link>
                </div>
              </>
            )}
          </Surface>
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

function StatPill({
  label,
  value,
  error,
}: {
  label: string;
  value?: number;
  error?: string;
}) {
  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-white/10 bg-zinc-900/50 px-3 py-1 text-xs text-zinc-400">
      <span>{label}</span>
      <span className="font-semibold tabular-nums text-white">
        {error ? '—' : value ?? '…'}
      </span>
    </span>
  );
}
