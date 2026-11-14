'use client';

import { createListViewOpaqueId } from './list-view-state';

const ENTRY_ID = '__lwcListEntryId';
const CONSUMED_FOCUS = '__lwcConsumedDocumentFocus';
const HISTORY_ENTRY_EVENT = 'lwc:list-history-entry';

export type DocumentFocusIdentity = {
  entryId: string;
  ownerId: string;
  namespaceId: string;
  projectId: string;
  documentId: string;
};

function currentHistoryState(): Record<string, unknown> {
  const state: unknown = window.history.state;
  return state && typeof state === 'object' && !Array.isArray(state)
    ? state as Record<string, unknown>
    : {};
}

function writeHistoryState(state: Record<string, unknown>): void {
  window.history.replaceState(
    state,
    '',
    `${window.location.pathname}${window.location.search}${window.location.hash}`,
  );
  window.dispatchEvent(new Event(HISTORY_ENTRY_EVENT));
}

export function subscribeListHistoryEntry(listener: () => void): () => void {
  window.addEventListener('popstate', listener);
  window.addEventListener(HISTORY_ENTRY_EVENT, listener);
  return () => {
    window.removeEventListener('popstate', listener);
    window.removeEventListener(HISTORY_ENTRY_EVENT, listener);
  };
}

export function ensureListHistoryEntryId(): string {
  const state = currentHistoryState();
  const existing = state[ENTRY_ID];
  if (typeof existing === 'string' && /^[A-Za-z0-9-]{1,128}$/.test(existing)) return existing;
  const entryId = createListViewOpaqueId();
  writeHistoryState({ ...state, [ENTRY_ID]: entryId });
  return entryId;
}

export function pushDocumentFocusEntry(documentId: string): string {
  const entryId = createListViewOpaqueId();
  const state = { ...currentHistoryState() };
  // Next's App Router patches native pushState to synchronize useSearchParams.
  // Passing its __NA marker bypasses that patch, so let Next copy its own state.
  delete state.__NA;
  delete state._N;
  delete state.__PRIVATE_NEXTJS_INTERNALS_TREE;
  delete state[CONSUMED_FOCUS];
  window.history.pushState(
    { ...state, [ENTRY_ID]: entryId },
    '',
    `/sources?document_id=${encodeURIComponent(documentId)}`,
  );
  window.dispatchEvent(new Event(HISTORY_ENTRY_EVENT));
  return entryId;
}

export function currentListHistoryEntryId(): string | null {
  const value = currentHistoryState()[ENTRY_ID];
  return typeof value === 'string' && /^[A-Za-z0-9-]{1,128}$/.test(value) ? value : null;
}

function sameFocusIdentity(left: unknown, right: DocumentFocusIdentity): boolean {
  if (!left || typeof left !== 'object') return false;
  const value = left as Partial<DocumentFocusIdentity>;
  return value.entryId === right.entryId
    && value.ownerId === right.ownerId
    && value.namespaceId === right.namespaceId
    && value.projectId === right.projectId
    && value.documentId === right.documentId;
}

export function hasConsumedDocumentFocus(identity: DocumentFocusIdentity): boolean {
  const state = currentHistoryState();
  return sameFocusIdentity(state[CONSUMED_FOCUS], identity);
}

export function consumeDocumentFocus(identity: DocumentFocusIdentity): void {
  const state = currentHistoryState();
  writeHistoryState({ ...state, [CONSUMED_FOCUS]: identity });
}

export function documentFocusMatchesLocation(identity: DocumentFocusIdentity): boolean {
  const url = new URL(window.location.href);
  return url.pathname === '/sources'
    && url.searchParams.get('document_id') === identity.documentId
    && currentListHistoryEntryId() === identity.entryId;
}
