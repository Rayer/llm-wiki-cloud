import { afterEach, describe, expect, it, vi } from 'vitest';

const { apiFetch } = vi.hoisted(() => ({ apiFetch: vi.fn() }));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return { ...actual, apiFetch };
});
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

  it('preserves Export error priority and both normalized server IDs', async () => {
    let bodyReads = 0;
    apiFetch.mockResolvedValue({
      ok: false,
      status: 503,
      headers: { get: (name: string) => name === 'X-Request-ID' ? '123e4567-e89b-42d3-a456-426614174005' : null },
      json: async () => {
        bodyReads += 1;
        return {
          error_message: 'export primary message',
          reason: 'lower priority reason',
          error: 'last priority error',
          diagnostic_id: 'export_help-123',
        };
      },
    });

    await expect(getExportState('project-a')).rejects.toMatchObject({
      message: 'export primary message',
      status: 503,
      diagnosticId: 'export_help-123',
      requestId: '123e4567-e89b-42d3-a456-426614174005',
    });
    expect(bodyReads).toBe(1);

    apiFetch.mockResolvedValue({
      ok: false,
      status: 502,
      headers: { get: () => 'invalid request id' },
      json: async () => ({ reason: 'export reason', diagnostic_id: 'invalid/id' }),
    });
    await expect(getExportState('project-a')).rejects.toMatchObject({
      message: 'export reason',
      status: 502,
      diagnosticId: undefined,
      requestId: undefined,
    });
  });
});
