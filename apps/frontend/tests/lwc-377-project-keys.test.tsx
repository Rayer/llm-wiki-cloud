import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', { configurable: true, value: (callback: () => unknown) => Promise.resolve(callback()) });
}

const { cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');
const mocks = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  listCLISessions: vi.fn(),
  listSyncBindings: vi.fn(),
  revokeCLISession: vi.fn(),
  revokeSyncBinding: vi.fn(),
  reauthorizeSyncBinding: vi.fn(),
  readGoogleIdentitySummary: vi.fn(),
  refreshAccessToken: vi.fn(),
  currentProject: { id: 'project-1', name: 'Research' } as { id: string; name: string } | null,
}));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));
vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken, user: { id: 'owner-1', email: 'owner@example.test' } }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: mocks.currentProject }) }));
vi.mock('@/lib/i18n', () => {
  const t = (key: string, params?: Record<string, string>) => params ? `${key}:${Object.values(params).join('|')}` : key;
  return { useLocale: () => ({ t }) };
});
vi.mock('@/lib/google-auth', () => ({ beginGoogleLink: vi.fn(), readGoogleIdentitySummary: mocks.readGoogleIdentitySummary }));
vi.mock('@/lib/cli-auth', () => ({
  listCLISessions: mocks.listCLISessions,
  listSyncBindings: mocks.listSyncBindings,
  revokeCLISession: mocks.revokeCLISession,
  revokeSyncBinding: mocks.revokeSyncBinding,
  reauthorizeSyncBinding: mocks.reauthorizeSyncBinding,
}));

import { AccountSettingsModal } from '@/components/AccountSettingsModal';
import type { ProjectKey } from '@/lib/project-keys';

const key: ProjectKey = {
  key_id: '0123456789abcdef0123456789abcdef',
  name: 'Indexer',
  project_id: 'project-1',
  capabilities: ['query'],
  state: 'active',
  created_at: '2026-10-01T00:00:00Z',
};
const secret = 'lwc_pk_0123456789abcdef0123456789abcdef.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

function response(payload: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: async () => payload } as Response;
}

beforeEach(() => {
  mocks.apiFetch.mockReset().mockResolvedValue(response({ keys: [] }));
  mocks.listCLISessions.mockReset().mockResolvedValue([]);
  mocks.listSyncBindings.mockReset().mockResolvedValue([]);
  mocks.revokeCLISession.mockReset().mockResolvedValue(undefined);
  mocks.revokeSyncBinding.mockReset().mockResolvedValue(undefined);
  mocks.reauthorizeSyncBinding.mockReset().mockResolvedValue(undefined);
  mocks.readGoogleIdentitySummary.mockReset().mockResolvedValue({ primary_email: 'owner@example.test', linked_providers: [] });
  mocks.refreshAccessToken.mockReset().mockResolvedValue('fresh-web-token');
  mocks.currentProject = { id: 'project-1', name: 'Research' };
  vi.stubGlobal('confirm', vi.fn(() => true));
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  vi.clearAllMocks();
});

describe('LWC-377 Project API keys', () => {
  it('loads only metadata for the current Project and creates a show-once secret', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledWith('/api/v1/projects/project-1/keys', { method: 'GET', projectId: 'project-1' }));
    expect(screen.getByText('AccountSettings.projectKeyBoundProject:Research|project-1')).toBeTruthy();
    expect(screen.queryByTestId('project-key-secret')).toBeNull();

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Indexer' } });
    mocks.apiFetch.mockResolvedValueOnce(response({ key, secret }, 201));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));
    expect((await screen.findByTestId('project-key-secret')).textContent).toBe(secret);
    expect(mocks.apiFetch).toHaveBeenLastCalledWith('/api/v1/projects/project-1/keys', {
      method: 'POST', projectId: 'project-1', json: true, body: JSON.stringify({ name: 'Indexer' }),
    });
    expect(screen.getByRole('list').textContent).toContain('Indexer');
    expect(screen.getByRole('list').textContent).toContain('AccountSettings.projectKeyActive');

    const copy = navigator.clipboard.writeText as ReturnType<typeof vi.fn>;
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCopy' }));
    await waitFor(() => expect(copy).toHaveBeenCalledWith(secret));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyDismiss' }));
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
  });

  it('disables creation when no current Project exists', () => {
    mocks.currentProject = null;
    render(<AccountSettingsModal onClose={vi.fn()} />);
    expect((screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('AccountSettings.projectKeyNoProject')).toBeTruthy();
    expect(mocks.apiFetch).not.toHaveBeenCalled();
  });

  it('clears the one-time secret when the settings modal closes', async () => {
    const onClose = vi.fn();
    render(<AccountSettingsModal onClose={onClose} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Close check' } });
    mocks.apiFetch.mockResolvedValueOnce(response({ key: { ...key, name: 'Close check' }, secret }, 201));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));
    expect((await screen.findByTestId('project-key-secret')).textContent).toBe(secret);
    fireEvent.click(screen.getByRole('button', { name: 'Close account settings' }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
  });

  it('confirms revocation and refreshes the metadata list', async () => {
    const revokedKey = { ...key, state: 'revoked' as const, revoked_at: '2026-10-02T00:00:00Z' };
    mocks.apiFetch
      .mockResolvedValueOnce(response({ keys: [key] }))
      .mockResolvedValueOnce(response({ key: revokedKey }))
      .mockResolvedValueOnce(response({ keys: [revokedKey] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'AccountSettings.projectKeyRevoke' }));
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledWith(
      `/api/v1/projects/project-1/keys/${key.key_id}/revoke`, { method: 'POST', projectId: 'project-1' },
    ));
    expect(window.confirm).toHaveBeenCalledWith('AccountSettings.confirmRevokeProjectKey:Indexer');
    await waitFor(() => expect(screen.getByText(/AccountSettings.projectKeyRevokedState/)).toBeTruthy());
    expect(screen.getByRole('status').textContent).toBe('AccountSettings.projectKeyRevoked');
  });

  it('reconciles an ambiguous revoke from the refreshed list without retrying the mutation', async () => {
    const revokedKey = { ...key, state: 'revoked' as const, revoked_at: '2026-10-02T00:00:00Z' };
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [key] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));

    mocks.apiFetch
      .mockResolvedValueOnce(response({ error: 'revoke outcome unknown' }, 503))
      .mockResolvedValueOnce(response({ keys: [revokedKey] }));
    fireEvent.click(await screen.findByRole('button', { name: 'AccountSettings.projectKeyRevoke' }));

    await waitFor(() => expect(screen.getByText(/AccountSettings.projectKeyRevokedState/)).toBeTruthy());
    expect(screen.getByRole('status').textContent).toBe('AccountSettings.projectKeyRevoked');
    expect(screen.queryByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeNull();
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(2);
  });

  it('hides stale active-key actions when revoke succeeds but the list read fails', async () => {
    const revokedKey = { ...key, state: 'revoked' as const, revoked_at: '2026-10-02T00:00:00Z' };
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [key] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));

    mocks.apiFetch
      .mockResolvedValueOnce(response({ key: revokedKey }))
      .mockResolvedValueOnce(response({ error: 'temporarily unavailable' }, 503));
    fireEvent.click(await screen.findByRole('button', { name: 'AccountSettings.projectKeyRevoke' }));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('AccountSettings.projectKeysLoadError');
    expect(screen.queryByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeNull();
    expect(screen.queryByRole('status')?.textContent).not.toBe('AccountSettings.projectKeyRevoked');
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(2);
  });

  it('keeps actions hidden after ambiguous revoke and list failures until explicit refresh succeeds', async () => {
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [key] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));

    mocks.apiFetch
      .mockResolvedValueOnce(response({ error: 'revoke outcome unknown' }, 503))
      .mockResolvedValueOnce(response({ error: 'temporarily unavailable' }, 503))
      .mockResolvedValueOnce(response({ keys: [key] }));
    fireEvent.click(await screen.findByRole('button', { name: 'AccountSettings.projectKeyRevoke' }));

    await screen.findByRole('alert');
    expect(screen.queryByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeNull();
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeysRefresh' }));
    await screen.findByRole('button', { name: 'AccountSettings.projectKeyRevoke' });
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(3);
  });

  it('reconciles a lost create response to the newly listed key without showing a secret or retrying', async () => {
    const lateKey = { ...key, key_id: 'fedcba9876543210fedcba9876543210', name: 'Lost response' };
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Lost response' } });
    mocks.apiFetch
      .mockRejectedValueOnce(new Error('socket hang up'))
      .mockResolvedValueOnce(response({ keys: [lateKey] }));

    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));

    expect((await screen.findByRole('status')).textContent).toContain('AccountSettings.projectKeyUnknownOutcomeNoId:Lost response');
    expect(screen.getByRole('list').textContent).toContain('Lost response');
    expect(screen.getByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeTruthy();
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
    expect(screen.getByRole('status').textContent).not.toContain(lateKey.key_id);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(2);
  });

  it('reconciles an unreadable successful create response before updating component state', async () => {
    const lateKey = { ...key, key_id: 'fedcba9876543210fedcba9876543210', name: 'Unreadable response' };
    const unreadableResponse = {
      ok: true,
      status: 201,
      json: async () => { throw new SyntaxError('invalid JSON'); },
    } as unknown as Response;
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Unreadable response' } });
    mocks.apiFetch
      .mockResolvedValueOnce(unreadableResponse)
      .mockResolvedValueOnce(response({ keys: [lateKey] }));

    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));

    expect((await screen.findByRole('status')).textContent).toContain('AccountSettings.projectKeyUnknownOutcomeNoId:Unreadable response');
    expect(screen.getByRole('list').textContent).toContain('Unreadable response');
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(2);
  });

  it('keeps actions hidden when create and its readback fail until explicit refresh recovers', async () => {
    const existingKey = { ...key, name: 'Existing key' };
    const lateKey = { ...key, key_id: 'fedcba9876543210fedcba9876543210', name: 'Lost response' };
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [existingKey] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Lost response' } });
    mocks.apiFetch
      .mockRejectedValueOnce(new Error('socket hang up'))
      .mockResolvedValueOnce(response({ error: 'temporarily unavailable' }, 503))
      .mockResolvedValueOnce(response({ keys: [existingKey, lateKey] }));

    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));

    expect((await screen.findByRole('status')).textContent).toContain('AccountSettings.projectKeyUnknownOutcomeNoId:Lost response');
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(screen.queryByRole('list')).toBeNull();
    expect(screen.queryByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeNull();
    expect((screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
    expect(screen.getByRole('status').textContent).not.toContain(lateKey.key_id);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeysRefresh' }));
    expect((await screen.findByRole('list')).textContent).toContain('Lost response');
    expect((screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getAllByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toHaveLength(2);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1);
    expect(mocks.apiFetch.mock.calls.filter(([, options]) => options?.method === 'GET')).toHaveLength(3);
  });

  it('does not retry an unknown create and offers delayed-list recovery', async () => {
    const lateKey = { ...key, key_id: 'fedcba9876543210fedcba9876543210', name: 'Late commit' };
    mocks.apiFetch.mockResolvedValueOnce(response({ keys: [] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Late commit' } });
    mocks.apiFetch
      .mockResolvedValueOnce(response({ error: 'project key creation outcome unknown', key_id: lateKey.key_id }, 503))
      .mockResolvedValueOnce(response({ keys: [] }))
      .mockResolvedValueOnce(response({ keys: [lateKey] }));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeyCreate' }));
    expect((await screen.findByRole('status')).textContent).toContain(lateKey.key_id);
    expect(screen.queryByText('Late commit')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.projectKeysRefresh' }));
    expect((await screen.findByRole('list')).textContent).toContain('Late commit');
    expect(mocks.apiFetch).toHaveBeenCalledTimes(4);
    expect(mocks.apiFetch.mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/projects/project-1/keys',
      '/api/v1/projects/project-1/keys',
      '/api/v1/projects/project-1/keys',
      '/api/v1/projects/project-1/keys',
    ]);
    expect(screen.queryByTestId('project-key-secret')).toBeNull();
    expect(screen.getByRole('button', { name: 'AccountSettings.projectKeyRevoke' })).toBeTruthy();
  });
});
