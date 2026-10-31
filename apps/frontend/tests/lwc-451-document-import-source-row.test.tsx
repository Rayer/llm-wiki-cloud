import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { SourceListClient } from '@/components/SourceListClient';

const state = vi.hoisted(() => ({
  currentProject: { id: 'project-1' },
  projects: [{ id: 'project-1' }],
  projectsLoading: false,
  token: 'session-1',
  selectProject: vi.fn(),
  refreshNavCounts: vi.fn(),
  search: 'document_id=document-1',
}));
const realSetInterval = globalThis.setInterval.bind(globalThis);
const realClearInterval = globalThis.clearInterval.bind(globalThis);

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => state }));
vi.mock('@/components/NavigationBlocker', () => ({ NavigationLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }));
vi.mock('@/lib/i18n', () => ({
  useT: () => ({
    t: (key: string, values?: Record<string, string | number>) => key === 'Source.documentImport.pipelineState'
      ? `Pipeline ${values?.state ?? ''}`
      : key,
  }),
}));
vi.mock('next/navigation', () => ({ usePathname: () => '/sources', useSearchParams: () => new URLSearchParams(state.search) }));

const documentImport = {
  document_id: 'document-1',
  original_filename: 'report.docx',
  media_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  bytes: 10,
  sha256: 'a'.repeat(64),
  created_at: '2026-10-31T00:00:00Z',
  updated_at: '2026-10-31T00:00:00Z',
  revision: 4,
  converter_version: 'pinned',
  conversion_state: 'succeeded',
  source_state: 'saved',
  source_ref: { raw_path: 'raw/document-import-document-1.md', sha256: 'b'.repeat(64), object_generation: 1 },
  pipeline_ref: { status: 'published', intent_id: 'intent-1', source_revision: 1 },
  deletion_state: 'present',
};

const pipelineRecord = (status: string) => ({
  ...documentImport,
  pipeline_ref: { ...documentImport.pipeline_ref, status },
});

let pendingPolls: Map<number, () => void>;

beforeEach(() => {
  pendingPolls = new Map();
  let nextPollId = 0;
  state.search = 'document_id=document-1';
  window.localStorage.setItem('llm-wiki-last-project', 'project-1');
  configureApiAuth({ getAccessToken: () => state.token, refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: TimerHandler, timeout?: number) => {
    if (timeout !== 5000) return realSetInterval(callback, timeout);
    const id = ++nextPollId;
    if (typeof callback === 'function') pendingPolls.set(id, callback as () => void);
    return id as unknown as ReturnType<typeof setInterval>;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => {
    if (!pendingPolls.delete(id)) realClearInterval(id);
  }) as typeof clearInterval);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

it('merges saved import and Pipeline status into the source row while retaining preview and deep-link focus', async () => {
  state.search = 'conversion_status=succeeded&document_id=document-1';
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes('/api/v1/sources')) {
      return Response.json([{ id: 'source-1', slug: 'report', title: 'Imported report', raw_path: documentImport.source_ref.raw_path, lifecycle: 'synced' }]);
    }
    if (url.includes('/document-imports/document-1')) return Response.json(documentImport);
    if (url.includes('/document-imports')) return Response.json({ items: [documentImport], snapshot_revision: 4, failed_count: 0, latest_failure_sequence: 0 });
    if (url.includes('/api/v1/raw/')) return new Response('# converted source preview');
    throw new Error(`Unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);

  render(<SourceListClient />);

  const sourceTitle = await screen.findByText('report.docx');
  expect(screen.getAllByText('report.docx')).toHaveLength(1);
  await waitFor(() => {
    const status = screen.getAllByRole('status').find((item) => item.textContent?.includes('Source.documentImport.sourceSaved'));
    expect(status?.textContent).toContain('Source.documentImport.pipelineStatus.succeeded');
  });

  const sourceRow = sourceTitle.closest('[tabindex="-1"]');
  expect(sourceRow).not.toBeNull();
  await waitFor(() => expect(document.activeElement).toBe(sourceRow));

  fireEvent.click(screen.getByRole('button', { name: 'Source.previewRawAria' }));
  expect(await screen.findByRole('dialog')).toBeTruthy();
  expect(await screen.findByText('converted source preview')).toBeTruthy();
});

for (const terminal of ['failed', 'published']) {
  it(`keeps the merged source row current through pending→running→${terminal}`, async () => {
    const progression = ['pending', 'running', terminal];
    let listCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/sources')) {
        return Response.json([{ id: 'source-1', slug: 'report', title: 'Imported report', raw_path: documentImport.source_ref.raw_path, lifecycle: 'synced' }]);
      }
      if (url.includes('/document-imports/document-1')) return Response.json(pipelineRecord('pending'));
      if (url.includes('/document-imports')) {
        const status = progression[Math.min(listCalls++, progression.length - 1)];
        return Response.json({ items: [pipelineRecord(status)], snapshot_revision: 4, failed_count: 0, latest_failure_sequence: 0 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<SourceListClient />);
    await waitFor(() => expect(screen.getByText('report.docx')).toBeTruthy());
    const sourceStatus = () => screen.getAllByRole('status').find((item) => item.textContent?.includes('Source.documentImport.sourceSaved'));
    await waitFor(() => expect(sourceStatus()?.textContent).toContain('Source.documentImport.pipelineStatus.pending'));
    expect(screen.getByText('report.docx')).toBeTruthy();
    await waitFor(() => expect(pendingPolls.size).toBe(1));

    for (const expected of ['running', terminal]) {
      const poll = [...pendingPolls.values()][0];
      expect(poll).toBeDefined();
      await act(async () => {
        poll?.();
        await Promise.resolve();
      });
      const statusKey = expected === 'running' ? 'running' : expected === 'published' ? 'succeeded' : expected;
      await waitFor(() => expect(sourceStatus()?.textContent).toContain(`Source.documentImport.pipelineStatus.${statusKey}`));
    }
    expect(pendingPolls.size).toBe(0);
    expect(screen.queryByText('Source.documentImport.failure.worker_unavailable')).toBeNull();
  });
}
