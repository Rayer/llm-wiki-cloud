'use client';

export type AuthUser = {
  id: string;
  email: string;
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

  const user = isRecord(payload.user) ? payload.user : {};
  if (
    typeof user.id !== 'string' ||
    !user.id.trim() ||
    typeof user.email !== 'string' ||
    !user.email.trim()
  ) {
    throw new Error('Auth response did not include a valid user.');
  }

  return {
    access_token: payload.access_token,
    user: { id: user.id, email: user.email },
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

  return { access_token: payload.access_token, user: isRecord(payload.user) ? { id: String((payload.user as Record<string,unknown>).id ?? ''), email: String((payload.user as Record<string,unknown>).email ?? '') } : undefined };
}

