'use client';

import { AUTH_URL } from './auth-core';

export const GOOGLE_CANCELLED_COPY = '已取消使用 Google 登入。';
export const GOOGLE_LINK_INTENT_KEY = 'lwc-google-link-intent';

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
};

export function createGoogleSupportReference(): string {
  try {
    const bytes = new Uint8Array(18);
    globalThis.crypto.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  } catch {
    return 'unavailable';
  }
}

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

function errorFromPayload(payload: unknown, status: number): GoogleAuthError {
  const record = isRecord(payload) ? payload : {};
  const message = typeof record.error === 'string' && record.error.trim()
    ? record.error.trim()
    : 'Unable to continue with Google sign-in.';
  const supportRef = typeof record.support_ref === 'string' && /^[A-Za-z0-9_-]{12,128}$/.test(record.support_ref)
    ? record.support_ref
    : 'unavailable';
  return new GoogleAuthError(message, status, supportRef);
}

export function startGoogleLogin(): void {
  clearGoogleLinkIntent();
  window.location.assign(`${AUTH_URL}/api/v1/auth/google/login/start`);
}

/**
 * The link endpoint is a navigation POST: the password never enters a URL or
 * browser storage, and the server owns the OAuth transaction and redirect.
 */
export async function beginGoogleLink(currentPassword: string): Promise<void> {
  if (!currentPassword) throw new GoogleAuthError('Current password is required.', 400);
  sessionStorage.setItem(GOOGLE_LINK_INTENT_KEY, '1');
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = `${AUTH_URL}/api/v1/auth/google/link/start`;
  form.hidden = true;
  const password = document.createElement('input');
  password.type = 'password';
  password.name = 'current_password';
  password.value = currentPassword;
  form.append(password);
  document.body.append(form);
  form.submit();
  window.setTimeout(() => form.remove(), 0);
}

export function hasGoogleLinkIntent(): boolean {
  try {
    return sessionStorage.getItem(GOOGLE_LINK_INTENT_KEY) === '1';
  } catch {
    return false;
  }
}

export function clearGoogleLinkIntent(): void {
  try {
    sessionStorage.removeItem(GOOGLE_LINK_INTENT_KEY);
  } catch {
    // Optional storage is unavailable in some privacy modes.
  }
}

async function authenticatedJson<T>(path: string, token: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${AUTH_URL}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      Authorization: `Bearer ${token}`,
      ...(init?.headers ?? {}),
    },
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) throw errorFromPayload(payload, response.status);
  return payload as T;
}

export async function readGoogleLinkCompletion(token: string): Promise<GoogleLinkCompletion> {
  const payload = await authenticatedJson<unknown>('/api/v1/auth/google/link/complete', token);
  if (!isRecord(payload)
    || payload.status !== 'confirmation_required'
    || typeof payload.confirmation_id !== 'string'
    || typeof payload.provider_email !== 'string'
    || typeof payload.current_email !== 'string') {
    throw new GoogleAuthError('Unable to link this Google account.', 502);
  }
  return {
    status: 'confirmation_required',
    confirmation_id: payload.confirmation_id,
    provider: typeof payload.provider === 'string' ? payload.provider : 'google',
    provider_email: payload.provider_email,
    provider_email_verified: payload.provider_email_verified === true,
    current_email: payload.current_email,
  };
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
