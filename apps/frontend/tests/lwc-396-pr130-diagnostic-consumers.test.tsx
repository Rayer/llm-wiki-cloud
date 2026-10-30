import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { configureApiAuth, getConcepts } from '@/lib/api';
import { ListClient } from '@/components/ListClient';
import { RawClient } from '@/components/RawClient';
import { SourceListClient } from '@/components/SourceListClient';

const workspace = vi.hoisted(() => ({ currentProject: { id: 'project-a' }, token: 'session-a' }));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
}));
vi.mock('next/navigation', () => ({ usePathname: () => '/sources', useSearchParams: () => new URLSearchParams() }));
vi.mock('@/components/DocumentImportsPanel', () => ({ DocumentImportsPanel: () => null }));

function consumer(kind: 'source' | 'concept') {
  return kind === 'source'
    ? <SourceListClient />
    : <ListClient title="Concepts" description="Concept list" load={getConcepts} basePath="/concepts" entryType="concept" />;
}

function errorResponse(diagnosticId?: string, requestId?: string) {
  return Response.json({ error: 'safe server error', ...(diagnosticId ? { diagnostic_id: diagnosticId } : {}) }, {
    status: 502,
    headers: requestId ? { 'X-Request-ID': requestId } : {},
  });
}

function configureAuth() {
  configureApiAuth({
    getAccessToken: () => workspace.token,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
}

beforeEach(() => {
  workspace.currentProject = { id: 'project-a' };
  workspace.token = 'session-a';
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  configureAuth();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  vi.clearAllMocks();
});

describe('LWC-396 actual list error consumers', () => {
  it('displays and copies the backend ID through both real API helpers', async () => {
    const sourceId = 'source_help_123';
    const conceptId = 'concept_help_456';
    const sourceRequestId = '123e4567-e89b-42d3-a456-426614174007';
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(errorResponse(sourceId, sourceRequestId))
      .mockResolvedValueOnce(errorResponse(conceptId));
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('fetch', fetchMock);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    const source = render(consumer('source'));
    expect(await screen.findByText(sourceRequestId)).toBeTruthy();
    expect(screen.getByText('QueryErrors.requestId')).toBeTruthy();
    expect(screen.queryByText(sourceId)).toBeNull();
    expect(screen.getByText('API request failed (502)')).toBeTruthy();
    expect(screen.queryByText('List.noSources')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(sourceRequestId));
    expect(await screen.findByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeTruthy();
    source.unmount();

    render(consumer('concept'));
    expect(await screen.findByText(conceptId)).toBeTruthy();
    expect(screen.getByText('QueryErrors.diagnosticId')).toBeTruthy();
    expect(screen.getByText('API request failed (502)')).toBeTruthy();
    expect(screen.queryByText('List.noConcepts')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(conceptId));
    expect(await screen.findByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('displays and copies the original SourceListClient raw-preview diagnostic ID', async () => {
    const diagnosticId = 'preview_help_123';
    const requestId = '123e4567-e89b-42d3-a456-426614174008';
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(Response.json([{ id: 'source-one', slug: 'source-one', title: 'Synthetic source', raw_path: 'fixture.txt', lifecycle: 'synced' }]))
      .mockResolvedValueOnce(errorResponse(diagnosticId, requestId));
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('fetch', fetchMock);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    render(<SourceListClient />);
    fireEvent.click(await screen.findByRole('button', { name: 'Source.previewRawAria' }));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText('Raw preview request failed (502)')).toBeTruthy();
    expect(within(dialog).getByText(requestId)).toBeTruthy();
    expect(within(dialog).getByText('QueryErrors.requestId')).toBeTruthy();
    expect(within(dialog).queryByText(diagnosticId)).toBeNull();
    fireEvent.click(within(dialog).getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(requestId));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('displays and copies IDs through the RawClient list and preview helpers', async () => {
    const listId = 'raw_list_help_123';
    const previewId = 'raw_preview_help_456';
    const listRequestId = '123e4567-e89b-42d3-a456-426614174009';
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(errorResponse(listId, listRequestId))
      .mockResolvedValueOnce(Response.json({ files: [{ name: 'fixture.md', size: 12, updated: '', sha256: '', ingested: false }] }))
      .mockResolvedValueOnce(errorResponse(previewId));
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('fetch', fetchMock);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    const list = render(<RawClient />);
    expect(await screen.findByText(listRequestId)).toBeTruthy();
    expect(screen.getByText('QueryErrors.requestId')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(listRequestId));
    list.unmount();

    render(<RawClient />);
    fireEvent.click(await screen.findByRole('button', { name: 'Raw.previewAria' }));
    expect(screen.getAllByText('fixture.md')).toHaveLength(2);
    expect(await screen.findByText('Raw preview request failed (502)')).toBeTruthy();
    expect(screen.getByText(previewId)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(previewId));
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('ignores a delayed RawClient preview error after the project and session change', async () => {
    const staleId = 'stale_raw_preview_123';
    let resolvePreview!: (response: Response) => void;
    const pendingPreview = new Promise<Response>((resolve) => { resolvePreview = resolve; });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(Response.json({ files: [{ name: 'fixture.md', size: 12, updated: '', sha256: '', ingested: false }] }))
      .mockImplementationOnce(() => pendingPreview)
      .mockResolvedValueOnce(Response.json({ files: [] }));
    vi.stubGlobal('fetch', fetchMock);

    const mounted = render(<RawClient />);
    fireEvent.click(await screen.findByRole('button', { name: 'Raw.previewAria' }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

    workspace.currentProject = { id: 'project-b' };
    workspace.token = 'session-b';
    window.localStorage.setItem('llm-wiki-last-project', 'project-b');
    mounted.rerender(<RawClient />);
    expect(await screen.findByText('Raw.empty')).toBeTruthy();

    await act(async () => resolvePreview(errorResponse(staleId)));
    expect(screen.queryByText(staleId)).toBeNull();
    expect(screen.queryByText('Raw preview request failed (502)')).toBeNull();
  });

  it('omits the ID and copy control when the backend ID is invalid or absent', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(errorResponse('bad/id'))
      .mockResolvedValueOnce(errorResponse());
    vi.stubGlobal('fetch', fetchMock);

    const source = render(consumer('source'));
    expect(await screen.findByText('API request failed (502)')).toBeTruthy();
    expect(screen.queryByText('bad/id')).toBeNull();
    expect(screen.queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();
    source.unmount();

    render(consumer('concept'));
    expect(await screen.findByText('API request failed (502)')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();
    expect(screen.queryByText('List.noConcepts')).toBeNull();
  });

  it.each(['source', 'concept'] as const)('ignores delayed copy feedback after a %s identity switch', async (kind) => {
    const firstId = 'first_help_123';
    const secondId = 'second_help_456';
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(errorResponse(firstId))
      .mockResolvedValueOnce(errorResponse(secondId));
    let resolveWrite!: () => void;
    const writeText = vi.fn(() => new Promise<void>((resolve) => { resolveWrite = resolve; }));
    vi.stubGlobal('fetch', fetchMock);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

    const mounted = render(consumer(kind));
    expect(await screen.findByText(firstId)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(firstId));

    workspace.currentProject = { id: 'project-b' };
    workspace.token = 'session-b';
    window.localStorage.setItem('llm-wiki-last-project', 'project-b');
    mounted.rerender(consumer(kind));
    expect(screen.queryByText(firstId)).toBeNull();
    expect(await screen.findByText(secondId)).toBeTruthy();

    await act(async () => resolveWrite());
    expect(screen.queryByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeNull();
    expect(screen.queryByText(firstId)).toBeNull();
  });
});
