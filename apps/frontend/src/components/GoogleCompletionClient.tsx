'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { useLocale } from '@/lib/i18n';
import {
  cancelGoogleLink,
  confirmGoogleLink,
  GOOGLE_CANCELLED_COPY,
  GoogleAuthError,
  readGoogleCompletionResult,
  readGoogleLinkCompletion,
  type GoogleLinkCompletion,
} from '@/lib/google-auth';

type CompletionState = 'completing' | 'confirming' | 'failed';

function tokenUserId(token: string): string | undefined {
  try {
    const payload = token.split('.')[1];
    if (!payload) return undefined;
    const decoded = JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/'))) as { sub?: unknown };
    return typeof decoded.sub === 'string' && decoded.sub ? decoded.sub : undefined;
  } catch {
    return undefined;
  }
}

export function GoogleCompletionClient() {
  const router = useRouter();
  const { accessToken, hydrated, getHydratedSession, sessionEpoch } = useAuth();
  const { t } = useLocale();
  const currentEpochRef = useRef(sessionEpoch);
  const currentTokenRef = useRef(accessToken);
  const startedRef = useRef(false);
  const mountedRef = useRef(false);
  useEffect(() => {
    currentEpochRef.current = sessionEpoch;
    currentTokenRef.current = accessToken;
  }, [accessToken, sessionEpoch]);
  const [state, setState] = useState<CompletionState>('completing');
  const [pending, setPending] = useState<GoogleLinkCompletion | null>(null);
  const [error, setError] = useState<GoogleAuthError | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    mountedRef.current = true;
    if (!hydrated || startedRef.current) return () => { mountedRef.current = false; };
    startedRef.current = true;
    void (async () => {
      try {
        const [result, session] = await Promise.all([readGoogleCompletionResult(), getHydratedSession()]);
        if (!mountedRef.current || (session && !session.isCurrent())) return;
        if (result.status === 'failure' || result.status === 'cancelled') {
          setError(new GoogleAuthError(result.error || 'Unable to continue with Google sign-in.', 200, result.support_ref));
          setState('failed');
          return;
        }

        if (!session) {
          setError(new GoogleAuthError('Unable to continue with Google sign-in.', 502, result.support_ref));
          setState('failed');
          return;
        }
        const token = session.accessToken;
        if (result.status === 'success') {
          if (result.jit_provisioned) {
            window.dispatchEvent(new CustomEvent('lwc-google-jit-completed', { detail: { userId: tokenUserId(token) } }));
          }
          router.replace('/');
          return;
        }

        const completion = await readGoogleLinkCompletion(token);
        if (!mountedRef.current || !session.isCurrent()) return;
        setPending(completion);
        setState('confirming');
      } catch (completionError) {
        if (!mountedRef.current) return;
        const failure = completionError instanceof GoogleAuthError
          ? completionError
          : new GoogleAuthError('Unable to continue with Google sign-in.', 502);
        setError(failure);
        setState('failed');
      }
    })();
    return () => { mountedRef.current = false; };
  // Completion is one-time; the provider checks session identity without waiting for a render.
  }, [hydrated, getHydratedSession, router]);

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
        setError(new GoogleAuthError(result.error || GOOGLE_CANCELLED_COPY, 200, result.support_ref || 'unavailable'));
        setState('failed');
        setBusy(false);
        return;
      }
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
