'use client';

import { useEffect, useState, type ReactNode } from 'react';
import { loadRuntimeConfig } from '@/lib/runtime-config';

export function RuntimeConfigBoundary({ children }: { children: ReactNode }) {
  const [attempt, setAttempt] = useState(0);
  const [status, setStatus] = useState<{ ready: boolean; error: string | null }>({ ready: false, error: null });

  useEffect(() => {
    let active = true;
    void loadRuntimeConfig().then(() => {
      if (active) setStatus({ ready: true, error: null });
    }).catch((error: unknown) => {
      if (active) {
        setStatus({
          ready: false,
          error: error instanceof Error ? error.message : 'Runtime config load failed.',
        });
      }
    });
    return () => { active = false; };
  }, [attempt]);

  if (status.error) {
    return (
      <main role="alert" aria-live="assertive" className="p-6">
        <p>{status.error}</p>
        <div className="mt-4 flex gap-3">
          <button type="button" onClick={() => {
            setStatus({ ready: false, error: null });
            setAttempt((value) => value + 1);
          }}>Retry</button>
          <button type="button" onClick={() => window.location.reload()}>Reload</button>
        </div>
      </main>
    );
  }

  if (!status.ready) {
    return <main role="status" aria-live="polite" aria-busy="true" className="p-6">Loading runtime configuration…</main>;
  }

  return children;
}
