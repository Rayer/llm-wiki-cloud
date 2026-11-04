import { describe, expect, it } from 'vitest';
import { portalConsentURL } from '@/components/PortalConsentRedirect';
import MCPOAuthConsentPage from '@/app/(workspace)/oauth/consent/page';
import { MCPOAuthConsentClient } from '@/components/MCPOAuthConsentClient';

const transactionId = `lwc_mcp_tx_${'A'.repeat(43)}`;

describe('GAT-42 portal handoff stays deferred', () => {
  it('builds the fixed Auth consent path with only the canonical transaction id', () => {
    const destination = new URL(portalConsentURL('https://auth.dev.example.test', transactionId));
    expect(destination.origin).toBe('https://auth.dev.example.test');
    expect(destination.pathname).toBe('/oauth/consent');
    expect([...destination.searchParams.entries()]).toEqual([['transaction_id', transactionId]]);
  });

  it('rejects invalid transaction ids and non-origin Auth config', () => {
    expect(() => portalConsentURL('https://auth.dev.example.test', `${transactionId}&next=/admin`)).toThrow();
    expect(() => portalConsentURL('https://auth.dev.example.test/nested', transactionId)).toThrow();
  });

  it('mounts the existing LWC consent client for the canonical producer transaction', async () => {
    const page = await MCPOAuthConsentPage({ searchParams: Promise.resolve({ transaction_id: transactionId }) });
    expect(page.type).toBe(MCPOAuthConsentClient);
    expect(page.props).toEqual({ transactionId });
  });

  it.each([
    ['missing', {}],
    ['malformed', { transaction_id: 'invalid' }],
    ['repeated', { transaction_id: [transactionId, transactionId] }],
    ['extra parameter', { transaction_id: transactionId, next: '/admin' }],
  ])('renders the existing client with an invalid sentinel for %s input', async (_name, searchParams) => {
    const page = await MCPOAuthConsentPage({ searchParams: Promise.resolve(searchParams) });
    expect(page.type).toBe(MCPOAuthConsentClient);
    expect(page.props).toEqual({ transactionId: '' });
  });
});
