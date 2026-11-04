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
    localStorage.setItem('locale', 'en');
    mocks.isDemoSession = true;

    render(<ProjectEmptyState />);

    expect(screen.getByText('Welcome to LLM Wiki Cloud')).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Create your first Wiki' })).toBeNull();
    expect(mocks.openNewProject).not.toHaveBeenCalled();
  });

  it.each([
    ['en', 'Create your first Wiki', 'Create your first Wiki to start building a searchable knowledge workspace.'],
    ['zh-TW', '建立第一個 Wiki', '建立你的第一個 Wiki，開始打造可搜尋的知識工作空間。'],
  ] as const)('renders localized first-Wiki guidance for %s', async (locale, buttonLabel, guidance) => {
    localStorage.setItem('locale', locale);
    render(<ProjectEmptyState />);
    expect(screen.getByText(guidance)).toBeDefined();
    const createButton = screen.getByRole('button', { name: buttonLabel });

    await fireEvent.click(createButton);

    expect(mocks.openNewProject).toHaveBeenCalledTimes(1);
  });
});
