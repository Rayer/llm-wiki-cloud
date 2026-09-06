import React, { act, useEffect } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, render, waitFor } from '@testing-library/react';
import { AuthProvider, useAuth } from '@/lib/auth';
import { GoogleCompletionClient } from '@/components/GoogleCompletionClient';
import { writeStoredAccessToken } from '@/lib/auth-core';

const navigation = vi.hoisted(() => ({ replace: vi.fn() }));
vi.mock('next/navigation', () => ({ useRouter: () => navigation }));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }) }));

function deferred() {
  let resolve!: (value: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve: (body: unknown, status = 200) => resolve(new Response(JSON.stringify(body), { status })) };
}
const token = (sub: string) => `header.${btoa(JSON.stringify({ sub }))}.signature`;
let auth: ReturnType<typeof useAuth>;
function Observer() {
  const value = useAuth();
  useEffect(() => { auth = value; }, [value]);
  return null;
}
let refresh: ReturnType<typeof deferred>;
let completion: ReturnType<typeof deferred>;
let fetchMock: ReturnType<typeof vi.fn>;
let onJit: ReturnType<typeof vi.fn<(event: Event) => void>>;

beforeEach(() => {
  localStorage.clear();
  writeStoredAccessToken(localStorage, token('prior-account'));
  refresh = deferred();
  completion = deferred();
  fetchMock = vi.fn((url: string) => {
    if (url.endsWith('/refresh')) return refresh.promise;
    if (url.endsWith('/google/complete')) return completion.promise;
    if (url.endsWith('/logout')) return Promise.resolve(new Response('{}'));
    if (url.endsWith('/login')) return Promise.resolve(new Response(JSON.stringify({ access_token: token('other-account'), user: { id: 'other-account', email: 'other@example.com' } })));
    throw new Error(`Unexpected request ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  onJit = vi.fn();
  window.addEventListener('lwc-google-jit-completed', onJit);
});
afterEach(() => {
  cleanup();
  window.removeEventListener('lwc-google-jit-completed', onJit);
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

for (const strict of [false, true]) {
  for (const first of ['refresh', 'completion']) {
    it(`uses the cookie session with ${first} first, StrictMode=${strict}`, async () => {
      const tree = <AuthProvider><Observer /><GoogleCompletionClient /></AuthProvider>;
      render(strict ? <React.StrictMode>{tree}</React.StrictMode> : tree);
      await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.endsWith('/google/complete'))).toBe(true));
      const finishRefresh = () => refresh.resolve({ access_token: token('google-account'), user: { id: 'google-account', email: 'google@example.com' } });
      const finishCompletion = () => completion.resolve({ status: 'success', jit_provisioned: true });
      await act(async () => { (first === 'refresh' ? finishRefresh : finishCompletion)(); });
      expect(navigation.replace).not.toHaveBeenCalled();
      expect(onJit).not.toHaveBeenCalled();
      await act(async () => { (first === 'refresh' ? finishCompletion : finishRefresh)(); });
      await waitFor(() => expect(navigation.replace).toHaveBeenCalledExactlyOnceWith('/'));
      expect(auth.accessToken).toBe(token('google-account'));
      expect(onJit).toHaveBeenCalledTimes(1);
      expect((onJit.mock.calls[0][0] as CustomEvent).detail.userId).toBe('google-account');
      expect(fetchMock.mock.calls.filter(([url]) => url.endsWith('/refresh'))).toHaveLength(1);
      expect(fetchMock.mock.calls.filter(([url]) => url.endsWith('/google/complete'))).toHaveLength(1);
    });
  }
}

for (const action of ['logout', 'login', 'unmount'] as const) {
  for (const refreshPending of [false, true]) {
    it(`fences completion after ${action}, refresh pending=${refreshPending}`, async () => {
      const view = render(<AuthProvider><Observer /><GoogleCompletionClient /></AuthProvider>);
      await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.endsWith('/google/complete'))).toBe(true));
      if (!refreshPending) {
        await act(async () => { refresh.resolve({ access_token: token('google-account') }); });
      }
      await act(async () => {
        if (action === 'unmount') view.unmount();
        else if (action === 'logout') await auth.logout();
        else await auth.login('other@example.com', 'password');
      });
      await act(async () => {
        if (refreshPending) refresh.resolve({ access_token: token('google-account') });
        completion.resolve({ status: 'success', jit_provisioned: true });
      });
      expect(navigation.replace).not.toHaveBeenCalled();
      expect(onJit).not.toHaveBeenCalled();
    });
  }
}

it('restores a cookie-only session once under StrictMode', async () => {
  localStorage.clear();
  render(<React.StrictMode><AuthProvider><Observer /><GoogleCompletionClient /></AuthProvider></React.StrictMode>);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  await act(async () => { refresh.resolve({ access_token: token('google-account') }); });
  await act(async () => { completion.resolve({ status: 'success', jit_provisioned: true }); });
  await waitFor(() => expect(navigation.replace).toHaveBeenCalledExactlyOnceWith('/'));
  expect(fetchMock.mock.calls.filter(([url]) => url.endsWith('/refresh'))).toHaveLength(1);
  expect((onJit.mock.calls[0][0] as CustomEvent).detail.userId).toBe('google-account');
});

it('does not fall back to the prior account when cookie refresh fails', async () => {
  const view = render(<AuthProvider><GoogleCompletionClient /></AuthProvider>);
  await act(async () => {
    refresh.resolve({ error: 'expired cookie' }, 401);
    completion.resolve({ status: 'success', jit_provisioned: true, support_ref: 'backend-ref-1234' });
  });
  await waitFor(() => expect(view.getByRole('alert')).toBeDefined());
  expect(view.getByText('backend-ref-1234')).toBeDefined();
  expect(navigation.replace).not.toHaveBeenCalled();
  expect(onJit).not.toHaveBeenCalled();
  expect(fetchMock.mock.calls.filter(([url]) => url.endsWith('/refresh'))).toHaveLength(1);
});
