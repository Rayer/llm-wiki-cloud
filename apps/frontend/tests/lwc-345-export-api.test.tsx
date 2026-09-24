import { afterEach, describe, expect, it, vi } from 'vitest';

const { apiFetch } = vi.hoisted(() => ({ apiFetch: vi.fn() }));
vi.mock('@/lib/api', () => ({ apiFetch }));
import { createExport, getExportState, getExportStatus, requestExportDownload } from '@/lib/export-api';

afterEach(() => vi.clearAllMocks());

describe('LWC-344 export API fixture contract', () => {
  it('uses the plural collection route and project header for state and create', async () => {
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ latest_job: null, current: null, previous: null, eligible: true, rejection_reason: null, next_allowed_at: null }) });
    await getExportState('project id');
    expect(apiFetch).toHaveBeenLastCalledWith('/api/v1/exports', { projectId: 'project id', method: undefined, json: false, body: undefined, signal: undefined });
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ export_id: 'exp-1', scope: 'raw', status: 'queued' }) });
    await createExport('project id', 'raw');
    expect(apiFetch).toHaveBeenLastCalledWith('/api/v1/exports', { method: 'POST', projectId: 'project id', json: true, body: JSON.stringify({ scope: 'raw' }), signal: undefined });
  });

  it('encodes job paths for status and only returns signed URLs from the download POST', async () => {
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ export_id: 'a/b', status: 'ready' }) });
    await getExportStatus('p', 'a/b');
    expect(apiFetch).toHaveBeenLastCalledWith('/api/v1/exports/a%2Fb/status', { projectId: 'p', method: undefined, json: false, body: undefined, signal: undefined });
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ export_id: 'a/b', signed_url: 'https://storage.example/file', expires_at: '2026-09-27T00:00:00Z', filename: 'project.zip' }) });
    const result = await requestExportDownload('p', 'a/b');
    expect(apiFetch).toHaveBeenLastCalledWith('/api/v1/exports/a%2Fb/download', { method: 'POST', projectId: 'p', json: false, body: undefined, signal: undefined });
    expect(result.signed_url).toBe('https://storage.example/file');
  });
});
