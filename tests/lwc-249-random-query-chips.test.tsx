import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getConcepts: vi.fn(),
  getStatus: vi.fn(),
  searchWiki: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getConcepts: mocks.getConcepts,
    getStatus: mocks.getStatus,
    searchWiki: mocks.searchWiki,
  };
});

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: mocks.currentProject }),
}));

import { HomeClient } from '@/components/HomeClient';

function status(suggestedQueries: string[]) {
  return { sourcesCount: 1, conceptsCount: 2, rawCount: 3, suggestedQueries, raw: {} };
}

function statusWithQueries(prefix = 'query-', placeholder = 'placeholder') {
  return status([placeholder, ...Array.from({ length: 19 }, (_, index) => `${prefix}${index + 1}`)]);
}

function chipLabels() {
  return screen.getAllByRole('button')
    .filter((button) => (button as HTMLButtonElement).type === 'button')
    .map((button) => button.textContent ?? '')
    .filter((label) => label.startsWith('query-') || label.startsWith('legacy-') || label.startsWith('dup-'));
}

function mockMathRandomSequence(values: number[]) {
  const randomSpy = vi.mocked(Math.random);
  const next = values.slice();
  randomSpy.mockImplementation(() => {
    const head = next.shift();
    return head === undefined ? 0 : head;
  });
  return randomSpy;
}

function getUniqueQueryChips() {
  const labels = chipLabels().filter((label) => label.startsWith('query-'));
  const queryMatch = /^query-(?:[1-9]|1[0-9])$/;
  expect(labels).toHaveLength(4);
  expect(new Set(labels).size).toBe(4);
  expect(labels.every((label) => queryMatch.test(label))).toBe(true);
  return labels;
}

beforeEach(() => {
  localStorage.clear();
  mocks.getConcepts.mockResolvedValue([]);
  mocks.searchWiki.mockResolvedValue({ results: [], aiAnswer: '', citations: [] });
  mocks.getStatus.mockResolvedValue(statusWithQueries());
  vi.spyOn(Math, 'random').mockReturnValue(0.25);
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('LWC-249 random query chips', () => {
  it('renders exactly four unique chips from the 19-item chip pool and keeps item 0 as placeholder', async () => {
    render(<HomeClient />);

    await waitFor(() => expect(screen.getByPlaceholderText('placeholder')).toBeDefined());

    expect(screen.getByPlaceholderText('placeholder')).toBeDefined();
    const chipButtons = screen.getAllByRole('button').filter((button) => (button as HTMLButtonElement).type === 'button')
      .filter((button) => button.textContent?.startsWith('query-'));
    expect(chipButtons).toHaveLength(4);
    expect(new Set(chipButtons.map((button) => button.textContent)).size).toBe(4);
    expect(chipButtons.every((button) => /^query-[1-9]$/.test(button.textContent ?? ''))).toBe(true);
  });

  it('keeps the sample stable through same-project churn, edits, chip fill, loading, and completion', async () => {
    let resolveSearch!: (response: { results: []; aiAnswer: string; citations: [] }) => void;
    const pendingSearch = new Promise<{ results: []; aiAnswer: string; citations: [] }>((resolve) => {
      resolveSearch = resolve;
    });
    mocks.searchWiki.mockReturnValue(pendingSearch);
    const { rerender } = render(<HomeClient />);
    await waitFor(() => expect(screen.getByPlaceholderText('placeholder')).toBeDefined());
    const initial = chipLabels();

    mocks.currentProject = { id: 'project-a', name: 'Project A refreshed' };
    mocks.getStatus.mockResolvedValue(status(['new-placeholder', 'query-new-1', 'query-new-2']));
    await act(async () => rerender(<HomeClient />));
    await act(async () => {
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'edited query' } });
      fireEvent.click(screen.getByRole('button', { name: 'Demo.full' }));
      fireEvent.click(screen.getByRole('button', { name: initial[0] }));
    });
    expect(chipLabels()).toEqual(initial);

    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Demo.search' })));
    expect(chipLabels()).toEqual(initial);
    expect(screen.getByRole('button', { name: 'Demo.search' })).toHaveProperty('disabled', true);
    await act(async () => resolveSearch({ results: [], aiAnswer: '', citations: [] }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Demo.search' })).toHaveProperty('disabled', false));
    expect(chipLabels()).toEqual(initial);
  });

  it('samples afresh on remount and active-project identity change', async () => {
    const randomSpy = vi.mocked(Math.random);
    mockMathRandomSequence([0, 0, 0, 0]);

    const first = render(<HomeClient />);
    await waitFor(() => expect(screen.getByPlaceholderText('placeholder')).toBeDefined());
    const firstLifecycle = getUniqueQueryChips();
    expect(firstLifecycle).toEqual(['query-1', 'query-2', 'query-3', 'query-4']);
    expect(randomSpy).toHaveBeenCalledTimes(4);

    first.unmount();
    mocks.getStatus.mockResolvedValue(statusWithQueries());
    mockMathRandomSequence([0.99, 0.99, 0.99, 0.99]);

    const second = render(<HomeClient />);
    await waitFor(() => expect(screen.getByPlaceholderText('placeholder')).toBeDefined());
    const remountLifecycle = getUniqueQueryChips();
    expect(remountLifecycle).toEqual(['query-19', 'query-1', 'query-2', 'query-3']);
    expect(remountLifecycle).not.toEqual(firstLifecycle);
    expect(randomSpy).toHaveBeenCalledTimes(8);

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    mocks.getStatus.mockResolvedValue(statusWithQueries('query-', 'placeholder-b'));
    mockMathRandomSequence([0.5, 0.5, 0.5, 0.5]);
    await act(async () => second.rerender(<HomeClient />));
    await waitFor(() => expect(screen.getByPlaceholderText('placeholder-b')).toBeDefined());
    const projectLifecycle = getUniqueQueryChips();
    expect(projectLifecycle).toEqual(['query-10', 'query-11', 'query-2', 'query-12']);
    expect(projectLifecycle).not.toEqual(remountLifecycle);
    expect(projectLifecycle).not.toEqual(firstLifecycle);
    expect(randomSpy).toHaveBeenCalledTimes(12);
  });

  it('defaults suggested queries open and persists collapse across remount', async () => {
    const first = render(<HomeClient />);
    await waitFor(() => expect(getUniqueQueryChips()).toHaveLength(4));
    const toggle = screen.getByRole('button', { name: 'Demo.hideSuggestedQueries' });
    expect(toggle.getAttribute('aria-expanded')).toBe('true');

    await act(async () => fireEvent.click(toggle));
    expect(chipLabels()).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Demo.showSuggestedQueries' }).getAttribute('aria-expanded')).toBe('false');
    expect(localStorage.getItem('llm-wiki:suggested-queries-open')).toBe('0');
    first.unmount();

    render(<HomeClient />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Demo.showSuggestedQueries' })).toBeDefined());
    expect(chipLabels()).toHaveLength(0);
  });

  it.each([
    [['placeholder'], 0],
    [['placeholder', 'legacy-1'], 1],
    [['placeholder', 'dup-1', 'dup-1', 'dup-2', 'dup-3', 'dup-4', 'dup-5'], 4],
  ] as const)('renders %s as %d unique legacy/partial chips safely', async (suggestedQueries, expectedCount) => {
    mocks.getStatus.mockResolvedValue(status([...suggestedQueries]));
    render(<HomeClient />);
    await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(chipLabels()).toHaveLength(expectedCount));
    expect(new Set(chipLabels()).size).toBe(expectedCount);
  });
});
