import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getConcept: vi.fn(),
  getConcepts: vi.fn(),
  getStatus: vi.fn(),
  searchWiki: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getConcept: mocks.getConcept, getConcepts: mocks.getConcepts, getStatus: mocks.getStatus, searchWiki: mocks.searchWiki };
});

vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: mocks.currentProject }) }));
vi.mock('@/components/NavigationBlocker', async () => {
  const actual = await vi.importActual<typeof import('@/components/NavigationBlocker')>('@/components/NavigationBlocker');
  return {
    ...actual,
    NavigationLink: ({ href, children, onClick, ...props }: { href: string; children: React.ReactNode; onClick?: React.MouseEventHandler }) => (
      <a href={href} onClick={onClick} {...props}>{children}</a>
    ),
    useNavigationBlocker: () => ({ confirmNavigation: () => true, consumeCapturedConfirmation: () => false, setBlocked: () => undefined }),
  };
});

import { HomeClient } from '@/components/HomeClient';

beforeEach(() => {
  mocks.getStatus.mockResolvedValue({ sourcesCount: 0, conceptsCount: 1, rawCount: 0, suggestedQueries: [], raw: {} });
  mocks.getConcepts.mockResolvedValue([]);
  mocks.searchWiki.mockResolvedValue({
    results: [],
    aiAnswer: '## Key points\n\n- *Italic* phrase\n- **Bold** item with `code`\n- second item\n\n1. First\n2. Second\n\nSee [Concept](https://example.invalid/concept-doc).\nSee [Concept](https://llm-wiki.invalid/citation/Concept).\n![Concept](https://example.invalid/concept.png)\nSee [Concept].\nEscaped \\[Concept]\nDecimal &#91;Concept]\nHex &#x5B;Concept]\nNamed &lbrack;Concept]\nRaw HTML <span>[Concept]</span>\nMixed [Concept](https://example.invalid/mixed) ![Concept](https://example.invalid/mixed.png) ` [Concept] `\n\nUnsafe [javascript](javascript:alert(1)) and [data](data:text/html,<script>alert(1)</script>).\n\n<script>alert(1)</script>',
    citations: [{ text: 'Concept', slug: 'concept', id: 'concept-id', type: 'concept' }],
  });
  mocks.getConcept.mockResolvedValue({ slug: 'concept', id: 'concept-id', title: 'Concept', content: 'Concept content.', raw: '' });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-15 AI-answer Markdown rendering', () => {
  it('renders block Markdown semantically while preserving inline formatting and citation buttons', async () => {
    const { container } = render(<HomeClient />);
    const input = await screen.findByRole('textbox');
    fireEvent.change(input, { target: { value: 'topic' } });
    fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));

    expect(await screen.findByText('Demo.answer')).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Key points', level: 2 })).toBeTruthy();
    expect(screen.getAllByRole('list')).toHaveLength(2);
    expect(screen.getAllByRole('listitem')).toHaveLength(5);
    expect(screen.getByText('Italic').tagName).toBe('EM');
    expect(screen.getByText('Bold').tagName).toBe('STRONG');
    expect(screen.getByText('code').tagName).toBe('CODE');
    expect(screen.getByText('<script>alert(1)</script>')).toBeTruthy();
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelectorAll('img')).toHaveLength(0);
    expect(screen.getAllByText('[Image: Concept]')).toHaveLength(2);
    const safeLink = screen.getAllByRole('link', { name: 'Concept', hidden: true }).find((link) => link.getAttribute('href') === 'https://example.invalid/concept-doc');
    expect(safeLink?.getAttribute('href')).toBe('https://example.invalid/concept-doc');
    expect(safeLink?.getAttribute('rel')).toBe('noopener noreferrer');
    expect(container.querySelector('a[href="https://llm-wiki.invalid/citation/Concept"]')).toBeTruthy();
    expect(container.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(container.querySelector('a[href^="data:"]')).toBeNull();
    expect(screen.getAllByRole('button', { name: 'Concept' })).toHaveLength(1);
    expect(container.textContent).toContain('Escaped [Concept]');
    expect(container.textContent).toContain('Decimal [Concept]');
    expect(container.textContent).toContain('Hex [Concept]');
    expect(container.textContent).toContain('Named [Concept]');
    expect(container.textContent).toContain('Raw HTML <span>[Concept]</span>');

    fireEvent.click(screen.getByRole('button', { name: 'Concept' }));
    await waitFor(() => expect(screen.getByRole('dialog')).toBeTruthy());
    expect(screen.getByText('Concept content.')).toBeTruthy();
  });

  it('keeps nested raw HTML literal while preserving standalone markdown citation behavior', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [],
      aiAnswer: 'Nested raw <span><em>[Concept]</em></span> stays literal.',
      citations: [{ text: 'Concept', slug: 'concept', id: 'concept-id', type: 'concept' }],
    });

    const { container } = render(<HomeClient />);
    const input = await screen.findByRole('textbox');
    fireEvent.change(input, { target: { value: 'topic' } });
    fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));

    expect(await screen.findByText('Demo.answer')).toBeTruthy();
    const resultText = container.textContent || '';
    expect(resultText).toContain('<span>');
    expect(resultText).toContain('<em>');
    expect(resultText).toContain('[Concept]');
    expect(screen.queryByRole('button', { name: 'Concept' })).toBeNull();
  });

  it('fails closed on malformed raw HTML closes and preserves only genuine trailing plain citations', async () => {
    const testCases = [
      { label: 'top-level unmatched close', aiAnswer: '</em>[Concept]', expectedButtons: 0 },
      { label: 'top-level unmatched close then newline', aiAnswer: '</em>\n[Concept]', expectedButtons: 0 },
      { label: 'top-level unmatched void close', aiAnswer: '</br>[Concept]', expectedButtons: 0 },
      { label: 'mismatched close while another tag is open', aiAnswer: '<span></em>[Concept]</span>', expectedButtons: 0 },
      { label: 'well-formed raw HTML then plain token', aiAnswer: '<span>literal token</span> [Concept]', expectedButtons: 1 },
    ] as const;

    for (const tc of testCases) {
      mocks.searchWiki.mockResolvedValue({
        results: [],
        aiAnswer: tc.aiAnswer,
        citations: [{ text: 'Concept', slug: 'concept', id: 'concept-id', type: 'concept' }],
      });

      render(<HomeClient />);
      const input = await screen.findByRole('textbox');
      fireEvent.change(input, { target: { value: `topic ${tc.label}` } });
      fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));

      expect(await screen.findByText('Demo.answer')).toBeTruthy();
      expect(screen.queryAllByRole('button', { name: 'Concept' })).toHaveLength(tc.expectedButtons);
      cleanup();
    }
  });
});
