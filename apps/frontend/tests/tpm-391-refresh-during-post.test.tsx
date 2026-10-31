import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { PipelineStatus } from '@/lib/api';

const m = vi.hoisted(() => ({
  project: { id: 'project-a', name: 'A' },
  status: vi.fn(),
  capability: vi.fn(),
  upload: vi.fn(),
  run: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: m.status,
  getRecompileAllCapability: m.capability,
  uploadRawFile: m.upload,
  recompileAll: m.run,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: m.project, isDemoSession: false, refreshNavCounts: vi.fn() }),
}));

import { PipelineClient } from '@/components/PipelineClient';
import { NavigationBlockerProvider } from '@/components/NavigationBlocker';

function pipelineTree() {
  return <NavigationBlockerProvider><PipelineClient /></NavigationBlockerProvider>;
}

const quota = {
  enforced: true,
  allowed: true,
  runs_today: 0,
  daily_limit: 2,
  new_raw_files: 0,
  min_new_raw: 1,
  already_running: false,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function executionStatus(
  projectId: string,
  name: string,
  status: 'RUNNING' | 'SUCCEEDED',
  quotaValue = quota,
): PipelineStatus {
  return {
    project_id: projectId,
    quota: quotaValue,
    last_execution: {
      name,
      status,
      compile_mode: 'full',
      publication_outcome: status === 'RUNNING' ? 'unknown' : 'confirmed_published',
      consumed_guidance: status === 'RUNNING'
        ? { status: 'unavailable', revision: null }
        : { status: 'available', kind: 'active', revision: 'OLD-REFRESH-DURING-POST', execution_id: name },
    },
  };
}

function fireGenericRefresh(kind: 'upload' | 'annotation', container: HTMLElement) {
  if (kind === 'upload') {
    fireEvent.change(container.querySelector('input[type=file]')!, {
      target: { files: [new File(['raw'], 'during-post.md')] },
    });
  } else {
    act(() => window.dispatchEvent(new Event('source-annotation-saved')));
  }
}

async function startFullRecompile() {
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  fireEvent.change(screen.getByTestId('pipeline-compile-mode'), { target: { value: 'full' } });
  const button = screen.getByRole('button', { name: '開始全面重編譯' });
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false));
  fireEvent.click(button);
}

beforeEach(() => {
  m.project = { id: 'project-a', name: 'A' };
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  m.status.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId ? executionStatus(projectId, executionId, 'RUNNING') : { project_id: projectId, quota },
  ));
  m.capability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  m.upload.mockResolvedValue({ status: 'created' });
  m.run.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'NEW-EXECUTION', compile_mode: 'full', quota });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

it.each(['upload', 'annotation'] as const)(
  'generic %s refresh started during pending full POST cannot replace the accepted execution',
  async (kind) => {
    const delayedPost = deferred<unknown>();
    const delayedRefresh = deferred<PipelineStatus>();
    let genericCalls = 0;
    m.run.mockReturnValue(delayedPost.promise);
    m.status.mockImplementation((projectId: string, executionId?: string) => {
      if (executionId) return Promise.resolve(executionStatus(projectId, executionId, 'RUNNING'));
      genericCalls += 1;
      return genericCalls === 2
        ? delayedRefresh.promise
        : Promise.resolve({ project_id: projectId, quota });
    });

    const view = render(pipelineTree());
    await waitFor(() => expect(m.capability).toHaveBeenCalledWith('project-a'));
    await startFullRecompile();
    await waitFor(() => expect(m.run).toHaveBeenCalled());
    fireGenericRefresh(kind, view.container);
    await waitFor(() => expect(genericCalls).toBe(2));

    await act(async () => {
      delayedPost.resolve({ status: 'accepted', project_id: 'project-a', execution_id: 'NEW-EXECUTION', compile_mode: 'full', quota });
      await delayedPost.promise;
    });
    await screen.findByText('Pipeline running...');
    fireEvent.change(screen.getByTestId('pipeline-compile-mode'), { target: { value: 'incremental' } });
    await act(async () => {
      delayedRefresh.resolve(executionStatus('project-a', 'OLD-EXECUTION', 'SUCCEEDED', { ...quota, new_raw_files: 1 }));
      await delayedRefresh.promise;
    });

    expect(screen.queryByText(/OLD-REFRESH-DURING-POST/)).toBeNull();
    expect(screen.getByText('Pipeline running...')).toBeDefined();
    expect(screen.getByTestId('pipeline-quota-line').textContent).toContain('新檔案：1');
  },
);

it.each(['upload', 'annotation'] as const)(
  'generic %s refresh started while execution tracking is active preserves execution evidence',
  async (kind) => {
    const delayedRefresh = deferred<PipelineStatus>();
    let genericCalls = 0;
    m.status.mockImplementation((projectId: string, executionId?: string) => {
      if (executionId) return Promise.resolve(executionStatus(projectId, executionId, 'RUNNING'));
      genericCalls += 1;
      return genericCalls === 2
        ? delayedRefresh.promise
        : Promise.resolve({ project_id: projectId, quota });
    });

    const view = render(pipelineTree());
    await waitFor(() => expect(m.capability).toHaveBeenCalledWith('project-a'));
    await startFullRecompile();
    await screen.findByText('Pipeline running...');
    fireGenericRefresh(kind, view.container);
    await waitFor(() => expect(genericCalls).toBe(2));
    fireEvent.change(screen.getByTestId('pipeline-compile-mode'), { target: { value: 'incremental' } });

    await act(async () => {
      delayedRefresh.resolve(executionStatus('project-a', 'OLD-EXECUTION', 'SUCCEEDED', { ...quota, new_raw_files: 1 }));
      await delayedRefresh.promise;
    });

    expect(screen.queryByText(/OLD-REFRESH-DURING-POST/)).toBeNull();
    expect(screen.getByText('Pipeline running...')).toBeDefined();
    expect(screen.getByTestId('pipeline-quota-line').textContent).toContain('新檔案：1');
  },
);
