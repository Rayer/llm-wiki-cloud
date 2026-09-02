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
    loginAsDemo: async () => undefined,
    register: async () => undefined,
    logout: async () => undefined,
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

  it('hides rename on the admin route', async () => {
    mocks.pathname = '/admin';
    mocks.user = { id: 'admin-1', email: 'admin@example.com', role: 'admin' };

    renderShell();

    await screen.findByRole('button', { name: 'Project Alpha' });
    expect(screen.queryByRole('button', { name: 'Rename project' })).toBeNull();
  });

  it('disables rename for demo sessions and when there is no current project', async () => {
    mocks.isDemoSession = true;
    const demoView = renderShell();
    const demoRename = await screen.findByRole('button', { name: 'Rename project' });
    expect((demoRename as HTMLButtonElement).disabled).toBe(true);
    demoView.unmount();

    mocks.getProjects.mockResolvedValue([]);
    mocks.isDemoSession = false;
    renderShell();
    const noProjectRename = await screen.findByRole('button', { name: 'Rename project' });
    expect((noProjectRename as HTMLButtonElement).disabled).toBe(true);
  });

  it('closes the A rename modal before B can submit A input', async () => {
    renderShell();

    await screen.findByRole('button', { name: 'Project Alpha' });
    fireEvent.click(screen.getByRole('button', { name: 'Rename project' }));
    const input = await screen.findByRole('textbox', { name: 'Project name' });
    expect((input as HTMLInputElement).value).toBe('Project Alpha');
    fireEvent.change(input, {
      target: { value: 'Stale A name' },
    });

    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const projectB = await screen.findByRole('button', { name: 'Project Beta' });
    fireEvent.click(projectB);

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Rename project' })).toBeNull());
    expect(mocks.renameProject).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Rename project' }));
    const projectBInput = await screen.findByRole('textbox', { name: 'Project name' });
    expect((projectBInput as HTMLInputElement).value).toBe('Project Beta');
    fireEvent.change(projectBInput, {
      target: { value: 'Renamed B' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));

    await waitFor(() => expect(mocks.renameProject).toHaveBeenCalledWith('project-b', 'Renamed B'));
    expect(mocks.renameProject).not.toHaveBeenCalledWith('project-b', 'Stale A name');
  });
});
