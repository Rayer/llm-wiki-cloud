import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ScrapeResult } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  token: 'session-a' as string | null,
  isDemoSession: false,
  getPipelineStatus: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  scrapeUrl: vi.fn(),
  confirmScrape: vi.fn(),
  triggerPipeline: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  getRecompileAllCapability: mocks.getRecompileAllCapability,
  scrapeUrl: mocks.scrapeUrl,
  confirmScrape: mocks.confirmScrape,
  triggerPipeline: mocks.triggerPipeline,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    token: mocks.token,
    isDemoSession: mocks.isDemoSession,
  }),
}));

vi.mock('@/components/RawUploadPanel', () => ({ RawUploadPanel: () => null }));
vi.mock('@/components/ExportPanel', () => ({ ExportPanel: () => null }));
vi.mock('@/lib/i18n', () => ({ useT: () => ({ t: (key: string) => key }) }));

import { PipelineClient } from '@/components/PipelineClient';

const preview = {
  title: 'Example article',
  source_url: 'https://example.com/article',
  markdown: '---\ntitle: Example article\nsource: https://example.com/article\ncreated: 2026-10-21\n---\n# Example article\nReadable body.',
  metadata: { title: 'Example article', source: 'https://example.com/article', created: '2026-10-21' },
  candidate_sha256: 'candidate-digest',
  provider: 'deepseek-fetch',
};

const stored: ScrapeResult = {
  status: 'stored',
  message: 'saved',
  preview,
  storage: { path: 'raw/Example-article.md', sha256: 'stored-digest', status: 'created' },
  processing_status: 'not_started',
};

const uncertain: ScrapeResult = {
  status: 'needs_confirmation',
  message: 'review candidate',
  confirmation_ref: 'opaque-confirmation-ref',
  expires_at: '2026-10-21T02:00:00Z',
  preview: { ...preview, reason: 'classification was uncertain' },
};

function submitUrl(url = 'https://example.com/article') {
  fireEvent.change(screen.getByPlaceholderText('https://example.com/article'), { target: { value: url } });
  const form = screen.getByPlaceholderText('https://example.com/article').closest('form');
  if (!form) throw new Error('URL intake form is not mounted');
  fireEvent.submit(form);
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.token = 'session-a';
  mocks.isDemoSession = false;
  mocks.getPipelineStatus.mockImplementation(async (projectId: string) => ({ project_id: projectId, quota: null }));
  mocks.getRecompileAllCapability.mockResolvedValue({ allowed: false, compile_mode: 'full', quota: null });
  mocks.scrapeUrl.mockResolvedValue(stored);
  mocks.confirmScrape.mockResolvedValue(stored);
  window.localStorage.setItem('locale', 'en');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it('submits a public URL to the selected Project and shows the saved preview and exact storage outcome without running Pipeline', async () => {
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByText('Pipeline.urlSaved')).toBeDefined();
  const bodyPreview = screen.getByText((_text, element) => element?.tagName === 'PRE');
  expect(bodyPreview.textContent).toContain('Readable body.');
  const storageOutcome = screen.getByText((_text, element) => element?.tagName === 'P' && Boolean(element.textContent?.includes('raw/Example-article.md')));
  expect(storageOutcome.textContent).toContain('created');
  expect(screen.getByText(/title: Example article/)).toBeDefined();
  expect(screen.queryByRole('button', { name: 'Pipeline.urlConfirmCandidate' })).toBeNull();
  await waitFor(() => expect(mocks.scrapeUrl).toHaveBeenCalledWith('https://example.com/article', 'project-a'));
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('shows uncertain content without a saved path and confirms only the bound candidate reference', async () => {
  mocks.scrapeUrl.mockResolvedValueOnce(uncertain);
  mocks.confirmScrape.mockResolvedValueOnce({
    ...stored,
    storage: { path: 'raw/Example-article.md', sha256: 'stored-digest', status: 'replaced' },
  });
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByText('Pipeline.urlNeedsConfirmation')).toBeDefined();
  expect(screen.getByText('Pipeline.urlConfirmationNotice')).toBeDefined();
  expect(screen.getByText('classification was uncertain')).toBeDefined();
  expect(screen.queryByText((_text, element) => element?.tagName === 'P' && Boolean(element.textContent?.includes('raw/Example-article.md')))).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByText('Pipeline.urlSaved')).toBeDefined();
  expect(await screen.findByText(/replaced/)).toBeDefined();
  await waitFor(() => expect(mocks.confirmScrape).toHaveBeenCalledWith('opaque-confirmation-ref', 'project-a'));
  expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1);
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('discards a late URL result after the selected Project changes', async () => {
  let resolveScrape!: (result: ScrapeResult) => void;
  mocks.scrapeUrl.mockReturnValue(new Promise<ScrapeResult>((resolve) => { resolveScrape = resolve; }));
  const view = render(<PipelineClient />);
  submitUrl();
  await waitFor(() => expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  resolveScrape(stored);

  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b'));
  expect(screen.queryByText('Example article')).toBeNull();
  expect(mocks.scrapeUrl).toHaveBeenCalledWith('https://example.com/article', 'project-a');
});
