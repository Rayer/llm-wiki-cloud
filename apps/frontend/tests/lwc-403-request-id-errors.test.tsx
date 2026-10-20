import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { configureApiAuth, getConcepts } from '@/lib/api';
import { HomeClient } from '@/components/HomeClient';
import { ListClient } from '@/components/ListClient';

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-a' },
  token: 'session-a',
  isDemoSession: false,
  refreshNavCounts: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('next/navigation', () => ({
  usePathname: () => '/',
  useSearchParams: () => new URLSearchParams(),
}));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
  useNavigationBlocker: () => ({
    setBlocked: vi.fn(),
    confirmNavigation: () => true,
    consumeCapturedConfirmation: () => false,
  }),
}));

const requestId = '123e4567-e89b-42d3-a456-426614174006';
const diagnosticId = 'diagnostic_help-403';

function errorResponse(ids: { requestId?: string; diagnosticId?: string } = {}) {
  return Response.json({
    error: 'safe server error',
    ...(ids.diagnosticId ? { diagnostic_id: ids.diagnosticId } : {}),
  }, {
    status: 502,
    headers: ids.requestId ? { 'X-Request-ID': ids.requestId } : {},
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
  workspace.isDemoSession = false;
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  configureAuth();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  vi.clearAllMocks();
});

it('shows and copies a real Query response request ID ahead of diagnostic_id', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    const url = new URL(String(input), window.location.origin);
    if (url.pathname.endsWith('/api/v1/status')) return Response.json({});
    if (url.pathname.endsWith('/api/v1/concepts')) return Response.json([]);
    return Response.json({
      error: 'safe query failure',
      code: 'query_provider_failed',
      diagnostic_id: diagnosticId,
    }, { status: 503, headers: { 'X-Request-ID': requestId } });
  }));

  render(<HomeClient />);
  const queryInput = await screen.findByRole('textbox');
  const searchButton = await screen.findByRole('button', { name: 'Demo.search' });
  const form = searchButton.closest('form');
  expect(form).not.toBeNull();
  fireEvent.change(queryInput, { target: { value: 'request id support' } });
  fireEvent.submit(form as HTMLFormElement);

  expect(await screen.findByText('QueryErrors.providerFailed')).toBeTruthy();
  expect(screen.getByText(requestId)).toBeTruthy();
  expect(screen.getByText('QueryErrors.requestId')).toBeTruthy();
  expect(screen.queryByText(diagnosticId)).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(requestId));
});

it('renders and copies the preferred real API error ID for all four response combinations', async () => {
  const cases = [
    { name: 'both IDs', ids: { requestId, diagnosticId }, value: requestId, label: 'QueryErrors.requestId' },
    { name: 'request ID only', ids: { requestId }, value: requestId, label: 'QueryErrors.requestId' },
    { name: 'diagnostic ID only', ids: { diagnosticId }, value: diagnosticId, label: 'QueryErrors.diagnosticId' },
    { name: 'neither ID', ids: {}, value: undefined, label: undefined },
  ];

  for (const [index, testCase] of cases.entries()) {
    cleanup();
    workspace.currentProject = { id: `project-${index}` };
    window.localStorage.setItem('llm-wiki-last-project', `project-${index}`);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(errorResponse(testCase.ids)));

    render(
      <ListClient
        title="Concepts"
        description="Concept list"
        load={getConcepts}
        basePath="/concepts"
        entryType="concept"
      />,
    );
    expect(await screen.findByText('API request failed (502)')).toBeTruthy();

    if (testCase.value && testCase.label) {
      expect(screen.getByText(testCase.value)).toBeTruthy();
      expect(screen.getByText(testCase.label)).toBeTruthy();
      fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith(testCase.value));
    } else {
      expect(screen.queryByText('QueryErrors.requestId')).toBeNull();
      expect(screen.queryByText('QueryErrors.diagnosticId')).toBeNull();
      expect(screen.queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();
      expect(writeText).not.toHaveBeenCalled();
    }
  }
});
