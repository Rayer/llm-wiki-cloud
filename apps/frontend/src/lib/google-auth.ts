'use client';

import { AUTH_URL } from './auth-core';

export const GOOGLE_CANCELLED_COPY = '已取消使用 Google 登入。';

export type GoogleCompletionResult = {
  status: 'success' | 'cancelled' | 'failure' | 'confirmation_required';
  error: string;
  support_ref: string;
  jit_provisioned: boolean;
};

export type GoogleLinkCompletion = {
  status: 'confirmation_required';
  confirmation_id: string;
  provider: string;
  provider_email: string;
  provider_email_verified: boolean;
  current_email: string;
};

export type GoogleLinkDecision = {
  status: 'linked' | 'cancelled';
  error?: string;
  support_ref?: string;
};

export type GoogleIdentitySummary = {
  primary_email: string;
  linked_providers: Array<{
    provider: string;
    provider_email: string;
    provider_email_verified: boolean;
  }>;
};

export class GoogleAuthError extends Error {
  status: number;
  supportRef: string;

  constructor(message: string, status: number, supportRef = 'unavailable') {
    super(message);
    this.name = 'GoogleAuthError';
    this.status = status;
    this.supportRef = supportRef;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function safeString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function supportReference(value: unknown): string {
  const reference = safeString(value);
  return /^[A-Za-z0-9_-]{12,128}$/.test(reference) ? reference : 'unavailable';
}

function errorFromPayload(payload: unknown, status: number): GoogleAuthError {
  const record = isRecord(payload) ? payload : {};
  const message = safeString(record.error) || 'Unable to continue with Google sign-in.';
  return new GoogleAuthError(message, status, supportReference(record.support_ref));
}

async function jsonRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${AUTH_URL}${path}`, {
    ...init,
    credentials: 'include',
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) throw errorFromPayload(payload, response.status);
  return payload as T;
}

async function authenticatedJson<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  return jsonRequest<T>(path, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(init.headers ?? {}),
    },
  });
}

export function startGoogleLogin(): void {
  window.location.assign(`${AUTH_URL}/api/v1/auth/google/login/start`);
}

export async function beginGoogleLink(currentPassword: string, token: string): Promise<void> {
  if (!currentPassword) throw new GoogleAuthError('Current password is required.', 400);
  if (!token) throw new GoogleAuthError('Your session has expired. Sign in again.', 401);
  const payload = await authenticatedJson<unknown>('/api/v1/auth/google/link/start', token, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ current_password: currentPassword }),
  });
  const authorizationURL = isRecord(payload) ? safeString(payload.authorization_url) : '';
  if (!authorizationURL) throw new GoogleAuthError('Unable to link this Google account.', 502);
  const parsed = new URL(authorizationURL);
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.hash) {
    throw new GoogleAuthError('Unable to link this Google account.', 502);
  }
  window.location.assign(authorizationURL);
}

export async function readGoogleCompletionResult(): Promise<GoogleCompletionResult> {
  const payload = await jsonRequest<unknown>('/api/v1/auth/google/complete');
  if (!isRecord(payload)) throw new GoogleAuthError('Unable to continue with Google sign-in.', 502);
  const status = payload.status;
  if (status !== 'success' && status !== 'cancelled' && status !== 'failure' && status !== 'confirmation_required') {
    throw new GoogleAuthError('Unable to continue with Google sign-in.', 502);
  }
  return {
    status,
    error: safeString(payload.error),
    support_ref: supportReference(payload.support_ref),
    jit_provisioned: payload.jit_provisioned === true,
  };
}

export async function readGoogleLinkCompletion(token: string): Promise<GoogleLinkCompletion> {
  const payload = await authenticatedJson<unknown>('/api/v1/auth/google/link/complete', token);
  if (!isRecord(payload)
    || payload.status !== 'confirmation_required'
    || !safeString(payload.confirmation_id)
    || !safeString(payload.provider_email)
    || !safeString(payload.current_email)) {
    throw new GoogleAuthError('Unable to link this Google account.', 502);
  }
  return {
    status: 'confirmation_required',
    confirmation_id: safeString(payload.confirmation_id),
    provider: safeString(payload.provider) || 'google',
    provider_email: safeString(payload.provider_email),
    provider_email_verified: payload.provider_email_verified === true,
    current_email: safeString(payload.current_email),
  };
}

export async function readGoogleIdentitySummary(token: string): Promise<GoogleIdentitySummary> {
  const payload = await authenticatedJson<unknown>('/api/v1/auth/google/identity', token);
  if (!isRecord(payload) || !safeString(payload.primary_email) || !Array.isArray(payload.linked_providers)) {
    throw new GoogleAuthError('Unable to load linked sign-in methods.', 502);
  }
  const linkedProviders = payload.linked_providers.flatMap((provider) => {
    if (!isRecord(provider) || !safeString(provider.provider)) return [];
    return [{
      provider: safeString(provider.provider),
      provider_email: safeString(provider.provider_email),
      provider_email_verified: provider.provider_email_verified === true,
    }];
  });
  return { primary_email: safeString(payload.primary_email), linked_providers: linkedProviders };
}

export function confirmGoogleLink(token: string, confirmationId: string): Promise<GoogleLinkDecision> {
  return authenticatedJson<GoogleLinkDecision>('/api/v1/auth/google/link/confirm', token, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirmation_id: confirmationId }),
  });
}

export function cancelGoogleLink(token: string, confirmationId: string): Promise<GoogleLinkDecision> {
  return authenticatedJson<GoogleLinkDecision>('/api/v1/auth/google/link/cancel', token, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ confirmation_id: confirmationId }),
  });
}
