'use client';

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';

export const LIST_VIEW_PAGE_SIZE = 12;
const LIST_SESSION_MARKER = 'llm-wiki:list-session:v1';
const LIST_STATE_PREFIX = 'llm-wiki:list-state:v1:';
const LIST_STATE_SCHEMA_VERSION = 1;
const listViewMemory = new Map<string, string>();
const memoryOnlyNamespaces = new Set<string>();

export type ListName = 'sources' | 'concepts';
export type ListViewState = {
  search: string;
  filters: Record<string, string>;
  order: string;
  page: number;
};
export type ListViewScope = {
  ownerId: string;
  namespaceId: string;
  projectId: string;
  list: ListName;
};
export type ListViewValidation = {
  filters: Record<string, readonly string[]>;
  orders: readonly string[];
};

function getSessionStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

function randomId(): string {
  try {
    if (typeof window !== 'undefined' && typeof window.crypto?.randomUUID === 'function') {
      return window.crypto.randomUUID();
    }
    if (typeof window !== 'undefined' && typeof window.crypto?.getRandomValues === 'function') {
      return [...window.crypto.getRandomValues(new Uint8Array(16))]
        .map((value) => value.toString(16).padStart(2, '0'))
        .join('');
    }
  } catch {
    // The namespace is an isolation identifier, not a credential.
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
}

export function createListViewOpaqueId(): string {
  return randomId();
}

type SessionMarker = { schema: 1; ownerId: string; namespaceId: string };

function parseSessionMarker(value: string | null): SessionMarker | null {
  if (!value) return null;
  try {
    const parsed: unknown = JSON.parse(value);
    if (!parsed || typeof parsed !== 'object') return null;
    const marker = parsed as Partial<SessionMarker>;
    return marker.schema === 1
      && typeof marker.ownerId === 'string'
      && typeof marker.namespaceId === 'string'
      && /^[A-Za-z0-9-]{1,128}$/.test(marker.namespaceId)
      ? marker as SessionMarker
      : null;
  } catch {
    return null;
  }
}

function memoryPrefix(namespaceId: string): string {
  return `${LIST_STATE_PREFIX}${namespaceId}:`;
}

function clearMemoryNamespace(namespaceId: string): void {
  const prefix = memoryPrefix(namespaceId);
  for (const key of listViewMemory.keys()) if (key.startsWith(prefix)) listViewMemory.delete(key);
  memoryOnlyNamespaces.delete(namespaceId);
}

function clearStoredNamespace(storage: Storage, namespaceId: string): void {
  const prefix = memoryPrefix(namespaceId);
  const keys: string[] = [];
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (key?.startsWith(prefix)) keys.push(key);
  }
  keys.forEach((key) => storage.removeItem(key));
}

function clearAllStoredListStates(storage: Storage): void {
  const keys: string[] = [];
  for (let index = 0; index < storage.length; index += 1) {
    const key = storage.key(index);
    if (key?.startsWith(LIST_STATE_PREFIX)) keys.push(key);
  }
  keys.forEach((key) => storage.removeItem(key));
}

/** Starts a new explicit login namespace or resumes the matching active tab session. */
export function beginListViewSession(ownerId: string, resume: boolean): string {
  const storage = getSessionStorage();
  let previous: SessionMarker | null = null;
  let canPersist = Boolean(storage);

  if (storage) {
    try {
      // A read-only or partially denied storage must never revive an old login namespace.
      const probeKey = `llm-wiki:list-session-probe:${randomId()}`;
      storage.setItem(probeKey, '1');
      storage.removeItem(probeKey);
      previous = parseSessionMarker(storage.getItem(LIST_SESSION_MARKER));
    } catch {
      canPersist = false;
    }
  }

  const namespaceId = resume && canPersist && previous?.ownerId === ownerId
    ? previous.namespaceId
    : randomId();

  if (previous && previous.namespaceId !== namespaceId) {
    clearMemoryNamespace(previous.namespaceId);
    if (storage && canPersist) {
      try {
        clearStoredNamespace(storage, previous.namespaceId);
      } catch {
        canPersist = false;
      }
    }
  } else if (!previous && storage && canPersist) {
    try {
      clearAllStoredListStates(storage);
    } catch {
      canPersist = false;
    }
  }

  if (canPersist && storage) {
    try {
      storage.setItem(LIST_SESSION_MARKER, JSON.stringify({ schema: 1, ownerId, namespaceId } satisfies SessionMarker));
    } catch {
      canPersist = false;
    }
  }
  if (!canPersist) memoryOnlyNamespaces.add(namespaceId);
  return namespaceId;
}

/** Invalidates one login's records; deletion is best-effort and never blocks logout. */
export function clearListViewSession(ownerId: string | null, namespaceId: string | null): void {
  if (namespaceId) clearMemoryNamespace(namespaceId);
  const storage = getSessionStorage();
  if (!storage) return;
  try {
    if (namespaceId) clearStoredNamespace(storage, namespaceId);
    const marker = parseSessionMarker(storage.getItem(LIST_SESSION_MARKER));
    if (marker && (!namespaceId || marker.namespaceId === namespaceId) && (!ownerId || marker.ownerId === ownerId)) {
      storage.removeItem(LIST_SESSION_MARKER);
    }
  } catch {
    // A subsequent login always receives a fresh namespace, so stale rows are unreachable.
  }
}

function stateKey(scope: ListViewScope): string {
  return `${LIST_STATE_PREFIX}${scope.namespaceId}:${encodeURIComponent(scope.ownerId)}:${encodeURIComponent(scope.projectId)}:${scope.list}`;
}

function decodeStoredState(value: string | null): unknown {
  if (!value) return null;
  try {
    const parsed: unknown = JSON.parse(value);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    const record = parsed as { schemaVersion?: unknown; state?: unknown };
    return record.schemaVersion === LIST_STATE_SCHEMA_VERSION ? record.state : null;
  } catch {
    return null;
  }
}

export function readListViewState(scope: ListViewScope): unknown {
  const key = stateKey(scope);
  const parseMemoryValue = (): unknown => decodeStoredState(listViewMemory.get(key) ?? null);
  if (memoryOnlyNamespaces.has(scope.namespaceId)) return parseMemoryValue();
  const storage = getSessionStorage();
  if (storage) {
    try {
      const stored = storage.getItem(key);
      if (stored !== null) return decodeStoredState(stored) ?? parseMemoryValue();
    } catch {
      memoryOnlyNamespaces.add(scope.namespaceId);
    }
  }
  return parseMemoryValue();
}

export function writeListViewState(scope: ListViewScope, state: ListViewState): void {
  const key = stateKey(scope);
  const value = JSON.stringify({ schemaVersion: LIST_STATE_SCHEMA_VERSION, state });
  listViewMemory.set(key, value);
  if (memoryOnlyNamespaces.has(scope.namespaceId)) return;
  const storage = getSessionStorage();
  if (!storage) {
    memoryOnlyNamespaces.add(scope.namespaceId);
    return;
  }
  try {
    storage.setItem(key, value);
  } catch {
    memoryOnlyNamespaces.add(scope.namespaceId);
  }
}

export function normalizeListViewState(
  value: unknown,
  defaults: ListViewState,
  validation: ListViewValidation,
): ListViewState {
  if (!value || typeof value !== 'object') return defaults;
  const stored = value as Partial<ListViewState>;
  const filters = stored.filters && typeof stored.filters === 'object' && !Array.isArray(stored.filters)
    ? stored.filters as Record<string, unknown>
    : {};
  const normalizedFilters = Object.fromEntries(Object.entries(defaults.filters).map(([name, fallback]) => {
    const allowed = validation.filters[name] ?? [];
    const candidate = filters[name];
    return [name, typeof candidate === 'string' && allowed.includes(candidate) ? candidate : fallback];
  }));
  return {
    search: typeof stored.search === 'string' ? stored.search.slice(0, 2000) : defaults.search,
    filters: normalizedFilters,
    order: typeof stored.order === 'string' && validation.orders.includes(stored.order) ? stored.order : defaults.order,
    page: Number.isSafeInteger(stored.page) && (stored.page ?? 0) > 0 && (stored.page ?? 0) <= 1_000_000
      ? stored.page as number
      : defaults.page,
  };
}

export function paginate<T>(items: readonly T[], requestedPage: number, pageSize = LIST_VIEW_PAGE_SIZE) {
  const safePageSize = Number.isSafeInteger(pageSize) && pageSize > 0 ? pageSize : LIST_VIEW_PAGE_SIZE;
  const pageCount = Math.ceil(items.length / safePageSize);
  const page = pageCount === 0 ? 1 : Math.min(Math.max(1, Number.isSafeInteger(requestedPage) ? requestedPage : 1), pageCount);
  const start = (page - 1) * safePageSize;
  return { page, pageCount, items: items.slice(start, start + safePageSize) };
}

export function useListViewState({
  list,
  projectId,
  ownerId,
  namespaceId,
  generation,
  enabled,
  defaults,
  validation,
  isSessionCurrent,
}: {
  list: ListName;
  projectId: string;
  ownerId: string | null;
  namespaceId: string | null;
  generation: number;
  enabled: boolean;
  defaults: ListViewState;
  validation: ListViewValidation;
  isSessionCurrent: (namespaceId: string, generation: number, ownerId: string) => boolean;
}) {
  const [storedState, setStoredState] = useState(defaults);
  const [hydratedScopeIdentity, setHydratedScopeIdentity] = useState<string | null>(null);
  const scopeRef = useRef({ list, projectId, ownerId, namespaceId, generation, enabled, isSessionCurrent });
  useLayoutEffect(() => {
    scopeRef.current = { list, projectId, ownerId, namespaceId, generation, enabled, isSessionCurrent };
  }, [enabled, generation, isSessionCurrent, list, namespaceId, ownerId, projectId]);
  const scopeIdentity = JSON.stringify([list, projectId, ownerId, namespaceId, generation, enabled]);
  const ready = hydratedScopeIdentity === scopeIdentity;
  const state = ready ? storedState : defaults;

  const currentScope = useCallback((): ListViewScope | null => {
    const current = scopeRef.current;
    if (!current.enabled || !current.ownerId || !current.namespaceId) return null;
    if (!current.isSessionCurrent(current.namespaceId, current.generation, current.ownerId)) return null;
    return { list: current.list, projectId: current.projectId, ownerId: current.ownerId, namespaceId: current.namespaceId };
  }, []);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      const scope = currentScope();
      if (!scope) return;
      const current = scopeRef.current;
      const currentIdentity = JSON.stringify([current.list, current.projectId, current.ownerId, current.namespaceId, current.generation, current.enabled]);
      if (currentIdentity !== scopeIdentity || !current.isSessionCurrent(current.namespaceId ?? '', current.generation, current.ownerId ?? '')) return;
      setStoredState(normalizeListViewState(readListViewState(scope), defaults, validation));
      setHydratedScopeIdentity(scopeIdentity);
    });
    return () => { cancelled = true; };
  }, [currentScope, defaults, scopeIdentity, validation]);

  useEffect(() => {
    if (!ready) return;
    const scope = currentScope();
    if (!scope) return;
    writeListViewState(scope, state);
  }, [currentScope, ready, scopeIdentity, state]);

  const update = useCallback((change: (current: ListViewState) => ListViewState) => {
    if (!ready || !currentScope()) return;
    setStoredState((current) => change(current));
  }, [currentScope, ready]);

  return { state, update, ready };
}
