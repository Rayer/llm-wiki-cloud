import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  confirmNavigation: vi.fn(),
  onClose: vi.fn(),
  push: vi.fn(),
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }),
}));

import { CommandPalette } from '@/components/ui/CommandPalette';

function renderPalette(home: string) {
  return render(
    <CommandPalette
      open
      onClose={mocks.onClose}
      projects={[]}
      onSelectProject={vi.fn()}
      labels={{
        placeholder: 'Search commands',
        navigate: 'Navigate',
        projects: 'Projects',
        home,
        search: 'Search',
        sources: 'Sources',
        concepts: 'Entries',
        status: 'Status',
      }}
    />,
  );
}

beforeEach(() => {
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.onClose.mockReset();
  mocks.push.mockReset();
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-406 Home navigation and search commands', () => {
  it.each(['Home', '主頁'])('labels the / navigation command %s and keeps its destination', (home) => {
    renderPalette(home);
    fireEvent.click(screen.getByRole('button', { name: home }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith('/');
    expect(mocks.onClose).toHaveBeenCalledOnce();
  });

  it('keeps the actual search query command and encoded URL unchanged', () => {
    renderPalette('Home');
    fireEvent.change(screen.getByRole('textbox', { name: 'Search commands' }), { target: { value: 'invoice notes' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search "invoice notes"' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.push).toHaveBeenCalledWith('/?q=invoice%20notes');
    expect(mocks.onClose).toHaveBeenCalledOnce();
  });
});
