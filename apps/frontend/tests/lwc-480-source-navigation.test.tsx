import React, { useEffect, useSyncExternalStore } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { DocumentImportList, DocumentImportRecord, SourceListItem, WikiEntry } from '@/lib/api';
import { NavigationBlockerProvider, useNavigationBlocker } from '@/components/NavigationBlocker';
import { DetailClient } from '@/components/DetailClient';
import { SourceListClient } from '@/components/SourceListClient';
import { RouterContext } from 'next/dist/shared/lib/router-context.shared-runtime';

const navigation = vi.hoisted(() => ({
  pathname: '/sources',
  search: '',
  listeners: new Set<() => void>(),
  sync: (): void => {},
  subscribe: (listener: () => void): (() => void) => () => { navigation.listeners.delete(listener); },
  router: {
    push: (href: string): void => { void href; },
    replace: (href: string): void => { void href; },
    back: (): void => {},
    forward: (): void => {},
    refresh: (): void => {},
    prefetch: (href: string): void => { void href; },
  },
}));

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-a' },
  token: 'in-memory-test-token',
  user: { id: 'user-a' },
  hydrated: true,
  listViewSessionId: 'list-session-a',
  listViewGeneration: 1,
  isListViewSessionCurrent: () => true,
  sessionIdentityEpoch: 1,
  sessionEpoch: 1,
  projects: [{ id: 'project-a' }],
  projectsLoading: false,
  selectProject: vi.fn(),
  refreshNavCounts: vi.fn(async () => true),
}));

const mocks = vi.hoisted(() => ({
  getSources: vi.fn<() => Promise<SourceListItem[]>>(),
  getDocumentImports: vi.fn<() => Promise<DocumentImportList>>(),
  getDocumentImport: vi.fn<() => Promise<DocumentImportRecord>>(),
  getConcepts: vi.fn<() => Promise<WikiEntry[]>>(),
  getSource: vi.fn<() => Promise<WikiEntry>>(),
  refreshNavCounts: vi.fn(async () => true),
}));
const translate = vi.hoisted(() => (key: string) => key);

vi.mock('next/navigation', async () => {
  const ReactModule = await vi.importActual<typeof import('react')>('react');
  return {
    usePathname: () => ReactModule.useSyncExternalStore(navigation.subscribe, () => navigation.pathname, () => navigation.pathname),
    useSearchParams: () => new URLSearchParams(ReactModule.useSyncExternalStore(navigation.subscribe, () => navigation.search, () => navigation.search)),
    useRouter: () => navigation.router,
  };
});
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => workspace,
}));
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>();
  return {
    ...actual,
    getSources: mocks.getSources,
    getDocumentImports: mocks.getDocumentImports,
    getDocumentImport: mocks.getDocumentImport,
    getConcepts: mocks.getConcepts,
    getSource: mocks.getSource,
  };
});
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: translate }) }));
vi.mock('@/components/AddMaterialAction', () => ({ AddMaterialAction: () => null }));

const importRecord: DocumentImportRecord = {
  document_id: 'document-a',
  original_filename: 'source-13.docx',
  media_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  bytes: 12,
  sha256: 'a'.repeat(64),
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
  revision: 1,
  converter_version: 'fixture',
  conversion_state: 'succeeded',
  source_state: 'saved',
  source_ref: { raw_path: 'raw/source-13.md', sha256: 'b'.repeat(64), object_generation: 1 },
  deletion_state: 'present',
};

const sources: SourceListItem[] = Array.from({ length: 25 }, (_, index) => ({
  id: `source-${index + 1}`,
  slug: `source-${String(index + 1).padStart(2, '0')}`,
  title: `Source ${String(index + 1).padStart(2, '0')}`,
  rawPath: `raw/source-${index + 1}.md`,
  lifecycle: 'synced',
  annotationPresent: false,
  annotationAllowed: true,
  raw: {},
}));

const sourceDetail: WikiEntry = { id: 'source-25', slug: 'source-25', title: 'Source 25', raw: {} };
const blockerOwner = {};
const pagesRouterContextValue = {
  basePath: '',
  pathname: '/sources',
  route: '/sources',
  query: {} as Record<string, string>,
  asPath: '/sources',
  push: (href: string) => navigation.router.push(href),
  replace: (href: string) => navigation.router.replace(href),
  reload: () => undefined,
  back: () => navigation.router.back(),
  forward: () => navigation.router.forward(),
  prefetch: (href: string) => navigation.router.prefetch(href),
  beforePopState: () => undefined,
  isFallback: false,
  isReady: true,
  isPreview: false,
};

function BlockerProbe({ blocked }: { blocked: boolean }) {
  const blocker = useNavigationBlocker();
  useEffect(() => {
    blocker.setBlocked(blocked, blockerOwner);
    return () => blocker.setBlocked(false, blockerOwner);
  }, [blocked, blocker]);
  return null;
}

function RouteHarness({ blocked = false }: { blocked?: boolean }) {
  const pathname = useSyncExternalStore(navigation.subscribe, () => navigation.pathname, () => navigation.pathname);
  return (
    <RouterContext.Provider value={pagesRouterContextValue as never}>
      <NavigationBlockerProvider>
        <BlockerProbe blocked={blocked} />
        {pathname === '/sources'
          ? <SourceListClient />
          : pathname.startsWith('/sources/')
            ? <DetailClient slug="source-25" entryType="source" backHref="/sources" load={mocks.getSource} />
            : null}
      </NavigationBlockerProvider>
    </RouterContext.Provider>
  );
}

function refreshRouteFromLocation() {
  const url = new URL(window.location.href);
  navigation.pathname = url.pathname;
  navigation.search = url.search;
  [...navigation.listeners].forEach((listener) => listener());
}

let originalPushState: History['pushState'];
let originalReplaceState: History['replaceState'];
let popstateListener: (() => void) | null = null;
let scrollIntoViewDescriptor: PropertyDescriptor | undefined;
let defaultRafDescriptor: PropertyDescriptor | undefined;
let defaultCancelRafDescriptor: PropertyDescriptor | undefined;
let testFrames: Map<number, FrameRequestCallback>;

function renderRoute(blocked = false) {
  return render(<RouteHarness blocked={blocked} />);
}

async function waitForFocusedImportRow() {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      const pending = [...testFrames.entries()];
      testFrames.clear();
      pending.forEach(([, callback]) => callback(0));
      await Promise.resolve();
    });
    const text = screen.queryAllByText('source-13.docx').find((element) => element.closest('.lwc-data-list'));
    const row = text?.closest('[tabindex="-1"]');
    if (row && document.activeElement === row) return;
  }
  const text = screen.queryAllByText('source-13.docx').find((element) => element.closest('.lwc-data-list'));
  const row = text?.closest('[tabindex="-1"]');
  throw new Error(`Imported source focus failed (text=${Boolean(text)}, row=${Boolean(row)}, active=${document.activeElement?.tagName ?? 'none'}).`);
}

beforeEach(() => {
  vi.useRealTimers();
  window.localStorage.clear();
  window.sessionStorage.clear();
  workspace.listViewSessionId = `list-session-${Math.random().toString(36).slice(2)}`;
  navigation.listeners.clear();
  window.history.replaceState({}, '', '/sources?document_id=document-a');
  refreshRouteFromLocation();
  navigation.subscribe = (listener) => {
    navigation.listeners.add(listener);
    return () => { navigation.listeners.delete(listener); };
  };
  navigation.sync = refreshRouteFromLocation;
  navigation.router = {
    push: (href) => window.history.pushState(window.history.state, '', href),
    replace: (href) => window.history.replaceState(window.history.state, '', href),
    back: () => window.history.back(),
    forward: () => window.history.forward(),
    refresh: () => undefined,
    prefetch: () => undefined,
  };
  originalPushState = window.history.pushState;
  originalReplaceState = window.history.replaceState;
  defaultRafDescriptor = Object.getOwnPropertyDescriptor(window, 'requestAnimationFrame');
  defaultCancelRafDescriptor = Object.getOwnPropertyDescriptor(window, 'cancelAnimationFrame');
  const frames = new Set<number>();
  testFrames = new Map();
  let nextFrame = 0;
  Object.defineProperty(window, 'requestAnimationFrame', {
    configurable: true,
    value: (callback: FrameRequestCallback) => {
      const id = ++nextFrame;
      frames.add(id);
      testFrames.set(id, callback);
      return id;
    },
  });
  Object.defineProperty(window, 'cancelAnimationFrame', {
    configurable: true,
    value: (id: number) => { frames.delete(id); testFrames.delete(id); },
  });
  vi.spyOn(window.history, 'pushState').mockImplementation(function pushState(this: History, state, title, url) {
    originalPushState.call(this, state, title, url);
    refreshRouteFromLocation();
  });
  vi.spyOn(window.history, 'replaceState').mockImplementation(function replaceState(this: History, state, title, url) {
    originalReplaceState.call(this, state, title, url);
    refreshRouteFromLocation();
  });
  popstateListener = refreshRouteFromLocation;
  window.addEventListener('popstate', popstateListener);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  scrollIntoViewDescriptor = Object.getOwnPropertyDescriptor(window.HTMLElement.prototype, 'scrollIntoView');
  Object.defineProperty(window.HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
  mocks.getSources.mockResolvedValue(sources);
  mocks.getDocumentImports.mockResolvedValue({ items: [importRecord], snapshot_revision: 1, failed_count: 0, latest_failure_sequence: 0 });
  mocks.getDocumentImport.mockResolvedValue(importRecord);
  mocks.getConcepts.mockResolvedValue([]);
  mocks.getSource.mockResolvedValue(sourceDetail);
  mocks.refreshNavCounts.mockResolvedValue(true);
});

afterEach(() => {
  cleanup();
  window.removeEventListener('popstate', popstateListener!);
  popstateListener = null;
  if (defaultRafDescriptor) Object.defineProperty(window, 'requestAnimationFrame', defaultRafDescriptor);
  else Reflect.deleteProperty(window, 'requestAnimationFrame');
  if (defaultCancelRafDescriptor) Object.defineProperty(window, 'cancelAnimationFrame', defaultCancelRafDescriptor);
  else Reflect.deleteProperty(window, 'cancelAnimationFrame');
  if (scrollIntoViewDescriptor) Object.defineProperty(window.HTMLElement.prototype, 'scrollIntoView', scrollIntoViewDescriptor);
  else Reflect.deleteProperty(window.HTMLElement.prototype, 'scrollIntoView');
  defaultRafDescriptor = undefined;
  defaultCancelRafDescriptor = undefined;
  testFrames = new Map();
  scrollIntoViewDescriptor = undefined;
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('LWC-480 actual source import and history integration', () => {
  it('keeps page 3 after page-2 focus, detail navigation, browser Back and explicit Back', async () => {
    renderRoute();
    await waitForFocusedImportRow();
    const firstEntryId = window.history.state.__lwcListEntryId;
    const consumed = window.history.state.__lwcConsumedDocumentFocus;
    expect(consumed?.entryId).toBe(firstEntryId);
    expect(consumed?.documentId).toBe('document-a');

    fireEvent.click(screen.getByRole('button', { name: 'List.nextPage' }));
    await screen.findByRole('link', { name: 'Source 25' });
    expect(document.querySelector('[aria-current="page"]')?.textContent).toBe('3');

    fireEvent.click(screen.getByRole('link', { name: 'Source 25' }));
    await screen.findByRole('link', { name: 'Detail.backTo' });
    await act(async () => {
      window.history.back();
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    await screen.findByRole('link', { name: 'Source 25' });
    expect(navigation.pathname).toBe('/sources');
    expect(navigation.search).toBe('?document_id=document-a');
    expect(window.history.state.__lwcListEntryId).toBe(firstEntryId);
    expect(window.history.state.__lwcConsumedDocumentFocus).toEqual(consumed);
    expect(document.querySelector('[aria-current="page"]')?.textContent).toBe('3');
    expect(document.activeElement?.closest('.lwc-data-list')).toBeNull();

    fireEvent.click(screen.getByRole('link', { name: 'Source 25' }));
    await screen.findByRole('link', { name: 'Detail.backTo' });
    fireEvent.click(screen.getByRole('link', { name: 'Detail.backTo' }));
    await screen.findByRole('link', { name: 'Source 25' });
    expect(navigation.search).toBe('');
    expect(document.querySelector('[aria-current="page"]')?.textContent).toBe('3');
  });

  it('allows a new same-document entry to focus again and leaves no new entry when navigation is declined', async () => {
    const oldEntryId = 'previous-entry';
    const consumedFocus = { entryId: oldEntryId, ownerId: 'user-a', namespaceId: workspace.listViewSessionId, projectId: 'project-a', documentId: 'document-a' };
    window.history.replaceState({ __lwcListEntryId: oldEntryId, __lwcConsumedDocumentFocus: consumedFocus }, '', '/sources?document_id=document-a');
    refreshRouteFromLocation();
    const oldHistoryLength = window.history.length;
    const view = renderRoute();
    fireEvent.click(await screen.findByRole('button', { name: 'Source.documentImport.showHistory' }));
    await screen.findByRole('link', { name: 'Source.documentImport.viewSource' });
    view.rerender(<RouteHarness blocked />);
    vi.mocked(window.confirm).mockReturnValue(false);
    fireEvent.click(screen.getByRole('link', { name: 'Source.documentImport.viewSource' }));
    expect(window.location.search).toBe('?document_id=document-a');
    expect(window.history.state.__lwcListEntryId).toBe(oldEntryId);
    expect(window.history.length).toBe(oldHistoryLength);

    view.rerender(<RouteHarness />);
    fireEvent.click(screen.getByRole('link', { name: 'Source.documentImport.viewSource' }));
    await waitForFocusedImportRow();
    expect(window.history.state.__lwcListEntryId).not.toBe(oldEntryId);
    expect(window.history.state.__lwcConsumedDocumentFocus?.entryId).toBe(window.history.state.__lwcListEntryId);
    expect(window.location.search).toBe('?document_id=document-a');
  });

  it('does not consume excluded targets or let a canceled delayed focus move the page', async () => {
    const callbacks = new Map<number, FrameRequestCallback>();
    const canceled = new Set<number>();
    let nextFrame = 0;
    Object.defineProperty(window, 'requestAnimationFrame', {
      configurable: true,
      value: (callback: FrameRequestCallback) => {
        const id = ++nextFrame;
        callbacks.set(id, callback);
        return id;
      },
    });
    Object.defineProperty(window, 'cancelAnimationFrame', {
      configurable: true,
      value: (id: number) => { canceled.add(id); },
    });

    renderRoute();
    await screen.findByRole('link', { name: 'source-13.docx' });
    const staleFrame = Math.max(...callbacks.keys());
    expect(canceled.has(staleFrame)).toBe(false);

    fireEvent.change(screen.getByLabelText('List.searchPlaceholder'), { target: { value: 'Source 01' } });
    await screen.findByRole('link', { name: 'Source 01' });
    expect(document.querySelector('[aria-current="page"]')).toBeNull();
    expect(window.history.state.__lwcConsumedDocumentFocus).toBeUndefined();
    act(() => callbacks.get(staleFrame)?.(0));
    expect(document.querySelector('[aria-current="page"]')).toBeNull();
    expect(window.history.state.__lwcConsumedDocumentFocus).toBeUndefined();
    expect(screen.getByLabelText('List.searchPlaceholder')).toHaveProperty('value', 'Source 01');

    fireEvent.change(screen.getByLabelText('List.searchPlaceholder'), { target: { value: 'Source' } });
    await screen.findByRole('link', { name: 'source-13.docx' });
    const currentFrame = Math.max(...callbacks.keys());
    act(() => callbacks.get(currentFrame)?.(0));
    await waitForFocusedImportRow();
    expect(window.history.state.__lwcConsumedDocumentFocus?.documentId).toBe('document-a');
  });
});
