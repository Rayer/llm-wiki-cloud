'use client';

export type AuthUser = {
  id: string;
  email: string;
};

export type LoginResponse = {
  token: string;
  user: AuthUser;
};

export const AUTH_TOKEN_KEY = 'llm-wiki-auth-token';
export const AUTH_USER_KEY = 'llm-wiki-auth-user';
export const API_URL =
  process.env.NEXT_PUBLIC_API_URL ?? 'https://llm-wiki-bff-dev.rayer.idv.tw';

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function responseError(payload: unknown, fallback: string): string {
  if (!isRecord(payload)) return fallback;
  const message = payload.error ?? payload.message ?? payload.detail;
  return typeof message === 'string' && message.trim() ? message : fallback;
}

export function normalizeLoginResponse(payload: unknown): LoginResponse {
  if (!isRecord(payload) || typeof payload.token !== 'string' || !payload.token.trim()) {
    throw new Error('Login response did not include a token.');
  }

  const user = isRecord(payload.user) ? payload.user : {};
  if (
    typeof user.id !== 'string' ||
    !user.id.trim() ||
    typeof user.email !== 'string' ||
    !user.email.trim()
  ) {
    throw new Error('Login response did not include a valid user.');
  }

  return {
    token: payload.token,
    user: { id: user.id, email: user.email },
  };
}

export function getStoredToken(): string | null {
  if (typeof window === 'undefined') return null;
  return window.localStorage.getItem(AUTH_TOKEN_KEY);
}

export function getStoredUser(): AuthUser | null {
  if (typeof window === 'undefined') return null;
  const value = window.localStorage.getItem(AUTH_USER_KEY);
  if (!value) return null;

  try {
    const parsed: unknown = JSON.parse(value);
    const user = isRecord(parsed) ? parsed : {};
    if (typeof user.id !== 'string' || typeof user.email !== 'string') return null;
    return { id: user.id, email: user.email };
  } catch {
    return null;
  }
}

export async function login(email: string, password: string): Promise<LoginResponse> {
  const response = await fetch(`${API_URL}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  });
  const payload: unknown = await response.json().catch(() => null);

  if (!response.ok) {
    throw new Error(responseError(payload, `Login failed (${response.status})`));
  }

  const result = normalizeLoginResponse(payload);
  window.localStorage.setItem(AUTH_TOKEN_KEY, result.token);
  window.localStorage.setItem(AUTH_USER_KEY, JSON.stringify(result.user));
  return result;
}

export function logout(): void {
  if (typeof window === 'undefined') return;
  window.localStorage.removeItem(AUTH_TOKEN_KEY);
  window.localStorage.removeItem(AUTH_USER_KEY);
}

export function getApiError(payload: unknown, fallback: string): string {
  return responseError(payload, fallback);
}
