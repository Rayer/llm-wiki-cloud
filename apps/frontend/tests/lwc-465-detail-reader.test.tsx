import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { configureApiAuth, type WikiEntry } from '@/lib/api';
import { DetailClient } from '@/components/DetailClient';

vi.mock('@/components/MarkdownView', () => ({
  MarkdownView: ({ content }: { content: string }) => <div>{content}</div>,
}));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a href={href} {...props}>{children}</a>,
}));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it('renders typed related cards and previews the mapped original Raw file', async () => {
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  configureApiAuth({ getAccessToken: () => 'fixture-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  const fetchMock = vi.fn(async (input: string | URL | Request) => {
    const url = String(input);
    if (url.endsWith('/api/v1/concepts')) return Response.json([]);
    if (url.includes('/api/v1/raw/topic/recipe.md?preview=true')) return new Response('Original recipe notes.');
    throw new Error(`Unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  const entry: WikiEntry = {
    id: 'source-id', slug: 'recipe', title: 'Recipe source', content: 'Source detail.',
    rawPath: 'raw/topic/recipe.md', annotationAllowed: false,
    relatedConcepts: [{ id: 'concept-id', slug: 'steam-cooking', title: 'Steam cooking', type: 'concept' }],
    relatedSources: [{ id: 'other-source', slug: 'vegetables', title: 'Vegetables', type: 'source' }],
    raw: {},
  };

  render(<DetailClient slug="recipe" label="Sources" backHref="/sources" load={async () => entry} entryType="source" />);

  expect(await screen.findByRole('heading', { name: 'Detail.relatedConcepts' })).toBeTruthy();
  expect(screen.getByRole('link', { name: 'Steam cooking' }).getAttribute('href')).toBe('/concepts/steam-cooking');
  expect(screen.getByRole('link', { name: 'Vegetables' }).getAttribute('href')).toBe('/sources/vegetables');
  expect(screen.getByText('Detail.rawMappingVerified')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Source.previewRaw' }));
  expect(await screen.findByText('Original recipe notes.')).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/raw/topic/recipe.md?preview=true'), expect.anything());
});
