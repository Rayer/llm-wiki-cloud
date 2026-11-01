import React from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AuthProvider } from '@/lib/auth';
import { readStoredAccessToken, writeStoredAccessToken, writeStoredAuthUser } from '@/lib/auth-core';
import { setRuntimeConfigForTests } from '@/lib/runtime-config';
import { installShellIntersectionObserver } from './shell-intersection-observer-helper';

const navigation = vi.hoisted(() => ({
  pathname: '/oauth/consent',
  replace: vi.fn(),
}));
const mocks = vi.hoisted(() => ({
  fetch: vi.fn(),
  getPublicConfig: vi.fn().mockResolvedValue({}),
  getStatus: vi.fn().mockResolvedValue({ sourcesCount: 0, conceptsCount: 0, rawCount: 0, suggestedQueries: [], lastExecution: null, raw: {} }),
  getDocumentImportFailureSummary: vi.fn().mockResolvedValue({ failed_count: 0 }),
  bootstrapDefaultProject: vi.fn().mockResolvedValue('project-one'),
  getProjects: vi.fn().mockResolvedValue([{ id: 'project-one', name: 'Recovery test' }]),
}));

vi.mock('next/navigation', () => ({
  usePathname: () => navigation.pathname,
  useRouter: () => ({ replace: navigation.replace }),
}));
vi.mock('@/lib/i18n', () => ({
  useT: () => ({ t: (key: string) => key, locale: 'en', setLocale: vi.fn(), hasFallback: false }),
  useLocale: () => ({ t: (key: string) => key, locale: 'en' }),
}));
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getPublicConfig: mocks.getPublicConfig,
    getStatus: mocks.getStatus,
    getDocumentImportFailureSummary: mocks.getDocumentImportFailureSummary,
  };
});
vi.mock('@/lib/projects', async () => {
  const actual = await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects');
  return {
    ...actual,
    bootstrapDefaultProject: mocks.bootstrapDefaultProject,
    getProjects: mocks.getProjects,
  };
});

const { cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');
import { usePathname } from 'next/navigation';
import { Shell } from '@/components/Shell';
import { GoogleCompletionClient } from '@/components/GoogleCompletionClient';
import { MCPOAuthConsentClient } from '@/components/MCPOAuthConsentClient';

const transactionId = `lwc_mcp_tx_${'R'.repeat(43)}`;
const consentPath = `/oauth/consent?transaction_id=${transactionId}`;
const briefResource = 'https://brief.dev.rayer.idv.tw/mcp';
const authToken = (sub: string) => `header.${btoa(JSON.stringify({ sub }))}.signature`;

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function requestURL(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.href;
  return input.url;
}

function RouteContent() {
  const pathname = usePathname();
  if (pathname === '/login') return <GoogleCompletionClient />;
  return <MCPOAuthConsentClient transactionId={transactionId} />;
}

function App() {
  return <AuthProvider><Shell><RouteContent /></Shell></AuthProvider>;
}

function consentDetails() {
  return {
    transaction_id: transactionId,
    account_email: 'account-a@example.test',
    client_name: 'Daily Brief MCP client',
    resource: briefResource,
    resource_kind: 'fixed',
    resource_name: 'Daily Brief',
    scope: 'brief:read',
    scope_details: [],
    expires_at: '2026-11-03T00:00:00Z',
    prompt: 'select_account',
    account_selected: false,
  };
}

function selectionReturnContext() {
  return JSON.stringify({
    path: consentPath,
    transaction_id: transactionId,
    intent: 'select_account',
    attempt_id: '12345678-1234-1234-1234-123456789abc',
  });
}

let calls: Array<{ url: string; method: string; authorization: string | null }>;
let view: ReturnType<typeof render>;

beforeEach(() => {
  installShellIntersectionObserver();
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState({}, '', '/');
  calls = [];
  navigation.pathname = '/oauth/consent';
  navigation.replace.mockReset().mockImplementation((path: string) => { navigation.pathname = path.split('?')[0]; });
  mocks.getPublicConfig.mockReset().mockResolvedValue({});
  mocks.getStatus.mockReset().mockResolvedValue({ sourcesCount: 0, conceptsCount: 0, rawCount: 0, suggestedQueries: [], lastExecution: null, raw: {} });
  mocks.getDocumentImportFailureSummary.mockReset().mockResolvedValue({ failed_count: 0 });
  mocks.bootstrapDefaultProject.mockReset().mockResolvedValue('project-one');
  mocks.getProjects.mockReset().mockResolvedValue([{ id: 'project-one', name: 'Recovery test' }]);
  setRuntimeConfigForTests({ schema_version: 1, api_url: 'https://api.runtime.test', auth_url: 'https://auth.dev.rayer.idv.tw' });
  mocks.fetch.mockReset().mockImplementation(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const url = requestURL(input);
    const method = init?.method ?? 'GET';
    const authorization = new Headers(init?.headers).get('Authorization');
    calls.push({ url, method, authorization });
    if (url.endsWith('/api/v1/auth/refresh')) return jsonResponse({ error: 'signed out' }, 401);
    if (url.endsWith('/api/v1/auth/google/complete')) return jsonResponse({ status: 'failure', error: 'Provider sign-in was cancelled.', support_ref: 'cancel-ref', jit_provisioned: false });
    if (url.includes('/api/v1/auth/mcp/oauth/consent/')) return jsonResponse({ error: 'expired' }, 410);
    return jsonResponse({});
  });
  vi.stubGlobal('fetch', mocks.fetch);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
  sessionStorage.clear();
});

it('shows a fixed Daily Brief landing above the signed-out login modal without trusting return_to', async () => {
  navigation.pathname = '/oauth/consent';
  window.history.replaceState({}, '', `${consentPath}&return_to=${encodeURIComponent('https://evil.example/')}`);
  view = render(<App />);

  const recovery = await screen.findByRole('link', { name: 'MCPOAuth.returnToDailyBrief' });
  expect(recovery.getAttribute('href')).toBe('https://brief.dev.rayer.idv.tw/');
  expect(recovery.className).toContain('z-[60]');
  expect(await screen.findByRole('dialog')).toBeDefined();
  expect(calls.some(({ url }) => url.includes('/mcp/oauth/'))).toBe(false);
  expect(calls.every(({ authorization }) => authorization === null)).toBe(true);

  navigation.pathname = '/advanced';
  view.rerender(<App />);
  expect(screen.queryByRole('link', { name: 'MCPOAuth.returnToDailyBrief' })).toBeNull();
});

it('returns from provider cancellation to the same transaction and offers the fixed landing when it expired', async () => {
  writeStoredAccessToken(localStorage, authToken('account-a'));
  writeStoredAuthUser(localStorage, { id: 'account-a', email: 'account-a@example.test' });
  sessionStorage.setItem('lwc-google-login-return-to', selectionReturnContext());
  navigation.pathname = '/login';
  mocks.fetch.mockImplementation(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const url = requestURL(input);
    const method = init?.method ?? 'GET';
    const authorization = new Headers(init?.headers).get('Authorization');
    calls.push({ url, method, authorization });
    if (url.endsWith('/api/v1/auth/refresh')) return jsonResponse({ access_token: authToken('account-a'), user: { id: 'account-a', email: 'account-a@example.test' } });
    if (url.endsWith('/api/v1/auth/google/complete')) return jsonResponse({ status: 'failure', error: 'Provider sign-in was cancelled.', support_ref: 'cancel-ref', jit_provisioned: false });
    if (url.includes('/api/v1/auth/mcp/oauth/consent/')) return jsonResponse({ error: 'expired' }, 410);
    return jsonResponse({});
  });
  view = render(<App />);

  fireEvent.click(await screen.findByRole('button', { name: 'GoogleCompletion.returnToConsent' }));
  await waitFor(() => expect(navigation.replace).toHaveBeenCalledWith(consentPath));
  view.rerender(<App />);
  const recovery = await screen.findByRole('link', { name: 'MCPOAuth.returnToDailyBrief' });
  expect(recovery.getAttribute('href')).toBe('https://brief.dev.rayer.idv.tw/');
  expect(calls.some(({ url, method }) => url.includes(`/consent/${transactionId}`) && method === 'POST')).toBe(false);
  expect(calls.filter(({ url }) => url.includes(`/consent/${transactionId}`)).every(({ authorization }) => authorization === `Bearer ${authToken('account-a')}`)).toBe(true);
});

it('offers fixed recovery after a flagged selection becomes terminal and sends no decision', async () => {
  writeStoredAccessToken(localStorage, authToken('account-a'));
  writeStoredAuthUser(localStorage, { id: 'account-a', email: 'account-a@example.test' });
  let consentReads = 0;
  mocks.fetch.mockImplementation(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const url = requestURL(input);
    const method = init?.method ?? 'GET';
    const authorization = new Headers(init?.headers).get('Authorization');
    calls.push({ url, method, authorization });
    if (url.endsWith('/api/v1/auth/refresh')) return jsonResponse({ access_token: authToken('account-a'), user: { id: 'account-a', email: 'account-a@example.test' } });
    if (url.includes(`/api/v1/auth/mcp/oauth/consent/${transactionId}`)) {
      consentReads += 1;
      return consentReads === 1 ? jsonResponse(consentDetails()) : jsonResponse({ error: 'expired' }, 410);
    }
    return jsonResponse({});
  });
  view = render(<App />);

  fireEvent.click(await screen.findByRole('button', { name: 'MCPOAuth.continueWithAccount' }));
  const recovery = await screen.findByRole('link', { name: 'MCPOAuth.returnToDailyBrief' });
  expect(recovery.getAttribute('href')).toBe('https://brief.dev.rayer.idv.tw/');
  expect(calls.filter(({ url, method }) => url.includes(`/consent/${transactionId}`) && method === 'POST')).toHaveLength(0);
  expect(screen.queryByRole('button', { name: 'MCPOAuth.allow' })).toBeNull();
});

it('retains only fixed Daily Brief navigation context when password sign-in changes A to B before a 410', async () => {
  writeStoredAccessToken(localStorage, authToken('account-a'));
  writeStoredAuthUser(localStorage, { id: 'account-a', email: 'account-a@example.test' });
  let consentReads = 0;
  let refreshes = 0;
  const tokenB = authToken('account-b');
  mocks.fetch.mockImplementation(async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const url = requestURL(input);
    const method = init?.method ?? 'GET';
    const authorization = new Headers(init?.headers).get('Authorization');
    calls.push({ url, method, authorization });
    if (url.endsWith('/api/v1/auth/refresh')) {
      refreshes += 1;
      const account = refreshes === 1 ? 'account-a' : 'account-b';
      return jsonResponse({ access_token: authToken(account), user: { id: account, email: `${account}@example.test` } });
    }
    if (url.endsWith('/api/v1/auth/login')) return jsonResponse({ access_token: tokenB, user: { id: 'account-b', email: 'account-b@example.test' } });
    if (url.includes(`/api/v1/auth/mcp/oauth/consent/${transactionId}`)) {
      consentReads += 1;
      return consentReads === 1 ? jsonResponse(consentDetails()) : jsonResponse({ error: 'expired' }, 410);
    }
    return jsonResponse({});
  });
  view = render(<App />);

  fireEvent.click(await screen.findByRole('button', { name: 'MCPOAuth.useAnotherAccount' }));
  await screen.findByRole('dialog');
  fireEvent.change(screen.getByLabelText('Login.email'), { target: { value: 'account-b@example.test' } });
  fireEvent.change(screen.getByLabelText('Login.password'), { target: { value: 'password-b' } });
  fireEvent.click(screen.getByRole('button', { name: 'Login.signIn' }));

  const recovery = await screen.findByRole('link', { name: 'MCPOAuth.returnToDailyBrief' });
  expect(recovery.getAttribute('href')).toBe('https://brief.dev.rayer.idv.tw/');
  expect(readStoredAccessToken(localStorage)).toBe(tokenB);
  expect(refreshes).toBeGreaterThanOrEqual(2);
  const consentRequests = calls.filter(({ url }) => url.includes(`/consent/${transactionId}`));
  expect(consentRequests.map(({ authorization }) => authorization)).toContain(`Bearer ${authToken('account-a')}`);
  expect(consentRequests.map(({ authorization }) => authorization)).toContain(`Bearer ${tokenB}`);
  expect(consentRequests.some(({ method }) => method === 'POST')).toBe(false);
  expect(screen.queryByRole('button', { name: 'MCPOAuth.continueWithAccount' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'MCPOAuth.allow' })).toBeNull();
});
