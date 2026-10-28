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
  listLLMProfiles: vi.fn(),
  getProjectLLMBinding: vi.fn(),
  saveProjectLLMBinding: vi.fn(),
  projects: [] as Array<{ id: string; name: string }>,
  locale: 'en' as 'en' | 'zh-TW',
  accessToken: 'web-access-token',
  userID: 'owner-1',
  sessionEpoch: 1,
  isDemoSession: false,
  refreshAccessToken: vi.fn(),
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ accessToken: mocks.accessToken, refreshAccessToken: mocks.refreshAccessToken, sessionEpoch: mocks.sessionEpoch, user: { id: mocks.userID, email: 'owner@example.test' }, isDemoSession: mocks.isDemoSession }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: null, projects: mocks.projects }) }));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    listLLMProfiles: mocks.listLLMProfiles,
    getProjectLLMBinding: mocks.getProjectLLMBinding,
    saveProjectLLMBinding: mocks.saveProjectLLMBinding,
  };
});
vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  const t = (key: string, params?: Record<string, string | number>) => actual.translate(mocks.locale, key, params);
  return {
    ...actual,
    useLocale: () => ({ t }),
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
import { PasswordSelfServiceError, type PasswordSelfServiceAuthContext } from '@/lib/account-password';

const session = { id: 'session-1', client_name: 'lwc-sync CLI', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const binding = { binding_id: 'binding-1', host: 'https://auth.example.test', wiki_id: 'wiki-1', project_id: 'project-1', authorized_by: 'owner-1', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const getParagraph = (text: string) => screen.getByText((_content, element) => element?.tagName === 'P' && element.textContent === text);

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.locale = 'en';
  mocks.userID = 'owner-1';
  mocks.accessToken = 'web-access-token';
  mocks.sessionEpoch = 1;
  mocks.isDemoSession = false;
  mocks.listCLISessions.mockReset().mockResolvedValue([session]);
  mocks.listSyncBindings.mockReset().mockResolvedValue([binding]);
  mocks.revokeCLISession.mockReset().mockResolvedValue(undefined);
  mocks.revokeSyncBinding.mockReset().mockResolvedValue(undefined);
  mocks.reauthorizeSyncBinding.mockReset().mockResolvedValue({ ...binding, binding_id: 'binding-2' });
  mocks.readGoogleIdentitySummary.mockReset().mockResolvedValue({ primary_email: 'owner@example.test', linked_providers: [] });
  mocks.readPasswordSelfServiceStatus.mockReset().mockResolvedValue({ allowed: true, configured: true });
  mocks.changeAccountPassword.mockReset().mockResolvedValue(undefined);
  mocks.listLLMProfiles.mockReset().mockResolvedValue([]);
  mocks.getProjectLLMBinding.mockReset().mockResolvedValue({ source: 'shared', revision: 1 });
  mocks.saveProjectLLMBinding.mockReset().mockResolvedValue({ source: 'shared', revision: 2 });
  mocks.projects = [];
  mocks.refreshAccessToken.mockReset().mockResolvedValue('fresh-web-token');
  vi.stubGlobal('confirm', vi.fn(() => true));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('LWC-346 account self-service', () => {
  it('does not enumerate or expose personal BYOK credentials in a shared Demo session', async () => {
    mocks.isDemoSession = true;
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(await screen.findByRole('dialog', { name: 'Account settings' })).not.toBeNull();
    expect(screen.queryByRole('heading', { name: 'My BYOK profiles' })).toBeNull();
    expect(mocks.listLLMProfiles).not.toHaveBeenCalled();
  });

  it('isolates the three settings tabs and preserves provider and assignment drafts while clearing secrets', async () => {
    mocks.projects = [{ id: 'project-1', name: 'Travel notes' }];
    render(<AccountSettingsModal onClose={vi.fn()} />);

    const accountTab = screen.getByRole('tab', { name: 'Account & security' });
    const providerTab = screen.getByRole('tab', { name: 'LLM Provider' });
    const connectionsTab = screen.getByRole('tab', { name: 'Connections & sync' });
    const accountPanel = document.getElementById('account-settings-panel-account') as HTMLDivElement;
    const providerPanel = document.getElementById('account-settings-panel-provider') as HTMLDivElement;
    const connectionsPanel = document.getElementById('account-settings-panel-connections') as HTMLDivElement;
    expect(accountTab.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(accountTab);
    expect(accountPanel.hidden).toBe(false);
    expect(providerPanel.hidden).toBe(true);
    expect(connectionsPanel.hidden).toBe(true);
    expect(screen.queryByRole('heading', { name: 'CLI sessions' })).toBeNull();

    fireEvent.click(providerTab);
    expect(providerPanel.hidden).toBe(false);
    expect(accountPanel.hidden).toBe(true);
    expect(await screen.findByText('Project: Travel notes')).not.toBeNull();
    const source = await screen.findByRole('combobox', { name: 'Service source' }) as HTMLSelectElement;
    fireEvent.change(source, { target: { value: 'personal' } });
    const providerName = screen.getByRole('textbox', { name: 'Provider name' }) as HTMLInputElement;
    const apiKey = screen.getByLabelText(/API key/) as HTMLInputElement;
    fireEvent.change(providerName, { target: { value: 'Unsaved provider' } });
    fireEvent.change(apiKey, { target: { value: 'unsaved-secret' } });

    fireEvent.click(accountTab);
    const currentPassword = await screen.findByLabelText('Current password') as HTMLInputElement;
    fireEvent.change(currentPassword, { target: { value: 'temporary-password' } });
    fireEvent.click(providerTab);
    expect(providerName.value).toBe('Unsaved provider');
    await waitFor(() => expect(apiKey.value).toBe(''));
    expect(currentPassword.value).toBe('');
    expect(source.value).toBe('personal');
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Cancel changes' }));
    expect(source.value).toBe('shared');
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();

    fireEvent.click(connectionsTab);
    expect(connectionsPanel.hidden).toBe(false);
    expect(screen.getByRole('heading', { name: 'CLI sessions' })).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Sync bindings' })).not.toBeNull();
  });

  it('supports keyboard tab navigation, opens directly on LLM Provider, and restores focus when closed', async () => {
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    trigger.focus();
    const onClose = vi.fn();
    const view = render(<AccountSettingsModal onClose={onClose} focusBYOK />);

    const providerTab = screen.getByRole('tab', { name: 'LLM Provider' });
    const connectionsTab = screen.getByRole('tab', { name: 'Connections & sync' });
    const accountTab = screen.getByRole('tab', { name: 'Account & security' });
    expect(providerTab.getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('heading', { name: 'LLM Provider' })).not.toBeNull();
    providerTab.focus();
    fireEvent.keyDown(providerTab, { key: 'ArrowRight' });
    expect(connectionsTab.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(connectionsTab);
    fireEvent.keyDown(connectionsTab, { key: 'ArrowLeft' });
    await waitFor(() => expect(document.activeElement).toBe(providerTab));
    expect(providerTab.getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(providerTab, { key: 'ArrowLeft' });
    expect(accountTab.getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(accountTab);
    fireEvent.keyDown(accountTab, { key: 'End' });
    expect(connectionsTab.getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(connectionsTab, { key: 'Home' });
    expect(accountTab.getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledOnce();

    view.unmount();
    expect(document.activeElement).toBe(trigger);
    trigger.remove();
  });

  it('uses the frozen Chinese account and provider labels', async () => {
    mocks.locale = 'zh-TW';
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(screen.getByRole('tab', { name: '帳號與安全' })).not.toBeNull();
    expect(screen.getByRole('tab', { name: 'LLM Provider' })).not.toBeNull();
    expect(screen.getByRole('tab', { name: '連線與同步' })).not.toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'LLM Provider' }));
    expect(await screen.findByRole('heading', { name: 'LLM Provider' })).not.toBeNull();
  });

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
      expect.objectContaining({ accessToken: 'web-access-token', refreshAccessToken: expect.any(Function) }), 'first-password-431', 'first-password-431',
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
      expect.objectContaining({ accessToken: 'web-access-token', refreshAccessToken: expect.any(Function) }), 'new-password-431', 'new-password-431', 'old-password-431',
    ));
    expect(await screen.findByText('Password updated. You remain signed in.')).not.toBeNull();
    expect(currentPassword.value).toBe('');
    expect(newPassword.value).toBe('');
    expect(confirmPassword.value).toBe('');
  });

  it('keeps a configured-account password change successful when the optional status refresh rejects', async () => {
    mocks.readPasswordSelfServiceStatus
      .mockResolvedValueOnce({ allowed: true, configured: true })
      .mockRejectedValueOnce(new TypeError('Failed to fetch'));
    render(<AccountSettingsModal onClose={vi.fn()} />);

    const currentPassword = await screen.findByLabelText('Current password') as HTMLInputElement;
    const newPassword = screen.getByLabelText('New password') as HTMLInputElement;
    const confirmPassword = screen.getByLabelText('Confirm new password') as HTMLInputElement;
    fireEvent.change(currentPassword, { target: { value: 'old-password-431' } });
    fireEvent.change(newPassword, { target: { value: 'new-password-431' } });
    fireEvent.change(confirmPassword, { target: { value: 'new-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }));

    await waitFor(() => expect(mocks.changeAccountPassword).toHaveBeenCalledWith(
      expect.objectContaining({ accessToken: 'web-access-token', refreshAccessToken: expect.any(Function) }), 'new-password-431', 'new-password-431', 'old-password-431',
    ));
    await waitFor(() => expect(mocks.readPasswordSelfServiceStatus).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('Password updated. You remain signed in.')).not.toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(currentPassword.value).toBe('');
    expect(newPassword.value).toBe('');
    expect(confirmPassword.value).toBe('');
  });

  it('moves OAuth setup to configured after POST success despite a failed refresh, then requires proof next time', async () => {
    mocks.readPasswordSelfServiceStatus
      .mockResolvedValueOnce({ allowed: true, configured: false })
      .mockRejectedValueOnce(new Error('password settings unavailable (503)'));
    render(<AccountSettingsModal onClose={vi.fn()} />);

    const firstPassword = await screen.findByLabelText('New password') as HTMLInputElement;
    expect(screen.queryByLabelText('Current password')).toBeNull();
    fireEvent.change(firstPassword, { target: { value: 'first-password-431' } });
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'first-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Set password' }));

    await waitFor(() => expect(mocks.changeAccountPassword).toHaveBeenNthCalledWith(
      1, expect.objectContaining({ accessToken: 'web-access-token', refreshAccessToken: expect.any(Function) }), 'first-password-431', 'first-password-431',
    ));
    expect(await screen.findByText('Password updated. You remain signed in.')).not.toBeNull();
    const currentPassword = await screen.findByLabelText('Current password') as HTMLInputElement;
    expect(currentPassword.value).toBe('');
    expect(firstPassword.value).toBe('');
    expect((screen.getByLabelText('Confirm new password') as HTMLInputElement).value).toBe('');
    expect(screen.queryByRole('alert')).toBeNull();

    fireEvent.change(currentPassword, { target: { value: 'first-password-431' } });
    fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'second-password-431' } });
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'second-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
    await waitFor(() => expect(mocks.changeAccountPassword).toHaveBeenNthCalledWith(
      2, expect.objectContaining({ accessToken: 'web-access-token', refreshAccessToken: expect.any(Function) }), 'second-password-431', 'second-password-431', 'first-password-431',
    ));
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

  it.each([
    ['account switch', 'owner-2', 'other-account-token', 2],
    ['same-account session rotation', 'owner-1', 'new-session-token', 2],
  ] as const)('does not refresh through a password request context after %s', async (_change, userID, accessToken, sessionEpoch) => {
    let capturedContext: PasswordSelfServiceAuthContext | undefined;
    mocks.changeAccountPassword.mockImplementationOnce(async (context: PasswordSelfServiceAuthContext) => {
      capturedContext = context;
    });
    const { rerender } = render(<AccountSettingsModal onClose={vi.fn()} />);
    fireEvent.change(await screen.findByLabelText('Current password'), { target: { value: 'old-password-431' } });
    fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'new-password-431' } });
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'new-password-431' } });
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
    await waitFor(() => expect(capturedContext).toBeDefined());

    mocks.userID = userID;
    mocks.accessToken = accessToken;
    mocks.sessionEpoch = sessionEpoch;
    rerender(<AccountSettingsModal onClose={vi.fn()} />);

    await expect(capturedContext?.refreshAccessToken()).resolves.toBeNull();
    expect(mocks.refreshAccessToken).not.toHaveBeenCalled();
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
    fireEvent.click(screen.getByRole('tab', { name: 'Connections & sync' }));
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
    fireEvent.click(screen.getByRole('tab', { name: 'Connections & sync' }));
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
    fireEvent.click(screen.getByRole('tab', { name: 'Connections & sync' }));

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
    fireEvent.click(screen.getByRole('tab', { name: 'Connections & sync' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke session' }));
    expect(await screen.findByText('backend detail stays raw')).not.toBeNull();
  });
});
