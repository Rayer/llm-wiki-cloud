import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getConcept: vi.fn(),
  getConcepts: vi.fn(),
  getSource: vi.fn(),
  getStatus: vi.fn(),
  searchWiki: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getConcept: mocks.getConcept,
    getConcepts: mocks.getConcepts,
    getSource: mocks.getSource,
    getStatus: mocks.getStatus,
    searchWiki: mocks.searchWiki,
  };
});

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
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
    }) => <a href={href} onClick={onClick} {...props}>{children}</a>,
    useNavigationBlocker: () => ({
      confirmNavigation: () => true,
      consumeCapturedConfirmation: () => false,
      setBlocked: () => undefined,
    }),
  };
});

import { HomeClient } from '@/components/HomeClient';

const citations = [
  { text: 'A', type: 'concept' as const, id: 'concept-a', slug: 'a-slug', path: '/concepts/a-slug' },
  { text: 'Shared title', type: 'source' as const, id: 'source-b', slug: 'b-slug', path: '/sources/b-slug' },
  { text: 'Shared title', type: 'concept' as const, id: 'concept-c', slug: 'c-slug', path: '/concepts/c-slug' },
];

async function runSearch() {
  render(<HomeClient />);
  const input = await screen.findByRole('textbox');
  fireEvent.change(input, { target: { value: 'topic' } });
  fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));
  await screen.findByRole('heading', { name: 'Demo.answer' });
}

beforeEach(() => {
  mocks.getConcepts.mockResolvedValue([]);
  mocks.getStatus.mockResolvedValue({ sourcesCount: 0, conceptsCount: 0, rawCount: 0, suggestedQueries: [], raw: {} });
  mocks.getConcept.mockImplementation((id: string) => Promise.resolve({
    id,
    slug: `${id}-slug`,
    title: id,
    content: `${id} content`,
    raw: '',
  }));
  mocks.getSource.mockImplementation((id: string) => Promise.resolve({
    id,
    slug: `${id}-slug`,
    title: id,
    content: `${id} content`,
    raw: '',
  }));
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-274 query citation links', () => {
  it('renders every API citation in order and opens inventory B by canonical identity', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Only [A] is mentioned inline.',
      citations,
    });

    await runSearch();

    const inventory = screen.getByRole('list', { name: 'Demo.sources' });
    expect(within(inventory).getAllByRole('listitem')).toHaveLength(3);
    expect(within(inventory).getAllByRole('button').map((button) => button.textContent)).toEqual([
      'A',
      'Shared title',
      'Shared title',
    ]);
    expect(screen.getAllByRole('button', { name: 'A' })).toHaveLength(1);
    expect(screen.getByText('Demo.sourcesCount')).toBeTruthy();

    fireEvent.click(within(inventory).getAllByRole('button')[1]);

    expect(await screen.findByText('source-b content')).toBeTruthy();
    expect(mocks.getSource).toHaveBeenCalledWith('source-b');
    expect(mocks.getConcept).not.toHaveBeenCalledWith('source-b');

    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close' }));
    fireEvent.click(within(inventory).getAllByRole('button')[2]);
    expect(await screen.findByText('concept-c content')).toBeTruthy();
    expect(mocks.getConcept).toHaveBeenCalledWith('concept-c');
  });

  it('fails closed for an ambiguous inline label while keeping both inventory identities bound', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Inline [Shared title] stays plain.',
      citations: [citations[1], citations[2]],
    });

    await runSearch();

    const answer = screen.getByRole('article');
    expect(within(answer).queryByRole('button', { name: 'Shared title' })).toBeNull();

    const inventory = screen.getByRole('list', { name: 'Demo.sources' });
    const inventoryItems = within(inventory).getAllByRole('button');
    expect(inventoryItems).toHaveLength(2);
    expect(inventoryItems.map((button) => button.textContent)).toEqual(['Shared title', 'Shared title']);

    fireEvent.click(inventoryItems[0]);
    expect(await screen.findByText('source-b content')).toBeTruthy();
    expect(mocks.getSource).toHaveBeenCalledWith('source-b');
    expect(mocks.getConcept).not.toHaveBeenCalledWith('source-b');

    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close' }));
    fireEvent.click(inventoryItems[1]);
    expect(await screen.findByText('concept-c content')).toBeTruthy();
    expect(mocks.getConcept).toHaveBeenCalledWith('concept-c');
  });

  it('keeps repeated inline A references separate while keeping one source inventory item', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: '[A] supports this, and [A] supports that.',
      citations: [citations[0]],
    });

    await runSearch();

    const inventory = screen.getByRole('list', { name: 'Demo.sources' });
    expect(within(inventory).getAllByRole('listitem')).toHaveLength(1);
    expect(within(inventory).getAllByRole('button')).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'A' })).toHaveLength(2);
  });

  it('does not auto-link ordinary titles, unknown labels, or protected Markdown contexts', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Shared title and [unknown] and [Shared title](https://example.invalid) and ` [A] `.',
      citations: [citations[0], citations[1]],
    });

    await runSearch();

    const answer = screen.getByRole('article');
    expect(within(answer).getAllByRole('list', { name: 'Demo.sources' })).toHaveLength(1);
    expect(within(answer).queryAllByRole('button', { name: 'A' })).toHaveLength(0);
    expect(within(answer).queryByRole('button', { name: 'unknown' })).toBeNull();
    expect(within(answer).getByRole('link', { name: 'Shared title' })).toBeTruthy();
    expect(within(answer).getByText('[A]')).toBeTruthy();
  });

  it('renders no source inventory for empty citations', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'There are no sources.',
      citations: [],
    });

    await runSearch();

    expect(screen.queryByRole('list', { name: 'Demo.sources' })).toBeNull();
  });
});
