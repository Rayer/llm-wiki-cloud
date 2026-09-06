'use client';

import { FormEvent, useEffect, useRef, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { beginGoogleLink } from '@/lib/google-auth';
import { useLocale } from '@/lib/i18n';

export function AccountSettingsModal({ onClose, linkedGoogleEmail = null }: { onClose: () => void; linkedGoogleEmail?: string | null }) {
  const { user } = useAuth();
  const { t } = useLocale();
  const [linkOpen, setLinkOpen] = useState(false);
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const passwordRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (linkOpen) passwordRef.current?.focus();
  }, [linkOpen]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !loading) onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [loading, onClose]);

  if (!user) return null;

  const handleLink = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (loading || !password) return;
    setLoading(true);
    setError('');
    try {
      await beginGoogleLink(password);
    } catch (linkError) {
      setError(linkError instanceof Error ? linkError.message : 'Unable to link this Google account.');
      setPassword('');
      setLoading(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-settings-title"
        className="w-full max-w-lg rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 id="account-settings-title" className="text-2xl font-semibold text-white">{t('AccountSettings.title')}</h2>
            <p className="mt-1 text-sm text-zinc-400">{t('AccountSettings.subtitle')}</p>
          </div>
          <button type="button" onClick={onClose} className="min-h-11 min-w-11 rounded-md p-2 text-zinc-400 hover:bg-white/10 hover:text-white" aria-label="Close account settings">×</button>
        </div>

        <dl className="mt-6 space-y-4 text-sm">
          <div className="rounded-lg border border-white/10 bg-black/20 p-4">
            <dt className="text-zinc-400">{t('AccountSettings.primaryEmail')}</dt>
            <dd className="mt-1 break-all font-medium text-white">{user.email}</dd>
            <p className="mt-2 text-xs leading-5 text-zinc-500">{t('AccountSettings.primaryHint')}</p>
          </div>
          <div className="rounded-lg border border-white/10 bg-black/20 p-4">
            <dt className="text-zinc-400">{t('AccountSettings.googleEmail')}</dt>
            <dd className="mt-1 break-all text-zinc-300">{linkedGoogleEmail ?? t('AccountSettings.googleNotLinked')}</dd>
            <p className="mt-2 text-xs leading-5 text-zinc-500">{t('AccountSettings.googleHint')}</p>
          </div>
        </dl>

        {!linkOpen ? (
          <button type="button" onClick={() => setLinkOpen(true)} className="mt-6 min-h-11 w-full rounded-lg bg-emerald-300 px-4 py-3 font-semibold text-black hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">
            {t('AccountSettings.linkGoogle')}
          </button>
        ) : (
          <form onSubmit={handleLink} className="mt-6 space-y-4 rounded-lg border border-emerald-300/20 bg-emerald-300/5 p-4">
            <p className="text-sm leading-6 text-zinc-300">{t('AccountSettings.reauthHint')}</p>
            <label className="block text-sm font-medium text-zinc-300">
              {t('AccountSettings.currentPassword')}
              <input ref={passwordRef} type="password" autoComplete="current-password" required value={password} onChange={(event) => setPassword(event.target.value)} className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400" />
            </label>
            {error ? <p role="alert" className="text-sm text-red-300">{error}</p> : null}
            <div className="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
              <button type="button" disabled={loading} onClick={() => { setLinkOpen(false); setPassword(''); setError(''); }} className="min-h-11 rounded-lg border border-white/10 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/10 disabled:opacity-60">{t('AccountSettings.cancel')}</button>
              <button type="submit" disabled={loading || !password} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60">{loading ? t('AccountSettings.openingGoogle') : t('AccountSettings.continue')}</button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}
