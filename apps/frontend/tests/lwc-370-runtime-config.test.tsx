import { act } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RuntimeConfigBoundary } from '@/components/RuntimeConfigBoundary';
import { AuthProvider } from '@/lib/auth';
import { apiFetch, clearPublicConfigCache, configureApiAuth, getBuildInfo, getPublicConfig, uploadRawFile } from '@/lib/api';
import { listCLISessions } from '@/lib/cli-auth';
import {
  clearRuntimeConfigForTests,
  getRuntimeConfig,
  loadRuntimeConfig,
} from '@/lib/runtime-config';
import {
  beginGoogleLink,
  cancelGoogleLink,
  confirmGoogleLink,
  readGoogleCompletionResult,
  readGoogleIdentitySummary,
  readGoogleLinkCompletion,
  startGoogleLogin,
} from '@/lib/google-auth';

const configURL = 'https://config.example.test/frontend-config.json';
const configA = {
  schema_version: 1,
  api_url: 'https://api-a.example.test',
  auth_url: 'https://auth-a.example.test',
};
const configB = {
  schema_version: 1,
  api_url: 'https://api-b.example.test',
  auth_url: 'https://auth-b.example.test',
};

function response(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function resetApiAuth() {
  configureApiAuth({
    getAccessToken: () => null,
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
}

beforeEach(() => {
  clearRuntimeConfigForTests();
  clearPublicConfigCache();
  resetApiAuth();
  vi.stubEnv('NEXT_PUBLIC_CONFIG_URL', configURL);
});

afterEach(() => {
  cleanup();
  clearRuntimeConfigForTests();
  clearPublicConfigCache();
  resetApiAuth();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.useRealTimers();
});

describe('LWC-370 runtime config', () => {
  it('reports a missing bootstrap parameter without making a request', async () => {
    const fetchMock = vi.fn();
    vi.stubEnv('NEXT_PUBLIC_CONFIG_URL', '   ');
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadRuntimeConfig()).rejects.toThrow('parameter missing: NEXT_PUBLIC_CONFIG_URL');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('loads once with no credentials or cache and validates the public schema', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(configA));
    vi.stubGlobal('fetch', fetchMock);

    const [first, second] = await Promise.all([loadRuntimeConfig(), loadRuntimeConfig()]);

    expect(first).toEqual(configA);
    expect(second).toBe(first);
    expect(getRuntimeConfig()).toBe(first);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(configURL, expect.objectContaining({
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
      signal: expect.any(AbortSignal),
    }));
    expect(fetchMock.mock.calls[0][1].headers?.Authorization).toBeUndefined();
  });

  it.each([
    [{ schema_version: 1 }, 'parameter missing: api_url'],
    [{ schema_version: 1, api_url: ' ', auth_url: configA.auth_url }, 'parameter missing: api_url'],
    [{ schema_version: 1, api_url: configA.api_url }, 'parameter missing: auth_url'],
    [{ ...configA, schema_version: 2 }, 'unsupported schema_version'],
    [{ ...configA, api_url: 'http://api.example.test' }, 'invalid URL for api_url'],
    [{ ...configA, api_url: 'not a URL' }, 'invalid URL for api_url'],
    [{ ...configA, private_value: 'must not be published' }, 'unexpected field'],
  ])('rejects malformed config %j', async (payload, message) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(payload)));
    await expect(loadRuntimeConfig()).rejects.toThrow(message);
  });

  it('distinguishes load, HTTP, JSON, and timeout failures and allows a retry', async () => {
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValueOnce(response({}, 503))
      .mockResolvedValueOnce(new Response('{not json'))
      .mockResolvedValueOnce(response(configA));
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadRuntimeConfig()).rejects.toThrow('Runtime config load failed.');
    await expect(loadRuntimeConfig()).rejects.toThrow('HTTP 503');
    await expect(loadRuntimeConfig()).rejects.toThrow('invalid JSON');
    await expect(loadRuntimeConfig()).resolves.toEqual(configA);
    expect(fetchMock).toHaveBeenCalledTimes(4);

    clearRuntimeConfigForTests();
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => {
      signal = init.signal ?? undefined;
      return new Promise<Response>(() => undefined);
    }));
    const pending = loadRuntimeConfig();
    const timedOut = expect(pending).rejects.toThrow('timed out after 10 seconds');
    await vi.advanceTimersByTimeAsync(10_000);
    await timedOut;
    expect(signal?.aborted).toBe(true);
  });

  it('does not mount AuthProvider until config is loaded, then refreshes against that Auth URL', async () => {
    let resolveConfig!: (value: Response) => void;
    const configResponse = new Promise<Response>((resolve) => { resolveConfig = resolve; });
    const fetchMock = vi.fn((url: string) => {
      if (url === configURL) return configResponse;
      if (url === `${configA.auth_url}/api/v1/auth/refresh`) return Promise.resolve(response({ error: 'expired' }, 401));
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(
      <RuntimeConfigBoundary>
        <AuthProvider><p>Workspace ready</p></AuthProvider>
      </RuntimeConfigBoundary>,
    );

    expect(screen.getByRole('status').textContent).toContain('Loading');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe(configURL);

    await act(async () => { resolveConfig(response(configA)); });
    await screen.findByText('Workspace ready');
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toBe(`${configA.auth_url}/api/v1/auth/refresh`);
  });

  it('retries a failed load from the error screen', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ error: 'offline' }, 503))
      .mockResolvedValueOnce(response(configA));
    vi.stubGlobal('fetch', fetchMock);

    render(<RuntimeConfigBoundary><p>Workspace ready</p></RuntimeConfigBoundary>);

    expect((await screen.findByRole('alert')).textContent).toContain('HTTP 503');
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('Workspace ready');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('uses a new file value after reload through the same API, upload, Auth, Google, and CLI consumers', async () => {
    const configByRead = [configA, configB];
    const requested: string[] = [];
    const navigation: string[] = [];
    let xhrURL = '';
    let xhrMethod = '';
    vi.stubGlobal('window', {
      location: {
        href: 'https://frontend.example.test/',
        assign: (url: string) => navigation.push(url),
      },
      localStorage: { getItem: () => 'project-1' },
    });
    const fetchMock = vi.fn(async (input: string) => {
      const url = String(input);
      requested.push(url);
      if (url === configURL) return response(configByRead.shift());
      if (url.endsWith('/api/v1/public/version')) return response({
        product_version: '1.2.3', commit: 'abc1234', branch: 'main', tag: '',
        image_tag: 'test', service: 'bff', revision: 'bff-test',
      });
      if (url.endsWith('/api/v1/public/config')) return response({ registration_enabled: true });
      if (url.endsWith('/api/v1/auth/google/complete')) return response({ status: 'cancelled' });
      if (url.endsWith('/api/v1/auth/google/link/start')) return response({ authorization_url: 'https://accounts.example.test/link' });
      if (url.endsWith('/api/v1/auth/google/link/complete')) return response({
        status: 'confirmation_required', confirmation_id: 'confirm-1',
        provider_email: 'provider@example.test', current_email: 'user@example.test',
      });
      if (url.endsWith('/api/v1/auth/google/identity')) return response({ primary_email: 'user@example.test', linked_providers: [] });
      if (url.endsWith('/api/v1/auth/google/link/confirm')) return response({ status: 'linked' });
      if (url.endsWith('/api/v1/auth/google/link/cancel')) return response({ status: 'cancelled' });
      if (url.endsWith('/api/v1/auth/cli/sessions')) return response({ sessions: [] });
      return response({});
    });
    vi.stubGlobal('fetch', fetchMock);
    class SyntheticXMLHttpRequest {
      status = 201;
      responseText = JSON.stringify({ filename: 'note.txt', path: 'raw/note.txt', bytes: 1, sha256: 'fixture', status: 'created' });
      upload = {};
      onload?: () => void;
      onerror?: () => void;
      ontimeout?: () => void;
      onabort?: () => void;
      open(method: string, url: string) { xhrMethod = method; xhrURL = url; }
      setRequestHeader() {}
      send() { this.onload?.(); }
    }
    vi.stubGlobal('XMLHttpRequest', SyntheticXMLHttpRequest);
    configureApiAuth({
      getAccessToken: () => 'fixture-access-token',
      getSessionEpoch: () => 1,
      refreshAccessToken: async () => null,
      onUnauthorized: () => undefined,
    });

    for (const config of [configA, configB]) {
      clearRuntimeConfigForTests();
      clearPublicConfigCache();
      await loadRuntimeConfig();
      expect(getRuntimeConfig()).toEqual(config);
      await apiFetch('/api/v1/status', { requireProject: false });
      await getBuildInfo();
      await getPublicConfig({ refresh: true });
      startGoogleLogin();
      await readGoogleCompletionResult();
      await beginGoogleLink('password', 'fixture-access-token');
      await readGoogleLinkCompletion('fixture-access-token');
      await readGoogleIdentitySummary('fixture-access-token');
      await confirmGoogleLink('fixture-access-token', 'confirm-1');
      await cancelGoogleLink('fixture-access-token', 'confirm-1');
      await listCLISessions({ accessToken: 'fixture-access-token', refreshAccessToken: async () => null });
      await uploadRawFile(new File(['x'], 'note.txt', { type: 'text/plain' }));

      expect(requested).toContain(`${config.api_url}/api/v1/status`);
      expect(requested).toContain(`${config.api_url}/api/v1/public/version`);
      expect(requested).toContain(`${config.api_url}/api/v1/public/config`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/complete`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/link/start`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/link/complete`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/identity`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/link/confirm`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/google/link/cancel`);
      expect(requested).toContain(`${config.auth_url}/api/v1/auth/cli/sessions`);
      expect(navigation).toContain(`${config.auth_url}/api/v1/auth/google/login/start`);
      expect(navigation).toContain('https://accounts.example.test/link');
      expect(xhrMethod).toBe('POST');
      expect(xhrURL).toBe(`${config.api_url}/api/v1/raw/upload`);
    }

    expect(fetchMock.mock.calls.filter(([url]) => url === configURL)).toHaveLength(2);
    expect(navigation).toContain(`${configA.auth_url}/api/v1/auth/google/login/start`);
    expect(navigation).toContain(`${configB.auth_url}/api/v1/auth/google/login/start`);
  });
});
