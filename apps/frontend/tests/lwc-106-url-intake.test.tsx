import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { ApiError, type ScrapeResult } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' } as { id: string; name: string } | null,
  token: 'session-a' as string | null,
  isDemoSession: false,
  getPipelineStatus: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  getProjectPipelineAutomation: vi.fn(),
  updateProjectPipelineAutomation: vi.fn(),
  scrapeUrl: vi.fn(),
  acceptScrapeCandidate: vi.fn(),
  rejectScrapeCandidate: vi.fn(),
  resolveScrapeCandidate: vi.fn(),
  triggerPipeline: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  getRecompileAllCapability: mocks.getRecompileAllCapability,
  getProjectPipelineAutomation: mocks.getProjectPipelineAutomation,
  updateProjectPipelineAutomation: mocks.updateProjectPipelineAutomation,
  scrapeUrl: mocks.scrapeUrl,
  acceptScrapeCandidate: mocks.acceptScrapeCandidate,
  rejectScrapeCandidate: mocks.rejectScrapeCandidate,
  resolveScrapeCandidate: mocks.resolveScrapeCandidate,
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
  markdown: '# Example article\n\nReadable body.',
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
  status: 'preview',
  message: 'Review the rendered candidate before saving.',
  candidate_id: 'opaque-candidate-id',
  candidate_revision: 'opaque-candidate-revision',
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
  mocks.getProjectPipelineAutomation.mockResolvedValue({
    enabled: false, revision: 0, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'idle',
    source_revision: 0, processed_revision: 0,
  });
  mocks.updateProjectPipelineAutomation.mockResolvedValue({
    enabled: true, revision: 1, admin_allowed: false, effective_enabled: false,
    blocked_reason: 'admin_disabled', backlog_scan_status: 'pending', pipeline_status: 'pending',
    source_revision: 1, processed_revision: 0,
  });
  mocks.scrapeUrl.mockResolvedValue(uncertain);
  mocks.acceptScrapeCandidate.mockResolvedValue(stored);
  mocks.rejectScrapeCandidate.mockResolvedValue({ status: 'rejected', message: 'Candidate declined.' });
  mocks.resolveScrapeCandidate.mockResolvedValue(stored);
  window.localStorage.setItem('locale', 'en');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it('saves the Project switch by revision and explains that Admin approval still gates processing', async () => {
  render(<PipelineClient />);

  const toggle = await screen.findByTestId('pipeline-automation-project-toggle') as HTMLInputElement;
  expect(toggle.checked).toBe(false);
  expect(screen.getByTestId('pipeline-automation-blocked').textContent).toContain('disabled by the Admin');
  fireEvent.click(toggle);

  await waitFor(() => expect(mocks.updateProjectPipelineAutomation).toHaveBeenCalledWith('project-a', true, 0));
  await waitFor(() => expect(toggle.checked).toBe(true));
  expect(screen.getByTestId('pipeline-automation-blocked').textContent).toContain('disabled by the Admin');
  expect(screen.getByTestId('pipeline-automation-settings').textContent).toContain('Processing status: pending');
});

it('renders a public URL candidate, binds the edited title, and links to the exact saved raw file without running Pipeline', async () => {
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByRole('dialog')).toBeDefined();
  expect(screen.getByText('Readable body.')).toBeDefined();
  const title = screen.getByLabelText('Pipeline.urlTitleLabel') as HTMLInputElement;
  fireEvent.change(title, { target: { value: 'Edited title' } });
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByText('saved')).toBeDefined();
  expect(screen.getByRole('link', { name: /Pipeline.urlRawArticle: raw\/Example-article\.md/ }).getAttribute('href'))
    .toBe('/raw?file=raw%2FExample-article.md');
  expect(screen.getByText(/When both auto-pipeline switches are on/)).toBeDefined();
  await waitFor(() => expect(mocks.acceptScrapeCandidate).toHaveBeenCalledWith('opaque-candidate-id', 'opaque-candidate-revision', 'Edited title', 'project-a'));
  await waitFor(() => expect(mocks.scrapeUrl).toHaveBeenCalledWith('https://example.com/article', 'project-a'));
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('shows the bounded Fetch dependency diagnosis on an unavailable URL result', async () => {
  mocks.scrapeUrl.mockResolvedValueOnce({
    status: 'unavailable',
    message: 'DeepSeek Fetch could not retrieve usable page content (upstream HTTP 404). Paste Markdown or upload a saved page capture instead.',
    failure_stage: 'fetch',
    failure_diagnostic: {
      dependency: 'deepseek-fetch',
      operation: 'fetch',
      category: 'content_unavailable',
      upstream_status: 404,
    },
  });
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByText(/DeepSeek Fetch could not retrieve usable page content/)).toBeDefined();
  expect(screen.getByText('deepseek-fetch fetch: content_unavailable (upstream HTTP 404)')).toBeDefined();
  expect(within(screen.getByRole('dialog')).getByText('Pipeline.urlManualCapture')).toBeDefined();
});

it('shows the localized unchecked-quality warning before save and after accepting the same candidate', async () => {
  const unchecked = {
    ...preview,
    quality_checked: false,
    quality_status: 'not_checked' as const,
  };
  mocks.scrapeUrl.mockResolvedValueOnce({
    ...uncertain,
    message: 'quality unavailable',
    preview: unchecked,
  });
  mocks.acceptScrapeCandidate.mockResolvedValueOnce({
    ...stored,
    preview: unchecked,
  });

  render(<PipelineClient />);
  submitUrl();

  const warning = 'Pipeline.urlQualityCheckUnavailable';
  expect(await screen.findByText(warning)).toBeDefined();
  expect(screen.getByText('Readable body.')).toBeDefined();
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByText('Pipeline.urlQualityCheckUnavailableSaved')).toBeDefined();
  await waitFor(() => expect(mocks.acceptScrapeCandidate).toHaveBeenCalledTimes(1));
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('keeps the unchecked-quality warning visible alongside an Accept conflict', async () => {
  const unchecked = { ...preview, quality_checked: false, quality_status: 'not_checked' as const };
  mocks.scrapeUrl.mockResolvedValueOnce({ ...uncertain, preview: unchecked });
  mocks.acceptScrapeCandidate.mockResolvedValueOnce({
    status: 'conflict',
    message: 'The raw article changed after this preview; no raw article was changed.',
    preview: unchecked,
  });

  render(<PipelineClient />);
  submitUrl();
  expect(await screen.findByText('Pipeline.urlQualityCheckUnavailable')).toBeDefined();
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByText('The raw article changed after this preview; no raw article was changed.')).toBeDefined();
  expect(screen.getByText('Pipeline.urlQualityCheckUnavailable')).toBeDefined();
  expect(screen.queryByRole('link', { name: /Pipeline.urlRawArticle:/ })).toBeNull();
  expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1);
  expect(mocks.acceptScrapeCandidate).toHaveBeenCalledTimes(1);
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('resolves an uncertain accept outcome from the same candidate without refetching', async () => {
  mocks.acceptScrapeCandidate.mockResolvedValueOnce({ status: 'uncertain', message: 'Save status is uncertain.', candidate_id: 'opaque-candidate-id', candidate_revision: 'opaque-candidate-revision' });
  mocks.resolveScrapeCandidate.mockResolvedValueOnce({
    ...stored, storage: { path: 'raw/Example-article.md', sha256: 'stored-digest', status: 'replaced' },
  });
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByRole('dialog')).toBeDefined();
  expect(screen.getByText('classification was uncertain')).toBeDefined();
  expect(screen.queryByText((_text, element) => element?.tagName === 'P' && Boolean(element.textContent?.includes('raw/Example-article.md')))).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByRole('button', { name: 'Pipeline.urlCheckSaveStatus' })).toBeDefined();
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlCheckSaveStatus' }));
  expect(await screen.findByText('saved')).toBeDefined();
  await waitFor(() => expect(mocks.resolveScrapeCandidate).toHaveBeenCalledWith('opaque-candidate-id', 'project-a'));
  await waitFor(() => expect(mocks.acceptScrapeCandidate).toHaveBeenCalledWith('opaque-candidate-id', 'opaque-candidate-revision', 'Example article', 'project-a'));
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
  resolveScrape(uncertain);

  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b'));
  expect(screen.queryByText('Example article')).toBeNull();
  expect(mocks.scrapeUrl).toHaveBeenCalledWith('https://example.com/article', 'project-a');
  await waitFor(() => expect(mocks.rejectScrapeCandidate).toHaveBeenCalledWith('opaque-candidate-id', 'opaque-candidate-revision', 'project-a'));
});

it('reports a definitive URL preview failure without claiming raw storage is uncertain', async () => {
  mocks.scrapeUrl.mockImplementation(async () => {
    throw new ApiError('response lost after commit', 0);
  });
  render(<PipelineClient />);
  submitUrl();

  expect((await screen.findByRole('alert')).textContent).toBe('Pipeline.urlPreviewUnavailable');
  expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1);
});

it('reports an uncertain accept outcome and offers status resolution without refetching', async () => {
  mocks.acceptScrapeCandidate.mockResolvedValueOnce({ status: 'uncertain', message: 'Save status is uncertain.', candidate_id: 'opaque-candidate-id', candidate_revision: 'opaque-candidate-revision' });
  render(<PipelineClient />);
  submitUrl();
  await screen.findByRole('button', { name: 'Pipeline.urlConfirmCandidate' });
  fireEvent.click(screen.getByRole('button', { name: 'Pipeline.urlConfirmCandidate' }));

  expect(await screen.findByRole('button', { name: 'Pipeline.urlCheckSaveStatus' })).toBeDefined();
  expect(mocks.acceptScrapeCandidate).toHaveBeenCalledTimes(1);
  expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1);
});

it('keeps a definitive precommit rejection on the unavailable guidance', async () => {
  mocks.scrapeUrl.mockRejectedValueOnce(new ApiError('invalid request', 422));
  render(<PipelineClient />);
  submitUrl();

  expect((await screen.findByRole('alert')).textContent).toBe('Pipeline.urlPreviewUnavailable');
  expect(mocks.scrapeUrl).toHaveBeenCalledTimes(1);
});

it('keeps candidate persistence failures stage-specific without fetch or anti-bot guidance', async () => {
  mocks.scrapeUrl.mockResolvedValueOnce({
    status: 'unavailable',
    failure_stage: 'candidate',
    message: 'The review candidate could not be stored; no raw article was changed.',
  });
  render(<PipelineClient />);
  submitUrl();

  expect(await screen.findByText('The review candidate could not be stored; no raw article was changed.')).toBeDefined();
  expect(within(screen.getByRole('dialog')).queryByText('Pipeline.urlManualCapture')).toBeNull();
});
