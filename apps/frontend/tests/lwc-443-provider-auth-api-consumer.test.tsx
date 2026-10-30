import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { configureApiAuth, clearPublicConfigCache } from '@/lib/api';
import { clearRuntimeConfigForTests, setRuntimeConfigForTests } from '@/lib/runtime-config';
import { BYOKProfilesPanel } from '@/components/BYOKProfilesPanel';

const apiURL = 'https://api.example.test';

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('actual LLM profile apiFetch consumer', () => {
  let refresh = vi.fn(async (): Promise<null> => null);
  let unauthorized = vi.fn((_failedToken: string, _failedEpoch: number): void => {});

  beforeEach(() => {
    localStorage.setItem('locale', 'en');
    clearRuntimeConfigForTests();
    clearPublicConfigCache();
    setRuntimeConfigForTests({ schema_version: 1, api_url: apiURL, auth_url: 'https://auth.example.test' });
    refresh = vi.fn(async (): Promise<null> => null);
    unauthorized = vi.fn((_failedToken: string, _failedEpoch: number): void => {});
    configureApiAuth({
      getAccessToken: () => 'fixture-session-token',
      getSessionEpoch: () => 1,
      refreshAccessToken: refresh,
      onUnauthorized: unauthorized,
    });
  });

  afterEach(() => {
    cleanup();
    clearRuntimeConfigForTests();
    clearPublicConfigCache();
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it('displays Test Message provider authentication failure without refresh, replay, or logout on HTTP 502', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/api/v1/llm/profiles')) return jsonResponse({ profiles: [] });
      if (url.endsWith('/api/v1/llm/profiles/test-message')) {
        return jsonResponse({ error: 'provider_authentication' }, 502);
      }
      throw new Error(`unexpected fixture URL ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<BYOKProfilesPanel />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Provider name' }), { target: { value: 'Local fixture' } });
    fireEvent.change(screen.getByLabelText('Fast model'), { target: { value: 'manual-fixture-model' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Test Message' })[0]);
    await screen.findByText('The provider rejected authentication. Check the draft key or saved credential.');

    const operationCalls = fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/api/v1/llm/profiles/test-message'));
    expect(operationCalls).toHaveLength(1);
    expect(refresh).not.toHaveBeenCalled();
    expect(unauthorized).not.toHaveBeenCalled();
  });
});
