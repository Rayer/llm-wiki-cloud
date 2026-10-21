import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import type { PipelineStatus } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  triggerPipeline: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  getRecompileAllCapability: mocks.getRecompileAllCapability,
  triggerPipeline: mocks.triggerPipeline,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: mocks.currentProject, isDemoSession: false, refreshNavCounts: vi.fn() }),
}));

import { PipelineClient } from '@/components/PipelineClient';

const quota = {
  enforced: true,
  allowed: true,
  runs_today: 0,
  daily_limit: 3,
  new_raw_files: 1,
  min_new_raw: 1,
  already_running: false,
};

function intentStatus(projectId: string, state: string): PipelineStatus {
  return {
    project_id: projectId,
    quota,
    pipeline_intent: {
      id: `intent-${projectId}`,
      status: state,
      compile_mode: 'incremental',
      execution_id: `execution-${projectId}`,
      saved_profile_revision: 1,
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

let intervals: Map<number, () => void>;

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  window.localStorage.setItem('locale', 'en');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  intervals = new Map();
  let nextIntervalId = 0;
  const nativeSetInterval = globalThis.setInterval;
  const nativeClearInterval = globalThis.clearInterval;
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: TimerHandler, timeout?: number) => {
    if (timeout !== 5000) return nativeSetInterval(callback, timeout);
    const id = ++nextIntervalId;
    if (typeof callback === 'function') intervals.set(id, callback as () => void);
    return id as unknown as ReturnType<typeof setInterval>;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => {
    if (!intervals.delete(id)) nativeClearInterval(id);
  }) as typeof clearInterval);
  mocks.getRecompileAllCapability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  mocks.triggerPipeline.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'execution-a' });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.resetAllMocks();
});

async function tickPipelinePoll() {
  const callback = [...intervals.values()][0];
  expect(callback).toBeDefined();
  await act(async () => { callback?.(); });
}

it('shows an initial read failure, resumes the same intent and tracks its terminal state without posting again', async () => {
  let initialReads = 0;
  let executionReads = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string, intentId?: string) => {
    if (executionId || intentId) {
      executionReads += 1;
      return Promise.resolve(intentStatus(projectId, executionReads === 1 ? 'compiling' : 'succeeded'));
    }
    initialReads += 1;
    if (initialReads === 1) return Promise.reject(new Error('temporary status outage'));
    if (initialReads === 2) return Promise.resolve(intentStatus(projectId, 'preparing'));
    return Promise.resolve({ project_id: projectId, quota });
  });

  render(<PipelineClient />);
  const readFailure = await screen.findByTestId('pipeline-status-read-failed');
  expect(readFailure.textContent).toContain('Retrying automatically');
  expect(screen.getByRole('button', { name: 'Run Pipeline' }).hasAttribute('disabled')).toBe(true);
  await waitFor(() => expect(intervals.size).toBe(1));

  await tickPipelinePoll();
  await screen.findByText('Pipeline compiling…');
  expect(screen.queryByTestId('pipeline-status-read-failed')).toBeNull();
  expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'execution-project-a', 'intent-project-a');

  await tickPipelinePoll();
  await screen.findByText('Pipeline complete');
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
  expect(intervals.size).toBe(0);
});

it('ignores a late recovery read after switching Projects', async () => {
  const delayedRecovery = deferred<PipelineStatus>();
  let projectAReads = 0;
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string) => {
    if (executionId) return Promise.resolve(intentStatus(projectId, 'compiling'));
    if (projectId === 'project-a') {
      projectAReads += 1;
      if (projectAReads === 1) return Promise.reject(new Error('temporary status outage'));
      return delayedRecovery.promise;
    }
    return Promise.resolve({ project_id: projectId, quota });
  });

  const view = render(<PipelineClient />);
  await screen.findByTestId('pipeline-status-read-failed');
  await waitFor(() => expect(intervals.size).toBe(1));
  await tickPipelinePoll();
  await waitFor(() => expect(projectAReads).toBe(2));

  mocks.currentProject = { id: 'project-b', name: 'Project B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenLastCalledWith('project-b'));
  await act(async () => {
    delayedRecovery.resolve(intentStatus('project-a', 'preparing'));
    await delayedRecovery.promise;
  });

  expect(screen.queryByTestId('pipeline-status-read-failed')).toBeNull();
  expect(screen.queryByText('Preparing saved Profile guidance…')).toBeNull();
  expect(mocks.getPipelineStatus.mock.calls.filter(([projectId, executionId]) => projectId === 'project-a' && executionId)).toEqual([]);
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
});
