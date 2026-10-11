import { MCPOAuthConsentClient } from '@/components/MCPOAuthConsentClient';

export default async function MCPOAuthConsentPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const params = await searchParams;
  const transactionId = params.transaction_id;
  return <MCPOAuthConsentClient transactionId={typeof transactionId === 'string' ? transactionId : ''} />;
}
