import { AUTH_URL } from './public-build-config.ts';

export type CLISession = {
  id: string;
  client_name: string;
  status: string;
  created_at: string;
  updated_at: string;
};

export type SyncBinding = {
  binding_id: string;
  host: string;
  wiki_id: string;
  project_id: string;
  authorized_by: string;
  status: 'active' | 'revoked' | string;
  created_at: string;
  updated_at: string;
};

type AuthContext = {
  accessToken: string;
  refreshAccessToken: () => Promise<string | null>;
};

type RequestOptions = {
  method?: 'GET' | 'POST' | 'DELETE';
  body?: unknown;
};

async function cliAuthRequest<T>(
  context: AuthContext,
  route: string,
  options: RequestOptions = {},
): Promise<T> {
  const url = `${AUTH_URL.replace(/\/$/, '')}/api/v1/auth/cli${route}`;
  const makeRequest = (token: string) => fetch(url, {
    method: options.method ?? 'GET',
    credentials: 'include',
    headers: {
      Authorization: `Bearer ${token}`,
      ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    redirect: 'error',
  });

  let response = await makeRequest(context.accessToken);
  if (response.status === 401) {
    const refreshed = await context.refreshAccessToken();
    if (refreshed) response = await makeRequest(refreshed);
  }
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const record = payload && typeof payload === 'object' ? payload as Record<string, unknown> : null;
    const message = record && typeof record.error === 'string' ? record.error : `Auth request failed (${response.status})`;
    throw new Error(message);
  }
  return payload as T;
}

export async function decideCLIPairing(
  context: AuthContext,
  userCode: string,
  decision: 'approve' | 'deny',
): Promise<void> {
  await cliAuthRequest(context, '/pairing/decision', { method: 'POST', body: { user_code: userCode, decision } });
}

export async function listCLISessions(context: AuthContext): Promise<CLISession[]> {
  const result = await cliAuthRequest<{ sessions: CLISession[] }>(context, '/sessions');
  return Array.isArray(result.sessions) ? result.sessions : [];
}

export async function revokeCLISession(context: AuthContext, sessionID: string): Promise<void> {
  await cliAuthRequest(context, `/sessions/${encodeURIComponent(sessionID)}/revoke`, { method: 'POST' });
}

export async function listSyncBindings(context: AuthContext): Promise<SyncBinding[]> {
  const result = await cliAuthRequest<{ bindings: SyncBinding[] }>(context, '/bindings');
  return Array.isArray(result.bindings) ? result.bindings : [];
}

export async function revokeSyncBinding(context: AuthContext, binding: SyncBinding): Promise<void> {
  const projectID = encodeURIComponent(binding.project_id);
  const bindingID = encodeURIComponent(binding.binding_id);
  await cliAuthRequest(context, `/bindings/${projectID}/${bindingID}/revoke`, { method: 'POST' });
}

export async function reauthorizeSyncBinding(context: AuthContext, binding: SyncBinding): Promise<SyncBinding> {
  const projectID = encodeURIComponent(binding.project_id);
  return cliAuthRequest<SyncBinding>(context, `/bindings/${projectID}/reauthorize`, {
    method: 'POST',
    body: { binding_id: binding.binding_id, wiki_id: binding.wiki_id, host: binding.host },
  });
}
