import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installShellIntersectionObserver } from './shell-intersection-observer-helper';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const { act, cleanup, fireEvent, render, screen, waitFor, within } = await import('@testing-library/react');

const mocks = vi.hoisted(() => ({
  bootstrapDefaultProject: vi.fn(),
  getProjects: vi.fn(),
  createProject: vi.fn(),
  renameProject: vi.fn(),
  getStatus: vi.fn(),
  replace: vi.fn(),
  push: vi.fn(),
  pathname: '/',
  token: 'auth-token' as string | null,
  user: { id: 'user-1', email: 'owner@example.com', role: 'owner' },
  isDemoSession: false,
  logout: vi.fn(),
}));

vi.mock('next/navigation', () => ({
  usePathname: () => mocks.pathname,
  useRouter: () => ({ replace: mocks.replace, push: mocks.push }),
}));

vi.mock('@/lib/projects', async () => {
  const actual = await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects');
  return {
    ...actual,
    bootstrapDefaultProject: mocks.bootstrapDefaultProject,
    getProjects: mocks.getProjects,
    createProject: mocks.createProject,
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
import { useWorkspace } from '@/components/WorkspaceProvider';
import { recordRequestIDHeader } from '@/lib/request-ids';

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

function BackgroundRefreshProbe() {
  const { currentProject, refreshProjects } = useWorkspace();
  return (
    <>
      <button type="button" onClick={() => void refreshProjects(true)}>refresh mounted workspace</button>
      <p data-testid="mounted-current-project-id">{currentProject?.id ?? ''}</p>
      <div>workspace content</div>
    </>
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

let observers: ReturnType<typeof installShellIntersectionObserver>;
beforeEach(() => {
  observers = installShellIntersectionObserver();
  mocks.bootstrapDefaultProject.mockReset().mockResolvedValue('default');
  mocks.getProjects.mockReset().mockResolvedValue([
    { id: 'project-a', name: 'Project Alpha' },
    { id: 'project-b', name: 'Project Beta' },
  ]);
  mocks.createProject.mockReset().mockResolvedValue({ id: 'created', name: 'Created' });
  mocks.renameProject.mockResolvedValue('Renamed project');
  mocks.getStatus.mockResolvedValue(apiStatus());
  mocks.pathname = '/';
  mocks.token = 'auth-token';
  mocks.user = { id: 'user-1', email: 'owner@example.com', role: 'owner' };
  mocks.isDemoSession = false;
  mocks.logout.mockReset().mockResolvedValue(undefined);
  mocks.push.mockReset();
  localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe('LWC-174 production Shell rename behavior', () => {
  it('loads an empty Shell, opens and cancels creation without mutation, then selects the server ID on submit', async () => {
    localStorage.setItem('locale', 'en');
    mocks.getProjects.mockResolvedValueOnce([]);
    mocks.createProject.mockResolvedValueOnce({ id: 'server-generated-wiki-id', name: 'First Wiki' });
    render(<Shell><BackgroundRefreshProbe /></Shell>);

    fireEvent.click(await screen.findByRole('button', { name: 'Create your first Wiki' }));
    expect(await screen.findByRole('dialog', { name: 'Create project' })).toBeTruthy();
    expect(mocks.createProject).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(mocks.createProject).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Create your first Wiki' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'First Wiki' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));
    await waitFor(() => expect(screen.getByTestId('mounted-current-project-id').textContent).toBe('server-generated-wiki-id'));
    expect(mocks.createProject).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith('/profile');
    expect(mocks.bootstrapDefaultProject).not.toHaveBeenCalled();
  });

  it('shows a load error instead of the empty CTA and retries the list read', async () => {
    localStorage.setItem('locale', 'en');
    mocks.getProjects.mockRejectedValueOnce(new Error('initial list failed'));
    renderShell();

    expect(await screen.findByRole('heading', { name: 'Projects unavailable' })).toBeTruthy();
    expect(screen.getByText('initial list failed')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Create your first Wiki' })).toBeNull();
    expect(mocks.bootstrapDefaultProject).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('workspace content')).toBeTruthy();
    expect(mocks.getProjects).toHaveBeenCalledTimes(2);
    expect(mocks.bootstrapDefaultProject).not.toHaveBeenCalled();
  });

  it('keeps mounted content and shows a visible retry after background list failure', async () => {
    localStorage.setItem('locale', 'en');
    render(<Shell><BackgroundRefreshProbe /></Shell>);
    expect(await screen.findByText('workspace content')).toBeTruthy();
    expect(mocks.bootstrapDefaultProject).not.toHaveBeenCalled();

    mocks.getProjects.mockRejectedValueOnce(new Error('background list failed'));
    fireEvent.click(screen.getByRole('button', { name: 'refresh mounted workspace' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('background list failed');
    expect(screen.getByText('workspace content')).toBeTruthy();

    const retry = deferred<{ id: string; name: string }[]>();
    mocks.getProjects.mockReturnValueOnce(retry.promise);
    fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
    expect(screen.getByText('workspace content')).toBeTruthy();
    await act(async () => {
      retry.resolve([{ id: 'project-a', name: 'Project Alpha' }]);
      await retry.promise;
    });
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(screen.getByText('workspace content')).toBeTruthy();
    expect(mocks.bootstrapDefaultProject).not.toHaveBeenCalled();
  });

  it('uses the observed marker to reveal scroll-to-top only above the viewport and disconnects on unmount', async () => {
    localStorage.setItem('locale', 'en');
    const view = renderShell();
    await screen.findByText('workspace content');
    const observer = observers.find(({ observe }) => observe.mock.calls.some(
      ([target]) => (target as HTMLElement).classList.contains('top-[320px]'),
    ))!;
    expect(observer).toBeDefined();
    const marker = observer.observe.mock.calls[0][0] as HTMLElement;
    expect(marker.getAttribute('aria-hidden')).toBe('true');
    expect(marker.classList.contains('top-[320px]')).toBe(true);
    const label = 'Scroll to top';
    expect(screen.queryByRole('button', { name: label })).toBeNull();
    act(() => observer.notify(false, 320));
    expect(screen.queryByRole('button', { name: label })).toBeNull();
    act(() => observer.notify(true, 0));
    expect(screen.queryByRole('button', { name: label })).toBeNull();
    act(() => observer.notify(false, -1));
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined);
    fireEvent.click(screen.getByRole('button', { name: label }));
    expect(scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 0, behavior: 'smooth' });
    scrollTo.mockRestore();
    act(() => observer.notify(true, 0));
    expect(screen.queryByRole('button', { name: label })).toBeNull();
    view.unmount();
    expect(observer.disconnect).toHaveBeenCalledOnce();
  });

  it('keeps the authenticated OAuth consent route reachable during Project loading, empty state, and load failure', async () => {
    mocks.pathname = '/oauth/consent';
    const projects = deferred<{ id: string; name: string }[]>();
    mocks.getProjects.mockReturnValueOnce(projects.promise);
    renderShell();

    expect(await screen.findByText('workspace content')).toBeTruthy();
    expect(screen.queryByText('Projects unavailable')).toBeNull();
    await act(async () => {
      projects.resolve([]);
      await projects.promise;
    });
    expect(screen.getByText('workspace content')).toBeTruthy();

    cleanup();
    const failedProjects = deferred<{ id: string; name: string }[]>();
    mocks.getProjects.mockReturnValueOnce(failedProjects.promise);
    renderShell();
    await act(async () => {
      failedProjects.reject(new Error('synthetic Project load failure'));
      await failedProjects.promise.catch(() => undefined);
    });
    expect(screen.getByText('workspace content')).toBeTruthy();
    expect(screen.queryByText('Projects unavailable')).toBeNull();
  });

  it('shows and copies the actual server response request ID in the shared shell', async () => {
    const requestID = '123e4567-e89b-12d3-a456-426614174099';
    const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    try {
      recordRequestIDHeader(requestID, 503);
      renderShell();
      expect(await screen.findByText(requestID)).toBeTruthy();
      expect(screen.getByText('HTTP 狀態: 503')).toBeTruthy();
      fireEvent.click(screen.getByRole('button', { name: '複製編號' }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith(requestID));
    } finally {
      if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard);
      else Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

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

  it('routes CLI setup from the sidebar and hides it in the trial Demo session', async () => {
    localStorage.setItem('locale', 'en');
    renderShell();
    expect(await screen.findByRole('link', { name: 'CLI setup Beta' })).toHaveProperty('href', expect.stringContaining('/cli'));

    cleanup();
    mocks.isDemoSession = true;
    renderShell();
    await screen.findByRole('button', { name: 'Project Alpha' });
    expect(screen.queryByRole('link', { name: /CLI setup/ })).toBeNull();
  });

  it.each([
    ['en', 'Primary navigation', 'CLI setup'],
    ['zh-TW', '主要導覽', 'CLI 設定'],
  ] as const)('shows localized Beta badges in desktop and expanded mobile navigation for %s', async (locale, navigationLabel, cliLabel) => {
    localStorage.setItem('locale', locale);
    renderShell();

    const navigation = await screen.findByRole('navigation', { name: navigationLabel });
    const home = screen.getByRole('link', { name: locale === 'en' ? 'Home' : '主頁' });
    expect(home.getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'Agent & MCP Beta' }).getAttribute('href')).toBe('/advanced');
    expect(screen.getByRole('link', { name: `${cliLabel} Beta` }).getAttribute('href')).toBe('/cli');

    const toggle = screen.getByRole('button', { name: locale === 'en' ? 'Open navigation' : '開啟導覽' });
    fireEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(navigation.querySelectorAll('a').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Beta')).toHaveLength(2);
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
    expect(settings.className).toContain('flex min-h-11 items-center gap-3 rounded-md');
    expect(settings.className).toContain('focus-visible:outline');
    expect(settings.parentElement?.className).toContain('mt-2 grid gap-0.5');

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
