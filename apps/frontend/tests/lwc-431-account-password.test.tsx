import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/lib/runtime-config', () => ({
  getRuntimeConfig: () => ({ auth_url: 'https://auth.example.test' }),
}));
vi.mock('@/lib/request-ids', () => ({ recordRequestResponse: vi.fn() }));

import { changeAccountPassword } from '@/lib/account-password';

describe('LWC-431 password request payload', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sends current proof with configured-account password changes', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ status: 'updated' }) });
    vi.stubGlobal('fetch', fetchMock);

    await changeAccountPassword('web-access-token', 'new-password', 'new-password', 'current-password');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://auth.example.test/api/v1/auth/account/password');
    expect(JSON.parse(init.body as string)).toEqual({
      current_password: 'current-password',
      new_password: 'new-password',
      confirm_password: 'new-password',
    });
  });

  it('omits current proof for OAuth first-password setup', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ status: 'updated' }) });
    vi.stubGlobal('fetch', fetchMock);

    await changeAccountPassword('web-access-token', 'first-password', 'first-password');

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({
      new_password: 'first-password',
      confirm_password: 'first-password',
    });
  });
});
