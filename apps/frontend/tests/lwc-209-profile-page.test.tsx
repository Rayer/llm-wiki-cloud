import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  renameProject: vi.fn().mockResolvedValue(undefined),
  hydrated: true,
  projectsLoading: false,
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  isDemoSession: false,
  user: { id: 'user-a', email: 'user@example.com' } as { id: string; email: string } | null,
  accessToken: 'profile-token' as string | null,
  sessionEpoch: 1,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    renameProject: mocks.renameProject,
    hydrated: mocks.hydrated,
    projectsLoading: mocks.projectsLoading,
    currentProject: mocks.currentProject,
    isDemoSession: mocks.isDemoSession,
  }),
}));

vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: mocks.user, accessToken: mocks.accessToken, sessionEpoch: mocks.sessionEpoch }) }));
vi.mock('@/components/ProjectProfilePanel', () => ({
  ProjectProfilePanel: ({ projectId }: { projectId: string }) => <div data-testid="profile-panel">{projectId}</div>,
}));
vi.mock('@/components/ProjectKeysSection', () => ({
  ProjectKeysSection: ({ currentProject, accessToken }: { currentProject: { id: string }; accessToken: string | null }) => (
    <div data-testid="project-keys-section" data-project={currentProject.id} data-token={accessToken} />
  ),
}));

import ProfilePage from '@/app/(workspace)/profile/page';

beforeEach(() => {
  mocks.hydrated = true;
  mocks.projectsLoading = false;
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.isDemoSession = false;
  mocks.user = { id: 'user-a', email: 'user@example.com' };
  mocks.accessToken = 'profile-token';
  mocks.sessionEpoch = 1;
  localStorage.setItem('locale', 'en');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-209 Profile route', () => {
  it('renders the selected project Profile on its own page', () => {
    render(<ProfilePage />);

    expect(screen.getByRole('heading', { name: 'Profile' })).toBeDefined();
    expect(screen.getByTestId('profile-panel').textContent).toBe('project-a');
    expect(screen.getByTestId('project-keys-section').getAttribute('data-project')).toBe('project-a');
    expect(screen.getByTestId('project-keys-section').getAttribute('data-token')).toBe('profile-token');
  });

  it('does not mount the trial Demo Profile panel', () => {
    mocks.isDemoSession = true;
    render(<ProfilePage />);

    expect(screen.getByText('Project Profile is not available in the trial Demo session.')).toBeDefined();
    expect(screen.queryByTestId('profile-panel')).toBeNull();
    expect(screen.queryByTestId('project-keys-section')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Rename project' })).toBeNull();
  });

  it('provides the route copy in Traditional Chinese', () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProfilePage />);

    expect(screen.getByRole('heading', { name: 'Profile' })).toBeDefined();
    expect(screen.getByRole('button', { name: '重新命名專案' })).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: '重新命名專案' }));
    expect(screen.getByRole('dialog', { name: '重新命名專案' })).toBeDefined();
    expect(screen.getByRole('textbox', { name: '專案名稱' })).toBeDefined();
    expect(screen.getByRole('button', { name: '取消' })).toBeDefined();
    expect(screen.getByRole('button', { name: '重新命名' })).toBeDefined();
  });
});

it('renames from the Profile page and clears an open rename when the project changes', async () => {
  const view = render(<ProfilePage />);
  fireEvent.click(screen.getByRole('button', { name: 'Rename project' }));
  fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Stale A' } });
  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ProfilePage />);
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(mocks.renameProject).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: 'Rename project' }));
  const input = screen.getByRole('textbox', { name: 'Project name' }) as HTMLInputElement;
  expect(input.value).toBe('Project B');
  fireEvent.change(input, { target: { value: '  Renamed B  ' } });
  fireEvent.click(screen.getByRole('button', { name: 'Rename' }));
  await waitFor(() => expect(mocks.renameProject).toHaveBeenCalledWith('project-b', 'Renamed B'));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
});

it('remounts Project key controls for account, Project, and session changes', () => {
  const view = render(<ProfilePage />);
  const initial = screen.getByTestId('project-keys-section');
  mocks.user = { id: 'user-b', email: 'other@example.com' };
  view.rerender(<ProfilePage />);
  const nextAccount = screen.getByTestId('project-keys-section');
  expect(nextAccount).not.toBe(initial);

  mocks.sessionEpoch = 2;
  mocks.accessToken = 'replacement-token';
  view.rerender(<ProfilePage />);
  const nextSession = screen.getByTestId('project-keys-section');
  expect(nextSession).not.toBe(nextAccount);
  expect(nextSession.getAttribute('data-token')).toBe('replacement-token');

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  view.rerender(<ProfilePage />);
  const nextProject = screen.getByTestId('project-keys-section');
  expect(nextProject).not.toBe(nextSession);
  expect(nextProject.getAttribute('data-project')).toBe('project-b');
});
