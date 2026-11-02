import React from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { configureApiAuth, getConcept, getSource, type WikiEntry } from '@/lib/api';
import { DetailClient } from '@/components/DetailClient';

const router = vi.hoisted(() => ({ navigate: undefined as ((href: string) => void) | undefined }));

vi.mock('@/components/MarkdownView', () => ({
  MarkdownView: ({ content }: { content: string }) => <div>{content}</div>,
}));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ href, children, onClick, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} onClick={(event) => {
      onClick?.(event);
      if (!event.defaultPrevented) {
        event.preventDefault();
        router.navigate?.(href ?? '');
      }
    }} {...props}>{children}</a>
  ),
}));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));

function DetailRouteHarness() {
  const [path, setPath] = React.useState('/sources/seed-source');
  React.useEffect(() => {
    router.navigate = setPath;
    return () => { router.navigate = undefined; };
  }, [setPath]);
  const [, collection, encodedSegment] = path.split('/');
  const slug = decodeURIComponent(encodedSegment ?? '');
  const isConcept = collection === 'concepts';
  return (
    <DetailClient
      key={path}
      slug={slug}
      label={isConcept ? 'Concepts' : 'Sources'}
      backHref="/sources"
      load={isConcept ? getConcept : getSource}
      entryType={isConcept ? 'concept' : 'source'}
    />
  );
}

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
  expect(screen.getByRole('link', { name: 'Steam cooking' }).getAttribute('href')).toBe('/concepts/concept-id-steam-cooking');
  expect(screen.getByRole('link', { name: 'Vegetables' }).getAttribute('href')).toBe('/sources/other-source-vegetables');
  expect(screen.getByText('Detail.rawMappingVerified')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', { name: 'Source.previewRaw' }));
  expect(await screen.findByText('Original recipe notes.')).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/raw/topic/recipe.md?preview=true'), expect.anything());
});

it.each([
  {
    type: 'concept' as const,
    id: '01M6EM80E934Y066HXKDKGF87R',
    slug: '散步',
    title: 'Kyoto walking concept',
    path: '/concepts/01M6EM80E934Y066HXKDKGF87R-%E6%95%A3%E6%AD%A5',
    endpoint: '/api/v1/concepts/01M6EM80E934Y066HXKDKGF87R-%E6%95%A3%E6%AD%A5',
    content: 'Canonical concept page.',
  },
  {
    type: 'source' as const,
    id: '2e3270fc51cd',
    slug: 'note 6f7a52f2-88a2-4a7a-a3cf-e82ecc959344',
    title: 'Walking source note',
    path: '/sources/2e3270fc51cd-note%206f7a52f2-88a2-4a7a-a3cf-e82ecc959344',
    endpoint: '/api/v1/sources/2e3270fc51cd-note%206f7a52f2-88a2-4a7a-a3cf-e82ecc959344',
    content: 'Canonical source page.',
  },
])('navigates the $type related card through its canonical detail endpoint', async (target) => {
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  configureApiAuth({ getAccessToken: () => 'fixture-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  const sourceRecord = {
    id: 'seed-source', slug: 'seed-source', title: 'Seed source', body: 'Seed content.',
    related_concepts: target.type === 'concept' ? [{ id: target.id, slug: target.slug, title: target.title, type: 'concept' }] : [],
    related_sources: target.type === 'source' ? [{ id: target.id, slug: target.slug, title: target.title, type: 'source' }] : [],
  };
  const targetRecord = { id: target.id, slug: target.slug, title: target.title, body: target.content };
  const fetchMock = vi.fn(async (input: string | URL | Request) => {
    const url = String(input);
    if (url.endsWith('/api/v1/concepts')) return Response.json([]);
    if (url.endsWith('/api/v1/sources/seed-source')) return Response.json(sourceRecord);
    if (url.endsWith(target.endpoint)) return Response.json(targetRecord);
    throw new Error(`Unexpected request: ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);

  render(<DetailRouteHarness />);
  expect(await screen.findByRole('heading', { name: 'Seed source' })).toBeTruthy();
  const link = screen.getByRole('link', { name: target.title });
  expect(link.getAttribute('href')).toBe(target.path);
  fireEvent.click(link);
  expect(await screen.findByRole('heading', { name: target.title })).toBeTruthy();
  expect(await screen.findByText(target.content)).toBeTruthy();
  expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith(target.endpoint))).toBe(true);
});
