import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { act } from 'react';
import { promises as fs } from 'node:fs';
import path from 'node:path';

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

vi.mock('@/lib/i18n', () => ({
  useT: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: mocks.currentProject }),
}));

vi.mock('@/components/NavigationBlocker', async () => {
  const actual = await vi.importActual<typeof import('@/components/NavigationBlocker')>('@/components/NavigationBlocker');
  return {
    ...actual,
    NavigationLink: ({ href, children, onClick, ...props }: {
      href: string;
      children: React.ReactNode;
      onClick?: React.MouseEventHandler;
    }) => (
      <a href={href} onClick={onClick} {...props}>
        {children}
      </a>
    ),
    useNavigationBlocker: () => ({
      confirmNavigation: () => true,
      consumeCapturedConfirmation: () => false,
      setBlocked: () => undefined,
    }),
  };
});

import { HomeClient } from '@/components/HomeClient';
import type { ApiStatus } from '@/lib/api';

const SEARCH_RESPONSE = {
  results: [],
  aiAnswer: '',
  citations: [],
};

function status(overrides: Partial<ApiStatus> = {}) {
  return {
    sourcesCount: 2,
    conceptsCount: 3,
    rawCount: 1,
    suggestedQueries: ['primary'],
    raw: {},
    ...overrides,
  } as ApiStatus;
}

let replaceStateSpy: ReturnType<typeof vi.spyOn>;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolveFn) => {
    resolve = resolveFn;
  });
  return { promise, resolve };
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.getStatus.mockResolvedValue(status());
  mocks.getConcepts.mockResolvedValue([]);
  mocks.searchWiki.mockResolvedValue(SEARCH_RESPONSE);
  replaceStateSpy = vi.spyOn(window.history, 'replaceState').mockImplementation(() => undefined);
});

afterEach(() => {
  replaceStateSpy.mockRestore();
  cleanup();
  vi.clearAllMocks();
});

async function getSuggestedChip(label: string) {
  return screen.findByRole('button', { name: label });
}

async function getSearchButton() {
  return screen.findByRole('button', { name: 'Demo.search' });
}

async function waitForInitialSearchState() {
  await waitFor(() => {
    expect(mocks.getStatus).toHaveBeenCalledTimes(1);
    expect(mocks.getConcepts).toHaveBeenCalledTimes(1);
  });
}

async function getFormFromSearchButton() {
  const searchButton = await getSearchButton();
  const form = searchButton.closest('form');
  expect(form).not.toBeNull();
  return { form: form as HTMLFormElement, searchButton };
}

describe('LWC-248 home search submission contract', () => {
  it('does not submit when changing modes with a non-empty query', async () => {
    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const fullMode = screen.getByRole('button', { name: 'Demo.full' });
    const wikiMode = screen.getByRole('button', { name: 'Demo.wiki' });

    expect(wikiMode.getAttribute('aria-pressed')).toBe('true');
    expect(fullMode.getAttribute('aria-pressed')).toBe('false');

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic alpha' } });
      fireEvent.click(fullMode);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(0);
    expect(replaceStateSpy).toHaveBeenCalledTimes(0);
    expect(wikiMode.getAttribute('aria-pressed')).toBe('false');
    expect(fullMode.getAttribute('aria-pressed')).toBe('true');
  });

  it('fills query from a suggested chip without searching or syncing URL', async () => {
    mocks.getStatus.mockResolvedValue(status({
      suggestedQueries: ['suggestion', 'chip suggestion'],
    }));

    render(<HomeClient />);
    await waitForInitialSearchState();
    const chip = await getSuggestedChip('chip suggestion');
    const queryInput = screen.getByRole('textbox');

    queryInput.focus();
    const mouseDown = new MouseEvent('mousedown', { bubbles: true, cancelable: true });
    await act(async () => {
      chip.dispatchEvent(mouseDown);
      fireEvent.click(chip);
    });

    expect((queryInput as HTMLInputElement).value).toBe('chip suggestion');
    expect(mouseDown.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(queryInput);
    expect(mocks.searchWiki).toHaveBeenCalledTimes(0);
    expect(replaceStateSpy).toHaveBeenCalledTimes(0);
    expect(window.location.search).toBe('');
  });

  it('searches exactly once from Search button with current mode and syncs URL once', async () => {
    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const fullMode = screen.getByRole('button', { name: 'Demo.full' });
    const searchButton = await getSearchButton();

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic beta' } });
      fireEvent.click(fullMode);
    });
    expect(fullMode.className).toContain('bg-emerald-400/20');
    await act(async () => {
      fireEvent.click(searchButton);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(mocks.searchWiki).toHaveBeenCalledWith('topic beta', 'full');
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);
    expect(replaceStateSpy).toHaveBeenCalledWith(null, '', '/?q=topic+beta&mode=full');
  });

  it('preserves submitted mode in result badge until a new explicit search starts', async () => {
    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const wikiMode = screen.getByRole('button', { name: 'Demo.wiki' });
    const fullMode = screen.getByRole('button', { name: 'Demo.full' });
    const searchButton = await getSearchButton();

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic epsilon' } });
      fireEvent.click(wikiMode);
    });

    await act(async () => {
      fireEvent.click(searchButton);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(mocks.searchWiki).toHaveBeenLastCalledWith('topic epsilon', 'wiki');
    expect(screen.getByText('wiki mode')).toBeDefined();
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);

    await act(async () => {
      fireEvent.click(fullMode);
    });
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(screen.getByText('wiki mode')).toBeDefined();

    await act(async () => {
      fireEvent.click(searchButton);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(1, 'topic epsilon', 'wiki');
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(2, 'topic epsilon', 'full');
    expect(screen.getByText('full mode')).toBeDefined();
    expect(replaceStateSpy).toHaveBeenCalledTimes(2);
    expect(replaceStateSpy).toHaveBeenCalledWith(null, '', '/?q=topic+epsilon&mode=full');
  });

  it('submits the form (Enter-equivalent) exactly once with one URL sync', async () => {
    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const { form } = await getFormFromSearchButton();

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic gamma' } });
      fireEvent.submit(form);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(mocks.searchWiki).toHaveBeenCalledWith('topic gamma', 'wiki');
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);
    expect(replaceStateSpy).toHaveBeenCalledWith(null, '', '/?q=topic+gamma');
  });

  it('restarts the Search-button cue with repeated chip clicks without submitting or stealing focus', async () => {
    mocks.getStatus.mockResolvedValue(status({
      suggestedQueries: ['suggestion', 'chip suggestion'],
    }));

    render(<HomeClient />);
    await waitForInitialSearchState();
    const chip = await getSuggestedChip('chip suggestion');
    const searchButton = await getSearchButton();
    const queryInput = screen.getByRole('textbox');
    queryInput.focus();

    await act(async () => {
      fireEvent.click(chip);
    });
    const firstCue = searchButton.getAttribute('data-search-cue');

    await act(async () => {
      fireEvent.click(chip);
    });
    const secondCue = searchButton.getAttribute('data-search-cue');
    expect(firstCue).toBe('1');
    expect(secondCue).toBe('2');
    expect(firstCue).not.toBe(secondCue);
    expect(document.activeElement).toBe(queryInput);
    expect(mocks.searchWiki).toHaveBeenCalledTimes(0);
    const globals = await fs.readFile(path.join(process.cwd(), 'src/app/globals.css'), 'utf8');
    expect(globals).toMatch(/button\[data-search-cue='1'\]\s*\{\s*animation: home-search-button-cue-gentle 220ms ease;\s*\}/);
    expect(globals).toMatch(/button\[data-search-cue='2'\]\s*\{\s*animation: home-search-button-cue-gentle-alt 220ms ease;\s*\}/);
    expect(globals).toMatch(/@keyframes home-search-button-cue-gentle \{\s*0% \{\s*transform: scale\(1\);\s*\}\s*30% \{\s*transform: scale\(1\.03\);\s*\}\s*60% \{\s*transform: scale\(0\.97\);\s*\}\s*100% \{\s*transform: scale\(1\);\s*\}\s*\}/);
    expect(globals).toMatch(/@keyframes home-search-button-cue-gentle-alt \{\s*0% \{\s*transform: scale\(1\);\s*\}\s*30% \{\s*transform: scale\(0\.97\);\s*\}\s*60% \{\s*transform: scale\(1\.03\);\s*\}\s*100% \{\s*transform: scale\(1\);\s*\}\s*\}/);
  });

  it('clears the Search-button cue after explicit form submission', async () => {
    mocks.getStatus.mockResolvedValue(status({
      suggestedQueries: ['suggestion', 'chip'],
    }));

    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const searchButton = await getSearchButton();
    const { form } = await getFormFromSearchButton();
    const chip = await getSuggestedChip('chip');

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic delta' } });
      fireEvent.click(chip);
      fireEvent.submit(form);
    });

    expect(searchButton.getAttribute('data-search-cue')).toBeNull();
  });

  it('disables cue motion in reduced-motion CSS while preserving a visible cue', async () => {
    const globals = await fs.readFile(path.join(process.cwd(), 'src/app/globals.css'), 'utf8');
    expect(globals).toMatch(/@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?button\[data-search-cue='1'\],\s*button\[data-search-cue='2'\]\s*\{\s*animation: none;\s*transform: none;\s*box-shadow: 0 0 0 1px rgba\(52, 211, 153, 0.6\);\s*\}[\s\S]*\}/);
  });

  it('disables Search and ignores form submission while a search is in flight', async () => {
    const pendingWiki = deferred<typeof SEARCH_RESPONSE>();
    mocks.searchWiki
      .mockReturnValueOnce(pendingWiki.promise)
      .mockResolvedValueOnce(SEARCH_RESPONSE);

    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const fullMode = screen.getByRole('button', { name: 'Demo.full' });
    const { form, searchButton } = await getFormFromSearchButton();

    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'topic racing' } });
      fireEvent.click(searchButton);
    });
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect((searchButton as HTMLButtonElement).disabled).toBe(true);

    await act(async () => {
      fireEvent.click(fullMode);
      fireEvent.submit(form);
    });
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);

    await act(async () => {
      pendingWiki.resolve(SEARCH_RESPONSE);
    });
    await waitFor(() => expect((searchButton as HTMLButtonElement).disabled).toBe(false));

    await act(async () => {
      fireEvent.submit(form);
    });
    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
    expect(mocks.searchWiki).toHaveBeenLastCalledWith('topic racing', 'full');
  });

  it('ignores a slow deep-linked response after switching projects', async () => {
    const deepWiki = deferred<{
      results: [{ id: 'wiki-result'; slug: string; title: string; type: string; excerpt: string; content: string }];
      aiAnswer: string;
      citations: [];
    }>();

    replaceStateSpy.mockRestore();
    window.history.replaceState(null, '', '/?q=deep%20travel');
    replaceStateSpy = vi.spyOn(window.history, 'replaceState').mockImplementation(() => undefined);
    mocks.searchWiki.mockReturnValue(deepWiki.promise);

    const { rerender } = render(<HomeClient />);
    await waitFor(() => expect(mocks.searchWiki).toHaveBeenCalledTimes(1));
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(1, 'deep travel', 'wiki');
    const queryInput = screen.getByRole('textbox') as HTMLInputElement;
    const searchButton = await getSearchButton() as HTMLButtonElement;
    expect(searchButton.disabled).toBe(true);

    await act(async () => {
      mocks.currentProject = { id: 'project-b', name: 'Project B' };
      rerender(<HomeClient />);
    });
    await waitFor(() => expect(queryInput.value).toBe(''));
    expect(searchButton.disabled).toBe(false);
    expect(replaceStateSpy).toHaveBeenCalledWith(null, '', '/');

    await act(async () => {
      deepWiki.resolve({
        results: [{
          id: 'wiki-result',
          slug: 'wiki-result',
          title: 'Stale Wiki Result',
          type: 'concept',
          excerpt: 'stale',
          content: '',
        }],
        aiAnswer: 'The stale wiki response should be ignored.',
        citations: [],
      });
    });

    expect(screen.queryByText('Stale Wiki Result')).toBeNull();
    expect(screen.queryByText('The stale wiki response should be ignored.')).toBeNull();
    expect(screen.queryByText(/Searching/)).toBeNull();
    expect(searchButton.disabled).toBe(false);
  });
});
