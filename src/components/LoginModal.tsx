'use client';

import { FormEvent, useCallback, useEffect, useState } from 'react';
import { getPublicConfig } from '@/lib/api';
import { AnnouncementBoard } from './AnnouncementBoard';
import { useLocale } from '@/lib/i18n';
import { RegisterModal } from './RegisterModal';
import { useWorkspace } from './WorkspaceProvider';

export function LoginModal() {
  const { loginOpen, signIn, signInAsDemo } = useWorkspace();
  const { t } = useLocale();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [registerOpen, setRegisterOpen] = useState(false);
  // Fail-closed until public config says open.
  const [registrationEnabled, setRegistrationEnabled] = useState(false);
  const [announcementMarkdown, setAnnouncementMarkdown] = useState<string | null>(null);

  useEffect(() => {
    if (!loginOpen) return;
    let cancelled = false;
    void getPublicConfig()
      .then((config) => {
        if (!cancelled) {
          setRegistrationEnabled(config.registration_enabled);
          setAnnouncementMarkdown(config.announcement_markdown ?? null);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setRegistrationEnabled(false);
          setAnnouncementMarkdown(null);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [loginOpen]);

  const handleDemo = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      await signInAsDemo('demo@llm-wiki.dev', 'demo123456');
    } catch {
      setError(t('Login.demoError'));
    } finally {
      setLoading(false);
    }
  }, [signInAsDemo, t]);

  if (!loginOpen) return null;

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      await signIn(email.trim(), password);
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : t('Login.error'));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm">
      <div
        className="max-h-[calc(100dvh-2rem)] w-full max-w-md overflow-y-auto rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8"
        role="dialog"
        aria-modal="true"
        aria-labelledby="login-title"
      >
        <div className="text-sm font-semibold uppercase tracking-[0.24em] text-emerald-300">
          {t('Login.brand')}
        </div>
        <h1 id="login-title" className="mt-3 text-3xl font-semibold text-white">
          {t('Login.title')}
        </h1>
        <p className="mt-2 text-sm leading-6 text-zinc-400">
          {t('Login.subtitle')}
        </p>

        <AnnouncementBoard markdown={announcementMarkdown} />

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.email')}
            <input
              type="email" autoComplete="email" required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition placeholder:text-zinc-600 focus:border-emerald-300"
              placeholder="you@example.com"
            />
          </label>
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.password')}
            <input
              type="password" autoComplete="current-password" required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition focus:border-emerald-300"
            />
          </label>
          {error ? (
            <p className="rounded-lg border border-red-400/20 bg-red-400/10 px-3 py-2 text-sm text-red-200">
              {error}
            </p>
          ) : null}
          <button
            type="submit" disabled={loading}
            className="w-full rounded-lg bg-emerald-300 px-4 py-3 font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {loading ? t('Login.signingIn') : t('Login.signIn')}
          </button>
          <button
            type="button" onClick={handleDemo}
            className="w-full rounded-lg border border-emerald-400/20 bg-emerald-400/10 px-4 py-3 text-sm font-medium text-emerald-200 transition hover:bg-emerald-400/20"
          >
            {t('Login.tryDemo')}
          </button>
          {registrationEnabled === true ? (
            <button
              type="button" onClick={() => setRegisterOpen(true)}
              className="w-full rounded-lg border border-white/10 bg-transparent px-4 py-3 text-sm font-medium text-zinc-300 transition hover:bg-white/10"
            >
              {t('Login.signUp')}
            </button>
          ) : null}
        </form>
      </div>
      {registrationEnabled === true && registerOpen && (
        <RegisterModal
          t={t}
          onClose={() => setRegisterOpen(false)}
          onSuccess={() => setRegisterOpen(false)}
        />
      )}
    </div>
  );
}
