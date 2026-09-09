import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';

const mocks = vi.hoisted(() => ({ getSources: vi.fn(), getConcepts: vi.fn(), currentProject: { id: 'demo' } }));
vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getSources: mocks.getSources,
  getConcepts: mocks.getConcepts,
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: mocks.currentProject }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props}>{children}</a>,
}));

import { SourceListClient } from '@/components/SourceListClient';
import { DetailClient } from '@/components/DetailClient';

beforeEach(() => {
  localStorage.clear();
  mocks.getConcepts.mockResolvedValue([]);
});
afterEach(() => { cleanup(); vi.clearAllMocks(); });

it('distinguishes no matches from an empty corpus and clears the filter', async () => {
  mocks.getSources.mockResolvedValue([{ id: 's1', title: 'Guide', slug: 'guide', rawPath: 'raw/guide.md', lifecycle: 'synced', annotationPresent: false }]);
  render(<SourceListClient />);
  await screen.findByRole('link', { name: 'Guide' });
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'no-match' } });
  expect(screen.getByText('找不到符合「no-match」的 來源。')).toBeDefined();
  expect(screen.queryByText('尚無來源。請先上傳內容並執行 Pipeline。')).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: '清除搜尋' }));
  expect(screen.getByRole('link', { name: 'Guide' })).toBeDefined();
  expect(document.activeElement).toBe(screen.getByRole('searchbox'));
});

it('keeps onboarding for a genuinely empty source corpus', async () => {
  mocks.getSources.mockResolvedValue([]);
  render(<SourceListClient />);
  expect(await screen.findByText('尚無來源。請先上傳內容並執行 Pipeline。')).toBeDefined();
  expect(screen.queryByRole('button', { name: '清除搜尋' })).toBeNull();
});

it('reads the body before collapsed metadata without repeating a whitespace-prefixed title', async () => {
  const load = vi.fn().mockResolvedValue({ slug: 'guide', title: 'Guide', content: '\n\r\n# Guide\r\n\r\n## Summary\r\nRead this first.', frontmatter: { source_file: 'raw/guide.md' } });
  const { container } = render(<DetailClient slug="guide" label="Sources" backHref="/sources" load={load} entryType="source" />);
  await screen.findByText('Read this first.');
  expect(screen.getAllByRole('heading', { name: 'Guide' })).toHaveLength(1);
  const article = container.querySelector('article')!;
  const details = container.querySelector('details')!;
  expect(details.open).toBe(false);
  expect(article.compareDocumentPosition(details) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  fireEvent.click(screen.getByText('文件資訊'));
  expect(details.open).toBe(true);
  expect(within(details).getByRole('link', { name: 'raw/guide.md' }).getAttribute('href')).toBe('/raw?file=guide.md');
  expect(screen.getByRole('link', { name: '返回 來源' })).toBeDefined();
});
