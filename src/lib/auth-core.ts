'use client';

export type AuthUser = {
  id: string;
  email: string;
  role?: string;
};

export type AuthResponse = {
  access_token: string;
  user: AuthUser;
};

export type RefreshResponse = {
  access_token: string;
  user?: AuthUser;
};

export const API_URL =
  process.env.NEXT_PUBLIC_API_URL ?? 'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app';

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function firstString(record: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
}

function normalizeUser(value: unknown): AuthUser | null {
  if (!isRecord(value)) return null;
  const id = firstString(value, ['id', 'user_id', 'userId']);
  const email = firstString(value, ['email']);
  const role = firstString(value, ['role']);
  if (!id || !email) return null;
  return role ? { id, email, role } : { id, email };
}

export function responseError(payload: unknown, fallback: string): string {
  if (!isRecord(payload)) return fallback;
  const message = payload.error ?? payload.message ?? payload.detail;
  return typeof message === 'string' && message.trim() ? message : fallback;
}

export function normalizeAuthResponse(payload: unknown): AuthResponse {
  if (
    !isRecord(payload) ||
    typeof payload.access_token !== 'string' ||
    !payload.access_token.trim()
  ) {
    throw new Error('Auth response did not include an access token.');
  }

  const user = normalizeUser(payload.user);
  if (!user) {
    throw new Error('Auth response did not include a valid user.');
  }

  return {
    access_token: payload.access_token,
    user,
  };
}

export function normalizeRefreshResponse(payload: unknown): RefreshResponse {
  if (
    !isRecord(payload) ||
    typeof payload.access_token !== 'string' ||
    !payload.access_token.trim()
  ) {
    throw new Error('Refresh response did not include an access token.');
  }

  const user = normalizeUser(payload.user);
  return user ? { access_token: payload.access_token, user } : { access_token: payload.access_token };
}
