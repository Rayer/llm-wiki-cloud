import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  beginListViewSession,
  clearListViewSession,
  normalizeListViewState,
  paginate,
  readListViewState,
  writeListViewState,
  type ListViewState,
} from '@/lib/list-view-state';

const validState: ListViewState = {
  search: 'saved query',
  filters: { status: 'published', dates: 'all' },
  order: 'title',
  page: 2,
};

let sessionDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  sessionDescriptor = Object.getOwnPropertyDescriptor(window, 'sessionStorage');
  window.sessionStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  if (sessionDescriptor) Object.defineProperty(window, 'sessionStorage', sessionDescriptor);
  else Reflect.deleteProperty(window, 'sessionStorage');
  window.sessionStorage.clear();
});

describe('LWC-480 list state namespace and codec', () => {
  it('resumes only the matching tab login and isolates project, list and new-login records', () => {
    const first = beginListViewSession('user-a', true);
    const projectA = { ownerId: 'user-a', namespaceId: first, projectId: 'project-a', list: 'concepts' as const };
    const projectB = { ...projectA, projectId: 'project-b' };
    const sources = { ...projectA, list: 'sources' as const };
    writeListViewState(projectA, validState);
    writeListViewState(projectB, { ...validState, search: 'other project' });
    writeListViewState(sources, { ...validState, search: 'other list' });

    expect(beginListViewSession('user-a', true)).toBe(first);
    expect(readListViewState(projectA)).toEqual(validState);
    expect(readListViewState(projectB)).toEqual({ ...validState, search: 'other project' });
    expect(readListViewState(sources)).toEqual({ ...validState, search: 'other list' });

    const nextLogin = beginListViewSession('user-a', false);
    expect(nextLogin).not.toBe(first);
    expect(readListViewState(projectA)).toBeNull();
    expect(readListViewState({ ...projectA, namespaceId: nextLogin })).toBeNull();

    const otherAccount = beginListViewSession('user-b', true);
    expect(otherAccount).not.toBe(nextLogin);
    expect(readListViewState({ ...projectA, ownerId: 'user-b', namespaceId: otherAccount })).toBeNull();
  });

  it('uses per-provider memory state when sessionStorage methods throw and starts fresh on a new provider', () => {
    const originalSetItem = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function setItem(this: Storage, key, value) {
      if (key.startsWith('llm-wiki:list-session')) throw new DOMException('blocked', 'SecurityError');
      return originalSetItem.call(this, key, value);
    });
    const first = beginListViewSession('user-a', true);
    const scope = { ownerId: 'user-a', namespaceId: first, projectId: 'project-a', list: 'sources' as const };
    writeListViewState(scope, validState);
    expect(readListViewState(scope)).toEqual(validState);

    vi.restoreAllMocks();
    const nextProvider = beginListViewSession('user-a', true);
    expect(nextProvider).not.toBe(first);
    expect(readListViewState({ ...scope, namespaceId: nextProvider })).toBeNull();
  });

  it('uses memory-only state when accessing the sessionStorage getter throws', () => {
    Object.defineProperty(window, 'sessionStorage', {
      configurable: true,
      get() { throw new DOMException('blocked', 'SecurityError'); },
    });
    const namespaceId = beginListViewSession('user-a', true);
    const scope = { ownerId: 'user-a', namespaceId, projectId: 'project-a', list: 'concepts' as const };
    writeListViewState(scope, validState);
    expect(readListViewState(scope)).toEqual(validState);
  });

  it('rejects unknown controls and clamps the full filtered result without mutating it', () => {
    expect(normalizeListViewState(
      { ...validState, filters: { status: 'future-state', dates: 'all' }, order: 'future-order', page: 0 },
      { search: '', filters: { status: 'all', dates: 'all' }, order: 'updated-desc', page: 1 },
      { filters: { status: ['all', 'published', 'draft', 'missing'], dates: ['all', 'dated', 'undated'] }, orders: ['updated-desc', 'title'] },
    )).toEqual({ search: 'saved query', filters: { status: 'all', dates: 'all' }, order: 'updated-desc', page: 1 });

    const items = Array.from({ length: 25 }, (_, index) => index);
    expect(paginate(items, 1).items).toEqual(items.slice(0, 12));
    expect(paginate(items, 2).items).toEqual(items.slice(12, 24));
    expect(paginate(items, 99).page).toBe(3);
    const traversed = [1, 2, 3].flatMap((page) => paginate(items, page).items);
    expect(traversed).toEqual(items);
    expect(new Set(traversed).size).toBe(items.length);
    expect(paginate([], 4)).toEqual({ page: 1, pageCount: 0, items: [] });
    expect(items).toHaveLength(25);
  });

  it('writes a versioned envelope and ignores unknown persisted schemas', () => {
    const namespaceId = beginListViewSession('user-a', false);
    const scope = { ownerId: 'user-a', namespaceId, projectId: 'project-a', list: 'concepts' as const };
    writeListViewState(scope, validState);
    const key = `llm-wiki:list-state:v1:${namespaceId}:user-a:project-a:concepts`;
    expect(JSON.parse(window.sessionStorage.getItem(key) ?? 'null')).toEqual({ schemaVersion: 1, state: validState });
    const nextNamespace = beginListViewSession('user-a', false);
    const nextKey = `llm-wiki:list-state:v1:${nextNamespace}:user-a:project-a:concepts`;
    const nextScope = { ...scope, namespaceId: nextNamespace };
    window.sessionStorage.setItem(nextKey, JSON.stringify({ schemaVersion: 99, state: validState }));
    expect(readListViewState(nextScope)).toBeNull();
  });

  it('invalidates the active namespace and its saved records on logout', () => {
    const namespaceId = beginListViewSession('user-a', true);
    const scope = { ownerId: 'user-a', namespaceId, projectId: 'project-a', list: 'concepts' as const };
    writeListViewState(scope, validState);
    clearListViewSession('user-a', namespaceId);
    expect(readListViewState(scope)).toBeNull();
    expect(beginListViewSession('user-a', true)).not.toBe(namespaceId);
  });
});
