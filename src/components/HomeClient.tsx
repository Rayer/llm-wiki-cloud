'use client';

import { FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { Content, PhrasingContent, Root, Text } from 'mdast';
import {
  citationPathSegment,
  getConcept,
  getConcepts,
  getSource,
  getStatus,
  safeWikiRouteSegment,
  searchWiki,
  type ApiStatus,
  type Citation,
  type SearchResult,
  type WikiEntry,
} from '@/lib/api';
import { useT } from '@/lib/i18n';
import { getExactRawCitationRange } from '@/lib/markdown-citations';
import { resolveWikilinksInMarkdown } from '@/lib/markdown-inline';
import { EmptyState, ErrorState, LoadingState } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { NavigationLink } from './NavigationBlocker';

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

type ModalEntry = {
  title: string;
  content: string;
  type: 'concept' | 'source';
  slug: string;
  id?: string;
  path?: string;
  lookup: string;
  label: string;
  error: string;
};
type SearchMode = 'wiki' | 'full';

function sampleSuggestedQueries(suggestedQueries: string[]): string[] {
  const available = [...new Set(suggestedQueries.slice(1))];
  const sampleSize = Math.min(4, available.length);

  for (let index = 0; index < sampleSize; index += 1) {
    const offset = Math.floor(Math.random() * (available.length - index));
    const swapIndex = index + offset;
    [available[index], available[swapIndex]] = [available[swapIndex], available[index]];
  }

  return available.slice(0, sampleSize);
}

function conceptHref(concept: WikiEntry): string | null {
  const id = safeWikiRouteSegment(concept.id);
  const slug = safeWikiRouteSegment(concept.slug);
  if (id && slug) return `/concepts/${encodeURIComponent(id)}-${encodeURIComponent(slug)}`;
  const target = id ?? slug;
  return target ? `/concepts/${encodeURIComponent(target)}` : null;
}


function entryDetailHref(entry: ModalEntry): string | null {
  const collection = entry.type === 'concept' ? 'concepts' : 'sources';
  const id = safeWikiRouteSegment(entry.id);
  const slug = safeWikiRouteSegment(entry.slug);

  if (id && slug) {
    return `/${collection}/${encodeURIComponent(id)}-${encodeURIComponent(slug)}`;
  }

  if (id) return `/${collection}/${encodeURIComponent(id)}`;

  if (entry.path) {
    const pathSegment = citationPathSegment(entry.type, entry.path);
    if (pathSegment) {
      return `/${collection}/${encodeURIComponent(pathSegment)}`;
    }
  }

  return slug ? `/${collection}/${encodeURIComponent(slug)}` : null;
}

export function HomeClient() {
  const { t } = useT();
  const { currentProject } = useWorkspace();
  const [initialSearch] = useState(() => readSearchParams());
  const [query, setQuery] = useState(initialSearch.q);
  const [mode, setMode] = useState<SearchMode>(initialSearch.mode);
  const [submittedMode, setSubmittedMode] = useState<SearchMode>(initialSearch.mode);
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [statusError, setStatusError] = useState('');
  const [results, setResults] = useState<SearchResult[]>([]);
  const [aiAnswer, setAiAnswer] = useState('');
  const [citations, setCitations] = useState<Citation[]>([]);
  const [expandKeywords, setExpandKeywords] = useState<string[]>([]);
  const [searched, setSearched] = useState(Boolean(initialSearch.q));
  const [loading, setLoading] = useState(Boolean(initialSearch.q));
  const [error, setError] = useState('');
  const [modal, setModal] = useState<ModalEntry | null>(null);
  const [modalLoading, setModalLoading] = useState(false);
  const citationRequestId = useRef(0);
  const searchRequestId = useRef(0);
  const previousProjectIdRef = useRef<string | undefined>(currentProject?.id);
  const sampledProjectIdRef = useRef<string | undefined>(undefined);
  const hasSampledQueriesRef = useRef(false);
  const [suggestedQueryChips, setSuggestedQueryChips] = useState<string[]>([]);
  const [latestConcepts, setLatestConcepts] = useState<WikiEntry[]>([]);
  const [searchButtonCue, setSearchButtonCue] = useState<0 | 1 | 2>(0);

  // Restore search from URL on mount (back-button support).
  useEffect(() => {
    if (initialSearch.q) {
      const requestId = ++searchRequestId.current;
      searchWiki(initialSearch.q, initialSearch.mode)
        .then((response) => {
          if (requestId !== searchRequestId.current) return;
          setSubmittedMode(initialSearch.mode);
          setResults(response.results);
          setAiAnswer(response.aiAnswer);
          setCitations(response.citations);
          setExpandKeywords(response.expand?.keywords ?? []);
        })
        .catch((err: Error) => {
          if (requestId !== searchRequestId.current) return;
          setError(err instanceof Error ? err.message : 'Search failed');
          setResults([]);
          setAiAnswer('');
          setCitations([]);
          setExpandKeywords([]);
        })
        .finally(() => {
          if (requestId === searchRequestId.current) setLoading(false);
        });
    }
  }, [initialSearch.q, initialSearch.mode]);

  // Drop previous project's query/results when the active project changes.
  // Skip initial hydrate (null → first project) so deep-linked /?q= is preserved.
  useEffect(() => {
    const previous = previousProjectIdRef.current;
    const next = currentProject?.id;
    previousProjectIdRef.current = next;
    if (!previous || !next || previous === next) return;

    sampledProjectIdRef.current = undefined;
    hasSampledQueriesRef.current = false;
    setSuggestedQueryChips([]);
    setStatus(null);
    citationRequestId.current += 1;
    searchRequestId.current += 1;
    setQuery('');
    setMode('wiki');
    setResults([]);
    setAiAnswer('');
    setCitations([]);
    setExpandKeywords([]);
    setSearched(false);
    setLoading(false);
    setError('');
    setModal(null);
    setModalLoading(false);
    setSubmittedMode('wiki');
    syncUrl('', 'wiki');
  }, [currentProject?.id]);

  useEffect(() => {
    getStatus()
      .then((data) => {
        if (ignore) return;
        setStatus(data);
        if (!hasSampledQueriesRef.current || sampledProjectIdRef.current !== currentProject?.id) {
          sampledProjectIdRef.current = currentProject?.id;
          hasSampledQueriesRef.current = true;
          setSuggestedQueryChips(sampleSuggestedQueries(data.suggestedQueries));
        }
      })
      .catch((err: Error) => {
        if (!ignore) setStatusError(err.message);
      });
    let ignore = false;
    return () => {
      ignore = true;
    };
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

  const handleSearch = useCallback(async (searchMode: SearchMode, rawQuery = query) => {
    const trimmed = rawQuery.trim();
    if (!trimmed) return;
    const requestId = ++searchRequestId.current;

    syncUrl(trimmed, searchMode);
    setSubmittedMode(searchMode);

    setLoading(true);
    setError('');
    setAiAnswer('');
    setCitations([]);
    setExpandKeywords([]);
    setSearched(true);

    try {
      const response = await searchWiki(trimmed, searchMode);
      if (requestId !== searchRequestId.current) return;
      setResults(response.results);
      setAiAnswer(response.aiAnswer);
      setCitations(response.citations);
      setExpandKeywords(response.expand?.keywords ?? []);
    } catch (err) {
      if (requestId !== searchRequestId.current) return;
      setError(err instanceof Error ? err.message : 'Search failed');
      setResults([]);
      setAiAnswer('');
      setCitations([]);
      setExpandKeywords([]);
    } finally {
      if (requestId === searchRequestId.current) {
        setLoading(false);
      }
    }
  }, [query]);

  const onSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (loading) return;
    setSearchButtonCue(0);
    await handleSearch(mode);
  }, [handleSearch, loading, mode]);

  const triggerSearchButtonCue = useCallback(() => {
    setSearchButtonCue((current) => (current === 1 ? 2 : 1));
  }, []);

  const handleSuggestedQuery = useCallback((suggestion: string) => {
    setQuery(suggestion);
    triggerSearchButtonCue();
  }, [triggerSearchButtonCue]);

  const closeCitationModal = useCallback(() => {
    citationRequestId.current += 1;
    setModalLoading(false);
    setModal(null);
  }, []);

  const openCitation = useCallback(async (citation: Citation) => {
    const requestId = ++citationRequestId.current;
    const safeId = safeWikiRouteSegment(citation.id);
    const safeSlug = safeWikiRouteSegment(citation.slug);
    const pathSlug = citationPathSegment(citation.type, citation.path);
    const lookup = safeId ?? safeSlug ?? pathSlug;

    setModalLoading(Boolean(lookup));
    setModal({
      title: citation.text,
      content: '',
      type: citation.type,
      slug: safeSlug ?? pathSlug ?? '',
      id: safeId ?? undefined,
      path: citation.path,
      lookup: lookup ?? '',
      label: citation.text,
      error: lookup ? '' : t('Detail.citationLoadFailed'),
    });

    if (!lookup) return;

    try {
      const fetch = citation.type === 'concept' ? getConcept : getSource;
      const entry: WikiEntry = await fetch(lookup);
      if (requestId !== citationRequestId.current) return;
      const entrySlug = safeWikiRouteSegment(entry.slug) ?? safeSlug ?? pathSlug ?? '';
      const entryId = safeWikiRouteSegment(entry.id) ?? safeId ?? undefined;
      setModal({
        title: entry.title,
        content: typeof entry.content === 'string'
          ? entry.content
          : typeof entry.raw === 'string'
            ? entry.raw
            : '',
        type: citation.type,
        slug: entrySlug,
        id: entryId,
        path: citation.path,
        lookup,
        label: citation.text,
        error: '',
      });
    } catch (err) {
      if (requestId !== citationRequestId.current) return;
      const message = err instanceof Error && err.message.trim()
        ? err.message
        : t('Detail.citationLoadFailed');
      setModal((current) => (current ? { ...current, error: message } : null));
    } finally {
      if (requestId === citationRequestId.current) {
        setModalLoading(false);
      }
    }
  }, [t]);

  // Close modal on Escape
  useEffect(() => {
    if (!modal) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        closeCitationModal();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [closeCitationModal, modal]);

  const retryCitation = useCallback(() => {
    if (!modal) return;
    openCitation({
      text: modal.label,
      slug: modal.slug,
      type: modal.type,
      id: modal.id,
      path: modal.path,
    });
  }, [modal, openCitation]);

  const resultType = (type?: string): 'source' | 'concept' =>
    type === 'source' ? 'source' : 'concept';
  const suggestedQueries = status?.suggestedQueries ?? [];
  const searchButtonCueState = searchButtonCue > 0 ? searchButtonCue.toString() : undefined;

  return (
    <div className="space-y-10">
      <section className="pt-10 sm:pt-14">
        <p className="font-mono text-[11px] uppercase tracking-[0.22em] text-emerald-300/90">{t('Demo.heroSubtitle')}</p>
        <h1 className="font-serif mt-4 max-w-3xl text-[1.85rem] font-medium leading-[1.28] tracking-tight text-[#eeeae4] sm:text-[2.35rem]">
          {t('Demo.heading')}
        </h1>

        <form onSubmit={onSubmit} className="mt-10 w-full max-w-3xl">
          <div className="flex flex-col gap-3 border-b border-white/18 pb-3 focus-within:border-emerald-400 sm:flex-row sm:items-end">
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label={t('Demo.search')}
              placeholder={suggestedQueries[0] ?? t('Demo.searchPlaceholder')}
              className="min-h-12 flex-1 bg-transparent px-0 text-lg text-white outline-none placeholder:text-zinc-500 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-400"
            />
            <div className="flex items-center gap-2 pb-1">
              <div className="grid grid-cols-2 text-sm">
                {(['wiki', 'full'] as const).map((item) => (
                  <button
                    key={item}
                    type="button"
                    onClick={() => setMode(item)}
                    aria-pressed={mode === item}
                    className={`min-h-11 px-3 font-medium ${
                      mode === item ? 'text-emerald-200 underline decoration-emerald-400/80 decoration-1 underline-offset-8' : 'text-zinc-500 hover:text-zinc-200'
                    }`}
                  >
                    {t(`Demo.${item}`)}
                  </button>
                ))}
              </div>
              <button
                type="submit"
                disabled={loading}
                aria-busy={loading}
                data-search-cue={searchButtonCueState}
                className="min-h-12 rounded-sm bg-emerald-400 px-5 text-sm font-semibold text-zinc-950 hover:bg-emerald-300 disabled:cursor-not-allowed disabled:opacity-60"
              >
                {t('Demo.search')}
              </button>
            </div>
          </div>
          {suggestedQueryChips.length > 0 ? (
            <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-1">
              {suggestedQueryChips.map((suggestion) => (
                <button
                  key={suggestion}
                  type="button"
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => void handleSuggestedQuery(suggestion)}
                  className="max-w-full min-h-11 px-0 text-left text-sm text-zinc-500 hover:text-zinc-200"
                >
                  <span className="block truncate">{suggestion}</span>
                </button>
              ))}
            </div>
          ) : null}
          <p className="mt-5 font-mono text-xs tracking-wide text-zinc-400">
            <StatPill label={t('Shell.sources')} value={status?.sourcesCount} error={statusError} />
            <span className="mx-2 text-white/20" aria-hidden="true">·</span>
            <StatPill label={t('Shell.concepts')} value={status?.conceptsCount} error={statusError} />
            <span className="mx-2 text-white/20" aria-hidden="true">·</span>
            <StatPill label={t('Shell.raw')} value={status?.rawCount} error={statusError} />
            <span className="ml-3 hidden text-zinc-500 sm:inline">⌘&nbsp;K</span>
          </p>
        </form>
      </section>

      {!searched && latestConcepts.length > 0 ? (
        <section className="space-y-4" aria-labelledby="latest-concepts-heading">
          <div className="flex items-center justify-between gap-4">
            <h2 id="latest-concepts-heading" className="font-serif text-xl font-medium text-[#eeeae4]">
              {t('Demo.latestConcepts')}
            </h2>
            <Badge variant="muted">{latestConcepts.length}</Badge>
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            {latestConcepts.map((concept, index) => {
              const href = conceptHref(concept);
              if (!href) return null;
              return (
                <NavigationLink key={concept.slug} href={href} className="group block">
                <Surface
                  variant="default"
                  className="animate-fade-in rounded-sm border-l-2 border-l-emerald-400/80 p-5 [animation-fill-mode:backwards] hover:border-white/20"
                  style={{ animationDelay: `${index * 50}ms` }}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="concept">{t('Entry.singular')}</Badge>
                    <h3 className="font-serif text-lg font-medium text-[#eeeae4] group-hover:text-white">
                      {concept.title}
                    </h3>
                  </div>
                  {concept.description ? (
                    <p className="mt-3 line-clamp-3 text-sm leading-6 text-zinc-400">
                      {concept.description}
                    </p>
                  ) : null}
                </Surface>
                </NavigationLink>
              );
            })}
          </div>
        </section>
      ) : null}

      <section className="space-y-4">
        {searched ? (
          <div className="flex items-center justify-between gap-4">
            <h2 className="text-lg font-semibold text-white">{t('Demo.results')}</h2>
            <Badge variant="muted">{submittedMode} mode</Badge>
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
                  <Badge variant="accent">
                    {t('Demo.sourcesCount', { count: citations.length })}
                  </Badge>
                ) : null}
              </div>
              <div className="mt-3 text-base leading-7 text-zinc-200
                [&_strong]:text-white [&_strong]:font-semibold
                [&_h2]:text-lg [&_h2]:font-semibold [&_h2]:text-white [&_h2]:mt-4 [&_h2]:mb-2
                [&_h3]:text-base [&_h3]:font-semibold [&_h3]:text-white [&_h3]:mt-4 [&_h3]:mb-1
                [&_ul]:list-disc [&_ul]:pl-5 [&_ul]:space-y-1 [&_ul]:mb-3
                [&_ol]:list-decimal [&_ol]:pl-5 [&_ol]:space-y-1 [&_ol]:mb-3
                [&_li]:leading-7
                [&_p]:mb-3
              ">
                <AiAnswerMarkdown content={aiAnswer} citations={citations} onCitationClick={openCitation} />
                {citations.length > 0 ? (
                  <section className="mt-5 border-t border-white/10 pt-4" aria-labelledby="answer-sources-heading">
                    <h4 id="answer-sources-heading" className="text-sm font-semibold text-emerald-200">
                      {t('Demo.sources')}
                    </h4>
                    <ul aria-labelledby="answer-sources-heading" className="mt-2 space-y-1">
                      {citations.map((citation, index) => (
                        <li
                          key={[citation.type, citation.id ?? '', citation.slug ?? '', citation.path ?? '', index]
                            .map(String)
                            .map(encodeURIComponent)
                            .join(':')}
                        >
                          <button
                            type="button"
                            aria-label={t('Demo.openCitation', { citation: citation.text })}
                            onClick={() => openCitation(citation)}
                            className="font-medium text-emerald-300 underline decoration-emerald-300/60 underline-offset-4 hover:text-emerald-200 cursor-pointer"
                          >
                            {citation.text}
                          </button>
                        </li>
                      ))}
                    </ul>
                  </section>
                ) : null}
              </div>
            </div>
          </article>
        ) : null}
        {!loading && !error && searched && results.length === 0 ? (
          <EmptyState message={t('Demo.noResults')} />
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
                  id: result.id,
                  path: '',
                })}
                className={`animate-fade-in rounded-[var(--radius-lg)] border border-l-[3px] border-white/10 bg-zinc-900/40 p-5 text-left backdrop-blur-sm transition duration-200 [animation-fill-mode:backwards] hover:-translate-y-0.5 hover:border-emerald-400/30 hover:shadow-lg hover:shadow-emerald-500/5 ${typeBorderClass}`}
                style={{ animationDelay: `${index * 50}ms` }}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={type}>
                      {type === 'source' ? t('Source.singular') : t('Entry.singular')}
                    </Badge>
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
          aria-labelledby="citation-modal-title"
          role="dialog"
          aria-modal="true"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm animate-fade-in"
          onClick={closeCitationModal}
        >
          <Surface
            variant="elevated"
            className="relative max-h-[85vh] w-full max-w-2xl overflow-y-auto p-6 animate-scale-in"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              onClick={closeCitationModal}
              autoFocus
              className="absolute right-4 top-4 rounded-md p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white"
              aria-label="Close"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
              </svg>
            </button>

            <Badge variant={modal.type === 'concept' ? 'concept' : 'source'}>
              {modal.type === 'concept' ? t('Entry.singular') : t('Source.singular')}
            </Badge>
            <h2 id="citation-modal-title" className="text-2xl font-semibold text-white">
              {modal.title}
            </h2>

            {modalLoading ? (
              <LoadingState label={t('Detail.citationLoading')} />
            ) : modal?.error ? (
              <div className="rounded-md border border-amber-300/30 bg-amber-500/10 p-4">
                <p role="alert" className="text-sm text-amber-100">{modal.error}</p>
                <button
                  type="button"
                  className="mt-3 rounded-md border border-amber-300/40 px-3 py-2 text-sm text-amber-100"
                  onClick={retryCitation}
                  aria-label={t('Detail.retryCitation')}
                >
                  {t('Detail.retryCitation')}
                </button>
              </div>
            ) : (
              <>
                <div className="mt-4 border-t border-white/10 pt-4">
                  <MarkdownBody content={stripLeadingHeading(modal.content)} />
                </div>
                {(() => {
                  const href = entryDetailHref(modal);
                  return href ? (
                    <div className="mt-6 border-t border-white/10 pt-4">
                      <NavigationLink
                        href={href}
                        className="text-sm font-medium text-emerald-300 hover:text-emerald-200"
                        onClick={closeCitationModal}
                      >
                        {t('Detail.openFullPage')}
                      </NavigationLink>
                    </div>
                  ) : null;
                })()}
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

function MarkdownBody({ content }: { content: string }) {
  if (!content) return <p className="text-zinc-400 italic">No content available.</p>;
  // Convert [[wikilinks]] with context-aware routing:
  // Under ## Sources → /sources/ | Under ## Concepts → /concepts/ | else → /concepts/
  const withLinks = resolveWikilinksInMarkdown(content);
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

type CitationText = Text & { value: string };

function isStandaloneCitation(source: string, start: number, end: number) {
  return source[start - 1] !== '!' && !/^[\s]*[(:]/.test(source.slice(end));
}

function transformCitationText(
  node: CitationText,
  citationMap: Map<string, number>,
  content: string,
): PhrasingContent[] {
  const result: PhrasingContent[] = [];
  const citationToken = /\[([^\]\n]+)\]/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = citationToken.exec(node.value))) {
    const label = match[1];
    const index = citationMap.get(label);
    const rawRange = getExactRawCitationRange(node, content, match.index, citationToken.lastIndex);
    if (index === undefined || rawRange === null || !isStandaloneCitation(node.value, match.index, citationToken.lastIndex)) continue;
    if (match.index > lastIndex) result.push({ type: 'text', value: node.value.slice(lastIndex, match.index) });
    result.push({
      type: 'link',
      url: `#citation-${index}`,
      children: [{ type: 'text', value: label }],
      data: { hProperties: { 'data-citation-index': index } },
    });
    lastIndex = citationToken.lastIndex;
  }

  if (lastIndex === 0) return [node];
  if (lastIndex < node.value.length) result.push({ type: 'text', value: node.value.slice(lastIndex) });
  return result;
}

function remarkCitations(citationMap: Map<string, number>, content: string) {
  const VOID_HTML_TAGS = new Set([
    'area',
    'base',
    'br',
    'col',
    'embed',
    'hr',
    'img',
    'input',
    'link',
    'meta',
    'param',
    'source',
    'track',
    'wbr',
  ]);
  const INVALID_HTML_CONTEXT = '?' as const;

  return () => (tree: Root) => {
    function updateRawHtmlContext(value: string, openTags: string[]) {
      const nextOpenTags = [...openTags];
      for (const tag of value.match(/<!--[\s\S]*?-->|<[^>]*>/g) ?? []) {
        if (tag.startsWith('<!--')) continue;

        const closeMatch = /^<\s*\/\s*([a-z][\w:-]*)\s*>$/i.exec(tag);
        if (closeMatch) {
          const tagName = closeMatch[1].toLowerCase();
          if (VOID_HTML_TAGS.has(tagName) && nextOpenTags.at(-1) !== tagName) {
            if (nextOpenTags.at(-1) !== INVALID_HTML_CONTEXT) {
              nextOpenTags.push(INVALID_HTML_CONTEXT);
            }
            continue;
          }
          if (VOID_HTML_TAGS.has(tagName)) continue;
          if (nextOpenTags.at(-1) === tagName) nextOpenTags.pop();
          else if (nextOpenTags.at(-1) !== INVALID_HTML_CONTEXT) {
            nextOpenTags.push(INVALID_HTML_CONTEXT);
          }
          continue;
        }

        const selfClosingMatch = /^<\s*([a-z][\w:-]*)(?:\s+[^>]*?)?\/\s*>$/i.exec(tag);
        if (selfClosingMatch) continue;

        const openMatch = /^<\s*([a-z][\w:-]*)(?:\s+[^>]*?)?>$/i.exec(tag);
        if (!openMatch) {
          if (nextOpenTags.at(-1) !== INVALID_HTML_CONTEXT) {
            nextOpenTags.push(INVALID_HTML_CONTEXT);
          }
          continue;
        }

        const tagName = openMatch[1].toLowerCase();
        if (VOID_HTML_TAGS.has(tagName)) continue;
        nextOpenTags.push(tagName);
      }
      return nextOpenTags;
    }

    function visit(
      node: Root | { type: string; children: Array<Content | PhrasingContent> },
      rawHtmlTags: string[] = [],
    ): string[] {
      if (!('children' in node) || node.type === 'link' || node.type === 'image' || node.type === 'inlineCode' || node.type === 'code' || node.type === 'html') return rawHtmlTags;
      const children: Array<Content | PhrasingContent> = [];
      let nextTags = rawHtmlTags;
      for (const child of node.children) {
        if (child.type === 'html') {
          nextTags = updateRawHtmlContext(child.value, nextTags);
          children.push(child);
          continue;
        }
        if (child.type === 'text') {
          if (nextTags.length > 0) children.push(child);
          else children.push(...transformCitationText(child, citationMap, content));
        }
        else {
          if ('children' in child) nextTags = visit(child, nextTags);
          children.push(child);
        }
      }
      node.children = children;
      return nextTags;
    }
    visit(tree);
  };
}

function AiAnswerMarkdown({
  content,
  citations,
  onCitationClick,
}: {
  content: string;
  citations: Citation[];
  onCitationClick: (citation: Citation) => void;
}) {
  const citationMap = new Map<string, number>();
  const ambiguousCitationLabels = new Set<string>();
  citations.forEach((citation, index) => {
    if (ambiguousCitationLabels.has(citation.text)) return;
    if (citationMap.has(citation.text)) {
      citationMap.delete(citation.text);
      ambiguousCitationLabels.add(citation.text);
      return;
    }
    citationMap.set(citation.text, index);
  });
  const citationByIndex = citations;

  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkCitations(citationMap, content)]}
      components={{
        img: ({ alt }) => {
          const label = alt?.trim();
          const safeLabel = label ? `Image: ${label}` : 'Image placeholder';

          return (
            <span
              role="img"
              aria-label={safeLabel}
              className="inline-block rounded border border-dashed border-zinc-700 bg-zinc-900/40 px-2 py-1 text-xs text-zinc-300"
            >
              {`[${safeLabel}]`}
            </span>
          );
        },
        a: ({ node, href, children }) => {
          const properties = node?.properties;
          const citationIndex = properties?.['data-citation-index'];
          const citation = typeof citationIndex === 'number' ? citationByIndex[citationIndex] : undefined;
          if (!citation) return <a href={href} rel="noopener noreferrer">{children}</a>;
          return (
            <button
              type="button"
              onClick={() => onCitationClick(citation)}
              className="font-medium text-emerald-300 underline decoration-emerald-300/60 underline-offset-4 hover:text-emerald-200 cursor-pointer"
            >
              {children}
            </button>
          );
        },
      }}
    >
      {content}
    </ReactMarkdown>
  );
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
    <span className="inline-flex items-center gap-1.5 text-zinc-400">
      <span className="tabular-nums text-[#eeeae4]">{error ? '—' : value ?? '…'}</span>
      <span>{label}</span>
    </span>
  );
}
