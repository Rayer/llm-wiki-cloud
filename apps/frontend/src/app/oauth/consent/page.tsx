import { PortalConsentRedirect } from '@/components/PortalConsentRedirect';
import { isCanonicalConsentTransactionId } from '@/lib/consent-transaction';

export default async function MCPOAuthConsentPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const params = await searchParams;
  const transactionId = params.transaction_id;
  const valid = Object.keys(params).length === 1 && isCanonicalConsentTransactionId(transactionId);

  return <PortalConsentRedirect transactionId={valid ? transactionId : null} />;
}
