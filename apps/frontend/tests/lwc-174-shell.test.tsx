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
  getProjects: vi.fn(),
  renameProject: vi.fn(),
  getStatus: vi.fn(),
  replace: vi.fn(),
  pathname: '/',
  token: 'auth-token' as string | null,
  user: { id: 'user-1', email: 'owner@example.com', role: 'owner' },
  isDemoSession: false,
  logout: vi.fn(),
}));

vi.mock('next/navigation', () => ({
  usePathname: () => mocks.pathname,
  useRouter: () => ({ replace: mocks.replace }),
}));

vi.mock('@/lib/projects', async () => {
  const actual = await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects');
  return {
    ...actual,
    getProjects: mocks.getProjects,
    renameProject: mocks.renameProject,
  };
});

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getStatus: mocks.getStatus };
});

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: mocks.token,
    access_token: mocks.token,
    user: mocks.user,
    hydrated: true,
    isAuthenticated: Boolean(mocks.token),
    isDemoSession: mocks.isDemoSession,
    login: async () => undefined,
    register: async () => undefined,
    logout: mocks.logout,
    refreshAccessToken: async () => mocks.token,
  }),
}));

import { Shell } from '@/components/Shell';

function apiStatus() {
  return {
    sourcesCount: 0,
    conceptsCount: 0,
    rawCount: 0,
    suggestedQueries: [],
    lastExecution: null,
    raw: {},
  };
}

function renderShell() {
  return render(
    <Shell>
      <div>workspace content</div>
    </Shell>,
  );
}

beforeEach(() => {
  mocks.getProjects.mockResolvedValue([
    { id: 'project-a', name: 'Project Alpha' },
    { id: 'project-b', name: 'Project Beta' },
  ]);
  mocks.renameProject.mockResolvedValue('Renamed project');
  mocks.getStatus.mockResolvedValue(apiStatus());
  mocks.pathname = '/';
  mocks.token = 'auth-token';
  mocks.user = { id: 'user-1', email: 'owner@example.com', role: 'owner' };
  mocks.isDemoSession = false;
  mocks.logout.mockReset().mockResolvedValue(undefined);
  localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  localStorage.clear();
});

describe('LWC-174 production Shell rename behavior', () => {
  it('exposes a skip link to main content', async () => {
    renderShell();
    const skip = await screen.findByRole('link', { name: '跳到主要內容' });
    expect(skip.getAttribute('href')).toBe('#main-content');
    expect(document.getElementById('main-content')).not.toBeNull();
  });

  it('routes Project Settings from the sidebar and hides it in the trial Demo session', async () => {
    localStorage.setItem('locale', 'en');
    renderShell();
    expect(await screen.findByRole('link', { name: 'Project settings' })).toHaveProperty('href', expect.stringContaining('/profile'));

    cleanup();
    mocks.isDemoSession = true;
    renderShell();
    await screen.findByRole('button', { name: 'Project Alpha' });
    expect(screen.queryByRole('link', { name: 'Project settings' })).toBeNull();
  });

  it.each([
    ['en', 'Account settings', 'Sign out', 'Close account settings'],
    ['zh-TW', '帳號設定', '登出', '關閉帳號設定'],
  ] as const)('shows localized account actions and opens settings in %s', async (locale, settingsLabel, logoutLabel, closeLabel) => {
    localStorage.setItem('locale', locale);
    renderShell();

    const settings = await screen.findByRole('button', { name: settingsLabel });
    const logout = screen.getByRole('button', { name: logoutLabel });
    expect(settings.tagName).toBe('BUTTON');
    expect(logout.tagName).toBe('BUTTON');
    expect(settings.className).toContain('border border-white/10 bg-white/5');
    expect(settings.className).toContain('focus-visible:outline');
    expect(settings.className).toContain('active:bg-white/15');
    expect(settings.parentElement?.className).toContain('mt-2 flex flex-wrap gap-2');

    fireEvent.click(logout);
    await waitFor(() => expect(mocks.logout).toHaveBeenCalledOnce());

    settings.focus();
    expect(document.activeElement).toBe(settings);
    fireEvent.click(settings);
    const dialog = await screen.findByRole('dialog', { name: settingsLabel });
    expect(dialog).not.toBeNull();
    fireEvent.click(screen.getByRole('button', { name: closeLabel }));
    await waitFor(() => expect(document.activeElement).toBe(settings));
  });

  it('hides rename on the admin route', async () => {
    mocks.pathname = '/admin';
    mocks.user = { id: 'admin-1', email: 'admin@example.com', role: 'admin' };

    renderShell();

    await screen.findByRole('button', { name: 'Project Alpha' });
    expect(screen.queryByRole('button', { name: 'Rename project' })).toBeNull();
  });

  it('keeps the project switcher without a sidebar rename action', async () => {
    renderShell();
    await screen.findByRole('button', { name: 'Project Alpha' });
    expect(screen.queryByRole('button', { name: /Rename project|重新命名專案/ })).toBeNull();
  });
});
