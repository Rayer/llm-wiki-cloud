import { afterEach, expect, it, vi } from 'vitest';
import { startGoogleLogin, readGoogleCompletionResult } from '../src/lib/google-auth';
import { clearRuntimeConfigForTests, getRuntimeConfig, loadRuntimeConfig } from '../src/lib/runtime-config';

afterEach(() => {
  clearRuntimeConfigForTests();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

for (const authURL of ['https://auth-a.example.test', 'https://auth-b.example.test']) {
  it(`uses the loaded Auth URL at call time: ${authURL}`, async () => {
    clearRuntimeConfigForTests();
    const configURL = 'https://config.example.test/frontend-config.json';
    const config = { schema_version: 1, api_url: 'https://api.example.test', auth_url: authURL };
    const assign = vi.fn();
    const fetch = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: async () => config })
      .mockResolvedValueOnce({ ok: true, json: async () => ({ status: 'cancelled' }) });
    vi.stubEnv('NEXT_PUBLIC_CONFIG_URL', configURL);
    vi.stubGlobal('window', { location: { href: 'https://frontend.example.test/', assign } });
    vi.stubGlobal('fetch', fetch);

    expect(fetch).not.toHaveBeenCalled();
    await loadRuntimeConfig();
    expect(getRuntimeConfig().auth_url).toBe(authURL);
    startGoogleLogin();
    await readGoogleCompletionResult();

    expect(fetch).toHaveBeenNthCalledWith(1, configURL, expect.objectContaining({ credentials: 'omit', cache: 'no-store' }));
    expect(fetch).toHaveBeenNthCalledWith(2, `${authURL}/api/v1/auth/google/complete`, { credentials: 'include' });
    expect(assign).toHaveBeenCalledWith(`${authURL}/api/v1/auth/google/login/start`);
  });
}
