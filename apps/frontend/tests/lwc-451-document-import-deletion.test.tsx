import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { DocumentImportList, DocumentImportRecord } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  search: 'conversion_status=all',
  getDocumentImports: vi.fn(),
  deleteDocumentImport: vi.fn(),
  refreshNavCounts: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getDocumentImports: mocks.getDocumentImports,
  deleteDocumentImport: mocks.deleteDocumentImport,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ refreshNavCounts: mocks.refreshNavCounts }),
}));

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string, params?: Record<string, string | number>) => key === 'Source.documentImport.pipelineState' ? `Pipeline ${params?.state}` : key }) }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams(mocks.search) }));

import { DocumentImportsPanel, documentImportPipelineStatusKey } from '@/components/DocumentImportsPanel';

const failedRecord = (deletionState: DocumentImportRecord['deletion_state']): DocumentImportRecord => ({
  document_id: 'document-1',
  original_filename: 'report.docx',
  media_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  bytes: 123,
  sha256: 'digest',
  created_at: '2026-10-30T00:00:00Z',
  updated_at: '2026-10-30T00:00:00Z',
  revision: 4,
  converter_version: '0.1.9',
  conversion_state: 'failed',
  source_state: 'not_started',
  failure: { stage: 'conversion', class: 'worker_unavailable', safe_message_key: 'worker_unavailable', retryable: false },
  deletion_state: deletionState,
});

const pipelineRecord = (status: string): DocumentImportRecord => ({
  ...failedRecord('present'),
  conversion_state: 'succeeded',
  source_state: 'saved',
  failure: null,
  source_ref: { raw_path: 'documents/report.md', sha256: 'source-digest', object_generation: 1, source_id: 'source-1' },
  pipeline_ref: { status, intent_id: 'intent-1', execution_id: 'execution-1', content_generation: '4' },
});

const list = (items: DocumentImportRecord[]): DocumentImportList => ({
  items,
  snapshot_revision: 4,
  failed_count: items.length,
  latest_failure_sequence: 1,
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

let intervals: Map<number, () => void>;

beforeEach(() => {
  mocks.search = 'conversion_status=all';
  window.localStorage.setItem('locale', 'en');
  intervals = new Map();
  let nextIntervalId = 0;
  const nativeSetInterval = globalThis.setInterval;
  const nativeClearInterval = globalThis.clearInterval;
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: TimerHandler, timeout?: number) => {
    if (timeout !== 5000) return nativeSetInterval(callback, timeout);
    const id = ++nextIntervalId;
    if (typeof callback === 'function') intervals.set(id, callback as () => void);
    return id as unknown as ReturnType<typeof setInterval>;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => {
    if (!intervals.delete(id)) nativeClearInterval(id);
  }) as typeof clearInterval);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  mocks.refreshNavCounts.mockResolvedValue(undefined);
  mocks.deleteDocumentImport.mockResolvedValue({ status: 'accepted' });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.resetAllMocks();
});

async function tickPendingPoll() {
  const callback = [...intervals.values()][0];
  expect(callback).toBeDefined();
  await act(async () => {
    callback?.();
    await Promise.resolve();
  });
}

async function beginDelete() {
  render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('report.docx');
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.delete' }));
  await screen.findByText('Source.documentImport.deleting');
  await waitFor(() => expect(intervals.size).toBe(1));
}

it('polls 202 deleting through delayed cleanup, then removes the row and refreshes the project badge once terminal', async () => {
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present')]))
    .mockResolvedValueOnce(list([failedRecord('deleting')]))
    .mockResolvedValueOnce(list([failedRecord('deleting')]))
    .mockResolvedValueOnce(list([]));

  await beginDelete();
  expect(mocks.deleteDocumentImport).toHaveBeenCalledTimes(1);
  expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(1);

  await tickPendingPoll();
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(3));
  expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(1);
  expect(screen.getByRole('button', { name: 'Source.documentImport.delete' }).hasAttribute('disabled')).toBe(true);
  expect(mocks.deleteDocumentImport).toHaveBeenCalledTimes(1);

  await tickPendingPoll();
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(4));
  await waitFor(() => expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(2));
  expect(screen.queryByText('report.docx')).toBeNull();
  expect(mocks.deleteDocumentImport).toHaveBeenCalledTimes(1);
  expect(intervals.size).toBe(0);
});

it('stops polling and refreshes the project badge when cleanup ends in delete_failed', async () => {
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present')]))
    .mockResolvedValueOnce(list([failedRecord('deleting')]))
    .mockResolvedValueOnce(list([failedRecord('delete_failed')]));

  await beginDelete();
  await tickPendingPoll();

  await screen.findByText('Source.documentImport.deleteFailed');
  await waitFor(() => expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(2));
  expect(mocks.deleteDocumentImport).toHaveBeenCalledTimes(1);
  expect(intervals.size).toBe(0);
});

it('cancels the deleting-state poll when the panel unmounts', async () => {
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present')]))
    .mockResolvedValueOnce(list([failedRecord('deleting')]));
  const { unmount } = render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('report.docx');
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.delete' }));
  await screen.findByText('Source.documentImport.deleting');
  await waitFor(() => expect(intervals.size).toBe(1));

  unmount();
  expect(intervals.size).toBe(0);
  expect(mocks.getDocumentImports).toHaveBeenCalledTimes(2);
});

it('does not let a late delete refresh overwrite a newer same-scope poll', async () => {
  const deleteRefresh = deferred<DocumentImportList>();
  const latestPoll = deferred<DocumentImportList>();
  const runningPipeline = { ...pipelineRecord('running'), document_id: 'pipeline-document-1', original_filename: 'pipeline.docx' };
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present'), runningPipeline]))
    .mockReturnValueOnce(deleteRefresh.promise)
    .mockReturnValueOnce(latestPoll.promise);

  render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('report.docx');
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.delete' }));
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(2));

  await tickPendingPoll();
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(3));
  await act(async () => latestPoll.resolve(list([{ ...runningPipeline, pipeline_ref: { status: 'succeeded', intent_id: 'intent-1', execution_id: 'execution-1', content_generation: '4' } }])));
  await waitFor(() => expect(screen.queryByText('report.docx')).toBeNull());

  await act(async () => deleteRefresh.resolve(list([failedRecord('present'), runningPipeline])));
  expect(screen.queryByText('report.docx')).toBeNull();
  expect(mocks.refreshNavCounts).toHaveBeenCalledTimes(1);
  expect(mocks.deleteDocumentImport).toHaveBeenCalledTimes(1);
});

it('does not let a retired delete continuation invalidate a different filter list', async () => {
  const deleting = deferred<{ status: string }>();
  const failedList = deferred<DocumentImportList>();
  const retiredRefresh = deferred<DocumentImportList>();
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present')]))
    .mockReturnValueOnce(failedList.promise)
    .mockReturnValueOnce(retiredRefresh.promise);
  mocks.deleteDocumentImport.mockReturnValueOnce(deleting.promise);

  const view = render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('report.docx');
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.delete' }));
  mocks.search = 'conversion_status=failed';
  view.rerender(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(2));

  await act(async () => deleting.resolve({ status: 'accepted' }));
  expect(mocks.getDocumentImports).toHaveBeenCalledTimes(2);
  await act(async () => failedList.resolve(list([])));

  expect(await screen.findByText('Source.documentImport.filterEmpty')).toBeTruthy();
  expect(mocks.getDocumentImports).toHaveBeenCalledTimes(2);
});

it('does not revive an old all-filter owner after all→failed→all', async () => {
  const deleting = deferred<{ status: string }>();
  const currentAll = deferred<DocumentImportList>();
  const retiredRefresh = deferred<DocumentImportList>();
  const currentRecord = { ...failedRecord('present'), document_id: 'current-document', original_filename: 'current.docx' };
  mocks.getDocumentImports
    .mockResolvedValueOnce(list([failedRecord('present')]))
    .mockResolvedValueOnce(list([]))
    .mockReturnValueOnce(currentAll.promise)
    .mockReturnValueOnce(retiredRefresh.promise);
  mocks.deleteDocumentImport.mockReturnValueOnce(deleting.promise);

  const view = render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('report.docx');
  fireEvent.click(screen.getByRole('button', { name: 'Source.documentImport.delete' }));
  mocks.search = 'conversion_status=failed';
  view.rerender(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await screen.findByText('Source.documentImport.filterEmpty');
  mocks.search = 'conversion_status=all';
  view.rerender(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
  await waitFor(() => expect(mocks.getDocumentImports).toHaveBeenCalledTimes(3));

  await act(async () => deleting.resolve({ status: 'accepted' }));
  expect(mocks.getDocumentImports).toHaveBeenCalledTimes(3);
  await act(async () => currentAll.resolve(list([currentRecord])));

  expect(await screen.findByText('current.docx')).toBeTruthy();
  expect(mocks.getDocumentImports).toHaveBeenCalledTimes(3);
});

const pipelineStatusText = (status: string) => `Pipeline ${documentImportPipelineStatusKey(status)}`;

for (const terminal of ['failed', 'published']) {
  it(`tracks translated pending→running→${terminal} status without changing successful conversion`, async () => {
    mocks.getDocumentImports
      .mockResolvedValueOnce(list([pipelineRecord('pending')]))
      .mockResolvedValueOnce(list([pipelineRecord('running')]))
      .mockResolvedValueOnce(list([pipelineRecord(terminal)]));

    render(<DocumentImportsPanel projectId="project-1" sourcePaths={[]} />);
    await screen.findByText(pipelineStatusText('pending'));
    await waitFor(() => expect(intervals.size).toBe(1));

    await tickPendingPoll();
    await screen.findByText(pipelineStatusText('running'));
    expect(screen.getByText('Source.documentImport.sourceSaved')).toBeDefined();

    await tickPendingPoll();
    await screen.findByText(pipelineStatusText(terminal));
    expect(screen.getByText('Source.documentImport.sourceSaved')).toBeDefined();
    expect(screen.queryByText('Source.documentImport.failure.worker_unavailable')).toBeNull();
    expect(intervals.size).toBe(0);
  });
}
