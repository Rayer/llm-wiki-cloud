import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createRoot } from 'react-dom/client';
import { act } from 'react';
import { useEffect } from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { AuthProvider } from '@/lib/auth';
import {
  AUTH_FORCE_HOME_REDIRECT_KEY,
  clearForceHomeRedirect,
  setForceHomeRedirect,
  writeStoredAccessToken,
  writeStoredAuthUser,
} from '@/lib/auth-core';
import { clearPublicConfigCache } from '@/lib/api';
import { uploadRawFile, ApiError } from '@/lib/api';
import { NavigationBlockerProvider } from '@/components/NavigationBlocker';
import { LoginModal } from '@/components/LoginModal';
import { useWorkspace, WorkspaceProvider } from '@/components/WorkspaceProvider';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

const TEST_USER = {
  id: 'u-1',
  email: 'test@llm-wiki.dev',
};

const navigation = vi.hoisted(() => ({
  pathname: '/projects',
  replace: vi.fn(),
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({ replace: navigation.replace }),
}));

type FetchResponse = { status: number; body?: unknown };
type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};
type QueuedResponse = FetchResponse | Promise<FetchResponse>;
type RouteQueue = {
  login: QueuedResponse[];
  refresh: QueuedResponse[];
  logout: QueuedResponse[];
  projects: QueuedResponse[];
  publicConfig: QueuedResponse[];
};

const queue: RouteQueue = {
  login: [],
  refresh: [],
  logout: [],
  projects: [],
  publicConfig: [],
};

function requestUrl(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

function routeFrom(input: Parameters<typeof fetch>[0]) {
  const url = requestUrl(input);
  const path = new URL(url).pathname;

  if (path.endsWith('/api/v1/auth/login')) return 'login';
  if (path.endsWith('/api/v1/auth/refresh')) return 'refresh';
  if (path.endsWith('/api/v1/auth/logout')) return 'logout';
  if (path.endsWith('/api/v1/public/config')) return 'publicConfig';
  if (path.includes('/api/v1/projects')) return 'projects';
  return null;
}

function defaultResponse(route: keyof RouteQueue): FetchResponse {
  if (route === 'refresh') {
    return { status: 500, body: { error: 'mocked refresh failure' } };
  }

  if (route === 'login') {
    return {
      status: 401,
      body: { error: 'mocked login failure' },
    };
  }

  if (route === 'publicConfig') {
    return { status: 200, body: { registration_enabled: false } };
  }

  return { status: 200, body: route === 'projects' ? [] : {} };
}

function buildResponse({ status, body }: FetchResponse) {
  const text = body === undefined ? '' : JSON.stringify(body);
  return new Response(text, {
    status,
    headers: text ? { 'Content-Type': 'application/json' } : undefined,
  });
}

async function queueResponse(route: keyof RouteQueue) {
  const response = queue[route].shift();
  return buildResponse(await (response ?? defaultResponse(route)));
}

function setQueue(route: keyof RouteQueue, responses: QueuedResponse[]) {
  queue[route] = responses.slice();
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
}

function clearQueues() {
  queue.login = [];
  queue.refresh = [];
  queue.logout = [];
  queue.projects = [];
  queue.publicConfig = [];
}

const workspaceRef: { current: ReturnType<typeof useWorkspace> | null } = {
  current: null,
};

function WorkspaceProbe() {
  const workspace = useWorkspace();

  useEffect(() => {
    workspaceRef.current = workspace;
  }, [workspace]);

  return null;
}

let container: HTMLDivElement | null = null;
let root: ReturnType<typeof createRoot> | null = null;

async function renderProviders({ withLoginModal = false }: { withLoginModal?: boolean } = {}) {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <AuthProvider>
        <NavigationBlockerProvider>
          <WorkspaceProvider>
            <WorkspaceProbe />
            {withLoginModal ? <LoginModal /> : null}
          </WorkspaceProvider>
        </NavigationBlockerProvider>
      </AuthProvider>,
  );
  });
}

async function unmountProviders() {
  await act(async () => {
    root?.unmount();
    if (container) {
      container.remove();
    }
    container = null;
    root = null;
  });
}

async function actSignIn(email: string, password: string) {
  await act(async () => {
    await workspaceRef.current!.signIn(email, password);
  });
}

async function actSignInAsDemo(email: string, password: string) {
  await act(async () => {
    await workspaceRef.current!.signInAsDemo(email, password);
  });
}

async function actRefreshProjects() {
  await act(async () => {
    await workspaceRef.current!.refreshProjects().catch(() => undefined);
  });
}

async function startRefreshProjects() {
  let request!: Promise<void>;
  await act(async () => {
    request = workspaceRef.current!.refreshProjects().catch(() => undefined);
  });
  return { request };
}

async function actSignOut() {
  await waitFor(() => expect(workspaceRef.current).not.toBeNull());
  await act(async () => {
    await workspaceRef.current!.signOut();
  });
}

afterEach(async () => {
  await unmountProviders();
  vi.restoreAllMocks();
  workspaceRef.current = null;
});

async function waitUntil(condition: () => boolean, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (condition()) {
      return;
    }
    await new Promise((resolve) => {
      setTimeout(resolve, 10);
    });
  }
  throw new Error('Timed out waiting for expected condition.');
}

async function resolveDeferred<T>(deferredValue: Deferred<T>, value: T) {
  await act(async () => {
    deferredValue.resolve(value);
    await deferredValue.promise;
  });
}

function getForceHomeRedirect() {
  return window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY);
}

function loginBody(token = 'login-token') {
  return {
    access_token: token,
    user: TEST_USER,
  };
}

function hydrateBody(token = 'seed-token') {
  return {
    access_token: token,
    user: TEST_USER,
  };
}

function waitForHydrated() {
  return waitUntil(() => workspaceRef.current?.hydrated === true);
}

beforeEach(() => {
  navigation.pathname = '/projects';
  window.history.pushState({}, '', navigation.pathname);
  navigation.replace.mockClear();
  clearQueues();
  clearPublicConfigCache();
  clearForceHomeRedirect(window.localStorage);
  window.localStorage.clear();
  vi.spyOn(window, 'confirm').mockReturnValue(true);

  global.fetch = vi.fn(async (input) => {
    const route = routeFrom(input);
    if (!route) {
      throw new Error(`Unhandled fetch route: ${input}`);
    }
    return queueResponse(route);
  });
});

describe('LWC-221 auth redirect behavior', () => {
  it('does not let a newer same-session project load be overwritten by an older success', async () => {
    const olderProjects = deferred<FetchResponse>();
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('session-token') }]);
    setQueue('projects', [olderProjects.promise, {
      status: 200,
      body: [{ id: 'new-project', name: 'New Project' }],
    }]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await startRefreshProjects();
    await waitUntil(() => workspaceRef.current?.currentProject?.id === 'new-project');
    await resolveDeferred(olderProjects, {
      status: 200,
      body: [{ id: 'old-project', name: 'Old Project' }],
    });

    expect(workspaceRef.current?.projects).toEqual([{ id: 'new-project', name: 'New Project' }]);
    expect(workspaceRef.current?.currentProject?.id).toBe('new-project');
  });

  it('does not let an older same-session project failure clear newer state or error', async () => {
    const olderProjects = deferred<FetchResponse>();
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('session-token') }]);
    setQueue('projects', [olderProjects.promise, {
      status: 200,
      body: [{ id: 'new-project', name: 'New Project' }],
    }]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await startRefreshProjects();
    await waitUntil(() => workspaceRef.current?.currentProject?.id === 'new-project');
    await resolveDeferred(olderProjects, { status: 500, body: { error: 'old failure' } });

    expect(workspaceRef.current?.projects).toEqual([{ id: 'new-project', name: 'New Project' }]);
    expect(workspaceRef.current?.projectsError).toBe('');
  });

  it('keeps loading true when an older same-session load settles while newer load is pending', async () => {
    const olderProjects = deferred<FetchResponse>();
    const newerProjects = deferred<FetchResponse>();
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('session-token') }]);
    setQueue('projects', [olderProjects.promise, newerProjects.promise]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await startRefreshProjects();
    await waitUntil(() => workspaceRef.current?.projectsLoading === true);
    await resolveDeferred(olderProjects, {
      status: 200,
      body: [{ id: 'old-project', name: 'Old Project' }],
    });

    expect(workspaceRef.current?.projectsLoading).toBe(true);
    expect(workspaceRef.current?.projects).toEqual([]);

    await resolveDeferred(newerProjects, {
      status: 200,
      body: [{ id: 'new-project', name: 'New Project' }],
    });
    expect(workspaceRef.current?.projectsLoading).toBe(false);
    expect(workspaceRef.current?.currentProject?.id).toBe('new-project');
  });

  it('clears project loading synchronously on logout and ignores the stale load settle', async () => {
    const pendingProjects = deferred<FetchResponse>();
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('session-token') }]);
    setQueue('logout', [{ status: 200, body: {} }]);
    setQueue('projects', [pendingProjects.promise]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.projectsLoading === true);
    await actSignOut();

    expect(workspaceRef.current?.projectsLoading).toBe(false);
    await resolveDeferred(pendingProjects, {
      status: 200,
      body: [{ id: 'stale-project', name: 'Stale Project' }],
    });
    expect(workspaceRef.current?.projects).toEqual([]);
    expect(workspaceRef.current?.currentProject).toBeNull();
    expect(workspaceRef.current?.projectsError).toBe('');
    expect(workspaceRef.current?.projectsLoading).toBe(false);
  });

  it('ignores a stale refresh success after its AuthProvider unmounts', async () => {
    const staleRefresh = deferred<FetchResponse>();
    setQueue('refresh', [staleRefresh.promise]);

    await renderProviders();
    await unmountProviders();
    writeStoredAccessToken(window.localStorage, 'provider-b-token');
    writeStoredAuthUser(window.localStorage, { id: 'provider-b', email: 'b@example.com' });

    await resolveDeferred(staleRefresh, {
      status: 200,
      body: {
        access_token: 'provider-a-late-token',
        user: { id: 'provider-a', email: 'a@example.com' },
      },
    });

    expect(window.localStorage.getItem('llm-wiki-access-token')).toBe('provider-b-token');
    expect(window.localStorage.getItem('llm-wiki-auth-user')).toBe(JSON.stringify({
      id: 'provider-b',
      email: 'b@example.com',
    }));
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
  });

  it('ignores a stale refresh success after a newer login with identical token bytes', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRefresh = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [{ status: 200, body: hydrateBody('seed-token') }, staleRefresh.promise]);
    setQueue('projects', [initialProjects.promise, { status: 401, body: { error: 'expired' } }, { status: 200, body: [] }]);
    setQueue('login', [{ status: 200, body: loginBody('login-token') }]);

    await renderProviders({ withLoginModal: true });
    await waitForHydrated();
    await act(async () => {
      initialProjects.resolve({ status: 200, body: [] });
    });

    const { request: refreshProjects } = await startRefreshProjects();
    await actSignIn('user@example.com', 'password');
    await act(async () => {
      staleRefresh.resolve({
        status: 200,
        body: { access_token: 'login-token', user: { id: 'old-user', email: 'old@example.com' } },
      });
      await refreshProjects;
    });

    expect(workspaceRef.current?.token).toBe('login-token');
    expect(workspaceRef.current?.user).toEqual(TEST_USER);
    expect(window.localStorage.getItem('llm-wiki-access-token')).toBe('login-token');
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('does not publish old project hydration success after a newer login', async () => {
    const oldProjects = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'old-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [{ status: 200, body: hydrateBody('old-token') }]);
    setQueue('projects', [oldProjects.promise, {
      status: 200,
      body: [],
    }, {
      status: 200,
      body: [{ id: 'new-project', name: 'New Project' }],
    }]);
    setQueue('login', [{ status: 200, body: loginBody('new-token') }]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.currentProject?.id === 'new-project');

    await resolveDeferred(oldProjects, { status: 200, body: [{ id: 'old-project', name: 'Old Project' }] });

    expect(workspaceRef.current?.projects).toEqual([{ id: 'new-project', name: 'New Project' }]);
    expect(workspaceRef.current?.currentProject?.id).toBe('new-project');
    expect(window.localStorage.getItem('llm-wiki-last-project')).toBe('new-project');
  });

  it('does not publish old project hydration failure after a newer login', async () => {
    const oldProjects = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'old-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [{ status: 200, body: hydrateBody('old-token') }]);
    setQueue('projects', [oldProjects.promise, {
      status: 200,
      body: [],
    }, {
      status: 200,
      body: [{ id: 'new-project', name: 'New Project' }],
    }]);
    setQueue('login', [{ status: 200, body: loginBody('new-token') }]);

    await renderProviders();
    await waitForHydrated();
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.currentProject?.id === 'new-project');

    await resolveDeferred(oldProjects, { status: 500, body: { error: 'old failure' } });

    expect(workspaceRef.current?.projectsError).toBe('');
    expect(workspaceRef.current?.projects).toEqual([{ id: 'new-project', name: 'New Project' }]);
    expect(window.localStorage.getItem('llm-wiki-last-project')).toBe('new-project');
  });

  it('ignores an anonymous null-token refresh success after explicit logout', async () => {
    const staleRefresh = deferred<FetchResponse>();
    setQueue('refresh', [staleRefresh.promise]);
    setQueue('logout', [{ status: 200, body: {} }]);

    await renderProviders();
    await actSignOut();
    await act(async () => {
      staleRefresh.resolve({ status: 200, body: hydrateBody('stale-anonymous-token') });
    });
    await waitForHydrated();

    expect(workspaceRef.current?.token).toBeNull();
    expect(workspaceRef.current?.user).toBeNull();
    expect(window.localStorage.getItem('llm-wiki-access-token')).toBeNull();
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('ignores a stale final fetch 401 after a newer login', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRetry = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('fresh-token') },
    ]);
    setQueue('projects', [
      initialProjects.promise,
      { status: 401, body: { error: 'expired' } },
      staleRetry.promise,
      { status: 200, body: [] },
    ]);
    setQueue('login', [{ status: 200, body: loginBody('login-token') }]);

    await renderProviders({ withLoginModal: true });
    await waitForHydrated();
    await act(async () => {
      initialProjects.resolve({ status: 200, body: [] });
    });

    const { request: refreshProjects } = await startRefreshProjects();
    await actSignIn('user@example.com', 'password');
    await act(async () => {
      staleRetry.resolve({ status: 401, body: { error: 'expired-again' } });
      await refreshProjects;
    });

    expect(workspaceRef.current?.token).toBe('login-token');
    expect(window.localStorage.getItem('llm-wiki-access-token')).toBe('login-token');
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('ignores a stale final fetch 401 after a newer login even when the token bytes are identical', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRetry = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('same-token') },
    ]);
    setQueue('projects', [
      initialProjects.promise,
      { status: 401, body: { error: 'expired' } },
      staleRetry.promise,
    ]);
    setQueue('login', [{ status: 200, body: loginBody('same-token') }]);

    await renderProviders({ withLoginModal: true });
    await waitForHydrated();
    await act(async () => {
      initialProjects.resolve({ status: 200, body: [] });
    });

    const { request: refreshProjects } = await startRefreshProjects();
    await actSignIn('user@example.com', 'password');
    await act(async () => {
      staleRetry.resolve({ status: 401, body: { error: 'expired-again' } });
      await refreshProjects;
    });

    expect(workspaceRef.current?.token).toBe('same-token');
    expect(workspaceRef.current?.user).toEqual(TEST_USER);
    expect(window.localStorage.getItem('llm-wiki-access-token')).toBe('same-token');
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('ignores a stale refresh 401 after explicit logout and later login', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRefresh = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [{ status: 200, body: hydrateBody('seed-token') }, staleRefresh.promise]);
    setQueue('projects', [initialProjects.promise, { status: 401, body: { error: 'expired' } }]);
    setQueue('logout', [{ status: 200, body: {} }]);

    await renderProviders();
    await waitForHydrated();
    await act(async () => {
      initialProjects.resolve({ status: 200, body: [] });
    });
    const { request: refreshProjects } = await startRefreshProjects();

    await actSignOut();
    await act(async () => {
      staleRefresh.resolve({ status: 401, body: { error: 'expired' } });
      await refreshProjects;
    });

    setQueue('login', [{ status: 200, body: loginBody('after-logout-token') }]);
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.token === 'after-logout-token');

    expect(workspaceRef.current?.token).toBe('after-logout-token');
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('ignores a stale final fetch 401 after explicit logout and later login', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRetry = deferred<FetchResponse>();
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('fresh-token') },
    ]);
    setQueue('projects', [initialProjects.promise, { status: 401, body: { error: 'expired' } }, staleRetry.promise]);
    setQueue('logout', [{ status: 200, body: {} }]);

    await renderProviders();
    await waitForHydrated();
    await act(async () => {
      initialProjects.resolve({ status: 200, body: [] });
    });
    const { request: refreshProjects } = await startRefreshProjects();

    await actSignOut();
    await act(async () => {
      staleRetry.resolve({ status: 401, body: { error: 'expired-again' } });
      await refreshProjects;
    });

    setQueue('login', [{ status: 200, body: loginBody('after-logout-token') }]);
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.token === 'after-logout-token');

    expect(workspaceRef.current?.token).toBe('after-logout-token');
    expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('preserves a newer login when a raw XHR retry completes with 401', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRetry = deferred<FetchResponse>();
    const originalXHR = globalThis.XMLHttpRequest;
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    window.localStorage.setItem('llm-wiki-last-project', 'project-1');
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('fresh-token') },
    ]);
    setQueue('projects', [
      initialProjects.promise,
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
    ]);
    setQueue('login', [{ status: 200, body: loginBody('login-token') }]);
    const requests: { authorization?: string }[] = [];
    class DeferredUploadXHR {
      status = 0;
      responseText = '';
      upload = {};
      onload = () => undefined;
      onerror = () => undefined;
      ontimeout = () => undefined;
      onabort = () => undefined;
      open() {}
      setRequestHeader(name: string, value: string) {
        if (name === 'Authorization') requests.push({ authorization: value });
      }
      send() {
        if (requests.length === 1) {
          this.status = 401;
          this.responseText = JSON.stringify({ error: 'expired' });
          this.onload();
          return;
        }
        void staleRetry.promise.then(({ status, body }) => {
          this.status = status;
          this.responseText = JSON.stringify(body);
          this.onload();
        });
      }
    }

    try {
      globalThis.XMLHttpRequest = DeferredUploadXHR as unknown as typeof XMLHttpRequest;
      await renderProviders();
      await waitForHydrated();
      await act(async () => {
        initialProjects.resolve({ status: 200, body: [{ id: 'project-1', name: 'Project 1' }] });
      });

      let uploadError: unknown;
      let uploadErrorPromise!: Promise<unknown>;
      await act(async () => {
        const upload = uploadRawFile(new File(['x'], 'retry.md'));
        uploadErrorPromise = upload.catch((error) => error);
        await waitUntil(() => requests.map(({ authorization }) => authorization).join(',') === 'Bearer seed-token,Bearer fresh-token');
      });
      await actSignIn('user@example.com', 'password');
      await act(async () => {
        staleRetry.resolve({ status: 401, body: { error: 'expired-again' } });
        uploadError = await uploadErrorPromise;
      });
      expect(uploadError).toBeInstanceOf(ApiError);
      expect(uploadError).toMatchObject({ status: 401 });
      expect(requests.map(({ authorization }) => authorization)).toEqual(['Bearer seed-token', 'Bearer fresh-token']);
      expect(workspaceRef.current?.token).toBe('login-token');
      expect(window.localStorage.getItem('llm-wiki-access-token')).toBe('login-token');
      expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();
      expect(navigation.replace).toHaveBeenCalledTimes(0);
    } finally {
      globalThis.XMLHttpRequest = originalXHR;
    }
  });

  it('ignores a stale raw XHR 401 after explicit logout and later login', async () => {
    const initialProjects = deferred<FetchResponse>();
    const staleRetry = deferred<FetchResponse>();
    const originalXHR = globalThis.XMLHttpRequest;
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    window.localStorage.setItem('llm-wiki-last-project', 'project-1');
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('fresh-token') },
    ]);
    setQueue('projects', [
      initialProjects.promise,
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
      { status: 200, body: [{ id: 'project-1', name: 'Project 1' }] },
    ]);
    setQueue('logout', [{ status: 200, body: {} }]);
    const requests: { authorization?: string }[] = [];
    class DeferredUploadXHR {
      status = 0;
      responseText = '';
      upload = {};
      onload = () => undefined;
      onerror = () => undefined;
      ontimeout = () => undefined;
      onabort = () => undefined;
      open() {}
      setRequestHeader(name: string, value: string) {
        if (name === 'Authorization') requests.push({ authorization: value });
      }
      send() {
        if (requests.length === 1) {
          this.status = 401;
          this.responseText = JSON.stringify({ error: 'expired' });
          this.onload();
          return;
        }
        void staleRetry.promise.then(({ status, body }) => {
          this.status = status;
          this.responseText = JSON.stringify(body);
          this.onload();
        });
      }
    }

    try {
      globalThis.XMLHttpRequest = DeferredUploadXHR as unknown as typeof XMLHttpRequest;
      await renderProviders();
      await waitForHydrated();
      await act(async () => {
        initialProjects.resolve({ status: 200, body: [{ id: 'project-1', name: 'Project 1' }] });
      });

      let uploadError: unknown;
      let uploadErrorPromise!: Promise<unknown>;
      await act(async () => {
        const upload = uploadRawFile(new File(['x'], 'logout-retry.md'));
        uploadErrorPromise = upload.catch((error) => error);
        await waitUntil(() => requests.map(({ authorization }) => authorization).join(',') === 'Bearer seed-token,Bearer fresh-token');
      });
      await actSignOut();
      await act(async () => {
        staleRetry.resolve({ status: 401, body: { error: 'expired-again' } });
        uploadError = await uploadErrorPromise;
      });
      expect(uploadError).toBeInstanceOf(ApiError);
      expect(uploadError).toMatchObject({ status: 401 });
      expect(requests.map(({ authorization }) => authorization)).toEqual(['Bearer seed-token', 'Bearer fresh-token']);
      expect(workspaceRef.current?.token).toBeNull();
      expect(window.localStorage.getItem('llm-wiki-access-token')).toBeNull();
      expect(window.localStorage.getItem(AUTH_FORCE_HOME_REDIRECT_KEY)).toBeNull();

      setQueue('login', [{ status: 200, body: loginBody('after-logout-token') }]);
      await actSignIn('user@example.com', 'password');
      await waitUntil(() => workspaceRef.current?.token === 'after-logout-token');
      expect(workspaceRef.current?.token).toBe('after-logout-token');
      expect(navigation.replace).toHaveBeenCalledTimes(0);
    } finally {
      globalThis.XMLHttpRequest = originalXHR;
    }
  });

  it('does not redirect after anonymous login without forced redirect marker', async () => {
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('anonymous-token') }]);
    setQueue('projects', [{ status: 200, body: [] }]);

    await renderProviders();
    await waitForHydrated();
    expect(getForceHomeRedirect()).toBeNull();

    await actSignIn('user@example.com', 'password');

    await waitUntil(() => workspaceRef.current?.token === 'anonymous-token');
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('sets one-shot intent when a refreshed business retry remains unauthorized, then redirects after login', async () => {
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('fresh-token') },
    ]);
    setQueue('projects', [
      { status: 200, body: [] },
      { status: 401, body: { error: 'expired' } },
      { status: 401, body: { error: 'expired-again' } },
    ]);
    setQueue('login', [{ status: 200, body: loginBody('login-token') }]);

    await renderProviders({ withLoginModal: true });
    await waitForHydrated();

    await actRefreshProjects();
    await waitUntil(() => getForceHomeRedirect() === '1');
    await waitUntil(() => workspaceRef.current?.token === null);
    expect(workspaceRef.current?.user).toBeNull();
    expect(window.localStorage.getItem('llm-wiki-access-token')).toBeNull();
    expect(queue.refresh).toHaveLength(0);
    expect(queue.projects).toHaveLength(0);
    await waitFor(() => expect(screen.getByRole('dialog')).toBeTruthy());

    await actSignIn('user@example.com', 'password');

    await waitUntil(() => (
      workspaceRef.current?.token === 'login-token'
      && getForceHomeRedirect() === null
    ));
    expect(navigation.replace).toHaveBeenCalledTimes(1);
    expect(navigation.replace).toHaveBeenCalledWith('/');
  });

  it('keeps intent when login fails and does not call router.replace', async () => {
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 401, body: { error: 'bad credentials' } }]);

    setForceHomeRedirect(window.localStorage);
    await renderProviders();
    await waitForHydrated();
    expect(getForceHomeRedirect()).toBe('1');

    await actSignIn('user@example.com', 'password').catch(() => undefined);

    expect(getForceHomeRedirect()).toBe('1');
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it.each([
    '/status?x=1',
    '/status#fragment',
    '/?q=stale',
    '/#stale',
    '/',
  ])('consumes marker after regular login and redirects only from dirty URL %s', async (url) => {
    window.history.pushState({}, '', url);
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('regular-token') }]);
    setQueue('projects', [{ status: 200, body: [] }]);

    setForceHomeRedirect(window.localStorage);
    await renderProviders();
    await waitForHydrated();

    await actSignIn('user@example.com', 'password');

    await waitUntil(() => getForceHomeRedirect() === null);
    expect(navigation.replace).toHaveBeenCalledTimes(url === '/' ? 0 : 1);
    if (url !== '/') expect(navigation.replace).toHaveBeenCalledWith('/');
  });

  it.each([
    '/status?x=1',
    '/status#fragment',
    '/?q=stale',
    '/#stale',
    '/',
  ])('consumes marker after demo login and redirects only from dirty URL %s', async (url) => {
    window.history.pushState({}, '', url);
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 200, body: loginBody('demo-token') }]);
    setQueue('projects', [{ status: 200, body: [] }]);

    setForceHomeRedirect(window.localStorage);
    await renderProviders();
    await waitForHydrated();

    await actSignInAsDemo('demo@llm-wiki.dev', 'demo123456');

    await waitUntil(() => getForceHomeRedirect() === null);
    expect(navigation.replace).toHaveBeenCalledTimes(url === '/' ? 0 : 1);
    if (url !== '/') expect(navigation.replace).toHaveBeenCalledWith('/');
  });

  it('redirects after a second forced-unauthorized/login episode in the same mount', async () => {
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 401, body: { error: 'expired' } },
      { status: 401, body: { error: 'expired-again' } },
    ]);
    setQueue('projects', [
      { status: 200, body: [] },
      { status: 200, body: [] },
      { status: 401, body: { error: 'expired' } },
      { status: 200, body: [] },
      { status: 401, body: { error: 'expired-again' } },
      { status: 200, body: [] },
    ]);
    setQueue('login', [
      { status: 200, body: loginBody('post-expiry-token') },
      { status: 200, body: loginBody('post-expiry-token-2') },
    ]);

    await renderProviders();
    await waitForHydrated();
    await waitUntil(() => workspaceRef.current?.token === 'seed-token');
    expect(workspaceRef.current?.token).toBe('seed-token');

    await actRefreshProjects();
    await waitUntil(() => getForceHomeRedirect() === '1');
    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.token === 'post-expiry-token');

    await actRefreshProjects();
    await waitUntil(() => getForceHomeRedirect() === '1');
    await actSignIn('user@example.com', 'password');

    await waitUntil(() => (
      workspaceRef.current?.token === 'post-expiry-token-2'
      && getForceHomeRedirect() === null
    ));

    expect(navigation.replace).toHaveBeenCalledTimes(2);
    expect(navigation.replace).toHaveBeenNthCalledWith(1, '/');
    expect(navigation.replace).toHaveBeenNthCalledWith(2, '/');
  });

  it('does not mark force-home intent on successful refresh', async () => {
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [{ status: 200, body: hydrateBody('seed-token') }]);
    setQueue('projects', [{ status: 200, body: [] }]);

    await renderProviders();
    await waitForHydrated();

    expect(getForceHomeRedirect()).toBeNull();
  });

  it('keeps the route and marker clear when business 401 refreshes and retries successfully', async () => {
    const url = '/status?x=1#fragment';
    window.history.pushState({}, '', url);
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setQueue('refresh', [
      { status: 200, body: hydrateBody('seed-token') },
      { status: 200, body: hydrateBody('refreshed-token') },
    ]);
    setQueue('projects', [
      { status: 200, body: [] },
      { status: 401, body: { error: 'expired' } },
      { status: 200, body: [] },
    ]);

    await renderProviders();
    await waitForHydrated();
    await actRefreshProjects();

    expect(getForceHomeRedirect()).toBeNull();
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(url);
    expect(navigation.replace).toHaveBeenCalledTimes(0);
    expect(workspaceRef.current?.token).toBe('refreshed-token');
  });

  it('clears one-shot marker on explicit sign-out', async () => {
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setForceHomeRedirect(window.localStorage);

    setQueue('refresh', [{ status: 200, body: hydrateBody('seed-token') }]);
    setQueue('projects', [{ status: 200, body: [] }]);
    setQueue('logout', [{ status: 200, body: {} }]);

    await renderProviders();
    await waitForHydrated();
    expect(getForceHomeRedirect()).toBe('1');
    expect(workspaceRef.current?.token).toBe('seed-token');

    await actSignOut();

    await waitUntil(() => getForceHomeRedirect() === null);
    await waitUntil(() => workspaceRef.current?.token === null);
  });

  it('clears marker on explicit logout before a later successful login', async () => {
    window.history.pushState({}, '', '/status?x=1#fragment');
    writeStoredAccessToken(window.localStorage, 'seed-token');
    writeStoredAuthUser(window.localStorage, TEST_USER);
    setForceHomeRedirect(window.localStorage);
    setQueue('refresh', [{ status: 200, body: hydrateBody('seed-token') }]);
    setQueue('projects', [
      { status: 200, body: [] },
      { status: 200, body: [] },
    ]);
    setQueue('logout', [{ status: 200, body: {} }]);
    setQueue('login', [{ status: 200, body: loginBody('after-logout-token') }]);

    await renderProviders();
    await waitForHydrated();
    await actSignOut();
    await waitUntil(() => workspaceRef.current?.token === null);

    await actSignIn('user@example.com', 'password');
    await waitUntil(() => workspaceRef.current?.token === 'after-logout-token');

    expect(getForceHomeRedirect()).toBeNull();
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });

  it('keeps the real login modal visible with its error after failed credentials', async () => {
    window.history.pushState({}, '', '/status?x=1');
    setForceHomeRedirect(window.localStorage);
    setQueue('refresh', [{ status: 401, body: { error: 'missing' } }]);
    setQueue('login', [{ status: 401, body: { error: 'bad credentials' } }]);

    await renderProviders({ withLoginModal: true });
    await waitForHydrated();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeTruthy());

    await act(async () => {
      fireEvent.change(screen.getByPlaceholderText('you@example.com'), { target: { value: 'user@example.com' } });
      fireEvent.change(screen.getByLabelText('密碼'), { target: { value: 'wrong-password' } });
      fireEvent.click(screen.getByRole('button', { name: '登入' }));
    });

    await waitFor(() => expect(screen.getByText('bad credentials')).toBeTruthy());
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(getForceHomeRedirect()).toBe('1');
    expect(navigation.replace).toHaveBeenCalledTimes(0);
  });
});
