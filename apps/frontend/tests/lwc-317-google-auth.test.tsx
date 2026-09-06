import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const { cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');

const mocks = vi.hoisted(() => ({
  getPublicConfig: vi.fn(),
  signIn: vi.fn(),
  signInAsDemo: vi.fn(),
  startGoogleLogin: vi.fn(),
  fetch: vi.fn(),
  refreshAccessToken: vi.fn(),
  pathname: '/login',
  token: null as string | null,
  user: null as { id: string; email: string; role?: string } | null,
  sessionEpoch: 0,
  replace: vi.fn(),
  currentProject: { id: 'default', name: 'Default Project' },
  projects: [{ id: 'default', name: 'Default Project' }],
  renameProject: vi.fn(),
  getProjects: vi.fn(),
  getStatus: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getPublicConfig: mocks.getPublicConfig, getStatus: mocks.getStatus };
});

vi.mock('@/lib/google-auth', async () => {
  const actual = await vi.importActual<typeof import('@/lib/google-auth')>('@/lib/google-auth');
  return { ...actual, startGoogleLogin: mocks.startGoogleLogin };
});

vi.mock('@/lib/i18n', () => ({
  useLocale: () => ({ t: (key: string) => key }),
  useT: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    hydrated: true,
    token: mocks.token,
    user: mocks.user,
    isDemoSession: false,
    projectsLoading: false,
    projectsError: '',
    navCounts: { sources: null, concepts: null },
    refreshNavCounts: vi.fn(),
    refreshProjects: vi.fn(),
    selectProject: vi.fn(),
    openNewProject: vi.fn(),
    closeNewProject: vi.fn(),
    signOut: vi.fn(),
    addProject: vi.fn(),
    currentProject: mocks.currentProject,
    projects: mocks.projects,
    loginOpen: mocks.pathname !== '/login' || !mocks.token,
    signIn: mocks.signIn,
    signInAsDemo: mocks.signInAsDemo,
    renameProject: mocks.renameProject,
    getProjects: mocks.getProjects,
  }),
  WorkspaceProvider: ({ children }: { children: React.ReactNode }) => children,
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: mocks.token,
    access_token: mocks.token,
    user: mocks.user,
    hydrated: true,
    isAuthenticated: Boolean(mocks.token),
    isDemoSession: false,
    refreshAccessToken: mocks.refreshAccessToken,
    sessionEpoch: mocks.sessionEpoch,
  }),
}));

vi.mock('next/navigation', () => ({
  usePathname: () => mocks.pathname,
  useRouter: () => ({ replace: mocks.replace }),
}));

import { LoginModal } from '@/components/LoginModal';
import { AccountSettingsModal } from '@/components/AccountSettingsModal';
import { GoogleCompletionClient } from '@/components/GoogleCompletionClient';

function jsonResponse(payload: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: vi.fn().mockResolvedValue(payload) };
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  mocks.getPublicConfig.mockResolvedValue({ registration_enabled: false });
  mocks.startGoogleLogin.mockReset();
  vi.stubGlobal('fetch', mocks.fetch);
  mocks.fetch.mockReset();
  mocks.signIn.mockResolvedValue(undefined);
  mocks.signInAsDemo.mockResolvedValue(undefined);
  mocks.refreshAccessToken.mockResolvedValue('google-access-token');
  mocks.pathname = '/login';
  mocks.token = null;
  mocks.user = null;
  mocks.sessionEpoch = 0;
  mocks.currentProject = { id: 'default', name: 'Default Project' };
  mocks.projects = [{ id: 'default', name: 'Default Project' }];
  mocks.fetch.mockResolvedValue(jsonResponse({ status: 'success', error: '', support_ref: '', jit_provisioned: false }));
  try {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...window.location, assign: vi.fn() },
    });
  } catch {
    // jsdom may expose location as non-configurable; login start remains mocked below.
  }
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

describe('LWC-317 Google auth production components', () => {
  it('keeps one Continue with Google action visible when registration is disabled', async () => {
    render(<LoginModal />);
    const google = await screen.findByRole('button', { name: 'Login.continueWithGoogle' });
    expect(google).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Login.googleSignUp' })).toBeNull();

    fireEvent.click(google);
    expect(mocks.startGoogleLogin).toHaveBeenCalledTimes(1);
  });

  it('uses the browser GET login-start contract', async () => {
    const actual = await vi.importActual<typeof import('@/lib/google-auth')>('@/lib/google-auth');
    actual.startGoogleLogin();
    expect(window.location.assign).toHaveBeenCalledWith(expect.stringContaining('/api/v1/auth/google/login/start'));
  });

  it('renders a bounded completion state and refreshes the normal LWC session', async () => {
    mocks.refreshAccessToken.mockResolvedValue('fresh-lwc-token');
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'success', error: '', support_ref: '', jit_provisioned: false }));
    render(<GoogleCompletionClient />);
    expect(screen.getByText('GoogleCompletion.completing')).toBeDefined();
    await waitFor(() => expect(mocks.refreshAccessToken).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith('/'));
    expect(mocks.fetch).toHaveBeenCalledWith(expect.stringContaining('/api/v1/auth/google/complete'), { credentials: 'include' });
    expect(window.location.search).toBe('');
    expect(window.location.hash).toBe('');
  });

  it('forwards the backend JIT signal without putting it in the URL or storage', async () => {
    mocks.token = 'fresh-lwc-token';
    mocks.user = { id: 'jit-user', email: 'jit@example.com' };
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'success', error: '', support_ref: '', jit_provisioned: true }));
    const onJit = vi.fn();
    window.addEventListener('lwc-google-jit-completed', onJit);
    render(<GoogleCompletionClient />);
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith('/'));
    expect(onJit).toHaveBeenCalledTimes(1);
    expect(window.location.search).toBe('');
    expect(sessionStorage.length).toBe(0);
    window.removeEventListener('lwc-google-jit-completed', onJit);
  });

  it('consumes the one-time completion result once under React effect replay', async () => {
    mocks.token = 'fresh-lwc-token';
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'success', error: '', support_ref: '', jit_provisioned: false }));
    render(<React.StrictMode><GoogleCompletionClient /></React.StrictMode>);
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith('/'));
    expect(mocks.fetch).toHaveBeenCalledTimes(1);
  });

  it('shows copyable opaque support reference for a non-success completion', async () => {
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'failure', error: 'backend failure', support_ref: 'backend-ref-1234', jit_provisioned: false }));
    render(<GoogleCompletionClient />);
    await waitFor(() => expect(screen.getByRole('alert')).toBeDefined());
    expect(screen.getByText('backend failure')).toBeDefined();
    expect(screen.getByText('backend-ref-1234')).toBeDefined();
    expect(screen.getByText('GoogleCompletion.supportReference')).toBeDefined();
    expect(screen.getByRole('button', { name: 'GoogleCompletion.copyReference' })).toBeDefined();
  });

  it('shows the exact cancellation copy and keeps cancellation reference copyable', async () => {
    mocks.token = 'session-token';
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'cancelled', error: '已取消使用 Google 登入。', support_ref: 'cancel-ref-1234', jit_provisioned: false }));
    render(<GoogleCompletionClient />);
    await waitFor(() => expect(screen.getByText('已取消使用 Google 登入。')).toBeDefined());
    expect(screen.getByText('cancel-ref-1234')).toBeDefined();
    expect(screen.getByRole('button', { name: 'GoogleCompletion.copyReference' })).toBeDefined();
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it('cancels explicit linking through the backend and preserves the current session', async () => {
    mocks.token = 'session-token';
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'confirmation_required', error: '', support_ref: '', jit_provisioned: false }));
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'confirmation_required', confirmation_id: 'opaque-confirmation-id', provider: 'google', provider_email: 'google@example.com', provider_email_verified: true, current_email: 'primary@example.com' }));
    render(<GoogleCompletionClient />);
    await screen.findByText('GoogleCompletion.confirmTitle');
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'cancelled', error: '已取消使用 Google 登入。', support_ref: 'backend-cancel-ref' }));
    fireEvent.click(screen.getByRole('button', { name: 'GoogleCompletion.cancel' }));
    await waitFor(() => expect(screen.getByText('已取消使用 Google 登入。')).toBeDefined());
    expect(screen.getByText('backend-cancel-ref')).toBeDefined();
    expect(mocks.replace).not.toHaveBeenCalled();
    expect(mocks.fetch).toHaveBeenLastCalledWith(
      expect.stringContaining('/api/v1/auth/google/link/cancel'),
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Authorization: 'Bearer session-token' }) }),
    );
  });

  it('requires current password and confirms both primary and Google display emails', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ primary_email: 'primary@example.com', linked_providers: [] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(screen.getByText('primary@example.com')).toBeDefined();
    expect(screen.getByText('AccountSettings.googleNotLinked')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.linkGoogle' }));
    const password = screen.getByLabelText('AccountSettings.currentPassword');
    fireEvent.change(password, { target: { value: 'current-password' } });
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ authorization_url: 'https://accounts.google.com/o/oauth2/auth?state=opaque-state&prompt=select_account' }));
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.continue' }));
    await waitFor(() => expect(mocks.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/auth/google/link/start'),
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ Authorization: 'Bearer session-token' }),
        body: JSON.stringify({ current_password: 'current-password' }),
      }),
    ));
    expect(window.location.assign).toHaveBeenCalledWith(expect.stringContaining('https://accounts.google.com/'));
    expect(window.location.assign).not.toHaveBeenCalledWith(expect.stringContaining('current-password'));
    expect(sessionStorage.length).toBe(0);

    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'confirmation_required', error: '', support_ref: '', jit_provisioned: false }));
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ status: 'confirmation_required', confirmation_id: 'opaque-confirmation-id', provider: 'google', provider_email: 'google@example.com', provider_email_verified: true, current_email: 'primary@example.com' }));
    render(<GoogleCompletionClient />);
    await screen.findByText('GoogleCompletion.confirmTitle');
    expect(screen.getAllByText('primary@example.com').length).toBe(2);
    expect(screen.getByText('google@example.com')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'GoogleCompletion.confirm' }));
    await waitFor(() => expect(mocks.fetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/auth/google/link/confirm'),
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ Authorization: 'Bearer session-token' }),
        body: JSON.stringify({ confirmation_id: 'opaque-confirmation-id' }),
      }),
    ));
  });

  it('hydrates a returning linked Google display email from the identity summary', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    mocks.fetch.mockResolvedValueOnce(jsonResponse({ primary_email: 'primary@example.com', linked_providers: [{ provider: 'google', provider_email: 'returning-google@example.com', provider_email_verified: true }] }));
    render(<AccountSettingsModal onClose={vi.fn()} />);
    await waitFor(() => expect(screen.getByText('returning-google@example.com')).toBeDefined());
    expect(mocks.fetch).toHaveBeenCalledWith(expect.stringContaining('/api/v1/auth/google/identity'), expect.objectContaining({ headers: expect.objectContaining({ Authorization: 'Bearer session-token' }) }));
  });

  it('does not infer JIT onboarding from a Default Project name', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    const { Shell } = await import('@/components/Shell');
    render(<Shell><div>workspace</div></Shell>);
    await waitFor(() => expect(screen.getByText('workspace')).toBeDefined());
    expect(screen.queryByRole('textbox', { name: 'Project name' })).toBeNull();
  });

  it('shows the provisional Default Project rename prompt without making login depend on rename', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    mocks.getProjects.mockResolvedValue(mocks.projects);
    mocks.renameProject.mockRejectedValue(new Error('rename unavailable'));
    const { Shell } = await import('@/components/Shell');
    render(<Shell><div>workspace</div></Shell>);
    window.dispatchEvent(new CustomEvent('lwc-google-jit-completed', { detail: { userId: 'user-1' } }));
    const input = await screen.findByRole('textbox', { name: 'Project name' });
    expect((input as HTMLInputElement).value).toBe('Default Project');
    fireEvent.change(input, { target: { value: 'My Project' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));
    await waitFor(() => expect(screen.getByText('rename unavailable')).toBeDefined());
    expect(screen.getByText('workspace')).toBeDefined();
  });
});
