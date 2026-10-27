'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getProjectLLMBinding, listLLMProfiles, saveProjectLLMBinding, type LLMProfile, type ProjectLLMBinding } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { SharedServiceNotice } from './SharedServiceNotice';

type LoadedSource = { key: string; binding: ProjectLLMBinding; profiles: LLMProfile[] };

export function ProjectLLMSourcePanel({ projectId, ownerId, sessionEpoch, compact = false, onAddProfile, onManageProviders, newlyCreatedProfile, allowPersonalProfiles = true }: {
  projectId: string;
  ownerId: string;
  sessionEpoch: number;
  compact?: boolean;
  onAddProfile?: () => void;
  onManageProviders?: () => void;
  newlyCreatedProfile?: LLMProfile | null;
  allowPersonalProfiles?: boolean;
}) {
  const { t } = useT();
  const scopeKey = useMemo(() => `${ownerId}\u0000${projectId}\u0000${sessionEpoch}\u0000${allowPersonalProfiles ? 'personal' : 'shared'}`, [allowPersonalProfiles, ownerId, projectId, sessionEpoch]);
  const [loaded, setLoaded] = useState<LoadedSource | null>(null);
  const [draftSource, setDraftSource] = useState<'shared' | 'personal'>('shared');
  const [draftProfileId, setDraftProfileId] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const sourceSelectRef = useRef<HTMLSelectElement>(null);
  const profileSelectRef = useRef<HTMLSelectElement>(null);
  const current = loaded?.key === scopeKey ? loaded : null;
  const selectedProfileExists = current?.profiles.some((profile) => profile.id === draftProfileId) ?? false;
  const dirty = Boolean(current && (draftSource !== current.binding.source || (draftSource === 'personal' && draftProfileId !== (current.binding.profile_id ?? ''))));

  const refresh = useCallback(async () => {
    try {
      const [binding, profiles] = await Promise.all([getProjectLLMBinding(projectId), listLLMProfiles()]);
      setLoaded({ key: scopeKey, binding, profiles });
      setDraftSource(binding.source === 'personal' && !allowPersonalProfiles ? 'shared' : binding.source);
      setDraftProfileId(binding.profile_id ?? '');
      setError('');
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('LLMSource.loadError'));
    } finally {
      setLoading(false);
    }
  }, [allowPersonalProfiles, projectId, scopeKey, t]);

  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [refresh]);

  useEffect(() => {
    if (!allowPersonalProfiles || !newlyCreatedProfile || !current || current.profiles.some((profile) => profile.id === newlyCreatedProfile.id)) return;
    queueMicrotask(() => {
      setLoaded({ ...current, profiles: [...current.profiles, newlyCreatedProfile] });
      setDraftSource('personal');
      setDraftProfileId(newlyCreatedProfile.id);
      setError('');
      setNotice('');
      window.setTimeout(() => profileSelectRef.current?.focus(), 0);
    });
  // A newly created profile is a return payload from the account settings pane.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [allowPersonalProfiles, newlyCreatedProfile?.id, current?.key, dirty]);

  async function save() {
    if (!current || !dirty || saving || (draftSource === 'personal' && (!allowPersonalProfiles || !selectedProfileExists))) return;
    setSaving(true);
    setError('');
    setNotice('');
    try {
      const next = await saveProjectLLMBinding(projectId, {
        source: draftSource,
        ...(draftSource === 'personal' ? { profile_id: draftProfileId } : {}),
        revision: current.binding.revision,
      });
      setLoaded({ ...current, binding: next });
      setDraftSource(next.source);
      setDraftProfileId(next.profile_id ?? '');
      setNotice(t('LLMSource.saved'));
      window.dispatchEvent(new CustomEvent('lwc-project-llm-binding-updated', { detail: { projectId } }));
    } catch (saveError: unknown) {
      setError(saveError instanceof Error ? saveError.message : t('LLMSource.saveError'));
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="mt-5 rounded-xl border border-white/10 bg-black/15 p-4" aria-labelledby={`llm-source-${projectId}`}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 id={`llm-source-${projectId}`} className="text-sm font-semibold text-white">{t('LLMSource.title')}</h3>
          {loading ? <p role="status" className="mt-1 text-xs text-zinc-400">{t('LLMSource.loading')}</p> : current ? (
            <p className="mt-1 text-xs text-zinc-400">{current.binding.source === 'personal' ? t('LLMSource.personal') : t('LLMSource.shared')}</p>
          ) : null}
        </div>
        <button type="button" disabled={loading} onClick={() => { setLoading(true); void refresh(); }} className="min-h-10 rounded-md border border-white/15 px-3 text-xs text-zinc-300 hover:bg-white/10 disabled:opacity-50">{t('BYOK.retry')}</button>
      </div>
      {!current && !loading && error ? <p role="alert" className="mt-3 text-sm text-red-200">{error}</p> : null}
      {current && allowPersonalProfiles ? <SharedServiceNotice ownerId={ownerId} source={current.binding.source} onManageProviders={onManageProviders} /> : null}
      {current ? (
        <>
          <label className="mt-3 block text-sm text-zinc-300">
            {t('LLMSource.source')}
            <select ref={sourceSelectRef} value={draftSource} onChange={(event) => setDraftSource(event.target.value as 'shared' | 'personal')} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
              <option value="shared">{t('LLMSource.shared')}</option>{allowPersonalProfiles ? <option value="personal">{t('LLMSource.personal')}</option> : null}
            </select>
          </label>
          {draftSource === 'personal' && allowPersonalProfiles ? (
            <div className="mt-3">
              {current.profiles.length ? (
                <label className="block text-sm text-zinc-300">{t('LLMSource.profile')}
                  <select ref={profileSelectRef} value={draftProfileId} onChange={(event) => setDraftProfileId(event.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
                    {!selectedProfileExists && draftProfileId ? <option value={draftProfileId}>{t('LLMSource.missingProfile')}</option> : null}
                    <option value="">{t('LLMSource.chooseProfile')}</option>
                    {current.profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.provider} · {profile.model}</option>)}
                  </select>
                </label>
              ) : <p className="text-sm text-zinc-400">{t('LLMSource.emptyProfiles')}</p>}
              {!compact && onAddProfile ? <button type="button" onClick={onAddProfile} className="mt-2 min-h-11 rounded-md border border-white/15 px-3 text-sm text-emerald-100 hover:bg-white/5">{t('LLMSource.addProfile')}</button> : null}
            </div>
          ) : null}
          {error ? <p role="alert" className="mt-3 text-sm text-red-200">{error}</p> : null}
          {notice ? <p role="status" className="mt-3 text-sm text-emerald-200">{notice}</p> : null}
          <div className="mt-3 flex flex-wrap justify-end gap-2">
            {dirty ? <button type="button" onClick={() => { setDraftSource(current.binding.source); setDraftProfileId(current.binding.profile_id ?? ''); setError(''); }} className="min-h-11 rounded-md border border-white/15 px-3 text-sm text-zinc-300 hover:bg-white/10">{t('LLMSource.cancel')}</button> : null}
            <button type="button" disabled={!dirty || saving || (draftSource === 'personal' && (!allowPersonalProfiles || !selectedProfileExists))} onClick={() => void save()} className="min-h-11 rounded-md bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">{saving ? t('LLMSource.saving') : t('LLMSource.save')}</button>
          </div>
        </>
      ) : null}
    </section>
  );
}
