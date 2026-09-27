import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  getExportState: vi.fn(), createExport: vi.fn(), getExportStatus: vi.fn(),
  requestExportDownload: vi.fn(), currentProject: { id: 'project-a', name: 'Project A' },
}));
vi.mock('@/lib/export-api', () => ({
  getExportState: mocks.getExportState, createExport: mocks.createExport,
  getExportStatus: mocks.getExportStatus, requestExportDownload: mocks.requestExportDownload,
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: mocks.currentProject }) }));
import { ExportPanel } from '@/components/ExportPanel';
import type { ExportState } from '@/lib/export-api';

const emptyState = { latest_job: null, current: null, previous: null, eligible: true, rejection_reason: null, next_allowed_at: null };
beforeEach(() => { mocks.currentProject = { id: 'project-a', name: 'Project A' }; window.localStorage.setItem('locale', 'zh-TW'); mocks.getExportState.mockResolvedValue(emptyState); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('LWC-345 export UI fixtures', () => {
  it('offers scope selection and explains exclusions, then renders server ready state and cooldown', async () => {
    render(<ExportPanel />);
    expect(await screen.findByRole('button', { name: '打包帶走' })).toBeDefined();
    expect(screen.getByRole('button', { name: '下載資料' })).toHaveProperty('disabled', true);
    fireEvent.click(screen.getByRole('button', { name: '打包帶走' }));
    expect(screen.getByRole('dialog')).toBeDefined();
    expect(screen.getByText(/不包含登入憑證/)).toBeDefined();
    expect(screen.getByText(/不會自動 compile/)).toBeDefined();
    expect(screen.getByText(/不保證可直接 restore/)).toBeDefined();
    fireEvent.click(screen.getByDisplayValue('raw-full'));
    mocks.createExport.mockResolvedValue({ export_id: 'exp-1', scope: 'raw-full', status: 'queued', created_at: '2026-09-24T03:00:00Z', snapshot_at: null, completed_at: null, expires_at: null, next_allowed_at: null, size_bytes: null, download_available: false, error_code: null, error_message: null });
    mocks.getExportState.mockResolvedValue({ ...emptyState, latest_job: { export_id: 'exp-1', scope: 'raw-full', status: 'ready', created_at: '2026-09-24T03:00:00Z', snapshot_at: '2026-09-24T02:55:00Z', completed_at: '2026-09-24T03:00:00Z', expires_at: '2026-09-27T03:00:00Z', next_allowed_at: '2026-09-25T03:00:00Z', size_bytes: 2048, download_available: true, error_code: null, error_message: null }, current: { export_id: 'exp-1', scope: 'raw-full', status: 'ready', snapshot_at: '2026-09-24T02:55:00Z', completed_at: '2026-09-24T03:00:00Z', expires_at: '2026-09-27T03:00:00Z', size_bytes: 2048, download_available: true }, eligible: false, rejection_reason: 'cooldown', next_allowed_at: '2026-09-25T03:00:00Z' });
    fireEvent.click(screen.getByRole('button', { name: '開始打包' }));
    await waitFor(() => expect(mocks.createExport).toHaveBeenCalledWith('project-a', 'raw-full'));
    expect(await screen.findByText(/2 KB/)).toBeDefined();
    expect(screen.getAllByText(/下次可打包/).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: '下載資料' })).toHaveProperty('disabled', false);
  });

  it('keeps a previous archive downloadable while the newest job is running', async () => {
    mocks.getExportState.mockResolvedValue({ ...emptyState, latest_job: { export_id: 'exp-2', scope: 'raw', status: 'running', created_at: '2026-09-24T04:00:00Z', snapshot_at: null, completed_at: null, expires_at: null, next_allowed_at: null, size_bytes: null, download_available: false, error_code: null, error_message: null }, current: { export_id: 'exp-1', scope: 'raw', status: 'ready', snapshot_at: '2026-09-24T02:00:00Z', completed_at: '2026-09-24T02:10:00Z', expires_at: '2026-09-27T02:10:00Z', size_bytes: 10, download_available: true }, eligible: false, rejection_reason: 'in_progress', next_allowed_at: null });
    render(<ExportPanel />);
    expect(await screen.findByText('資料準備中', { selector: 'p' })).toBeDefined();
    expect(screen.getByRole('button', { name: '資料準備中' })).toBeDefined();
    expect(screen.getByRole('button', { name: '下載上一份' })).toHaveProperty('disabled', false);
  });

  it('shows server failure and expiry states with an available retry', async () => {
    mocks.getExportState.mockResolvedValueOnce({
      ...emptyState,
      latest_job: { export_id: 'exp-f', scope: 'raw', status: 'failed', created_at: '2026-09-24T04:00:00Z', snapshot_at: null, completed_at: null, expires_at: null, next_allowed_at: null, size_bytes: null, download_available: false, error_code: 'worker_failed', error_message: 'Archive could not be created.' },
      eligible: true,
    });
    const failed = render(<ExportPanel />);
    expect(await screen.findByText(/匯出失敗: Archive could not be created\./)).toBeDefined();
    expect(screen.getByRole('button', { name: '重試' })).toHaveProperty('disabled', false);
    failed.unmount();

    mocks.getExportState.mockResolvedValueOnce({
      ...emptyState,
      latest_job: { export_id: 'exp-e', scope: 'raw', status: 'expired', created_at: '2026-09-20T04:00:00Z', snapshot_at: '2026-09-20T04:00:00Z', completed_at: '2026-09-20T04:00:00Z', expires_at: '2026-09-23T04:00:00Z', next_allowed_at: null, size_bytes: 10, download_available: false, error_code: null, error_message: null },
      eligible: true,
    });
    render(<ExportPanel />);
    expect(await screen.findByText(/匯出檔已到期/)).toBeDefined();
    expect(screen.getByRole('button', { name: '下載資料' })).toHaveProperty('disabled', true);
  });

  it('surfaces a project permission rejection returned by the API', async () => {
    mocks.getExportState.mockRejectedValueOnce(new Error('project_or_export_not_found'));
    render(<ExportPanel />);
    expect((await screen.findByRole('alert')).textContent).toContain('project_or_export_not_found');
    expect(screen.getByRole('button', { name: '下載資料' })).toHaveProperty('disabled', true);
  });

  it('ignores a late response from the previously selected project', async () => {
    let resolveFirst!: (state: ExportState) => void;
    mocks.getExportState
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
      .mockResolvedValueOnce({
        ...emptyState,
        current: { export_id: 'exp-b', scope: 'raw', status: 'ready', snapshot_at: null, completed_at: null, expires_at: null, size_bytes: 10, download_available: true },
      });
    const { rerender } = render(<ExportPanel />);
    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    rerender(<ExportPanel />);
    expect(await screen.findByText('原始資料')).toBeDefined();
    resolveFirst({
      ...emptyState,
      current: { export_id: 'exp-a', scope: 'raw-full-metadata', status: 'ready', snapshot_at: null, completed_at: null, expires_at: null, size_bytes: 10, download_available: true },
    });
    await waitFor(() => expect(screen.queryByText('原始資料、Wiki 與中繼資料')).toBeNull());
  });

  it('sends only one create request for two synchronous start clicks', async () => {
    let resolveCreate!: (job: { export_id: string; scope: 'raw-full-metadata'; status: 'queued' }) => void;
    mocks.createExport.mockImplementation(() => new Promise((resolve) => { resolveCreate = resolve; }));
    render(<ExportPanel />);
    fireEvent.click(await screen.findByRole('button', { name: '打包帶走' }));
    const start = screen.getByRole('button', { name: '開始打包' });
    act(() => { fireEvent.click(start); fireEvent.click(start); });
    expect(mocks.createExport).toHaveBeenCalledTimes(1);
    await act(async () => resolveCreate({ export_id: 'exp-once', scope: 'raw-full-metadata', status: 'queued' }));
  });

  it('keeps a create error after its state refresh succeeds', async () => {
    mocks.createExport.mockRejectedValue(new Error('export_rejected:cooldown'));
    render(<ExportPanel />);
    fireEvent.click(await screen.findByRole('button', { name: '打包帶走' }));
    fireEvent.click(screen.getByRole('button', { name: '開始打包' }));
    await waitFor(() => expect(screen.getAllByRole('alert').some((node) => node.textContent?.includes('export_rejected:cooldown'))).toBe(true));
  });

  it('keeps a download error after its state refresh succeeds', async () => {
    mocks.getExportState.mockResolvedValue({ ...emptyState, current: { export_id: 'exp-d', scope: 'raw', status: 'ready', snapshot_at: null, completed_at: null, expires_at: null, size_bytes: 10, download_available: true } });
    mocks.requestExportDownload.mockRejectedValue(new Error('export_expired'));
    render(<ExportPanel />);
    fireEvent.click(await screen.findByRole('button', { name: '下載資料' }));
    await waitFor(() => expect(screen.getAllByRole('alert').some((node) => node.textContent?.includes('export_expired'))).toBe(true));
  });

  it('closes the modal and resets scope when the project changes', async () => {
    const { rerender } = render(<ExportPanel />);
    fireEvent.click(await screen.findByRole('button', { name: '打包帶走' }));
    fireEvent.click(screen.getByDisplayValue('raw'));
    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    rerender(<ExportPanel />);
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    fireEvent.click(await screen.findByRole('button', { name: '打包帶走' }));
    expect(screen.getByDisplayValue('raw-full-metadata')).toBeDefined();
  });

  it('focuses the dialog, closes on Escape, and restores focus to its opener', async () => {
    render(<ExportPanel />);
    const opener = await screen.findByRole('button', { name: '打包帶走' });
    opener.focus();
    fireEvent.click(opener);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByDisplayValue('raw-full-metadata')));
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.activeElement).toBe(opener);
  });

  it('rechecks server eligibility when the cooldown timestamp passes without enabling locally', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-24T00:00:00Z'));
    mocks.getExportState.mockResolvedValue({ ...emptyState, eligible: false, rejection_reason: 'cooldown', next_allowed_at: '2026-09-24T00:00:01Z' });
    render(<ExportPanel />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    const packageButton = screen.getByRole('button', { name: '打包帶走' });
    expect(packageButton).toHaveProperty('disabled', true);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(mocks.getExportState).toHaveBeenCalledTimes(2);
    expect(packageButton).toHaveProperty('disabled', true);
    vi.useRealTimers();
  });
});
