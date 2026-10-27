'use client';

import { FormEvent, useCallback, useEffect, useState } from 'react';
import { createLLMProfile, deleteLLMProfile, listLLMProfiles, updateLLMProfile, type LLMProfile, type LLMProfileInput } from '@/lib/api';
import { useT } from '@/lib/i18n';

const openAIEndpoint = 'https://api.openai.com/v1';
const xaiEndpoint = 'https://api.x.ai/v1';
const anthropicEndpoint = 'https://api.anthropic.com';

export function BYOKProfilesPanel({ onProfileCreated }: { onProfileCreated?: (profile: LLMProfile) => void }) {
  const { t } = useT();
  const [profiles, setProfiles] = useState<LLMProfile[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [requestError, setRequestError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<LLMProfile | null>(null);
  const [name, setName] = useState('');
  const [provider, setProvider] = useState<LLMProfile['provider']>('openai');
  const [protocol, setProtocol] = useState<LLMProfile['protocol']>('openai_chat_completions');
  const [endpoint, setEndpoint] = useState(openAIEndpoint);
  const [model, setModel] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [clearAPIKey, setClearAPIKey] = useState(false);

  const load = useCallback(async () => {
    try {
      setProfiles(await listLLMProfiles());
      setLoadError('');
    } catch (error: unknown) {
      setLoadError(error instanceof Error ? error.message : t('BYOK.loadError'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { void Promise.resolve().then(load); }, [load]);

  function resetForm() {
    setEditing(null);
    setName('');
    setProvider('openai');
    setProtocol('openai_chat_completions');
    setEndpoint(openAIEndpoint);
    setModel('');
    setApiKey('');
    setClearAPIKey(false);
  }

  function editProfile(profile: LLMProfile) {
    setEditing(profile);
    setName(profile.name);
    setProvider(profile.provider);
    setProtocol(profile.protocol);
    setEndpoint(profile.endpoint);
    setModel(profile.model);
    setApiKey('');
    setClearAPIKey(false);
    setRequestError('');
    setNotice('');
  }

  function selectProvider(next: LLMProfile['provider']) {
    setProvider(next);
    if (next === 'anthropic') {
      setProtocol('anthropic_messages');
      setEndpoint(anthropicEndpoint);
    } else if (next === 'openai' || next === 'xai') {
      setProtocol('openai_chat_completions');
      setEndpoint(next === 'xai' ? xaiEndpoint : openAIEndpoint);
    }
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setRequestError('');
    setNotice('');
    const input: LLMProfileInput = {
      name: name.trim(), provider, protocol, endpoint: endpoint.trim(), model: model.trim(),
      ...(apiKey ? { api_key: apiKey } : {}), ...(clearAPIKey ? { clear_api_key: true } : {}),
      ...(editing ? { revision: editing.revision } : {}),
    };
    try {
      const saved = editing
        ? await updateLLMProfile(editing.id, input)
        : await createLLMProfile(input);
      setProfiles((current) => editing ? current.map((item) => item.id === saved.id ? saved : item) : [...current, saved]);
      if (!editing) onProfileCreated?.(saved);
      setNotice(editing ? t('BYOK.updated') : t('BYOK.created'));
      resetForm();
      setApiKey('');
    } catch (error: unknown) {
      setRequestError(error instanceof Error ? error.message : t('BYOK.saveError'));
    } finally {
      setBusy(false);
    }
  }

  async function remove(profile: LLMProfile) {
    if (busy || !window.confirm(t('BYOK.confirmDelete', { name: profile.name }))) return;
    setBusy(true);
    setRequestError('');
    setNotice('');
    try {
      await deleteLLMProfile(profile.id);
      setProfiles((current) => current.filter((item) => item.id !== profile.id));
      if (editing?.id === profile.id) resetForm();
      setNotice(t('BYOK.deleted'));
    } catch (error: unknown) {
      setRequestError(error instanceof Error ? error.message : t('BYOK.deleteError'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="mt-8 border-t border-white/10 pt-6" aria-labelledby="byok-profiles-heading">
      <h3 id="byok-profiles-heading" tabIndex={-1} className="text-lg font-semibold text-white focus:outline-none">{t('BYOK.title')}</h3>
      <p className="mt-1 text-sm leading-6 text-zinc-400">{t('BYOK.hint')}</p>
      {loading ? <p role="status" className="mt-3 text-sm text-zinc-400">{t('BYOK.loading')}</p> : null}
      {loadError ? <p role="alert" className="mt-3 text-sm text-red-200">{loadError} <button type="button" onClick={() => { setLoading(true); void load(); }} className="underline">{t('BYOK.retry')}</button></p> : null}
      {!loading && !loadError && profiles.length === 0 ? <p className="mt-3 rounded-lg border border-white/10 bg-black/20 p-3 text-sm text-zinc-400">{t('BYOK.empty')}</p> : null}
      {profiles.length > 0 ? (
        <ul className="mt-3 space-y-2" aria-label={t('BYOK.title')}>
          {profiles.map((profile) => (
            <li key={profile.id} className="flex flex-col gap-3 rounded-lg border border-white/10 bg-black/20 p-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium text-white">{profile.name}</p>
                <p className="mt-1 break-all text-xs text-zinc-400">{profile.provider} · {profile.model} · {profile.secret_configured ? t('BYOK.secretSaved') : t('BYOK.noSecret')}</p>
              </div>
              <div className="flex shrink-0 gap-2">
                <button type="button" disabled={busy} onClick={() => editProfile(profile)} className="min-h-10 rounded-md border border-white/15 px-3 text-sm text-zinc-200 hover:bg-white/10 disabled:opacity-50">{t('BYOK.edit')}</button>
                <button type="button" disabled={busy} onClick={() => void remove(profile)} className="min-h-10 rounded-md border border-red-300/20 px-3 text-sm text-red-200 hover:bg-red-300/10 disabled:opacity-50">{t('BYOK.delete')}</button>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      <form onSubmit={(event) => void save(event)} className="mt-4 space-y-3 rounded-lg border border-white/10 bg-black/20 p-4">
        <h4 className="text-sm font-semibold text-zinc-100">{editing ? t('BYOK.editTitle') : t('BYOK.addTitle')}</h4>
        <label className="block text-sm text-zinc-300">{t('BYOK.name')}
          <input required maxLength={100} value={name} onChange={(event) => setName(event.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
        </label>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm text-zinc-300">{t('BYOK.provider')}
            <select value={provider} onChange={(event) => selectProvider(event.target.value as LLMProfile['provider'])} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
              <option value="openai">OpenAI</option><option value="xai">xAI / Grok</option><option value="anthropic">Anthropic / Claude</option><option value="custom">{t('BYOK.custom')}</option>
            </select>
          </label>
          <label className="block text-sm text-zinc-300">{t('BYOK.protocol')}
            <select value={protocol} disabled={provider !== 'custom'} onChange={(event) => setProtocol(event.target.value as LLMProfile['protocol'])} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white disabled:opacity-70">
              <option value="openai_chat_completions">OpenAI Chat Completions</option><option value="anthropic_messages">Anthropic Messages</option>
            </select>
          </label>
        </div>
        <label className="block text-sm text-zinc-300">{t('BYOK.endpoint')}
          <input required type="url" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} readOnly={provider !== 'custom'} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white read-only:text-zinc-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
        </label>
        <label className="block text-sm text-zinc-300">{t('BYOK.model')}
          <input required maxLength={200} value={model} onChange={(event) => setModel(event.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
          <span className="mt-1 block text-xs text-zinc-500">{t('BYOK.modelHint')}</span>
        </label>
        <label className="block text-sm text-zinc-300">{t('BYOK.apiKey')}
          <input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => { setApiKey(event.target.value); setClearAPIKey(false); }} placeholder={editing?.secret_configured ? t('BYOK.keyReplacementPlaceholder') : ''} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
          <span className="mt-1 block text-xs text-zinc-500">{editing?.secret_configured ? t('BYOK.keyReplacementHint') : t('BYOK.keyWriteOnlyHint')}</span>
        </label>
        {editing?.secret_configured && provider === 'custom' ? (
          <label className="flex min-h-11 items-center gap-2 text-sm text-zinc-300"><input type="checkbox" checked={clearAPIKey} onChange={(event) => setClearAPIKey(event.target.checked)} />{t('BYOK.clearKey')}</label>
        ) : null}
        {requestError ? <p role="alert" className="text-sm text-red-200">{requestError}</p> : null}
        {notice ? <p role="status" className="text-sm text-emerald-200">{notice}</p> : null}
        <div className="flex flex-wrap justify-end gap-2">
          {editing ? <button type="button" disabled={busy} onClick={resetForm} className="min-h-11 rounded-md border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/10">{t('BYOK.cancelEdit')}</button> : null}
          <button type="submit" disabled={busy || !name.trim() || !model.trim() || (!editing && provider !== 'custom' && !apiKey.trim())} className="min-h-11 rounded-md bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">{busy ? t('BYOK.saving') : editing ? t('BYOK.save') : t('BYOK.create')}</button>
        </div>
      </form>
    </section>
  );
}
