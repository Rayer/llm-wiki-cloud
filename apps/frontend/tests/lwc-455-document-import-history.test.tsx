import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { DocumentImportList, DocumentImportRecord } from '@/lib/api';

const state = vi.hoisted(() => ({
  search: 'conversion_status=failed',
  getDocumentImports: vi.fn(),
  getDocumentImport: vi.fn(),
  deleteDocumentImport: vi.fn(),
  downloadDocumentImportOriginal: vi.fn(),
  refreshNavCounts: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getDocumentImports: state.getDocumentImports,
  getDocumentImport: state.getDocumentImport,
  deleteDocumentImport: state.deleteDocumentImport,
  downloadDocumentImportOriginal: state.downloadDocumentImportOriginal,
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ refreshNavCounts: state.refreshNavCounts }) }));
vi.mock('@/components/NavigationBlocker', () => ({ NavigationLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(state.search) }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string, params?: Record<string, string | number>) => {
  if (key === 'Source.documentImport.pipelineStatus.disabled') return 'Automatic processing is disabled.';
  if (key === 'Source.documentImport.pipelineState') return `Source pipeline: ${params?.state}`;
  return key;
} }) }));

import { DocumentImportsPanel, documentImportPipelineStatusKey } from '@/components/DocumentImportsPanel';

const savedImport = {
  document_id: 'synthetic-document-1',
  original_filename: 'meeting-notes.docx',
  media_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  bytes: 1024,
  sha256: 'a'.repeat(64),
  created_at: '2026-10-31T00:00:00Z',
  updated_at: '2026-10-31T00:00:00Z',
  revision: 2,
  converter_version: 'synthetic-converter',
  conversion_state: 'succeeded',
  source_state: 'saved',
  source_ref: { raw_path: 'raw/document-synthetic-document-1.md', sha256: 'b'.repeat(64), object_generation: 1 },
  pipeline_ref: { status: 'not_started_disabled' },
  deletion_state: 'present',
};

beforeEach(() => {
  state.search = 'conversion_status=failed';
  state.getDocumentImports.mockReset();
  state.getDocumentImport.mockReset();
  state.deleteDocumentImport.mockReset();
  state.downloadDocumentImportOriginal.mockReset();
  state.refreshNavCounts.mockReset();
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
});

it('keeps the status filter and history controls when a filter has no matches', async () => {
  state.getDocumentImports.mockResolvedValue({ items: [], snapshot_revision: 1, failed_count: 0, latest_failure_sequence: 0 });
  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);

  expect(await screen.findByText('Source.documentImport.filterEmpty')).toBeTruthy();
  expect(screen.getByRole('combobox', { name: 'Source.documentImport.filterLabel' })).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Source.documentImport.showHistory' })).toBeTruthy();
});

it('keeps saved imports in the history view and links them by their source metadata', async () => {
  state.search = 'conversion_status=succeeded';
  state.getDocumentImports.mockResolvedValue({ items: [savedImport], snapshot_revision: 2, failed_count: 0, latest_failure_sequence: 0 });
  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[savedImport.source_ref.raw_path]} />);

  expect(await screen.findByText('Source.documentImport.hiddenHistoryEmpty')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.showHistory' }));
  expect(await screen.findByRole('heading', { name: 'meeting-notes.docx' })).toBeTruthy();
  expect(screen.getByText('Source pipeline: Automatic processing is disabled.')).toBeTruthy();
  expect(documentImportPipelineStatusKey('not_started_disabled')).toBe('Source.documentImport.pipelineStatus.disabled');
  expect(screen.getByRole('link', { name: 'Source.documentImport.viewSource' }).getAttribute('href'))
    .toBe('/sources?document_id=synthetic-document-1');
});

it('loads a deep-linked document that is not in the current list page', async () => {
  state.search = 'conversion_status=all&document_id=synthetic-document-1';
  state.getDocumentImports.mockResolvedValueOnce(importPage([], 1));
  state.getDocumentImport.mockResolvedValueOnce(savedImport as DocumentImportRecord);

  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);

  expect(await screen.findByRole('heading', { name: 'meeting-notes.docx' })).toBeTruthy();
  expect(state.getDocumentImport).toHaveBeenCalledWith('synthetic-project', 'synthetic-document-1');
});

it('keeps the deep-link record when GET wins before an incomplete first page and still paginates', async () => {
  const callbacks = capturePollingIntervals();
  const refreshedFirstPage = deferred<DocumentImportList>();
  const requestedGet = deferred<DocumentImportRecord>();
  const oldFirstPageRecord = {
    ...savedImport,
    document_id: 'old-first-page',
    original_filename: 'old-first-page.docx',
    conversion_state: 'converting',
    source_state: 'not_started',
    source_ref: undefined,
    pipeline_ref: { status: 'running' },
  } as DocumentImportRecord;
  const currentFirstPageRecord = { ...oldFirstPageRecord, document_id: 'current-first-page', original_filename: 'current-first-page.docx' };
  const pageTwoRecord = { ...savedImport, document_id: 'page-two', original_filename: 'page-two.docx' } as DocumentImportRecord;
  const requestedRecord = { ...savedImport, document_id: 'requested-document', original_filename: 'requested.docx' } as DocumentImportRecord;
  state.search = 'conversion_status=all';
  state.getDocumentImports
    .mockResolvedValueOnce(importPage([oldFirstPageRecord], 1, 'initial-cursor'))
    .mockReturnValueOnce(refreshedFirstPage.promise)
    .mockResolvedValueOnce(importPage([pageTwoRecord], 3));
  state.getDocumentImport.mockReturnValueOnce(requestedGet.promise);

  const view = render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  expect(await screen.findByRole('heading', { name: 'old-first-page.docx' })).toBeTruthy();
  await waitFor(() => expect(callbacks.length).toBe(1));

  state.search = 'conversion_status=all&document_id=requested-document';
  view.rerender(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  await waitFor(() => expect(state.getDocumentImport).toHaveBeenCalledWith('synthetic-project', 'requested-document'));
  await act(async () => requestedGet.resolve(requestedRecord));
  expect(await screen.findByRole('heading', { name: 'requested.docx' })).toBeTruthy();

  await act(async () => callbacks[0]());
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(2));
  await act(async () => refreshedFirstPage.resolve(importPage([currentFirstPageRecord], 2, 'cursor-page-two')));
  expect(await screen.findByRole('heading', { name: 'current-first-page.docx' })).toBeTruthy();
  expect(screen.getByRole('heading', { name: 'requested.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'old-first-page.docx' })).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.loadMore' }));
  expect(await screen.findByRole('heading', { name: 'page-two.docx' })).toBeTruthy();
  expect(state.getDocumentImports).toHaveBeenLastCalledWith('synthetic-project', 'all', 'cursor-page-two');
  expect(screen.getByRole('heading', { name: 'requested.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'old-first-page.docx' })).toBeNull();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

const importPage = (items: DocumentImportRecord[], snapshot_revision: number, next_cursor?: string): DocumentImportList => ({
  items, snapshot_revision, failed_count: items.filter((item) => item.conversion_state === 'failed').length,
  latest_failure_sequence: 0, ...(next_cursor ? { next_cursor } : {}),
});

function capturePollingIntervals() {
  const callbacks: Array<() => void> = [];
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: TimerHandler, delay?: number) => {
    if (delay === 5000 && typeof callback === 'function') callbacks.push(callback as () => void);
    return 99991 as unknown as ReturnType<typeof setInterval>;
  }) as unknown as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation((() => undefined) as typeof clearInterval);
  return callbacks;
}

it('fences all→failed→all responses by request generation, including the first all response', async () => {
  const firstAll = deferred<ReturnType<typeof importPage>>();
  const staleAll = { ...savedImport, document_id: 'stale-document', original_filename: 'stale-all-response.docx', source_state: 'not_started', source_ref: undefined };
  const currentAll = { ...savedImport, document_id: 'current-document', original_filename: 'current-all-response.docx', source_state: 'not_started', source_ref: undefined };
  state.search = 'conversion_status=all';
  state.getDocumentImports
    .mockReturnValueOnce(firstAll.promise)
    .mockResolvedValueOnce(importPage([], 2))
    .mockResolvedValueOnce(importPage([currentAll], 3));
  const view = render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);

  state.search = 'conversion_status=failed';
  view.rerender(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  expect(await screen.findByText('Source.documentImport.filterEmpty')).toBeTruthy();
  state.search = 'conversion_status=all';
  view.rerender(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  expect(await screen.findByRole('heading', { name: 'current-all-response.docx' })).toBeTruthy();

  await act(async () => firstAll.resolve(importPage([staleAll], 1)));
  expect(screen.getByRole('heading', { name: 'current-all-response.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'stale-all-response.docx' })).toBeNull();
});

it('keeps the newest same-scope poll response when an older poll finishes last', async () => {
  const callbacks = capturePollingIntervals();
  const firstPoll = deferred<ReturnType<typeof importPage>>();
  const secondPoll = deferred<ReturnType<typeof importPage>>();
  const pending = { ...savedImport, original_filename: 'pending.docx', conversion_state: 'converting', source_state: 'not_started', source_ref: undefined, pipeline_ref: { status: 'running' } };
  const stale = { ...savedImport, document_id: 'stale-poll', original_filename: 'stale-poll.docx', source_state: 'not_started', source_ref: undefined };
  const latest = { ...savedImport, document_id: 'latest-poll', original_filename: 'latest-poll.docx', source_state: 'not_started', source_ref: undefined };
  state.search = 'conversion_status=all';
  state.getDocumentImports.mockResolvedValueOnce(importPage([pending], 1))
    .mockReturnValueOnce(firstPoll.promise)
    .mockReturnValueOnce(secondPoll.promise);
  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  await screen.findByRole('heading', { name: 'pending.docx' });
  await waitFor(() => expect(callbacks.length).toBe(1));

  await act(async () => { callbacks[0](); callbacks[0](); });
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(3));
  await act(async () => secondPoll.resolve(importPage([latest], 3)));
  expect(await screen.findByRole('heading', { name: 'latest-poll.docx' })).toBeTruthy();
  await act(async () => firstPoll.resolve(importPage([stale], 2)));
  expect(screen.getByRole('heading', { name: 'latest-poll.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'stale-poll.docx' })).toBeNull();
});

it('keeps the latest pagination cursor when a newer poll completes before the older page', async () => {
  const callbacks = capturePollingIntervals();
  const oldPage = deferred<ReturnType<typeof importPage>>();
  const newerPoll = deferred<ReturnType<typeof importPage>>();
  const pending = { ...savedImport, original_filename: 'pending-page.docx', conversion_state: 'converting', source_state: 'not_started', source_ref: undefined, pipeline_ref: { status: 'running' } };
  const stale = { ...savedImport, document_id: 'stale-page', original_filename: 'stale-page.docx', source_state: 'not_started', source_ref: undefined };
  const latest = { ...savedImport, document_id: 'latest-page', original_filename: 'latest-page.docx', source_state: 'not_started', source_ref: undefined };
  state.search = 'conversion_status=all';
  state.getDocumentImports.mockResolvedValueOnce(importPage([pending], 1, 'cursor-old'))
    .mockReturnValueOnce(oldPage.promise)
    .mockReturnValueOnce(newerPoll.promise)
    .mockResolvedValueOnce(importPage([], 4));
  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} />);
  await screen.findByRole('heading', { name: 'pending-page.docx' });
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.loadMore' }));
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(callbacks.length).toBe(1));
  await act(async () => callbacks[0]());
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(3));
  await act(async () => newerPoll.resolve(importPage([latest], 3, 'cursor-new')));
  await screen.findByRole('heading', { name: 'latest-page.docx' });
  await act(async () => oldPage.resolve(importPage([stale], 2, 'stale-cursor')));
  expect(screen.getByRole('heading', { name: 'latest-page.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'stale-page.docx' })).toBeNull();

  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.loadMore' }));
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(4));
  expect(state.getDocumentImports).toHaveBeenLastCalledWith('synthetic-project', 'all', 'cursor-new');
});

it('keeps a newer Pipeline projection over an older-revision document GET', async () => {
  const callbacks = capturePollingIntervals();
  const olderGet = deferred<DocumentImportRecord>();
  const latestPoll = deferred<DocumentImportList>();
  const listedRecord = {
    ...savedImport,
    revision: 8,
    original_filename: 'current-meeting.docx',
    source_ref: { ...savedImport.source_ref, raw_path: 'raw/current.md' },
    pipeline_ref: { status: 'running', intent_id: 'intent-current', execution_id: 'execution-current' },
  } as DocumentImportRecord;
  const projectedRecord: DocumentImportRecord = {
    ...listedRecord,
    pipeline_ref: { ...listedRecord.pipeline_ref!, status: 'published', content_generation: 'generation-current' },
  };
  const olderRevisionRecord: DocumentImportRecord = {
    ...listedRecord,
    revision: 7,
    original_filename: 'old-meeting.docx',
    source_ref: { raw_path: 'raw/old.md', sha256: 'c'.repeat(64), object_generation: 1 },
    pipeline_ref: { status: 'running', intent_id: 'intent-old', execution_id: 'execution-old' },
  };
  state.search = 'conversion_status=all&document_id=synthetic-document-1';
  state.getDocumentImports
    .mockResolvedValueOnce(importPage([listedRecord], 8))
    .mockReturnValueOnce(latestPoll.promise);
  state.getDocumentImport.mockReturnValueOnce(olderGet.promise);
  const observed: DocumentImportRecord[][] = [];
  const onRecordsChange = (records: DocumentImportRecord[]) => observed.push(records);

  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} onRecordsChange={onRecordsChange} />);
  expect(await screen.findByRole('heading', { name: 'current-meeting.docx' })).toBeTruthy();
  await waitFor(() => expect(state.getDocumentImport).toHaveBeenCalledWith('synthetic-project', 'synthetic-document-1'));
  await waitFor(() => expect(callbacks.length).toBe(1));

  await act(async () => callbacks[0]());
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(2));
  await act(async () => latestPoll.resolve(importPage([projectedRecord], 8)));
  expect(await screen.findByText('Source pipeline: Source.documentImport.pipelineStatus.succeeded')).toBeTruthy();

  await act(async () => olderGet.resolve(olderRevisionRecord));
  expect(screen.getByRole('heading', { name: 'current-meeting.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'old-meeting.docx' })).toBeNull();
  expect(observed.at(-1)?.[0].original_filename).toBe('current-meeting.docx');
  expect(observed.at(-1)?.[0].source_ref?.raw_path).toBe('raw/current.md');
  expect(observed.at(-1)?.[0].pipeline_ref?.status).toBe('published');
});

it('keeps a same-revision published projection over a late running document GET', async () => {
  const callbacks = capturePollingIntervals();
  const olderGet = deferred<DocumentImportRecord>();
  const latestPoll = deferred<DocumentImportList>();
  const listedRecord = {
    ...savedImport,
    revision: 8,
    original_filename: 'current-meeting.docx',
    source_ref: { ...savedImport.source_ref, raw_path: 'raw/current.md' },
    pipeline_ref: { status: 'running', intent_id: 'intent-current', execution_id: 'execution-current' },
  } as DocumentImportRecord;
  const publishedRecord: DocumentImportRecord = {
    ...listedRecord,
    pipeline_ref: { ...listedRecord.pipeline_ref!, status: 'published', content_generation: 'generation-current' },
  };
  const lateRunningRecord: DocumentImportRecord = {
    ...listedRecord,
    original_filename: 'late-running-projection.docx',
    source_ref: { raw_path: 'raw/older-projection.md', sha256: 'c'.repeat(64), object_generation: 1 },
    pipeline_ref: { status: 'running', intent_id: 'intent-current', execution_id: 'execution-current' },
  };
  state.search = 'conversion_status=all&document_id=synthetic-document-1';
  state.getDocumentImports
    .mockResolvedValueOnce(importPage([listedRecord], 8))
    .mockReturnValueOnce(latestPoll.promise);
  state.getDocumentImport.mockReturnValueOnce(olderGet.promise);
  const observed: DocumentImportRecord[][] = [];
  const onRecordsChange = (records: DocumentImportRecord[]) => observed.push(records);

  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} onRecordsChange={onRecordsChange} />);
  expect(await screen.findByRole('heading', { name: 'current-meeting.docx' })).toBeTruthy();
  await waitFor(() => expect(state.getDocumentImport).toHaveBeenCalledWith('synthetic-project', 'synthetic-document-1'));
  await waitFor(() => expect(callbacks.length).toBe(1));

  await act(async () => callbacks[0]());
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(2));
  await act(async () => latestPoll.resolve(importPage([publishedRecord], 8)));
  expect(await screen.findByText('Source pipeline: Source.documentImport.pipelineStatus.succeeded')).toBeTruthy();

  await act(async () => olderGet.resolve(lateRunningRecord));
  expect(screen.getByRole('heading', { name: 'current-meeting.docx' })).toBeTruthy();
  expect(screen.queryByRole('heading', { name: 'late-running-projection.docx' })).toBeNull();
  expect(observed.at(-1)?.[0].revision).toBe(8);
  expect(observed.at(-1)?.[0].source_ref?.raw_path).toBe('raw/current.md');
  expect(observed.at(-1)?.[0].pipeline_ref?.status).toBe('published');
});

it('retains a completed deletion tombstone against a late deep-link GET', async () => {
  const callbacks = capturePollingIntervals();
  const olderGet = deferred<DocumentImportRecord>();
  const deletingRecord = { ...savedImport, conversion_state: 'failed', deletion_state: 'deleting' } as DocumentImportRecord;
  const stalePresentRecord = { ...deletingRecord, deletion_state: 'present' };
  state.search = 'conversion_status=all&document_id=synthetic-document-1';
  state.getDocumentImports
    .mockResolvedValueOnce(importPage([deletingRecord], 4))
    .mockResolvedValueOnce(importPage([], 5));
  state.getDocumentImport.mockReturnValueOnce(olderGet.promise);
  const observed: DocumentImportRecord[][] = [];
  const onRecordsChange = (records: DocumentImportRecord[]) => observed.push(records);

  render(<DocumentImportsPanel projectId="synthetic-project" sourcePaths={[]} onRecordsChange={onRecordsChange} />);
  expect(await screen.findByRole('heading', { name: 'meeting-notes.docx' })).toBeTruthy();
  await waitFor(() => expect(callbacks.length).toBe(1));

  await act(async () => callbacks[0]());
  await waitFor(() => expect(state.getDocumentImports).toHaveBeenCalledTimes(2));
  expect(await screen.findByText('Source.documentImport.hiddenHistoryEmpty')).toBeTruthy();
  expect(observed.at(-1)?.[0].deletion_state).toBe('deleted');

  await act(async () => olderGet.resolve(stalePresentRecord));
  expect(screen.queryByRole('heading', { name: 'meeting-notes.docx' })).toBeNull();
  expect(observed.at(-1)?.[0].deletion_state).toBe('deleted');
});
