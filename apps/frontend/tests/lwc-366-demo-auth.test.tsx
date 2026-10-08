import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { setRuntimeConfigForTests } from '@/lib/runtime-config';

const configuredAuthOrigin = 'https://auth-lwc366.example.test';

function jsonResponse(payload: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: vi.fn().mockResolvedValue(payload) };
}

const fetchMock = vi.fn();

async function mountDemoLoginProbe(onError = vi.fn()) {
  const auth = await import('@/lib/auth');
  const authCore = await import('@/lib/auth-core');
  function DemoLoginProbe() {
    const { hydrated, loginAsDemo } = auth.useAuth();
    return (
      <button type="button" disabled={!hydrated} onClick={() => void loginAsDemo().catch(onError)}>
        Demo login
      </button>
    );
  }

  render(<auth.AuthProvider><DemoLoginProbe /></auth.AuthProvider>);
  const button = await screen.findByRole('button', { name: 'Demo login' });
  await waitFor(() => expect(button).not.toHaveProperty('disabled', true));
  return { button, authCore, onError };
}

describe('LWC-366 Demo auth', () => {
  beforeEach(() => {
    vi.resetModules();
    setRuntimeConfigForTests({
      schema_version: 1,
      api_url: 'https://api-lwc366.example.test',
      auth_url: configuredAuthOrigin,
    });
    localStorage.clear();
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    fetchMock.mockImplementation(async (url: string) => {
      if (url.endsWith('/api/v1/auth/refresh')) return jsonResponse({ error: 'invalid refresh token' }, 401);
      if (url.endsWith('/api/v1/auth/demo')) {
        return jsonResponse({ access_token: 'fixture-access-token', user: { id: 'fixture-user', email: 'demo@example.test' } });
      }
      return jsonResponse({ error: 'unexpected endpoint' }, 404);
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    vi.resetModules();
    localStorage.clear();
  });

  it('posts to the configured Auth origin with no body and stores the Demo session', async () => {
    const { button, authCore } = await mountDemoLoginProbe();
    fireEvent.click(button);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      `${configuredAuthOrigin}/api/v1/auth/demo`,
      { method: 'POST', credentials: 'include', headers: undefined, body: undefined },
    ));
    await waitFor(() => expect(authCore.readStoredDemoSession(localStorage)).toBe(true));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/auth/login'))).toBe(false);
  });

  it('surfaces a Demo failure without calling ordinary login or storing a session', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      if (url.endsWith('/api/v1/auth/refresh')) return jsonResponse({ error: 'invalid refresh token' }, 401);
      if (url.endsWith('/api/v1/auth/demo')) return jsonResponse({ error: 'demo unavailable' }, 503);
      return jsonResponse({ access_token: 'unexpected-login-token', user: { id: 'unexpected-user', email: 'demo@example.test' } });
    });

    const { button, authCore, onError } = await mountDemoLoginProbe();
    fireEvent.click(button);

    await waitFor(() => expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'demo unavailable' })));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/auth/demo'))).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/api/v1/auth/login'))).toBe(false);
    expect(authCore.readStoredDemoSession(localStorage)).toBe(false);
    expect(authCore.readStoredAccessToken(localStorage)).toBeNull();
  });
});
