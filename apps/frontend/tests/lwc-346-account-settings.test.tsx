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
  readPasswordSelfServiceStatus: vi.fn(),
  changeAccountPassword: vi.fn(),
  locale: 'en' as 'en' | 'zh-TW',
  accessToken: 'web-access-token',
  userID: 'owner-1',
  refreshAccessToken: vi.fn(),
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ accessToken: mocks.accessToken, refreshAccessToken: mocks.refreshAccessToken, user: { id: mocks.userID, email: 'owner@example.test' } }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: null }) }));
vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  return {
    ...actual,
    useLocale: () => ({
      t: (key: string, params?: Record<string, string | number>) => actual.translate(mocks.locale, key, params),
    }),
  };
});
vi.mock('@/lib/google-auth', async () => {
  const actual = await vi.importActual<typeof import('@/lib/google-auth')>('@/lib/google-auth');
  return { ...actual, readGoogleIdentitySummary: mocks.readGoogleIdentitySummary };
});
vi.mock('@/lib/account-password', async () => {
  const actual = await vi.importActual<typeof import('@/lib/account-password')>('@/lib/account-password');
  return {
    ...actual,
    readPasswordSelfServiceStatus: mocks.readPasswordSelfServiceStatus,
    changeAccountPassword: mocks.changeAccountPassword,
  };
});
vi.mock('@/lib/cli-auth', async () => {
  const actual = await vi.importActual<typeof import('@/lib/cli-auth')>('@/lib/cli-auth');
  return {
    ...actual,
    listCLISessions: mocks.listCLISessions,
    listSyncBindings: mocks.listSyncBindings,
    revokeCLISession: mocks.revokeCLISession,
    revokeSyncBinding: mocks.revokeSyncBinding,
    reauthorizeSyncBinding: mocks.reauthorizeSyncBinding,
  };
});

import { AccountSettingsModal } from '@/components/AccountSettingsModal';
import { PasswordSelfServiceError } from '@/lib/account-password';

const session = { id: 'session-1', client_name: 'lwc-sync CLI', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const binding = { binding_id: 'binding-1', host: 'https://auth.example.test', wiki_id: 'wiki-1', project_id: 'project-1', authorized_by: 'owner-1', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const getParagraph = (text: string) => screen.getByText((_content, element) => element?.tagName === 'P' && element.textContent === text);

beforeEach(() => {
  mocks.locale = 'en';
  mocks.userID = 'owner-1';
  mocks.listCLISessions.mockReset().mockResolvedValue([session]);
  mocks.listSyncBindings.mockReset().mockResolvedValue([binding]);
  mocks.revokeCLISession.mockReset().mockResolvedValue(undefined);
  mocks.revokeSyncBinding.mockReset().mockResolvedValue(undefined);
  mocks.reauthorizeSyncBinding.mockReset().mockResolvedValue({ ...binding, binding_id: 'binding-2' });
  mocks.readGoogleIdentitySummary.mockReset().mockResolvedValue({ primary_email: 'owner@example.test', linked_providers: [] });
  mocks.readPasswordSelfServiceStatus.mockReset().mockResolvedValue({ allowed: true, configured: true });
  mocks.changeAccountPassword.mockReset().mockResolvedValue(undefined);
  mocks.refreshAccessToken.mockReset().mockResolvedValue('fresh-web-token');
  vi.stubGlobal('confirm', vi.fn(() => true));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('LWC-346 account self-service', () => {
  it('lets a signed-in OAuth user optionally set an email password without an unknown old password', async () => {
    mocks.readPasswordSelfServiceStatus.mockImplementation(async () => ({
      allowed: true,
      configured: mocks.changeAccountPassword.mock.calls.length > 0,
    }));
    render(<AccountSettingsModal onClose={vi.fn()} />);

    const newPassword = await screen.findByLabelText('New password');
    expect(screen.queryByLabelText('Current password')).toBeNull();
    fireEvent.change(newPassword, { target: { value: 'first-password-431' } });
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'first-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Set password' }));

    await waitFor(() => expect(mocks.changeAccountPassword).toHaveBeenCalledWith(
      'web-access-token', 'first-password-431', 'first-password-431',
    ));
    expect(await screen.findByText('Password updated. You remain signed in.')).not.toBeNull();
    expect(screen.getByLabelText('Current password')).not.toBeNull();
  });

  it('requires and sends the current password for configured accounts, then clears password fields on success', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);

    const currentPassword = await screen.findByLabelText('Current password') as HTMLInputElement;
    const newPassword = screen.getByLabelText('New password') as HTMLInputElement;
    const confirmPassword = screen.getByLabelText('Confirm new password') as HTMLInputElement;
    fireEvent.change(currentPassword, { target: { value: 'old-password-431' } });
    fireEvent.change(newPassword, { target: { value: 'new-password-431' } });
    fireEvent.change(confirmPassword, { target: { value: 'new-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }));

    await waitFor(() => expect(mocks.changeAccountPassword).toHaveBeenCalledWith(
      'web-access-token', 'new-password-431', 'new-password-431', 'old-password-431',
    ));
    expect(await screen.findByText('Password updated. You remain signed in.')).not.toBeNull();
    expect(currentPassword.value).toBe('');
    expect(newPassword.value).toBe('');
    expect(confirmPassword.value).toBe('');
  });

  it.each([
    ['en', 'Enter your current password to change it.'],
    ['zh-TW', '請輸入目前密碼以變更密碼。'],
  ] as const)('shows localized current-password validation in %s', async (locale, message) => {
    mocks.locale = locale;
    render(<AccountSettingsModal onClose={vi.fn()} />);
    const currentPassword = await screen.findByLabelText(locale === 'en' ? 'Current password' : '目前密碼');
    const form = currentPassword.closest('form');
    if (!form) throw new Error('password form is missing');
    fireEvent.submit(form);

    expect(await screen.findByText(message)).not.toBeNull();
    expect(mocks.changeAccountPassword).not.toHaveBeenCalled();
  });

  it('localizes a rejected current-password proof in Traditional Chinese', async () => {
    mocks.locale = 'zh-TW';
    mocks.changeAccountPassword.mockRejectedValueOnce(new PasswordSelfServiceError('current_password_invalid', 'current password is incorrect'));
    render(<AccountSettingsModal onClose={vi.fn()} />);

    fireEvent.change(await screen.findByLabelText('目前密碼'), { target: { value: 'wrong-password' } });
    fireEvent.change(screen.getByLabelText('新密碼'), { target: { value: 'new-password-431' } });
    fireEvent.change(screen.getByLabelText('確認新密碼'), { target: { value: 'new-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: '變更密碼' }));

    expect(await screen.findByText('目前密碼不正確，請確認後再試一次。')).not.toBeNull();
  });

  it('clears password inputs on modal close and account switch', async () => {
    const onClose = vi.fn();
    const { rerender } = render(<AccountSettingsModal onClose={onClose} />);
    const currentPassword = await screen.findByLabelText('Current password') as HTMLInputElement;
    const newPassword = screen.getByLabelText('New password') as HTMLInputElement;
    fireEvent.change(currentPassword, { target: { value: 'old-password-431' } });
    fireEvent.change(newPassword, { target: { value: 'new-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Close account settings' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(currentPassword.value).toBe('');
    expect(newPassword.value).toBe('');

    fireEvent.change(currentPassword, { target: { value: 'another-old-password' } });
    mocks.userID = 'owner-2';
    rerender(<AccountSettingsModal onClose={onClose} />);
    await waitFor(() => expect((screen.getByLabelText('Current password') as HTMLInputElement).value).toBe(''));
  });

  it('uses Auth-qualified Demo status to suppress password controls', async () => {
    mocks.readPasswordSelfServiceStatus.mockResolvedValue({ allowed: false, configured: true });
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(await screen.findByText('Password setup is not available for this shared Demo account.')).not.toBeNull();
    expect(screen.queryByLabelText('New password')).toBeNull();
    expect(mocks.changeAccountPassword).not.toHaveBeenCalled();
  });

  it.each([
    ['en', 'Unable to link this Google account.', 200, {}],
    ['zh-TW', '無法連結這個 Google 帳號。', 200, { authorization_url: 'not a valid URL' }],
    ['en', 'Unable to link this Google account.', 503, {}],
    ['zh-TW', '無法連結這個 Google 帳號。', 502, { authorization_url: 'javascript:alert(1)' }],
  ] as const)('localizes explicit Google-link fallback reasons in %s without contacting a provider', async (locale, fallback, status, payload) => {
    mocks.locale = locale;
    mocks.readPasswordSelfServiceStatus.mockResolvedValue({ allowed: true, configured: false });
    const fetchMock = vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: async () => payload,
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: locale === 'en' ? 'Link Google' : '連結 Google' }));
    fireEvent.change(screen.getByLabelText(locale === 'en' ? 'Current password' : '目前密碼'), { target: { value: 'disposable-local-test' } });
    fireEvent.click(screen.getByRole('button', { name: locale === 'en' ? 'Continue' : '繼續' }));
    expect((await screen.findByRole('alert')).textContent).toContain(fallback);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toContain('/api/v1/auth/google/link/start');
  });

  it('keeps a backend Google-link message verbatim instead of translating it as a local fallback', async () => {
    mocks.locale = 'zh-TW';
    mocks.readPasswordSelfServiceStatus.mockResolvedValue({ allowed: true, configured: false });
    const backendMessage = 'Provider returned an unfamiliar refusal.';
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({ error: backendMessage }),
    });
    vi.stubGlobal('fetch', fetchMock);
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: '連結 Google' }));
    fireEvent.change(screen.getByLabelText('目前密碼'), { target: { value: 'disposable-local-test' } });
    fireEvent.click(screen.getByRole('button', { name: '繼續' }));
    expect((await screen.findByRole('alert')).textContent).toContain(backendMessage);
    expect(screen.getByRole('alert').textContent).not.toContain('無法連結這個 Google 帳號。');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('lets a Web user revoke an active CLI session', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);
    const revoke = await screen.findByRole('button', { name: 'Revoke session' });
    fireEvent.click(revoke);
    await waitFor(() => expect(mocks.revokeCLISession).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      'session-1',
    ));
    expect(screen.getByRole('status').textContent).toBe('CLI session revoked.');
  });

  it('lets a Web user revoke and explicitly replace a Project binding', async () => {
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke binding' }));
    await waitFor(() => expect(mocks.revokeSyncBinding).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      binding,
    ));
    fireEvent.click(screen.getByRole('button', { name: 'Explicitly reauthorize' }));
    await waitFor(() => expect(mocks.reauthorizeSyncBinding).toHaveBeenCalledWith(
      { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken },
      binding,
    ));
    expect(screen.getByRole('status').textContent).toBe('Sync binding reauthorized.');
  });

  it.each([
    ['en', 'Account settings', 'Close account settings'],
    ['zh-TW', '帳號設定', '關閉帳號設定'],
  ] as const)('localizes the dialog title and close accessible name in %s', async (locale, title, closeLabel) => {
    mocks.locale = locale;
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(await screen.findByRole('dialog', { name: title })).not.toBeNull();
    expect(screen.getByRole('button', { name: closeLabel })).not.toBeNull();
  });

  it('translates known statuses and keeps unknown session and binding statuses verbatim', async () => {
    mocks.listCLISessions.mockResolvedValue([
      { ...session, id: 'session-active', status: 'active' },
      { ...session, id: 'session-revoked', status: 'revoked' },
      { ...session, id: 'session-expired', status: 'expired' },
      { ...session, id: 'session-future', status: 'future-state' },
    ]);
    mocks.listSyncBindings.mockResolvedValue([
      { ...binding, project_id: 'project-active', status: 'active' },
      { ...binding, project_id: 'project-revoked', status: 'revoked' },
      { ...binding, project_id: 'project-future', status: 'future-state' },
    ]);
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(await screen.findByText((_content, element) => element?.tagName === 'P' && element.textContent === 'lwc-sync CLI · Active')).not.toBeNull();
    expect(getParagraph('lwc-sync CLI · Revoked')).not.toBeNull();
    expect(getParagraph('lwc-sync CLI · Expired')).not.toBeNull();
    expect(getParagraph('lwc-sync CLI · future-state')).not.toBeNull();
    expect(getParagraph('project-active · Active')).not.toBeNull();
    expect(getParagraph('project-revoked · Revoked')).not.toBeNull();
    expect(getParagraph('project-future · future-state')).not.toBeNull();
    expect(screen.getByText('session-future')).not.toBeNull();
  });

  it.each([
    ['en', 'Unable to load CLI sessions.'],
    ['zh-TW', '無法載入 CLI 工作階段。'],
  ] as const)('localizes fixed load fallback errors in %s and preserves backend error messages', async (locale, message) => {
    mocks.locale = locale;
    mocks.listCLISessions.mockRejectedValueOnce(null);
    render(<AccountSettingsModal onClose={vi.fn()} />);
    expect(await screen.findByText(message)).not.toBeNull();

    cleanup();
    mocks.locale = 'en';
    mocks.listCLISessions.mockResolvedValueOnce([session]);
    mocks.revokeCLISession.mockRejectedValueOnce(new Error('backend detail stays raw'));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke session' }));
    expect(await screen.findByText('backend detail stays raw')).not.toBeNull();
  });
});
