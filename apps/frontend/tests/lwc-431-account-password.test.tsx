import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/lib/runtime-config', () => ({
  getRuntimeConfig: () => ({ auth_url: 'https://auth.example.test' }),
}));
vi.mock('@/lib/request-ids', () => ({ recordRequestResponse: vi.fn() }));

import { changeAccountPassword, readPasswordSelfServiceStatus, type PasswordSelfServiceAuthContext } from '@/lib/account-password';

const authContext = (refreshAccessToken = vi.fn().mockResolvedValue('fresh-access-token')): PasswordSelfServiceAuthContext => ({
  accessToken: 'stale-access-token',
  refreshAccessToken,
});

const response = (status: number, payload: unknown) => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => payload,
});

describe('LWC-431 password request payload and refresh', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sends current proof with configured-account password changes', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(200, { status: 'updated' }));
    vi.stubGlobal('fetch', fetchMock);

    await changeAccountPassword(authContext(), 'new-password', 'new-password', 'current-password');

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://auth.example.test/api/v1/auth/account/password');
    expect(init.headers).toEqual({ Authorization: 'Bearer stale-access-token', 'Content-Type': 'application/json' });
    expect(JSON.parse(init.body as string)).toEqual({
      current_password: 'current-password',
      new_password: 'new-password',
      confirm_password: 'new-password',
    });
  });

  it('omits current proof for OAuth first-password setup', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(200, { status: 'updated' }));
    vi.stubGlobal('fetch', fetchMock);

    await changeAccountPassword(authContext(), 'first-password', 'first-password');

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({
      new_password: 'first-password',
      confirm_password: 'first-password',
    });
  });

  it('refreshes once after a status GET receives 401 and retries with the fresh token', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(401, { error: 'expired token' }))
      .mockResolvedValueOnce(response(200, { allowed: true, configured: true }));
    const refreshAccessToken = vi.fn().mockResolvedValue('fresh-access-token');
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext(refreshAccessToken))).resolves.toEqual({ allowed: true, configured: true });

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toEqual({ Authorization: 'Bearer stale-access-token' });
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toEqual({ Authorization: 'Bearer fresh-access-token' });
  });

  it('refreshes once after POST 401 and reuses the exact password body', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(401, { error: 'expired token' }))
      .mockResolvedValueOnce(response(200, { status: 'updated' }));
    const refreshAccessToken = vi.fn().mockResolvedValue('fresh-access-token');
    vi.stubGlobal('fetch', fetchMock);

    await changeAccountPassword(authContext(refreshAccessToken), 'new-password', 'new-password', 'current-password');

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toEqual({ Authorization: 'Bearer stale-access-token', 'Content-Type': 'application/json' });
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toEqual({ Authorization: 'Bearer fresh-access-token', 'Content-Type': 'application/json' });
    expect(fetchMock.mock.calls[1][1]?.body).toBe(fetchMock.mock.calls[0][1]?.body);
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toEqual({
      current_password: 'current-password',
      new_password: 'new-password',
      confirm_password: 'new-password',
    });
  });

  it.each([
    ['null', async () => null],
    ['rejected', async () => { throw new Error('refresh failed'); }],
  ])('does not retry after a %s refresh result', async (_name, refresh) => {
    const fetchMock = vi.fn().mockResolvedValue(response(401, { error: 'expired token' }));
    const refreshAccessToken = vi.fn(refresh);
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext(refreshAccessToken))).rejects.toThrow(/expired token|refresh failed/);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('stops after the single fresh-token retry also receives 401', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(401, { error: 'still unauthorized' }));
    const refreshAccessToken = vi.fn().mockResolvedValue('fresh-access-token');
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext(refreshAccessToken))).rejects.toThrow('still unauthorized');
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it.each([400, 403, 503])('does not refresh or retry a non-401 HTTP %i response', async (status) => {
    const fetchMock = vi.fn().mockResolvedValue(response(status, { error: 'request rejected' }));
    const refreshAccessToken = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext(refreshAccessToken))).rejects.toThrow('request rejected');
    expect(refreshAccessToken).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not refresh or retry an ambiguous network failure', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'));
    const refreshAccessToken = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext(refreshAccessToken))).rejects.toThrow('Failed to fetch');
    expect(refreshAccessToken).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('surfaces a malformed successful status response to its optional caller', async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(200, { allowed: true }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(readPasswordSelfServiceStatus(authContext())).rejects.toThrow('Auth response did not include password settings.');
  });
});
