'use client';

import { FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import { getPublicConfig } from '@/lib/api';
import { AnnouncementModal } from './AnnouncementModal';
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
  const [announcementDigest, setAnnouncementDigest] = useState<string | null>(null);
  const [announcementOpen, setAnnouncementOpen] = useState(false);
  const [announcementDismiss, setAnnouncementDismiss] = useState(false);
  const [announcementAuto, setAnnouncementAuto] = useState(false);
  const autoOpenedDigest = useRef<string | null>(null);
  const announcementTriggerRef = useRef<HTMLButtonElement>(null);
  const emailRef = useRef<HTMLInputElement>(null);
  const dismissKey = 'llm-wiki:announcement-dismissed-digest';

  useEffect(() => {
    if (!loginOpen) {
      autoOpenedDigest.current = null;
      /* eslint-disable react-hooks/set-state-in-effect -- reset hidden modal state at the login lifecycle boundary. */
      setAnnouncementOpen(false);
      setAnnouncementMarkdown(null);
      setAnnouncementDigest(null);
      setAnnouncementAuto(false);
      setAnnouncementDismiss(false);
      /* eslint-enable react-hooks/set-state-in-effect */
      return;
    }
    let cancelled = false;
    void getPublicConfig({ refresh: true })
      .then((config) => {
        if (!cancelled) {
          setRegistrationEnabled(config.registration_enabled);
          setAnnouncementMarkdown(config.announcement_markdown ?? null);
          const digest = typeof config.announcement_digest === 'string' && /^sha256:[0-9a-f]{64}$/.test(config.announcement_digest) ? config.announcement_digest : null;
          setAnnouncementDigest(digest);
          if (config.announcement_markdown?.trim() && digest && autoOpenedDigest.current !== digest) {
            let dismissed = false;
            try { dismissed = localStorage.getItem(dismissKey) === digest; } catch { /* storage is optional */ }
            autoOpenedDigest.current = digest;
            if (!dismissed) {
              setAnnouncementDismiss(false);
              setAnnouncementAuto(true);
              setAnnouncementOpen(true);
            }
          }
        }
      })
      .catch(() => {
        if (!cancelled) {
          setRegistrationEnabled(false);
          setAnnouncementMarkdown(null);
          setAnnouncementDigest(null);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [loginOpen]);

  const openAnnouncement = useCallback(() => {
    setAnnouncementDismiss(false);
    setAnnouncementAuto(false);
    setAnnouncementOpen(true);
  }, []);

  const closeAnnouncement = useCallback(() => {
    if (announcementDismiss && announcementDigest) {
      try { localStorage.setItem(dismissKey, announcementDigest); } catch { /* storage is optional */ }
    }
    const wasAuto = announcementAuto;
    setAnnouncementOpen(false);
    setAnnouncementAuto(false);
    queueMicrotask(() => (wasAuto ? emailRef.current : announcementTriggerRef.current)?.focus());
  }, [announcementAuto, announcementDigest, announcementDismiss]);

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
        inert={announcementOpen || undefined}
        aria-hidden={announcementOpen ? 'true' : undefined}
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

        {announcementMarkdown?.trim() ? (
          <button ref={announcementTriggerRef} type="button" onClick={openAnnouncement} className="mt-5 w-full rounded-lg border border-emerald-300/20 bg-emerald-300/5 px-4 py-3 text-left text-sm font-medium text-emerald-200 hover:bg-emerald-300/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
            {t('Announcement.open')}
          </button>
        ) : null}

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.email')}
            <input
              ref={emailRef} type="email" autoComplete="email" required
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
      {announcementOpen && announcementMarkdown?.trim() ? (
        <AnnouncementModal
          markdown={announcementMarkdown}
          title={t('Announcement.title')}
          closeLabel={t('Announcement.close')}
          dismissLabel={t('Announcement.dismiss')}
          checked={announcementDismiss}
          onCheckedChange={setAnnouncementDismiss}
          onClose={closeAnnouncement}
        />
      ) : null}
    </div>
  );
}
