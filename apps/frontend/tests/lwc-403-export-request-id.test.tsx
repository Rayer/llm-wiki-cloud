import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { ExportPanel } from '@/components/ExportPanel';

const workspace = vi.hoisted(() => ({
  currentProject: { id: 'project-export' },
  isDemoSession: false,
  token: 'session-export',
}));
const translations = vi.hoisted(() => ({ t: (key: string) => key, locale: 'en' }));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => translations }));

const requestId = '123e4567-e89b-42d3-a456-426614174018';
const diagnosticId = 'export_help-403';
const emptyState = {
  latest_job: null,
  current: null,
  previous: null,
  eligible: true,
  rejection_reason: null,
  next_allowed_at: null,
};

beforeEach(() => {
  workspace.currentProject = { id: 'project-export' };
  workspace.isDemoSession = false;
  workspace.token = 'session-export';
  configureApiAuth({
    getAccessToken: () => workspace.token,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  window.localStorage.setItem('llm-wiki-last-project', 'project-export');
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
});

it('renders and copies the export adapter request ID from its actual error response', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
  const fetchMock = vi.fn(async (_input: string | URL | Request, init?: RequestInit) => {
    if (init?.method === 'POST') {
      return Response.json({
        error_message: 'export rejected safely',
        diagnostic_id: diagnosticId,
      }, { status: 503, headers: { 'X-Request-ID': requestId } });
    }
    return Response.json(emptyState);
  });
  vi.stubGlobal('fetch', fetchMock);

  render(<ExportPanel />);
  const packageButton = await screen.findByRole('button', { name: 'Export.package' });
  await waitFor(() => expect(packageButton).toHaveProperty('disabled', false));
  fireEvent.click(packageButton);
  const dialog = screen.getByRole('dialog');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Export.start' }));

  expect(await within(dialog).findByText('export rejected safely')).toBeTruthy();
  expect(within(dialog).getByText(requestId)).toBeTruthy();
  expect(within(dialog).getByText('QueryErrors.requestId')).toBeTruthy();
  expect(within(dialog).queryByText(diagnosticId)).toBeNull();
  fireEvent.click(within(dialog).getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(requestId));
  expect(fetchMock).toHaveBeenCalledTimes(3);
});
