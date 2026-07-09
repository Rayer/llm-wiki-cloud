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

export const ACCESS_TOKEN_STORAGE_KEY = 'llm-wiki-access-token';

export function isAuthFailureStatus(status: number): boolean {
  return status === 401;
}

export function readStoredAccessToken(
  storage: Pick<Storage, 'getItem'> | null | undefined,
): string | null {
  if (!storage) return null;
  try {
    const value = storage.getItem(ACCESS_TOKEN_STORAGE_KEY);
    return value && value.trim() ? value.trim() : null;
  } catch {
    return null;
  }
}

export function writeStoredAccessToken(
  storage: Pick<Storage, 'setItem'> | null | undefined,
  token: string,
): void {
  if (!storage || !token.trim()) return;
  try {
    storage.setItem(ACCESS_TOKEN_STORAGE_KEY, token.trim());
  } catch {
    // ignore quota / private mode
  }
}

export function clearStoredAccessToken(
  storage: Pick<Storage, 'removeItem'> | null | undefined,
): void {
  if (!storage) return;
  try {
    storage.removeItem(ACCESS_TOKEN_STORAGE_KEY);
  } catch {
    // ignore
  }
}

export const DEMO_SESSION_STORAGE_KEY = 'llm-wiki-demo-session';

export function readStoredDemoSession(
  storage: Pick<Storage, 'getItem'> | null | undefined,
): boolean {
  if (!storage) return false;
  try {
    return storage.getItem(DEMO_SESSION_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

export function writeStoredDemoSession(
  storage: Pick<Storage, 'setItem' | 'removeItem'> | null | undefined,
  active: boolean,
): void {
  if (!storage) return;
  try {
    if (active) {
      storage.setItem(DEMO_SESSION_STORAGE_KEY, '1');
    } else {
      storage.removeItem(DEMO_SESSION_STORAGE_KEY);
    }
  } catch {
    // ignore quota / private mode
  }
}

export function clearStoredDemoSession(
  storage: Pick<Storage, 'removeItem'> | null | undefined,
): void {
  if (!storage) return;
  try {
    storage.removeItem(DEMO_SESSION_STORAGE_KEY);
  } catch {
    // ignore
  }
}
