'use client';

import { FormEvent, useCallback, useEffect, useState } from 'react';
import { createAdminLLMProfile, deleteAdminLLMProfile, listAdminLLMProfiles, updateAdminLLMProfile, type DeploymentLLMCredentialReference, type LLMProfile, type LLMProfileInput } from '@/lib/api';
import { useT } from '@/lib/i18n';

const defaults = { openai: 'https://api.openai.com/v1', xai: 'https://api.x.ai/v1', anthropic: 'https://api.anthropic.com' };

export function AdminSharedProfilesPanel() {
  const { t } = useT();
  const [profiles, setProfiles] = useState<LLMProfile[]>([]);
  const [deploymentCredentials, setDeploymentCredentials] = useState<DeploymentLLMCredentialReference[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editing, setEditing] = useState<LLMProfile | null>(null);
  const [name, setName] = useState('');
  const [provider, setProvider] = useState<LLMProfile['provider']>('openai');
  const [protocol, setProtocol] = useState<LLMProfile['protocol']>('openai_chat_completions');
  const [endpoint, setEndpoint] = useState(defaults.openai);
  const [model, setModel] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [credentialReference, setCredentialReference] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [available, setAvailable] = useState(true);
  const [priority, setPriority] = useState(100);

  const load = useCallback(async () => {
    try {
      const result = await listAdminLLMProfiles();
      setProfiles(result.profiles); setDeploymentCredentials(result.deployment_credentials); setError('');
    }
    catch (loadError: unknown) { setError(loadError instanceof Error ? loadError.message : t('Admin.sharedProfiles.loadError')); }
    finally { setLoading(false); }
  }, [t]);
  useEffect(() => { void Promise.resolve().then(load); }, [load]);

  function reset() {
    setEditing(null); setName(''); setProvider('openai'); setProtocol('openai_chat_completions');
    setEndpoint(defaults.openai); setModel(''); setApiKey(''); setCredentialReference(''); setEnabled(true); setAvailable(true); setPriority(100);
  }
  function edit(profile: LLMProfile) {
    setEditing(profile); setName(profile.name); setProvider(profile.provider); setProtocol(profile.protocol);
    setEndpoint(profile.endpoint); setModel(profile.model); setApiKey(''); setCredentialReference(profile.credential_reference ?? ''); setEnabled(profile.enabled ?? false);
    setAvailable(profile.available_to_public ?? false); setPriority(profile.priority ?? 100); setError(''); setNotice('');
  }
  function chooseProvider(next: LLMProfile['provider']) {
    setProvider(next);
    if (next === 'anthropic') { setProtocol('anthropic_messages'); setEndpoint(defaults.anthropic); }
    else if (next === 'openai' || next === 'xai') { setProtocol('openai_chat_completions'); setEndpoint(defaults[next]); }
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (busy) return;
    setBusy(true); setError(''); setNotice('');
    const input: LLMProfileInput = {
      name: name.trim(), provider, protocol, endpoint: endpoint.trim(), model: model.trim(),
      enabled, available_to_public: available, priority,
      ...(credentialReference ? { credential_reference: credentialReference } : {}),
      ...(apiKey ? { api_key: apiKey } : {}), ...(editing ? { revision: editing.revision } : {}),
    };
    try {
      const saved = editing ? await updateAdminLLMProfile(editing.id, input) : await createAdminLLMProfile(input);
      setProfiles((current) => editing ? current.map((item) => item.id === saved.id ? saved : item) : [...current, saved]);
      setNotice(t('Admin.sharedProfiles.saved')); reset();
    } catch (saveError: unknown) { setError(saveError instanceof Error ? saveError.message : t('Admin.sharedProfiles.saveError')); }
    finally { setBusy(false); }
  }
  async function remove(profile: LLMProfile) {
    if (busy || !window.confirm(t('Admin.sharedProfiles.confirmDelete', { name: profile.name }))) return;
    setBusy(true); setError(''); setNotice('');
    try { await deleteAdminLLMProfile(profile.id); setProfiles((current) => current.filter((item) => item.id !== profile.id)); setNotice(t('Admin.sharedProfiles.deleted')); if (editing?.id === profile.id) reset(); }
    catch (removeError: unknown) { setError(removeError instanceof Error ? removeError.message : t('Admin.sharedProfiles.deleteError')); }
    finally { setBusy(false); }
  }

  return <section className="space-y-4" aria-labelledby="admin-shared-profiles-title">
    <div>
      <h2 id="admin-shared-profiles-title" className="text-xl font-semibold text-white">{t('Admin.sharedProfiles.title')}</h2>
      <p className="mt-1 text-sm text-zinc-400">{t('Admin.sharedProfiles.hint')}</p>
    </div>
    {loading ? <p role="status" className="text-sm text-zinc-400">{t('Admin.sharedProfiles.loading')}</p> : null}
    {!loading && profiles.length === 0 ? <p className="rounded-lg border border-white/10 bg-black/20 p-3 text-sm text-zinc-400">{t('Admin.sharedProfiles.empty')}</p> : null}
    {profiles.length ? <ul className="space-y-2">{profiles.map((profile) => <li key={profile.id} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-white/10 bg-black/20 p-3">
      <div className="min-w-0"><p className="font-medium text-white">{profile.name}</p><p className="mt-1 break-all text-xs text-zinc-400">{profile.provider} · {profile.model} · {t('Admin.sharedProfiles.priority')} {profile.priority ?? 100} · {profile.available_to_public ? t('Admin.sharedProfiles.public') : t('Admin.sharedProfiles.private')} · {profile.enabled ? t('Admin.sharedProfiles.enabled') : t('Admin.sharedProfiles.disabled')} · {profile.secret_configured ? t('Admin.sharedProfiles.keySaved') : t('Admin.sharedProfiles.noKey')}</p></div>
      <div className="flex gap-2"><button type="button" disabled={busy} onClick={() => edit(profile)} className="min-h-10 rounded-md border border-white/15 px-3 text-sm text-zinc-200">{t('BYOK.edit')}</button><button type="button" disabled={busy} onClick={() => void remove(profile)} className="min-h-10 rounded-md border border-red-300/20 px-3 text-sm text-red-200">{t('BYOK.delete')}</button></div>
    </li>)}</ul> : null}
    <form onSubmit={(event) => void save(event)} className="space-y-3 rounded-lg border border-white/10 bg-black/20 p-4">
      <h3 className="font-semibold text-white">{editing ? t('Admin.sharedProfiles.editTitle') : t('Admin.sharedProfiles.addTitle')}</h3>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="text-sm text-zinc-300">{t('BYOK.name')}<input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white" /></label>
        <label className="text-sm text-zinc-300">{t('BYOK.provider')}<select value={provider} onChange={(e) => chooseProvider(e.target.value as LLMProfile['provider'])} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white"><option value="openai">OpenAI</option><option value="xai">xAI</option><option value="anthropic">Anthropic</option><option value="custom">{t('BYOK.custom')}</option></select></label>
        <label className="text-sm text-zinc-300">{t('BYOK.protocol')}<select disabled={provider !== 'custom'} value={protocol} onChange={(e) => setProtocol(e.target.value as LLMProfile['protocol'])} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white"><option value="openai_chat_completions">OpenAI Chat Completions</option><option value="anthropic_messages">Anthropic Messages</option></select></label>
        <label className="text-sm text-zinc-300">{t('BYOK.endpoint')}<input type="url" required value={endpoint} onChange={(e) => setEndpoint(e.target.value)} readOnly={provider !== 'custom'} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white" /></label>
        <label className="text-sm text-zinc-300">{t('BYOK.model')}<input required maxLength={200} value={model} onChange={(e) => setModel(e.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white" /></label>
        <label className="text-sm text-zinc-300">{t('Admin.sharedProfiles.credentialSource')}<select value={credentialReference} disabled={editing?.credential_origin === 'deployment'} onChange={(e) => setCredentialReference(e.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white"><option value="">{t('Admin.sharedProfiles.managedKey')}</option>{credentialReference && !deploymentCredentials.some((item) => item.reference === credentialReference) ? <option value={credentialReference}>{t('Admin.sharedProfiles.unavailableReference', { reference: credentialReference })}</option> : null}{deploymentCredentials.map((item) => <option key={item.reference} value={item.reference}>{item.label} · {item.reference}</option>)}</select></label>
        {credentialReference ? <p className="text-xs text-zinc-500">{t('Admin.sharedProfiles.referenceHint')}</p> : <label className="text-sm text-zinc-300">{t('BYOK.apiKey')}<input type="password" autoComplete="new-password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={editing?.secret_configured ? t('Admin.sharedProfiles.keyReplacementPlaceholder') : ''} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white" /><span className="mt-1 block text-xs text-zinc-500">{t('Admin.sharedProfiles.keyWriteOnlyHint')}</span></label>}
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="flex min-h-11 items-center gap-2 text-sm text-zinc-300"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />{t('Admin.sharedProfiles.enabled')}</label>
        <label className="flex min-h-11 items-center gap-2 text-sm text-zinc-300"><input type="checkbox" checked={available} onChange={(e) => setAvailable(e.target.checked)} />{t('Admin.sharedProfiles.publicToggle')}</label>
        <label className="text-sm text-zinc-300">{t('Admin.sharedProfiles.priority')}<input type="number" min={0} max={100000} value={priority} onChange={(e) => setPriority(Number(e.target.value))} className="ml-2 min-h-10 w-24 rounded-md border border-white/10 bg-black/30 px-2 text-white" /></label>
      </div>
      {error ? <p role="alert" className="text-sm text-red-200">{error}</p> : null}{notice ? <p role="status" className="text-sm text-emerald-200">{notice}</p> : null}
      <div className="flex justify-end gap-2">{editing ? <button type="button" disabled={busy} onClick={reset} className="min-h-11 rounded-md border border-white/15 px-3 text-sm text-zinc-300">{t('BYOK.cancelEdit')}</button> : null}<button type="submit" disabled={busy || !name.trim() || !model.trim() || (!credentialReference && !editing?.secret_configured && provider !== 'custom' && !apiKey.trim())} className="min-h-11 rounded-md bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 disabled:opacity-50">{busy ? t('BYOK.saving') : t('Admin.sharedProfiles.save')}</button></div>
    </form>
  </section>;
}
