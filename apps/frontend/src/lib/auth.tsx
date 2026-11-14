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
  clearStoredAccessToken,
  clearStoredAuthUser,
  clearStoredDemoSession,
  isAuthFailureStatus,
  normalizeAuthResponse,
  normalizeRegistrationResponse,
  normalizeRefreshResponse,
  persistAuthSession,
  RegistrationDisabledError,
  readStoredAccessToken,
  readStoredAuthUser,
  readStoredDemoSession,
  responseError,
  writeStoredAccessToken,
  writeStoredAuthUser,
  setForceHomeRedirect,
  clearForceHomeRedirect,
  type AuthResponse,
  type AuthUser,
} from './auth-core';
import { configureApiAuth } from './api';
import { getRuntimeConfig } from './runtime-config';
import { recordRequestResponse } from './request-ids';
import { beginListViewSession, clearListViewSession } from './list-view-state';

type RefreshAccessTokenOptions = {
  /** When false, HTTP 401 does not clear session (used by hydrate soft-rotate). Default true. */
  clearOnAuthFailure?: boolean;
};

type HydratedSession = { accessToken: string; isCurrent: () => boolean };

type AuthContextValue = {
  accessToken: string | null;
  access_token: string | null;
  user: AuthUser | null;
  hydrated: boolean;
  isAuthenticated: boolean;
  isDemoSession: boolean;
  login: (email: string, password: string) => Promise<void>;
  loginAsDemo: () => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshAccessToken: (options?: RefreshAccessTokenOptions) => Promise<string | null>;
  /** Changes only when the authenticated session boundary changes, not on credential rotation. */
  sessionIdentityEpoch: number;
  sessionEpoch: number;
  /** Opaque, nonsecret per-login namespace used only for list view state. */
  listViewSessionId: string | null;
  listViewGeneration: number;
  isListViewSessionCurrent: (namespaceId: string, generation: number, ownerId: string) => boolean;
  /** Await the initial cookie refresh, never the cached token; call after hydration. */
  getHydratedSession: () => Promise<HydratedSession | null>;
};

const AuthContext = createContext<AuthContextValue | null>(null);

async function postAuth(path: string, body?: unknown): Promise<unknown> {
  const response = await fetch(`${getRuntimeConfig().auth_url}${path}`, {
    method: 'POST',
    credentials: 'include',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  recordRequestResponse(response);
  const payload: unknown = await response.json().catch(() => null);

  if (!response.ok) {
    if (response.status === 403 && path.includes('/register')) {
      throw new RegistrationDisabledError();
    }
    throw new Error(responseError(payload, `Auth request failed (${response.status})`));
  }

  return payload;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [accessToken, setAccessToken] = useState<string | null>(null);
  const [user, setUser] = useState<AuthUser | null>(null);
  const [isDemoSession, setIsDemoSession] = useState(false);
  const [hydrated, setHydrated] = useState(false);
  const [sessionIdentityEpoch, setSessionIdentityEpoch] = useState(0);
  const [sessionEpoch, setSessionEpoch] = useState(0);
  const [listViewSessionId, setListViewSessionId] = useState<string | null>(null);
  const [listViewGeneration, setListViewGeneration] = useState(0);
  const accessTokenRef = useRef<string | null>(null);
  const userRef = useRef<AuthUser | null>(null);
  const sessionIdentityEpochRef = useRef(0);
  const sessionEpochRef = useRef(0);
  const providerGenerationRef = useRef(0);
  const listViewSessionIdRef = useRef<string | null>(null);
  const listViewOwnerIdRef = useRef<string | null>(null);
  const listViewGenerationRef = useRef(0);
  const allowHydrationListViewResumeRef = useRef(true);
  const hydrationRefreshRef = useRef<Promise<{ accessToken: string; epoch: number } | null> | null>(null);

  useEffect(() => {
    accessTokenRef.current = accessToken;
  }, [accessToken]);

  useEffect(() => {
    userRef.current = user;
  }, [user]);

  const advanceSessionIdentity = useCallback(() => {
    sessionIdentityEpochRef.current += 1;
    setSessionIdentityEpoch(sessionIdentityEpochRef.current);
  }, []);

  const activateListViewSession = useCallback((ownerId: string, resume: boolean) => {
    if (resume && listViewOwnerIdRef.current === ownerId && listViewSessionIdRef.current) {
      return listViewSessionIdRef.current;
    }
    if (listViewSessionIdRef.current) {
      clearListViewSession(listViewOwnerIdRef.current, listViewSessionIdRef.current);
    }
    const namespaceId = beginListViewSession(ownerId, resume);
    listViewSessionIdRef.current = namespaceId;
    listViewOwnerIdRef.current = ownerId;
    listViewGenerationRef.current += 1;
    setListViewSessionId(namespaceId);
    setListViewGeneration(listViewGenerationRef.current);
    return namespaceId;
  }, []);

  const invalidateListViewSession = useCallback(() => {
    const ownerId = listViewOwnerIdRef.current;
    const namespaceId = listViewSessionIdRef.current;
    listViewGenerationRef.current += 1;
    listViewSessionIdRef.current = null;
    listViewOwnerIdRef.current = null;
    setListViewSessionId(null);
    setListViewGeneration(listViewGenerationRef.current);
    clearListViewSession(ownerId, namespaceId);
  }, []);

  const isListViewSessionCurrent = useCallback((namespaceId: string, generation: number, ownerId: string) => (
    listViewSessionIdRef.current === namespaceId
    && listViewGenerationRef.current === generation
    && listViewOwnerIdRef.current === ownerId
    && userRef.current?.id === ownerId
  ), []);

  const clearSession = useCallback(() => {
    allowHydrationListViewResumeRef.current = false;
    advanceSessionIdentity();
    invalidateListViewSession();
    sessionEpochRef.current += 1;
    setSessionEpoch(sessionEpochRef.current);
    setAccessToken(null);
    accessTokenRef.current = null;
    setUser(null);
    userRef.current = null;
    setIsDemoSession(false);
    clearStoredAccessToken(typeof window !== 'undefined' ? window.localStorage : null);
    clearStoredAuthUser(typeof window !== 'undefined' ? window.localStorage : null);
    clearStoredDemoSession(typeof window !== 'undefined' ? window.localStorage : null);
  }, [advanceSessionIdentity, invalidateListViewSession]);

  const clearSessionForUnauthorized = useCallback((failedToken: string, failedEpoch: number) => {
    if (accessTokenRef.current !== failedToken || sessionEpochRef.current !== failedEpoch) return;
    setForceHomeRedirect(typeof window !== 'undefined' ? window.localStorage : null);
    clearSession();
  }, [clearSession]);

  const applyAuthResponse = useCallback((result: AuthResponse, options?: { demo?: boolean }) => {
    const demo = options?.demo === true;
    allowHydrationListViewResumeRef.current = false;
    advanceSessionIdentity();
    activateListViewSession(result.user.id, false);
    sessionEpochRef.current += 1;
    setSessionEpoch(sessionEpochRef.current);
    setAccessToken(result.access_token);
    accessTokenRef.current = result.access_token;
    setUser(result.user);
    userRef.current = result.user;
    setIsDemoSession(demo);
    persistAuthSession(
      typeof window !== 'undefined' ? window.localStorage : null,
      result,
      demo,
    );
  }, [activateListViewSession, advanceSessionIdentity]);

  const refreshAccessToken = useCallback(async (options?: RefreshAccessTokenOptions) => {
    const clearOnAuthFailure = options?.clearOnAuthFailure !== false;
    const startedProviderGeneration = providerGenerationRef.current;
    const startedToken = accessTokenRef.current;
    const startedEpoch = sessionEpochRef.current;

    try {
      const response = await fetch(`${getRuntimeConfig().auth_url}/api/v1/auth/refresh`, {
        method: 'POST',
        credentials: 'include',
      });
      recordRequestResponse(response);
      const payload: unknown = await response.json().catch(() => null);
      if (providerGenerationRef.current !== startedProviderGeneration) return null;

      if (!response.ok) {
        if (
          isAuthFailureStatus(response.status)
          && clearOnAuthFailure
          && accessTokenRef.current === startedToken
          && sessionEpochRef.current === startedEpoch
        ) {
          clearSession();
        }
        return null;
      }

      const result = normalizeRefreshResponse(payload);
      if (accessTokenRef.current !== startedToken || sessionEpochRef.current !== startedEpoch) return null;
      sessionEpochRef.current += 1;
      setSessionEpoch(sessionEpochRef.current);
      setAccessToken(result.access_token);
      accessTokenRef.current = result.access_token;
      writeStoredAccessToken(
        typeof window !== 'undefined' ? window.localStorage : null,
        result.access_token,
      );
      if (result.user) {
        const previousUserId = userRef.current?.id ?? null;
        if (previousUserId !== result.user.id) {
          advanceSessionIdentity();
          const resume = allowHydrationListViewResumeRef.current && previousUserId === null;
          activateListViewSession(result.user.id, resume);
          if (resume) allowHydrationListViewResumeRef.current = false;
        } else if (!listViewSessionIdRef.current) {
          const resume = allowHydrationListViewResumeRef.current;
          activateListViewSession(result.user.id, resume);
          if (resume) allowHydrationListViewResumeRef.current = false;
        }
        setUser(result.user);
        userRef.current = result.user;
        writeStoredAuthUser(
          typeof window !== 'undefined' ? window.localStorage : null,
          result.user,
        );
      }
      return { accessToken: result.access_token, epoch: sessionEpochRef.current };
    } catch {
      // Network / parse failures: keep session
      return null;
    }
  }, [activateListViewSession, advanceSessionIdentity, clearSession]);

  const refreshAccessTokenForContext = useCallback(async (options?: RefreshAccessTokenOptions) => (
    (await refreshAccessToken(options))?.accessToken ?? null
  ), [refreshAccessToken]);

  useEffect(() => {
    configureApiAuth({
      getAccessToken: () => accessTokenRef.current,
      getSessionEpoch: () => sessionEpochRef.current,
      // API owns final unauthorized handling after its token-identity check.
      refreshAccessToken: () => refreshAccessToken({ clearOnAuthFailure: false }),
      onUnauthorized: clearSessionForUnauthorized,
    });

    return () => {
      providerGenerationRef.current += 1;
      configureApiAuth({
        getAccessToken: () => null,
        refreshAccessToken: async () => null,
        onUnauthorized: () => undefined,
      });
    };
  }, [clearSession, clearSessionForUnauthorized, refreshAccessToken]);

  const getHydratedSession = useCallback(async (): Promise<HydratedSession | null> => {
    const generation = providerGenerationRef.current;
    const result = await hydrationRefreshRef.current;
    if (!result) return null;
    return {
      accessToken: result.accessToken,
      isCurrent: () => providerGenerationRef.current === generation
        && sessionEpochRef.current === result.epoch
        && accessTokenRef.current === result.accessToken,
    };
  }, []);

  useEffect(() => {
    let cancelled = false;

    async function hydrateFromRefreshCookie() {
      const storage = typeof window !== 'undefined' ? window.localStorage : null;
      const stored = readStoredAccessToken(storage);

      // Approach B (LWC-116): restore stored token immediately so reload never depends on
      // refresh cookie success. Soft-rotate in background without clearSession on 401 —
      // apiFetch still handles real unauthorized after business 401.
      // Also restore cached user: JWT has only sub; refresh often omits user / fails for demo.
      if (stored) {
        if (!cancelled) {
          sessionEpochRef.current += 1;
          setSessionEpoch(sessionEpochRef.current);
          advanceSessionIdentity();
          setAccessToken(stored);
          accessTokenRef.current = stored;
          const storedUser = readStoredAuthUser(storage);
          setUser(storedUser);
          userRef.current = storedUser;
          if (storedUser) activateListViewSession(storedUser.id, true);
          setIsDemoSession(readStoredDemoSession(storage));
          setHydrated(true);
        }
        // StrictMode discards its first effect before rotating the single-use cookie.
        hydrationRefreshRef.current = Promise.resolve().then(() => (
          cancelled ? null : refreshAccessToken({ clearOnAuthFailure: false })
        ));
        void hydrationRefreshRef.current.finally(() => {
          if (!cancelled) allowHydrationListViewResumeRef.current = false;
        });
        return;
      }

      hydrationRefreshRef.current = Promise.resolve().then(() => (cancelled ? null : refreshAccessToken()));
      const refreshed = await hydrationRefreshRef.current;
      if (!cancelled) {
        void refreshed;
        allowHydrationListViewResumeRef.current = false;
        setHydrated(true);
      }
    }

    void hydrateFromRefreshCookie();

    return () => {
      cancelled = true;
    };
  }, [activateListViewSession, advanceSessionIdentity, refreshAccessToken]);

  const login = useCallback(async (email: string, password: string) => {
    const payload = await postAuth('/api/v1/auth/login', { email, password });
    applyAuthResponse(normalizeAuthResponse(payload), { demo: false });
  }, [applyAuthResponse]);

  const loginAsDemo = useCallback(async () => {
    const payload = await postAuth('/api/v1/auth/demo');
    applyAuthResponse(normalizeAuthResponse(payload), { demo: true });
  }, [applyAuthResponse]);

  const register = useCallback(async (email: string, password: string) => {
    const payload = await postAuth('/api/v1/auth/register', { email, password });
    applyAuthResponse(normalizeRegistrationResponse(payload), { demo: false });
  }, [applyAuthResponse]);

  const logout = useCallback(async () => {
    try {
      await postAuth('/api/v1/auth/logout');
    } finally {
      clearForceHomeRedirect(typeof window !== 'undefined' ? window.localStorage : null);
      clearSession();
    }
  }, [clearSession]);

  const value = useMemo<AuthContextValue>(() => ({
    accessToken,
    access_token: accessToken,
    user,
    hydrated,
    isAuthenticated: Boolean(accessToken),
    isDemoSession,
    login,
    loginAsDemo,
    register,
    logout,
    refreshAccessToken: refreshAccessTokenForContext,
    sessionIdentityEpoch,
    sessionEpoch,
    listViewSessionId,
    listViewGeneration,
    isListViewSessionCurrent,
    getHydratedSession,
  }), [accessToken, getHydratedSession, hydrated, isDemoSession, isListViewSessionCurrent, listViewGeneration, listViewSessionId, login, loginAsDemo, logout, refreshAccessTokenForContext, register, sessionEpoch, sessionIdentityEpoch, user]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error('useAuth must be used within AuthProvider.');
  return value;
}

export type { AuthUser };
export { normalizeAuthResponse, normalizeRefreshResponse };
