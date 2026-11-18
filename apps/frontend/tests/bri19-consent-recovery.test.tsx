import { describe, expect, it } from 'vitest';
import { PortalConsentRedirect } from '@/components/PortalConsentRedirect';
import MCPOAuthConsentPage from '@/app/oauth/consent/page';

const transactionId = `lwc_mcp_tx_${'R'.repeat(43)}`;

describe('BRI-19 / GAT-42 LWC consent compatibility', () => {
  it('hands a canonical transaction to the existing Auth-domain redirect shim', async () => {
    const page = await MCPOAuthConsentPage({ searchParams: Promise.resolve({ transaction_id: transactionId }) });
    expect(page.type).toBe(PortalConsentRedirect);
    expect(page.props).toEqual({ transactionId });
  });

  it.each([
    ['missing', {}],
    ['malformed', { transaction_id: 'invalid' }],
    ['repeated', { transaction_id: [transactionId, transactionId] }],
    ['extra query', { transaction_id: transactionId, return_to: 'https://evil.example/' }],
  ])('does not start LWC auth recovery for %s input', async (_label, searchParams) => {
    const page = await MCPOAuthConsentPage({ searchParams: Promise.resolve(searchParams) });
    expect(page.type).toBe(PortalConsentRedirect);
    expect(page.props).toEqual({ transactionId: null });
  });
});
