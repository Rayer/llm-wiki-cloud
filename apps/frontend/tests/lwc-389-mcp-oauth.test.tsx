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
  resourceURL: vi.fn(() => 'http://localhost:18080/mcp/projects/project-one'),
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

vi.mock('@/lib/i18n', () => {
  const t = (key: string) => key;
  return { useT: () => ({ t }) };
});

vi.mock('next/link', () => ({ default: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => <a href={href as string} {...props}>{children}</a> }));

vi.mock('@/components/ProjectKeysSection', () => ({ ProjectKeysSection: () => {
  const [secret, setSecret] = React.useState('');
  return <label>One-time project key<input aria-label="fake one-time Project key" value={secret} onChange={(event) => setSecret(event.target.value)} /></label>;
} }));

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

const { act, cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');
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
  mocks.safeReturnURL.mockReturnValue(true);
});

afterEach(() => { cleanup(); vi.clearAllMocks(); });

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
    expect(await screen.findByText('Hermes Agent')).toBeDefined();
    expect(mocks.listGrants).toHaveBeenCalledWith('project-one', 'web-session-token');
    fireEvent.click(screen.getByRole('button', { name: 'MCPOAuth.revoke' }));
    await waitFor(() => expect(mocks.revokeGrant).toHaveBeenCalledWith('grant-one', 'web-session-token'));
    await waitFor(() => expect(screen.getByText('MCPOAuth.grantRevoked')).toBeDefined());
    expect(mocks.listGrants).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('textbox', { name: 'fake one-time Project key' })).toBeDefined();
  });

  it('remounts Advanced key state across account, session, and Project changes', async () => {
    const view = render(<AdvancedPage />);
    let secret = await screen.findByRole('textbox', { name: 'fake one-time Project key' }) as HTMLInputElement;
    fireEvent.change(secret, { target: { value: 'secret-for-user-one' } });

    mocks.user = { id: 'user-two', email: 'other@example.com' };
    view.rerender(<AdvancedPage />);
    secret = await screen.findByRole('textbox', { name: 'fake one-time Project key' }) as HTMLInputElement;
    expect(secret.value).toBe('');
    fireEvent.change(secret, { target: { value: 'secret-for-session-one' } });

    mocks.sessionEpoch = 2;
    mocks.accessToken = 'replacement-session-token';
    view.rerender(<AdvancedPage />);
    secret = await screen.findByRole('textbox', { name: 'fake one-time Project key' }) as HTMLInputElement;
    expect(secret.value).toBe('');
    fireEvent.change(secret, { target: { value: 'secret-for-project-one' } });

    mocks.project = { id: 'project-two', name: 'Project Two' };
    view.rerender(<AdvancedPage />);
    secret = await screen.findByRole('textbox', { name: 'fake one-time Project key' }) as HTMLInputElement;
    expect(secret.value).toBe('');
  });
});
