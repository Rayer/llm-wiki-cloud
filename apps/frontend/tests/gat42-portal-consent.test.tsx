import { describe, expect, it } from 'vitest';
import { portalConsentURL } from '@/components/PortalConsentRedirect';

const transactionId = `lwc_mcp_tx_${'A'.repeat(43)}`;

describe('GAT-42 old consent route shim', () => {
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
});
