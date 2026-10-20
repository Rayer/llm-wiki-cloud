import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  accessToken: 'web-session-token',
  sessionEpoch: 1,
  user: { id: 'user-one', email: 'owner@example.com' },
  project: { id: 'project-one', name: 'Research Project' },
  readConsent: vi.fn(),
  decideConsent: vi.fn(),
  listGrants: vi.fn(),
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
  mocks.decideConsent.mockReset().mockResolvedValue({ redirect_url: 'http://127.0.0.1:4321/oauth/callback?code=opaque&state=opaque' });
  mocks.listGrants.mockReset().mockResolvedValue([{
    id: 'grant-one', client_name: 'Hermes Agent', project_id: 'project-one',
    resource: consent.resource, scope: 'mcp:query', status: 'active',
    created_at: '2026-10-01T00:00:00Z', expires_at: '2026-10-31T00:00:00Z',
  }]);
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
  it('shows the actual client and Project without deciding until the user acts', async () => {
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

  it('explains the exact raw write permission when the client requests it', async () => {
    mocks.readConsent.mockResolvedValueOnce({ ...consent, scope: 'mcp:query mcp:raw:write' });
    render(<MCPOAuthConsentClient transactionId={transactionId} />);
    expect(await screen.findByText('mcp:query mcp:raw:write')).toBeDefined();
    expect(screen.getByText('MCPOAuth.queryAndRawWrite')).toBeDefined();
    expect(screen.queryByText('MCPOAuth.queryOnly')).toBeNull();
    expect(mocks.decideConsent).not.toHaveBeenCalled();
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
    mocks.decideConsent.mockRejectedValueOnce(new MCPOAuthApiError('invalid_request', 410));
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
    mocks.decideConsent.mockReturnValueOnce(pending.promise);
    const assign = mockLocationAssign();
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(1));

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
    mocks.decideConsent.mockReturnValueOnce(pending.promise);
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(1));

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
    mocks.decideConsent.mockReturnValueOnce(pending.promise);
    const assign = mockLocationAssign();
    const { rerender } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(1));

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
    mocks.decideConsent.mockReturnValueOnce(pending.promise);
    const assign = mockLocationAssign();
    const { unmount } = render(<MCPOAuthConsentClient transactionId={transactionId} />);
    await screen.findByText('Hermes Agent');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.allow' }));
    await waitFor(() => expect(mocks.decideConsent).toHaveBeenCalledTimes(1));
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
