'use client';

import { FormEvent, useState } from 'react';
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
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setLoading(true);
    setError('');
    try {
      await register(email.trim(), password);
      onSuccess();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Registration failed');
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm">
      <div className="w-full max-w-md rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl sm:p-8">
        <h2 className="text-2xl font-semibold text-white">{t('Register.title')}</h2>
        <p className="mt-1 text-sm text-zinc-400">{t('Register.subtitle')}</p>
        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.email')}
            <input
              type="email" autoComplete="email" required
              value={email}
              onChange={e => setEmail(e.target.value)}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition focus:border-emerald-300"
              placeholder="you@example.com"
            />
          </label>
          <label className="block text-sm font-medium text-zinc-300">
            {t('Login.password')}
            <input
              type="password" autoComplete="new-password" required
              minLength={8}
              value={password}
              onChange={e => setPassword(e.target.value)}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition focus:border-emerald-300"
            />
          </label>
          {error ? (
            <p className="rounded-lg border border-red-400/20 bg-red-400/10 px-3 py-2 text-sm text-red-200">{error}</p>
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
