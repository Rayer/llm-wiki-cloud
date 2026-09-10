import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'demo' },
  isDemoSession: false,
  getPipelineStatus: vi.fn(),
  triggerPipeline: vi.fn(),
  refreshNavCounts: vi.fn(),
}));
vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  triggerPipeline: mocks.triggerPipeline,
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => mocks }));

import { PipelineClient } from '@/components/PipelineClient';

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('locale', 'zh-TW');
  localStorage.setItem('llm-wiki-last-project', 'demo');
  mocks.isDemoSession = false;
  mocks.getPipelineStatus.mockResolvedValue({ quota: { enforced: false, allowed: true } });
  mocks.triggerPipeline.mockResolvedValue({ status: 'accepted' });
});
afterEach(() => { cleanup(); vi.clearAllMocks(); });

it('keeps the manual pipeline workflow visible in Demo without allowing execution', async () => {
  mocks.isDemoSession = true;
  render(<PipelineClient />);
  const run = screen.getByRole('button', { name: '執行 Pipeline' });
  expect((run as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByTestId('pipeline-block-reason').textContent).toBe('Demo 模式不提供此功能');
  expect(screen.getByText(/想建立自己的知識庫/)).toBeDefined();
  expect(screen.queryByRole('heading', { name: '上傳檔案' })).toBeNull();
  expect(screen.queryByRole('textbox', { name: '擷取 URL' })).toBeNull();
  fireEvent.click(run);
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledOnce());
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});

it('lets a regular account manually trigger the pipeline and blocks another run once accepted', async () => {
  render(<PipelineClient />);
  const run = screen.getByRole('button', { name: '執行 Pipeline' });
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledOnce());
  expect(screen.getByRole('heading', { name: '新增內容' })).toBeDefined();
  expect((run as HTMLButtonElement).disabled).toBe(false);
  fireEvent.click(run);
  await waitFor(() => expect(mocks.triggerPipeline).toHaveBeenCalledOnce());
  await waitFor(() => expect((run as HTMLButtonElement).disabled).toBe(true));
});
