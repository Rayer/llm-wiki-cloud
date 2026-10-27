'use client';

import { FormEvent, useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from 'react';
import { usePathname, useSearchParams } from 'next/navigation';
import { ChevronDown } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { Content, PhrasingContent, Root, Text } from 'mdast';
import {
  citationPathSegment,
  ApiError,
  getConcept,
  getConcepts,
  getSource,
  getStatus,
  safeWikiRouteSegment,
  searchWiki,
  QueryApiError,
  type ApiStatus,
  type Citation,
  type QueryTerminalMetadata,
  type SearchResult,
  type WikiEntry,
} from '@/lib/api';
import { useT } from '@/lib/i18n';
import { getExactRawCitationRange } from '@/lib/markdown-citations';
import { resolveWikilinksInMarkdown } from '@/lib/markdown-inline';
import { LAST_PROJECT_KEY } from '@/lib/projects';
import {
  copyQueryTerminalMetadata,
  ensureQueryHistoryEntry,
  getCompletedQueryResult,
  isCurrentQueryHistoryEntry,
  readQueryHistoryEntry,
  replaceQueryHistoryUrl,
  saveCompletedQueryResult,
  type CompletedQueryResult,
  type QueryHistoryEntry,
} from '@/lib/query-result-history';
import { displayErrorId, EmptyState, ErrorIdDetails, ErrorState, LoadingState, useErrorIdCopy } from './States';
import { useWorkspace } from './WorkspaceProvider';
import { SharedServiceOnboardingNotice } from './SharedServiceNotice';
import { Badge } from './ui/Badge';
import { Surface } from './ui/Surface';
import { NavigationLink } from './NavigationBlocker';

function readSearchParams(): { q: string; mode: 'wiki' | 'full'; historyEntry: QueryHistoryEntry } {
  if (typeof window === 'undefined') {
    return { q: '', mode: 'wiki', historyEntry: { entryId: null, isCurrentDocument: false } };
  }
  const params = new URLSearchParams(window.location.search);
  const modeParam = params.get('mode');
  return {
    q: params.get('q') ?? '',
    mode: modeParam === 'full' ? 'full' : 'wiki',
    historyEntry: readQueryHistoryEntry(),
  };
}

function syncUrl(q: string, mode: 'wiki' | 'full'): string | null {
  if (typeof window === 'undefined') return null;
  const params = new URLSearchParams();
  if (q) params.set('q', q);
  if (mode !== 'wiki') params.set('mode', mode);
  const search = params.toString();
  const url = window.location.pathname + (search ? `?${search}` : '');
  return replaceQueryHistoryUrl(url, Boolean(q));
}

type SearchIdentity = { ownerId: string | null; projectId: string | null; authenticated: boolean };

function selectedProjectId(projectId: string | undefined): string | null {
  if (projectId) return projectId;
  try {
    return window.localStorage.getItem(LAST_PROJECT_KEY);
  } catch {
    return null;
  }
}

function sameSearchIdentity(left: SearchIdentity, right: SearchIdentity): boolean {
  return left.ownerId === right.ownerId
    && left.projectId === right.projectId
    && left.authenticated === right.authenticated;
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
  requestId?: string;
  diagnosticId?: string;
};
type SearchMode = 'wiki' | 'full';
type SearchError = { messageKey: string; requestId?: string; diagnosticId?: string };

function searchError(error: unknown): SearchError {
  const apiError = error instanceof QueryApiError ? error : null;
  const legacy = apiError?.message.trim().toLowerCase();
  const legacyKey = legacy === 'q field is required' || legacy?.startsWith('invalid json:')
    ? 'invalidRequest'
    : legacy === 'project not found'
      ? 'projectNotFound'
      : legacy === 'required profile condition is unsupported'
        ? 'unsupportedProfileCondition'
        : legacy === 'concept cache is not configured'
          ? 'queryIndexUnavailable'
          : 'queryFailure';
  const keyByCode: Record<string, string> = {
    invalid_query: 'invalidRequest',
    project_not_found: 'projectNotFound',
    published_generation_missing: 'publishedGenerationMissing',
    profile_unavailable: 'profileUnavailable',
    storage_unavailable: 'storageUnavailable',
    query_runtime_unavailable: 'runtimeUnavailable',
    runtime_identity_unavailable: 'runtimeUnavailable',
    query_provider_failed: 'providerFailed',
    query_index_unavailable: 'queryIndexUnavailable',
    query_timeout: 'timedOut',
    query_canceled: 'canceled',
    unsupported_profile_condition: 'unsupportedProfileCondition',
  };
  return {
    messageKey: `QueryErrors.${apiError ? keyByCode[apiError.code] ?? legacyKey : 'queryFailure'}`,
    requestId: apiError?.requestId,
    diagnosticId: apiError?.diagnosticId,
  };
}

const SUGGESTED_QUERIES_OPEN_KEY = 'llm-wiki:suggested-queries-open';

function subscribeSuggestedQueriesOpen(onStoreChange: () => void) {
  window.addEventListener('storage', onStoreChange);
  window.addEventListener('llm-wiki:suggested-queries-open', onStoreChange);
  return () => {
    window.removeEventListener('storage', onStoreChange);
    window.removeEventListener('llm-wiki:suggested-queries-open', onStoreChange);
  };
}

function getSuggestedQueriesOpen(): boolean {
  return localStorage.getItem(SUGGESTED_QUERIES_OPEN_KEY) !== '0';
}

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
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const routeSearch = searchParams?.toString() ?? '';
  const routeLocation = `${pathname ?? ''}?${routeSearch}`;
  const { currentProject, token, user, hydrated, projectsLoading, isDemoSession, sessionEpoch } = useWorkspace();
  const [initialSearch] = useState(() => readSearchParams());
  const historyResult = initialSearch.historyEntry.entryId && initialSearch.historyEntry.isCurrentDocument
    ? getCompletedQueryResult(initialSearch.historyEntry.entryId)
    : null;
  const initialIdentity: SearchIdentity = {
    ownerId: user?.id ?? null,
    projectId: selectedProjectId(currentProject?.id),
    authenticated: Boolean(token),
  };
  const restoredResult = historyResult
    && initialSearch.q === historyResult.query
    && initialSearch.mode === historyResult.mode
    && initialIdentity.authenticated
    && initialIdentity.ownerId === historyResult.ownerId
    && initialIdentity.projectId === historyResult.projectId
    ? historyResult
    : null;
  const historyRestoreMiss = Boolean(
    initialSearch.q
    && initialSearch.historyEntry.entryId
    && initialSearch.historyEntry.isCurrentDocument
    && !restoredResult,
  );
  const [query, setQuery] = useState(restoredResult?.query ?? initialSearch.q);
  const [mode, setMode] = useState<SearchMode>(restoredResult?.mode ?? initialSearch.mode);
  const [submittedMode, setSubmittedMode] = useState<SearchMode>(restoredResult?.mode ?? initialSearch.mode);
  const [status, setStatus] = useState<ApiStatus | null>(null);
  const [statusError, setStatusError] = useState('');
  const [results, setResults] = useState<SearchResult[]>(restoredResult?.results ?? []);
  const [aiAnswer, setAiAnswer] = useState(restoredResult?.aiAnswer ?? '');
  const [queryOutcome, setQueryOutcome] = useState<QueryTerminalMetadata>(() =>
    restoredResult ? copyQueryTerminalMetadata(restoredResult) : {},
  );
  const [citations, setCitations] = useState<Citation[]>(restoredResult?.citations ?? []);
  const [expandKeywords, setExpandKeywords] = useState<string[]>(restoredResult?.expandKeywords ?? []);
  const [searched, setSearched] = useState(Boolean(initialSearch.q));
  const [loading, setLoading] = useState(Boolean(initialSearch.q && !restoredResult && !historyRestoreMiss));
  const [resultUnavailable, setResultUnavailable] = useState(historyRestoreMiss);
  const [error, setError] = useState<SearchError | null>(null);
  const [errorIdCopied, setErrorIdCopied] = useState(false);
  const [modal, setModal] = useState<ModalEntry | null>(null);
  const [modalLoading, setModalLoading] = useState(false);
  const { copyErrorId: copyCitationErrorId, errorIdCopied: citationErrorIdCopied } =
    useErrorIdCopy(modal, modal?.requestId, modal?.diagnosticId);
  const citationRequestId = useRef(0);
  const searchRequestId = useRef(0);
  const initialUrlSearchStartedRef = useRef(false);
  const initialUrlSearchCancelledRef = useRef(false);
  const mountedRef = useRef(false);
  const searchInFlightRef = useRef(false);
  const queryEntryIdRef = useRef(restoredResult?.entryId ?? null);
  const routeLocationRef = useRef(routeLocation);
  const searchErrorRef = useRef(error);
  const identityRef = useRef({ ...initialIdentity, token });
  const previousAuthOwnerRef = useRef<string | null>(token ? user?.id ?? null : null);
  useLayoutEffect(() => {
    identityRef.current = {
      ownerId: user?.id ?? null,
      projectId: selectedProjectId(currentProject?.id),
      authenticated: Boolean(token),
      token,
    };
  }, [currentProject?.id, token, user?.id]);
  useEffect(() => {
    searchErrorRef.current = error;
  }, [error]);
  const previousProjectIdRef = useRef<string | undefined>(currentProject?.id);
  const sampledProjectIdRef = useRef<string | undefined>(undefined);
  const hasSampledQueriesRef = useRef(false);
  const [suggestedQueryChips, setSuggestedQueryChips] = useState<string[]>([]);
  const suggestedQueriesOpen = useSyncExternalStore(
    subscribeSuggestedQueriesOpen,
    getSuggestedQueriesOpen,
    () => true,
  );
  const [latestConcepts, setLatestConcepts] = useState<WikiEntry[]>([]);
  const [searchButtonCue, setSearchButtonCue] = useState<0 | 1 | 2>(0);

  const isCurrentSearch = useCallback((requestId: number, identity: SearchIdentity, entryId: string | null) => (
    requestId === searchRequestId.current
    && mountedRef.current
    && sameSearchIdentity(identity, identityRef.current)
    && Boolean(entryId && isCurrentQueryHistoryEntry(entryId))
  ), []);

  const applyCompletedResult = useCallback((completed: CompletedQueryResult) => {
    setQuery(completed.query);
    setMode(completed.mode);
    setSubmittedMode(completed.mode);
    setResults(completed.results);
    setAiAnswer(completed.aiAnswer);
    setQueryOutcome(copyQueryTerminalMetadata(completed));
    setCitations(completed.citations);
    setExpandKeywords(completed.expandKeywords);
    setSearched(true);
    setLoading(false);
    setResultUnavailable(false);
    setError(null);
    setErrorIdCopied(false);
    queryEntryIdRef.current = completed.entryId;
  }, []);

  const applyRestoreMiss = useCallback((q: string, searchMode: SearchMode, entryId: string | null) => {
    searchRequestId.current += 1;
    searchInFlightRef.current = false;
    setQuery(q);
    setMode(searchMode);
    setSubmittedMode(searchMode);
    setResults([]);
    setAiAnswer('');
    setQueryOutcome({});
    setCitations([]);
    setExpandKeywords([]);
    setSearched(true);
    setLoading(false);
    setResultUnavailable(true);
    setError(null);
    setErrorIdCopied(false);
    queryEntryIdRef.current = entryId;
  }, []);

  const runUrlSearch = useCallback((q: string, searchMode: SearchMode, entryId: string | null) => {
    if (!entryId) return;
    queryEntryIdRef.current = entryId;
    const requestId = ++searchRequestId.current;
    const identity: SearchIdentity = {
      ownerId: identityRef.current.ownerId,
      projectId: identityRef.current.projectId,
      authenticated: identityRef.current.authenticated,
    };
    setQuery(q);
    setMode(searchMode);
    setSubmittedMode(searchMode);
    setSearched(true);
    setLoading(true);
    setResultUnavailable(false);
    setError(null);
    setErrorIdCopied(false);
    setResults([]);
    setAiAnswer('');
    setQueryOutcome({});
    setCitations([]);
    setExpandKeywords([]);

    searchWiki(q, searchMode)
      .then((response) => {
        if (!isCurrentSearch(requestId, identity, entryId)) return;
        const completed: CompletedQueryResult = {
          entryId,
          ownerId: identity.ownerId ?? '',
          projectId: identity.projectId ?? '',
          query: q,
          mode: searchMode,
          ...copyQueryTerminalMetadata(response),
          results: response.results,
          aiAnswer: response.aiAnswer,
          citations: response.citations,
          expandKeywords: response.expand?.keywords ?? [],
        };
        setResults(response.results);
        setAiAnswer(response.aiAnswer);
        setQueryOutcome(copyQueryTerminalMetadata(response));
        setCitations(response.citations);
        setExpandKeywords(response.expand?.keywords ?? []);
        if (identity.authenticated && identity.ownerId && identity.projectId) {
          saveCompletedQueryResult(completed);
        }
      })
      .catch((err: Error) => {
        if (!isCurrentSearch(requestId, identity, entryId)) return;
        setError(searchError(err));
        setResults([]);
        setAiAnswer('');
        setQueryOutcome({});
        setCitations([]);
        setExpandKeywords([]);
      })
      .finally(() => {
        if (isCurrentSearch(requestId, identity, entryId)) setLoading(false);
      });
  }, [isCurrentSearch]);

  const toggleSuggestedQueries = useCallback(() => {
    const next = localStorage.getItem(SUGGESTED_QUERIES_OPEN_KEY) === '0';
    localStorage.setItem(SUGGESTED_QUERIES_OPEN_KEY, next ? '1' : '0');
    window.dispatchEvent(new Event('llm-wiki:suggested-queries-open'));
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // App Router can preserve this client component across a detail round trip.
  // Reconcile the actual URL/history entry whenever the route changes so a
  // Back navigation restores its own result or reports a safe restore miss.
  useEffect(() => {
    if (routeLocationRef.current === routeLocation) return;
    if (pathname !== '/') {
      routeLocationRef.current = routeLocation;
      return;
    }
    if (!hydrated || projectsLoading || !currentProject) return;

    routeLocationRef.current = routeLocation;
    const next = readSearchParams();
    if (!next.q) {
      searchRequestId.current += 1;
      searchInFlightRef.current = false;
      queryEntryIdRef.current = null;
      setQuery('');
      setMode('wiki');
      setSubmittedMode('wiki');
      setResults([]);
      setAiAnswer('');
      setQueryOutcome({});
      setCitations([]);
      setExpandKeywords([]);
      setSearched(false);
      setLoading(false);
      setResultUnavailable(false);
      setError(null);
      return;
    }

    if (next.historyEntry.isCurrentDocument && next.historyEntry.entryId) {
      if (next.historyEntry.entryId === queryEntryIdRef.current) return;
      const completed = getCompletedQueryResult(next.historyEntry.entryId);
      const identity = identityRef.current;
      if (
        completed
        && completed.query === next.q
        && completed.mode === next.mode
        && identity.authenticated
        && completed.ownerId === identity.ownerId
        && completed.projectId === identity.projectId
      ) {
        applyCompletedResult(completed);
      } else {
        applyRestoreMiss(next.q, next.mode, next.historyEntry.entryId);
      }
      return;
    }

    const entryId = ensureQueryHistoryEntry();
    runUrlSearch(next.q, next.mode, entryId);
  }, [
    applyCompletedResult,
    applyRestoreMiss,
    currentProject,
    hydrated,
    pathname,
    projectsLoading,
    routeLocation,
    runUrlSearch,
  ]);

  // Initial URL queries remain fresh on direct entry and reload, after auth and Project loading.
  useEffect(() => {
    if (
      !initialSearch.q
      || restoredResult
      || historyRestoreMiss
      || initialUrlSearchStartedRef.current
      || initialUrlSearchCancelledRef.current
      || !hydrated
      || !token
      || projectsLoading
      || !currentProject
    ) return;

    initialUrlSearchStartedRef.current = true;
    const entryId = ensureQueryHistoryEntry();
    queryEntryIdRef.current = entryId;
    const requestId = ++searchRequestId.current;
    const identity: SearchIdentity = {
      ownerId: identityRef.current.ownerId,
      projectId: identityRef.current.projectId,
      authenticated: identityRef.current.authenticated,
    };
    searchWiki(initialSearch.q, initialSearch.mode)
      .then((response) => {
        if (!isCurrentSearch(requestId, identity, entryId)) return;
        setSubmittedMode(initialSearch.mode);
        setResults(response.results);
        setAiAnswer(response.aiAnswer);
        setQueryOutcome(copyQueryTerminalMetadata(response));
        setCitations(response.citations);
        setExpandKeywords(response.expand?.keywords ?? []);
        if (entryId && identity.authenticated && identity.ownerId && identity.projectId) {
          const completed: CompletedQueryResult = {
            entryId,
            ownerId: identity.ownerId,
            projectId: identity.projectId,
            query: initialSearch.q,
            mode: initialSearch.mode,
            ...copyQueryTerminalMetadata(response),
            results: response.results,
            aiAnswer: response.aiAnswer,
            citations: response.citations,
            expandKeywords: response.expand?.keywords ?? [],
          };
          saveCompletedQueryResult(completed);
        }
      })
      .catch((err: Error) => {
        if (!isCurrentSearch(requestId, identity, entryId)) return;
        setError(searchError(err));
        setErrorIdCopied(false);
        setResults([]);
        setAiAnswer('');
        setQueryOutcome({});
        setCitations([]);
        setExpandKeywords([]);
      })
      .finally(() => {
        if (isCurrentSearch(requestId, identity, entryId)) setLoading(false);
      });
  }, [
    historyRestoreMiss,
    hydrated,
    initialSearch.mode,
    initialSearch.q,
    isCurrentSearch,
    currentProject,
    projectsLoading,
    restoredResult,
    token,
  ]);

  useEffect(() => {
    const ownerId = token ? user?.id ?? null : null;
    const previousOwnerId = previousAuthOwnerRef.current;
    previousAuthOwnerRef.current = ownerId;
    if (previousOwnerId === null || previousOwnerId === ownerId) return;

    initialUrlSearchCancelledRef.current = true;
    searchRequestId.current += 1;
    searchInFlightRef.current = false;
    citationRequestId.current += 1;
    setQuery('');
    setMode('wiki');
    setResults([]);
    setAiAnswer('');
    setQueryOutcome({});
    setCitations([]);
    setExpandKeywords([]);
    setSearched(false);
    setLoading(false);
    setResultUnavailable(false);
    setError(null);
    setErrorIdCopied(false);
    setModal(null);
    setModalLoading(false);
    setSubmittedMode('wiki');
    queryEntryIdRef.current = null;
    syncUrl('', 'wiki');
  }, [token, user?.id]);

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
    searchInFlightRef.current = false;
    initialUrlSearchCancelledRef.current = true;
    setQuery('');
    setMode('wiki');
    setResults([]);
    setAiAnswer('');
    setQueryOutcome({});
    setCitations([]);
    setExpandKeywords([]);
    setSearched(false);
    setLoading(false);
    setResultUnavailable(false);
    setError(null);
    setErrorIdCopied(false);
    setQueryOutcome({});
    setModal(null);
    setModalLoading(false);
    setSubmittedMode('wiki');
    queryEntryIdRef.current = null;
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
    if (!trimmed || searchInFlightRef.current) return;
    searchInFlightRef.current = true;
    const requestId = ++searchRequestId.current;

    const entryId = syncUrl(trimmed, searchMode);
    queryEntryIdRef.current = entryId;
    const identity: SearchIdentity = {
      ownerId: identityRef.current.ownerId,
      projectId: identityRef.current.projectId,
      authenticated: identityRef.current.authenticated,
    };
    setSubmittedMode(searchMode);

    setLoading(true);
    setResultUnavailable(false);
    setError(null);
    setErrorIdCopied(false);
    setQueryOutcome({});
    setAiAnswer('');
    setCitations([]);
    setExpandKeywords([]);
    setSearched(true);

    try {
      const response = await searchWiki(trimmed, searchMode);
      if (!isCurrentSearch(requestId, identity, entryId)) return;
      setResults(response.results);
      setAiAnswer(response.aiAnswer);
      setQueryOutcome(copyQueryTerminalMetadata(response));
      setCitations(response.citations);
      setExpandKeywords(response.expand?.keywords ?? []);
      if (entryId && identity.authenticated && identity.ownerId && identity.projectId) {
        const completed: CompletedQueryResult = {
          entryId,
          ownerId: identity.ownerId,
          projectId: identity.projectId,
          query: trimmed,
          mode: searchMode,
          ...copyQueryTerminalMetadata(response),
          results: response.results,
          aiAnswer: response.aiAnswer,
          citations: response.citations,
          expandKeywords: response.expand?.keywords ?? [],
        };
        saveCompletedQueryResult(completed);
      }
    } catch (err) {
      if (!isCurrentSearch(requestId, identity, entryId)) return;
      setError(searchError(err));
      setResults([]);
      setAiAnswer('');
      setQueryOutcome({});
      setCitations([]);
      setExpandKeywords([]);
    } finally {
      if (isCurrentSearch(requestId, identity, entryId)) {
        setLoading(false);
        searchInFlightRef.current = false;
      }
    }
  }, [isCurrentSearch, query]);

  const copyErrorId = async () => {
    const errorId = displayErrorId(error?.requestId, error?.diagnosticId);
    if (!errorId || !navigator.clipboard?.writeText) return;
    const requestId = searchRequestId.current;
    const identity = identityRef.current;
    const isCurrentError = () => {
      const currentErrorId = displayErrorId(searchErrorRef.current?.requestId, searchErrorRef.current?.diagnosticId);
      return requestId === searchRequestId.current
        && identity.projectId === identityRef.current.projectId
        && identity.token === identityRef.current.token
        && currentErrorId?.value === errorId.value
        && currentErrorId.kind === errorId.kind;
    };
    try {
      await navigator.clipboard.writeText(errorId.value);
      if (!isCurrentError()) return;
      setErrorIdCopied(true);
    } catch {
      if (!isCurrentError()) return;
      setErrorIdCopied(false);
    }
  };

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
      setModal((current) => (current ? {
        ...current,
        error: message,
        requestId: err instanceof ApiError ? err.requestId : undefined,
        diagnosticId: err instanceof ApiError ? err.diagnosticId : undefined,
      } : null));
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
  const insufficientEvidence = queryOutcome.status === 'insufficient_evidence';
  const aiOperationFailures = queryOutcome.ai_operation_failures ?? [];
  const canReviewAISettings = !isDemoSession && aiOperationFailures.some((failure) => (
    failure.class === 'authentication' || failure.class === 'quota_exhausted' || failure.class === 'billing'
  ));
  const showModelPriorDisclosure = submittedMode === 'full'
    && Boolean(aiAnswer)
    && (queryOutcome.answer_basis === 'model_prior' || queryOutcome.disclosure_required === true);
  const searchButtonCueState = searchButtonCue > 0 ? searchButtonCue.toString() : undefined;

  return (
    <div className="space-y-12">
      <section className="relative overflow-hidden border-b border-white/10 pb-10 pt-10 sm:pt-14">
        <div aria-hidden="true" className="pointer-events-none absolute -right-32 -top-44 size-[34rem] rounded-full bg-emerald-400/10 blur-3xl" />
        <p className="font-mono text-[11px] uppercase tracking-[0.22em] text-emerald-300/90">{t('Demo.heroSubtitle')}</p>
        <h1 className="font-serif relative mt-4 max-w-3xl text-[1.95rem] font-medium leading-[1.2] tracking-tight text-[#eeeae4] text-pretty sm:text-[2.7rem]">
          {t('Demo.heading')}
        </h1>

        <form onSubmit={onSubmit} className="relative mt-10 w-full max-w-4xl overflow-hidden rounded-[var(--radius-lg)] border border-white/15 bg-zinc-900/65 shadow-xl shadow-black/20 backdrop-blur-sm focus-within:border-emerald-300/70">
          <div className="flex items-center gap-3 px-4 py-3 sm:px-5">
            <input
              name="query"
              autoComplete="off"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label={t('Demo.search')}
              placeholder={suggestedQueries[0] ?? t('Demo.searchPlaceholder')}
              className="min-h-12 min-w-0 flex-1 bg-transparent px-0 text-base text-white outline-none placeholder:text-zinc-500 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-400"
            />
            <button
              type="submit"
              disabled={loading}
              aria-busy={loading}
              data-search-cue={searchButtonCueState}
              className="min-h-12 shrink-0 rounded-lg bg-emerald-400 px-5 text-sm font-semibold text-zinc-950 transition hover:bg-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-200 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {t('Demo.search')}
            </button>
          </div>
          <div className="flex flex-col gap-3 border-t border-white/10 px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5">
            <div className="grid w-fit grid-cols-2 rounded-md bg-black/20 p-1 text-sm">
              {(['wiki', 'full'] as const).map((item) => (
                <button
                  key={item}
                  type="button"
                  onClick={() => setMode(item)}
                  aria-pressed={mode === item}
                  className={`min-h-10 rounded px-3 font-medium transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 ${
                    mode === item ? 'bg-emerald-400/15 text-emerald-200 underline decoration-emerald-400/80 decoration-1 underline-offset-4' : 'text-zinc-500 hover:text-zinc-200'
                  }`}
                >
                  {t(`Demo.${item}`)}
                </button>
              ))}
            </div>
            <p className="font-mono text-xs tracking-wide text-zinc-400">
              <StatPill label={t('Shell.sources')} value={status?.sourcesCount} error={statusError} />
              <span className="mx-2 text-white/20" aria-hidden="true">·</span>
              <StatPill label={t('Shell.concepts')} value={status?.conceptsCount} error={statusError} />
              <span className="mx-2 text-white/20" aria-hidden="true">·</span>
              <StatPill label={t('Shell.raw')} value={status?.rawCount} error={statusError} />
              <span className="ml-3 hidden text-zinc-500 sm:inline">⌘&nbsp;K</span>
            </p>
          </div>
          {suggestedQueryChips.length > 0 ? (
            <div className="border-t border-white/10 px-4 py-2 sm:px-5">
              <button
                type="button"
                aria-expanded={suggestedQueriesOpen}
                aria-controls="suggested-queries"
                aria-label={suggestedQueriesOpen ? t('Demo.hideSuggestedQueries') : t('Demo.showSuggestedQueries')}
                onClick={toggleSuggestedQueries}
                className="inline-flex min-h-11 items-center gap-1.5 text-left text-xs tracking-wide text-zinc-500 transition hover:text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
              >
                <ChevronDown className={`size-4 shrink-0 transition-transform ${suggestedQueriesOpen ? '' : '-rotate-90'}`} aria-hidden="true" />
                {t('Demo.suggestedQueries')}
              </button>
              {suggestedQueriesOpen ? (
                <div id="suggested-queries" className="mb-2 flex flex-wrap items-center gap-2">
                  {suggestedQueryChips.map((suggestion) => (
                    <button
                      key={suggestion}
                      type="button"
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => void handleSuggestedQuery(suggestion)}
                      className="max-w-full min-h-11 rounded-full border border-emerald-300/20 bg-emerald-300/10 px-3 py-1.5 text-left text-sm text-emerald-100 transition hover:border-emerald-300/40 hover:bg-emerald-300/15 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
                    >
                      <span className="block truncate">{suggestion}</span>
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}
        </form>
      </section>

      {token && currentProject && user?.id && !isDemoSession ? (
        <SharedServiceOnboardingNotice
          projectId={currentProject.id}
          ownerId={user.id}
          sessionEpoch={sessionEpoch}
          isDemoSession={isDemoSession}
        />
      ) : null}

      {!searched && latestConcepts.length > 0 ? (
        <section className="space-y-4" aria-labelledby="latest-concepts-heading">
          <div className="flex items-center justify-between gap-4">
            <h2 id="latest-concepts-heading" className="font-serif text-xl font-medium text-[#eeeae4]">
              {t('Demo.latestConcepts')}
            </h2>
            <Badge variant="muted">{latestConcepts.length}</Badge>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            {latestConcepts.map((concept, index) => {
              const href = conceptHref(concept);
              if (!href) return null;
              return (
                <NavigationLink key={concept.slug} href={href} className="group block focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-emerald-400">
                <Surface
                  variant="default"
                  className="animate-fade-in rounded-lg border-l-2 border-l-emerald-400/80 p-5 [animation-fill-mode:backwards] transition hover:border-white/20 hover:bg-zinc-900/75"
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
            <Badge variant="muted">{t(submittedMode === 'full' ? 'QueryResults.fullMode' : 'QueryResults.wikiMode')}</Badge>
          </div>
        ) : null}
        {loading ? <LoadingState label={t('QueryResults.searching')} /> : null}
        {error ? (
          <ErrorState
            message={t(error.messageKey)}
            requestId={error.requestId}
            diagnosticId={error.diagnosticId}
            requestIdLabel={t('QueryErrors.requestId')}
            diagnosticIdLabel={t('QueryErrors.diagnosticId')}
            copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
            errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
            errorIdCopied={errorIdCopied}
            onCopyErrorId={copyErrorId}
          />
        ) : null}
        {!loading && resultUnavailable ? (
          <div role="alert" className="rounded-md border border-amber-300/30 bg-amber-500/10 p-4">
            <p className="text-sm text-amber-100">{t('QueryResults.resultUnavailable')}</p>
            <button
              type="button"
              className="mt-3 rounded-md border border-amber-300/40 px-3 py-2 text-sm text-amber-100"
              onClick={() => { void handleSearch(mode, query); }}
            >
              {t('QueryResults.searchAgain')}
            </button>
          </div>
        ) : null}
        {!loading && !error && !resultUnavailable && insufficientEvidence ? (
          <div role="status" className="rounded-md border border-amber-300/30 bg-amber-500/10 p-4">
            <p className="text-sm text-amber-100">{t('QueryResults.insufficientEvidence')}</p>
          </div>
        ) : null}
        {!loading && !error && !resultUnavailable && aiOperationFailures.length > 0 ? (
          <aside role="alert" className="rounded-md border border-amber-300/30 bg-amber-500/10 p-4">
            <h3 className="text-sm font-semibold text-amber-100">{t('QueryResults.aiFailure.heading')}</h3>
            <ul className="mt-2 space-y-3">
              {aiOperationFailures.map((failure, index) => (
                <li key={`${failure.stage}-${failure.class}-${index}`} className="text-sm leading-6 text-amber-100">
                  <p>{t(`QueryResults.aiFailure.stage.${failure.stage}`)}</p>
                  <p>{t(`QueryResults.aiFailure.class.${failure.class}`)}</p>
                  {failure.fallback !== 'none' ? (
                    <p>{t(`QueryResults.aiFailure.fallback.${failure.fallback}`)}</p>
                  ) : null}
                </li>
              ))}
            </ul>
            {canReviewAISettings ? (
              <button
                type="button"
                className="mt-3 min-h-11 rounded-lg border border-amber-200/50 px-4 py-2 text-sm font-medium text-amber-50 hover:bg-amber-100/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-200"
                onClick={() => window.dispatchEvent(new CustomEvent('lwc-open-account-settings', { detail: { focusBYOK: true } }))}
              >
                {t('QueryResults.aiFailure.openSettings')}
              </button>
            ) : null}
          </aside>
        ) : null}
        {!loading && !error && aiAnswer ? (
          <article className="relative overflow-hidden rounded-[var(--radius-lg)] border border-emerald-400/20 bg-emerald-400/[0.06] p-5 backdrop-blur-sm">
            <div className="absolute inset-y-0 left-0 w-1 bg-gradient-to-b from-emerald-300 to-teal-500" />
            <div className="pl-3">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-semibold text-emerald-200">{t('Demo.answer')}</h3>
                {showModelPriorDisclosure ? (
                  <span className="text-sm text-amber-100">{t('QueryResults.modelPriorDisclosure')}</span>
                ) : null}
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
                            className="cursor-pointer font-medium text-emerald-300 underline decoration-emerald-300/60 underline-offset-4 hover:text-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
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
        {!loading && !error && !resultUnavailable && !insufficientEvidence && !aiAnswer && searched && results.length === 0 ? (
          <EmptyState message={t('QueryResults.noResults')} />
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
                className={`animate-fade-in rounded-[var(--radius-lg)] border border-l-[3px] border-white/10 bg-zinc-900/40 p-5 text-left backdrop-blur-sm transition duration-200 [animation-fill-mode:backwards] hover:-translate-y-0.5 hover:border-emerald-400/30 hover:shadow-lg hover:shadow-emerald-500/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 ${typeBorderClass}`}
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
                  {result.excerpt ?? result.description ?? t('QueryResults.openEntryFallback')}
                </p>
              </button>
            );
          })}
        </div>
        {!loading && !error && searched && expandKeywords.length > 0 ? (
          <div className="mt-1 text-xs text-zinc-500">
            {t('QueryResults.expansionKeywords', { keywords: expandKeywords.join('、') })}
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
                <ErrorIdDetails
                  requestId={modal.requestId}
                  diagnosticId={modal.diagnosticId}
                  requestIdLabel={t('QueryErrors.requestId')}
                  diagnosticIdLabel={t('QueryErrors.diagnosticId')}
                  copyErrorIdLabel={t('QueryErrors.copyDiagnosticId')}
                  errorIdCopiedLabel={t('QueryErrors.diagnosticIdCopied')}
                  errorIdCopied={citationErrorIdCopied}
                  onCopyErrorId={copyCitationErrorId}
                  className="mt-3 flex flex-wrap items-center gap-3 text-sm text-amber-100"
                />
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
              className="cursor-pointer font-medium text-emerald-300 underline decoration-emerald-300/60 underline-offset-4 hover:text-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
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
