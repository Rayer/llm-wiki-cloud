import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  hydrated: true,
  projectsLoading: false,
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  isDemoSession: false,
  user: { id: 'user-a', email: 'user@example.com' } as { id: string; email: string } | null,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    hydrated: mocks.hydrated,
    projectsLoading: mocks.projectsLoading,
    currentProject: mocks.currentProject,
    isDemoSession: mocks.isDemoSession,
  }),
}));

vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: mocks.user }) }));
vi.mock('@/components/ProjectProfilePanel', () => ({
  ProjectProfilePanel: ({ projectId }: { projectId: string }) => <div data-testid="profile-panel">{projectId}</div>,
}));

import ProfilePage from '@/app/(workspace)/profile/page';

beforeEach(() => {
  mocks.hydrated = true;
  mocks.projectsLoading = false;
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.isDemoSession = false;
  mocks.user = { id: 'user-a', email: 'user@example.com' };
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
  });

  it('does not mount the trial Demo Profile panel', () => {
    mocks.isDemoSession = true;
    render(<ProfilePage />);

    expect(screen.getByText('Project Profile is not available in the trial Demo session.')).toBeDefined();
    expect(screen.queryByTestId('profile-panel')).toBeNull();
  });

  it('provides the route copy in Traditional Chinese', () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProfilePage />);

    expect(screen.getByRole('heading', { name: 'Profile' })).toBeDefined();
  });
});
