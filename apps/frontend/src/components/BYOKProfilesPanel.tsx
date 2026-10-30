'use client';

import { FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, createLLMProfile, deleteLLMProfile, discoverLLMProfileModels, listLLMProfiles, testLLMProfileMessage, updateLLMProfile, type LLMProfile, type LLMProfileInput, type LLMProfileOperationInput } from '@/lib/api';
import { useT } from '@/lib/i18n';

const openAIEndpoint = 'https://api.openai.com/v1';
const xaiEndpoint = 'https://api.x.ai/v1';
const anthropicEndpoint = 'https://api.anthropic.com';
type Role = 'fast' | 'heavy';
type Operation = 'discover' | 'test';
type OperationState = { busy: Operation | null; error: string; notice: string; testText: string };
const emptyOperation = (): OperationState => ({ busy: null, error: '', notice: '', testText: '' });

export function BYOKProfilesPanel({ active = true, onProfileCreated }: { active?: boolean; onProfileCreated?: (profile: LLMProfile) => void }) {
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
  const [fastModel, setFastModel] = useState('');
  const [heavyModel, setHeavyModel] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [clearAPIKey, setClearAPIKey] = useState(false);
  const [discovered, setDiscovered] = useState<Record<Role, string[]>>({ fast: [], heavy: [] });
  const [operations, setOperations] = useState<Record<Role, OperationState>>({ fast: emptyOperation(), heavy: emptyOperation() });
  const draftVersion = useRef(0);
  const operationID = useRef<Record<Role, number>>({ fast: 0, heavy: 0 });

  function invalidateOperationResults() {
    draftVersion.current += 1;
    setDiscovered({ fast: [], heavy: [] });
    setOperations({ fast: emptyOperation(), heavy: emptyOperation() });
  }

  useEffect(() => {
    if (!active) {
      // Keep editable provider details across tabs, but discard the hidden key draft.
      setApiKey('');
      invalidateOperationResults();
    }
  }, [active]);

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
    invalidateOperationResults();
    setEditing(null);
    setName('');
    setProvider('openai');
    setProtocol('openai_chat_completions');
    setEndpoint(openAIEndpoint);
    setFastModel('');
    setHeavyModel('');
    setApiKey('');
    setClearAPIKey(false);
  }

  function editProfile(profile: LLMProfile) {
    invalidateOperationResults();
    const models = profile.models ?? { fast: profile.model, heavy: profile.model };
    setEditing(profile);
    setName(profile.name);
    setProvider(profile.provider);
    setProtocol(profile.protocol);
    setEndpoint(profile.endpoint);
    setFastModel(models.fast);
    setHeavyModel(models.heavy);
    setApiKey('');
    setClearAPIKey(false);
    setRequestError('');
    setNotice('');
  }

  function selectProvider(next: LLMProfile['provider']) {
    invalidateOperationResults();
    setProvider(next);
    if (next === 'anthropic') {
      setProtocol('anthropic_messages');
      setEndpoint(anthropicEndpoint);
    } else if (next === 'openai' || next === 'xai') {
      setProtocol('openai_chat_completions');
      setEndpoint(next === 'xai' ? xaiEndpoint : openAIEndpoint);
    }
  }

  function operationError(error: unknown) {
    const code = error instanceof ApiError ? error.message : '';
    const keys: Record<string, string> = {
      provider_authentication: 'BYOK.operationAuthenticationError',
      provider_rate_limited: 'BYOK.operationRateLimitError',
      quota_exhausted: 'BYOK.operationQuotaError',
      provider_timeout: 'BYOK.operationTimeoutError',
      provider_billing: 'BYOK.operationBillingError',
      provider_response_invalid: 'BYOK.operationResponseError',
      credential_unavailable: 'BYOK.operationCredentialError',
      profile_changed: 'BYOK.operationProfileChanged',
    };
    return t(keys[code] ?? 'BYOK.operationError');
  }

  function operationInput(role: Role): LLMProfileOperationInput {
    return {
      ...(editing ? { profile_id: editing.id, revision: editing.revision } : {}),
      provider, protocol, endpoint: endpoint.trim(), models: { fast: fastModel.trim(), heavy: heavyModel.trim() }, role,
      ...(apiKey.trim() ? { api_key: apiKey } : {}),
    };
  }

  async function runOperation(role: Role, operation: Operation) {
    if (busy || operations[role].busy) return;
    const version = draftVersion.current;
    const requestID = ++operationID.current[role];
    setOperations((current) => ({ ...current, [role]: { busy: operation, error: '', notice: '', testText: '' } }));
    try {
      if (operation === 'discover') {
        const result = await discoverLLMProfileModels(operationInput(role));
        if (version !== draftVersion.current || requestID !== operationID.current[role]) return;
        setDiscovered((current) => ({ ...current, [role]: result.models }));
        setOperations((current) => ({ ...current, [role]: { busy: null, error: '', notice: t('BYOK.discoverySuccess', { count: result.models.length }), testText: '' } }));
      } else {
        const result = await testLLMProfileMessage(operationInput(role));
        if (version !== draftVersion.current || requestID !== operationID.current[role]) return;
        setOperations((current) => ({ ...current, [role]: { busy: null, error: '', notice: t('BYOK.testSuccess'), testText: result.text } }));
      }
    } catch (error: unknown) {
      if (version !== draftVersion.current || requestID !== operationID.current[role]) return;
      setOperations((current) => ({ ...current, [role]: { busy: null, error: operationError(error), notice: '', testText: '' } }));
    }
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setRequestError('');
    setNotice('');
    invalidateOperationResults();
    const input: LLMProfileInput = {
      name: name.trim(), provider, protocol, endpoint: endpoint.trim(), model: heavyModel.trim(),
      models: { fast: fastModel.trim(), heavy: heavyModel.trim() },
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

  const renderRoleModel = (role: Role, model: string, setModel: (value: string) => void) => {
    const op = operations[role];
    const modelListID = `byok-${role}-models`;
    return <div className="space-y-2 rounded-md border border-white/10 bg-black/20 p-3">
      <label className="block text-sm text-zinc-300">{t(role === 'fast' ? 'BYOK.fastModel' : 'BYOK.heavyModel')}
        <input required maxLength={200} list={modelListID} value={model} onChange={(event) => { invalidateOperationResults(); setModel(event.target.value); }} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
        <datalist id={modelListID}>{discovered[role].map((item) => <option key={item} value={item} />)}</datalist>
      </label>
      <div className="flex flex-wrap gap-2">
        <button type="button" disabled={busy || op.busy !== null} onClick={() => void runOperation(role, 'discover')} className="min-h-10 rounded-md border border-white/15 px-3 text-sm text-zinc-200 disabled:opacity-50">{op.busy === 'discover' ? t('BYOK.discovering') : t('BYOK.discoverModels')}</button>
        <button type="button" disabled={busy || op.busy !== null || !model.trim()} onClick={() => void runOperation(role, 'test')} className="min-h-10 rounded-md border border-emerald-300/30 px-3 text-sm text-emerald-100 disabled:opacity-50">{op.busy === 'test' ? t('BYOK.testing') : t('BYOK.testMessage')}</button>
      </div>
      {op.error ? <p role="alert" className="text-sm text-red-200">{op.error}</p> : null}
      {op.notice ? <p role="status" className="text-sm text-emerald-200">{op.notice}</p> : null}
      {op.testText ? <p className="rounded-md bg-black/30 p-2 text-sm text-zinc-200">{op.testText}</p> : null}
    </div>;
  };

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
                <p className="mt-1 break-all text-xs text-zinc-400">{profile.provider} · {t('BYOK.fastModel')}: {profile.models?.fast ?? profile.model} · {t('BYOK.heavyModel')}: {profile.models?.heavy ?? profile.model} · {profile.secret_configured ? t('BYOK.secretSaved') : t('BYOK.noSecret')}</p>
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
          <input required maxLength={100} value={name} onChange={(event) => { invalidateOperationResults(); setName(event.target.value); }} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
        </label>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm text-zinc-300">{t('BYOK.provider')}
            <select value={provider} onChange={(event) => selectProvider(event.target.value as LLMProfile['provider'])} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
              <option value="openai">OpenAI</option><option value="xai">xAI / Grok</option><option value="anthropic">Anthropic / Claude</option><option value="custom">{t('BYOK.custom')}</option>
            </select>
          </label>
          <label className="block text-sm text-zinc-300">{t('BYOK.protocol')}
            <select value={protocol} disabled={provider !== 'custom'} onChange={(event) => { invalidateOperationResults(); setProtocol(event.target.value as LLMProfile['protocol']); }} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white disabled:opacity-70">
              <option value="openai_chat_completions">OpenAI Chat Completions</option><option value="anthropic_messages">Anthropic Messages</option>
            </select>
          </label>
        </div>
        <label className="block text-sm text-zinc-300">{t('BYOK.endpoint')}
          <input required type="url" value={endpoint} onChange={(event) => { invalidateOperationResults(); setEndpoint(event.target.value); }} readOnly={provider !== 'custom'} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white read-only:text-zinc-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
        </label>
        <p className="text-xs text-zinc-500">{t('BYOK.modelHint')}</p>
        <div className="grid gap-3 md:grid-cols-2">
          {renderRoleModel('fast', fastModel, setFastModel)}
          {renderRoleModel('heavy', heavyModel, setHeavyModel)}
        </div>
        <label className="block text-sm text-zinc-300">{t('BYOK.apiKey')}
          <input type="password" autoComplete="new-password" value={apiKey} onChange={(event) => { invalidateOperationResults(); setApiKey(event.target.value); setClearAPIKey(false); }} placeholder={editing?.secret_configured ? t('BYOK.keyReplacementPlaceholder') : ''} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-black/30 px-3 text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300" />
          <span className="mt-1 block text-xs text-zinc-500">{editing?.secret_configured ? t('BYOK.keyReplacementHint') : t('BYOK.keyWriteOnlyHint')}</span>
        </label>
        {editing?.secret_configured && provider === 'custom' ? (
          <label className="flex min-h-11 items-center gap-2 text-sm text-zinc-300"><input type="checkbox" checked={clearAPIKey} onChange={(event) => { invalidateOperationResults(); setClearAPIKey(event.target.checked); }} />{t('BYOK.clearKey')}</label>
        ) : null}
        {requestError ? <p role="alert" className="text-sm text-red-200">{requestError}</p> : null}
        {notice ? <p role="status" className="text-sm text-emerald-200">{notice}</p> : null}
        <div className="flex flex-wrap justify-end gap-2">
          {editing ? <button type="button" disabled={busy} onClick={resetForm} className="min-h-11 rounded-md border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/10">{t('BYOK.cancelEdit')}</button> : null}
          <button type="submit" disabled={busy || !name.trim() || !fastModel.trim() || !heavyModel.trim() || (!editing && provider !== 'custom' && !apiKey.trim())} className="min-h-11 rounded-md bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">{busy ? t('BYOK.saving') : editing ? t('BYOK.save') : t('BYOK.create')}</button>
        </div>
      </form>
    </section>
  );
}
