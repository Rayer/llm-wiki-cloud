import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { PipelineResult, PipelineStatus } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  recompileAll: vi.fn(),
  triggerPipeline: vi.fn(),
  uploadRawFile: vi.fn(),
  refreshNavCounts: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  getRecompileAllCapability: mocks.getRecompileAllCapability,
  recompileAll: mocks.recompileAll,
  triggerPipeline: mocks.triggerPipeline,
  uploadRawFile: mocks.uploadRawFile,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    isDemoSession: false,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

import { PipelineClient } from '@/components/PipelineClient';

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
  let reject!: (error?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function status(projectId: string, executionStatus?: 'RUNNING' | 'SUCCEEDED'): PipelineStatus {
  return {
    project_id: projectId,
    quota,
    ...(executionStatus ? {
      last_execution: {
        status: executionStatus,
        compile_mode: 'full',
        duration: '1m',
        publication_outcome: 'unknown',
        consumed_guidance: { status: 'unavailable', revision: null },
      },
    } : {}),
  };
}

function statusWithGuidance(projectId: string, revision: string): PipelineStatus {
  return {
    project_id: projectId,
    quota,
    last_execution: {
      name: `${projectId}-execution`,
      status: 'SUCCEEDED',
      compile_mode: 'full',
      publication_outcome: 'confirmed_published',
      consumed_guidance: {
        status: 'available',
        kind: 'active',
        revision,
        execution_id: `${projectId}-execution`,
      },
    },
  };
}

async function renderPipeline() {
  const view = render(<PipelineClient />);
  await screen.findByTestId('pipeline-compile-mode');
  return view;
}

async function startFullRecompile(projectId = 'project-a') {
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  window.localStorage.setItem('llm-wiki-last-project', projectId);
  fireEvent.change(await screen.findByTestId('pipeline-compile-mode'), { target: { value: 'full' } });
  const button = await screen.findByRole('button', { name: '開始全面重編譯' });
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false));
  fireEvent.click(button);
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  mocks.getPipelineStatus.mockImplementation((projectId: string) => Promise.resolve(status(projectId)));
  mocks.getRecompileAllCapability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  mocks.recompileAll.mockResolvedValue({
    status: 'accepted', project_id: 'project-a', execution_id: 'execution-a', compile_mode: 'full', quota,
  });
  mocks.triggerPipeline.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'incremental' });
  mocks.uploadRawFile.mockResolvedValue({ status: 'created' });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

it('ignores a delayed full POST after switching Project, before it starts polling', async () => {
  const delayedPost = deferred<PipelineResult>();
  mocks.recompileAll.mockReturnValue(delayedPost.promise);
  const view = await renderPipeline();
  await startFullRecompile();
  await waitFor(() => expect(mocks.recompileAll).toHaveBeenCalledWith('project-a'));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b'));

  await act(async () => {
    delayedPost.resolve({
      status: 'accepted', project_id: 'project-a', execution_id: 'execution-a', compile_mode: 'full', quota,
    });
    await delayedPost.promise;
  });
  expect(mocks.getPipelineStatus.mock.calls.filter(([id, executionId]) => id === 'project-a' && executionId === 'execution-a')).toEqual([]);
  expect(screen.queryByText('Pipeline status failed')).toBeNull();
});

it('ignores a late Project A poll without stopping Project B polling', async () => {
  const delayedPoll = deferred<PipelineStatus>();
  const pollCallbacks: Array<() => void> = [];
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((handler: TimerHandler, timeout?: number) => {
    if (typeof handler === 'function' && timeout === 5000) pollCallbacks.push(handler as () => void);
    return pollCallbacks.length;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(() => undefined);
  mocks.recompileAll
    .mockResolvedValueOnce({ status: 'accepted', project_id: 'project-a', execution_id: 'execution-a', compile_mode: 'full', quota })
    .mockResolvedValueOnce({ status: 'accepted', project_id: 'project-b', execution_id: 'execution-b', compile_mode: 'full', quota });
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => {
    if (projectId === 'project-a' && executionId === 'execution-a') return delayedPoll.promise;
    if (projectId === 'project-b' && executionId === 'execution-b') return Promise.resolve(status(projectId, 'RUNNING'));
    return Promise.resolve(status(projectId));
  });
  const view = await renderPipeline();
  await startFullRecompile();
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'execution-a'));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b'));
  await startFullRecompile('project-b');
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b', 'execution-b'));
  await screen.findByText('Pipeline running...');

  await act(async () => {
    delayedPoll.resolve(status('project-a', 'SUCCEEDED'));
    await delayedPoll.promise;
    await Promise.resolve();
  });
  expect(screen.getByText('Pipeline running...')).toBeDefined();
  expect(pollCallbacks).toHaveLength(2);
  await act(async () => {
    pollCallbacks[1]?.();
    await Promise.resolve();
  });
  await waitFor(() => expect(mocks.getPipelineStatus.mock.calls.filter(([id, executionId]) => id === 'project-b' && executionId === 'execution-b')).toHaveLength(2));
  expect(mocks.getPipelineStatus.mock.calls.filter(([id, executionId]) => id === 'project-b' && executionId === 'execution-b')).toHaveLength(2);
});

it('uses a context generation so an old A response cannot apply after A to B to A', async () => {
  const delayedPost = deferred<PipelineResult>();
  mocks.recompileAll.mockReturnValue(delayedPost.promise);
  const view = await renderPipeline();
  await startFullRecompile();

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b'));
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getPipelineStatus.mock.calls.filter(([id]) => id === 'project-a').length).toBeGreaterThan(1));

  await act(async () => {
    delayedPost.resolve({
      status: 'accepted', project_id: 'project-a', execution_id: 'old-execution-a', compile_mode: 'full', quota,
    });
    await delayedPost.promise;
  });
  expect(mocks.getPipelineStatus.mock.calls.filter(([id, executionId]) => id === 'project-a' && executionId === 'old-execution-a')).toEqual([]);
});

it('does not let a late A capability refresh replace B capability after A completes', async () => {
  const delayedCapability = deferred<{ allowed: boolean; compile_mode: string; quota: typeof quota }>();
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId === 'execution-a' ? status(projectId, 'SUCCEEDED') : status(projectId),
  ));
  mocks.getRecompileAllCapability.mockImplementation((projectId: string) => {
    if (projectId === 'project-a') {
      return mocks.getRecompileAllCapability.mock.calls.filter(([id]) => id === 'project-a').length > 1
        ? delayedCapability.promise
        : Promise.resolve({ allowed: true, compile_mode: 'full', quota });
    }
    return Promise.resolve({ allowed: false, compile_mode: 'full', denial_code: 'not_available', quota: { ...quota, allowed: false } });
  });
  const view = await renderPipeline();
  await startFullRecompile();
  await waitFor(() => expect(mocks.getRecompileAllCapability.mock.calls.filter(([id]) => id === 'project-a')).toHaveLength(2));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getRecompileAllCapability).toHaveBeenCalledWith('project-b'));
  const fullButton = await screen.findByRole('button', { name: '開始全面重編譯' });
  await waitFor(() => expect(fullButton.hasAttribute('disabled')).toBe(true));

  await act(async () => {
    delayedCapability.resolve({ allowed: true, compile_mode: 'full', quota });
    await delayedCapability.promise;
  });
  expect(fullButton.hasAttribute('disabled')).toBe(true);
});

it('does not show an old Project failure after switching context', async () => {
  const delayedPost = deferred<PipelineResult>();
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId ? status(projectId, 'RUNNING') : status(projectId),
  ));
  mocks.recompileAll.mockReturnValueOnce(delayedPost.promise).mockResolvedValueOnce({
    status: 'accepted', project_id: 'project-b', execution_id: 'execution-b', compile_mode: 'full', quota,
  });
  const view = await renderPipeline();
  await startFullRecompile();
  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getRecompileAllCapability).toHaveBeenCalledWith('project-b'));
  await startFullRecompile('project-b');
  await waitFor(() => expect(mocks.recompileAll).toHaveBeenCalledTimes(2));

  await act(async () => {
    delayedPost.reject(new Error('late Project A failure'));
    await Promise.resolve();
  });
  expect(screen.queryByText('late Project A failure')).toBeNull();
  expect(screen.getByText('Pipeline running...')).toBeDefined();
});

it('ignores a delayed upload status refresh after switching Projects', async () => {
  const delayedRefresh = deferred<PipelineStatus>();
  let projectACalls = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string) => {
    if (projectId === 'project-a' && ++projectACalls === 2) return delayedRefresh.promise;
    if (projectId === 'project-b') return Promise.resolve(statusWithGuidance(projectId, 'B-CURRENT-REVISION'));
    return Promise.resolve(status(projectId));
  });
  const view = await renderPipeline();
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(1));

  fireEvent.change(view.container.querySelector('input[type=file]')!, {
    target: { files: [new File(['raw'], 'fixture.md', { type: 'text/plain' })] },
  });
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));
  expect(mocks.getPipelineStatus).toHaveBeenLastCalledWith('project-a');

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  expect(await screen.findByText(/B-CURRENT-REVISION/)).toBeDefined();

  await act(async () => {
    delayedRefresh.resolve(statusWithGuidance('project-a', 'A-UPLOAD-ONLY-REVISION'));
    await delayedRefresh.promise;
  });
  expect(screen.getByText(/B-CURRENT-REVISION/)).toBeDefined();
  expect(screen.queryByText(/A-UPLOAD-ONLY-REVISION/)).toBeNull();
});

it('ignores an upload refresh response for a different Project', async () => {
  const delayedRefresh = deferred<PipelineStatus>();
  let projectACalls = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string) => {
    if (projectId === 'project-a' && ++projectACalls === 2) return delayedRefresh.promise;
    return Promise.resolve(status(projectId));
  });
  const view = await renderPipeline();
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(1));

  fireEvent.change(view.container.querySelector('input[type=file]')!, {
    target: { files: [new File(['raw'], 'fixture.md', { type: 'text/plain' })] },
  });
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));
  expect(mocks.getPipelineStatus).toHaveBeenLastCalledWith('project-a');

  await act(async () => {
    delayedRefresh.resolve(statusWithGuidance('project-b', 'MISMATCHED-RESPONSE-REVISION'));
    await delayedRefresh.promise;
  });
  expect(screen.queryByText(/MISMATCHED-RESPONSE-REVISION/)).toBeNull();
});

it('rejects an old upload refresh after Project A to B to A', async () => {
  const delayedRefresh = deferred<PipelineStatus>();
  let projectACalls = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string) => {
    if (projectId === 'project-a') {
      projectACalls += 1;
      if (projectACalls === 2) return delayedRefresh.promise;
      if (projectACalls >= 3) return Promise.resolve(statusWithGuidance(projectId, 'A-CURRENT-REVISION'));
    }
    if (projectId === 'project-b') return Promise.resolve(statusWithGuidance(projectId, 'B-CURRENT-REVISION'));
    return Promise.resolve(status(projectId));
  });
  const view = await renderPipeline();
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(1));

  fireEvent.change(view.container.querySelector('input[type=file]')!, {
    target: { files: [new File(['raw'], 'fixture.md', { type: 'text/plain' })] },
  });
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await screen.findByText(/B-CURRENT-REVISION/);
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  view.rerender(<PipelineClient />);
  expect(await screen.findByText(/A-CURRENT-REVISION/)).toBeDefined();

  await act(async () => {
    delayedRefresh.resolve(statusWithGuidance('project-a', 'A-OLD-UPLOAD-REVISION'));
    await delayedRefresh.promise;
  });
  expect(screen.getByText(/A-CURRENT-REVISION/)).toBeDefined();
  expect(screen.queryByText(/A-OLD-UPLOAD-REVISION/)).toBeNull();
});

it('does not let a delayed upload refresh replace a newer same-Project execution', async () => {
  const delayedRefresh = deferred<PipelineStatus>();
  let projectACalls = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => {
    if (executionId) return Promise.resolve(status(projectId, 'RUNNING'));
    if (projectId === 'project-a' && ++projectACalls === 2) return delayedRefresh.promise;
    return Promise.resolve(status(projectId));
  });
  const view = await renderPipeline();
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(1));

  fireEvent.change(view.container.querySelector('input[type=file]')!, {
    target: { files: [new File(['raw'], 'fixture.md', { type: 'text/plain' })] },
  });
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));

  await startFullRecompile();
  await screen.findByText('Pipeline running...');
  await act(async () => {
    delayedRefresh.resolve(statusWithGuidance('project-a', 'A-OLD-EXECUTION-REVISION'));
    await delayedRefresh.promise;
  });
  expect(screen.getByText('Pipeline running...')).toBeDefined();
  expect(screen.queryByText(/A-OLD-EXECUTION-REVISION/)).toBeNull();
});
