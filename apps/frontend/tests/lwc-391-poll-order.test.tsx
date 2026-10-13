import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
const m = vi.hoisted(() => ({ status: vi.fn(), capability: vi.fn(), run: vi.fn() }));
vi.mock('@/lib/api', async () => ({ ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'), getPipelineStatus: m.status, getRecompileAllCapability: m.capability, recompileAll: m.run }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: { id: 'project-a' }, isDemoSession: false, refreshNavCounts: vi.fn() }) }));
import { PipelineClient } from '@/components/PipelineClient';
const quota = { enforced: true, allowed: true, runs_today: 0, daily_limit: 2, new_raw_files: 1, min_new_raw: 1, already_running: false };
const status = (state: string) => ({ project_id: 'project-a', quota, full_quota: quota, last_execution: { name: 'execution-1', status: state, duration: '1m', compile_mode: 'full', publication_outcome: state === 'SUCCEEDED' ? 'confirmed_published' : 'unknown', consumed_guidance: state === 'SUCCEEDED' ? { status: 'available', kind: 'active', revision: 'ACTUAL-G1', execution_id: 'execution-1' } : { status: 'unavailable', revision: null } } });
let intervals: Map<number, () => void>;
beforeEach(() => {
  window.localStorage.setItem('locale', 'zh-TW');
  window.localStorage.setItem('llm-wiki-last-project', 'project-a');
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  intervals = new Map(); let next = 0;
  vi.spyOn(globalThis, 'setInterval').mockImplementation(((cb: () => void) => { const id = ++next; intervals.set(id, cb); return id; }) as typeof setInterval);
  vi.spyOn(globalThis, 'clearInterval').mockImplementation(((id: number) => { intervals.delete(id); }) as typeof clearInterval);
  m.capability.mockResolvedValue({ allowed: true, compile_mode: 'full', quota });
  m.run.mockResolvedValue({ status: 'accepted', project_id: 'project-a', execution_id: 'execution-1', compile_mode: 'full', quota });
  m.status.mockImplementation((_p: string, e?: string) => Promise.resolve(e ? status('RUNNING') : { project_id: 'project-a', quota }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetAllMocks(); });
async function start() {
  render(<PipelineClient />);
  fireEvent.change(screen.getByTestId('pipeline-compile-mode'), { target: { value: 'full' } });
  const button = screen.getByRole('button', { name: '開始全面重編譯' });
  await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false));
  fireEvent.click(button);
  await screen.findByText('Pipeline running...');
}
async function tick() { await act(async () => { for (const cb of [...intervals.values()]) cb(); }); }

it('original owner recovery oracle using retained interval driver, with guidance and no second POST', async () => {
  await start();
  m.status.mockRejectedValueOnce(new Error('temporary status outage'));
  await tick();
  await screen.findByText('temporary status outage');
  m.status.mockResolvedValue(status('SUCCEEDED'));
  await act(async () => { window.dispatchEvent(new Event('source-annotation-saved')); });
  await tick();
  await waitFor(() => expect(screen.queryByText('Pipeline running...')).toBeNull());
  expect(screen.getByText('Pipeline complete (1m)')).toBeDefined();
  expect(screen.getByRole('button', { name: '開始全面重編譯' }).hasAttribute('disabled')).toBe(false);
  expect(screen.getByTestId('pipeline-full-evidence').textContent).toContain('ACTUAL-G1');
  expect(m.run).toHaveBeenCalledTimes(1);
});

it('a late same-execution RUNNING poll must not erase terminal recovery and strand the page', async () => {
  await start();
  m.status.mockRejectedValueOnce(new Error('temporary status outage'));
  await tick();
  await screen.findByText('temporary status outage');
  let resolveOld!: (value: ReturnType<typeof status>) => void;
  m.status.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }));
  await tick(); // recovery poll A is slow, beyond the next five-second interval
  m.status.mockResolvedValue(status('SUCCEEDED'));
  await tick(); // recovery poll B observes terminal first
  await screen.findByText('Pipeline complete (1m)');
  expect(intervals.size).toBe(0);
  expect(screen.getByTestId('pipeline-full-evidence').textContent).toContain('ACTUAL-G1');
  await act(async () => { resolveOld(status('RUNNING')); });
  // A later generic read cannot repair lifecycle under the execution owner.
  await act(async () => { window.dispatchEvent(new Event('source-annotation-saved')); });
  expect(m.status).toHaveBeenCalledWith('project-a', 'execution-1');
  expect(m.run).toHaveBeenCalledTimes(1);
  expect(intervals.size).toBe(0);
  expect.soft(screen.queryByText('Pipeline running...')).toBeNull();
  expect.soft(screen.queryByText('Pipeline complete (1m)')).not.toBeNull();
  expect.soft(screen.getByTestId('pipeline-full-evidence').textContent).toContain('ACTUAL-G1');
  expect.soft(screen.getByRole('button', { name: '開始全面重編譯' }).hasAttribute('disabled')).toBe(false);
});

it('persistent errors notify once, success resets the error series, unmount cleans the interval', async () => {
  await start();
  m.status.mockRejectedValue(new Error('temporary status outage'));
  await tick(); await tick();
  expect(screen.getAllByText('temporary status outage')).toHaveLength(1);
  expect(intervals.size).toBe(1);
  m.status.mockResolvedValue(status('RUNNING')); await tick();
  m.status.mockRejectedValue(new Error('temporary status outage')); await tick();
  expect(screen.getAllByText('temporary status outage')).toHaveLength(2);
  expect(m.run).toHaveBeenCalledTimes(1);
  cleanup(); expect(intervals.size).toBe(0);
});

it('ignores a late poll error after another poll confirms terminal', async () => {
  await start();
  m.status.mockRejectedValueOnce(new Error('temporary status outage'));
  await tick();
  await screen.findByText('temporary status outage');
  let rejectOld!: (error: Error) => void;
  m.status.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectOld = reject; }));
  await tick();
  m.status.mockResolvedValue(status('SUCCEEDED'));
  await tick();
  await screen.findByText('Pipeline complete (1m)');
  await act(async () => { rejectOld(new Error('late poll failure')); });
  expect(screen.queryByText('late poll failure')).toBeNull();
  expect(screen.getByText('Pipeline complete (1m)')).toBeDefined();
  expect(screen.getByTestId('pipeline-full-evidence').textContent).toContain('ACTUAL-G1');
  expect(screen.getByRole('button', { name: '開始全面重編譯' }).hasAttribute('disabled')).toBe(false);
  expect(m.run).toHaveBeenCalledTimes(1);
  expect(intervals.size).toBe(0);
});
