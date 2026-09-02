import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { ProjectEmptyState } from '@/components/ProjectEmptyState';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

const mocks = vi.hoisted(() => ({
  isDemoSession: false,
  openNewProject: vi.fn(),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    isDemoSession: mocks.isDemoSession,
    openNewProject: mocks.openNewProject,
  }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  mocks.isDemoSession = false;
});

describe('LWC-68 project empty state', () => {
  it('hides empty-state create action in demo sessions', () => {
    mocks.isDemoSession = true;

    render(<ProjectEmptyState />);

    expect(screen.getByText('Welcome to LLM Wiki Cloud')).toBeDefined();
    expect(screen.queryByRole('button', { name: '+ Create Project' })).toBeNull();
    expect(mocks.openNewProject).not.toHaveBeenCalled();
  });

  it('renders create action for normal sessions and calls openNewProject', async () => {
    render(<ProjectEmptyState />);
    const createButton = screen.getByRole('button', { name: '+ Create Project' });

    await fireEvent.click(createButton);

    expect(mocks.openNewProject).toHaveBeenCalledTimes(1);
  });
});
