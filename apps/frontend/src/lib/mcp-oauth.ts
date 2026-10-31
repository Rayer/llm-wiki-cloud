'use client';

import { getRuntimeConfig } from './runtime-config';
import { recordRequestResponse } from './request-ids';

export type MCPOAuthConsent = {
  status?: 'pending' | 'completed';
  schema_version?: 1;
  redirect_url?: string;
  requires_consent?: boolean;
  transaction_id: string;
  can_continue?: boolean;
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

export type MCPOAuthConsentDecision = {
  redirect_url: string;
  requires_consent: boolean;
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

const DAILY_BRIEF_DEV_MCP_RESOURCE = 'https://brief-dev.rayer.idv.tw/mcp';
const DAILY_BRIEF_PROD_MCP_RESOURCE = 'https://brief.rayer.idv.tw/mcp';

export class MCPOAuthApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
    this.name = 'MCPOAuthApiError';
  }
}

async function request<T>(path: string, accessToken: string, init: RequestInit = {}): Promise<T> {
  return requestFrom(`${getRuntimeConfig().auth_url}/api/v1/auth/mcp/oauth${path}`, accessToken, init);
}

async function requestBFF<T>(path: string, accessToken: string, init: RequestInit = {}): Promise<T> {
  return requestFrom(`${getRuntimeConfig().api_url}/api/v1/mcp/oauth${path}`, accessToken, init);
}

async function requestFrom<T>(url: string, accessToken: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(url, {
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
  const route = `/consent/${encodeURIComponent(transactionId)}`;
  return request<{ schema_version?: number; resource_kind?: string } & MCPOAuthConsent>(route, accessToken)
    .then((classification) => classification.resource_kind === 'project-template'
      ? requestBFF<MCPOAuthConsent>(route, accessToken)
      : classification);
}

export function decideMCPOAuthConsent(
  transactionId: string,
  decision: 'allow' | 'deny' | 'continue',
  accessToken: string,
): Promise<MCPOAuthConsentDecision> {
  const route = `/consent/${encodeURIComponent(transactionId)}`;
  return request<{ resource_kind?: string }>(route, accessToken).then((classification) => {
    const isProject = classification.resource_kind === 'project-template';
    return (isProject ? requestBFF : request)<MCPOAuthConsentDecision>(`${route}/decision`, accessToken, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(isProject ? { schema_version: 1, decision } : { decision }),
    });
  });
}

export async function listMCPOAuthGrants(projectId: string, accessToken: string): Promise<MCPOAuthGrant[]> {
  const result = await requestBFF<{ grants?: unknown }>(`/grants?project_id=${encodeURIComponent(projectId)}`, accessToken);
  if (!Array.isArray(result.grants)) throw new MCPOAuthApiError('The MCP grant list was invalid.', 502);
  return result.grants as MCPOAuthGrant[];
}

export async function listMCPOAuthResourceGrants(resource: string, accessToken: string): Promise<MCPOAuthGrant[]> {
  const result = await request<{ grants?: unknown }>(`/grants?resource=${encodeURIComponent(resource)}`, accessToken);
  if (!Array.isArray(result.grants)) throw new MCPOAuthApiError('The MCP grant list was invalid.', 502);
  return result.grants as MCPOAuthGrant[];
}

export function dailyBriefMcpResourceURL(): string {
  const authURL = new URL(getRuntimeConfig().auth_url);
  if (authURL.origin === 'https://auth.dev.rayer.idv.tw') return DAILY_BRIEF_DEV_MCP_RESOURCE;
  if (authURL.origin === 'https://auth.rayer.idv.tw') return DAILY_BRIEF_PROD_MCP_RESOURCE;
  if (authURL.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(authURL.hostname)) {
    return DAILY_BRIEF_PROD_MCP_RESOURCE;
  }
  throw new Error('Daily Brief resource is not configured for this Auth endpoint.');
}

export function revokeMCPOAuthGrant(grantId: string, accessToken: string): Promise<void> {
  const path = `/grants/${encodeURIComponent(grantId)}`;
  return request(path, accessToken, { method: 'DELETE' });
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
