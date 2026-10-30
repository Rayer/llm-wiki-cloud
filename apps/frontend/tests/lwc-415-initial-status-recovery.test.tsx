import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { PipelineStatus } from '@/lib/api';

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  getProjectPipelineAutomation: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  triggerPipeline: vi.fn(),
  updateProjectPipelineAutomation: vi.fn(),
  uploadRawFile: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: mocks.getPipelineStatus,
  getProjectPipelineAutomation: mocks.getProjectPipelineAutomation,
  getRecompileAllCapability: mocks.getRecompileAllCapability,
  triggerPipeline: mocks.triggerPipeline,
  updateProjectPipelineAutomation: mocks.updateProjectPipelineAutomation,
  uploadRawFile: mocks.uploadRawFile,
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

function intentStatus(projectId: string, state: string, intentId = `intent-${projectId}`): PipelineStatus {
  return {
    project_id: projectId,
    quota,
    pipeline_intent: {
      id: intentId,
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

let intervals: Map<number, { callback: () => void; timeout: number }>;

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
    if (typeof callback === 'function') intervals.set(id, { callback: callback as () => void, timeout: timeout ?? 0 });
    return id as unknown as ReturnType<typeof setInterval>;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => {
    if (!intervals.delete(id)) nativeClearInterval(id);
  }) as typeof clearInterval);
  mocks.getRecompileAllCapability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  mocks.getProjectPipelineAutomation.mockImplementation(async () => ({
    enabled: true, revision: 1, admin_allowed: true, effective_enabled: true,
    backlog_scan_status: 'idle', pipeline_status: 'idle', source_revision: 0, processed_revision: 0,
  }));
  mocks.triggerPipeline.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'execution-a' });
  mocks.updateProjectPipelineAutomation.mockResolvedValue({
    enabled: true, revision: 2, admin_allowed: true, effective_enabled: true,
    backlog_scan_status: 'pending', pipeline_status: 'pending', source_revision: 1, processed_revision: 0,
  });
  mocks.uploadRawFile.mockResolvedValue({
    filename: 'source.md', path: 'raw/source.md', bytes: 18, sha256: 'source-digest',
    status: 'created', processing_status: 'pending', source_revision: 1, commit_status: 'saved',
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.resetAllMocks();
});

async function tickPipelinePoll() {
  const callback = [...intervals.values()].at(-1)?.callback;
  expect(callback).toBeDefined();
  await act(async () => { callback?.(); });
}

async function tickAutomationPoll() {
  const callback = [...intervals.values()][0]?.callback;
  expect(callback).toBeDefined();
  await act(async () => { callback?.(); });
}

it('shows an initial read failure, resumes the same intent and tracks its terminal state without posting again', async () => {
  let initialReads = 0;
  let executionReads = 0;
  const delayedCompile = deferred<PipelineStatus>();
  mocks.getPipelineStatus.mockImplementation((projectId: string, executionId?: string, intentId?: string) => {
    if (executionId || intentId) {
      executionReads += 1;
      return executionReads === 1
        ? delayedCompile.promise
        : Promise.resolve(intentStatus(projectId, 'succeeded'));
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
  await screen.findByText('Preparing saved Profile guidance…');
  expect(screen.queryByTestId('pipeline-status-read-failed')).toBeNull();
  expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'execution-project-a', 'intent-project-a');
  await act(async () => {
    delayedCompile.resolve(intentStatus('project-a', 'compiling'));
    await delayedCompile.promise;
  });
  await screen.findByText('Pipeline compiling…');

  await tickPipelinePoll();
  await screen.findByText('Pipeline complete');
  expect(mocks.triggerPipeline).not.toHaveBeenCalled();
  expect(intervals.size).toBe(0);
});

it('refreshes automatic processing status during mounted Pipeline polling through publication', async () => {
	let automationStatus = 'idle';
	let executionPolls = 0;
	mocks.getPipelineStatus.mockImplementation(async (projectId: string, executionId?: string, intentId?: string) => {
		if (!executionId && !intentId) return intentStatus(projectId, 'preparing');
		executionPolls += 1;
		automationStatus = executionPolls === 1 ? 'pending' : executionPolls === 2 ? 'running' : 'published';
		return intentStatus(projectId, executionPolls < 3 ? 'compiling' : 'succeeded');
	});
	mocks.getProjectPipelineAutomation.mockImplementation(async () => ({
		enabled: true, revision: 1, admin_allowed: true, effective_enabled: true,
		backlog_scan_status: 'idle', pipeline_status: automationStatus, source_revision: 1,
		processed_revision: automationStatus === 'published' ? 1 : 0,
	}));

	render(<PipelineClient />);
	const settings = await screen.findByTestId('pipeline-automation-settings');
	await waitFor(() => expect(settings.textContent).toContain('Processing status: pending'));
	await tickPipelinePoll();
	await waitFor(() => expect(settings.textContent).toContain('Processing status: running'));
	await tickPipelinePoll();
	await waitFor(() => expect(settings.textContent).toContain('Processing status: published'));
	expect(mocks.getProjectPipelineAutomation).toHaveBeenCalledTimes(4);
	expect(intervals.size).toBe(0);
});

it('refreshes mounted automatic processing state through a terminal failure', async () => {
	let automationStatus = 'idle';
	let executionPolls = 0;
	mocks.getPipelineStatus.mockImplementation(async (projectId: string, executionId?: string, intentId?: string) => {
		if (!executionId && !intentId) return intentStatus(projectId, 'preparing');
		executionPolls += 1;
		automationStatus = executionPolls === 1 ? 'pending' : executionPolls === 2 ? 'running' : 'failed';
		return intentStatus(projectId, executionPolls < 3 ? 'compiling' : 'failed');
	});
	mocks.getProjectPipelineAutomation.mockImplementation(async () => ({
		enabled: true, revision: 1, admin_allowed: true, effective_enabled: true,
		backlog_scan_status: 'idle', pipeline_status: automationStatus, source_revision: 1,
		processed_revision: 0, ...(automationStatus === 'failed' ? { failure_stage: 'pipeline', failure_code: 'pipeline_failed' } : {}),
	}));

	render(<PipelineClient />);
	const settings = await screen.findByTestId('pipeline-automation-settings');
	await waitFor(() => expect(settings.textContent).toContain('Processing status: pending'));
	await tickPipelinePoll();
	await waitFor(() => expect(settings.textContent).toContain('Processing status: running'));
	await tickPipelinePoll();
	await waitFor(() => expect(settings.textContent).toContain('Processing status: failed'));
	await waitFor(() => expect(screen.getByTestId('pipeline-automation-failure').textContent).toContain('pipeline_failed'));
	expect(intervals.size).toBe(0);
});

it('discovers delayed upload admission and a chained intent while Pipeline stays mounted', async () => {
  let automationStatus = 'idle';
  let sourceRevision = 0;
  let processedRevision = 0;
  let statusReads = 0;
  const executionPolls = new Map<string, number>();
  mocks.getProjectPipelineAutomation.mockImplementation(async () => ({
    enabled: true, revision: 1, admin_allowed: true, effective_enabled: true,
    backlog_scan_status: 'idle', pipeline_status: automationStatus, source_revision: sourceRevision,
    processed_revision: processedRevision,
  }));
  mocks.getPipelineStatus.mockImplementation(async (projectId: string, executionId?: string, intentId?: string) => {
    if (intentId) {
      const count = (executionPolls.get(intentId) ?? 0) + 1;
      executionPolls.set(intentId, count);
      if (intentId === 'intent-1' && count === 1) {
        automationStatus = 'running';
        return intentStatus(projectId, 'compiling', intentId);
      }
      if (intentId === 'intent-1') {
        automationStatus = 'published';
        sourceRevision = 2;
        processedRevision = 1;
        return intentStatus(projectId, 'succeeded', intentId);
      }
      if (intentId === 'intent-2' && count === 1) {
        automationStatus = 'running';
        return intentStatus(projectId, 'compiling', intentId);
      }
      automationStatus = 'published';
      sourceRevision = 2;
      processedRevision = 2;
      return intentStatus(projectId, 'succeeded', intentId);
    }
    statusReads += 1;
    if (statusReads === 4) return intentStatus(projectId, 'preparing', 'intent-1');
    if (statusReads === 5) return intentStatus(projectId, 'preparing', 'intent-2');
    return { project_id: projectId, quota };
  });

  render(<PipelineClient />);
  await screen.findByTestId('pipeline-automation-settings');
  await waitFor(() => expect(statusReads).toBe(1));

  automationStatus = 'pending';
  sourceRevision = 1;
  const uploadInput = document.querySelector<HTMLInputElement>('#raw-file-upload');
  expect(uploadInput).not.toBeNull();
  const file = new File(['synthetic source'], 'source.md', { type: 'text/markdown' });
  await act(async () => {
    fireEvent.change(uploadInput as HTMLInputElement, { target: { files: [file] } });
  });
  await screen.findByText('Created');
  await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));
  expect(intervals.size).toBe(1);

  await tickAutomationPoll();
  expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(3);
  await tickAutomationPoll();
  await screen.findByText('Pipeline compiling…');
  expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'execution-project-a', 'intent-1');
  await tickPipelinePoll();
  await screen.findByText('Pipeline complete');

  await tickAutomationPoll();
  await screen.findByText('Pipeline compiling…');
  expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-a', 'execution-project-a', 'intent-2');
  await tickPipelinePoll();
  await screen.findByText('Pipeline complete');
  await tickAutomationPoll();
  await waitFor(() => expect(intervals.size).toBe(0));
  expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(10);
  expect(mocks.uploadRawFile).toHaveBeenCalledTimes(1);
});

it('follows backlog admission to failure after enabling Project automation', async () => {
  let automationStatus = 'idle';
  let backlogStatus = 'idle';
  let statusReads = 0;
  let executionPolls = 0;
  const settings = () => ({
    enabled: automationStatus !== 'idle', revision: automationStatus === 'idle' ? 1 : 2,
    admin_allowed: true, effective_enabled: automationStatus !== 'idle',
    backlog_scan_status: backlogStatus, pipeline_status: automationStatus,
    source_revision: 1, processed_revision: 0,
    ...(automationStatus === 'failed' ? { failure_stage: 'pipeline', failure_code: 'backlog_pipeline_failed' } : {}),
  });
  mocks.getProjectPipelineAutomation.mockImplementation(async () => settings());
  mocks.updateProjectPipelineAutomation.mockImplementation(async () => {
    automationStatus = 'pending';
    backlogStatus = 'pending';
    return settings();
  });
  mocks.getPipelineStatus.mockImplementation(async (projectId: string, executionId?: string, intentId?: string) => {
    if (intentId) {
      executionPolls += 1;
      if (executionPolls === 1) {
        automationStatus = 'running';
        backlogStatus = 'complete';
        return intentStatus(projectId, 'compiling', 'backlog-intent');
      }
      automationStatus = 'failed';
      return intentStatus(projectId, 'failed', 'backlog-intent');
    }
    statusReads += 1;
    if (statusReads === 3) return intentStatus(projectId, 'preparing', 'backlog-intent');
    if (statusReads > 3) return intentStatus(projectId, 'failed', 'backlog-intent');
    return { project_id: projectId, quota };
  });

  render(<PipelineClient />);
  const disabledToggle = await screen.findByRole('checkbox', { name: 'Auto-pipeline for this Project' });
  expect((disabledToggle as HTMLInputElement).checked).toBe(false);
  fireEvent.click(disabledToggle);
  await waitFor(() => expect(mocks.updateProjectPipelineAutomation).toHaveBeenCalledWith('project-a', true, 1));
  await tickAutomationPoll();
  await tickAutomationPoll();
  await screen.findByText('Pipeline compiling…');
  await tickPipelinePoll();
  await screen.findByText('Pipeline needs attention');
  await waitFor(() => expect(screen.getByTestId('pipeline-automation-failure').textContent).toContain('backlog_pipeline_failed'));
  await tickAutomationPoll();
  await waitFor(() => expect(intervals.size).toBe(0));
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
