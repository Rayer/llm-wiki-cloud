import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  recompileAll: vi.fn(),
  triggerPipeline: vi.fn(),
  refreshNavCounts: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getPipelineStatus: mocks.getPipelineStatus,
    getRecompileAllCapability: mocks.getRecompileAllCapability,
    recompileAll: mocks.recompileAll,
    triggerPipeline: mocks.triggerPipeline,
  };
});

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    isDemoSession: false,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

import { PipelineClient } from '@/components/PipelineClient';

const fullQuota = {
  enforced: true,
  allowed: true,
  runs_today: 0,
  daily_limit: 2,
  new_raw_files: 0,
  min_new_raw: 1,
  already_running: false,
};

beforeEach(() => {
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  mocks.getPipelineStatus.mockResolvedValue({ project_id: 'project-a', quota: fullQuota });
  mocks.getRecompileAllCapability.mockResolvedValue({
    allowed: true,
    compile_mode: 'full',
    quota: fullQuota,
  });
  mocks.recompileAll.mockResolvedValue({
    status: 'accepted',
    project_id: 'project-a',
    execution_id: 'full-execution',
    compile_mode: 'full',
    quota: fullQuota,
  });
  mocks.triggerPipeline.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'incremental-execution' });
  render(<PipelineClient />);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('LWC-391 full recompile', () => {
  it('uses full quota without pending raw files and cancel sends no POST', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const mode = await screen.findByTestId('pipeline-compile-mode');
    fireEvent.change(mode, { target: { value: 'full' } });

    expect(await screen.findByTestId('pipeline-full-notice')).toBeDefined();
    const runButton = screen.getByRole('button', { name: '開始全面重編譯' });
    expect(runButton.hasAttribute('disabled')).toBe(false);
    fireEvent.click(runButton);

    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(mocks.recompileAll).not.toHaveBeenCalled();
    expect(mocks.triggerPipeline).not.toHaveBeenCalled();
  });

  it('tracks the accepted execution and displays only its confirmed evidence', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => {
      if (!executionId) return Promise.resolve({ project_id: projectId, quota: fullQuota });
      return Promise.resolve({
        project_id: projectId,
        quota: fullQuota,
        last_execution: {
          status: 'SUCCEEDED',
          compile_mode: 'full',
          publication_outcome: 'confirmed_published',
          consumed_guidance: {
            status: 'available',
            execution_id: executionId,
            content_generation: 'generation-a',
            kind: 'active',
            revision: 'a'.repeat(64),
          },
        },
      });
    });
    fireEvent.change(await screen.findByTestId('pipeline-compile-mode'), { target: { value: 'full' } });
    fireEvent.click(await screen.findByRole('button', { name: '開始全面重編譯' }));

    await waitFor(() => expect(mocks.recompileAll).toHaveBeenCalledWith('project-a'));
    await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'full-execution'));
    const evidence = await screen.findByTestId('pipeline-full-evidence');
    expect(evidence.textContent).toContain('已確認由本次執行發布');
    expect(evidence.textContent).toContain(`已啟用的 Profile 指引 ${'a'.repeat(64)}`);
    expect(mocks.triggerPipeline).not.toHaveBeenCalled();
  });
});
