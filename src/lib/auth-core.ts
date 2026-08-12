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

export const AUTH_URL = process.env.NEXT_PUBLIC_AUTH_URL ?? 'https://auth-dev.rayer.idv.tw';

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

export class RegistrationDisabledError extends Error {
  constructor() {
    super('registration disabled');
    this.name = 'RegistrationDisabledError';
  }
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

export function normalizeRegistrationResponse(payload: unknown): AuthResponse {
  if (!isRecord(payload) || typeof payload.token !== 'string' || !payload.token.trim()) {
    throw new Error('Registration response did not include an access token.');
  }

  const user = normalizeUser({ id: payload.user_id, email: payload.email });
  if (!user) {
    throw new Error('Registration response did not include a valid user.');
  }

  return {
    access_token: payload.token,
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

export const AUTH_FORCE_HOME_REDIRECT_KEY = 'llm-wiki-force-home-login';

export function setForceHomeRedirect(storage: Pick<Storage, 'getItem' | 'setItem'> | null | undefined): void {
  if (!storage) return;
  try {
    if (storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY) !== '1') {
      storage.setItem(AUTH_FORCE_HOME_REDIRECT_KEY, '1');
    }
  } catch {
    // ignore quota / private mode
  }
}

export function consumeForceHomeRedirect(
  storage: Pick<Storage, 'getItem' | 'removeItem'> | null | undefined,
): boolean {
  if (!storage) return false;
  try {
    const value = storage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY);
    if (value !== '1') return false;
    storage.removeItem(AUTH_FORCE_HOME_REDIRECT_KEY);
    return true;
  } catch {
    return false;
  }
}

export function clearForceHomeRedirect(storage: Pick<Storage, 'removeItem'> | null | undefined): void {
  if (!storage) return;
  try {
    storage.removeItem(AUTH_FORCE_HOME_REDIRECT_KEY);
  } catch {
    // ignore
  }
}

export const AUTH_USER_STORAGE_KEY = 'llm-wiki-auth-user';

export function readStoredAuthUser(
  storage: Pick<Storage, 'getItem'> | null | undefined,
): AuthUser | null {
  if (!storage) return null;
  try {
    const raw = storage.getItem(AUTH_USER_STORAGE_KEY);
    if (!raw?.trim()) return null;
    return normalizeUser(JSON.parse(raw) as unknown);
  } catch {
    return null;
  }
}

export function writeStoredAuthUser(
  storage: Pick<Storage, 'setItem'> | null | undefined,
  user: AuthUser,
): void {
  if (!storage || !user.id || !user.email) return;
  try {
    storage.setItem(AUTH_USER_STORAGE_KEY, JSON.stringify(user));
  } catch {
    // ignore quota / private mode
  }
}

export function persistAuthSession(
  storage: Pick<Storage, 'setItem' | 'removeItem'> | null | undefined,
  response: AuthResponse,
  demo: boolean,
): void {
  writeStoredAccessToken(storage, response.access_token);
  writeStoredAuthUser(storage, response.user);
  writeStoredDemoSession(storage, demo);
}

export function clearStoredAuthUser(
  storage: Pick<Storage, 'removeItem'> | null | undefined,
): void {
  if (!storage) return;
  try {
    storage.removeItem(AUTH_USER_STORAGE_KEY);
  } catch {
    // ignore
  }
}
