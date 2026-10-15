import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { configureApiAuth, getConcept, getSource } from '@/lib/api';
import { DetailClient } from '@/components/DetailClient';
import { HomeClient } from '@/components/HomeClient';
import { RawClient } from '@/components/RawClient';
import { SourceAnnotationEditor } from '@/components/SourceAnnotationEditor';

const mocks = vi.hoisted(() => ({ searchWiki: vi.fn() }));

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-a' },
  token: 'session-a',
  isDemoSession: false,
  refreshNavCounts: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, searchWiki: mocks.searchWiki };
});
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('next/navigation', () => ({ useSearchParams: () => new URLSearchParams() }));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ href, children, onClick }: {
    href: string;
    children: React.ReactNode;
    onClick?: React.MouseEventHandler<HTMLAnchorElement>;
  }) => <a href={href} onClick={onClick}>{children}</a>,
  useNavigationBlocker: () => ({
    setBlocked: vi.fn(),
    confirmNavigation: () => true,
    consumeCapturedConfirmation: () => false,
  }),
}));

function configureAuth() {
  configureApiAuth({
    getAccessToken: () => workspace.token,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
}

function setProject(id: string) {
  workspace.currentProject = { id };
  workspace.token = `session-${id}`;
  window.localStorage.setItem('llm-wiki-last-project', id);
}

function errorResponse(diagnosticId: string | undefined, status = 502) {
  return Response.json({
    error: 'existing safe server error',
    ...(diagnosticId ? { diagnostic_id: diagnosticId } : {}),
  }, { status });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

beforeEach(() => {
  setProject('project-a');
  workspace.isDemoSession = false;
  workspace.refreshNavCounts.mockClear();
  mocks.searchWiki.mockResolvedValue({
    results: [],
    aiAnswer: 'See [Source].',
    citations: [{ text: 'Source', id: 'source-1', slug: 'source-1', type: 'source' }],
  });
  configureAuth();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  vi.clearAllMocks();
});

it('keeps a pending preview alive through a failed download and ignores that download after a project switch', async () => {
  const pendingPreview = deferred<Response>();
  const pendingStaleDownload = deferred<Response>();
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(Response.json({ files: [
      { name: 'fixture.md', size: 12, updated: '', sha256: '', ingested: false },
      { name: 'manual.pdf', size: 12, updated: '', sha256: '', ingested: false },
    ] }))
    .mockImplementationOnce(() => pendingPreview.promise)
    .mockResolvedValueOnce(errorResponse('download_help_123'))
    .mockImplementationOnce(() => pendingStaleDownload.promise)
    .mockResolvedValueOnce(Response.json({ files: [] }));
  vi.stubGlobal('fetch', fetchMock);
  const mounted = render(<RawClient />);

  fireEvent.click(await screen.findByRole('button', { name: 'Raw.previewAria' }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  fireEvent.click(screen.getByRole('button', { name: 'Raw.download' }));
  expect(await screen.findByText('download_help_123')).toBeTruthy();

  await act(async () => pendingPreview.resolve(new Response('preview body')));
  expect(screen.queryByText(/Raw.loadingPreview/)).toBeNull();
  expect(screen.getByText('preview body')).toBeTruthy();
  expect(screen.getByText('download_help_123')).toBeTruthy();

  fireEvent.click(screen.getByRole('button', { name: 'Raw.close' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Raw.downloadAria' }));
  fireEvent.click(screen.getByRole('button', { name: 'Raw.download' }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));

  setProject('project-b');
  mounted.rerender(<RawClient />);
  expect(await screen.findByText('Raw.empty')).toBeTruthy();
  await act(async () => pendingStaleDownload.resolve(errorResponse('stale_download_123')));
  expect(screen.queryByText('stale_download_123')).toBeNull();
});

it.each(['source', 'concept'] as const)('renders and copies a real %s.read diagnostic ID in DetailClient', async (kind) => {
  const diagnosticId = `${kind}_detail_help_123`;
  const fetchMock = vi.fn(async (input: string | URL | Request) => (
    String(input).endsWith('/concepts')
      ? Response.json([])
      : errorResponse(diagnosticId)
  ));
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

  render(
    <DetailClient
      slug="fixture"
      label="Details"
      backHref="/sources"
      load={kind === 'source' ? getSource : getConcept}
      entryType={kind}
    />,
  );

  expect(await screen.findByText('API request failed (502)')).toBeTruthy();
  expect(screen.getByText(diagnosticId)).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));
  expect(await screen.findByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeTruthy();
});

it('drops invalid detail IDs and prevents delayed copy feedback after the slug changes', async () => {
  const firstId = 'first_detail_help_123';
  const secondId = 'second_detail_help_456';
  const fetchMock = vi.fn(async (input: string | URL | Request) => {
    const url = String(input);
    if (url.endsWith('/concepts')) return Response.json([]);
    if (url.endsWith('/sources/invalid')) return errorResponse('bad/id');
    return errorResponse(url.endsWith('/sources/first') ? firstId : secondId);
  });
  let resolveWrite!: () => void;
  const writeText = vi.fn(() => new Promise<void>((resolve) => { resolveWrite = resolve; }));
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

  const mounted = render(
    <DetailClient slug="invalid" label="Sources" backHref="/sources" load={getSource} entryType="source" />,
  );
  expect(await screen.findByText('API request failed (502)')).toBeTruthy();
  expect(screen.queryByText('bad/id')).toBeNull();
  expect(screen.queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();

  mounted.rerender(
    <DetailClient slug="first" label="Sources" backHref="/sources" load={getSource} entryType="source" />,
  );
  expect(await screen.findByText(firstId)).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(firstId));

  mounted.rerender(
    <DetailClient slug="second" label="Sources" backHref="/sources" load={getSource} entryType="source" />,
  );
  expect(screen.queryByText(firstId)).toBeNull();
  expect(await screen.findByText(secondId)).toBeTruthy();
  await act(async () => resolveWrite());
  expect(screen.queryByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeNull();
  expect(screen.getByText(secondId)).toBeTruthy();
});

it('preserves annotation load error and copies its optional server ID', async () => {
  const diagnosticId = 'annotation_load_help_123';
  const fetchMock = vi.fn().mockResolvedValue(errorResponse(diagnosticId));
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

  render(<SourceAnnotationEditor sourceId="source-1" />);
  expect(await screen.findByText('Annotation.loadFailed')).toBeTruthy();
  expect(screen.getByText(diagnosticId)).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Annotation.retry' })).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));
});

it('keeps annotation conflict and draft behavior while exposing its optional server ID', async () => {
  const diagnosticId = 'annotation_conflict_help_456';
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(Response.json({ body: 'original', expected_generation: '1' }))
    .mockResolvedValueOnce(errorResponse(diagnosticId, 412));
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });

  render(<SourceAnnotationEditor sourceId="source-1" />);
  const textarea = await screen.findByRole('textbox', { name: 'Annotation.label' }) as HTMLTextAreaElement;
  fireEvent.change(textarea, { target: { value: 'edited draft' } });
  fireEvent.click(screen.getByRole('button', { name: 'Annotation.save' }));

  expect(await screen.findByText('Annotation.conflict')).toBeTruthy();
  expect(screen.getByText(diagnosticId)).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Annotation.reload' })).toBeTruthy();
  expect(textarea.value).toBe('edited draft');
  fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));
});

function homeFetch(detailResponse: Response | Promise<Response>, diagnosticId?: string) {
  return vi.fn(async (input: string | URL | Request) => {
    const url = new URL(String(input), window.location.origin);
    if (url.pathname.endsWith('/api/v1/status')) return Response.json({});
    if (url.pathname.endsWith('/api/v1/concepts')) return Response.json([]);
    if (url.pathname.endsWith('/api/v1/sources/source-1')) {
      return detailResponse;
    }
    return errorResponse(diagnosticId);
  });
}

async function searchForSourceCitation() {
  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'topic' } });
  fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));
  fireEvent.click((await screen.findAllByRole('button', { name: 'Source' }))[0]);
  return screen.findByRole('dialog');
}

it('shows and copies only the real source citation read ID in its own modal error', async () => {
  const diagnosticId = 'citation_source_help_123';
  const fetchMock = homeFetch(errorResponse(diagnosticId));
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  render(<HomeClient />);

  const dialog = await searchForSourceCitation();
  expect(await screen.findByText('API request failed (502)')).toBeTruthy();
  expect(dialog.querySelector('code')?.textContent).toBe(diagnosticId);
  fireEvent.click(within(dialog).getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));
  expect(await within(dialog).findByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeTruthy();
});

it('does not invent a citation ID when the source response has an invalid ID', async () => {
  const fetchMock = homeFetch(errorResponse('bad/id'));
  vi.stubGlobal('fetch', fetchMock);
  render(<HomeClient />);

  const dialog = await searchForSourceCitation();
  expect(await within(dialog).findByText('API request failed (502)')).toBeTruthy();
  expect(dialog.querySelector('code')).toBeNull();
  expect(within(dialog).queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();
});

it('ignores a late citation read error after the project changes', async () => {
  const pending = deferred<Response>();
  const fetchMock = homeFetch(pending.promise);
  vi.stubGlobal('fetch', fetchMock);
  const mounted = render(<HomeClient />);

  fireEvent.change(screen.getByRole('textbox'), { target: { value: 'topic' } });
  fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));
  fireEvent.click((await screen.findAllByRole('button', { name: 'Source' }))[0]);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/sources/source-1'), expect.anything()));

  setProject('project-b');
  mounted.rerender(<HomeClient />);
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await act(async () => pending.resolve(errorResponse('late_citation_help_123')));
  expect(screen.queryByText('late_citation_help_123')).toBeNull();
});

it('does not mark a citation ID copied after its project identity changes', async () => {
  const diagnosticId = 'citation_copy_help_123';
  const fetchMock = homeFetch(errorResponse(diagnosticId));
  let resolveWrite!: () => void;
  const writeText = vi.fn(() => new Promise<void>((resolve) => { resolveWrite = resolve; }));
  vi.stubGlobal('fetch', fetchMock);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  const mounted = render(<HomeClient />);

  const dialog = await searchForSourceCitation();
  expect(await within(dialog).findByText(diagnosticId)).toBeTruthy();
  fireEvent.click(within(dialog).getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));

  setProject('project-b');
  mounted.rerender(<HomeClient />);
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  await act(async () => resolveWrite());
  expect(screen.queryByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeNull();
});
