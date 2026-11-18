const CONSENT_TRANSACTION_PREFIX = 'lwc_mcp_tx_';
const CONSENT_TRANSACTION_SUFFIX = /^[A-Za-z0-9_-]{43}$/;

export function isCanonicalConsentTransactionId(value: unknown): value is string {
  if (typeof value !== 'string' || !value.startsWith(CONSENT_TRANSACTION_PREFIX)) return false;
  const suffix = value.slice(CONSENT_TRANSACTION_PREFIX.length);
  if (!CONSENT_TRANSACTION_SUFFIX.test(suffix)) return false;
  try {
    const standardBase64 = suffix.replace(/-/g, '+').replace(/_/g, '/');
    const decoded = atob(`${standardBase64}=`);
    const canonical = btoa(decoded).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    return decoded.length === 32 && canonical === suffix;
  } catch {
    return false;
  }
}
