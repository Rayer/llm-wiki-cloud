'use client';

import { useEffect, useState } from 'react';
import { loadRuntimeConfig } from '@/lib/runtime-config';

const transactionPattern = /^lwc_mcp_tx_[A-Za-z0-9_-]{43}$/;

export function portalConsentURL(authURL: string, transactionId: string): string {
  if (!transactionPattern.test(transactionId)) throw new Error('Invalid consent transaction');
  const configured = new URL(authURL);
  if (configured.pathname !== '/' || configured.search || configured.hash || configured.username || configured.password) {
    throw new Error('Auth URL must be an origin');
  }
  const destination = new URL('/oauth/consent', configured.origin);
  destination.searchParams.set('transaction_id', transactionId);
  return destination.toString();
}

export function PortalConsentRedirect({ transactionId }: { transactionId: string | null }) {
  const [error, setError] = useState(false);

  useEffect(() => {
    if (!transactionId || !transactionPattern.test(transactionId)) return;
    let active = true;
    void loadRuntimeConfig().then(config => {
      if (!active) return;
      window.location.replace(portalConsentURL(config.auth_url, transactionId));
    }).catch(() => {
      if (active) setError(true);
    });
    return () => { active = false; };
  }, [transactionId]);

  if (!transactionId || !transactionPattern.test(transactionId)) {
    return (
      <main className="mx-auto max-w-2xl p-8" role="status" aria-live="polite">
        <h1 className="text-xl font-semibold">無法確認這筆授權</h1>
        <p className="mt-3">請回到發起連線的產品重新開始。</p>
      </main>
    );
  }

  if (error) {
    return (
      <main className="mx-auto max-w-2xl p-8" role="alert" aria-live="assertive">
        <h1 className="text-xl font-semibold">目前無法開啟 Auth 授權頁</h1>
        <p className="mt-3">請回到發起連線的產品重新開始。</p>
      </main>
    );
  }

  return <main className="p-8" role="status" aria-live="polite">正在開啟 Auth 授權頁…</main>;
}
