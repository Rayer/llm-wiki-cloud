import { afterEach, expect, it, vi } from 'vitest';

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.resetModules();
});

for (const override of [undefined, 'https://auth.example', 'https://auth-dev.rayer.idv.tw']) {
  it(`uses effective Auth origin for navigation and completion: ${override ?? 'fallback'}`, async () => {
    vi.resetModules();
    vi.stubEnv('NEXT_PUBLIC_AUTH_URL', override);
    const expected = override ?? 'https://auth.dev.rayer.idv.tw';
    const assign = vi.fn();
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ status: 'cancelled' }) });
    vi.stubGlobal('window', { location: { assign } });
    vi.stubGlobal('fetch', fetch);
    const { AUTH_URL } = await import('../src/lib/auth-core');
    const { startGoogleLogin, readGoogleCompletionResult } = await import('../src/lib/google-auth');
    expect(AUTH_URL).toBe(expected);
    startGoogleLogin();
    await readGoogleCompletionResult();
    expect(assign).toHaveBeenCalledWith(`${expected}/api/v1/auth/google/login/start`);
    expect(fetch).toHaveBeenCalledWith(`${expected}/api/v1/auth/google/complete`, { credentials: 'include' });
  });
}
