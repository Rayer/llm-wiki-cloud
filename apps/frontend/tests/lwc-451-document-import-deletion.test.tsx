import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { DocumentImportList, DocumentImportRecord } from '@/lib/api';

const mocks = vi.hoisted(() => ({
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
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams() }));

import { DocumentImportsPanel } from '@/components/DocumentImportsPanel';

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

let intervals: Map<number, () => void>;

beforeEach(() => {
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

for (const terminal of ['failed', 'published']) {
  it(`tracks Pipeline pending→running→${terminal} without changing successful conversion`, async () => {
    mocks.getDocumentImports
      .mockResolvedValueOnce(list([pipelineRecord('pending')]))
      .mockResolvedValueOnce(list([pipelineRecord('running')]))
      .mockResolvedValueOnce(list([pipelineRecord(terminal)]));

    render(<DocumentImportsPanel projectId="project-1" sourcePaths={['documents/report.md']} />);
    await screen.findByText('Pipeline pending');
    await waitFor(() => expect(intervals.size).toBe(1));

    await tickPendingPoll();
    await screen.findByText('Pipeline running');
    expect(screen.getByText('Source.documentImport.sourceSaved')).toBeDefined();

    await tickPendingPoll();
    await screen.findByText(`Pipeline ${terminal}`);
    expect(screen.getByText('Source.documentImport.sourceSaved')).toBeDefined();
    expect(screen.queryByText('Source.documentImport.failure.worker_unavailable')).toBeNull();
    expect(intervals.size).toBe(0);
  });
}
