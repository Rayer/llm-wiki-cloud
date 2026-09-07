import React, { act, useLayoutEffect } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getBuildInfo: vi.fn(),
  getPipelineLog: vi.fn(),
  getStatus: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getBuildInfo: mocks.getBuildInfo,
    getPipelineLog: mocks.getPipelineLog,
    getStatus: mocks.getStatus,
  };
});

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ user: { id: 'user-a' } }),
}));

vi.mock('@/lib/i18n', () => ({
  useT: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: mocks.currentProject }),
}));

import { normalizeStatus } from '@/lib/api';
import { StatusClient } from '@/components/StatusClient';

type Execution = {
  name: string;
  status: string;
  log_state?: string;
  log_state_reason?: string;
  log_url?: string;
  diagnostic?: Record<string, unknown> | null;
};

function execution(overrides: Partial<Execution> = {}): Execution {
  return {
    name: 'execution-a',
    status: 'FAILED',
    log_state: 'available',
    log_url: 'https://logs.example/project-a',
    diagnostic: {
      stage: 'concept_reconciliation',
      error_class: 'A-error-class',
      detail_code: 'A-detail-code',
      child_command: 'A-child-command',
      exit_code: 17,
    },
    ...overrides,
  };
}

function status(overrides: Partial<{ sourcesCount: number; conceptsCount: number; rawCount: number; lastExecution: Execution | null }> = {}) {
  return {
    sourcesCount: 11,
    conceptsCount: 22,
    rawCount: 33,
    suggestedQueries: [],
    lastExecution: execution(),
    raw: {},
    ...overrides,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function buildInfo() {
  return {
    product_version: 'test',
    commit: 'test',
    branch: 'test',
    tag: '',
    image_tag: 'test',
    service: 'test',
    revision: 'test',
  };
}

function RenderSnapshot({ onLayout }: { onLayout: () => void }) {
  useLayoutEffect(onLayout);
  return null;
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.getBuildInfo.mockResolvedValue(buildInfo());
  mocks.getStatus.mockResolvedValue(status());
  mocks.getPipelineLog.mockResolvedValue('project-a log');
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.resetAllMocks();
});

describe('StatusClient polling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  it('refreshes RUNNING/pending to SUCCEEDED with terminal counts and lazy log availability without reload', async () => {
    mocks.getStatus
      .mockResolvedValueOnce(status({
        lastExecution: execution({ name: 'olw-pipeline-dev-fj256', status: 'RUNNING', log_state: 'pending' }),
      }))
      .mockResolvedValue(status({
        sourcesCount: 1,
        conceptsCount: 4,
        rawCount: 1,
        lastExecution: execution({ name: 'olw-pipeline-dev-fj256', status: 'SUCCEEDED' }),
      }));

    await act(async () => { render(<StatusClient />); });
    expect(screen.getByText('RUNNING')).not.toBeNull();
    expect(screen.getByText('Pipeline log is still pending.')).not.toBeNull();
    expect(screen.queryByRole('button', { name: 'Open pipeline log' })).toBeNull();
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();

    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });

    expect(mocks.getStatus).toHaveBeenCalledTimes(2);
    expect(screen.getByText('SUCCEEDED')).not.toBeNull();
    for (const [label, count] of [['Sources', '1'], ['Concepts', '4'], ['Raw', '1']]) {
      expect(screen.getByText(label).nextElementSibling?.textContent).toBe(count);
    }
    expect(screen.queryByText('Pipeline log is still pending.')).toBeNull();
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' })); });
    expect(screen.getByText('project-a log')).not.toBeNull();
    expect(mocks.getPipelineLog).toHaveBeenCalledExactlyOnceWith('https://logs.example/project-a', 'project-a');

    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(2);
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('waits five seconds after each response and never overlaps slow status requests', async () => {
    const initial = deferred<ReturnType<typeof status>>();
    const poll = deferred<ReturnType<typeof status>>();
    mocks.getStatus.mockReturnValueOnce(initial.promise).mockReturnValueOnce(poll.promise);
    await act(async () => { render(<StatusClient />); });
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(1);

    await act(async () => { initial.resolve(status({ lastExecution: execution({ status: 'RUNNING' }) })); });
    await act(async () => { await vi.advanceTimersByTimeAsync(4999); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Loading status...')).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(2);

    await act(async () => { poll.resolve(status({ lastExecution: execution({ status: 'RUNNING' }) })); });
    await act(async () => { await vi.advanceTimersByTimeAsync(4999); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(3);
    expect(mocks.getStatus.mock.calls).toEqual([['project-a'], ['project-a'], ['project-a']]);
  });

  it('recovers initial and active refresh errors, retaining counts and clearing the error on success', async () => {
    mocks.getStatus
      .mockRejectedValueOnce(new Error('Initial status failed (503)'))
      .mockResolvedValueOnce(status({ lastExecution: execution({ status: 'RUNNING', log_state: 'pending' }) }))
      .mockRejectedValueOnce(new Error('Status refresh failed (503)'))
      .mockResolvedValueOnce(status({ sourcesCount: 44 }));
    await act(async () => { render(<StatusClient />); });
    expect(screen.getByText('Initial status failed (503)')).not.toBeNull();
    expect(screen.queryByText('Loading status...')).toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.queryByText('Initial status failed (503)')).toBeNull();
    expect(screen.getByText('RUNNING')).not.toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByText('Status refresh failed (503)')).not.toBeNull();
    expect(screen.getByText('11')).not.toBeNull();
    expect(screen.getByText('RUNNING')).not.toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.queryByText('Status refresh failed (503)')).toBeNull();
    expect(screen.getByText('44')).not.toBeNull();
    expect(screen.getByText('FAILED')).not.toBeNull();
    expect(vi.getTimerCount()).toBe(0);
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();
  });

  it.each(['SUCCEEDED', 'FAILED', 'CANCELLED', 'UNKNOWN', null])('does not poll an initial terminal or idle response: %s', async (executionStatus) => {
    mocks.getStatus.mockResolvedValue(status({
      lastExecution: executionStatus ? execution({ status: executionStatus, log_state: 'pending' }) : null,
    }));
    await act(async () => { render(<StatusClient />); });
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();
  });

  it('cancels a queued poll when switching projects', async () => {
    mocks.getStatus.mockResolvedValueOnce(status({ lastExecution: execution({ status: 'RUNNING' }) }));
    const view = render(<StatusClient />);
    await act(async () => {});
    expect(vi.getTimerCount()).toBe(1);
    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    await act(async () => { view.rerender(<StatusClient />); });
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus.mock.calls).toEqual([['project-a'], ['project-b']]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(['resolve', 'reject'] as const)('ignores a late polled response after A to B to A: %s', async (outcome) => {
    const stale = deferred<ReturnType<typeof status>>();
    mocks.getStatus
      .mockResolvedValueOnce(status({ lastExecution: execution({ status: 'RUNNING' }) }))
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce(status({ sourcesCount: 222 }))
      .mockResolvedValueOnce(status({ sourcesCount: 333 }));
    const view = render(<StatusClient />);
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    await act(async () => { view.rerender(<StatusClient />); });
    mocks.currentProject = { id: 'project-a', name: 'Project A' };
    await act(async () => { view.rerender(<StatusClient />); });
    await act(async () => {
      if (outcome === 'resolve') stale.resolve(status({ sourcesCount: 999, lastExecution: execution({ status: 'RUNNING' }) }));
      else stale.reject(new Error('STALE-STATUS-ERROR'));
    });
    expect(screen.getByText('333')).not.toBeNull();
    expect(screen.queryByText('999')).toBeNull();
    expect(screen.queryByText('STALE-STATUS-ERROR')).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus.mock.calls).toEqual([['project-a'], ['project-a'], ['project-b'], ['project-a']]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(['queued', 'resolve', 'reject'] as const)('stops polling on unmount with a %s refresh', async (outcome) => {
    const pending = deferred<ReturnType<typeof status>>();
    mocks.getStatus
      .mockResolvedValueOnce(status({ lastExecution: execution({ status: 'RUNNING' }) }))
      .mockReturnValueOnce(pending.promise);
    const view = render(<StatusClient />);
    await act(async () => {});
    if (outcome !== 'queued') {
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    }
    view.unmount();
    await act(async () => {
      if (outcome === 'resolve') pending.resolve(status({ lastExecution: execution({ status: 'RUNNING' }) }));
      if (outcome === 'reject') pending.reject(new Error('UNMOUNTED-STATUS-ERROR'));
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(mocks.getStatus).toHaveBeenCalledTimes(outcome === 'queued' ? 1 : 2);
    expect(vi.getTimerCount()).toBe(0);
    expect(view.container.textContent).toBe('');
  });

  it.each(['running', 'error'])('exposes an honest pause and explicit bounded retry after exhausting the %s budget', async (outcome) => {
    const running = status({ lastExecution: execution({ status: 'RUNNING' }) });
    if (outcome === 'running') mocks.getStatus.mockResolvedValue(running);
    else mocks.getStatus.mockRejectedValue(new Error('Status unavailable (503)'));
    await act(async () => { render(<StatusClient />); });
    if (outcome === 'running') {
      await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' })); });
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(120 * 5000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(121);
    expect(vi.getTimerCount()).toBe(0);
    expect(screen.getByText('Automatic status refresh paused after repeated checks. The displayed status may be out of date.')).not.toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(121);

    const retry = deferred<ReturnType<typeof status>>();
    mocks.getStatus.mockReturnValueOnce(retry.promise).mockResolvedValue(status());
    const button = screen.getByRole('button', { name: 'Retry status refresh' });
    act(() => { fireEvent.click(button); fireEvent.click(button); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(122);
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.getStatus).toHaveBeenCalledTimes(122);
    await act(async () => { retry.resolve(running); });
    expect(screen.queryByText('Status unavailable (503)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Retry status refresh' })).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByText('FAILED')).not.toBeNull();
    expect(mocks.getStatus).toHaveBeenCalledTimes(123);
    expect(vi.getTimerCount()).toBe(0);
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(outcome === 'running' ? 1 : 0);
    if (outcome === 'running') expect(screen.getByText('project-a log')).not.toBeNull();
  });

  it('invalidates an in-flight log when polling discovers a different execution', async () => {
    const oldLog = deferred<string>();
    mocks.getPipelineLog.mockReturnValueOnce(oldLog.promise).mockResolvedValueOnce('NEW-EXECUTION-LOG');
    mocks.getStatus
      .mockResolvedValueOnce(status({ lastExecution: execution({ status: 'RUNNING' }) }))
      .mockResolvedValueOnce(status({ lastExecution: execution({ name: 'new-execution', log_url: 'https://logs.example/new' }) }));
    await act(async () => { render(<StatusClient />); });
    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' })); });
    await act(async () => { oldLog.resolve('STALE-EXECUTION-LOG'); });
    expect(screen.queryByText('STALE-EXECUTION-LOG')).toBeNull();
    expect(screen.getByText('NEW-EXECUTION-LOG')).not.toBeNull();
    expect(mocks.getPipelineLog.mock.calls).toEqual([
      ['https://logs.example/project-a', 'project-a'],
      ['https://logs.example/new', 'project-a'],
    ]);
  });
});

describe('StatusClient behavior', () => {
  it('fetches no log initially, then makes exactly one explicit owner-scoped request and renders it', async () => {
    const log = deferred<string>();
    mocks.getPipelineLog.mockReturnValue(log.promise);

    render(<StatusClient />);
    await screen.findByText('11');

    expect(mocks.getStatus).toHaveBeenCalledWith('project-a');
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();

    const open = screen.getByRole('button', { name: 'Open pipeline log' });
    act(() => {
      fireEvent.click(open);
      fireEvent.click(open);
    });

    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);
    expect(mocks.getPipelineLog).toHaveBeenCalledWith(
      'https://logs.example/project-a',
      'project-a',
    );

    log.resolve('PROJECT-A-LOG');
    expect(await screen.findByText('PROJECT-A-LOG')).not.toBeNull();
  });

  it.each([
    ['pending', undefined, 'Pipeline log is still pending.'],
    ['unavailable', 'unsupported_execution_status', 'Pipeline log is unavailable for this execution status.'],
    ['unavailable', 'storage_unavailable', 'Pipeline log storage is unavailable.'],
    ['unavailable', 'log_unavailable', 'Pipeline log is unavailable.'],
    ['unavailable', 'log_too_large', 'Pipeline log is too large to display.'],
    ['missing', undefined, 'No pipeline log is available.'],
    ['unknown_future', 'attacker-controlled text', 'Pipeline log is unavailable.'],
  ] as const)('renders the finite %s log state without fetching before open', async (logState, reason, message) => {
    mocks.getStatus.mockResolvedValue(status({
      lastExecution: execution({ log_state: logState, log_state_reason: reason, log_url: 'https://logs.example/stale' }),
    }));

    render(<StatusClient />);
    expect(await screen.findByText(message)).not.toBeNull();
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();
    expect(screen.queryByText('attacker-controlled text')).toBeNull();
  });

  it('renders an empty loaded log as a finite terminal state with no active open control', async () => {
    const log = deferred<string>();
    mocks.getPipelineLog.mockReturnValue(log.promise);
    render(<StatusClient />);
    await screen.findByText('11');

    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));
    expect(screen.getByRole('button', { name: 'Loading pipeline log...' })).toHaveProperty('disabled', true);
    expect(screen.getByRole('button', { name: 'Loading pipeline log...' }).getAttribute('aria-busy')).toBe('true');
    expect(screen.getByRole('status').textContent).toContain('Loading log...');

    log.resolve('');
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Pipeline log is empty.'));
    expect(screen.queryByRole('button', { name: 'Open pipeline log' })).toBeNull();
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);
  });

  it('renders an explicit fetch error and retries exactly once', async () => {
    mocks.getPipelineLog
      .mockRejectedValueOnce(new Error('Pipeline log request failed (503)'))
      .mockResolvedValueOnce('retried log');
    render(<StatusClient />);
    await screen.findByText('11');

    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Pipeline log request failed (503)');
    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));

    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(2);
    expect(await screen.findByText('retried log')).not.toBeNull();
  });

  it('normalizes malformed diagnostic output before rendering and exposes required accessibility landmarks', async () => {
    const normalized = normalizeStatus({
      sourcesCount: 1,
      conceptsCount: 2,
      rawCount: 3,
      last_execution: {
        name: 'malformed-execution',
        status: 'FAILED',
        log_state: 'available',
        log_url: 'https://logs.example/malformed',
        diagnostic: {
          stage: ['not-a-string'],
          error_class: { attacker: 'object' },
          detail_code: ['not-a-string'],
          child_command: 42,
          exit_code: '17',
        },
      },
    });
    mocks.getStatus.mockResolvedValue(normalized);

    render(<StatusClient />);
    expect(await screen.findByText('Failed — stage unavailable')).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Pipeline status' })).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Pipeline timeline' })).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Pipeline log' })).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Open pipeline log' })).not.toBeNull();
  });

  it.each([
    ['input_materialization', 'Input materialization'],
    ['synto_migration', 'Synto migration'],
    ['synto_config_normalization', 'Synto config normalization'],
    ['synto_config_validation', 'Synto config validation'],
    ['synto_run', 'Synto run'],
    ['synto_index_export', 'Synto index export'],
    ['source_reconciliation', 'Source reconciliation'],
    ['concept_reconciliation', 'Concept reconciliation'],
    ['postprocess', 'Postprocess'],
    ['generation_publish', 'Generation publish'],
    ['receipt_recording', 'Receipt recording'],
    ['lease_cleanup', 'Lease cleanup'],
  ] as const)('renders the known failed stage label for %s in the StatusClient UI', async (stage, label) => {
    mocks.getStatus.mockResolvedValue(status({
      lastExecution: execution({ diagnostic: { stage } }),
    }));

    render(<StatusClient />);

    expect(await screen.findByText(label)).not.toBeNull();
    expect(screen.queryByText('Failed — stage unavailable')).toBeNull();
  });

  it('renders an unknown failed stage as unavailable in the StatusClient UI', async () => {
    mocks.getStatus.mockResolvedValue(status({
      lastExecution: execution({ diagnostic: { stage: 'future_stage' } }),
    }));

    render(<StatusClient />);

    expect(await screen.findByText('Failed — stage unavailable')).not.toBeNull();
  });

  it('expands a large opened log to the full text and restores the latest-lines preview without refetching', async () => {
    const largeLog = [
      'BEGIN-UNIQUE-SENTINEL',
      'MIDDLE-UNIQUE-SENTINEL',
      ...Array.from({ length: 57 }, (_, index) => `filler-${index}-${'x'.repeat(220)}`),
      'END-UNIQUE-SENTINEL',
    ].join('\n');
    mocks.getPipelineLog.mockResolvedValue(largeLog);

    render(<StatusClient />);
    await screen.findByText('11');
    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));

    await waitFor(() => expect(document.querySelector('pre')?.textContent).toContain('END-UNIQUE-SENTINEL'));
    expect(document.querySelector('pre')?.textContent).not.toContain('BEGIN-UNIQUE-SENTINEL');
    expect(document.querySelector('pre')?.textContent).not.toContain('MIDDLE-UNIQUE-SENTINEL');
    expect(screen.getByRole('button', { name: 'Show full log' })).not.toBeNull();
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole('button', { name: 'Show full log' }));
    expect(document.querySelector('pre')?.textContent).toContain('BEGIN-UNIQUE-SENTINEL');
    expect(document.querySelector('pre')?.textContent).toContain('MIDDLE-UNIQUE-SENTINEL');
    expect(document.querySelector('pre')?.textContent).toContain('END-UNIQUE-SENTINEL');
    expect(screen.getByRole('button', { name: 'Show latest lines' })).not.toBeNull();
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole('button', { name: 'Show latest lines' }));
    expect(document.querySelector('pre')?.textContent).toContain('END-UNIQUE-SENTINEL');
    expect(document.querySelector('pre')?.textContent).not.toContain('BEGIN-UNIQUE-SENTINEL');
    expect(document.querySelector('pre')?.textContent).not.toContain('MIDDLE-UNIQUE-SENTINEL');
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);
  });

  it('removes project A status, diagnostics, log, and open control during the first project B render', async () => {
    const statusB = deferred<ReturnType<typeof status>>();
    mocks.getStatus.mockImplementation((projectId: string) =>
      projectId === 'project-a' ? Promise.resolve(status({
        sourcesCount: 101,
        conceptsCount: 202,
        rawCount: 303,
      })) : statusB.promise,
    );
    let firstBLayout = '';
    let captureBLayout = false;
    let clickedStaleOpen = false;
    const view = () => (
      <>
        <StatusClient />
        <RenderSnapshot
          onLayout={() => {
            if (captureBLayout) {
              firstBLayout = document.body.textContent ?? '';
              const staleOpen = screen.queryByRole('button', { name: 'Open pipeline log' });
              if (staleOpen) {
                clickedStaleOpen = true;
                fireEvent.click(staleOpen);
              }
            }
          }}
        />
      </>
    );

    const { rerender } = render(view());
    expect(await screen.findByText('101')).not.toBeNull();
    expect(screen.getByText('A-error-class')).not.toBeNull();

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    captureBLayout = true;
    rerender(view());

    expect(firstBLayout).not.toContain('101');
    expect(firstBLayout).not.toContain('A-error-class');
    expect(firstBLayout).not.toContain('Open pipeline log');
    expect(clickedStaleOpen).toBe(false);
    expect(mocks.getPipelineLog).not.toHaveBeenCalled();

    statusB.resolve(status({
      sourcesCount: 909,
      conceptsCount: 808,
      rawCount: 707,
      lastExecution: execution({
        name: 'execution-b',
        log_url: 'https://logs.example/project-b',
        diagnostic: { stage: 'source_reconciliation', error_class: 'B-error-class' },
      }),
    }));
    expect(await screen.findByText('B-error-class')).not.toBeNull();
    expect(screen.queryByText('A-error-class')).toBeNull();
  });

  it('never calls a stale project A log URL with the project B header or renders stale log data', async () => {
    const staleLog = deferred<string>();
    const statusB = deferred<ReturnType<typeof status>>();
    mocks.getPipelineLog.mockReturnValue(staleLog.promise);
    mocks.getStatus.mockImplementation((projectId: string) =>
      projectId === 'project-a' ? Promise.resolve(status()) : statusB.promise,
    );
    const { rerender } = render(<StatusClient />);
    await screen.findByText('11');
    fireEvent.click(screen.getByRole('button', { name: 'Open pipeline log' }));
    expect(mocks.getPipelineLog).toHaveBeenCalledWith('https://logs.example/project-a', 'project-a');

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    rerender(<StatusClient />);
    staleLog.resolve('STALE-PROJECT-A-LOG');
    await waitFor(() => expect(screen.queryByText('STALE-PROJECT-A-LOG')).toBeNull());
    expect(mocks.getPipelineLog).toHaveBeenCalledTimes(1);

    statusB.resolve(status({
      sourcesCount: 909,
      lastExecution: execution({ name: 'execution-b', log_url: 'https://logs.example/project-b' }),
    }));
    expect(await screen.findByText('909')).not.toBeNull();
    expect(screen.queryByText('STALE-PROJECT-A-LOG')).toBeNull();
  });

  it('prevents a late project A status response from winning a rapid A to B switch', async () => {
    const statusA = deferred<ReturnType<typeof status>>();
    const statusB = deferred<ReturnType<typeof status>>();
    mocks.getStatus.mockImplementation((projectId: string) =>
      projectId === 'project-a' ? statusA.promise : statusB.promise,
    );
    const { rerender } = render(<StatusClient />);
    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    rerender(<StatusClient />);

    await act(async () => {
      statusA.resolve(status({ sourcesCount: 111, lastExecution: execution({ diagnostic: { error_class: 'STALE-A' } }) }));
      await statusA.promise;
    });
    expect(screen.queryByText('STALE-A')).toBeNull();

    await act(async () => {
      statusB.resolve(status({ sourcesCount: 222, lastExecution: execution({ diagnostic: { error_class: 'CURRENT-B' } }) }));
      await statusB.promise;
    });
    expect(await screen.findByText('CURRENT-B')).not.toBeNull();
    expect(screen.queryByText('STALE-A')).toBeNull();
    expect(screen.getByText('222')).not.toBeNull();
  });
});
