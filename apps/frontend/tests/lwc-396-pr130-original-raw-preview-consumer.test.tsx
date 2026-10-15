import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { configureApiAuth } from '@/lib/api';
import { SourceListClient } from '@/components/SourceListClient';

const workspace = vi.hoisted(() => ({ currentProject: { id: 'main-synthetic-project' }, token: 'synthetic-token' }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => workspace }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/NavigationBlocker', () => ({ NavigationLink: ({ children }: { children: React.ReactNode }) => <span>{children}</span> }));
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it('real SourceListClient raw-preview response retains the existing backend diagnostic ID', async () => {
  configureApiAuth({ getAccessToken: () => workspace.token, refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  window.localStorage.setItem('llm-wiki-last-project', workspace.currentProject.id);
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(Response.json([{ id: 'source-one', slug: 'source-one', title: 'Synthetic source', raw_path: 'fixture.txt', lifecycle: 'synced' }]))
    .mockResolvedValueOnce(Response.json({ error: 'safe preview failure', diagnostic_id: 'preview_help_123' }, { status: 502 }));
  vi.stubGlobal('fetch', fetchMock);
  render(<SourceListClient />);
  fireEvent.click(await screen.findByRole('button', { name: 'Source.previewRawAria' }));
  const dialog = await screen.findByRole('dialog');
  expect(await within(dialog).findByText(/502/)).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(within(dialog).queryByText('preview_help_123')).not.toBeNull();
});
