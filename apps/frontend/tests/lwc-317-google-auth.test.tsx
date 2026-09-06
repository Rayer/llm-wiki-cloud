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
  refreshAccessToken: vi.fn(),
  beginGoogleLink: vi.fn(),
  readGoogleLinkCompletion: vi.fn(),
  confirmGoogleLink: vi.fn(),
  cancelGoogleLink: vi.fn(),
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
  return {
    ...actual,
    startGoogleLogin: mocks.startGoogleLogin,
    beginGoogleLink: mocks.beginGoogleLink,
    readGoogleLinkCompletion: mocks.readGoogleLinkCompletion,
    confirmGoogleLink: mocks.confirmGoogleLink,
    cancelGoogleLink: mocks.cancelGoogleLink,
  };
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

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  mocks.getPublicConfig.mockResolvedValue({ registration_enabled: false });
  mocks.startGoogleLogin.mockReset();
  mocks.signIn.mockResolvedValue(undefined);
  mocks.signInAsDemo.mockResolvedValue(undefined);
  mocks.refreshAccessToken.mockResolvedValue('google-access-token');
  mocks.beginGoogleLink.mockResolvedValue(undefined);
  mocks.readGoogleLinkCompletion.mockResolvedValue({
    status: 'confirmation_required',
    confirmation_id: 'opaque-confirmation-id',
    provider: 'google',
    provider_email: 'google@example.com',
    provider_email_verified: true,
    current_email: 'primary@example.com',
  });
  mocks.confirmGoogleLink.mockResolvedValue({ status: 'linked' });
  mocks.cancelGoogleLink.mockResolvedValue({ status: 'cancelled' });
  mocks.pathname = '/login';
  mocks.token = null;
  mocks.user = null;
  mocks.sessionEpoch = 0;
  mocks.currentProject = { id: 'default', name: 'Default Project' };
  mocks.projects = [{ id: 'default', name: 'Default Project' }];
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
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

  it('renders a bounded completion state and refreshes the normal LWC session', async () => {
    mocks.refreshAccessToken.mockResolvedValue('fresh-lwc-token');
    render(<GoogleCompletionClient />);
    expect(screen.getByText('GoogleCompletion.completing')).toBeDefined();
    await waitFor(() => expect(mocks.refreshAccessToken).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.replace).toHaveBeenCalledWith('/'));
    expect(window.location.search).toBe('');
    expect(window.location.hash).toBe('');
  });

  it('shows copyable opaque support reference for a non-success completion', async () => {
    mocks.refreshAccessToken.mockResolvedValue(null);
    render(<GoogleCompletionClient />);
    await waitFor(() => expect(screen.getByRole('alert')).toBeDefined());
    expect(screen.getByText('GoogleCompletion.supportReference')).toBeDefined();
    expect(screen.getByRole('button', { name: 'GoogleCompletion.copyReference' })).toBeDefined();
  });

  it('shows the exact cancellation copy and keeps cancellation reference copyable', async () => {
    mocks.token = 'session-token';
    sessionStorage.setItem('lwc-google-link-intent', '1');
    render(<GoogleCompletionClient />);
    await screen.findByText('GoogleCompletion.confirmTitle');
    fireEvent.click(screen.getByRole('button', { name: 'GoogleCompletion.cancel' }));
    await waitFor(() => expect(screen.getByText('已取消使用 Google 登入。')).toBeDefined());
    expect(screen.getByRole('button', { name: 'GoogleCompletion.copyReference' })).toBeDefined();
    expect(mocks.replace).not.toHaveBeenCalled();
  });

  it('requires current password and confirms both primary and Google display emails', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    render(<AccountSettingsModal onClose={vi.fn()} />);

    expect(screen.getByText('primary@example.com')).toBeDefined();
    expect(screen.getByText('AccountSettings.googleNotLinked')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.linkGoogle' }));
    const password = screen.getByLabelText('AccountSettings.currentPassword');
    fireEvent.change(password, { target: { value: 'current-password' } });
    fireEvent.click(screen.getByRole('button', { name: 'AccountSettings.continue' }));
    await waitFor(() => expect(mocks.beginGoogleLink).toHaveBeenCalledWith('current-password'));

    sessionStorage.setItem('lwc-google-link-intent', '1');
    render(<GoogleCompletionClient />);
    await screen.findByText('GoogleCompletion.confirmTitle');
    expect(screen.getAllByText('primary@example.com').length).toBe(2);
    expect(screen.getByText('google@example.com')).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'GoogleCompletion.confirm' }));
    await waitFor(() => expect(mocks.confirmGoogleLink).toHaveBeenCalledWith('session-token', 'opaque-confirmation-id'));
  });

  it('shows the provisional Default Project rename prompt without making login depend on rename', async () => {
    mocks.token = 'session-token';
    mocks.user = { id: 'user-1', email: 'primary@example.com' };
    mocks.getProjects.mockResolvedValue(mocks.projects);
    mocks.renameProject.mockRejectedValue(new Error('rename unavailable'));
    const { Shell } = await import('@/components/Shell');
    render(<Shell><div>workspace</div></Shell>);
    const input = await screen.findByRole('textbox', { name: 'Project name' });
    expect((input as HTMLInputElement).value).toBe('Default Project');
    fireEvent.change(input, { target: { value: 'My Project' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));
    await waitFor(() => expect(screen.getByText('rename unavailable')).toBeDefined());
    expect(screen.getByText('workspace')).toBeDefined();
  });
});
