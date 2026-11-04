import { MCPOAuthConsentClient } from '@/components/MCPOAuthConsentClient';

const transactionPattern = /^lwc_mcp_tx_[A-Za-z0-9_-]{43}$/;

export default async function MCPOAuthConsentPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const params = await searchParams;
  const transactionId = params.transaction_id;
  const valid = Object.keys(params).length === 1 && typeof transactionId === 'string' && transactionPattern.test(transactionId);

  return <MCPOAuthConsentClient transactionId={valid ? transactionId : ''} />;
}
