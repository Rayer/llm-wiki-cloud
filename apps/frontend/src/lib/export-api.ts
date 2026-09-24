import { apiFetch } from './api';

export type ExportScope = 'raw' | 'raw-full' | 'raw-full-metadata';
export type ExportJobStatus = 'queued' | 'running' | 'ready' | 'failed' | 'expired';

export type ExportJob = {
  export_id: string;
  scope: ExportScope;
  status: ExportJobStatus;
  created_at: string;
  snapshot_at: string | null;
  completed_at: string | null;
  expires_at: string | null;
  next_allowed_at: string | null;
  size_bytes: number | null;
  download_available: boolean;
  error_code: string | null;
  error_message: string | null;
};

export type ExportArchive = Pick<ExportJob,
  'export_id' | 'scope' | 'status' | 'snapshot_at' | 'completed_at' | 'expires_at' | 'size_bytes' | 'download_available'
>;

export type ExportState = {
  latest_job: ExportJob | null;
  current: ExportArchive | null;
  previous: ExportArchive | null;
  eligible: boolean;
  rejection_reason: 'in_progress' | 'cooldown' | null;
  next_allowed_at: string | null;
};

export type ExportDownload = {
  export_id: string;
  signed_url: string;
  expires_at: string;
  filename: string;
};

async function exportJson<T>(path: string, projectId: string, options: { method?: string; body?: unknown; signal?: AbortSignal } = {}): Promise<T> {
  const response = await apiFetch(path, {
    method: options.method,
    projectId,
    json: options.body !== undefined,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const message = payload && typeof payload === 'object' && 'error_message' in payload
      ? String(payload.error_message ?? '')
      : payload && typeof payload === 'object' && 'reason' in payload
        ? String(payload.reason ?? '')
        : payload && typeof payload === 'object' && 'error' in payload
          ? String(payload.error ?? '')
          : '';
    throw Object.assign(new Error(message || `Export request failed (${response.status})`), {
      status: response.status,
      payload,
    });
  }
  return payload as T;
}

export function getExportState(projectId: string, signal?: AbortSignal): Promise<ExportState> {
  return exportJson<ExportState>('/api/v1/exports', projectId, { signal });
}

export function getExportStatus(projectId: string, exportId: string, signal?: AbortSignal): Promise<ExportJob> {
  return exportJson<ExportJob>(`/api/v1/exports/${encodeURIComponent(exportId)}/status`, projectId, { signal });
}

export function createExport(projectId: string, scope: ExportScope): Promise<ExportJob> {
  return exportJson<ExportJob>('/api/v1/exports', projectId, { method: 'POST', body: { scope } });
}

export function requestExportDownload(projectId: string, exportId: string): Promise<ExportDownload> {
  return exportJson<ExportDownload>(`/api/v1/exports/${encodeURIComponent(exportId)}/download`, projectId, { method: 'POST' });
}
