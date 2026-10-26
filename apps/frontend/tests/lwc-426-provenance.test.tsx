import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import type { WikiEntry } from '@/lib/api';
import { DetailClient } from '@/components/DetailClient';

const mocks = vi.hoisted(() => ({ getConcepts: vi.fn().mockResolvedValue([]) }));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, getConcepts: mocks.getConcepts };
});
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/MarkdownView', () => ({
  MarkdownView: ({ content }: { content: string }) => <div data-testid="article-body">{content}</div>,
}));
vi.mock('@/components/NavigationBlocker', () => ({
  NavigationLink: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
  useNavigationBlocker: () => ({ setBlocked: vi.fn() }),
}));

type DetailResponse = {
  slug: string;
  title: string;
  id?: string;
  status?: string;
  frontmatter: Record<string, unknown>;
  body: string;
  raw?: string;
};

function entryFromResponse(response: DetailResponse): WikiEntry {
  return {
    slug: response.slug,
    title: response.title,
    id: response.id,
    status: response.status,
    frontmatter: response.frontmatter,
    content: response.body,
    raw: response.raw,
  };
}

function conceptEntry(frontmatter: Record<string, unknown>, slug = 'alpha'): WikiEntry {
  return { slug, title: slug, frontmatter, content: 'Article body.', raw: frontmatter };
}

function renderConcept(entry: WikiEntry) {
  return render(
    <DetailClient
      slug={entry.slug}
      label="Concepts"
      backHref="/concepts"
      load={async () => entry}
      entryType="concept"
    />,
  );
}

beforeEach(() => mocks.getConcepts.mockResolvedValue([]));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it('renders latest provenance and preserves full lineage from the producer-publication-BFF response', async () => {
  const responsePath = process.env.LWC426_API_RESPONSE_PATH;
  const response: DetailResponse = responsePath
    ? JSON.parse(await readFile(responsePath, 'utf8')) as DetailResponse
    : {
      slug: 'alpha',
      title: 'Alpha',
      id: 'stable-alpha',
      status: 'published',
      body: 'Same generated body for both compile runs.',
      frontmatter: {
        id: 'stable-alpha',
        lineage: [
          {
            compile_run: '01JAZ5N7Y3K8M2Q4R6T9VWXAC3',
            pipeline: { fast_model: 'legacy-fast-model', heavy_model: 'legacy-heavy-model' },
            timestamp: '2026-10-25T09:00:00',
            generator: { implementation: 'olw', version: '0.8.5', revision: 'fixture-r1' },
          },
          {
            compile_run: '01JAZ5N7Y3K8M2Q4R6T9VWXAC1',
            pipeline: { fast_model: 'synto-fast-model', heavy_model: 'synto-heavy-model' },
            timestamp: '2026-10-26T10:00:00',
            generator: { implementation: 'synto', version: '0.7.0' },
          },
          {
            compile_run: '01JAZ5N7Y3K8M2Q4R6T9VWXAC2',
            pipeline: { fast_model: 'synto-fast-model', heavy_model: 'synto-heavy-model' },
            timestamp: '2026-10-27T11:00:00',
            generator: { implementation: 'synto', version: '0.7.0' },
          },
        ],
      },
    };

  renderConcept(entryFromResponse(response));

  expect(await screen.findByTestId('concept-generation-provenance')).toBeTruthy();
  expect(screen.getByText('synto')).toBeTruthy();
  expect(screen.getByText('0.7.0')).toBeTruthy();
  expect(screen.getByText('01JAZ5N7Y3K8M2Q4R6T9VWXAC2')).toBeTruthy();
  const lineageLabel = screen.getByText('lineage');
  const lineageText = lineageLabel.parentElement?.querySelector('dd')?.textContent ?? '';
  expect(lineageText).toContain('01JAZ5N7Y3K8M2Q4R6T9VWXAC3');
  expect(lineageText).toContain('"pipeline"');
  expect(lineageText).toContain('"timestamp"');
  expect(lineageText).toContain('fixture-r1');
  expect(screen.getByTestId('article-body').textContent).toBe(response.body);
});

it('shows unknown for the latest generation instead of borrowing an older known identity', async () => {
  renderConcept(conceptEntry({
    lineage: [
      {
        compile_run: 'older-run',
        pipeline: { fast_model: 'older-fast-model', heavy_model: 'older-heavy-model' },
        timestamp: '2026-10-26T10:00:00',
        generator: { implementation: 'olw', version: '0.8.5' },
      },
      {
        compile_run: 'latest-run',
        pipeline: { fast_model: 'latest-fast-model', heavy_model: 'latest-heavy-model' },
        timestamp: '2026-10-27T11:00:00',
      },
    ],
  }));

  expect(await screen.findByTestId('concept-generation-provenance')).toBeTruthy();
  expect(screen.getAllByText('ConceptProvenance.unknown')).toHaveLength(3);
  expect(screen.getByText('latest-run')).toBeTruthy();
  const lineageLabel = screen.getByText('lineage');
  const lineageText = lineageLabel.parentElement?.querySelector('dd')?.textContent ?? '';
  expect(lineageText).toContain('older-run');
  expect(lineageText).toContain('older-fast-model');
  expect(lineageText).toContain('"implementation":"olw"');
  expect(lineageText).toContain('"version":"0.8.5"');
  expect(lineageText).toContain('2026-10-26T10:00:00');
  expect(lineageText).toContain('latest-fast-model');
  expect(lineageText).toContain('2026-10-27T11:00:00');
  const provenance = screen.getByTestId('concept-generation-provenance');
  expect(provenance.textContent).not.toContain('olw');
  expect(provenance.textContent).not.toContain('older-run');
});

it('shows historical OLW identity exactly as stored and unknown for unrecorded history', async () => {
  const historical = conceptEntry({
    lineage: [{
      compile_run: 'olw-run-from-record',
      generator: { implementation: 'OLW', version: '0.8.5', revision: 'fixture-r1' },
    }],
  }, 'historical-concept');
  const mounted = renderConcept(historical);
  expect(await screen.findByText('OLW')).toBeTruthy();
  expect(screen.getByText('0.8.5')).toBeTruthy();
  expect(screen.getByText('fixture-r1')).toBeTruthy();
  expect(screen.getByText('olw-run-from-record')).toBeTruthy();

  mounted.rerender(
    <DetailClient
      slug="platform-engineering"
      label="Concepts"
      backHref="/concepts"
      load={async () => conceptEntry({ lineage: [{ compile_run: 'historical-run' }] }, 'platform-engineering')}
      entryType="concept"
    />,
  );
  expect(await screen.findAllByText('ConceptProvenance.unknown')).toHaveLength(3);
  expect(screen.queryByText('synto')).toBeNull();
  expect(screen.queryByText('OLW')).toBeNull();
});

it('provides localized provenance labels', async () => {
  const en = JSON.parse(await readFile(resolve(process.cwd(), 'src/messages/en.json'), 'utf8'));
  const zh = JSON.parse(await readFile(resolve(process.cwd(), 'src/messages/zh-TW.json'), 'utf8'));

  expect(en.ConceptProvenance).toEqual({
    title: 'Generation provenance',
    implementation: 'Implementation',
    version: 'Version',
    revision: 'Revision',
    compileRun: 'Compile run',
    unknown: 'Unknown / not recorded',
  });
  expect(zh.ConceptProvenance).toEqual({
    title: '內容生成來源',
    implementation: '實作',
    version: '版本',
    revision: '修訂版',
    compileRun: '編譯執行識別碼',
    unknown: '未知／未記錄',
  });
});
