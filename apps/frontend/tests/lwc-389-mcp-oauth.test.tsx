import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import enMessages from '@/messages/en.json';
import zhTWMessages from '@/messages/zh-TW.json';

const mocks = vi.hoisted(() => ({
  accessToken: 'web-session-token',
  sessionEpoch: 1,
  user: { id: 'user-one', email: 'owner@example.com' },
  project: { id: 'project-one', name: 'Research Project' } as { id: string; name: string } | null,
  readConsent: vi.fn(),
  decideConsent: vi.fn(),
  listGrants: vi.fn(),
  listResourceGrants: vi.fn(),
  revokeGrant: vi.fn(),
  apiFetch: vi.fn(),
  resourceURL: vi.fn((projectId = 'project-one') => `http://localhost:18080/mcp/projects/${encodeURIComponent(projectId)}`),
  safeReturnURL: vi.fn(() => true),
}));

vi.mock('@/lib/auth', () => ({ useAuth: () => ({
  accessToken: mocks.accessToken,
  sessionEpoch: mocks.sessionEpoch,
  user: mocks.user,
  isDemoSession: false,
}) }));

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({
  hydrated: true,
  currentProject: mocks.project,
  projectsLoading: false,
}) }));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));

vi.mock('@/lib/i18n', () => {
  const t = (key: string) => key;
  return { useT: () => ({ t }) };
});

vi.mock('next/link', () => ({ default: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a href={href as string} {...props}>{children}</a> }));

vi.mock('@/lib/mcp-oauth', async () => {
  const actual = await vi.importActual<typeof import('@/lib/mcp-oauth')>('@/lib/mcp-oauth');
  return {
    ...actual,
    readMCPOAuthConsent: mocks.readConsent,
    decideMCPOAuthConsent: mocks.decideConsent,
    listMCPOAuthGrants: mocks.listGrants,
    listMCPOAuthResourceGrants: mocks.listResourceGrants,
    revokeMCPOAuthGrant: mocks.revokeGrant,
    mcpResourceURL: mocks.resourceURL,
    isSafeMCPOAuthReturnURL: mocks.safeReturnURL,
  };
});

const { act, cleanup, fireEvent, render, screen, waitFor, within } = await import('@testing-library/react');
import { MCPOAuthConsentClient } from '@/components/MCPOAuthConsentClient';
import AdvancedPage from '@/app/(workspace)/advanced/page';
import { MCPOAuthApiError } from '@/lib/mcp-oauth';

const transactionId = `lwc_mcp_tx_${'C'.repeat(43)}`;
const consent = {
  transaction_id: transactionId,
  account_email: 'owner@example.com',
  project_id: 'project-one',
  project_name: 'Research Project',
  client_name: 'Hermes Agent',
  resource: 'http://localhost:18080/mcp/projects/project-one',
  scope: 'mcp:query',
  expires_at: '2026-10-12T02:00:00Z',
};
const otherTransactionId = `lwc_mcp_tx_${'D'.repeat(43)}`;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function mockLocationAssign() {
  const assign = vi.fn();
  Object.defineProperty(window, 'location', { configurable: true, value: { ...window.location, assign } });
  return assign;
}

beforeEach(() => {
  mocks.accessToken = 'web-session-token';
  mocks.sessionEpoch = 1;
  mocks.user = { id: 'user-one', email: 'owner@example.com' };
  mocks.project = { id: 'project-one', name: 'Research Project' };
  mocks.readConsent.mockReset().mockResolvedValue(consent);
  mocks.decideConsent.mockReset().mockImplementation((_transactionId: string, decision: string) => Promise.resolve(
    decision === 'continue'
      ? { redirect_url: '', requires_consent: true }
      : { redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque', requires_consent: false },
  ));
  mocks.listGrants.mockReset().mockResolvedValue([{
    id: 'grant-one', client_name: 'Hermes Agent', project_id: 'project-one',
    resource: consent.resource, scope: 'mcp:query', status: 'active',
    created_at: '2026-10-01T00:00:00Z', expires_at: '2026-10-31T00:00:00Z',
  }]);
  mocks.listResourceGrants.mockReset().mockResolvedValue([]);
  mocks.revokeGrant.mockReset().mockResolvedValue(undefined);
  mocks.apiFetch.mockReset().mockResolvedValue({ ok: true, status: 200, json: async () => ({ keys: [] }) });
  mocks.safeReturnURL.mockReturnValue(true);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

afterEach(() => {
  cleanup();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
  vi.clearAllMocks();
});

describe('LWC-389 MCP OAuth frontend', () => {
  it('shows the actual client and Project without deciding when the server finds no reusable grant', async () => {
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText('Hermes Agent')).toBeDefined();
    expect(screen.getByText('owner@example.com')).toBeDefined();
    expect(screen.getByText('Research Project')).toBeDefined();
    expect(screen.getByText('mcp:query')).toBeDefined();
    expect(mocks.readConsent).toHaveBeenCalledWith(transactionId, 'web-session-token');
    expect(mocks.decideConsent).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'MCPOAuth.allow' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'MCPOAuth.deny' })).toBeDefined();
  });

  it('redirects through the validated callback when the backend confirms a reusable grant', async () => {
    const assign = mockLocationAssign();
    mocks.readConsent.mockResolvedValueOnce({ ...consent, can_continue: true });
    mocks.decideConsent.mockImplementationOnce(async (_transactionId: string, decision: string) => (
      decision === 'continue'
        ? { redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=continuation&state=kept', requires_consent: false }
        : { redirect_url: '', requires_consent: true }
    ));

    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('http://127.0.0.1:4321/oauth/callback?code=continuation&state=kept'));
    expect(screen.queryByRole('button', { name: 'MCPOAuth.allow' })).toBeNull();
    expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'continue', 'web-session-token');
  });

  it('keeps explicit consent when an eligible grant is revoked before continuation commits', async () => {
    const assign = mockLocationAssign();
    mocks.readConsent.mockResolvedValueOnce({ ...consent, can_continue: true });
    mocks.decideConsent.mockResolvedValueOnce({ redirect_url: '', requires_consent: true });

    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByRole('button', { name: 'MCPOAuth.allow' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'MCPOAuth.deny' })).toBeDefined();
    expect(assign).not.toHaveBeenCalled();
  });

  it('ignores a reusable-grant callback after the authenticated account changes', async () => {
    const pending = deferred<{ redirect_url: string; requires_consent: boolean }>();
    let continuationCalls = 0;
    mocks.readConsent.mockResolvedValue({ ...consent, can_continue: true });
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => {
      if (decision !== 'continue') return Promise.resolve({ redirect_url: '', requires_consent: true });
      continuationCalls += 1;
      return continuationCalls === 1
        ? pending.promise
        : Promise.resolve({ redirect_url: '', requires_consent: true });
    });
    const assign = mockLocationAssign();
    const view = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(1));

    mocks.user = { id: 'user-two', email: 'other@example.com' };
    mocks.sessionEpoch = 2;
    mocks.accessToken = 'replacement-session-token';
    view.rerender(<MCPOAuthConsentClient transactionId={transactionId} />);
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(2));
    await act(async () => {
      pending.resolve({ redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=stale&state=stale', requires_consent: false });
      await pending.promise;
    });

    expect(assign).not.toHaveBeenCalled();
  });

  it('keeps account-owned Daily Brief grant management reachable without an LWC Project', async () => {
    mocks.project = null;
    mocks.listResourceGrants.mockResolvedValueOnce([{
      id: 'brief-grant-one', client_name: 'Daily Brief Web', project_id: '',
      resource: 'https://brief.rayer.idv.tw/mcp', scope: 'brief:manage', status: 'active',
      created_at: '2026-10-01T00:00:00Z', expires_at: '2026-10-31T00:00:00Z',
    }]);
    render(<AdvancedPage />);
    expect(screen.getAllByText('MCPOAuth.accountGrantsTitle').length).toBeGreaterThan(0);
    expect(screen.queryByText('MCPOAuth.currentProject')).toBeNull();
    expect(screen.queryByText('MCPOAuth.manualFallback')).toBeNull();
    const accountPanel = screen.getAllByText('MCPOAuth.accountGrantsTitle')[0].closest('details')!;
    fireEvent.click(accountPanel.querySelector('summary')!);
    expect(await screen.findByText('Daily Brief Web')).toBeDefined();
    expect(mocks.listResourceGrants).toHaveBeenCalledWith('https://brief.rayer.idv.tw/mcp', 'web-session-token');
    fireEvent.click(within(accountPanel).getByRole('button', { name: 'MCPOAuth.revoke' }));
    await waitFor(() => expect(mocks.revokeGrant).toHaveBeenCalledWith('brief-grant-one', 'web-session-token'));
    await waitFor(() => expect(mocks.listResourceGrants).toHaveBeenCalledTimes(2));
    expect(mocks.listGrants).not.toHaveBeenCalled();
  });

  it('clears account-owned grant state when the signed-in account changes', async () => {
    mocks.project = null;
    mocks.listResourceGrants.mockResolvedValueOnce([{
      id: 'brief-grant-one', client_name: 'First account client', project_id: '',
      resource: 'https://brief.rayer.idv.tw/mcp', scope: 'brief:manage', status: 'active',
      created_at: '2026-10-01T00:00:00Z', expires_at: '2026-10-31T00:00:00Z',
    }]);
    const view = render(<AdvancedPage />);
    const accountPanel = screen.getAllByText('MCPOAuth.accountGrantsTitle')[0].closest('details')!;
    fireEvent.click(accountPanel.querySelector('summary')!);
    expect(await screen.findByText('First account client')).toBeDefined();
    mocks.user = { id: 'user-two', email: 'other@example.com' };
    mocks.sessionEpoch = 2;
    mocks.accessToken = 'replacement-session-token';
    view.rerender(<AdvancedPage />);
    await waitFor(() => expect(screen.queryByText('First account client')).toBeNull());
    await waitFor(() => expect(mocks.listResourceGrants).toHaveBeenLastCalledWith('https://brief.rayer.idv.tw/mcp', 'replacement-session-token'));
  });

  it('explains the exact raw write permission when the client requests it', async () => {
    mocks.readConsent.mockResolvedValueOnce({
      ...consent,
      scope: 'mcp:query mcp:raw:write',
      scope_details: [
        { name: 'mcp:query', description: 'Query project knowledge.' },
        { name: 'mcp:raw:write', description: 'Add permitted raw sources.' },
      ],
    });
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText('mcp:query mcp:raw:write')).toBeDefined();
    expect(screen.getByText('Add permitted raw sources.')).toBeDefined();
    expect(screen.queryByText('MCPOAuth.queryAndRawWrite')).toBeNull();
    expect(mocks.decideConsent).not.toHaveBeenCalled();
  });

  it.each([
    ['legacy query response', 'mcp:query', 'MCPOAuth.queryOnly'],
    ['legacy query and raw-write response', 'mcp:query mcp:raw:write', 'MCPOAuth.queryAndRawWrite'],
  ])('keeps the localized explanation for a %s', async (_name, scope, explanation) => {
    mocks.readConsent.mockResolvedValueOnce({ ...consent, scope });
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText(scope)).toBeDefined();
    expect(screen.getByText(explanation)).toBeDefined();
  });

  it('renders fixed Daily Brief consent without Project fields or labels', async () => {
    mocks.readConsent.mockResolvedValueOnce({
      transaction_id: transactionId,
      account_email: 'owner@example.com',
      client_name: 'Daily Brief Web',
      resource: 'https://brief.rayer.idv.tw/mcp',
      resource_kind: 'fixed',
      resource_name: 'Daily Brief',
      scope: 'brief:manage',
      scope_details: [{ name: 'brief:manage', description: 'Read and manage Daily Brief reports and annotations in your private account space.' }],
      expires_at: '2026-10-12T02:00:00Z',
    });
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText('Daily Brief Web')).toBeDefined();
    expect(screen.getByText('Daily Brief')).toBeDefined();
    expect(screen.getAllByText('brief:manage').length).toBe(2);
    expect(screen.getByText('Read and manage Daily Brief reports and annotations in your private account space.')).toBeDefined();
    expect(screen.queryByText('MCPOAuth.project')).toBeNull();
    expect(screen.getByRole('button', { name: 'MCPOAuth.allow' })).toBeDefined();
    expect(screen.getByRole('button', { name: 'MCPOAuth.deny' })).toBeDefined();
  });

  it('localizes the account-grant labels in both supported locales', () => {
    expect(enMessages.MCPOAuth.accountGrantsTitle).toBe('Account authorizations');
    expect(zhTWMessages.MCPOAuth.accountGrantsTitle).toBe('帳戶授權');
    expect(enMessages.MCPOAuth.resourceName).toBe('Resource name');
    expect(zhTWMessages.MCPOAuth.resourceName).toBe('資源名稱');
  });

  it('describes bounded URL imports for raw-write consent and setup in both locales', () => {
    const enConsent = enMessages.MCPOAuth.queryAndRawWrite;
    const zhTWConsent = zhTWMessages.MCPOAuth.queryAndRawWrite;

    expect(enConsent).toContain('import URLs only when the extraction is clearly usable');
    expect(enConsent).toContain('re-importing the same URL refreshes its existing entry');
    expect(enConsent).toContain('verify facts');
    expect(enConsent).not.toContain('cannot import URLs');
    expect(zhTWConsent).toContain('只在 URL 擷取內容明確可用時匯入網址');
    expect(zhTWConsent).toContain('重新匯入相同網址會更新既有來源');
    expect(zhTWConsent).toContain('驗證事實');
    expect(zhTWConsent).not.toContain('不能匯入網址');

    expect(enMessages.MCPOAuth.setupIntro).toContain('import URL extractions only when the extracted content is clearly usable');
    expect(zhTWMessages.MCPOAuth.setupIntro).toContain('只有在 URL 擷取內容明確可用時才會匯入網址');
  });

  it('posts an explicit denial and validates the Hermes callback before redirecting', async () => {
    const assign = vi.fn();
    Object.defineProperty(window, 'location', { configurable: true, value: { ...window.location, assign } });
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.deny' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'deny', 'web-session-token'));
    await waitFor(() => expect(assign).toHaveBeenCalledWith('http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque'));
  });

  it('makes a stale decision terminal, localizes the error, and offers a restart path', async () => {
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => decision === 'continue'
      ? Promise.resolve({ redirect_url: '', requires_consent: true })
      : Promise.reject(new MCPOAuthApiError('invalid_request', 410)));
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    expect(await screen.findByRole('alert')).toBeDefined();
    expect(screen.getByRole('alert').textContent).toBe('MCPOAuth.expired');
    expect(screen.queryByRole('button', { name: 'MCPOAuth.allow' })).toBeNull();
    expect(screen.getAllByRole('link', { name: 'MCPOAuth.backToAdvanced' }).length).toBeGreaterThan(0);
  });

  it('ignores a delayed success after the transaction changes', async () => {
    const pending = deferred<{ redirect_url: string }>();
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => decision === 'continue'
      ? Promise.resolve({ redirect_url: '', requires_consent: true })
      : pending.promise);
    const assign = mockLocationAssign();
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'allow', 'web-session-token'));

    rerender(<MCPOAuthConsentClient transactionId={otherTransactionId} />);
    await waitFor(() => expect(mocks.readConsent).toHaveBeenCalledTimes(2));
    await act(async () => {
      pending.resolve({ redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque' });
      await pending.promise;
    });

    expect(assign).not.toHaveBeenCalled();
  });

  it('ignores a delayed error after the transaction changes', async () => {
    const pending = deferred<{ redirect_url: string }>();
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => decision === 'continue'
      ? Promise.resolve({ redirect_url: '', requires_consent: true })
      : pending.promise);
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'allow', 'web-session-token'));

    rerender(<MCPOAuthConsentClient transactionId={otherTransactionId} />);
    await screen.findByRole('button', { name: 'MCPOAuth.allow' });
    await act(async () => {
      pending.reject(new MCPOAuthApiError('invalid_request', 410));
      await pending.promise.catch(() => undefined);
    });

    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('button', { name: 'MCPOAuth.allow' })).toBeDefined();
  });

  it('rejects A-to-B-to-A stale decision completion by operation generation', async () => {
    const pending = deferred<{ redirect_url: string }>();
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => decision === 'continue'
      ? Promise.resolve({ redirect_url: '', requires_consent: true })
      : pending.promise);
    const assign = mockLocationAssign();
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'allow', 'web-session-token'));

    rerender(<MCPOAuthConsentClient transactionId={otherTransactionId} />);
    rerender(<MCPOAuthConsentClient transactionId={transactionId} />);
    await waitFor(() => expect(mocks.readConsent).toHaveBeenCalledTimes(3));
    await screen.findByRole('button', { name: 'MCPOAuth.allow' });
    await act(async () => {
      pending.resolve({ redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque' });
      await pending.promise;
    });

    expect(assign).not.toHaveBeenCalled();
  });

  it('ignores a delayed decision after the consent component unmounts', async () => {
    const pending = deferred<{ redirect_url: string }>();
    mocks.decideConsent.mockImplementation((_transactionId: string, decision: string) => decision === 'continue'
      ? Promise.resolve({ redirect_url: '', requires_consent: true })
      : pending.promise);
    const assign = mockLocationAssign();
    const { unmount } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledWith(transactionId, 'allow', 'web-session-token'));
    unmount();

    await act(async () => {
      pending.resolve({ redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque' });
      await pending.promise;
    });
    expect(assign).not.toHaveBeenCalled();
  });

  it.each([
    [410, 'MCPOAuth.expired'],
    [403, 'MCPOAuth.notAllowed'],
    [404, 'MCPOAuth.notAllowed'],
  ])('clears cached consent and restart-blocks after a terminal load error (%i)', async (status, message) => {
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');

    mocks.readConsent.mockImplementationOnce(() => new Promise(() => {}));
    rerender(<MCPOAuthConsentClient transactionId={otherTransactionId} />);
    await waitFor(() => expect(mocks.readConsent).toHaveBeenCalledTimes(2));
    mocks.readConsent.mockRejectedValueOnce(new MCPOAuthApiError('invalid_request', Number(status)));
    rerender(<MCPOAuthConsentClient transactionId={transactionId} />);

    expect((await screen.findByRole('alert')).textContent).toBe(message);
    expect(screen.queryByRole('button', { name: 'MCPOAuth.allow' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'MCPOAuth.deny' })).toBeNull();
    expect(screen.getByRole('link', { name: 'MCPOAuth.backToAdvanced' })).toBeDefined();
  });

  it('hides old consent details as soon as the signed-in account context changes', async () => {
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText('Hermes Agent')).toBeDefined();
    mocks.user = { id: 'user-two', email: 'other@example.com' };
    mocks.sessionEpoch = 2;
    mocks.readConsent.mockImplementationOnce(() => new Promise(() => {}));
    rerender(<MCPOAuthConsentClient transactionId={transactionId} />);
    await waitFor(() => expect(screen.queryByText('Hermes Agent')).toBeNull());
  });

  it('lists and revokes a project grant, then reads the list back', async () => {
    render(<AdvancedPage />);
    fireEvent.click(screen.getAllByText('MCPOAuth.grantsTitle')[0].closest('summary')!);
    expect(await screen.findByText('Hermes Agent')).toBeDefined();
    expect(mocks.listGrants).toHaveBeenCalledWith('project-one', 'web-session-token');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.revoke' }));
    await waitFor(() => expect(mocks.revokeGrant).toHaveBeenCalledWith('grant-one', 'web-session-token'));
    await waitFor(() => expect(screen.getByText('MCPOAuth.grantRevoked')).toBeDefined());
    expect(mocks.listGrants).toHaveBeenCalledTimes(2);
    const manualDisclosure = screen.getByText('MCPOAuth.manualFallback').closest('details')!;
    fireEvent.click(manualDisclosure.querySelector('summary')!);
    expect(manualDisclosure.querySelector('input')).toBeDefined();
  });

  it('shows an actual dynamic Hermes resource, copies its configuration, and keeps copy failures honest', async () => {
    render(<AdvancedPage />);
    const config = document.querySelector('code')!;
    expect(config.textContent).toContain('http://localhost:18080/mcp/projects/project-one');
    expect(config.textContent).toContain('auth: oauth');
    expect(config.textContent).toContain('scope: "mcp:query mcp:raw:write"');
    expect(config.textContent).not.toContain('Bearer');
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>;
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.copyConfig' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(config.textContent));

    writeText.mockRejectedValueOnce(new Error('clipboard unavailable'));
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.copied' }));
    expect(await screen.findByText('MCPOAuth.copyFailed')).toBeDefined();
  });

  it('updates guide URLs and clears copied feedback when the selected Project changes', async () => {
    const firstProjectId = 'project/a & team';
    mocks.project = { id: firstProjectId, name: 'Project A' };
    const view = render(<AdvancedPage />);
    fireEvent.click(screen.getByRole('tab', { name: 'MCPOAuth.toolCodex' }));
    const firstURL = `http://localhost:18080/mcp/projects/${encodeURIComponent(firstProjectId)}`;
    const firstCode = within(screen.getByRole('tabpanel')).getByText((_, element) => element?.tagName === 'CODE');
    expect(firstCode.textContent).toContain(firstURL);
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.copySetup' }));
    expect(await screen.findByRole('button', { name: 'MCPOAuth.copied' })).toBeDefined();

    mocks.project = { id: 'project-b', name: 'Project B' };
    view.rerender(<AdvancedPage />);
    const secondURL = 'http://localhost:18080/mcp/projects/project-b';
    const secondCode = within(screen.getByRole('tabpanel')).getByText((_, element) => element?.tagName === 'CODE');
    expect(secondCode.textContent).toContain(secondURL);
    expect(secondCode.textContent).not.toContain(firstURL);
    expect(screen.queryByRole('button', { name: 'MCPOAuth.copied' })).toBeNull();
    expect(mocks.resourceURL).toHaveBeenLastCalledWith('project-b');
  });

  it('keeps a real key-name draft when the manual manager is collapsed and reopened', async () => {
    render(<AdvancedPage />);
    const disclosure = screen.getByText('MCPOAuth.manualFallback').closest('details')!;
    fireEvent.click(disclosure.querySelector('summary')!);
    const input = disclosure.querySelector('input') as HTMLInputElement;
    expect(input).toBeDefined();
    fireEvent.change(input, { target: { value: 'Retained draft' } });
    fireEvent.click(disclosure.querySelector('summary')!);
    expect((disclosure.querySelector('input') as HTMLInputElement).value).toBe('Retained draft');
    fireEvent.click(disclosure.querySelector('summary')!);
    expect(disclosure.querySelector('input')).toBe(input);
  });

  it('shows an error and leaves the grant visible when revocation fails', async () => {
    mocks.revokeGrant.mockRejectedValueOnce(new Error('service unavailable'));
    render(<AdvancedPage />);
    fireEvent.click(screen.getAllByText('MCPOAuth.grantsTitle')[0].closest('summary')!);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.revoke' }));
    expect(await screen.findByRole('alert')).toBeDefined();
    expect(screen.getByText('MCPOAuth.revokeFailed')).toBeDefined();
    expect(screen.getByText('Hermes Agent')).toBeDefined();
    expect(mocks.listGrants).toHaveBeenCalledTimes(1);
  });

  it('does not show a late revoke result after the signed-in account changes', async () => {
    const pending = deferred<void>();
    mocks.revokeGrant.mockReturnValueOnce(pending.promise);
    const view = render(<AdvancedPage />);
    fireEvent.click(screen.getAllByText('MCPOAuth.grantsTitle')[0].closest('summary')!);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.revoke' }));
    await waitFor(() => expect(mocks.revokeGrant).toHaveBeenCalledWith('grant-one', 'web-session-token'));

    mocks.user = { id: 'user-two', email: 'other@example.com' };
    mocks.sessionEpoch = 2;
    mocks.accessToken = 'replacement-session-token';
    view.rerender(<AdvancedPage />);
    pending.resolve(undefined);
    await pending.promise;
    await waitFor(() => expect(screen.queryByText('MCPOAuth.grantRevoked')).toBeNull());
  });

  it('remounts Advanced key state across account, session, and Project changes', async () => {
    const view = render(<AdvancedPage />);
    const disclosure = screen.getByText('MCPOAuth.manualFallback').closest('details')!;
    fireEvent.click(disclosure.querySelector('summary')!);
    let name = disclosure.querySelector('input') as HTMLInputElement;
    fireEvent.change(name, { target: { value: 'draft-for-user-one' } });

    mocks.user = { id: 'user-two', email: 'other@example.com' };
    view.rerender(<AdvancedPage />);
    name = disclosure.querySelector('input') as HTMLInputElement;
    expect(name.value).toBe('');
    fireEvent.change(name, { target: { value: 'draft-for-session-one' } });

    mocks.sessionEpoch = 2;
    mocks.accessToken = 'replacement-session-token';
    view.rerender(<AdvancedPage />);
    name = disclosure.querySelector('input') as HTMLInputElement;
    expect(name.value).toBe('');
    fireEvent.change(name, { target: { value: 'draft-for-project-one' } });

    mocks.project = { id: 'project-two', name: 'Project Two' };
    view.rerender(<AdvancedPage />);
    name = disclosure.querySelector('input') as HTMLInputElement;
    expect(name.value).toBe('');
    expect(document.querySelector('code')?.textContent).toContain('/mcp/projects/project-two');
  });
});
