'use client';

import { FormEvent, useEffect, useRef, useState } from 'react';
import { RegistrationDisabledError } from '@/lib/auth-core';
import { useWorkspace } from './WorkspaceProvider';

interface Props {
  onClose: () => void;
  onSuccess: () => void;
  t: (key: string) => string;
}

export function RegisterModal({ onClose, onSuccess, t }: Props) {
  const { register } = useWorkspace();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const emailRef = useRef<HTMLInputElement>(null);
  const onCloseRef = useRef(onClose);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    emailRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseRef.current();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, []);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    try {
      await register(email.trim(), password);
      onSuccess();
    } catch (err) {
      if (err instanceof RegistrationDisabledError) {
        setError(t('Register.disabled'));
      } else {
        setError(err instanceof Error ? err.message : t('Register.error'));
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm" onClick={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="register-title"
        className="w-full max-w-md rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8"
        onClick={(event) => event.stopPropagation()}
      >
        <h2 id="register-title" className="text-2xl font-semibold text-white">{t('Register.title')}</h2>
        <p className="mt-1 text-sm text-zinc-400">{t('Register.subtitle')}</p>
        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.email')}
            <input
              ref={emailRef} type="email" autoComplete="email" required
              value={email}
              onChange={e => setEmail(e.target.value)}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition placeholder:text-zinc-400 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
              placeholder="you@example.com"
            />
          </label>
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.password')}
            <span className="relative mt-2 block">
              <input
                type={showPassword ? 'text' : 'password'} autoComplete="new-password" required
                minLength={8}
                value={password}
                onChange={e => setPassword(e.target.value)}
                className="w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 pr-24 text-white outline-none transition focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
              />
              <button
                type="button"
                aria-pressed={showPassword}
                aria-label={showPassword ? t('Login.hidePassword') : t('Login.showPassword')}
                onClick={() => setShowPassword((visible) => !visible)}
                className="absolute inset-y-0 right-1 my-1 inline-flex min-h-11 min-w-11 items-center justify-center rounded-md px-2 text-xs font-medium text-zinc-300 transition hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400"
              >
                {showPassword ? t('Login.hidePassword') : t('Login.showPassword')}
              </button>
            </span>
          </label>
          {error ? (
            <p role="alert" className="rounded-lg border border-red-400/20 bg-red-400/10 px-3 py-2 text-sm text-red-200">{error}</p>
          ) : null}
          <button
            type="submit" disabled={loading}
            className="w-full rounded-lg bg-emerald-300 px-4 py-3 font-semibold text-black transition hover:bg-emerald-200 disabled:opacity-60"
          >
            {loading ? t('Register.registering') : t('Register.signUp')}
          </button>
          <button
            type="button" onClick={onClose}
            className="w-full rounded-lg border border-white/10 bg-transparent px-4 py-3 text-sm font-medium text-zinc-300 transition hover:bg-white/10"
          >
            {t('Register.backToLogin')}
          </button>
        </form>
      </div>
    </div>
  );
}
