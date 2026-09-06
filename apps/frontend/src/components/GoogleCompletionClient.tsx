'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { useLocale } from '@/lib/i18n';
import {
  cancelGoogleLink,
  clearGoogleLinkIntent,
  createGoogleSupportReference,
  GOOGLE_CANCELLED_COPY,
  GoogleAuthError,
  hasGoogleLinkIntent,
  readGoogleLinkCompletion,
  confirmGoogleLink,
  type GoogleLinkCompletion,
} from '@/lib/google-auth';

type CompletionState = 'completing' | 'confirming' | 'failed';

export function GoogleCompletionClient() {
  const router = useRouter();
  const { accessToken, hydrated, refreshAccessToken, sessionEpoch } = useAuth();
  const { t } = useLocale();
  const currentEpochRef = useRef(sessionEpoch);
  const currentTokenRef = useRef(accessToken);
  const startedRef = useRef(false);
  useEffect(() => { currentEpochRef.current = sessionEpoch; currentTokenRef.current = accessToken; }, [accessToken, sessionEpoch]);
  const [state, setState] = useState<CompletionState>('completing');
  const [pending, setPending] = useState<GoogleLinkCompletion | null>(null);
  const [error, setError] = useState<GoogleAuthError | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!hydrated || startedRef.current) return;
    startedRef.current = true;
    let active = true;
    const startedEpoch = sessionEpoch;
    const linkIntent = hasGoogleLinkIntent();
    void (async () => {
      try {
        // AuthProvider hydrates the callback refresh cookie before exposing
        // `hydrated`; avoid rotating the same refresh session a second time.
        const refreshedToken = accessToken ?? await refreshAccessToken();
        if (!active || currentEpochRef.current !== startedEpoch) return;
        const token = refreshedToken ?? (linkIntent ? accessToken : null);
        if (!token) throw new GoogleAuthError('Unable to continue with Google sign-in.', 502);
        if (linkIntent) {
          const completion = await readGoogleLinkCompletion(token);
          if (!active || currentEpochRef.current !== startedEpoch) return;
          setPending(completion);
          setState('confirming');
          return;
        }
        clearGoogleLinkIntent();
        router.replace('/');
      } catch (completionError) {
        if (!active) return;
        const failure = completionError instanceof GoogleAuthError
          ? completionError
          : new GoogleAuthError('Unable to continue with Google sign-in.', 502);
        setError(failure);
        setState('failed');
      }
    })();
    return () => { active = false; };
  }, [accessToken, hydrated, refreshAccessToken, router, sessionEpoch]);

  const decideLink = async (confirm: boolean) => {
    if (!pending || !accessToken || busy || currentTokenRef.current !== accessToken) return;
    const operationEpoch = currentEpochRef.current;
    const operationToken = accessToken;
    setBusy(true);
    try {
      const result = confirm
        ? await confirmGoogleLink(accessToken, pending.confirmation_id)
        : await cancelGoogleLink(accessToken, pending.confirmation_id);
      if (currentEpochRef.current !== operationEpoch || currentTokenRef.current !== operationToken) return;
      if (result.status !== (confirm ? 'linked' : 'cancelled')) throw new Error('Invalid Google link response.');
      if (!confirm) {
        clearGoogleLinkIntent();
        setError(new GoogleAuthError(GOOGLE_CANCELLED_COPY, 200, createGoogleSupportReference()));
        setState('failed');
        setBusy(false);
        return;
      }
      if (confirm) {
        window.dispatchEvent(new CustomEvent('lwc-google-link-confirmed', { detail: { email: pending.provider_email } }));
      }
      clearGoogleLinkIntent();
      router.replace('/');
    } catch (decisionError) {
      setError(decisionError instanceof GoogleAuthError ? decisionError : new GoogleAuthError('Unable to link this Google account.', 502));
      setState('failed');
      setBusy(false);
    }
  };

  return (
    <main className="flex min-h-dvh items-center justify-center px-4 py-8">
      <section className="w-full max-w-md rounded-2xl border border-white/10 bg-[#151515] p-6 text-center shadow-2xl sm:p-8" aria-live="polite">
        {state === 'completing' ? <><p className="text-lg font-semibold text-white">{t('GoogleCompletion.completing')}</p><p className="mt-2 text-sm text-zinc-400">{t('GoogleCompletion.completingHint')}</p></> : null}
        {state === 'confirming' && pending ? (
          <>
            <h1 className="text-xl font-semibold text-white">{t('GoogleCompletion.confirmTitle')}</h1>
            <p className="mt-3 text-left text-sm leading-6 text-zinc-300">{t('GoogleCompletion.confirmHint')}</p>
            <dl className="mt-4 space-y-3 text-left text-sm">
              <div><dt className="text-zinc-500">{t('AccountSettings.primaryEmail')}</dt><dd className="break-all font-medium text-white">{pending.current_email}</dd></div>
              <div><dt className="text-zinc-500">{t('AccountSettings.googleEmail')}</dt><dd className="break-all font-medium text-white">{pending.provider_email}</dd></div>
            </dl>
            <div className="mt-6 flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <button type="button" disabled={busy} onClick={() => void decideLink(false)} className="min-h-11 rounded-lg border border-white/10 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/10 disabled:opacity-60">{t('GoogleCompletion.cancel')}</button>
              <button type="button" disabled={busy} onClick={() => void decideLink(true)} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black hover:bg-emerald-200 disabled:opacity-60">{busy ? t('GoogleCompletion.saving') : t('GoogleCompletion.confirm')}</button>
            </div>
          </>
        ) : null}
        {state === 'failed' && error ? (
          <>
            <p role="alert" className="text-lg font-semibold text-white">{error.message}</p>
            <div className="mt-4 text-left text-sm text-zinc-300"><span className="text-zinc-500">{t('GoogleCompletion.supportReference')}</span> <code className="break-all select-all text-emerald-200">{error.supportRef}</code><button type="button" className="mt-2 min-h-11 rounded border border-white/10 px-3 text-xs text-zinc-200 hover:bg-white/10" onClick={() => { void navigator.clipboard?.writeText(error.supportRef); }}>{t('GoogleCompletion.copyReference')}</button></div>
            <button type="button" onClick={() => router.replace('/')} className="mt-6 min-h-11 w-full rounded-lg border border-white/10 px-4 py-2.5 text-sm font-medium text-zinc-200 hover:bg-white/10">{t('GoogleCompletion.return')}</button>
          </>
        ) : null}
      </section>
    </main>
  );
}
