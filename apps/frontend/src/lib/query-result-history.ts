import type { Citation, QueryTerminalMetadata, SearchResult } from './api';

const ENTRY_ID_KEY = '__lwc342QueryEntryId';
const DOCUMENT_ID_KEY = '__lwc342DocumentId';
let fallbackId = 0;

function createId(): string {
  if (typeof window !== 'undefined' && window.crypto?.randomUUID) {
    return window.crypto.randomUUID();
  }
  fallbackId += 1;
  return `${Date.now().toString(36)}-${fallbackId.toString(36)}-${Math.random().toString(36).slice(2)}`;
}

const documentId = typeof window === 'undefined' ? '' : createId();

export type QueryHistoryEntry = {
  entryId: string | null;
  isCurrentDocument: boolean;
};

export type CompletedQueryResult = QueryTerminalMetadata & {
  entryId: string;
  ownerId: string;
  projectId: string;
  query: string;
  mode: 'wiki' | 'full';
  results: SearchResult[];
  aiAnswer: string;
  citations: Citation[];
  expandKeywords: string[];
};

export function copyQueryTerminalMetadata(source: QueryTerminalMetadata): QueryTerminalMetadata {
  return {
    status: source.status,
    reason: source.reason,
    answer_basis: source.answer_basis,
    wiki_evidence_status: source.wiki_evidence_status,
    disclosure_required: source.disclosure_required,
    ai_operation_failures: source.ai_operation_failures?.map((failure) => ({ ...failure })),
  };
}

const completedResults = new Map<string, CompletedQueryResult>();

function currentHistoryState(): Record<string, unknown> {
  const state: unknown = typeof window === 'undefined' ? null : window.history.state;
  return state && typeof state === 'object' ? state as Record<string, unknown> : {};
}

function customHistoryState(): Record<string, unknown> {
  const state = { ...currentHistoryState() };
  delete state.__NA;
  delete state._N;
  delete state.__PRIVATE_NEXTJS_INTERNALS_TREE;
  return state;
}

export function readQueryHistoryEntry(): QueryHistoryEntry {
  if (typeof window === 'undefined') return { entryId: null, isCurrentDocument: false };
  const state = currentHistoryState();
  const entryId = typeof state[ENTRY_ID_KEY] === 'string' ? state[ENTRY_ID_KEY] as string : null;
  return {
    entryId,
    isCurrentDocument: Boolean(entryId && state[DOCUMENT_ID_KEY] === documentId),
  };
}

export function replaceQueryHistoryUrl(url: string, hasQuery: boolean): string | null {
  if (typeof window === 'undefined') return null;
  const state = customHistoryState();
  const entryId = hasQuery ? createId() : null;
  if (entryId) {
    state[ENTRY_ID_KEY] = entryId;
    state[DOCUMENT_ID_KEY] = documentId;
  } else {
    delete state[ENTRY_ID_KEY];
    delete state[DOCUMENT_ID_KEY];
  }
  window.history.replaceState(state, '', url);
  return entryId;
}

export function ensureQueryHistoryEntry(): string | null {
  if (typeof window === 'undefined') return null;
  const current = readQueryHistoryEntry();
  if (current.entryId && current.isCurrentDocument) return current.entryId;

  const state = customHistoryState();
  const entryId = createId();
  state[ENTRY_ID_KEY] = entryId;
  state[DOCUMENT_ID_KEY] = documentId;
  window.history.replaceState(state, '');
  return entryId;
}

export function getCompletedQueryResult(entryId: string): CompletedQueryResult | null {
  return completedResults.get(entryId) ?? null;
}

export function isCurrentQueryHistoryEntry(entryId: string): boolean {
  const current = readQueryHistoryEntry();
  return current.isCurrentDocument && current.entryId === entryId;
}

export function saveCompletedQueryResult(result: CompletedQueryResult): void {
  if (isCurrentQueryHistoryEntry(result.entryId)) {
    completedResults.set(result.entryId, result);
  }
}

export function clearCompletedQueryResults(): void {
  completedResults.clear();
}
