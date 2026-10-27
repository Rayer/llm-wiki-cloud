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
  token: 'session-a',
  user: { id: 'account-a' },
  hydrated: true,
  projectsLoading: false,
  locale: 'en' as 'en' | 'zh-TW',
  useRealLocaleCopy: false,
  getConcepts: vi.fn(),
  getProfile: vi.fn(),
  getStatus: vi.fn(),
  searchWiki: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getConcepts: mocks.getConcepts,
    getProfile: mocks.getProfile,
    getStatus: mocks.getStatus,
    searchWiki: mocks.searchWiki,
  };
});

vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  return {
    ...actual,
    useT: () => ({
      t: (key: string, params?: Record<string, string | number>) => mocks.useRealLocaleCopy
        ? actual.translate(mocks.locale, key, params)
        : key,
    }),
  };
});

vi.mock('next/navigation', () => ({
  usePathname: () => window.location.pathname,
  useSearchParams: () => new URLSearchParams(window.location.search),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    token: mocks.token,
    user: mocks.user,
    hydrated: mocks.hydrated,
    projectsLoading: mocks.projectsLoading,
  }),
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
import { QueryApiError, type ApiStatus } from '@/lib/api';
import { clearCompletedQueryResults } from '@/lib/query-result-history';

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
  vi.resetAllMocks();
  clearCompletedQueryResults();
  window.history.replaceState(null, '', '/');
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.token = 'session-a';
  mocks.user = { id: 'account-a' };
  mocks.hydrated = true;
  mocks.projectsLoading = false;
  mocks.locale = 'en';
  mocks.useRealLocaleCopy = false;
  mocks.getStatus.mockResolvedValue(status());
  mocks.getConcepts.mockResolvedValue([]);
  mocks.searchWiki.mockResolvedValue(SEARCH_RESPONSE);
  replaceStateSpy = vi.spyOn(window.history, 'replaceState');
});

afterEach(() => {
  replaceStateSpy.mockRestore();
  cleanup();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
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
  it('keeps Project Profile out of Search and does not load it there', async () => {
    render(<HomeClient />);
    await waitForInitialSearchState();

    expect(screen.queryByRole('heading', { name: 'Project Profile' })).toBeNull();
    expect(mocks.getProfile).not.toHaveBeenCalled();
  });

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
    expect(fullMode.className).toContain('underline');
    await act(async () => {
      fireEvent.click(searchButton);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(mocks.searchWiki).toHaveBeenCalledWith('topic beta', 'full');
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);
    expect(replaceStateSpy).toHaveBeenCalledWith(expect.objectContaining({ __lwc342QueryEntryId: expect.any(String) }), '', '/?q=topic+beta&mode=full');
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
    expect(screen.getByText('QueryResults.wikiMode')).toBeDefined();
    expect(replaceStateSpy).toHaveBeenCalledTimes(1);

    await act(async () => {
      fireEvent.click(fullMode);
    });
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
    expect(screen.getByText('QueryResults.wikiMode')).toBeDefined();

    await act(async () => {
      fireEvent.click(searchButton);
    });

    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(1, 'topic epsilon', 'wiki');
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(2, 'topic epsilon', 'full');
    expect(screen.getByText('QueryResults.fullMode')).toBeDefined();
    expect(replaceStateSpy).toHaveBeenCalledTimes(2);
    expect(replaceStateSpy).toHaveBeenCalledWith(expect.objectContaining({ __lwc342QueryEntryId: expect.any(String) }), '', '/?q=topic+epsilon&mode=full');
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
    expect(replaceStateSpy).toHaveBeenCalledWith(expect.objectContaining({ __lwc342QueryEntryId: expect.any(String) }), '', '/?q=topic+gamma');
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
    replaceStateSpy = vi.spyOn(window.history, 'replaceState');
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
    expect(replaceStateSpy).toHaveBeenCalledWith(expect.any(Object), '', '/');

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

  it('shows a localized Query failure and copyable diagnostic ID instead of no-results', async () => {
    const diagnosticId = 'f197cbe1-b0e3-4a68-a3eb-47526d98a622';
    const writeText = vi.fn().mockResolvedValue(undefined);
    replaceStateSpy.mockRestore();
    window.history.replaceState(null, '', '/');
    replaceStateSpy = vi.spyOn(window.history, 'replaceState');
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    mocks.searchWiki.mockRejectedValueOnce(new QueryApiError('safe provider detail', 503, 'query_provider_failed', diagnosticId));

    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const { form } = await getFormFromSearchButton();
    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'query with failure' } });
      fireEvent.submit(form);
    });

    expect(await screen.findByText('QueryErrors.providerFailed')).toBeTruthy();
    expect(screen.getByText(diagnosticId)).toBeTruthy();
    expect(screen.queryByText('QueryResults.noResults')).toBeNull();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    });
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticId));
    expect(screen.getByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeTruthy();
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  });

  it('keeps a Query error without a copy affordance when the server ID is absent', async () => {
    const writeText = vi.fn();
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    mocks.searchWiki.mockRejectedValueOnce(new QueryApiError('safe provider detail', 503, 'query_provider_failed'));

    render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const { form } = await getFormFromSearchButton();
    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'query without diagnostic ID' } });
      fireEvent.submit(form);
    });

    expect(await screen.findByText('QueryErrors.providerFailed')).toBeTruthy();
    expect(screen.queryByText('QueryResults.noResults')).toBeNull();
    expect(screen.queryByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeNull();
    expect(screen.queryByText('QueryErrors.diagnosticId')).toBeNull();
    expect(writeText).not.toHaveBeenCalled();
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  });

  it('does not apply a pending diagnostic copy to a newer request, Project, or session', async () => {
    const firstDiagnosticID = 'first_help-123';
    const nextDiagnosticID = 'next_help-456';
    const firstRequestID = '123e4567-e89b-42d3-a456-426614174016';
    const nextRequestID = '123e4567-e89b-42d3-a456-426614174017';
    let resolveCopy!: () => void;
    const pendingCopy = new Promise<void>((resolve) => { resolveCopy = resolve; });
    const writeText = vi.fn().mockReturnValue(pendingCopy);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    mocks.searchWiki
      .mockRejectedValueOnce(new QueryApiError('first error', 503, 'query_provider_failed', firstDiagnosticID, firstRequestID))
      .mockRejectedValueOnce(new QueryApiError('next error', 503, 'query_provider_failed', nextDiagnosticID, nextRequestID));

    const { rerender } = render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const { form } = await getFormFromSearchButton();
    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'first request' } });
      fireEvent.submit(form);
    });
    expect(await screen.findByText(firstRequestID)).toBeTruthy();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' }));
    });
    expect(writeText).toHaveBeenCalledWith(firstRequestID);

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    mocks.token = 'session-b';
    rerender(<HomeClient />);
    await act(async () => {
      fireEvent.change(screen.getByRole('textbox'), { target: { value: 'next request' } });
      fireEvent.submit((await getFormFromSearchButton()).form);
    });
    expect(await screen.findByText(nextRequestID)).toBeTruthy();

    await act(async () => {
      resolveCopy();
      await pendingCopy;
    });
    expect(screen.getByRole('button', { name: 'QueryErrors.copyDiagnosticId' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'QueryErrors.diagnosticIdCopied' })).toBeNull();
  });

  it.each(['en', 'zh-TW'] as const)('localizes the no-summary fallback and keeps real summaries in %s', async (locale) => {
    mocks.locale = locale;
    mocks.useRealLocaleCopy = true;
    mocks.searchWiki.mockResolvedValueOnce({
      results: [
        { id: 'without-summary', slug: 'without-summary', title: 'Entry without summary', type: 'concept', content: '' },
        { id: 'with-excerpt', slug: 'with-excerpt', title: 'Entry with excerpt', type: 'concept', excerpt: 'This excerpt comes from the Wiki.', content: '' },
        { id: 'with-description', slug: 'with-description', title: 'Entry with description', type: 'concept', description: 'This description comes from the Wiki.', content: '' },
      ],
      aiAnswer: '',
      citations: [],
    });
    render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'qualified evidence query' } });
      fireEvent.submit(input.closest('form') as HTMLFormElement);
    });

    const fallback = locale === 'en' ? 'Open this wiki entry.' : '開啟此 Wiki 條目。';
    const noSummaryCard = await screen.findByRole('button', { name: /Entry without summary/ });
    const excerptCard = screen.getByRole('button', { name: /Entry with excerpt/ });
    const descriptionCard = screen.getByRole('button', { name: /Entry with description/ });

    expect(noSummaryCard.textContent).toContain(fallback);
    expect(excerptCard.textContent).toContain('This excerpt comes from the Wiki.');
    expect(excerptCard.textContent).not.toContain(fallback);
    expect(descriptionCard.textContent).toContain('This description comes from the Wiki.');
    expect(descriptionCard.textContent).not.toContain(fallback);
    expect(screen.queryByText('QueryResults.openEntryFallback')).toBeNull();
  });
});

describe('LWC-342 completed query history restoration', () => {
  it.each(['wiki', 'full'] as const)('restores the exact %s result through browser Back and resubmits distinctly', async (searchMode) => {
    const firstResponse = {
      results: [{ id: 'first-result', slug: 'first-result', title: 'First result', type: 'concept', excerpt: 'first excerpt', content: '' }],
      aiAnswer: 'First answer from this submission.',
      citations: [{ text: 'First citation', type: 'source', slug: 'source-one', id: 'source-one', path: 'source-one.md' }],
      expand: { keywords: ['first keyword'] },
      ai_operation_failures: [{ stage: 'query_expansion', class: 'authentication', fallback: 'deterministic_plan' }],
    };
    const secondResponse = {
      results: [{ id: 'second-result', slug: 'second-result', title: 'Second result', type: 'concept', excerpt: 'second excerpt', content: '' }],
      aiAnswer: 'Different answer from the resubmission.',
      citations: [{ text: 'Second citation', type: 'source', slug: 'source-two', id: 'source-two', path: 'source-two.md' }],
      expand: { keywords: ['second keyword'] },
    };
    mocks.searchWiki
      .mockResolvedValueOnce(firstResponse)
      .mockResolvedValueOnce(secondResponse);

    const firstPage = render(<HomeClient />);
    await waitForInitialSearchState();
    const queryInput = screen.getByRole('textbox');
    const form = (await getFormFromSearchButton()).form;
    await act(async () => {
      fireEvent.change(queryInput, { target: { value: 'same question' } });
    });
    if (searchMode === 'full') {
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Demo.full' }));
      });
    }
    await act(async () => {
      fireEvent.submit(form);
    });
    expect(await screen.findByText('First answer from this submission.')).toBeTruthy();
    expect(screen.getByText('QueryResults.aiFailure.heading')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);

    window.history.pushState({}, '', '/concepts/first-result');
    firstPage.unmount();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));

    const restoredPage = render(<HomeClient />);
    expect(await screen.findByText('First answer from this submission.')).toBeTruthy();
    expect(screen.getByRole('textbox')).toHaveProperty('value', 'same question');
    expect(screen.getByText(searchMode === 'wiki' ? 'QueryResults.wikiMode' : 'QueryResults.fullMode')).toBeTruthy();
    expect(screen.getByText('First citation')).toBeTruthy();
    expect(screen.getByText('QueryResults.expansionKeywords')).toBeTruthy();
    expect(screen.getByText('QueryResults.aiFailure.heading')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);

    await act(async () => {
      fireEvent.submit((await getFormFromSearchButton()).form);
    });
    expect(await screen.findByText('Different answer from the resubmission.')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
    expect(mocks.searchWiki).toHaveBeenNthCalledWith(2, 'same question', searchMode);

    window.history.pushState({}, '', '/sources/source-two');
    restoredPage.unmount();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));
    render(<HomeClient />);

    expect(await screen.findByText('Different answer from the resubmission.')).toBeTruthy();
    expect(screen.getByText('Second citation')).toBeTruthy();
    expect(screen.getByText('QueryResults.expansionKeywords')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
  });

  it('restores a completed empty result and keeps a restore miss query-free until explicit retry', async () => {
    mocks.searchWiki.mockResolvedValueOnce({ results: [], aiAnswer: '', citations: [], expand: { keywords: [] } });
    const firstPage = render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'empty answer' } });
      fireEvent.submit((await getFormFromSearchButton()).form);
    });
    expect(await screen.findByText('QueryResults.noResults')).toBeTruthy();

    window.history.pushState({}, '', '/sources/empty-result');
    firstPage.unmount();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));
    render(<HomeClient />);
    expect(await screen.findByText('QueryResults.noResults')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);

    window.history.pushState({}, '', '/sources/empty-result');
    clearCompletedQueryResults();
    cleanup();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));
    render(<HomeClient />);
    expect(await screen.findByText('QueryResults.resultUnavailable')).toBeTruthy();
    expect(screen.queryByText('QueryResults.noResults')).toBeNull();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);

    mocks.searchWiki.mockResolvedValueOnce({
      results: [{ id: 'retry-result', slug: 'retry-result', title: 'Retry result', type: 'concept', excerpt: 'retry', content: '' }],
      aiAnswer: 'Retry result answer.',
      citations: [],
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'QueryResults.searchAgain' }));
    });
    expect(await screen.findByText('Retry result answer.')).toBeTruthy();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(2);
  });

  it.each(['en', 'zh-TW'] as const)('renders insufficient Wiki evidence as a localized terminal state in %s', async (locale) => {
    mocks.locale = locale;
    mocks.useRealLocaleCopy = true;
    mocks.searchWiki.mockResolvedValueOnce({
      status: 'insufficient_evidence',
      reason: 'no_qualified_evidence',
      wiki_evidence_status: 'insufficient_evidence',
      results: [],
      aiAnswer: '',
      citations: [],
    });
    render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'wiki evidence query' } });
      fireEvent.submit(input.closest('form') as HTMLFormElement);
    });

    expect(await screen.findByText(
      locale === 'en'
        ? 'This Wiki retrieval did not find sufficient evidence to answer this question.'
        : '目前的 Wiki 檢索未找到足夠證據回答這個問題。',
    )).toBeTruthy();
    expect(screen.queryByText(locale === 'en' ? 'No results were found for this search.' : '這次搜尋沒有找到結果。')).toBeNull();
    expect(screen.queryByText('QueryResults.insufficientEvidence')).toBeNull();
    expect(screen.queryByText('QueryResults.searching')).toBeNull();
  });

  it.each(['en', 'zh-TW'] as const)('discloses and restores a Full model-prior answer with empty results in %s', async (locale) => {
    mocks.locale = locale;
    mocks.useRealLocaleCopy = true;
    mocks.searchWiki.mockResolvedValueOnce({
      status: 'insufficient_evidence',
      reason: 'no_qualified_evidence',
      answer_basis: 'model_prior',
      wiki_evidence_status: 'no_relevant_evidence',
      disclosure_required: true,
      results: [],
      aiAnswer: 'Model-prior answer.',
      citations: [],
      expand: { keywords: [] },
    });
    const firstPage = render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    const fullMode = screen.getByRole('button', { name: locale === 'en' ? 'Full' : '完整' });
    fireEvent.click(fullMode);
    await act(async () => {
      fireEvent.change(input, { target: { value: 'full evidence query' } });
      fireEvent.submit(input.closest('form') as HTMLFormElement);
    });

    const insufficientCopy = locale === 'en'
      ? 'This Wiki retrieval did not find sufficient evidence to answer this question.'
      : '目前的 Wiki 檢索未找到足夠證據回答這個問題。';
    const disclosureCopy = locale === 'en'
      ? 'This Full mode answer may use general model knowledge and is not supported by the retrieved Wiki evidence.'
      : '此完整模式回答可能使用模型的一般知識，並非由檢索到的 Wiki 證據支持。';
    const noResultsCopy = locale === 'en' ? 'No results were found for this search.' : '這次搜尋沒有找到結果。';
    expect(await screen.findByText('Model-prior answer.')).toBeTruthy();
    expect(screen.getByText(insufficientCopy)).toBeTruthy();
    expect(screen.getByText(disclosureCopy)).toBeTruthy();
    expect(screen.getByText(locale === 'en' ? 'Full mode' : '完整模式')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByText(noResultsCopy)).toBeNull();
    expect(screen.queryByText('QueryResults.modelPriorDisclosure')).toBeNull();

    window.history.pushState({}, '', '/sources/model-prior-detail');
    firstPage.unmount();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));
    render(<HomeClient />);
    expect(await screen.findByText('Model-prior answer.')).toBeTruthy();
    expect(screen.getByText(insufficientCopy)).toBeTruthy();
    expect(screen.getByText(disclosureCopy)).toBeTruthy();
    expect(screen.queryByText(noResultsCopy)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(mocks.searchWiki).toHaveBeenCalledTimes(1);
  });

  it('keeps an invalid successful response in the retryable technical error path', async () => {
    mocks.searchWiki.mockRejectedValueOnce(new QueryApiError('The query response did not match the expected schema.', 502, 'invalid_response'));
    render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'schema failure query' } });
      fireEvent.submit(input.closest('form') as HTMLFormElement);
    });

    expect(await screen.findByText('QueryErrors.queryFailure')).toBeTruthy();
    expect(screen.queryByText('QueryResults.insufficientEvidence')).toBeNull();
    expect(screen.queryByText('QueryResults.noResults')).toBeNull();
  });

  it('shows safe authentication failures while keeping Wiki results and opening BYOK settings', async () => {
    mocks.useRealLocaleCopy = true;
    mocks.locale = 'en';
    mocks.searchWiki.mockResolvedValueOnce({
      results: [{ id: 'routing', slug: 'byok-profile-routing', title: 'BYOK Profile Routing', type: 'concept', excerpt: 'Project profile routing.', content: '' }],
      aiAnswer: '',
      citations: [],
      ai_operation_failures: [
        { stage: 'query_expansion', class: 'authentication', fallback: 'deterministic_plan' },
        { stage: 'answer_synthesis', class: 'authentication', fallback: 'retrieval_only' },
      ],
    });
    const settingsEvent = vi.fn();
    const onSettingsEvent = (event: Event) => settingsEvent((event as CustomEvent).detail);
    window.addEventListener('lwc-open-account-settings', onSettingsEvent);

    try {
      render(<HomeClient />);
      await waitForInitialSearchState();
      const input = screen.getByRole('textbox');
      const form = input.closest('form') as HTMLFormElement;
      await act(async () => {
        fireEvent.change(input, { target: { value: 'How does BYOK profile routing work?' } });
        fireEvent.click(screen.getByRole('button', { name: 'Full' }));
        fireEvent.submit(form);
      });

      const warning = await screen.findByRole('alert');
      expect(warning.textContent).toContain('An AI step could not be completed.');
      expect(warning.textContent).toContain('The selected AI provider rejected authentication. Check its API key and settings.');
      expect(warning.textContent).toContain('Wiki results are shown below.');
      expect(warning.textContent).not.toContain('synthetic-invalid-fixture-key');
      expect(await screen.findByRole('button', { name: /BYOK Profile Routing/ })).toBeTruthy();

      fireEvent.click(screen.getByRole('button', { name: 'Review AI provider settings' }));
      expect(settingsEvent).toHaveBeenCalledWith({ focusBYOK: true });
    } finally {
      window.removeEventListener('lwc-open-account-settings', onSettingsEvent);
    }
  });

  it('reports an expansion fallback without claiming a successful answer failed', async () => {
    mocks.useRealLocaleCopy = true;
    mocks.locale = 'en';
    mocks.searchWiki.mockResolvedValueOnce({
      results: [{ id: 'routing', slug: 'byok-profile-routing', title: 'BYOK Profile Routing', type: 'concept', excerpt: 'Project profile routing.', content: '' }],
      aiAnswer: 'The Project uses its selected profile for model calls.',
      citations: [],
      ai_operation_failures: [
        { stage: 'query_expansion', class: 'authentication', fallback: 'deterministic_plan' },
      ],
    });
    render(<HomeClient />);
    await waitForInitialSearchState();
    const input = screen.getByRole('textbox');
    await act(async () => {
      fireEvent.change(input, { target: { value: 'How does BYOK profile routing work?' } });
      fireEvent.click(screen.getByRole('button', { name: 'Full' }));
      fireEvent.submit(input.closest('form') as HTMLFormElement);
    });

    const warning = await screen.findByRole('alert');
    expect(warning.textContent).toContain('Search expansion failed; the original question was searched.');
    expect(warning.textContent).not.toContain('The AI answer could not be generated');
    expect(await screen.findByText('The Project uses its selected profile for model calls.')).toBeTruthy();
  });
});
