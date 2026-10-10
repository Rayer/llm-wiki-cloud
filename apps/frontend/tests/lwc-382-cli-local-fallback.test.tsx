import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', { configurable: true, value: (callback: () => unknown) => Promise.resolve(callback()) });
}

const { cleanup, fireEvent, render, screen } = await import('@testing-library/react');
const mocks = vi.hoisted(() => ({
  locale: 'en' as 'en' | 'zh-TW',
  refreshAccessToken: vi.fn(),
  readGoogleIdentitySummary: vi.fn(),
}));

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: 'web-access-token',
    refreshAccessToken: mocks.refreshAccessToken,
    user: { id: 'owner-1', email: 'owner@example.test' },
  }),
}));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: null }) }));
vi.mock('@/lib/runtime-config', () => ({ getRuntimeConfig: () => ({ auth_url: 'https://auth.fixture.test' }) }));
vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  return {
    ...actual,
    useLocale: () => ({ t: (key: string, params?: Record<string, string | number>) => actual.translate(mocks.locale, key, params) }),
  };
});
vi.mock('@/lib/google-auth', async () => {
  const actual = await vi.importActual<typeof import('@/lib/google-auth')>('@/lib/google-auth');
  return { ...actual, readGoogleIdentitySummary: mocks.readGoogleIdentitySummary };
});

import { AccountSettingsModal } from '@/components/AccountSettingsModal';
import { CLIAuthError, reauthorizeSyncBinding, revokeCLISession, revokeSyncBinding } from '@/lib/cli-auth';
import { translate } from '@/lib/i18n';

const session = { id: 'session-1', client_name: 'lwc-sync CLI', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const binding = { binding_id: 'binding-1', host: 'https://auth.example.test', wiki_id: 'wiki-1', project_id: 'project-1', authorized_by: 'owner-1', status: 'active', created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' };
const context = { accessToken: 'web-access-token', refreshAccessToken: mocks.refreshAccessToken };

function jsonResponse(ok: boolean, status: number, body: unknown) {
  return { ok, status, json: async () => body };
}

const actions = [
  {
    name: 'session revoke', buttonKey: 'AccountSettings.revokeSession', errorKey: 'AccountSettings.cliSessionRevokeError',
    path: '/sessions/session-1/revoke', request: () => revokeCLISession(context, session.id),
  },
  {
    name: 'binding revoke', buttonKey: 'AccountSettings.revokeBinding', errorKey: 'AccountSettings.syncBindingRevokeError',
    path: '/bindings/project-1/binding-1/revoke', request: () => revokeSyncBinding(context, binding),
  },
  {
    name: 'binding reauthorize', buttonKey: 'AccountSettings.reauthorizeBinding', errorKey: 'AccountSettings.syncBindingReauthorizeError',
    path: '/bindings/project-1/reauthorize', request: () => reauthorizeSyncBinding(context, binding),
  },
] as const;

const scenarios = ['missing backend error', 'blank backend error', 'backend message', 'backend message equals localized fallback'] as const;

beforeEach(() => {
  mocks.locale = 'en';
  mocks.refreshAccessToken.mockReset().mockResolvedValue(null);
  mocks.readGoogleIdentitySummary.mockReset().mockResolvedValue({ primary_email: 'owner@example.test', linked_providers: [] });
  vi.stubGlobal('confirm', vi.fn(() => true));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('LWC-382 CLI and Sync local fallback copy', () => {
  for (const action of actions) {
    it.each(['en', 'zh-TW'] as const)(`${action.name} translates only its local fallback in %s`, async (locale) => {
      const fallback = translate(locale, action.errorKey);
      for (const scenario of scenarios) {
        cleanup();
        mocks.locale = locale;
        const payload = scenario === 'missing backend error'
          ? {}
          : scenario === 'blank backend error'
            ? { error: ' \t ' }
            : scenario === 'backend message equals localized fallback'
              ? { error: fallback }
              : { error: 'Backend detail remains unchanged.' };
        const expected = scenario === 'missing backend error' || scenario === 'blank backend error'
          ? fallback
          : payload.error;
        const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
          const url = String(input);
          if (url.endsWith('/sessions')) return jsonResponse(true, 200, { sessions: [session] });
          if (url.endsWith('/bindings')) return jsonResponse(true, 200, { bindings: [binding] });
          if (url.includes(action.path)) return jsonResponse(false, 503, payload);
          throw new Error(`Unexpected local test request: ${url} ${init?.method ?? 'GET'}`);
        });
        vi.stubGlobal('fetch', fetchMock);

        render(<AccountSettingsModal onClose={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: translate(locale, action.buttonKey) }));
        expect((await screen.findByRole('alert')).textContent).toBe(expected);

        const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST');
        expect(posts).toHaveLength(1);
        expect(String(posts[0][0])).toContain(action.path);
      }
    });
  }

  it('keeps an actual nonblank backend error untyped even when its text equals the localized fallback', async () => {
    mocks.locale = 'zh-TW';
    const operations = [
      { request: actions[0].request, fallback: actions[0].errorKey },
      { request: actions[1].request, fallback: actions[1].errorKey },
      { request: actions[2].request, fallback: actions[2].errorKey },
    ];
    for (const operation of operations) {
      const backendMessage = translate('zh-TW', operation.fallback);
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(false, 503, { error: backendMessage })));
      let caught: unknown;
      try {
        await operation.request();
      } catch (error) {
        caught = error;
      }
      expect(caught).toBeInstanceOf(Error);
      expect(caught).not.toBeInstanceOf(CLIAuthError);
      expect((caught as Error).message).toBe(backendMessage);
    }
  });

  it('marks helper-generated fallback errors so the modal can localize them', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(false, 503, {})));
    await expect(revokeCLISession(context, session.id)).rejects.toMatchObject({
      name: 'CLIAuthError',
      status: 503,
      localReason: 'missing_backend_error',
    });
  });
});
