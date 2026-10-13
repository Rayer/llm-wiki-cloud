import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const m = vi.hoisted(() => ({
  project: { id: 'project-a', name: 'A' },
  status: vi.fn(),
  capability: vi.fn(),
  run: vi.fn(),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  getPipelineStatus: m.status,
  getRecompileAllCapability: m.capability,
  recompileAll: m.run,
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ currentProject: m.project, isDemoSession: false, refreshNavCounts: vi.fn() }),
}));

import { PipelineClient } from '@/components/PipelineClient';

const quota = {
  enforced: true,
  allowed: true,
  runs_today: 0,
  daily_limit: 2,
  new_raw_files: 1,
  min_new_raw: 1,
  already_running: false,
};

const accepted = (id: string) => ({
  status: 'accepted',
  project_id: 'project-a',
  execution_id: id,
  compile_mode: 'full',
  quota,
});

const status = (id: string, state: 'RUNNING' | 'SUCCEEDED') => ({
  project_id: 'project-a',
  quota,
  full_quota: quota,
  last_execution: {
    name: id,
    status: state,
    duration: '1m',
    compile_mode: 'full',
    publication_outcome: state === 'SUCCEEDED' ? 'confirmed_published' : 'unknown',
    consumed_guidance: state === 'SUCCEEDED'
      ? { status: 'available', kind: 'active', revision: 'RECOVERED-GUIDANCE', execution_id: id }
      : { status: 'unavailable', revision: null },
  },
});

let activeIntervals: Map<number, () => void>;
let nextInterval: number;

beforeEach(() => {
  m.project = { id: 'project-a', name: 'A' };
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  activeIntervals = new Map();
  nextInterval = 0;
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((callback: () => void) => {
    const id = ++nextInterval;
    activeIntervals.set(id, callback);
    return id;
  }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => {
    activeIntervals.delete(id);
  }) as typeof clearInterval);
  m.capability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  m.run.mockResolvedValue(accepted('execution-1'));
  m.status.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId ? status(executionId, 'RUNNING') : { project_id: projectId, quota, full_quota: quota },
  ));
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

async function start() {
  fireEvent.change(screen.getByTestId('pipeline-compile-mode'), { target: { value: 'full' } });
  const button = screen.getByRole('button', { name: '開始全面重編譯' });
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false));
  fireEvent.click(button);
}

it('recovers the accepted execution after a poll error without accepting another execution refresh', async () => {
  render(<PipelineClient />);
  await start();
  await screen.findByText('Pipeline running...');

  m.status.mockRejectedValueOnce(new Error('temporary status outage'));
  const retryPoll = [...activeIntervals.values()][0];
  await act(async () => { retryPoll?.(); });
  await screen.findByText('temporary status outage');
  expect(activeIntervals.size).toBe(1);

  m.status.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId
      ? status(executionId, 'SUCCEEDED')
      : status('different-execution', 'SUCCEEDED'),
  ));
  const priorCalls = m.status.mock.calls.length;
  await act(async () => { window.dispatchEvent(new Event('source-annotation-saved')); });
  await waitFor(() => expect(m.status.mock.calls.length).toBeGreaterThan(priorCalls));
  expect(screen.getByText('Pipeline running...')).toBeDefined();
  expect(screen.getByTestId('pipeline-full-evidence').textContent).not.toContain('different-execution');

  await act(async () => { retryPoll?.(); });
  await screen.findByText('Pipeline complete (1m)');
  expect(m.status).toHaveBeenCalledWith('project-a', 'execution-1');
  const evidence = screen.getByTestId('pipeline-full-evidence');
  expect(evidence.textContent).toContain('已確認由本次執行發布');
  expect(evidence.textContent).toContain('已啟用的 Profile 指引 RECOVERED-GUIDANCE');
  expect(screen.getByRole('button', { name: '開始全面重編譯' }).hasAttribute('disabled')).toBe(false);
  expect(m.run).toHaveBeenCalledTimes(1);
});

it('can start a second execution after terminal scoped polling', async () => {
  m.status.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId ? status(executionId, 'SUCCEEDED') : { project_id: projectId, quota },
  ));
  render(<PipelineClient />);
  await start();
  await screen.findByText('Pipeline complete (1m)');
  await waitFor(() => expect(m.capability).toHaveBeenCalledTimes(2));

  m.run.mockResolvedValue(accepted('execution-2'));
  m.status.mockImplementation((projectId: string, executionId?: string) => Promise.resolve(
    executionId ? status(executionId, 'RUNNING') : { project_id: projectId, quota },
  ));
  await start();
  await waitFor(() => expect(m.status).toHaveBeenCalledWith('project-a', 'execution-2'));
  expect(screen.getByText('Pipeline running...')).toBeDefined();
});

it('POST rejection releases loading and permits same Project retry', async () => {
  m.run.mockRejectedValueOnce(new Error('quota rejected'));
  render(<PipelineClient />);
  await start();
  await screen.findByText('quota rejected');
  await start();
  await waitFor(() => expect(m.run).toHaveBeenCalledTimes(2));
  await screen.findByText('Pipeline running...');
});

it('nonaccepted POST leaves generic lifecycle refresh usable', async () => {
  m.run.mockResolvedValue({ status: 'denied' });
  render(<PipelineClient />);
  await start();
  await waitFor(() => expect(m.run).toHaveBeenCalledTimes(1));
  m.status.mockResolvedValue(status('previous', 'SUCCEEDED'));
  await act(async () => { window.dispatchEvent(new Event('source-annotation-saved')); });
  expect(screen.getByText('Pipeline complete (1m)')).toBeDefined();
});

it('Project context change resets execution owner for generic refresh', async () => {
  const view = render(<PipelineClient />);
  await start();
  await screen.findByText('Pipeline running...');
  m.project = { id: 'project-b', name: 'B' };
  window.localStorage.setItem('llm-wiki-last-project', 'project-b');
  view.rerender(<PipelineClient />);
  await waitFor(() => expect(m.capability).toHaveBeenCalledWith('project-b'));
  m.status.mockResolvedValue({ ...status('execution-b', 'SUCCEEDED'), project_id: 'project-b' });
  await act(async () => { window.dispatchEvent(new Event('source-annotation-saved')); });
  expect(screen.getByText('Pipeline complete (1m)')).toBeDefined();
});
