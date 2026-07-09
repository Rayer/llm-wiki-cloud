'use client';

import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import {
  API_URL,
  clearStoredAccessToken,
  isAuthFailureStatus,
  normalizeAuthResponse,
  normalizeRefreshResponse,
  readStoredAccessToken,
  responseError,
  writeStoredAccessToken,
  type AuthResponse,
  type AuthUser,
} from './auth-core';
import { configureApiAuth } from './api';

type AuthContextValue = {
  accessToken: string | null;
  access_token: string | null;
  user: AuthUser | null;
  hydrated: boolean;
  isAuthenticated: boolean;
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshAccessToken: () => Promise<string | null>;
};

const AuthContext = createContext<AuthContextValue | null>(null);

async function postAuth(path: string, body?: unknown): Promise<unknown> {
  const response = await fetch(`${API_URL}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload: unknown = await response.json().catch(() => null);

  if (!response.ok) {
    throw new Error(responseError(payload, `Auth request failed (${response.status})`));
  }

  return payload;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [accessToken, setAccessToken] = useState<string | null>(null);
  const [user, setUser] = useState<AuthUser | null>(null);
  const [hydrated, setHydrated] = useState(false);
  const accessTokenRef = useRef<string | null>(null);

  useEffect(() => {
    accessTokenRef.current = accessToken;
  }, [accessToken]);

  const clearSession = useCallback(() => {
    setAccessToken(null);
    accessTokenRef.current = null;
    setUser(null);
    clearStoredAccessToken(typeof window !== 'undefined' ? window.localStorage : null);
  }, []);

  const applyAuthResponse = useCallback((result: AuthResponse) => {
    setAccessToken(result.access_token);
    accessTokenRef.current = result.access_token;
    setUser(result.user);
    writeStoredAccessToken(
      typeof window !== 'undefined' ? window.localStorage : null,
      result.access_token,
    );
  }, []);

  const refreshAccessToken = useCallback(async () => {
    try {
      const response = await fetch(`${API_URL}/api/v1/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
      });
      const payload: unknown = await response.json().catch(() => null);

      if (!response.ok) {
        if (isAuthFailureStatus(response.status)) {
          clearSession();
        }
        return null;
      }

      const result = normalizeRefreshResponse(payload);
      setAccessToken(result.access_token);
      accessTokenRef.current = result.access_token;
      writeStoredAccessToken(
        typeof window !== 'undefined' ? window.localStorage : null,
        result.access_token,
      );
      if (result.user) {
        setUser(result.user);
      }
      return result.access_token;
    } catch {
      // Network / parse failures: keep session
      return null;
    }
  }, [clearSession]);

  useEffect(() => {
    configureApiAuth({
      getAccessToken: () => accessTokenRef.current,
      refreshAccessToken,
      onUnauthorized: clearSession,
    });

    return () => {
      configureApiAuth({
        getAccessToken: () => null,
        refreshAccessToken: async () => null,
        onUnauthorized: () => undefined,
      });
    };
  }, [clearSession, refreshAccessToken]);

  useEffect(() => {
    let cancelled = false;

    async function hydrateFromRefreshCookie() {
      const stored = readStoredAccessToken(
        typeof window !== 'undefined' ? window.localStorage : null,
      );
      if (stored && !cancelled) {
        setAccessToken(stored);
        accessTokenRef.current = stored;
      }

      const refreshed = await refreshAccessToken();
      if (!cancelled) {
        // refreshAccessToken already cleared on auth failure
        // transient failure keeps stored/in-memory token
        void refreshed;
        setHydrated(true);
      }
    }

    void hydrateFromRefreshCookie();

    return () => {
      cancelled = true;
    };
  }, [refreshAccessToken]);

  const login = useCallback(async (email: string, password: string) => {
    const payload = await postAuth('/api/v1/auth/login', { email, password });
    applyAuthResponse(normalizeAuthResponse(payload));
  }, [applyAuthResponse]);

  const register = useCallback(async (email: string, password: string) => {
    const payload = await postAuth('/api/v1/auth/register', { email, password });
    applyAuthResponse(normalizeAuthResponse(payload));
  }, [applyAuthResponse]);

  const logout = useCallback(async () => {
    try {
      await postAuth('/api/v1/auth/logout');
    } finally {
      clearSession();
    }
  }, [clearSession]);

  const value = useMemo<AuthContextValue>(() => ({
    accessToken,
    access_token: accessToken,
    user,
    hydrated,
    isAuthenticated: Boolean(accessToken),
    login,
    register,
    logout,
    refreshAccessToken,
  }), [accessToken, hydrated, login, logout, refreshAccessToken, register, user]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth must be used within AuthProvider.');
  return value;
}

export type { AuthUser };
export { normalizeAuthResponse, normalizeRefreshResponse };
