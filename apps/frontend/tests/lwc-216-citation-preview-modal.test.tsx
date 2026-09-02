import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const {
  act: testingLibraryAct,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} = await import('@testing-library/react');

void testingLibraryAct;

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getConcept: vi.fn(),
  getSource: vi.fn(),
  getStatus: vi.fn(),
  getConcepts: vi.fn(),
  searchWiki: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getConcept: mocks.getConcept,
    getSource: mocks.getSource,
    getStatus: mocks.getStatus,
    getConcepts: mocks.getConcepts,
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
import type { ApiStatus, WikiEntry } from '@/lib/api';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function status(): ApiStatus {
  return {
    sourcesCount: 2,
    conceptsCount: 3,
    rawCount: 1,
    suggestedQueries: [],
    raw: {},
  };
}

function conceptEntry(overrides: Partial<WikiEntry> = {}): WikiEntry {
  return {
    slug: 'concept-example',
    title: 'Concept Example',
    content: 'Loaded concept body',
    raw: '',
    ...overrides,
  };
}

function sourceEntry(overrides: Partial<WikiEntry> = {}): WikiEntry {
  return {
    slug: 'source-example',
    title: 'Source Example',
    content: 'Loaded source body',
    raw: '',
    ...overrides,
  };
}

async function runSearch() {
  const input = screen.getByRole('textbox');
  fireEvent.change(input, { target: { value: 'topic' } });
  fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));
}

beforeEach(async () => {
  mocks.getStatus.mockResolvedValue(status());
  mocks.getConcepts.mockResolvedValue([]);
  mocks.searchWiki.mockResolvedValue({
    results: [],
    aiAnswer: '',
    citations: [],
  });

  render(<HomeClient />);
  await waitFor(() => expect(screen.getByRole('textbox')).toBeTruthy());
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-216 citation preview modal behavior', () => {
  it('prefers the canonical id when a completed-query result card opens the modal', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [{ id: 'result-canonical-id', slug: 'decorative-slug', title: 'Result Concept', type: 'concept' }],
      aiAnswer: '',
      citations: [],
    });
    mocks.getConcept.mockResolvedValue(conceptEntry({
      id: 'result-canonical-id',
      slug: 'decorative-slug',
      title: 'Result Concept',
      content: 'Result content.',
    }));

    await runSearch();
    fireEvent.click(await screen.findByRole('button', { name: /Result Concept/ }));

    expect(await screen.findByText('Result content.')).toBeTruthy();
    expect(mocks.getConcept).toHaveBeenCalledWith('result-canonical-id');
  });

  it('opens concept citation modal with loaded concept details via canonical id', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'See [Concept].',
      citations: [{
        text: 'Concept',
        slug: 'concept-from-body',
        id: 'c-id',
        type: 'concept',
      }],
    });

    mocks.getConcept.mockResolvedValue(conceptEntry({ title: 'Loaded Concept', slug: 'concept-canonical', id: 'c-id', content: 'Concept content.' }));
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Concept' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Concept' }));
    expect(await screen.findByRole('dialog')).toBeTruthy();
    expect(await screen.findByText('Concept content.')).toBeTruthy();
    expect(mocks.getConcept).toHaveBeenCalledWith('c-id');
    expect(screen.getByRole('link', { name: 'Detail.openFullPage' }).getAttribute('href')).toEqual(
      '/concepts/c-id-concept-canonical',
    );
  });

  it('opens source citation modal with loaded source details', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Inspect [Source].',
      citations: [{
        text: 'Source',
        slug: 'source-from-body',
        id: 's-id',
        type: 'source',
      }],
    });

    mocks.getSource.mockResolvedValue(sourceEntry({ title: 'Loaded Source', slug: 'source-canonical', id: 's-id', content: 'Source content.' }));
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Source' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Source' }));
    expect(await screen.findByRole('dialog')).toBeTruthy();
    expect(await screen.findByText('Source content.')).toBeTruthy();
    expect(mocks.getSource).toHaveBeenCalledWith('s-id');
    expect(screen.getByRole('link', { name: 'Detail.openFullPage' }).getAttribute('href')).toEqual(
      '/sources/s-id-source-canonical',
    );
  });

  it('rejects unsafe explicit citation locators before detail fetch', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Inspect [Unsafe].',
      citations: [{ text: 'Unsafe', slug: '..', id: '..', type: 'concept' }],
    });
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Unsafe' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Unsafe' }));

    expect(await screen.findByRole('dialog')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('Detail.citationLoadFailed');
    expect(mocks.getConcept).not.toHaveBeenCalled();
    expect(mocks.getSource).not.toHaveBeenCalled();
    expect(screen.queryByRole('link', { name: 'Detail.openFullPage' })).toBeNull();
  });

  it('keeps modal open and shows retry when detail fetch fails, then succeeds on retry', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Open [Retry].',
      citations: [{ text: 'Retry', slug: 'retry', id: 'r-id', type: 'concept' }],
    });

    mocks.getConcept
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce(conceptEntry({ title: 'Recovered Concept', content: 'Recovered content.' }));
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog.getAttribute('aria-labelledby')).toBe('citation-modal-title');
    expect(within(dialog).getByRole('heading', { name: 'Retry' })).toBeTruthy();
    expect(within(dialog).getByRole('button', { name: 'Detail.retryCitation' })).toBeTruthy();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Detail.retryCitation' }));
    expect(await screen.findByText('Recovered content.')).not.toBeNull();
    expect(mocks.getConcept).toHaveBeenCalledTimes(2);
  });

  it('preserves newer citation response when older request resolves after it', async () => {
    const requestA = deferred<WikiEntry>();
    const requestB = deferred<WikiEntry>();

    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Compare [First] and [Second].',
      citations: [
        { text: 'First', slug: 'first', id: 'a-id', type: 'concept' },
        { text: 'Second', slug: 'second', id: 'b-id', type: 'concept' },
      ],
    });
    mocks.getConcept.mockImplementation((id: string) => {
      if (id === 'a-id') return requestA.promise;
      if (id === 'b-id') return requestB.promise;
      return Promise.reject(new Error(`unexpected id ${id}`));
    });
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'First' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'First' }));
    fireEvent.click(screen.getByRole('button', { name: 'Second' }));

    requestB.resolve(conceptEntry({ title: 'Second Entry', content: 'second-content', id: 'b-id', slug: 'second-canonical' }));
    expect(await screen.findByText('second-content')).not.toBeNull();

    requestA.resolve(conceptEntry({ title: 'First Entry', content: 'first-content', id: 'a-id', slug: 'first-canonical' }));

    expect(screen.queryByText('first-content')).toBeNull();
    expect(screen.getByText('second-content')).not.toBeNull();
  });

  it('does not reopen a closed modal when the pending request resolves later', async () => {
    const detail = deferred<WikiEntry>();
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Open [Keep it pending].',
      citations: [{ text: 'Keep it pending', slug: 'close', id: 'c-id', type: 'concept' }],
    });
    mocks.getConcept.mockReturnValue(detail.promise);
    await runSearch();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Keep it pending' })).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Keep it pending' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }));

    detail.resolve(conceptEntry({ title: 'Late', content: 'late-content', id: 'c-id', slug: 'close-canonical' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(screen.queryByText('late-content')).toBeNull();
  });

  it('keeps bracket tokens without payload as plain text', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'A [missing] bracket token with no citation payload.',
      citations: [{ text: 'Something Else', slug: 'unused', type: 'concept' }],
    });
    await runSearch();

    const article = await screen.findByRole('article');
    expect(article.textContent).toContain('A [missing] bracket token with no citation payload.');
    expect(screen.queryByRole('button', { name: 'missing' })).toBeNull();
  });
});
