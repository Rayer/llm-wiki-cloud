'use client';

import { FormEvent, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { useLocale } from '@/lib/i18n';
import { decideCLIPairing } from '@/lib/cli-auth';

export function CLIPairingClient({ initialUserCode }: { initialUserCode: string }) {
  const { accessToken, hydrated, isDemoSession, refreshAccessToken, user } = useAuth();
  const { t } = useLocale();
  const [userCode, setUserCode] = useState(initialUserCode.toUpperCase());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<'approved' | 'denied' | null>(null);

  const normalizedCode = userCode.replace(/[\s-]/g, '').toUpperCase();
  const codeIsValid = /^[23456789ABCDEFGHJKMNPQRSTVWXYZ]{8}$/.test(normalizedCode);

  const decide = async (decision: 'approve' | 'deny') => {
    if (!accessToken || !codeIsValid || busy || result) return;
    setBusy(true);
    setError('');
    try {
      await decideCLIPairing({ accessToken, refreshAccessToken }, normalizedCode, decision);
      setResult(decision === 'approve' ? 'approved' : 'denied');
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : t('CLIPairing.actionFailed'));
    } finally {
      setBusy(false);
    }
  };

  const submitCode = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setError('');
  };

  if (!hydrated) {
    return <p className="text-sm text-zinc-400">{t('CLIPairing.loading')}</p>;
  }

  return (
    <div className="mx-auto flex min-h-[70vh] max-w-xl items-center px-4 py-10">
      <section className="w-full rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8">
        <p className="font-mono text-xs uppercase tracking-[0.2em] text-emerald-300">LLM Wiki Cloud</p>
        <h1 className="mt-3 text-2xl font-semibold text-white">{t('CLIPairing.title')}</h1>
        <p className="mt-3 text-sm leading-6 text-zinc-300">{t('CLIPairing.description')}</p>

        {!accessToken || !user ? (
          <p className="mt-6 rounded-lg border border-amber-300/20 bg-amber-300/5 p-4 text-sm text-amber-100">
            {t('CLIPairing.signInHint')}
          </p>
        ) : isDemoSession ? (
          <p className="mt-6 rounded-lg border border-amber-300/20 bg-amber-300/5 p-4 text-sm text-amber-100">
            {t('CLIPairing.demoDisabled')}
          </p>
        ) : result ? (
          <p role="status" className="mt-6 rounded-lg border border-emerald-300/20 bg-emerald-300/5 p-4 text-sm text-emerald-100">
            {result === 'approved' ? t('CLIPairing.approved') : t('CLIPairing.denied')}
          </p>
        ) : (
          <form onSubmit={submitCode} className="mt-6 space-y-5">
            <label className="block text-sm font-medium text-zinc-200">
              {t('CLIPairing.codeLabel')}
              <input
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                maxLength={10}
                value={userCode}
                onChange={(event) => setUserCode(event.target.value.toUpperCase())}
                aria-describedby="cli-pairing-help"
                className="mt-2 min-h-12 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 font-mono text-lg tracking-[0.18em] text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400"
              />
            </label>
            <p id="cli-pairing-help" className="text-sm leading-6 text-zinc-400">
              {t('CLIPairing.signedInAs', { email: user.email })}
            </p>
            {error ? <p role="alert" className="text-sm text-red-300">{error}</p> : null}
            <div className="grid gap-3 sm:grid-cols-2">
              <button
                type="button"
                disabled={!codeIsValid || busy}
                onClick={() => void decide('approve')}
                className="min-h-11 rounded-lg bg-emerald-300 px-4 py-3 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {busy ? t('CLIPairing.processing') : t('CLIPairing.approve')}
              </button>
              <button
                type="button"
                disabled={!codeIsValid || busy}
                onClick={() => void decide('deny')}
                className="min-h-11 rounded-lg border border-white/15 px-4 py-3 text-sm font-medium text-zinc-200 hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {t('CLIPairing.deny')}
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
