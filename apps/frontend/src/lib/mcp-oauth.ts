'use client';

import { getRuntimeConfig } from './runtime-config';
import { recordRequestResponse } from './request-ids';

export type MCPOAuthConsent = {
  transaction_id: string;
  account_email: string;
  project_id?: string;
  project_name?: string;
  client_name: string;
  resource: string;
  resource_kind?: 'project' | 'fixed' | string;
  resource_name?: string;
  scope_details?: Array<{ name: string; description: string }>;
  scope: string;
  expires_at: string;
};

export type MCPOAuthGrant = {
  id: string;
  client_name: string;
  project_id: string;
  resource: string;
  scope: string;
  status: 'active' | 'revoked' | 'expired' | string;
  created_at: string;
  expires_at: string;
  revoked_at?: string;
};

export const DAILY_BRIEF_MCP_RESOURCE = 'https://brief.rayer.idv.tw/mcp';

export class MCPOAuthApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
    this.name = 'MCPOAuthApiError';
  }
}

async function request<T>(path: string, accessToken: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${getRuntimeConfig().auth_url}/api/v1/auth/mcp/oauth${path}`, {
    ...init,
    credentials: 'include',
    cache: 'no-store',
    headers: {
      Authorization: `Bearer ${accessToken}`,
      ...(init.headers ?? {}),
    },
  });
  recordRequestResponse(response);
  if (response.status === 204) return undefined as T;
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const message = payload && typeof payload === 'object' && !Array.isArray(payload)
      && typeof (payload as Record<string, unknown>).error === 'string'
      ? String((payload as Record<string, unknown>).error)
      : `MCP OAuth request failed (${response.status}).`;
    throw new MCPOAuthApiError(message, response.status);
  }
  return payload as T;
}

export function readMCPOAuthConsent(transactionId: string, accessToken: string): Promise<MCPOAuthConsent> {
  return request(`/consent/${encodeURIComponent(transactionId)}`, accessToken);
}

export function decideMCPOAuthConsent(
  transactionId: string,
  decision: 'allow' | 'deny',
  accessToken: string,
): Promise<{ redirect_url: string }> {
  return request(`/consent/${encodeURIComponent(transactionId)}/decision`, accessToken, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ decision }),
  });
}

export async function listMCPOAuthGrants(projectId: string, accessToken: string): Promise<MCPOAuthGrant[]> {
  const result = await request<{ grants?: unknown }>(`/grants?project_id=${encodeURIComponent(projectId)}`, accessToken);
  if (!Array.isArray(result.grants)) throw new MCPOAuthApiError('The MCP grant list was invalid.', 502);
  return result.grants as MCPOAuthGrant[];
}

export async function listMCPOAuthResourceGrants(resource: string, accessToken: string): Promise<MCPOAuthGrant[]> {
  const result = await request<{ grants?: unknown }>(`/grants?resource=${encodeURIComponent(resource)}`, accessToken);
  if (!Array.isArray(result.grants)) throw new MCPOAuthApiError('The MCP grant list was invalid.', 502);
  return result.grants as MCPOAuthGrant[];
}

export function revokeMCPOAuthGrant(grantId: string, accessToken: string): Promise<void> {
  return request(`/grants/${encodeURIComponent(grantId)}`, accessToken, { method: 'DELETE' });
}

export function mcpResourceURL(projectId: string): string {
  return `${getRuntimeConfig().api_url}/mcp/projects/${encodeURIComponent(projectId)}`;
}

export function isSafeMCPOAuthReturnURL(value: string): boolean {
  try {
    const url = new URL(value);
    const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
    return (url.protocol === 'https:' || (url.protocol === 'http:' && loopback && Boolean(url.port)))
      && Boolean(url.host)
      && !url.username
      && !url.password
      && !url.hash;
  } catch {
    return false;
  }
}
