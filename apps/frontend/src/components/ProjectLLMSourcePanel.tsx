'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { getProjectLLMBinding, listLLMProfiles, saveProjectLLMBinding, type LLMProfile, type ProjectLLMBinding } from '@/lib/api';
import { useT } from '@/lib/i18n';

type LoadedSource = { key: string; binding: ProjectLLMBinding; profiles: LLMProfile[] };

export function ProjectLLMSourcePanel({ projectId, ownerId, sessionEpoch, compact = false, onAddProfile, newlyCreatedProfile }: {
  projectId: string;
  ownerId: string;
  sessionEpoch: number;
  compact?: boolean;
  onAddProfile?: () => void;
  newlyCreatedProfile?: LLMProfile | null;
}) {
  const { t } = useT();
  const scopeKey = useMemo(() => `${ownerId}\u0000${projectId}\u0000${sessionEpoch}`, [ownerId, projectId, sessionEpoch]);
  const [loaded, setLoaded] = useState<LoadedSource | null>(null);
  const [draftSource, setDraftSource] = useState<'shared' | 'personal'>('shared');
  const [draftProfileId, setDraftProfileId] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [dismissedFor, setDismissedFor] = useState<string | null>(null);
  const current = loaded?.key === scopeKey ? loaded : null;
  const selectedProfileExists = current?.profiles.some((profile) => profile.id === draftProfileId) ?? false;
  const dirty = Boolean(current && (draftSource !== current.binding.source || (draftSource === 'personal' && draftProfileId !== (current.binding.profile_id ?? ''))));
  const showSharedNotice = !compact && Boolean(current?.binding.source === 'shared' && dismissedFor !== null && dismissedFor !== ownerId);

  const refresh = useCallback(async () => {
    try {
      const [binding, profiles] = await Promise.all([getProjectLLMBinding(projectId), listLLMProfiles()]);
      setLoaded({ key: scopeKey, binding, profiles });
      setDraftSource(binding.source);
      setDraftProfileId(binding.profile_id ?? '');
      setError('');
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('LLMSource.loadError'));
    } finally {
      setLoading(false);
    }
  }, [projectId, scopeKey, t]);

  useEffect(() => {
    void Promise.resolve().then(refresh);
  }, [refresh]);

  useEffect(() => {
    if (!ownerId) return;
    const key = `lwc:byok-shared-notice:v1:${ownerId}`;
    queueMicrotask(() => setDismissedFor(window.localStorage.getItem(key) === '1' ? ownerId : ''));
  }, [ownerId]);

  useEffect(() => {
    if (!newlyCreatedProfile || !current || current.profiles.some((profile) => profile.id === newlyCreatedProfile.id)) return;
    queueMicrotask(() => {
      setLoaded({ ...current, profiles: [...current.profiles, newlyCreatedProfile] });
      setDraftSource('personal');
      setDraftProfileId(newlyCreatedProfile.id);
      setError('');
      setNotice('');
    });
  // A newly created profile is a return payload from the account settings pane.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [newlyCreatedProfile?.id, current?.key]);

  async function save() {
    if (!current || !dirty || saving || (draftSource === 'personal' && !selectedProfileExists)) return;
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
    } catch (saveError: unknown) {
      setError(saveError instanceof Error ? saveError.message : t('LLMSource.saveError'));
    } finally {
      setSaving(false);
    }
  }

  function dismissNotice() {
    if (!ownerId) return;
    window.localStorage.setItem(`lwc:byok-shared-notice:v1:${ownerId}`, '1');
    setDismissedFor(ownerId);
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
      {showSharedNotice ? (
        <aside className="relative mt-3 rounded-lg border border-emerald-200/20 bg-emerald-200/[0.05] p-3 pr-12" aria-labelledby={`llm-shared-notice-${projectId}`}>
          <h4 id={`llm-shared-notice-${projectId}`} className="text-sm font-semibold text-emerald-50">{t('LLMSource.sharedNoticeTitle')}</h4>
          <p className="mt-1 text-sm leading-6 text-zinc-300">{t('LLMSource.sharedNoticeBody')}</p>
          <p className="mt-1 text-xs leading-5 text-zinc-500 lg:hidden">{t('LLMSource.mobileHint')}</p>
          <button type="button" onClick={dismissNotice} aria-label={t('LLMSource.dismiss')} className="absolute right-1 top-1 min-h-11 min-w-11 rounded-md text-lg text-zinc-400 hover:bg-white/10 hover:text-white">×</button>
        </aside>
      ) : null}
      {current ? (
        <>
          <label className="mt-3 block text-sm text-zinc-300">
            {t('LLMSource.title')}
            <select value={draftSource} onChange={(event) => setDraftSource(event.target.value as 'shared' | 'personal')} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
              <option value="shared">{t('LLMSource.shared')}</option><option value="personal">{t('LLMSource.personal')}</option>
            </select>
          </label>
          {draftSource === 'personal' ? (
            <div className="mt-3">
              {current.profiles.length ? (
                <label className="block text-sm text-zinc-300">{t('LLMSource.profile')}
                  <select value={draftProfileId} onChange={(event) => setDraftProfileId(event.target.value)} className="mt-1 min-h-11 w-full rounded-md border border-white/10 bg-zinc-900 px-3 text-white">
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
            <button type="button" disabled={!dirty || saving || (draftSource === 'personal' && !selectedProfileExists)} onClick={() => void save()} className="min-h-11 rounded-md bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">{saving ? t('LLMSource.saving') : t('LLMSource.save')}</button>
          </div>
        </>
      ) : null}
    </section>
  );
}
