import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', { configurable: true, value: (callback: () => unknown) => Promise.resolve(callback()) });
}

const { cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');
const mocks = vi.hoisted(() => ({
  listCLISessions: vi.fn(),
  listSyncBindings: vi.fn(),
  revokeCLISession: vi.fn(),
  revokeSyncBinding: vi.fn(),
  reauthorizeSyncBinding: vi.fn(),
  readGoogleIdentitySummary: vi.fn(),
  accessToken: 'web-access-token',
  refreshAccessToken: vi.fn(),
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ accessToken: mocks.accessToken, refreshAccessToken: mocks.refreshAccessToken, user: { id: 'owner-1', email: 'owner@example.test' } }),
}));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }) }));
vi.mock('@/lib/google-auth', () => ({ beginGoogleLink: vi.fn(), readGoogleIdentitySummary: mocks.readGoogleIdentitySummary }));
vi.mock('@/lib/cli-auth', () => ({
  listCLISessions: mocks.listCLISessions,
  listSyncBindings: mocks.listSyncBindings,
  revokeCLISession: mocks.revokeCLISession,
  revokeSyncBinding: mocks.revokeSyncBinding,
  reauthorizeSyncBinding: mocks.reauthorizeSyncBinding,
}));

import { AccountSettingsModal } from '@/components/AccountSettingsModal';

const session = { id: 'session-1', client_name: 'lwc-sync CLI', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const binding = { binding_id: 'binding-1', host: 'https://auth.example.test', wiki_id: 'wiki-1', project_id: 'project-1', authorized_by: 'owner-1', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };

beforeEach(() => {
  mocks.listCLISessions.mockReset().mockResolvedValue([session]);
  mocks.listSyncBindings.mockReset().mockResolvedValue([binding]);
  mocks.revokeCLISession.mockReset().mockResolvedValue(undefined);
  mocks.revokeSyncBinding.mockReset().mockResolvedValue(undefined);
  mocks.reauthorizeSyncBinding.mockReset().mockResolvedValue({ ...binding, binding_id: 'binding-2' });
  mocks.readGoogleIdentitySummary.mockReset().mockResolvedValue({ primary_email: 'owner@example.test', linked_providers: [] });
  mocks.refreshAccessToken.mockReset().mockResolvedValue('fresh-web-token');
  vi.stubGlobal('confirm', vi.fn(() => true));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('LWC-346 account self-service', () => {
  it('lets a Web user revoke an active CLI session', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);
    const revoke = await screen.findByRole('button', { name: 'AccountSettings.revokeSession' });
    fireEvent.click(revoke);
    await waitFor(() => expect(mocks.revokeCLISession).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      'session-1',
    ));
    expect(screen.getByRole('status').textContent).toBe('AccountSettings.sessionRevoked');
  });

  it('lets a Web user revoke and explicitly replace a Project binding', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'AccountSettings.revokeBinding' }));
    await waitFor(() => expect(mocks.revokeSyncBinding).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      binding,
    ));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.reauthorizeBinding' }));
    await waitFor(() => expect(mocks.reauthorizeSyncBinding).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      binding,
    ));
    expect(screen.getByRole('status').textContent).toBe('AccountSettings.bindingReauthorized');
  });
});
